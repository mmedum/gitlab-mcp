package gitlabtest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// send makes a JSON request and decodes an object answer.
func send(t *testing.T, s *Server, method, path, token string, v any) (*http.Response, map[string]any) {
	t.Helper()
	var raw []byte
	if v != nil {
		var err error
		if raw, err = json.Marshal(v); err != nil {
			t.Fatal(err)
		}
	}
	return do(t, method, s.URL+"/api/v4"+path, token, bytes.NewReader(raw), "application/json")
}

type obj = map[string]any

// frozen is an instance on a clock the test moves.
func frozen(t *testing.T) (*Server, *time.Time) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	s := New(t, Options{Now: func() time.Time { return now }, AccessTokenTTL: 1000 * time.Hour})
	return s, &now
}

func wantStatus(t *testing.T, what string, resp *http.Response, body map[string]any, status int) {
	t.Helper()
	if resp.StatusCode != status {
		t.Fatalf("%s = %d %v, want %d", what, resp.StatusCode, body, status)
	}
}

func wantError(t *testing.T, what string, resp *http.Response, body map[string]any, status int, key, contains string) {
	t.Helper()
	raw, _ := json.Marshal(body[key])
	if resp.StatusCode != status || !strings.Contains(string(raw), contains) {
		t.Errorf("%s = %d %v, want %d with %s containing %q", what, resp.StatusCode, body, status, key, contains)
	}
}

func TestEveryWriteNeedsTheAPIScope(t *testing.T) {
	s := New(t, Options{})
	ro := s.TokenFor("alice", "read_api")
	for _, c := range []struct{ method, path string }{
		{"POST", "/projects/2001/issues"}, {"PUT", "/projects/2001/issues/1"},
		{"DELETE", "/projects/2001/merge_requests/1/draft_notes/80001"}, {"POST", "/todos/95001/mark_as_done"},
		{"POST", "/projects/2001/ci/lint"},
	} {
		resp, body := send(t, s, c.method, c.path, ro, obj{"title": "x", "content": "x"})
		if resp.StatusCode != 403 || body["error"] != "insufficient_scope" || resp.Header.Get("WWW-Authenticate") == "" {
			t.Errorf("%s %s = %d %v", c.method, c.path, resp.StatusCode, body)
		}
	}
	if iss, _ := s.Issue(ProjectAlpha, 1); iss.State != "opened" {
		t.Error("a read-only token changed an issue")
	}
}

func TestWritesToAnUnseenProjectAre404(t *testing.T) {
	s := New(t, Options{})
	tok := s.Token()
	for _, path := range []string{"/projects/example-group%2Fsecret/issues", "/projects/2003/merge_requests",
		"/projects/2003/repository/branches", "/projects/2003/issues/1/notes"} {
		resp, body := send(t, s, "POST", path, tok, obj{"title": "x", "body": "x"})
		if resp.StatusCode != 404 || body["message"] != "404 Project Not Found" {
			t.Errorf("%s = %d %v", path, resp.StatusCode, body)
		}
	}
	resp, _ := send(t, s, "POST", "/projects/example-group%2Fold-alpha/issues", tok, obj{"title": "x"})
	if resp.StatusCode != 405 {
		t.Errorf("moved project = %d", resp.StatusCode)
	}
	resp, body := send(t, s, "PATCH", "/projects/2001/issues", tok, obj{})
	if resp.StatusCode != 404 || body["error"] != "404 Not Found" {
		t.Errorf("unrouted method = %d %v", resp.StatusCode, body)
	}
	resp, body = send(t, s, "PUT", "/projects/2001/merge_requests/99", tok, obj{"title": "x"})
	if resp.StatusCode != 404 || body["message"] != "404 Merge Request Not Found" {
		t.Errorf("unknown MR = %d %v", resp.StatusCode, body)
	}
	resp, _ = send(t, s, "POST", "/projects/2001/issues", tok, nil)
	if resp.StatusCode != 400 {
		t.Errorf("empty body = %d", resp.StatusCode)
	}
	resp, body = do(t, "POST", s.URL+"/api/v4/projects/2001/issues", tok, strings.NewReader("{not json"), "application/json")
	if resp.StatusCode != 400 || body["error"] == nil {
		t.Errorf("bad JSON = %d %v", resp.StatusCode, body)
	}
}

func TestCreateIssue(t *testing.T) {
	s, now := frozen(t)
	tok := s.Token()
	resp, body := send(t, s, "POST", "/projects/2001/issues", tok, obj{
		"title": "A new one", "description": "Body text.\n/label ~\"needs review\" ~bug\n/close",
		"labels": []string{"docs", "fresh"}, "assignee_ids": []int{1002, 1002, 9999}, "milestone_id": MilestoneActive,
		"due_date": "2026-10-01", "confidential": true,
	})
	wantStatus(t, "create", resp, body, 201)
	if body["iid"] != float64(26) || body["description"] != "Body text." || body["state"] != "closed" ||
		body["confidential"] != true || body["due_date"] != "2026-10-01" || body["author"].(obj)["username"] != "alice" ||
		body["created_at"] != now.Format(time.RFC3339) {
		t.Errorf("created = %v", body)
	}
	iss, _ := s.Issue(ProjectAlpha, 26)
	if !slices.Equal(iss.Labels, []string{"docs", "fresh", "needs review", "bug"}) || len(iss.Assignees) != 1 ||
		iss.Assignees[0].Username != "bob" || iss.Milestone == nil || iss.Milestone.ID != MilestoneActive || iss.ClosedBy == nil {
		t.Errorf("stored = %+v", iss)
	}
	resp, _ = send(t, s, "GET", "/projects/2001/labels?search=fresh", tok, nil)
	if resp.Header.Get("X-Total") != "1" {
		t.Errorf("an assigned label was not created: %v", resp.Header)
	}
	resp, body = send(t, s, "POST", "/projects/2001/issues", tok, obj{"title": "Group milestone", "labels": "a, b",
		"milestone_id": MilestoneGroup})
	wantStatus(t, "comma labels", resp, body, 201)
	if iss, _ := s.Issue(ProjectAlpha, 27); !slices.Equal(iss.Labels, []string{"a", "b"}) || iss.Milestone == nil {
		t.Errorf("stored = %+v", iss)
	}

	resp, body = send(t, s, "POST", "/projects/2001/issues", tok, obj{"description": "no title"})
	wantError(t, "missing title", resp, body, 400, "error", "title is missing")
	resp, body = send(t, s, "POST", "/projects/2001/issues", tok, obj{"title": "  "})
	wantError(t, "blank title", resp, body, 400, "error", "title is missing")
	resp, body = send(t, s, "POST", "/projects/2001/issues", tok, obj{"title": "x", "due_date": "tomorrow"})
	wantError(t, "bad date", resp, body, 400, "error", "due_date is invalid")
	resp, body = send(t, s, "POST", "/projects/2001/issues", tok, obj{"title": "x", "state_event": "archive"})
	wantError(t, "bad state_event", resp, body, 400, "error", "state_event does not have a valid value")
}

func TestUpdateIssue(t *testing.T) {
	s, now := frozen(t)
	tok := s.Token()
	before, _ := s.Issue(ProjectAlpha, 2)

	resp, body := send(t, s, "PUT", "/projects/2001/issues/2", tok, obj{"title": before.Title, "labels": before.Labels})
	wantStatus(t, "no-op", resp, body, 200)
	if after, _ := s.Issue(ProjectAlpha, 2); !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("a no-op moved updated_at: %v → %v", before.UpdatedAt, after.UpdatedAt)
	}
	resp, body = send(t, s, "PUT", "/projects/2001/issues/2", tok, obj{"not_a_field": 1})
	wantStatus(t, "unknown field", resp, body, 200)
	resp, body = send(t, s, "PUT", "/projects/2001/issues/2", tok, obj{})
	wantError(t, "nothing sent", resp, body, 400, "error", "at least one parameter must be provided")
	resp, body = send(t, s, "PUT", "/projects/2001/issues/2", tok, obj{"title": ""})
	wantError(t, "blank title", resp, body, 400, "message", "can't be blank")

	resp, body = send(t, s, "PUT", "/projects/2001/issues/2", tok, obj{"add_labels": []string{"docs", "feature"},
		"remove_labels": []string{"priority::high"}, "state_event": "close", "due_date": "2026-11-02",
		"milestone_id": MilestoneActive, "assignee_ids": []int{}, "description": "Rewritten.\n/label ~triaged"})
	wantStatus(t, "update", resp, body, 200)
	after, _ := s.Issue(ProjectAlpha, 2)
	if !slices.Equal(after.Labels, []string{"feature", "docs", "triaged"}) || after.State != "closed" || after.ClosedBy == nil ||
		after.DueDate != "2026-11-02" || after.Milestone == nil || len(after.Assignees) != 0 || after.Description != "Rewritten." {
		t.Errorf("updated = %+v", after)
	}
	if !after.UpdatedAt.Equal(*now) {
		t.Errorf("updated_at = %v, want %v", after.UpdatedAt, *now)
	}
	// A second change on the same clock still moves updated_at.
	resp, body = send(t, s, "PUT", "/projects/2001/issues/2", tok, obj{"state_event": "reopen", "due_date": "",
		"milestone_id": 0, "labels": []string{"bug"}})
	wantStatus(t, "reopen", resp, body, 200)
	again, _ := s.Issue(ProjectAlpha, 2)
	if !again.UpdatedAt.After(after.UpdatedAt) || again.State != "opened" || again.ClosedAt != nil || again.DueDate != "" ||
		again.Milestone != nil || !slices.Equal(again.Labels, []string{"bug"}) {
		t.Errorf("reopened = %+v", again)
	}
}

func TestNotesAndRepliesOnBothKinds(t *testing.T) {
	s := New(t, Options{})
	tok := s.Token()
	resp, body := send(t, s, "POST", "/projects/2001/merge_requests/1/notes", tok, obj{"body": "Looks good."})
	wantStatus(t, "MR note", resp, body, 201)
	if body["noteable_type"] != "MergeRequest" || body["body"] != "Looks good." { //nolint:misspell // GitLab's wire name
		t.Errorf("note = %v", body)
	}
	resp, body = send(t, s, "POST", "/projects/2001/merge_requests/1/notes", tok, obj{"body": "/label ~applied\n/close"})
	wantStatus(t, "MR command note", resp, body, 202)
	mr, _ := s.MergeRequest(ProjectAlpha, 1)
	if mr.State != "closed" || !slices.Contains(mr.Labels, "applied") || mr.UserNotesCount != 1 {
		t.Errorf("MR after commands = %+v", mr)
	}
	resp, body = send(t, s, "POST", "/projects/2001/merge_requests/1/notes", tok, obj{"body": "/reopen\n/unlabel ~applied"})
	wantStatus(t, "MR reopen", resp, body, 202)
	if mr, _ := s.MergeRequest(ProjectAlpha, 1); mr.State != "opened" || slices.Contains(mr.Labels, "applied") {
		t.Errorf("MR after reopen = %+v", mr)
	}
	resp, body = send(t, s, "POST", "/projects/2001/merge_requests/1/notes", tok, obj{})
	wantError(t, "MR note without body", resp, body, 400, "error", "body is missing")

	issueThread := fakeSHA("issue:1", "thread")
	resp, body = send(t, s, "POST", "/projects/2001/issues/1/discussions/"+issueThread+"/notes", tok, obj{"body": "Me too."})
	wantStatus(t, "issue reply", resp, body, 201)
	if ds := s.Discussions(ProjectAlpha, "issue", 1); len(ds[0].Notes) != 3 || ds[0].Notes[2].Body != "Me too." {
		t.Errorf("issue thread = %+v", ds[0])
	}
	resp, body = send(t, s, "POST", "/projects/2001/issues/1/discussions/"+issueThread+"/notes", tok, obj{"body": "/close"})
	wantStatus(t, "command reply", resp, body, 202)
	resp, body = send(t, s, "POST", "/projects/2001/issues/1/discussions/"+issueThread+"/notes", tok, obj{})
	wantError(t, "reply without body", resp, body, 400, "error", "body is missing")
	resp, body = send(t, s, "POST", "/projects/2001/issues/1/discussions/nope/notes", tok, obj{"body": "x"})
	wantError(t, "unknown thread", resp, body, 404, "message", "404 Discussion Not Found")

	mrThread := fakeSHA("mr:1", "thread")
	resp, body = send(t, s, "POST", "/projects/2001/merge_requests/1/discussions/"+mrThread+"/notes", tok, obj{"body": "Fixed."})
	wantStatus(t, "MR reply", resp, body, 201)
	if body["resolvable"] != true || body["type"] != "DiffNote" {
		t.Errorf("MR reply = %v", body)
	}
}

// A planted comment is stored as written: its quick action does not run.
func TestCommentPlantsContentWithoutRunningIt(t *testing.T) {
	s := New(t, Options{})
	for _, kind := range []string{"issue", "mr"} {
		note, ok := s.Comment(ProjectAlpha, kind, 1, "dave", "Please do this.\n/close")
		if !ok || note.Author.Username != "dave" {
			t.Fatalf("%s: Comment = %+v, %v", kind, note, ok)
		}
		ds := s.Discussions(ProjectAlpha, kind, 1)
		if last := ds[len(ds)-1].Notes[0]; last.ID != note.ID || last.Body != "Please do this.\n/close" {
			t.Errorf("%s: last note = %+v", kind, last)
		}
	}
	if iss, _ := s.Issue(ProjectAlpha, 1); iss.State != "opened" {
		t.Errorf("the planted /close ran: issue %s", iss.State)
	}
	if mr, _ := s.MergeRequest(ProjectAlpha, 1); mr.State != "opened" {
		t.Errorf("the planted /close ran: merge request %s", mr.State)
	}
	for _, c := range []struct{ project, kind string }{{ProjectAlpha, "epic"}, {ProjectAlpha + "x", "issue"}} {
		if _, ok := s.Comment(c.project, c.kind, 1, "dave", "x"); ok {
			t.Errorf("Comment(%s, %s) planted a comment", c.project, c.kind)
		}
	}
	if _, ok := s.Comment(ProjectAlpha, "issue", 999, "dave", "x"); ok {
		t.Error("Comment on a missing issue planted a comment")
	}
}

// loginPos is a position on ProjectAlpha's merge request 1.
func loginPos(s *Server, path string, oldLine, newLine int) obj {
	mr, _ := s.MergeRequest(ProjectAlpha, 1)
	pos := obj{"base_sha": mr.DiffRefs.BaseSHA, "start_sha": mr.DiffRefs.StartSHA, "head_sha": mr.DiffRefs.HeadSHA,
		"position_type": "text", "old_path": path, "new_path": path}
	if oldLine > 0 {
		pos["old_line"] = oldLine
	}
	if newLine > 0 {
		pos["new_line"] = newLine
	}
	return pos
}

func TestCreateDiscussion(t *testing.T) {
	s := New(t, Options{})
	tok := s.Token()
	path := "/projects/2001/merge_requests/1/discussions"
	resp, body := send(t, s, "POST", path, tok, obj{"body": "A general thread."})
	wantStatus(t, "general", resp, body, 201)
	note := body["notes"].([]any)[0].(obj)
	if body["individual_note"] != false || note["type"] != "DiscussionNote" || note["resolvable"] != true || note["position"] != nil {
		t.Errorf("general = %v", body)
	}

	pos := loginPos(s, "src/login.go", 0, 3)
	pos["line_range"] = obj{"start": obj{"line_code": "x_0_2", "type": "new", "new_line": 2},
		"end": obj{"line_code": "x_0_3", "type": "new", "new_line": 3}}
	resp, body = send(t, s, "POST", path, tok, obj{"body": "On a line.", "position": pos})
	wantStatus(t, "diff", resp, body, 201)
	note = body["notes"].([]any)[0].(obj)
	np := note["position"].(obj)
	if note["type"] != "DiffNote" || np["new_line"] != float64(3) || np["line_range"].(obj)["start"].(obj)["new_line"] != float64(2) {
		t.Errorf("diff = %v", body)
	}

	for _, c := range []struct {
		name string
		pos  obj
	}{
		{"removed line", loginPos(s, "README.md", 3, 0)},
		{"context line by both", loginPos(s, "README.md", 1, 1)},
		{"context line by new", loginPos(s, "README.md", 0, 2)},
	} {
		resp, body = send(t, s, "POST", path, tok, obj{"body": "x", "position": c.pos})
		wantStatus(t, c.name, resp, body, 201)
	}
	file := loginPos(s, "src/login.go", 0, 0)
	file["position_type"] = "file"
	resp, body = send(t, s, "POST", path, tok, obj{"body": "On the file.", "position": file})
	wantStatus(t, "file", resp, body, 201)

	stale := loginPos(s, "src/login.go", 0, 3)
	stale["head_sha"] = strings.Repeat("0", 40)
	resp, body = send(t, s, "POST", path, tok, obj{"body": "x", "position": stale})
	wantError(t, "stale refs", resp, body, 400, "message", "The diff position is not valid")
	for _, c := range []struct {
		name string
		pos  obj
	}{
		{"line past the diff", loginPos(s, "src/login.go", 0, 9)},
		{"added line as old", loginPos(s, "src/login.go", 2, 0)},
		{"context pair mismatched", loginPos(s, "README.md", 1, 2)},
		{"file not in diff", loginPos(s, "src/main.go", 0, 1)},
		{"no line", loginPos(s, "src/login.go", 0, 0)},
		{"renamed without text", loginPos(s, "docs/new-name.md", 0, 1)},
	} {
		resp, body = send(t, s, "POST", path, tok, obj{"body": "x", "position": c.pos})
		wantError(t, c.name, resp, body, 400, "message", "must be a valid line code")
	}
	missing := loginPos(s, "src/login.go", 0, 3)
	delete(missing, "base_sha")
	resp, body = send(t, s, "POST", path, tok, obj{"body": "x", "position": missing})
	wantError(t, "missing sha", resp, body, 400, "error", "position[base_sha] is missing")
	image := loginPos(s, "src/login.go", 0, 3)
	image["position_type"] = "image"
	resp, body = send(t, s, "POST", path, tok, obj{"body": "x", "position": image})
	wantError(t, "image", resp, body, 400, "message", "is invalid")
	file["new_path"], file["old_path"] = "nowhere.go", "nowhere.go"
	resp, body = send(t, s, "POST", path, tok, obj{"body": "x", "position": file})
	wantError(t, "file not in diff", resp, body, 400, "message", "is invalid")
	resp, body = send(t, s, "POST", path, tok, obj{"body": "x", "position": "line 3"})
	wantError(t, "position not an object", resp, body, 400, "error", "position is invalid")
	resp, body = send(t, s, "POST", path, tok, obj{"position": loginPos(s, "src/login.go", 0, 3)})
	wantError(t, "no body", resp, body, 400, "error", "body is missing")
	resp, body = send(t, s, "POST", path, tok, obj{"body": "/label ~from-thread"})
	wantStatus(t, "command thread", resp, body, 202)
	if mr, _ := s.MergeRequest(ProjectAlpha, 1); !slices.Contains(mr.Labels, "from-thread") {
		t.Errorf("a thread's quick action did not run: %v", mr.Labels)
	}
}

func TestResolveDiscussion(t *testing.T) {
	s := New(t, Options{})
	tok := s.Token()
	thread := "/projects/2001/merge_requests/1/discussions/" + fakeSHA("mr:1", "thread")
	resp, body := send(t, s, "PUT", thread, tok, obj{"resolved": true})
	wantStatus(t, "resolve", resp, body, 200)
	for _, n := range body["notes"].([]any) {
		if n := n.(obj); n["resolved"] != true || n["resolved_by"].(obj)["username"] != "alice" || n["resolved_at"] == nil {
			t.Errorf("note = %v", n)
		}
	}
	resp, body = send(t, s, "PUT", thread, tok, obj{"resolved": false})
	wantStatus(t, "reopen", resp, body, 200)
	if n := body["notes"].([]any)[0].(obj); n["resolved"] != false || n["resolved_by"] != nil {
		t.Errorf("note = %v", n)
	}
	if ds := s.Discussions(ProjectAlpha, "mr", 1); ds[0].Notes[0].Resolved {
		t.Error("the stored thread is still resolved")
	}
	resp, body = send(t, s, "PUT", thread, tok, obj{})
	wantError(t, "missing", resp, body, 400, "error", "resolved is missing")
	resp, body = send(t, s, "PUT", thread, tok, obj{"resolved": "maybe"})
	wantError(t, "not a bool", resp, body, 400, "error", "resolved is invalid")
	resp, body = send(t, s, "PUT", "/projects/2001/merge_requests/1/discussions/"+fakeSHA("mr:1", "single"), tok, obj{"resolved": true})
	wantError(t, "not resolvable", resp, body, 400, "message", "not resolvable")
	resp, body = send(t, s, "PUT", "/projects/2001/merge_requests/1/discussions/nope", tok, obj{"resolved": true})
	wantError(t, "unknown", resp, body, 404, "message", "404 Discussion Not Found")
}

func TestDraftsAndPublishing(t *testing.T) {
	s := New(t, Options{})
	tok := s.Token()
	path := "/projects/2001/merge_requests/1/draft_notes"
	resp, body := send(t, s, "POST", path, tok, obj{"note": "Rename this.", "position": loginPos(s, "src/login.go", 0, 4)})
	wantStatus(t, "draft", resp, body, 201)
	id := int64(body["id"].(float64))
	if id < firstDraftID+30 || body["author_id"] != float64(1001) || body["merge_request_id"] != float64(40001) ||
		!strings.HasSuffix(body["line_code"].(string), "_0_4") {
		t.Errorf("draft = %v", body)
	}
	resp, body = send(t, s, "POST", path, tok, obj{"note": "/label ~published\nApplies at publish.", "commit_id": "abc"})
	wantStatus(t, "general draft", resp, body, 201)
	if int64(body["id"].(float64)) != id+1 || body["line_code"] != nil || body["commit_id"] != "abc" {
		t.Errorf("second draft = %v", body)
	}
	if mr, _ := s.MergeRequest(ProjectAlpha, 1); slices.Contains(mr.Labels, "published") {
		t.Error("a draft ran its quick action at creation")
	}
	resp, body = send(t, s, "POST", path, tok, obj{"note": "x", "position": loginPos(s, "src/login.go", 0, 40)})
	wantError(t, "bad line", resp, body, 400, "message", "must be a valid line code")
	resp, body = send(t, s, "POST", path, tok, obj{"note": "x", "position": 7})
	wantError(t, "bad position", resp, body, 400, "error", "position is invalid")
	resp, body = send(t, s, "POST", path, tok, obj{"note": "x", "in_reply_to_discussion_id": "nope"})
	wantError(t, "unknown thread", resp, body, 404, "message", "404 Discussion Not Found")
	resp, body = send(t, s, "POST", path, tok, obj{})
	wantError(t, "no note", resp, body, 400, "error", "note is missing")

	// bob writes one draft; alice cannot delete it, and publishing
	// alice's review leaves it.
	bob := s.TokenFor("bob", "api")
	resp, body = send(t, s, "POST", path, bob, obj{"note": "Bob's thought."})
	wantStatus(t, "bob's draft", resp, body, 201)
	bobs := int64(body["id"].(float64))
	resp, body = send(t, s, "DELETE", path+"/"+itoa(bobs), tok, nil)
	wantError(t, "another's draft", resp, body, 404, "message", "404 Not Found")
	resp, body = send(t, s, "DELETE", path+"/99", tok, nil)
	wantError(t, "unknown draft", resp, body, 404, "message", "404 Not Found")
	resp, _ = send(t, s, "DELETE", path+"/"+itoa(id+1), tok, nil)
	if resp.StatusCode != 204 {
		t.Errorf("delete = %d", resp.StatusCode)
	}
	resp, body = send(t, s, "POST", path, tok, obj{"note": "/label ~published\nApplies at publish."})
	wantStatus(t, "redraft", resp, body, 201)

	resp, body = send(t, s, "POST", path+"/bulk_publish", tok, obj{"reviewer_state": "done"})
	wantError(t, "bad state", resp, body, 400, "error", "reviewer_state does not have a valid value")
	before := len(s.Discussions(ProjectAlpha, "mr", 1))
	resp, _ = send(t, s, "POST", path+"/bulk_publish", tok, obj{"note": "Summary.", "reviewer_state": "approved"})
	if resp.StatusCode != 204 {
		t.Fatalf("publish = %d", resp.StatusCode)
	}
	ds := s.Discussions(ProjectAlpha, "mr", 1)
	// Two fixture drafts (a reply and a diff note) and two of the test's
	// became one reply, three new threads and the summary.
	if len(ds) != before+4 {
		t.Errorf("discussions %d → %d", before, len(ds))
	}
	thread := ds[0]
	if last := thread.Notes[len(thread.Notes)-1]; last.Body != "Agreed; resolving." || !thread.Notes[0].Resolved {
		t.Errorf("reply = %+v", thread)
	}
	var diffNotes int
	for _, d := range ds[before:] {
		if d.Notes[0].Type != nil && *d.Notes[0].Type == "DiffNote" {
			diffNotes++
		}
	}
	if diffNotes != 2 {
		t.Errorf("diff threads = %d", diffNotes)
	}
	if last := ds[len(ds)-1]; !last.IndividualNote || last.Notes[0].Body != "Summary." {
		t.Errorf("summary = %+v", last)
	}
	mr, _ := s.MergeRequest(ProjectAlpha, 1)
	if !slices.Contains(mr.Labels, "published") {
		t.Error("a published draft's quick action did not run")
	}
	if got := s.ReviewerState(ProjectAlpha, 1, "alice"); got != "approved" {
		t.Errorf("reviewer state = %q", got)
	}
	resp, body = send(t, s, "GET", "/projects/2001/merge_requests/1/approvals", tok, nil)
	if raw, _ := json.Marshal(body["approved_by"]); resp.StatusCode != 200 || !strings.Contains(string(raw), `"alice"`) {
		t.Errorf("approvals = %v", body)
	}
	drafts := s.Drafts(ProjectAlpha, 1)
	if len(drafts) != 1 || drafts[0].ID != bobs {
		t.Errorf("drafts left = %+v", drafts)
	}
	resp, _ = send(t, s, "POST", path+"/bulk_publish", tok, nil)
	if resp.StatusCode != 204 {
		t.Errorf("empty publish = %d", resp.StatusCode)
	}
	resp, _ = send(t, s, "POST", "/projects/2001/merge_requests/2/draft_notes/bulk_publish", bob, obj{"reviewer_state": "requested_changes"})
	if resp.StatusCode != 204 || s.ReviewerState(ProjectAlpha, 2, "bob") != "requested_changes" || s.ReviewerState(ProjectAlpha, 2, "alice") != "" {
		t.Errorf("bob's review = %d", resp.StatusCode)
	}
}

func TestCreateMergeRequest(t *testing.T) {
	s := New(t, Options{})
	tok := s.Token()
	resp, body := send(t, s, "POST", "/projects/2001/repository/commits", tok, obj{"branch": "topic", "start_branch": "main",
		"commit_message": "Change the guide", "actions": []obj{
			{"action": "update", "file_path": "docs/guide.md", "content": "# Guide\n\nRead me first.\n"},
			{"action": "create", "file_path": "docs/new.md", "content": "new\n"},
		}})
	wantStatus(t, "commit", resp, body, 201)
	head := body["id"].(string)

	resp, body = send(t, s, "POST", "/projects/2001/merge_requests", tok, obj{"source_branch": "topic", "target_branch": "main",
		"title": "Draft: guide", "description": "Why.\n/label ~ready", "assignee_ids": []int{1001}, "reviewer_ids": []int{1003},
		"milestone_id": MilestoneActive, "remove_source_branch": true, "squash": true, "labels": []string{"docs"}})
	wantStatus(t, "create", resp, body, 201)
	main, _ := s.BranchHead(ProjectAlpha, "main")
	refs := body["diff_refs"].(obj)
	if body["iid"] != float64(4) || body["draft"] != true || body["sha"] != head || refs["head_sha"] != head ||
		refs["base_sha"] != main.ID || body["detailed_merge_status"] != "mergeable" || body["description"] != "Why." ||
		body["force_remove_source_branch"] != true || body["squash"] != true || body["changes_count"] != "2" {
		t.Errorf("created = %v", body)
	}
	mr, _ := s.MergeRequest(ProjectAlpha, 4)
	if !slices.Equal(mr.Labels, []string{"docs", "ready"}) || mr.Reviewers[0].Username != "carol" || mr.Milestone == nil {
		t.Errorf("stored = %+v", mr)
	}
	resp, _ = send(t, s, "GET", "/projects/2001/merge_requests/4/approvals", tok, nil)
	if resp.StatusCode != 200 {
		t.Errorf("approvals of a new MR = %d", resp.StatusCode)
	}
	// The generated diff is consistent with the branches: its lines take
	// a position.
	resp, body = send(t, s, "POST", "/projects/2001/merge_requests/4/discussions", tok, obj{"body": "Nice.", "position": obj{
		"base_sha": main.ID, "start_sha": main.ID, "head_sha": head, "position_type": "text",
		"old_path": "docs/guide.md", "new_path": "docs/guide.md", "new_line": 3}})
	wantStatus(t, "position on a generated diff", resp, body, 201)

	for _, title := range []string{"[Draft] x", "(draft) x", "draft: x"} {
		if !draftTitle.MatchString(title) {
			t.Errorf("%q is not a draft title", title)
		}
	}
	resp, body = send(t, s, "POST", "/projects/2001/merge_requests", tok, obj{"source_branch": "topic", "target_branch": "main", "title": "Again"})
	wantError(t, "duplicate", resp, body, 409, "message", "Another open merge request already exists for this source branch: !4")
	resp, body = send(t, s, "POST", "/projects/2001/merge_requests", tok, obj{"title": "x"})
	wantError(t, "missing", resp, body, 400, "error", "source_branch is missing, target_branch is missing")
	resp, body = send(t, s, "POST", "/projects/2001/merge_requests", tok, obj{"source_branch": "nope", "target_branch": "main", "title": "x"})
	wantError(t, "unknown source", resp, body, 400, "message", `Source branch \"nope\" does not exist`)
	resp, body = send(t, s, "POST", "/projects/2001/merge_requests", tok, obj{"source_branch": "topic", "target_branch": "gone", "title": "x"})
	wantError(t, "unknown target", resp, body, 400, "message", `Target branch \"gone\" does not exist`)
	resp, body = send(t, s, "POST", "/projects/2001/merge_requests", tok, obj{"source_branch": "main", "target_branch": "main", "title": "x"})
	wantError(t, "same branch", resp, body, 400, "message", "same project/branch")
	resp, body = send(t, s, "POST", "/projects/2001/merge_requests", tok, obj{"source_branch": "topic", "target_branch": "release/1.0",
		"title": "x", "state_event": "merge"})
	wantError(t, "bad state_event", resp, body, 400, "error", "state_event does not have a valid value")
}

func TestUpdateMergeRequest(t *testing.T) {
	s, _ := frozen(t)
	tok := s.Token()
	before, _ := s.MergeRequest(ProjectAlpha, 2)
	resp, body := send(t, s, "PUT", "/projects/2001/merge_requests/2", tok, obj{"title": before.Title, "squash": false})
	wantStatus(t, "no-op", resp, body, 200)
	if mr, _ := s.MergeRequest(ProjectAlpha, 2); !mr.UpdatedAt.Equal(before.UpdatedAt) {
		t.Error("a no-op moved updated_at")
	}
	resp, body = send(t, s, "PUT", "/projects/2001/merge_requests/2", tok, obj{"title": "Draft: rework", "reviewer_ids": []int{1002},
		"remove_labels": []string{"feature"}, "add_labels": []string{"docs"}, "remove_source_branch": true, "state_event": "close",
		"target_branch": "release/1.0"})
	wantStatus(t, "update", resp, body, 200)
	mr, _ := s.MergeRequest(ProjectAlpha, 2)
	rel, _ := s.BranchHead(ProjectAlpha, "release/1.0")
	if !mr.Draft || mr.Reviewers[0].Username != "bob" || !slices.Equal(mr.Labels, []string{"docs"}) || !mr.ForceRemoveSourceBranch ||
		mr.State != "closed" || mr.ClosedAt == nil || mr.TargetBranch != "release/1.0" || mr.DiffRefs.BaseSHA != rel.ID ||
		!mr.UpdatedAt.After(before.UpdatedAt) {
		t.Errorf("updated = %+v", mr)
	}
	resp, body = send(t, s, "PUT", "/projects/2001/merge_requests/2", tok, obj{"title": "Ready now", "state_event": "reopen"})
	wantStatus(t, "reopen", resp, body, 200)
	if body["draft"] != false || body["state"] != "opened" {
		t.Errorf("reopened = %v", body)
	}
	resp, body = send(t, s, "PUT", "/projects/2001/merge_requests/2", tok, obj{"target_branch": "gone"})
	wantError(t, "unknown target", resp, body, 400, "message", "does not exist")
	resp, body = send(t, s, "PUT", "/projects/2001/merge_requests/2", tok, obj{})
	wantError(t, "nothing sent", resp, body, 400, "error", "at least one parameter must be provided")
	resp, body = send(t, s, "PUT", "/projects/2001/merge_requests/2", tok, obj{"state_event": "merge"})
	wantError(t, "bad state_event", resp, body, 400, "error", "state_event does not have a valid value")
	resp, body = send(t, s, "PUT", "/projects/2001/merge_requests/2", tok, obj{"title": " "})
	wantError(t, "blank title", resp, body, 400, "message", "can't be blank")

	s.mu.Lock()
	s.projectByPath(ProjectAlpha).mrs[2].State = "merged"
	s.mu.Unlock()
	resp, body = send(t, s, "PUT", "/projects/2001/merge_requests/3", tok, obj{"state_event": "reopen"})
	wantError(t, "reopen merged", resp, body, 400, "message", "cannot be reopened")
}

func TestCreateBranch(t *testing.T) {
	s := New(t, Options{})
	tok := s.Token()
	main, _ := s.BranchHead(ProjectAlpha, "main")
	resp, body := send(t, s, "POST", "/projects/2001/repository/branches", tok, obj{"branch": "release/2.0", "ref": "main"})
	wantStatus(t, "create", resp, body, 201)
	if body["name"] != "release/2.0" || body["protected"] != true || body["commit"].(obj)["id"] != main.ID || body["default"] != false {
		t.Errorf("created = %v", body)
	}
	resp, body = send(t, s, "GET", "/projects/2001/repository/branches/release%2F2.0", tok, nil)
	wantStatus(t, "read back", resp, body, 200)
	resp, body = send(t, s, "GET", "/projects/2001/repository/branches/nope", tok, nil)
	wantError(t, "unknown", resp, body, 404, "message", "404 Branch Not Found")
	if c, ok := s.FileAt(ProjectAlpha, "release/2.0", "README.md"); !ok || !strings.HasPrefix(c, "# Alpha") {
		t.Errorf("tree not copied: %q", c)
	}
	resp, body = send(t, s, "POST", "/projects/2001/repository/branches", tok, obj{"branch": "from-tag", "ref": TagRelease})
	wantStatus(t, "from a tag", resp, body, 201)
	if body["protected"] != false {
		t.Errorf("from-tag = %v", body)
	}
	s.mu.Lock()
	older := s.projectByPath(ProjectAlpha).commits["main"][3].ID
	s.mu.Unlock()
	resp, body = send(t, s, "POST", "/projects/2001/repository/branches", tok, obj{"branch": "from-sha", "ref": older[:10]})
	wantStatus(t, "from an older commit", resp, body, 201)
	if body["commit"].(obj)["id"] != older {
		t.Errorf("from-sha = %v", body)
	}
	resp, body = send(t, s, "POST", "/projects/2001/repository/branches", tok, obj{"branch": "main", "ref": "main"})
	wantError(t, "exists", resp, body, 400, "message", "Branch already exists")
	resp, body = send(t, s, "POST", "/projects/2001/repository/branches", tok, obj{"branch": "x", "ref": "nope"})
	wantError(t, "bad ref", resp, body, 400, "message", "Invalid reference name: nope")
	resp, body = send(t, s, "POST", "/projects/2001/repository/branches", tok, obj{"branch": "x"})
	wantError(t, "no ref", resp, body, 400, "error", "ref is missing")
}

func TestProtectedFixture(t *testing.T) {
	s := New(t, Options{})
	s.mu.Lock()
	p := s.projectByPath(ProjectAlpha)
	got := []bool{protectedName(p, "main"), protectedName(p, "release/1.0"), protectedName(p, "feature/login"), protectedName(p, "release")}
	s.mu.Unlock()
	if !slices.Equal(got, []bool{true, true, false, false}) {
		t.Errorf("protected = %v", got)
	}
	for _, c := range []struct {
		pattern, name string
		want          bool
	}{{"*-stable", "1-0-stable", true}, {"a*b*c", "a-b-c", true}, {"a*b*c", "a-c", false}, {"x*", "y", false}} {
		if wildcard(c.pattern, c.name) != c.want {
			t.Errorf("wildcard(%q, %q) != %v", c.pattern, c.name, c.want)
		}
	}
}

func fileWitness(t *testing.T, s *Server, tok, path, ref string) string {
	t.Helper()
	resp, body := send(t, s, "GET", "/projects/2001/repository/files/"+strings.ReplaceAll(path, "/", "%2F")+"?ref="+ref, tok, nil)
	wantStatus(t, "read "+path, resp, body, 200)
	return body["last_commit_id"].(string)
}

func TestCreateCommit(t *testing.T) {
	s := New(t, Options{})
	tok := s.Token()
	path := "/projects/2001/repository/commits"
	witness := fileWitness(t, s, tok, "src/login.go", "feature/login")
	untouched := fileWitness(t, s, tok, "README.md", "feature/login")
	mrBefore, _ := s.MergeRequest(ProjectAlpha, 1)

	resp, body := send(t, s, "POST", path, tok, obj{"branch": "feature/login", "commit_message": "Rework login\n\nWhy.",
		"author_name": "ignored", "actions": []obj{
			{"action": "update", "file_path": "src/login.go", "content": "package main\n\n// login signs in.\nfunc login() {}\n",
				"last_commit_id": witness},
			{"action": "create", "file_path": "src/b64.txt", "content": base64.StdEncoding.EncodeToString([]byte("hi\n")), "encoding": "base64"},
			{"action": "delete", "file_path": "docs/pages/page-01.md"},
			{"action": "move", "previous_path": "docs/guide.md", "file_path": "docs/handbook.md"},
			{"action": "chmod", "file_path": "src/main.go"},
		}})
	wantStatus(t, "commit", resp, body, 201)
	sha := body["id"].(string)
	stats := body["stats"].(obj)
	if body["title"] != "Rework login" || body["author_email"] != "alice@example.com" || stats["additions"] != float64(2) ||
		stats["deletions"] != float64(2) || body["parent_ids"].([]any)[0] != mrBefore.SHA {
		t.Errorf("commit = %v", body)
	}
	if c, _ := s.FileAt(ProjectAlpha, "feature/login", "src/b64.txt"); c != "hi\n" {
		t.Errorf("base64 content = %q", c)
	}
	if _, ok := s.FileAt(ProjectAlpha, "feature/login", "docs/guide.md"); ok {
		t.Error("a moved file stayed")
	}
	if c, _ := s.FileAt(ProjectAlpha, "feature/login", "docs/handbook.md"); !strings.HasPrefix(c, "# Guide") {
		t.Errorf("moved content = %q", c)
	}
	if got := fileWitness(t, s, tok, "src/login.go", "feature/login"); got != sha {
		t.Errorf("touched file's last commit = %s, want %s", got, sha)
	}
	if got := fileWitness(t, s, tok, "README.md", "feature/login"); got != untouched {
		t.Errorf("untouched file's last commit moved: %s → %s", untouched, got)
	}
	if got := fileWitness(t, s, tok, "README.md", sha[:8]); got != untouched {
		t.Errorf("read by sha = %s", got)
	}
	resp, _ = send(t, s, "GET", "/projects/2001/repository/commits/"+sha+"/diff", tok, nil)
	if resp.StatusCode != 200 || resp.Header.Get("X-Total") != "5" {
		t.Errorf("commit diff = %d %v", resp.StatusCode, resp.Header.Get("X-Total"))
	}
	if mr, _ := s.MergeRequest(ProjectAlpha, 1); mr.SHA != sha || mr.DiffRefs.HeadSHA != sha {
		t.Errorf("the MR did not follow its source: %+v", mr.DiffRefs)
	}
	_, body = send(t, s, "GET", "/projects/2001/repository/branches/feature%2Flogin", tok, nil)
	if body["commit"].(obj)["id"] != sha {
		t.Errorf("branch head = %v", body)
	}

	// The old witness is now stale.
	resp, body = send(t, s, "POST", path, tok, obj{"branch": "feature/login", "commit_message": "x", "actions": []obj{
		{"action": "update", "file_path": "src/login.go", "content": "x", "last_commit_id": witness}}})
	wantError(t, "stale", resp, body, 400, "message", "has changed since you started editing it: src/login.go")
	// An untouched file's old witness still holds.
	resp, body = send(t, s, "POST", path, tok, obj{"branch": "feature/login", "commit_message": "x", "actions": []obj{
		{"action": "update", "file_path": "README.md", "content": "# Alpha\n", "last_commit_id": untouched}}})
	wantStatus(t, "untouched witness", resp, body, 201)

	head, _ := s.BranchHead(ProjectAlpha, "feature/login")
	for _, c := range []struct {
		name    string
		in      obj
		key     string
		message string
	}{
		{"create existing", obj{"action": "create", "file_path": "README.md", "content": "x"}, "message", "A file with this name already exists"},
		{"update missing", obj{"action": "update", "file_path": "nope.md", "content": "x"}, "message", "A file with this name doesn't exist"},
		{"delete missing", obj{"action": "delete", "file_path": "nope.md"}, "message", "A file with this name doesn't exist"},
		{"move missing", obj{"action": "move", "previous_path": "nope.md", "file_path": "b.md"}, "message", "A file with this name doesn't exist"},
		{"move onto a file", obj{"action": "move", "previous_path": "README.md", "file_path": "src/main.go"}, "message", "already exists"},
		{"move without previous", obj{"action": "move", "file_path": "b.md"}, "error", "actions[1][previous_path] is missing"},
		{"bad action", obj{"action": "rename", "file_path": "b.md"}, "error", "actions[1][action] does not have a valid value"},
		{"no action", obj{"file_path": "b.md"}, "error", "actions[1][action] is missing"},
		{"no path", obj{"action": "create"}, "error", "actions[1][file_path] is missing"},
		{"bad base64", obj{"action": "create", "file_path": "b.md", "content": "***", "encoding": "base64"}, "message", "Invalid base64"},
	} {
		// The first action is valid; the whole commit must still fail.
		resp, body = send(t, s, "POST", path, tok, obj{"branch": "feature/login", "commit_message": "x", "actions": []obj{
			{"action": "create", "file_path": "atomic.md", "content": "x"}, c.in}})
		wantError(t, c.name, resp, body, 400, c.key, c.message)
	}
	if _, ok := s.FileAt(ProjectAlpha, "feature/login", "atomic.md"); ok {
		t.Error("a refused commit applied an action")
	}
	if now, _ := s.BranchHead(ProjectAlpha, "feature/login"); now.ID != head.ID {
		t.Error("a refused commit moved the branch")
	}

	resp, body = send(t, s, "POST", path, tok, obj{"branch": "new-one", "commit_message": "x", "actions": []obj{
		{"action": "create", "file_path": "a.md", "content": "x"}}})
	wantError(t, "no branch", resp, body, 400, "message", "You can only create or edit files when you are on a branch")
	resp, body = send(t, s, "POST", path, tok, obj{"branch": "new-one", "start_branch": "nope", "commit_message": "x",
		"actions": []obj{{"action": "create", "file_path": "a.md", "content": "x"}}})
	wantError(t, "bad start", resp, body, 400, "message", "Invalid reference name: nope")
	resp, body = send(t, s, "POST", path, tok, obj{"branch": "x"})
	wantError(t, "missing", resp, body, 400, "error", "commit_message is missing, actions is missing")
	resp, body = send(t, s, "POST", path, tok, obj{"branch": "x", "commit_message": "x", "actions": []obj{}})
	wantError(t, "empty actions", resp, body, 400, "error", "actions is empty")
	resp, body = send(t, s, "POST", path, tok, obj{"branch": "x", "commit_message": "x", "actions": "create"})
	wantError(t, "actions not a list", resp, body, 400, "error", "actions is invalid")

	// A witness read from main holds for a branch started from main.
	mainWitness := fileWitness(t, s, tok, "src/main.go", "main")
	resp, body = send(t, s, "POST", path, tok, obj{"branch": "topic", "start_branch": "main", "commit_message": "Start",
		"actions": []obj{{"action": "update", "file_path": "src/main.go", "content": "package main\n", "last_commit_id": mainWitness}}})
	wantStatus(t, "new branch", resp, body, 201)
	if h, _ := s.BranchHead(ProjectAlpha, "topic"); h.ID != body["id"] || h.ParentIDs[0] == "" {
		t.Errorf("topic head = %+v", h)
	}
}

func TestUnifiedDiff(t *testing.T) {
	cases := []struct{ before, after, want string }{
		{"", "a\nb\n", "@@ -0,0 +1,2 @@\n+a\n+b\n"},
		{"a\n", "", "@@ -1 +0,0 @@\n-a\n"},
		{"1\n2\n3\n4\n5\n6\n7\n8\n", "1\n2\n3\n4\nX\n6\n7\n8\n", "@@ -2,7 +2,7 @@\n 2\n 3\n 4\n-5\n+X\n 6\n 7\n 8\n"},
		{"same\n", "same\n", ""},
	}
	for _, c := range cases {
		if got, _, _ := unified(c.before, c.after); got != c.want {
			t.Errorf("unified(%q, %q) = %q, want %q", c.before, c.after, got, c.want)
		}
	}
	bin := "\x00"
	if d, _, _ := fileDiff("x.bin", "x.bin", &bin, nil); !strings.HasPrefix(d.Diff, "Binary files") || !d.DeletedFile {
		t.Errorf("binary = %+v", d)
	}
}

func TestMarkTodoDone(t *testing.T) {
	s := New(t, Options{})
	tok := s.Token()
	resp, body := send(t, s, "POST", "/todos/95001/mark_as_done", tok, nil)
	wantStatus(t, "done", resp, body, 201)
	if body["state"] != "done" || s.TodoState(TodoAssigned) != "done" {
		t.Errorf("todo = %v", body)
	}
	resp, body = send(t, s, "POST", "/todos/95001/mark_as_done", tok, nil)
	wantStatus(t, "again", resp, body, 201)
	resp, body = send(t, s, "POST", "/todos/95004/mark_as_done", tok, nil)
	wantError(t, "another's", resp, body, 404, "message", "404 Todo Not Found")
	resp, body = send(t, s, "POST", "/todos/1/mark_as_done", tok, nil)
	wantError(t, "unknown", resp, body, 404, "message", "404 Todo Not Found")
	if s.TodoState(1) != "" {
		t.Error("an unknown to-do has a state")
	}
	resp, body = send(t, s, "POST", "/todos/95005/mark_as_done", tok, nil)
	if resp.StatusCode != 201 || body["target"].(obj)["id"] == nil {
		t.Errorf("commit to-do = %d %v", resp.StatusCode, body)
	}
}

func TestLintContent(t *testing.T) {
	s := New(t, Options{})
	tok := s.Token()
	cfg := "stages: [build, test]\n\n.hidden:\n  script: x\n\n# a comment\nbuild:\n  stage: build\n  script: make\n\ncheck:\n  script: make check\n  when: manual\n  allow_failure: true\n"
	resp, body := send(t, s, "POST", "/projects/2001/ci/lint", tok, obj{"content": cfg, "include_jobs": true, "dry_run": true, "ref": "main"})
	wantStatus(t, "lint", resp, body, 200)
	jobs := body["jobs"].([]any)
	if body["valid"] != true || len(jobs) != 2 || jobs[1].(obj)["when"] != "manual" || jobs[1].(obj)["allow_failure"] != true ||
		jobs[0].(obj)["stage"] != "build" || len(body["warnings"].([]any)) != 1 {
		t.Errorf("lint = %v", body)
	}
	resp, body = send(t, s, "POST", "/projects/2001/ci/lint", tok, obj{"content": "build:\n  stage: build\n"})
	if resp.StatusCode != 200 || body["valid"] != false || !strings.Contains(body["errors"].([]any)[0].(string), "jobs:build config should implement") {
		t.Errorf("no script = %v", body)
	}
	_, body = send(t, s, "POST", "/projects/2001/ci/lint", tok, obj{"content": "stages: [a]\n"})
	if body["valid"] != false || !strings.Contains(body["errors"].([]any)[0].(string), "at least one visible job") {
		t.Errorf("no jobs = %v", body)
	}
	_, body = send(t, s, "POST", "/projects/2001/ci/lint", tok, obj{"content": "build:\n  script: 42\n"})
	if body["valid"] != false {
		t.Errorf("number script = %v", body)
	}
	resp, body = send(t, s, "POST", "/projects/2001/ci/lint", tok, obj{"include_jobs": true})
	wantError(t, "no content", resp, body, 400, "error", "content is missing")
	// The GET lints the stored configuration the same way.
	_, body = send(t, s, "GET", "/projects/2001/ci/lint?include_jobs=true", tok, nil)
	if body["valid"] != true || len(body["jobs"].([]any)) != 4 {
		t.Errorf("GET lint = %v", body)
	}
}

func TestUsersByExactUsername(t *testing.T) {
	s := New(t, Options{})
	resp, _ := send(t, s, "GET", "/users?username=BOB", s.Token(), nil)
	if resp.Header.Get("X-Total") != "1" {
		t.Errorf("exact username = %v", resp.Header.Get("X-Total"))
	}
	resp, _ = send(t, s, "GET", "/users?username=bo", s.Token(), nil)
	if resp.Header.Get("X-Total") != "0" {
		t.Errorf("a prefix matched: %v", resp.Header.Get("X-Total"))
	}
}

func TestAfterApplyFaultLosesTheAnswer(t *testing.T) {
	s := New(t, Options{})
	tok := s.Token()
	s.Inject(Fault{Method: "POST", Path: "/projects/2001/issues", Status: http.StatusBadGateway, Body: `{"message":"502 Bad Gateway"}`,
		AfterApply: true, Delay: time.Millisecond})
	resp, body := send(t, s, "POST", "/projects/2001/issues", tok, obj{"title": "Landed anyway"})
	if resp.StatusCode != 502 || body["message"] != "502 Bad Gateway" {
		t.Errorf("answer = %d %v", resp.StatusCode, body)
	}
	if iss, ok := s.Issue(ProjectAlpha, 26); !ok || iss.Title != "Landed anyway" {
		t.Errorf("the write did not land: %+v", iss)
	}
	resp, _ = send(t, s, "POST", "/projects/2001/issues", tok, obj{"title": "Second"})
	if resp.StatusCode != 201 {
		t.Errorf("after the fault = %d", resp.StatusCode)
	}
}

// Accessors answer nothing for what does not exist.
func TestAccessorsOnMissingThings(t *testing.T) {
	s := New(t, Options{})
	if _, ok := s.MergeRequest("nope/nope", 1); ok {
		t.Error("MergeRequest")
	}
	if s.Discussions("nope/nope", "mr", 1) != nil || s.Drafts("nope/nope", 1) != nil {
		t.Error("Discussions or Drafts")
	}
	if _, ok := s.FileAt("nope/nope", "main", "x"); ok {
		t.Error("FileAt")
	}
	if _, ok := s.BranchHead(ProjectAlpha, "nope"); ok {
		t.Error("BranchHead")
	}
	if _, ok := s.userByID(1); ok {
		t.Error("userByID")
	}
}

// The reads a write is checked through answer 200 on the instance's own
// tests, not only through the packages that use it.
func TestReadsAnswer(t *testing.T) {
	s := New(t, Options{})
	tok := s.Token()
	for _, path := range []string{
		"/groups/example-group/projects?include_subgroups=true", "/groups/3001/issues?labels=bug", "/groups/example-group/merge_requests",
		"/groups/3001/milestones?include_ancestors=true", "/groups/example-group%2Fsub/milestones?include_ancestors=true",
		"/groups/3001/search?scope=issues&search=generated", "/merge_requests?scope=all&draft=false",
		"/issues?scope=assigned_to_me&assignee_username=alice&created_after=2026-01-01T00:00:00Z&updated_before=2027-01-01T00:00:00Z",
		"/projects/2001/merge_requests?source_branch=feature%2Flogin&reviewer_username=carol&order_by=updated_at&sort=asc",
		"/projects/2001/merge_requests/1/discussions", "/projects/2001/merge_requests/1/diffs",
		"/projects/2001/merge_requests/1/commits", "/projects/2001/merge_requests/1/draft_notes",
		"/projects/2001/issues/1/discussions", "/projects/2001/repository/tree?path=docs&recursive=true",
		"/projects/2001/repository/tree?pagination=keyset&per_page=2", "/projects/2001/repository/branches?search=feat",
		"/projects/2001/repository/commits?ref_name=main&path=docs&author=Alice&since=2026-01-01T00:00:00Z&until=2027-01-01T00:00:00Z",
		"/projects/2001/repository/compare?from=main&to=feature%2Flogin", "/projects/2001/repository/tags?search=%5Ev&order_by=name",
		"/projects/2001/pipelines?ref=main&sort=asc&order_by=updated_at", "/projects/2001/pipelines/61001",
		"/projects/2001/pipelines/61001/jobs?scope=failed", "/projects/2001/jobs/70002", "/projects/2001/jobs/70002/trace",
		"/projects/2001/members/all?query=car", "/projects/2001/milestones?state=active&include_ancestors=true",
		"/projects/2001/labels?with_counts=true&search=bug", "/projects/2001/search?scope=blobs&search=package",
		"/projects/2001/search?scope=commits&search=update", "/projects/2001/search?scope=notes&search=test",
		"/todos?type=Issue&action=assigned&project_id=2001", "/search?scope=projects&search=alpha",
	} {
		if resp, body := send(t, s, "GET", path, tok, nil); resp.StatusCode != 200 {
			t.Errorf("%s = %d %v", path, resp.StatusCode, body)
		}
	}
	for _, path := range []string{"/groups/9/projects", "/projects/2001/pipelines/1", "/projects/2001/pipelines/1/jobs",
		"/projects/2001/jobs/1", "/projects/2001/jobs/1/trace", "/projects/2001/repository/tree?path=nope",
		"/projects/2001/repository/commits?ref_name=nope", "/projects/2001/repository/compare?from=nope&to=main"} {
		if resp, _ := send(t, s, "GET", path, tok, nil); resp.StatusCode != 404 {
			t.Errorf("%s = %d", path, resp.StatusCode)
		}
	}
	s.Revoke(tok)
	s.ResetRequests()
	if resp, _ := send(t, s, "GET", "/user", tok, nil); resp.StatusCode != 401 || len(s.Requests()) != 1 {
		t.Errorf("revoked = %d", resp.StatusCode)
	}
}

// Time tracking answers as lib/api/time_tracking_endpoints.rb does: 200
// for the estimate and the resets, 201 for added time, 400 for what
// GitLab cannot parse or refuses, 403 below the role that manages the
// item, and updated_at moving with each change.
func TestTimeTracking(t *testing.T) {
	s, now := frozen(t)
	tok := s.Token()
	issue := "/projects/2001/issues/3/"
	resp, body := send(t, s, "GET", issue+"time_stats", tok, nil)
	wantStatus(t, "time_stats", resp, body, 200)
	if body["time_estimate"] != float64(12600) || body["human_time_estimate"] != "3h 30m" || body["human_total_time_spent"] != "10h" {
		t.Errorf("seeded stats = %v", body)
	}
	updated := func() time.Time {
		_, is := send(t, s, "GET", "/projects/2001/issues/3", tok, nil)
		at, _ := time.Parse(time.RFC3339Nano, is["updated_at"].(string))
		return at
	}
	*now = now.Add(time.Hour)
	before := updated()
	resp, body = send(t, s, "POST", issue+"time_estimate", tok, obj{"duration": "1w 2d 3h"})
	wantStatus(t, "time_estimate", resp, body, 200)
	if body["time_estimate"] != float64(144000+57600+10800) || body["human_time_estimate"] != "59h" || !updated().After(before) {
		t.Errorf("time_estimate = %v", body)
	}
	// The same estimate again moves nothing.
	*now = now.Add(time.Hour)
	before = updated()
	send(t, s, "POST", issue+"time_estimate", tok, obj{"duration": "1w 2d 3h"})
	if !updated().Equal(before) {
		t.Error("an unchanged estimate moved updated_at")
	}
	resp, body = send(t, s, "POST", issue+"time_estimate", tok, obj{"duration": "-1h"})
	wantError(t, "a negative estimate", resp, body, 400, "message", "must have a valid format")
	// Words GitLab does not know are dropped, and an estimate keeps a
	// zero, so a word alone resets it.
	resp, body = send(t, s, "POST", issue+"time_estimate", tok, obj{"duration": "soon"})
	wantStatus(t, "time_estimate soon", resp, body, 200)
	if body["time_estimate"] != float64(0) || body["human_time_estimate"] != nil {
		t.Errorf("a word as the estimate = %v, want 0 with a null human form", body)
	}

	*now = now.Add(time.Hour)
	before = updated()
	resp, body = send(t, s, "POST", issue+"add_spent_time", tok, obj{"duration": "1h 30m"})
	wantStatus(t, "add_spent_time", resp, body, 201)
	// Added time leaves updated_at where it was, as gitlab.com does.
	if body["total_time_spent"] != float64(36000+5400) || !updated().Equal(before) {
		t.Errorf("add_spent_time = %v", body)
	}
	resp, body = send(t, s, "POST", issue+"add_spent_time", tok, obj{"duration": "-2d"})
	wantError(t, "subtracting too much", resp, body, 400, "message", "Time to subtract exceeds the total time spent")
	resp, body = send(t, s, "POST", issue+"add_spent_time", tok, obj{"duration": "5y"})
	wantError(t, "past four years", resp, body, 400, "message", "Total time spent cannot exceed 4 years.")
	resp, body = send(t, s, "POST", issue+"time_estimate", tok, obj{"duration": "99999999h"})
	if body["time_estimate"] != float64(2147483647) {
		t.Errorf("an estimate past the limit = %d %v, want it kept as the limit", resp.StatusCode, body)
	}
	resp, body = send(t, s, "POST", issue+"add_spent_time", tok, obj{"duration": "0h"})
	wantError(t, "a zero duration", resp, body, 400, "message", "can't be blank")
	// GitLab's parser drops words it does not know.
	resp, body = send(t, s, "POST", issue+"add_spent_time", tok, obj{"duration": "1 hour and 30 foo"})
	wantStatus(t, "add_spent_time words", resp, body, 201)
	if body["total_time_spent"] != float64(36000+5400+31*3600) {
		t.Errorf("words: %v, want 1 hour and 30 hours more", body)
	}
	*now = now.Add(time.Hour)
	before = updated()
	resp, body = send(t, s, "POST", issue+"reset_spent_time", tok, nil)
	wantStatus(t, "reset_spent_time", resp, body, 200)
	// A reset leaves updated_at where it was too.
	if body["total_time_spent"] != float64(0) || body["human_total_time_spent"] != nil || !updated().Equal(before) {
		t.Errorf("reset_spent_time = %v", body)
	}
	resp, body = send(t, s, "POST", issue+"reset_time_estimate", tok, nil)
	wantStatus(t, "reset_time_estimate", resp, body, 200)

	// dave does not belong to the project; he may read its issues, not
	// track time on them. A merge request needs a developer.
	dave := s.TokenFor("dave", "api")
	resp, body = send(t, s, "POST", issue+"add_spent_time", dave, obj{"duration": "1h"})
	wantError(t, "dave", resp, body, 403, "message", "403 Forbidden")
	resp, body = send(t, s, "POST", "/projects/2001/merge_requests/1/add_spent_time", tok, obj{"duration": "1mo"})
	wantStatus(t, "merge request", resp, body, 201)
	if body["human_total_time_spent"] != "160h" {
		t.Errorf("merge request = %v", body)
	}
}

func TestTimeTrackingDurations(t *testing.T) {
	for _, c := range []struct {
		in       string
		keepZero bool
		want     int64
		ok       bool
	}{
		{"3", false, 3 * 3600, true},
		{"1h30m", false, 5400, true},
		{"1mo 1w 1d 1h 1m 1s", false, 576000 + 144000 + 28800 + 3600 + 60 + 1, true},
		{"1.5h", false, 5400, true},
		{"-30m", false, -1800, true},
		{"1:30", false, 90, true},
		{"5 foo", false, 5 * 3600, true},
		{"day", false, 28800, true},
		{"0", false, 0, false},
		{"0", true, 0, true},
		{"soon", false, 0, false},
	} {
		got, ok := parseTimeTracking(c.in, c.keepZero)
		if got != c.want || ok != c.ok {
			t.Errorf("parse %q = %d %v, want %d %v", c.in, got, ok, c.want, c.ok)
		}
	}
	// gitlab.com writes hours at most, below a year.
	for secs, want := range map[int64]string{60: "1m", 5400: "1h 30m", 28800: "8h", 144000 + 28800: "48h", 576000: "160h",
		576000*2 + 3600 + 1: "321h 1s", -1800: "-30m", 31557600 + 28800: "1y 1d"} {
		if got := humanDuration(secs); got == nil || *got != want {
			t.Errorf("human %d = %v, want %s", secs, got, want)
		}
	}
	if humanDuration(0) != nil {
		t.Error("human 0 is not null")
	}
}

// Participants are subscribed until they unsubscribe, as GitLab's
// participant declarations make them: a merge request's reviewers, a
// system note's author, and whoever the description mentions.
func TestParticipantsAreSubscribed(t *testing.T) {
	s, _ := frozen(t)
	dave := s.TokenFor("dave", "api")
	subscribe := func(path string) int {
		resp, _ := send(t, s, http.MethodPost, "/projects/2001/"+path+"/subscribe", dave, nil)
		return resp.StatusCode
	}
	if got := subscribe("issues/1"); got != http.StatusCreated {
		t.Fatalf("dave takes no part in issue 1, yet subscribing answered %d", got)
	}
	s.mu.Lock()
	p := s.projectByPath(ProjectAlpha)
	p.mrs[1].Reviewers = append(p.mrs[1].Reviewers, s.user("dave"))
	p.issues[1].Description += "\n\ncc @dave."
	key := "issue:4"
	ds := p.discussions[key]
	ds[len(ds)-1].Notes[0].Author = s.user("dave") // the system note
	p.levels["dave"], p.members["dave"] = 30, true // to be let subscribe to a merge request
	s.mu.Unlock()
	for _, path := range []string{"merge_requests/2", "issues/2", "issues/4"} {
		if got := subscribe(path); got != http.StatusNotModified {
			t.Errorf("%s: %d, want 304", path, got)
		}
	}
}

// Closing an item marks the closer's pending to-dos on it done.
func TestClosingClearsYourTodos(t *testing.T) {
	s, _ := frozen(t)
	alice := s.Token()
	resp, body := send(t, s, http.MethodPost, "/projects/2001/issues/2/todo", alice, nil)
	wantStatus(t, "todo", resp, body, http.StatusCreated)
	id := int64(body["id"].(float64))
	resp, body = send(t, s, http.MethodPut, "/projects/2001/issues/2", alice, obj{"state_event": "close"})
	wantStatus(t, "close", resp, body, http.StatusOK)
	if got := s.TodoState(id); got != "done" {
		t.Errorf("to-do after the close: %q", got)
	}
}
