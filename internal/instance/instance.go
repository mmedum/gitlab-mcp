// Package instance normalizes the configured GitLab base URL, checks
// URLs against it, reads the instance's version and edition, and turns
// GitLab web URLs into tool arguments.
//
// The base URL is normalized once, so every request and every origin
// check compares the same scheme, host, port and sub-path (§4.9).
package instance

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ErrInvalid is wrapped by every error this package returns. The caller
// maps it to the [invalid] class.
var ErrInvalid = errors.New("invalid instance or URL")

// invalidError keeps the message free of the sentinel's text while
// still matching errors.Is(err, ErrInvalid).
type invalidError struct{ msg string }

func (e *invalidError) Error() string { return e.msg }
func (e *invalidError) Unwrap() error { return ErrInvalid }

func invalidf(format string, args ...any) error {
	return &invalidError{msg: fmt.Sprintf(format, args...)}
}

// apiSuffix is the REST root below the web base.
const apiSuffix = "/api/v4"

// Instance is a normalized GitLab base URL: scheme, host[:port] and an
// optional sub-path such as "/gitlab".
type Instance struct {
	scheme   string // "https" or "http"
	hostname string // lowercased, no trailing dot, no brackets
	port     string // "" when it is the scheme's default
	path     string // "" or "/gitlab", decoded, no trailing slash
}

// Parse normalizes a configured base URL. A missing scheme is read as
// https. http is refused for a host that is not loopback unless
// allowHTTP is set, because the token would travel in the clear.
func Parse(raw string, allowHTTP bool) (Instance, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Instance{}, invalidf("the instance URL is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		// url.Parse's error quotes the input, which may carry
		// credentials, so it is not wrapped.
		return Instance{}, invalidf("the instance URL does not parse as a URL")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return Instance{}, invalidf("the instance URL must use https (or http on loopback), not %q", u.Scheme)
	}
	if u.User != nil {
		return Instance{}, invalidf("credentials do not belong in the instance URL; sign in with login instead")
	}
	if u.Opaque != "" {
		return Instance{}, invalidf("the instance URL has no host")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return Instance{}, invalidf("the instance URL must not carry a query")
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return Instance{}, invalidf("the instance URL must not carry a fragment")
	}
	hostname, port, err := normalizeAuthority(u.Scheme, u.Hostname(), u.Port())
	if err != nil {
		return Instance{}, err
	}
	path, err := normalizeBasePath(u.Path)
	if err != nil {
		return Instance{}, err
	}
	if u.Scheme == "http" && !allowHTTP && !IsLoopback(hostname) {
		return Instance{}, invalidf("http is refused for %s because the token would travel in the clear; use https, or set GITLAB_MCP_ALLOW_HTTP=true to accept that", hostname)
	}
	return Instance{scheme: u.Scheme, hostname: hostname, port: port, path: path}, nil
}

// normalizeAuthority lowercases the host, drops a trailing dot and drops
// the scheme's default port, so equal origins compare equal as strings.
func normalizeAuthority(scheme, hostname, port string) (string, string, error) {
	hostname = strings.TrimSuffix(strings.ToLower(hostname), ".")
	if hostname == "" {
		return "", "", invalidf("the instance URL has no host")
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", "", invalidf("the instance URL has an invalid port")
		}
		port = strconv.Itoa(n)
	}
	if port == defaultPort(scheme) {
		port = ""
	}
	return hostname, port, nil
}

func defaultPort(scheme string) string {
	switch strings.ToLower(scheme) {
	case "https":
		return "443"
	case "http":
		return "80"
	}
	return ""
}

// normalizeBasePath collapses slashes, strips a trailing /api/v4 and
// refuses paths that cannot be an installation's sub-path.
func normalizeBasePath(p string) (string, error) {
	var segs []string
	for s := range strings.SplitSeq(p, "/") {
		switch s {
		case "":
			continue
		case ".", "..":
			return "", invalidf("the instance URL path must not contain %q segments", s)
		case "-":
			return "", invalidf("the instance URL looks like a page URL (it contains /-/); give the instance's base URL, such as https://gitlab.example.com")
		}
		segs = append(segs, s)
	}
	if n := len(segs); n >= 2 && strings.EqualFold(segs[n-2], "api") && strings.EqualFold(segs[n-1], "v4") {
		segs = segs[:n-2]
	}
	for j := 0; j+1 < len(segs); j++ {
		if strings.EqualFold(segs[j], "api") && strings.EqualFold(segs[j+1], "v4") {
			return "", invalidf("the instance URL points at an API endpoint; give the instance's base URL, such as https://gitlab.example.com")
		}
	}
	if len(segs) == 0 {
		return "", nil
	}
	return "/" + strings.Join(segs, "/"), nil
}

// IsLoopback reports whether hostname is localhost or a loopback
// address (127.0.0.0/8 or ::1). Brackets and a trailing dot are
// tolerated.
func IsLoopback(hostname string) bool {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// IsZero reports whether i was never parsed.
func (i Instance) IsZero() bool { return i.scheme == "" }

// Scheme is "https" or "http".
func (i Instance) Scheme() string { return i.scheme }

// Host is host[:port] as it appears in a URL authority, with an IPv6
// address bracketed and the default port left out.
func (i Instance) Host() string { return joinHost(i.hostname, i.port) }

func joinHost(hostname, port string) string {
	if port != "" {
		return net.JoinHostPort(hostname, port)
	}
	if strings.Contains(hostname, ":") {
		return "[" + hostname + "]"
	}
	return hostname
}

// Hostname is the host without port or brackets.
func (i Instance) Hostname() string { return i.hostname }

// Path is the sub-path, "" or for example "/gitlab".
func (i Instance) Path() string { return i.path }

// effectivePort is the port a connection uses, default included.
func (i Instance) effectivePort() string {
	if i.port != "" {
		return i.port
	}
	return defaultPort(i.scheme)
}

// WebBase is the instance's web root. Each call returns a fresh copy,
// so callers may modify it.
func (i Instance) WebBase() *url.URL {
	return &url.URL{Scheme: i.scheme, Host: i.Host(), Path: i.path}
}

// APIRoot is WebBase plus /api/v4.
func (i Instance) APIRoot() *url.URL {
	return &url.URL{Scheme: i.scheme, Host: i.Host(), Path: i.path + apiSuffix}
}

// String is the web root as a URL string.
func (i Instance) String() string {
	if i.IsZero() {
		return ""
	}
	return i.WebBase().String()
}

// SameOrigin reports whether u has the instance's scheme, host and
// port. Host case and default ports are normalized.
func (i Instance) SameOrigin(u *url.URL) bool {
	if i.IsZero() || u == nil || u.Opaque != "" {
		return false
	}
	if !strings.EqualFold(u.Scheme, i.scheme) {
		return false
	}
	hostname, port, err := normalizeAuthority(u.Scheme, u.Hostname(), u.Port())
	if err != nil {
		return false
	}
	return hostname == i.hostname && port == i.port
}

// UnderAPIRoot reports whether u may carry the token: same origin, no
// credentials, and a path at or below the API root with no dot
// segments, raw or percent-encoded. It guards following a Link header
// (§11).
func (i Instance) UnderAPIRoot(u *url.URL) bool {
	if !i.SameOrigin(u) || u.User != nil {
		return false
	}
	p := u.EscapedPath()
	for seg := range strings.SplitSeq(p, "/") {
		if isDotSegment(seg) {
			return false
		}
		dec, err := url.PathUnescape(seg)
		if err != nil {
			return false
		}
		// An encoded slash can hide a dot segment inside one raw segment.
		for part := range strings.SplitSeq(dec, "/") {
			if isDotSegment(part) {
				return false
			}
		}
	}
	root := i.APIRoot().EscapedPath()
	return p == root || strings.HasPrefix(p, root+"/")
}

func isDotSegment(s string) bool { return s == "." || s == ".." }
