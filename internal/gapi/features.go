package gapi

import (
	"context"
	"net/url"
	"strconv"

	"github.com/mmedum/gitlab-mcp/internal/gitlab"
)

// The phase 6 operations (§17.13): moving and linking issues, label and
// milestone writes, rebase, cherry-pick, revert, blame, job artifacts and
// tag writes.

// ----------------------------------------------------------- issues

// moveBody is POST …/issues/:iid/move's request.
type moveBody struct {
	ToProjectID int64 `json:"to_project_id"`
}

// MoveIssue moves an issue to another project: GitLab makes a copy there
// and closes the original, so the move is never repeated.
func (c *Client) MoveIssue(ctx context.Context, p Project, iid, toProjectID int64) (*gitlab.Issue, error) {
	var out gitlab.Issue
	err := c.Do(ctx, Call{Method: "POST", Path: "projects/{}/issues/{}/move", Args: []string{p.segment(), idArg(iid)},
		Body: moveBody{ToProjectID: toProjectID}, Name: "move_issue"}, &out)
	return &out, err
}

// ListIssueLinks lists the issues an issue is linked to, with each link.
func (c *Client) ListIssueLinks(ctx context.Context, p Project, iid int64) ([]gitlab.RelatedIssue, error) {
	var out []gitlab.RelatedIssue
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/issues/{}/links", Args: []string{p.segment(), idArg(iid)},
		Name: "list_issue_links"}, &out)
	return out, err
}

// IssueLinkCreate is POST …/issues/:iid/links' request.
type IssueLinkCreate struct {
	TargetProjectID string `json:"target_project_id"`
	TargetIssueIID  string `json:"target_issue_iid"`
	LinkType        string `json:"link_type,omitempty"`
}

// CreateIssueLink links two issues. It is never repeated.
func (c *Client) CreateIssueLink(ctx context.Context, p Project, iid int64, in IssueLinkCreate) (*gitlab.IssueLink, error) {
	var out gitlab.IssueLink
	err := c.Do(ctx, Call{Method: "POST", Path: "projects/{}/issues/{}/links", Args: []string{p.segment(), idArg(iid)},
		Body: in, Name: "link_issues"}, &out)
	return &out, err
}

// DeleteIssueLink removes a link between two issues.
func (c *Client) DeleteIssueLink(ctx context.Context, p Project, iid, linkID int64) error {
	return c.Do(ctx, Call{Method: "DELETE", Path: "projects/{}/issues/{}/links/{}",
		Args: []string{p.segment(), idArg(iid), idArg(linkID)}, Name: "unlink_issues"}, nil)
}

// ----------------------------------------------------------- planning

// LabelCreate is POST …/labels' request.
type LabelCreate struct {
	Name        string  `json:"name"`
	Color       string  `json:"color"`
	Description *string `json:"description,omitempty"`
	Priority    *int    `json:"priority,omitempty"`
}

// LabelUpdate is PUT …/labels/:id's request. Only the fields set are
// sent.
type LabelUpdate struct {
	NewName     string  `json:"new_name,omitempty"`
	Color       string  `json:"color,omitempty"`
	Description *string `json:"description,omitempty"`
	Priority    *int    `json:"priority,omitempty"`
}

// GetLabel reads one label by id, a group's included, so a write can
// refuse a group's label by name rather than not find it.
func (c *Client) GetLabel(ctx context.Context, p Project, id int64) (*gitlab.Label, error) {
	var out gitlab.Label
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/labels/{}", Args: []string{p.segment(), idArg(id)},
		Query: url.Values{"include_ancestor_groups": {"true"}}, Name: "get_label"}, &out)
	return &out, err
}

// CreateLabel creates a project label. It is never repeated.
func (c *Client) CreateLabel(ctx context.Context, p Project, in LabelCreate) (*gitlab.Label, error) {
	var out gitlab.Label
	err := c.Do(ctx, Call{Method: "POST", Path: "projects/{}/labels", Args: []string{p.segment()}, Body: in,
		Name: "create_label"}, &out)
	return &out, err
}

// UpdateLabel changes a project label, addressed by id.
func (c *Client) UpdateLabel(ctx context.Context, p Project, id int64, in LabelUpdate) (*gitlab.Label, error) {
	var out gitlab.Label
	err := c.Do(ctx, Call{Method: "PUT", Path: "projects/{}/labels/{}", Args: []string{p.segment(), idArg(id)}, Body: in,
		Name: "update_label"}, &out)
	return &out, err
}

// DeleteLabel deletes a project label, addressed by id.
func (c *Client) DeleteLabel(ctx context.Context, p Project, id int64) error {
	return c.Do(ctx, Call{Method: "DELETE", Path: "projects/{}/labels/{}", Args: []string{p.segment(), idArg(id)},
		Name: "delete_label"}, nil)
}

// MilestoneCreate is POST …/milestones' request.
type MilestoneCreate struct {
	Title       string  `json:"title"`
	Description *string `json:"description,omitempty"`
	DueDate     *string `json:"due_date,omitempty"`
	StartDate   *string `json:"start_date,omitempty"`
}

// MilestoneUpdate is PUT …/milestones/:id's request. Only the fields set
// are sent.
type MilestoneUpdate struct {
	Title       string  `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	DueDate     *string `json:"due_date,omitempty"`
	StartDate   *string `json:"start_date,omitempty"`
	StateEvent  string  `json:"state_event,omitempty"`
}

// GetMilestone reads one project milestone by id.
func (c *Client) GetMilestone(ctx context.Context, p Project, id int64) (*gitlab.ProjectMilestone, error) {
	var out gitlab.ProjectMilestone
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/milestones/{}", Args: []string{p.segment(), idArg(id)},
		Name: "get_milestone"}, &out)
	return &out, err
}

// CreateMilestone creates a project milestone. It is never repeated.
func (c *Client) CreateMilestone(ctx context.Context, p Project, in MilestoneCreate) (*gitlab.ProjectMilestone, error) {
	var out gitlab.ProjectMilestone
	err := c.Do(ctx, Call{Method: "POST", Path: "projects/{}/milestones", Args: []string{p.segment()}, Body: in,
		Name: "create_milestone"}, &out)
	return &out, err
}

// UpdateMilestone changes a project milestone.
func (c *Client) UpdateMilestone(ctx context.Context, p Project, id int64, in MilestoneUpdate) (*gitlab.ProjectMilestone, error) {
	var out gitlab.ProjectMilestone
	err := c.Do(ctx, Call{Method: "PUT", Path: "projects/{}/milestones/{}", Args: []string{p.segment(), idArg(id)}, Body: in,
		Name: "update_milestone"}, &out)
	return &out, err
}

// DeleteMilestone deletes a project milestone.
func (c *Client) DeleteMilestone(ctx context.Context, p Project, id int64) error {
	return c.Do(ctx, Call{Method: "DELETE", Path: "projects/{}/milestones/{}", Args: []string{p.segment(), idArg(id)},
		Name: "delete_milestone"}, nil)
}

// ----------------------------------------------------------- history

// pickBody is the request of POST …/commits/:sha/cherry_pick and
// …/revert.
type pickBody struct {
	Branch  string `json:"branch"`
	DryRun  bool   `json:"dry_run,omitempty"`
	Message string `json:"message,omitempty"`
}

// revertBody is POST …/commits/:sha/revert's request, which takes no
// message.
type revertBody struct {
	Branch string `json:"branch"`
	DryRun bool   `json:"dry_run,omitempty"`
}

// CherryPick applies a commit to a branch as a new commit. With dryRun
// GitLab commits nothing and only says whether it would apply.
func (c *Client) CherryPick(ctx context.Context, p Project, sha, branch, message string, dryRun bool) (*gitlab.Commit, error) {
	var out gitlab.Commit
	call := Call{Method: "POST", Path: "projects/{}/repository/commits/{}/cherry_pick", Args: []string{p.segment(), sha},
		Body: pickBody{Branch: branch, DryRun: dryRun, Message: message}, Name: "cherry_pick_commit"}
	if dryRun {
		call.ReadOnly = "GitLab's dry_run commits nothing"
		call.Name = "cherry_pick_commit_check"
	}
	err := c.Do(ctx, call, &out)
	return &out, err
}

// Revert applies a commit's inverse to a branch as a new commit. With
// dryRun GitLab commits nothing and only says whether it would apply.
func (c *Client) Revert(ctx context.Context, p Project, sha, branch string, dryRun bool) (*gitlab.Commit, error) {
	var out gitlab.Commit
	call := Call{Method: "POST", Path: "projects/{}/repository/commits/{}/revert", Args: []string{p.segment(), sha},
		Body: revertBody{Branch: branch, DryRun: dryRun}, Name: "revert_commit"}
	if dryRun {
		call.ReadOnly = "GitLab's dry_run commits nothing"
		call.Name = "revert_commit_check"
	}
	err := c.Do(ctx, call, &out)
	return &out, err
}

// Blame reads who last changed each line of a file at a ref, from line
// start to line end when both are set.
func (c *Client) Blame(ctx context.Context, p Project, path, ref string, start, end int) ([]gitlab.BlameRange, error) {
	v := url.Values{"ref": {ref}}
	if start > 0 && end > 0 {
		v.Set("range[start]", strconv.Itoa(start))
		v.Set("range[end]", strconv.Itoa(end))
	}
	var out []gitlab.BlameRange
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/repository/files/{}/blame", Args: []string{p.segment(), path},
		Query: v, Name: "get_blame"}, &out)
	return out, err
}

// tagBody is POST …/repository/tags' request.
type tagBody struct {
	TagName string `json:"tag_name"`
	Ref     string `json:"ref"`
	Message string `json:"message,omitempty"`
}

// CreateTag creates a tag at a ref. It is never repeated.
func (c *Client) CreateTag(ctx context.Context, p Project, name, ref, message string) (*gitlab.Tag, error) {
	var out gitlab.Tag
	err := c.Do(ctx, Call{Method: "POST", Path: "projects/{}/repository/tags", Args: []string{p.segment()},
		Body: tagBody{TagName: name, Ref: ref, Message: message}, Name: "create_tag"}, &out)
	return &out, err
}

// DeleteTag deletes a tag.
func (c *Client) DeleteTag(ctx context.Context, p Project, name string) error {
	return c.Do(ctx, Call{Method: "DELETE", Path: "projects/{}/repository/tags/{}", Args: []string{p.segment(), name},
		Name: "delete_tag"}, nil)
}

// ListProtectedTags lists a project's protected tag rules. A rule name
// may be a wildcard.
func (c *Client) ListProtectedTags(ctx context.Context, p Project, opts ListOptions) ([]gitlab.ProtectedTag, Page, error) {
	var out []gitlab.ProtectedTag
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/protected_tags", Args: []string{p.segment()},
		Name: "list_protected_tags"}, opts, &out)
	return out, page, err
}

// rebaseBody is PUT …/merge_requests/:iid/rebase's request.
type rebaseBody struct {
	SkipCI bool `json:"skip_ci,omitempty"`
}

// RebaseMergeRequest asks GitLab to rebase a merge request's source
// branch onto its target. GitLab answers 202 and rebases in the
// background.
func (c *Client) RebaseMergeRequest(ctx context.Context, p Project, iid int64, skipCI bool) (*gitlab.RebaseState, error) {
	var out gitlab.RebaseState
	err := c.Do(ctx, Call{Method: "PUT", Path: "projects/{}/merge_requests/{}/rebase", Args: []string{p.segment(), idArg(iid)},
		Body: rebaseBody{SkipCI: skipCI}, Name: "rebase_merge_request"}, &out)
	return &out, err
}

// GetMergeRequestRebase reads a merge request with whether a rebase is
// in progress.
func (c *Client) GetMergeRequestRebase(ctx context.Context, p Project, iid int64) (*gitlab.MergeRequestRebase, error) {
	var out gitlab.MergeRequestRebase
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/merge_requests/{}", Args: []string{p.segment(), idArg(iid)},
		Query: url.Values{"include_rebase_in_progress": {"true"}}, Name: "get_merge_request"}, &out)
	return &out, err
}

// ----------------------------------------------------------- artifacts

// ListArtifacts lists the files and directories of a job's artifacts
// under a directory, or every entry below it when recursive.
func (c *Client) ListArtifacts(ctx context.Context, p Project, job int64, dir string, recursive bool, opts ListOptions) ([]gitlab.ArtifactEntry, Page, error) {
	v := url.Values{}
	setString(v, "path", dir)
	setBool(v, "recursive", recursive)
	var out []gitlab.ArtifactEntry
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/jobs/{}/artifacts/tree", Args: []string{p.segment(), idArg(job)},
		Query: v, Name: "list_job_artifacts"}, opts, &out)
	return out, page, err
}

// GetArtifact reads one file of a job's artifacts, as GitLab extracts it
// from the archive.
func (c *Client) GetArtifact(ctx context.Context, p Project, job int64, path string) ([]byte, error) {
	var out []byte
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/jobs/{}/artifacts/{}", Args: []string{p.segment(), idArg(job), path},
		Name: "get_job_artifact"}, &out)
	return out, err
}
