package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/config"
	"github.com/mmedum/gitlab-mcp/internal/diffpos"
	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/internal/model"
)

// Comments, threads and reviews (§7.3, §7.4). Bodies arrive already
// through the quick-action guard.

// DiffLocation is a line of a merge request's diff, as the caller names
// it (§6.2).
type DiffLocation struct {
	File    string
	Side    string // new or old
	Line    int
	EndLine int // 0 for one line
}

// place computes where a comment on loc lands, from the merge request's
// latest diff refs and that file's diff (§7.4).
func (s *Service) place(ctx context.Context, p gapi.Project, iid int64, loc DiffLocation) (*diffpos.Position, error) {
	var mr *gitlab.MergeRequest
	var diffs []gitlab.Diff
	if err := parallel(
		func() (err error) { mr, err = s.client.GetMergeRequest(ctx, p, iid); return err },
		func() (err error) {
			diffs, _, err = readPagesUntil(maxMRDiffPages, func(o gapi.ListOptions) ([]gitlab.Diff, gapi.Page, error) {
				return s.client.ListMergeRequestDiffs(ctx, p, iid, o)
			}, func(rows []gitlab.Diff) bool { return diffIndex(rows, loc.File) >= 0 })
			return err
		},
	); err != nil {
		return nil, err
	}
	if mr.DiffRefs == nil || mr.DiffRefs.HeadSHA == "" {
		return nil, gapi.Errf(gapi.ClassInvalid, "GitLab has not computed this merge request's diff yet, so no line of it can take a comment; try again shortly")
	}
	i := diffIndex(diffs, loc.File)
	if i < 0 {
		return nil, gapi.Errf(gapi.ClassInvalid, "the merge request does not change a file at that path; list_mr_files lists the files it changes")
	}
	d := diffs[i]
	if d.Diff == "" && (d.TooLarge || d.Collapsed) {
		return nil, gapi.Errf(gapi.ClassInvalid, "GitLab withheld this file's diff as too large or collapsed, so no line of it can take an inline comment; "+
			"comment on the merge request instead, naming the file and line")
	}
	placed, err := diffpos.Compute(diffpos.Refs{BaseSHA: mr.DiffRefs.BaseSHA, StartSHA: mr.DiffRefs.StartSHA, HeadSHA: mr.DiffRefs.HeadSHA},
		diffpos.File{OldPath: d.OldPath, NewPath: d.NewPath, Diff: d.Diff}, diffpos.Side(loc.Side), loc.Line, loc.EndLine)
	var perr *diffpos.Error
	if errors.As(err, &perr) {
		return nil, gapi.Errf(gapi.ClassInvalid, "%s", perr.Error())
	}
	if err != nil {
		return nil, err
	}
	return &placed.Position, nil
}

// landed is where GitLab says a comment sits, from the position it
// stored: the line, and the lines a range covers.
func landed(pos *gitlab.Position) (*model.DiffPosition, *model.LineSpan) {
	at := diffPosition(pos)
	if at == nil || pos.LineRange == nil {
		return at, nil
	}
	return at, lineSpan(pos.LineRange.Start.OldLine, pos.LineRange.Start.NewLine, pos.LineRange.End.OldLine, pos.LineRange.End.NewLine)
}

// wouldLand is where a dry run's comment would sit, from the position
// computed for it.
func wouldLand(pos *diffpos.Position) (*model.DiffPosition, *model.LineSpan) {
	if pos == nil {
		return nil, nil
	}
	at := &model.DiffPosition{OldPath: pos.OldPath, NewPath: pos.NewPath, OldLine: pos.OldLine, NewLine: pos.NewLine, HeadSHA: pos.HeadSHA}
	if lr := pos.LineRange; lr != nil {
		return at, lineSpan(lr.Start.OldLine, lr.Start.NewLine, lr.End.OldLine, lr.End.NewLine)
	}
	return at, nil
}

// lineSpan is a range's first and last line on the side both ends have.
func lineSpan(startOld, startNew, endOld, endNew *int) *model.LineSpan {
	switch {
	case startNew != nil && endNew != nil:
		return &model.LineSpan{Side: "new", Start: *startNew, End: *endNew}
	case startOld != nil && endOld != nil:
		return &model.LineSpan{Side: "old", Start: *startOld, End: *endOld}
	}
	return nil
}

// checkComment refuses a comment that cannot be sent, before anything
// is read: an empty body, a reply that also names a line, or a line
// that cannot be one.
func checkComment(body, discussionID string, loc *DiffLocation) error {
	switch {
	case strings.TrimSpace(body) == "":
		return gapi.Errf(gapi.ClassInvalid, "body is empty")
	case loc != nil && discussionID != "":
		return gapi.Errf(gapi.ClassInvalid, "a reply joins its thread where it is: pass discussion_id or a file and line, not both")
	}
	return checkLocation(loc)
}

// checkLocation refuses a location that cannot be placed before anything
// is read.
func checkLocation(loc *DiffLocation) error {
	if loc == nil {
		return nil
	}
	switch {
	case strings.TrimSpace(loc.File) == "":
		return gapi.Errf(gapi.ClassInvalid, "file is empty: an inline comment names the file and its line")
	case loc.Line < 1:
		return gapi.Errf(gapi.ClassInvalid, "line is required with file, and starts at 1")
	case loc.Side != "new" && loc.Side != "old":
		return gapi.Errf(gapi.ClassInvalid, "side must be new (the line as it is after the change) or old (as it was before)")
	case loc.EndLine != 0 && loc.EndLine < loc.Line:
		return gapi.Errf(gapi.ClassInvalid, "end_line %d is before line %d", loc.EndLine, loc.Line)
	}
	return nil
}

// Comment is add_comment's request.
type Comment struct {
	Project      string
	Type         string // issue or merge_request
	IID          int64
	Body         string
	DiscussionID string
	Location     *DiffLocation
}

// AddComment posts a comment now: on an issue or a merge request, as a
// reply in a thread, or as a new thread on a line of a merge request's
// diff.
func (s *Service) AddComment(ctx context.Context, in Comment) (model.CommentWrite, error) {
	mr := in.Type == "merge_request"
	if in.Location != nil && !mr {
		return model.CommentWrite{}, gapi.Errf(gapi.ClassInvalid, "an inline comment is on a merge request's diff: pass type merge_request")
	}
	if err := checkComment(in.Body, in.DiscussionID, in.Location); err != nil {
		return model.CommentWrite{}, err
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.CommentWrite{}, err
	}
	kind, operation := "comment", "comment on the "+strings.ReplaceAll(in.Type, "_", " ")
	var pos *diffpos.Position
	switch {
	case in.DiscussionID != "":
		kind, operation = "reply", "reply in a thread"
	case in.Location != nil:
		kind, operation = "thread", "start a thread on a line of the diff"
		if pos, err = s.place(ctx, t.p, in.IID, *in.Location); err != nil {
			return model.CommentWrite{}, err
		}
	}
	if gapi.IsDryRun(ctx) {
		out := model.CommentWrite{Outcome: "dry_run", Kind: kind, Write: model.Write{DryRun: true, Target: t.ref,
			WouldSend: preview("POST", operation, names(field{"body", true}, field{"position", pos != nil}))}}
		out.Position, out.LineRange = wouldLand(pos)
		return out, nil
	}
	start := time.Now()
	var note *gitlab.Note
	discussionID := in.DiscussionID
	switch {
	case in.DiscussionID != "" && mr:
		note, err = s.client.ReplyToMergeRequestDiscussion(ctx, t.p, in.IID, in.DiscussionID, in.Body)
	case in.DiscussionID != "":
		note, err = s.client.ReplyToIssueDiscussion(ctx, t.p, in.IID, in.DiscussionID, in.Body)
	case pos != nil:
		var d *gitlab.Discussion
		if d, err = s.client.CreateMergeRequestDiscussion(ctx, t.p, in.IID, in.Body, pos); err == nil {
			if len(d.Notes) == 0 {
				return model.CommentWrite{}, gapi.Errf(gapi.ClassUnexpected, "GitLab answered the new thread with no comment in it")
			}
			note, discussionID = &d.Notes[0], d.ID
		}
	case mr:
		note, err = s.client.CreateMergeRequestNote(ctx, t.p, in.IID, in.Body)
	default:
		note, err = s.client.CreateIssueNote(ctx, t.p, in.IID, in.Body)
	}
	if err != nil {
		return model.CommentWrite{}, settle(err, "comment", func() (string, error) {
			return s.findNote(ctx, t.p, in.IID, mr, in.DiscussionID, in.Body, start)
		})
	}
	out := model.CommentWrite{Outcome: "created", Kind: kind, Write: model.Write{Target: t.ref}, NoteID: note.ID, DiscussionID: discussionID}
	out.Position, out.LineRange = landed(note.Position)
	return out, nil
}

// newestThreads reads the page of an item's threads that holds the
// newest: page 1, then the last page by number when there are more.
// Threads list oldest first. all is true when the threads read are every
// one.
func (s *Service) newestThreads(ctx context.Context, p gapi.Project, iid int64, mr bool) (rows []gitlab.Discussion, all bool, err error) {
	read := func(o gapi.ListOptions) ([]gitlab.Discussion, gapi.Page, error) {
		if mr {
			return s.client.ListMergeRequestDiscussions(ctx, p, iid, o)
		}
		return s.client.ListIssueDiscussions(ctx, p, iid, o)
	}
	rows, page, err := read(gapi.ListOptions{PerPage: gapi.MaxPerPage})
	if err != nil || page.Complete() {
		return rows, err == nil, err
	}
	if page.Pages < 1 {
		// GitLab did not say how many pages: the newest is not known.
		return nil, false, errors.New("GitLab did not say how many pages of threads there are")
	}
	rows, _, err = read(gapi.ListOptions{PerPage: gapi.MaxPerPage, Page: page.Pages})
	return rows, false, err
}

// findNote settles an ambiguous comment: one by this account with the
// same body since the call started. A reply is looked for in its thread;
// a new comment starts the newest thread, so the last page of threads
// holds it if it was made.
func (s *Service) findNote(ctx context.Context, p gapi.Project, iid int64, mr bool, discussionID, body string, start time.Time) (string, error) {
	me, err := s.me(ctx)
	if err != nil {
		return "", err
	}
	since := start.Add(-settleSkew)
	ours := func(n gitlab.Note) bool {
		return n.Author.Username == me.Username && !n.CreatedAt.Before(since) && sameText(n.Body, body)
	}
	if discussionID != "" {
		var thread *gitlab.Discussion
		if mr {
			thread, err = s.client.GetMergeRequestDiscussion(ctx, p, iid, discussionID)
		} else {
			thread, err = s.client.GetIssueDiscussion(ctx, p, iid, discussionID)
		}
		if err != nil {
			return "", err
		}
		if i := slices.IndexFunc(thread.Notes, ours); i >= 0 {
			return fmt.Sprintf("comment %d in thread %s", thread.Notes[i].ID, thread.ID), nil
		}
		return "", nil
	}
	rows, all, err := s.newestThreads(ctx, p, iid, mr)
	if err != nil {
		return "", err
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if j := slices.IndexFunc(rows[i].Notes, ours); j >= 0 {
			return fmt.Sprintf("comment %d in thread %s", rows[i].Notes[j].ID, rows[i].ID), nil
		}
	}
	// Absent from the newest threads is "not created" only when the page
	// reaches back past the call: had a full page of threads been started
	// since, it could sit on the page before.
	if all || (len(rows) > 0 && len(rows[0].Notes) > 0 && rows[0].Notes[0].CreatedAt.Before(since)) {
		return "", nil
	}
	return "", errors.New("more threads were started since the call than one page shows")
}

// ReviewComment is add_review_comment's request.
type ReviewComment struct {
	Project       string
	IID           int64
	Body          string
	DiscussionID  string
	ResolveThread bool
	Location      *DiffLocation
}

// AddReviewComment creates a draft: a review comment only its author
// sees until submit_review publishes it.
func (s *Service) AddReviewComment(ctx context.Context, in ReviewComment) (model.CommentWrite, error) {
	if in.ResolveThread && in.DiscussionID == "" {
		return model.CommentWrite{}, gapi.Errf(gapi.ClassInvalid, "resolve_thread resolves the thread a reply joins: pass discussion_id with it")
	}
	if err := checkComment(in.Body, in.DiscussionID, in.Location); err != nil {
		return model.CommentWrite{}, err
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.CommentWrite{}, err
	}
	body := gapi.DraftCreate{Note: in.Body, InReplyToDiscussionID: in.DiscussionID, ResolveDiscussion: in.ResolveThread}
	if in.Location != nil {
		if body.Position, err = s.place(ctx, t.p, in.IID, *in.Location); err != nil {
			return model.CommentWrite{}, err
		}
	}
	if gapi.IsDryRun(ctx) {
		out := model.CommentWrite{Outcome: "dry_run", Kind: "draft", DiscussionID: in.DiscussionID, Write: model.Write{DryRun: true, Target: t.ref,
			WouldSend: preview("POST", "add a draft review comment", fieldsOf(body))}}
		out.Position, out.LineRange = wouldLand(body.Position)
		return out, nil
	}
	// The drafts already there, so a lost create is settled by a new one
	// with this body rather than an older draft that says the same.
	existing, err := s.client.ListDraftNotes(ctx, t.p, in.IID)
	if err != nil {
		return model.CommentWrite{}, err
	}
	draft, err := s.client.CreateDraftNote(ctx, t.p, in.IID, body)
	if err != nil {
		return model.CommentWrite{}, settle(err, "draft review comment", func() (string, error) {
			return s.findDraft(ctx, t.p, in.IID, in.Body, existing)
		})
	}
	out := model.CommentWrite{Outcome: "created", Kind: "draft", Write: model.Write{Target: t.ref}, NoteID: draft.ID}
	if draft.DiscussionID != nil {
		out.DiscussionID = *draft.DiscussionID
	}
	out.Position, out.LineRange = landed(draft.Position)
	return out, nil
}

// findDraft settles an ambiguous draft: drafts are only ever the
// signed-in account's, so one with the same body that was not there
// before the call is it.
func (s *Service) findDraft(ctx context.Context, p gapi.Project, iid int64, body string, before []gitlab.DraftNote) (string, error) {
	drafts, err := s.client.ListDraftNotes(ctx, p, iid)
	if err != nil {
		return "", err
	}
	for _, d := range drafts {
		if sameText(d.Note, body) && !containsDraft(before, d.ID) {
			return fmt.Sprintf("draft %d", d.ID), nil
		}
	}
	return "", nil
}

// DeleteReviewComment deletes one of the signed-in account's drafts.
func (s *Service) DeleteReviewComment(ctx context.Context, raw string, iid, draftID int64) (model.DraftDelete, error) {
	t, err := s.writeTarget(ctx, raw)
	if err != nil {
		return model.DraftDelete{}, err
	}
	notYours := gapi.Errf(gapi.ClassNotFound, "you have no draft %d on this merge request; list_review_comments lists yours", draftID)
	if gapi.IsDryRun(ctx) {
		drafts, err := s.client.ListDraftNotes(ctx, t.p, iid)
		if err != nil {
			return model.DraftDelete{}, err
		}
		if !containsDraft(drafts, draftID) {
			return model.DraftDelete{}, notYours
		}
		return model.DraftDelete{Outcome: "dry_run", DraftID: draftID, Remaining: len(drafts) - 1, Write: model.Write{DryRun: true, Target: t.ref,
			WouldSend: preview("DELETE", "delete a draft review comment", nil)}}, nil
	}
	// GitLab answers 404 for a draft that is not the account's own, so
	// the delete itself is the check.
	if err := s.client.DeleteDraftNote(ctx, t.p, iid, draftID); err != nil {
		if gapi.IsClass(err, gapi.ClassNotFound) {
			return model.DraftDelete{}, notYours
		}
		return model.DraftDelete{}, err
	}
	after, err := s.client.ListDraftNotes(ctx, t.p, iid)
	if err != nil {
		return model.DraftDelete{}, err
	}
	if containsDraft(after, draftID) {
		return model.DraftDelete{}, gapi.Errf(gapi.ClassUnexpected, "GitLab accepted the delete, but draft %d is still listed", draftID)
	}
	return model.DraftDelete{Outcome: "deleted", DraftID: draftID, Remaining: len(after), Write: model.Write{Target: t.ref}}, nil
}

func containsDraft(drafts []gitlab.DraftNote, id int64) bool {
	return slices.ContainsFunc(drafts, func(d gitlab.DraftNote) bool { return d.ID == id })
}

// ReviewerStates are submit_review's reviewer states.
var ReviewerStates = []string{"approved", "requested_changes", "reviewed"}

// Review is submit_review's request.
type Review struct {
	Project       string
	IID           int64
	Summary       string
	ReviewerState string
}

// SubmitReview publishes every draft of the signed-in account on a merge
// request at once. An approving review is Ship: it is someone's sign-off
// another person's merge rule counts (§4.3).
func (s *Service) SubmitReview(ctx context.Context, in Review) (model.ReviewSubmit, error) {
	if in.ReviewerState == "approved" && !s.cfg.EnableShip {
		return model.ReviewSubmit{}, gapi.Errf(gapi.ClassBlocked, "an approving review is an approval, which this server sends only when %s=true; "+
			"nothing was sent. Submit with reviewer_state reviewed, or leave it out", config.EnvEnableShip)
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.ReviewSubmit{}, err
	}
	before, err := s.client.ListDraftNotes(ctx, t.p, in.IID)
	if err != nil {
		return model.ReviewSubmit{}, err
	}
	if len(before) == 0 && strings.TrimSpace(in.Summary) == "" && in.ReviewerState == "" {
		return model.ReviewSubmit{}, gapi.Errf(gapi.ClassInvalid, "there is nothing to submit: you have no drafts on this merge request, "+
			"and no summary or reviewer_state was given")
	}
	summary := strings.TrimSpace(in.Summary) != ""
	if gapi.IsDryRun(ctx) {
		return model.ReviewSubmit{Outcome: "dry_run", Published: len(before), Remaining: len(before), Summary: summary, ReviewerState: in.ReviewerState,
			Write: model.Write{DryRun: true, Target: t.ref, WouldSend: preview("POST", "publish your drafts as one review",
				names(field{"note", summary}, field{"reviewer_state", in.ReviewerState != ""}))}}, nil
	}
	if err := s.client.PublishDraftNotes(ctx, t.p, in.IID, in.Summary, in.ReviewerState); err != nil {
		return model.ReviewSubmit{}, settle(err, "review", func() (string, error) {
			if len(before) == 0 {
				return "", errors.New("a review of only a summary leaves no draft to read")
			}
			after, err := s.client.ListDraftNotes(ctx, t.p, in.IID)
			if err != nil || len(after) == len(before) {
				return "", err
			}
			return fmt.Sprintf("%d of %d drafts are no longer pending", len(before)-len(after), len(before)), nil
		})
	}
	after, err := s.client.ListDraftNotes(ctx, t.p, in.IID)
	if err != nil {
		return model.ReviewSubmit{}, err
	}
	out := model.ReviewSubmit{Outcome: "published", Published: len(before) - len(after), Remaining: len(after), Summary: summary,
		ReviewerState: in.ReviewerState, Write: model.Write{Target: t.ref}}
	if len(after) > 0 {
		out.Notes = []string{fmt.Sprintf("%d draft(s) are still listed after publishing; list_review_comments shows them", len(after))}
	}
	return out, nil
}

// ResolveDiscussion resolves or reopens a merge request's thread.
func (s *Service) ResolveDiscussion(ctx context.Context, raw string, iid int64, discussionID string, resolve bool) (model.DiscussionWrite, error) {
	t, err := s.writeTarget(ctx, raw)
	if err != nil {
		return model.DiscussionWrite{}, err
	}
	thread, err := s.client.GetMergeRequestDiscussion(ctx, t.p, iid, discussionID)
	if err != nil {
		return model.DiscussionWrite{}, err
	}
	resolvable, was := resolution(*thread)
	if !resolvable {
		return model.DiscussionWrite{}, gapi.Errf(gapi.ClassInvalid, "this thread cannot be resolved: it is a standalone comment, not a thread")
	}
	out := model.DiscussionWrite{Outcome: "unchanged", DiscussionID: discussionID, Resolved: was, Write: model.Write{Target: t.ref}}
	verb, state := "reopen a thread", "unresolved"
	if resolve {
		verb, state = "resolve a thread", "resolved"
	}
	if was == resolve {
		out.Notes = []string{"It was already " + state + "."}
		return out, nil
	}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun = "dry_run", true
		out.WouldSend = preview("PUT", verb, []string{"resolved"})
		return out, nil
	}
	d, err := s.client.ResolveMergeRequestDiscussion(ctx, t.p, iid, discussionID, resolve)
	if err != nil {
		return model.DiscussionWrite{}, err
	}
	_, now := resolution(*d)
	out.Resolved = now
	switch {
	case now != resolve:
		out.Outcome = "unchanged"
	case now:
		out.Outcome = "resolved"
	default:
		out.Outcome = "reopened"
	}
	return out, nil
}
