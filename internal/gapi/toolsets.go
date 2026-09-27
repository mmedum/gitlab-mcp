package gapi

import (
	"context"
	"net/url"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/gitlab"
)

// The optional toolsets of phase 3 (docs/architecture.md §7.8): project
// wikis, snippets, releases, environments and deployments, and activity.

// ListWikiPages lists a project wiki's pages, without their content.
// GitLab answers with every page at once; the listing is not paged.
func (c *Client) ListWikiPages(ctx context.Context, p Project) ([]gitlab.WikiPageBasic, error) {
	var out []gitlab.WikiPageBasic
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/wikis", Args: []string{p.segment()}, Name: "list_wiki_pages"}, &out)
	return out, err
}

// GetWikiPage reads a wiki page's raw content. The slug may hold
// slashes, which the path escapes as one segment.
func (c *Client) GetWikiPage(ctx context.Context, p Project, slug string) (*gitlab.WikiPage, error) {
	var out gitlab.WikiPage
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/wikis/{}", Args: []string{p.segment(), slug}, Name: "get_wiki_page"}, &out)
	return &out, err
}

// WikiPageCreate is POST /projects/:id/wikis.
type WikiPageCreate struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Format  string `json:"format,omitempty"`
}

// CreateWikiPage creates a page. GitLab refuses a title that exists.
func (c *Client) CreateWikiPage(ctx context.Context, p Project, in WikiPageCreate) (*gitlab.WikiPage, error) {
	var out gitlab.WikiPage
	err := c.Do(ctx, Call{Method: "POST", Path: "projects/{}/wikis", Args: []string{p.segment()}, Body: in,
		Name: "save_wiki_page"}, &out)
	return &out, err
}

// WikiPageUpdate is PUT /projects/:id/wikis/:slug. A nil field is not
// sent.
type WikiPageUpdate struct {
	Title   *string `json:"title,omitempty"`
	Content *string `json:"content,omitempty"`
	Format  *string `json:"format,omitempty"`
}

// UpdateWikiPage changes the fields given.
func (c *Client) UpdateWikiPage(ctx context.Context, p Project, slug string, in WikiPageUpdate) (*gitlab.WikiPage, error) {
	var out gitlab.WikiPage
	err := c.Do(ctx, Call{Method: "PUT", Path: "projects/{}/wikis/{}", Args: []string{p.segment(), slug}, Body: in,
		Name: "save_wiki_page"}, &out)
	return &out, err
}

// DeleteWikiPage deletes a page.
func (c *Client) DeleteWikiPage(ctx context.Context, p Project, slug string) error {
	return c.Do(ctx, Call{Method: "DELETE", Path: "projects/{}/wikis/{}", Args: []string{p.segment(), slug},
		Name: "delete_wiki_page"}, nil)
}

// ListSnippets lists the signed-in account's own snippets.
func (c *Client) ListSnippets(ctx context.Context, createdAfter time.Time, opts ListOptions) ([]gitlab.Snippet, Page, error) {
	v := url.Values{}
	setTime(v, "created_after", createdAfter)
	var out []gitlab.Snippet
	page, err := c.list(ctx, Call{Method: "GET", Path: "snippets", Query: v, Name: "list_snippets"}, opts, &out)
	return out, page, err
}

// ListProjectSnippets lists a project's snippets.
func (c *Client) ListProjectSnippets(ctx context.Context, p Project, opts ListOptions) ([]gitlab.Snippet, Page, error) {
	var out []gitlab.Snippet
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/snippets", Args: []string{p.segment()},
		Name: "list_snippets"}, opts, &out)
	return out, page, err
}

// GetSnippet reads a personal snippet.
func (c *Client) GetSnippet(ctx context.Context, id int64) (*gitlab.Snippet, error) {
	var out gitlab.Snippet
	err := c.Do(ctx, Call{Method: "GET", Path: "snippets/{}", Args: []string{idArg(id)}, Name: "get_snippet"}, &out)
	return &out, err
}

// GetProjectSnippet reads a project's snippet.
func (c *Client) GetProjectSnippet(ctx context.Context, p Project, id int64) (*gitlab.Snippet, error) {
	var out gitlab.Snippet
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/snippets/{}", Args: []string{p.segment(), idArg(id)},
		Name: "get_snippet"}, &out)
	return &out, err
}

// SnippetRaw reads a personal snippet's first file as bytes.
func (c *Client) SnippetRaw(ctx context.Context, id int64) ([]byte, error) {
	var out []byte
	err := c.Do(ctx, Call{Method: "GET", Path: "snippets/{}/raw", Args: []string{idArg(id)}, Name: "get_snippet"}, &out)
	return out, err
}

// ProjectSnippetRaw reads a project snippet's first file as bytes.
func (c *Client) ProjectSnippetRaw(ctx context.Context, p Project, id int64) ([]byte, error) {
	var out []byte
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/snippets/{}/raw", Args: []string{p.segment(), idArg(id)},
		Name: "get_snippet"}, &out)
	return out, err
}

// SnippetFileRaw reads one file of a personal snippet at a ref.
func (c *Client) SnippetFileRaw(ctx context.Context, id int64, ref, path string) ([]byte, error) {
	var out []byte
	err := c.Do(ctx, Call{Method: "GET", Path: "snippets/{}/files/{}/{}/raw", Args: []string{idArg(id), ref, path},
		Name: "get_snippet"}, &out)
	return out, err
}

// ProjectSnippetFileRaw reads one file of a project snippet at a ref.
func (c *Client) ProjectSnippetFileRaw(ctx context.Context, p Project, id int64, ref, path string) ([]byte, error) {
	var out []byte
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/snippets/{}/files/{}/{}/raw",
		Args: []string{p.segment(), idArg(id), ref, path}, Name: "get_snippet"}, &out)
	return out, err
}

// SnippetFileCreate is one file of a new snippet.
type SnippetFileCreate struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

// SnippetCreate is POST /snippets and /projects/:id/snippets. Visibility
// is always private: the service never sends another (§7.8).
type SnippetCreate struct {
	Title       string              `json:"title"`
	Description string              `json:"description,omitempty"`
	Visibility  string              `json:"visibility"`
	Files       []SnippetFileCreate `json:"files"`
}

// CreateSnippet creates a personal snippet. It is not repeated.
func (c *Client) CreateSnippet(ctx context.Context, in SnippetCreate) (*gitlab.Snippet, error) {
	var out gitlab.Snippet
	err := c.Do(ctx, Call{Method: "POST", Path: "snippets", Body: in, Name: "create_snippet"}, &out)
	return &out, err
}

// CreateProjectSnippet creates a snippet in a project. It is not
// repeated.
func (c *Client) CreateProjectSnippet(ctx context.Context, p Project, in SnippetCreate) (*gitlab.Snippet, error) {
	var out gitlab.Snippet
	err := c.Do(ctx, Call{Method: "POST", Path: "projects/{}/snippets", Args: []string{p.segment()}, Body: in,
		Name: "create_snippet"}, &out)
	return &out, err
}

// GetTag reads one tag; a tag that does not exist is [not_found].
func (c *Client) GetTag(ctx context.Context, p Project, name string) (*gitlab.Tag, error) {
	var out gitlab.Tag
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/repository/tags/{}", Args: []string{p.segment(), name},
		Name: "get_tag"}, &out)
	return &out, err
}

// ReleaseQuery orders ListReleases.
type ReleaseQuery struct {
	OrderBy string // released_at or created_at
	Sort    string // asc or desc
}

// ListReleases lists a project's releases.
func (c *Client) ListReleases(ctx context.Context, p Project, q ReleaseQuery, opts ListOptions) ([]gitlab.Release, Page, error) {
	v := url.Values{}
	setString(v, "order_by", q.OrderBy)
	setString(v, "sort", q.Sort)
	var out []gitlab.Release
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/releases", Args: []string{p.segment()}, Query: v,
		Name: "list_releases"}, opts, &out)
	return out, page, err
}

// GetRelease reads the release of a tag.
func (c *Client) GetRelease(ctx context.Context, p Project, tag string) (*gitlab.Release, error) {
	var out gitlab.Release
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/releases/{}", Args: []string{p.segment(), tag},
		Name: "get_release"}, &out)
	return &out, err
}

// ReleaseCreate is POST /projects/:id/releases.
type ReleaseCreate struct {
	TagName     string        `json:"tag_name"`
	Ref         string        `json:"ref,omitempty"`
	TagMessage  string        `json:"tag_message,omitempty"`
	Name        string        `json:"name,omitempty"`
	Description string        `json:"description,omitempty"`
	Milestones  []string      `json:"milestones,omitempty"`
	ReleasedAt  *time.Time    `json:"released_at,omitempty"`
	Assets      *ReleaseLinks `json:"assets,omitempty"`
}

// ReleaseLinks are the asset links a new release is created with.
type ReleaseLinks struct {
	Links []ReleaseLink `json:"links"`
}

// ReleaseLink is one asset link: a name and a URL, and optionally its
// kind and the path GitLab serves it at under the release.
type ReleaseLink struct {
	Name            string `json:"name"`
	URL             string `json:"url"`
	LinkType        string `json:"link_type,omitempty"`
	DirectAssetPath string `json:"direct_asset_path,omitempty"`
}

// CreateRelease creates a release, and its tag at ref when the tag does
// not exist. GitLab refuses a second release of a tag with 409. It is
// not repeated.
func (c *Client) CreateRelease(ctx context.Context, p Project, in ReleaseCreate) (*gitlab.Release, error) {
	var out gitlab.Release
	err := c.Do(ctx, Call{Method: "POST", Path: "projects/{}/releases", Args: []string{p.segment()}, Body: in,
		Name: "create_release"}, &out)
	return &out, err
}

// EnvironmentQuery filters ListEnvironments.
type EnvironmentQuery struct {
	Name   string
	Search string
	States string // available, stopping or stopped
}

// ListEnvironments lists a project's environments.
func (c *Client) ListEnvironments(ctx context.Context, p Project, q EnvironmentQuery, opts ListOptions) ([]gitlab.Environment, Page, error) {
	v := url.Values{}
	setString(v, "name", q.Name)
	setString(v, "search", q.Search)
	setString(v, "states", q.States)
	var out []gitlab.Environment
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/environments", Args: []string{p.segment()}, Query: v,
		Name: "list_environments"}, opts, &out)
	return out, page, err
}

// DeploymentQuery filters ListDeployments.
type DeploymentQuery struct {
	Environment   string
	Status        string
	OrderBy       string // id, iid, created_at, updated_at, finished_at or ref
	Sort          string
	UpdatedAfter  time.Time
	UpdatedBefore time.Time
}

// ListDeployments lists a project's deployments.
func (c *Client) ListDeployments(ctx context.Context, p Project, q DeploymentQuery, opts ListOptions) ([]gitlab.Deployment, Page, error) {
	v := url.Values{}
	setString(v, "environment", q.Environment)
	setString(v, "status", q.Status)
	setString(v, "order_by", q.OrderBy)
	setString(v, "sort", q.Sort)
	setTime(v, "updated_after", q.UpdatedAfter)
	setTime(v, "updated_before", q.UpdatedBefore)
	var out []gitlab.Deployment
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/deployments", Args: []string{p.segment()}, Query: v,
		Name: "list_deployments"}, opts, &out)
	return out, page, err
}

// EventQuery filters ListEvents. Before and After are days, YYYY-MM-DD,
// and exclusive.
type EventQuery struct {
	Action     string
	TargetType string
	Before     string
	After      string
	Sort       string
}

func (q EventQuery) values() url.Values {
	v := url.Values{}
	setString(v, "action", q.Action)
	setString(v, "target_type", q.TargetType)
	setString(v, "before", q.Before)
	setString(v, "after", q.After)
	setString(v, "sort", q.Sort)
	return v
}

// ListEvents lists the signed-in account's own activity.
func (c *Client) ListEvents(ctx context.Context, q EventQuery, opts ListOptions) ([]gitlab.Event, Page, error) {
	var out []gitlab.Event
	page, err := c.list(ctx, Call{Method: "GET", Path: "events", Query: q.values(), Name: "list_events"}, opts, &out)
	return out, page, err
}

// ListProjectEvents lists a project's activity.
func (c *Client) ListProjectEvents(ctx context.Context, p Project, q EventQuery, opts ListOptions) ([]gitlab.Event, Page, error) {
	var out []gitlab.Event
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/events", Args: []string{p.segment()}, Query: q.values(),
		Name: "list_events"}, opts, &out)
	return out, page, err
}
