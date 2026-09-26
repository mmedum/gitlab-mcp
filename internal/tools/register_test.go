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

	"github.com/mmedum/gitlab-mcp/internal/config"
	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/instance"
	"github.com/mmedum/gitlab-mcp/internal/model"
	"github.com/mmedum/gitlab-mcp/internal/render"
	"github.com/mmedum/gitlab-mcp/internal/scopes"
	"github.com/mmedum/gitlab-mcp/internal/service"
)

// register's rules, held with tools of every kind. Phase 0 ships only
// reads, so these stand in for the writes to come.

type fakeIn struct {
	Confirm bool   `json:"confirm,omitempty" jsonschema:"Say that the deletion is meant"`
	DryRun  bool   `json:"dry_run,omitempty" jsonschema:"Show what would be sent without sending it"`
	Fail    string `json:"fail,omitempty" jsonschema:"How to fail"`
}

type fakeOut struct {
	At     time.Time  `json:"at"`
	Maybe  *time.Time `json:"maybe"`
	DryRun bool       `json:"dry_run"`
}

var fixedTime = time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)

func fake(name string, k Kind, toolset, minVersion string) definition {
	return tool[fakeIn, fakeOut]{
		sp: spec{Name: name, Kind: k, Toolset: toolset, MinVersion: minVersion, Description: "A fake."},
		run: func(ctx context.Context, _ *service.Service, in fakeIn) (fakeOut, error) {
			switch in.Fail {
			case "hinted":
				return fakeOut{}, withHint(gapi.Errf(gapi.ClassRateLimited, "GitLab is rate limiting"), "narrow the query")
			case "plain":
				return fakeOut{}, errors.New("something odd at https://gitlab.example.com/secret/path")
			case "panic":
				panic("payload in a panic")
			}
			return fakeOut{At: fixedTime, DryRun: gapi.IsDryRun(ctx)}, nil
		},
		text: func(o fakeOut, _ render.Boundary) string { return "at " + o.At.Format(time.RFC3339) },
	}
}

func fakes() []definition {
	return []definition{
		fake("fake_read", Read, "", ""),
		fake("fake_write", Write, "", ""),
		fake("fake_ship", Ship, "", ""),
		fake("fake_delete", Destructive, "", ""),
		fake("fake_wiki", Read, "wiki", ""),
		fake("fake_new", Read, "", "19.5"),
	}
}

func names(r []service.Registered) []string {
	out := make([]string, len(r))
	for i, x := range r {
		out[i] = x.Name
	}
	return out
}

func meta(t *testing.T, v string) instance.Metadata {
	t.Helper()
	m, err := instance.NewMetadata(v, "abc", false)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestGate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     config.Config
		meta    instance.Metadata
		granted []string
		want    []string
	}{
		{"default", config.Config{}, instance.Metadata{}, nil,
			[]string{"fake_read", "fake_write", "fake_new"}},
		{"read-only beats every flag", config.Config{ReadOnly: true, EnableShip: true, EnableDestructive: true}, instance.Metadata{}, nil,
			[]string{"fake_read", "fake_new"}},
		{"ship", config.Config{EnableShip: true}, instance.Metadata{}, nil,
			[]string{"fake_read", "fake_write", "fake_ship", "fake_new"}},
		{"destructive", config.Config{EnableDestructive: true}, instance.Metadata{}, nil,
			[]string{"fake_read", "fake_write", "fake_delete", "fake_new"}},
		{"toolset", config.Config{Toolsets: []string{"wiki"}}, instance.Metadata{}, nil,
			[]string{"fake_read", "fake_write", "fake_wiki", "fake_new"}},
		{"older instance", config.Config{}, meta(t, "19.4.2"), nil,
			[]string{"fake_read", "fake_write"}},
		{"new enough instance", config.Config{}, meta(t, "19.5.0"), nil,
			[]string{"fake_read", "fake_write", "fake_new"}},
		{"a read_api token gets no write tool", FullSurface(config.Config{}), instance.Metadata{}, []string{scopes.ReadAPI},
			[]string{"fake_read", "fake_wiki", "fake_new"}},
		{"an api token gets everything", FullSurface(config.Config{}), instance.Metadata{}, []string{scopes.API},
			[]string{"fake_read", "fake_write", "fake_ship", "fake_delete", "fake_wiki", "fake_new"}},
	}
	for _, c := range cases {
		got := names(surface(fakes(), c.cfg, c.meta, c.granted))
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: registered %v, want %v", c.name, got, c.want)
		}
	}
}

// TestFullSurfaceRegistersEverything holds FullSurface's claim: no
// combination of the gates registers a tool it does not.
func TestFullSurfaceRegistersEverything(t *testing.T) {
	full := names(surface(fakes(), FullSurface(config.Config{}), instance.Metadata{}, nil))
	if len(full) != len(fakes()) {
		t.Fatalf("FullSurface registers %v, want all %d", full, len(fakes()))
	}
	for mask := range 1 << 4 {
		cfg := config.Config{ReadOnly: mask&1 != 0, EnableShip: mask&2 != 0, EnableDestructive: mask&4 != 0}
		if mask&8 != 0 {
			cfg.Toolsets = config.Toolsets
		}
		for _, n := range names(surface(fakes(), cfg, instance.Metadata{}, nil)) {
			if !slices.Contains(full, n) {
				t.Errorf("%+v registers %s, which FullSurface does not", cfg, n)
			}
		}
	}
	if got := len(Surface(FullSurface(config.Config{}), instance.Metadata{}, nil)); got != len(definitions()) {
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
		"[rate_limited] narrow the query (GitLab is rate limiting)" {
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
	err := checkEnums(map[string]any{"state": "open"}, map[string][]string{"state": {"opened", "closed", "all"}})
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
