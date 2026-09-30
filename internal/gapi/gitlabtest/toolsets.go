package gitlabtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
)

// The optional toolsets' half of the instance: a project wiki, snippets,
// releases, environments and deployments, and events. Everything is
// generated here, as the rest of the instance is (§9.1).

// Fixture ids and names tests address.
const (
	// SnippetAlpha is bob's snippet in ProjectAlpha, two files long.
	SnippetAlpha = 80001
	// SnippetPersonal is alice's private personal snippet.
	SnippetPersonal = 80002
	// WikiNested is a wiki page whose slug holds a slash.
	WikiNested = "guides/setup"
)

// snippet is a snippet and its files, in order.
type snippet struct {
	gitlab.Snippet
	files [][2]string // path, content
}

// event is an event and the action it filters by, which differs from
// the name GitLab shows ("created" is shown as "opened").
type event struct {
	gitlab.Event
	action string
}

// fillToolsets gives ProjectAlpha wiki pages, a snippet, a release,
// environments, deployments and events, and alice a personal snippet.
func (s *Server) fillToolsets(p *project) {
	p.wiki = []gitlab.WikiPage{
		{Format: "markdown", Slug: "home", Title: "home", Content: "# Home\n\nWelcome to the generated wiki.\n", Encoding: "UTF-8"},
		{Format: "markdown", Slug: WikiNested, Title: WikiNested, Content: "# Setup\n\nRun make.<!-- hidden -->\n", Encoding: "UTF-8"},
	}
	at := p.CreatedAt.Add(48 * time.Hour)
	desc := "Two generated files."
	pid := p.ID
	s.snippets = append(s.snippets,
		&snippet{Snippet: gitlab.Snippet{ID: SnippetAlpha, Title: "Generated snippet", Description: &desc, Visibility: "internal",
			Author: s.user("bob"), ProjectID: &pid, CreatedAt: at, UpdatedAt: at,
			WebURL: fmt.Sprintf("%s/-/snippets/%d", p.WebURL, SnippetAlpha)},
			files: [][2]string{{"notes.md", "# Notes\n\nGenerated.\n"}, {"run.sh", "#!/bin/sh\necho generated\n"}}},
		&snippet{Snippet: gitlab.Snippet{ID: SnippetPersonal, Title: "Personal snippet", Visibility: "private",
			Author: s.user(DefaultUser), CreatedAt: at, UpdatedAt: at, WebURL: fmt.Sprintf("%s/-/snippets/%d", s.URL, SnippetPersonal)},
			files: [][2]string{{"scratch.txt", "generated scratch\n"}}})

	release := p.commits["release/1.0"][0]
	notes := "Notes for 1.0.\n\n<!-- hidden -->Fixed things.\n"
	author := s.user(DefaultUser)
	released := release.CommittedDate.Add(2 * time.Hour)
	p.releases = []gitlab.Release{{TagName: TagRelease, Name: "Release 1.0", Description: &notes, CreatedAt: released,
		ReleasedAt: &released, Author: &author, Commit: &gitlab.ReleaseCommit{ID: release.ID, ShortID: release.ShortID, Title: release.Title},
		Milestones: []gitlab.ReleaseMilestone{}, Assets: &gitlab.ReleaseAssets{Count: 4}}}

	main := p.commits["main"][0]
	deployed := main.CommittedDate.Add(30 * time.Minute)
	external := "https://example.invalid/"
	p.environments = []gitlab.Environment{
		{ID: 95001, Name: "production", Slug: "production", State: "available", Tier: "production", ExternalURL: &external,
			CreatedAt: p.CreatedAt, UpdatedAt: deployed,
			LastDeployment: &gitlab.EnvironmentDeployment{ID: 96002, IID: 2, Status: "failed", Ref: "main", SHA: main.ID, CreatedAt: deployed}},
		{ID: 95002, Name: "review/login", Slug: "review-login", State: "stopped", Tier: "development", CreatedAt: p.CreatedAt,
			UpdatedAt: p.CreatedAt},
	}
	earlier := deployed.Add(-time.Hour)
	p.deployments = []gitlab.Deployment{
		{ID: 96001, IID: 1, Status: "success", Ref: "main", SHA: p.commits["main"][1].ID, CreatedAt: earlier, UpdatedAt: &earlier,
			User: &author, Environment: gitlab.DeploymentEnvironment{ID: 95001, Name: "production"},
			Deployable: &gitlab.DeploymentJob{ID: JobPassed, Name: "build", Status: "success"}},
		{ID: 96002, IID: 2, Status: "failed", Ref: "main", SHA: main.ID, CreatedAt: deployed, UpdatedAt: &deployed,
			User: &author, Environment: gitlab.DeploymentEnvironment{ID: 95001, Name: "production"},
			Deployable: &gitlab.DeploymentJob{ID: JobManual, Name: "deploy", Status: "manual"}},
	}

	next := int64(97001)
	add := func(action, name, author string, targetType *string, targetID, targetIID *int64, title *string, at time.Time, push *gitlab.EventPush) {
		p.events = append(p.events, event{action: action, Event: gitlab.Event{ID: next, ActionName: name, TargetType: targetType,
			TargetID: targetID, TargetIID: targetIID, TargetTitle: title, AuthorUsername: author, ProjectID: &pid, CreatedAt: at,
			PushData: push}})
		next++
	}
	issueType, mrType := "Issue", "MergeRequest"
	for _, iss := range p.issues[:3] {
		add("created", "opened", iss.Author.Username, &issueType, &iss.ID, &iss.IID, &iss.Title, iss.CreatedAt, nil)
	}
	mr := p.mrs[0]
	add("created", "opened", mr.Author.Username, &mrType, &mr.ID, &mr.IID, &mr.Title, mr.CreatedAt, nil)
	ref := "main"
	add("pushed", "pushed to", DefaultUser, nil, nil, nil, nil, main.CommittedDate,
		&gitlab.EventPush{Action: "pushed", RefType: "branch", Ref: &ref, CommitCount: 1, CommitTitle: &main.Title})
}

// serveToolsetRead serves the GETs of the optional toolsets under a
// project; it reports whether it answered.
func (s *Server) serveToolsetRead(w http.ResponseWriter, r *http.Request, p *project, user string, seg []string) bool {
	switch {
	case len(seg) >= 1 && (seg[0] == "wikis" || seg[0] == "snippets"):
		return s.serveContentRead(w, r, p, user, seg)
	case match(seg, "releases"):
		rows := slices.Clone(p.releases)
		asc := r.URL.Query().Get("sort") == "asc"
		slices.SortStableFunc(rows, func(a, b gitlab.Release) int {
			c := a.CreatedAt.Compare(b.CreatedAt)
			if r.URL.Query().Get("order_by") != "created_at" && a.ReleasedAt != nil && b.ReleasedAt != nil {
				c = a.ReleasedAt.Compare(*b.ReleasedAt)
			}
			if !asc {
				c = -c
			}
			return c
		})
		writePage(s, w, r, rows)
	case match(seg, "releases", "*"):
		for _, rel := range p.releases {
			if rel.TagName == seg[1] {
				writeJSON(w, http.StatusOK, rel)
				return true
			}
		}
		message(w, http.StatusNotFound, "404 Not Found")
	case match(seg, "environments"):
		q := r.URL.Query()
		var rows []gitlab.Environment
		for _, e := range p.environments {
			if (q.Get("name") == "" || e.Name == q.Get("name")) && (q.Get("search") == "" || strings.Contains(e.Name, q.Get("search"))) &&
				(q.Get("states") == "" || e.State == q.Get("states")) {
				rows = append(rows, e)
			}
		}
		writePage(s, w, r, rows)
	case match(seg, "deployments"):
		s.listDeployments(w, r, p)
	case match(seg, "events"):
		s.listEvents(w, r, p.events)
	default:
		return false
	}
	return true
}

// serveContentRead serves a project's wiki pages and snippets.
func (s *Server) serveContentRead(w http.ResponseWriter, r *http.Request, p *project, user string, seg []string) bool {
	switch {
	case match(seg, "wikis"):
		rows := make([]map[string]any, 0, len(p.wiki))
		for _, pg := range p.wiki {
			// GitLab lists a page in a directory by its last part.
			row := map[string]any{"format": pg.Format, "slug": pg.Slug, "title": pg.Title[strings.LastIndex(pg.Title, "/")+1:],
				"wiki_page_meta_id": 1}
			if r.URL.Query().Get("with_content") == "true" {
				row["content"], row["encoding"] = pg.Content, pg.Encoding
			}
			rows = append(rows, row)
		}
		writeJSON(w, http.StatusOK, rows)
	case match(seg, "wikis", "*"):
		if i := wikiIndex(p, seg[1]); i >= 0 {
			writeJSON(w, http.StatusOK, withExtra(p.wiki[i], map[string]any{"wiki_page_meta_id": i + 1, "front_matter": map[string]any{}}))
			return true
		}
		message(w, http.StatusNotFound, "404 Wiki Page Not Found")
	case match(seg, "snippets"):
		var rows []map[string]any
		for _, sn := range s.snippets {
			if sn.ProjectID != nil && *sn.ProjectID == p.ID {
				rows = append(rows, s.snippetJSON(sn))
			}
		}
		writePage(s, w, r, rows)
	case len(seg) >= 2 && seg[0] == "snippets":
		sn := s.findSnippet(p, seg[1], user)
		if sn == nil {
			message(w, http.StatusNotFound, "404 Snippet Not Found")
			return true
		}
		s.serveSnippet(w, sn, seg[2:])
	default:
		return false
	}
	return true
}

// serveToolsetWrite routes the writes of the optional toolsets under a
// project; it reports whether it answered.
func (s *Server) serveToolsetWrite(w http.ResponseWriter, r *http.Request, p *project, user string, seg []string) bool {
	switch {
	case r.Method == http.MethodPost && match(seg, "wikis"):
		s.createWikiPage(w, r, p)
	case r.Method == http.MethodPut && match(seg, "wikis", "*"):
		s.updateWikiPage(w, r, p, seg[1])
	case r.Method == http.MethodDelete && match(seg, "wikis", "*"):
		i := wikiIndex(p, seg[1])
		if i < 0 {
			message(w, http.StatusNotFound, "404 Wiki Page Not Found")
			return true
		}
		p.wiki = slices.Delete(p.wiki, i, i+1)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && match(seg, "snippets"):
		s.createSnippet(w, r, p, user)
	case (r.Method == http.MethodPut || r.Method == http.MethodDelete) && match(seg, "snippets", "*"):
		s.writeSnippet(w, r, p, user, seg[1])
	case r.Method == http.MethodPost && match(seg, "releases"):
		s.createRelease(w, r, p, user)
	default:
		return false
	}
	return true
}

// serveToolsetTop serves the account-level routes: personal snippets and
// the account's events. It reports whether it answered.
func (s *Server) serveToolsetTop(w http.ResponseWriter, r *http.Request, user string, seg []string) bool {
	get := r.Method == http.MethodGet
	switch {
	case get && match(seg, "events"):
		var rows []event
		for _, p := range s.visibleProjects(user) {
			for _, e := range p.events {
				if e.AuthorUsername == user {
					rows = append(rows, e)
				}
			}
		}
		s.listEvents(w, r, rows)
	case get && match(seg, "snippets"):
		after, _ := time.Parse(time.RFC3339, r.URL.Query().Get("created_after"))
		var rows []map[string]any
		for _, sn := range s.snippets {
			if sn.Author.Username == user && !sn.CreatedAt.Before(after) {
				rows = append(rows, s.snippetJSON(sn))
			}
		}
		writePage(s, w, r, rows)
	case r.Method == http.MethodPost && match(seg, "snippets"):
		s.createSnippet(w, r, nil, user)
	case (r.Method == http.MethodPut || r.Method == http.MethodDelete) && match(seg, "snippets", "*"):
		s.writeSnippet(w, r, nil, user, seg[1])
	case get && len(seg) >= 2 && seg[0] == "snippets":
		sn := s.findSnippet(nil, seg[1], user)
		if sn == nil {
			message(w, http.StatusNotFound, "404 Snippet Not Found")
			return true
		}
		s.serveSnippet(w, sn, seg[2:])
	default:
		return false
	}
	return true
}

// ------------------------------------------------------------------ wiki

func wikiIndex(p *project, slug string) int {
	return slices.IndexFunc(p.wiki, func(pg gitlab.WikiPage) bool { return pg.Slug == slug })
}

// wikiSlug is a title's slug: spaces become hyphens, as GitLab's do.
func wikiSlug(title string) string { return strings.ReplaceAll(strings.TrimSpace(title), " ", "-") }

var wikiFormats = []string{"markdown", "rdoc", "asciidoc", "org"}

func (s *Server) createWikiPage(w http.ResponseWriter, r *http.Request, p *project) {
	b, ok := readBody(w, r)
	if !ok || !b.require(w, "title", "content") || !b.valid(w, "format", wikiFormats...) {
		return
	}
	title, _ := b.str("title")
	content, _ := b.str("content")
	format, sent := b.str("format")
	if !sent {
		format = "markdown"
	}
	if wikiIndex(p, wikiSlug(title)) >= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": map[string]any{"base": []string{
			"Duplicate page: A page with that title already exists"}}})
		return
	}
	pg := gitlab.WikiPage{Format: format, Slug: wikiSlug(title), Title: title, Content: content, Encoding: "UTF-8"}
	p.wiki = append(p.wiki, pg)
	writeJSON(w, http.StatusCreated, withExtra(pg, map[string]any{"wiki_page_meta_id": len(p.wiki)}))
}

func (s *Server) updateWikiPage(w http.ResponseWriter, r *http.Request, p *project, slug string) {
	i := wikiIndex(p, slug)
	if i < 0 {
		message(w, http.StatusNotFound, "404 Wiki Page Not Found")
		return
	}
	b, ok := readBody(w, r)
	if !ok || !b.valid(w, "format", wikiFormats...) {
		return
	}
	if !b.has("content") && !b.has("title") && !b.has("format") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "content, title, format are missing, at least one parameter must be provided"})
		return
	}
	pg := &p.wiki[i]
	if v, ok := b.str("content"); ok {
		pg.Content = v
	}
	if v, ok := b.str("format"); ok {
		pg.Format = v
	}
	if v, ok := b.str("title"); ok && v != "" {
		pg.Title, pg.Slug = v, wikiSlug(v)
	}
	writeJSON(w, http.StatusOK, withExtra(*pg, map[string]any{"wiki_page_meta_id": i + 1}))
}

// -------------------------------------------------------------- snippets

// findSnippet finds a snippet of p, or one on the personal routes when p
// is nil, that user may read: a private one is its author's and, in a
// project, its members'. The personal routes find the user's own
// snippets in projects too, as SnippetsFinder does without a project;
// GitLab's also finds others' in projects the user sees, which this
// instance leaves out.
func (s *Server) findSnippet(p *project, id, user string) *snippet {
	for _, sn := range s.snippets {
		if itoa(sn.ID) != id {
			continue
		}
		switch {
		case p == nil && sn.ProjectID != nil && sn.Author.Username != user, p != nil && (sn.ProjectID == nil || *sn.ProjectID != p.ID):
			return nil
		case sn.Visibility == "private" && sn.Author.Username != user && (p == nil || !p.members[user]):
			return nil
		}
		return sn
	}
	return nil
}

func (s *Server) snippetJSON(sn *snippet) map[string]any {
	files := make([]map[string]any, 0, len(sn.files))
	for _, f := range sn.files {
		files = append(files, map[string]any{"path": f[0], "raw_url": sn.WebURL + "/raw/main/" + f[0]})
	}
	// GitLab shows times to the millisecond and keeps them to the
	// microsecond.
	return withExtra(sn.Snippet, map[string]any{"files": files, "file_name": sn.files[0][0], "raw_url": sn.WebURL + "/raw",
		"created_at": shownTime(sn.CreatedAt), "updated_at": shownTime(sn.UpdatedAt)})
}

func shownTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// serveSnippet serves one snippet, its first file raw, or one file raw
// at a ref, which is HEAD or the snippet repository's main.
func (s *Server) serveSnippet(w http.ResponseWriter, sn *snippet, rest []string) {
	raw := func(content string) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(content))
	}
	switch {
	case len(rest) == 0:
		writeJSON(w, http.StatusOK, s.snippetJSON(sn))
	case match(rest, "raw"):
		raw(sn.files[0][1])
	case match(rest, "files", "*", "*", "raw"):
		if rest[1] != "HEAD" && rest[1] != "main" {
			message(w, http.StatusNotFound, "404 Reference Not Found")
			return
		}
		for _, f := range sn.files {
			if f[0] == rest[2] {
				raw(f[1])
				return
			}
		}
		message(w, http.StatusNotFound, "404 File Not Found")
	default:
		routeNotFound(w)
	}
}

// createSnippet is POST /snippets and /projects/:id/snippets. A project
// snippet requires visibility; a personal one defaults to internal.
func (s *Server) createSnippet(w http.ResponseWriter, r *http.Request, p *project, user string) {
	b, ok := readBody(w, r)
	if !ok || !b.require(w, "title") || !b.valid(w, "visibility", "private", "internal", "public") {
		return
	}
	if p != nil && !b.require(w, "visibility") {
		return
	}
	var files []struct {
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(b["files"], &files); err != nil || len(files) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "files, content are missing, exactly one parameter must be provided"})
		return
	}
	title, _ := b.str("title")
	visibility, sent := b.str("visibility")
	if !sent {
		visibility = "internal"
	}
	var id int64 = SnippetPersonal
	for _, sn := range s.snippets {
		id = max(id, sn.ID)
	}
	id++
	now := s.opts.Now().UTC().Truncate(time.Microsecond)
	sn := &snippet{Snippet: gitlab.Snippet{ID: id, Title: title, Visibility: visibility, Author: s.user(user), CreatedAt: now, UpdatedAt: now,
		WebURL: fmt.Sprintf("%s/-/snippets/%d", s.URL, id)}}
	if d, ok := b.str("description"); ok {
		sn.Description = &d
	}
	if p != nil {
		pid := p.ID
		sn.ProjectID, sn.WebURL = &pid, fmt.Sprintf("%s/-/snippets/%d", p.WebURL, id)
	}
	for _, f := range files {
		sn.files = append(sn.files, [2]string{f.FilePath, f.Content})
	}
	s.snippets = append(s.snippets, sn)
	writeJSON(w, http.StatusCreated, s.snippetJSON(sn))
}

// writeSnippet is PUT and DELETE on a snippet, as lib/api/snippets.rb
// and project_snippets.rb make them. The personal routes find only the
// user's own snippets, their snippets in projects included
// (SnippetsFinder with author:); a project's find the project's. An
// update needs update_snippet: the author, or a maintainer of the
// project. A delete needs admin_snippet: the author, at least a reporter
// in a project, or a maintainer; it is conditional on
// If-Unmodified-Since, as destroy_conditionally! makes it.
func (s *Server) writeSnippet(w http.ResponseWriter, r *http.Request, p *project, user, id string) {
	var sn *snippet
	if p == nil {
		for _, c := range s.snippets {
			if itoa(c.ID) == id && c.Author.Username == user {
				sn = c
			}
		}
	} else {
		sn = s.findSnippet(p, id, user)
	}
	if sn == nil {
		message(w, http.StatusNotFound, "404 Snippet Not Found")
		return
	}
	author := sn.Author.Username == user
	if r.Method == http.MethodDelete {
		if p != nil && p.levels[user] < 40 && (!author || p.levels[user] < 20) {
			message(w, http.StatusForbidden, "403 Forbidden")
			return
		}
		if since, ok := unmodifiedSince(r); ok && sn.UpdatedAt.After(since) {
			message(w, http.StatusPreconditionFailed, "412 Precondition Failed")
			return
		}
		s.snippets = slices.DeleteFunc(s.snippets, func(c *snippet) bool { return c == sn })
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if p != nil && !author && p.levels[user] < 40 {
		message(w, http.StatusForbidden, "403 Forbidden")
		return
	}
	s.updateSnippet(w, r, sn)
}

// snippetFileAction is one element of a snippet update's files.
type snippetFileAction struct {
	Action       string `json:"action"`
	FilePath     string `json:"file_path"`
	PreviousPath string `json:"previous_path"`
	Content      string `json:"content"`
}

// valid is SnippetInputAction's validation.
func (a snippetFileAction) valid() bool {
	switch a.Action {
	case "create":
		return a.Content != ""
	case "update":
		return a.FilePath != "" && a.Content != "" && (a.PreviousPath == "" || a.PreviousPath == a.FilePath)
	case "delete":
		return a.FilePath != ""
	case "move":
		return a.PreviousPath != "" && a.FilePath != a.PreviousPath
	}
	return false
}

// updateSnippet is the body of a snippet PUT, as the API's params and
// Snippets::UpdateService take it: files excludes content and file_name,
// content on a snippet of several files is 400, an invalid action 422,
// and an action the snippet's repository cannot commit 400. updated_at
// moves when anything changed. Visibility is taken as sent.
func (s *Server) updateSnippet(w http.ResponseWriter, r *http.Request, sn *snippet) {
	b, ok := readBody(w, r)
	if !ok || !snippetUpdateParams(w, b, len(sn.files)) {
		return
	}
	var actions []snippetFileAction
	if b.has("files") {
		if err := json.Unmarshal(b["files"], &actions); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "files is invalid"})
			return
		}
	}
	files := slices.Clone(sn.files)
	for _, a := range actions {
		if !a.valid() {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": map[string]any{"error": "Snippet actions have invalid data"}})
			return
		}
		if files, ok = applySnippetAction(files, a); !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": map[string]any{"error": "Repository Error updating the snippet"}})
			return
		}
	}
	if v, sent := b.str("content"); sent {
		files[0][1] = v
	}
	if v, sent := b.str("file_name"); sent {
		files[0][0] = v
	}
	next := sn.Snippet
	if v, sent := b.str("title"); sent {
		next.Title = v
	}
	if v, sent := b.str("description"); sent {
		next.Description = &v
	}
	if v, sent := b.str("visibility"); sent {
		next.Visibility = v
	}
	if !reflect.DeepEqual(next, sn.Snippet) || !slices.Equal(files, sn.files) {
		next.UpdatedAt = s.opts.Now().UTC().Truncate(time.Microsecond)
		sn.Snippet, sn.files = next, files
	}
	writeJSON(w, http.StatusOK, s.snippetJSON(sn))
}

// snippetUpdateParams answers the refusals of a snippet PUT's params,
// Grape's and validate_params_for_multiple_files; it reports whether
// the update may go on.
func snippetUpdateParams(w http.ResponseWriter, b body, files int) bool {
	if !b.valid(w, "visibility", "private", "internal", "public") {
		return false
	}
	if !b.has("content") && !b.has("description") && !b.has("files") && !b.has("file_name") && !b.has("title") && !b.has("visibility") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "content, description, files, file_name, title, visibility are missing, " +
			"at least one parameter must be provided"})
		return false
	}
	for _, k := range []string{"content", "file_name"} {
		if b.has("files") && b.has(k) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "files, " + k + " are mutually exclusive"})
			return false
		}
	}
	for _, k := range []string{"title", "content"} {
		if v, sent := b.str(k); sent && strings.TrimSpace(v) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": k + " is empty"})
			return false
		}
	}
	if (b.has("content") || b.has("file_name")) && files > 1 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": map[string]any{
			"error": "To update Snippets with multiple files, you must use the `files` parameter"}})
		return false
	}
	return true
}

// applySnippetAction commits one file action as the snippet repository
// would; it reports false for one Git refuses: a file created twice, or
// one updated, deleted or moved that is not there.
func applySnippetAction(files [][2]string, a snippetFileAction) ([][2]string, bool) {
	find := func(path string) int { return slices.IndexFunc(files, func(f [2]string) bool { return f[0] == path }) }
	switch a.Action {
	case "create":
		if find(a.FilePath) >= 0 {
			return nil, false
		}
		return append(files, [2]string{a.FilePath, a.Content}), true
	case "update":
		i := find(a.FilePath)
		if i < 0 {
			return nil, false
		}
		files[i][1] = a.Content
	case "delete":
		i := find(a.FilePath)
		if i < 0 {
			return nil, false
		}
		files = slices.Delete(files, i, i+1)
	case "move":
		i := find(a.PreviousPath)
		if i < 0 || find(a.FilePath) >= 0 {
			return nil, false
		}
		files[i][0] = a.FilePath
		if a.Content != "" {
			files[i][1] = a.Content
		}
	}
	return files, true
}

// unmodifiedSince reads If-Unmodified-Since as check_unmodified_since!
// does, with Ruby's Time.parse: an RFC 3339 time keeps its fraction, an
// HTTP-date is to the second, and anything unparseable is no condition.
func unmodifiedSince(r *http.Request) (time.Time, bool) {
	v := r.Header.Get("If-Unmodified-Since")
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t, true
	}
	t, err := http.ParseTime(v)
	return t, err == nil
}

// -------------------------------------------------------------- releases

func (s *Server) createRelease(w http.ResponseWriter, r *http.Request, p *project, user string) {
	b, ok := readBody(w, r)
	if !ok || !b.require(w, "tag_name") {
		return
	}
	tag, _ := b.str("tag_name")
	if slices.ContainsFunc(p.releases, func(rel gitlab.Release) bool { return rel.TagName == tag }) {
		message(w, http.StatusConflict, "Release already exists")
		return
	}
	i := slices.IndexFunc(p.tags, func(t gitlab.Tag) bool { return t.Name == tag })
	if i < 0 {
		ref, _ := b.str("ref")
		if ref == "" {
			message(w, http.StatusUnprocessableEntity, "Ref is not specified")
			return
		}
		commits, found := history(p, ref)
		if !found {
			message(w, http.StatusUnprocessableEntity, "Ref is not found")
			return
		}
		msg, _ := b.str("tag_message")
		p.tags = append(p.tags, gitlab.Tag{Name: tag, Message: msg, Target: commits[0].ID, Commit: commits[0]})
		i = len(p.tags) - 1
	}
	var milestones []gitlab.ReleaseMilestone
	titles, _ := b.strs("milestones")
	for _, t := range titles {
		j := slices.IndexFunc(p.milestones, func(m gitlab.ProjectMilestone) bool { return m.Title == t })
		if j < 0 {
			message(w, http.StatusBadRequest, "Milestone(s) not found: "+t)
			return
		}
		milestones = append(milestones, gitlab.ReleaseMilestone{ID: p.milestones[j].ID, Title: t})
	}
	now := s.opts.Now().UTC()
	released := now
	if v, ok := b.str("released_at"); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			released = t
		}
	}
	name, sent := b.str("name")
	if !sent {
		name = tag
	}
	desc, _ := b.str("description")
	// Asset links, as GitLab keeps them: other when no type is given.
	var assets struct {
		Links []gitlab.ReleaseLink `json:"links"`
	}
	if b.has("assets") {
		if err := json.Unmarshal(b["assets"], &assets); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "assets is invalid"})
			return
		}
	}
	for k := range assets.Links {
		if assets.Links[k].LinkType == "" {
			assets.Links[k].LinkType = "other"
		}
	}
	author := s.user(user)
	c := p.tags[i].Commit
	rel := gitlab.Release{TagName: tag, Name: name, Description: &desc, CreatedAt: now, ReleasedAt: &released,
		UpcomingRelease: released.After(now), Author: &author, Commit: &gitlab.ReleaseCommit{ID: c.ID, ShortID: c.ShortID, Title: c.Title},
		Milestones: append([]gitlab.ReleaseMilestone{}, milestones...),
		Assets:     &gitlab.ReleaseAssets{Count: 4 + len(assets.Links), Links: append([]gitlab.ReleaseLink{}, assets.Links...)}}
	p.releases = append(p.releases, rel)
	writeJSON(w, http.StatusCreated, rel)
}

// ----------------------------------------------------------- deployments

func (s *Server) listDeployments(w http.ResponseWriter, r *http.Request, p *project) {
	q := r.URL.Query()
	if (q.Has("updated_after") || q.Has("updated_before")) && q.Get("order_by") != "updated_at" {
		message(w, http.StatusBadRequest, "400 Bad request - `updated_at` filter requires `updated_at` sort")
		return
	}
	after, _ := time.Parse(time.RFC3339, q.Get("updated_after"))
	before, errBefore := time.Parse(time.RFC3339, q.Get("updated_before"))
	rows := make([]gitlab.Deployment, 0, len(p.deployments))
	for _, d := range p.deployments {
		switch {
		case q.Get("environment") != "" && d.Environment.Name != q.Get("environment"),
			q.Get("status") != "" && d.Status != q.Get("status"),
			d.UpdatedAt != nil && d.UpdatedAt.Before(after),
			errBefore == nil && d.UpdatedAt != nil && d.UpdatedAt.After(before):
			continue
		}
		rows = append(rows, d)
	}
	desc := q.Get("sort") == "desc"
	slices.SortStableFunc(rows, func(a, b gitlab.Deployment) int {
		c := int(a.ID - b.ID)
		if desc {
			c = -c
		}
		return c
	})
	writePage(s, w, r, rows)
}

// ------------------------------------------------------------- activity

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request, all []event) {
	q := r.URL.Query()
	types := map[string]string{"issue": "Issue", "merge_request": "MergeRequest", "note": "Note", "milestone": "Milestone",
		"project": "Project", "snippet": "Snippet", "user": "User", "wiki": "WikiPage::Meta", "design": "DesignManagement::Design"}
	before, errBefore := time.Parse(time.DateOnly, q.Get("before"))
	after, errAfter := time.Parse(time.DateOnly, q.Get("after"))
	rows := make([]gitlab.Event, 0, len(all))
	for _, e := range all {
		switch {
		case q.Get("action") != "" && e.action != q.Get("action"),
			q.Get("target_type") != "" && (e.TargetType == nil || *e.TargetType != types[q.Get("target_type")]),
			errBefore == nil && !e.CreatedAt.Before(before),
			errAfter == nil && e.CreatedAt.Before(after.AddDate(0, 0, 1)):
			continue
		}
		rows = append(rows, e.Event)
	}
	at := func(e gitlab.Event) time.Time { return e.CreatedAt }
	sortByTime(rows, q, at, at)
	writePage(s, w, r, rows)
}

// ------------------------------------------------------------- inspection

// WikiPage is a wiki page as the instance holds it.
func (s *Server) WikiPage(projectPath, slug string) (gitlab.WikiPage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil {
		return gitlab.WikiPage{}, false
	}
	if i := wikiIndex(p, slug); i >= 0 {
		return p.wiki[i], true
	}
	return gitlab.WikiPage{}, false
}

// EditWikiPage changes a page's content, as an edit made elsewhere does.
func (s *Server) EditWikiPage(projectPath, slug, content string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil {
		return false
	}
	if i := wikiIndex(p, slug); i >= 0 {
		p.wiki[i].Content = content
		return true
	}
	return false
}

// SnippetVisibility is a snippet's visibility, "" when there is none.
func (s *Server) SnippetVisibility(id int64) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sn := range s.snippets {
		if sn.ID == id {
			return sn.Visibility
		}
	}
	return ""
}

// SnippetFiles is a snippet's files, each its path and content, in
// order; nil when there is no such snippet.
func (s *Server) SnippetFiles(id int64) [][2]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sn := range s.snippets {
		if sn.ID == id {
			return slices.Clone(sn.files)
		}
	}
	return nil
}

// TouchSnippet moves a snippet's updated_at, as an edit made elsewhere
// does.
func (s *Server) TouchSnippet(id int64, at time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sn := range s.snippets {
		if sn.ID == id {
			sn.UpdatedAt = at.UTC()
			return true
		}
	}
	return false
}

// Release is a project's release of a tag.
func (s *Server) Release(projectPath, tag string) (gitlab.Release, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil {
		return gitlab.Release{}, false
	}
	for _, rel := range p.releases {
		if rel.TagName == tag {
			return rel, true
		}
	}
	return gitlab.Release{}, false
}
