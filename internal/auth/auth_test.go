package auth

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/gapi/gitlabtest"
	"github.com/mmedum/gitlab-mcp/internal/instance"
)

// clock is a settable time shared by the instance and the code under
// test.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newApp(t *testing.T, srv *gitlabtest.Server, c *clock) *Application {
	t.Helper()
	inst, err := instance.Parse(srv.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	a := &Application{Instance: inst, ClientID: gitlabtest.ClientID, Timeout: 5 * time.Second}
	if c != nil {
		a.Now = c.Now
	}
	return a
}

// browser follows the authorization URL through GitLab's redirect to
// the callback, as a person's browser would, and records what it saw.
type browser struct {
	mu   sync.Mutex
	seen []*url.URL
	// callback, when set, replaces the callback's query.
	callback func(q url.Values) url.Values
	done     chan error
}

func newBrowser() *browser { return &browser{done: make(chan error, 4)} }

func (b *browser) open(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.seen = append(b.seen, u)
	b.mu.Unlock()
	go func() { b.done <- b.follow(raw) }()
	return nil
}

func (b *browser) follow(raw string) error {
	stop := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := stop.Get(raw) //nolint:noctx // test browser
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Host == "" {
		return errors.New("the authorization answered without a redirect")
	}
	if b.callback != nil {
		loc.RawQuery = b.callback(loc.Query()).Encode()
	}
	resp, err = http.Get(loc.String()) //nolint:noctx // test browser
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func (b *browser) last() *url.URL {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seen[len(b.seen)-1]
}

func TestLoginRunsPKCEOnALoopbackPortAndReportsTheGrant(t *testing.T) {
	srv := gitlabtest.New(t, gitlabtest.Options{})
	app := newApp(t, srv, nil)
	b := newBrowser()
	var out bytes.Buffer
	g, err := app.Login(context.Background(), []string{"api"}, LoginOptions{OpenBrowser: b.open, Out: &out})
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out.String())
	}
	if g.Token.AccessToken == "" || g.Token.RefreshToken == "" {
		t.Fatalf("grant carries no pair: %+v", g.Token)
	}
	if strings.Join(g.Scopes, " ") != "api" {
		t.Errorf("granted %v, want [api]", g.Scopes)
	}
	// 7,200 s is the instance's default lifetime; the expiry is read
	// from expires_in.
	if left := time.Until(g.Token.Expiry); left < 7100*time.Second || left > 7200*time.Second {
		t.Errorf("expiry %s from now, want about 7200s", left)
	}

	q := b.last().Query()
	for k, want := range map[string]string{
		"client_id": gitlabtest.ClientID, "response_type": "code", "scope": "api", "code_challenge_method": "S256",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("authorize %s = %q, want %q", k, got, want)
		}
	}
	for _, absent := range []string{"resource", "client_secret"} {
		if q.Has(absent) {
			t.Errorf("authorize request carries %s", absent)
		}
	}
	if len(q.Get("code_challenge")) != 43 || q.Get("state") == "" {
		t.Errorf("challenge %q, state %q", q.Get("code_challenge"), q.Get("state"))
	}
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		t.Fatal(err)
	}
	if redirect.Scheme != "http" || redirect.Hostname() != "127.0.0.1" || redirect.Path != "/callback" || redirect.Port() == "" {
		t.Errorf("redirect_uri %s, want http://127.0.0.1:<port>/callback", redirect)
	}
	if !strings.Contains(out.String(), "Open this URL in your browser to authorize gitlab-mcp") {
		t.Errorf("output %q", out.String())
	}
}

func TestLoginAcceptsAnyLoopbackPort(t *testing.T) {
	srv := gitlabtest.New(t, gitlabtest.Options{})
	app := newApp(t, srv, nil)
	ports := map[string]bool{}
	for range 2 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		b := newBrowser()
		if _, err := app.Login(context.Background(), []string{"read_api"}, LoginOptions{OpenBrowser: b.open, Listener: ln}); err != nil {
			t.Fatalf("login on %s: %v", ln.Addr(), err)
		}
		u, _ := url.Parse(b.last().Query().Get("redirect_uri"))
		ports[u.Port()] = true
	}
	if len(ports) != 2 {
		t.Errorf("both logins used one port: %v", ports)
	}
}

// TestLoginIgnoresACallbackWithoutItsState: a request to the callback
// that does not carry this login's state came from somewhere other than
// the authorization, so it is refused on its own and the login keeps
// waiting for the real redirect.
func TestLoginIgnoresACallbackWithoutItsState(t *testing.T) {
	srv := gitlabtest.New(t, gitlabtest.Options{})
	app := newApp(t, srv, nil)
	statuses := make(chan []int, 1)
	open := func(raw string) error {
		go func() {
			stop := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			resp, err := stop.Get(raw) //nolint:noctx // test browser
			if err != nil {
				statuses <- nil
				return
			}
			_ = resp.Body.Close()
			loc, _ := url.Parse(resp.Header.Get("Location"))
			real := loc.Query()
			var got []int
			for _, q := range []url.Values{
				{"state": {"forged"}, "code": {real.Get("code")}},
				{"error": {"access_denied"}},
				{"state": {"forged"}, "error": {"access_denied"}},
				real,
			} {
				u := *loc
				u.RawQuery = q.Encode()
				resp, err := http.Get(u.String()) //nolint:noctx // test browser
				if err != nil {
					got = append(got, 0)
					continue
				}
				_ = resp.Body.Close()
				got = append(got, resp.StatusCode)
			}
			statuses <- got
		}()
		return nil
	}
	g, err := app.Login(context.Background(), []string{"api"}, LoginOptions{OpenBrowser: open, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if g.Token.AccessToken == "" {
		t.Error("no token")
	}
	if got := <-statuses; !slices.Equal(got, []int{400, 400, 400, 200}) {
		t.Errorf("callback statuses = %v, want three refused and the real one accepted", got)
	}
}

func TestLoginReportsADeniedAuthorization(t *testing.T) {
	srv := gitlabtest.New(t, gitlabtest.Options{})
	app := newApp(t, srv, nil)
	b := newBrowser()
	b.callback = func(q url.Values) url.Values { q.Del("code"); q.Set("error", "access_denied"); return q }
	_, err := app.Login(context.Background(), []string{"api"}, LoginOptions{OpenBrowser: b.open})
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("err = %v, want access_denied", err)
	}
}

func TestLoginToAConfidentialApplicationSaysToUntickIt(t *testing.T) {
	srv := gitlabtest.New(t, gitlabtest.Options{Confidential: true, ClientSecret: "not-sent"})
	app := newApp(t, srv, nil)
	b := newBrowser()
	_, err := app.Login(context.Background(), []string{"api"}, LoginOptions{OpenBrowser: b.open})
	if !errors.Is(err, ErrConfidential) {
		t.Fatalf("err = %v, want ErrConfidential", err)
	}
	if !strings.Contains(err.Error(), `untick "Confidential"`) {
		t.Errorf("err %q does not say what to change", err)
	}
}

// syncBuffer is a Writer the test can read while Login writes to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestLoginWithoutABrowserPrintsTheSSHForward(t *testing.T) {
	srv := gitlabtest.New(t, gitlabtest.Options{})
	app := newApp(t, srv, nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	out := &syncBuffer{}
	opened := false
	result := make(chan error, 1)
	go func() {
		_, err := app.Login(context.Background(), []string{"api"}, LoginOptions{
			NoBrowser: true, Out: out, Listener: ln,
			OpenBrowser: func(string) error { opened = true; return nil },
		})
		result <- err
	}()

	urlRE := regexp.MustCompile(`http://\S+/oauth/authorize\?\S+`)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "Waiting for the browser") {
		if time.Now().After(deadline) {
			t.Fatalf("no prompt: %q", out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	text := out.String()
	want := "ssh -L " + strconv.Itoa(port) + ":127.0.0.1:" + strconv.Itoa(port) + " <this-host>"
	if !strings.Contains(text, want) {
		t.Errorf("output lacks %q:\n%s", want, text)
	}
	if err := newBrowser().follow(urlRE.FindString(text)); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if opened {
		t.Error("--no-browser opened a browser")
	}
}

func TestTheVerifierIs64CharactersAndTheChallengeIsS256(t *testing.T) {
	v, err := randomString(verifierBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 64 || !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(v) {
		t.Errorf("verifier %q: %d characters", v, len(v))
	}
	// RFC 7636 Appendix B.
	if got := challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Errorf("challenge = %q", got)
	}
}

// authorize runs the authorization step by hand and returns the code.
func authorize(t *testing.T, app *Application, redirectURI, codeChallenge, scope string) string {
	t.Helper()
	raw := app.authorizeURL(redirectURI, "s", codeChallenge, []string{scope})
	stop := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := stop.Get(raw) //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Query().Get("code") == "" {
		t.Fatalf("authorize answered %d, Location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	return loc.Query().Get("code")
}

func TestExchangeChecksTheVerifier(t *testing.T) {
	srv := gitlabtest.New(t, gitlabtest.Options{})
	app := newApp(t, srv, nil)
	const redirect = "http://127.0.0.1:41234/callback"
	verifier, _ := randomString(verifierBytes)
	other, _ := randomString(verifierBytes)

	code := authorize(t, app, redirect, challenge(verifier), "api")
	if _, err := app.Exchange(context.Background(), code, redirect, other); !IsInvalidGrant(err) {
		t.Errorf("a wrong verifier: err = %v, want invalid_grant", err)
	}
	code = authorize(t, app, redirect, challenge(verifier), "api")
	if _, err := app.Exchange(context.Background(), code, redirect, verifier); err != nil {
		t.Errorf("the right verifier: %v", err)
	}
}

func TestTokenInfoAndRevoke(t *testing.T) {
	srv := gitlabtest.New(t, gitlabtest.Options{})
	app := newApp(t, srv, nil)
	g := signIn(t, app, "read_api")
	ctx := context.Background()

	info, err := app.TokenInfo(ctx, g.Token.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(info.Scopes, " ") != "read_api" || info.ApplicationID != gitlabtest.ClientID || info.ExpiresIn <= 0 {
		t.Errorf("token info %+v", info)
	}

	if err := app.Revoke(ctx, g.Token.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := app.TokenInfo(ctx, g.Token.AccessToken); err == nil {
		t.Error("the access token still works after its refresh token was revoked")
	}
	if _, err := app.Refresh(ctx, g.Token.RefreshToken); !IsInvalidGrant(err) {
		t.Errorf("refresh after revoke: err = %v, want invalid_grant", err)
	}
}

func TestTransportErrorsCarryNoURL(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	inst, err := instance.Parse("http://"+addr+"/example-group", false)
	if err != nil {
		t.Fatal(err)
	}
	app := &Application{Instance: inst, ClientID: gitlabtest.ClientID, Timeout: 2 * time.Second}
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"token info": func() error { _, err := app.TokenInfo(ctx, "test-access-secret"); return err },
		"refresh":    func() error { _, err := app.Refresh(ctx, "test-refresh-secret"); return err },
		"revoke":     func() error { return app.Revoke(ctx, "test-refresh-secret") },
	} {
		err := call()
		var te *TransportError
		if !errors.As(err, &te) {
			t.Errorf("%s: err = %v, want a TransportError", name, err)
			continue
		}
		for _, leak := range []string{"http://", addr, "example-group", "secret", "oauth"} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("%s: %q carries %q", name, err, leak)
			}
		}
	}
}

func TestAnApplicationNeedsAnIDAndAnInstance(t *testing.T) {
	inst, _ := instance.Parse("https://gitlab.example.com", false)
	for name, app := range map[string]*Application{
		"no id":       {Instance: inst},
		"no instance": {ClientID: gitlabtest.ClientID},
	} {
		if _, err := app.Refresh(context.Background(), "r"); err == nil {
			t.Errorf("%s: refresh went ahead", name)
		}
	}
}

// failWith is a transport that fails every request with err, as a
// dialer or a TLS handshake would.
type failWith struct{ err error }

func (f failWith) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

// TestTransportErrorsCarryNoHostName: a failed lookup names the host it
// looked up, and a certificate for the wrong host names both hosts.
// Neither name may reach the error text, which reaches logs.
func TestTransportErrorsCarryNoHostName(t *testing.T) {
	inst, err := instance.Parse("https://canary-host.example.net", false)
	if err != nil {
		t.Fatal(err)
	}
	for name, cause := range map[string]error{
		"lookup": &net.OpError{Op: "dial", Net: "tcp",
			Err: &net.DNSError{Name: "canary-host.example.net", Server: "10.9.8.7:53", Err: "no such host", IsNotFound: true}},
		"certificate host": &tls.CertificateVerificationError{Err: x509.HostnameError{
			Certificate: &x509.Certificate{DNSNames: []string{"canary-cert.example.net"}}, Host: "canary-host.example.net"}},
		"lookup in a url error": &url.Error{Op: "Post", URL: "https://canary-host.example.net/oauth/token",
			Err: &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Name: "canary-host.example.net",
				Server: "10.9.8.7:53", Err: "no such host", IsNotFound: true}}},
		"certificate in a url error": &url.Error{Op: "Post", URL: "https://canary-host.example.net/oauth/token",
			Err: &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{
				Cert: &x509.Certificate{Subject: pkix.Name{CommonName: "canary-ca.example.net"}}}}},
		"an unknown shape": errors.New("proxyconnect tcp: canary-proxy.example.net 10.9.8.7:3128 refused"),
	} {
		app := &Application{Instance: inst, ClientID: gitlabtest.ClientID,
			HTTPClient: &http.Client{Transport: failWith{cause}}}
		_, err := app.Refresh(context.Background(), "test-refresh-secret")
		var te *TransportError
		if !errors.As(err, &te) {
			t.Errorf("%s: err = %v, want a TransportError", name, err)
			continue
		}
		for _, leak := range []string{"canary", "10.9.8.7"} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("%s: %q carries %q", name, err, leak)
			}
		}
	}
}
