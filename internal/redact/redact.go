// Package redact masks what this server prints for a person and must
// not survive being pasted somewhere else, and the secrets a job log can
// carry.
//
// `status` and `doctor` print for a human, and the bug form asks for
// their output. An instance's host names an organization, a project path
// names its work, and a username or an address names a person. GitLab's
// error text and a request URL carry all of them by routes nobody chose.
// They are masked here, in one place, where the text is produced — not
// in a Writer, which could see an address split across two writes and
// miss it — so a print added later is safe without its author knowing
// the rule.
package redact

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"syscall"
	"unicode/utf8"
)

// The shapes this package masks, as pattern text so the maintainer
// tooling under scripts/ matches the same ones.
const (
	// TokenPattern is every token shape GitLab documents, from the token
	// prefix table at https://docs.gitlab.com/security/tokens/ (read
	// 2026-09-26): personal, project, group and impersonation tokens
	// (glpat-), OAuth application secrets (gloas-), deploy tokens
	// (gldt-), runner authentication tokens (glrt-, glrtr-), CI/CD job
	// tokens (glcbt-), trigger tokens (glptt-), feed tokens (glft-),
	// incoming mail tokens (glimt-), agent for Kubernetes tokens
	// (glagent-), workspace tokens (glwt-), SCIM tokens (glsoat-),
	// feature flag client tokens (glffct-) and the session cookie
	// (_gitlab_session=). The legacy runner registration token prefix
	// GR1348941 is on GitLab's runner pages rather than that table.
	//
	// The body takes dots, because a routable token carries dotted
	// segments after the random part; a trailing sentence period is left
	// alone. An instance can change the personal access token prefix,
	// and a token with a custom prefix has no shape to find.
	TokenPattern = `\b(?:glpat|gloas|gldt|glrtr|glrt|glcbt|glptt|glft|glimt|glagent|glwt|glsoat|glffct)-[0-9A-Za-z_\-]{20,}(?:\.[0-9A-Za-z_\-]+)*` +
		`|\bGR1348941[0-9A-Za-z_\-]{20,}` +
		`|_gitlab_session=[0-9A-Za-z]+`

	// AddressPattern is an email address in free text. The local part
	// takes "…", so an address already masked here is matched whole and
	// masked to the same thing again rather than half of it surviving.
	AddressPattern = `[A-Za-z0-9._%+\-=…]+@[A-Za-z0-9.\-…]+\.[A-Za-z]{2,}`

	// ClientIDPattern is an OAuth application id as GitLab shows it: 64
	// lowercase hex characters. A SHA-1 commit id is 40 and is left
	// alone; a SHA-256 one would be masked, which is the safe direction.
	ClientIDPattern = `\b[0-9a-f]{64}\b`

	// URLPattern is an http or https URL in free text.
	URLPattern = `https?://[^\s"'<>()\[\]{}]+`
)

var (
	token    = regexp.MustCompile(TokenPattern)
	address  = regexp.MustCompile(AddressPattern)
	clientID = regexp.MustCompile(ClientIDPattern)
	urlRE    = regexp.MustCompile(URLPattern)
)

// Kinds of value, as a Masker's placeholders name them.
const (
	KindToken    = "token"
	KindHost     = "host"
	KindPath     = "path"
	KindQuery    = "query"
	KindUser     = "user"
	KindEmail    = "email"
	KindClientID = "client-id"
)

// replacer turns one found value of a kind into what stands in its
// place. The package functions use a fixed marker per kind; a Masker
// numbers them.
type replacer func(kind, value string) string

// marker is the package functions' replacer: <kind>.
func marker(kind, _ string) string { return "<" + kind + ">" }

// Tokens replaces every GitLab token shape with <token>.
func Tokens(s string) string { return token.ReplaceAllString(s, "<token>") }

// ClientID replaces every OAuth application id shape with <client-id>.
func ClientID(s string) string { return clientID.ReplaceAllString(s, "<client-id>") }

// Email keeps enough of an address for its owner to recognize it and not
// enough for anyone else to use it. The domain goes too: for somebody
// else's address it names their organization.
func Email(addr string) string {
	local, domain, ok := strings.Cut(addr, "@")
	if !ok || local == "" || domain == "" {
		return mask(addr)
	}
	tld := ""
	if i := strings.LastIndexByte(domain, '.'); i >= 0 {
		tld, domain = domain[i:], domain[:i]
	}
	return mask(local) + "@" + mask(domain) + tld
}

// Addresses masks every address in s with Email.
func Addresses(s string) string { return address.ReplaceAllStringFunc(s, Email) }

// publicHosts name no organization, and "gitlab.com or the test
// instance" is the first thing a diagnosis asks. Loopback names nobody
// either.
var publicHosts = map[string]bool{
	"gitlab.com": true, "localhost": true, "127.0.0.1": true, "::1": true,
}

// keptPrefixes are URL paths GitLab uses for itself rather than for a
// namespace. Everything else after the host is a group, a user or a
// project, and is masked.
var keptPrefixes = []string{"/api/v4", "/oauth", "/-"}

// namedSegment is an API path segment that names a project, group,
// user or namespace, which may be a path rather than a number.
var namedSegment = regexp.MustCompile(`/(projects|groups|users|namespaces)/([^/?#]+)`)

// maskHost masks a host unless it is gitlab.com or loopback. The port is
// kept: it says nothing about who runs the instance.
func maskHost(h string, rep replacer) string {
	name, port := h, ""
	if n, p, err := net.SplitHostPort(h); err == nil {
		name, port = n, ":"+p
	}
	name = strings.Trim(name, "[]")
	if publicHosts[asciiLower(name)] {
		return h
	}
	return rep(KindHost, asciiLower(name)) + port
}

// URL masks one URL: the host unless public, a namespace or project
// path, an API resource named by path, any user information, the query
// and the fragment. What GitLab uses for itself — /api/v4, /oauth — is
// kept, because which endpoint failed is most of a diagnosis.
func URL(raw string) string { return maskURL(raw, marker) }

func maskURL(raw string, rep replacer) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return rep("url", raw)
	}
	out := u.Scheme + "://" + maskHost(u.Host, rep)
	path := u.EscapedPath()
	switch {
	case path == "" || path == "/":
		out += path
	case hasKeptPrefix(path):
		out += namedSegment.ReplaceAllStringFunc(path, func(m string) string {
			parts := namedSegment.FindStringSubmatch(m)
			if isDigits(parts[2]) {
				return m
			}
			name, err := url.PathUnescape(parts[2])
			if err != nil {
				name = parts[2]
			}
			return "/" + parts[1] + "/" + rep(KindPath, name)
		})
	default:
		out += "/" + rep(KindPath, strings.Trim(u.Path, "/"))
	}
	if u.RawQuery != "" || u.ForceQuery {
		out += "?" + rep(KindQuery, u.RawQuery)
	}
	return out
}

// URLs masks every URL in s with URL.
func URLs(s string) string { return urlRE.ReplaceAllStringFunc(s, URL) }

// Text masks everything in s that has a shape: tokens, URLs, addresses
// and application ids. It is for text this server did not write, such
// as an error, whose values are not known in advance. Tokens go first,
// so nothing after them can split one and leave half behind.
func Text(s string) string {
	s = Tokens(s)
	s = URLs(s)
	s = Addresses(s)
	return ClientID(s)
}

// Clip masks s with Text and then truncates it with Truncate. In that
// order: truncating first can cut a value short of its shape, and the
// rest of it then survives.
func Clip(s string, n int) string { return Truncate(Text(s), n) }

// Truncate cuts s to at most n bytes, on a rune boundary, marking the
// cut with "…". It masks nothing.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := max(n, 0)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// ID truncates an identifier to a correlation key that cannot be looked
// up or pasted into a URL: its first six characters. The logging rule
// (§9.2) allows that much and no more.
func ID(id string) string {
	r := []rune(id)
	if len(r) <= 6 {
		return "[id]"
	}
	return string(r[:6]) + "…"
}

// NetError renders a transport error without the host names, addresses
// and URLs net, net/http and crypto/x509 put in it, which logs may not
// carry (§9.2): a lookup names the host it looked up, a dial the
// address, a request the URL, and a certificate for the wrong host names
// both hosts.
//
// It is an allowlist. Only shapes whose text is known to be fixed are
// rendered, each in words of its own; anything else becomes a fixed
// phrase, because an error nobody has read may carry any of those.
func NetError(err error) string {
	var ue *url.Error
	var dns *net.DNSError
	var op *net.OpError
	var errno syscall.Errno
	var alert tls.AlertError
	var header tls.RecordHeaderError
	switch {
	case errors.As(err, &dns):
		return "lookup: " + lookupFailure(dns)
	case IsCertificateError(err):
		return certificate(err)
	case errors.As(err, &op) && op.Err != nil:
		// Op is a fixed word: dial, read, write.
		return op.Op + ": " + NetError(op.Err)
	case errors.As(err, &ue) && ue.Err != nil:
		return NetError(ue.Err)
	case errors.Is(err, context.Canceled):
		return "canceled"
	case isTimeout(err):
		return "timed out"
	case errors.As(err, &errno):
		// The operating system's own text for the code: "connection
		// refused", "connection reset by peer".
		return errno.Error()
	case errors.As(err, &alert):
		return alert.Error()
	case errors.As(err, &header):
		return "tls: the server did not answer with TLS"
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return "the connection closed before the answer was complete"
	}
	return "the connection failed"
}

// lookupFailure says why a name lookup failed. The DNSError's own text
// can quote the resolver's address.
func lookupFailure(dns *net.DNSError) string {
	switch {
	case dns.IsNotFound:
		return "no such host"
	case dns.IsTimeout:
		return "timed out"
	case dns.IsTemporary:
		return "temporary failure in name resolution"
	}
	return "the name could not be resolved"
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

// IsCertificateError reports a certificate the client refused: an
// unknown authority, a name that does not match, one expired or unfit.
func IsCertificateError(err error) bool {
	var verify *tls.CertificateVerificationError
	var hostname x509.HostnameError
	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	return errors.As(err, &verify) || errors.As(err, &hostname) ||
		errors.As(err, &unknown) || errors.As(err, &invalid)
}

// certificate describes why a certificate was refused in words of its
// own: x509's text quotes the certificate's names.
func certificate(err error) string {
	var hostname x509.HostnameError
	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	switch {
	case errors.As(err, &hostname):
		return "x509: the certificate is not valid for the instance's host name"
	case errors.As(err, &unknown):
		return "x509: certificate signed by unknown authority"
	case errors.As(err, &invalid) && invalid.Reason == x509.Expired:
		return "x509: certificate has expired or is not yet valid"
	case errors.As(err, &invalid):
		return "x509: the certificate is not valid for this use"
	}
	return "tls: the certificate was not accepted"
}

// mask keeps the first rune and replaces the rest with an ellipsis, so
// the result cannot be mistaken for a short value. A value already
// masked comes out unchanged.
func mask(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return ""
	}
	return string(r[0]) + "…"
}

func hasKeptPrefix(path string) bool {
	for _, p := range keptPrefixes {
		if path == p || strings.HasPrefix(path, strings.TrimSuffix(p, "/")+"/") {
			return true
		}
	}
	return false
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
