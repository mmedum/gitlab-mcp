package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/gitlab-mcp/internal/config"
	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/gapi/gitlabtest"
	"github.com/mmedum/gitlab-mcp/internal/instance"
	"github.com/mmedum/gitlab-mcp/internal/service"
)

type staticTokens string

func (s staticTokens) Token(context.Context) (string, error) { return string(s), nil }

// harness is a client session against the tools registered over an
// in-memory GitLab.
type harness struct {
	t   *testing.T
	gl  *gitlabtest.Server
	cs  *mcp.ClientSession
	reg []service.Registered
}

type harnessOptions struct {
	gl       gitlabtest.Options
	cfg      config.Config
	logger   *slog.Logger
	noClient bool
	meta     instance.Metadata
	granted  []string
	defs     []definition // nil: every definition
}

func newHarness(t *testing.T, o harnessOptions) *harness {
	t.Helper()
	gl := gitlabtest.New(t, o.gl)
	inst, err := instance.Parse(gl.URL, false)
	if err != nil {
		t.Fatalf("instance: %v", err)
	}
	var client *gapi.Client
	if !o.noClient {
		client, err = gapi.New(gapi.Options{Instance: inst, Tokens: staticTokens(gl.Token()), Logger: o.logger,
			Sleep: func(context.Context, time.Duration) error { return nil }})
		if err != nil {
			t.Fatalf("client: %v", err)
		}
	}
	cfg := o.cfg
	if cfg.Instance == "" {
		cfg.Instance = gl.URL
	}
	svc := service.New(service.Options{Client: client, Config: cfg, Metadata: o.meta, Granted: o.granted})
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	defs := o.defs
	if defs == nil {
		defs = definitions()
	}
	reg := register(s, Deps{Service: svc, Config: cfg, Metadata: o.meta, Granted: o.granted, Logger: o.logger}, defs)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := s.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return &harness{t: t, gl: gl, cs: cs, reg: reg}
}

// call calls a tool and returns its text, its structured half decoded
// into a map, and whether it was an error.
func (h *harness) call(name string, args map[string]any) (string, map[string]any, bool) {
	h.t.Helper()
	res, err := h.cs.CallTool(h.t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		h.t.Fatalf("call %s: %v", name, err)
	}
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	var structured map[string]any
	if res.StructuredContent != nil {
		raw, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(raw, &structured)
	}
	return text.String(), structured, res.IsError
}

// ok calls a tool that must succeed.
func (h *harness) ok(name string, args map[string]any) (string, map[string]any) {
	h.t.Helper()
	text, out, isErr := h.call(name, args)
	if isErr {
		h.t.Fatalf("%s failed: %s", name, text)
	}
	return text, out
}

// fails calls a tool that must fail with the given class.
func (h *harness) fails(name string, args map[string]any, class string) string {
	h.t.Helper()
	text, _, isErr := h.call(name, args)
	if !isErr {
		h.t.Fatalf("%s succeeded, want [%s]: %s", name, class, text)
	}
	if !strings.HasPrefix(text, "["+class+"] ") {
		h.t.Fatalf("%s error = %q, want class [%s]", name, text, class)
	}
	return text
}

// get walks a decoded JSON value by keys and indexes.
func get(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			a, _ := v.([]any)
			if k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}
