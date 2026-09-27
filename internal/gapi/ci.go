package gapi

import (
	"context"
	"net/url"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/gitlab"
)

// The phase-1 CI reads: pipelines, jobs, a job's log and the CI lint.

// PipelineQuery filters ListPipelines.
type PipelineQuery struct {
	Ref           string
	SHA           string
	Status        string
	Source        string
	Username      string
	UpdatedAfter  time.Time
	UpdatedBefore time.Time
	// CreatedAfter is how a lost run_pipeline is settled (§4.5).
	CreatedAfter time.Time
	OrderBy      string // id, status, ref, updated_at or user_id
	Sort         string // asc or desc
}

// ListPipelines lists a project's pipelines.
func (c *Client) ListPipelines(ctx context.Context, p Project, q PipelineQuery, opts ListOptions) ([]gitlab.Pipeline, Page, error) {
	v := url.Values{}
	setString(v, "ref", q.Ref)
	setString(v, "sha", q.SHA)
	setString(v, "status", q.Status)
	setString(v, "source", q.Source)
	setString(v, "username", q.Username)
	setTime(v, "updated_after", q.UpdatedAfter)
	setTime(v, "updated_before", q.UpdatedBefore)
	setTime(v, "created_after", q.CreatedAfter)
	setString(v, "order_by", q.OrderBy)
	setString(v, "sort", q.Sort)
	var out []gitlab.Pipeline
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/pipelines", Args: []string{p.segment()},
		Query: v, Name: "list_pipelines"}, opts, &out)
	return out, page, err
}

// GetPipeline reads one pipeline.
func (c *Client) GetPipeline(ctx context.Context, p Project, id int64) (*gitlab.PipelineDetail, error) {
	var out gitlab.PipelineDetail
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/pipelines/{}", Args: []string{p.segment(), idArg(id)},
		Name: "get_pipeline"}, &out)
	return &out, err
}

// JobQuery filters ListPipelineJobs.
type JobQuery struct {
	// Scope is one job status, such as failed; empty lists every job.
	Scope          string
	IncludeRetried bool
}

// ListPipelineJobs lists a pipeline's jobs.
func (c *Client) ListPipelineJobs(ctx context.Context, p Project, pipeline int64, q JobQuery, opts ListOptions) ([]gitlab.Job, Page, error) {
	v := url.Values{}
	setString(v, "scope", q.Scope)
	setBool(v, "include_retried", q.IncludeRetried)
	var out []gitlab.Job
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/pipelines/{}/jobs", Args: []string{p.segment(), idArg(pipeline)},
		Query: v, Name: "list_jobs"}, opts, &out)
	return out, page, err
}

// GetJob reads one job.
func (c *Client) GetJob(ctx context.Context, p Project, id int64) (*gitlab.Job, error) {
	var out gitlab.Job
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/jobs/{}", Args: []string{p.segment(), idArg(id)},
		Name: "get_job"}, &out)
	return &out, err
}

// GetJobTrace reads a job's log as GitLab stored it: bytes, ANSI escapes
// and section markers included.
func (c *Client) GetJobTrace(ctx context.Context, p Project, id int64) ([]byte, error) {
	var out []byte
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/jobs/{}/trace", Args: []string{p.segment(), idArg(id)},
		Name: "get_job_log"}, &out)
	return out, err
}

// LintQuery selects what LintCI checks.
type LintQuery struct {
	// Ref is the branch or tag whose configuration is linted; the
	// default branch when empty.
	Ref string
	// Simulate asks GitLab to simulate creating a pipeline for Ref,
	// which finds what a static check cannot, such as rules that match
	// no job.
	Simulate    bool
	IncludeJobs bool
}

// LintCI checks a project's CI configuration at a ref.
func (c *Client) LintCI(ctx context.Context, p Project, q LintQuery) (*gitlab.Lint, error) {
	v := url.Values{}
	setString(v, "content_ref", q.Ref)
	if q.Simulate {
		v.Set("dry_run", "true")
		setString(v, "dry_run_ref", q.Ref)
	}
	setBool(v, "include_jobs", q.IncludeJobs)
	var out gitlab.Lint
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/ci/lint", Args: []string{p.segment()}, Query: v,
		Name: "lint_ci"}, &out)
	return &out, err
}
