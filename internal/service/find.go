package service

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/internal/instance"
	"github.com/mmedum/gitlab-mcp/internal/model"
	"github.com/mmedum/gitlab-mcp/internal/render"
)

type gitlabUser = gitlab.UserBasic

// ---------------------------------------------------------- resolve_url

// looksLikeSHA is a commit id, full or abbreviated.
var looksLikeSHA = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// ResolveURL turns a web URL on the configured instance into tool
// arguments (§6.1). It needs the network only when a file or tree URL's
// ref has slashes in it and could end anywhere: then one branch listing
// settles where the ref stops and the path starts.
func (s *Service) ResolveURL(ctx context.Context, raw string) (model.Resolved, error) {
	if s.inst.IsZero() {
		return model.Resolved{}, gapi.Errf(gapi.ClassInvalid, "no GitLab instance is configured")
	}
	ref, err := s.inst.ResolveURL(raw)
	if err != nil {
		return model.Resolved{}, gapi.AsError(err)
	}
	out := model.Resolved{Kind: string(ref.Kind), Project: ref.Project, SHA: ref.SHA, Ref: ref.Ref, Path: ref.Path,
		From: ref.From, To: ref.To, Slug: ref.Slug, RefCandidates: []model.RefSplit{}, Arguments: map[string]any{}}
	if ref.IID != 0 {
		out.IID = &ref.IID
	}
	if ref.ID != 0 {
		out.ID = &ref.ID
	}
	if ref.Line != 0 {
		out.Line = &ref.Line
	}
	if ref.EndLine != 0 {
		out.EndLine = &ref.EndLine
	}
	if ref.Note != 0 {
		out.Note = &ref.Note
	}
	if cands := ref.RefPathCandidates(); len(cands) > 1 {
		chosen, rest, err := s.splitRef(ctx, ref.Project, cands)
		if err != nil {
			return model.Resolved{}, err
		}
		out.Ref, out.Path = chosen.Ref, chosen.Path
		out.RefCandidates = append(out.RefCandidates, rest...)
	}
	s.suggest(&out)
	return out, nil
}

// splitRef picks the split whose ref is a branch, the longest when
// several are. With none, a SHA-shaped first segment is taken as the
// ref; otherwise the shortest split is returned with the others listed,
// since a tag or an unknown ref cannot be told apart without more reads.
func (s *Service) splitRef(ctx context.Context, project string, cands []instance.RefPath) (model.RefSplit, []model.RefSplit, error) {
	first := cands[0]
	if looksLikeSHA.MatchString(first.Ref) {
		return model.RefSplit{Ref: first.Ref, Path: first.Path}, nil, nil
	}
	var branches []gitlab.Branch
	if c, err := s.api(); err == nil {
		if p, perr := gapi.ParseProject(project); perr == nil {
			branches, _, err = c.ListBranches(ctx, p, first.Ref, gapi.ListOptions{PerPage: gapi.MaxPerPage})
			if err != nil && !soft(err) {
				return model.RefSplit{}, nil, err
			}
		}
	}
	for i := len(cands) - 1; i >= 0; i-- {
		if slices.ContainsFunc(branches, func(b gitlab.Branch) bool { return b.Name == cands[i].Ref }) {
			return model.RefSplit{Ref: cands[i].Ref, Path: cands[i].Path}, nil, nil
		}
	}
	rest := make([]model.RefSplit, 0, len(cands)-1)
	for _, c := range cands[1:] {
		rest = append(rest, model.RefSplit{Ref: c.Ref, Path: c.Path})
	}
	return model.RefSplit{Ref: first.Ref, Path: first.Path}, rest, nil
}

// suggest names the registered tool that reads what the URL points at,
// with its arguments. A kind no registered tool reads gets none, so the
// answer never sends the caller to a tool that is not there.
func (s *Service) suggest(r *model.Resolved) {
	args := map[string]any{"project": r.Project}
	var tool string
	switch instance.Kind(r.Kind) {
	case instance.KindIssue:
		tool, args["iid"] = "get_issue", *r.IID
	case instance.KindMergeRequest:
		tool, args["iid"] = "get_merge_request", *r.IID
	case instance.KindFile:
		tool, args["path"], args["ref"] = "get_file", r.Path, r.Ref
	case instance.KindCommit:
		tool, args["sha"] = "get_commit", r.SHA
	case instance.KindCompare:
		tool, args["from"], args["to"] = "compare_refs", r.From, r.To
	case instance.KindPipeline:
		if r.ID != nil {
			tool, args["pipeline_id"] = "get_pipeline", *r.ID
		}
	case instance.KindJob:
		if r.ID != nil {
			tool, args["job_id"] = "get_job_log", *r.ID
		}
	case instance.KindProject:
		tool = "get_project"
		if r.Ref != "" {
			tool, args["ref"] = "list_tree", r.Ref
			if r.Path != "" {
				args["path"] = r.Path
			}
		}
	}
	if tool == "" || !s.isRegistered(tool) {
		return
	}
	r.Tool, r.Arguments = tool, args
}

// ------------------------------------------------------------- projects

// ProjectSearch is search_projects' query.
type ProjectSearch struct {
	Search           string
	Group            string
	IncludeSubgroups bool
	// Scope is member (default), owned, starred or all.
	Scope           string
	Visibility      string
	IncludeArchived bool
	List            gapi.ListOptions
}

// SearchProjects lists projects.
func (s *Service) SearchProjects(ctx context.Context, q ProjectSearch) (model.ProjectList, error) {
	c, err := s.api()
	if err != nil {
		return model.ProjectList{}, err
	}
	pq := gapi.ProjectQuery{Search: q.Search, IncludeSubgroups: q.IncludeSubgroups, Visibility: q.Visibility,
		OrderBy: "last_activity_at"}
	switch q.Scope {
	case "", "member":
		pq.Membership = true
	case "owned":
		pq.Owned = true
	case "starred":
		pq.Starred = true
	}
	if !q.IncludeArchived {
		f := false
		pq.Archived = &f
	}
	if q.Group != "" {
		if pq.Group, err = gapi.ParseGroup(q.Group); err != nil {
			return model.ProjectList{}, err
		}
	}
	rows, page, err := c.SearchProjects(ctx, pq, q.List)
	if err != nil {
		return model.ProjectList{}, err
	}
	out := model.ProjectList{Projects: make([]model.ProjectRow, 0, len(rows)), Listing: listing(len(rows), page)}
	for _, p := range rows {
		name, _ := render.Line(p.Name, render.TitleChars)
		out.Projects = append(out.Projects, model.ProjectRow{ID: p.ID, Path: p.PathWithNamespace, Visibility: p.Visibility,
			DefaultBranch: p.DefaultBranch, Archived: p.Archived, LastActivityAt: p.LastActivityAt, UntrustedName: name})
	}
	return out, nil
}

// GetProject reads one project.
func (s *Service) GetProject(ctx context.Context, raw string) (model.Project, error) {
	c, err := s.api()
	if err != nil {
		return model.Project{}, err
	}
	p, err := s.parseProject(raw)
	if err != nil {
		return model.Project{}, err
	}
	proj, err := c.GetProject(ctx, p)
	if err != nil {
		return model.Project{}, err
	}
	name, _ := render.Line(proj.Name, render.TitleChars)
	desc, _ := render.Markdown(proj.Description, s.self())
	desc, _ = render.Cut(desc, 0, render.DescriptionBudget)
	return model.Project{
		Project: projectRef(ctx, proj.ID, proj.PathWithNamespace), WebURL: proj.WebURL, Visibility: proj.Visibility,
		DefaultBranch: proj.DefaultBranch, Archived: proj.Archived, EmptyRepo: proj.EmptyRepo, Topics: proj.Topics,
		Stars: proj.StarCount, Forks: proj.ForksCount, OpenIssues: proj.OpenIssuesCount, CreatedAt: proj.CreatedAt,
		LastActivityAt: proj.LastActivityAt, Namespace: proj.Namespace.FullPath, NamespaceKind: proj.Namespace.Kind,
		UntrustedName: name, UntrustedDescription: desc,
	}, nil
}

// ------------------------------------------------------ issue searches

// ItemSearch is search_issues' and search_merge_requests' query. At
// most one of Project and Group is set.
type ItemSearch struct {
	Project string
	Group   string
	Query   gapi.ItemQuery
	List    gapi.ListOptions
}

// SearchIssues lists issues.
func (s *Service) SearchIssues(ctx context.Context, q ItemSearch) (model.ItemList, error) {
	if err := s.scopeSearch(&q); err != nil {
		return model.ItemList{}, err
	}
	rows, page, err := s.client.SearchIssues(ctx, q.Query, q.List)
	if err != nil {
		return model.ItemList{}, err
	}
	out := model.ItemList{Items: make([]model.ItemRow, 0, len(rows)), Listing: listing(len(rows), page)}
	for _, it := range rows {
		out.Items = append(out.Items, issueRow(it))
	}
	return out, nil
}

// SearchMergeRequests lists merge requests.
func (s *Service) SearchMergeRequests(ctx context.Context, q ItemSearch) (model.ItemList, error) {
	if err := s.scopeSearch(&q); err != nil {
		return model.ItemList{}, err
	}
	rows, page, err := s.client.SearchMergeRequests(ctx, q.Query, q.List)
	if err != nil {
		return model.ItemList{}, err
	}
	out := model.ItemList{Items: make([]model.ItemRow, 0, len(rows)), Listing: listing(len(rows), page)}
	for _, it := range rows {
		out.Items = append(out.Items, mrRow(it))
	}
	return out, nil
}

// scopeSearch sets the query's project or group.
func (s *Service) scopeSearch(q *ItemSearch) error {
	if _, err := s.api(); err != nil {
		return err
	}
	if q.Project != "" && q.Group != "" {
		return gapi.Errf(gapi.ClassInvalid, "pass project or group, not both")
	}
	if q.Project != "" {
		p, err := s.parseProject(q.Project)
		if err != nil {
			return err
		}
		q.Query.Project = p
	}
	if q.Group != "" {
		g, err := gapi.ParseGroup(q.Group)
		if err != nil {
			return err
		}
		q.Query.Group = g
	}
	return nil
}

func issueRow(it gitlab.Issue) model.ItemRow {
	return baseRow(model.ItemRow{IID: it.IID, Reference: it.References.Full, State: it.State, Author: it.Author.Username,
		Labels: it.Labels, CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt}, it.ProjectID, it.Title)
}

func mrRow(mr gitlab.MergeRequest) model.ItemRow {
	return baseRow(model.ItemRow{IID: mr.IID, Reference: mr.References.Full, State: mr.State, Draft: mr.Draft,
		Author: mr.Author.Username, Labels: mr.Labels, CreatedAt: mr.CreatedAt, UpdatedAt: mr.UpdatedAt}, mr.ProjectID, mr.Title)
}

// baseRow finishes a search row the same way for issues and merge
// requests: the project named by id and by the path in the reference,
// the title prepared, and labels never null.
func baseRow(r model.ItemRow, projectID int64, title string) model.ItemRow {
	path := r.Reference
	if i := strings.LastIndexAny(path, "#!"); i > 0 {
		path = path[:i]
	}
	r.Project = model.ProjectRef{ID: projectID, Path: path}
	r.UntrustedTitle, _ = render.Line(title, render.TitleChars)
	r.Labels = nonNil(r.Labels)
	return r
}
