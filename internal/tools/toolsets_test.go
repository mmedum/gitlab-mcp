package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/mmedum/gitlab-mcp/v2/internal/config"
	"github.com/mmedum/gitlab-mcp/v2/internal/gapi/gitlabtest"
)

// The optional toolsets against the in-memory instance.

func withToolsets(cfg config.Config, sets ...string) config.Config {
	cfg.Toolsets = sets
	return cfg
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ------------------------------------------------------------------ wiki

func TestWikiReads(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: withToolsets(config.Config{}, "wiki")})
	text, out := h.ok("list_wiki_pages", map[string]any{"project": alpha})
	if get(out, "listing", "complete") != true || len(get(out, "pages").([]any)) != 2 || get(out, "pages", 1, "slug") != gitlabtest.WikiNested {
		t.Errorf("pages = %v", out)
	}
	if !strings.Contains(text, "UNTRUSTED") && !strings.Contains(text, "markers") {
		t.Errorf("titles are not marked:\n%s", text)
	}

	page, _ := h.gl.WikiPage(alpha, gitlabtest.WikiNested)
	text, out = h.ok("get_wiki_page", map[string]any{"project": alpha, "slug": gitlabtest.WikiNested})
	if get(out, "content_sha256") != hash(page.Content) || strings.Contains(get(out, "untrusted_content").(string), "hidden") ||
		get(out, "content_budget", "hidden_removed") == float64(0) {
		t.Errorf("page = %v", out)
	}
	if !strings.Contains(text, "kind=wiki_page") || !strings.Contains(text, "content_sha256 "+hash(page.Content)) {
		t.Errorf("text:\n%s", text)
	}
	h.fails("get_wiki_page", map[string]any{"project": alpha, "slug": "no-such-page"}, "not_found")
	h.fails("get_wiki_page", map[string]any{"project": alpha, "slug": " "}, "invalid")

	// resolve_url names the tool once the toolset is on.
	_, r := h.ok("resolve_url", map[string]any{"url": h.gl.URL + "/" + alpha + "/-/wikis/" + gitlabtest.WikiNested})
	if get(r, "tool") != "get_wiki_page" || get(r, "arguments", "slug") != gitlabtest.WikiNested {
		t.Errorf("resolve_url = %v", r)
	}
	_, r = h.ok("resolve_url", map[string]any{"url": h.gl.URL + "/" + alpha + "/-/wikis/" + gitlabtest.WikiNested + "/edit"})
	if get(r, "arguments", "slug") != gitlabtest.WikiNested {
		t.Errorf("edit view = %v", r)
	}
	_, r = h.ok("resolve_url", map[string]any{"url": h.gl.URL + "/" + alpha + "/-/wikis/pages"})
	if get(r, "tool") != "list_wiki_pages" {
		t.Errorf("index = %v", r)
	}
}

func TestSaveWikiPage(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: withToolsets(config.Config{}, "wiki")})
	h.fails("save_wiki_page", map[string]any{"project": alpha, "content": "x"}, "invalid")
	h.fails("save_wiki_page", map[string]any{"project": alpha, "title": "T", "content": "x", "format": "html"}, "invalid")
	h.fails("save_wiki_page", map[string]any{"project": alpha, "title": "T", "content": "x", "content_sha256": "abc"}, "invalid")

	// A wiki page runs no quick action, so a slash line is sent as it is.
	content := "# New page\n\n/close\n"
	_, out := h.ok("save_wiki_page", map[string]any{"project": alpha, "title": "New page", "content": content})
	if get(out, "outcome") != "created" || get(out, "slug") != "New-page" || get(out, "content_sha256") != hash(content) {
		t.Errorf("create = %v", out)
	}
	h.fails("save_wiki_page", map[string]any{"project": alpha, "title": "New page", "content": "again"}, "conflict")

	witness := get(out, "content_sha256").(string)
	h.fails("save_wiki_page", map[string]any{"project": alpha, "slug": "New-page", "content": "y"}, "invalid")
	h.fails("save_wiki_page", map[string]any{"project": alpha, "slug": "New-page", "content_sha256": witness}, "invalid")
	text, out := h.ok("save_wiki_page", map[string]any{"project": alpha, "slug": "New-page", "content_sha256": witness,
		"content": "# New page\n\nShorter.\n", "title": "Renamed page"})
	if get(out, "outcome") != "updated" || get(out, "slug") != "Renamed-page" || get(out, "content_removed", "lines") != float64(1) ||
		strings.Join(strs(get(out, "changed")), ",") != "title,content" {
		t.Errorf("update = %v", out)
	}
	if !strings.Contains(text, "pass it to save_wiki_page") {
		t.Errorf("text:\n%s", text)
	}
	// The old witness no longer holds.
	h.fails("save_wiki_page", map[string]any{"project": alpha, "slug": "Renamed-page", "content_sha256": witness, "content": "z"}, "stale")

	// An edit made elsewhere after the read is stale too.
	w2 := get(out, "content_sha256").(string)
	h.gl.EditWikiPage(alpha, "Renamed-page", "Someone else's words.\n")
	before := writesSent(h)
	h.fails("save_wiki_page", map[string]any{"project": alpha, "slug": "Renamed-page", "content_sha256": w2, "content": "mine"}, "stale")
	if writesSent(h) != before {
		t.Error("a stale write was sent")
	}
	_, same := h.ok("save_wiki_page", map[string]any{"project": alpha, "slug": "Renamed-page",
		"content_sha256": hash("Someone else's words.\n"), "format": "markdown"})
	if get(same, "outcome") != "unchanged" {
		t.Errorf("unchanged = %v", same)
	}
}

func TestDeleteWikiPage(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: withToolsets(destructive, "wiki")})
	page, _ := h.gl.WikiPage(alpha, "home")
	h.fails("delete_wiki_page", map[string]any{"project": alpha, "slug": "home", "content_sha256": hash(page.Content)}, "blocked")
	h.fails("delete_wiki_page", map[string]any{"project": alpha, "slug": "home", "content_sha256": hash("other"), "confirm": true}, "stale")
	h.fails("delete_wiki_page", map[string]any{"project": alpha, "slug": "home", "confirm": true}, "invalid")
	text, out := h.ok("delete_wiki_page", map[string]any{"project": alpha, "slug": "home", "content_sha256": hash(page.Content),
		"confirm": true})
	if get(out, "outcome") != "deleted" || !strings.Contains(text, "finds no such page") {
		t.Errorf("delete = %v\n%s", out, text)
	}
	if _, ok := h.gl.WikiPage(alpha, "home"); ok {
		t.Error("the page is still there")
	}
}

// -------------------------------------------------------------- snippets

func TestSnippetReads(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: withToolsets(config.Config{}, "snippets")})
	_, out := h.ok("list_snippets", map[string]any{"project": alpha})
	if len(get(out, "snippets").([]any)) != 1 || get(out, "project", "path") != alpha ||
		strings.Join(strs(get(out, "snippets", 0, "files")), ",") != "notes.md,run.sh" {
		t.Errorf("project snippets = %v", out)
	}
	_, out = h.ok("list_snippets", map[string]any{})
	if len(get(out, "snippets").([]any)) != 1 || get(out, "project") != nil || get(out, "snippets", 0, "id") != float64(gitlabtest.SnippetPersonal) {
		t.Errorf("personal snippets = %v", out)
	}

	text, out := h.ok("get_snippet", map[string]any{"project": alpha, "snippet_id": gitlabtest.SnippetAlpha})
	if get(out, "file") != "notes.md" || !strings.Contains(get(out, "untrusted_content").(string), "# Notes") ||
		get(out, "untrusted_description") != "Two generated files." {
		t.Errorf("snippet = %v", out)
	}
	if !strings.Contains(text, "kind=snippet_file") || !strings.Contains(text, "kind=snippet_description") {
		t.Errorf("text:\n%s", text)
	}
	_, out = h.ok("get_snippet", map[string]any{"project": alpha, "snippet_id": gitlabtest.SnippetAlpha, "file": "run.sh"})
	if get(out, "file") != "run.sh" || !strings.Contains(get(out, "untrusted_content").(string), "echo generated") {
		t.Errorf("second file = %v", out)
	}
	h.fails("get_snippet", map[string]any{"project": alpha, "snippet_id": gitlabtest.SnippetAlpha, "file": "missing"}, "invalid")
	_, out = h.ok("get_snippet", map[string]any{"snippet_id": gitlabtest.SnippetPersonal})
	if get(out, "project") != nil || get(out, "file") != "scratch.txt" {
		t.Errorf("personal = %v", out)
	}
	h.fails("get_snippet", map[string]any{"snippet_id": gitlabtest.SnippetAlpha}, "not_found")
}

func TestCreateSnippetIsPrivate(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: withToolsets(config.Config{}, "snippets")})
	files := []map[string]any{{"path": "a.md", "content": "# A\n"}, {"path": "b.sh", "content": "echo b\n"}}
	h.fails("create_snippet", map[string]any{"project": alpha, "title": "T", "files": []map[string]any{}}, "invalid")
	h.fails("create_snippet", map[string]any{"project": alpha, "title": "T", "files": []map[string]any{{"path": "a", "content": ""}}}, "invalid")
	h.fails("create_snippet", map[string]any{"project": alpha, "title": " ", "files": files}, "invalid")

	_, out := h.ok("create_snippet", map[string]any{"project": alpha, "title": "Made here", "description": "/close is plain here",
		"files": files})
	if get(out, "outcome") != "created" || get(out, "visibility") != "private" || strings.Join(strs(get(out, "files")), ",") != "a.md,b.sh" {
		t.Errorf("project snippet = %v", out)
	}
	if v := h.gl.SnippetVisibility(int64(get(out, "id").(float64))); v != "private" {
		t.Errorf("GitLab holds it %s", v)
	}
	text, out := h.ok("create_snippet", map[string]any{"title": "Mine", "files": files[:1]})
	if get(out, "outcome") != "created" || !strings.Contains(text, "only you can see it") {
		t.Errorf("personal snippet = %v\n%s", out, text)
	}

	// Under the write allow-list a personal snippet is in no namespace.
	confined := withToolsets(config.Config{WriteNamespaces: []string{gitlabtest.GroupTop}}, "snippets")
	h2 := newHarness(t, harnessOptions{cfg: confined})
	h2.fails("create_snippet", map[string]any{"title": "Mine", "files": files[:1]}, "blocked")
	h2.ok("create_snippet", map[string]any{"project": alpha, "title": "Inside", "files": files[:1]})
}

// -------------------------------------------------------------- releases

func TestReleases(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: withToolsets(ship, "releases")})
	_, out := h.ok("list_releases", map[string]any{"project": alpha})
	if len(get(out, "releases").([]any)) != 1 || get(out, "releases", 0, "tag_name") != gitlabtest.TagRelease {
		t.Errorf("releases = %v", out)
	}
	text, out := h.ok("get_release", map[string]any{"project": alpha, "tag_name": gitlabtest.TagRelease})
	if strings.Contains(get(out, "untrusted_description").(string), "hidden") || get(out, "assets") != float64(4) {
		t.Errorf("release = %v", out)
	}
	if !strings.Contains(text, "kind=release_notes") {
		t.Errorf("text:\n%s", text)
	}
	h.fails("get_release", map[string]any{"project": alpha, "tag_name": "v404"}, "not_found")

	h.fails("create_release", map[string]any{"project": alpha, "tag_name": "v9.9.9"}, "invalid")
	h.fails("create_release", map[string]any{"project": alpha, "tag_name": gitlabtest.TagPlain, "ref": "main"}, "invalid")
	h.fails("create_release", map[string]any{"project": alpha, "tag_name": gitlabtest.TagPlain, "released_at": "soon"}, "invalid")
	_, dry := h.ok("create_release", map[string]any{"project": alpha, "tag_name": "v9.9.9", "ref": "main", "dry_run": true})
	if get(dry, "outcome") != "dry_run" || get(dry, "tag_created") != true {
		t.Errorf("dry run = %v", dry)
	}

	_, out = h.ok("create_release", map[string]any{"project": alpha, "tag_name": gitlabtest.TagPlain, "name": "Zero nine",
		"description": "Notes.\n/close\n", "milestones": []string{"Sprint 2"}})
	if get(out, "outcome") != "created" || get(out, "tag_created") != false || strings.Join(strs(get(out, "milestones")), ",") != "Sprint 2" {
		t.Errorf("existing tag = %v", out)
	}
	text, out = h.ok("create_release", map[string]any{"project": alpha, "tag_name": "v9.9.9", "ref": "main", "tag_message": "Annotated"})
	if get(out, "outcome") != "created" || get(out, "tag_created") != true || !strings.Contains(text, "tag pipelines") {
		t.Errorf("new tag = %v\n%s", out, text)
	}
	h.fails("create_release", map[string]any{"project": alpha, "tag_name": "v9.9.9"}, "conflict")
}

func TestCreateReleaseWhoseAnswerWasLostIsSettledByTheTag(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: withToolsets(ship, "releases")})
	id := h.gl.ProjectID(alpha)
	h.gl.Inject(gitlabtest.Fault{Method: "POST", Path: fmt.Sprintf("/projects/%d/releases", id), Status: 500, AfterApply: true,
		Body: `{"message":"500 Internal Server Error"}`})
	text := h.fails("create_release", map[string]any{"project": alpha, "tag_name": gitlabtest.TagPlain}, "ambiguous_outcome")
	if !strings.Contains(text, "a read shows it was created: the release of tag "+gitlabtest.TagPlain) {
		t.Errorf("settled: %s", text)
	}
	if _, ok := h.gl.Release(alpha, gitlabtest.TagPlain); !ok {
		t.Error("the release is not there")
	}
}

// ------------------------------------------------ deployments, activity

func TestEnvironmentsAndDeployments(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: withToolsets(config.Config{}, "deployments")})
	text, out := h.ok("list_environments", map[string]any{"project": alpha})
	if len(get(out, "environments").([]any)) != 2 || get(out, "environments", 0, "last_deployment", "status") != "failed" {
		t.Errorf("environments = %v", out)
	}
	if !strings.Contains(text, "served at https://example.invalid/") {
		t.Errorf("text:\n%s", text)
	}
	_, out = h.ok("list_environments", map[string]any{"project": alpha, "states": "stopped"})
	if len(get(out, "environments").([]any)) != 1 {
		t.Errorf("stopped = %v", out)
	}
	h.fails("list_environments", map[string]any{"project": alpha, "name": "a", "search": "b"}, "invalid")

	_, out = h.ok("list_deployments", map[string]any{"project": alpha, "environment": "production", "sort": "desc"})
	if len(get(out, "deployments").([]any)) != 2 || get(out, "deployments", 0, "status") != "failed" ||
		get(out, "deployments", 1, "job_id") != float64(gitlabtest.JobPassed) {
		t.Errorf("deployments = %v", out)
	}
	_, out = h.ok("list_deployments", map[string]any{"project": alpha, "status": "success"})
	if len(get(out, "deployments").([]any)) != 1 {
		t.Errorf("successful = %v", out)
	}
	h.fails("list_deployments", map[string]any{"project": alpha, "updated_after": "yesterday"}, "invalid")
	// GitLab filters by updated_at only when it sorts by it: the server
	// sorts so, and refuses another order with the filter.
	_, out = h.ok("list_deployments", map[string]any{"project": alpha, "updated_after": "2020-01-01T00:00:00Z"})
	if len(get(out, "deployments").([]any)) != 2 {
		t.Errorf("updated after = %v", out)
	}
	h.fails("list_deployments", map[string]any{"project": alpha, "updated_after": "2020-01-01T00:00:00Z", "order_by": "id"}, "invalid")
}

func TestEvents(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: withToolsets(config.Config{}, "activity")})
	text, out := h.ok("list_events", map[string]any{"project": alpha})
	if len(get(out, "events").([]any)) != 5 || get(out, "project", "path") != alpha {
		t.Errorf("project events = %v", out)
	}
	if !strings.Contains(text, "pushed to main, 1 commit(s)") {
		t.Errorf("text:\n%s", text)
	}
	_, out = h.ok("list_events", map[string]any{"project": alpha, "action": "pushed"})
	if len(get(out, "events").([]any)) != 1 || get(out, "events", 0, "push_ref") != "main" {
		t.Errorf("pushes = %v", out)
	}
	_, out = h.ok("list_events", map[string]any{"project": alpha, "target_type": "merge_request"})
	if len(get(out, "events").([]any)) != 1 || get(out, "events", 0, "target_type") != "MergeRequest" {
		t.Errorf("merge requests = %v", out)
	}
	_, out = h.ok("list_events", map[string]any{})
	for _, e := range get(out, "events").([]any) {
		if get(e, "author") != gitlabtest.DefaultUser {
			t.Errorf("your events include %v", e)
		}
	}
	if get(out, "project") != nil || len(get(out, "events").([]any)) == 0 {
		t.Errorf("your events = %v", out)
	}
	h.fails("list_events", map[string]any{"before": "last week"}, "invalid")
}

// A lost create of a page in a directory is found by its slug: GitLab
// lists it with only the last part of the path as its title.
func TestLostWikiCreateInADirectoryIsSettled(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: withToolsets(config.Config{}, "wiki")})
	id := h.gl.ProjectID(alpha)
	h.gl.Inject(gitlabtest.Fault{Method: "POST", Path: fmt.Sprintf("/projects/%d/wikis", id), Status: 500, AfterApply: true,
		Body: `{"message":"500 Internal Server Error"}`})
	text := h.fails("save_wiki_page", map[string]any{"project": alpha, "title": "guides/new page", "content": "x"}, "ambiguous_outcome")
	if !strings.Contains(text, "a read shows it was created") {
		t.Errorf("settled: %s", text)
	}
}

// A release's asset links must point at the instance itself: a release
// page sends every reader wherever they lead.
func TestCreateReleaseWithAssetLinks(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: withToolsets(ship, "releases")})
	base := h.gl.URL
	link := func(name, u string) map[string]any { return map[string]any{"name": name, "url": u} }
	text, out := h.ok("create_release", map[string]any{"project": alpha, "tag_name": gitlabtest.TagPlain, "links": []map[string]any{
		{"name": "linux binary", "url": base + "/example-group/alpha/-/packages/1", "link_type": "package", "direct_asset_path": "/bin/linux"},
		link("runbook", base+"/example-group/alpha/-/wikis/runbook"),
		link("generic package", fmt.Sprintf("%s/api/v4/projects/%d/packages/generic/app/1.0/app", base, h.gl.ProjectID(alpha))),
	}})
	if get(out, "links", 0, "name") != "linux binary" || get(out, "links", 0, "link_type") != "package" ||
		get(out, "links", 1, "link_type") != "other" || !strings.Contains(text, "Asset link linux binary (package)") {
		t.Errorf("links = %v\n%s", get(out, "links"), text)
	}
	for name, u := range map[string]string{
		"another host":     "https://example.invalid/payload",
		"credentials":      strings.Replace(base, "://", "://user:pass@", 1) + "/x",
		"a relative URL":   "/example-group/alpha/-/packages/1",
		"another scheme":   "javascript:alert(1)",
		"another project":  base + "/example-group/sub/beta/-/raw/main/installer.sh",
		"a way out":        base + "/example-group/alpha/../sub/beta/-/raw/main/installer.sh",
		"the project page": base + "/example-group/alpha-lookalike/-/raw/main/x",
	} {
		h.fails("create_release", map[string]any{"project": alpha, "tag_name": gitlabtest.TagPlain, "dry_run": true,
			"links": []map[string]any{link(name, u)}}, "blocked")
	}
	h.fails("create_release", map[string]any{"project": alpha, "tag_name": gitlabtest.TagPlain, "dry_run": true,
		"links": []map[string]any{{"name": "x", "url": base + "/example-group/alpha/x", "link_type": "script"}}}, "invalid")
	h.fails("create_release", map[string]any{"project": alpha, "tag_name": gitlabtest.TagPlain, "dry_run": true,
		"links": []map[string]any{{"name": "x", "url": base + "/example-group/alpha/x", "direct_asset_path": "/../up"}}}, "invalid")
	h.fails("create_release", map[string]any{"project": alpha, "tag_name": gitlabtest.TagPlain, "dry_run": true,
		"links": []map[string]any{link(" ", base+"/example-group/alpha/x")}}, "invalid")
}
