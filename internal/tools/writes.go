package tools

import (
	"context"

	"github.com/mmedum/gitlab-mcp/v2/internal/gapi"
	"github.com/mmedum/gitlab-mcp/v2/internal/model"
	"github.com/mmedum/gitlab-mcp/v2/internal/render"
	"github.com/mmedum/gitlab-mcp/v2/internal/service"
)

// The write tools of phase 2 (docs/architecture.md §7, §8). Every body
// input is declared in Guarded, so register routes it through the
// quick-action guard before the handler runs (§4.2); every one takes
// dry_run, which register turns into a context the client refuses to
// write under.

// visibleNote ends the description of every write others can see.
const visibleNote = " The result names the project's visibility: in a public project, what is written is public."

var sides = []string{"new", "old"}

// ------------------------------------------------------------- issues

type createIssueIn struct {
	Project        idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	Title          string   `json:"title" jsonschema:"The title"`
	Description    string   `json:"description,omitempty" jsonschema:"The description, in Markdown"`
	Labels         []string `json:"labels,omitempty" jsonschema:"Label names, exactly as they exist in the project or its groups; a name that does not exist is refused rather than created"`
	Assignees      []string `json:"assignees,omitempty" jsonschema:"Usernames to assign"`
	Milestone      string   `json:"milestone,omitempty" jsonschema:"A milestone's exact title, in the project or its groups"`
	DueDate        string   `json:"due_date,omitempty" jsonschema:"The due day, YYYY-MM-DD"`
	Confidential   bool     `json:"confidential,omitempty" jsonschema:"Make the issue visible only to project members with at least the Planner role and its author and assignees"`
	EscapeCommands bool     `json:"escape_commands,omitempty" jsonschema:"GitLab runs a line starting with a slash, such as /close or /merge, as a command. By default such a line refuses the call; true sends each one as plain text instead, with a leading backslash that renders the same. There is no way to run them"`
	DryRun         bool     `json:"dry_run,omitempty" jsonschema:"Return what would be sent, and what the quick-action guard would do to the text, without writing anything"`
}

func createIssue() definition {
	return tool[createIssueIn, model.IssueWrite]{
		sp: spec{Name: "create_issue", Kind: Write, Guarded: []string{"description"},
			Description: "Create an issue in a project. Labels, assignees and milestone are checked to exist first. A line in the " +
				"description that GitLab would run as a quick action (/close, /assign and the rest) refuses the call unless " +
				"escape_commands is true; set a field instead. Never repeated after a lost answer: if GitLab does not confirm, " +
				"the result says whether a read found the issue." + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in createIssueIn) (model.IssueWrite, error) {
			return svc.CreateIssue(ctx, service.IssueCreate{Project: string(in.Project), Title: in.Title, Description: in.Description,
				Labels: in.Labels, Assignees: in.Assignees, Milestone: in.Milestone, DueDate: in.DueDate, Confidential: in.Confidential})
		},
		text: render.IssueWrite,
	}
}

type updateIssueIn struct {
	Project         idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	IID             int64    `json:"iid" jsonschema:"The issue's number in its project: the number shown as #12 for issues and !12 for merge requests; not the global id"`
	UpdatedAt       string   `json:"updated_at" jsonschema:"The updated_at of your latest read of it, as get_issue or get_merge_request returned it. The write is refused [stale] if it changed since. A [stale] refusal is NOT a retry signal: read it again and decide whether the change still makes sense before passing the new value"`
	Title           *string  `json:"title,omitempty" jsonschema:"A new title"`
	Description     *string  `json:"description,omitempty" jsonschema:"A new description, replacing the old one whole; the result counts what it removed"`
	AddLabels       []string `json:"add_labels,omitempty" jsonschema:"Label names to add, exactly as they exist; other labels stay"`
	RemoveLabels    []string `json:"remove_labels,omitempty" jsonschema:"Label names to remove; other labels stay"`
	AddAssignees    []string `json:"add_assignees,omitempty" jsonschema:"Usernames to assign, beside those already assigned"`
	RemoveAssignees []string `json:"remove_assignees,omitempty" jsonschema:"Usernames to unassign"`
	Milestone       *string  `json:"milestone,omitempty" jsonschema:"A milestone's exact title, in the project or its groups"`
	ClearMilestone  bool     `json:"clear_milestone,omitempty" jsonschema:"Remove the milestone"`
	State           string   `json:"state,omitempty" jsonschema:"close or reopen"`
	DueDate         *string  `json:"due_date,omitempty" jsonschema:"A new due day, YYYY-MM-DD"`
	ClearDueDate    bool     `json:"clear_due_date,omitempty" jsonschema:"Remove the due date"`
	Confidential    *bool    `json:"confidential,omitempty" jsonschema:"true makes the issue confidential; false makes a confidential issue public, which needs GITLAB_MCP_ENABLE_SHIP=true"`
	EscapeCommands  bool     `json:"escape_commands,omitempty" jsonschema:"GitLab runs a line starting with a slash, such as /close or /merge, as a command. By default such a line refuses the call; true sends each one as plain text instead, with a leading backslash that renders the same. There is no way to run them"`
	DryRun          bool     `json:"dry_run,omitempty" jsonschema:"Return what would be sent, and what the quick-action guard would do to the text, without writing anything"`
}

func updateIssue() definition {
	return tool[updateIssueIn, model.IssueWrite]{
		sp: spec{Name: "update_issue", Asks: "before it makes a confidential issue public", Kind: Write, Guarded: []string{"description"}, Enums: map[string][]string{"state": {"close", "reopen"}},
			Description: "Change an issue. Only the fields given change; labels and assignees are added and removed, never " +
				"replaced by a list. updated_at from your latest read is required, and the call is refused [stale] if the issue " +
				"changed since. The result reports the labels before and after, the fields that changed as GitLab read them back, " +
				"and says so when nothing did. A quick-action line in the description refuses the call unless escape_commands " +
				"is true." + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in updateIssueIn) (model.IssueWrite, error) {
			return svc.UpdateIssue(ctx, service.IssueUpdate{Project: string(in.Project), IID: in.IID, UpdatedAt: in.UpdatedAt,
				Title: in.Title, Description: in.Description, AddLabels: in.AddLabels, RemoveLabels: in.RemoveLabels,
				AddAssignees: in.AddAssignees, RemoveAssignees: in.RemoveAssignees, Milestone: in.Milestone, ClearMilestone: in.ClearMilestone,
				State: in.State, DueDate: in.DueDate, ClearDueDate: in.ClearDueDate, Confidential: in.Confidential})
		},
		text: render.IssueWrite,
	}
}

// ------------------------------------------------------------ comments

type addCommentIn struct {
	Project      idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	Type         string   `json:"type" jsonschema:"issue or merge_request"`
	IID          int64    `json:"iid" jsonschema:"The number in its project: the number shown as #12 for issues and !12 for merge requests; not the global id"`
	Body         string   `json:"body" jsonschema:"The comment, in Markdown"`
	DiscussionID string   `json:"discussion_id,omitempty" jsonschema:"Reply in this thread, as list_discussions names it"`
	Thread       bool     `json:"thread,omitempty" jsonschema:"Start a new thread that can be resolved, rather than a standalone comment"`
	diffLineIn
	EscapeCommands bool `json:"escape_commands,omitempty" jsonschema:"GitLab runs a line starting with a slash, such as /close or /merge, as a command. By default such a line refuses the call; true sends each one as plain text instead, with a leading backslash that renders the same. There is no way to run them"`
	DryRun         bool `json:"dry_run,omitempty" jsonschema:"Return what would be sent, and what the quick-action guard would do to the text, without writing anything"`
}

// diffLineIn is a line of a merge request's diff, as add_comment and
// add_review_comment take it (§6.2).
type diffLineIn struct {
	File    string `json:"file,omitempty" jsonschema:"Comment on a line of this file of the merge request's diff, starting a thread there; the path as list_mr_files shows it"`
	Line    int    `json:"line,omitempty" jsonschema:"With file: the line number on side"`
	Side    string `json:"side,omitempty" jsonschema:"With file: new for a line as it is after the change (added or unchanged), old for a line as it was before (removed or unchanged)"`
	EndLine int    `json:"end_line,omitempty" jsonschema:"With file: the last line of a range starting at line, on the same side and in the same hunk"`
}

// location is the line asked for, nil when none was.
func (in diffLineIn) location() *service.DiffLocation {
	if in.File == "" && in.Line == 0 && in.Side == "" && in.EndLine == 0 {
		return nil
	}
	return &service.DiffLocation{File: in.File, Side: in.Side, Line: in.Line, EndLine: in.EndLine}
}

func addComment() definition {
	return tool[addCommentIn, model.CommentWrite]{
		sp: spec{Name: "add_comment", Kind: Write, Guarded: []string{"body"}, Bucket: gapi.BucketNotes,
			Enums: map[string][]string{"type": {"issue", "merge_request"}, "side": sides},
			Description: "Post a comment now, visible at once: on an issue or a merge request, as a reply in a thread " +
				"(discussion_id), as a new thread that can be resolved (thread), or as a new thread on a line of a merge " +
				"request's diff (file, line and side, with end_line for a range). The server computes where an inline comment lands from the diff and reports the position " +
				"GitLab stored; a line outside the diff is refused naming the nearest hunks. For a review others see only when " +
				"you submit it, use add_review_comment. A quick-action line in the body refuses the call unless " +
				"escape_commands is true. Never repeated after a lost answer." + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in addCommentIn) (model.CommentWrite, error) {
			return svc.AddComment(ctx, service.Comment{Project: string(in.Project), Type: in.Type, IID: in.IID, Body: in.Body,
				DiscussionID: in.DiscussionID, Location: in.location(), Thread: in.Thread})
		},
		text: render.CommentWrite,
	}
}

type updateCommentIn struct {
	Project        idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	Type           string   `json:"type" jsonschema:"issue or merge_request"`
	IID            int64    `json:"iid" jsonschema:"The number in its project: the number shown as #12 for issues and !12 for merge requests; not the global id"`
	NoteID         int64    `json:"note_id" jsonschema:"The comment's id, as list_discussions or add_comment gave it"`
	Body           string   `json:"body" jsonschema:"The comment's new text, in Markdown, replacing the old one whole; the result counts what it removed"`
	UpdatedAt      string   `json:"updated_at" jsonschema:"The comment's updated_at as list_discussions or the last update_comment returned it. The edit is refused [stale] if it changed since. A [stale] refusal is NOT a retry signal: read the comment again before deciding"`
	EscapeCommands bool     `json:"escape_commands,omitempty" jsonschema:"GitLab runs a line starting with a slash, such as /close or /merge, as a command, in an edit too. By default such a line refuses the call; true sends each one as plain text instead, with a leading backslash that renders the same. There is no way to run them"`
	DryRun         bool     `json:"dry_run,omitempty" jsonschema:"Check the comment and return what would be sent, and what the quick-action guard would do to the text, without writing anything"`
}

func updateComment() definition {
	return tool[updateCommentIn, model.CommentUpdate]{
		sp: spec{Name: "update_comment", Kind: Write, Idempotent: true, Guarded: []string{"body"},
			Enums: map[string][]string{"type": {"issue", "merge_request"}},
			Description: "Replace the text of one of your own comments on an issue or a merge request, keeping it where it " +
				"is: in its thread, with its replies, on its diff line. Another person's comment is refused [blocked], even " +
				"where GitLab would allow it. updated_at from your read is required, and the call is refused [stale] if the " +
				"comment changed since. The result counts what the old text lost and gives the new updated_at. Anyone the " +
				"new text newly mentions is notified. A quick-action line in the body refuses the call unless " +
				"escape_commands is true." + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in updateCommentIn) (model.CommentUpdate, error) {
			return svc.UpdateComment(ctx, service.CommentEdit{Project: string(in.Project), Type: in.Type, IID: in.IID, NoteID: in.NoteID,
				Body: in.Body, UpdatedAt: in.UpdatedAt})
		},
		text: render.CommentUpdate,
	}
}

type resolveDiscussionIn struct {
	Project      idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	Type         string   `json:"type,omitempty" jsonschema:"issue or merge_request, what iid names; default merge_request"`
	IID          int64    `json:"iid" jsonschema:"The number in its project: the number shown as #12 for issues and !12 for merge requests; not the global id"`
	DiscussionID string   `json:"discussion_id" jsonschema:"The thread, as list_discussions names it"`
	Reopen       bool     `json:"reopen,omitempty" jsonschema:"Mark the thread unresolved instead of resolved"`
	DryRun       bool     `json:"dry_run,omitempty" jsonschema:"Return what would be sent without writing anything"`
}

func resolveDiscussion() definition {
	return tool[resolveDiscussionIn, model.DiscussionWrite]{
		sp: spec{Name: "resolve_discussion", Kind: Write, Idempotent: true,
			Enums: map[string][]string{"type": {"issue", "merge_request"}},
			Description: "Resolve a thread on a merge request or an issue, or reopen it with reopen: true. The result gives the thread's " +
				"state as GitLab reported it, and says when it already was in that state." + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in resolveDiscussionIn) (model.DiscussionWrite, error) {
			return svc.ResolveDiscussion(ctx, string(in.Project), in.Type, in.IID, in.DiscussionID, !in.Reopen)
		},
		text: render.DiscussionWrite,
	}
}

// ------------------------------------------------------------- reviews

type addReviewCommentIn struct {
	Project       idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	IID           int64    `json:"iid" jsonschema:"The merge request's number in its project: the number shown as #12 for issues and !12 for merge requests; not the global id"`
	Body          string   `json:"body" jsonschema:"The review comment, in Markdown"`
	DiscussionID  string   `json:"discussion_id,omitempty" jsonschema:"Reply in this thread, as list_discussions names it"`
	ResolveThread bool     `json:"resolve_thread,omitempty" jsonschema:"With discussion_id: resolve the thread when the review is submitted"`
	diffLineIn
	EscapeCommands bool `json:"escape_commands,omitempty" jsonschema:"GitLab runs a line starting with a slash, such as /close or /merge, as a command. By default such a line refuses the call; true sends each one as plain text instead, with a leading backslash that renders the same. There is no way to run them"`
	DryRun         bool `json:"dry_run,omitempty" jsonschema:"Return what would be sent, and what the quick-action guard would do to the text, without writing anything"`
}

func addReviewComment() definition {
	return tool[addReviewCommentIn, model.CommentWrite]{
		sp: spec{Name: "add_review_comment", Kind: Write, Guarded: []string{"body"}, Enums: map[string][]string{"side": sides},
			Description: "Add a draft review comment to a merge request: only you see it until submit_review publishes all your " +
				"drafts at once. It can be on the merge request, a reply in a thread, or on a line of the diff (file, line, side, " +
				"optionally end_line); the server computes the position from the diff and reports it. list_review_comments lists " +
				"your drafts and delete_review_comment removes one. GitLab keeps one draft reply per person per thread, so a " +
				"second is refused [conflict]. A quick-action line in the body refuses the call unless " +
				"escape_commands is true, because GitLab runs it when the review is published."},
		run: func(ctx context.Context, svc *service.Service, in addReviewCommentIn) (model.CommentWrite, error) {
			return svc.AddReviewComment(ctx, service.ReviewComment{Project: string(in.Project), IID: in.IID, Body: in.Body,
				DiscussionID: in.DiscussionID, ResolveThread: in.ResolveThread, Location: in.location()})
		},
		text: render.CommentWrite,
	}
}

type deleteReviewCommentIn struct {
	Project idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	IID     int64    `json:"iid" jsonschema:"The merge request's number in its project: the number shown as #12 for issues and !12 for merge requests; not the global id"`
	DraftID int64    `json:"draft_id" jsonschema:"The draft's id, as list_review_comments or add_review_comment gave it"`
	DryRun  bool     `json:"dry_run,omitempty" jsonschema:"Return what would be sent without writing anything"`
}

func deleteReviewComment() definition {
	return tool[deleteReviewCommentIn, model.DraftDelete]{
		sp: spec{Name: "delete_review_comment", Kind: Write,
			Description: "Delete one of your own draft review comments before it is published. Nobody else has seen a draft, " +
				"so this removes nothing anyone read. Published comments are not deleted here."},
		run: func(ctx context.Context, svc *service.Service, in deleteReviewCommentIn) (model.DraftDelete, error) {
			return svc.DeleteReviewComment(ctx, string(in.Project), in.IID, in.DraftID)
		},
		text: render.DraftDelete,
	}
}

type updateReviewCommentIn struct {
	Project        idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	IID            int64    `json:"iid" jsonschema:"The merge request's number in its project: the number shown as #12 for issues and !12 for merge requests; not the global id"`
	DraftID        int64    `json:"draft_id" jsonschema:"The draft's id, as list_review_comments or add_review_comment gave it"`
	Body           string   `json:"body" jsonschema:"The draft's new text, in Markdown, replacing the old one whole; the result counts what it removed"`
	NoteSHA256     string   `json:"note_sha256" jsonschema:"The draft's note_sha256 as list_review_comments, add_review_comment or the last update_review_comment gave it. The edit is refused [stale] if the draft changed since. A [stale] refusal is NOT a retry signal: read the draft again before deciding"`
	EscapeCommands bool     `json:"escape_commands,omitempty" jsonschema:"GitLab runs a line starting with a slash, such as /close or /merge, as a command when the draft is published. By default such a line refuses the call; true sends each one as plain text instead, with a leading backslash that renders the same. There is no way to run them"`
	DryRun         bool     `json:"dry_run,omitempty" jsonschema:"Check the draft and return what would be sent, and what the quick-action guard would do to the text, without writing anything"`
}

func updateReviewComment() definition {
	return tool[updateReviewCommentIn, model.DraftUpdate]{
		sp: spec{Name: "update_review_comment", Kind: Write, Idempotent: true, OwnOnly: true, Guarded: []string{"body"},
			Description: "Replace the text of one of your own draft review comments, keeping it where it is: in its thread or on " +
				"its line of the diff. Only your drafts can be edited; nobody else sees them until they are published. " +
				"note_sha256 from your read is required, and the call is refused [stale] if the draft changed since. The " +
				"result counts what the old text lost and gives the new note_sha256. A quick-action line in the body refuses " +
				"the call unless escape_commands is true, because GitLab runs it when the draft is published."},
		run: func(ctx context.Context, svc *service.Service, in updateReviewCommentIn) (model.DraftUpdate, error) {
			return svc.UpdateReviewComment(ctx, service.DraftEdit{Project: string(in.Project), IID: in.IID, DraftID: in.DraftID,
				Body: in.Body, NoteSHA256: in.NoteSHA256})
		},
		text: render.DraftUpdate,
	}
}

type publishReviewCommentIn struct {
	Project idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	IID     int64    `json:"iid" jsonschema:"The merge request's number in its project: the number shown as #12 for issues and !12 for merge requests; not the global id"`
	DraftID int64    `json:"draft_id" jsonschema:"The draft's id, as list_review_comments or add_review_comment gave it"`
	DryRun  bool     `json:"dry_run,omitempty" jsonschema:"Check the draft and return what would be sent without writing anything"`
}

func publishReviewComment() definition {
	return tool[publishReviewCommentIn, model.DraftPublish]{
		sp: spec{Name: "publish_review_comment", Kind: Write, Bucket: gapi.BucketNotes,
			Description: "Publish one of your own draft review comments now, on its own, as the thread or reply it was drafted " +
				"as; submit_review publishes all of them at once. A reply drafted to resolve its thread resolves it when your " +
				"role may, and any other reply reopens a resolved thread, as GitLab does. A draft with a quick-action line is refused. GitLab " +
				"does not say what it created, so the server reads the threads afterwards and reports the comment; if GitLab " +
				"deleted the draft without saving it, the outcome is lost and the result gives the text back. Never repeated " +
				"after a lost answer." + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in publishReviewCommentIn) (model.DraftPublish, error) {
			return svc.PublishReviewComment(ctx, string(in.Project), in.IID, in.DraftID)
		},
		text: render.DraftPublish,
	}
}

type submitReviewIn struct {
	Project        idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	IID            int64    `json:"iid" jsonschema:"The merge request's number in its project: the number shown as #12 for issues and !12 for merge requests; not the global id"`
	Summary        string   `json:"summary,omitempty" jsonschema:"A summary comment published with the review, in Markdown"`
	ReviewerState  string   `json:"reviewer_state,omitempty" jsonschema:"reviewed, requested_changes, or approved; approved is an approval and needs GITLAB_MCP_ENABLE_SHIP=true"`
	EscapeCommands bool     `json:"escape_commands,omitempty" jsonschema:"GitLab runs a line starting with a slash, such as /close or /merge, as a command. By default such a line refuses the call; true sends each one as plain text instead, with a leading backslash that renders the same. There is no way to run them"`
	DryRun         bool     `json:"dry_run,omitempty" jsonschema:"Return what would be sent, and what the quick-action guard would do to the text, without writing anything"`
}

func submitReview() definition {
	return tool[submitReviewIn, model.ReviewSubmit]{
		sp: spec{Name: "submit_review", Kind: Write, Guarded: []string{"summary"}, Bucket: gapi.BucketNotes,
			Enums: map[string][]string{"reviewer_state": service.ReviewerStates},
			Description: "Publish all your draft review comments on a merge request at once, with an optional summary and " +
				"reviewer state. reviewer_state approved approves the merge request, which other people's merge rules count, " +
				"so it is refused unless the server was started with GITLAB_MCP_ENABLE_SHIP=true. The result counts what was " +
				"published and what is still pending, read afterwards. A quick-action line in the summary refuses the call " +
				"unless escape_commands is true, and one in any of your drafts refuses it too, since GitLab runs it on " +
				"publishing." + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in submitReviewIn) (model.ReviewSubmit, error) {
			return svc.SubmitReview(ctx, service.Review{Project: string(in.Project), IID: in.IID, Summary: in.Summary,
				ReviewerState: in.ReviewerState})
		},
		text: render.ReviewSubmit,
	}
}

// ------------------------------------------------------ merge requests

type createMergeRequestIn struct {
	Project            idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	SourceBranch       string   `json:"source_branch" jsonschema:"The branch with the changes"`
	TargetBranch       string   `json:"target_branch,omitempty" jsonschema:"The branch to merge into; the project's default branch when omitted"`
	Title              string   `json:"title" jsonschema:"The title"`
	Description        string   `json:"description,omitempty" jsonschema:"The description, in Markdown"`
	Labels             []string `json:"labels,omitempty" jsonschema:"Label names, exactly as they exist in the project or its groups; a name that does not exist is refused rather than created"`
	Assignees          []string `json:"assignees,omitempty" jsonschema:"Usernames to assign"`
	Reviewers          []string `json:"reviewers,omitempty" jsonschema:"Usernames to request a review from"`
	Milestone          string   `json:"milestone,omitempty" jsonschema:"A milestone's exact title, in the project or its groups"`
	Draft              bool     `json:"draft,omitempty" jsonschema:"Open it as a draft, which cannot be merged until marked ready"`
	RemoveSourceBranch bool     `json:"remove_source_branch,omitempty" jsonschema:"Delete the source branch when it is merged"`
	Squash             bool     `json:"squash,omitempty" jsonschema:"Squash the commits when it is merged"`
	EscapeCommands     bool     `json:"escape_commands,omitempty" jsonschema:"GitLab runs a line starting with a slash, such as /close or /merge, as a command. By default such a line refuses the call; true sends each one as plain text instead, with a leading backslash that renders the same. There is no way to run them"`
	DryRun             bool     `json:"dry_run,omitempty" jsonschema:"Return what would be sent, and what the quick-action guard would do to the text, without writing anything"`
}

func createMergeRequest() definition {
	return tool[createMergeRequestIn, model.MergeRequestWrite]{
		sp: spec{Name: "create_merge_request", Kind: Write, Guarded: []string{"description"},
			Description: "Open a merge request from a branch, into the default branch unless target_branch says otherwise. " +
				"GitLab refuses a second open merge request for the same branches [conflict]. Labels, assignees, reviewers " +
				"and milestone are checked to exist first. A quick-action line in the description refuses the call unless " +
				"escape_commands is true. Merging is not done here. Never repeated after a lost answer." + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in createMergeRequestIn) (model.MergeRequestWrite, error) {
			return svc.CreateMergeRequest(ctx, service.MergeRequestCreate{Project: string(in.Project), SourceBranch: in.SourceBranch,
				TargetBranch: in.TargetBranch, Title: in.Title, Description: in.Description, Labels: in.Labels, Assignees: in.Assignees,
				Reviewers: in.Reviewers, Milestone: in.Milestone, Draft: in.Draft, RemoveSourceBranch: in.RemoveSourceBranch, Squash: in.Squash})
		},
		text: render.MergeRequestWrite,
	}
}

type updateMergeRequestIn struct {
	Project            idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	IID                int64    `json:"iid" jsonschema:"The merge request's number in its project: the number shown as #12 for issues and !12 for merge requests; not the global id"`
	UpdatedAt          string   `json:"updated_at" jsonschema:"The updated_at of your latest read of it, as get_issue or get_merge_request returned it. The write is refused [stale] if it changed since. A [stale] refusal is NOT a retry signal: read it again and decide whether the change still makes sense before passing the new value"`
	Title              *string  `json:"title,omitempty" jsonschema:"A new title"`
	Description        *string  `json:"description,omitempty" jsonschema:"A new description, replacing the old one whole; the result counts what it removed"`
	AddLabels          []string `json:"add_labels,omitempty" jsonschema:"Label names to add, exactly as they exist; other labels stay"`
	RemoveLabels       []string `json:"remove_labels,omitempty" jsonschema:"Label names to remove; other labels stay"`
	AddAssignees       []string `json:"add_assignees,omitempty" jsonschema:"Usernames to assign, beside those already assigned"`
	RemoveAssignees    []string `json:"remove_assignees,omitempty" jsonschema:"Usernames to unassign"`
	AddReviewers       []string `json:"add_reviewers,omitempty" jsonschema:"Usernames to request a review from, beside those already asked"`
	RemoveReviewers    []string `json:"remove_reviewers,omitempty" jsonschema:"Usernames to remove as reviewers"`
	Milestone          *string  `json:"milestone,omitempty" jsonschema:"A milestone's exact title, in the project or its groups"`
	ClearMilestone     bool     `json:"clear_milestone,omitempty" jsonschema:"Remove the milestone"`
	State              string   `json:"state,omitempty" jsonschema:"close or reopen"`
	TargetBranch       *string  `json:"target_branch,omitempty" jsonschema:"A new branch to merge into"`
	Draft              *bool    `json:"draft,omitempty" jsonschema:"true marks it a draft, false marks it ready; GitLab keeps this as the title's Draft: prefix"`
	RemoveSourceBranch *bool    `json:"remove_source_branch,omitempty" jsonschema:"Delete the source branch when it is merged, or not"`
	Squash             *bool    `json:"squash,omitempty" jsonschema:"Squash the commits when it is merged, or not"`
	EscapeCommands     bool     `json:"escape_commands,omitempty" jsonschema:"GitLab runs a line starting with a slash, such as /close or /merge, as a command. By default such a line refuses the call; true sends each one as plain text instead, with a leading backslash that renders the same. There is no way to run them"`
	DryRun             bool     `json:"dry_run,omitempty" jsonschema:"Return what would be sent, and what the quick-action guard would do to the text, without writing anything"`
}

func updateMergeRequest() definition {
	return tool[updateMergeRequestIn, model.MergeRequestWrite]{
		sp: spec{Name: "update_merge_request", Kind: Write, Guarded: []string{"description"}, Enums: map[string][]string{"state": {"close", "reopen"}},
			Description: "Change a merge request. Only the fields given change; labels, assignees and reviewers are added and " +
				"removed, never replaced by a list. updated_at from your latest read is required, and the call is refused " +
				"[stale] if the merge request changed since. The result reports the fields that changed as GitLab read them " +
				"back, and says so when nothing did. Merging and approving are not done here. A quick-action line in the " +
				"description refuses the call unless escape_commands is true." + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in updateMergeRequestIn) (model.MergeRequestWrite, error) {
			return svc.UpdateMergeRequest(ctx, service.MergeRequestUpdate{Project: string(in.Project), IID: in.IID, UpdatedAt: in.UpdatedAt,
				Title: in.Title, Description: in.Description, AddLabels: in.AddLabels, RemoveLabels: in.RemoveLabels,
				AddAssignees: in.AddAssignees, RemoveAssignees: in.RemoveAssignees, AddReviewers: in.AddReviewers,
				RemoveReviewers: in.RemoveReviewers, Milestone: in.Milestone, ClearMilestone: in.ClearMilestone, State: in.State,
				TargetBranch: in.TargetBranch, Draft: in.Draft, RemoveSourceBranch: in.RemoveSourceBranch, Squash: in.Squash})
		},
		text: render.MergeRequestWrite,
	}
}

// ------------------------------------------------------- time tracking

type trackTimeIn struct {
	Project        idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	Type           string   `json:"type" jsonschema:"issue or merge_request"`
	IID            int64    `json:"iid" jsonschema:"The number in its project: the number shown as #12 for issues and !12 for merge requests; not the global id"`
	UpdatedAt      string   `json:"updated_at" jsonschema:"The updated_at of your latest read of it, as get_issue or get_merge_request returned it. The write is refused [stale] if it changed since. A [stale] refusal is NOT a retry signal: read it again and decide whether the change still makes sense before passing the new value"`
	Estimate       string   `json:"estimate,omitempty" jsonschema:"Set the estimate to this duration, such as 3h30m, 1w 2d or 1.5 (hours). Units mo, w, d, h and m; 1mo is 4w, 1w is 5d, 1d is 8h"`
	ResetEstimate  bool     `json:"reset_estimate,omitempty" jsonschema:"Remove the estimate"`
	AddSpent       string   `json:"add_spent,omitempty" jsonschema:"Add this duration to the time spent, in the same form as estimate; a leading minus, such as -30m, subtracts, down to zero at most"`
	ResetSpent     bool     `json:"reset_spent,omitempty" jsonschema:"Set the time spent back to zero"`
	TotalTimeSpent *int64   `json:"total_time_spent,omitempty" jsonschema:"Required with add_spent or reset_spent: the seconds spent as your latest read gave them, time_stats.total_time_spent of get_issue or get_merge_request or total_time_spent of track_time. GitLab does not move updated_at when spent time is added or reset, so this is the witness that catches the same change sent twice: the call is refused [stale] if the total moved. A [stale] refusal is NOT a retry signal: read it again and decide whether the time still needs adding"`
	DryRun         bool     `json:"dry_run,omitempty" jsonschema:"Return what would be sent without writing anything"`
}

func trackTime() definition {
	return tool[trackTimeIn, model.TimeWrite]{
		sp: spec{Name: "track_time", Kind: Write, Enums: map[string][]string{"type": {"issue", "merge_request"}},
			Description: "Set or reset the time estimate of an issue or a merge request, and add or reset its time spent: " +
				"estimate or reset_estimate, add_spent or reset_spent, at least one. updated_at from your latest read is " +
				"required, and the call is refused [stale] if the item changed since; adding or resetting spent time also needs " +
				"total_time_spent from that read, since GitLab does not move updated_at when spent time is added or reset. Adding or resetting spent time is never repeated after a " +
				"lost answer; the result then says what a read of the total shows. The result gives the time stats before " +
				"and after, as read back, and says so when nothing changed. GitLab records each change as a note on the item." +
				visibleNote},
		run: func(ctx context.Context, svc *service.Service, in trackTimeIn) (model.TimeWrite, error) {
			return svc.TrackTime(ctx, service.TimeTracking{Project: string(in.Project), Type: in.Type, IID: in.IID,
				UpdatedAt: in.UpdatedAt, Estimate: in.Estimate, ResetEstimate: in.ResetEstimate, AddSpent: in.AddSpent,
				ResetSpent: in.ResetSpent, TotalTimeSpent: in.TotalTimeSpent})
		},
		text: render.TimeWrite,
	}
}

// ---------------------------------------------------------- repository

type createBranchIn struct {
	Project idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	Branch  string   `json:"branch" jsonschema:"The new branch's name"`
	Ref     string   `json:"ref,omitempty" jsonschema:"The branch, tag or commit SHA to start it from; the default branch when omitted"`
	DryRun  bool     `json:"dry_run,omitempty" jsonschema:"Return what would be sent without writing anything"`
}

func createBranch() definition {
	return tool[createBranchIn, model.BranchWrite]{
		sp: spec{Name: "create_branch", Kind: Write,
			Description: "Create a branch from a ref, the default branch unless ref says otherwise. A name that exists is " +
				"refused [conflict], and a name a protected-branch rule covers is refused [blocked]: code reaches a protected " +
				"branch only through a merge request. The result names the commit it points at." + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in createBranchIn) (model.BranchWrite, error) {
			return svc.CreateBranch(ctx, string(in.Project), in.Branch, in.Ref)
		},
		text: render.BranchWrite,
	}
}

type commitActionIn struct {
	Action       string `json:"action" jsonschema:"create, update, delete or move"`
	FilePath     string `json:"file_path" jsonschema:"The file's path from the repository root"`
	PreviousPath string `json:"previous_path,omitempty" jsonschema:"For move: the file's path before the move"`
	Content      string `json:"content,omitempty" jsonschema:"For create and update: the file's whole new content. For move: new content, or leave it out to keep the file's own"`
	Encoding     string `json:"encoding,omitempty" jsonschema:"text (default) or base64, for binary content"`
	LastCommitID string `json:"last_commit_id,omitempty" jsonschema:"Required for update, delete and move: the file's last_commit_id as get_file returned it. GitLab refuses the commit [stale] if the file changed since. A [stale] refusal is NOT a retry signal: read the file again and redo the change on the new content"`
}

type createCommitIn struct {
	Project     idOrPath         `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	Branch      string           `json:"branch" jsonschema:"The branch to commit to; never the default branch or a protected one"`
	StartBranch string           `json:"start_branch,omitempty" jsonschema:"When branch does not exist yet: the branch to create it from"`
	Message     string           `json:"message" jsonschema:"The commit message"`
	Actions     []commitActionIn `json:"actions" jsonschema:"The file changes, applied together or not at all; at most 100"`
	DryRun      bool             `json:"dry_run,omitempty" jsonschema:"Return what would be sent without writing anything"`
}

func createCommit() definition {
	return tool[createCommitIn, model.CommitWrite]{
		sp: spec{Name: "create_commit", Kind: Write,
			Description: "Commit file changes to a branch in one commit: create, update, delete or move files. It refuses the " +
				"default branch and every protected branch [blocked]: code reaches those only through a merge request, so " +
				"commit to a new branch (start_branch names where it starts) and open one with create_merge_request. Update, " +
				"delete and move need each file's last_commit_id from get_file, so a change made since is never overwritten. " +
				"The result names the commit and the branch head read afterwards. Never repeated after a lost answer." + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in createCommitIn) (model.CommitWrite, error) {
			actions := make([]service.CommitAction, 0, len(in.Actions))
			for _, a := range in.Actions {
				actions = append(actions, service.CommitAction{Action: a.Action, FilePath: a.FilePath, PreviousPath: a.PreviousPath,
					Content: a.Content, Encoding: a.Encoding, LastCommitID: a.LastCommitID})
			}
			return svc.CreateCommit(ctx, service.CommitCreate{Project: string(in.Project), Branch: in.Branch, StartBranch: in.StartBranch,
				Message: in.Message, Actions: actions})
		},
		text: render.CommitWrite,
	}
}

// --------------------------------------------------------------- todos

type markTodosDoneIn struct {
	IDs    []int64 `json:"ids" jsonschema:"To-do item ids, as list_todos gave them; at most 100"`
	DryRun bool    `json:"dry_run,omitempty" jsonschema:"Return what would be sent without writing anything"`
}

func markTodosDone() definition {
	return tool[markTodosDoneIn, model.TodosDone]{
		sp: spec{Name: "mark_todos_done", Kind: Write, Idempotent: true, OwnOnly: true,
			Description: "Mark your own to-do items done, by id, at most 100 at a time. Each id gets its own outcome: done, or " +
				"not_found for one that is not yours or does not exist, or blocked for one whose project GITLAB_MCP_WRITE_NAMESPACES " +
				"leaves out."},
		run: func(ctx context.Context, svc *service.Service, in markTodosDoneIn) (model.TodosDone, error) {
			return svc.MarkTodosDone(ctx, in.IDs)
		},
		text: render.TodosDone,
	}
}

type addTodoIn struct {
	Project idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	Type    string   `json:"type" jsonschema:"issue or merge_request"`
	IID     int64    `json:"iid" jsonschema:"The number in its project: the number shown as #12 for issues and !12 for merge requests; not the global id"`
	DryRun  bool     `json:"dry_run,omitempty" jsonschema:"Return what would be sent without writing anything"`
}

func addTodo() definition {
	return tool[addTodoIn, model.TodoWrite]{
		sp: spec{Name: "add_todo", Kind: Write, OwnOnly: true, Enums: map[string][]string{"type": {"issue", "merge_request"}},
			Description: "Add a to-do item for yourself on an issue or a merge request. The result gives its id, which " +
				"mark_todos_done takes. While a pending to-do you added is there, GitLab adds none and the result is unchanged, " +
				"with that one's id; other pending items, such as a mention, do not count. GitLab marks your pending to-dos on " +
				"the item done when you comment on it outside a thread, close or merge it, or submit a review of it. Never " +
				"repeated after a lost answer: the result then says whether a read found the to-do. Only you see your to-do list."},
		run: func(ctx context.Context, svc *service.Service, in addTodoIn) (model.TodoWrite, error) {
			return svc.AddTodo(ctx, string(in.Project), in.Type, in.IID)
		},
		text: render.TodoWrite,
	}
}

// -------------------------------------------------------- subscriptions

type subscribeIn struct {
	Project     idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	Type        string   `json:"type" jsonschema:"issue or merge_request"`
	IID         int64    `json:"iid" jsonschema:"The number in its project: the number shown as #12 for issues and !12 for merge requests; not the global id"`
	Unsubscribe bool     `json:"unsubscribe,omitempty" jsonschema:"Unsubscribe instead of subscribing"`
	DryRun      bool     `json:"dry_run,omitempty" jsonschema:"Return what would be sent, and whether you are subscribed now, without writing anything"`
}

func subscribe() definition {
	return tool[subscribeIn, model.SubscriptionWrite]{
		sp: spec{Name: "subscribe", Kind: Write, Idempotent: true, OwnOnly: true, Enums: map[string][]string{"type": {"issue", "merge_request"}},
			Description: "Subscribe yourself to an issue's or a merge request's notifications, or unsubscribe with unsubscribe: " +
				"true. You are already subscribed, until you unsubscribe, to what you created, are assigned to or asked to review, " +
				"commented on, reacted to, changed in a way GitLab notes, or are @-mentioned in. " +
				"The result says whether you are subscribed, and says unchanged when you already were so. On a merge request " +
				"GitLab allows it only to a Developer or higher, or its author or an assignee. Only you see your subscriptions."},
		run: func(ctx context.Context, svc *service.Service, in subscribeIn) (model.SubscriptionWrite, error) {
			return svc.Subscribe(ctx, string(in.Project), in.Type, in.IID, !in.Unsubscribe)
		},
		text: render.SubscriptionWrite,
	}
}

// ------------------------------------------------------------ reactions

type reactIn struct {
	Project idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	Type    string   `json:"type" jsonschema:"issue or merge_request"`
	IID     int64    `json:"iid" jsonschema:"The number in its project: the number shown as #12 for issues and !12 for merge requests; not the global id"`
	NoteID  int64    `json:"note_id,omitempty" jsonschema:"React on this comment, as list_discussions gave its id, rather than on the issue or merge request itself"`
	Emoji   string   `json:"emoji" jsonschema:"The emoji's name, such as thumbsup, thumbsdown, tada, heart, eyes or rocket; +1 and -1 are thumbsup and thumbsdown. A custom emoji works only in a project in a group that defines it"`
	Remove  bool     `json:"remove,omitempty" jsonschema:"Remove your reaction with this emoji instead of adding it"`
	DryRun  bool     `json:"dry_run,omitempty" jsonschema:"Return what would be sent, and whether you reacted with it now, without writing anything"`
}

func react() definition {
	return tool[reactIn, model.ReactionWrite]{
		sp: spec{Name: "react", Kind: Write, Idempotent: true, Enums: map[string][]string{"type": {"issue", "merge_request"}},
			Description: "React with an emoji on an issue, a merge request or a comment on one (note_id), or remove your reaction " +
				"with remove: true. Only your own reactions change. The result says unchanged when you already reacted with it, " +
				"or had no such reaction to remove. GitLab refuses an emoji it does not know, a comment it wrote itself and an " +
				"item you may not react to alike, as not found. A reaction on a comment moves the comment's updated_at, which " +
				"update_comment and delete_comment compare; one on an issue or a merge request does not." + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in reactIn) (model.ReactionWrite, error) {
			return svc.React(ctx, service.Reaction{Project: string(in.Project), Type: in.Type, IID: in.IID, NoteID: in.NoteID,
				Emoji: in.Emoji, Remove: in.Remove})
		},
		text: render.ReactionWrite,
	}
}
