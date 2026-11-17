package gapi

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/mmedum/gitlab-mcp/v4/internal/gitlab"
)

// The reads for planning and navigation: labels, milestones, members,
// users, to-do items and search.

// LabelQuery filters ListLabels.
type LabelQuery struct {
	Search string
	// IncludeAncestorGroups adds the labels of the groups above the
	// project, which its issues can carry too.
	IncludeAncestorGroups bool
	WithCounts            bool
}

// ListLabels lists the labels a project's issues and merge requests can
// carry.
func (c *Client) ListLabels(ctx context.Context, p Project, q LabelQuery, opts ListOptions) ([]gitlab.Label, Page, error) {
	v := url.Values{}
	setString(v, "search", q.Search)
	// GitLab's default is true; false is sent so the caller's choice holds.
	v.Set("include_ancestor_groups", strconv.FormatBool(q.IncludeAncestorGroups))
	setBool(v, "with_counts", q.WithCounts)
	var out []gitlab.Label
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/labels", Args: []string{p.segment()}, Query: v,
		Name: "list_labels"}, opts, &out)
	return out, page, err
}

// MilestoneQuery filters ListMilestones. Exactly one of Project and Group
// is set.
type MilestoneQuery struct {
	Project Project
	Group   Group
	State   string // active or closed
	Search  string
	Title   string
	// IncludeAncestors adds the milestones of the groups above.
	IncludeAncestors bool
}

// ListMilestones lists a project's or a group's milestones.
func (c *Client) ListMilestones(ctx context.Context, q MilestoneQuery, opts ListOptions) ([]gitlab.ProjectMilestone, Page, error) {
	if q.Project.IsZero() == q.Group.IsZero() {
		return nil, Page{}, Errf(ClassInvalid, "pass a project or a group, one of them")
	}
	v := url.Values{}
	setString(v, "state", q.State)
	setString(v, "search", q.Search)
	setString(v, "title", q.Title)
	setBool(v, "include_ancestors", q.IncludeAncestors)
	var out []gitlab.ProjectMilestone
	var page Page
	var err error
	if !q.Project.IsZero() {
		page, err = c.list(ctx, Call{Method: "GET", Path: "projects/{}/milestones", Args: []string{q.Project.segment()},
			Query: v, Name: "list_milestones"}, opts, &out)
	} else {
		page, err = c.list(ctx, Call{Method: "GET", Path: "groups/{}/milestones", Args: []string{q.Group.segment()},
			Query: v, Name: "list_milestones"}, opts, &out)
	}
	return out, page, err
}

// EpicQuery filters SearchEpics. Epics are a Premium feature: on a group
// without it GitLab answers 403.
type EpicQuery struct {
	State          string // opened, closed or all
	Search         string
	Labels         []string
	AuthorUsername string
	// IncludeDescendantGroups adds the epics of every group below; nil
	// leaves GitLab's default, true (ee/lib/api/epics.rb at v19.5.3-ee).
	IncludeDescendantGroups *bool
}

// SearchEpics lists a group's epics, newest first.
func (c *Client) SearchEpics(ctx context.Context, g Group, q EpicQuery, opts ListOptions) ([]gitlab.Epic, Page, error) {
	v := url.Values{}
	setString(v, "state", q.State)
	setString(v, "search", q.Search)
	setString(v, "labels", strings.Join(q.Labels, ","))
	setString(v, "author_username", q.AuthorUsername)
	if q.IncludeDescendantGroups != nil {
		v.Set("include_descendant_groups", strconv.FormatBool(*q.IncludeDescendantGroups))
	}
	var out []gitlab.Epic
	page, err := c.list(ctx, Call{Method: "GET", Path: "groups/{}/epics", Args: []string{g.segment()}, Query: v,
		Name: "search_epics"}, opts, &out)
	return out, page, err
}

// GetEpic reads one epic by its iid in the group.
func (c *Client) GetEpic(ctx context.Context, g Group, iid int64) (*gitlab.Epic, error) {
	var out gitlab.Epic
	err := c.Do(ctx, Call{Method: "GET", Path: "groups/{}/epics/{}", Args: []string{g.segment(), idArg(iid)}, Name: "get_epic"}, &out)
	return &out, err
}

// ListEpicChildren lists an epic's child epics. GitLab answers them all
// at once, unpaged.
func (c *Client) ListEpicChildren(ctx context.Context, g Group, iid int64) ([]gitlab.Epic, error) {
	var out []gitlab.Epic
	err := c.Do(ctx, Call{Method: "GET", Path: "groups/{}/epics/{}/epics", Args: []string{g.segment(), idArg(iid)},
		Name: "get_epic"}, &out)
	return out, err
}

// ListEpicIssues lists the issues assigned to an epic.
func (c *Client) ListEpicIssues(ctx context.Context, g Group, iid int64, opts ListOptions) ([]gitlab.LinkedIssue, Page, error) {
	var out []gitlab.LinkedIssue
	page, err := c.list(ctx, Call{Method: "GET", Path: "groups/{}/epics/{}/issues", Args: []string{g.segment(), idArg(iid)},
		Name: "get_epic"}, opts, &out)
	return out, page, err
}

// IterationQuery filters ListIterations. Exactly one of Project and Group
// is set. Iterations are a Premium feature.
type IterationQuery struct {
	Project Project
	Group   Group
	State   string // opened, upcoming, current, closed or all
	Search  string
	// IncludeAncestors adds the iterations of the groups above; nil leaves
	// GitLab's default, true (ee/lib/api/iterations.rb at v19.5.3-ee).
	IncludeAncestors *bool
}

// ListIterations lists a project's or a group's iterations.
func (c *Client) ListIterations(ctx context.Context, q IterationQuery, opts ListOptions) ([]gitlab.Iteration, Page, error) {
	if q.Project.IsZero() == q.Group.IsZero() {
		return nil, Page{}, Errf(ClassInvalid, "pass a project or a group, one of them")
	}
	v := url.Values{}
	setString(v, "state", q.State)
	setString(v, "search", q.Search)
	if q.IncludeAncestors != nil {
		v.Set("include_ancestors", strconv.FormatBool(*q.IncludeAncestors))
	}
	var out []gitlab.Iteration
	var page Page
	var err error
	if !q.Project.IsZero() {
		page, err = c.list(ctx, Call{Method: "GET", Path: "projects/{}/iterations", Args: []string{q.Project.segment()},
			Query: v, Name: "list_iterations"}, opts, &out)
	} else {
		page, err = c.list(ctx, Call{Method: "GET", Path: "groups/{}/iterations", Args: []string{q.Group.segment()},
			Query: v, Name: "list_iterations"}, opts, &out)
	}
	return out, page, err
}

// ListBoards lists a project's issue boards, each with its lists, which
// GitLab includes whole.
func (c *Client) ListBoards(ctx context.Context, p Project, opts ListOptions) ([]gitlab.Board, Page, error) {
	var out []gitlab.Board
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/boards", Args: []string{p.segment()}, Name: "list_boards"},
		opts, &out)
	return out, page, err
}

// ListProjectMembers lists everyone with access to a project, those who
// have it through a group included. query matches names and usernames.
func (c *Client) ListProjectMembers(ctx context.Context, p Project, query string, opts ListOptions) ([]gitlab.Member, Page, error) {
	v := url.Values{}
	setString(v, "query", query)
	var out []gitlab.Member
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/members/all", Args: []string{p.segment()}, Query: v,
		Name: "list_members"}, opts, &out)
	return out, page, err
}

// UserQuery filters ListUsers. Username is an exact match; Search
// matches names and usernames.
type UserQuery struct {
	Search   string
	Username string
}

// ListUsers finds accounts.
func (c *Client) ListUsers(ctx context.Context, q UserQuery, opts ListOptions) ([]gitlab.UserBasic, Page, error) {
	v := url.Values{}
	setString(v, "search", q.Search)
	setString(v, "username", q.Username)
	var out []gitlab.UserBasic
	page, err := c.list(ctx, Call{Method: "GET", Path: "users", Query: v, Name: "find_users"}, opts, &out)
	return out, page, err
}

// TodoQuery filters ListTodos.
type TodoQuery struct {
	// ProjectID limits the list to one project; GitLab takes only the
	// numeric id here.
	ProjectID int64
	State     string // pending or done
	Action    string
	Type      string
}

// ListTodos lists the signed-in account's to-do items.
func (c *Client) ListTodos(ctx context.Context, q TodoQuery, opts ListOptions) ([]gitlab.Todo, Page, error) {
	v := url.Values{}
	if q.ProjectID > 0 {
		v.Set("project_id", strconv.FormatInt(q.ProjectID, 10))
	}
	setString(v, "state", q.State)
	setString(v, "action", q.Action)
	setString(v, "type", q.Type)
	var out []gitlab.Todo
	page, err := c.list(ctx, Call{Method: "GET", Path: "todos", Query: v, Name: "list_todos"}, opts, &out)
	return out, page, err
}

// SearchQuery is one search. At most one of Project and Group is set;
// neither searches the whole instance.
type SearchQuery struct {
	Project Project
	Group   Group
	Scope   string
	Search  string
	State   string // issues and merge requests
	Ref     string // a project's blobs and commits
}

// values builds the query; inProject is true for a project's search,
// the only one that takes a ref.
func (q SearchQuery) values(inProject bool) url.Values {
	v := url.Values{"scope": {q.Scope}, "search": {q.Search}}
	setString(v, "state", q.State)
	if inProject {
		setString(v, "ref", q.Ref)
	}
	return v
}

// Search runs a search of any scope but commits, blobs and wiki blobs,
// whose rows have other shapes (SearchCommits, SearchBlobs). The three
// stay separate methods, each with its own Call literals and decoded
// type, so the API gates can read each.
func (c *Client) Search(ctx context.Context, q SearchQuery, opts ListOptions) ([]gitlab.SearchHit, Page, error) {
	var out []gitlab.SearchHit
	var page Page
	var err error
	switch {
	case !q.Project.IsZero():
		page, err = c.list(ctx, Call{Method: "GET", Path: "projects/{}/search", Args: []string{q.Project.segment()},
			Query: q.values(true), Name: "search"}, opts, &out)
	case !q.Group.IsZero():
		page, err = c.list(ctx, Call{Method: "GET", Path: "groups/{}/search", Args: []string{q.Group.segment()},
			Query: q.values(false), Name: "search"}, opts, &out)
	default:
		page, err = c.list(ctx, Call{Method: "GET", Path: "search", Query: q.values(false), Name: "search"}, opts, &out)
	}
	return out, page, advancedSearch(err)
}

// SearchBlobs runs a search of the blobs or wiki_blobs scope.
func (c *Client) SearchBlobs(ctx context.Context, q SearchQuery, opts ListOptions) ([]gitlab.SearchBlob, Page, error) {
	var out []gitlab.SearchBlob
	var page Page
	var err error
	switch {
	case !q.Project.IsZero():
		page, err = c.list(ctx, Call{Method: "GET", Path: "projects/{}/search", Args: []string{q.Project.segment()},
			Query: q.values(true), Name: "search"}, opts, &out)
	case !q.Group.IsZero():
		page, err = c.list(ctx, Call{Method: "GET", Path: "groups/{}/search", Args: []string{q.Group.segment()},
			Query: q.values(false), Name: "search"}, opts, &out)
	default:
		page, err = c.list(ctx, Call{Method: "GET", Path: "search", Query: q.values(false), Name: "search"}, opts, &out)
	}
	return out, page, advancedSearch(err)
}

// SearchCommits runs a search of the commits scope.
func (c *Client) SearchCommits(ctx context.Context, q SearchQuery, opts ListOptions) ([]gitlab.SearchCommit, Page, error) {
	var out []gitlab.SearchCommit
	var page Page
	var err error
	switch {
	case !q.Project.IsZero():
		page, err = c.list(ctx, Call{Method: "GET", Path: "projects/{}/search", Args: []string{q.Project.segment()},
			Query: q.values(true), Name: "search"}, opts, &out)
	case !q.Group.IsZero():
		page, err = c.list(ctx, Call{Method: "GET", Path: "groups/{}/search", Args: []string{q.Group.segment()},
			Query: q.values(false), Name: "search"}, opts, &out)
	default:
		page, err = c.list(ctx, Call{Method: "GET", Path: "search", Query: q.values(false), Name: "search"}, opts, &out)
	}
	return out, page, advancedSearch(err)
}

// advancedSearch turns GitLab's refusal of a scope that a group or the
// instance searches only with advanced search into [unsupported]. It is a
// 400 whose words differ by release: "Scope supported only with
// advanced search or exact code search" on gitlab.com, "Scope not
// supported without Elasticsearch!" on older ones. GitLab's search page
// gives neither. Checked by the live step "code is searched across a group".
// https://docs.gitlab.com/api/search/
func advancedSearch(err error) error {
	var e *Error
	if !errors.As(err, &e) || e.Class != ClassInvalid {
		return err
	}
	msg := strings.ToLower(e.Message)
	if !strings.Contains(msg, "scope not supported") && !strings.Contains(msg, "only with advanced search") {
		return err
	}
	return &Error{Class: ClassUnsupported, Status: e.Status, Message: "GitLab searches this scope here only with advanced search, which is not available"}
}
