package gapi

import (
	"context"
	"net/url"

	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
)

// The phase-1 reads of a merge request's review and the repository's
// history: diffs, commits, drafts, compare and tags.

// ListMergeRequestDiffs lists a merge request's per-file diffs.
func (c *Client) ListMergeRequestDiffs(ctx context.Context, p Project, iid int64, opts ListOptions) ([]gitlab.Diff, Page, error) {
	var out []gitlab.Diff
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/merge_requests/{}/diffs",
		Args: []string{p.segment(), idArg(iid)}, Name: "list_mr_diffs"}, opts, &out)
	return out, page, err
}

// ListMergeRequestCommits lists a merge request's commits, newest first.
func (c *Client) ListMergeRequestCommits(ctx context.Context, p Project, iid int64, opts ListOptions) ([]gitlab.Commit, Page, error) {
	var out []gitlab.Commit
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/merge_requests/{}/commits",
		Args: []string{p.segment(), idArg(iid)}, Name: "list_mr_commits"}, opts, &out)
	return out, page, err
}

// ListMergeRequestVersions lists a merge request's diff versions, newest
// first.
func (c *Client) ListMergeRequestVersions(ctx context.Context, p Project, iid int64, opts ListOptions) ([]gitlab.MergeRequestVersion, Page, error) {
	var out []gitlab.MergeRequestVersion
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/merge_requests/{}/versions",
		Args: []string{p.segment(), idArg(iid)}, Name: "list_mr_versions"}, opts, &out)
	return out, page, err
}

// ListDraftNotes lists the signed-in account's unpublished review
// comments on a merge request. GitLab returns them all, unpaged.
func (c *Client) ListDraftNotes(ctx context.Context, p Project, iid int64) ([]gitlab.DraftNote, error) {
	var out []gitlab.DraftNote
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/merge_requests/{}/draft_notes",
		Args: []string{p.segment(), idArg(iid)}, Name: "list_draft_notes"}, &out)
	return out, err
}

// Compare compares two refs. Without straight, GitLab compares from the
// merge base, as a merge request would.
func (c *Client) Compare(ctx context.Context, p Project, from, to string, straight bool) (*gitlab.Compare, error) {
	v := url.Values{"from": {from}, "to": {to}}
	setBool(v, "straight", straight)
	var out gitlab.Compare
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/repository/compare", Args: []string{p.segment()},
		Query: v, Name: "compare_refs"}, &out)
	return &out, err
}

// TagQuery filters ListTags.
type TagQuery struct {
	Search  string
	OrderBy string // name, updated or version
	Sort    string // asc or desc
}

// ListTags lists a project's tags.
func (c *Client) ListTags(ctx context.Context, p Project, q TagQuery, opts ListOptions) ([]gitlab.Tag, Page, error) {
	v := url.Values{}
	setString(v, "search", q.Search)
	setString(v, "order_by", q.OrderBy)
	setString(v, "sort", q.Sort)
	var out []gitlab.Tag
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/repository/tags", Args: []string{p.segment()},
		Query: v, Name: "list_tags"}, opts, &out)
	return out, page, err
}
