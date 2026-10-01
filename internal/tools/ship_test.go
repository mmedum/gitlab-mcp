package tools

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/internal/config"
	"github.com/mmedum/gitlab-mcp/v2/internal/gapi/gitlabtest"
)

// The Ship and Destructive tools against the in-memory instance: that
// only their flag registers them, the witness each carries, what each
// refuses before sending, and what each reads back (§4.3, §4.5, §4.6).

var (
	ship        = config.Config{EnableShip: true}
	destructive = config.Config{EnableDestructive: true}
)

func mrSHA(h *harness, iid int) string {
	h.t.Helper()
	_, out := h.ok("get_merge_request", map[string]any{"project": alpha, "iid": iid})
	return get(out, "sha").(string)
}

func TestShipAndDestructiveAreRegisteredOnlyByTheirFlags(t *testing.T) {
	names := func(cfg config.Config) []string {
		var out []string
		for _, r := range Surface(cfg, nil) {
			out = append(out, r.Name)
		}
		return out
	}
	shipTools := []string{"merge_merge_request", "cancel_auto_merge", "approve_merge_request", "unapprove_merge_request",
		"apply_suggestions", "run_pipeline", "run_merge_request_pipeline", "retry_pipeline", "retry_job", "play_job", "cancel_pipeline"}
	deleteTools := []string{"delete_branch", "delete_comment"}
	for _, c := range []struct {
		name       string
		cfg        config.Config
		ship, dels bool
	}{
		{"default", config.Config{}, false, false},
		{"ship", ship, true, false},
		{"destructive", destructive, false, true},
		{"both, read-only", config.Config{ReadOnly: true, EnableShip: true, EnableDestructive: true}, false, false},
	} {
		got := names(c.cfg)
		for _, tool := range shipTools {
			if slices.Contains(got, tool) != c.ship {
				t.Errorf("%s: %s registered = %v", c.name, tool, !c.ship)
			}
		}
		for _, tool := range deleteTools {
			if slices.Contains(got, tool) != c.dels {
				t.Errorf("%s: %s registered = %v", c.name, tool, !c.dels)
			}
		}
	}
	// A toolset's Ship and Destructive tools need both the toolset and
	// the flag.
	releases := config.Config{Toolsets: []string{"releases", "wiki", "snippets"}}
	if got := names(releases); slices.Contains(got, "create_release") || slices.Contains(got, "delete_wiki_page") ||
		slices.Contains(got, "delete_snippet") || !slices.Contains(got, "list_releases") || !slices.Contains(got, "save_wiki_page") ||
		!slices.Contains(got, "update_snippet") {
		t.Errorf("toolsets without the flags: %v", got)
	}
	releases.EnableShip, releases.EnableDestructive = true, true
	if got := names(releases); !slices.Contains(got, "create_release") || !slices.Contains(got, "delete_wiki_page") ||
		!slices.Contains(got, "delete_snippet") {
		t.Errorf("toolsets with the flags: %v", got)
	}
}

// ------------------------------------------------------------- merging

func TestMergeMergeRequest(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	sha := mrSHA(h, 1)
	before := writesSent(h)

	h.fails("merge_merge_request", map[string]any{"project": alpha, "iid": 1, "sha": ""}, "invalid")
	h.fails("merge_merge_request", map[string]any{"project": alpha, "iid": 1, "sha": strings.Repeat("0", 40)}, "stale")
	_, dry := h.ok("merge_merge_request", map[string]any{"project": alpha, "iid": 1, "sha": sha, "dry_run": true, "squash": true})
	if get(dry, "outcome") != "dry_run" || !slices.Contains(strs(get(dry, "would_send", "fields")), "squash") {
		t.Errorf("dry run = %v", dry)
	}
	if writesSent(h) != before {
		t.Fatal("a refusal or a dry run wrote")
	}

	text, out := h.ok("merge_merge_request", map[string]any{"project": alpha, "iid": 1, "sha": sha,
		"merge_commit_message": "Merge it", "remove_source_branch": false})
	if get(out, "outcome") != "merged" || get(out, "state") != "merged" || get(out, "merge_commit_sha") == "" ||
		get(out, "merged_by") != gitlabtest.DefaultUser || get(out, "sha") != sha {
		t.Errorf("merged = %v", out)
	}
	if !strings.Contains(text, "Merged merge request !1.") || !strings.Contains(text, "Merged ") {
		t.Errorf("text:\n%s", text)
	}
	head, _ := h.gl.BranchHead(alpha, "main")
	if head.ID != get(out, "merge_commit_sha") {
		t.Errorf("main's head is %s, not the merge commit", head.ID)
	}
	_, again := h.ok("merge_merge_request", map[string]any{"project": alpha, "iid": 1, "sha": sha})
	if get(again, "outcome") != "unchanged" || !strings.Contains(fmt.Sprint(get(again, "notes")), "already merged") {
		t.Errorf("again = %v", again)
	}
}

func TestMergeRefusalsNameTheMergeStatus(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	sha := mrSHA(h, 2)
	h.gl.SetMergeStatus(alpha, 2, "not_approved")
	text := h.fails("merge_merge_request", map[string]any{"project": alpha, "iid": 2, "sha": sha}, "conflict")
	if !strings.Contains(text, `"not_approved"`) {
		t.Errorf("refusal: %s", text)
	}

	// Closed and draft merge requests are refused before anything is
	// sent, with what to do next.
	at := mrWitness(h, 3)
	h.ok("update_merge_request", map[string]any{"project": alpha, "iid": 3, "updated_at": at, "draft": true})
	before := writesSent(h)
	text = h.fails("merge_merge_request", map[string]any{"project": alpha, "iid": 3, "sha": mrSHA(h, 3)}, "conflict")
	if !strings.Contains(text, "draft") || writesSent(h) != before {
		t.Errorf("draft refusal: %s", text)
	}
	h.ok("update_merge_request", map[string]any{"project": alpha, "iid": 3, "updated_at": mrWitness(h, 3), "state": "close"})
	h.fails("merge_merge_request", map[string]any{"project": alpha, "iid": 3, "sha": mrSHA(h, 3)}, "conflict")
}

func TestMergeAfterAPushIsStale(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	sha := mrSHA(h, 2)
	if _, ok := h.gl.PushTo(alpha, "feature/login"); !ok {
		t.Fatal("push")
	}
	text := h.fails("merge_merge_request", map[string]any{"project": alpha, "iid": 2, "sha": sha}, "stale")
	if !strings.Contains(text, "NOT") && !strings.Contains(text, "moved") {
		t.Errorf("stale: %s", text)
	}
}

func TestAutoMerge(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	h.gl.SetHeadPipelineStatus(alpha, 2, "running")
	text, out := h.ok("merge_merge_request", map[string]any{"project": alpha, "iid": 2, "sha": mrSHA(h, 2), "auto_merge": true})
	if get(out, "outcome") != "auto_merge_set" || get(out, "state") != "opened" || !strings.Contains(text, "not merged yet") {
		t.Errorf("auto merge = %v\n%s", out, text)
	}
}

func TestMergeWhoseAnswerWasLostReadsItBack(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	sha := mrSHA(h, 1)
	id := h.gl.ProjectID(alpha)
	h.gl.Inject(gitlabtest.Fault{Method: "PUT", Path: fmt.Sprintf("/projects/%d/merge_requests/1/merge", id), Status: 502,
		AfterApply: true, Body: `{"message":"502 Bad Gateway"}`})
	_, out := h.ok("merge_merge_request", map[string]any{"project": alpha, "iid": 1, "sha": sha})
	if get(out, "outcome") != "merged" || !strings.Contains(fmt.Sprint(get(out, "notes")), "a read afterwards shows it merged") {
		t.Errorf("lost merge = %v", out)
	}
}

// ----------------------------------------------------------- approving

func TestApproveAndUnapprove(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	sha := mrSHA(h, 2) // carol's, not approved
	h.fails("approve_merge_request", map[string]any{"project": alpha, "iid": 2, "sha": strings.Repeat("0", 40)}, "stale")
	_, dry := h.ok("approve_merge_request", map[string]any{"project": alpha, "iid": 2, "sha": sha, "dry_run": true})
	if get(dry, "outcome") != "dry_run" || get(dry, "you_approved") != false {
		t.Errorf("dry run = %v", dry)
	}

	text, out := h.ok("approve_merge_request", map[string]any{"project": alpha, "iid": 2, "sha": sha})
	if get(out, "outcome") != "approved" || get(out, "you_approved") != true ||
		!slices.Contains(strs(get(out, "approved_by")), gitlabtest.DefaultUser) || !strings.Contains(text, "Approved merge request !2.") {
		t.Errorf("approve = %v\n%s", out, text)
	}
	before := writesSent(h)
	_, again := h.ok("approve_merge_request", map[string]any{"project": alpha, "iid": 2, "sha": sha})
	if get(again, "outcome") != "unchanged" || writesSent(h) != before {
		t.Errorf("again = %v", again)
	}

	_, un := h.ok("unapprove_merge_request", map[string]any{"project": alpha, "iid": 2})
	if get(un, "outcome") != "unapproved" || get(un, "you_approved") != false {
		t.Errorf("unapprove = %v", un)
	}
	_, unAgain := h.ok("unapprove_merge_request", map[string]any{"project": alpha, "iid": 2})
	if get(unAgain, "outcome") != "unchanged" {
		t.Errorf("unapprove again = %v", unAgain)
	}

	// alice wrote !3, and the instance forbids approving your own.
	text = h.fails("approve_merge_request", map[string]any{"project": alpha, "iid": 3, "sha": mrSHA(h, 3)}, "forbidden")
	if !strings.Contains(text, "nothing changed") {
		t.Errorf("own merge request: %s", text)
	}
}

func TestApprovalWhoseAnswerWasLostIsSettled(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	id := h.gl.ProjectID(alpha)
	h.gl.Inject(gitlabtest.Fault{Method: "POST", Path: fmt.Sprintf("/projects/%d/merge_requests/2/approve", id), Status: 500,
		AfterApply: true, Body: `{"message":"500 Internal Server Error"}`})
	_, out := h.ok("approve_merge_request", map[string]any{"project": alpha, "iid": 2, "sha": mrSHA(h, 2)})
	if get(out, "outcome") != "approved" || get(out, "you_approved") != true {
		t.Errorf("settled approval = %v", out)
	}
	if a, _ := h.gl.Approvals(alpha, 2, gitlabtest.DefaultUser); len(a.ApprovedBy) != 1 {
		t.Errorf("approved %d times", len(a.ApprovedBy))
	}
}

// ------------------------------------------------------------------- CI

func TestRunPipeline(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	h.fails("run_pipeline", map[string]any{"project": alpha, "ref": " "}, "invalid")
	h.fails("run_pipeline", map[string]any{"project": alpha, "ref": "main",
		"variables": []map[string]any{{"key": "A", "value": "1"}, {"key": "A", "value": "2"}}}, "invalid")
	h.fails("run_pipeline", map[string]any{"project": alpha, "ref": "main", "variables": []map[string]any{{"key": "A", "value": "1", "type": "secret"}}},
		"invalid")
	h.fails("run_pipeline", map[string]any{"project": alpha, "ref": "no-such-ref"}, "invalid")

	secret := "s3cret-generated-value"
	args := map[string]any{"project": alpha, "ref": "main", "variables": []map[string]any{{"key": "DEPLOY_TARGET", "value": secret},
		{"key": "CONFIG", "value": secret, "type": "file"}}, "inputs": map[string]any{"environment": "staging"}}
	_, dry := h.ok("run_pipeline", map[string]any{"project": alpha, "ref": "main", "dry_run": true, "variables": args["variables"]})
	if get(dry, "outcome") != "dry_run" || get(dry, "pipeline_id") != float64(0) {
		t.Errorf("dry run = %v", dry)
	}
	text, out := h.ok("run_pipeline", args)
	if get(out, "outcome") != "created" || get(out, "source") != "api" || get(out, "ref") != "main" ||
		strings.Join(strs(get(out, "variables")), ",") != "DEPLOY_TARGET,CONFIG" || strings.Join(strs(get(out, "inputs")), ",") != "environment" {
		t.Errorf("run = %v", out)
	}
	if strings.Contains(text, secret) || strings.Contains(fmt.Sprint(out), secret) {
		t.Errorf("a variable's value was shown:\n%s", text)
	}
}

func TestRunPipelineWhoseAnswerWasLostIsNotRepeated(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	id := h.gl.ProjectID(alpha)
	h.gl.Inject(gitlabtest.Fault{Method: "POST", Path: fmt.Sprintf("/projects/%d/pipeline", id), Status: 500, AfterApply: true,
		Body: `{"message":"500 Internal Server Error"}`})
	text := h.fails("run_pipeline", map[string]any{"project": alpha, "ref": "main"}, "ambiguous_outcome")
	if !strings.Contains(text, "a read shows it was created: pipeline") {
		t.Errorf("settled: %s", text)
	}
	posts := 0
	for _, r := range h.gl.Requests() {
		if r.Method == "POST" && strings.HasSuffix(r.EscapedPath, "/pipeline") {
			posts++
		}
	}
	if posts != 1 {
		t.Errorf("the create was sent %d times", posts)
	}
}

// mrPipelinePosts counts the merge request pipeline creates sent.
func mrPipelinePosts(h *harness) int {
	n := 0
	for _, r := range h.gl.Requests() {
		if r.Method == "POST" && strings.HasSuffix(r.EscapedPath, "/pipelines") && strings.Contains(r.EscapedPath, "/merge_requests/") {
			n++
		}
	}
	return n
}

// run_merge_request_pipeline runs a detached pipeline, or a merged
// results one where the project has them on, and names which by its ref.
func TestRunMergeRequestPipeline(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	_, dry := h.ok("run_merge_request_pipeline", map[string]any{"project": alpha, "iid": 1, "dry_run": true})
	if get(dry, "outcome") != "dry_run" || get(dry, "pipeline_id") != float64(0) || get(dry, "kind") != "" || mrPipelinePosts(h) != 0 {
		t.Errorf("dry run = %v; %d sent", dry, mrPipelinePosts(h))
	}

	head := mrSHA(h, 1)
	text, out := h.ok("run_merge_request_pipeline", map[string]any{"project": alpha, "iid": 1})
	if get(out, "outcome") != "created" || get(out, "kind") != "detached" || get(out, "ref") != "refs/merge-requests/1/head" ||
		get(out, "sha") != head || get(out, "source") != "merge_request_event" || get(out, "status") != "created" ||
		get(out, "web_url") == "" || get(out, "source_branch") != "feature/login" {
		t.Errorf("detached = %v", out)
	}
	if !strings.Contains(text, "a detached pipeline") {
		t.Errorf("text:\n%s", text)
	}

	h.gl.SetMergePipelines(alpha, true)
	text, out = h.ok("run_merge_request_pipeline", map[string]any{"project": alpha, "iid": 2})
	if get(out, "kind") != "merged_results" || get(out, "ref") != "refs/merge-requests/2/merge" || get(out, "sha") == mrSHA(h, 2) {
		t.Errorf("merged results = %v", out)
	}
	if !strings.Contains(text, "a merged results pipeline") {
		t.Errorf("text:\n%s", text)
	}
	if n := mrPipelinePosts(h); n != 2 {
		t.Errorf("%d creates sent", n)
	}
}

// GitLab's refusals are named: 405 for no commits, 400 for an account
// that may not run it, and 400 for a pipeline it could not save. A fork's
// merge request is refused before anything is sent.
func TestRunMergeRequestPipelineRefusals(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	h.ok("create_branch", map[string]any{"project": alpha, "branch": "empty", "ref": "main"})
	_, mr := h.ok("create_merge_request", map[string]any{"project": alpha, "source_branch": "empty", "target_branch": "main",
		"title": "Nothing yet"})
	empty := get(mr, "iid")
	if text := h.fails("run_merge_request_pipeline", map[string]any{"project": alpha, "iid": empty}, "conflict"); !strings.Contains(text, "no commits") {
		t.Errorf("no commits: %s", text)
	}

	carol := newHarness(t, harnessOptions{cfg: ship, over: h.gl, token: func(gl *gitlabtest.Server) string { return gl.TokenFor("carol", "api") }})
	if text := carol.fails("run_merge_request_pipeline", map[string]any{"project": alpha, "iid": 1}, "forbidden"); !strings.Contains(text,
		"Insufficient permissions") {
		t.Errorf("no permission: %s", text)
	}

	id := h.gl.ProjectID(alpha)
	h.gl.Inject(gitlabtest.Fault{Method: "POST", Path: fmt.Sprintf("/projects/%d/merge_requests/1/pipelines", id), Status: 400,
		Body: `{"message":{"base":["No stages / jobs for this pipeline."]}}`})
	if text := h.fails("run_merge_request_pipeline", map[string]any{"project": alpha, "iid": 1}, "conflict"); !strings.Contains(text,
		"No stages / jobs") {
		t.Errorf("not saved: %s", text)
	}

	h.gl.SetMRSourceProject(alpha, 2, gitlabtest.ProjectBeta)
	h.gl.ResetRequests()
	if text := h.fails("run_merge_request_pipeline", map[string]any{"project": alpha, "iid": 2}, "blocked"); !strings.Contains(text, "fork") {
		t.Errorf("fork: %s", text)
	}
	h.fails("run_merge_request_pipeline", map[string]any{"project": alpha, "iid": 2, "dry_run": true}, "blocked")
	if n := writesSent(h); n != 0 {
		t.Errorf("fork: %d writes sent", n)
	}
}

// A lost answer is settled by reading the merge request's pipelines, and
// the create is never sent again.
func TestRunMergeRequestPipelineWhoseAnswerWasLostIsNotRepeated(t *testing.T) {
	for _, c := range []struct {
		applied bool
		says    string
	}{
		{true, "a read shows it was created: pipeline"},
		{false, "a read shows it was not created"},
	} {
		h := newHarness(t, harnessOptions{cfg: ship})
		// Someone else's pipeline on the same merge request is not this one.
		bob := newHarness(t, harnessOptions{cfg: ship, over: h.gl, token: func(gl *gitlabtest.Server) string { return gl.TokenFor("bob", "api") }})
		bob.ok("run_merge_request_pipeline", map[string]any{"project": alpha, "iid": 1})
		h.gl.ResetRequests()
		id := h.gl.ProjectID(alpha)
		h.gl.Inject(gitlabtest.Fault{Method: "POST", Path: fmt.Sprintf("/projects/%d/merge_requests/1/pipelines", id), Status: 502,
			AfterApply: c.applied, Body: `{"message":"502 Bad Gateway"}`})
		text := h.fails("run_merge_request_pipeline", map[string]any{"project": alpha, "iid": 1}, "ambiguous_outcome")
		if !strings.Contains(text, c.says) {
			t.Errorf("applied %v: %s", c.applied, text)
		}
		if n := mrPipelinePosts(h); n != 1 {
			t.Errorf("applied %v: the create was sent %d times", c.applied, n)
		}
	}
}

// GitLab starts merge request pipelines under the account's own name
// when it pushes to the source branch or opens a merge request. One
// started before the call is not taken for the lost create.
func TestRunMergeRequestPipelineSettleIgnoresPipelinesFromBefore(t *testing.T) {
	for _, c := range []struct {
		applied bool
		says    string
	}{
		{true, "a read shows it was created: pipeline"},
		{false, "a read shows it was not created"},
	} {
		h := newHarness(t, harnessOptions{cfg: ship})
		h.gl.SetAutoMRPipelines(alpha, true)
		h.ok("create_commit", map[string]any{"project": alpha, "branch": "feature/login", "message": "Push to the source",
			"actions": []map[string]any{{"action": "create", "file_path": "pushed.txt", "content": "x\n"}}})
		if _, out := h.ok("list_pipelines", map[string]any{"project": alpha, "ref": "refs/merge-requests/1/head"}); len(get(out, "pipelines").([]any)) == 0 {
			t.Fatalf("the push started no merge request pipeline: %v", out)
		}
		h.gl.ResetRequests()
		id := h.gl.ProjectID(alpha)
		h.gl.Inject(gitlabtest.Fault{Method: "POST", Path: fmt.Sprintf("/projects/%d/merge_requests/1/pipelines", id), Status: 502,
			AfterApply: c.applied, Body: `{"message":"502 Bad Gateway"}`})
		text := h.fails("run_merge_request_pipeline", map[string]any{"project": alpha, "iid": 1}, "ambiguous_outcome")
		if !strings.Contains(text, c.says) {
			t.Errorf("applied %v: %s", c.applied, text)
		}
		if n := mrPipelinePosts(h); n != 1 {
			t.Errorf("applied %v: the create was sent %d times", c.applied, n)
		}
	}
}

// A settle whose read fails leaves the outcome unknown, never safe to
// repeat.
func TestRunMergeRequestPipelineSettleThatCannotReadIsUnknown(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	id := h.gl.ProjectID(alpha)
	h.gl.Inject(gitlabtest.Fault{Method: "POST", Path: fmt.Sprintf("/projects/%d/merge_requests/1/pipelines", id), Status: 502,
		AfterApply: true, Body: `{"message":"502 Bad Gateway"}`})
	h.gl.Inject(gitlabtest.Fault{Method: "GET", Path: fmt.Sprintf("/projects/%d/pipelines", id), Query: "source=merge_request_event",
		Status: 403, Times: 10, Body: `{"message":"403 Forbidden"}`})
	text := h.fails("run_merge_request_pipeline", map[string]any{"project": alpha, "iid": 1}, "ambiguous_outcome")
	if !strings.Contains(text, "it is unknown") || strings.Contains(text, "safe") {
		t.Errorf("settled: %s", text)
	}
}

// A detached pipeline at another head than the one read says so.
func TestRunMergeRequestPipelineNamesAMovedHead(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	id := h.gl.ProjectID(alpha)
	other := strings.Repeat("f", 40)
	h.gl.Inject(gitlabtest.Fault{Method: "POST", Path: fmt.Sprintf("/projects/%d/merge_requests/1/pipelines", id), Status: 200,
		Body: fmt.Sprintf(`{"id":99001,"iid":9,"project_id":%d,"sha":%q,"ref":"refs/merge-requests/1/head","status":"created",`+
			`"source":"merge_request_event","web_url":"https://gitlab.example.com/p/-/pipelines/99001"}`, id, other)})
	text, out := h.ok("run_merge_request_pipeline", map[string]any{"project": alpha, "iid": 1})
	if get(out, "sha") != other || !strings.Contains(fmt.Sprint(get(out, "notes")), "moved after it was read") ||
		!strings.Contains(text, "moved after it was read") {
		t.Errorf("moved head not named: %v\n%s", out, text)
	}
}

func TestRetryAndCancelPipeline(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	failed := gitlabtest.PipelineFailed
	_, out := h.ok("retry_pipeline", map[string]any{"project": alpha, "pipeline_id": failed})
	if get(out, "outcome") != "retried" || get(out, "status_before") != "failed" || get(out, "status") != "running" {
		t.Errorf("retry = %v", out)
	}
	// GitLab answers before it recomputes the pipeline's status, as the
	// fake does: the cancel took effect though the answer reads running.
	_, out = h.ok("cancel_pipeline", map[string]any{"project": alpha, "pipeline_id": failed})
	if get(out, "outcome") != "canceled" || get(out, "status") != "running" ||
		!strings.Contains(fmt.Sprint(get(out, "notes")), "canceled the pipeline's jobs") {
		t.Errorf("cancel = %v", out)
	}
	if _, pl := h.ok("get_pipeline", map[string]any{"project": alpha, "pipeline_id": failed}); get(pl, "status") != "canceled" {
		t.Errorf("after the cancel, get_pipeline = %v", pl)
	}
	before := writesSent(h)
	_, out = h.ok("cancel_pipeline", map[string]any{"project": alpha, "pipeline_id": failed})
	if get(out, "outcome") != "unchanged" || writesSent(h) != before {
		t.Errorf("cancel a finished pipeline = %v", out)
	}

	// A pipeline with nothing failed is answered unchanged.
	_, mr := h.ok("get_merge_request", map[string]any{"project": alpha, "iid": 2})
	head := get(mr, "head_pipeline", "id")
	_, out = h.ok("retry_pipeline", map[string]any{"project": alpha, "pipeline_id": head})
	if get(out, "outcome") != "unchanged" || !strings.Contains(fmt.Sprint(get(out, "notes")), "retries failed and canceled jobs only") {
		t.Errorf("retry of a green pipeline = %v", out)
	}
	h.fails("retry_pipeline", map[string]any{"project": alpha, "pipeline_id": 999999}, "not_found")
}

func TestRetryAndPlayJob(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	text, out := h.ok("retry_job", map[string]any{"project": alpha, "job_id": gitlabtest.JobFailed})
	if get(out, "outcome") != "retried" || get(out, "from_job_id") != float64(gitlabtest.JobFailed) ||
		get(out, "job_id") == float64(gitlabtest.JobFailed) || get(out, "name") != "unit tests" || get(out, "status") != "pending" {
		t.Errorf("retry = %v", out)
	}
	if !strings.Contains(text, "Retried job") {
		t.Errorf("text:\n%s", text)
	}
	h.fails("retry_job", map[string]any{"project": alpha, "job_id": gitlabtest.JobManual}, "conflict")

	_, out = h.ok("play_job", map[string]any{"project": alpha, "job_id": gitlabtest.JobManual,
		"variables": []map[string]any{{"key": "TARGET", "value": "hidden-value"}}})
	if get(out, "outcome") != "played" || get(out, "status") != "pending" || strings.Join(strs(get(out, "variables")), ",") != "TARGET" {
		t.Errorf("play = %v", out)
	}
	h.fails("play_job", map[string]any{"project": alpha, "job_id": gitlabtest.JobManual}, "conflict")
	h.fails("play_job", map[string]any{"project": alpha, "job_id": gitlabtest.JobPassed,
		"variables": []map[string]any{{"key": "", "value": "x"}}}, "invalid")
}

// Job inputs are sent as given, named in the result, and checked by
// GitLab against what the job declares.
func TestRetryAndPlayJobWithInputs(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	text, out := h.ok("retry_job", map[string]any{"project": alpha, "job_id": gitlabtest.JobFailed,
		"inputs": map[string]any{"target": "staging"}})
	if get(out, "outcome") != "retried" || strings.Join(strs(get(out, "inputs")), ",") != "target" ||
		h.gl.JobInputsSent()["target"] != "staging" || !strings.Contains(text, "Inputs sent: target.") {
		t.Errorf("retry = %v, sent %v\n%s", out, h.gl.JobInputsSent(), text)
	}
	h.fails("retry_job", map[string]any{"project": alpha, "job_id": gitlabtest.JobFailed,
		"inputs": map[string]any{"undeclared": true}}, "invalid")
	_, out = h.ok("retry_job", map[string]any{"project": alpha, "job_id": gitlabtest.JobFailed,
		"inputs": map[string]any{"target": "x"}, "dry_run": true})
	if get(out, "outcome") != "dry_run" || !strings.Contains(fmt.Sprint(get(out, "would_send")), "inputs") {
		t.Errorf("dry run = %v", out)
	}

	_, out = h.ok("play_job", map[string]any{"project": alpha, "job_id": gitlabtest.JobManual,
		"inputs": map[string]any{"target": "production"}})
	if get(out, "outcome") != "played" || strings.Join(strs(get(out, "inputs")), ",") != "target" ||
		h.gl.JobInputsSent()["target"] != "production" {
		t.Errorf("play = %v, sent %v", out, h.gl.JobInputsSent())
	}
	// Without inputs, none are sent and the result names none.
	_, out = h.ok("retry_job", map[string]any{"project": alpha, "job_id": gitlabtest.JobFailed})
	if len(strs(get(out, "inputs"))) != 0 || h.gl.JobInputsSent() != nil {
		t.Errorf("no inputs: %v, sent %v", out, h.gl.JobInputsSent())
	}
}

func TestRetryJobWhoseAnswerWasLostIsSettled(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	id := h.gl.ProjectID(alpha)
	h.gl.Inject(gitlabtest.Fault{Method: "POST", Path: fmt.Sprintf("/projects/%d/jobs/%d/retry", id, gitlabtest.JobFailed), Status: 503,
		AfterApply: true, Body: `{"message":"503 Service Unavailable"}`})
	text := h.fails("retry_job", map[string]any{"project": alpha, "job_id": gitlabtest.JobFailed}, "ambiguous_outcome")
	if !strings.Contains(text, "a read shows it was created: job") {
		t.Errorf("settled: %s", text)
	}
}

// ---------------------------------------------------------- auto-merge

// cancelPosts counts the auto-merge cancels sent.
func cancelPosts(h *harness) int {
	n := 0
	for _, r := range h.gl.Requests() {
		if r.Method == "POST" && strings.HasSuffix(r.EscapedPath, "/cancel_merge_when_pipeline_succeeds") {
			n++
		}
	}
	return n
}

func TestCancelAutoMerge(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	text, out := h.ok("cancel_auto_merge", map[string]any{"project": alpha, "iid": 2})
	if get(out, "outcome") != "unchanged" || get(out, "auto_merge") != false || cancelPosts(h) != 0 {
		t.Errorf("without an auto-merge = %v, %d sent", out, cancelPosts(h))
	}
	if !strings.Contains(text, "nothing was sent") {
		t.Errorf("text:\n%s", text)
	}

	h.gl.SetAutoMerge(alpha, 2, "bob")
	before, _ := h.gl.MergeRequest(alpha, 2)
	_, dry := h.ok("cancel_auto_merge", map[string]any{"project": alpha, "iid": 2, "dry_run": true})
	if get(dry, "outcome") != "dry_run" || get(dry, "set_by") != "bob" || cancelPosts(h) != 0 {
		t.Errorf("dry run = %v, %d sent", dry, cancelPosts(h))
	}

	text, out = h.ok("cancel_auto_merge", map[string]any{"project": alpha, "iid": 2})
	after, _ := h.gl.MergeRequest(alpha, 2)
	if get(out, "outcome") != "canceled" || get(out, "auto_merge") != false || get(out, "set_by") != "bob" ||
		get(out, "state") != "opened" || after.MergeWhenPipelineSucceeds || after.MergeUser != nil {
		t.Errorf("canceled = %v; instance holds %v, %v", out, after.MergeWhenPipelineSucceeds, after.MergeUser)
	}
	if !after.UpdatedAt.After(before.UpdatedAt) || get(out, "updated_at") != after.UpdatedAt.Format(time.RFC3339Nano) {
		t.Errorf("updated_at = %v, instance %v, before %v", get(out, "updated_at"), after.UpdatedAt, before.UpdatedAt)
	}
	if !strings.Contains(text, "Canceled the auto-merge of merge request !2") {
		t.Errorf("text:\n%s", text)
	}

	_, again := h.ok("cancel_auto_merge", map[string]any{"project": alpha, "iid": 2})
	if get(again, "outcome") != "unchanged" || cancelPosts(h) != 1 {
		t.Errorf("again = %v, %d sent", again, cancelPosts(h))
	}
}

// GitLab answers 201 when it does not cancel, with the reason in the
// body; the result reads it rather than the status.
func TestCancelAutoMergeReadsTheBody(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	h.gl.SetAutoMerge(alpha, 2, "alice")
	path := fmt.Sprintf("/projects/%d/merge_requests/2/cancel_merge_when_pipeline_succeeds", h.gl.ProjectID(alpha))
	refused := `{"status":"error","message":"Can't cancel the automatic merge","http_status":406}`
	h.gl.Inject(gitlabtest.Fault{Method: "POST", Path: path, Status: 201, Body: refused})
	text := h.fails("cancel_auto_merge", map[string]any{"project": alpha, "iid": 2}, "conflict")
	if !strings.Contains(text, "still set") || !strings.Contains(text, "Can't cancel the automatic merge") {
		t.Errorf("refusal: %s", text)
	}
	// The same answer to a cancel that landed, as when one raced with
	// another: the read afterwards shows none set.
	h.gl.Inject(gitlabtest.Fault{Method: "POST", Path: path, Status: 201, Body: refused, AfterApply: true})
	text, out := h.ok("cancel_auto_merge", map[string]any{"project": alpha, "iid": 2})
	if get(out, "outcome") != "unchanged" || get(out, "auto_merge") != false || !strings.Contains(text, "in the meantime") {
		t.Errorf("raced = %v\n%s", out, text)
	}
}

// Whoever may merge it, or its author, cancels; GitLab answers anyone
// else 401, which is the account's role, not its token.
func TestCancelAutoMergePermission(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	h.gl.SetAutoMerge(alpha, 1, "alice")
	dave := newHarness(t, harnessOptions{cfg: ship, over: h.gl, token: func(gl *gitlabtest.Server) string { return gl.TokenFor("dave", "api") }})
	dave.fails("cancel_auto_merge", map[string]any{"project": alpha, "iid": 1}, "forbidden")
	if mr, _ := h.gl.MergeRequest(alpha, 1); !mr.MergeWhenPipelineSucceeds {
		t.Fatal("a refused cancel canceled")
	}
	// bob wrote !1 and develops the project, so he may not merge into
	// its protected main, and may cancel all the same.
	bob := newHarness(t, harnessOptions{cfg: ship, over: h.gl, token: func(gl *gitlabtest.Server) string { return gl.TokenFor("bob", "api") }})
	_, out := bob.ok("cancel_auto_merge", map[string]any{"project": alpha, "iid": 1})
	if get(out, "outcome") != "canceled" {
		t.Errorf("the author's cancel = %v", out)
	}
}

// --------------------------------------------------------- suggestions

// login is src/login.go on feature/login, the fixtures' merge request
// source branch.
const loginFile = "src/login.go"

// suggest adds a suggestion by bob on a merge request and returns its
// comment's and its own id.
func suggest(h *harness, iid int64, from, to int, replacement string) (int64, int64) {
	h.t.Helper()
	note, id, ok := h.gl.AddSuggestion(alpha, iid, "bob", loginFile, from, to, replacement)
	if !ok {
		h.t.Fatalf("no suggestion on !%d lines %d-%d", iid, from, to)
	}
	return note, id
}

// applyPuts counts the applies sent.
func applyPuts(h *harness) int {
	n := 0
	for _, r := range h.gl.Requests() {
		if r.Method == "PUT" && strings.HasPrefix(r.EscapedPath, "/api/v4/suggestions/") {
			n++
		}
	}
	return n
}

func TestListDiscussionsNamesSuggestions(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	note, id := suggest(h, 1, 3, 3, "// login signs a user in.\n")
	text, out := h.ok("list_discussions", map[string]any{"project": alpha, "type": "merge_request", "iid": 1})
	var found any
	for _, th := range get(out, "threads").([]any) {
		for _, n := range get(th, "notes").([]any) {
			if get(n, "id") == float64(note) {
				found = get(n, "suggestions")
			} else if len(get(n, "suggestions").([]any)) != 0 {
				t.Errorf("comment %v has suggestions %v", get(n, "id"), get(n, "suggestions"))
			}
		}
	}
	want := []any{map[string]any{"id": float64(id), "from_line": float64(3), "to_line": float64(3), "appliable": true, "applied": false}}
	if fmt.Sprint(found) != fmt.Sprint(want) {
		t.Errorf("suggestions = %v, want %v", found, want)
	}
	if !strings.Contains(text, fmt.Sprintf("Suggestions in comment %d, for apply_suggestions: %d (line 3, appliable).", note, id)) {
		t.Errorf("text:\n%s", text)
	}
}

func TestApplySuggestion(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	note, id := suggest(h, 1, 3, 3, "// login signs a user in.\n")
	before := branchHead(h, "feature/login")

	_, dry := h.ok("apply_suggestions", map[string]any{"project": alpha, "iid": 1, "ids": []int64{id}, "dry_run": true})
	if get(dry, "outcome") != "dry_run" || get(dry, "head_before") != before || applyPuts(h) != 0 {
		t.Errorf("dry run = %v, %d sent", dry, applyPuts(h))
	}

	text, out := h.ok("apply_suggestions", map[string]any{"project": alpha, "iid": 1, "ids": []int64{id},
		"commit_message": "Apply %{suggestions_count} suggestion on %{branch_name}"})
	head := branchHead(h, "feature/login")
	if get(out, "outcome") != "applied" || get(out, "head_before") != before || get(out, "head") != head || head == before ||
		get(out, "source_branch") != "feature/login" || get(out, "source_project") != alpha {
		t.Errorf("applied = %v; branch at %s, was %s", out, head, before)
	}
	want := map[string]any{"id": float64(id), "note_id": float64(note), "file_path": loginFile, "from_line": float64(3),
		"to_line": float64(3), "applied": true}
	if fmt.Sprint(get(out, "suggestions", 0)) != fmt.Sprint(want) {
		t.Errorf("suggestion = %v, want %v", get(out, "suggestions", 0), want)
	}
	if file, _ := h.gl.FileAt(alpha, "feature/login", loginFile); file != "package main\n\n// login signs a user in.\nfunc login() {}\n" {
		t.Errorf("file = %q", file)
	}
	if c, _ := h.gl.BranchHead(alpha, "feature/login"); c.Message != "Apply 1 suggestion on feature/login" ||
		c.AuthorName != "Alice Example" {
		t.Errorf("commit %q by %s", c.Message, c.AuthorName)
	}
	if !strings.Contains(text, "Applied 1 suggestion(s) of merge request !1") || !strings.Contains(text, head) {
		t.Errorf("text:\n%s", text)
	}
	if applyPuts(h) != 1 {
		t.Errorf("%d applies sent", applyPuts(h))
	}

	// Applied already: nothing is sent, and the head is reported.
	_, again := h.ok("apply_suggestions", map[string]any{"project": alpha, "iid": 1, "ids": []int64{id}})
	if get(again, "outcome") != "unchanged" || get(again, "head") != head || applyPuts(h) != 1 {
		t.Errorf("again = %v, %d sent", again, applyPuts(h))
	}
}

// Several suggestions go in one commit through the batch route.
func TestApplySuggestionsInOneCommit(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	_, first := suggest(h, 1, 1, 1, "package login\n")
	_, second := suggest(h, 1, 3, 4, "// login is real now.\nfunc login() { return }\n")
	before := branchHead(h, "feature/login")
	_, out := h.ok("apply_suggestions", map[string]any{"project": alpha, "iid": 1, "ids": []int64{second, first}})
	if get(out, "outcome") != "applied" || get(out, "suggestions", 0, "applied") != true || get(out, "suggestions", 1, "applied") != true {
		t.Errorf("applied = %v", out)
	}
	file, _ := h.gl.FileAt(alpha, "feature/login", loginFile)
	if file != "package login\n\n// login is real now.\nfunc login() { return }\n" {
		t.Errorf("file = %q", file)
	}
	c, _ := h.gl.BranchHead(alpha, "feature/login")
	if len(c.ParentIDs) != 1 || c.ParentIDs[0] != before || c.Message != "Apply 2 suggestion(s) to 1 file(s)" {
		t.Errorf("head %v", c)
	}
	var paths []string
	for _, r := range h.gl.Requests() {
		if r.Method == "PUT" {
			paths = append(paths, r.EscapedPath)
		}
	}
	if fmt.Sprint(paths) != "[/api/v4/suggestions/batch_apply]" {
		t.Errorf("sent %v", paths)
	}
}

// What the server can tell before sending is refused before sending.
func TestApplySuggestionsRefusedBeforeSending(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	_, id := suggest(h, 1, 3, 3, "// one\n")
	_, onTwo := suggest(h, 2, 3, 3, "// two\n")
	_, applied := suggest(h, 1, 1, 1, "package login\n")
	h.ok("apply_suggestions", map[string]any{"project": alpha, "iid": 1, "ids": []int64{applied}})
	sent := applyPuts(h)
	for _, c := range []struct {
		name  string
		iid   int
		ids   []int64
		class string
	}{
		{"no ids", 1, []int64{}, "invalid"},
		{"an id twice", 1, []int64{id, id}, "invalid"},
		{"not a positive id", 1, []int64{0}, "invalid"},
		{"another merge request's", 1, []int64{onTwo}, "not_found"},
		{"no such suggestion", 1, []int64{999999}, "not_found"},
		{"one applied among others", 1, []int64{id, applied}, "conflict"},
	} {
		text, _, isErr := h.call("apply_suggestions", map[string]any{"project": alpha, "iid": c.iid, "ids": c.ids})
		if !isErr || !strings.HasPrefix(text, "["+c.class+"]") {
			t.Errorf("%s: %s", c.name, text)
		}
	}
	h.ok("update_merge_request", map[string]any{"project": alpha, "iid": 2, "updated_at": mrWitness(h, 2), "state": "close"})
	h.fails("apply_suggestions", map[string]any{"project": alpha, "iid": 2, "ids": []int64{onTwo}}, "conflict")
	if applyPuts(h) != sent {
		t.Errorf("%d applies sent by refused calls", applyPuts(h)-sent)
	}
}

// A protected source branch takes code only through a merge request, so
// a suggestion is not committed to it (§4.4), even where GitLab would.
func TestApplySuggestionsRefusesAProtectedSourceBranch(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	_, out := h.ok("create_merge_request", map[string]any{"project": alpha, "source_branch": "release/1.0", "target_branch": "main",
		"title": "Release"})
	iid := int64(get(out, "iid").(float64))
	_, id, ok := h.gl.AddSuggestion(alpha, iid, "bob", "src/main.go", 3, 3, "func main() { run() }\n")
	if !ok {
		t.Fatal("no suggestion")
	}
	text := h.fails("apply_suggestions", map[string]any{"project": alpha, "iid": iid, "ids": []int64{id}}, "blocked")
	if !strings.Contains(text, "protected") || applyPuts(h) != 0 {
		t.Errorf("refusal: %s; %d sent", text, applyPuts(h))
	}
}

// A merge request from a fork commits to the fork, which is held to the
// write allow-list too.
func TestApplySuggestionsHoldsTheForkToTheAllowList(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: config.Config{EnableShip: true, WriteNamespaces: []string{alpha}}})
	_, id := suggest(h, 1, 3, 3, "// one\n")
	h.gl.SetMRSourceProject(alpha, 1, gitlabtest.ProjectBeta)
	text := h.fails("apply_suggestions", map[string]any{"project": alpha, "iid": 1, "ids": []int64{id}}, "blocked")
	if !strings.Contains(text, config.EnvWriteNamespaces) || applyPuts(h) != 0 {
		t.Errorf("refusal: %s; %d sent", text, applyPuts(h))
	}
}

// GitLab's own refusals name the reason; right after a push it refuses
// every suggestion until it catches up, which is said.
func TestApplySuggestionsRefusedByGitLab(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	_, id := suggest(h, 1, 3, 3, "// one\n")
	if _, ok := h.gl.PushTo(alpha, "feature/login"); !ok {
		t.Fatal("push")
	}
	text := h.fails("apply_suggestions", map[string]any{"project": alpha, "iid": 1, "ids": []int64{id}}, "conflict")
	if !strings.Contains(text, "A file has been changed.") || !strings.Contains(text, "try again shortly") {
		t.Errorf("after a push: %s", text)
	}

	_, other := suggest(h, 1, 3, 3, "// two\n")
	dave := newHarness(t, harnessOptions{cfg: ship, over: h.gl, token: func(gl *gitlabtest.Server) string { return gl.TokenFor("dave", "api") }})
	text = dave.fails("apply_suggestions", map[string]any{"project": alpha, "iid": 1, "ids": []int64{other}}, "forbidden")
	if !strings.Contains(text, "may not push to the source branch") {
		t.Errorf("no push rights: %s", text)
	}
	if s, _ := h.gl.Suggestion(other); s.Applied {
		t.Error("a refused apply applied")
	}
}

// A lost answer is settled by reading, never by applying again.
func TestApplySuggestionsWhoseAnswerWasLost(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	_, id := suggest(h, 1, 3, 3, "// one\n")
	path := fmt.Sprintf("/suggestions/%d/apply", id)
	h.gl.Inject(gitlabtest.Fault{Method: "PUT", Path: path, Status: 500, AfterApply: true, Body: `{"message":"500 Internal Server Error"}`})
	text := h.fails("apply_suggestions", map[string]any{"project": alpha, "iid": 1, "ids": []int64{id}}, "ambiguous_outcome")
	if !strings.Contains(text, "a read shows every suggestion applied") || !strings.Contains(text, branchHead(h, "feature/login")) ||
		!strings.Contains(text, "Do not repeat the call") || applyPuts(h) != 1 {
		t.Errorf("landed: %s; %d sent", text, applyPuts(h))
	}

	_, other := suggest(h, 2, 3, 3, "// two\n")
	h.gl.Inject(gitlabtest.Fault{Method: "PUT", Path: fmt.Sprintf("/suggestions/%d/apply", other), Status: 502,
		Body: `{"message":"502 Bad Gateway"}`})
	text = h.fails("apply_suggestions", map[string]any{"project": alpha, "iid": 2, "ids": []int64{other}}, "ambiguous_outcome")
	if !strings.Contains(text, "a read shows none applied") || !strings.Contains(text, "calling again is safe") || applyPuts(h) != 2 {
		t.Errorf("not landed: %s; %d sent", text, applyPuts(h))
	}

	// None applied, but the branch moved: someone else's push or this
	// commit cannot be told apart.
	_, third := suggest(h, 3, 3, 3, "// three\n")
	h.gl.Inject(gitlabtest.Fault{Method: "PUT", Path: fmt.Sprintf("/suggestions/%d/apply", third), Status: 502,
		Body: `{"message":"502 Bad Gateway"}`, Before: func() { h.gl.PushTo(alpha, "feature/login") }})
	text = h.fails("apply_suggestions", map[string]any{"project": alpha, "iid": 3, "ids": []int64{third}}, "ambiguous_outcome")
	if !strings.Contains(text, "0 of 1 applied") || !strings.Contains(text, "do not repeat the call") {
		t.Errorf("unknown: %s", text)
	}
}

// ---------------------------------------------------------- destructive

func branchHead(h *harness, name string) string {
	h.t.Helper()
	c, ok := h.gl.BranchHead(alpha, name)
	if !ok {
		h.t.Fatalf("no branch %s", name)
	}
	return c.ID
}

func TestDeleteBranch(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: destructive})
	feature := "feature/login"
	sha := branchHead(h, feature)
	before := writesSent(h)
	text := h.fails("delete_branch", map[string]any{"project": alpha, "branch": feature, "sha": sha, "unmerged": true}, "blocked")
	if !strings.Contains(text, "confirm: true") {
		t.Errorf("without confirm: %s", text)
	}
	h.fails("delete_branch", map[string]any{"project": alpha, "branch": "main", "sha": branchHead(h, "main"), "confirm": true}, "blocked")
	h.fails("delete_branch", map[string]any{"project": alpha, "branch": "release/1.0", "sha": branchHead(h, "release/1.0"), "confirm": true},
		"blocked")
	text = h.fails("delete_branch", map[string]any{"project": alpha, "branch": feature, "sha": sha, "confirm": true}, "blocked")
	if !strings.Contains(text, "unmerged: true") {
		t.Errorf("unmerged: %s", text)
	}
	h.fails("delete_branch", map[string]any{"project": alpha, "branch": feature, "sha": strings.Repeat("0", 40), "confirm": true,
		"unmerged": true}, "stale")
	h.fails("delete_branch", map[string]any{"project": alpha, "branch": feature, "confirm": true}, "invalid")
	_, dry := h.ok("delete_branch", map[string]any{"project": alpha, "branch": feature, "sha": sha, "unmerged": true, "dry_run": true})
	if get(dry, "outcome") != "dry_run" {
		t.Errorf("dry run = %v", dry)
	}
	if writesSent(h) != before {
		t.Fatal("a refusal or a dry run wrote")
	}

	text, out := h.ok("delete_branch", map[string]any{"project": alpha, "branch": feature, "sha": sha, "unmerged": true, "confirm": true})
	if get(out, "outcome") != "deleted" || get(out, "merged") != false || !strings.Contains(text, "finds no such branch") {
		t.Errorf("delete = %v\n%s", out, text)
	}
	if _, ok := h.gl.BranchHead(alpha, feature); ok {
		t.Error("the branch is still there")
	}
	h.fails("delete_branch", map[string]any{"project": alpha, "branch": feature, "sha": sha, "unmerged": true, "confirm": true}, "not_found")
}

func TestDeleteMergedBranchNeedsNoOverride(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: destructive})
	h.ok("create_branch", map[string]any{"project": alpha, "branch": "merged-topic"})
	_, out := h.ok("delete_branch", map[string]any{"project": alpha, "branch": "merged-topic", "sha": branchHead(h, "merged-topic"),
		"confirm": true})
	if get(out, "outcome") != "deleted" || get(out, "merged") != true {
		t.Errorf("delete = %v", out)
	}
}

// commentOf finds a note on issue 1 by its author, as list_discussions
// shows it.
func commentOf(h *harness, author string, system bool) (id float64, updatedAt string) {
	h.t.Helper()
	_, out := h.ok("list_discussions", map[string]any{"project": alpha, "type": "issue", "iid": 1, "include_system": true})
	for _, th := range get(out, "threads").([]any) {
		for _, n := range get(th, "notes").([]any) {
			if get(n, "author", "username") == author && get(n, "system") == system {
				return get(n, "id").(float64), get(n, "updated_at").(string)
			}
		}
	}
	h.t.Fatalf("no note by %s", author)
	return 0, ""
}

func TestDeleteComment(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: destructive})
	id, at := commentOf(h, gitlabtest.DefaultUser, false)
	bobs, bobsAt := commentOf(h, "bob", false)
	system, systemAt := commentOf(h, gitlabtest.DefaultUser, true)
	args := func(id float64, at string) map[string]any {
		return map[string]any{"project": alpha, "type": "issue", "iid": 1, "note_id": id, "updated_at": at, "confirm": true}
	}
	before := writesSent(h)
	text := h.fails("delete_comment", args(bobs, bobsAt), "blocked")
	if !strings.Contains(text, "@bob") {
		t.Errorf("another's comment: %s", text)
	}
	h.fails("delete_comment", args(system, systemAt), "invalid")
	h.fails("delete_comment", args(id, "2020-01-01T00:00:00Z"), "stale")
	h.fails("delete_comment", args(id, "yesterday"), "invalid")
	if writesSent(h) != before {
		t.Fatal("a refusal wrote")
	}

	text, out := h.ok("delete_comment", args(id, at))
	if get(out, "outcome") != "deleted" || !strings.Contains(text, "finds no such comment") {
		t.Errorf("delete = %v\n%s", out, text)
	}
	// The witness went as If-Unmodified-Since, stretched to the end of
	// its millisecond, which GitLab keeps to the microsecond.
	witness, _ := time.Parse(time.RFC3339Nano, at)
	want := witness.Add(time.Millisecond - time.Microsecond).UTC().Format(time.RFC3339Nano)
	sent := ""
	for _, r := range h.gl.Requests() {
		if r.Method == "DELETE" {
			sent = r.IfUnmodifiedSince
		}
	}
	if sent != want {
		t.Errorf("If-Unmodified-Since = %q, want %q", sent, want)
	}
	h.fails("delete_comment", args(id, at), "not_found")
}

func TestDeleteCommentEditedAfterTheReadIsStale(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: destructive})
	id, at := commentOf(h, gitlabtest.DefaultUser, false)
	// An edit between the server's read and its delete: GitLab's own
	// check refuses it with 412.
	project := h.gl.ProjectID(alpha)
	h.gl.Inject(gitlabtest.Fault{Method: "DELETE", Path: fmt.Sprintf("/projects/%d/issues/1/notes/", project), Status: 412,
		Body: `{"message":"412 Precondition Failed"}`})
	h.fails("delete_comment", map[string]any{"project": alpha, "type": "issue", "iid": 1, "note_id": id, "updated_at": at, "confirm": true},
		"stale")
	if !h.gl.TouchNote(alpha, "issue", 1, int64(id), time.Now().Add(time.Hour)) {
		t.Fatal("touch")
	}
	h.fails("delete_comment", map[string]any{"project": alpha, "type": "issue", "iid": 1, "note_id": id, "updated_at": at, "confirm": true},
		"stale")
}

// gitlab.com refuses pipeline variables on a new project to everyone:
// 400 on a pipeline, a bare 403 on a manual job (phase 3 live run). No
// argument fixes that, so it is [forbidden] naming the setting.
func TestPipelineVariablesRefusedByTheProject(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	id := h.gl.ProjectID(alpha)
	h.gl.Inject(gitlabtest.Fault{Method: "POST", Path: fmt.Sprintf("/projects/%d/pipeline", id), Status: 400,
		Body: `{"message":{"base":["Insufficient permissions to set pipeline variables"]}}`})
	text := h.fails("run_pipeline", map[string]any{"project": alpha, "ref": "main",
		"variables": []map[string]any{{"key": "A", "value": "1"}}}, "forbidden")
	if !strings.Contains(text, "minimum role to use pipeline variables") {
		t.Errorf("pipeline: %s", text)
	}
	h.gl.Inject(gitlabtest.Fault{Method: "POST", Path: fmt.Sprintf("/projects/%d/jobs/%d/play", id, gitlabtest.JobManual), Status: 403,
		Body: `{"message":"403 Forbidden"}`})
	text = h.fails("play_job", map[string]any{"project": alpha, "job_id": gitlabtest.JobManual,
		"variables": []map[string]any{{"key": "A", "value": "1"}}}, "forbidden")
	if !strings.Contains(text, "without variables") {
		t.Errorf("job: %s", text)
	}
}

func TestDeleteBranchTakesTheShortSHAListBranchesShows(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: destructive})
	head := branchHead(h, "feature/login")
	h.fails("delete_branch", map[string]any{"project": alpha, "branch": "feature/login", "sha": head[:6], "unmerged": true,
		"confirm": true}, "invalid")
	_, out := h.ok("delete_branch", map[string]any{"project": alpha, "branch": "feature/login", "sha": " " + head[:12] + " ",
		"unmerged": true, "confirm": true})
	if get(out, "outcome") != "deleted" {
		t.Errorf("delete = %v", out)
	}
}

// A delete whose answer is lost is sent again, and the repeat finds
// nothing: the read afterwards says the thing is gone.
func TestDeleteWhoseAnswerWasLostIsReportedDeleted(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: destructive})
	id := h.gl.ProjectID(alpha)
	h.gl.Inject(gitlabtest.Fault{Method: "DELETE", Path: fmt.Sprintf("/projects/%d/repository/branches/", id), Status: 502,
		AfterApply: true, Body: `{"message":"502 Bad Gateway"}`})
	_, out := h.ok("delete_branch", map[string]any{"project": alpha, "branch": "feature/login", "sha": branchHead(h, "feature/login"),
		"unmerged": true, "confirm": true})
	if get(out, "outcome") != "deleted" || !strings.Contains(fmt.Sprint(get(out, "notes")), "it is gone") {
		t.Errorf("lost delete = %v", out)
	}
}

func TestAutoMergeWhoseAnswerWasLostReadsItBack(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	h.gl.SetHeadPipelineStatus(alpha, 2, "running")
	id := h.gl.ProjectID(alpha)
	// Every attempt lands and every answer is lost.
	h.gl.Inject(gitlabtest.Fault{Method: "PUT", Path: fmt.Sprintf("/projects/%d/merge_requests/2/merge", id), Status: 502, Times: 10,
		AfterApply: true, Body: `{"message":"502 Bad Gateway"}`})
	_, out := h.ok("merge_merge_request", map[string]any{"project": alpha, "iid": 2, "sha": mrSHA(h, 2), "auto_merge": true})
	if get(out, "outcome") != "auto_merge_set" {
		t.Errorf("lost auto merge = %v", out)
	}
}

// Only GitLab's bare 403 is offered the variables setting as a cause;
// a refusal with a reason keeps it.
func TestPlayJobKeepsGitLabsReason(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: ship})
	id := h.gl.ProjectID(alpha)
	h.gl.Inject(gitlabtest.Fault{Method: "POST", Path: fmt.Sprintf("/projects/%d/jobs/%d/play", id, gitlabtest.JobManual), Status: 403,
		Body: `{"message":"403 Forbidden - You are not allowed to deploy to production"}`})
	text := h.fails("play_job", map[string]any{"project": alpha, "job_id": gitlabtest.JobManual,
		"variables": []map[string]any{{"key": "A", "value": "1"}}}, "forbidden")
	if strings.Contains(text, "pipeline variables") || !strings.Contains(text, "deploy to production") {
		t.Errorf("reason lost: %s", text)
	}
}
