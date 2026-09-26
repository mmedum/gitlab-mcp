package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/internal/model"
	"github.com/mmedum/gitlab-mcp/internal/render"
)

// maxDiscussionPages bounds the threads read for one call: ten pages of
// a hundred. A longer history is reported as incomplete, never as
// shorter than it is.
const maxDiscussionPages = 10

// description prepares a description for a result: hidden text
// removed, links shown, and the part from offset that fits the budget.
func (s *Service) description(text string, offset int) (string, model.Budget, error) {
	clean, removed := render.Markdown(text, s.self())
	shown, b := render.Cut(clean, offset, render.DescriptionBudget)
	if offset > b.TotalChars {
		return "", model.Budget{}, gapi.Errf(gapi.ClassInvalid, "offset %d is past the end of the description, which has %d characters", offset, b.TotalChars)
	}
	b.HiddenRemoved = removed
	return shown, b, nil
}

// GetIssue reads an issue with a summary of its threads (§7.2).
func (s *Service) GetIssue(ctx context.Context, raw string, iid int64, offset int) (model.Issue, error) {
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.Issue{}, err
	}
	is, err := s.client.GetIssue(ctx, p, iid)
	if err != nil {
		return model.Issue{}, err
	}
	desc, budget, err := s.description(is.Description, offset)
	if err != nil {
		return model.Issue{}, err
	}
	title, _ := render.Line(is.Title, 0)
	out := model.Issue{
		Project: ref, IID: is.IID, Reference: is.References.Full, WebURL: is.WebURL, State: is.State, Type: is.Type,
		Confidential: is.Confidential, Author: user(is.Author), Assignees: users(is.Assignees), Labels: nonNil(is.Labels),
		Milestone: milestone(is.Milestone), CreatedAt: is.CreatedAt, UpdatedAt: is.UpdatedAt, ClosedAt: is.ClosedAt,
		UntrustedTitle: title, UntrustedDescription: desc, DescriptionBudget: budget,
	}
	if is.ClosedBy != nil {
		u := user(*is.ClosedBy)
		out.ClosedBy = &u
	}
	if is.DueDate != "" {
		d := is.DueDate
		out.DueDate = &d
	}
	if t := is.TaskCompletion; t != nil {
		out.Tasks = &model.Tasks{Count: t.Count, Completed: t.CompletedCount}
	}
	if out.Discussions, err = s.summary(ctx, p, iid, false); err != nil {
		return model.Issue{}, err
	}
	return out, nil
}

// GetMergeRequest reads a merge request with its approval state and a
// summary of its threads (§7.2). The approval read is best effort: an
// instance that refuses it still has a merge request to show.
func (s *Service) GetMergeRequest(ctx context.Context, raw string, iid int64, offset int) (model.MergeRequest, error) {
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.MergeRequest{}, err
	}
	mr, err := s.client.GetMergeRequest(ctx, p, iid)
	if err != nil {
		return model.MergeRequest{}, err
	}
	desc, budget, err := s.description(mr.Description, offset)
	if err != nil {
		return model.MergeRequest{}, err
	}
	title, _ := render.Line(mr.Title, 0)
	out := model.MergeRequest{
		Project: ref, IID: mr.IID, Reference: mr.References.Full, WebURL: mr.WebURL, State: mr.State, Draft: mr.Draft,
		Author: user(mr.Author), Assignees: users(mr.Assignees), Reviewers: users(mr.Reviewers), Labels: nonNil(mr.Labels),
		Milestone: milestone(mr.Milestone), SourceBranch: mr.SourceBranch, TargetBranch: mr.TargetBranch,
		SourceProjectID: mr.SourceProjectID, SHA: mr.SHA, DetailedMergeStatus: mr.DetailedMergeStatus,
		HasConflicts: mr.HasConflicts, ChangesCount: mr.ChangesCount, CreatedAt: mr.CreatedAt, UpdatedAt: mr.UpdatedAt,
		MergedAt: mr.MergedAt, ClosedAt: mr.ClosedAt, UntrustedTitle: title, UntrustedDescription: desc,
		DescriptionBudget: budget,
	}
	if mr.MergeUser != nil {
		u := user(*mr.MergeUser)
		out.MergedBy = &u
	}
	if d := mr.DiffRefs; d != nil {
		out.DiffRefs = &model.DiffRefs{BaseSHA: d.BaseSHA, StartSHA: d.StartSHA, HeadSHA: d.HeadSHA}
	}
	if hp := mr.HeadPipeline; hp != nil {
		out.HeadPipeline = &model.Pipeline{ID: hp.ID, Status: hp.Status, Ref: hp.Ref, SHA: hp.SHA}
	}
	a, err := s.client.GetMergeRequestApprovals(ctx, p, iid)
	switch {
	case err == nil:
		out.Approvals = &model.Approvals{Approved: a.Approved, Required: a.ApprovalsRequired, Left: a.ApprovalsLeft,
			ApprovedBy: []string{}}
		for _, by := range a.ApprovedBy {
			out.Approvals.ApprovedBy = append(out.Approvals.ApprovedBy, by.User.Username)
		}
	case !soft(err):
		return model.MergeRequest{}, err
	}
	if out.Discussions, err = s.summary(ctx, p, iid, true); err != nil {
		return model.MergeRequest{}, err
	}
	return out, nil
}

// discussions reads every thread of an issue or merge request, up to
// maxDiscussionPages; complete is false when there were more.
func (s *Service) discussions(ctx context.Context, p gapi.Project, iid int64, mr bool) ([]gitlab.Discussion, bool, error) {
	var all []gitlab.Discussion
	opts := gapi.ListOptions{PerPage: gapi.MaxPerPage}
	for range maxDiscussionPages {
		var rows []gitlab.Discussion
		var page gapi.Page
		var err error
		if mr {
			rows, page, err = s.client.ListMergeRequestDiscussions(ctx, p, iid, opts)
		} else {
			rows, page, err = s.client.ListIssueDiscussions(ctx, p, iid, opts)
		}
		if err != nil {
			return nil, false, err
		}
		all = append(all, rows...)
		if page.Complete() {
			return all, true, nil
		}
		opts.PageToken = page.NextToken
	}
	return all, false, nil
}

// summary counts threads for get_issue and get_merge_request. It is
// best effort: a failure leaves it unknown rather than failing the read.
func (s *Service) summary(ctx context.Context, p gapi.Project, iid int64, mr bool) (model.DiscussionSummary, error) {
	all, complete, err := s.discussions(ctx, p, iid, mr)
	if err != nil {
		if soft(err) {
			return model.DiscussionSummary{}, nil
		}
		return model.DiscussionSummary{}, err
	}
	sum := model.DiscussionSummary{Known: true, Complete: complete}
	for _, d := range all {
		person := false
		for _, n := range d.Notes {
			person = person || !n.System
			at := lastTime(n)
			if sum.LastActivity == nil || at.After(*sum.LastActivity) {
				sum.LastActivity = &at
			}
		}
		if person {
			sum.Threads++
		}
		if resolvable, resolved := resolution(d); resolvable && !resolved {
			sum.Unresolved++
		}
	}
	return sum, nil
}

// resolution reads a thread's resolved state: resolvable when any note
// is, resolved when every resolvable note is.
func resolution(d gitlab.Discussion) (resolvable, resolved bool) {
	resolved = true
	for _, n := range d.Notes {
		if n.Resolvable {
			resolvable = true
			resolved = resolved && n.Resolved
		}
	}
	return resolvable, resolvable && resolved
}

func lastTime(n gitlab.Note) time.Time {
	if n.UpdatedAt.After(n.CreatedAt) {
		return n.UpdatedAt
	}
	return n.CreatedAt
}

// -------------------------------------------------------- discussions

// DiscussionQuery is list_discussions' query.
type DiscussionQuery struct {
	Project        string
	IID            int64
	MergeRequest   bool
	IncludeSystem  bool
	UnresolvedOnly bool
	Max            int
	PageToken      string
	// NoteID and Offset read one comment on from an offset, when a
	// previous page cut it.
	NoteID int64
	Offset int
}

// threadToken is list_discussions' own page token: GitLab returns
// threads oldest first, and they are shown newest first, so the
// position is this server's rather than GitLab's.
type threadToken struct {
	B string `json:"b"`
	O int    `json:"o"`
}

func (q DiscussionQuery) binding(projectID int64) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%d/%d/%t/%t/%t", projectID, q.IID, q.MergeRequest, q.IncludeSystem, q.UnresolvedOnly))
	return hex.EncodeToString(sum[:12])
}

// ListDiscussions lists threads newest first under the character
// budget (§4.8). Threads of the page that do not fit are named with
// their author and date, and the next page starts with them.
func (s *Service) ListDiscussions(ctx context.Context, q DiscussionQuery) (model.Discussions, error) {
	p, ref, err := s.project(ctx, q.Project)
	if err != nil {
		return model.Discussions{}, err
	}
	all, complete, err := s.discussions(ctx, p, q.IID, q.MergeRequest)
	if err != nil {
		return model.Discussions{}, err
	}
	out := model.Discussions{Project: ref, IID: q.IID, Type: "issue", Threads: []model.Thread{},
		NotShown: []model.ThreadStub{}, Budget: render.DiscussionBudget}
	if q.MergeRequest {
		out.Type = "merge_request"
	}
	if q.NoteID != 0 {
		return s.oneNote(out, all, q)
	}

	threads := s.filterThreads(all, q)
	start := 0
	if q.PageToken != "" {
		start, err = decodeThreadToken(q.PageToken, q.binding(p.ID()))
		if err != nil {
			return model.Discussions{}, err
		}
	}
	perPage := q.Max
	if perPage <= 0 {
		perPage = gapi.DefaultPerPage
	}
	start = min(start, len(threads))
	window := threads[start:min(len(threads), start+perPage)]
	used := 0
	next := start + len(window)
	for i, t := range window {
		cost := 0
		for _, n := range t.Notes {
			cost += n.Budget.ShownChars
		}
		if len(out.Threads) > 0 && used+cost > render.DiscussionBudget {
			next = start + i
			for _, rest := range window[i:] {
				out.NotShown = append(out.NotShown, model.ThreadStub{ID: rest.ID, Author: rest.Notes[0].Author.Username,
					Notes: len(rest.Notes), LastActivity: rest.LastActivity})
			}
			break
		}
		used += cost
		out.Threads = append(out.Threads, t)
	}
	out.Listing = model.Listing{Returned: len(out.Threads), Complete: complete && next >= len(threads)}
	if complete {
		total := len(threads)
		out.Listing.Total = &total
	}
	// When every thread read is shown but GitLab had more than were
	// read, the listing is incomplete and there is no token to offer:
	// one would return nothing new.
	if next < len(threads) {
		raw, _ := json.Marshal(threadToken{B: q.binding(p.ID()), O: next})
		tok := base64.RawURLEncoding.EncodeToString(raw)
		out.Listing.NextPageToken = &tok
	}
	return out, nil
}

// filterThreads prepares, filters and orders the threads.
func (s *Service) filterThreads(all []gitlab.Discussion, q DiscussionQuery) []model.Thread {
	var threads []model.Thread
	for _, d := range all {
		resolvable, resolved := resolution(d)
		if q.UnresolvedOnly && (!resolvable || resolved) {
			continue
		}
		t := model.Thread{ID: d.ID, Individual: d.IndividualNote, Resolvable: resolvable, Resolved: resolved}
		for _, n := range d.Notes {
			if n.System && !q.IncludeSystem {
				continue
			}
			if at := lastTime(n); at.After(t.LastActivity) {
				t.LastActivity = at
			}
			if t.Position == nil && n.Position != nil {
				pos := n.Position
				t.Position = &model.DiffPosition{OldPath: pos.OldPath, NewPath: pos.NewPath, OldLine: pos.OldLine,
					NewLine: pos.NewLine, HeadSHA: pos.HeadSHA}
			}
			t.Notes = append(t.Notes, s.note(n, 0, render.NoteBudget))
		}
		if len(t.Notes) > 0 {
			threads = append(threads, t)
		}
	}
	slices.SortStableFunc(threads, func(a, b model.Thread) int {
		if c := b.LastActivity.Compare(a.LastActivity); c != 0 {
			return c
		}
		return compareStrings(a.ID, b.ID)
	})
	return threads
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// note prepares one comment.
func (s *Service) note(n gitlab.Note, offset, budget int) model.Note {
	clean, removed := render.Markdown(n.Body, s.self())
	body, b := render.Cut(clean, offset, budget)
	b.HiddenRemoved = removed
	return model.Note{ID: n.ID, Author: user(n.Author), CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt, System: n.System,
		Internal: n.Internal, UntrustedBody: body, Budget: b}
}

// oneNote answers a read of one comment from an offset.
func (s *Service) oneNote(out model.Discussions, all []gitlab.Discussion, q DiscussionQuery) (model.Discussions, error) {
	for _, d := range all {
		for _, n := range d.Notes {
			if n.ID != q.NoteID {
				continue
			}
			resolvable, resolved := resolution(d)
			nm := s.note(n, q.Offset, render.DiscussionBudget)
			if q.Offset > nm.Budget.TotalChars {
				return model.Discussions{}, gapi.Errf(gapi.ClassInvalid,
					"offset %d is past the end of comment %d, which has %d characters", q.Offset, q.NoteID, nm.Budget.TotalChars)
			}
			out.Threads = append(out.Threads, model.Thread{ID: d.ID, Individual: d.IndividualNote, Resolvable: resolvable,
				Resolved: resolved, LastActivity: lastTime(n), Notes: []model.Note{nm}})
			total := 1
			out.Listing = model.Listing{Returned: 1, Complete: true, Total: &total}
			return out, nil
		}
	}
	return model.Discussions{}, gapi.Errf(gapi.ClassNotFound, "comment %d is not on this %s", q.NoteID, map[bool]string{false: "issue", true: "merge request"}[q.MergeRequest])
}

func decodeThreadToken(s, binding string) (int, error) {
	var tok threadToken
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err == nil {
		err = json.Unmarshal(raw, &tok)
	}
	if err != nil || tok.B == "" || tok.O < 0 {
		return 0, gapi.Errf(gapi.ClassInvalid, "page_token is not one this server issued: pass it exactly as returned, or start again without it")
	}
	if tok.B != binding {
		return 0, gapi.Errf(gapi.ClassInvalid,
			"page_token was issued for a different query: repeat the call that returned it with the same arguments, or start again without it")
	}
	return tok.O, nil
}

// ------------------------------------------------------------ helpers

func user(u gitlab.UserBasic) model.User { return model.User{Username: u.Username, Name: u.Name} }

func milestone(m *gitlab.Milestone) *model.Milestone {
	if m == nil {
		return nil
	}
	title, _ := render.Line(m.Title, render.TitleChars)
	return &model.Milestone{ID: m.ID, Title: title, State: m.State}
}

func nonNil[T any](xs []T) []T {
	if xs == nil {
		return []T{}
	}
	return xs
}
