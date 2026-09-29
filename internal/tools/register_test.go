package tools

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/gitlab-mcp/v2/internal/config"
	"github.com/mmedum/gitlab-mcp/v2/internal/gapi"
	"github.com/mmedum/gitlab-mcp/v2/internal/model"
	"github.com/mmedum/gitlab-mcp/v2/internal/render"
	"github.com/mmedum/gitlab-mcp/v2/internal/scopes"
	"github.com/mmedum/gitlab-mcp/v2/internal/service"
)

// register's rules, held with fake tools of every kind, so a rule is
// tested apart from any one real tool.

type fakeIn struct {
	Confirm bool   `json:"confirm,omitempty" jsonschema:"Say that the deletion is meant"`
	DryRun  bool   `json:"dry_run,omitempty" jsonschema:"Show what would be sent without sending it"`
	Fail    string `json:"fail,omitempty" jsonschema:"How to fail"`
}

type fakeOut struct {
	At    time.Time  `json:"at"`
	Maybe *time.Time `json:"maybe"`
	model.Write
}

var fixedTime = time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)

// fakeAsks is what every Destructive tool must say about asking.
func fakeAsks(k Kind) string {
	if k == Destructive {
		return "before it deletes"
	}
	return ""
}

func fake(name string, k Kind, toolset string) definition {
	return tool[fakeIn, fakeOut]{
		sp: spec{Name: name, Kind: k, Toolset: toolset, Description: "A fake.", Asks: fakeAsks(k)},
		run: func(ctx context.Context, _ *service.Service, in fakeIn) (fakeOut, error) {
			switch in.Fail {
			case "hinted":
				return fakeOut{}, withHint(gapi.Errf(gapi.ClassRateLimited, "GitLab is rate limiting"), "narrow the query")
			case "plain":
				return fakeOut{}, errors.New("something odd at https://gitlab.example.com/secret/path")
			case "panic":
				panic("payload in a panic")
			}
			return fakeOut{At: fixedTime, Write: model.Write{DryRun: gapi.IsDryRun(ctx)}}, nil
		},
		text: func(o fakeOut, _ render.Boundary) string { return "at " + o.At.Format(time.RFC3339) },
	}
}

func fakes() []definition {
	return []definition{
		fake("fake_read", Read, ""),
		fake("fake_write", Write, ""),
		fake("fake_ship", Ship, ""),
		fake("fake_delete", Destructive, ""),
		fake("fake_wiki", Read, "wiki"),
	}
}

func names(r []service.Registered) []string {
	out := make([]string, len(r))
	for i, x := range r {
		out[i] = x.Name
	}
	return out
}

func TestGate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     config.Config
		granted []string
		want    []string
	}{
		{"default", config.Config{}, nil,
			[]string{"fake_read", "fake_write"}},
		{"read-only beats every flag", config.Config{ReadOnly: true, EnableShip: true, EnableDestructive: true}, nil,
			[]string{"fake_read"}},
		{"ship", config.Config{EnableShip: true}, nil,
			[]string{"fake_read", "fake_write", "fake_ship"}},
		{"destructive", config.Config{EnableDestructive: true}, nil,
			[]string{"fake_read", "fake_write", "fake_delete"}},
		{"toolset", config.Config{Toolsets: []string{"wiki"}}, nil,
			[]string{"fake_read", "fake_write", "fake_wiki"}},
		{"a read_api token gets no write tool", FullSurface(config.Config{}), []string{scopes.ReadAPI},
			[]string{"fake_read", "fake_wiki"}},
		{"an api token gets everything", FullSurface(config.Config{}), []string{scopes.API},
			[]string{"fake_read", "fake_write", "fake_ship", "fake_delete", "fake_wiki"}},
	}
	for _, c := range cases {
		got := names(surface(fakes(), c.cfg, c.granted))
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: registered %v, want %v", c.name, got, c.want)
		}
	}
}

// TestFullSurfaceRegistersEverything holds FullSurface's claim: no
// combination of the gates registers a tool it does not.
func TestFullSurfaceRegistersEverything(t *testing.T) {
	full := names(surface(fakes(), FullSurface(config.Config{}), nil))
	if len(full) != len(fakes()) {
		t.Fatalf("FullSurface registers %v, want all %d", full, len(fakes()))
	}
	for mask := range 1 << 4 {
		cfg := config.Config{ReadOnly: mask&1 != 0, EnableShip: mask&2 != 0, EnableDestructive: mask&4 != 0}
		if mask&8 != 0 {
			cfg.Toolsets = config.Toolsets
		}
		for _, n := range names(surface(fakes(), cfg, nil)) {
			if !slices.Contains(full, n) {
				t.Errorf("%+v registers %s, which FullSurface does not", cfg, n)
			}
		}
	}
	if got := len(Surface(FullSurface(config.Config{}), nil)); got != len(definitions()) {
		t.Errorf("FullSurface registers %d of %d real tools", got, len(definitions()))
	}
}

func listed(t *testing.T, h *harness) map[string]*mcp.Tool {
	t.Helper()
	res, err := h.cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*mcp.Tool{}
	for _, tl := range res.Tools {
		out[tl.Name] = tl
	}
	return out
}

func TestAnnotationsAndMeta(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: FullSurface(config.Config{}), defs: fakes()})
	tools := listed(t, h)
	cases := []struct {
		name                         string
		readOnly, destructive, world bool
		interactive                  bool
	}{
		{"fake_read", true, false, false, false},
		{"fake_write", false, false, true, false},
		{"fake_ship", false, true, true, true},
		{"fake_delete", false, true, true, true},
	}
	for _, c := range cases {
		tl := tools[c.name]
		a := tl.Annotations
		if a.ReadOnlyHint != c.readOnly || (a.DestructiveHint != nil && *a.DestructiveHint) != c.destructive ||
			a.OpenWorldHint == nil || *a.OpenWorldHint != c.world {
			t.Errorf("%s annotations = %+v", c.name, a)
		}
		_, interactive := tl.Meta["anthropic/requiresUserInteraction"]
		if interactive != c.interactive {
			t.Errorf("%s requiresUserInteraction = %v, want %v", c.name, interactive, c.interactive)
		}
	}
	// Times are date-time in the output schema, pointers nullable.
	raw, _ := json.Marshal(tools["fake_read"].OutputSchema)
	if !strings.Contains(string(raw), `"at":{"format":"date-time","type":"string"}`) ||
		!strings.Contains(string(raw), `"maybe":{"format":"date-time","type":["null","string"]}`) {
		t.Errorf("output schema = %s", raw)
	}
}

func TestDryRunReachesTheClient(t *testing.T) {
	h := newHarness(t, harnessOptions{defs: fakes()})
	_, out := h.ok("fake_write", map[string]any{"dry_run": true})
	if get(out, "dry_run") != true {
		t.Errorf("dry_run did not put the call on a dry-run context: %v", out)
	}
	_, out = h.ok("fake_write", nil)
	if get(out, "dry_run") != false {
		t.Errorf("a plain call ran as a dry run: %v", out)
	}
}

func TestDestructiveNeedsConfirm(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: config.Config{EnableDestructive: true}, defs: fakes()})
	text := h.fails("fake_delete", nil, "blocked")
	if !strings.Contains(text, "Pass confirm: true to allow it") {
		t.Errorf("the refusal does not name its unlock: %s", text)
	}
	h.ok("fake_delete", map[string]any{"confirm": true})
	h.ok("fake_delete", map[string]any{"dry_run": true})
}

func TestErrors(t *testing.T) {
	h := newHarness(t, harnessOptions{defs: fakes()})
	// The hint survives and the class still comes from the wrapped error.
	if got := h.fails("fake_read", map[string]any{"fail": "hinted"}, "rate_limited"); got !=
		"[rate_limited] GitLab is rate limiting; narrow the query" {
		t.Errorf("hinted = %q", got)
	}
	// Anything unclassified is unexpected, with any URL cut out.
	if got := h.fails("fake_read", map[string]any{"fail": "plain"}, "unexpected"); strings.Contains(got, "gitlab.example.com") {
		t.Errorf("unclassified error kept its URL: %q", got)
	}
	// A panic is a result, and its value is not shown.
	if got := h.fails("fake_read", map[string]any{"fail": "panic"}, "unexpected"); strings.Contains(got, "payload") {
		t.Errorf("a panic's value was shown: %q", got)
	}
}

func TestBothHalvesDiffer(t *testing.T) {
	h := newHarness(t, harnessOptions{defs: fakes()})
	res, err := h.cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "fake_read"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("content = %d blocks, want 1", len(res.Content))
	}
	text := res.Content[0].(*mcp.TextContent).Text
	raw, _ := json.Marshal(res.StructuredContent)
	if text != "at 2026-01-05T09:00:00Z" || text == string(raw) {
		t.Errorf("text = %q, structured = %s", text, raw)
	}
}

func TestLenient(t *testing.T) {
	schema := inputSchema[searchIssuesIn](spec{Name: "search_issues"})
	cases := []struct {
		raw      string
		want     string
		adjusted []string
	}{
		{`{"labels":"[\"a\",\"b\"]","max":"7"}`, `{"labels":["a","b"],"max":7}`, []string{"labels", "max"}},
		{`{"author":"","milestone":null,"search":"x"}`, `{"search":"x"}`, []string{"author", "milestone"}},
		// Not JSON in the string: left for the schema to refuse.
		{`{"labels":"a","max":"seven"}`, `{"labels":"a","max":"seven"}`, nil},
		{`{"max":7}`, `{"max":7}`, nil},
		{``, `{}`, nil},
	}
	for _, c := range cases {
		args, adjusted, err := lenient(json.RawMessage(c.raw), schema)
		if err != nil {
			t.Fatalf("%s: %v", c.raw, err)
		}
		got, _ := json.Marshal(args)
		if string(got) != c.want || !slices.Equal(adjusted, c.adjusted) {
			t.Errorf("%s: got %s %v, want %s %v", c.raw, got, adjusted, c.want, c.adjusted)
		}
	}
	// A required input is never treated as absent.
	required := inputSchema[getIssueIn](spec{Name: "get_issue"})
	args, _, _ := lenient(json.RawMessage(`{"project":"","iid":"3"}`), required)
	if got, _ := json.Marshal(args); string(got) != `{"iid":3,"project":""}` {
		t.Errorf("required: %s", got)
	}
	// An input that takes a string is left alone, even one that also
	// takes an integer.
	args, adjusted, _ := lenient(json.RawMessage(`{"project":"2001","iid":3}`), required)
	if got, _ := json.Marshal(args); string(got) != `{"iid":3,"project":"2001"}` || len(adjusted) != 0 {
		t.Errorf("project: %s %v", got, adjusted)
	}
	if _, _, err := lenient(json.RawMessage(`[1]`), schema); err == nil {
		t.Error("a non-object was accepted")
	}
}

func TestEnumErrorsNameTheSetSorted(t *testing.T) {
	err := checkEnums(map[string]any{"state": "open"}, enumsOf(map[string][]string{"state": {"opened", "closed", "all"}}))
	if err == nil || err.Error() != "[invalid] state must be one of all|closed|opened" {
		t.Errorf("err = %v", err)
	}
}

func TestDescriptionsFollowTheHouseStyle(t *testing.T) {
	for _, def := range definitions() {
		sp := def.spec()
		if n := strings.Count(sp.Description, "IMPORTANT:"); n > 1 {
			t.Errorf("%s has %d IMPORTANT:", sp.Name, n)
		}
		if sp.Description == "" || !strings.HasSuffix(sp.Description, ".") {
			t.Errorf("%s: description is empty or unfinished", sp.Name)
		}
	}
	// Every input of every tool is described; iid in the words of §6.1.
	h := newHarness(t, harnessOptions{})
	for name, tl := range listed(t, h) {
		props, _ := tl.InputSchema.(map[string]any)["properties"].(map[string]any)
		for input, raw := range props {
			desc, _ := raw.(map[string]any)["description"].(string)
			if desc == "" {
				t.Errorf("%s.%s has no description", name, input)
			}
			if input == "iid" && !strings.Contains(desc, "the number shown as #12 for issues and !12 for merge requests") {
				t.Errorf("%s.iid = %q", name, desc)
			}
		}
	}
}

// The model package is the output of every tool; a type there that
// cannot be given a schema would panic at start.
func TestEveryOutputHasASchema(t *testing.T) {
	for _, s := range []any{outputSchema[model.Me](), outputSchema[model.Commit](), outputSchema[model.Discussions]()} {
		if s == nil {
			t.Fatal("nil schema")
		}
	}
}

// ------------------------------------------------------------ the guard

type guardedIn struct {
	Body           string  `json:"body" jsonschema:"A body"`
	Title          *string `json:"title,omitempty" jsonschema:"A body behind a pointer"`
	Plain          string  `json:"plain,omitempty" jsonschema:"Not a body"`
	EscapeCommands bool    `json:"escape_commands,omitempty" jsonschema:"Escape instead of refusing"`
	DryRun         bool    `json:"dry_run,omitempty" jsonschema:"Show what would be sent"`
}

type guardedOut struct {
	Body  string `json:"body"`
	Title string `json:"title"`
	Plain string `json:"plain"`
	model.Write
}

// guardedTool echoes what its handler was given, and counts its runs.
func guardedTool(runs *int) definition {
	return tool[guardedIn, guardedOut]{
		sp: spec{Name: "fake_comment", Kind: Write, Guarded: []string{"body", "title"}, Description: "A fake comment."},
		run: func(_ context.Context, _ *service.Service, in guardedIn) (guardedOut, error) {
			*runs++
			out := guardedOut{Body: in.Body, Plain: in.Plain}
			if in.Title != nil {
				out.Title = *in.Title
			}
			return out, nil
		},
		text: func(o guardedOut, _ render.Boundary) string { return o.Body },
	}
}

func TestTheGuardRefusesAQuickActionBeforeTheHandler(t *testing.T) {
	runs := 0
	h := newHarness(t, harnessOptions{defs: []definition{guardedTool(&runs)}})
	for _, args := range []map[string]any{
		{"body": "Looks good.\n\n/merge"},
		{"body": "fine", "title": "/close"},
		{"body": "/approve", "dry_run": true},
	} {
		text := h.fails("fake_comment", args, "blocked")
		if !strings.Contains(text, "GitLab would run") || !strings.Contains(text, "escape_commands") {
			t.Errorf("the refusal does not say what and how: %s", text)
		}
	}
	if !strings.Contains(h.fails("fake_comment", map[string]any{"body": "a\n\n/merge"}, "blocked"), "body: GitLab would run a quick action in this text: line 3 /merge") {
		t.Error("the refusal does not name the input and the line")
	}
	if runs != 0 {
		t.Errorf("the handler ran %d times on refused text", runs)
	}
	// A string that is not declared is not the guard's.
	_, out := h.ok("fake_comment", map[string]any{"body": "fine", "plain": "/close"})
	if get(out, "plain") != "/close" || runs != 1 {
		t.Errorf("an undeclared input was touched: %v", out)
	}
}

func TestTheGuardEscapesWhenAsked(t *testing.T) {
	runs := 0
	h := newHarness(t, harnessOptions{defs: []definition{guardedTool(&runs)}})
	_, out := h.ok("fake_comment", map[string]any{"body": "Done.\n\n/close\n", "title": "/label ~x", "escape_commands": true})
	if get(out, "body") != "Done.\n\n\\/close\n" || get(out, "title") != `\/label ~x` {
		t.Errorf("the handler was not given escaped text: %v", out)
	}
	escaped, _ := out["escaped_commands"].([]any)
	if len(escaped) != 2 {
		t.Fatalf("escaped_commands = %v", out["escaped_commands"])
	}
	first, _ := escaped[0].(map[string]any)
	if first["input"] != "body" || first["line"] != float64(3) || first["command"] != "close" {
		t.Errorf("escaped_commands[0] = %v", first)
	}
	// Nothing to escape leaves an empty list, never null.
	_, out = h.ok("fake_comment", map[string]any{"body": "plain", "escape_commands": true})
	if l, ok := out["escaped_commands"].([]any); !ok || len(l) != 0 {
		t.Errorf("escaped_commands = %v, want []", out["escaped_commands"])
	}
}

func TestTheGuardedInputsAreDeclared(t *testing.T) {
	runs := 0
	h := newHarness(t, harnessOptions{defs: []definition{guardedTool(&runs)}})
	got, _ := listed(t, h)["fake_comment"].Meta[quickActionMeta].([]any)
	if len(got) != 2 || got[0] != "body" || got[1] != "title" {
		t.Errorf("_meta[%q] = %v", quickActionMeta, got)
	}
}

// A write that forgets a rule fails when the server starts, not when a
// person first calls it.
func TestRegisterRefusesAWriteMissingARule(t *testing.T) {
	type noDryIn struct {
		Body           string `json:"body" jsonschema:"A body"`
		EscapeCommands bool   `json:"escape_commands,omitempty" jsonschema:"Escape"`
	}
	type noEscapeIn struct {
		Body   string `json:"body" jsonschema:"A body"`
		DryRun bool   `json:"dry_run,omitempty" jsonschema:"Dry"`
	}
	run := func(context.Context, *service.Service, guardedIn) (guardedOut, error) { return guardedOut{}, nil }
	text := func(guardedOut, render.Boundary) string { return "" }
	for name, def := range map[string]definition{
		"no dry_run": tool[noDryIn, guardedOut]{sp: spec{Name: "x", Kind: Write, Description: "x"},
			run: func(context.Context, *service.Service, noDryIn) (guardedOut, error) { return guardedOut{}, nil }, text: text},
		"no escape_commands": tool[noEscapeIn, guardedOut]{sp: spec{Name: "x", Kind: Write, Guarded: []string{"body"}, Description: "x"},
			run: func(context.Context, *service.Service, noEscapeIn) (guardedOut, error) { return guardedOut{}, nil }, text: text},
		"no such input":  tool[guardedIn, guardedOut]{sp: spec{Name: "x", Kind: Write, Guarded: []string{"nope"}, Description: "x"}, run: run, text: text},
		"a non-string":   tool[guardedIn, guardedOut]{sp: spec{Name: "x", Kind: Write, Guarded: []string{"dry_run"}, Description: "x"}, run: run, text: text},
		"a guarded read": tool[guardedIn, guardedOut]{sp: spec{Name: "x", Kind: Read, Guarded: []string{"body"}, Description: "x"}, run: run, text: text},
		"no model.Write": tool[fakeIn, time.Time]{sp: spec{Name: "x", Kind: Write, Description: "x"}, run: func(context.Context, *service.Service, fakeIn) (time.Time, error) { return time.Time{}, nil }, text: func(time.Time, render.Boundary) string { return "" }},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: registered without a panic", name)
				}
			}()
			def.add(mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil), Deps{})
		}()
	}
}
