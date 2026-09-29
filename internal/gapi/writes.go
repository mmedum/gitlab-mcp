package gapi

import (
	"context"

	"github.com/mmedum/gitlab-mcp/v2/internal/diffpos"
	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
)

// The writes of phase 2: issues, comments, reviews, merge requests,
// branches, commits and to-do items (docs/architecture.md §7).
//
// Every body is sent as given: the quick-action guard ran before the
// service called here (§4.2), and a request struct's omitted fields are
// left out of the JSON, because GitLab's PUT changes only what it is
// sent (§4.6). None of these is declared Repeatable unless applying it
// twice is the same as once; a create never is (§4.5).

// IssueCreate is POST /projects/:id/issues.
type IssueCreate struct {
	Title        string   `json:"title"`
	Description  string   `json:"description,omitempty"`
	Labels       []string `json:"labels,omitempty"`
	AssigneeIDs  []int64  `json:"assignee_ids,omitempty"`
	MilestoneID  int64    `json:"milestone_id,omitempty"`
	DueDate      string   `json:"due_date,omitempty"`
	Confidential bool     `json:"confidential,omitempty"`
}

// CreateIssue creates an issue.
func (c *Client) CreateIssue(ctx context.Context, p Project, in IssueCreate) (*gitlab.Issue, error) {
	var out gitlab.Issue
	err := c.Do(ctx, Call{Method: "POST", Path: "projects/{}/issues", Args: []string{p.segment()}, Body: in,
		Name: "create_issue"}, &out)
	return &out, err
}

// IssueUpdate is PUT /projects/:id/issues/:iid. A nil field is not sent.
type IssueUpdate struct {
	Title        *string  `json:"title,omitempty"`
	Description  *string  `json:"description,omitempty"`
	AddLabels    []string `json:"add_labels,omitempty"`
	RemoveLabels []string `json:"remove_labels,omitempty"`
	// AssigneeIDs is the whole set, computed from a fresh read: GitLab
	// takes no add or remove for assignees.
	AssigneeIDs *[]int64 `json:"assignee_ids,omitempty"`
	// MilestoneID 0 clears the milestone.
	MilestoneID  *int64  `json:"milestone_id,omitempty"`
	StateEvent   string  `json:"state_event,omitempty"`
	DueDate      *string `json:"due_date,omitempty"`
	Confidential *bool   `json:"confidential,omitempty"`
}

// UpdateIssue changes the fields given.
func (c *Client) UpdateIssue(ctx context.Context, p Project, iid int64, in IssueUpdate) (*gitlab.Issue, error) {
	var out gitlab.Issue
	err := c.Do(ctx, Call{Method: "PUT", Path: "projects/{}/issues/{}", Args: []string{p.segment(), idArg(iid)}, Body: in,
		Name: "update_issue"}, &out)
	return &out, err
}

// noteBody is a note's request.
type noteBody struct {
	Body string `json:"body"`
}

// CreateIssueNote comments on an issue.
func (c *Client) CreateIssueNote(ctx context.Context, p Project, iid int64, body string) (*gitlab.Note, error) {
	return c.createNote(ctx, Call{Method: "POST", Path: "projects/{}/issues/{}/notes", Args: []string{p.segment(), idArg(iid)},
		Body: noteBody{body}, Bucket: BucketNotes, Name: "add_comment"})
}

// CreateMergeRequestNote comments on a merge request.
func (c *Client) CreateMergeRequestNote(ctx context.Context, p Project, iid int64, body string) (*gitlab.Note, error) {
	return c.createNote(ctx, Call{Method: "POST", Path: "projects/{}/merge_requests/{}/notes", Args: []string{p.segment(), idArg(iid)},
		Body: noteBody{body}, Bucket: BucketNotes, Name: "add_comment"})
}

// UpdateIssueNote replaces the body of a comment on an issue. GitLab
// holds no witness for an edit, so the caller reads first (§4.6).
func (c *Client) UpdateIssueNote(ctx context.Context, p Project, iid, note int64, body string) (*gitlab.Note, error) {
	var out gitlab.Note
	err := c.Do(ctx, Call{Method: "PUT", Path: "projects/{}/issues/{}/notes/{}", Args: []string{p.segment(), idArg(iid), idArg(note)},
		Body: noteBody{body}, Name: "update_comment"}, &out)
	return &out, err
}

// UpdateMergeRequestNote replaces the body of a comment on a merge
// request, one on a diff line included. GitLab holds no witness for an
// edit, so the caller reads first (§4.6).
func (c *Client) UpdateMergeRequestNote(ctx context.Context, p Project, iid, note int64, body string) (*gitlab.Note, error) {
	var out gitlab.Note
	err := c.Do(ctx, Call{Method: "PUT", Path: "projects/{}/merge_requests/{}/notes/{}", Args: []string{p.segment(), idArg(iid), idArg(note)},
		Body: noteBody{body}, Name: "update_comment"}, &out)
	return &out, err
}

// ReplyToIssueDiscussion adds a note to an issue's thread.
func (c *Client) ReplyToIssueDiscussion(ctx context.Context, p Project, iid int64, discussion, body string) (*gitlab.Note, error) {
	return c.createNote(ctx, Call{Method: "POST", Path: "projects/{}/issues/{}/discussions/{}/notes",
		Args: []string{p.segment(), idArg(iid), discussion}, Body: noteBody{body}, Bucket: BucketNotes, Name: "add_comment"})
}

// ReplyToMergeRequestDiscussion adds a note to a merge request's thread.
func (c *Client) ReplyToMergeRequestDiscussion(ctx context.Context, p Project, iid int64, discussion, body string) (*gitlab.Note, error) {
	return c.createNote(ctx, Call{Method: "POST", Path: "projects/{}/merge_requests/{}/discussions/{}/notes",
		Args: []string{p.segment(), idArg(iid), discussion}, Body: noteBody{body}, Bucket: BucketNotes, Name: "add_comment"})
}

// createNote sends a note. A 202, GitLab running the body as quick
// actions, is refused for every notes call in Client.send.
func (c *Client) createNote(ctx context.Context, call Call) (*gitlab.Note, error) {
	var out gitlab.Note
	if err := c.Do(ctx, call, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// discussionBody starts a thread, on a diff line when Position is set.
type discussionBody struct {
	Body     string            `json:"body"`
	Position *diffpos.Position `json:"position,omitempty"`
}

// CreateMergeRequestDiscussion starts a thread on a merge request, on a
// line of its diff when pos is not nil.
func (c *Client) CreateMergeRequestDiscussion(ctx context.Context, p Project, iid int64, body string, pos *diffpos.Position) (*gitlab.Discussion, error) {
	var out gitlab.Discussion
	err := c.Do(ctx, Call{Method: "POST", Path: "projects/{}/merge_requests/{}/discussions", Args: []string{p.segment(), idArg(iid)},
		Body: discussionBody{Body: body, Position: pos}, Bucket: BucketNotes, Name: "add_comment"}, &out)
	return &out, err
}

// CreateIssueDiscussion starts a resolvable thread on an issue.
func (c *Client) CreateIssueDiscussion(ctx context.Context, p Project, iid int64, body string) (*gitlab.Discussion, error) {
	var out gitlab.Discussion
	err := c.Do(ctx, Call{Method: "POST", Path: "projects/{}/issues/{}/discussions", Args: []string{p.segment(), idArg(iid)},
		Body: noteBody{body}, Bucket: BucketNotes, Name: "add_comment"}, &out)
	return &out, err
}

// GetIssueDiscussion reads one thread of an issue.
func (c *Client) GetIssueDiscussion(ctx context.Context, p Project, iid int64, discussion string) (*gitlab.Discussion, error) {
	var out gitlab.Discussion
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/issues/{}/discussions/{}",
		Args: []string{p.segment(), idArg(iid), discussion}, Name: "get_discussion"}, &out)
	return &out, err
}

// GetMergeRequestDiscussion reads one thread of a merge request.
func (c *Client) GetMergeRequestDiscussion(ctx context.Context, p Project, iid int64, discussion string) (*gitlab.Discussion, error) {
	var out gitlab.Discussion
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/merge_requests/{}/discussions/{}",
		Args: []string{p.segment(), idArg(iid), discussion}, Name: "get_discussion"}, &out)
	return &out, err
}

// resolveBody is a thread's resolved state.
type resolveBody struct {
	Resolved bool `json:"resolved"`
}

// ResolveMergeRequestDiscussion resolves or reopens a thread. Setting a
// state twice leaves it set, which PUT already says.
func (c *Client) ResolveMergeRequestDiscussion(ctx context.Context, p Project, iid int64, discussion string, resolved bool) (*gitlab.Discussion, error) {
	var out gitlab.Discussion
	err := c.Do(ctx, Call{Method: "PUT", Path: "projects/{}/merge_requests/{}/discussions/{}",
		Args: []string{p.segment(), idArg(iid), discussion}, Body: resolveBody{Resolved: resolved},
		Name: "resolve_discussion"}, &out)
	return &out, err
}

// ResolveIssueDiscussion resolves or reopens an issue's thread.
func (c *Client) ResolveIssueDiscussion(ctx context.Context, p Project, iid int64, discussion string, resolved bool) (*gitlab.Discussion, error) {
	var out gitlab.Discussion
	err := c.Do(ctx, Call{Method: "PUT", Path: "projects/{}/issues/{}/discussions/{}",
		Args: []string{p.segment(), idArg(iid), discussion}, Body: resolveBody{Resolved: resolved},
		Name: "resolve_discussion"}, &out)
	return &out, err
}

// DraftCreate is POST …/draft_notes.
type DraftCreate struct {
	Note                  string            `json:"note"`
	Position              *diffpos.Position `json:"position,omitempty"`
	InReplyToDiscussionID string            `json:"in_reply_to_discussion_id,omitempty"`
	ResolveDiscussion     bool              `json:"resolve_discussion,omitempty"`
}

// CreateDraftNote adds an unpublished review comment.
func (c *Client) CreateDraftNote(ctx context.Context, p Project, iid int64, in DraftCreate) (*gitlab.DraftNote, error) {
	var out gitlab.DraftNote
	err := c.Do(ctx, Call{Method: "POST", Path: "projects/{}/merge_requests/{}/draft_notes", Args: []string{p.segment(), idArg(iid)},
		Body: in, Name: "add_review_comment"}, &out)
	return &out, err
}

// DeleteDraftNote deletes one of the signed-in account's drafts.
func (c *Client) DeleteDraftNote(ctx context.Context, p Project, iid, draft int64) error {
	return c.Do(ctx, Call{Method: "DELETE", Path: "projects/{}/merge_requests/{}/draft_notes/{}",
		Args: []string{p.segment(), idArg(iid), idArg(draft)}, Name: "delete_review_comment"}, nil)
}

// publishBody is bulk_publish's request.
type publishBody struct {
	Note          string `json:"note,omitempty"`
	ReviewerState string `json:"reviewer_state,omitempty"`
}

// PublishDraftNotes publishes every draft of the signed-in account on a
// merge request at once, with an optional summary comment and reviewer
// state. It creates comments, so it is not repeated.
func (c *Client) PublishDraftNotes(ctx context.Context, p Project, iid int64, summary, reviewerState string) error {
	return c.Do(ctx, Call{Method: "POST", Path: "projects/{}/merge_requests/{}/draft_notes/bulk_publish",
		Args: []string{p.segment(), idArg(iid)}, Body: publishBody{Note: summary, ReviewerState: reviewerState},
		Bucket: BucketNotes, Name: "submit_review"}, nil)
}

// MergeRequestCreate is POST /projects/:id/merge_requests.
type MergeRequestCreate struct {
	SourceBranch       string   `json:"source_branch"`
	TargetBranch       string   `json:"target_branch"`
	Title              string   `json:"title"`
	Description        string   `json:"description,omitempty"`
	Labels             []string `json:"labels,omitempty"`
	AssigneeIDs        []int64  `json:"assignee_ids,omitempty"`
	ReviewerIDs        []int64  `json:"reviewer_ids,omitempty"`
	MilestoneID        int64    `json:"milestone_id,omitempty"`
	RemoveSourceBranch bool     `json:"remove_source_branch,omitempty"`
	Squash             bool     `json:"squash,omitempty"`
}

// CreateMergeRequest opens a merge request. GitLab refuses a second open
// one for the same branches with 409, which guards this create itself.
func (c *Client) CreateMergeRequest(ctx context.Context, p Project, in MergeRequestCreate) (*gitlab.MergeRequest, error) {
	var out gitlab.MergeRequest
	err := c.Do(ctx, Call{Method: "POST", Path: "projects/{}/merge_requests", Args: []string{p.segment()}, Body: in,
		Name: "create_merge_request"}, &out)
	return &out, err
}

// MergeRequestUpdate is PUT /projects/:id/merge_requests/:iid. A nil
// field is not sent. GitLab takes no draft field: the title's "Draft:"
// prefix is the draft state.
type MergeRequestUpdate struct {
	Title              *string  `json:"title,omitempty"`
	Description        *string  `json:"description,omitempty"`
	AddLabels          []string `json:"add_labels,omitempty"`
	RemoveLabels       []string `json:"remove_labels,omitempty"`
	AssigneeIDs        *[]int64 `json:"assignee_ids,omitempty"`
	ReviewerIDs        *[]int64 `json:"reviewer_ids,omitempty"`
	MilestoneID        *int64   `json:"milestone_id,omitempty"`
	StateEvent         string   `json:"state_event,omitempty"`
	TargetBranch       *string  `json:"target_branch,omitempty"`
	RemoveSourceBranch *bool    `json:"remove_source_branch,omitempty"`
	Squash             *bool    `json:"squash,omitempty"`
}

// UpdateMergeRequest changes the fields given.
func (c *Client) UpdateMergeRequest(ctx context.Context, p Project, iid int64, in MergeRequestUpdate) (*gitlab.MergeRequest, error) {
	var out gitlab.MergeRequest
	err := c.Do(ctx, Call{Method: "PUT", Path: "projects/{}/merge_requests/{}", Args: []string{p.segment(), idArg(iid)}, Body: in,
		Name: "update_merge_request"}, &out)
	return &out, err
}

// branchBody is a new branch's request.
type branchBody struct {
	Branch string `json:"branch"`
	Ref    string `json:"ref"`
}

// CreateBranch creates a branch at ref. GitLab refuses a name that
// exists, which guards this create itself (§2.11), but a retry after a
// lost answer would still read as that refusal, so it is not repeated.
func (c *Client) CreateBranch(ctx context.Context, p Project, branch, ref string) (*gitlab.Branch, error) {
	var out gitlab.Branch
	err := c.Do(ctx, Call{Method: "POST", Path: "projects/{}/repository/branches", Args: []string{p.segment()},
		Body: branchBody{Branch: branch, Ref: ref}, Name: "create_branch"}, &out)
	return &out, err
}

// GetBranch reads one branch and its head commit.
func (c *Client) GetBranch(ctx context.Context, p Project, branch string) (*gitlab.Branch, error) {
	var out gitlab.Branch
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/repository/branches/{}", Args: []string{p.segment(), branch},
		Name: "get_branch"}, &out)
	return &out, err
}

// CommitAction is one file change of a commit.
type CommitAction struct {
	Action       string `json:"action"`
	FilePath     string `json:"file_path"`
	PreviousPath string `json:"previous_path,omitempty"`
	// Content is the file's new content. nil leaves it out: a move then
	// keeps the file's content, where "" would empty it
	// (app/services/files/multi_service.rb), and a delete takes none. An
	// update needs the key even for an empty file.
	Content  *string `json:"content,omitempty"`
	Encoding string  `json:"encoding,omitempty"`
	// LastCommitID is the witness: GitLab refuses the action with 400
	// when the file's last commit is another (§2.10).
	LastCommitID string `json:"last_commit_id,omitempty"`
}

// CommitCreate is POST /projects/:id/repository/commits.
type CommitCreate struct {
	Branch        string         `json:"branch"`
	StartBranch   string         `json:"start_branch,omitempty"`
	CommitMessage string         `json:"commit_message"`
	Actions       []CommitAction `json:"actions"`
}

// CreateCommit commits the actions to a branch.
func (c *Client) CreateCommit(ctx context.Context, p Project, in CommitCreate) (*gitlab.Commit, error) {
	var out gitlab.Commit
	err := c.Do(ctx, Call{Method: "POST", Path: "projects/{}/repository/commits", Args: []string{p.segment()}, Body: in,
		Name: "create_commit"}, &out)
	return &out, err
}

// MarkTodoDone marks one of the signed-in account's to-do items done.
func (c *Client) MarkTodoDone(ctx context.Context, id int64) (*gitlab.Todo, error) {
	var out gitlab.Todo
	err := c.Do(ctx, Call{Method: "POST", Path: "todos/{}/mark_as_done", Args: []string{idArg(id)},
		Repeatable: "marking a to-do item done twice leaves it done", Name: "mark_todos_done"}, &out)
	return &out, err
}

// lintBody is POST /projects/:id/ci/lint's request.
type lintBody struct {
	Content     string `json:"content"`
	IncludeJobs bool   `json:"include_jobs,omitempty"`
	DryRun      bool   `json:"dry_run,omitempty"`
	Ref         string `json:"ref,omitempty"`
}

// LintCIContent validates supplied configuration in the project's
// context. It is a POST that changes nothing, so it may repeat and may
// run under a dry run; a read-only token still cannot send it, because
// read_api is enforced by method (§2.6).
func (c *Client) LintCIContent(ctx context.Context, p Project, content string, q LintQuery) (*gitlab.Lint, error) {
	var out gitlab.Lint
	err := c.Do(ctx, Call{Method: "POST", Path: "projects/{}/ci/lint", Args: []string{p.segment()},
		Body:     lintBody{Content: content, IncludeJobs: q.IncludeJobs, DryRun: q.Simulate, Ref: q.Ref},
		ReadOnly: "linting creates nothing", Name: "lint_ci"}, &out)
	return &out, err
}
