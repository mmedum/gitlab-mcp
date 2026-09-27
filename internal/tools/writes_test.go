package tools

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/mmedum/gitlab-mcp/internal/config"
	"github.com/mmedum/gitlab-mcp/internal/gapi/gitlabtest"
)

// The write tools against the in-memory instance: what each sends, what
// it refuses before sending, and what it reads back (§4.2–§4.7, §4.11).

const alpha = gitlabtest.ProjectAlpha

// writesSent counts the requests that could change something.
func writesSent(h *harness) int {
	n := 0
	for _, r := range h.gl.Requests() {
		if r.Method != http.MethodGet {
			n++
		}
	}
	return n
}

// issueWitness is an issue's updated_at, as get_issue reads it.
func issueWitness(h *harness, iid int) string {
	h.t.Helper()
	_, out := h.ok("get_issue", map[string]any{"project": alpha, "iid": iid})
	return get(out, "updated_at").(string)
}

func mrWitness(h *harness, iid int) string {
	h.t.Helper()
	_, out := h.ok("get_merge_request", map[string]any{"project": alpha, "iid": iid})
	return get(out, "updated_at").(string)
}

func strs(v any) []string {
	a, _ := v.([]any)
	out := make([]string, 0, len(a))
	for _, x := range a {
		out = append(out, fmt.Sprint(x))
	}
	return out
}

// ------------------------------------------------------------- issues

func TestCreateIssue(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, out := h.ok("create_issue", map[string]any{"project": alpha, "title": "A new issue", "description": "Some text.",
		"labels": []string{"bug", "priority::high", gitlabtest.GroupLabel}, "assignees": []string{"bob"}, "milestone": "Q1 goals",
		"due_date": "2026-02-01"})
	if get(out, "outcome") != "created" || get(out, "iid") == nil || get(out, "target", "visibility") != "public" {
		t.Errorf("result = %v", out)
	}
	if strings.Join(strs(get(out, "labels")), ",") != "bug,priority::high,"+gitlabtest.GroupLabel ||
		strings.Join(strs(get(out, "assignees")), ",") != "bob" || get(out, "milestone", "title") != "Q1 goals" {
		t.Errorf("fields = %v", out)
	}
	if !strings.Contains(text, "Created issue #") || !strings.Contains(text, "public: anyone can see what is written there") {
		t.Errorf("text:\n%s", text)
	}

	h.fails("create_issue", map[string]any{"project": alpha, "title": "x", "labels": []string{"no-such-label"}}, "invalid")
	h.fails("create_issue", map[string]any{"project": alpha, "title": "x", "assignees": []string{"nobody-here"}}, "invalid")
	h.fails("create_issue", map[string]any{"project": alpha, "title": "x", "milestone": "No such milestone"}, "invalid")
	h.fails("create_issue", map[string]any{"project": alpha, "title": "x", "due_date": "tomorrow"}, "invalid")
	h.fails("create_issue", map[string]any{"project": alpha, "title": "  "}, "invalid")
	h.fails("create_issue", map[string]any{"project": gitlabtest.ProjectSecret, "title": "x"}, "not_found")
}

func TestCreateIssueGuardsItsDescription(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	before := writesSent(h)
	text := h.fails("create_issue", map[string]any{"project": alpha, "title": "Guarded", "description": "Close it.\n\n/close"}, "blocked")
	if !strings.Contains(text, "description: GitLab would run a quick action in this text: line 3 /close") {
		t.Errorf("refusal: %s", text)
	}
	if writesSent(h) != before {
		t.Fatal("a refused body was sent")
	}
	_, out := h.ok("create_issue", map[string]any{"project": alpha, "title": "Escaped", "description": "Close it.\n\n/close",
		"escape_commands": true})
	if get(out, "state") != "opened" || get(out, "escaped_commands", 0, "command") != "close" {
		t.Errorf("an escaped /close changed the issue, or was not reported: %v", out)
	}
	_, issue := h.ok("get_issue", map[string]any{"project": alpha, "iid": get(out, "iid")})
	if !strings.Contains(fmt.Sprint(get(issue, "untrusted_description")), `\/close`) {
		t.Errorf("the stored description is not the escaped one: %v", get(issue, "untrusted_description"))
	}
}

func TestDryRunsSendNothing(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	before := writesSent(h)
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"create_issue", map[string]any{"project": alpha, "title": "x", "labels": []string{"bug"}}},
		{"update_issue", map[string]any{"project": alpha, "iid": 1, "updated_at": issueWitness(h, 1), "state": "close"}},
		{"add_comment", map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "body": "x", "file": "README.md", "line": 3, "side": "new"}},
		{"add_review_comment", map[string]any{"project": alpha, "iid": 1, "body": "x"}},
		{"submit_review", map[string]any{"project": alpha, "iid": 1, "summary": "x"}},
		{"create_merge_request", map[string]any{"project": alpha, "source_branch": "release/1.0", "title": "x"}},
		{"update_merge_request", map[string]any{"project": alpha, "iid": 1, "updated_at": mrWitness(h, 1), "draft": true}},
		{"create_branch", map[string]any{"project": alpha, "branch": "dry"}},
		{"create_commit", map[string]any{"project": alpha, "branch": "dry", "start_branch": "main", "message": "x",
			"actions": []map[string]any{{"action": "create", "file_path": "new.txt", "content": "x"}}}},
		{"mark_todos_done", map[string]any{"ids": []int{gitlabtest.TodoAssigned}}},
	} {
		args := c.args
		args["dry_run"] = true
		text, out := h.ok(c.tool, args)
		if get(out, "dry_run") != true || get(out, "outcome") != "dry_run" || !strings.HasPrefix(text, "Dry run: nothing was written.") {
			t.Errorf("%s: %v\n%s", c.tool, out, text)
		}
	}
	if n := writesSent(h) - before; n != 0 {
		t.Errorf("dry runs sent %d writes", n)
	}
	_, out := h.ok("add_comment", map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "body": "x",
		"file": "README.md", "line": 3, "side": "new", "dry_run": true})
	if get(out, "position", "new_line") != float64(3) {
		t.Errorf("a dry run does not say where the comment would land: %v", out)
	}
}

func TestUpdateIssue(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	witness := issueWitness(h, 2)
	_, before := h.ok("get_issue", map[string]any{"project": alpha, "iid": 2})
	_, out := h.ok("update_issue", map[string]any{"project": alpha, "iid": 2, "updated_at": witness,
		"add_labels": []string{"docs"}, "remove_labels": []string{"feature"}, "add_assignees": []string{"dave"},
		"state": "close", "description": "Replaced.", "milestone": "Sprint 2"})
	if get(out, "outcome") != "updated" || get(out, "state") != "closed" {
		t.Fatalf("result = %v", out)
	}
	if strings.Join(strs(get(out, "labels_before")), ",") != strings.Join(strs(get(before, "labels")), ",") {
		t.Errorf("labels_before = %v, want %v", get(out, "labels_before"), get(before, "labels"))
	}
	labels := strings.Join(strs(get(out, "labels")), ",")
	if !strings.Contains(labels, "docs") || strings.Contains(labels, "feature") {
		t.Errorf("labels = %s", labels)
	}
	assignees := strs(get(out, "assignees"))
	if len(assignees) != len(strs(get(before, "assignees")))+1 || assignees[len(assignees)-1] != "dave" {
		t.Errorf("assignees = %v: an add replaced the set", assignees)
	}
	for _, f := range []string{"description", "state", "labels", "assignees", "milestone"} {
		if !strings.Contains(fmt.Sprint(get(out, "changed")), f) {
			t.Errorf("changed %v lacks %s", get(out, "changed"), f)
		}
	}
	if get(out, "description_removed", "lines") == nil {
		t.Errorf("a replaced description is not measured: %v", out)
	}

	// The old witness is stale now, and the refusal says so.
	text := h.fails("update_issue", map[string]any{"project": alpha, "iid": 2, "updated_at": witness, "title": "x"}, "stale")
	if !strings.Contains(text, "changed since it was read") {
		t.Errorf("stale: %s", text)
	}
	// Already closed: nothing sent, and said.
	witness = get(out, "updated_at").(string)
	sent := writesSent(h)
	text, out = h.ok("update_issue", map[string]any{"project": alpha, "iid": 2, "updated_at": witness, "state": "close"})
	if get(out, "outcome") != "unchanged" || writesSent(h) != sent || !strings.Contains(text, "already closed") {
		t.Errorf("closing a closed issue: %v\n%s", out, text)
	}
	h.fails("update_issue", map[string]any{"project": alpha, "iid": 2, "updated_at": witness}, "invalid")
	h.fails("update_issue", map[string]any{"project": alpha, "iid": 2, "updated_at": "yesterday", "title": "x"}, "invalid")
	h.fails("update_issue", map[string]any{"project": alpha, "iid": 2, "updated_at": witness, "milestone": "Sprint 2",
		"clear_milestone": true}, "invalid")
	h.fails("update_issue", map[string]any{"project": alpha, "iid": 2, "updated_at": witness, "state": "shut"}, "invalid")
	h.fails("update_issue", map[string]any{"project": alpha, "iid": 2, "updated_at": witness, "due_date": "2026-10-01",
		"clear_due_date": true}, "invalid")

	// Clearing fields sends zero values, and the result reads them back.
	_, out = h.ok("update_issue", map[string]any{"project": alpha, "iid": 2, "updated_at": witness, "clear_milestone": true,
		"remove_assignees": []string{"dave"}, "state": "reopen"})
	if get(out, "milestone") != nil || strings.Contains(fmt.Sprint(get(out, "assignees")), "dave") || get(out, "state") != "opened" {
		t.Errorf("clearing: %v", out)
	}
}

// -------------------------------------------------------- settle by reading

func TestALostCreateIsSettledByReading(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	// The issue is made, and the answer is lost.
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: "/projects/2001/issues", AfterApply: true, Status: http.StatusBadGateway,
		Body: `{"message":"502 Bad Gateway"}`})
	text := h.fails("create_issue", map[string]any{"project": alpha, "title": "Made, then lost"}, "ambiguous_outcome")
	if !strings.Contains(text, "a read shows it was created: issue #") || !strings.Contains(text, "Do not repeat") {
		t.Errorf("found: %s", text)
	}
	// Nothing is made, and the answer is lost.
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: "/projects/2001/issues", Status: http.StatusBadGateway,
		Body: `{"message":"502 Bad Gateway"}`})
	text = h.fails("create_issue", map[string]any{"project": alpha, "title": "Never made"}, "ambiguous_outcome")
	if !strings.Contains(text, "a read shows it was not created") {
		t.Errorf("not found: %s", text)
	}
	// Exactly one create was sent each time: a lost create is never
	// repeated (§4.5).
	creates := 0
	for _, r := range h.gl.Requests() {
		if r.Method == http.MethodPost && r.EscapedPath == "/api/v4/projects/2001/issues" {
			creates++
		}
	}
	if creates != 2 {
		t.Errorf("%d creates were sent for two calls", creates)
	}
	// A comment is settled the same way.
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: "/projects/2001/issues/1/notes", AfterApply: true,
		Status: http.StatusBadGateway, Body: `{"message":"502 Bad Gateway"}`})
	text = h.fails("add_comment", map[string]any{"project": alpha, "type": "issue", "iid": 1, "body": "Lost comment."}, "ambiguous_outcome")
	if !strings.Contains(text, "a read shows it was created: comment") {
		t.Errorf("comment: %s", text)
	}
}

func TestA202FromANoteIsADefect(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: "/projects/2001/issues/1/notes", Status: http.StatusAccepted,
		Body: `{"commands_changes":{},"summary":["Closed this issue."]}`})
	text := h.fails("add_comment", map[string]any{"project": alpha, "type": "issue", "iid": 1, "body": "Harmless."}, "unexpected")
	if !strings.Contains(text, "quick-action guard exists to prevent") {
		t.Errorf("202: %s", text)
	}
	// A new thread on a diff line is a note too.
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: "/projects/2001/merge_requests/1/discussions", Status: http.StatusAccepted,
		Body: `{"commands_changes":{}}`})
	h.fails("add_comment", map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "body": "Harmless.", "file": "README.md",
		"line": 3, "side": "new"}, "unexpected")
}

// ------------------------------------------------------------ comments

func TestAddComment(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, out := h.ok("add_comment", map[string]any{"project": alpha, "type": "issue", "iid": 1, "body": "A comment."})
	if get(out, "kind") != "comment" || get(out, "note_id") == float64(0) || get(out, "discussion_id") == "" {
		t.Errorf("comment: %v", out)
	}
	if !strings.Contains(text, "Posted comment") {
		t.Errorf("text:\n%s", text)
	}
	thread := get(out, "discussion_id").(string)
	_, out = h.ok("add_comment", map[string]any{"project": alpha, "type": "issue", "iid": 1, "body": "A reply.", "discussion_id": thread})
	if get(out, "kind") != "reply" || get(out, "discussion_id") != thread {
		t.Errorf("reply: %v", out)
	}
	h.ok("add_comment", map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "body": "On the merge request."})

	h.fails("add_comment", map[string]any{"project": alpha, "type": "issue", "iid": 1, "body": "x", "file": "README.md", "line": 1, "side": "new"}, "invalid")
	h.fails("add_comment", map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "body": "x", "file": "README.md", "line": 1,
		"side": "new", "discussion_id": thread}, "invalid")
	h.fails("add_comment", map[string]any{"project": alpha, "type": "issue", "iid": 1, "body": "   "}, "invalid")
	h.fails("add_comment", map[string]any{"project": alpha, "type": "issue", "iid": 1, "body": "Done.\n/close"}, "blocked")
}

// README.md in merge request !1 of the fixture:
//
//	@@ -1,3 +1,3 @@
//	 # Alpha
//
//	-A generated repository.
//	+A generated repository with a login.
func TestAnInlineCommentLandsWhereAsked(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	for _, c := range []struct {
		side          string
		line, endLine int
		old, new      any
		rng           string
	}{
		{"new", 3, 0, nil, float64(3), ""},
		{"old", 3, 0, float64(3), nil, ""},
		{"new", 1, 0, float64(1), float64(1), ""},
		{"new", 1, 3, nil, float64(3), "new 1-3"},
	} {
		args := map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "body": "Inline.", "file": "README.md",
			"line": c.line, "side": c.side}
		if c.endLine != 0 {
			args["end_line"] = c.endLine
		}
		text, out := h.ok("add_comment", args)
		if get(out, "kind") != "thread" || get(out, "position", "old_line") != c.old || get(out, "position", "new_line") != c.new {
			t.Errorf("%s %d: %v", c.side, c.line, out)
		}
		if c.rng != "" && fmt.Sprintf("%v %v-%v", get(out, "line_range", "side"), get(out, "line_range", "start"), get(out, "line_range", "end")) != c.rng {
			t.Errorf("range: %v", get(out, "line_range"))
		}
		if !strings.Contains(text, "Landed on README.md") {
			t.Errorf("text:\n%s", text)
		}
	}
	text := h.fails("add_comment", map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "body": "x", "file": "README.md",
		"line": 9, "side": "new"}, "invalid")
	if !strings.Contains(text, "new line 9 is not in the diff") || !strings.Contains(text, "new lines 1–3") {
		t.Errorf("outside the diff: %s", text)
	}
	h.fails("add_comment", map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "body": "x", "file": "no/such.go",
		"line": 1, "side": "new"}, "invalid")
	h.fails("add_comment", map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "body": "x", "file": "vendor/big.txt",
		"line": 1, "side": "new"}, "invalid")
	h.fails("add_comment", map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "body": "x", "file": "README.md",
		"line": 3}, "invalid")
}

func TestResolveDiscussion(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	_, out := h.ok("add_comment", map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "body": "Please fix.",
		"file": "README.md", "line": 3, "side": "new"})
	thread := get(out, "discussion_id").(string)
	_, out = h.ok("resolve_discussion", map[string]any{"project": alpha, "iid": 1, "discussion_id": thread})
	if get(out, "outcome") != "resolved" || get(out, "resolved") != true {
		t.Errorf("resolve: %v", out)
	}
	sent := writesSent(h)
	text, out := h.ok("resolve_discussion", map[string]any{"project": alpha, "iid": 1, "discussion_id": thread})
	if get(out, "outcome") != "unchanged" || writesSent(h) != sent || !strings.Contains(text, "already resolved") {
		t.Errorf("again: %v\n%s", out, text)
	}
	_, out = h.ok("resolve_discussion", map[string]any{"project": alpha, "iid": 1, "discussion_id": thread, "reopen": true})
	if get(out, "outcome") != "reopened" || get(out, "resolved") != false {
		t.Errorf("reopen: %v", out)
	}
	h.fails("resolve_discussion", map[string]any{"project": alpha, "iid": 1, "discussion_id": "no-such-thread"}, "not_found")
	_, out = h.ok("add_comment", map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "body": "Standalone."})
	h.fails("resolve_discussion", map[string]any{"project": alpha, "iid": 1, "discussion_id": get(out, "discussion_id")}, "invalid")
}

// ------------------------------------------------------------- reviews

func TestAReviewIsDraftedThenSubmitted(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	drafts := len(h.gl.Drafts(alpha, 1))
	_, out := h.ok("add_review_comment", map[string]any{"project": alpha, "iid": 1, "body": "Rename this.", "file": "README.md",
		"line": 3, "side": "new"})
	if get(out, "kind") != "draft" || get(out, "position", "new_line") != float64(3) {
		t.Fatalf("draft: %v", out)
	}

	if len(h.gl.Drafts(alpha, 1)) != drafts+1 {
		t.Fatal("no draft was stored")
	}
	_, out = h.ok("add_review_comment", map[string]any{"project": alpha, "iid": 1, "body": "Drop me."})
	_, del := h.ok("delete_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": get(out, "note_id")})
	if get(del, "outcome") != "deleted" || get(del, "remaining") != float64(drafts+1) {
		t.Errorf("delete: %v", del)
	}
	h.fails("delete_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": get(out, "note_id")}, "not_found")

	// A draft with a quick action is refused now, since it would run
	// when published.
	h.fails("add_review_comment", map[string]any{"project": alpha, "iid": 1, "body": "/approve"}, "blocked")

	// Approving needs the Ship flag.
	text := h.fails("submit_review", map[string]any{"project": alpha, "iid": 1, "reviewer_state": "approved"}, "blocked")
	if !strings.Contains(text, config.EnvEnableShip) {
		t.Errorf("approve: %s", text)
	}
	_, out = h.ok("submit_review", map[string]any{"project": alpha, "iid": 1, "summary": "Two things.", "reviewer_state": "requested_changes"})
	if get(out, "outcome") != "published" || get(out, "published") != float64(drafts+1) || get(out, "remaining") != float64(0) ||
		get(out, "summary") != true {
		t.Errorf("submit: %v", out)
	}
	if h.gl.ReviewerState(alpha, 1, gitlabtest.DefaultUser) != "requested_changes" {
		t.Errorf("reviewer state = %q", h.gl.ReviewerState(alpha, 1, gitlabtest.DefaultUser))
	}
	h.fails("submit_review", map[string]any{"project": alpha, "iid": 1}, "invalid")
}

func TestAnApprovingReviewWithShip(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: config.Config{EnableShip: true}})
	_, out := h.ok("submit_review", map[string]any{"project": alpha, "iid": 1, "reviewer_state": "approved"})
	if get(out, "outcome") != "published" || h.gl.ReviewerState(alpha, 1, gitlabtest.DefaultUser) != "approved" {
		t.Errorf("approve: %v", out)
	}
}

// ------------------------------------------------ branches, commits, MRs

func TestCodeReachesMainThroughAMergeRequest(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	for _, branch := range []string{"main", "release/1.0", "release/2.0"} {
		args := map[string]any{"project": alpha, "branch": branch, "message": "x",
			"actions": []map[string]any{{"action": "create", "file_path": "new.txt", "content": "x"}}}
		if branch == "release/2.0" {
			args["start_branch"] = "main"
		}
		text := h.fails("create_commit", args, "blocked")
		if !strings.Contains(text, "merge request") {
			t.Errorf("%s: %s", branch, text)
		}
	}

	// A new branch under a protected rule would carry its ref's code there
	// with no merge request.
	sent := writesSent(h)
	h.fails("create_branch", map[string]any{"project": alpha, "branch": "release/9.0", "ref": "feature/login"}, "blocked")
	h.fails("create_branch", map[string]any{"project": alpha, "branch": "main", "dry_run": true}, "blocked")
	if writesSent(h) != sent {
		t.Fatal("a refused branch was sent")
	}

	// More rules than one read covers: an unread one could protect it.
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodGet, Path: "/projects/2001/protected_branches", Times: 10, Status: http.StatusOK,
		Header: http.Header{"X-Next-Page": {"2"}}, Body: `[]`})
	if text := h.fails("create_branch", map[string]any{"project": alpha, "branch": "unread-rules"}, "blocked"); !strings.Contains(text, "more protected-branch rules") {
		t.Errorf("incomplete rules: %s", text)
	}

	_, out := h.ok("create_branch", map[string]any{"project": alpha, "branch": "topic"})
	if get(out, "outcome") != "created" || get(out, "commit_sha") == "" {
		t.Fatalf("branch: %v", out)
	}
	h.fails("create_branch", map[string]any{"project": alpha, "branch": "topic"}, "conflict")

	_, file := h.ok("get_file", map[string]any{"project": alpha, "path": "README.md", "ref": "topic"})
	witness := get(file, "last_commit_id")
	h.fails("create_commit", map[string]any{"project": alpha, "branch": "topic", "message": "x",
		"actions": []map[string]any{{"action": "update", "file_path": "README.md", "content": "x"}}}, "invalid")
	_, out = h.ok("create_commit", map[string]any{"project": alpha, "branch": "topic", "message": "Change the readme",
		"actions": []map[string]any{{"action": "update", "file_path": "README.md", "content": "# Alpha\n\nChanged.\n", "last_commit_id": witness},
			{"action": "create", "file_path": "docs/new.md", "content": "New.\n"}}})
	if get(out, "outcome") != "created" || get(out, "sha") != get(out, "branch_head") || strings.Join(strs(get(out, "files")), ",") != "README.md,docs/new.md" {
		t.Fatalf("commit: %v", out)
	}
	if got, _ := h.gl.FileAt(alpha, "topic", "README.md"); got != "# Alpha\n\nChanged.\n" {
		t.Errorf("README.md = %q", got)
	}
	// The same witness is stale now.
	h.fails("create_commit", map[string]any{"project": alpha, "branch": "topic", "message": "Again",
		"actions": []map[string]any{{"action": "update", "file_path": "README.md", "content": "y", "last_commit_id": witness}}}, "stale")
	h.fails("create_commit", map[string]any{"project": alpha, "branch": "nowhere", "message": "x",
		"actions": []map[string]any{{"action": "create", "file_path": "a", "content": "x"}}}, "invalid")
	h.fails("create_commit", map[string]any{"project": alpha, "branch": "topic", "start_branch": "main", "message": "x",
		"actions": []map[string]any{{"action": "create", "file_path": "a", "content": "x"}}}, "invalid")
	h.fails("create_commit", map[string]any{"project": alpha, "branch": "topic", "message": "x",
		"actions": []map[string]any{{"action": "move", "file_path": "b", "last_commit_id": "abc"}}}, "invalid")

	// A move without content keeps the file's own; a delete takes none.
	_, file = h.ok("get_file", map[string]any{"project": alpha, "path": "docs/new.md", "ref": "topic"})
	h.ok("create_commit", map[string]any{"project": alpha, "branch": "topic", "message": "Move the doc",
		"actions": []map[string]any{{"action": "move", "previous_path": "docs/new.md", "file_path": "docs/moved.md",
			"last_commit_id": get(file, "last_commit_id")}}})
	if got, ok := h.gl.FileAt(alpha, "topic", "docs/moved.md"); !ok || got != "New.\n" {
		t.Errorf("the moved file holds %q: a move emptied it", got)
	}

	// A new branch from a commit.
	_, out = h.ok("create_commit", map[string]any{"project": alpha, "branch": "topic-2", "start_branch": "main", "message": "Start",
		"actions": []map[string]any{{"action": "create", "file_path": "t2.txt", "content": "x\n"}}})
	if get(out, "branch_head") != get(out, "sha") {
		t.Errorf("new branch: %v", out)
	}

	_, mr := h.ok("create_merge_request", map[string]any{"project": alpha, "source_branch": "topic", "title": "Change the readme",
		"description": "Why.", "draft": true, "labels": []string{"docs"}, "reviewers": []string{"bob"}, "squash": true})
	if get(mr, "outcome") != "created" || get(mr, "draft") != true || get(mr, "target_branch") != "main" ||
		strings.Join(strs(get(mr, "reviewers")), ",") != "bob" || get(mr, "squash") != true {
		t.Fatalf("merge request: %v", mr)
	}
	h.fails("create_merge_request", map[string]any{"project": alpha, "source_branch": "topic", "title": "Again"}, "conflict")

	iid := int(get(mr, "iid").(float64))
	if get(mr, "updated_at") != nil {
		t.Errorf("a create offered updated_at as a witness: %v", get(mr, "updated_at"))
	}
	witness = mrWitness(h, iid)
	_, out = h.ok("update_merge_request", map[string]any{"project": alpha, "iid": iid, "updated_at": witness,
		"draft": false, "add_reviewers": []string{"carol"}, "remove_source_branch": true})
	if get(out, "draft") != false || strings.Join(strs(get(out, "reviewers")), ",") != "bob,carol" || get(out, "remove_source_branch") != true {
		t.Errorf("update: %v", out)
	}
	for _, f := range []string{"title", "draft", "reviewers", "remove_source_branch"} {
		if !strings.Contains(fmt.Sprint(get(out, "changed")), f) {
			t.Errorf("changed %v lacks %s", get(out, "changed"), f)
		}
	}
	got, _ := h.gl.MergeRequest(alpha, int64(iid))
	if got.Title != "Change the readme" {
		t.Errorf("title = %q: marking it ready left the prefix", got.Title)
	}
	h.fails("update_merge_request", map[string]any{"project": alpha, "iid": iid, "updated_at": witness, "squash": false}, "stale")

	// A new title alone leaves the draft state as it was.
	_, out = h.ok("update_merge_request", map[string]any{"project": alpha, "iid": iid, "updated_at": mrWitness(h, iid), "draft": true})
	h.ok("update_merge_request", map[string]any{"project": alpha, "iid": iid, "updated_at": get(out, "updated_at"), "title": "Renamed"})
	if got, _ := h.gl.MergeRequest(alpha, int64(iid)); !got.Draft || got.Title != "Draft: Renamed" {
		t.Errorf("a title alone changed the draft state: %q, draft %v", got.Title, got.Draft)
	}
}

// ----------------------------------------------------------------- todos

func TestMarkTodosDone(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, out := h.ok("mark_todos_done", map[string]any{"ids": []int{gitlabtest.TodoAssigned, 99999, gitlabtest.TodoAssigned}})
	if get(out, "outcome") != "partly_done" || len(get(out, "items").([]any)) != 2 {
		t.Fatalf("result: %v", out)
	}
	if get(out, "items", 0, "outcome") != "done" || get(out, "items", 1, "outcome") != "not_found" {
		t.Errorf("items: %v", get(out, "items"))
	}
	if h.gl.TodoState(gitlabtest.TodoAssigned) != "done" || !strings.Contains(text, "Marked 1 of 2") {
		t.Errorf("state %q\n%s", h.gl.TodoState(gitlabtest.TodoAssigned), text)
	}
	h.fails("mark_todos_done", map[string]any{"ids": []int{}}, "invalid")
}

// ------------------------------------------------------ where writes go

func TestTheWriteAllowList(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: config.Config{WriteNamespaces: []string{gitlabtest.GroupSub}}})
	sent := writesSent(h)
	text := h.fails("create_issue", map[string]any{"project": alpha, "title": "x"}, "blocked")
	if !strings.Contains(text, config.EnvWriteNamespaces) {
		t.Errorf("refusal: %s", text)
	}
	h.fails("add_comment", map[string]any{"project": alpha, "type": "issue", "iid": 1, "body": "x"}, "blocked")
	if writesSent(h) != sent {
		t.Fatal("a write outside the allow-list was sent")
	}
	_, out := h.ok("create_issue", map[string]any{"project": gitlabtest.ProjectBeta, "title": "Allowed"})
	if get(out, "target", "visibility") != "private" {
		t.Errorf("beta: %v", out)
	}
}

func TestLintSuppliedContent(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	_, out := h.ok("lint_ci", map[string]any{"project": alpha, "content": "build:\n  script:\n    - echo hi\n", "include_jobs": true})
	if get(out, "supplied") != true || get(out, "valid") != true {
		t.Errorf("lint: %v", out)
	}
	// GitLab fetches what an include names while linting: a way out no
	// write control sees, so supplied content may not have one.
	sent := len(h.gl.Requests())
	h.fails("lint_ci", map[string]any{"project": alpha, "content": "include:\n  - remote: https://example.invalid/x.yml\n"}, "blocked")
	for _, r := range h.gl.Requests()[sent:] {
		if r.Method == http.MethodPost {
			t.Error("content with an include was sent")
		}
	}
	ro := newHarness(t, harnessOptions{cfg: config.Config{ReadOnly: true}})
	text := ro.fails("lint_ci", map[string]any{"project": alpha, "content": "build:\n  script: [x]\n"}, "blocked")
	if !strings.Contains(text, config.EnvReadOnly) {
		t.Errorf("read-only: %s", text)
	}
	ro.ok("lint_ci", map[string]any{"project": alpha})
}

// A lost comment is looked for on the newest page of threads, and is
// "not created" only when that page reaches back past the call.
func TestALostCommentIsSettledOnTheNewestThreads(t *testing.T) {
	thread := func(created string) string {
		return `[{"id":"t1","individual_note":true,"notes":[{"id":1,"body":"Older.","author":{"username":"bob"},"created_at":"` + created + `"}]}]`
	}
	lost := func(h *harness) {
		h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: "/projects/2001/issues/1/notes", Status: http.StatusBadGateway,
			Body: `{"message":"502 Bad Gateway"}`})
	}
	pages := func(h *harness, header http.Header, body string) {
		h.gl.Inject(gitlabtest.Fault{Method: http.MethodGet, Path: "/projects/2001/issues/1/discussions", Times: 2, Status: http.StatusOK,
			Header: header, Body: body})
	}
	for _, c := range []struct {
		name   string
		header http.Header
		body   string
		want   string
	}{
		{"no page count", http.Header{"X-Next-Page": {"2"}}, thread("2026-01-01T00:00:00Z"), "reading to find out failed too"},
		{"the newest page is all newer than the call", http.Header{"X-Next-Page": {"2"}, "X-Total-Pages": {"3"}},
			thread("2999-01-01T00:00:00Z"), "reading to find out failed too"},
		{"the newest page reaches back past the call", http.Header{"X-Next-Page": {"2"}, "X-Total-Pages": {"3"}},
			thread("2026-01-01T00:00:00Z"), "a read shows it was not created"},
	} {
		h := newHarness(t, harnessOptions{})
		lost(h)
		pages(h, c.header, c.body)
		if text := h.fails("add_comment", map[string]any{"project": alpha, "type": "issue", "iid": 1, "body": "Lost."}, "ambiguous_outcome"); !strings.Contains(text, c.want) {
			t.Errorf("%s: %s", c.name, text)
		}
	}

	// A lost reply is found in its own thread, however many there are.
	h := newHarness(t, harnessOptions{})
	_, out := h.ok("add_comment", map[string]any{"project": alpha, "type": "issue", "iid": 1, "body": "First."})
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: "/projects/2001/issues/1/discussions/", AfterApply: true,
		Status: http.StatusBadGateway, Body: `{"message":"502 Bad Gateway"}`})
	text := h.fails("add_comment", map[string]any{"project": alpha, "type": "issue", "iid": 1, "body": "A reply.",
		"discussion_id": get(out, "discussion_id")}, "ambiguous_outcome")
	if !strings.Contains(text, "a read shows it was created") {
		t.Errorf("reply: %s", text)
	}
}

// A new comment's thread is found on the last page of threads, with two
// reads however many pages there are.
func TestANewCommentsThreadIsFoundOnTheLastPage(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	// The filler goes straight to the instance: through the tool, the
	// notes rate model would pace 120 comments over two minutes.
	for i := range 120 {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, h.gl.URL+"/api/v4/projects/2001/issues/3/notes",
			strings.NewReader(fmt.Sprintf(`{"body":"Filler %d."}`, i)))
		req.Header.Set("Authorization", "Bearer "+h.gl.Token())
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil || res.StatusCode != http.StatusCreated {
			t.Fatalf("filler %d: %v %v", i, res, err)
		}
		_ = res.Body.Close()
	}
	h.gl.ResetRequests()
	_, out := h.ok("add_comment", map[string]any{"project": alpha, "type": "issue", "iid": 3, "body": "The newest."})
	threads := h.gl.Discussions(alpha, "issue", 3)
	if want := threads[len(threads)-1].ID; get(out, "discussion_id") != want {
		t.Errorf("discussion_id = %v, want the newest thread %s", get(out, "discussion_id"), want)
	}
	reads := 0
	for _, r := range h.gl.Requests() {
		if r.Method == http.MethodGet && strings.HasSuffix(r.EscapedPath, "/issues/3/discussions") {
			reads++
		}
	}
	if reads != 2 {
		t.Errorf("%d thread pages read, want 2", reads)
	}
}

// A lost draft is settled by a new one, never by an older draft that
// says the same.
func TestALostDraftIsNotAnOlderOne(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.ok("add_review_comment", map[string]any{"project": alpha, "iid": 1, "body": "nit: typo"})
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: "/projects/2001/merge_requests/1/draft_notes", Status: http.StatusBadGateway,
		Body: `{"message":"502 Bad Gateway"}`})
	text := h.fails("add_review_comment", map[string]any{"project": alpha, "iid": 1, "body": "nit: typo"}, "ambiguous_outcome")
	if !strings.Contains(text, "a read shows it was not created") {
		t.Errorf("an older draft settled a lost one: %s", text)
	}
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: "/projects/2001/merge_requests/1/draft_notes", AfterApply: true,
		Status: http.StatusBadGateway, Body: `{"message":"502 Bad Gateway"}`})
	text = h.fails("add_review_comment", map[string]any{"project": alpha, "iid": 1, "body": "nit: typo"}, "ambiguous_outcome")
	if !strings.Contains(text, "a read shows it was created: draft") {
		t.Errorf("a new draft: %s", text)
	}
}
