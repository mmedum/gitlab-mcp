package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/internal/config"
	"github.com/mmedum/gitlab-mcp/v2/internal/diffpos"
	"github.com/mmedum/gitlab-mcp/v2/internal/gapi"
	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/v2/internal/model"
	"github.com/mmedum/gitlab-mcp/v2/internal/quickaction"
	"github.com/mmedum/gitlab-mcp/v2/internal/render"
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
	// Thread starts a resolvable thread rather than a standalone comment.
	Thread bool
}

// AddComment posts a comment now: on an issue or a merge request, as a
// reply in a thread, as a new resolvable thread, or as a new thread on a
// line of a merge request's diff.
func (s *Service) AddComment(ctx context.Context, in Comment) (model.CommentWrite, error) {
	mr := in.Type == "merge_request"
	if in.Location != nil && !mr {
		return model.CommentWrite{}, gapi.Errf(gapi.ClassInvalid, "an inline comment is on a merge request's diff: pass type merge_request")
	}
	if in.Thread && in.DiscussionID != "" {
		return model.CommentWrite{}, gapi.Errf(gapi.ClassInvalid, "thread starts a new thread and discussion_id replies in one: pass one of them")
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
	case in.Thread:
		kind, operation = "thread", "start a thread on the "+strings.ReplaceAll(in.Type, "_", " ")
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
	case pos != nil || in.Thread:
		var d *gitlab.Discussion
		if mr {
			d, err = s.client.CreateMergeRequestDiscussion(ctx, t.p, in.IID, in.Body, pos)
		} else {
			d, err = s.client.CreateIssueDiscussion(ctx, t.p, in.IID, in.Body)
		}
		if err == nil {
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
	out := model.CommentWrite{Outcome: "created", Kind: kind, Write: model.Write{Target: t.ref}, NoteID: note.ID, DiscussionID: discussionID,
		UpdatedAt: &note.UpdatedAt}
	out.Position, out.LineRange = landed(note.Position)
	return out, nil
}

// thread reads one thread of a merge request, or of an issue.
func (s *Service) thread(ctx context.Context, p gapi.Project, iid int64, mr bool, id string) (*gitlab.Discussion, error) {
	if mr {
		return s.client.GetMergeRequestDiscussion(ctx, p, iid, id)
	}
	return s.client.GetIssueDiscussion(ctx, p, iid, id)
}

// findNote settles an ambiguous comment: one by this account with the
// same body since the call started. A reply is looked for in its thread;
// a new comment starts the newest thread, so the last page of threads
// holds it if it was made.
func (s *Service) findNote(ctx context.Context, p gapi.Project, iid int64, mr bool, discussionID, body string, start time.Time) (string, error) {
	note, thread, err := s.locateNote(ctx, p, iid, mr, discussionID, body, start)
	if err != nil || note == nil {
		return "", err
	}
	return fmt.Sprintf("comment %d in thread %s", note.ID, thread.ID), nil
}

// locateNote finds the comment findNote describes, and its thread; a
// nil note when there is none.
func (s *Service) locateNote(ctx context.Context, p gapi.Project, iid int64, mr bool, discussionID, body string,
	start time.Time) (*gitlab.Note, *gitlab.Discussion, error) {
	me, err := s.me(ctx)
	if err != nil {
		return nil, nil, err
	}
	since := start.Add(-settleSkew)
	ours := func(n gitlab.Note) bool {
		return n.Author.Username == me.Username && !n.CreatedAt.Before(since) && sameText(n.Body, body)
	}
	if discussionID != "" {
		thread, err := s.thread(ctx, p, iid, mr, discussionID)
		if err != nil {
			return nil, nil, err
		}
		if i := slices.IndexFunc(thread.Notes, ours); i >= 0 {
			return &thread.Notes[i], thread, nil
		}
		return nil, nil, nil
	}
	// Threads list oldest first, so the newest are on the last page, and
	// the walk goes back a page at a time until one starts before the
	// call: a comment made then is on it or after it.
	read := func(page int) ([]gitlab.Discussion, gapi.Page, error) {
		o := gapi.ListOptions{PerPage: gapi.MaxPerPage, Page: page}
		if mr {
			return s.client.ListMergeRequestDiscussions(ctx, p, iid, o)
		}
		return s.client.ListIssueDiscussions(ctx, p, iid, o)
	}
	pageOne, first, err := read(1)
	if err != nil {
		return nil, nil, err
	}
	rows, page := pageOne, 1
	if !first.Complete() {
		if first.Pages < 1 {
			return nil, nil, errors.New("GitLab did not say how many pages of threads there are")
		}
		page = first.Pages
		if rows, _, err = read(page); err != nil {
			return nil, nil, err
		}
	}
	for {
		for i := len(rows) - 1; i >= 0; i-- {
			if j := slices.IndexFunc(rows[i].Notes, ours); j >= 0 {
				return &rows[i].Notes[j], &rows[i], nil
			}
		}
		if page == 1 || (len(rows) > 0 && len(rows[0].Notes) > 0 && rows[0].Notes[0].CreatedAt.Before(since)) {
			return nil, nil, nil
		}
		page--
		if page == 1 {
			rows = pageOne
		} else if rows, _, err = read(page); err != nil {
			return nil, nil, err
		}
	}
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
	out := model.CommentWrite{Outcome: "created", Kind: "draft", Write: model.Write{Target: t.ref}, NoteID: draft.ID,
		NoteSHA256: contentHash(draft.Note)}
	if draft.DiscussionID != nil {
		out.DiscussionID = *draft.DiscussionID
	}
	if draft.LineCode != nil {
		out.LineCode = *draft.LineCode
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

// notYourDraft is GitLab's 404 for a draft, which it answers for
// another person's too: drafts are their author's alone.
func notYourDraft(id int64) error {
	return gapi.Errf(gapi.ClassNotFound, "you have no draft %d on this merge request: this server changes only your own drafts, "+
		"and GitLab shows no one else's. list_review_comments lists yours", id)
}

// ownDraft reads one of the signed-in account's drafts.
func (s *Service) ownDraft(ctx context.Context, p gapi.Project, iid, id int64) (*gitlab.DraftNote, error) {
	d, err := s.client.GetDraftNote(ctx, p, iid, id)
	if gapi.IsClass(err, gapi.ClassNotFound) {
		return nil, notYourDraft(id)
	}
	return d, err
}

// keptPosition is the position a draft's edit sends back so the draft
// keeps its place: GitLab clears a position the edit leaves out. A
// draft not on the diff has none, or one with no commits, and keeps
// none.
func keptPosition(pos *gitlab.Position) (*diffpos.Position, error) {
	if pos == nil || pos.HeadSHA == "" {
		return nil, nil
	}
	if pos.PositionType != "text" && pos.PositionType != "file" {
		return nil, gapi.Errf(gapi.ClassUnsupported, "this draft is on an image, and an edit here would lose its place on it; "+
			"edit it in GitLab, or delete it and add it again")
	}
	out := &diffpos.Position{BaseSHA: pos.BaseSHA, StartSHA: pos.StartSHA, HeadSHA: pos.HeadSHA, PositionType: pos.PositionType,
		OldPath: pos.OldPath, NewPath: pos.NewPath, OldLine: pos.OldLine, NewLine: pos.NewLine}
	if lr := pos.LineRange; lr != nil {
		end := func(e gitlab.LineRangeEnd) diffpos.RangeEnd {
			return diffpos.RangeEnd{LineCode: e.LineCode, Type: e.Type, OldLine: e.OldLine, NewLine: e.NewLine}
		}
		out.LineRange = &diffpos.LineRange{Start: end(lr.Start), End: end(lr.End)}
	}
	return out, nil
}

// DraftEdit is update_review_comment's request.
type DraftEdit struct {
	Project    string
	IID        int64
	DraftID    int64
	Body       string
	NoteSHA256 string
}

// UpdateReviewComment replaces the text of one of the signed-in
// account's drafts, keeping it where it is: in its thread, on its line.
func (s *Service) UpdateReviewComment(ctx context.Context, in DraftEdit) (model.DraftUpdate, error) {
	switch {
	case strings.TrimSpace(in.Body) == "":
		return model.DraftUpdate{}, gapi.Errf(gapi.ClassInvalid, "body is empty")
	case strings.TrimSpace(in.NoteSHA256) == "":
		return model.DraftUpdate{}, gapi.Errf(gapi.ClassInvalid, "note_sha256 is required: pass the one list_review_comments or "+
			"add_review_comment gave for the draft")
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.DraftUpdate{}, err
	}
	before, err := s.ownDraft(ctx, t.p, in.IID, in.DraftID)
	if err != nil {
		return model.DraftUpdate{}, err
	}
	out := draftUpdate(t, in.IID, before)
	// Before the witness: an edit that landed and is asked again, after a
	// lost answer, reads as done rather than stale.
	if sameText(before.Note, in.Body) {
		out.Notes = []string{"The draft already reads so; nothing was sent."}
		return out, nil
	}
	// GitLab exposes no time or version for a draft, so the witness is a
	// hash of its text; the window between this read and the PUT stays
	// open (§4.6).
	if current := contentHash(before.Note); !strings.EqualFold(strings.TrimSpace(in.NoteSHA256), current) {
		return model.DraftUpdate{}, gapi.Errf(gapi.ClassStale, "the draft changed since it was read: its note_sha256 is now %s. "+
			"Read it again with list_review_comments, check the change still makes sense, and pass the new note_sha256", current)
	}
	pos, err := keptPosition(before.Position)
	if err != nil {
		return model.DraftUpdate{}, err
	}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun = "dry_run", true
		out.WouldSend = preview("PUT", "update the draft review comment", names(field{"note", true}, field{"position", pos != nil}))
		return out, nil
	}
	after, err := s.client.UpdateDraftNote(ctx, t.p, in.IID, in.DraftID, gapi.DraftUpdate{Note: in.Body, Position: pos})
	if err != nil {
		if after, err = s.settleDraftEdit(ctx, t.p, in, err); err != nil {
			return model.DraftUpdate{}, err
		}
	}
	switch {
	case after.ID != in.DraftID || strings.TrimSpace(after.Note) == "":
		return model.DraftUpdate{}, gapi.Errf(gapi.ClassUnexpected, "GitLab answered the edit without the draft; "+
			"list_review_comments shows what it holds")
	case pos != nil && diffPosition(after.Position) == nil:
		return model.DraftUpdate{}, gapi.Errf(gapi.ClassUnexpected, "GitLab took the new text, but the draft lost its place on the "+
			"diff and would publish as a general thread; list_review_comments shows it")
	}
	out = draftUpdate(t, in.IID, after)
	out.Outcome, out.BodyRemoved = "updated", removedFrom(before.Note, after.Note)
	if !sameText(after.Note, in.Body) {
		out.Notes = []string{"GitLab stored the text with differences from what was sent; list_review_comments shows it."}
	}
	return out, nil
}

// settleDraftEdit reads a draft whose edit failed. GitLab answers 500,
// not 400, when it will not save a draft, so a 500 after which the
// draft still reads as before is the caller's to fix.
func (s *Service) settleDraftEdit(ctx context.Context, p gapi.Project, in DraftEdit, sendErr error) (*gitlab.DraftNote, error) {
	var e *gapi.Error
	if gapi.IsClass(sendErr, gapi.ClassNotFound) {
		return nil, notYourDraft(in.DraftID)
	}
	if !errors.As(sendErr, &e) || e.Status != http.StatusInternalServerError {
		return nil, sendErr
	}
	now, err := s.client.GetDraftNote(ctx, p, in.IID, in.DraftID)
	switch {
	case err != nil:
		return nil, sendErr
	case sameText(now.Note, in.Body):
		return now, nil
	}
	return nil, gapi.Wrap(gapi.ClassInvalid, sendErr, "GitLab failed on the edit with status 500, and the draft still reads as before. "+
		"GitLab answers so when it will not save a draft: a text over its length limit, or a place on the diff it no longer "+
		"accepts. If neither applies, it was a passing failure, and asking again is safe")
}

// draftUpdate is update_review_comment's result for a draft as read.
func draftUpdate(t target, iid int64, d *gitlab.DraftNote) model.DraftUpdate {
	out := model.DraftUpdate{Outcome: "unchanged", Write: model.Write{Target: t.ref}, IID: iid, DraftID: d.ID,
		NoteSHA256: contentHash(d.Note)}
	if d.DiscussionID != nil {
		out.DiscussionID = *d.DiscussionID
	}
	out.Position, out.LineRange = landed(d.Position)
	return out
}

// PublishReviewComment publishes one of the signed-in account's drafts
// on its own, as the comment, thread or reply it was drafted as.
func (s *Service) PublishReviewComment(ctx context.Context, raw string, iid, draftID int64) (model.DraftPublish, error) {
	t, err := s.writeTarget(ctx, raw)
	if err != nil {
		return model.DraftPublish{}, err
	}
	d, err := s.ownDraft(ctx, t.p, iid, draftID)
	if err != nil {
		return model.DraftPublish{}, err
	}
	// GitLab runs a draft's quick actions when it is published. A draft
	// this server wrote passed the guard, but one written elsewhere
	// may hold any (§4.2).
	if lines := quickaction.Find(d.Note); len(lines) > 0 {
		at := make([]string, len(lines))
		for i, l := range lines {
			at[i] = fmt.Sprintf("line %d /%s", l.Number, l.Command)
		}
		return model.DraftPublish{}, gapi.Errf(gapi.ClassBlocked, "GitLab would run the quick actions in draft %d when it is "+
			"published (%s), so nothing was published. update_review_comment with escape_commands: true rewrites those lines "+
			"as plain text", draftID, strings.Join(at, ", "))
	}
	out := model.DraftPublish{Outcome: "dry_run", Write: model.Write{Target: t.ref}, IID: iid, DraftID: draftID, Kind: "thread"}
	out.Position, out.LineRange = landed(d.Position)
	discussionID, wasResolved := "", false
	if d.DiscussionID != nil {
		discussionID, out.Kind, out.DiscussionID = *d.DiscussionID, "reply", *d.DiscussionID
		thread, err := s.thread(ctx, t.p, iid, true, discussionID)
		if err != nil {
			return model.DraftPublish{}, err
		}
		_, wasResolved = resolution(*thread)
		if wasResolved && !d.ResolveDiscussion {
			out.Notes = append(out.Notes, "The thread is resolved, and publishing this reply reopens it: GitLab reopens a thread for "+
				"a reply that does not resolve it.")
		}
	}
	if gapi.IsDryRun(ctx) {
		out.DryRun, out.WouldSend = true, preview("PUT", "publish a draft review comment", nil)
		return out, nil
	}
	start := time.Now()
	if err := s.client.PublishDraftNote(ctx, t.p, iid, draftID); err != nil {
		if gapi.IsClass(err, gapi.ClassNotFound) {
			return model.DraftPublish{}, notYourDraft(draftID)
		}
		return model.DraftPublish{}, s.settlePublish(ctx, t.p, iid, d, start, err)
	}
	// GitLab answers 204 without the comment, and answers so too when it
	// dropped the draft without saving one, so only a read tells.
	note, thread, err := s.locateNote(ctx, t.p, iid, true, discussionID, d.Note, start)
	if err != nil {
		return model.DraftPublish{}, gapi.Wrap(gapi.ClassUnexpected, err, "GitLab answered the publish, but reading the threads to "+
			"find the comment failed; list_discussions shows whether it is there. Do not publish it again: the draft is gone")
	}
	out.Notes = nil
	if note == nil {
		return lostDraft(out, d, s.self()), nil
	}
	out.Outcome, out.NoteID, out.DiscussionID, out.UpdatedAt = "published", note.ID, thread.ID, &note.UpdatedAt
	out.Position, out.LineRange = landed(note.Position)
	if resolvable, now := resolution(*thread); resolvable {
		out.ThreadResolved = &now
		switch {
		case d.DiscussionID != nil && d.ResolveDiscussion && !now:
			out.Notes = append(out.Notes, "The draft was to resolve the thread, but it is unresolved: GitLab resolves it only when "+
				"your role may.")
		case wasResolved && !now:
			out.Notes = append(out.Notes, "Publishing the reply reopened the thread, which was resolved.")
		}
	}
	if diffPosition(d.Position) != nil && out.Position == nil {
		out.Notes = append(out.Notes, "The draft was on a line of the diff, but GitLab published it as a general thread.")
	}
	return out, nil
}

// lostDraft is the result for a draft GitLab deleted without saving a
// comment: it logs a warning and answers 204 all the same. The text is
// given back so it can be added again.
func lostDraft(out model.DraftPublish, d *gitlab.DraftNote, self string) model.DraftPublish {
	clean, _ := render.Markdown(d.Note, self)
	text, b := render.Cut(clean, 0, render.DiscussionBudget)
	out.Outcome, out.DiscussionID, out.Position, out.LineRange, out.UntrustedBody = "lost", "", nil, nil, text
	if d.DiscussionID != nil {
		out.DiscussionID = *d.DiscussionID
	}
	out.Notes = []string{"GitLab answered the publish, but no comment of yours with the draft's text is on the merge request, " +
		"and the draft is gone: GitLab deleted it without saving a comment, as it does when the comment fails a check. " +
		"Its text is below; add_review_comment adds it again."}
	if b.ContinueOffset != nil {
		out.Notes = append(out.Notes, fmt.Sprintf("The text is cut at %d of its %d characters.", b.ShownChars, b.TotalChars))
	}
	return out
}

// settlePublish reads after a publish GitLab did not confirm: the draft
// still there is a publish not made; gone, the comment it became.
func (s *Service) settlePublish(ctx context.Context, p gapi.Project, iid int64, d *gitlab.DraftNote, start time.Time, sendErr error) error {
	var e *gapi.Error
	if !errors.As(sendErr, &e) || e.Class != gapi.ClassAmbiguousOutcome {
		return sendErr
	}
	unknown := gapi.Wrap(gapi.ClassAmbiguousOutcome, sendErr, "GitLab did not confirm whether draft %d was published, and reading to "+
		"find out failed too, so it is unknown: %s", d.ID, settledUnknown)
	_, err := s.client.GetDraftNote(ctx, p, iid, d.ID)
	switch {
	case err == nil && e.Status == http.StatusInternalServerError:
		return gapi.Wrap(gapi.ClassAmbiguousOutcome, sendErr, "GitLab failed on the publish with status 500, and a read shows draft "+
			"%d is still unpublished. GitLab answers so when it refuses to publish, most often because your role may no longer "+
			"comment on this merge request, such as when its discussion is locked. %s", d.ID, settledNotLanded)
	case err == nil:
		return gapi.Wrap(gapi.ClassAmbiguousOutcome, sendErr, "GitLab did not confirm the publish, and a read shows draft %d is "+
			"still unpublished. %s", d.ID, settledNotLanded)
	case !gapi.IsClass(err, gapi.ClassNotFound):
		return unknown
	}
	discussionID := ""
	if d.DiscussionID != nil {
		discussionID = *d.DiscussionID
	}
	note, thread, err := s.locateNote(ctx, p, iid, true, discussionID, d.Note, start)
	switch {
	case err != nil:
		return unknown
	case note != nil:
		return gapi.Wrap(gapi.ClassAmbiguousOutcome, sendErr, "GitLab did not confirm the publish, but a read shows it was published: "+
			"comment %d in thread %s. %s", note.ID, thread.ID, settledLanded)
	}
	return gapi.Wrap(gapi.ClassAmbiguousOutcome, sendErr, "GitLab did not confirm the publish, and draft %d is gone with no comment "+
		"of yours with its text on the merge request: GitLab may have dropped it without saving a comment, or it was deleted. "+
		"Do not publish it again; list_review_comments and list_discussions show what is there", d.ID)
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

// ResolveDiscussion resolves or reopens a thread of a merge request, or
// of an issue when typ is issue.
func (s *Service) ResolveDiscussion(ctx context.Context, raw, typ string, iid int64, discussionID string, resolve bool) (model.DiscussionWrite, error) {
	if typ != "" && typ != "issue" && typ != "merge_request" {
		return model.DiscussionWrite{}, gapi.Errf(gapi.ClassInvalid, "type is issue or merge_request")
	}
	t, err := s.writeTarget(ctx, raw)
	if err != nil {
		return model.DiscussionWrite{}, err
	}
	mr := typ != "issue"
	thread, err := s.thread(ctx, t.p, iid, mr, discussionID)
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
	var d *gitlab.Discussion
	if mr {
		d, err = s.client.ResolveMergeRequestDiscussion(ctx, t.p, iid, discussionID, resolve)
	} else {
		d, err = s.client.ResolveIssueDiscussion(ctx, t.p, iid, discussionID, resolve)
	}
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

// CommentEdit is update_comment's request.
type CommentEdit struct {
	Project   string
	Type      string // issue or merge_request
	IID       int64
	NoteID    int64
	Body      string
	UpdatedAt string
}

// UpdateComment replaces the text of one of the signed-in account's own
// comments, keeping it in its thread.
func (s *Service) UpdateComment(ctx context.Context, in CommentEdit) (model.CommentUpdate, error) {
	if strings.TrimSpace(in.Body) == "" {
		return model.CommentUpdate{}, gapi.Errf(gapi.ClassInvalid, "body is empty")
	}
	witness, err := parseWitness(in.UpdatedAt)
	if err != nil {
		return model.CommentUpdate{}, err
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.CommentUpdate{}, err
	}
	mr := in.Type == "merge_request"
	before, err := s.ownComment(ctx, t.p, mr, in.IID, in.NoteID, "edits")
	if err != nil {
		return model.CommentUpdate{}, err
	}
	out := model.CommentUpdate{Outcome: "unchanged", Write: model.Write{Target: t.ref}, Type: in.Type, IID: in.IID, NoteID: in.NoteID}
	// Before the witness: an edit that landed and is asked again, after a
	// lost answer, reads as done rather than stale.
	if sameText(before.Body, in.Body) {
		out.UpdatedAt = &before.UpdatedAt
		out.Notes = []string{"The comment already reads so; nothing was sent."}
		return out, nil
	}
	// GitLab holds no witness for an edit (§18 row 93), so the window
	// between this read and the PUT stays open; the check narrows it.
	if err := checkWitness(witness, before.UpdatedAt, "comment"); err != nil {
		return model.CommentUpdate{}, err
	}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun, out.WouldSend = "dry_run", true, preview("PUT", "update the comment", []string{"body"})
		return out, nil
	}
	var after *gitlab.Note
	if mr {
		after, err = s.client.UpdateMergeRequestNote(ctx, t.p, in.IID, in.NoteID, in.Body)
	} else {
		after, err = s.client.UpdateIssueNote(ctx, t.p, in.IID, in.NoteID, in.Body)
	}
	if err != nil {
		return model.CommentUpdate{}, err
	}
	switch {
	case after.ID != in.NoteID || after.UpdatedAt.IsZero() || strings.TrimSpace(after.Body) == "":
		return model.CommentUpdate{}, gapi.Errf(gapi.ClassUnexpected, "GitLab answered the edit without the comment; "+
			"list_discussions shows what it holds")
	case sameText(after.Body, in.Body):
	case after.UpdatedAt.Equal(before.UpdatedAt):
		return model.CommentUpdate{}, gapi.Errf(gapi.ClassUnexpected, "GitLab answered the edit, but the comment is unchanged")
	default:
		out.Notes = []string{"GitLab stored the text with differences from what was sent; list_discussions shows it."}
	}
	out.Outcome, out.UpdatedAt, out.BodyRemoved = "updated", &after.UpdatedAt, removedFrom(before.Body, after.Body)
	return out, nil
}

// ownComment reads a comment this server may change: not a note GitLab
// wrote to record an event, and not another person's. GitLab lets a
// maintainer change anyone's; another person's words are theirs.
func (s *Service) ownComment(ctx context.Context, p gapi.Project, mr bool, iid, noteID int64, verb string) (*gitlab.Note, error) {
	get := s.client.GetIssueNote
	if mr {
		get = s.client.GetMergeRequestNote
	}
	var note *gitlab.Note
	var me *gitlab.User
	if err := parallel(
		func() (err error) { note, err = get(ctx, p, iid, noteID); return err },
		func() (err error) { me, err = s.me(ctx); return err },
	); err != nil {
		return nil, err
	}
	switch {
	case note.System:
		return nil, gapi.Errf(gapi.ClassInvalid, "that is a note GitLab wrote to record an event, not a comment")
	case note.Author.Username != me.Username:
		return nil, gapi.Errf(gapi.ClassBlocked, "the comment is @%s's, and this server %s only your own; nothing was sent",
			note.Author.Username, verb)
	}
	return note, nil
}
