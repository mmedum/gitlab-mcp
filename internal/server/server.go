// Package server wires the tools onto the MCP SDK and dumps the tool
// surface.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/gitlab-mcp/v4/internal/config"
	"github.com/mmedum/gitlab-mcp/v4/internal/gapi"
	"github.com/mmedum/gitlab-mcp/v4/internal/scopes"
	"github.com/mmedum/gitlab-mcp/v4/internal/service"
	"github.com/mmedum/gitlab-mcp/v4/internal/tools"
	"github.com/mmedum/gitlab-mcp/v4/internal/version"
)

// Name is the MCP server name.
const Name = "gitlab-mcp"

// Description is the one-line description shared by the bundle manifest
// and the registry entry. At most 100 characters.
const Description = "GitLab over MCP from your own account: issues, merge requests, reviews, the repository and CI."

// sdkModule is the MCP SDK's module path; the schema dump records the
// version the binary was built with.
const sdkModule = "github.com/modelcontextprotocol/go-sdk"

// Options is what the server needs from startup.
type Options struct {
	Config  config.Config
	Client  *gapi.Client
	Granted []string // scopes the token was granted
	Logger  *slog.Logger
	Version string
}

// New builds the MCP server with every tool the configuration registers.
// A nil Client is allowed: every tool then answers [auth], which is what
// a client started before `gitlab-mcp login` should see.
func New(opts Options) *mcp.Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	so := &mcp.ServerOptions{Instructions: instructionsFor(opts.Config, opts.Granted)}
	// The SDK's own logger writes session chatter. It is attached only at
	// debug, where someone asked for it.
	if logger.Enabled(context.Background(), slog.LevelDebug) {
		so.Logger = logger
	}
	s := mcp.NewServer(&mcp.Implementation{Name: Name, Version: opts.Version}, so)
	s.AddReceivingMiddleware(logMethods(logger))
	svc := service.New(service.Options{Client: opts.Client, Config: opts.Config, Granted: opts.Granted})
	deps := tools.Deps{Service: svc, Config: opts.Config, Granted: opts.Granted, Logger: logger}
	tools.Register(s, deps)
	tools.RegisterResources(s, deps)
	return s
}

// logMethods logs every method but a tool call, which the tools log
// themselves with their outcome class and rate bucket: the method, how
// it ended and how long it took, and nothing it carried (D-05).
func logMethods(lg *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			start := time.Now()
			res, err := next(ctx, method, req)
			if method != "tools/call" {
				outcome := "ok"
				if err != nil {
					outcome = "error"
				}
				lg.Debug("mcp call", "method", method, "outcome", outcome, "ms", time.Since(start).Milliseconds())
			}
			return res, err
		}
	}
}

// Dump is what --dump-schemas writes: the whole registrable surface,
// every mcp.Tool as the wire carries it, _meta and output schema
// included, and the instructions a client puts in front of the model.
// The schema gates compare two of these. The server has no resources or
// prompts today; while it has none the dump leaves them out, and one
// added later still reaches the gates.
type Dump struct {
	Server  string `json:"server"`
	Version string `json:"version"`
	SDK     string `json:"sdk"`
	// Instructions are the text the full surface is served, keyed "full",
	// and the text each of instructionConfigs is.
	Instructions      map[string]string       `json:"instructions"`
	Tools             []*mcp.Tool             `json:"tools"`
	ResourceTemplates []*mcp.ResourceTemplate `json:"resourceTemplates"`
	Resources         []*mcp.Resource         `json:"resources,omitempty"`
	Prompts           []*mcp.Prompt           `json:"prompts,omitempty"`
}

// instructionConfigs are the configurations, beside the full surface,
// whose instructions the dump carries. The instructions differ by what
// a configuration registers, so between them these reach every sentence
// instructionsFor can say: the read-only one, the one for a token that
// may only read, and the settings that would register more.
var instructionConfigs = map[string]struct {
	cfg     config.Config
	granted []string
}{
	"default":   {config.Config{}, nil},
	"read_only": {config.Config{ReadOnly: true}, nil},
	"read_api":  {config.Config{}, []string{scopes.ReadAPI}},
}

// DumpSchemas writes the full tool surface — every flag and toolset on —
// as JSON, without touching the network or credentials. The SDK has no
// public enumerator, so an in-memory client asks the server, which is
// also exactly what a client would be told. The other configurations'
// instructions are the text New serves them, read without a session.
func DumpSchemas(w io.Writer, version string) error {
	ctx := context.Background()
	d := Dump{Server: Name, Version: version, SDK: sdkVersion(), Instructions: map[string]string{}}
	full := New(Options{Config: tools.FullSurface(config.Config{}), Version: version})
	if err := withSession(ctx, full, func(cs *mcp.ClientSession) error {
		d.Instructions["full"] = cs.InitializeResult().Instructions
		return listSurface(ctx, cs, &d)
	}); err != nil {
		return err
	}
	for name, c := range instructionConfigs {
		d.Instructions[name] = instructionsFor(c.cfg, c.granted)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(d)
}

// withSession serves s to an in-memory client and hands use the client's
// session.
func withSession(ctx context.Context, s *mcp.Server, use func(*mcp.ClientSession) error) error {
	ct, st := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, st, nil)
	if err != nil {
		return fmt.Errorf("connect server: %w", err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "schema-dump"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		return fmt.Errorf("connect client: %w", err)
	}
	defer func() { _ = cs.Close() }()
	return use(cs)
}

// listSurface reads the tools, resource templates, resources and prompts
// the session is served into d, each list sorted by what names it.
func listSurface(ctx context.Context, cs *mcp.ClientSession, d *Dump) error {
	tl, err := cs.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("list tools: %w", err)
	}
	templates, err := cs.ListResourceTemplates(ctx, nil)
	if err != nil {
		return fmt.Errorf("list resource templates: %w", err)
	}
	resources, err := cs.ListResources(ctx, nil)
	if err != nil {
		return fmt.Errorf("list resources: %w", err)
	}
	prompts, err := cs.ListPrompts(ctx, nil)
	if err != nil {
		return fmt.Errorf("list prompts: %w", err)
	}
	d.Tools = slices.SortedFunc(slices.Values(tl.Tools), func(a, b *mcp.Tool) int { return strings.Compare(a.Name, b.Name) })
	d.ResourceTemplates = slices.SortedFunc(slices.Values(templates.ResourceTemplates), func(a, b *mcp.ResourceTemplate) int {
		return strings.Compare(a.URITemplate, b.URITemplate)
	})
	d.Resources = slices.SortedFunc(slices.Values(resources.Resources), func(a, b *mcp.Resource) int { return strings.Compare(a.URI, b.URI) })
	d.Prompts = slices.SortedFunc(slices.Values(prompts.Prompts), func(a, b *mcp.Prompt) int { return strings.Compare(a.Name, b.Name) })
	return nil
}

// sdkVersion is the MCP SDK version from the build info, so a diff an
// SDK upgrade caused can be told from a change to the surface.
func sdkVersion() string { return version.Module(sdkModule) }
