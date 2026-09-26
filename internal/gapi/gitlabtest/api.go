package gitlabtest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/gitlab"
)

// serveAPI authenticates, then routes a request under /api/v4.
func (s *Server) serveAPI(w http.ResponseWriter, r *http.Request, rest string) {
	tok, status, body := s.authenticate(r)
	if tok == nil {
		writeJSON(w, status, body)
		return
	}
	if r.Method != http.MethodGet && !tok.has("api") {
		w.Header().Set("WWW-Authenticate", `Bearer realm="GitLab", error="insufficient_scope", `+
			`error_description="The request requires higher privileges than provided by the access token.", scope="api"`)
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "insufficient_scope",
			"error_description": "The request requires higher privileges than provided by the access token.", "scope": "api"})
		return
	}
	seg, ok := segments(rest)
	if !ok {
		routeNotFound(w)
		return
	}
	user := tok.user
	s.mu.Lock()
	defer s.mu.Unlock()

	get := r.Method == http.MethodGet
	switch {
	case get && match(seg, "metadata"):
		writeJSON(w, http.StatusOK, map[string]any{"version": s.opts.Version, "revision": "0a1b2c3d4e5",
			"enterprise": s.opts.Enterprise, "kas": map[string]any{"enabled": false, "externalUrl": nil, "version": nil}})
	case get && match(seg, "user"):
		u := s.user(user)
		writeJSON(w, http.StatusOK, withExtra(gitlab.User{ID: u.ID, Username: u.Username, Name: u.Name, State: u.State,
			WebURL: u.WebURL, CreatedAt: &epoch}, map[string]any{"email": user + "@example.com", "two_factor_enabled": false}))
	case get && match(seg, "projects"):
		s.listProjects(w, r, user, nil)
	case get && match(seg, "issues"):
		s.listIssues(w, r, user, s.visibleProjects(user), "created_by_me")
	case get && match(seg, "merge_requests"):
		s.listMRs(w, r, user, s.visibleProjects(user), "created_by_me")
	case get && len(seg) == 3 && seg[0] == "groups":
		s.serveGroup(w, r, user, seg[1], seg[2])
	case len(seg) >= 2 && seg[0] == "projects":
		p, done := s.projectFor(w, r, seg[1], user)
		if done {
			return
		}
		s.serveProject(w, r, p, user, seg[2:])
	default:
		routeNotFound(w)
	}
}

func match(seg []string, want ...string) bool {
	if len(seg) != len(want) {
		return false
	}
	for i := range want {
		if want[i] != "*" && seg[i] != want[i] {
			return false
		}
	}
	return true
}

func (s *Server) serveGroup(w http.ResponseWriter, r *http.Request, user, id, what string) {
	g := s.findGroup(id)
	if g == nil {
		message(w, http.StatusNotFound, "404 Group Not Found")
		return
	}
	switch what {
	case "projects":
		s.listProjects(w, r, user, g)
	case "issues":
		s.listIssues(w, r, user, s.groupProjects(user, g, true), "all")
	case "merge_requests":
		s.listMRs(w, r, user, s.groupProjects(user, g, true), "all")
	default:
		routeNotFound(w)
	}
}

func (s *Server) serveProject(w http.ResponseWriter, r *http.Request, p *project, user string, seg []string) {
	get := r.Method == http.MethodGet
	switch {
	case get && len(seg) == 0:
		writeJSON(w, http.StatusOK, s.projectJSON(p))
	case get && match(seg, "issues"):
		s.listIssues(w, r, user, []*project{p}, "all")
	case len(seg) >= 2 && seg[0] == "issues":
		s.serveIssue(w, r, p, user, seg[1], seg[2:])
	case get && match(seg, "merge_requests"):
		s.listMRs(w, r, user, []*project{p}, "all")
	case get && len(seg) >= 2 && seg[0] == "merge_requests":
		s.serveMR(w, r, p, seg[1], seg[2:])
	case get && len(seg) >= 2 && seg[0] == "repository":
		s.serveRepository(w, r, p, seg[1:])
	case get && match(seg, "protected_branches"):
		if start, end, ok := s.offsetPage(w, r, len(p.protected), false); ok {
			writeJSON(w, http.StatusOK, p.protected[start:end])
		}
	default:
		routeNotFound(w)
	}
}

func (s *Server) serveIssue(w http.ResponseWriter, r *http.Request, p *project, user, iid string, rest []string) {
	iss := findIssue(p, iid)
	if iss == nil {
		message(w, http.StatusNotFound, "404 Issue Not Found")
		return
	}
	get := r.Method == http.MethodGet
	switch {
	case get && len(rest) == 0:
		writeJSON(w, http.StatusOK, iss)
	case get && match(rest, "discussions"):
		s.listDiscussions(w, r, p.discussions["issue:"+iid])
	case r.Method == http.MethodPost && match(rest, "notes"):
		s.createIssueNote(w, r, p, iss, user)
	default:
		routeNotFound(w)
	}
}

func (s *Server) serveMR(w http.ResponseWriter, r *http.Request, p *project, iid string, rest []string) {
	mr := findMR(p, iid)
	if mr == nil {
		message(w, http.StatusNotFound, "404 Merge Request Not Found")
		return
	}
	switch {
	case len(rest) == 0:
		writeJSON(w, http.StatusOK, mr)
	case match(rest, "approvals"):
		s.approvals(w, p, mr)
	case match(rest, "discussions"):
		s.listDiscussions(w, r, p.discussions["mr:"+iid])
	default:
		routeNotFound(w)
	}
}

// serveRepository serves GETs under /repository/.
func (s *Server) serveRepository(w http.ResponseWriter, r *http.Request, p *project, seg []string) {
	switch {
	case match(seg, "files", "*"):
		s.getFile(w, r, p, seg[1])
	case match(seg, "tree"):
		s.listTree(w, r, p)
	case match(seg, "branches"):
		s.listBranches(w, r, p)
	case match(seg, "commits"):
		s.listCommits(w, r, p)
	case match(seg, "commits", "*"):
		if c := findCommit(p, seg[1]); c != nil {
			writeJSON(w, http.StatusOK, c)
			return
		}
		message(w, http.StatusNotFound, "404 Commit Not Found")
	case match(seg, "commits", "*", "diff"):
		c := findCommit(p, seg[1])
		if c == nil {
			message(w, http.StatusNotFound, "404 Commit Not Found")
			return
		}
		diffs := p.diffs[c.ID]
		if start, end, ok := s.offsetPage(w, r, len(diffs), false); ok {
			writeJSON(w, http.StatusOK, diffs[start:end])
		}
	default:
		routeNotFound(w)
	}
}

// ------------------------------------------------------------ auth

func (s *Server) authenticate(r *http.Request) (*tokenRec, int, map[string]any) {
	auth := r.Header.Get("Authorization")
	bearer, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || bearer == "" {
		return nil, http.StatusUnauthorized, map[string]any{"message": "401 Unauthorized"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.oauth.access[bearer]
	switch {
	case t == nil || t.revoked:
		return nil, http.StatusUnauthorized, map[string]any{"error": "invalid_token", "error_description": "Token was revoked. You have to re-authorize from the user."}
	case !s.opts.Now().Before(t.created.Add(t.ttl)):
		return nil, http.StatusUnauthorized, map[string]any{"error": "invalid_token", "error_description": "Token is expired. You can either do re-authorization or token refresh."}
	}
	return t, 0, nil
}

// ------------------------------------------------------------ projects

func (s *Server) visible(p *project, user string) bool { return !p.private || p.members[user] }

func (s *Server) visibleProjects(user string) []*project {
	var out []*project
	for _, p := range s.projects {
		if s.visible(p, user) {
			out = append(out, p)
		}
	}
	return out
}

func (s *Server) findGroup(seg string) *group {
	for _, g := range s.groups {
		if strconv.FormatInt(g.id, 10) == seg || strings.EqualFold(g.path, seg) {
			return g
		}
	}
	return nil
}

func (s *Server) groupProjects(user string, g *group, subgroups bool) []*project {
	var out []*project
	for _, p := range s.visibleProjects(user) {
		if p.groupID == g.id || (subgroups && s.isAncestor(g.id, p.groupID)) {
			out = append(out, p)
		}
	}
	return out
}

func (s *Server) isAncestor(ancestor, id int64) bool {
	for _, g := range s.groups {
		if g.id == id && g.parentID != 0 {
			return g.parentID == ancestor || s.isAncestor(ancestor, g.parentID)
		}
	}
	return false
}

// projectFor resolves a project segment, answering 404 for a missing or
// private one and handling a moved one. done reports an answer written.
func (s *Server) projectFor(w http.ResponseWriter, r *http.Request, seg, user string) (*project, bool) {
	if to, ok := s.moved[strings.ToLower(seg)]; ok {
		if r.Method != http.MethodGet {
			message(w, http.StatusMethodNotAllowed, "405 Method Not Allowed")
			return nil, true
		}
		// The rest of the escaped path after the project segment.
		esc := r.URL.EscapedPath()
		prefix := "/api/v4/projects/"
		rest := esc[len(prefix):]
		if i := strings.Index(rest, "/"); i >= 0 {
			rest = rest[i:]
		} else {
			rest = ""
		}
		loc := s.URL + prefix + url.PathEscape(to) + rest
		if r.URL.RawQuery != "" {
			loc += "?" + r.URL.RawQuery
		}
		w.Header().Set("Location", loc)
		message(w, http.StatusMovedPermanently, "301 Moved Permanently")
		return nil, true
	}
	for _, p := range s.projects {
		if strconv.FormatInt(p.ID, 10) == seg || strings.EqualFold(p.PathWithNamespace, seg) {
			if s.visible(p, user) {
				return p, false
			}
			break
		}
	}
	message(w, http.StatusNotFound, "404 Project Not Found")
	return nil, true
}

func (s *Server) projectJSON(p *project) map[string]any {
	// A secret field GitLab has leaked through project reads (§3.19) is
	// served so a test can prove the client never decodes it.
	return withExtra(p.Project, map[string]any{
		"runners_token":                   "fixture-secret-never-decoded",
		"ssh_url_to_repo":                 "git@gitlab.example.com:" + p.PathWithNamespace + ".git",
		"container_registry_image_prefix": "registry.example.com/" + p.PathWithNamespace,
	})
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request, user string, g *group) {
	q := r.URL.Query()
	var list []*project
	if g == nil {
		list = s.visibleProjects(user)
	} else {
		list = s.groupProjects(user, g, q.Get("include_subgroups") == "true")
	}
	var rows []*project
	search := strings.ToLower(q.Get("search"))
	for _, p := range list {
		if search != "" && !strings.Contains(strings.ToLower(p.Name+" "+p.PathWithNamespace), search) {
			continue
		}
		if q.Get("membership") == "true" && !p.members[user] {
			continue
		}
		if q.Get("visibility") != "" && q.Get("visibility") != p.Visibility {
			continue
		}
		rows = append(rows, p)
	}
	desc := q.Get("sort") != "asc"
	sort.SliceStable(rows, func(i, j int) bool {
		if desc {
			return rows[i].ID > rows[j].ID
		}
		return rows[i].ID < rows[j].ID
	})

	if q.Get("pagination") == "keyset" {
		if q.Get("order_by") != "id" {
			message(w, http.StatusMethodNotAllowed, "Keyset pagination is not yet available for this type of request")
			return
		}
		after := int64(atoiDefault(q.Get("id_after"), 0))
		before := int64(atoiDefault(q.Get("id_before"), 0))
		var page []*project
		for _, p := range rows {
			if (after != 0 && p.ID <= after) || (before != 0 && p.ID >= before) {
				continue
			}
			page = append(page, p)
		}
		more := len(page) > perPageOf(r)
		page = page[:min(len(page), perPageOf(r))]
		if more {
			last := strconv.FormatInt(page[len(page)-1].ID, 10)
			if desc {
				s.keysetLink(w, r, map[string]string{"id_before": last})
			} else {
				s.keysetLink(w, r, map[string]string{"id_after": last})
			}
		}
		s.writeProjects(w, page)
		return
	}
	start, end, ok := s.offsetPage(w, r, len(rows), true)
	if ok {
		s.writeProjects(w, rows[start:end])
	}
}

func (s *Server) writeProjects(w http.ResponseWriter, rows []*project) {
	out := make([]map[string]any, 0, len(rows))
	for _, p := range rows {
		out = append(out, s.projectJSON(p))
	}
	writeJSON(w, http.StatusOK, out)
}

// ------------------------------------------------------------ issues, MRs

func findIssue(p *project, iid string) *gitlab.Issue {
	for _, i := range p.issues {
		if strconv.FormatInt(i.IID, 10) == iid {
			return i
		}
	}
	return nil
}

func findMR(p *project, iid string) *gitlab.MergeRequest {
	for _, m := range p.mrs {
		if strconv.FormatInt(m.IID, 10) == iid {
			return m
		}
	}
	return nil
}

// itemFilter is the part of an issue or merge request the list filters
// read.
type itemFilter struct {
	state, title, description, author string
	labels, assignees, reviewers      []string
	created, updated                  time.Time
	source, target                    string
	draft                             bool
}

func usernames(us []gitlab.UserBasic) []string {
	out := make([]string, 0, len(us))
	for _, u := range us {
		out = append(out, u.Username)
	}
	return out
}

func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// keep applies GitLab's list filters. defaultScope differs by endpoint:
// the instance-wide lists default to created_by_me.
func keep(q url.Values, it itemFilter, user, defaultScope string) bool {
	return keepFields(q, it) && keepTimes(q, it) && keepScope(q, it, user, defaultScope)
}

func keepFields(q url.Values, it itemFilter) bool {
	if l := q.Get("labels"); l != "" {
		for want := range strings.SplitSeq(l, ",") {
			if !has(it.labels, strings.TrimSpace(want)) {
				return false
			}
		}
	}
	equal := map[string]string{"state": it.state, "author_username": it.author,
		"source_branch": it.source, "target_branch": it.target, "draft": strconv.FormatBool(it.draft)}
	for key, v := range equal {
		want := q.Get(key)
		if key == "state" && want == "all" {
			continue
		}
		if want != "" && want != v {
			return false
		}
	}
	member := map[string][]string{"assignee_username": it.assignees, "reviewer_username": it.reviewers}
	for key, list := range member {
		if want := q.Get(key); want != "" && !has(list, want) {
			return false
		}
	}
	term := strings.ToLower(q.Get("search"))
	return term == "" || strings.Contains(strings.ToLower(it.title+"\n"+it.description), term)
}

func keepTimes(q url.Values, it itemFilter) bool {
	for key, t := range map[string]time.Time{"created_after": it.created, "updated_after": it.updated} {
		if v, err := time.Parse(time.RFC3339, q.Get(key)); err == nil && t.Before(v) {
			return false
		}
	}
	for key, t := range map[string]time.Time{"created_before": it.created, "updated_before": it.updated} {
		if v, err := time.Parse(time.RFC3339, q.Get(key)); err == nil && t.After(v) {
			return false
		}
	}
	return true
}

func keepScope(q url.Values, it itemFilter, user, defaultScope string) bool {
	scope := q.Get("scope")
	if scope == "" {
		scope = defaultScope
	}
	switch scope {
	case "created_by_me", "created-by-me":
		return it.author == user
	case "assigned_to_me", "assigned-to-me":
		return has(it.assignees, user)
	}
	return true
}

func sortByTime[T any](rows []T, q url.Values, created, updated func(T) time.Time) {
	key := created
	if q.Get("order_by") == "updated_at" {
		key = updated
	}
	asc := q.Get("sort") == "asc"
	sort.SliceStable(rows, func(i, j int) bool {
		if asc {
			return key(rows[i]).Before(key(rows[j]))
		}
		return key(rows[i]).After(key(rows[j]))
	})
}

func (s *Server) listIssues(w http.ResponseWriter, r *http.Request, user string, projects []*project, defaultScope string) {
	q := r.URL.Query()
	var rows []*gitlab.Issue
	for _, p := range projects {
		for _, i := range p.issues {
			if keep(q, itemFilter{state: i.State, title: i.Title, description: i.Description, author: i.Author.Username,
				labels: i.Labels, assignees: usernames(i.Assignees), created: i.CreatedAt, updated: i.UpdatedAt},
				user, defaultScope) {
				rows = append(rows, i)
			}
		}
	}
	sortByTime(rows, q, func(i *gitlab.Issue) time.Time { return i.CreatedAt }, func(i *gitlab.Issue) time.Time { return i.UpdatedAt })
	if start, end, ok := s.offsetPage(w, r, len(rows), false); ok {
		writeJSON(w, http.StatusOK, rows[start:end])
	}
}

func (s *Server) listMRs(w http.ResponseWriter, r *http.Request, user string, projects []*project, defaultScope string) {
	q := r.URL.Query()
	var rows []map[string]any
	var kept []*gitlab.MergeRequest
	for _, p := range projects {
		for _, m := range p.mrs {
			if keep(q, itemFilter{state: m.State, title: m.Title, description: m.Description, author: m.Author.Username,
				labels: m.Labels, assignees: usernames(m.Assignees), reviewers: usernames(m.Reviewers),
				created: m.CreatedAt, updated: m.UpdatedAt, source: m.SourceBranch, target: m.TargetBranch, draft: m.Draft},
				user, defaultScope) {
				kept = append(kept, m)
			}
		}
	}
	sortByTime(kept, q, func(m *gitlab.MergeRequest) time.Time { return m.CreatedAt },
		func(m *gitlab.MergeRequest) time.Time { return m.UpdatedAt })
	start, end, ok := s.offsetPage(w, r, len(kept), false)
	if !ok {
		return
	}
	for _, m := range kept[start:end] {
		// The list omits what only the single read carries.
		row := withExtra(m, nil)
		delete(row, "diff_refs")
		delete(row, "head_pipeline")
		rows = append(rows, row)
	}
	if rows == nil {
		rows = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) approvals(w http.ResponseWriter, p *project, mr *gitlab.MergeRequest) {
	a := *p.approvals[mr.IID]
	if a.ApprovedBy == nil {
		a.ApprovedBy = []gitlab.Approver{}
	}
	if s.opts.Enterprise {
		required, left := 1, 1
		if a.Approved {
			left = 0
		}
		a.ApprovalsRequired, a.ApprovalsLeft = &required, &left
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) listDiscussions(w http.ResponseWriter, r *http.Request, list []gitlab.Discussion) {
	if list == nil {
		list = []gitlab.Discussion{}
	}
	if start, end, ok := s.offsetPage(w, r, len(list), false); ok {
		writeJSON(w, http.StatusOK, list[start:end])
	}
}

// ------------------------------------------------------------ repository

// treeAt resolves a ref: a branch, HEAD, or a commit sha on a branch.
func treeAt(p *project, ref string) (map[string]string, string, bool) {
	if ref == "HEAD" || ref == "" {
		ref = p.DefaultBranch
	}
	if t, ok := p.trees[ref]; ok {
		return t, p.commits[ref][0].ID, true
	}
	for branch, commits := range p.commits {
		if len(commits) > 0 && (commits[0].ID == ref || commits[0].ShortID == ref) {
			return p.trees[branch], commits[0].ID, true
		}
	}
	return nil, "", false
}

func (s *Server) getFile(w http.ResponseWriter, r *http.Request, p *project, path string) {
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "ref is missing"})
		return
	}
	tree, head, ok := treeAt(p, ref)
	if !ok {
		message(w, http.StatusNotFound, "404 Commit Not Found")
		return
	}
	content, ok := tree[path]
	if !ok {
		message(w, http.StatusNotFound, "404 File Not Found")
		return
	}
	sum := sha256.Sum256([]byte(content))
	name := path[strings.LastIndex(path, "/")+1:]
	writeJSON(w, http.StatusOK, gitlab.File{FileName: name, FilePath: path, Size: int64(len(content)), Encoding: "base64",
		Content: base64.StdEncoding.EncodeToString([]byte(content)), ContentSHA256: hex.EncodeToString(sum[:]),
		Ref: ref, BlobID: fakeSHA("blob", content), CommitID: head, LastCommitID: head})
}

func (s *Server) listTree(w http.ResponseWriter, r *http.Request, p *project) {
	q := r.URL.Query()
	tree, _, ok := treeAt(p, q.Get("ref"))
	if !ok {
		message(w, http.StatusNotFound, "404 Tree Not Found")
		return
	}
	dir := strings.Trim(q.Get("path"), "/")
	recursive := q.Get("recursive") == "true"
	entries := map[string]gitlab.TreeEntry{}
	for full := range tree {
		rel := full
		if dir != "" {
			var found bool
			if rel, found = strings.CutPrefix(full, dir+"/"); !found {
				continue
			}
		}
		parts := strings.Split(rel, "/")
		prefix := dir
		for i, part := range parts {
			entry := part
			if prefix != "" {
				entry = prefix + "/" + part
			}
			if i == len(parts)-1 {
				entries[entry] = gitlab.TreeEntry{ID: fakeSHA("blob", entry, tree[full]), Name: part, Type: "blob", Path: entry, Mode: "100644"}
			} else if _, ok := entries[entry]; !ok {
				entries[entry] = gitlab.TreeEntry{ID: fakeSHA("tree", entry), Name: part, Type: "tree", Path: entry, Mode: "040000"}
			}
			if !recursive {
				break
			}
			prefix = entry
		}
	}
	if dir != "" && len(entries) == 0 {
		message(w, http.StatusNotFound, "404 Tree Not Found")
		return
	}
	rows := make([]gitlab.TreeEntry, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, e)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Type != rows[j].Type {
			return rows[i].Type == "tree"
		}
		return rows[i].Path < rows[j].Path
	})
	if q.Get("pagination") == "keyset" {
		start := 0
		if tok := q.Get("page_token"); tok != "" {
			start = -1
			for i, e := range rows {
				if e.ID == tok {
					start = i + 1
				}
			}
			if start < 0 {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid page token"})
				return
			}
		}
		end := min(start+perPageOf(r), len(rows))
		if end < len(rows) {
			s.keysetLink(w, r, map[string]string{"page_token": rows[end-1].ID})
		}
		writeJSON(w, http.StatusOK, rows[start:end])
		return
	}
	if start, end, ok := s.offsetPage(w, r, len(rows), false); ok {
		writeJSON(w, http.StatusOK, rows[start:end])
	}
}

func (s *Server) listBranches(w http.ResponseWriter, r *http.Request, p *project) {
	term := r.URL.Query().Get("search")
	var rows []gitlab.Branch
	for _, b := range p.branches {
		if term == "" || strings.Contains(b.Name, term) {
			rows = append(rows, b)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	if rows == nil {
		rows = []gitlab.Branch{}
	}
	if start, end, ok := s.offsetPage(w, r, len(rows), false); ok {
		writeJSON(w, http.StatusOK, rows[start:end])
	}
}

func findCommit(p *project, sha string) *gitlab.Commit {
	for _, commits := range p.commits {
		for _, c := range commits {
			if c.ID == sha || (len(sha) >= 7 && strings.HasPrefix(c.ID, sha)) {
				copied := c
				return &copied
			}
		}
	}
	return nil
}

func (s *Server) listCommits(w http.ResponseWriter, r *http.Request, p *project) {
	q := r.URL.Query()
	ref := q.Get("ref_name")
	if ref == "" {
		ref = p.DefaultBranch
	}
	commits, ok := p.commits[ref]
	if !ok {
		message(w, http.StatusNotFound, "404 Commit Not Found")
		return
	}
	since, _ := time.Parse(time.RFC3339, q.Get("since"))
	until, _ := time.Parse(time.RFC3339, q.Get("until"))
	path := q.Get("path")
	var rows []gitlab.Commit
	for _, c := range commits {
		if (!since.IsZero() && c.CommittedDate.Before(since)) || (!until.IsZero() && c.CommittedDate.After(until)) {
			continue
		}
		if a := q.Get("author"); a != "" && !strings.Contains(c.AuthorName+" "+c.AuthorEmail, a) {
			continue
		}
		if path != "" && !touches(p.diffs[c.ID], path) {
			continue
		}
		c.Stats = nil // only the single read carries stats
		rows = append(rows, c)
	}
	if rows == nil {
		rows = []gitlab.Commit{}
	}
	if start, end, ok := s.offsetPage(w, r, len(rows), false); ok {
		writeJSON(w, http.StatusOK, rows[start:end])
	}
}

func touches(diffs []gitlab.Diff, path string) bool {
	for _, d := range diffs {
		if d.NewPath == path || strings.HasPrefix(d.NewPath, strings.TrimSuffix(path, "/")+"/") {
			return true
		}
	}
	return false
}

// Issue returns a copy of an issue, for a test to check what a write did.
func (s *Server) Issue(projectPath string, iid int64) (gitlab.Issue, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.projects {
		if p.PathWithNamespace == projectPath {
			if i := findIssue(p, strconv.FormatInt(iid, 10)); i != nil {
				return *i, true
			}
		}
	}
	return gitlab.Issue{}, false
}

// ProjectID returns a fixture project's numeric id.
func (s *Server) ProjectID(path string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.projects {
		if p.PathWithNamespace == path {
			return p.ID
		}
	}
	panic(fmt.Sprintf("gitlabtest: no project %q", path))
}
