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

	"github.com/mmedum/gitlab-mcp/internal/config"
	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/instance"
	"github.com/mmedum/gitlab-mcp/internal/service"
	"github.com/mmedum/gitlab-mcp/internal/tools"
	"github.com/mmedum/gitlab-mcp/internal/version"
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
	Config   config.Config
	Client   *gapi.Client
	Metadata instance.Metadata // zero when the instance was not reachable at startup
	Granted  []string          // scopes the token was granted
	Logger   *slog.Logger
	Version  string
}

// New builds the MCP server with every tool the configuration registers.
// A nil Client is allowed: every tool then answers [auth], which is what
// a client started before `gitlab-mcp login` should see.
func New(opts Options) *mcp.Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	so := &mcp.ServerOptions{Instructions: instructionsFor(opts.Config, opts.Metadata, opts.Granted)}
	// The SDK's own logger writes session chatter. It is attached only at
	// debug, where someone asked for it.
	if logger.Enabled(context.Background(), slog.LevelDebug) {
		so.Logger = logger
	}
	s := mcp.NewServer(&mcp.Implementation{Name: Name, Version: opts.Version}, so)
	s.AddReceivingMiddleware(logMethods(logger))
	svc := service.New(service.Options{Client: opts.Client, Config: opts.Config, Metadata: opts.Metadata, Granted: opts.Granted})
	tools.Register(s, tools.Deps{Service: svc, Config: opts.Config, Metadata: opts.Metadata, Granted: opts.Granted, Logger: logger})
	return s
}

// logMethods logs every method but a tool call, which the tools log
// themselves with their outcome class and rate bucket: the method, how
// it ended and how long it took, and nothing it carried (§9.2).
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
// included. The schema-diff gate compares two of these.
type Dump struct {
	Server            string                  `json:"server"`
	Version           string                  `json:"version"`
	SDK               string                  `json:"sdk"`
	Tools             []*mcp.Tool             `json:"tools"`
	ResourceTemplates []*mcp.ResourceTemplate `json:"resourceTemplates"`
}

// DumpSchemas writes the full tool surface — every flag and toolset on —
// as JSON, without touching the network or credentials. The SDK has no
// public enumerator, so an in-memory client asks the server, which is
// also exactly what a client would be told.
func DumpSchemas(w io.Writer, version string) error {
	ctx := context.Background()
	s := New(Options{Config: tools.FullSurface(config.Config{}), Version: version})
	ct, st := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, st, nil)
	if err != nil {
		return fmt.Errorf("connect server: %w", err)
	}
	defer func() { _ = ss.Close() }()
	client := mcp.NewClient(&mcp.Implementation{Name: "schema-dump", Version: version}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		return fmt.Errorf("connect client: %w", err)
	}
	defer func() { _ = cs.Close() }()
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("list tools: %w", err)
	}
	slices.SortFunc(list.Tools, func(a, b *mcp.Tool) int { return strings.Compare(a.Name, b.Name) })
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	// No resources yet; the field is written so the shape does not
	// change when they arrive.
	return enc.Encode(Dump{Server: Name, Version: version, SDK: sdkVersion(), Tools: list.Tools,
		ResourceTemplates: []*mcp.ResourceTemplate{}})
}

// sdkVersion is the MCP SDK version from the build info, so a diff an
// SDK upgrade caused can be told from a change to the surface.
func sdkVersion() string { return version.Module(sdkModule) }
