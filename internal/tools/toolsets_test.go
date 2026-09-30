package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

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

// snippetAt is a snippet's updated_at as get_snippet shows it; project
// is "" for a personal snippet.
func snippetAt(h *harness, project string, id any) string {
	h.t.Helper()
	args := map[string]any{"snippet_id": id}
	if project != "" {
		args["project"] = project
	}
	_, out := h.ok("get_snippet", args)
	return get(out, "updated_at").(string)
}

func TestUpdateOneFileSnippet(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: withToolsets(config.Config{}, "snippets")})
	_, made := h.ok("create_snippet", map[string]any{"title": "Mine", "description": "one\ntwo",
		"files": []map[string]any{{"path": "a.txt", "content": "old\n"}}})
	id := get(made, "id").(float64)
	at := snippetAt(h, "", id)
	args := func(extra map[string]any) map[string]any {
		out := map[string]any{"snippet_id": id, "updated_at": at}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	before := writesSent(h)
	h.fails("update_snippet", args(nil), "invalid")
	h.fails("update_snippet", args(map[string]any{"title": " "}), "invalid")
	h.fails("update_snippet", args(map[string]any{"content": ""}), "invalid")
	h.fails("update_snippet", args(map[string]any{"content": "x", "files": []map[string]any{{"action": "create", "path": "b", "content": "b"}}}),
		"invalid")
	h.fails("update_snippet", map[string]any{"snippet_id": id, "updated_at": "yesterday", "title": "x"}, "invalid")
	h.fails("update_snippet", map[string]any{"snippet_id": id, "updated_at": "2020-01-01T00:00:00Z", "title": "x"}, "stale")
	_, dry := h.ok("update_snippet", args(map[string]any{"content": "new\n", "dry_run": true}))
	if get(dry, "outcome") != "dry_run" || fmt.Sprint(get(dry, "would_send", "fields")) != "[content]" {
		t.Errorf("dry run = %v", dry)
	}
	if writesSent(h) != before {
		t.Fatal("a refusal or a dry run wrote")
	}

	text, out := h.ok("update_snippet", args(map[string]any{"title": "Mine, renamed", "description": "one", "content": "new\n"}))
	if get(out, "outcome") != "updated" || get(out, "visibility") != "private" || fmt.Sprint(get(out, "changed")) != "[title description content]" ||
		get(out, "description_removed", "lines") != float64(1) || !strings.Contains(text, "A personal snippet.") {
		t.Errorf("update = %v\n%s", out, text)
	}
	if files := h.gl.SnippetFiles(int64(id)); len(files) != 1 || files[0] != [2]string{"a.txt", "new\n"} {
		t.Errorf("GitLab holds %v", files)
	}
	// The witness moved with the write.
	h.fails("update_snippet", args(map[string]any{"title": "again"}), "stale")

	// A change to the content is a commit, and GitLab moves updated_at
	// again once it has processed it: no witness is handed on, and a
	// read gives the one that holds.
	if get(out, "updated_at") != nil || !strings.Contains(text, "read the snippet with get_snippet") {
		t.Errorf("a witness about to go stale was handed on: %v\n%s", out, text)
	}
	at = snippetAt(h, "", id)
	// A title alone is no commit: its answer's updated_at holds.
	_, renamed := h.ok("update_snippet", args(map[string]any{"title": "Mine, renamed again"}))
	at, _ = get(renamed, "updated_at").(string)
	if at == "" || at != snippetAt(h, "", id) {
		t.Fatalf("title change = %v", renamed)
	}
	_, same := h.ok("update_snippet", args(map[string]any{"title": "Mine, renamed again"}))
	if get(same, "outcome") != "unchanged" || get(same, "updated_at") != at {
		t.Errorf("same title = %v", same)
	}
}

func TestUpdateSnippetFiles(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: withToolsets(config.Config{}, "snippets")})
	_, made := h.ok("create_snippet", map[string]any{"project": alpha, "title": "Two",
		"files": []map[string]any{{"path": "a.md", "content": "# A\n"}, {"path": "b.sh", "content": "echo b\n"}}})
	id := get(made, "id").(float64)
	at := snippetAt(h, alpha, id)
	files := func(changes ...map[string]any) map[string]any {
		return map[string]any{"project": alpha, "snippet_id": id, "updated_at": at, "files": changes}
	}
	change := func(action, path, previous, content string) map[string]any {
		out := map[string]any{"action": action, "path": path}
		if previous != "" {
			out["previous_path"] = previous
		}
		if content != "" {
			out["content"] = content
		}
		return out
	}

	before := writesSent(h)
	// content replaces a one-file snippet's file; this one has two.
	text := h.fails("update_snippet", map[string]any{"project": alpha, "snippet_id": id, "updated_at": at, "content": "x"}, "invalid")
	if !strings.Contains(text, "has 2 files") {
		t.Errorf("content on two files: %s", text)
	}
	for _, bad := range []map[string]any{
		files(change("rename", "a.md", "", "x")),
		files(change("create", "a.md", "", "x")),
		files(change("create", "c.md", "", "")),
		files(change("update", "missing.md", "", "x")),
		files(change("update", "a.md", "b.sh", "x")),
		files(change("delete", "a.md", "", "x")),
		files(change("move", "c.md", "", "")),
		files(change("move", "a.md", "a.md", "")),
		files(change("move", "b.sh", "a.md", "")),
		files(change("delete", "a.md", "", ""), change("update", "a.md", "", "x")),
		files(change("delete", "a.md", "", ""), change("delete", "b.sh", "", "")),
		files(change("update", "", "", "x")),
	} {
		h.fails("update_snippet", bad, "invalid")
	}
	if writesSent(h) != before {
		t.Fatal("a refused file change was sent")
	}

	text, out := h.ok("update_snippet", files(change("update", "b.sh", "", "echo c\n"), change("move", "c.md", "a.md", ""),
		change("create", "d.txt", "", "d\n"), change("delete", "c.md", "", "")))
	if get(out, "outcome") != "updated" || strings.Join(strs(get(out, "files")), ",") != "b.sh,d.txt" ||
		fmt.Sprint(get(out, "changed")) != "[files]" || !strings.Contains(text, "Project: ") {
		t.Errorf("file changes = %v\n%s", out, text)
	}
	if got := h.gl.SnippetFiles(int64(id)); fmt.Sprint(got) != "[[b.sh echo c\n] [d.txt d\n]]" {
		t.Errorf("GitLab holds %q", got)
	}
	if v := h.gl.SnippetVisibility(int64(id)); v != "private" {
		t.Errorf("visibility = %s", v)
	}
}

// Another person's snippet is refused, even where GitLab lets a
// maintainer change it; one of yours in a project is reached through
// its project.
func TestSnippetWritesAreYourOwn(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: withToolsets(destructive, "snippets")})
	at := snippetAt(h, alpha, gitlabtest.SnippetAlpha)
	before := writesSent(h)
	text := h.fails("update_snippet", map[string]any{"project": alpha, "snippet_id": gitlabtest.SnippetAlpha, "updated_at": at,
		"title": "x"}, "blocked")
	if !strings.Contains(text, "@bob") {
		t.Errorf("another's snippet: %s", text)
	}
	h.fails("delete_snippet", map[string]any{"project": alpha, "snippet_id": gitlabtest.SnippetAlpha, "updated_at": at, "confirm": true},
		"blocked")
	if writesSent(h) != before {
		t.Fatal("a write to another's snippet was sent")
	}

	_, made := h.ok("create_snippet", map[string]any{"project": alpha, "title": "Mine", "files": []map[string]any{{"path": "a", "content": "a"}}})
	id := get(made, "id").(float64)
	at = snippetAt(h, alpha, id)
	before = writesSent(h)
	text = h.fails("update_snippet", map[string]any{"snippet_id": id, "updated_at": at, "title": "x"}, "invalid")
	if !strings.Contains(text, "pass that project") {
		t.Errorf("a project snippet by the personal route: %s", text)
	}
	h.fails("delete_snippet", map[string]any{"snippet_id": id, "updated_at": at, "confirm": true}, "invalid")
	if writesSent(h) != before {
		t.Fatal("a refusal wrote")
	}
}

// Under the write allow-list a personal snippet is in no namespace, and
// a project outside it is refused.
func TestSnippetWritesHoldToTheAllowList(t *testing.T) {
	cfg := withToolsets(config.Config{EnableDestructive: true, WriteNamespaces: []string{gitlabtest.GroupSub}}, "snippets")
	h := newHarness(t, harnessOptions{cfg: cfg})
	personal := snippetAt(h, "", gitlabtest.SnippetPersonal)
	project := snippetAt(h, alpha, gitlabtest.SnippetAlpha)
	before := writesSent(h)
	h.fails("update_snippet", map[string]any{"snippet_id": gitlabtest.SnippetPersonal, "updated_at": personal, "title": "x"}, "blocked")
	h.fails("delete_snippet", map[string]any{"snippet_id": gitlabtest.SnippetPersonal, "updated_at": personal, "confirm": true}, "blocked")
	h.fails("update_snippet", map[string]any{"project": alpha, "snippet_id": gitlabtest.SnippetAlpha, "updated_at": project, "title": "x"},
		"blocked")
	h.fails("delete_snippet", map[string]any{"project": alpha, "snippet_id": gitlabtest.SnippetAlpha, "updated_at": project, "confirm": true},
		"blocked")
	if writesSent(h) != before {
		t.Fatal("a write outside the allow-list was sent")
	}
}

func TestDeleteSnippet(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: withToolsets(destructive, "snippets")})
	// GitLab keeps updated_at to the microsecond and shows the
	// millisecond; the witness still holds.
	if !h.gl.TouchSnippet(gitlabtest.SnippetPersonal, time.Date(2026, 9, 1, 10, 0, 0, 123456000, time.UTC)) {
		t.Fatal("touch")
	}
	at := snippetAt(h, "", gitlabtest.SnippetPersonal)
	args := func(at string) map[string]any {
		return map[string]any{"snippet_id": gitlabtest.SnippetPersonal, "updated_at": at, "confirm": true}
	}
	before := writesSent(h)
	h.fails("delete_snippet", map[string]any{"snippet_id": gitlabtest.SnippetPersonal, "updated_at": at}, "blocked")
	h.fails("delete_snippet", args("2020-01-01T00:00:00Z"), "stale")
	h.fails("delete_snippet", args("yesterday"), "invalid")
	dryArgs := args(at)
	dryArgs["dry_run"] = true
	if _, dry := h.ok("delete_snippet", dryArgs); get(dry, "outcome") != "dry_run" {
		t.Errorf("dry run = %v", dry)
	}
	if writesSent(h) != before {
		t.Fatal("a refusal or a dry run wrote")
	}

	text, out := h.ok("delete_snippet", args(at))
	if get(out, "outcome") != "deleted" || !strings.Contains(text, "finds no such snippet") {
		t.Errorf("delete = %v\n%s", out, text)
	}
	if h.gl.SnippetFiles(gitlabtest.SnippetPersonal) != nil {
		t.Error("the snippet is still there")
	}
	// The witness went as If-Unmodified-Since, stretched to the end of
	// its millisecond.
	sent := ""
	for _, r := range h.gl.Requests() {
		if r.Method == "DELETE" {
			sent = r.IfUnmodifiedSince
		}
	}
	if sent != "2026-09-01T10:00:00.123999Z" {
		t.Errorf("If-Unmodified-Since = %q", sent)
	}
	h.fails("delete_snippet", args(at), "not_found")

	// A project snippet of your own.
	_, made := h.ok("create_snippet", map[string]any{"project": alpha, "title": "Mine", "files": []map[string]any{{"path": "a", "content": "a"}}})
	id := get(made, "id").(float64)
	_, out = h.ok("delete_snippet", map[string]any{"project": alpha, "snippet_id": id, "updated_at": snippetAt(h, alpha, id), "confirm": true})
	if get(out, "outcome") != "deleted" || get(out, "target", "project", "path") != alpha {
		t.Errorf("project delete = %v", out)
	}
}

// A change between the server's read and its delete: GitLab's own check
// refuses it with 412, which is [stale].
func TestDeleteSnippetChangedAfterTheReadIsStale(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: withToolsets(destructive, "snippets")})
	at := snippetAt(h, "", gitlabtest.SnippetPersonal)
	h.gl.Inject(gitlabtest.Fault{Method: "DELETE", Path: fmt.Sprintf("/snippets/%d", gitlabtest.SnippetPersonal), Status: 412,
		Body: `{"message":"412 Precondition Failed"}`})
	h.fails("delete_snippet", map[string]any{"snippet_id": gitlabtest.SnippetPersonal, "updated_at": at, "confirm": true}, "stale")
	if h.gl.SnippetFiles(gitlabtest.SnippetPersonal) == nil {
		t.Error("the snippet was deleted")
	}
	if !h.gl.TouchSnippet(gitlabtest.SnippetPersonal, time.Now().Add(time.Hour)) {
		t.Fatal("touch")
	}
	h.fails("delete_snippet", map[string]any{"snippet_id": gitlabtest.SnippetPersonal, "updated_at": at, "confirm": true}, "stale")
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

// A delete is read back: a snippet GitLab says it deleted and still
// holds is a defect to report, and one whose answer was lost is gone.
func TestDeleteSnippetIsReadBack(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: withToolsets(destructive, "snippets")})
	at := snippetAt(h, "", gitlabtest.SnippetPersonal)
	path := fmt.Sprintf("/snippets/%d", gitlabtest.SnippetPersonal)
	h.gl.Inject(gitlabtest.Fault{Method: "DELETE", Path: path, Status: 204})
	h.fails("delete_snippet", map[string]any{"snippet_id": gitlabtest.SnippetPersonal, "updated_at": at, "confirm": true}, "unexpected")
	h.gl.Inject(gitlabtest.Fault{Method: "DELETE", Path: path, Status: 502, AfterApply: true, Body: `{"message":"502 Bad Gateway"}`})
	_, out := h.ok("delete_snippet", map[string]any{"snippet_id": gitlabtest.SnippetPersonal, "updated_at": at, "confirm": true})
	if get(out, "outcome") != "deleted" || !strings.Contains(fmt.Sprint(get(out, "notes")), "it is gone") {
		t.Errorf("lost delete = %v", out)
	}
}

// A snippet that is not private is not written to, personal or in a
// project: writing into one is how private content leaves (§4.7).
func TestUpdateSnippetWritesOnlyPrivateSnippets(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: withToolsets(config.Config{}, "snippets")})
	for _, project := range []string{"", alpha} {
		args := map[string]any{"title": "Mine", "files": []map[string]any{{"path": "a", "content": "a"}}}
		if project != "" {
			args["project"] = project
		}
		_, made := h.ok("create_snippet", args)
		id := get(made, "id").(float64)
		for _, visibility := range []string{"internal", "public"} {
			if !h.gl.SetSnippetVisibility(int64(id), visibility) {
				t.Fatal("visibility")
			}
			call := map[string]any{"snippet_id": id, "updated_at": snippetAt(h, project, id), "title": "x"}
			if project != "" {
				call["project"] = project
			}
			before := writesSent(h)
			text := h.fails("update_snippet", call, "blocked")
			if !strings.Contains(text, "writes only to private snippets") || writesSent(h) != before {
				t.Errorf("%s %s snippet: %s", project, visibility, text)
			}
		}
	}
}

// Content that is only white space is refused before anything is sent,
// as GitLab refuses it.
func TestUpdateSnippetRefusesBlankContent(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: withToolsets(config.Config{}, "snippets")})
	at := snippetAt(h, "", gitlabtest.SnippetPersonal)
	before := writesSent(h)
	h.fails("update_snippet", map[string]any{"snippet_id": gitlabtest.SnippetPersonal, "updated_at": at, "content": " \n\t"}, "invalid")
	for _, action := range []string{"create", "update"} {
		path := "scratch.txt"
		if action == "create" {
			path = "new.txt"
		}
		h.fails("update_snippet", map[string]any{"snippet_id": gitlabtest.SnippetPersonal, "updated_at": at,
			"files": []map[string]any{{"action": action, "path": path, "content": "  \n"}}}, "invalid")
	}
	if writesSent(h) != before {
		t.Fatal("blank content was sent")
	}
}

// A change to a file other than the first leaves the updated_at GitLab
// answers where it was; the commit's post-receive job moves it after.
// The result hands on no witness, and a read gives the moved one.
func TestUpdateSnippetFilesMoveUpdatedAtAfterTheAnswer(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: withToolsets(destructive, "snippets")})
	_, made := h.ok("create_snippet", map[string]any{"project": alpha, "title": "Two",
		"files": []map[string]any{{"path": "a.md", "content": "# A\n"}, {"path": "b.sh", "content": "echo b\n"}}})
	id := get(made, "id").(float64)
	at := snippetAt(h, alpha, id)
	text, out := h.ok("update_snippet", map[string]any{"project": alpha, "snippet_id": id, "updated_at": at, "files": []map[string]any{
		{"action": "update", "path": "a.md", "content": "# A\n"}, {"action": "update", "path": "b.sh", "content": "echo z\n"}}})
	if get(out, "outcome") != "updated" || fmt.Sprint(get(out, "changed")) != "[files]" || get(out, "updated_at") != nil ||
		!strings.Contains(text, "read the snippet with get_snippet") {
		t.Errorf("update = %v\n%s", out, text)
	}
	if got := h.gl.SnippetFiles(int64(id)); got[1][1] != "echo z\n" {
		t.Errorf("GitLab holds %q", got)
	}
	// The old witness is stale now, and the one a read gives deletes.
	h.fails("delete_snippet", map[string]any{"project": alpha, "snippet_id": id, "updated_at": at, "confirm": true}, "stale")
	h.ok("delete_snippet", map[string]any{"project": alpha, "snippet_id": id, "updated_at": snippetAt(h, alpha, id), "confirm": true})
}

// A change that creates, deletes or moves a file is sent once: a lost
// answer is settled by reading the snippet, never by sending it again.
// A change of content alone may repeat.
func TestUpdateSnippetFileActionsAreNotRepeated(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: withToolsets(config.Config{}, "snippets")})
	path := fmt.Sprintf("/snippets/%d", gitlabtest.SnippetPersonal)
	puts := func() int {
		n := 0
		for _, r := range h.gl.Requests() {
			if r.Method == "PUT" {
				n++
			}
		}
		return n
	}
	create := func(name string) map[string]any {
		return map[string]any{"snippet_id": gitlabtest.SnippetPersonal, "updated_at": snippetAt(h, "", gitlabtest.SnippetPersonal),
			"files": []map[string]any{{"action": "create", "path": name, "content": "x\n"}}}
	}

	// Landed, answer lost.
	h.gl.Inject(gitlabtest.Fault{Method: "PUT", Path: path, Status: 502, AfterApply: true, Body: `{"message":"502 Bad Gateway"}`})
	h.gl.ResetRequests()
	text := h.fails("update_snippet", create("b.txt"), "ambiguous_outcome")
	if puts() != 1 || !strings.Contains(text, "most likely landed") || !strings.Contains(text, "b.txt") {
		t.Errorf("%d PUTs: %s", puts(), text)
	}
	// Not landed.
	h.gl.Inject(gitlabtest.Fault{Method: "PUT", Path: path, Status: 502, Body: `{"message":"502 Bad Gateway"}`})
	h.gl.ResetRequests()
	text = h.fails("update_snippet", create("c.txt"), "ambiguous_outcome")
	if puts() != 1 || !strings.Contains(text, "as it was") {
		t.Errorf("%d PUTs: %s", puts(), text)
	}
	if len(h.gl.SnippetFiles(gitlabtest.SnippetPersonal)) != 2 {
		t.Errorf("GitLab holds %q", h.gl.SnippetFiles(gitlabtest.SnippetPersonal))
	}
	// A title alone repeats.
	h.gl.Inject(gitlabtest.Fault{Method: "PUT", Path: path, Status: 502, Body: `{"message":"502 Bad Gateway"}`})
	h.gl.ResetRequests()
	h.ok("update_snippet", map[string]any{"snippet_id": gitlabtest.SnippetPersonal,
		"updated_at": snippetAt(h, "", gitlabtest.SnippetPersonal), "title": "Again"})
	if puts() != 2 {
		t.Errorf("%d PUTs for a title", puts())
	}
}

// The witness goes to the end of the millisecond GitLab showed, and
// GitLab compares it with updated_at to the microsecond: a change after
// the server's read within that millisecond is not seen, one after it
// is refused [stale].
func TestDeleteSnippetWitnessAgainstAChangeAfterTheRead(t *testing.T) {
	for _, c := range []struct {
		name  string
		after time.Duration
		class string
	}{
		{"within the shown millisecond", 400 * time.Microsecond, ""},
		{"after it", 700 * time.Microsecond, "stale"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{cfg: withToolsets(destructive, "snippets")})
			kept := time.Date(2026, 9, 1, 10, 0, 0, 123456000, time.UTC)
			if !h.gl.TouchSnippet(gitlabtest.SnippetPersonal, kept) {
				t.Fatal("touch")
			}
			at := snippetAt(h, "", gitlabtest.SnippetPersonal)
			h.gl.Inject(gitlabtest.Fault{Method: "DELETE", Path: fmt.Sprintf("/snippets/%d", gitlabtest.SnippetPersonal), Pass: true,
				Before: func() { h.gl.TouchSnippet(gitlabtest.SnippetPersonal, kept.Add(c.after)) }})
			args := map[string]any{"snippet_id": gitlabtest.SnippetPersonal, "updated_at": at, "confirm": true}
			if c.class == "" {
				h.ok("delete_snippet", args)
				if h.gl.SnippetFiles(gitlabtest.SnippetPersonal) != nil {
					t.Error("the snippet is still there")
				}
				return
			}
			h.fails("delete_snippet", args, c.class)
			if h.gl.SnippetFiles(gitlabtest.SnippetPersonal) == nil {
				t.Error("the snippet was deleted")
			}
		})
	}
}
