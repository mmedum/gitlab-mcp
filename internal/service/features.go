package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mmedum/gitlab-mcp/v2/internal/gapi"
	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/v2/internal/model"
	"github.com/mmedum/gitlab-mcp/v2/internal/redact"
	"github.com/mmedum/gitlab-mcp/v2/internal/render"
)

// The phase 6 tools (§17.13): moving and linking issues, label and
// milestone writes, rebase, cherry-pick, revert, blame, job artifacts and
// tag writes.

// ----------------------------------------------------------- issues

// visibilityRank orders who can see a project: private below internal
// below public; -1 for a value it does not know.
func visibilityRank(v string) int {
	return slices.Index([]string{"private", "internal", "public"}, v)
}

// issueAudience ranks who can see a project's issues: its visibility, or
// members only when its issues are set so.
func issueAudience(p *gitlab.Project) int {
	if p.IssuesAccessLevel == "private" {
		return 0
	}
	return visibilityRank(p.Visibility)
}

// IssueMoveRequest is move_issue's request.
type IssueMoveRequest struct {
	Project   string
	IID       int64
	ToProject string
	UpdatedAt string
}

// MoveIssue moves an issue to another project. Both projects are held to
// the write allow-list, and a move that would show the issue to more
// people than can see it now is refused: an injected "move this" is how
// a private issue would reach a public project (§17.13).
func (s *Service) MoveIssue(ctx context.Context, in IssueMoveRequest) (model.IssueMove, error) {
	witness, err := parseWitness(in.UpdatedAt)
	if err != nil {
		return model.IssueMove{}, err
	}
	from, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.IssueMove{}, err
	}
	to, err := s.writeTarget(ctx, in.ToProject)
	if err != nil {
		return model.IssueMove{}, err
	}
	if to.project.ID == from.project.ID {
		return model.IssueMove{}, gapi.Errf(gapi.ClassInvalid, "the issue is already in %s", from.project.PathWithNamespace)
	}
	src, dst := issueAudience(from.project), issueAudience(to.project)
	if src < 0 || dst < 0 || dst > src {
		return model.IssueMove{}, gapi.Errf(gapi.ClassBlocked, "the issues of %s can be seen by more people than those of %s, or who "+
			"can see them is unknown: moving the issue would show it to people who cannot see it now, so nothing was sent",
			to.project.PathWithNamespace, from.project.PathWithNamespace)
	}
	issue, err := s.client.GetIssue(ctx, from.p, in.IID)
	if err != nil {
		return model.IssueMove{}, err
	}
	if err := checkWitness(witness, issue.UpdatedAt, "issue"); err != nil {
		return model.IssueMove{}, err
	}
	out := model.IssueMove{Outcome: "moved", Write: model.Write{Target: from.ref}, FromIID: in.IID, To: to.ref.Project,
		ToVisibility: to.project.Visibility}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun, out.WouldSend = "dry_run", true, preview("POST", "move the issue", []string{"to_project_id"})
		return out, nil
	}
	moved, err := s.client.MoveIssue(ctx, from.p, in.IID, to.project.ID)
	if err != nil {
		return model.IssueMove{}, settle(err, "move", func() (string, error) {
			after, err := s.client.GetIssue(ctx, from.p, in.IID)
			if err != nil || after.MovedToID == nil {
				return "", err
			}
			return fmt.Sprintf("the issue was moved, to the issue with id %d", *after.MovedToID), nil
		})
	}
	out.IID, out.WebURL = moved.IID, moved.WebURL
	return out, nil
}

// IssueLinkTypes are the link types GitLab takes; blocks and
// is_blocked_by need Premium.
var IssueLinkTypes = []string{"relates_to", "blocks", "is_blocked_by"}

// IssueLinkRequest is link_issues' and unlink_issues' request.
type IssueLinkRequest struct {
	Project       string
	IID           int64
	TargetProject string
	TargetIID     int64
	LinkType      string
}

// linkTargets resolves both issues' projects as write targets: a link
// shows on both issues.
func (s *Service) linkTargets(ctx context.Context, in IssueLinkRequest) (target, target, error) {
	if in.IID < 1 || in.TargetIID < 1 {
		return target{}, target{}, gapi.Errf(gapi.ClassInvalid, "iid and target_iid are issue numbers, 1 or more")
	}
	from, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return target{}, target{}, err
	}
	to := from
	if in.TargetProject != "" {
		if to, err = s.writeTarget(ctx, in.TargetProject); err != nil {
			return target{}, target{}, err
		}
	}
	if to.project.ID == from.project.ID && in.TargetIID == in.IID {
		return target{}, target{}, gapi.Errf(gapi.ClassInvalid, "an issue is not linked to itself")
	}
	return from, to, nil
}

// findLink finds the link from an issue to the target issue.
func findLink(links []gitlab.RelatedIssue, projectID, iid int64) (gitlab.RelatedIssue, bool) {
	i := slices.IndexFunc(links, func(l gitlab.RelatedIssue) bool { return l.ProjectID == projectID && l.IID == iid })
	if i < 0 {
		return gitlab.RelatedIssue{}, false
	}
	return links[i], true
}

// LinkIssues links two issues. A link that exists is reported unchanged.
func (s *Service) LinkIssues(ctx context.Context, in IssueLinkRequest) (model.IssueLinkWrite, error) {
	if in.LinkType == "" {
		in.LinkType = "relates_to"
	}
	if !slices.Contains(IssueLinkTypes, in.LinkType) {
		return model.IssueLinkWrite{}, gapi.Errf(gapi.ClassInvalid, "link_type is one of %s", strings.Join(IssueLinkTypes, ", "))
	}
	from, to, err := s.linkTargets(ctx, in)
	if err != nil {
		return model.IssueLinkWrite{}, err
	}
	out := model.IssueLinkWrite{Outcome: "linked", Write: model.Write{Target: from.ref}, IID: in.IID, TargetProject: to.ref.Project,
		TargetIID: in.TargetIID, LinkType: in.LinkType}
	links, err := s.client.ListIssueLinks(ctx, from.p, in.IID)
	if err != nil {
		return model.IssueLinkWrite{}, err
	}
	if l, ok := findLink(links, to.project.ID, in.TargetIID); ok {
		out.Outcome, out.LinkID, out.LinkType = "unchanged", l.IssueLinkID, l.LinkType
		out.Notes = []string{"The issues were already linked."}
		return out, nil
	}
	body := gapi.IssueLinkCreate{TargetProjectID: fmt.Sprint(to.project.ID), TargetIssueIID: fmt.Sprint(in.TargetIID), LinkType: in.LinkType}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun, out.WouldSend = "dry_run", true, preview("POST", "link the issues", fieldsOf(body))
		return out, nil
	}
	l, err := s.client.CreateIssueLink(ctx, from.p, in.IID, body)
	if err != nil {
		return model.IssueLinkWrite{}, settle(err, "link", func() (string, error) {
			links, err := s.client.ListIssueLinks(ctx, from.p, in.IID)
			if err != nil {
				return "", err
			}
			if l, ok := findLink(links, to.project.ID, in.TargetIID); ok {
				return fmt.Sprintf("link %d", l.IssueLinkID), nil
			}
			return "", nil
		})
	}
	out.LinkID, out.LinkType = l.ID, l.LinkType
	return out, nil
}

// UnlinkIssues removes the link between two issues. With no link, the
// result is unchanged.
func (s *Service) UnlinkIssues(ctx context.Context, in IssueLinkRequest) (model.IssueLinkWrite, error) {
	from, to, err := s.linkTargets(ctx, in)
	if err != nil {
		return model.IssueLinkWrite{}, err
	}
	out := model.IssueLinkWrite{Outcome: "unlinked", Write: model.Write{Target: from.ref}, IID: in.IID, TargetProject: to.ref.Project,
		TargetIID: in.TargetIID}
	links, err := s.client.ListIssueLinks(ctx, from.p, in.IID)
	if err != nil {
		return model.IssueLinkWrite{}, err
	}
	l, ok := findLink(links, to.project.ID, in.TargetIID)
	if !ok {
		out.Outcome, out.Notes = "unchanged", []string{"The issues were not linked."}
		return out, nil
	}
	out.LinkID, out.LinkType = l.IssueLinkID, l.LinkType
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun, out.WouldSend = "dry_run", true, preview("DELETE", "unlink the issues", nil)
		return out, nil
	}
	if err := s.client.DeleteIssueLink(ctx, from.p, in.IID, l.IssueLinkID); err != nil {
		return model.IssueLinkWrite{}, err
	}
	return out, nil
}

// ----------------------------------------------------------- labels

// labelVersion is the label witness: a hash of what an update can change,
// since GitLab keeps no version of a label (§4.6).
func labelVersion(l gitlab.Label) string {
	priority := ""
	if l.Priority != nil {
		priority = fmt.Sprint(*l.Priority)
	}
	return contentHash(strings.Join([]string{l.Name, l.Color, l.Description, priority, fmt.Sprint(l.Archived)}, "\x00"))
}

// labelRow is a label as list_labels shows it.
func labelRow(l gitlab.Label) model.Label {
	desc, _ := render.Line(l.Description, render.TitleChars)
	return model.Label{ID: l.ID, Name: l.Name, Color: l.Color, ProjectOnly: l.IsProjectLabel, Priority: l.Priority,
		UntrustedDescription: desc, Version: labelVersion(l)}
}

// LabelRequest is create_label's and update_label's request. A nil field
// is left as it is.
type LabelRequest struct {
	Project          string
	LabelID          int64
	Version          string
	Name             string
	Color            string
	Description      *string
	ClearDescription bool
	Priority         *int
	ClearPriority    bool
}

// CreateLabel creates a project label.
func (s *Service) CreateLabel(ctx context.Context, in LabelRequest) (model.LabelWrite, error) {
	switch {
	case strings.TrimSpace(in.Name) == "":
		return model.LabelWrite{}, gapi.Errf(gapi.ClassInvalid, "name is empty")
	case strings.TrimSpace(in.Color) == "":
		return model.LabelWrite{}, gapi.Errf(gapi.ClassInvalid, "color is required: #RRGGBB, or a CSS color name")
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.LabelWrite{}, err
	}
	existing, err := s.labelNamed(ctx, t, in.Name)
	if err != nil {
		return model.LabelWrite{}, err
	}
	if existing != nil {
		return model.LabelWrite{}, gapi.Errf(gapi.ClassConflict, "a label named %q exists, id %d", in.Name, existing.ID)
	}
	body := gapi.LabelCreate{Name: in.Name, Color: in.Color, Description: in.Description, Priority: in.Priority}
	out := model.LabelWrite{Outcome: "created", Write: model.Write{Target: t.ref}, Changed: []string{}}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun, out.WouldSend = "dry_run", true, preview("POST", "create a label", fieldsOf(body))
		out.Label = model.Label{Name: in.Name, Color: in.Color, ProjectOnly: true, Priority: in.Priority}
		return out, nil
	}
	l, err := s.client.CreateLabel(ctx, t.p, body)
	if err != nil {
		return model.LabelWrite{}, settle(err, "label", func() (string, error) {
			got, err := s.labelNamed(ctx, t, in.Name)
			if err != nil || got == nil {
				return "", err
			}
			return fmt.Sprintf("label %d", got.ID), nil
		})
	}
	out.Label = labelRow(*l)
	return out, nil
}

// labelNamed finds the label a project's issues know by name, its own
// or a group's, or nil.
func (s *Service) labelNamed(ctx context.Context, t target, name string) (*gitlab.Label, error) {
	rows, _, err := s.client.ListLabels(ctx, t.p, gapi.LabelQuery{Search: name, IncludeAncestorGroups: true},
		gapi.ListOptions{PerPage: gapi.MaxPerPage})
	if err != nil {
		return nil, err
	}
	if i := slices.IndexFunc(rows, func(l gitlab.Label) bool { return l.Name == name }); i >= 0 {
		return &rows[i], nil
	}
	return nil, nil
}

// projectLabel reads a label and holds it to its version. A group label
// is refused: it is the group's, shared by every project under it.
func (s *Service) projectLabel(ctx context.Context, t target, id int64, version string) (*gitlab.Label, error) {
	if strings.TrimSpace(version) == "" {
		return nil, gapi.Errf(gapi.ClassInvalid, "version is required: the label's version as list_labels returned it")
	}
	l, err := s.client.GetLabel(ctx, t.p, id)
	if err != nil {
		return nil, err
	}
	if !l.IsProjectLabel {
		return nil, gapi.Errf(gapi.ClassBlocked, "label %q is a group's, shared by every project under it; this server changes "+
			"only a project's own labels, and nothing was sent", l.Name)
	}
	if current := labelVersion(*l); !strings.EqualFold(strings.TrimSpace(version), current) {
		return nil, gapi.Errf(gapi.ClassStale, "the label changed since it was read: its version is now %s. Read it again with "+
			"list_labels, check the change still makes sense, and pass the new version", current)
	}
	return l, nil
}

// UpdateLabel changes a project label. Only the fields given are sent.
func (s *Service) UpdateLabel(ctx context.Context, in LabelRequest) (model.LabelWrite, error) {
	description, err := clearing("description", in.Description, in.ClearDescription)
	if err != nil {
		return model.LabelWrite{}, err
	}
	if err := notBoth("priority", in.Priority != nil, in.ClearPriority); err != nil {
		return model.LabelWrite{}, err
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.LabelWrite{}, err
	}
	before, err := s.projectLabel(ctx, t, in.LabelID, in.Version)
	if err != nil {
		return model.LabelWrite{}, err
	}
	body := gapi.LabelUpdate{Priority: in.Priority, Description: description, ClearPriority: in.ClearPriority && before.Priority != nil}
	if in.Name != "" && in.Name != before.Name {
		body.NewName = in.Name
	}
	if in.Color != "" && !strings.EqualFold(in.Color, before.Color) {
		body.Color = in.Color
	}
	if body.Description != nil && *body.Description == before.Description {
		body.Description = nil
	}
	if body.Priority != nil && samePriority(body.Priority, before.Priority) {
		body.Priority = nil
	}
	out := model.LabelWrite{Outcome: "updated", Write: model.Write{Target: t.ref}, Label: labelRow(*before), Changed: []string{}}
	fields := fieldsOf(body)
	if len(fields) == 0 {
		out.Outcome, out.Notes = "unchanged", []string{"Every field given already had that value; nothing was sent."}
		return out, nil
	}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun, out.WouldSend = "dry_run", true, preview("PUT", "update the label", fields)
		return out, nil
	}
	after, err := s.client.UpdateLabel(ctx, t.p, in.LabelID, body)
	if err != nil {
		return model.LabelWrite{}, err
	}
	out.Label = labelRow(*after)
	out.Changed = names(field{"name", after.Name != before.Name}, field{"color", !strings.EqualFold(after.Color, before.Color)},
		field{"description", after.Description != before.Description}, field{"priority", !samePriority(after.Priority, before.Priority)})
	return out, nil
}

// samePriority compares two label priorities, none being a value too.
func samePriority(a, b *int) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// DeleteLabel deletes a project label, which takes it off every issue
// and merge request.
func (s *Service) DeleteLabel(ctx context.Context, raw string, id int64, version string) (model.LabelWrite, error) {
	t, err := s.writeTarget(ctx, raw)
	if err != nil {
		return model.LabelWrite{}, err
	}
	l, err := s.projectLabel(ctx, t, id, version)
	if err != nil {
		return model.LabelWrite{}, err
	}
	out := model.LabelWrite{Outcome: "deleted", Write: model.Write{Target: t.ref}, Label: labelRow(*l), Changed: []string{}}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun, out.WouldSend = "dry_run", true, preview("DELETE", "delete the label", nil)
		return out, nil
	}
	if err := ask(ctx, render.AskDeleteLabel(t.ref.Project.Path, l.Name, l.OpenIssuesCount)); err != nil {
		return model.LabelWrite{}, err
	}
	err = s.client.DeleteLabel(ctx, t.p, id)
	_, readErr := s.client.GetLabel(ctx, t.p, id)
	if out.Notes, err = deleted(err, readErr, "label"); err != nil {
		return model.LabelWrite{}, err
	}
	return out, nil
}

// ----------------------------------------------------------- milestones

// MilestoneStates are the state changes update_milestone takes.
var MilestoneStates = []string{"close", "activate"}

// MilestoneRequest is create_milestone's and update_milestone's request.
// A nil field is left as it is. The clear_ inputs are update's: each
// clears its field.
type MilestoneRequest struct {
	Project          string
	ID               int64
	UpdatedAt        string
	Title            string
	Description      *string
	ClearDescription bool
	DueDate          *string
	ClearDueDate     bool
	StartDate        *string
	ClearStartDate   bool
	StateEvent       string
}

func (in MilestoneRequest) check() error {
	for name, d := range map[string]*string{"due_date": in.DueDate, "start_date": in.StartDate} {
		if d != nil {
			if err := checkDate(name, *d); err != nil {
				return err
			}
		}
	}
	if in.StateEvent != "" && !slices.Contains(MilestoneStates, in.StateEvent) {
		return gapi.Errf(gapi.ClassInvalid, "state is close or activate")
	}
	return nil
}

// milestoneRow is a milestone as list_milestones shows it.
func milestoneRow(m gitlab.ProjectMilestone) model.MilestoneRow {
	title, _ := render.Line(m.Title, render.TitleChars)
	row := model.MilestoneRow{ID: m.ID, IID: m.IID, State: m.State, Expired: m.Expired != nil && *m.Expired, UpdatedAt: m.UpdatedAt,
		WebURL: m.WebURL, UntrustedTitle: title}
	if m.DueDate != "" {
		d := m.DueDate
		row.DueDate = &d
	}
	if m.StartDate != "" {
		d := m.StartDate
		row.StartDate = &d
	}
	return row
}

// milestoneWrite is a milestone as a write reports it.
func milestoneWrite(t target, outcome string, m gitlab.ProjectMilestone) model.MilestoneWrite {
	desc, _ := render.Markdown(m.Description, "")
	return model.MilestoneWrite{Outcome: outcome, Write: model.Write{Target: t.ref}, Milestone: milestoneRow(m), UntrustedDescription: desc,
		Changed: []string{}}
}

// CreateMilestone creates a project milestone.
func (s *Service) CreateMilestone(ctx context.Context, in MilestoneRequest) (model.MilestoneWrite, error) {
	if strings.TrimSpace(in.Title) == "" {
		return model.MilestoneWrite{}, gapi.Errf(gapi.ClassInvalid, "title is empty")
	}
	if err := in.check(); err != nil {
		return model.MilestoneWrite{}, err
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.MilestoneWrite{}, err
	}
	body := gapi.MilestoneCreate{Title: in.Title, Description: in.Description, DueDate: in.DueDate, StartDate: in.StartDate}
	if gapi.IsDryRun(ctx) {
		out := milestoneWrite(t, "dry_run", gitlab.ProjectMilestone{Title: in.Title, State: "active"})
		out.DryRun, out.WouldSend = true, preview("POST", "create a milestone", fieldsOf(body))
		return out, nil
	}
	start := time.Now()
	m, err := s.client.CreateMilestone(ctx, t.p, body)
	if err != nil {
		return model.MilestoneWrite{}, settle(err, "milestone", func() (string, error) {
			rows, _, err := s.client.ListMilestones(ctx, gapi.MilestoneQuery{Project: t.p, Title: in.Title}, gapi.ListOptions{PerPage: gapi.MaxPerPage})
			if err != nil {
				return "", err
			}
			// A milestone of that title made before the call is not
			// this call's.
			since := start.Add(-settleSkew)
			if i := slices.IndexFunc(rows, func(m gitlab.ProjectMilestone) bool { return !m.CreatedAt.Before(since) }); i >= 0 {
				return fmt.Sprintf("milestone %d", rows[i].ID), nil
			}
			return "", nil
		})
	}
	return milestoneWrite(t, "created", *m), nil
}

// UpdateMilestone changes a project milestone the caller read. Only the
// fields given are sent.
func (s *Service) UpdateMilestone(ctx context.Context, in MilestoneRequest) (model.MilestoneWrite, error) {
	witness, err := parseWitness(in.UpdatedAt)
	if err != nil {
		return model.MilestoneWrite{}, err
	}
	if in.Description, err = clearing("description", in.Description, in.ClearDescription); err != nil {
		return model.MilestoneWrite{}, err
	}
	if in.DueDate, err = clearing("due_date", in.DueDate, in.ClearDueDate); err != nil {
		return model.MilestoneWrite{}, err
	}
	if in.StartDate, err = clearing("start_date", in.StartDate, in.ClearStartDate); err != nil {
		return model.MilestoneWrite{}, err
	}
	if err := in.check(); err != nil {
		return model.MilestoneWrite{}, err
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.MilestoneWrite{}, err
	}
	before, err := s.client.GetMilestone(ctx, t.p, in.ID)
	if err != nil {
		return model.MilestoneWrite{}, err
	}
	if err := checkWitness(witness, before.UpdatedAt, "milestone"); err != nil {
		return model.MilestoneWrite{}, err
	}
	body := gapi.MilestoneUpdate{Description: in.Description, DueDate: in.DueDate, StartDate: in.StartDate}
	if in.Title != "" && in.Title != before.Title {
		body.Title = in.Title
	}
	if (in.StateEvent == "close" && before.State != "closed") || (in.StateEvent == "activate" && before.State != "active") {
		body.StateEvent = in.StateEvent
	}
	for _, f := range []struct {
		v   **string
		was string
	}{{&body.Description, before.Description}, {&body.DueDate, before.DueDate}, {&body.StartDate, before.StartDate}} {
		if *f.v != nil && **f.v == f.was {
			*f.v = nil
		}
	}
	fields := fieldsOf(body)
	if len(fields) == 0 {
		out := milestoneWrite(t, "unchanged", *before)
		out.Notes = []string{"Every field given already had that value; nothing was sent."}
		return out, nil
	}
	if gapi.IsDryRun(ctx) {
		out := milestoneWrite(t, "dry_run", *before)
		out.DryRun, out.WouldSend = true, preview("PUT", "update the milestone", fields)
		return out, nil
	}
	after, err := s.client.UpdateMilestone(ctx, t.p, in.ID, body)
	if err != nil {
		return model.MilestoneWrite{}, err
	}
	out := milestoneWrite(t, "updated", *after)
	out.Changed = names(field{"title", after.Title != before.Title}, field{"description", after.Description != before.Description},
		field{"due_date", after.DueDate != before.DueDate}, field{"start_date", after.StartDate != before.StartDate},
		field{"state", after.State != before.State})
	return out, nil
}

// DeleteMilestone deletes a project milestone the caller read, which
// takes it off every issue and merge request.
func (s *Service) DeleteMilestone(ctx context.Context, raw string, id int64, updatedAt string) (model.MilestoneWrite, error) {
	witness, err := parseWitness(updatedAt)
	if err != nil {
		return model.MilestoneWrite{}, err
	}
	t, err := s.writeTarget(ctx, raw)
	if err != nil {
		return model.MilestoneWrite{}, err
	}
	m, err := s.client.GetMilestone(ctx, t.p, id)
	if err != nil {
		return model.MilestoneWrite{}, err
	}
	if err := checkWitness(witness, m.UpdatedAt, "milestone"); err != nil {
		return model.MilestoneWrite{}, err
	}
	out := milestoneWrite(t, "deleted", *m)
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun, out.WouldSend = "dry_run", true, preview("DELETE", "delete the milestone", nil)
		return out, nil
	}
	if err := ask(ctx, render.AskDeleteMilestone(t.ref.Project.Path, m.Title)); err != nil {
		return model.MilestoneWrite{}, err
	}
	err = s.client.DeleteMilestone(ctx, t.p, id)
	_, readErr := s.client.GetMilestone(ctx, t.p, id)
	if out.Notes, err = deleted(err, readErr, "milestone"); err != nil {
		return model.MilestoneWrite{}, err
	}
	return out, nil
}

// ----------------------------------------------------------- rebase

// RebaseMergeRequest rebases a merge request's source branch onto its
// target, from the head the caller reviewed. GitLab rebases in the
// background; the result says whether it is still running.
func (s *Service) RebaseMergeRequest(ctx context.Context, raw string, iid int64, sha string, skipCI bool) (model.RebaseWrite, error) {
	if len(strings.TrimSpace(sha)) < 7 {
		return model.RebaseWrite{}, gapi.Errf(gapi.ClassInvalid, "sha is required: the merge request's head as get_merge_request "+
			"returned it, at least its first seven characters")
	}
	t, err := s.writeTarget(ctx, raw)
	if err != nil {
		return model.RebaseWrite{}, err
	}
	mr, err := s.client.GetMergeRequest(ctx, t.p, iid)
	if err != nil {
		return model.RebaseWrite{}, err
	}
	switch {
	case mr.State != "opened":
		return model.RebaseWrite{}, gapi.Errf(gapi.ClassConflict, "the merge request is %s; only an open one is rebased", mr.State)
	case !sameHead(mr.SHA, sha):
		return model.RebaseWrite{}, gapi.Errf(gapi.ClassStale, "the merge request's head moved since it was read: it is now %s, not %s. "+
			"Look at what was pushed before rebasing", mr.SHA, sha)
	}
	// A rebase force-pushes the source branch, in the source project,
	// which for a fork is another project: it is held to the allow-list,
	// and one more people can see is refused, since the target branch's
	// history is what lands there. A protected source branch takes code
	// only through a merge request (§4.4).
	source := t
	if mr.SourceProjectID != t.project.ID {
		if source, err = s.writeTarget(ctx, fmt.Sprint(mr.SourceProjectID)); err != nil {
			return model.RebaseWrite{}, err
		}
		if a, b := visibilityRank(source.project.Visibility), visibilityRank(t.project.Visibility); a < 0 || b < 0 || a > b {
			return model.RebaseWrite{}, gapi.Errf(gapi.ClassBlocked, "the source branch is in %s, which more people can see than %s: a "+
				"rebase would push the target branch's history there, so nothing was sent", source.project.PathWithNamespace,
				t.project.PathWithNamespace)
		}
	}
	b, err := s.client.GetBranch(ctx, source.p, mr.SourceBranch)
	if err != nil {
		return model.RebaseWrite{}, err
	}
	if b.Protected {
		return model.RebaseWrite{}, gapi.Errf(gapi.ClassBlocked, "the source branch is protected, and a rebase rewrites it; nothing was sent")
	}
	out := model.RebaseWrite{Outcome: "started", Write: model.Write{Target: t.ref}, IID: iid, SHA: mr.SHA, SkipCI: skipCI}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun, out.WouldSend = "dry_run", true, preview("PUT", "rebase the source branch", names(field{"skip_ci", skipCI}))
		return out, nil
	}
	state, err := s.client.RebaseMergeRequest(ctx, t.p, iid, skipCI)
	if err != nil {
		return model.RebaseWrite{}, err
	}
	out.RebaseInProgress = state.RebaseInProgress
	if after, err := s.client.GetMergeRequestRebase(ctx, t.p, iid); err == nil {
		out.RebaseInProgress = after.RebaseInProgress != nil && *after.RebaseInProgress
		if after.MergeError != nil {
			out.UntrustedMergeError, _ = render.Line(*after.MergeError, render.NoteBudget)
		}
	}
	return out, nil
}

// ----------------------------------------------------------- cherry-pick

// PickRequest is cherry_pick_commit's and revert_commit's request.
type PickRequest struct {
	Project string
	SHA     string
	Branch  string
	Message string
	Revert  bool
}

// Pick cherry-picks or reverts a commit onto a branch as a new commit.
// The default branch and protected branches are refused as create_commit
// refuses them (§4.4). A dry run asks GitLab's own dry run, which says
// whether the change applies.
func (s *Service) Pick(ctx context.Context, in PickRequest) (model.PickWrite, error) {
	switch {
	case len(strings.TrimSpace(in.SHA)) < 7:
		return model.PickWrite{}, gapi.Errf(gapi.ClassInvalid, "commit is the commit to apply, at least its first seven characters")
	case strings.TrimSpace(in.Branch) == "":
		return model.PickWrite{}, gapi.Errf(gapi.ClassInvalid, "branch is empty")
	case in.Revert && in.Message != "":
		return model.PickWrite{}, gapi.Errf(gapi.ClassInvalid, "a revert's message is GitLab's own; leave message out")
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.PickWrite{}, err
	}
	b, err := s.client.GetBranch(ctx, t.p, in.Branch)
	if err != nil {
		return model.PickWrite{}, err
	}
	if err := s.guardBranch(ctx, t, in.Branch, b); err != nil {
		return model.PickWrite{}, err
	}
	what, verb := "cherry-pick", "cherry-pick the commit"
	if in.Revert {
		what, verb = "revert", "revert the commit"
	}
	out := model.PickWrite{Outcome: "created", Write: model.Write{Target: t.ref}, Action: "cherry_pick", FromSHA: in.SHA, Branch: in.Branch}
	if in.Revert {
		out.Action = "revert"
	}
	apply := func(dryRun bool) (*gitlab.Commit, error) {
		if in.Revert {
			return s.client.Revert(ctx, t.p, in.SHA, in.Branch, dryRun)
		}
		return s.client.CherryPick(ctx, t.p, in.SHA, in.Branch, in.Message, dryRun)
	}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun = "dry_run", true
		out.WouldSend = preview("POST", verb, names(field{"branch", true}, field{"message", in.Message != ""}))
		_, err := apply(true)
		var e *gapi.Error
		switch {
		case err == nil:
			out.Applies = true
		case errors.As(err, &e) && e.Class == gapi.ClassConflict:
			out.Notes = []string{"The " + what + " does not apply to " + in.Branch + ". " + e.Message}
		default:
			return model.PickWrite{}, err
		}
		return out, nil
	}
	c, err := apply(false)
	if err != nil {
		return model.PickWrite{}, settle(err, what, func() (string, error) {
			// The head read before is the parent of a commit GitLab made;
			// a head that moved otherwise cannot tell.
			after, err := s.client.GetBranch(ctx, t.p, in.Branch)
			switch {
			case err != nil:
				return "", err
			case after.Commit.ID == b.Commit.ID:
				return "", nil
			case slices.Equal(after.Commit.ParentIDs, []string{b.Commit.ID}):
				return fmt.Sprintf("commit %s, the branch's head, on the head read before", after.Commit.ID), nil
			}
			return "", errors.New("the branch moved past the head read before, so which commit is this call's cannot be told")
		})
	}
	out.Applies, out.SHA, out.ShortID, out.WebURL = true, c.ID, c.ShortID, c.WebURL
	out.UntrustedTitle, _ = render.Line(c.Title, render.TitleChars)
	return out, nil
}

// ----------------------------------------------------------- blame

// BlameRequest is get_blame's request.
type BlameRequest struct {
	Project   string
	Path      string
	Ref       string
	StartLine int
	EndLine   int
}

// Blame shows who last changed each line of a file, a run of lines per
// commit, under the file budget (§4.8).
func (s *Service) Blame(ctx context.Context, in BlameRequest) (model.Blame, error) {
	in.Path = strings.Trim(in.Path, "/")
	switch {
	case in.Path == "":
		return model.Blame{}, gapi.Errf(gapi.ClassInvalid, "path is empty: pass a file path relative to the repository root")
	case in.StartLine < 0 || in.EndLine < 0 || (in.EndLine > 0 && in.EndLine < max(in.StartLine, 1)):
		return model.Blame{}, gapi.Errf(gapi.ClassInvalid, "start_line and end_line are line numbers, 1 or more, start before end")
	}
	c, err := s.api()
	if err != nil {
		return model.Blame{}, err
	}
	p, err := s.parseProject(in.Project)
	if err != nil {
		return model.Blame{}, err
	}
	proj, err := c.GetProject(ctx, p)
	if err != nil {
		return model.Blame{}, err
	}
	p = gapi.ProjectByID(proj.ID)
	if in.Ref == "" {
		in.Ref = proj.DefaultBranch
	}
	start := max(in.StartLine, 1)
	end := in.EndLine
	if end == 0 && start > 1 {
		end = start + maxBlameLines - 1
	}
	ranges, err := s.client.Blame(ctx, p, in.Path, in.Ref, start, end)
	if err != nil {
		return model.Blame{}, err
	}
	out := model.Blame{Project: projectRef(ctx, proj.ID, proj.PathWithNamespace), Path: in.Path, Ref: in.Ref, Ranges: []model.BlameRange{}}
	line, used := start, 0
	stop := func() {
		next := line
		out.NextLine = &next
	}
	for _, r := range ranges {
		// A run takes the lines that fit the budget; one run alone may
		// be cut, the next call starting at the line it stopped before.
		lines := r.Lines
		for i, l := range lines {
			if used+utf8.RuneCountInString(l)+1 > render.FileBudget && (len(out.Ranges) > 0 || i > 0) {
				lines = lines[:i]
				break
			}
			used += utf8.RuneCountInString(l) + 1
		}
		if len(lines) == 0 {
			stop()
			break
		}
		text, hidden := render.Code(strings.Join(lines, "\n"))
		summary, _ := render.Line(firstLine(r.Commit.Message), render.TitleChars)
		out.Ranges = append(out.Ranges, model.BlameRange{StartLine: line, EndLine: line + len(lines) - 1, CommitSHA: r.Commit.ID,
			Author: r.Commit.AuthorName, AuthoredAt: r.Commit.AuthoredDate, UntrustedSummary: summary, UntrustedLines: text})
		out.HiddenRemoved += hidden
		line += len(lines)
		if len(lines) < len(r.Lines) {
			stop()
			break
		}
	}
	if out.NextLine == nil && in.EndLine == 0 && end > 0 && line > end {
		// The window this server chose was full: more may follow.
		stop()
	}
	return out, nil
}

// maxBlameLines is the window a blame from start_line reads when no
// end_line is given.
const maxBlameLines = 2000

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// ----------------------------------------------------------- artifacts

// ListArtifacts lists a job's artifact files.
func (s *Service) ListArtifacts(ctx context.Context, raw string, job int64, dir string, recursive bool, opts gapi.ListOptions) (model.Artifacts, error) {
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.Artifacts{}, err
	}
	dir = strings.Trim(dir, "/")
	rows, page, err := s.client.ListArtifacts(ctx, p, job, dir, recursive, opts)
	if err != nil {
		return model.Artifacts{}, err
	}
	out := model.Artifacts{Project: ref, JobID: job, Path: dir, Entries: make([]model.ArtifactEntry, 0, len(rows)), Listing: listing(len(rows), page)}
	for _, e := range rows {
		out.Entries = append(out.Entries, model.ArtifactEntry{Path: e.Path, Type: e.Type, Size: e.Size})
	}
	return out, nil
}

// GetArtifact shows one text file of a job's artifacts, masked as a job
// log is (§4.1.4), under the file budget. A binary file is named with its
// size only.
func (s *Service) GetArtifact(ctx context.Context, raw string, job int64, path string, offset int) (model.Artifact, error) {
	path = strings.Trim(path, "/")
	if path == "" {
		return model.Artifact{}, gapi.Errf(gapi.ClassInvalid, "path is empty: list_job_artifacts lists the files")
	}
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.Artifact{}, err
	}
	b, err := s.client.GetArtifact(ctx, p, job, path)
	if err != nil {
		return model.Artifact{}, err
	}
	out := model.Artifact{Project: ref, JobID: job, Path: path, Size: len(b)}
	if !render.IsBinary(b) {
		var masked string
		// Escapes removed first, as a job log's are, or a colored token
		// escapes the masks.
		masked, out.SecretsMasked = redact.MaskSecrets(render.StripANSI(string(b)))
		b = []byte(masked)
	}
	if out.UntrustedContent, out.Binary, out.Budget, err = fileContent(b, offset); err != nil {
		return model.Artifact{}, err
	}
	return out, nil
}

// ----------------------------------------------------------- tags

// CreateTag creates a tag at a ref. A name a protected-tag rule covers is
// refused: a protected tag's pipelines see protected variables and may
// deploy, so it is made by a person (§17.13).
func (s *Service) CreateTag(ctx context.Context, raw, name, ref, message string) (model.TagWrite, error) {
	switch {
	case strings.TrimSpace(name) == "":
		return model.TagWrite{}, gapi.Errf(gapi.ClassInvalid, "tag_name is empty")
	case strings.TrimSpace(ref) == "":
		return model.TagWrite{}, gapi.Errf(gapi.ClassInvalid, "ref is empty: the branch, tag or commit to create the tag at")
	}
	t, err := s.writeTarget(ctx, raw)
	if err != nil {
		return model.TagWrite{}, err
	}
	exists, err := s.tagExists(ctx, t.p, name)
	if err != nil {
		return model.TagWrite{}, err
	}
	if exists {
		return model.TagWrite{}, gapi.Errf(gapi.ClassConflict, "the tag %q exists", name)
	}
	rule, err := protectingRule(func(o gapi.ListOptions) ([]string, gapi.Page, error) {
		rows, page, err := s.client.ListProtectedTags(ctx, t.p, o)
		return ruleNames(rows, func(r gitlab.ProtectedTag) string { return r.Name }), page, err
	}, "tag", name)
	if err != nil {
		return model.TagWrite{}, err
	}
	if rule != "" {
		return model.TagWrite{}, gapi.Errf(gapi.ClassBlocked, "the tag would be protected (rule %q): its pipelines see protected "+
			"variables and may deploy, so this server does not create it; nothing was sent", rule)
	}
	out := model.TagWrite{Outcome: "created", Write: model.Write{Target: t.ref}, Tag: name, Annotated: message != ""}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun = "dry_run", true
		out.WouldSend = preview("POST", "create a tag", names(field{"tag_name", true}, field{"ref", true}, field{"message", message != ""}))
		return out, nil
	}
	if err := ask(ctx, render.AskCreateTag(t.ref.Project.Path, name, ref)); err != nil {
		return model.TagWrite{}, err
	}
	tag, err := s.client.CreateTag(ctx, t.p, name, ref, message)
	if err != nil {
		return model.TagWrite{}, settle(err, "tag", func() (string, error) {
			got, err := s.client.GetTag(ctx, t.p, name)
			if gapi.IsClass(err, gapi.ClassNotFound) {
				return "", nil
			}
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("the tag %s at %s", got.Name, got.Commit.ID), nil
		})
	}
	out.CommitSHA, out.Protected = tag.Commit.ID, tag.Protected
	return out, nil
}

// DeleteTag deletes a tag the caller read. A protected tag is refused.
func (s *Service) DeleteTag(ctx context.Context, raw, name, sha string) (model.TagWrite, error) {
	switch {
	case strings.TrimSpace(name) == "":
		return model.TagWrite{}, gapi.Errf(gapi.ClassInvalid, "tag_name is empty")
	case len(strings.TrimSpace(sha)) < 7:
		return model.TagWrite{}, gapi.Errf(gapi.ClassInvalid, "sha is required: the commit the tag points at as list_tags returned it, "+
			"at least its first seven characters")
	}
	t, err := s.writeTarget(ctx, raw)
	if err != nil {
		return model.TagWrite{}, err
	}
	tag, err := s.client.GetTag(ctx, t.p, name)
	if err != nil {
		return model.TagWrite{}, err
	}
	switch {
	case tag.Protected:
		return model.TagWrite{}, gapi.Errf(gapi.ClassBlocked, "the tag is protected, and a protected tag is never deleted here; nothing was sent")
	case !sameHead(tag.Commit.ID, sha):
		return model.TagWrite{}, gapi.Errf(gapi.ClassStale, "the tag points at %s, not %s: it was recreated since it was read", tag.Commit.ID, sha)
	}
	out := model.TagWrite{Outcome: "deleted", Write: model.Write{Target: t.ref}, Tag: name, CommitSHA: tag.Commit.ID,
		Annotated: tag.Message != ""}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun, out.WouldSend = "dry_run", true, preview("DELETE", "delete the tag", nil)
		return out, nil
	}
	if err := ask(ctx, render.AskDeleteTag(t.ref.Project.Path, name, tag.Commit.ID, tag.Release != nil)); err != nil {
		return model.TagWrite{}, err
	}
	err = s.client.DeleteTag(ctx, t.p, name)
	_, readErr := s.client.GetTag(ctx, t.p, name)
	if out.Notes, err = deleted(err, readErr, "tag"); err != nil {
		return model.TagWrite{}, err
	}
	return out, nil
}
