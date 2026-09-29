package service

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/internal/gapi"
	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/v2/internal/instance"
	"github.com/mmedum/gitlab-mcp/v2/internal/model"
	"github.com/mmedum/gitlab-mcp/v2/internal/render"
)

// maxDiscussionPages bounds the threads read for one call: ten pages of
// a hundred. A longer history is reported as incomplete, never as
// shorter than it is.
const maxDiscussionPages = 10

// description prepares a description for a result: hidden text
// removed, links shown, and the part from offset that fits the budget.
func (s *Service) description(text string, offset int) (string, model.Budget, error) {
	clean, removed := render.Markdown(text, s.self())
	return cut(clean, removed, offset, render.DescriptionBudget, "the description", "offset")
}

// GetIssue reads an issue with a summary of its threads and the merge
// requests linked to it (§7.2). The reads are independent and run at
// once. The link reads are best effort, as the summary is, and a read
// that continues the description from an offset leaves them out.
func (s *Service) GetIssue(ctx context.Context, raw string, iid int64, offset int) (model.Issue, error) {
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.Issue{}, err
	}
	var (
		wg                     sync.WaitGroup
		is                     *gitlab.Issue
		summary                model.DiscussionSummary
		summaryErr             error
		related, closing       *model.LinkedItems
		relatedErr, closingErr error
	)
	wg.Go(func() { is, err = s.client.GetIssue(ctx, p, iid) })
	wg.Go(func() { summary, summaryErr = s.summary(ctx, p, iid, false) })
	if offset == 0 {
		wg.Go(func() {
			related, relatedErr = linked(func(opts gapi.ListOptions) ([]gitlab.LinkedMergeRequest, gapi.Page, error) {
				return s.client.ListIssueRelatedMergeRequests(ctx, p, iid, opts)
			}, linkedMergeRequest)
		})
		wg.Go(func() {
			closing, closingErr = linked(func(opts gapi.ListOptions) ([]gitlab.LinkedMergeRequest, gapi.Page, error) {
				return s.client.ListIssueClosedBy(ctx, p, iid, opts)
			}, linkedMergeRequest)
		})
	}
	wg.Wait()
	if err != nil {
		return model.Issue{}, err
	}
	for _, err := range []error{summaryErr, relatedErr, closingErr} {
		if err != nil {
			return model.Issue{}, err
		}
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
		UntrustedTitle: title, UntrustedDescription: desc, DescriptionBudget: budget, Discussions: summary,
		RelatedMergeRequests: related, ClosingMergeRequests: closing,
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
	return out, nil
}

// GetMergeRequest reads a merge request with its approval state, a
// summary of its threads and the issues linked to it (§7.2). The reads
// are independent and run at once. The approval and link reads are best
// effort: an instance that refuses them still has a merge request to
// show. A read that continues the description from an offset leaves the
// links out.
func (s *Service) GetMergeRequest(ctx context.Context, raw string, iid int64, offset int) (model.MergeRequest, error) {
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.MergeRequest{}, err
	}
	var (
		wg                    sync.WaitGroup
		mr                    *gitlab.MergeRequest
		approvals             *gitlab.Approvals
		approvalsErr          error
		summary               model.DiscussionSummary
		summaryErr            error
		closes, related       *model.LinkedItems
		closesErr, relatedErr error
	)
	wg.Go(func() { mr, err = s.client.GetMergeRequest(ctx, p, iid) })
	wg.Go(func() { approvals, approvalsErr = s.client.GetMergeRequestApprovals(ctx, p, iid) })
	wg.Go(func() { summary, summaryErr = s.summary(ctx, p, iid, true) })
	if offset == 0 {
		wg.Go(func() {
			closes, closesErr = linked(func(opts gapi.ListOptions) ([]gitlab.LinkedIssue, gapi.Page, error) {
				return s.client.ListMergeRequestClosesIssues(ctx, p, iid, opts)
			}, s.linkedIssue)
		})
		wg.Go(func() {
			related, relatedErr = linked(func(opts gapi.ListOptions) ([]gitlab.LinkedIssue, gapi.Page, error) {
				return s.client.ListMergeRequestRelatedIssues(ctx, p, iid, opts)
			}, s.linkedIssue)
		})
	}
	wg.Wait()
	if err != nil {
		return model.MergeRequest{}, err
	}
	if approvalsErr != nil && !soft(approvalsErr) {
		return model.MergeRequest{}, approvalsErr
	}
	for _, err := range []error{summaryErr, closesErr, relatedErr} {
		if err != nil {
			return model.MergeRequest{}, err
		}
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
		DescriptionBudget: budget, Discussions: summary, ClosesIssues: closes, RelatedIssues: related,
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
	if a := approvals; approvalsErr == nil {
		out.Approvals = &model.Approvals{Approved: a.Approved, Required: a.ApprovalsRequired, Left: a.ApprovalsLeft,
			ApprovedBy: []string{}}
		for _, by := range a.ApprovedBy {
			out.Approvals.ApprovedBy = append(out.Approvals.ApprovedBy, by.User.Username)
		}
	}
	return out, nil
}

// linkedPage is how many linked items a read shows: one page, GitLab's
// default size.
const linkedPage = gapi.DefaultPerPage

// linked reads the first page of a list of linked items. It is best
// effort: a soft failure leaves the list null rather than failing the
// read it is part of.
func linked[T any](read func(gapi.ListOptions) ([]T, gapi.Page, error), item func(T) model.LinkedItem) (*model.LinkedItems, error) {
	rows, page, err := read(gapi.ListOptions{PerPage: linkedPage})
	if err != nil {
		if soft(err) {
			return nil, nil
		}
		return nil, err
	}
	out := &model.LinkedItems{Items: make([]model.LinkedItem, 0, len(rows)), More: !page.Complete()}
	if page.TotalKnown() {
		total := page.Total
		out.Total = &total
	}
	for _, r := range rows {
		out.Items = append(out.Items, item(r))
	}
	return out, nil
}

func linkedMergeRequest(mr gitlab.LinkedMergeRequest) model.LinkedItem {
	title, _ := render.Line(mr.Title, render.TitleChars)
	return model.LinkedItem{Reference: mr.References.Full, IID: mr.IID, ProjectID: mr.ProjectID, State: mr.State,
		WebURL: mr.WebURL, UntrustedTitle: title}
}

// externalID is the shape an external tracker's issue id is kept in:
// PROJ-123 and the like. Anything else is someone else's text, which
// only the title, inside the boundary, carries (§4.1).
var externalID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.#-]{0,63}$`)

// linkedIssue is an issue, or an external tracker's issue, which has
// only its id and a title.
func (s *Service) linkedIssue(is gitlab.LinkedIssue) model.LinkedItem {
	title, _ := render.Line(is.Title, render.TitleChars)
	if id := is.ExternalID(); id != "" {
		out := model.LinkedItem{External: true, UntrustedTitle: title}
		if externalID.MatchString(id) {
			out.ExternalID = &id
		}
		return out
	}
	return model.LinkedItem{Reference: s.issueReference(is.WebURL, is.IID), IID: is.IID, ProjectID: is.ProjectID,
		State: is.State, WebURL: is.WebURL, UntrustedTitle: title}
}

// issueReference spells an issue's full reference, group/project#12,
// from its web URL, as resolve_url reads one: GitLab's issue rows in
// these lists carry no references. It is "" for a URL that is not this
// issue's on this instance.
func (s *Service) issueReference(webURL string, iid int64) string {
	ref, err := s.inst.ResolveURL(webURL)
	if err != nil || ref.Kind != instance.KindIssue || ref.IID != iid {
		return ""
	}
	return ref.Project + "#" + strconv.FormatInt(iid, 10)
}

// discussions reads every thread of an issue or merge request, up to
// maxDiscussionPages; complete is false when there were more.
func (s *Service) discussions(ctx context.Context, p gapi.Project, iid int64, mr bool) ([]gitlab.Discussion, bool, error) {
	return readPages(maxDiscussionPages, func(opts gapi.ListOptions) ([]gitlab.Discussion, gapi.Page, error) {
		if mr {
			return s.client.ListMergeRequestDiscussions(ctx, p, iid, opts)
		}
		return s.client.ListIssueDiscussions(ctx, p, iid, opts)
	})
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
	Project string
	IID     int64
	// Type is issue or merge_request: which IID names.
	Type           string
	IncludeSystem  bool
	UnresolvedOnly bool
	Max            int
	PageToken      string
	// NoteID and Offset read one comment on from an offset, when a
	// previous page cut it.
	NoteID int64
	Offset int
}

func (q DiscussionQuery) mergeRequest() bool { return q.Type == "merge_request" }

// binding names the query a list_discussions page token belongs to. The
// token is this server's own rather than GitLab's: GitLab returns
// threads oldest first, and they are shown newest first.
func (q DiscussionQuery) binding(projectID int64) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%d/%d/%s/%t/%t", projectID, q.IID, q.Type, q.IncludeSystem, q.UnresolvedOnly))
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
	all, complete, err := s.discussions(ctx, p, q.IID, q.mergeRequest())
	if err != nil {
		return model.Discussions{}, err
	}
	out := model.Discussions{Project: ref, IID: q.IID, Type: q.Type, Threads: []model.Thread{},
		NotShown: []model.ThreadStub{}, Budget: render.DiscussionBudget}
	if q.NoteID != 0 {
		return s.oneNote(out, all, q)
	}

	threads := s.filterThreads(all, q)
	start := 0
	if q.PageToken != "" {
		if err := gapi.DecodeToken(q.PageToken, q.binding(p.ID()), &start); err != nil {
			return model.Discussions{}, err
		}
		if start < 0 {
			return model.Discussions{}, gapi.Errf(gapi.ClassInvalid,
				"page_token is not one this server issued: pass it exactly as returned, or start again without it")
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
		tok := gapi.EncodeToken(q.binding(p.ID()), next)
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
			if t.Position == nil {
				t.Position = diffPosition(n.Position)
			}
			nm, _ := s.note(n, 0, render.NoteBudget) // offset 0 is never past the end
			t.Notes = append(t.Notes, nm)
		}
		if len(t.Notes) > 0 {
			threads = append(threads, t)
		}
	}
	slices.SortStableFunc(threads, func(a, b model.Thread) int {
		if c := b.LastActivity.Compare(a.LastActivity); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return threads
}

// note prepares one comment from offset.
func (s *Service) note(n gitlab.Note, offset, budget int) (model.Note, error) {
	clean, removed := render.Markdown(n.Body, s.self())
	body, b, err := cut(clean, removed, offset, budget, fmt.Sprintf("comment %d", n.ID), "offset")
	if err != nil {
		return model.Note{}, err
	}
	return model.Note{ID: n.ID, Author: user(n.Author), CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt, System: n.System,
		Internal: n.Internal, UntrustedBody: body, Budget: b}, nil
}

// oneNote answers a read of one comment from an offset.
func (s *Service) oneNote(out model.Discussions, all []gitlab.Discussion, q DiscussionQuery) (model.Discussions, error) {
	for _, d := range all {
		for _, n := range d.Notes {
			if n.ID != q.NoteID {
				continue
			}
			resolvable, resolved := resolution(d)
			nm, err := s.note(n, q.Offset, render.DiscussionBudget)
			if err != nil {
				return model.Discussions{}, err
			}
			out.Threads = append(out.Threads, model.Thread{ID: d.ID, Individual: d.IndividualNote, Resolvable: resolvable,
				Resolved: resolved, LastActivity: lastTime(n), Notes: []model.Note{nm}})
			total := 1
			out.Listing = model.Listing{Returned: 1, Complete: true, Total: &total}
			return out, nil
		}
	}
	return model.Discussions{}, gapi.Errf(gapi.ClassNotFound, "comment %d is not on this %s", q.NoteID, strings.ReplaceAll(q.Type, "_", " "))
}

// ------------------------------------------------------------- events

// maxEventPages bounds the pages of one kind of event read for one call:
// ten of a hundred. GitLab lists events oldest first, so past that the
// first page and the newest pages are read, and the timeline says where
// it starts.
const maxEventPages = 10

// EventQuery is list_item_events' query.
type EventQuery struct {
	Project string
	IID     int64
	// Type is issue or merge_request: which IID names.
	Type      string
	Max       int
	PageToken string
}

// binding names the query a list_item_events page token belongs to. The
// token is this server's own: the timeline merges four listings.
func (q EventQuery) binding(projectID int64) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "events/%d/%d/%s", projectID, q.IID, q.Type))
	return hex.EncodeToString(sum[:12])
}

// eventStream is one kind of event as read.
type eventStream struct {
	events []model.ItemEvent
	// cut is true when pages between the first and the newest were not
	// read; events then holds the newest pages only.
	cut bool
}

// ListItemEvents merges an issue's or a merge request's label, state,
// milestone and weight events into one timeline, newest first (§7.2).
// Each kind is read whole, up to maxEventPages; when one is cut, the
// timeline starts at the oldest event read of it, so no kind is missing
// from the part shown. Weight is an issue's only.
func (s *Service) ListItemEvents(ctx context.Context, q EventQuery) (model.ItemEvents, error) {
	p, ref, err := s.project(ctx, q.Project)
	if err != nil {
		return model.ItemEvents{}, err
	}
	start := 0
	if q.PageToken != "" {
		if err := gapi.DecodeToken(q.PageToken, q.binding(p.ID()), &start); err != nil {
			return model.ItemEvents{}, err
		}
		if start < 0 {
			return model.ItemEvents{}, gapi.Errf(gapi.ClassInvalid,
				"page_token is not one this server issued: pass it exactly as returned, or start again without it")
		}
	}
	streams, err := s.eventStreams(ctx, p, q.IID, q.Type == "merge_request")
	if err != nil {
		return model.ItemEvents{}, err
	}

	// A cut kind is known only from its oldest event read on; the others
	// are shown from there too.
	var since *time.Time
	complete := true
	for _, st := range streams {
		if !st.cut {
			continue
		}
		complete = false
		if len(st.events) == 0 {
			continue
		}
		oldest := st.events[0].CreatedAt
		for _, e := range st.events {
			if e.CreatedAt.Before(oldest) {
				oldest = e.CreatedAt
			}
		}
		if since == nil || oldest.After(*since) {
			since = &oldest
		}
	}
	events := []model.ItemEvent{}
	for _, st := range streams {
		for _, e := range st.events {
			if since == nil || !e.CreatedAt.Before(*since) {
				events = append(events, e)
			}
		}
	}
	slices.SortStableFunc(events, func(a, b model.ItemEvent) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		if c := strings.Compare(a.Kind, b.Kind); c != 0 {
			return c
		}
		return cmp.Compare(b.ID, a.ID)
	})

	perPage := q.Max
	if perPage <= 0 {
		perPage = gapi.DefaultPerPage
	}
	start = min(start, len(events))
	next := min(len(events), start+perPage)
	out := model.ItemEvents{Project: ref, IID: q.IID, Type: q.Type, Events: events[start:next], Since: since,
		Listing: model.Listing{Returned: next - start, Complete: complete && next >= len(events)}}
	if complete {
		total := len(events)
		out.Listing.Total = &total
	}
	if next < len(events) {
		tok := gapi.EncodeToken(q.binding(p.ID()), next)
		out.Listing.NextPageToken = &tok
	}
	return out, nil
}

// eventStreams reads each kind of event at once.
func (s *Service) eventStreams(ctx context.Context, p gapi.Project, iid int64, mr bool) ([]eventStream, error) {
	reads := []func() (eventStream, error){
		func() (eventStream, error) {
			return readEvents(func(o gapi.ListOptions) ([]gitlab.LabelEvent, gapi.Page, error) {
				if mr {
					return s.client.ListMergeRequestLabelEvents(ctx, p, iid, o)
				}
				return s.client.ListIssueLabelEvents(ctx, p, iid, o)
			}, eventLabel)
		},
		func() (eventStream, error) {
			return readEvents(func(o gapi.ListOptions) ([]gitlab.StateEvent, gapi.Page, error) {
				if mr {
					return s.client.ListMergeRequestStateEvents(ctx, p, iid, o)
				}
				return s.client.ListIssueStateEvents(ctx, p, iid, o)
			}, eventState)
		},
		func() (eventStream, error) {
			return readEvents(func(o gapi.ListOptions) ([]gitlab.MilestoneEvent, gapi.Page, error) {
				if mr {
					return s.client.ListMergeRequestMilestoneEvents(ctx, p, iid, o)
				}
				return s.client.ListIssueMilestoneEvents(ctx, p, iid, o)
			}, eventMilestone)
		},
	}
	if !mr {
		reads = append(reads, func() (eventStream, error) {
			return readEvents(func(o gapi.ListOptions) ([]gitlab.WeightEvent, gapi.Page, error) {
				return s.client.ListIssueWeightEvents(ctx, p, iid, o)
			}, eventWeight)
		})
	}
	streams := make([]eventStream, len(reads))
	errs := make([]error, len(reads))
	var wg sync.WaitGroup
	for i, read := range reads {
		wg.Go(func() { streams[i], errs[i] = read() })
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return streams, nil
}

// readEvents reads one kind of event, which GitLab lists oldest first:
// every page up to maxEventPages, or else the first page and then the
// newest, from the page count GitLab gives.
func readEvents[T any](read func(gapi.ListOptions) ([]T, gapi.Page, error), event func(T) model.ItemEvent) (eventStream, error) {
	page := func(n int) ([]T, gapi.Page, error) { return read(gapi.ListOptions{PerPage: gapi.MaxPerPage, Page: n}) }
	rows, first, err := page(1)
	if err != nil {
		return eventStream{}, err
	}
	var st eventStream
	if !first.Complete() {
		if first.Pages < 1 {
			return eventStream{}, gapi.Errf(gapi.ClassUnexpected,
				"GitLab did not say how many pages of events there are, which it stops saying past 10,000; the timeline cannot be read newest first")
		}
		from := max(2, first.Pages-maxEventPages+2)
		if st.cut = from > 2; st.cut {
			rows = nil
		}
		for n := from; n <= first.Pages; n++ {
			more, _, err := page(n)
			if err != nil {
				return eventStream{}, err
			}
			rows = append(rows, more...)
		}
	}
	for _, r := range rows {
		st.events = append(st.events, event(r))
	}
	return st, nil
}

func eventUser(u *gitlab.UserBasic) *model.User {
	if u == nil {
		return nil
	}
	m := user(*u)
	return &m
}

func eventLabel(e gitlab.LabelEvent) model.ItemEvent {
	out := model.ItemEvent{Kind: "label", ID: e.ID, CreatedAt: e.CreatedAt, User: eventUser(e.User), Action: e.Action,
		LabelDeleted: e.Label == nil}
	if e.Label != nil {
		name := e.Label.Name
		out.Label = &name
	}
	return out
}

func eventState(e gitlab.StateEvent) model.ItemEvent {
	state := e.State
	return model.ItemEvent{Kind: "state", ID: e.ID, CreatedAt: e.CreatedAt, User: eventUser(e.User), State: &state,
		SourceCommit: e.SourceCommit, SourceMergeRequestID: e.SourceMergeRequestID}
}

func eventMilestone(e gitlab.MilestoneEvent) model.ItemEvent {
	out := model.ItemEvent{Kind: "milestone", ID: e.ID, CreatedAt: e.CreatedAt, User: eventUser(e.User), Action: e.Action}
	if m := e.Milestone; m != nil {
		title, _ := render.Line(m.Title, render.TitleChars)
		out.Milestone = &model.EventMilestone{ID: m.ID, UntrustedTitle: title}
	}
	return out
}

func eventWeight(e gitlab.WeightEvent) model.ItemEvent {
	return model.ItemEvent{Kind: "weight", ID: e.ID, CreatedAt: e.CreatedAt, User: eventUser(e.User), Weight: e.Weight}
}

// ------------------------------------------------------------ helpers

// diffPosition is where a note sits on a diff, nil when it sits on none.
// A general draft carries a position with no paths rather than none
// (live, 2026-09-26), which is none here too.
func diffPosition(pos *gitlab.Position) *model.DiffPosition {
	if pos == nil || (pos.NewPath == "" && pos.OldPath == "") {
		return nil
	}
	return &model.DiffPosition{OldPath: pos.OldPath, NewPath: pos.NewPath, OldLine: pos.OldLine, NewLine: pos.NewLine, HeadSHA: pos.HeadSHA}
}

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
