package tools

import (
	"context"
	"log/slog"
	"net/url"
	"path/filepath"
	"runtime"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The client's roots, which upload_file may read images from beside the
// directories GITLAB_MCP_UPLOAD_DIRS names (docs/architecture.md §7.10).
// MCP's roots are how a client says which directories a server may work
// in, and the specification says a server should hold paths to them.

// rootsWait bounds the wait for a client to list its roots.
const rootsWait = 5 * time.Second

// clientRoots asks the client of one call for its roots, when the call
// needs them.
type clientRoots struct {
	req *mcp.CallToolRequest
	lg  *slog.Logger
}

// Roots lists the local directories the client shares as its roots, or
// none: when it declared no roots capability, when it does not answer,
// and from protocol 2026-07-28, where the SDK does not ask in the middle
// of a call and roots are deprecated (SEP-2577), so the setting is the
// way to allow a directory (§18 row 114).
func (r clientRoots) Roots(ctx context.Context) []string {
	if r.req == nil || r.req.Session == nil {
		return nil
	}
	// Roots are deprecated from 2026-07-28 (SEP-2577) and served through
	// the deprecation window; after it the setting is the way.
	if c := r.req.ClientCapabilities(); c == nil || c.RootsV2 == nil { //nolint:staticcheck // SA1019: deprecated, still served (§18 row 114)
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, rootsWait)
	defer cancel()
	res, err := r.req.Session.ListRoots(ctx, nil) //nolint:staticcheck // SA1019: deprecated, still served (§18 row 114)
	if err != nil {
		// The client's or the SDK's words are not logged; that the roots
		// could not be read is.
		r.lg.Debug("client roots unavailable")
		return nil
	}
	var dirs []string
	for _, root := range res.Roots {
		if root != nil {
			if dir, ok := rootDir(root.URI); ok {
				dirs = append(dirs, dir)
			}
		}
	}
	return dirs
}

// rootDir is the local directory a root's file: URI names. Roots are
// file: URIs for now, and one on another host is no directory here.
func rootDir(uri string) (string, bool) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" || u.Host != "" && u.Host != "localhost" {
		return "", false
	}
	p := u.Path
	// file:///C:/work on Windows: the drive follows a slash.
	if runtime.GOOS == "windows" && len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	p = filepath.FromSlash(p)
	if !filepath.IsAbs(p) {
		return "", false
	}
	return filepath.Clean(p), true
}
