package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/config"
	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/internal/model"
)

// What every write shares: where it may go (§4.7), what a dry run shows,
// how names become ids (§6.3), and how an ambiguous create is settled by
// reading (§4.5).

// target is a write's project, read fresh: its visibility goes into the
// result, and its path decides the allow-list.
type target struct {
	p       gapi.Project
	project *gitlab.Project
	ref     model.WriteTarget
}

// writeTarget resolves and reads the project a write goes to and refuses
// one outside GITLAB_MCP_WRITE_NAMESPACES. It reads the project every
// time: the path the allow-list is held against and the visibility the
// result names must be GitLab's current ones, not a cached path.
func (s *Service) writeTarget(ctx context.Context, raw string) (target, error) {
	c, err := s.api()
	if err != nil {
		return target{}, err
	}
	p, err := s.parseProject(raw)
	if err != nil {
		return target{}, err
	}
	// One read resolves a path and reports the project, so the id every
	// later request uses comes from this answer.
	proj, err := c.GetProject(ctx, p)
	if err != nil {
		return target{}, err
	}
	p = gapi.ProjectByID(proj.ID)
	if !s.writeAllowed(proj.PathWithNamespace) {
		return target{}, gapi.Errf(gapi.ClassBlocked, "writes are confined by %s, and this project is outside every namespace it names; "+
			"nothing was sent. To write here, add its group to that setting and restart the server", config.EnvWriteNamespaces)
	}
	return target{p: p, project: proj, ref: model.WriteTarget{
		Project: projectRef(ctx, proj.ID, proj.PathWithNamespace), Visibility: proj.Visibility}}, nil
}

// writeAllowed holds a project's full path to the allow-list: the path
// or one of its parent groups must be listed. Unset allows every path.
func (s *Service) writeAllowed(path string) bool {
	if len(s.cfg.WriteNamespaces) == 0 {
		return true
	}
	lower := strings.ToLower(path)
	for _, ns := range s.cfg.WriteNamespaces {
		ns = strings.ToLower(strings.Trim(ns, "/"))
		if lower == ns || strings.HasPrefix(lower, ns+"/") {
			return true
		}
	}
	return false
}

// preview is the request a dry run stopped before.
func preview(method, operation string, fields []string) *model.Preview {
	return &model.Preview{Method: method, Operation: operation, Fields: nonNil(fields)}
}

// fieldsOf names the top-level fields a request body puts on the wire,
// in order. It reads the JSON the client would send, so a preview says
// exactly what the omitempty tags leave in.
func fieldsOf(body any) []string {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	out := []string{}
	if _, err := dec.Token(); err != nil { // {
		return out
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return out
		}
		out = append(out, key.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return out
		}
	}
	return out
}

// field is one named condition, for names.
type field struct {
	name string
	on   bool
}

// names lists the names whose condition holds, never nil.
func names(fs ...field) []string {
	out := []string{}
	for _, f := range fs {
		if f.on {
			out = append(out, f.name)
		}
	}
	return out
}

// ------------------------------------------------------------ names

// me is the signed-in account, read afresh: a settling read matches what
// this account made, and a login as someone else while the server runs
// changes who that is.
func (s *Service) me(ctx context.Context) (*gitlab.User, error) {
	return s.client.GetCurrentUser(ctx)
}

// parallel runs independent reads at once and returns the first error
// in argument order, so which one is reported does not depend on timing.
func parallel(fns ...func() error) error {
	errs := make([]error, len(fns))
	var wg sync.WaitGroup
	for i, fn := range fns {
		wg.Go(func() { errs[i] = fn() })
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// userIDs turns usernames into ids, exactly, looking them up at once. An
// unknown one is invalid naming it (§6.3).
func (s *Service) userIDs(ctx context.Context, names []string) ([]int64, error) {
	out := make([]int64, len(names))
	lookups := make([]func() error, len(names))
	for i, name := range names {
		name = strings.TrimPrefix(strings.TrimSpace(name), "@")
		lookups[i] = func() error {
			rows, _, err := s.client.ListUsers(ctx, gapi.UserQuery{Username: name}, gapi.ListOptions{PerPage: 1})
			if err != nil {
				return err
			}
			j := slices.IndexFunc(rows, func(u gitlab.UserBasic) bool { return strings.EqualFold(u.Username, name) })
			if j < 0 {
				return gapi.Errf(gapi.ClassInvalid, "there is no user with the username %q; find_users looks one up", name)
			}
			out[i] = rows[j].ID
			return nil
		}
	}
	return out, parallel(lookups...)
}

// checkLabels refuses a label name the project and its groups do not
// have, because GitLab would create it silently (§6.3). Names match
// exactly, scoped labels included.
func (s *Service) checkLabels(ctx context.Context, p gapi.Project, names []string) error {
	if len(names) == 0 {
		return nil
	}
	checks := make([]func() error, len(names))
	for i, name := range names {
		checks[i] = func() error {
			// A search narrows to names containing this one; the match is
			// exact here.
			labels, page, err := s.client.ListLabels(ctx, p, gapi.LabelQuery{Search: name, IncludeAncestorGroups: true},
				gapi.ListOptions{PerPage: gapi.MaxPerPage})
			switch {
			case err != nil:
				return err
			case slices.ContainsFunc(labels, func(l gitlab.Label) bool { return l.Name == name }):
				return nil
			case !page.Complete():
				return gapi.Errf(gapi.ClassInvalid, "the label %q was not among the first %d labels whose names contain it; check the name with list_labels",
					name, len(labels))
			}
			return gapi.Errf(gapi.ClassInvalid, "the project has no label named exactly %q, and GitLab would create one rather than refuse; "+
				"list_labels shows the names", name)
		}
	}
	return parallel(checks...)
}

// milestoneID finds a milestone by exact title in the project and its
// groups. Two matches are ambiguous with both ids (§6.3).
func (s *Service) milestoneID(ctx context.Context, p gapi.Project, title string) (int64, error) {
	if title == "" {
		return 0, nil
	}
	rows, _, err := s.client.ListMilestones(ctx, gapi.MilestoneQuery{Project: p, Title: title, IncludeAncestors: true},
		gapi.ListOptions{PerPage: gapi.MaxPerPage})
	if err != nil {
		return 0, err
	}
	var ids []string
	var id int64
	for _, m := range rows {
		if m.Title == title {
			id = m.ID
			ids = append(ids, fmt.Sprint(m.ID))
		}
	}
	switch len(ids) {
	case 0:
		return 0, gapi.Errf(gapi.ClassInvalid, "there is no milestone titled exactly %q in the project or its groups; list_milestones shows them", title)
	case 1:
		return id, nil
	}
	return 0, gapi.Errf(gapi.ClassAmbiguous, "%d milestones are titled %q (ids %s); rename one, or set it in GitLab", len(ids), title, strings.Join(ids, ", "))
}

// ------------------------------------------------------------ settling

// settleSkew is how far before the call's start a settling read looks,
// for a clock that differs from GitLab's.
const settleSkew = 2 * time.Minute

// settle turns a create's ambiguous failure into its verdict (§4.5).
// find reads what the create would have made: a non-empty description
// when found, "" when not, and an error when the read itself failed.
// Every other error comes back unchanged.
func settle(err error, what string, find func() (string, error)) error {
	var e *gapi.Error
	if !errors.As(err, &e) || e.Class != gapi.ClassAmbiguousOutcome {
		return err
	}
	found, readErr := find()
	switch {
	case readErr != nil:
		return gapi.Wrap(gapi.ClassAmbiguousOutcome, err, "GitLab did not confirm whether the %s was created, and reading to find out failed too, "+
			"so it is unknown: read before doing anything, and do not repeat the call", what)
	case found != "":
		return gapi.Wrap(gapi.ClassAmbiguousOutcome, err, "GitLab did not confirm the %s, but a read shows it was created: %s. "+
			"Do not repeat the call", what, found)
	}
	return gapi.Wrap(gapi.ClassAmbiguousOutcome, err, "GitLab did not confirm the %s, and a read shows it was not created. "+
		"Nothing was repeated; calling again is safe", what)
}

// sameText compares a body sent with one read back. GitLab trims the
// white space around a body.
func sameText(a, b string) bool {
	return strings.TrimSpace(strings.ReplaceAll(a, "\r\n", "\n")) == strings.TrimSpace(strings.ReplaceAll(b, "\r\n", "\n"))
}

// ------------------------------------------------------------ changes

// removedFrom counts the lines of before that after no longer has, and
// their characters, for a text replaced wholesale (§4.6).
func removedFrom(before, after string) *model.Removed {
	left := map[string]int{}
	for l := range strings.SplitSeq(after, "\n") {
		left[l]++
	}
	r := &model.Removed{}
	if before == "" {
		return r
	}
	for l := range strings.SplitSeq(before, "\n") {
		if left[l] > 0 {
			left[l]--
			continue
		}
		r.Lines++
		r.Chars += len([]rune(l))
	}
	return r
}

// usernames lists accounts by username.
func usernames(us []gitlab.UserBasic) []string {
	out := make([]string, 0, len(us))
	for _, u := range us {
		out = append(out, u.Username)
	}
	return out
}

// withSet is the ids of current plus add, less the usernames in remove:
// assignees and reviewers change by add and remove, never by a list the
// caller typed (§4.6).
func withSet(current []gitlab.UserBasic, add []int64, remove []string) []int64 {
	removed := func(u gitlab.UserBasic) bool {
		return slices.ContainsFunc(remove, func(n string) bool {
			return strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(n), "@"), u.Username)
		})
	}
	out := make([]int64, 0, len(current)+len(add))
	var removedIDs []int64
	for _, u := range current {
		if removed(u) {
			removedIDs = append(removedIDs, u.ID)
			continue
		}
		out = append(out, u.ID)
	}
	for _, id := range add {
		if !slices.Contains(out, id) && !slices.Contains(removedIDs, id) {
			out = append(out, id)
		}
	}
	return out
}

// sameSet reports two string lists holding the same members.
func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

func milestoneTitle(m *gitlab.Milestone) string {
	if m == nil {
		return ""
	}
	return m.Title
}

// parseWitness reads the updated_at a caller passes as its witness.
func parseWitness(raw string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, gapi.Errf(gapi.ClassInvalid, "updated_at must be the RFC 3339 time a read returned, such as 2026-01-05T09:00:00.000Z")
	}
	return t, nil
}

// checkWitness refuses an update whose witness is not the item's current
// updated_at (§4.6). GitLab has no guard for this, so the window between
// this read and the write stays open; the check narrows it.
func checkWitness(witness, current time.Time, what string) error {
	if witness.Equal(current) {
		return nil
	}
	return gapi.Errf(gapi.ClassStale, "the %s changed since it was read: updated_at is now %s, not %s. Read it again, "+
		"check the change still makes sense, and pass the new updated_at", what, current.UTC().Format(time.RFC3339Nano),
		witness.UTC().Format(time.RFC3339Nano))
}
