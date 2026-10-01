package gapi

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
)

// The phase-0 reads. Each builds its Call as a literal so the
// api-coverage gate can bind it to an operation.

// GetMetadata reads the instance's version and edition. It needs
// authentication.
func (c *Client) GetMetadata(ctx context.Context) (*gitlab.Metadata, error) {
	var out gitlab.Metadata
	err := c.Do(ctx, Call{Method: "GET", Path: "metadata", Name: "get_metadata"}, &out)
	return &out, err
}

// GetCurrentUser reads the signed-in account.
func (c *Client) GetCurrentUser(ctx context.Context) (*gitlab.User, error) {
	var out gitlab.User
	err := c.Do(ctx, Call{Method: "GET", Path: "user", Name: "get_current_user"}, &out)
	return &out, err
}

// TokenInfo introspects the bearer token: scopes, expiry, application.
// Doorkeeper serves it under the web base, not the API root.
func (c *Client) TokenInfo(ctx context.Context) (*gitlab.TokenInfo, error) {
	var out gitlab.TokenInfo
	err := c.Do(ctx, Call{Method: "GET", Path: "oauth/token/info", Root: RootWeb, Name: "token_info"}, &out)
	return &out, err
}

// ResolveProject returns p addressed by numeric id, with its full path.
// A path is read at most once per call context (WithCall), and then not
// again for a few minutes in this process; a move GitLab reports
// forgets what the process remembered. A proxy that decodes %2F then
// breaks at most this one request (§6.1).
//
// An id is returned as given, with its path when the process knows it,
// and is never read for it.
func (c *Client) ResolveProject(ctx context.Context, p Project) (Project, error) {
	if p.IsZero() {
		return Project{}, Errf(ClassInvalid, "no project was given: pass its numeric id or full path")
	}
	if p.ID() != 0 {
		if known, ok := c.projects.byID(p.ID()); ok && p.Path() == "" {
			return known, nil
		}
		return p, nil
	}
	key := strings.ToLower(p.Path())
	s := stateOf(ctx)
	if s != nil {
		s.mu.Lock()
		cached, ok := s.projects[key]
		s.mu.Unlock()
		if ok {
			return cached, nil
		}
	}
	resolved, ok := c.projects.byPath(key)
	if !ok {
		proj, err := c.GetProject(ctx, p)
		if err != nil {
			return Project{}, err
		}
		resolved = ProjectByID(proj.ID).withPath(proj.PathWithNamespace)
	}
	if s != nil {
		s.mu.Lock()
		s.projects[key] = resolved
		s.mu.Unlock()
	}
	return resolved, nil
}

// GetProject reads one project, and remembers its id and path.
func (c *Client) GetProject(ctx context.Context, p Project) (*gitlab.Project, error) {
	var out gitlab.Project
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}", Args: []string{p.segment()}, Name: "get_project"}, &out)
	if err == nil && out.ID != 0 && out.PathWithNamespace != "" {
		c.projects.remember(ProjectByID(out.ID).withPath(out.PathWithNamespace))
	}
	return &out, err
}

// ProjectQuery filters SearchProjects.
type ProjectQuery struct {
	// Group limits the search to one group; zero searches everything the
	// token can see.
	Group            Group
	IncludeSubgroups bool
	Search           string
	Membership       bool
	Owned            bool
	Starred          bool
	Archived         *bool
	Visibility       string
	OrderBy, Sort    string
	// Keyset asks for keyset pagination, which GitLab offers on project
	// lists ordered by id and which has no 50,000-row edge.
	Keyset bool
}

// values builds the query; inGroup is true for a group's project list.
func (q ProjectQuery) values(inGroup bool) url.Values {
	v := url.Values{}
	setString(v, "search", q.Search)
	// A group's project list publishes no membership filter: every
	// project it returns is already one the token can see in the group.
	if !inGroup {
		setBool(v, "membership", q.Membership)
	}
	setBool(v, "owned", q.Owned)
	setBool(v, "starred", q.Starred)
	if q.Archived != nil {
		v.Set("archived", strconv.FormatBool(*q.Archived))
	}
	setString(v, "visibility", q.Visibility)
	setString(v, "order_by", q.OrderBy)
	setString(v, "sort", q.Sort)
	if inGroup {
		setBool(v, "include_subgroups", q.IncludeSubgroups)
	}
	if q.Keyset {
		v.Set("pagination", "keyset")
		if q.OrderBy == "" {
			v.Set("order_by", "id")
		}
		if q.Sort == "" {
			v.Set("sort", "asc")
		}
	}
	return v
}

// SearchProjects lists projects, across the instance or in one group.
func (c *Client) SearchProjects(ctx context.Context, q ProjectQuery, opts ListOptions) ([]gitlab.Project, Page, error) {
	var out []gitlab.Project
	var page Page
	var err error
	if q.Group.IsZero() {
		page, err = c.list(ctx, Call{Method: "GET", Path: "projects", Query: q.values(false), Name: "search_projects"}, opts, &out)
	} else {
		page, err = c.list(ctx, Call{Method: "GET", Path: "groups/{}/projects", Args: []string{q.Group.segment()},
			Query: q.values(true), Name: "search_projects"}, opts, &out)
	}
	return out, page, err
}

// ItemQuery filters SearchIssues and SearchMergeRequests. At most one of
// Project and Group is set; neither searches across the instance.
type ItemQuery struct {
	Project          Project
	Group            Group
	State            string // opened, closed, merged, all
	Labels           []string
	AuthorUsername   string
	AssigneeUsername string
	ReviewerUsername string // merge requests only
	Milestone        string
	Search           string
	// Scope is created_by_me, assigned_to_me or all. GitLab's default
	// differs by endpoint, so it is sent only when set.
	Scope         string
	CreatedAfter  time.Time
	CreatedBefore time.Time
	UpdatedAfter  time.Time
	UpdatedBefore time.Time
	OrderBy, Sort string
	SourceBranch  string // merge requests only
	TargetBranch  string // merge requests only
	Draft         *bool  // merge requests only
}

func (q ItemQuery) values(mergeRequests bool) url.Values {
	v := url.Values{}
	setString(v, "state", q.State)
	if len(q.Labels) > 0 {
		v.Set("labels", strings.Join(q.Labels, ","))
	}
	setString(v, "author_username", q.AuthorUsername)
	setString(v, "assignee_username", q.AssigneeUsername)
	setString(v, "milestone", q.Milestone)
	setString(v, "search", q.Search)
	setString(v, "scope", q.Scope)
	setTime(v, "created_after", q.CreatedAfter)
	setTime(v, "created_before", q.CreatedBefore)
	setTime(v, "updated_after", q.UpdatedAfter)
	setTime(v, "updated_before", q.UpdatedBefore)
	setString(v, "order_by", q.OrderBy)
	setString(v, "sort", q.Sort)
	if mergeRequests {
		setString(v, "reviewer_username", q.ReviewerUsername)
		setString(v, "source_branch", q.SourceBranch)
		setString(v, "target_branch", q.TargetBranch)
		if q.Draft != nil {
			v.Set("draft", strconv.FormatBool(*q.Draft))
		}
	}
	return v
}

func (q ItemQuery) check() error {
	if !q.Project.IsZero() && !q.Group.IsZero() {
		return Errf(ClassInvalid, "pass a project or a group, not both")
	}
	return nil
}

// SearchIssues lists issues in a project, a group, or across the
// instance.
func (c *Client) SearchIssues(ctx context.Context, q ItemQuery, opts ListOptions) ([]gitlab.Issue, Page, error) {
	if err := q.check(); err != nil {
		return nil, Page{}, err
	}
	var out []gitlab.Issue
	var page Page
	var err error
	switch {
	case !q.Project.IsZero():
		page, err = c.list(ctx, Call{Method: "GET", Path: "projects/{}/issues", Args: []string{q.Project.segment()},
			Query: q.values(false), Name: "search_issues"}, opts, &out)
	case !q.Group.IsZero():
		page, err = c.list(ctx, Call{Method: "GET", Path: "groups/{}/issues", Args: []string{q.Group.segment()},
			Query: q.values(false), Name: "search_issues"}, opts, &out)
	default:
		page, err = c.list(ctx, Call{Method: "GET", Path: "issues", Query: q.values(false), Name: "search_issues"}, opts, &out)
	}
	return out, page, err
}

// GetIssue reads one issue by project and iid.
func (c *Client) GetIssue(ctx context.Context, p Project, iid int64) (*gitlab.Issue, error) {
	var out gitlab.Issue
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/issues/{}", Args: []string{p.segment(), idArg(iid)},
		Name: "get_issue"}, &out)
	return &out, err
}

// ListIssueDiscussions lists an issue's threads, oldest first as GitLab
// returns them.
func (c *Client) ListIssueDiscussions(ctx context.Context, p Project, iid int64, opts ListOptions) ([]gitlab.Discussion, Page, error) {
	var out []gitlab.Discussion
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/issues/{}/discussions",
		Args: []string{p.segment(), idArg(iid)}, Name: "list_discussions"}, opts, &out)
	return out, page, err
}

// ListIssueRelatedMergeRequests lists the merge requests GitLab relates
// to an issue: those it mentions and those that mention it.
func (c *Client) ListIssueRelatedMergeRequests(ctx context.Context, p Project, iid int64, opts ListOptions) ([]gitlab.LinkedMergeRequest, Page, error) {
	var out []gitlab.LinkedMergeRequest
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/issues/{}/related_merge_requests",
		Args: []string{p.segment(), idArg(iid)}, Name: "list_issue_related_merge_requests"}, opts, &out)
	return out, page, err
}

// ListIssueClosedBy lists the merge requests in the issue's project that
// close it when merged.
func (c *Client) ListIssueClosedBy(ctx context.Context, p Project, iid int64, opts ListOptions) ([]gitlab.LinkedMergeRequest, Page, error) {
	var out []gitlab.LinkedMergeRequest
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/issues/{}/closed_by",
		Args: []string{p.segment(), idArg(iid)}, Name: "list_issue_closed_by"}, opts, &out)
	return out, page, err
}

// SearchMergeRequests lists merge requests in a project, a group, or
// across the instance.
func (c *Client) SearchMergeRequests(ctx context.Context, q ItemQuery, opts ListOptions) ([]gitlab.MergeRequest, Page, error) {
	if err := q.check(); err != nil {
		return nil, Page{}, err
	}
	var out []gitlab.MergeRequest
	var page Page
	var err error
	switch {
	case !q.Project.IsZero():
		page, err = c.list(ctx, Call{Method: "GET", Path: "projects/{}/merge_requests", Args: []string{q.Project.segment()},
			Query: q.values(true), Name: "search_merge_requests"}, opts, &out)
	case !q.Group.IsZero():
		page, err = c.list(ctx, Call{Method: "GET", Path: "groups/{}/merge_requests", Args: []string{q.Group.segment()},
			Query: q.values(true), Name: "search_merge_requests"}, opts, &out)
	default:
		page, err = c.list(ctx, Call{Method: "GET", Path: "merge_requests", Query: q.values(true),
			Name: "search_merge_requests"}, opts, &out)
	}
	return out, page, err
}

// GetMergeRequest reads one merge request by project and iid.
func (c *Client) GetMergeRequest(ctx context.Context, p Project, iid int64) (*gitlab.MergeRequest, error) {
	var out gitlab.MergeRequest
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/merge_requests/{}", Args: []string{p.segment(), idArg(iid)},
		Name: "get_merge_request"}, &out)
	return &out, err
}

// GetMergeRequestApprovals reads a merge request's approval state.
func (c *Client) GetMergeRequestApprovals(ctx context.Context, p Project, iid int64) (*gitlab.Approvals, error) {
	var out gitlab.Approvals
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/merge_requests/{}/approvals",
		Args: []string{p.segment(), idArg(iid)}, Name: "get_merge_request_approvals"}, &out)
	return &out, err
}

// ListMergeRequestReviewers lists a merge request's reviewers with each
// one's review state, in no order GitLab promises.
func (c *Client) ListMergeRequestReviewers(ctx context.Context, p Project, iid int64, opts ListOptions) ([]gitlab.MergeRequestReviewer, Page, error) {
	var out []gitlab.MergeRequestReviewer
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/merge_requests/{}/reviewers",
		Args: []string{p.segment(), idArg(iid)}, Name: "list_merge_request_reviewers"}, opts, &out)
	return out, page, err
}

// ListMergeRequestDiscussions lists a merge request's threads.
func (c *Client) ListMergeRequestDiscussions(ctx context.Context, p Project, iid int64, opts ListOptions) ([]gitlab.Discussion, Page, error) {
	var out []gitlab.Discussion
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/merge_requests/{}/discussions",
		Args: []string{p.segment(), idArg(iid)}, Name: "list_discussions"}, opts, &out)
	return out, page, err
}

// ListMergeRequestClosesIssues lists the issues a merge request closes
// when merged.
func (c *Client) ListMergeRequestClosesIssues(ctx context.Context, p Project, iid int64, opts ListOptions) ([]gitlab.LinkedIssue, Page, error) {
	var out []gitlab.LinkedIssue
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/merge_requests/{}/closes_issues",
		Args: []string{p.segment(), idArg(iid)}, Name: "list_merge_request_closes_issues"}, opts, &out)
	return out, page, err
}

// ListMergeRequestRelatedIssues lists the issues a merge request's title,
// description, comments and commits mention.
func (c *Client) ListMergeRequestRelatedIssues(ctx context.Context, p Project, iid int64, opts ListOptions) ([]gitlab.LinkedIssue, Page, error) {
	var out []gitlab.LinkedIssue
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/merge_requests/{}/related_issues",
		Args: []string{p.segment(), idArg(iid)}, Name: "list_merge_request_related_issues"}, opts, &out)
	return out, page, err
}

// ListIssueLabelEvents lists an issue's label events, oldest first.
func (c *Client) ListIssueLabelEvents(ctx context.Context, p Project, iid int64, opts ListOptions) ([]gitlab.LabelEvent, Page, error) {
	var out []gitlab.LabelEvent
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/issues/{}/resource_label_events",
		Args: []string{p.segment(), idArg(iid)}, Name: "list_issue_label_events"}, opts, &out)
	return out, page, err
}

// ListIssueStateEvents lists an issue's state events, oldest first.
func (c *Client) ListIssueStateEvents(ctx context.Context, p Project, iid int64, opts ListOptions) ([]gitlab.StateEvent, Page, error) {
	var out []gitlab.StateEvent
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/issues/{}/resource_state_events",
		Args: []string{p.segment(), idArg(iid)}, Name: "list_issue_state_events"}, opts, &out)
	return out, page, err
}

// ListIssueMilestoneEvents lists an issue's milestone events, oldest first.
func (c *Client) ListIssueMilestoneEvents(ctx context.Context, p Project, iid int64, opts ListOptions) ([]gitlab.MilestoneEvent, Page, error) {
	var out []gitlab.MilestoneEvent
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/issues/{}/resource_milestone_events",
		Args: []string{p.segment(), idArg(iid)}, Name: "list_issue_milestone_events"}, opts, &out)
	return out, page, err
}

// ListMergeRequestLabelEvents lists a merge request's label events, oldest first.
func (c *Client) ListMergeRequestLabelEvents(ctx context.Context, p Project, iid int64, opts ListOptions) ([]gitlab.LabelEvent, Page, error) {
	var out []gitlab.LabelEvent
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/merge_requests/{}/resource_label_events",
		Args: []string{p.segment(), idArg(iid)}, Name: "list_merge_request_label_events"}, opts, &out)
	return out, page, err
}

// ListMergeRequestStateEvents lists a merge request's state events, oldest first.
func (c *Client) ListMergeRequestStateEvents(ctx context.Context, p Project, iid int64, opts ListOptions) ([]gitlab.StateEvent, Page, error) {
	var out []gitlab.StateEvent
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/merge_requests/{}/resource_state_events",
		Args: []string{p.segment(), idArg(iid)}, Name: "list_merge_request_state_events"}, opts, &out)
	return out, page, err
}

// ListMergeRequestMilestoneEvents lists a merge request's milestone events, oldest first.
func (c *Client) ListMergeRequestMilestoneEvents(ctx context.Context, p Project, iid int64, opts ListOptions) ([]gitlab.MilestoneEvent, Page, error) {
	var out []gitlab.MilestoneEvent
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/merge_requests/{}/resource_milestone_events",
		Args: []string{p.segment(), idArg(iid)}, Name: "list_merge_request_milestone_events"}, opts, &out)
	return out, page, err
}

// ListIssueWeightEvents lists an issue's weight events, oldest first.
// Weight is a paid feature: on a Free namespace the list is empty.
func (c *Client) ListIssueWeightEvents(ctx context.Context, p Project, iid int64, opts ListOptions) ([]gitlab.WeightEvent, Page, error) {
	var out []gitlab.WeightEvent
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/issues/{}/resource_weight_events",
		Args: []string{p.segment(), idArg(iid)}, Name: "list_issue_weight_events"}, opts, &out)
	return out, page, err
}

// GetFile reads a file at a ref. An empty ref reads HEAD, the default
// branch. The path is escaped once, slashes included, as GitLab expects.
func (c *Client) GetFile(ctx context.Context, p Project, path, ref string) (*gitlab.File, error) {
	if ref == "" {
		ref = "HEAD"
	}
	var out gitlab.File
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/repository/files/{}", Args: []string{p.segment(), path},
		Query: url.Values{"ref": {ref}}, Name: "get_file"}, &out)
	return &out, err
}

// TreeQuery selects a directory listing.
type TreeQuery struct {
	Path      string
	Ref       string
	Recursive bool
}

// ListTree lists a directory, paged by keyset.
func (c *Client) ListTree(ctx context.Context, p Project, q TreeQuery, opts ListOptions) ([]gitlab.TreeEntry, Page, error) {
	v := url.Values{"pagination": {"keyset"}}
	setString(v, "path", q.Path)
	setString(v, "ref", q.Ref)
	setBool(v, "recursive", q.Recursive)
	var out []gitlab.TreeEntry
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/repository/tree", Args: []string{p.segment()},
		Query: v, Name: "list_tree"}, opts, &out)
	return out, page, err
}

// ListBranches lists branches, optionally those matching search.
func (c *Client) ListBranches(ctx context.Context, p Project, search string, opts ListOptions) ([]gitlab.Branch, Page, error) {
	v := url.Values{}
	setString(v, "search", search)
	var out []gitlab.Branch
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/repository/branches", Args: []string{p.segment()},
		Query: v, Name: "list_branches"}, opts, &out)
	return out, page, err
}

// CommitQuery filters ListCommits.
type CommitQuery struct {
	Ref         string
	Path        string
	Author      string
	Since       time.Time
	Until       time.Time
	FirstParent bool
}

// ListCommits lists commits reachable from a ref.
func (c *Client) ListCommits(ctx context.Context, p Project, q CommitQuery, opts ListOptions) ([]gitlab.Commit, Page, error) {
	v := url.Values{}
	setString(v, "ref_name", q.Ref)
	setString(v, "path", q.Path)
	setString(v, "author", q.Author)
	setTime(v, "since", q.Since)
	setTime(v, "until", q.Until)
	setBool(v, "first_parent", q.FirstParent)
	var out []gitlab.Commit
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/repository/commits", Args: []string{p.segment()},
		Query: v, Name: "list_commits"}, opts, &out)
	return out, page, err
}

// GetCommit reads one commit with its line counts.
func (c *Client) GetCommit(ctx context.Context, p Project, sha string) (*gitlab.Commit, error) {
	var out gitlab.Commit
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/repository/commits/{}", Args: []string{p.segment(), sha},
		Query: url.Values{"stats": {"true"}}, Name: "get_commit"}, &out)
	return &out, err
}

// GetCommitDiff lists a commit's per-file diffs.
func (c *Client) GetCommitDiff(ctx context.Context, p Project, sha string, opts ListOptions) ([]gitlab.Diff, Page, error) {
	var out []gitlab.Diff
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/repository/commits/{}/diff",
		Args: []string{p.segment(), sha}, Name: "get_commit_diff"}, opts, &out)
	return out, page, err
}

// ListCommitMergeRequests lists the merge requests in the project that
// contain a commit.
func (c *Client) ListCommitMergeRequests(ctx context.Context, p Project, sha string, opts ListOptions) ([]gitlab.LinkedMergeRequest, Page, error) {
	var out []gitlab.LinkedMergeRequest
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/repository/commits/{}/merge_requests",
		Args: []string{p.segment(), sha}, Name: "list_commit_merge_requests"}, opts, &out)
	return out, page, err
}

// ListProtectedBranches lists a project's protected branch rules. A rule
// name may be a wildcard.
func (c *Client) ListProtectedBranches(ctx context.Context, p Project, opts ListOptions) ([]gitlab.ProtectedBranch, Page, error) {
	var out []gitlab.ProtectedBranch
	page, err := c.list(ctx, Call{Method: "GET", Path: "projects/{}/protected_branches", Args: []string{p.segment()},
		Name: "list_protected_branches"}, opts, &out)
	return out, page, err
}

// ------------------------------------------------------------ helpers

// idArg renders an iid, or a pipeline or job id; a non-positive one
// becomes "" and is refused by fillPath as empty.
func idArg(id int64) string {
	if id <= 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

func setString(v url.Values, k, s string) {
	if s != "" {
		v.Set(k, s)
	}
}

func setBool(v url.Values, k string, b bool) {
	if b {
		v.Set(k, "true")
	}
}

func setTime(v url.Values, k string, t time.Time) {
	if !t.IsZero() {
		v.Set(k, t.UTC().Format(time.RFC3339))
	}
}
