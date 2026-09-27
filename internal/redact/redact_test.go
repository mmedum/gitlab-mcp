package redact

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
)

// Secret-shaped values are built by concatenation, so no whole token
// shape sits in the source for a scanner to flag, and no allow-list
// entry has to exist for one.
var (
	body20    = strings.Repeat("Ab1_", 5)
	fakePAT   = "glpat" + "-" + body20
	fakeAppID = strings.Repeat("0123456789abcdef", 4)
)

func TestTokensEveryPrefix(t *testing.T) {
	prefixes := []string{
		"glpat-", "gloas-", "gldt-", "glrt-", "glrtr-", "glcbt-", "glptt-", "glft-",
		"glimt-", "glagent-", "glwt-", "glsoat-", "glffct-", "GR1348941",
	}
	for _, p := range prefixes {
		in := "value " + p + body20 + " end"
		if got, want := Tokens(in), "value <token> end"; got != want {
			t.Errorf("Tokens(%q prefix) = %q, want %q", p, got, want)
		}
	}
	if got, want := Tokens("cookie _gitlab_session="+"0123abcd; path=/"), "cookie <token>; path=/"; got != want {
		t.Errorf("session cookie: %q, want %q", got, want)
	}
}

func TestTokensShapes(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"routable token with dotted segments", "t " + fakePAT + ".01.1a2b3c4d5 x", "t <token> x"},
		{"sentence period kept", "token " + fakePAT + ".", "token <token>."},
		{"job token with partition", "glcbt" + "-64_" + body20, "<token>"},
		{"too short to be a token", "glpat-short", "glpat-short"},
		{"prefix inside a word", "xglpat-" + body20, "xglpat-" + body20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Tokens(tt.in); got != tt.want {
				t.Errorf("Tokens(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestEmail(t *testing.T) {
	tests := map[string]string{
		"alice@example.com":    "a…@e….com",
		"a@b.co.test":          "a…@b….test",
		"not-an-address":       "n…",
		"":                     "",
		"@example.com":         "@…",
		"ünïcode@exämple.test": "ü…@e….test",
	}
	for in, want := range tests {
		if got := Email(in); got != want {
			t.Errorf("Email(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestAddressesMatchesAnAlreadyMaskedValue: text masked once and passed
// through again must come out the same, not with half an address left.
func TestAddressesMatchesAnAlreadyMaskedValue(t *testing.T) {
	in := "denied for alice@example.com and bob.b@example.org"
	once := Addresses(in)
	if want := "denied for a…@e….com and b…@e….org"; once != want {
		t.Fatalf("Addresses = %q, want %q", once, want)
	}
	if twice := Addresses(once); twice != once {
		t.Errorf("a second pass changed %q to %q", once, twice)
	}
}

func TestClientID(t *testing.T) {
	if got, want := ClientID("application "+fakeAppID+" refused"), "application <client-id> refused"; got != want {
		t.Errorf("ClientID = %q, want %q", got, want)
	}
	sha1 := "0123456789abcdef0123456789abcdef01234567"
	if got := ClientID("commit " + sha1); got != "commit "+sha1 {
		t.Errorf("ClientID masked a commit id: %q", got)
	}
	pseudo := "v0.0.0-20260925101500-3f2a9c1b7d4e"
	if got := ClientID(pseudo); got != pseudo {
		t.Errorf("ClientID masked a pseudo-version: %q", got)
	}
}

func TestMaskHost(t *testing.T) {
	tests := map[string]string{
		"gitlab.com":              "gitlab.com",
		"GitLab.com":              "GitLab.com",
		"gitlab.example.com":      "<host>",
		"gitlab.example.com:8443": "<host>:8443",
		"127.0.0.1:3000":          "127.0.0.1:3000",
		"[::1]:3000":              "[::1]:3000",
		"[fd00::1]:3000":          "<host>:3000",
	}
	for in, want := range tests {
		if got := maskHost(in, marker); got != want {
			t.Errorf("maskHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://gitlab.example.com/example-group/app/-/issues/3", "https://<host>/<path>"},
		{"https://gitlab.com/example-group/app", "https://gitlab.com/<path>"},
		{"https://gitlab.example.com/api/v4/user", "https://<host>/api/v4/user"},
		{"https://gitlab.example.com/api/v4/projects/example-group%2Fapp/issues?search=secret", "https://<host>/api/v4/projects/<path>/issues?<query>"},
		{"https://gitlab.example.com/api/v4/projects/42/merge_requests", "https://<host>/api/v4/projects/42/merge_requests"},
		{"https://gitlab.example.com/api/v4/users/alice", "https://<host>/api/v4/users/<path>"},
		{"https://gitlab.example.com/oauth/token", "https://<host>/oauth/token"},
		{"https://gitlab.example.com/", "https://<host>/"},
		{"https://alice:hunter2@gitlab.example.com/x", "https://<host>/<path>"},
		{"https://gitlab.example.com/gitlab/api/v4/user", "https://<host>/<path>"},
	}
	for _, tt := range tests {
		if got := URL(tt.in); got != tt.want {
			t.Errorf("URL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestText(t *testing.T) {
	in := `Get "https://gitlab.example.com/api/v4/projects/example-group%2Fapp": token ` + fakePAT +
		" for alice@example.com, application " + fakeAppID
	want := `Get "https://<host>/api/v4/projects/<path>": token <token> for a…@e….com, application <client-id>`
	if got := Text(in); got != want {
		t.Errorf("Text =\n%q\nwant\n%q", got, want)
	}
}

// TestClipRedactsBeforeTruncating: a token cut in half by the limit no
// longer has its shape, and its first half would survive.
func TestClipRedactsBeforeTruncating(t *testing.T) {
	in := "token " + fakePAT + " trailing text"
	got := Clip(in, 20)
	if strings.Contains(got, "glpat") || strings.Contains(got, body20[:4]) {
		t.Errorf("Clip left part of the token: %q", got)
	}
	if want := "token <token> traili…"; got != want {
		t.Errorf("Clip = %q, want %q", got, want)
	}
	if got := Clip("short", 20); got != "short" {
		t.Errorf("Clip(short) = %q", got)
	}
	// A cut inside a multi-byte rune backs off to the rune's start.
	if got := Clip("ab…cd", 3); got != "ab…" {
		t.Errorf("Clip across a rune = %q, want %q", got, "ab…")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 20, "short"},
		{"exactly", 7, "exactly"},
		{"ab…cd", 3, "ab…"},
		{"ab…cd", 4, "ab…"},
		{"ab…cd", 5, "ab……"},
		{"é", 1, "…"},
		{"abc", 0, "…"},
	}
	for _, tt := range tests {
		if got := Truncate(tt.in, tt.n); got != tt.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}

// TestNetErrorIsAnAllowlist: known shapes render in fixed words, and
// anything else becomes a fixed phrase, never its own text, which can
// name a host, an address or a URL.
func TestNetErrorIsAnAllowlist(t *testing.T) {
	const host = "canary-host.example.net"
	dns := &net.DNSError{Name: host, Server: "10.9.8.7:53", Err: "lookup " + host + " on 10.9.8.7:53", IsNotFound: true}
	hostname := x509.HostnameError{Certificate: &x509.Certificate{DNSNames: []string{"canary-cert.example.net"}}, Host: host}
	unknown := x509.UnknownAuthorityError{Cert: &x509.Certificate{Subject: pkix.Name{CommonName: "canary-ca.example.net"}}}
	inURL := func(err error) error { return &url.Error{Op: "Get", URL: "https://" + host + "/api/v4/user", Err: err} }
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"lookup in a dial", &net.OpError{Op: "dial", Net: "tcp", Err: dns}, "lookup: no such host"},
		{"lookup in a url error", inURL(&net.OpError{Op: "dial", Net: "tcp", Err: dns}), "lookup: no such host"},
		{"lookup timing out", &net.DNSError{Name: host, Err: "i/o timeout on " + host, IsTimeout: true}, "lookup: timed out"},
		{"refused dial", &net.OpError{Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.IPv4(10, 9, 8, 7), Port: 443},
			Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}, "dial: " + syscall.ECONNREFUSED.Error()},
		{"wrong host name", &tls.CertificateVerificationError{Err: hostname},
			"x509: the certificate is not valid for the instance's host name"},
		{"unknown authority in a url error", inURL(&tls.CertificateVerificationError{Err: unknown}),
			"x509: certificate signed by unknown authority"},
		{"url error in a url error", inURL(inURL(&tls.CertificateVerificationError{Err: unknown})),
			"x509: certificate signed by unknown authority"},
		{"expired", x509.CertificateInvalidError{Reason: x509.Expired}, "x509: certificate has expired or is not yet valid"},
		{"deadline", inURL(context.DeadlineExceeded), "timed out"},
		{"canceled", inURL(context.Canceled), "canceled"},
		{"cut short", inURL(io.ErrUnexpectedEOF), "the connection closed before the answer was complete"},
		{"not TLS", tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"},
			"tls: the server did not answer with TLS"},
		{"unknown shape", errors.New("proxy " + host + " said no"), "the connection failed"},
		{"unknown shape in a url error", inURL(errors.New("proxyconnect " + host)), "the connection failed"},
		{"a url error with no cause", &url.Error{Op: "Get", URL: "https://" + host}, "the connection failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NetError(tt.err)
			if got != tt.want {
				t.Errorf("NetError = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, "canary") || strings.Contains(got, "10.9.8.7") {
				t.Errorf("NetError leaked a name: %q", got)
			}
		})
	}
}

func TestIsCertificateError(t *testing.T) {
	yes := []error{
		&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}},
		&url.Error{Op: "Get", URL: "https://gitlab.example.com", Err: x509.HostnameError{Certificate: &x509.Certificate{}}},
		x509.CertificateInvalidError{Reason: x509.Expired},
	}
	for _, err := range yes {
		if !IsCertificateError(err) {
			t.Errorf("IsCertificateError(%T) = false", err)
		}
	}
	if IsCertificateError(&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}) {
		t.Error("a refused dial counted as a certificate error")
	}
}

func TestID(t *testing.T) {
	tests := map[string]string{
		"18c2f0a1b2c3d4e5": "18c2f0…",
		"abcdefg":          "abcdef…",
		"abcdef":           "[id]",
		"":                 "[id]",
	}
	for in, want := range tests {
		if got := ID(in); got != want {
			t.Errorf("ID(%q) = %q, want %q", in, got, want)
		}
	}
}
