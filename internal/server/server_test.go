package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/gitlab-mcp/v2/internal/config"
	"github.com/mmedum/gitlab-mcp/v2/internal/scopes"
	"github.com/mmedum/gitlab-mcp/v2/internal/tools"
)

func TestDescriptionFitsTheRegistry(t *testing.T) {
	if n := len(Description); n > 100 || n == 0 {
		t.Errorf("Description is %d characters; the registry allows 100", n)
	}
}

func TestDumpSchemas(t *testing.T) {
	var buf bytes.Buffer
	if err := DumpSchemas(&buf, "v0.0.0-test"); err != nil {
		t.Fatal(err)
	}
	var d struct {
		Server            string           `json:"server"`
		Version           string           `json:"version"`
		SDK               string           `json:"sdk"`
		Tools             []map[string]any `json:"tools"`
		ResourceTemplates []any            `json:"resourceTemplates"`
	}
	if err := json.Unmarshal(buf.Bytes(), &d); err != nil {
		t.Fatalf("dump is not JSON: %v", err)
	}
	if d.Server != "gitlab-mcp" || d.Version != "v0.0.0-test" || d.SDK != "v1.8.0" {
		t.Errorf("header = %q %q %q", d.Server, d.Version, d.SDK)
	}
	if len(d.Tools) != 87 || len(d.ResourceTemplates) != 3 {
		t.Fatalf("%d tools, templates %v", len(d.Tools), d.ResourceTemplates)
	}
	var names []string
	for _, tl := range d.Tools {
		names = append(names, tl["name"].(string))
		for _, key := range []string{"description", "inputSchema", "outputSchema", "annotations"} {
			if tl[key] == nil {
				t.Errorf("%s has no %s", tl["name"], key)
			}
		}
	}
	if !slices.IsSorted(names) {
		t.Errorf("tools are not sorted: %v", names)
	}
}

// toolName matches anything shaped like a tool name.
var toolName = regexp.MustCompile(`\b[a-z]+(?:_[a-z]+)+\b`)

func TestInstructionsNameOnlyRegisteredTools(t *testing.T) {
	var all []string
	for _, r := range tools.Surface(tools.FullSurface(config.Config{}), nil) {
		all = append(all, r.Name)
	}
	cases := []struct {
		name    string
		cfg     config.Config
		granted []string
	}{
		{"default", config.Config{}, nil},
		{"read-only", config.Config{ReadOnly: true}, nil},
		{"full", tools.FullSurface(config.Config{}), nil},
		{"read_api token", config.Config{}, []string{scopes.ReadAPI}},
	}
	for _, c := range cases {
		var registered []string
		for _, r := range tools.Surface(c.cfg, c.granted) {
			registered = append(registered, r.Name)
		}
		text := instructionsFor(c.cfg, c.granted)
		named := 0
		for _, word := range toolName.FindAllString(text, -1) {
			if !slices.Contains(all, word) {
				continue
			}
			named++
			if !slices.Contains(registered, word) {
				t.Errorf("%s: the instructions name %s, which is not registered", c.name, word)
			}
		}
		// The floor: the check read something.
		if named < len(registered) {
			t.Errorf("%s: the instructions name %d tools of %d registered", c.name, named, len(registered))
		}
	}
	if !strings.Contains(instructionsFor(config.Config{ReadOnly: true}, nil), "read-only") {
		t.Error("read-only mode is not stated")
	}
}

func TestLoggerOnlyAttachedAtDebug(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
		var buf bytes.Buffer
		lg := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: level}))
		s := New(Options{Logger: lg, Version: "test"})
		ct, st := mcp.NewInMemoryTransports()
		ss, err := s.Connect(t.Context(), st, nil)
		if err != nil {
			t.Fatal(err)
		}
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v"}, nil).Connect(t.Context(), ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = cs.Close()
		_ = ss.Close()
		chatter := strings.Contains(buf.String(), "session")
		if chatter != (level == slog.LevelDebug) {
			t.Errorf("at %v the SDK logged session chatter: %v\n%s", level, chatter, buf.String())
		}
	}
}

func TestSignedOutServerAnswersAuth(t *testing.T) {
	s := New(Options{Config: config.Config{}, Version: "test"})
	ct, st := mcp.NewInMemoryTransports()
	ss, err := s.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_me"})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !res.IsError || !strings.HasPrefix(text, "[auth] ") {
		t.Errorf("get_me signed out = %q", text)
	}
	// resolve_url needs no sign-in for what it can answer alone.
	res, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "resolve_url",
		Arguments: map[string]any{"url": "https://gitlab.com/example-group/alpha/-/issues/3"}})
	if err != nil || res.IsError {
		t.Errorf("resolve_url signed out: %v %v", err, res.Content)
	}
}
