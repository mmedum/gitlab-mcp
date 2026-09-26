// Package tools registers the MCP tools. A handler validates its input,
// calls the service and returns the model; register decides everything
// every tool shares — annotations, gating, decoding, errors, the two
// halves of the reply and the log line — once.
package tools

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/gitlab-mcp/internal/config"
	"github.com/mmedum/gitlab-mcp/internal/instance"
	"github.com/mmedum/gitlab-mcp/internal/service"
)

// definitions is every tool this server has, in the order §8 lists
// them. Adding a tool here is the whole of registering it.
func definitions() []definition {
	return []definition{
		getMe(), resolveURL(),
		searchProjects(), getProject(),
		searchIssues(), getIssue(), listDiscussions(),
		searchMergeRequests(), getMergeRequest(),
		getFile(), listTree(), listBranches(), listCommits(), getCommit(),
	}
}

// FullSurface is the configuration under which every tool registers:
// the schema dump and the schema diff compare the whole registrable
// surface, so a gated tool is neither reported as new on every run nor
// lost without notice. It names every gate gate() consults, which
// TestFullSurfaceRegistersEverything holds.
func FullSurface(cfg config.Config) config.Config {
	cfg.ReadOnly = false
	cfg.EnableShip = true
	cfg.EnableDestructive = true
	cfg.Toolsets = append([]string(nil), config.Toolsets...)
	return cfg
}

// Register adds every tool the configuration allows and returns what it
// registered, which it also tells the service, so get_me and resolve_url
// report the surface that exists.
func Register(s *mcp.Server, d Deps) []service.Registered {
	return register(s, d, definitions())
}

func register(s *mcp.Server, d Deps, defs []definition) []service.Registered {
	defs = allowed(defs, d.Config, d.Metadata, d.Granted)
	for _, def := range defs {
		def.add(s, d)
	}
	out := registered(defs)
	if d.Service != nil {
		d.Service.SetRegistered(out)
	}
	return out
}

// Surface is what Register would register under a configuration,
// without building a server. The server instructions are written from
// it, and from what a flag would add to it.
func Surface(cfg config.Config, meta instance.Metadata, granted []string) []service.Registered {
	return surface(definitions(), cfg, meta, granted)
}

func surface(defs []definition, cfg config.Config, meta instance.Metadata, granted []string) []service.Registered {
	return registered(allowed(defs, cfg, meta, granted))
}

// allowed is the definitions the gates let through, each gated once.
func allowed(defs []definition, cfg config.Config, meta instance.Metadata, granted []string) []definition {
	var out []definition
	for _, def := range defs {
		if gate(def.spec(), cfg, meta, granted) == "" {
			out = append(out, def)
		}
	}
	return out
}

func registered(defs []definition) []service.Registered {
	out := make([]service.Registered, len(defs))
	for i, def := range defs {
		out[i] = service.Registered{Name: def.spec().Name, Kind: def.spec().Kind.Scope()}
	}
	return out
}
