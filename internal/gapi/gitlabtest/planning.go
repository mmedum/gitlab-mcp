package gitlabtest

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/gitlab"
)

// The planning half of the instance: labels, milestones, members, users,
// to-do items and search.

// Fixture ids tests address.
const (
	// MilestoneActive and MilestoneClosed are ProjectAlpha's; the group
	// milestone is GroupTop's.
	MilestoneActive = 90001
	MilestoneClosed = 90002
	MilestoneGroup  = 90003
	// GroupLabel is a label GroupTop defines for every project under it.
	GroupLabel = "group-wide"
	// TodoAssigned, TodoReview and TodoDone are alice's to-do items.
	TodoAssigned = 95001
	TodoReview   = 95002
	TodoDone     = 95003
)

// todo is one user's to-do item.
type todo struct {
	user string
	gitlab.Todo
}

// searchNeedsAdvanced are the scopes GitLab searches across a group or
// the instance only with advanced search, which this instance lacks.
var searchNeedsAdvanced = []string{"blobs", "commits", "notes", "wiki_blobs"}

// fillPlanning gives ProjectAlpha labels and milestones, GroupTop a label,
// a milestone and an owner, and alice to-do items.
func (s *Server) fillPlanning(p *project) {
	count := func(label string, state string) *int {
		n := 0
		for _, i := range p.issues {
			if slices.Contains(i.Labels, label) && i.State == state {
				n++
			}
		}
		return &n
	}
	for i, name := range []string{"bug", "feature", "docs", "priority::high"} {
		mrs := 0
		for _, m := range p.mrs {
			if slices.Contains(m.Labels, name) {
				mrs++
			}
		}
		l := gitlab.Label{ID: int64(96001 + i), Name: name, Color: "#428bca", Description: "Generated label " + name + ".",
			OpenIssuesCount: count(name, "opened"), ClosedIssuesCount: count(name, "closed"), OpenMergeRequestsCount: &mrs,
			IsProjectLabel: true}
		if name == "priority::high" {
			one := 1
			l.Priority = &one
		}
		p.labels = append(p.labels, l)
	}
	zero := 0
	s.groupLabels = map[int64][]gitlab.Label{firstGroupID: {{ID: 96100, Name: GroupLabel, Color: "#ff0000",
		OpenIssuesCount: &zero, ClosedIssuesCount: &zero, OpenMergeRequestsCount: &zero}}}

	no, yes := false, true
	at := p.CreatedAt.Add(24 * time.Hour)
	p.milestones = []gitlab.ProjectMilestone{
		{ID: MilestoneActive, IID: 2, Title: "Sprint 2", State: "active", StartDate: "2026-01-12", DueDate: "2026-01-23",
			Expired: &no, UpdatedAt: at, WebURL: p.WebURL + "/-/milestones/2"},
		{ID: MilestoneClosed, IID: 1, Title: "Sprint 1", State: "closed", StartDate: "2025-12-29", DueDate: "2026-01-09",
			Expired: &yes, UpdatedAt: at, WebURL: p.WebURL + "/-/milestones/1"},
	}
	s.groupMilestones = map[int64][]gitlab.ProjectMilestone{firstGroupID: {{ID: MilestoneGroup, IID: 1, Title: "Q1 goals",
		State: "active", DueDate: "2026-03-31", Expired: &no, UpdatedAt: at, WebURL: s.URL + "/groups/" + GroupTop + "/-/milestones/1"}}}
	s.groupLevels = map[int64]map[string]int{firstGroupID: {"carol": 50}}

	alpha := &gitlab.TodoProject{ID: p.ID, PathWithNamespace: p.PathWithNamespace}
	iss, mr := p.issues[1], p.mrs[0]
	s.todos = []todo{
		{user: "alice", Todo: gitlab.Todo{ID: TodoAssigned, Project: alpha, Author: s.user("bob"), ActionName: "assigned",
			TargetType: "Issue", Target: gitlab.TodoTarget{IID: iss.IID, Title: iss.Title, State: iss.State},
			TargetURL: iss.WebURL, Body: iss.Title, State: "pending", CreatedAt: iss.CreatedAt}},
		{user: "alice", Todo: gitlab.Todo{ID: TodoReview, Project: alpha, Author: s.user("carol"), ActionName: "review_requested",
			TargetType: "MergeRequest", Target: gitlab.TodoTarget{IID: mr.IID, Title: mr.Title, State: mr.State},
			TargetURL: mr.WebURL, Body: mr.Title, State: "pending", CreatedAt: mr.CreatedAt}},
		{user: "alice", Todo: gitlab.Todo{ID: TodoDone, Project: alpha, Author: s.user("bob"), ActionName: "mentioned",
			TargetType: "Issue", Target: gitlab.TodoTarget{IID: p.issues[0].IID, Title: p.issues[0].Title},
			TargetURL: p.issues[0].WebURL, Body: "@alice can you look?", State: "done", CreatedAt: p.issues[0].CreatedAt}},
		{user: "alice", Todo: gitlab.Todo{ID: TodoDone + 2, Project: alpha, Author: s.user("bob"), ActionName: "mentioned",
			TargetType: "Commit", Target: gitlab.TodoTarget{Title: "Update README.md"}, State: "done", CreatedAt: p.CreatedAt}},
		{user: "bob", Todo: gitlab.Todo{ID: TodoDone + 1, Project: alpha, Author: s.user("alice"), ActionName: "assigned",
			TargetType: "Issue", Target: gitlab.TodoTarget{IID: 1}, State: "pending", CreatedAt: p.CreatedAt}},
	}
}

// servePlanning serves a project's labels, milestones, members and
// search; it reports whether the path was one of them.
func (s *Server) servePlanning(w http.ResponseWriter, r *http.Request, p *project, seg []string) bool {
	q := r.URL.Query()
	switch {
	case match(seg, "labels"):
		rows := slices.Clone(p.labels)
		if q.Get("include_ancestor_groups") != "false" {
			for g := p.groupID; g != 0; g = s.parentOf(g) {
				rows = append(rows, s.groupLabels[g]...)
			}
		}
		term := strings.ToLower(q.Get("search"))
		rows = slices.DeleteFunc(rows, func(l gitlab.Label) bool {
			return term != "" && !strings.Contains(strings.ToLower(l.Name+" "+l.Description), term)
		})
		out := make([]map[string]any, 0, len(rows))
		for _, l := range rows {
			row := withExtra(l, nil)
			if q.Get("with_counts") != "true" {
				delete(row, "open_issues_count")
				delete(row, "closed_issues_count")
				delete(row, "open_merge_requests_count")
			}
			out = append(out, row)
		}
		writePage(s, w, r, out)
	case match(seg, "milestones"):
		rows := slices.Clone(p.milestones)
		if q.Get("include_ancestors") == "true" {
			for g := p.groupID; g != 0; g = s.parentOf(g) {
				rows = append(rows, s.groupMilestones[g]...)
			}
		}
		writePage(s, w, r, filterMilestones(rows, q.Get("state"), q.Get("search"), q.Get("title")))
	case match(seg, "members", "all"):
		writePage(s, w, r, s.members(p, q.Get("query")))
	case match(seg, "search"):
		s.search(w, r, []*project{p}, "project")
	default:
		return false
	}
	return true
}

// servePlanningTop serves the instance-wide users, to-do items and
// search; it reports whether the path was one of them.
func (s *Server) servePlanningTop(w http.ResponseWriter, r *http.Request, user string, seg []string) bool {
	switch {
	case match(seg, "users"):
		s.listUsers(w, r)
	case match(seg, "todos"):
		s.listTodos(w, r, user)
	case match(seg, "search"):
		s.search(w, r, s.visibleProjects(user), "instance")
	default:
		return false
	}
	return true
}

func (s *Server) parentOf(id int64) int64 {
	for _, g := range s.groups {
		if g.id == id {
			return g.parentID
		}
	}
	return 0
}

func filterMilestones(rows []gitlab.ProjectMilestone, state, search, title string) []gitlab.ProjectMilestone {
	return slices.DeleteFunc(rows, func(m gitlab.ProjectMilestone) bool {
		return (state != "" && m.State != state) || (title != "" && m.Title != title) ||
			(search != "" && !strings.Contains(strings.ToLower(m.Title), strings.ToLower(search)))
	})
}

// members is everyone with access to a project: its own members, and the
// members of every group above it, the higher level winning.
func (s *Server) members(p *project, query string) []gitlab.Member {
	levels := map[string]int{}
	for u, l := range p.levels {
		levels[u] = l
	}
	for g := p.groupID; g != 0; g = s.parentOf(g) {
		for u, l := range s.groupLevels[g] {
			levels[u] = max(levels[u], l)
		}
	}
	var out []gitlab.Member
	for _, name := range Users {
		l, ok := levels[name]
		if !ok {
			continue
		}
		u := s.user(name)
		if query != "" && !strings.Contains(strings.ToLower(u.Username+" "+u.Name), strings.ToLower(query)) {
			continue
		}
		out = append(out, gitlab.Member{ID: u.ID, Username: u.Username, Name: u.Name, State: u.State, AccessLevel: l})
	}
	return out
}

func (s *Server) serveGroupPlanning(w http.ResponseWriter, r *http.Request, user string, g *group, what string) bool {
	q := r.URL.Query()
	switch what {
	case "milestones":
		rows := slices.Clone(s.groupMilestones[g.id])
		if q.Get("include_ancestors") == "true" {
			for a := g.parentID; a != 0; a = s.parentOf(a) {
				rows = append(rows, s.groupMilestones[a]...)
			}
		}
		writePage(s, w, r, filterMilestones(rows, q.Get("state"), q.Get("search"), q.Get("title")))
	case "search":
		s.search(w, r, s.groupProjects(user, g, true), "group")
	default:
		return false
	}
	return true
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rows := make([]gitlab.UserBasic, 0, len(Users))
	for _, name := range Users {
		u := s.user(name)
		switch {
		case q.Get("username") != "" && !strings.EqualFold(q.Get("username"), u.Username):
			continue
		case q.Get("search") != "" && !strings.Contains(strings.ToLower(u.Username+" "+u.Name), strings.ToLower(q.Get("search"))):
			continue
		}
		rows = append(rows, u)
	}
	writePage(s, w, r, rows)
}

func (s *Server) listTodos(w http.ResponseWriter, r *http.Request, user string) {
	q := r.URL.Query()
	state := q.Get("state")
	if state == "" {
		state = "pending"
	}
	var rows []map[string]any
	for i := len(s.todos) - 1; i >= 0; i-- {
		t := s.todos[i]
		switch {
		case t.user != user, t.State != state:
			continue
		case q.Get("project_id") != "" && (t.Project == nil || strconv.FormatInt(t.Project.ID, 10) != q.Get("project_id")):
			continue
		case q.Get("action") != "" && t.ActionName != q.Get("action"):
			continue
		case q.Get("type") != "" && t.TargetType != q.Get("type"):
			continue
		}
		row := withExtra(t.Todo, nil)
		if t.TargetType == "Commit" {
			// GitLab gives a commit target its SHA as its id.
			row["target"] = map[string]any{"id": fakeSHA("todo-commit"), "title": t.Target.Title}
		}
		rows = append(rows, row)
	}
	writePage(s, w, r, rows)
}

// search answers a search over projects. where is instance, group or
// project; code, commits, comments and wiki pages across a group or the
// instance need advanced search and are refused as GitLab refuses them.
func (s *Server) search(w http.ResponseWriter, r *http.Request, projects []*project, where string) {
	q := r.URL.Query()
	scope, term := q.Get("scope"), strings.ToLower(q.Get("search"))
	if term == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "search is missing"})
		return
	}
	if where != "project" && slices.Contains(searchNeedsAdvanced, scope) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Scope supported only with advanced search or exact code search"})
		return
	}
	matches := func(text string) bool { return strings.Contains(strings.ToLower(text), term) }
	var rows []any
	for _, p := range projects {
		hits, ok := s.scopeHits(p, q, where, matches)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "scope does not have a valid value"})
			return
		}
		rows = append(rows, hits...)
	}
	if where == "instance" && scope == "users" {
		rows = nil
		for _, name := range Users {
			if u := s.user(name); matches(u.Username + " " + u.Name) {
				rows = append(rows, u)
			}
		}
	}
	writePage(s, w, r, rows)
}

// scopeHits is one project's matches for a search's scope; ok is false
// for a scope GitLab does not take.
func (s *Server) scopeHits(p *project, q url.Values, where string, matches func(string) bool) ([]any, bool) {
	var rows []any
	state := q.Get("state")
	switch q.Get("scope") {
	case "issues":
		for _, i := range p.issues {
			if matches(i.Title+"\n"+i.Description) && (state == "" || state == i.State) {
				rows = append(rows, i)
			}
		}
	case "merge_requests":
		for _, m := range p.mrs {
			if matches(m.Title+"\n"+m.Description) && (state == "" || state == m.State) {
				row := withExtra(m, nil)
				delete(row, "diff_refs")
				delete(row, "head_pipeline")
				rows = append(rows, row)
			}
		}
	case "projects":
		if where != "project" && matches(p.Name+" "+p.PathWithNamespace) {
			rows = append(rows, s.projectJSON(p))
		}
	case "milestones":
		for _, m := range p.milestones {
			if matches(m.Title) {
				rows = append(rows, withExtra(m, map[string]any{"project_id": p.ID}))
			}
		}
	case "blobs":
		rows = blobHits(p, q.Get("ref"), matches)
	case "commits":
		rows = commitHits(p, q.Get("ref"), matches)
	case "notes":
		rows = noteHits(p, matches)
	case "wiki_blobs":
		// No project here has a wiki.
	case "users":
		for _, m := range s.members(p, "") {
			if matches(m.Username + " " + m.Name) {
				rows = append(rows, s.user(m.Username))
			}
		}
	default:
		return nil, false
	}
	return rows, true
}

func commitHits(p *project, ref string, matches func(string) bool) []any {
	if ref == "" {
		ref = p.DefaultBranch
	}
	commits, _ := history(p, ref)
	var out []any
	for _, c := range commits {
		if matches(c.Message) {
			c.Stats = nil
			out = append(out, withExtra(c, map[string]any{"project_id": p.ID}))
		}
	}
	return out
}

func noteHits(p *project, matches func(string) bool) []any {
	var out []any
	for _, list := range p.discussions {
		for _, d := range list {
			for _, n := range d.Notes {
				if !n.System && matches(n.Body) {
					out = append(out, withExtra(n, map[string]any{"project_id": p.ID}))
				}
			}
		}
	}
	return out
}

// blobHits finds the lines of a project's files at a ref that match, as
// GitLab's blob search reports them: the path, the ref, the line the
// excerpt starts at and the excerpt, one line either side.
func blobHits(p *project, ref string, matches func(string) bool) []any {
	tree, _, ok := treeAt(p, ref)
	if !ok {
		return nil
	}
	if ref == "" {
		ref = p.DefaultBranch
	}
	var paths []string
	for path := range tree {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	var out []any
	for _, path := range paths {
		lines := strings.Split(tree[path], "\n")
		for i, line := range lines {
			if !matches(line) {
				continue
			}
			from := max(0, i-1)
			to := min(len(lines), i+2)
			out = append(out, map[string]any{"basename": strings.TrimSuffix(path, pathExt(path)), "data": strings.Join(lines[from:to], "\n"),
				"path": path, "filename": path, "id": nil, "ref": ref, "startline": from + 1, "project_id": p.ID})
			break
		}
	}
	return out
}

func pathExt(path string) string {
	base := path[strings.LastIndex(path, "/")+1:]
	if i := strings.LastIndex(base, "."); i > 0 {
		return base[i:]
	}
	return ""
}
