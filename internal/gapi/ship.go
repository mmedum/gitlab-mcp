package gapi

import (
	"context"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/gitlab"
)

// The Ship and Destructive calls of phase 3 (docs/architecture.md §4.3):
// merging and approving, running and retrying CI, and deleting a branch
// or a comment. The tools that reach them are registered only when the
// person running the server turned their kind on.

// MergeBody is PUT …/merge_requests/:iid/merge. SHA is the witness:
// GitLab refuses with 409 when the source branch's head is another.
type MergeBody struct {
	SHA                      string `json:"sha"`
	Squash                   *bool  `json:"squash,omitempty"`
	ShouldRemoveSourceBranch *bool  `json:"should_remove_source_branch,omitempty"`
	MergeCommitMessage       string `json:"merge_commit_message,omitempty"`
	SquashCommitMessage      string `json:"squash_commit_message,omitempty"`
	AutoMerge                bool   `json:"auto_merge,omitempty"`
}

// MergeMergeRequest merges a merge request, or sets it to merge when its
// pipeline succeeds. A PUT repeats; with the head fixed by the witness,
// a repeat can only merge the same commits, and GitLab refuses a merge
// request that is already merged.
func (c *Client) MergeMergeRequest(ctx context.Context, p Project, iid int64, in MergeBody) (*gitlab.MergeRequest, error) {
	var out gitlab.MergeRequest
	err := c.Do(ctx, Call{Method: "PUT", Path: "projects/{}/merge_requests/{}/merge", Args: []string{p.segment(), idArg(iid)},
		Body: in, Witness: "sha", Name: "merge_merge_request"}, &out)
	return &out, err
}

// approveBody is POST …/approve's request.
type approveBody struct {
	SHA string `json:"sha"`
}

// ApproveMergeRequest approves at the head sha. GitLab answers 409 when
// the head moved, and 401 when the account may not approve or already
// has.
func (c *Client) ApproveMergeRequest(ctx context.Context, p Project, iid int64, sha string) (*gitlab.Approvals, error) {
	var out gitlab.Approvals
	err := c.Do(ctx, Call{Method: "POST", Path: "projects/{}/merge_requests/{}/approve", Args: []string{p.segment(), idArg(iid)},
		Body: approveBody{SHA: sha}, Witness: "sha", Name: "approve_merge_request"}, &out)
	return &out, err
}

// UnapproveMergeRequest withdraws the account's approval. GitLab answers
// 404 when there is none.
func (c *Client) UnapproveMergeRequest(ctx context.Context, p Project, iid int64) (*gitlab.Approvals, error) {
	var out gitlab.Approvals
	err := c.Do(ctx, Call{Method: "POST", Path: "projects/{}/merge_requests/{}/unapprove", Args: []string{p.segment(), idArg(iid)},
		Name: "unapprove_merge_request"}, &out)
	return &out, err
}

// PipelineVariable is one variable of a new pipeline.
type PipelineVariable struct {
	Key          string `json:"key"`
	Value        string `json:"value"`
	VariableType string `json:"variable_type,omitempty"`
}

// PipelineCreate is POST /projects/:id/pipeline.
type PipelineCreate struct {
	Ref       string             `json:"ref"`
	Variables []PipelineVariable `json:"variables,omitempty"`
	Inputs    map[string]any     `json:"inputs,omitempty"`
}

// CreatePipeline runs a pipeline for a ref. It creates one, so it is not
// repeated (§4.5).
func (c *Client) CreatePipeline(ctx context.Context, p Project, in PipelineCreate) (*gitlab.PipelineDetail, error) {
	var out gitlab.PipelineDetail
	err := c.Do(ctx, Call{Method: "POST", Path: "projects/{}/pipeline", Args: []string{p.segment()}, Body: in,
		Name: "run_pipeline"}, &out)
	return &out, err
}

// RetryPipeline retries a pipeline's failed and canceled jobs; with
// none, GitLab changes nothing. Each retry creates jobs, so it is not
// repeated.
func (c *Client) RetryPipeline(ctx context.Context, p Project, id int64) (*gitlab.PipelineDetail, error) {
	var out gitlab.PipelineDetail
	err := c.Do(ctx, Call{Method: "POST", Path: "projects/{}/pipelines/{}/retry", Args: []string{p.segment(), idArg(id)},
		Name: "retry_pipeline"}, &out)
	return &out, err
}

// CancelPipeline cancels a pipeline's jobs. GitLab answers with the
// pipeline whether or not anything could be canceled.
func (c *Client) CancelPipeline(ctx context.Context, p Project, id int64) (*gitlab.PipelineDetail, error) {
	var out gitlab.PipelineDetail
	err := c.Do(ctx, Call{Method: "POST", Path: "projects/{}/pipelines/{}/cancel", Args: []string{p.segment(), idArg(id)},
		Repeatable: "canceling a pipeline twice leaves it canceled", Name: "cancel_pipeline"}, &out)
	return &out, err
}

// RetryJob runs a job again as a new job, which it returns.
func (c *Client) RetryJob(ctx context.Context, p Project, id int64, inputs map[string]any) (*gitlab.Job, error) {
	var out gitlab.Job
	err := c.Do(ctx, Call{Method: "POST", Path: "projects/{}/jobs/{}/retry", Args: []string{p.segment(), idArg(id)},
		Body: retryBody{Inputs: inputs}, Name: "retry_job"}, &out)
	return &out, err
}

// retryBody is POST …/jobs/:id/retry's request: values for the inputs
// the job declares, which GitLab checks against its specification.
type retryBody struct {
	Inputs map[string]any `json:"inputs,omitempty"`
}

// JobVariable is one variable a manual job is played with.
type JobVariable struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// playBody is POST …/jobs/:id/play's request.
type playBody struct {
	Variables []JobVariable  `json:"job_variables_attributes,omitempty"`
	Inputs    map[string]any `json:"job_inputs,omitempty"`
}

// PlayJob starts a manual job. It runs the job, so it is not repeated.
func (c *Client) PlayJob(ctx context.Context, p Project, id int64, vars []JobVariable, inputs map[string]any) (*gitlab.Job, error) {
	var out gitlab.Job
	err := c.Do(ctx, Call{Method: "POST", Path: "projects/{}/jobs/{}/play", Args: []string{p.segment(), idArg(id)},
		Body: playBody{Variables: vars, Inputs: inputs}, Name: "play_job"}, &out)
	return &out, err
}

// DeleteBranch deletes a branch. GitLab deletes it at the head it reads
// itself; the service holds the caller's head before sending.
func (c *Client) DeleteBranch(ctx context.Context, p Project, branch string) error {
	return c.Do(ctx, Call{Method: "DELETE", Path: "projects/{}/repository/branches/{}", Args: []string{p.segment(), branch},
		Name: "delete_branch"}, nil)
}

// GetIssueNote reads one comment on an issue.
func (c *Client) GetIssueNote(ctx context.Context, p Project, iid, note int64) (*gitlab.Note, error) {
	var out gitlab.Note
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/issues/{}/notes/{}", Args: []string{p.segment(), idArg(iid), idArg(note)},
		Name: "get_note"}, &out)
	return &out, err
}

// GetMergeRequestNote reads one comment on a merge request.
func (c *Client) GetMergeRequestNote(ctx context.Context, p Project, iid, note int64) (*gitlab.Note, error) {
	var out gitlab.Note
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/merge_requests/{}/notes/{}", Args: []string{p.segment(), idArg(iid), idArg(note)},
		Name: "get_note"}, &out)
	return &out, err
}

// shownSince is the If-Unmodified-Since for a time GitLab showed: it
// shows times to the millisecond and compares them to the microsecond,
// so a shown time stands for the end of its millisecond (spike N).
func shownSince(shown time.Time) time.Time {
	return shown.Truncate(time.Millisecond).Add(time.Millisecond - time.Microsecond)
}

// DeleteIssueNote deletes a comment on an issue unless it changed after
// updatedAt, the time a read showed, which GitLab answers with 412.
func (c *Client) DeleteIssueNote(ctx context.Context, p Project, iid, note int64, updatedAt time.Time) error {
	return c.Do(ctx, Call{Method: "DELETE", Path: "projects/{}/issues/{}/notes/{}", Args: []string{p.segment(), idArg(iid), idArg(note)},
		UnmodifiedSince: shownSince(updatedAt), Name: "delete_comment"}, nil)
}

// DeleteMergeRequestNote deletes a comment on a merge request unless it
// changed after updatedAt, the time a read showed, which GitLab answers
// with 412.
func (c *Client) DeleteMergeRequestNote(ctx context.Context, p Project, iid, note int64, updatedAt time.Time) error {
	return c.Do(ctx, Call{Method: "DELETE", Path: "projects/{}/merge_requests/{}/notes/{}", Args: []string{p.segment(), idArg(iid), idArg(note)},
		UnmodifiedSince: shownSince(updatedAt), Name: "delete_comment"}, nil)
}
