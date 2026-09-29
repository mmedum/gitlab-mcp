package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/mmedum/gitlab-mcp/v2/internal/gapi/gitlabtest"
	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
)

// The review and history reads against the in-memory instance. Every
// merge request of ProjectAlpha changes the same six files, one of each
// kind GitLab marks (gitlabtest.fillReview).

func TestListMRFiles(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, out := h.ok("list_mr_files", map[string]any{"project": gitlabtest.ProjectAlpha, "iid": 1})
	if get(out, "listing", "total") != float64(6) || get(out, "listing", "complete") != true {
		t.Fatalf("listing = %v", get(out, "listing"))
	}
	want := map[string]string{
		"src/login.go":     "added +4 -0",
		"README.md":        "modified +1 -1",
		"docs/new-name.md": "renamed +0 -0",
		"assets/logo.png":  "modified +0 -0 binary",
		"vendor/big.txt":   "modified +0 -0 too_large",
		"gen/api.pb.go":    "modified +0 -0 collapsed generated",
	}
	for i := range 6 {
		f := get(out, "files", i).(map[string]any)
		got := fmt.Sprintf("%s +%v -%v", f["status"], f["additions"], f["deletions"])
		for _, m := range []string{"binary", "too_large", "collapsed"} {
			if f[m] == true {
				got += " " + m
			}
		}
		if f["generated_file"] == true {
			got += " generated"
		}
		if want[f["new_path"].(string)] != got {
			t.Errorf("%s: %s, want %s", f["new_path"], got, want[f["new_path"].(string)])
		}
	}
	if !strings.Contains(text, "docs/old-name.md -> docs/new-name.md") || !strings.Contains(text, "(too large, no diff sent)") {
		t.Errorf("text:\n%s", text)
	}
	_, page := h.ok("list_mr_files", map[string]any{"project": gitlabtest.ProjectAlpha, "iid": 1, "max": 4})
	tok, _ := get(page, "listing", "next_page_token").(string)
	_, rest := h.ok("list_mr_files", map[string]any{"project": gitlabtest.ProjectAlpha, "iid": 1, "max": 4, "page_token": tok})
	if get(rest, "listing", "returned") != float64(2) || get(rest, "listing", "complete") != true {
		t.Errorf("second page = %v", get(rest, "listing"))
	}
	h.fails("list_mr_files", map[string]any{"project": gitlabtest.ProjectAlpha, "iid": 99}, "not_found")
}

func TestGetMRDiff(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, out := h.ok("get_mr_diff", map[string]any{"project": gitlabtest.ProjectAlpha, "iid": 1})
	var shown, notShown []string
	for _, f := range get(out, "files").([]any) {
		shown = append(shown, f.(map[string]any)["new_path"].(string))
	}
	for _, f := range get(out, "files_not_shown").([]any) {
		m := f.(map[string]any)
		notShown = append(notShown, m["new_path"].(string)+":"+m["reason"].(string))
	}
	if fmt.Sprint(shown) != "[src/login.go README.md docs/new-name.md assets/logo.png]" ||
		fmt.Sprint(notShown) != "[vendor/big.txt:too_large gen/api.pb.go:collapsed]" {
		t.Errorf("shown %v, not shown %v", shown, notShown)
	}
	if get(out, "files_complete") != true || get(out, "next_file_offset") != nil {
		t.Errorf("complete %v, next %v", get(out, "files_complete"), get(out, "next_file_offset"))
	}
	if !strings.Contains(text, "+func login() {}") || !strings.Contains(text, "GitLab did not return a diff: too large") {
		t.Errorf("text:\n%s", text)
	}

	_, one := h.ok("get_mr_diff", map[string]any{"project": gitlabtest.ProjectAlpha, "iid": 1, "paths": []any{"docs/old-name.md"}})
	if len(get(one, "files").([]any)) != 1 || get(one, "files", 0, "status") != "renamed" {
		t.Errorf("by old path: %v", get(one, "files"))
	}
	text = h.fails("get_mr_diff", map[string]any{"project": gitlabtest.ProjectAlpha, "iid": 1, "paths": []any{"src/nope.go"}}, "invalid")
	if !strings.Contains(text, "src/nope.go") || !strings.Contains(text, "list_mr_files") {
		t.Errorf("refusal: %s", text)
	}
	h.fails("get_mr_diff", map[string]any{"project": gitlabtest.ProjectAlpha, "iid": 1, "file_offset": 7}, "invalid")
}

func TestListMRCommits(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, out := h.ok("list_mr_commits", map[string]any{"project": gitlabtest.ProjectAlpha, "iid": 2})
	if get(out, "listing", "total") != float64(1) || get(out, "commits", 0, "untrusted_title") != "Add login stub" {
		t.Errorf("commits = %v", get(out, "commits"))
	}
	if !strings.Contains(text, "by Bob Example") {
		t.Errorf("text:\n%s", text)
	}
}

func TestListReviewComments(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, out := h.ok("list_review_comments", map[string]any{"project": gitlabtest.ProjectAlpha, "iid": 1})
	if get(out, "listing", "total") != float64(2) {
		t.Fatalf("listing = %v", get(out, "listing"))
	}
	if get(out, "drafts", 0, "position", "new_line") != float64(3) || get(out, "drafts", 0, "discussion_id") != "" {
		t.Errorf("inline draft = %v", get(out, "drafts", 0))
	}
	if get(out, "drafts", 1, "resolve_discussion") != true || get(out, "drafts", 1, "position") != nil {
		t.Errorf("reply draft = %v", get(out, "drafts", 1))
	}
	if !strings.Contains(text, "a new inline thread on src/login.go new line 3") || !strings.Contains(text, "that resolves it when published") {
		t.Errorf("text:\n%s", text)
	}
	// Another account's drafts are not listed: GitLab shows each person
	// only their own.
	bob := newHarness(t, harnessOptions{token: func(gl *gitlabtest.Server) string { return gl.TokenFor("bob", "api") }})
	_, none := bob.ok("list_review_comments", map[string]any{"project": gitlabtest.ProjectAlpha, "iid": 1})
	if get(none, "listing", "returned") != float64(0) {
		t.Errorf("bob sees %v", get(none, "drafts"))
	}
}

func TestListReviewCommentsPagesByBudget(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	// Eight drafts of 5,000 characters: six fit the 30,000 budget.
	var drafts []map[string]any
	for i := range 8 {
		drafts = append(drafts, map[string]any{"id": 81001 + i, "author_id": 1001, "note": strings.Repeat("x", 5000)})
	}
	body, _ := json.Marshal(drafts)
	path := fmt.Sprintf("/projects/%d/merge_requests/1/draft_notes", alphaID)
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodGet, Path: path, Times: 2, Status: http.StatusOK, Body: string(body),
		Header: http.Header{"Content-Type": {"application/json"}}})
	text, first := h.ok("list_review_comments", map[string]any{"project": alphaID, "iid": 1})
	if get(first, "listing", "returned") != float64(6) || fmt.Sprint(get(first, "not_shown")) != "[81007 81008]" {
		t.Fatalf("first page: %v, not shown %v", get(first, "listing"), get(first, "not_shown"))
	}
	if !strings.Contains(text, "the next page starts with drafts 81007, 81008") {
		t.Errorf("text does not name the drafts left out:\n%s", text[len(text)-300:])
	}
	tok := get(first, "listing", "next_page_token").(string)
	_, second := h.ok("list_review_comments", map[string]any{"project": alphaID, "iid": 1, "page_token": tok})
	if get(second, "listing", "returned") != float64(2) || get(second, "listing", "complete") != true {
		t.Errorf("second page: %v", get(second, "listing"))
	}
	// A draft published between pages does not move the next page: it
	// still starts at the draft the token names.
	body, _ = json.Marshal(append(drafts[:1:1], drafts[2:]...))
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodGet, Path: path, Status: http.StatusOK, Body: string(body),
		Header: http.Header{"Content-Type": {"application/json"}}})
	_, after := h.ok("list_review_comments", map[string]any{"project": alphaID, "iid": 1, "page_token": tok})
	if get(after, "drafts", 0, "id") != float64(81007) {
		t.Errorf("after a draft was published the page starts at %v, want 81007", get(after, "drafts", 0, "id"))
	}
	h.fails("list_review_comments", map[string]any{"project": alphaID, "iid": 2, "page_token": tok}, "invalid")
}

func TestCompareRefs(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, out := h.ok("compare_refs", map[string]any{"project": gitlabtest.ProjectAlpha, "from": "main", "to": "feature/login"})
	if get(out, "commits_total") != float64(1) || get(out, "commits", 0, "untrusted_title") != "Add login stub" {
		t.Errorf("commits = %v", get(out, "commits"))
	}
	if get(out, "files", 0, "new_path") != "src/login.go" || get(out, "same_ref") != false || get(out, "straight") != false {
		t.Errorf("compare = %v", out)
	}
	if !strings.Contains(text, "compared from their merge base") {
		t.Errorf("text:\n%s", text)
	}
	// The lightweight tag is main's head.
	_, same := h.ok("compare_refs", map[string]any{"project": gitlabtest.ProjectAlpha, "from": gitlabtest.TagPlain, "to": "main",
		"straight": true})
	if get(same, "same_ref") != true || get(same, "commits_total") != float64(0) || get(same, "straight") != true {
		t.Errorf("same ref = %v", same)
	}
	h.fails("compare_refs", map[string]any{"project": gitlabtest.ProjectAlpha, "from": "main", "to": "nope"}, "not_found")
	h.fails("compare_refs", map[string]any{"project": gitlabtest.ProjectAlpha, "from": "main", "to": "feature/login",
		"commit_offset": 2}, "invalid")
}

func TestCompareRefsPagesCommits(t *testing.T) {
	h := newHarness(t, harnessOptions{gl: gitlabtest.Options{AlphaCommits: 130}})
	// main's oldest commit is the last of its history.
	_, page := h.ok("list_commits", map[string]any{"project": gitlabtest.ProjectAlpha, "max": 100})
	tok := get(page, "listing", "next_page_token").(string)
	_, rest := h.ok("list_commits", map[string]any{"project": gitlabtest.ProjectAlpha, "max": 100, "page_token": tok})
	oldest := get(rest, "commits", 29, "id").(string)
	newest := get(page, "commits", 0, "id").(string)

	args := map[string]any{"project": gitlabtest.ProjectAlpha, "from": oldest, "to": "main"}
	text, first := h.ok("compare_refs", args)
	if get(first, "commits_total") != float64(129) || len(get(first, "commits").([]any)) != 100 ||
		get(first, "next_commit_offset") != float64(100) {
		t.Fatalf("first: total %v, shown %d, next %v", get(first, "commits_total"), len(get(first, "commits").([]any)),
			get(first, "next_commit_offset"))
	}
	if get(first, "commits", 0, "id") != newest {
		t.Errorf("first commit %v, want the newest %s", get(first, "commits", 0, "id"), newest)
	}
	if !strings.Contains(text, "More commits: pass commit_offset=100.") {
		t.Errorf("text does not say how to continue")
	}
	args["commit_offset"] = 100
	_, second := h.ok("compare_refs", args)
	if len(get(second, "commits").([]any)) != 29 || get(second, "next_commit_offset") != nil {
		t.Errorf("second: %d commits, next %v", len(get(second, "commits").([]any)), get(second, "next_commit_offset"))
	}
}

func TestListTags(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, out := h.ok("list_tags", map[string]any{"project": gitlabtest.ProjectAlpha, "order_by": "name", "sort": "asc"})
	if get(out, "tags", 0, "name") != gitlabtest.TagPlain || get(out, "tags", 1, "name") != gitlabtest.TagRelease {
		t.Fatalf("tags = %v", get(out, "tags"))
	}
	if get(out, "tags", 1, "protected") != true || get(out, "tags", 1, "release") != true ||
		get(out, "tags", 1, "untrusted_message") != "Release 1.0" || get(out, "tags", 0, "created_at") != nil {
		t.Errorf("tags = %v", get(out, "tags"))
	}
	if !strings.Contains(text, "v0.9 (lightweight)") || !strings.Contains(text, "v1.0 (protected, has a release)") {
		t.Errorf("text:\n%s", text)
	}
	_, one := h.ok("list_tags", map[string]any{"project": gitlabtest.ProjectAlpha, "search": "^v1"})
	if get(one, "listing", "returned") != float64(1) {
		t.Errorf("search ^v1: %v", get(one, "tags"))
	}
}

// Paging through a large merge request reads each page of diffs about
// once: a file_offset starts at the page that holds it.
func TestGetMRDiffReadsFromItsPage(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	var diffs []gitlab.Diff
	for i := range 250 {
		path := fmt.Sprintf("src/file%03d.go", i)
		diffs = append(diffs, gitlab.Diff{OldPath: path, NewPath: path, AMode: "100644", BMode: "100644",
			Diff: "@@ -1 +1 @@\n-" + strings.Repeat("a", 500) + "\n+" + strings.Repeat("b", 500) + "\n"})
	}
	h.gl.SetMRDiffs(gitlabtest.ProjectAlpha, 1, diffs)
	pages := func() []string {
		var out []string
		for _, r := range h.gl.Requests() {
			if strings.HasSuffix(r.EscapedPath, "/merge_requests/1/diffs") {
				q, _ := url.ParseQuery(r.RawQuery)
				out = append(out, q.Get("page"))
			}
		}
		return out
	}
	h.gl.ResetRequests()
	_, out := h.ok("get_mr_diff", map[string]any{"project": gitlabtest.ProjectAlpha, "iid": 1})
	next := get(out, "next_file_offset")
	if next == nil || get(out, "files_complete") != false || fmt.Sprint(pages()) != "[1]" {
		t.Fatalf("next %v, complete %v, pages %v", next, get(out, "files_complete"), pages())
	}
	h.gl.ResetRequests()
	_, out = h.ok("get_mr_diff", map[string]any{"project": gitlabtest.ProjectAlpha, "iid": 1, "file_offset": 150})
	if get(out, "files", 0, "new_path") != "src/file150.go" || fmt.Sprint(pages()) != "[2]" {
		t.Errorf("first %v, pages %v", get(out, "files", 0, "new_path"), pages())
	}
	if n := get(out, "next_file_offset"); n == nil || n.(float64) <= 150 || n.(float64) >= 200 {
		t.Errorf("next_file_offset = %v", n)
	}
	// The last files end the listing.
	h.gl.ResetRequests()
	_, out = h.ok("get_mr_diff", map[string]any{"project": gitlabtest.ProjectAlpha, "iid": 1, "file_offset": 230})
	if get(out, "files_complete") != true || get(out, "next_file_offset") != nil || fmt.Sprint(pages()) != "[3]" {
		t.Errorf("complete %v, next %v, pages %v", get(out, "files_complete"), get(out, "next_file_offset"), pages())
	}
	h.fails("get_mr_diff", map[string]any{"project": gitlabtest.ProjectAlpha, "iid": 1, "file_offset": 260}, "invalid")
}

// One diff larger than the whole budget is read to its end by
// diff_offset, and nothing is lost or repeated at the cuts.
func TestGetMRDiffContinuesACutDiff(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	var body strings.Builder
	body.WriteString("@@ -0,0 +1,4000 @@\n")
	for i := range 4000 {
		fmt.Fprintf(&body, "+line %04d of a generated file\n", i)
	}
	h.gl.SetMRDiffs(gitlabtest.ProjectAlpha, 1, []gitlab.Diff{
		{OldPath: "gen/big.go", NewPath: "gen/big.go", AMode: "100644", BMode: "100644", NewFile: true, Diff: body.String()},
		{OldPath: "README.md", NewPath: "README.md", AMode: "100644", BMode: "100644", Diff: "@@ -1 +1 @@\n-a\n+b\n"},
	})
	var got strings.Builder
	args := map[string]any{"project": gitlabtest.ProjectAlpha, "iid": 1}
	for range 10 {
		text, out := h.ok("get_mr_diff", args)
		got.WriteString(get(out, "files", 0, "untrusted_diff").(string))
		next := get(out, "files", 0, "continue_diff_offset")
		if next == nil {
			break
		}
		if get(out, "files", 0, "truncated") != true || !strings.Contains(text, fmt.Sprintf("diff_offset=%d", int(next.(float64)))) {
			t.Fatalf("a cut diff without its continuation:\n%s", text[max(0, len(text)-400):])
		}
		args = map[string]any{"project": gitlabtest.ProjectAlpha, "iid": 1, "diff_offset": next}
	}
	if got.String() != body.String() {
		t.Errorf("the pieces read are not the diff: %d of %d characters", got.Len(), body.Len())
	}
	h.fails("get_mr_diff", map[string]any{"project": gitlabtest.ProjectAlpha, "iid": 1, "diff_offset": body.Len() + 1}, "invalid")
	h.fails("get_mr_diff", map[string]any{"project": gitlabtest.ProjectAlpha, "iid": 1, "file_offset": 2, "diff_offset": 5}, "invalid")
}
