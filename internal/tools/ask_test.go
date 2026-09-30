package tools

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/gitlab-mcp/v2/internal/gapi/gitlabtest"
)

// The protocols a question goes out on: before 2026-07-28 the SDK asks
// with elicitation/create inside the call; from it, the call returns the
// question and comes back with the answer (§4.12).
var protocols = []string{"2025-06-18", "2025-11-25", "2026-07-28"}

// answerer answers the questions a test client is asked, and keeps them.
type answerer struct {
	mu        sync.Mutex
	questions []*mcp.ElicitParams
	answer    func() (*mcp.ElicitResult, error)
}

func (p *answerer) handle(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
	p.mu.Lock()
	p.questions = append(p.questions, req.Params)
	answer := p.answer
	p.mu.Unlock()
	return answer()
}

func (p *answerer) asked() []*mcp.ElicitParams {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.questions)
}

// then forgets the questions so far and answers the next ones with a.
func (p *answerer) then(a func() (*mcp.ElicitResult, error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.questions, p.answer = nil, a
}

func says(action string) func() (*mcp.ElicitResult, error) {
	return func() (*mcp.ElicitResult, error) { return &mcp.ElicitResult{Action: action}, nil }
}

var (
	accepts  = says("accept")
	declines = says("decline")
)

// askingHarness connects a client that declares form elicitation and answers
// with p, on protocol, to every tool.
func askingHarness(t *testing.T, protocol string, p *answerer, o harnessOptions) *harness {
	t.Helper()
	o.cfg, o.person, o.protocol = full, p, protocol
	return newHarness(t, o)
}

// askCase makes what a call needs, answering yes to anything it asks on
// the way, and returns arguments that clear the tool's own guards and
// reach its write, with words its question must carry.
type askCase struct {
	setup func(h *harness) map[string]any
	shows []string
}

var askCases = map[string]askCase{
	"merge_merge_request": {
		setup: func(h *harness) map[string]any {
			return map[string]any{"project": alpha, "iid": 1, "sha": mrSHA(h, 1), "squash": true}
		},
		shows: []string{"merge merge request !1 in `" + alpha + "`?", "at head ", "squashes its commits"},
	},
	"approve_merge_request": {
		setup: func(h *harness) map[string]any { return map[string]any{"project": alpha, "iid": 2, "sha": mrSHA(h, 2)} },
		shows: []string{"approve merge request !2", "counts toward the project's merge rules"},
	},
	"play_job": {
		setup: func(*harness) map[string]any {
			return map[string]any{"project": alpha, "job_id": gitlabtest.JobManual,
				"variables": []map[string]any{{"key": "TARGET", "value": "hidden-value"}}}
		},
		shows: []string{"run the manual job", "variables: `TARGET`"},
	},
	"run_pipeline": {
		setup: func(*harness) map[string]any { return map[string]any{"project": alpha, "ref": "main"} },
		shows: []string{"run a pipeline on `main`", "the project's default branch"},
	},
	"create_release": {
		setup: func(*harness) map[string]any {
			return map[string]any{"project": alpha, "tag_name": "v9.9.9", "ref": "main"}
		},
		shows: []string{"publish a release for the tag `v9.9.9`", "creates the tag from `main`"},
	},
	"create_tag": {
		setup: func(*harness) map[string]any {
			return map[string]any{"project": alpha, "tag_name": "v9", "ref": "main"}
		},
		shows: []string{"create the tag `v9` at `main`"},
	},
	"update_issue": {
		setup: func(h *harness) map[string]any {
			_, out := h.ok("update_issue", map[string]any{"project": alpha, "iid": 2, "updated_at": issueWitness(h, 2), "confidential": true})
			return map[string]any{"project": alpha, "iid": 2, "updated_at": get(out, "updated_at"), "confidential": false}
		},
		shows: []string{"make the confidential issue #2", "public?"},
	},
	"delete_branch": {
		setup: func(h *harness) map[string]any {
			return map[string]any{"project": alpha, "branch": "feature/login", "sha": branchHead(h, "feature/login"), "unmerged": true,
				"confirm": true}
		},
		shows: []string{"delete the branch `feature/login`", "not merged into the default branch"},
	},
	"delete_tag": {
		setup: func(h *harness) map[string]any {
			_, out := h.ok("list_tags", map[string]any{"project": alpha, "search": "^" + gitlabtest.TagPlain})
			return map[string]any{"project": alpha, "tag_name": gitlabtest.TagPlain, "sha": get(out, "tags", 0, "commit_id"), "confirm": true}
		},
		shows: []string{"delete the tag `" + gitlabtest.TagPlain + "`", "at commit "},
	},
	"delete_label": {
		setup: func(h *harness) map[string]any {
			_, out := h.ok("create_label", map[string]any{"project": alpha, "name": "triage", "color": "#aa0000"})
			return map[string]any{"project": alpha, "label_id": get(out, "label", "id"), "version": get(out, "label", "version"), "confirm": true}
		},
		shows: []string{"delete the label `triage`", "cannot be restored"},
	},
	"delete_milestone": {
		setup: func(h *harness) map[string]any {
			_, out := h.ok("create_milestone", map[string]any{"project": alpha, "title": "Q4"})
			return map[string]any{"project": alpha, "milestone_id": get(out, "milestone", "id"),
				"updated_at": get(out, "milestone", "updated_at"), "confirm": true}
		},
		shows: []string{"delete the milestone `Q4`"},
	},
	"delete_wiki_page": {
		setup: func(h *harness) map[string]any {
			page, _ := h.gl.WikiPage(alpha, "home")
			return map[string]any{"project": alpha, "slug": "home", "content_sha256": hash(page.Content), "confirm": true}
		},
		shows: []string{"delete the wiki page", "slug: `home`"},
	},
	"delete_comment": {
		setup: func(h *harness) map[string]any {
			id, at := commentOf(h, gitlabtest.DefaultUser, false)
			return map[string]any{"project": alpha, "type": "issue", "iid": 1, "note_id": id, "updated_at": at, "confirm": true}
		},
		shows: []string{"delete a comment on issue #1", "by `" + gitlabtest.DefaultUser + "`", "text: `"},
	},
	"delete_snippet": {
		setup: func(h *harness) map[string]any {
			_, out := h.ok("get_snippet", map[string]any{"snippet_id": gitlabtest.SnippetPersonal})
			return map[string]any{"snippet_id": gitlabtest.SnippetPersonal, "updated_at": get(out, "updated_at"), "confirm": true}
		},
		shows: []string{"delete snippet 80002, `Personal snippet`, in your personal snippets", "It has 1 file:", "file `scratch.txt`"},
	},
}

// asksPerson is every tool registered to ask, from the definitions
// themselves.
func asksPerson() []string {
	var out []string
	for _, d := range definitions() {
		if d.spec().Asks != "" {
			out = append(out, d.spec().Name)
		}
	}
	return out
}

// Every tool that asks puts one question to the person when the client
// can ask: declined, it writes nothing; accepted, it writes. The list
// comes from the definitions, with a floor.
func TestEveryAskingToolAsksThePerson(t *testing.T) {
	names := asksPerson()
	if len(names) < 14 {
		t.Fatalf("found %d tools that ask, below the floor of 14: %v", len(names), names)
	}
	for _, name := range names {
		c, ok := askCases[name]
		if !ok {
			t.Errorf("%s asks the person and has no case here: add one", name)
			continue
		}
		for _, protocol := range protocols {
			t.Run(name+"/"+protocol, func(t *testing.T) {
				p := &answerer{answer: accepts}
				h := askingHarness(t, protocol, p, harnessOptions{})
				args := c.setup(h)
				h.gl.ResetRequests()
				p.then(declines)
				text := h.fails(name, args, "blocked")
				if !strings.Contains(text, "not confirmed by the person") || strings.Contains(text, "declined") {
					t.Errorf("refusal: %s", text)
				}
				if n := writesSent(h); n != 0 {
					t.Fatalf("declined, and %d writes were sent", n)
				}
				qs := p.asked()
				if len(qs) != 1 {
					t.Fatalf("asked %d questions", len(qs))
				}
				q := qs[0]
				if q.Mode != "form" || !strings.HasPrefix(q.Message, name+": ") || !isEmptyForm(q.RequestedSchema) {
					t.Errorf("question %+v", q)
				}
				for _, s := range c.shows {
					if !strings.Contains(q.Message, s) {
						t.Errorf("the question does not show %q:\n%s", s, q.Message)
					}
				}
				if strings.Contains(q.Message, "hidden-value") {
					t.Errorf("the question shows a variable's value:\n%s", q.Message)
				}

				p.then(accepts)
				h.ok(name, args)
				if writesSent(h) == 0 {
					t.Fatal("accepted, and nothing was sent")
				}
			})
		}
	}
}

// isEmptyForm says the question's form has no fields: accepting it is the
// confirmation (§4.12).
func isEmptyForm(schema any) bool {
	s, ok := schema.(map[string]any)
	if !ok {
		return false
	}
	props, ok := s["properties"].(map[string]any)
	return s["type"] == "object" && ok && len(props) == 0
}

// Only an accept confirms. Whatever else comes back, nothing is sent,
// and the refusal never says the person declined: a client may answer
// without showing anyone anything.
func TestOnlyAnAcceptConfirms(t *testing.T) {
	for _, protocol := range protocols {
		for _, tc := range []struct {
			name   string
			answer func() (*mcp.ElicitResult, error)
		}{
			{"decline", declines},
			{"cancel", says("cancel")},
			{"error", func() (*mcp.ElicitResult, error) { return nil, errors.New("no dialog here") }},
		} {
			t.Run(protocol+"/"+tc.name, func(t *testing.T) {
				p := &answerer{answer: accepts}
				h := askingHarness(t, protocol, p, harnessOptions{})
				args := askCases["merge_merge_request"].setup(h)
				h.gl.ResetRequests()
				p.then(tc.answer)
				res, err := h.cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "merge_merge_request", Arguments: args})
				if err != nil {
					// From 2026-07-28 the client fulfills the question itself, and
					// an answer it cannot give fails there; the call never comes
					// back.
					if protocol != "2026-07-28" {
						t.Fatalf("calling: %v", err)
					}
				} else if text := textOf(res); !res.IsError || !strings.HasPrefix(text, "[blocked]") ||
					!strings.Contains(text, "not confirmed by the person") || strings.Contains(text, "declined") ||
					strings.Contains(text, "no dialog here") {
					t.Errorf("result: %s", text)
				}
				if n := writesSent(h); n != 0 {
					t.Fatalf("%d writes", n)
				}
			})
		}
	}
}

// A client that declares no elicitation gets no question, unless
// GITLAB_MCP_REQUIRE_PROMPT refuses what cannot be asked.
func TestNoPromptPossible(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: full})
	h.ok("merge_merge_request", askCases["merge_merge_request"].setup(h))

	strict := full
	strict.RequirePrompt = true
	for _, name := range asksPerson() {
		t.Run(name, func(t *testing.T) {
			// The setup's own writes are made where they are not asked about.
			h := newHarness(t, harnessOptions{cfg: full})
			args := askCases[name].setup(h)
			h.gl.ResetRequests()
			sh := newHarness(t, harnessOptions{cfg: strict, over: h.gl})
			text := sh.fails(name, args, "blocked")
			if !strings.Contains(text, "GITLAB_MCP_REQUIRE_PROMPT") {
				t.Errorf("%s", text)
			}
			if n := writesSent(sh); n != 0 {
				t.Errorf("%d writes", n)
			}
		})
	}
}

// A dry run never asks, a call a guard refuses is refused before anyone
// is asked — a job GitLab will not play among them — and a pipeline on a
// ref nothing protects or an issue that stays confidential asks nothing.
func TestNothingIsAskedThatWouldNotBeWritten(t *testing.T) {
	for _, protocol := range protocols {
		p := &answerer{answer: accepts}
		for name, c := range askCases {
			h := askingHarness(t, protocol, p, harnessOptions{})
			args := c.setup(h)
			p.then(accepts)
			dry := maps.Clone(args)
			dry["dry_run"] = true
			h.ok(name, dry)
			if _, ok := args["confirm"]; ok {
				undone := maps.Clone(args)
				undone["confirm"] = false
				h.fails(name, undone, "blocked")
			}
			if n := len(p.asked()); n != 0 {
				t.Errorf("%s/%s: asked %d questions", name, protocol, n)
			}
		}
		h := askingHarness(t, protocol, p, harnessOptions{})
		p.then(accepts)
		h.ok("run_pipeline", map[string]any{"project": alpha, "ref": "feature/login"})
		h.ok("update_issue", map[string]any{"project": alpha, "iid": 2, "updated_at": issueWitness(h, 2), "confidential": true})
		h.fails("play_job", map[string]any{"project": alpha, "job_id": gitlabtest.JobFailed}, "conflict")
		if n := len(p.asked()); n != 0 {
			t.Errorf("%s: asked %d questions", protocol, n)
		}
	}
}

// mrtr connects on 2026-07-28 with the client's own round trip off, so
// the test answers, forges and replays by hand.
func mrtr(t *testing.T) *harness {
	t.Helper()
	return askingHarness(t, "2026-07-28", &answerer{answer: accepts}, harnessOptions{client: func(o *mcp.ClientOptions) {
		o.MultiRoundTrip = &mcp.MultiRoundTripOptions{Disabled: true}
	}})
}

func (h *harness) raw(p *mcp.CallToolParams) *mcp.CallToolResult {
	h.t.Helper()
	res, err := h.cs.CallTool(h.t.Context(), p)
	if err != nil {
		h.t.Fatalf("calling %s: %v", p.Name, err)
	}
	return res
}

var accepted = mcp.InputResponseMap{askKey: &mcp.ElicitResult{Action: "accept"}}

// The first round only asks. The answer counts once, only with the state
// it was asked with, only for that call, and only while fresh; the write
// happens on the verified retry and never again.
func TestTheAnswerIsBoundToItsQuestion(t *testing.T) {
	h := mrtr(t)
	args := askCases["merge_merge_request"].setup(h)
	h.gl.ResetRequests()
	first := h.raw(&mcp.CallToolParams{Name: "merge_merge_request", Arguments: args})
	q, ok := first.InputRequests["confirm"].(*mcp.ElicitParams)
	if !first.NeedsInput() || !ok || q.Mode != "form" || first.RequestState == "" || writesSent(h) != 0 {
		t.Fatalf("first round %+v; %d writes", first, writesSent(h))
	}
	state := first.RequestState

	blocked := func(p *mcp.CallToolParams, want string) {
		t.Helper()
		res := h.raw(p)
		if text := textOf(res); !res.IsError || !strings.HasPrefix(text, "[blocked]") || !strings.Contains(text, want) {
			t.Errorf("%s", text)
		}
	}
	blocked(&mcp.CallToolParams{Name: "merge_merge_request", Arguments: args, InputResponses: accepted},
		"answers to a question this server has not asked")
	blocked(&mcp.CallToolParams{Name: "merge_merge_request", Arguments: args, InputResponses: accepted, RequestState: state + "x"},
		"did not ask")
	blocked(&mcp.CallToolParams{Name: "merge_merge_request", Arguments: args, InputResponses: accepted,
		RequestState: "e30." + strings.Split(state, ".")[1]}, "did not ask")
	other := maps.Clone(args)
	other["squash"] = false
	blocked(&mcp.CallToolParams{Name: "merge_merge_request", Arguments: other, InputResponses: accepted, RequestState: state},
		"another call")
	blocked(&mcp.CallToolParams{Name: "approve_merge_request", Arguments: map[string]any{"project": alpha, "iid": 1, "sha": args["sha"]},
		InputResponses: accepted, RequestState: state}, "another call")
	blocked(&mcp.CallToolParams{Name: "get_issue", Arguments: map[string]any{"project": alpha, "iid": 1},
		InputResponses: accepted, RequestState: state}, "asks the person nothing")
	if writesSent(h) != 0 {
		t.Fatalf("%d writes before the answer", writesSent(h))
	}

	done := h.raw(&mcp.CallToolParams{Name: "merge_merge_request", Arguments: args, InputResponses: accepted, RequestState: state})
	if done.IsError || writesSent(h) != 1 {
		t.Fatalf("the verified retry: %s; %d writes", textOf(done), writesSent(h))
	}
	blocked(&mcp.CallToolParams{Name: "merge_merge_request", Arguments: args, InputResponses: accepted, RequestState: state}, "already used")
	if writesSent(h) != 1 {
		t.Fatalf("%d writes after a replay", writesSent(h))
	}
}

// A state that travels through the client expires; one that stays in
// the process, before 2026-07-28, waits as long as the request does, so
// a slow accept still counts.
func TestALateAnswerIsRefusedOnlyWhenTheStateTravels(t *testing.T) {
	was := askTTL
	askTTL = -time.Minute
	t.Cleanup(func() { askTTL = was })
	h := mrtr(t)
	args := askCases["merge_merge_request"].setup(h)
	h.gl.ResetRequests()
	first := h.raw(&mcp.CallToolParams{Name: "merge_merge_request", Arguments: args})
	res := h.raw(&mcp.CallToolParams{Name: "merge_merge_request", Arguments: args, InputResponses: accepted, RequestState: first.RequestState})
	if text := textOf(res); !res.IsError || !strings.Contains(text, "expired") || writesSent(h) != 0 {
		t.Fatalf("%s; %d writes", text, writesSent(h))
	}

	for _, protocol := range []string{"2025-06-18", "2025-11-25"} {
		h := askingHarness(t, protocol, &answerer{answer: accepts}, harnessOptions{})
		h.ok("merge_merge_request", askCases["merge_merge_request"].setup(h))
	}
}

// retry answers a first round by hand, on 2026-07-28, after between runs.
func (h *harness) retry(name string, args map[string]any, between func()) *mcp.CallToolResult {
	h.t.Helper()
	first := h.raw(&mcp.CallToolParams{Name: name, Arguments: args})
	if !first.NeedsInput() {
		h.t.Fatalf("%s did not ask: %s", name, textOf(first))
	}
	between()
	return h.raw(&mcp.CallToolParams{Name: name, Arguments: args, InputResponses: accepted, RequestState: first.RequestState})
}

// What the person saw is what is written: a merge request retitled
// between the question and the answer is asked about again.
func TestAChangeAfterTheQuestionIsRefused(t *testing.T) {
	h := mrtr(t)
	args := askCases["merge_merge_request"].setup(h)
	res := h.retry("merge_merge_request", args, func() {
		h.ok("update_merge_request", map[string]any{"project": alpha, "iid": 1, "updated_at": mrWitness(h, 1), "title": "Retitled"})
		h.gl.ResetRequests()
	})
	if text := textOf(res); !res.IsError || !strings.Contains(text, "changed after the person was asked") || writesSent(h) != 0 {
		t.Fatalf("%s; %d writes", text, writesSent(h))
	}
}

// A label applied while the person reads is still the label they were
// asked about: its count is shown, not bound.
func TestALabelsCountMayMoveBetweenRounds(t *testing.T) {
	h := mrtr(t)
	args := askCases["delete_label"].setup(h)
	res := h.retry("delete_label", args, func() {
		h.ok("update_issue", map[string]any{"project": alpha, "iid": 1, "updated_at": issueWitness(h, 1), "add_labels": []string{"triage"}})
	})
	if res.IsError {
		t.Fatalf("%s", textOf(res))
	}
}

// A write confirmed and made, whose reply is then lost, is
// [ambiguous_outcome], never "nothing was written" (§4.5).
func TestAReplyLostAfterTheWriteIsAmbiguous(t *testing.T) {
	// lose stands where the SDK builds and sends the reply, after the
	// handler returned from its write.
	lose := func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			call, isCall := req.(*mcp.CallToolRequest)
			if r, ok := res.(*mcp.CallToolResult); ok && err == nil && isCall && call.Params.Name == "merge_merge_request" &&
				r.InputRequests == nil && !r.IsError {
				return nil, errors.New("reply lost")
			}
			return res, err
		}
	}
	p := &answerer{answer: accepts}
	h := askingHarness(t, "2025-11-25", p, harnessOptions{middleware: []mcp.Middleware{lose}})
	args := askCases["merge_merge_request"].setup(h)
	h.gl.ResetRequests()
	res := h.raw(&mcp.CallToolParams{Name: "merge_merge_request", Arguments: args})
	text := textOf(res)
	if !res.IsError || !strings.HasPrefix(text, "[ambiguous_outcome]") || strings.Contains(text, "Nothing was written") || writesSent(h) != 1 {
		t.Fatalf("%s; %d writes", text, writesSent(h))
	}
}

// Every tool that asks says so in its description.
func TestTheDescriptionSaysItAsks(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: full})
	res, err := h.cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	asks := asksPerson()
	for _, tool := range res.Tools {
		says := strings.Contains(tool.Description, "the server also asks the person")
		if says != slices.Contains(asks, tool.Name) {
			t.Errorf("%s: asks %v, says so %v", tool.Name, slices.Contains(asks, tool.Name), says)
		}
	}
}

// A decline stops the call before anything is read, so a write whose
// question depends on what GitLab holds is not made on a round that
// would no longer ask.
func TestADeclineIsRefusedBeforeAnythingRuns(t *testing.T) {
	h := mrtr(t)
	args := askCases["run_pipeline"].setup(h)
	first := h.raw(&mcp.CallToolParams{Name: "run_pipeline", Arguments: args})
	if !first.NeedsInput() {
		t.Fatalf("did not ask: %s", textOf(first))
	}
	h.gl.ResetRequests()
	res := h.raw(&mcp.CallToolParams{Name: "run_pipeline", Arguments: args, RequestState: first.RequestState,
		InputResponses: mcp.InputResponseMap{askKey: &mcp.ElicitResult{Action: "decline"}}})
	if text := textOf(res); !res.IsError || !strings.Contains(text, "not confirmed by the person") || len(h.gl.Requests()) != 0 {
		t.Fatalf("%s; %d requests", text, len(h.gl.Requests()))
	}
}

// run_pipeline asks on a protected branch or tag as on the default
// branch, naming which, however the ref is spelled, and on a ref it
// cannot resolve; not on a ref nothing protects.
func TestAPipelineAsksOnAProtectedRef(t *testing.T) {
	p := &answerer{answer: accepts}
	h := askingHarness(t, "2025-11-25", p, harnessOptions{})
	for ref, says := range map[string]string{"release/1.0": "a protected branch", gitlabtest.TagRelease: "a protected tag",
		"refs/heads/release/1.0": "a protected branch", "refs/tags/" + gitlabtest.TagRelease: "a protected tag",
		"refs/heads/main": "default branch", "no-such-ref": "cannot be told"} {
		p.then(accepts)
		h.call("run_pipeline", map[string]any{"project": alpha, "ref": ref})
		if qs := p.asked(); len(qs) != 1 || !strings.Contains(qs[0].Message, says) {
			t.Errorf("%s: %d questions %v", ref, len(qs), qs)
		}
	}
}
