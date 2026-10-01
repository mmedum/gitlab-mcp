package tools

import (
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/mmedum/gitlab-mcp/v2/internal/config"
	"github.com/mmedum/gitlab-mcp/v2/internal/gapi/gitlabtest"
	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
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
		{"update_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": lineDraft, "body": "x", "note_sha256": hash(lineText)}},
		{"publish_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": replyDraft}},
		{"create_merge_request", map[string]any{"project": alpha, "source_branch": "release/1.0", "title": "x"}},
		{"update_merge_request", map[string]any{"project": alpha, "iid": 1, "updated_at": mrWitness(h, 1), "draft": true}},
		{"create_branch", map[string]any{"project": alpha, "branch": "dry"}},
		{"create_commit", map[string]any{"project": alpha, "branch": "dry", "start_branch": "main", "message": "x",
			"actions": []map[string]any{{"action": "create", "file_path": "new.txt", "content": "x"}}}},
		{"mark_todos_done", map[string]any{"ids": []int{gitlabtest.TodoAssigned}}},
		{"track_time", map[string]any{"project": alpha, "type": "issue", "iid": 3, "updated_at": issueWitness(h, 3),
			"estimate": "1w", "add_spent": "-1h", "total_time_spent": 36000}},
		{"subscribe", map[string]any{"project": alpha, "type": "issue", "iid": 1, "unsubscribe": true}},
		{"subscribe", map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "unsubscribe": true}},
		{"add_todo", map[string]any{"project": alpha, "type": "issue", "iid": 1}},
		{"add_todo", map[string]any{"project": alpha, "type": "merge_request", "iid": 1}},
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

// Making a confidential issue public widens who can read it, so it is
// Ship's to allow; making one confidential is not.
func TestMakingAnIssuePublicNeedsShip(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	_, out := h.ok("update_issue", map[string]any{"project": alpha, "iid": 2, "updated_at": issueWitness(h, 2), "confidential": true})
	if get(out, "confidential") != true {
		t.Fatalf("result = %v", out)
	}
	sent := writesSent(h)
	for _, dry := range []bool{true, false} {
		text := h.fails("update_issue", map[string]any{"project": alpha, "iid": 2, "updated_at": get(out, "updated_at"),
			"confidential": false, "dry_run": dry}, "blocked")
		if !strings.Contains(text, "GITLAB_MCP_ENABLE_SHIP") || writesSent(h) != sent {
			t.Errorf("dry run %v: %s", dry, text)
		}
	}
	s := newHarness(t, harnessOptions{cfg: ship})
	_, out = s.ok("update_issue", map[string]any{"project": alpha, "iid": 2, "updated_at": issueWitness(s, 2), "confidential": true})
	if _, out = s.ok("update_issue", map[string]any{"project": alpha, "iid": 2, "updated_at": get(out, "updated_at"),
		"confidential": false}); get(out, "confidential") != false {
		t.Errorf("with Ship: %v", out)
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
	if get(out, "kind") != "comment" || get(out, "note_id") == float64(0) || get(out, "discussion_id") != "" {
		t.Errorf("comment: %v", out)
	}
	if !strings.Contains(text, "Posted comment") {
		t.Errorf("text:\n%s", text)
	}
	threads := h.gl.Discussions(alpha, "issue", 1)
	thread := threads[len(threads)-1].ID
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
	h.ok("add_comment", map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "body": "Standalone."})
	threads := h.gl.Discussions(alpha, "mr", 1)
	h.fails("resolve_discussion", map[string]any{"project": alpha, "iid": 1, "discussion_id": threads[len(threads)-1].ID}, "invalid")
}

// An issue takes a resolvable thread as a merge request does, and
// resolve_discussion resolves it with type issue.
func TestAnIssueThreadIsStartedAndResolved(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	_, out := h.ok("add_comment", map[string]any{"project": alpha, "type": "issue", "iid": 1, "body": "Is this still open?",
		"thread": true})
	thread, _ := get(out, "discussion_id").(string)
	if get(out, "kind") != "thread" || thread == "" {
		t.Fatalf("thread: %v", out)
	}
	threads := h.gl.Discussions(alpha, "issue", 1)
	if last := threads[len(threads)-1]; last.ID != thread || !last.Notes[0].Resolvable {
		t.Fatalf("the thread stored is %+v", last)
	}
	_, out = h.ok("resolve_discussion", map[string]any{"project": alpha, "type": "issue", "iid": 1, "discussion_id": thread})
	if get(out, "outcome") != "resolved" || get(out, "resolved") != true {
		t.Errorf("resolve: %v", out)
	}
	_, out = h.ok("resolve_discussion", map[string]any{"project": alpha, "type": "issue", "iid": 1, "discussion_id": thread,
		"reopen": true})
	if get(out, "outcome") != "reopened" {
		t.Errorf("reopen: %v", out)
	}
	// A standalone comment cannot be resolved; a reply cannot start a thread.
	h.ok("add_comment", map[string]any{"project": alpha, "type": "issue", "iid": 1, "body": "Standalone."})
	threads = h.gl.Discussions(alpha, "issue", 1)
	h.fails("resolve_discussion", map[string]any{"project": alpha, "type": "issue", "iid": 1,
		"discussion_id": threads[len(threads)-1].ID}, "invalid")
	h.fails("add_comment", map[string]any{"project": alpha, "type": "issue", "iid": 1, "body": "Both.", "thread": true,
		"discussion_id": thread}, "invalid")
	// A general thread on a merge request is resolvable too.
	_, out = h.ok("add_comment", map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "body": "A question.",
		"thread": true})
	_, out = h.ok("resolve_discussion", map[string]any{"project": alpha, "iid": 1, "discussion_id": get(out, "discussion_id")})
	if get(out, "outcome") != "resolved" {
		t.Errorf("merge request thread: %v", out)
	}
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
	if code, _ := get(out, "line_code").(string); !regexp.MustCompile(`^[0-9a-f]{40}_[0-9]+_3$`).MatchString(code) {
		t.Errorf("line_code = %q", code)
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

// A to-do you add has an id mark_todos_done takes. While it is pending
// GitLab adds no other, and the result names the one there; a to-do of
// another kind does not stop it.
func TestAddTodo(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, out := h.ok("add_todo", map[string]any{"project": alpha, "type": "issue", "iid": 1})
	id, _ := get(out, "todo_id").(float64)
	if get(out, "outcome") != "created" || id == 0 || h.gl.TodoState(int64(id)) != "pending" {
		t.Fatalf("created: %v", out)
	}
	if !strings.Contains(text, "Added a to-do for #1.") || !strings.Contains(text, fmt.Sprintf("To-do item %d", int64(id))) {
		t.Errorf("text:\n%s", text)
	}
	sent := writesSent(h)
	text, out = h.ok("add_todo", map[string]any{"project": alpha, "type": "issue", "iid": 1})
	if get(out, "outcome") != "unchanged" || get(out, "todo_id") != id || !strings.Contains(text, "already there") {
		t.Errorf("again: %v\n%s", out, text)
	}
	if writesSent(h) != sent+1 || len(h.gl.PendingTodos("alice")) != 3 {
		t.Errorf("again: %d writes, pending %v", writesSent(h)-sent, h.gl.PendingTodos("alice"))
	}
	_, out = h.ok("add_todo", map[string]any{"project": alpha, "type": "issue", "iid": 1, "dry_run": true})
	if get(out, "outcome") != "dry_run" || get(out, "todo_id") != id || get(out, "would_send") != nil {
		t.Errorf("dry run with one there: %v", out)
	}
	if _, out = h.ok("mark_todos_done", map[string]any{"ids": []float64{id}}); get(out, "outcome") != "done" {
		t.Fatalf("mark: %v", out)
	}
	if _, out = h.ok("add_todo", map[string]any{"project": alpha, "type": "issue", "iid": 1}); get(out, "outcome") != "created" ||
		get(out, "todo_id") == id {
		t.Errorf("after it was done: %v", out)
	}
	// alice's review request on !1 is pending, and is not one she added.
	if _, out = h.ok("add_todo", map[string]any{"project": alpha, "type": "merge_request", "iid": 1}); get(out, "outcome") != "created" {
		t.Errorf("merge request: %v", out)
	}
	h.fails("add_todo", map[string]any{"project": alpha, "type": "issue", "iid": 999}, "not_found")
	h.fails("add_todo", map[string]any{"project": alpha, "type": "issue", "iid": 999, "dry_run": true}, "not_found")
	h.fails("add_todo", map[string]any{"project": alpha, "type": "epic", "iid": 1}, "invalid")
}

// A to-do is a create: a lost answer is never sent again, and a read of
// the account's pending to-dos on the item settles it (§4.5).
func TestALostTodoIsSettledNotSent(t *testing.T) {
	todoPosts := func(h *harness) int {
		n := 0
		for _, r := range h.gl.Requests() {
			if r.Method == http.MethodPost && strings.HasSuffix(r.EscapedPath, "/todo") {
				n++
			}
		}
		return n
	}
	h := newHarness(t, harnessOptions{})
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: "/projects/2001/issues/2/todo", AfterApply: true,
		Status: http.StatusBadGateway, Body: `{"message":"502 Bad Gateway"}`})
	text := h.fails("add_todo", map[string]any{"project": alpha, "type": "issue", "iid": 2}, "ambiguous_outcome")
	pending := h.gl.PendingTodos("alice")
	if want := fmt.Sprintf("a read shows it was created: to-do item %d", pending[len(pending)-1]); !strings.Contains(text, want) ||
		len(pending) != 3 || todoPosts(h) != 1 {
		t.Errorf("landed: %s; pending %v, %d sent", text, pending, todoPosts(h))
	}

	h = newHarness(t, harnessOptions{})
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: "/projects/2001/issues/2/todo",
		Status: http.StatusBadGateway, Body: `{"message":"502 Bad Gateway"}`})
	text = h.fails("add_todo", map[string]any{"project": alpha, "type": "issue", "iid": 2}, "ambiguous_outcome")
	if !strings.Contains(text, "a read shows it was not created") || len(h.gl.PendingTodos("alice")) != 2 || todoPosts(h) != 1 {
		t.Errorf("not landed: %s", text)
	}
}

// Commenting outside a thread marks your pending to-dos on the item
// done, as GitLab does, so adding one afterwards makes a new one.
func TestACommentClearsYourTodo(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	_, out := h.ok("add_todo", map[string]any{"project": alpha, "type": "issue", "iid": 2})
	first := get(out, "todo_id").(float64)
	h.ok("add_comment", map[string]any{"project": alpha, "type": "issue", "iid": 2, "body": "Looked at it."})
	if h.gl.TodoState(int64(first)) != "done" {
		t.Fatalf("the comment left the to-do %s", h.gl.TodoState(int64(first)))
	}
	if _, out = h.ok("add_todo", map[string]any{"project": alpha, "type": "issue", "iid": 2}); get(out, "outcome") != "created" ||
		get(out, "todo_id") == first {
		t.Errorf("after the comment: %v", out)
	}
}

// Subscribing, adding a to-do, marking to-dos done and editing a draft
// are writes only the account sees.
func TestOwnOnlyWritesAreNotOpenWorld(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	tools := listed(t, h)
	for name, idempotent := range map[string]bool{"subscribe": true, "add_todo": false, "mark_todos_done": true,
		"update_review_comment": true} {
		a := tools[name].Annotations
		if a.OpenWorldHint == nil || *a.OpenWorldHint || a.IdempotentHint != idempotent {
			t.Errorf("%s annotations = %+v", name, a)
		}
	}
	if a := tools["add_comment"].Annotations; a.OpenWorldHint == nil || !*a.OpenWorldHint {
		t.Errorf("add_comment annotations = %+v", a)
	}
}

// A subscribe whose answer was lost is sent again; the 304 the second
// gets is the first one's work, reported as done.
func TestALostSubscribeIsReportedAsDone(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.ok("subscribe", map[string]any{"project": alpha, "type": "issue", "iid": 1, "unsubscribe": true})
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: "/projects/2001/issues/1/subscribe", AfterApply: true,
		Status: http.StatusBadGateway, Body: `{"message":"502 Bad Gateway"}`})
	text, out := h.ok("subscribe", map[string]any{"project": alpha, "type": "issue", "iid": 1})
	if get(out, "outcome") != "subscribed" || get(out, "subscribed") != true || !strings.Contains(text, "answer to the first attempt was lost") {
		t.Errorf("result %v\n%s", out, text)
	}
}

// You are subscribed to what you take part in; unsubscribing and
// subscribing change it, and asking for the state it is in is unchanged.
func TestSubscribe(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	issue := map[string]any{"project": alpha, "type": "issue", "iid": 1}
	with := func(extra map[string]any) map[string]any {
		out := maps.Clone(issue)
		maps.Copy(out, extra)
		return out
	}
	text, out := h.ok("subscribe", issue)
	if get(out, "outcome") != "unchanged" || get(out, "subscribed") != true || get(out, "web_url") == "" ||
		!strings.Contains(text, "GitLab reports you are subscribed; it made no change on this request") {
		t.Errorf("a participant: %v\n%s", out, text)
	}
	text, out = h.ok("subscribe", with(map[string]any{"unsubscribe": true}))
	if get(out, "outcome") != "unsubscribed" || get(out, "subscribed") != false || h.gl.Subscribed(alpha, "issue", 1, "alice") ||
		!strings.Contains(text, "Unsubscribed you from #1.") {
		t.Errorf("unsubscribe: %v\n%s", out, text)
	}
	if _, out = h.ok("subscribe", with(map[string]any{"unsubscribe": true})); get(out, "outcome") != "unchanged" || get(out, "subscribed") != false {
		t.Errorf("unsubscribe again: %v", out)
	}
	_, out = h.ok("subscribe", with(map[string]any{"dry_run": true}))
	if get(out, "outcome") != "dry_run" || get(out, "subscribed") != false || get(out, "would_send", "operation") != "subscribe to the issue" {
		t.Errorf("dry run: %v", out)
	}
	if _, out = h.ok("subscribe", issue); get(out, "outcome") != "subscribed" || !h.gl.Subscribed(alpha, "issue", 1, "alice") {
		t.Errorf("subscribe: %v", out)
	}
	_, out = h.ok("subscribe", with(map[string]any{"dry_run": true}))
	if get(out, "outcome") != "dry_run" || get(out, "would_send") != nil || !strings.Contains(fmt.Sprint(get(out, "notes")), "already are subscribed") {
		t.Errorf("dry run, already: %v", out)
	}
	mr := map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "unsubscribe": true}
	if _, out = h.ok("subscribe", mr); get(out, "outcome") != "unsubscribed" || h.gl.Subscribed(alpha, "mr", 1, "alice") {
		t.Errorf("merge request: %v", out)
	}
	h.fails("subscribe", map[string]any{"project": alpha, "type": "issue", "iid": 999}, "not_found")
	h.fails("subscribe", map[string]any{"project": alpha, "type": "issue", "iid": 0}, "invalid")
}

// GitLab lets only those who may update a merge request subscribe to it;
// anyone who can read an issue may subscribe to it.
func TestSubscribingToAMergeRequestNeedsTheRightsToUpdateIt(t *testing.T) {
	h := newHarness(t, harnessOptions{token: func(gl *gitlabtest.Server) string { return gl.TokenFor("dave", "api") }})
	text := h.fails("subscribe", map[string]any{"project": alpha, "type": "merge_request", "iid": 1}, "forbidden")
	if !strings.Contains(text, "403 Forbidden. It may be the role: subscribing to a merge request") ||
		!strings.Contains(text, "the Developer role or higher, or its author or an assignee") || h.gl.Subscribed(alpha, "mr", 1, "dave") {
		t.Errorf("forbidden: %s", text)
	}
	// Mentioned in a comment, dave takes part in the issue.
	h.gl.Comment(alpha, "issue", 4, "bob", "@dave can you look?")
	if _, out := h.ok("subscribe", map[string]any{"project": alpha, "type": "issue", "iid": 4}); get(out, "outcome") != "unchanged" {
		t.Errorf("mentioned: %v", out)
	}
	if _, out := h.ok("subscribe", map[string]any{"project": alpha, "type": "issue", "iid": 1}); get(out, "outcome") != "subscribed" {
		t.Errorf("issue: %v", out)
	}
	h.gl.SetMemberLevel(alpha, "dave", 30)
	if _, out := h.ok("subscribe", map[string]any{"project": alpha, "type": "merge_request", "iid": 1}); get(out, "outcome") != "subscribed" {
		t.Errorf("as a Developer: %v", out)
	}
	// A merge request dave may not read refuses both writes with 403, as
	// find_merge_request_with_access does.
	h.gl.SetMemberLevel(alpha, "dave", 0)
	h.gl.SetMergeRequestsAccess(alpha, "private")
	h.fails("add_todo", map[string]any{"project": alpha, "type": "merge_request", "iid": 1}, "forbidden")
	h.fails("subscribe", map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "unsubscribe": true}, "forbidden")
}

// Subscribing and adding a to-do are held to the allow-list by the
// item's project, as marking one done is, and nothing is sent.
func TestSubscriptionsAndNewTodosAreHeldToTheAllowList(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: config.Config{WriteNamespaces: []string{gitlabtest.GroupSub}}})
	sent := writesSent(h)
	for _, dry := range []bool{false, true} {
		for _, tool := range []string{"subscribe", "add_todo"} {
			text := h.fails(tool, map[string]any{"project": alpha, "type": "issue", "iid": 1, "dry_run": dry}, "blocked")
			if !strings.Contains(text, config.EnvWriteNamespaces) {
				t.Errorf("%s: %s", tool, text)
			}
		}
	}
	if writesSent(h) != sent || len(h.gl.PendingTodos("alice")) != 2 {
		t.Error("a write outside the allow-list was sent")
	}
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
	h.fails("update_comment", map[string]any{"project": alpha, "type": "issue", "iid": 1, "note_id": 1, "body": "x",
		"updated_at": "2026-01-01T00:00:00Z"}, "blocked")
	h.fails("update_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": lineDraft, "body": "x",
		"note_sha256": hash(lineText)}, "blocked")
	h.fails("publish_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": lineDraft}, "blocked")
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

// A lost comment is looked for from the newest page of threads back, and
// is "not created" only once a page reaches back past the call.
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
		{"the newest page is all newer than the call, and the page before is read", http.Header{"X-Next-Page": {"2"},
			"X-Total-Pages": {"3"}}, thread("2999-01-01T00:00:00Z"), "a read shows it was not created"},
		{"the newest page reaches back past the call", http.Header{"X-Next-Page": {"2"}, "X-Total-Pages": {"3"}},
			thread("2026-01-01T00:00:00Z"), "a read shows it was not created"},
	} {
		h := newHarness(t, harnessOptions{})
		lost(h)
		pages(h, c.header, c.body)
		if text := h.fails("add_comment", map[string]any{"project": alpha, "type": "issue", "iid": 1, "body": "Lost."}, "ambiguous_outcome"); !strings.Contains(text, c.want) {
			t.Errorf("%s: %s", c.name, text)
		}
		var read []string
		for _, r := range h.gl.Requests() {
			if r.Method == http.MethodGet && strings.HasSuffix(r.EscapedPath, "/issues/1/discussions") {
				q, _ := url.ParseQuery(r.RawQuery)
				read = append(read, q.Get("page"))
			}
		}
		if strings.Contains(c.name, "page before") && fmt.Sprint(read) != "[1 3 2]" {
			t.Errorf("%s: pages read %v", c.name, read)
		}
	}

	// A lost reply is found in its own thread, however many there are.
	h := newHarness(t, harnessOptions{})
	h.ok("add_comment", map[string]any{"project": alpha, "type": "issue", "iid": 1, "body": "First."})
	threads := h.gl.Discussions(alpha, "issue", 1)
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: "/projects/2001/issues/1/discussions/", AfterApply: true,
		Status: http.StatusBadGateway, Body: `{"message":"502 Bad Gateway"}`})
	text := h.fails("add_comment", map[string]any{"project": alpha, "type": "issue", "iid": 1, "body": "A reply.",
		"discussion_id": threads[len(threads)-1].ID}, "ambiguous_outcome")
	if !strings.Contains(text, "a read shows it was created") {
		t.Errorf("reply: %s", text)
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

// Under the write allow-list a to-do item in a project outside it is
// refused, and nothing is sent for it.
func TestTodosAreHeldToTheAllowList(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: config.Config{WriteNamespaces: []string{gitlabtest.GroupSub}}})
	sent := writesSent(h)
	_, out := h.ok("mark_todos_done", map[string]any{"ids": []int{gitlabtest.TodoAssigned}})
	if get(out, "items", 0, "outcome") != "blocked" || get(out, "outcome") != "none_done" ||
		!strings.Contains(fmt.Sprint(get(out, "items", 0, "error")), config.EnvWriteNamespaces) {
		t.Errorf("outside: %v", out)
	}
	if writesSent(h) != sent || h.gl.TodoState(gitlabtest.TodoAssigned) != "pending" {
		t.Error("a to-do item outside the allow-list was marked")
	}
	_, out = h.ok("mark_todos_done", map[string]any{"ids": []int{gitlabtest.TodoAssigned}, "dry_run": true})
	if get(out, "items", 0, "outcome") != "blocked" {
		t.Errorf("dry run: %v", out)
	}
	in := newHarness(t, harnessOptions{cfg: config.Config{WriteNamespaces: []string{gitlabtest.GroupTop}}})
	if _, out := in.ok("mark_todos_done", map[string]any{"ids": []int{gitlabtest.TodoAssigned}}); get(out, "outcome") != "done" {
		t.Errorf("inside: %v", out)
	}
}

// A thread type other than issue or merge_request is refused, not read
// as a merge request.
func TestResolveDiscussionRefusesAnUnknownType(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.fails("resolve_discussion", map[string]any{"project": alpha, "type": "epic", "iid": 1, "discussion_id": "x"}, "invalid")
}

// ------------------------------------------------------ comment edits

// noteIn finds a note in list_discussions and returns its thread and the
// note as read.
func noteIn(h *harness, typ string, iid int, id float64) (thread, note any) {
	h.t.Helper()
	_, out := h.ok("list_discussions", map[string]any{"project": alpha, "type": typ, "iid": iid})
	for _, th := range get(out, "threads").([]any) {
		for _, n := range get(th, "notes").([]any) {
			if get(n, "id") == id {
				return th, n
			}
		}
	}
	h.t.Fatalf("no note %v on %s %d", id, typ, iid)
	return nil, nil
}

func TestUpdateComment(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	id, at := commentOf(h, gitlabtest.DefaultUser, false)
	bobs, bobsAt := commentOf(h, "bob", false)
	system, systemAt := commentOf(h, gitlabtest.DefaultUser, true)
	args := func(id float64, at, body string) map[string]any {
		return map[string]any{"project": alpha, "type": "issue", "iid": 1, "note_id": id, "updated_at": at, "body": body}
	}
	threadBefore, _ := noteIn(h, "issue", 1, id)

	before := writesSent(h)
	text := h.fails("update_comment", args(bobs, bobsAt, "Mine now."), "blocked")
	if !strings.Contains(text, "@bob") {
		t.Errorf("another's comment: %s", text)
	}
	h.fails("update_comment", args(system, systemAt, "x"), "invalid")
	h.fails("update_comment", args(id, "2020-01-01T00:00:00Z", "x"), "stale")
	h.fails("update_comment", args(id, "yesterday", "x"), "invalid")
	h.fails("update_comment", args(id, at, "  "), "invalid")
	// A note is found only on the issue or merge request named.
	h.fails("update_comment", map[string]any{"project": alpha, "type": "issue", "iid": 2, "note_id": id, "updated_at": at, "body": "x"},
		"not_found")
	text = h.fails("update_comment", args(id, at, "Done.\n/close"), "blocked")
	if !strings.Contains(text, "/close") {
		t.Errorf("quick action: %s", text)
	}
	_, out := h.ok("update_comment", map[string]any{"project": alpha, "type": "issue", "iid": 1, "note_id": id, "updated_at": at,
		"body": "Rewritten.", "dry_run": true})
	if get(out, "outcome") != "dry_run" || get(out, "would_send", "method") != "PUT" {
		t.Errorf("dry run = %v", out)
	}
	if writesSent(h) != before {
		t.Fatal("a refusal or a dry run wrote")
	}

	text, out = h.ok("update_comment", args(id, at, "Rewritten.\nSecond line."))
	if get(out, "outcome") != "updated" || get(out, "body_removed") == nil || get(out, "updated_at") == at {
		t.Errorf("update = %v", out)
	}
	if !strings.Contains(text, "Updated comment") || !strings.Contains(text, "pass it to update_comment") {
		t.Errorf("text:\n%s", text)
	}
	thread, note := noteIn(h, "issue", 1, id)
	if get(note, "untrusted_body") != "Rewritten.\nSecond line." || get(thread, "id") != get(threadBefore, "id") {
		t.Errorf("after the edit: thread %v, note %v", get(thread, "id"), note)
	}
	next := get(out, "updated_at").(string)
	if get(note, "updated_at") != next {
		t.Errorf("updated_at %v, list_discussions reads %v", next, get(note, "updated_at"))
	}

	// The old witness is stale now. The same text again sends nothing and
	// reads as done, even with that old witness: an edit repeated after a
	// lost answer is not stale.
	h.fails("update_comment", args(id, at, "Again."), "stale")
	sent := writesSent(h)
	for _, witness := range []string{next, at} {
		_, out = h.ok("update_comment", args(id, witness, "Rewritten.\nSecond line."))
		if get(out, "outcome") != "unchanged" || get(out, "updated_at") != next || writesSent(h) != sent {
			t.Errorf("same text with %s = %v, %d writes", witness, out, writesSent(h)-sent)
		}
	}
}

// add_comment returns the witness update_comment needs, so a comment
// just posted is edited without reading the threads.
func TestAddCommentGivesTheWitnessForAnEdit(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, posted := h.ok("add_comment", map[string]any{"project": alpha, "type": "issue", "iid": 1, "body": "Tpyo."})
	if !strings.Contains(text, "pass it to update_comment") {
		t.Errorf("text:\n%s", text)
	}
	_, out := h.ok("update_comment", map[string]any{"project": alpha, "type": "issue", "iid": 1, "note_id": get(posted, "note_id"),
		"updated_at": get(posted, "updated_at"), "body": "Typo."})
	if get(out, "outcome") != "updated" {
		t.Errorf("update = %v", out)
	}
}

func TestUpdateCommentEscapesAQuickAction(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	id, at := commentOf(h, gitlabtest.DefaultUser, false)
	_, out := h.ok("update_comment", map[string]any{"project": alpha, "type": "issue", "iid": 1, "note_id": id, "updated_at": at,
		"body": "Done.\n/close", "escape_commands": true})
	if get(out, "outcome") != "updated" || get(out, "escaped_commands", 0, "command") != "close" {
		t.Errorf("escaped = %v", out)
	}
	_, issue := h.ok("get_issue", map[string]any{"project": alpha, "iid": 1})
	if get(issue, "state") != "opened" {
		t.Error("an escaped /close closed the issue")
	}
}

// A comment on a diff line keeps its thread and its place.
func TestUpdateCommentOnADiffLine(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	_, posted := h.ok("add_comment", map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "body": "Rename this.",
		"file": "README.md", "line": 3, "side": "new"})
	id := get(posted, "note_id").(float64)
	threadBefore, note := noteIn(h, "merge_request", 1, id)
	_, out := h.ok("update_comment", map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "note_id": id,
		"updated_at": get(note, "updated_at"), "body": "```suggestion:-0+0\nrenamed\n```"})
	if get(out, "outcome") != "updated" {
		t.Fatalf("update = %v", out)
	}
	thread, note := noteIn(h, "merge_request", 1, id)
	if get(thread, "id") != get(threadBefore, "id") || fmt.Sprint(get(thread, "position")) != fmt.Sprint(get(threadBefore, "position")) {
		t.Errorf("thread moved: %v, was %v", thread, threadBefore)
	}
	if !strings.Contains(fmt.Sprint(get(note, "untrusted_body")), "suggestion") {
		t.Errorf("body %v", get(note, "untrusted_body"))
	}
}

// The read-back: an answer whose updated_at did not move is an edit not
// made; one that moved but reads otherwise landed, and says so.
func TestUpdateCommentReadsTheEditBack(t *testing.T) {
	for _, c := range []struct {
		name, body, updatedAt string
		wantClass             string
	}{
		{"not made", "old", "", "unexpected"},
		{"answered empty", "", "2030-01-01T00:00:00Z", "unexpected"},
		{"stored otherwise", "Something else, as GitLab stored it.", "2030-01-01T00:00:00Z", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{})
			id, at := commentOf(h, gitlabtest.DefaultUser, false)
			_, note := noteIn(h, "issue", 1, id)
			body, updatedAt := c.body, c.updatedAt
			if body == "old" {
				body = get(note, "untrusted_body").(string)
			}
			if updatedAt == "" {
				updatedAt = at
			}
			project := h.gl.ProjectID(alpha)
			h.gl.Inject(gitlabtest.Fault{Method: "PUT", Path: fmt.Sprintf("/projects/%d/issues/1/notes/", project), Status: 200,
				Body: fmt.Sprintf(`{"id":%d,"body":%q,"updated_at":%q,"author":{"username":%q}}`, int64(id), body, updatedAt,
					gitlabtest.DefaultUser)})
			args := map[string]any{"project": alpha, "type": "issue", "iid": 1, "note_id": id, "updated_at": at, "body": "Something else."}
			if c.wantClass != "" {
				h.fails("update_comment", args, c.wantClass)
				return
			}
			text, out := h.ok("update_comment", args)
			if get(out, "outcome") != "updated" || !strings.Contains(text, "differences from what was sent") {
				t.Errorf("result = %v\n%s", out, text)
			}
		})
	}
}

// ------------------------------------------------- one draft at a time

// The fixture's drafts on merge request 1: one on src/login.go new line
// 3, and a reply that resolves the merge request's thread.
const (
	lineDraft  = 80001
	replyDraft = 80002
	lineText   = "Consider naming this loginUser."
	replyText  = "Agreed; resolving."
)

// draftPuts counts the edits and publishes of drafts the instance received.
func draftPuts(h *harness, suffix string) int {
	n := 0
	for _, r := range h.gl.Requests() {
		if r.Method == http.MethodPut && strings.Contains(r.EscapedPath, "/draft_notes/") && strings.HasSuffix(r.EscapedPath, suffix) {
			n++
		}
	}
	return n
}

func draftOf(h *harness, iid, id int64) (gitlab.DraftNote, bool) {
	for _, d := range h.gl.Drafts(alpha, iid) {
		if d.ID == id {
			return d, true
		}
	}
	return gitlab.DraftNote{}, false
}

func TestUpdateReviewComment(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	args := func(id int, witness, body string) map[string]any {
		return map[string]any{"project": alpha, "iid": 1, "draft_id": id, "note_sha256": witness, "body": body}
	}
	before := writesSent(h)
	h.fails("update_review_comment", args(lineDraft, hash(lineText), "  "), "invalid")
	h.fails("update_review_comment", args(lineDraft, "", "x"), "invalid")
	h.fails("update_review_comment", args(lineDraft, hash("an older text"), "x"), "stale")
	h.fails("update_review_comment", args(lineDraft, hash(lineText), "Fine.\n/approve"), "blocked")
	_, out := h.ok("update_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": lineDraft,
		"note_sha256": hash(lineText), "body": "Rename it.", "dry_run": true})
	if get(out, "outcome") != "dry_run" || fmt.Sprint(get(out, "would_send", "fields")) != "[note position]" ||
		get(out, "position", "new_line") != float64(3) {
		t.Errorf("dry run = %v", out)
	}
	if writesSent(h) != before {
		t.Fatal("a refusal or a dry run wrote")
	}

	text, out := h.ok("update_review_comment", args(lineDraft, hash(lineText), "Rename it to loginUser.\nOr keep it."))
	if get(out, "outcome") != "updated" || get(out, "note_sha256") != hash("Rename it to loginUser.\nOr keep it.") ||
		get(out, "body_removed", "lines") != float64(1) || get(out, "position", "new_line") != float64(3) {
		t.Errorf("update = %v", out)
	}
	if !strings.Contains(text, "Updated draft 80001 on !1.") || !strings.Contains(text, "pass it to update_review_comment") {
		t.Errorf("text:\n%s", text)
	}
	// GitLab clears a position the edit leaves out; the draft keeps its line.
	d, _ := draftOf(h, 1, lineDraft)
	if d.Note != "Rename it to loginUser.\nOr keep it." || d.Position == nil || d.Position.NewLine == nil || *d.Position.NewLine != 3 ||
		d.Position.NewPath != "src/login.go" {
		t.Errorf("stored draft = %+v, position %+v", d, d.Position)
	}
	_, list := h.ok("list_review_comments", map[string]any{"project": alpha, "iid": 1})
	for _, row := range get(list, "drafts").([]any) {
		if get(row, "id") == float64(lineDraft) && get(row, "note_sha256") != get(out, "note_sha256") {
			t.Errorf("list_review_comments gives %v, the edit %v", get(row, "note_sha256"), get(out, "note_sha256"))
		}
	}

	// The old witness is stale now; the same text again sends nothing and
	// reads as done, with either witness.
	h.fails("update_review_comment", args(lineDraft, hash(lineText), "Again."), "stale")
	sent := writesSent(h)
	for _, w := range []string{hash(lineText), get(out, "note_sha256").(string)} {
		_, again := h.ok("update_review_comment", args(lineDraft, w, "Rename it to loginUser.\nOr keep it."))
		if get(again, "outcome") != "unchanged" || writesSent(h) != sent {
			t.Errorf("same text with %s = %v", w, again)
		}
	}

	// A reply stays in its thread, and a general draft gets no position.
	_, out = h.ok("update_review_comment", args(replyDraft, hash(replyText), "Agreed."))
	if get(out, "outcome") != "updated" || get(out, "discussion_id") == "" || get(out, "position") != nil {
		t.Errorf("reply = %v", out)
	}
	if d, _ := draftOf(h, 1, replyDraft); d.DiscussionID == nil || d.Note != "Agreed." {
		t.Errorf("stored reply = %+v", d)
	}
}

// add_review_comment returns the witness update_review_comment needs.
func TestAddReviewCommentGivesTheWitnessForAnEdit(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, added := h.ok("add_review_comment", map[string]any{"project": alpha, "iid": 1, "body": "Tpyo."})
	if get(added, "note_sha256") != hash("Tpyo.") || !strings.Contains(text, "pass it to update_review_comment") {
		t.Errorf("add = %v\n%s", added, text)
	}
	_, out := h.ok("update_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": get(added, "note_id"),
		"note_sha256": get(added, "note_sha256"), "body": "Typo."})
	if get(out, "outcome") != "updated" {
		t.Errorf("update = %v", out)
	}
}

func TestUpdateReviewCommentEscapesAQuickAction(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	_, out := h.ok("update_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": lineDraft,
		"note_sha256": hash(lineText), "body": "Fine.\n/approve", "escape_commands": true})
	if get(out, "outcome") != "updated" || get(out, "escaped_commands", 0, "command") != "approve" {
		t.Errorf("escaped = %v", out)
	}
	if d, _ := draftOf(h, 1, lineDraft); d.Note != "Fine.\n\\/approve" {
		t.Errorf("stored %q", d.Note)
	}
}

// Drafts are their author's alone: another person's is refused as one
// that is not there, saying so, and nothing is sent.
func TestReviewCommentsAreYourOwnDrafts(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	bob := newHarness(t, harnessOptions{over: h.gl, token: func(gl *gitlabtest.Server) string { return gl.TokenFor("bob", "api") }})
	_, out := bob.ok("add_review_comment", map[string]any{"project": alpha, "iid": 1, "body": "Bob's thought."})
	id := get(out, "note_id")
	sent := draftPuts(h, "")
	text := h.fails("update_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": id,
		"note_sha256": hash("Bob's thought."), "body": "Mine now."}, "not_found")
	if !strings.Contains(text, "only your own drafts") {
		t.Errorf("update: %s", text)
	}
	text = h.fails("publish_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": id}, "not_found")
	if !strings.Contains(text, "only your own drafts") {
		t.Errorf("publish: %s", text)
	}
	if draftPuts(h, "") != sent {
		t.Error("a write was sent for another person's draft")
	}
	if d, ok := draftOf(h, 1, int64(id.(float64))); !ok || d.Note != "Bob's thought." {
		t.Errorf("bob's draft = %+v, %v", d, ok)
	}
}

// GitLab answers 500 when it will not save a draft; the server reads the
// draft and says what to fix.
func TestUpdateReviewCommentGitLabWillNotSave(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPut, Path: fmt.Sprintf("/projects/2001/merge_requests/1/draft_notes/%d", lineDraft),
		Status: http.StatusInternalServerError, Body: `{"message":"500 Internal Server Error"}`})
	text := h.fails("update_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": lineDraft,
		"note_sha256": hash(lineText), "body": "Rename it."}, "invalid")
	if !strings.Contains(text, "still reads as before") {
		t.Errorf("refusal: %s", text)
	}
	// GitLab's 500 here comes again on every try, so it is not repeated.
	if n := draftPuts(h, ""); n != 1 {
		t.Errorf("the edit was sent %d times", n)
	}
	// A 500 whose edit landed after all reads as the edit.
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPut, Path: fmt.Sprintf("/projects/2001/merge_requests/1/draft_notes/%d", lineDraft),
		AfterApply: true, Status: http.StatusInternalServerError, Body: `{"message":"500 Internal Server Error"}`})
	_, out := h.ok("update_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": lineDraft,
		"note_sha256": hash(lineText), "body": "Rename it."})
	if get(out, "outcome") != "updated" || get(out, "note_sha256") != hash("Rename it.") {
		t.Errorf("landed = %v", out)
	}
}

// An answer that took the text but dropped the draft's line is not an
// edit that kept its place.
func TestUpdateReviewCommentReadsThePositionBack(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPut, Path: fmt.Sprintf("/projects/2001/merge_requests/1/draft_notes/%d", lineDraft),
		Status: http.StatusOK, Body: fmt.Sprintf(`{"id":%d,"note":"Rename it.","position":{}}`, lineDraft)})
	text := h.fails("update_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": lineDraft,
		"note_sha256": hash(lineText), "body": "Rename it."}, "unexpected")
	if !strings.Contains(text, "lost its place") {
		t.Errorf("refusal: %s", text)
	}
}

func TestPublishReviewComment(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	_, out := h.ok("publish_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": lineDraft, "dry_run": true})
	if get(out, "outcome") != "dry_run" || get(out, "kind") != "thread" || get(out, "position", "new_line") != float64(3) ||
		draftPuts(h, "/publish") != 0 {
		t.Errorf("dry run = %v", out)
	}
	text, out := h.ok("publish_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": lineDraft})
	if get(out, "outcome") != "published" || get(out, "kind") != "thread" || get(out, "position", "new_line") != float64(3) ||
		get(out, "thread_resolved") != false || get(out, "updated_at") == nil {
		t.Errorf("publish = %v", out)
	}
	if !strings.HasPrefix(text, "Published draft 80001 as comment ") {
		t.Errorf("text:\n%s", text)
	}
	thread, note := noteIn(h, "merge_request", 1, get(out, "note_id").(float64))
	if get(note, "untrusted_body") != lineText || get(thread, "id") != get(out, "discussion_id") {
		t.Errorf("published note %v in %v", note, get(thread, "id"))
	}
	if _, ok := draftOf(h, 1, lineDraft); ok {
		t.Error("the draft is still there")
	}
	if _, ok := draftOf(h, 1, replyDraft); !ok {
		t.Error("the other draft was published too")
	}
	// The comment it became takes update_comment with the updated_at given.
	_, edit := h.ok("update_comment", map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "note_id": get(out, "note_id"),
		"updated_at": get(out, "updated_at"), "body": "Consider loginUser."})
	if get(edit, "outcome") != "updated" {
		t.Errorf("edit = %v", edit)
	}
}

// A reply drafted to resolve its thread resolves it; one that was not
// reopens it, as GitLab does, and both the dry run and the result say so.
func TestPublishingAReplySetsItsThread(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	threadID := h.gl.Discussions(alpha, "mr", 1)[0].ID
	_, out := h.ok("publish_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": replyDraft})
	if get(out, "outcome") != "published" || get(out, "kind") != "reply" || get(out, "discussion_id") != threadID ||
		get(out, "thread_resolved") != true {
		t.Errorf("resolving reply = %v", out)
	}
	if n := h.gl.Discussions(alpha, "mr", 1)[0].Notes; n[len(n)-1].Body != replyText || !n[0].Resolved {
		t.Errorf("thread after = %+v", n)
	}

	_, draft := h.ok("add_review_comment", map[string]any{"project": alpha, "iid": 1, "body": "One more thing.", "discussion_id": threadID})
	args := map[string]any{"project": alpha, "iid": 1, "draft_id": get(draft, "note_id")}
	text, _ := h.ok("publish_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": get(draft, "note_id"), "dry_run": true})
	if !strings.Contains(text, "publishing this reply reopens it") {
		t.Errorf("dry run:\n%s", text)
	}
	text, out = h.ok("publish_review_comment", args)
	if get(out, "thread_resolved") != false || !strings.Contains(text, "reopened the thread") {
		t.Errorf("reopening reply = %v\n%s", out, text)
	}
	if n := h.gl.Discussions(alpha, "mr", 1)[0].Notes; n[0].Resolved {
		t.Error("the thread is still resolved")
	}
}

// A draft written elsewhere may hold a quick action, which GitLab would
// run on publishing: it is refused, and nothing is sent.
func TestPublishingADraftWithAQuickActionIsRefused(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.gl.EditDraft(alpha, 1, lineDraft, "Looks good.\n/approve")
	text := h.fails("publish_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": lineDraft}, "blocked")
	if !strings.Contains(text, "line 2 /approve") || !strings.Contains(text, "escape_commands") {
		t.Errorf("refusal: %s", text)
	}
	if draftPuts(h, "/publish") != 0 {
		t.Error("the publish was sent")
	}
}

// GitLab saves the comment and then deletes the draft, with no
// transaction between: a draft still there does not rule the comment
// out, and a search that cannot be finished leaves it unknown.
func TestALostPublishWithTheDraftKept(t *testing.T) {
	publish := fmt.Sprintf("/projects/2001/merge_requests/1/draft_notes/%d/publish", replyDraft)
	h := newHarness(t, harnessOptions{})
	threadID := h.gl.Discussions(alpha, "mr", 1)[0].ID
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPut, Path: publish, Status: http.StatusBadGateway, Body: `{"message":"failed"}`,
		Before: func() { h.gl.Reply(alpha, 1, threadID, gitlabtest.DefaultUser, replyText) }})
	text := h.fails("publish_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": replyDraft}, "ambiguous_outcome")
	if !strings.Contains(text, "a read shows it was published: comment ") {
		t.Errorf("verdict: %s", text)
	}

	h = newHarness(t, harnessOptions{})
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPut, Path: publish, Status: http.StatusBadGateway, Body: `{"message":"failed"}`})
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodGet, Path: "/projects/2001/merge_requests/1/discussions/", Skip: 1, Times: 10,
		Status: http.StatusInternalServerError, Body: `{"message":"failed"}`})
	text = h.fails("publish_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": replyDraft}, "ambiguous_outcome")
	if !strings.Contains(text, "so it is unknown") {
		t.Errorf("verdict: %s", text)
	}
}

// The comment a draft became is newer than every note read before the
// publish: an identical reply already in the thread is not it.
func TestAnOlderSameReplyIsNotThePublishedOne(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	threadID := h.gl.Discussions(alpha, "mr", 1)[0].ID
	h.gl.Reply(alpha, 1, threadID, gitlabtest.DefaultUser, replyText)
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPut, Path: fmt.Sprintf("/projects/2001/merge_requests/1/draft_notes/%d/publish", replyDraft),
		Status: http.StatusNoContent, Before: func() { h.gl.DropDraft(alpha, 1, replyDraft) }})
	_, out := h.ok("publish_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": replyDraft})
	if get(out, "outcome") != "lost" {
		t.Errorf("publish = %v", out)
	}
}

// GitLab drops every carriage return from a published comment; the read
// back compares the texts as it does.
func TestAPublishedDraftIsFoundWithoutItsCarriageReturns(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.gl.EditDraft(alpha, 1, lineDraft, "Line one.\rLine two.")
	_, out := h.ok("publish_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": lineDraft})
	if get(out, "outcome") != "published" {
		t.Errorf("publish = %v", out)
	}
}

// A reply drafted to resolve a resolved thread may still reopen it, when
// the account may not resolve it, and the dry run says so.
func TestAResolvingReplyMayReopen(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	threadID := h.gl.Discussions(alpha, "mr", 1)[0].ID
	h.ok("resolve_discussion", map[string]any{"project": alpha, "iid": 1, "discussion_id": threadID})
	text, _ := h.ok("publish_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": replyDraft, "dry_run": true})
	if !strings.Contains(text, "may reopen it") {
		t.Errorf("dry run:\n%s", text)
	}
}

// A merge request that is not there is named as such, not as a draft
// that is not yours.
func TestADraftOnAMissingMergeRequest(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"update_review_comment", map[string]any{"project": alpha, "iid": 999, "draft_id": lineDraft, "note_sha256": hash(lineText), "body": "x"}},
		{"publish_review_comment", map[string]any{"project": alpha, "iid": 999, "draft_id": lineDraft}},
	} {
		text := h.fails(c.tool, c.args, "not_found")
		if strings.Contains(text, "only your own drafts") || !strings.Contains(text, "merge request") {
			t.Errorf("%s: %s", c.tool, text)
		}
	}
}

// A draft not on the diff by diffPosition's rule, commits named but no
// path, is sent no position, and its edit is not taken for one that
// lost its place.
func TestADraftWithNoPathKeepsNoPosition(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	mr, _ := h.gl.MergeRequest(alpha, 1)
	h.gl.SetDraftPosition(alpha, 1, replyDraft, &gitlab.Position{BaseSHA: mr.DiffRefs.BaseSHA, StartSHA: mr.DiffRefs.StartSHA,
		HeadSHA: mr.DiffRefs.HeadSHA, PositionType: "text"})
	_, out := h.ok("update_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": replyDraft,
		"note_sha256": hash(replyText), "body": "Agreed.", "dry_run": true})
	if fmt.Sprint(get(out, "would_send", "fields")) != "[note]" {
		t.Errorf("dry run = %v", out)
	}
	_, out = h.ok("update_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": replyDraft,
		"note_sha256": hash(replyText), "body": "Agreed."})
	if get(out, "outcome") != "updated" {
		t.Errorf("update = %v", out)
	}
}

// submit_review publishes drafts written elsewhere too, and GitLab runs
// their quick actions then: a review with one is refused, naming them.
func TestSubmitReviewRefusesDraftsWithQuickActions(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.gl.EditDraft(alpha, 1, lineDraft, "Fine.\n/merge")
	h.gl.EditDraft(alpha, 1, replyDraft, "/approve")
	for _, dry := range []bool{true, false} {
		text := h.fails("submit_review", map[string]any{"project": alpha, "iid": 1, "summary": "x", "dry_run": dry}, "blocked")
		if !strings.Contains(text, "draft 80001: ") || !strings.Contains(text, "line 2 /merge") || !strings.Contains(text, "Drafts 80002") ||
			!strings.Contains(text, "update_review_comment") {
			t.Errorf("refusal: %s", text)
		}
	}
	if len(h.gl.Drafts(alpha, 1)) != 2 || writesSent(h) != 0 {
		t.Error("the review was published")
	}
}

// GitLab deletes a draft whose comment does not save and answers 204 all
// the same: the read afterwards finds no comment, and the text comes back.
func TestALostDraftIsReportedLost(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	threads := len(h.gl.Discussions(alpha, "mr", 1))
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPut, Path: fmt.Sprintf("/projects/2001/merge_requests/1/draft_notes/%d/publish", lineDraft),
		Status: http.StatusNoContent, Before: func() { h.gl.DropDraft(alpha, 1, lineDraft) }})
	text, out := h.ok("publish_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": lineDraft})
	if get(out, "outcome") != "lost" || get(out, "note_id") != float64(0) || get(out, "untrusted_body") != lineText ||
		get(out, "position") != nil {
		t.Errorf("lost = %v", out)
	}
	if !strings.HasPrefix(text, "Draft 80001 was lost") || !strings.Contains(text, "<<<UNTRUSTED") {
		t.Errorf("text:\n%s", text)
	}
	if len(h.gl.Discussions(alpha, "mr", 1)) != threads {
		t.Error("a comment was made")
	}
}

// A publish GitLab did not confirm is never sent again; a read settles it.
func TestALostPublishIsSettledByReading(t *testing.T) {
	path := func(id int) string { return fmt.Sprintf("/projects/2001/merge_requests/1/draft_notes/%d/publish", id) }
	for _, c := range []struct {
		name   string
		fault  gitlabtest.Fault
		before func(h *harness)
		want   string
	}{
		{"landed", gitlabtest.Fault{AfterApply: true, Status: http.StatusBadGateway}, nil, "a read shows it was published: comment "},
		{"not landed", gitlabtest.Fault{Status: http.StatusBadGateway}, nil, "still unpublished and no comment came of it. Nothing was repeated"},
		{"refused with a 500", gitlabtest.Fault{Status: http.StatusInternalServerError}, nil, "status 500; draft 80001 is still unpublished"},
		// A comment of the same text that is not a new thread on the
		// draft's line is not the draft's.
		{"gone, the same text elsewhere", gitlabtest.Fault{Status: http.StatusBadGateway}, func(h *harness) {
			h.gl.Comment(alpha, "mr", 1, gitlabtest.DefaultUser, lineText)
			h.gl.DropDraft(alpha, 1, lineDraft)
		}, "is gone with no comment"},
		{"gone", gitlabtest.Fault{Status: http.StatusBadGateway}, func(h *harness) { h.gl.DropDraft(alpha, 1, lineDraft) }, "is gone with no comment"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{})
			f := c.fault
			f.Method, f.Path, f.Body = http.MethodPut, path(lineDraft), `{"message":"failed"}`
			if c.before != nil {
				f.Before = func() { c.before(h) }
			}
			h.gl.Inject(f)
			text := h.fails("publish_review_comment", map[string]any{"project": alpha, "iid": 1, "draft_id": lineDraft}, "ambiguous_outcome")
			if !strings.Contains(text, c.want) {
				t.Errorf("verdict: %s", text)
			}
			if n := draftPuts(h, "/publish"); n != 1 {
				t.Errorf("the publish was sent %d times", n)
			}
		})
	}
}

// ------------------------------------------------------- time tracking

// timePosts counts the time tracking writes the instance received.
func timePosts(h *harness, op string) int {
	n := 0
	for _, r := range h.gl.Requests() {
		if r.Method == http.MethodPost && strings.HasSuffix(r.EscapedPath, "/"+op) {
			n++
		}
	}
	return n
}

// spentWitness is an item's total_time_spent, as get_issue or
// get_merge_request reads it.
func spentWitness(h *harness, typ string, iid int) any {
	h.t.Helper()
	tool := "get_issue"
	if typ == "merge_request" {
		tool = "get_merge_request"
	}
	_, out := h.ok(tool, map[string]any{"project": alpha, "iid": iid})
	return get(out, "time_stats", "total_time_spent")
}

func TestGetIssueAndMergeRequestShowTimeStats(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, out := h.ok("get_issue", map[string]any{"project": alpha, "iid": 3})
	if get(out, "time_stats", "time_estimate") != float64(12600) || get(out, "time_stats", "total_time_spent") != float64(36000) ||
		get(out, "time_stats", "human_time_estimate") != "3h 30m" || get(out, "time_stats", "human_total_time_spent") != "10h" {
		t.Errorf("issue time_stats = %v", get(out, "time_stats"))
	}
	if !strings.Contains(text, "Time: estimate 3h 30m, spent 10h.") {
		t.Errorf("issue text:\n%s", text)
	}
	text, out = h.ok("get_merge_request", map[string]any{"project": alpha, "iid": 1})
	if get(out, "time_stats", "time_estimate") != float64(144000) || get(out, "time_stats", "human_time_estimate") != "40h" ||
		get(out, "time_stats", "human_total_time_spent") != "" {
		t.Errorf("merge request time_stats = %v", get(out, "time_stats"))
	}
	if !strings.Contains(text, "Time: estimate 40h, spent (none).") {
		t.Errorf("merge request text:\n%s", text)
	}
}

func TestTrackTime(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	witness := issueWitness(h, 3)
	text, out := h.ok("track_time", map[string]any{"project": alpha, "type": "issue", "iid": 3, "updated_at": witness,
		"estimate": "1w 2d", "add_spent": "1h30m", "total_time_spent": 36000})
	if get(out, "outcome") != "updated" || get(out, "before", "time_estimate") != float64(12600) ||
		get(out, "after", "time_estimate") != float64(144000+2*28800) || get(out, "after", "total_time_spent") != float64(36000+5400) ||
		get(out, "after", "human_total_time_spent") != "11h 30m" || get(out, "total_time_spent") != float64(36000+5400) {
		t.Fatalf("result = %v", out)
	}
	if strings.Join(strs(get(out, "sent")), ",") != "time_estimate 1w 2d,add_spent_time 1h30m" ||
		strings.Join(strs(get(out, "changed")), ",") != "time_estimate,total_time_spent" {
		t.Errorf("sent %v, changed %v", get(out, "sent"), get(out, "changed"))
	}
	if !strings.Contains(text, "Before: estimate 3h 30m, spent 10h.") || !strings.Contains(text, "After, as read back: estimate 56h, spent 11h 30m.") ||
		!strings.Contains(text, "total_time_spent is 41400; pass it") {
		t.Errorf("text:\n%s", text)
	}
	// The writes moved updated_at, the result carries the new one, and the
	// old one is stale; nothing is sent.
	next := get(out, "updated_at").(string)
	if next == witness || next != issueWitness(h, 3) {
		t.Errorf("updated_at %s after %s; get_issue reads %s", next, witness, issueWitness(h, 3))
	}
	sent := writesSent(h)
	if text := h.fails("track_time", map[string]any{"project": alpha, "type": "issue", "iid": 3, "updated_at": witness,
		"add_spent": "1h", "total_time_spent": get(out, "total_time_spent")}, "stale"); !strings.Contains(text, "changed since it was read") || writesSent(h) != sent {
		t.Errorf("stale: %s", text)
	}

	// A subtraction leaves updated_at where it was, as GitLab does, so the
	// total is what refuses it sent again.
	subtract := map[string]any{"project": alpha, "type": "issue", "iid": 3, "updated_at": next, "add_spent": "-30m",
		"total_time_spent": get(out, "total_time_spent")}
	_, out = h.ok("track_time", subtract)
	if get(out, "after", "total_time_spent") != float64(36000+5400-1800) || get(out, "updated_at") != next {
		t.Errorf("subtracting: %v", out)
	}
	sent = writesSent(h)
	if text := h.fails("track_time", subtract, "stale"); !strings.Contains(text, "total_time_spent is now 39600") || writesSent(h) != sent {
		t.Errorf("the same subtraction again: %s", text)
	}
	_, out = h.ok("track_time", map[string]any{"project": alpha, "type": "issue", "iid": 3, "updated_at": get(out, "updated_at"),
		"reset_estimate": true, "reset_spent": true, "total_time_spent": get(out, "total_time_spent")})
	if get(out, "outcome") != "updated" || get(out, "after", "time_estimate") != float64(0) || get(out, "after", "total_time_spent") != float64(0) ||
		get(out, "after", "human_time_estimate") != "" {
		t.Errorf("resetting: %v", out)
	}

	// Nothing left to reset: nothing sent, and said.
	sent = writesSent(h)
	text, out = h.ok("track_time", map[string]any{"project": alpha, "type": "issue", "iid": 3, "updated_at": get(out, "updated_at"),
		"reset_estimate": true, "reset_spent": true, "total_time_spent": 0})
	if get(out, "outcome") != "unchanged" || writesSent(h) != sent || len(strs(get(out, "sent"))) != 0 ||
		!strings.Contains(text, "There is no estimate, so resetting it is left out.") || !strings.Contains(text, "was not changed") {
		t.Errorf("unchanged: %v\n%s", out, text)
	}

	// A merge request, the same way.
	_, out = h.ok("track_time", map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "updated_at": mrWitness(h, 1),
		"estimate": "2", "add_spent": "1mo", "total_time_spent": 0})
	if get(out, "before", "time_estimate") != float64(144000) || get(out, "after", "time_estimate") != float64(7200) ||
		get(out, "after", "total_time_spent") != float64(576000) || get(out, "after", "human_total_time_spent") != "160h" {
		t.Errorf("merge request: %v", out)
	}
	// The estimate it already has is left out, and the rest is sent.
	text, out = h.ok("track_time", map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "updated_at": get(out, "updated_at"),
		"estimate": "2h", "add_spent": "15m", "total_time_spent": get(out, "total_time_spent")})
	if strings.Join(strs(get(out, "sent")), ",") != "add_spent_time 15m" || !strings.Contains(text, "The estimate is already 2h") {
		t.Errorf("already set: %v\n%s", out, text)
	}
}

func TestTrackTimeRefusesBeforeSending(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	w := issueWitness(h, 3)
	sent := writesSent(h)
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{}, "nothing to change"},
		{map[string]any{"estimate": "1h", "reset_estimate": true}, "not both"},
		{map[string]any{"add_spent": "1h", "reset_spent": true}, "not both"},
		{map[string]any{"estimate": "-1h"}, "must not be negative"},
		{map[string]any{"estimate": "3 hours"}, "is not a duration"},
		{map[string]any{"estimate": "5 foo"}, "is not a duration"},
		{map[string]any{"estimate": "1h 30"}, "is not a duration"},
		{map[string]any{"add_spent": "0m"}, "is zero"},
		{map[string]any{"add_spent": "-"}, "is empty"},
		{map[string]any{"add_spent": "-2d"}, "below zero: 10h is spent so far"},
		{map[string]any{"add_spent": "5y"}, "is not a duration"},
		{map[string]any{"add_spent": "900mo"}, "past four years"},
	} {
		args := map[string]any{"project": alpha, "type": "issue", "iid": 3, "updated_at": w, "total_time_spent": 36000}
		maps.Copy(args, c.args)
		if text := h.fails("track_time", args, "invalid"); !strings.Contains(text, c.want) {
			t.Errorf("%v: %s, want %q", c.args, text, c.want)
		}
	}
	// Spent time needs its own witness: updated_at does not move when spent
	// time is added or reset.
	if text := h.fails("track_time", map[string]any{"project": alpha, "type": "issue", "iid": 3, "updated_at": w, "reset_spent": true},
		"invalid"); !strings.Contains(text, "need total_time_spent") {
		t.Errorf("no spent witness: %s", text)
	}
	if n := writesSent(h) - sent; n != 0 {
		t.Errorf("refusals sent %d writes", n)
	}
}

// Tracking time needs the rights to manage the item, which a user who can
// only read it lacks.
func TestTrackTimeNeedsTheRightsToManageTheItem(t *testing.T) {
	h := newHarness(t, harnessOptions{token: func(gl *gitlabtest.Server) string { return gl.TokenFor("dave", "api") }})
	text := h.fails("track_time", map[string]any{"project": alpha, "type": "issue", "iid": 3, "updated_at": issueWitness(h, 3),
		"add_spent": "1h", "total_time_spent": 36000}, "forbidden")
	if !strings.Contains(text, "manage the issue, the Planner role or higher") {
		t.Errorf("forbidden: %s", text)
	}
}

// A Reporter tracks time on an issue but not on a merge request, which
// takes a Developer, and the refusal names that role.
func TestTrackTimeOnAMergeRequestNeedsADeveloper(t *testing.T) {
	h := newHarness(t, harnessOptions{token: func(gl *gitlabtest.Server) string { return gl.TokenFor("dave", "api") }})
	h.gl.SetMemberLevel(alpha, "dave", 20)
	h.ok("track_time", map[string]any{"project": alpha, "type": "issue", "iid": 3, "updated_at": issueWitness(h, 3), "add_spent": "1h",
		"total_time_spent": 36000})
	text := h.fails("track_time", map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "updated_at": mrWitness(h, 1),
		"add_spent": "1h", "total_time_spent": 0}, "forbidden")
	if !strings.Contains(text, "manage the merge request, the Developer role or higher") {
		t.Errorf("forbidden: %s", text)
	}
}

// GitLab keeps an estimate past its integer limit as the limit rather
// than refusing it, and so does the result.
func TestTrackTimeCapsAnEstimateAsGitLabDoes(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, out := h.ok("track_time", map[string]any{"project": alpha, "type": "issue", "iid": 3, "updated_at": issueWitness(h, 3),
		"estimate": "99999999h"})
	if get(out, "after", "time_estimate") != float64(2147483647) || !strings.Contains(text, "at most 2147483647 seconds") {
		t.Errorf("capped: %v\n%s", out, text)
	}
	// Asked again, it already holds.
	if _, out = h.ok("track_time", map[string]any{"project": alpha, "type": "issue", "iid": 3, "updated_at": get(out, "updated_at"),
		"estimate": "99999999h"}); get(out, "outcome") != "unchanged" {
		t.Errorf("again: %v", out)
	}
}

// Values that already hold are checked before the witness, so a call
// repeated after it landed reads as unchanged rather than stale.
func TestARepeatedTrackTimeIsUnchangedNotStale(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	w := issueWitness(h, 3)
	args := map[string]any{"project": alpha, "type": "issue", "iid": 3, "updated_at": w, "estimate": "2h"}
	h.ok("track_time", args)
	sent := writesSent(h)
	if _, out := h.ok("track_time", args); get(out, "outcome") != "unchanged" || writesSent(h) != sent {
		t.Errorf("repeated: %v", out)
	}
	args["add_spent"], args["total_time_spent"] = "1h", 36000
	h.fails("track_time", args, "stale")
}

// A failure after an earlier step landed says so, names the updated_at
// that change left, and a failure before a later step names what was not
// sent.
func TestTrackTimeSaysWhatLandedAndWhatWasNotSent(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: "/projects/2001/issues/3/add_spent_time", Status: http.StatusBadRequest,
		Body: `{"message":{"base":["refused"]}}`})
	text := h.fails("track_time", map[string]any{"project": alpha, "type": "issue", "iid": 3, "updated_at": issueWitness(h, 3),
		"estimate": "4h", "add_spent": "1h", "total_time_spent": 36000}, "invalid")
	now := issueWitness(h, 3)
	if !strings.HasPrefix(text, "[invalid] GitLab took time_estimate 4h first. add_spent_time failed:") ||
		!strings.Contains(text, "updated_at is now "+now+" and total_time_spent 36000;") {
		t.Errorf("second step: %s (updated_at %s)", text, now)
	}
	// The new updated_at is the witness for the rest.
	h.ok("track_time", map[string]any{"project": alpha, "type": "issue", "iid": 3, "updated_at": now, "add_spent": "1h",
		"total_time_spent": 36000})

	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: "/projects/2001/issues/3/time_estimate", Status: http.StatusBadRequest,
		Body: `{"message":"400 Bad request"}`})
	adds := timePosts(h, "add_spent_time")
	text = h.fails("track_time", map[string]any{"project": alpha, "type": "issue", "iid": 3, "updated_at": issueWitness(h, 3),
		"estimate": "5h", "add_spent": "1h", "total_time_spent": spentWitness(h, "issue", 3)}, "invalid")
	if !strings.Contains(text, "Not sent: add_spent_time 1h.") || timePosts(h, "add_spent_time") != adds {
		t.Errorf("first step: %s", text)
	}

	// An estimate GitLab never confirmed may have landed on any try.
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: "/projects/2001/issues/3/time_estimate", Status: http.StatusServiceUnavailable,
		Body: `{"message":"503"}`, AfterApply: true, Times: 20})
	text = h.fails("track_time", map[string]any{"project": alpha, "type": "issue", "iid": 3, "updated_at": issueWitness(h, 3),
		"estimate": "6h", "add_spent": "1h", "total_time_spent": spentWitness(h, "issue", 3)}, "unavailable")
	if !strings.Contains(text, "may have landed all the same") || !strings.Contains(text, "estimate at 6h, as asked") ||
		!strings.Contains(text, "Not sent: add_spent_time 1h.") || !strings.Contains(text, "updated_at is now "+issueWitness(h, 3)) {
		t.Errorf("estimate unconfirmed: %s", text)
	}
}

// Every write confirmed but the read-back failed: the result stands on
// GitLab's last answer, with updated_at unknown, rather than an error
// that invites sending it again.
func TestTrackTimeWhenTheReadBackFails(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	w := issueWitness(h, 3)
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodGet, Path: "/projects/2001/issues/3", Skip: 1, Status: http.StatusNotFound,
		Body: `{"message":"404 Issue Not Found"}`})
	text, out := h.ok("track_time", map[string]any{"project": alpha, "type": "issue", "iid": 3, "updated_at": w, "add_spent": "1h",
		"total_time_spent": 36000})
	if get(out, "outcome") != "updated" || get(out, "after", "total_time_spent") != float64(36000+3600) || get(out, "updated_at") != nil ||
		get(out, "total_time_spent") != float64(36000+3600) || !strings.Contains(text, "Reading it back failed") {
		t.Errorf("read-back failed: %v\n%s", out, text)
	}
}

// Adding spent time is never sent twice: a lost answer is settled by
// reading the total.
func TestALostSpentTimeIsSettledByReading(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	path := "/projects/2001/issues/3/add_spent_time"
	lost := gitlabtest.Fault{Method: http.MethodPost, Path: path, Status: http.StatusBadGateway, Body: `{"message":"502 Bad Gateway"}`}
	landed := lost
	landed.AfterApply = true
	h.gl.Inject(landed)
	text := h.fails("track_time", map[string]any{"project": alpha, "type": "issue", "iid": 3, "updated_at": issueWitness(h, 3),
		"add_spent": "1h", "total_time_spent": 36000}, "ambiguous_outcome")
	if !strings.Contains(text, "went from 10h to 11h, so it landed. Do not repeat") || !strings.Contains(text, "total_time_spent 39600;") || !strings.Contains(text, "updated_at is now "+issueWitness(h, 3)) {
		t.Errorf("landed: %s", text)
	}
	h.gl.Inject(lost)
	text = h.fails("track_time", map[string]any{"project": alpha, "type": "issue", "iid": 3, "updated_at": issueWitness(h, 3),
		"add_spent": "1h", "total_time_spent": 39600}, "ambiguous_outcome")
	if !strings.Contains(text, "still at 11h, so it did not land. Nothing was repeated") {
		t.Errorf("not landed: %s", text)
	}
	if n := timePosts(h, "add_spent_time"); n != 2 {
		t.Errorf("%d add_spent_time requests for two calls", n)
	}
	// The estimate went first and landed; the result says so.
	h.gl.Inject(lost)
	text = h.fails("track_time", map[string]any{"project": alpha, "type": "issue", "iid": 3, "updated_at": issueWitness(h, 3),
		"estimate": "4h", "add_spent": "1h", "total_time_spent": 39600}, "ambiguous_outcome")
	if !strings.HasPrefix(text, "[ambiguous_outcome] GitLab took time_estimate 4h first.") {
		t.Errorf("after the estimate: %s", text)
	}

	// A reset is settled by a total of zero.
	resets := "/projects/2001/issues/3/reset_spent_time"
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: resets, Status: http.StatusBadGateway, Body: `{"message":"502 Bad Gateway"}`})
	text = h.fails("track_time", map[string]any{"project": alpha, "type": "issue", "iid": 3, "updated_at": issueWitness(h, 3),
		"reset_spent": true, "total_time_spent": 39600}, "ambiguous_outcome")
	if !strings.Contains(text, "still at 11h, so it did not land") {
		t.Errorf("reset not landed: %s", text)
	}
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: resets, AfterApply: true, Status: http.StatusBadGateway,
		Body: `{"message":"502 Bad Gateway"}`})
	text = h.fails("track_time", map[string]any{"project": alpha, "type": "issue", "iid": 3, "updated_at": issueWitness(h, 3),
		"reset_spent": true, "total_time_spent": 39600}, "ambiguous_outcome")
	if !strings.Contains(text, "no time spent, so it is reset either way. Do not repeat") {
		t.Errorf("reset landed: %s", text)
	}
	if n := timePosts(h, "reset_spent_time"); n != 2 {
		t.Errorf("%d reset_spent_time requests for two calls", n)
	}

	// A merge request settles the same way.
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: "/projects/2001/merge_requests/1/add_spent_time", AfterApply: true,
		Status: http.StatusBadGateway, Body: `{"message":"502 Bad Gateway"}`})
	text = h.fails("track_time", map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "updated_at": mrWitness(h, 1),
		"add_spent": "30m", "total_time_spent": 0}, "ambiguous_outcome")
	if !strings.Contains(text, "went from none to 30m, so it landed") || !strings.Contains(text, "updated_at is now "+mrWitness(h, 1)) {
		t.Errorf("merge request: %s", text)
	}
}

// ------------------------------------------------------------ reactions

// awardWrites counts the reaction POSTs and DELETEs sent.
func awardWrites(h *harness) int {
	n := 0
	for _, r := range h.gl.Requests() {
		if r.Method != http.MethodGet && strings.Contains(r.EscapedPath, "/award_emoji") {
			n++
		}
	}
	return n
}

// A reaction is added once and removed by its id; asking for the state
// it is in sends nothing. The item's votes count thumbsup and thumbsdown.
func TestReact(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	issue := func(extra map[string]any) map[string]any {
		out := map[string]any{"project": alpha, "type": "issue", "iid": 1}
		maps.Copy(out, extra)
		return out
	}
	text, out := h.ok("react", issue(map[string]any{"emoji": "thumbsup"}))
	if get(out, "outcome") != "added" || get(out, "emoji") != "thumbsup" || get(out, "reacted") != true ||
		!strings.Contains(text, "Reacted with :thumbsup: on #1.") {
		t.Errorf("add: %v\n%s", out, text)
	}
	if _, out = h.ok("get_issue", map[string]any{"project": alpha, "iid": 1}); get(out, "upvotes") != 1.0 || get(out, "downvotes") != 0.0 {
		t.Errorf("votes after the add: %v %v", get(out, "upvotes"), get(out, "downvotes"))
	}
	sent := awardWrites(h)
	// +1 is GitLab's alias of thumbsup.
	for _, emoji := range []string{"thumbsup", "+1", ":thumbsup:"} {
		_, out = h.ok("react", issue(map[string]any{"emoji": emoji}))
		if get(out, "outcome") != "unchanged" || get(out, "reacted") != true || !strings.Contains(fmt.Sprint(get(out, "notes")), "already reacted") {
			t.Errorf("again as %s: %v", emoji, out)
		}
	}
	if awardWrites(h) != sent {
		t.Errorf("an unchanged reaction sent %d writes", awardWrites(h)-sent)
	}
	_, out = h.ok("react", issue(map[string]any{"emoji": "tada", "dry_run": true}))
	if get(out, "outcome") != "dry_run" || get(out, "would_send", "operation") != "react with tada" || get(out, "reacted") != false {
		t.Errorf("dry run: %v", out)
	}
	if _, out = h.ok("react", issue(map[string]any{"emoji": ":tada:"})); get(out, "outcome") != "added" || get(out, "emoji") != "tada" {
		t.Errorf("tada: %v", out)
	}
	// bob's reaction is his: alice's remove leaves it.
	h.gl.React(alpha, "issue", 1, 0, "bob", "eyes")
	_, out = h.ok("react", issue(map[string]any{"emoji": "tada", "remove": true, "dry_run": true}))
	if get(out, "outcome") != "dry_run" || get(out, "would_send", "method") != "DELETE" || get(out, "reacted") != true {
		t.Errorf("remove dry run: %v", out)
	}
	text, out = h.ok("react", issue(map[string]any{"emoji": "tada", "remove": true}))
	if get(out, "outcome") != "removed" || get(out, "reacted") != false || !strings.Contains(text, "Removed your :tada: reaction from #1.") {
		t.Errorf("remove: %v\n%s", out, text)
	}
	sent = awardWrites(h)
	text, out = h.ok("react", issue(map[string]any{"emoji": "eyes", "remove": true}))
	if get(out, "outcome") != "unchanged" || !strings.Contains(text, "You have no eyes reaction there") ||
		!strings.Contains(text, "Yours there: thumbsup.") || awardWrites(h) != sent {
		t.Errorf("remove one you never added: %v\n%s", out, text)
	}
	if _, out = h.ok("react", issue(map[string]any{"emoji": "+1", "remove": true})); get(out, "outcome") != "removed" {
		t.Errorf("remove by alias: %v", out)
	}
	if got, want := h.gl.Reactions(alpha, "issue", 1, 0), []string{"bob:eyes"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("reactions left = %v, want %v", got, want)
	}
	// A merge request's votes, and its reactions, are its own.
	if _, out = h.ok("react", map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "emoji": "-1"}); get(out, "outcome") != "added" ||
		get(out, "emoji") != "thumbsdown" {
		t.Errorf("merge request: %v", out)
	}
	if _, out = h.ok("get_merge_request", map[string]any{"project": alpha, "iid": 1}); get(out, "downvotes") != 1.0 {
		t.Errorf("merge request votes: %v", get(out, "downvotes"))
	}
	if a := listed(t, h)["react"].Annotations; a.OpenWorldHint == nil || !*a.OpenWorldHint || !a.IdempotentHint {
		t.Errorf("annotations = %+v", a)
	}
}

// A reaction on a comment is the comment's: it moves the comment's
// updated_at, which update_comment compares, and not the issue's.
func TestReactOnAComment(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	note := h.gl.Discussions(alpha, "issue", 2)[0].Notes[1] // alice's reply in a thread
	before, _ := h.gl.Issue(alpha, 2)
	args := map[string]any{"project": alpha, "type": "issue", "iid": 2, "note_id": note.ID, "emoji": "heart"}
	text, out := h.ok("react", args)
	if get(out, "outcome") != "added" || get(out, "note_id") != float64(note.ID) ||
		!strings.Contains(text, fmt.Sprintf("Reacted with :heart: on comment %d on #2.", note.ID)) {
		t.Errorf("comment: %v\n%s", out, text)
	}
	if got := h.gl.Reactions(alpha, "issue", 2, note.ID); fmt.Sprint(got) != "[alice:heart]" {
		t.Errorf("comment reactions = %v", got)
	}
	if h.gl.Reactions(alpha, "issue", 2, 0) != nil {
		t.Errorf("the issue got the comment's reaction: %v", h.gl.Reactions(alpha, "issue", 2, 0))
	}
	after, _ := h.gl.Issue(alpha, 2)
	if moved := h.gl.Discussions(alpha, "issue", 2)[0].Notes[1].UpdatedAt; !moved.After(note.UpdatedAt) || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("comment updated_at %v -> %v; issue %v -> %v", note.UpdatedAt, moved, before.UpdatedAt, after.UpdatedAt)
	}
	h.fails("update_comment", map[string]any{"project": alpha, "type": "issue", "iid": 2, "note_id": note.ID, "body": "Edited.",
		"updated_at": note.UpdatedAt.Format("2006-01-02T15:04:05.000Z07:00")}, "stale")
	if _, out = h.ok("react", map[string]any{"project": alpha, "type": "issue", "iid": 2, "note_id": note.ID, "emoji": "heart",
		"remove": true}); get(out, "outcome") != "removed" || len(h.gl.Reactions(alpha, "issue", 2, note.ID)) != 0 {
		t.Errorf("remove from the comment: %v", out)
	}
}

// GitLab refuses a reaction with 404 whatever the reason; the result
// says what it may mean, and names a comment GitLab wrote itself.
func TestReactionsGitLabRefuses(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	sent := awardWrites(h)
	for _, emoji := range []string{"👍", "Thumbsup", "", "thumbs up"} {
		text := h.fails("react", map[string]any{"project": alpha, "type": "issue", "iid": 1, "emoji": emoji}, "invalid")
		if !strings.Contains(text, "not an emoji name") {
			t.Errorf("%q: %s", emoji, text)
		}
	}
	if awardWrites(h) != sent {
		t.Error("a malformed name was sent")
	}
	text := h.fails("react", map[string]any{"project": alpha, "type": "issue", "iid": 1, "emoji": "no_such_emoji"}, "not_found")
	if !strings.Contains(text, "Name is not a valid emoji name") || !strings.Contains(text, "when it does not know the emoji") {
		t.Errorf("unknown emoji: %s", text)
	}
	system := h.gl.Discussions(alpha, "issue", 1)[2].Notes[0]
	text = h.fails("react", map[string]any{"project": alpha, "type": "issue", "iid": 1, "note_id": system.ID, "emoji": "tada"}, "invalid")
	if !system.System || !strings.Contains(text, "a note GitLab wrote to record an event") {
		t.Errorf("system note: %s", text)
	}
	h.fails("react", map[string]any{"project": alpha, "type": "issue", "iid": 999, "emoji": "tada"}, "not_found")
	h.fails("react", map[string]any{"project": alpha, "type": "issue", "iid": 1, "note_id": 1, "emoji": "tada"}, "not_found")
	// thumbs_up is an alias this server does not know, and no read can
	// tell it is thumbsup: GitLab's refusal stands, naming yours.
	h.ok("react", map[string]any{"project": alpha, "type": "issue", "iid": 1, "emoji": "thumbsup"})
	text = h.fails("react", map[string]any{"project": alpha, "type": "issue", "iid": 1, "emoji": "thumbs_up"}, "not_found")
	if !strings.Contains(text, "under another of its names") || !strings.Contains(text, "Your reactions there: thumbsup.") {
		t.Errorf("already there under an alias: %s", text)
	}
	// A locked discussion takes no reaction from one who is not a member.
	h.gl.LockDiscussion(alpha, "issue", 2)
	dave := newHarness(t, harnessOptions{over: h.gl, token: func(gl *gitlabtest.Server) string { return gl.TokenFor("dave", "api") }})
	text = dave.fails("react", map[string]any{"project": alpha, "type": "issue", "iid": 2, "emoji": "tada"}, "not_found")
	if !strings.Contains(text, "locked discussion") {
		t.Errorf("locked: %s", text)
	}
	// A merge request dave may not read is not found, as GitLab's
	// reaction routes answer.
	h = newHarness(t, harnessOptions{token: func(gl *gitlabtest.Server) string { return gl.TokenFor("dave", "api") }})
	h.gl.SetMergeRequestsAccess(alpha, "private")
	h.fails("react", map[string]any{"project": alpha, "type": "merge_request", "iid": 1, "emoji": "tada"}, "not_found")
}

// GitLab words its refusal in the account's language; a reaction a read
// finds there is unchanged whatever the words.
func TestARefusedAddIsReadNotParsed(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: "/projects/2001/issues/3/award_emoji", AfterApply: true,
		Status: http.StatusNotFound, Body: `{"message":"404 Award Emoji Name ya está en uso Not Found"}`})
	text, out := h.ok("react", map[string]any{"project": alpha, "type": "issue", "iid": 3, "emoji": "rocket"})
	if get(out, "outcome") != "unchanged" || get(out, "reacted") != true || !strings.Contains(text, "GitLab refused the add, and a read shows") {
		t.Errorf("result %v\n%s", out, text)
	}
}

// Past 1,000 reactions a read cannot say whether yours is there, and a
// lost add stays unknown.
func TestManyReactionsLeaveYoursUnknown(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	for range 1001 {
		h.gl.React(alpha, "issue", 3, 0, "bob", "eyes")
	}
	_, out := h.ok("react", map[string]any{"project": alpha, "type": "issue", "iid": 3, "emoji": "tada", "dry_run": true})
	if get(out, "reacted") != nil {
		t.Errorf("reacted = %v, want null", get(out, "reacted"))
	}
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: "/projects/2001/issues/3/award_emoji", AfterApply: true,
		Status: http.StatusBadGateway, Body: `{"message":"502 Bad Gateway"}`})
	text := h.fails("react", map[string]any{"project": alpha, "type": "issue", "iid": 3, "emoji": "tada"}, "ambiguous_outcome")
	if !strings.Contains(text, "so it is unknown") {
		t.Errorf("lost add: %s", text)
	}
}

// One react call reads who you are once, and a remove reads its
// reaction back by id rather than listing them all again.
func TestReactReadsLittle(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.ok("react", map[string]any{"project": alpha, "type": "issue", "iid": 3, "emoji": "rocket"})
	h.gl.ResetRequests()
	h.ok("react", map[string]any{"project": alpha, "type": "issue", "iid": 3, "emoji": "rocket", "remove": true})
	users, lists, byID := 0, 0, 0
	for _, r := range h.gl.Requests() {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.EscapedPath, "/user"):
			users++
		case r.Method == http.MethodGet && strings.HasSuffix(r.EscapedPath, "/award_emoji"):
			lists++
		case r.Method == http.MethodGet && strings.Contains(r.EscapedPath, "/award_emoji/"):
			byID++
		}
	}
	if users != 1 || lists != 1 || byID != 1 {
		t.Errorf("GET /user %d, lists %d, reads by id %d; want 1 each", users, lists, byID)
	}
}

// An add whose answer was lost is never sent again; a read settles it.
// A remove repeats, and the second answer's 404 is read as removed.
func TestALostReactionIsSettledByReading(t *testing.T) {
	path := "/projects/2001/issues/3/award_emoji"
	h := newHarness(t, harnessOptions{})
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: path, AfterApply: true, Status: http.StatusBadGateway,
		Body: `{"message":"502 Bad Gateway"}`})
	text, out := h.ok("react", map[string]any{"project": alpha, "type": "issue", "iid": 3, "emoji": "rocket"})
	if get(out, "outcome") != "added" || !strings.Contains(text, "answer was lost, and a read shows") || awardWrites(h) != 1 {
		t.Errorf("landed: %v\n%s\n%d sent", out, text, awardWrites(h))
	}

	h = newHarness(t, harnessOptions{})
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: path, Status: http.StatusBadGateway, Body: `{"message":"502 Bad Gateway"}`})
	text = h.fails("react", map[string]any{"project": alpha, "type": "issue", "iid": 3, "emoji": "rocket"}, "ambiguous_outcome")
	if !strings.Contains(text, "a read shows it was not created") || awardWrites(h) != 1 || len(h.gl.Reactions(alpha, "issue", 3, 0)) != 0 {
		t.Errorf("not landed: %s", text)
	}

	// An alias GitLab keeps under another name is settled by the new
	// reaction, not by the name given.
	h = newHarness(t, harnessOptions{})
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: path, AfterApply: true, Status: http.StatusBadGateway,
		Body: `{"message":"502 Bad Gateway"}`})
	if _, out = h.ok("react", map[string]any{"project": alpha, "type": "issue", "iid": 3, "emoji": "thumbs_up"}); get(out, "outcome") != "added" ||
		get(out, "emoji") != "thumbsup" {
		t.Errorf("lost add of an alias: %v", out)
	}

	h = newHarness(t, harnessOptions{})
	h.ok("react", map[string]any{"project": alpha, "type": "issue", "iid": 3, "emoji": "rocket"})
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodDelete, Path: path + "/", AfterApply: true, Status: http.StatusBadGateway,
		Body: `{"message":"502 Bad Gateway"}`})
	text, out = h.ok("react", map[string]any{"project": alpha, "type": "issue", "iid": 3, "emoji": "rocket", "remove": true})
	if get(out, "outcome") != "removed" || !strings.Contains(text, "this call, or its repeat, removed it") {
		t.Errorf("lost remove: %v\n%s", out, text)
	}

	// A remove GitLab never confirmed, and a read back that fails too, is
	// unknown, not a failure of the call.
	h = newHarness(t, harnessOptions{})
	h.ok("react", map[string]any{"project": alpha, "type": "issue", "iid": 3, "emoji": "rocket"})
	for _, m := range []string{http.MethodDelete, http.MethodGet} {
		h.gl.Inject(gitlabtest.Fault{Method: m, Path: path + "/", Times: 10, Status: http.StatusBadGateway, Body: `{"message":"502 Bad Gateway"}`})
	}
	text = h.fails("react", map[string]any{"project": alpha, "type": "issue", "iid": 3, "emoji": "rocket", "remove": true}, "ambiguous_outcome")
	if !strings.Contains(text, "whether it is gone is unknown") {
		t.Errorf("unconfirmed remove: %s", text)
	}
}

// A reaction is held to the allow-list by the item's project.
func TestReactionsAreHeldToTheAllowList(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: config.Config{WriteNamespaces: []string{gitlabtest.GroupSub}}})
	sent := writesSent(h)
	for _, dry := range []bool{false, true} {
		h.fails("react", map[string]any{"project": alpha, "type": "issue", "iid": 1, "emoji": "tada", "dry_run": dry}, "blocked")
	}
	if writesSent(h) != sent {
		t.Error("a reaction outside the allow-list was sent")
	}
}

// GitLab answers a removal 204 even when its service refused it; the
// read afterwards says so.
func TestARemovalGitLabDidNotMakeIsReported(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.ok("react", map[string]any{"project": alpha, "type": "issue", "iid": 3, "emoji": "rocket"})
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodDelete, Path: "/projects/2001/issues/3/award_emoji/", Status: http.StatusNoContent})
	text := h.fails("react", map[string]any{"project": alpha, "type": "issue", "iid": 3, "emoji": "rocket", "remove": true}, "unexpected")
	if !strings.Contains(text, "your rocket reaction is still there") {
		t.Errorf("result: %s", text)
	}
}
