package tools

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/mmedum/gitlab-mcp/v3/internal/gapi/gitlabtest"
)

// carolRequestsChanges makes carol, !1's reviewer, request changes, and
// returns the harness signed in as alice.
func carolRequestsChanges(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, harnessOptions{})
	carol := newHarness(t, harnessOptions{over: h.gl, token: func(gl *gitlabtest.Server) string { return gl.TokenFor("carol", "api") }})
	carol.ok("submit_review", map[string]any{"project": alpha, "iid": 1, "reviewer_state": "requested_changes"})
	return h
}

// askCarolAgain is rerequest_review's arguments for carol on !1, as a
// read after carolRequestsChanges shows her.
func askCarolAgain() map[string]any {
	return map[string]any{"project": alpha, "iid": 1, "username": "carol", "reviewer_state": "requested_changes"}
}

// A reviewer who requested changes is asked again: GitLab sets them back
// to unreviewed, which removing and re-adding them would not.
func TestRerequestReviewAsksAReviewerAgain(t *testing.T) {
	h := carolRequestsChanges(t)
	args := askCarolAgain()
	args["username"] = "@carol"
	text, out := h.ok("rerequest_review", args)
	if get(out, "outcome") != "rerequested" || get(out, "state_before") != "requested_changes" ||
		get(out, "state_after") != "unreviewed" || !strings.Contains(text, "Asked @carol for a new review of !1.") {
		t.Errorf("rerequest_review = %v\n%s", out, text)
	}
	if _, mr := h.ok("get_merge_request", map[string]any{"project": alpha, "iid": 1}); get(mr, "reviewer_states", "reviewers", 0,
		"state") != "unreviewed" {
		t.Errorf("carol's state after = %v", get(mr, "reviewer_states"))
	}
}

// A refused re-request sends nothing.
func TestRerequestReviewRefuses(t *testing.T) {
	approvedInRead := gitlabtest.Fault{Method: http.MethodGet, Path: fmt.Sprintf("/projects/%d/merge_requests/1/approvals", alphaID),
		Status: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		Body: `{"approved":true,"approved_by":[{"user":{"id":1003,"username":"carol","name":"Carol Example","state":"active"}}]}`}
	cases := []struct {
		name, username, state, class, says string
		iid                                int
		setup                              func(*testing.T) *harness
	}{
		{"a state that moved since the read", "carol", "reviewed", "stale", "requested_changes now, not reviewed", 1, carolRequestsChanges},
		{"a state GitLab does not report", "carol", "changes_requested", "invalid", `"changes_requested" is not a review state`, 1,
			carolRequestsChanges},
		{"a reviewer whose state is approved", "carol", "approved", "blocked", "never takes away another person's approval", 1,
			func(t *testing.T) *harness { return newHarness(t, harnessOptions{}) }}, // carol approved !1
		{"a reviewer the approvals list, whatever their state", "carol", "requested_changes", "blocked",
			"never takes away another person's approval", 1, func(t *testing.T) *harness {
				h := carolRequestsChanges(t)
				h.gl.Inject(approvedInRead)
				return h
			}},
		{"a reviewer who has not reviewed", "carol", "unreviewed", "conflict", "carol has not reviewed !2 since being asked", 2,
			func(t *testing.T) *harness { return newHarness(t, harnessOptions{}) }}, // carol is asked on !2
		{"someone who is not a reviewer", "bob", "requested_changes", "invalid", "bob is not a reviewer of !1", 1, carolRequestsChanges},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := c.setup(t)
			sent := writesSent(h)
			text := h.fails("rerequest_review", map[string]any{"project": alpha, "iid": c.iid, "username": c.username,
				"reviewer_state": c.state}, c.class)
			if !strings.Contains(text, c.says) || writesSent(h) != sent {
				t.Errorf("%s: %s, %d writes sent", c.name, text, writesSent(h)-sent)
			}
		})
	}
}

// A payload means the mutation ran, even beside a top-level error, so
// the call is not reported as refused and repeated.
func TestARerequestPayloadWinsOverTopLevelErrors(t *testing.T) {
	h := carolRequestsChanges(t)
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: "/graphql", Status: http.StatusOK,
		Header: http.Header{"Content-Type": {"application/json"}},
		Body:   `{"data":{"mergeRequestReviewerRereview":{"errors":[]}},"errors":[{"message":"a field failed"}]}`})
	if _, out := h.ok("rerequest_review", askCarolAgain()); get(out, "outcome") != "rerequested" {
		t.Errorf("rerequest_review = %v", out)
	}
}

// A lost answer is settled by the reviewer's state, and the mutation is
// sent once either way: each one notifies the reviewer.
func TestALostRerequestIsSettledByReading(t *testing.T) {
	args := askCarolAgain()
	lost := gitlabtest.Fault{Method: http.MethodPost, Path: "/graphql", Status: http.StatusBadGateway, Body: `{"message":"502 Bad Gateway"}`}

	h := carolRequestsChanges(t)
	made := lost
	made.AfterApply = true
	h.gl.Inject(made)
	text, out := h.ok("rerequest_review", args)
	if get(out, "outcome") != "rerequested" || get(out, "state_after") != "unreviewed" ||
		!strings.Contains(text, "a read shows the reviewer back at unreviewed") || graphQLPosts(h) != 1 {
		t.Errorf("made, answer lost = %v, %d mutations sent\n%s", out, graphQLPosts(h), text)
	}

	h = carolRequestsChanges(t)
	h.gl.Inject(lost)
	text = h.fails("rerequest_review", args, "ambiguous_outcome")
	if !strings.Contains(text, "still at requested_changes, so it was not made") || graphQLPosts(h) != 1 {
		t.Errorf("not made, answer lost: %s, %d mutations sent", text, graphQLPosts(h))
	}
}

func graphQLPosts(h *harness) int {
	n := 0
	for _, r := range h.gl.Requests() {
		if r.Method == http.MethodPost && r.EscapedPath == "/api/graphql" {
			n++
		}
	}
	return n
}

// GitLab answers a refused mutation with status 200 and its errors in the
// body; each maps to the class the caller acts on.
func TestRerequestReviewMapsGraphQLErrors(t *testing.T) {
	cases := []struct{ name, body, class string }{
		{"no access, top level", `{"data":{"mergeRequestReviewerRereview":null},"errors":[{"message":"The resource that ` +
			`you are attempting to access does not exist or you don't have permission to perform this action"}]}`, "not_found"},
		{"the mutation's own permission error", `{"data":{"mergeRequestReviewerRereview":{"errors":["Invalid permissions"]}}}`,
			"forbidden"},
		{"not a reviewer after all", `{"data":{"mergeRequestReviewerRereview":{"errors":["Reviewer not found"]}}}`, "invalid"},
		{"an error this server does not know", `{"errors":[{"message":"Field 'x' doesn't exist"}]}`, "unexpected"},
	}
	// Each fault answers one mutation and changes nothing, so one
	// harness serves every case.
	h := carolRequestsChanges(t)
	for _, c := range cases {
		h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: "/graphql", Status: http.StatusOK,
			Header: http.Header{"Content-Type": {"application/json"}}, Body: c.body})
		if text, _, isErr := h.call("rerequest_review", askCarolAgain()); !isErr || !strings.HasPrefix(text, "["+c.class+"]") {
			t.Errorf("%s: rerequest_review = %q, want class [%s]", c.name, text, c.class)
		}
	}
}
