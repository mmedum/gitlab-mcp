package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"

	"github.com/mmedum/gitlab-mcp/internal/auth"
	"github.com/mmedum/gitlab-mcp/internal/config"
	"github.com/mmedum/gitlab-mcp/internal/credentials"
	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/gapi/gitlabtest"
	"github.com/mmedum/gitlab-mcp/internal/instance"
	"github.com/mmedum/gitlab-mcp/internal/server"
	"github.com/mmedum/gitlab-mcp/internal/userconfig"
)

// memKeyring is an in-memory keyring. The real one is never reached
// from this package's tests: Options.Keyring is always this or nil.
type memKeyring map[string]string

func (k memKeyring) Get(service, account string) (string, error) {
	v, ok := k[service+"/"+account]
	if !ok {
		return "", errKeyringNotFound
	}
	return v, nil
}
func (k memKeyring) Set(service, account, secret string) error {
	k[service+"/"+account] = secret
	return nil
}
func (k memKeyring) Delete(service, account string) error { delete(k, service+"/"+account); return nil }

var errKeyringNotFound = keyring.ErrNotFound

type env map[string]string

func (e env) get(k string) string { return e[k] }

// setup is a config directory, an environment pointing at it, and a
// keyring. A non-empty instanceURL is the test instance, which must be
// loopback.
func setup(t *testing.T, instanceURL string) (env, memKeyring) {
	t.Helper()
	e := env{
		config.EnvConfigDir:                 t.TempDir(),
		config.EnvConfigDirAllowOutsideHome: "true",
	}
	if instanceURL != "" {
		e[config.EnvTestInstance] = instanceURL
	}
	return e, memKeyring{}
}

func load(t *testing.T, e env) config.Config {
	t.Helper()
	cfg, err := config.Load(nil, e.get)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// signIn stores a profile and a token pair for it as login would.
func signIn(t *testing.T, cfg config.Config, k memKeyring, instanceURL, access string, granted ...string) *credentials.Store {
	t.Helper()
	uc := userconfig.Config{Instance: instanceURL, ClientID: gitlabtest.ClientID, Username: "alice",
		TokenStore: string(credentials.SourceKeyring), Scopes: granted}
	if err := cfg.ConfigDir.Save(userconfig.DefaultProfile, uc); err != nil {
		t.Fatal(err)
	}
	st := &credentials.Store{Profile: userconfig.DefaultProfile, Keyring: k, Env: func(string) string { return "" }}
	if _, err := st.Save(&oauth2.Token{AccessToken: access, RefreshToken: "test-refresh-unused",
		Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return st
}

// TestATokenStaysWithTheInstanceThatIssuedIt holds the one credential
// rule the test override could break: a profile signed in to the test
// instance never sends its token to gitlab.com, and the reverse.
func TestATokenStaysWithTheInstanceThatIssuedIt(t *testing.T) {
	const fake = "http://127.0.0.1:9"
	for _, tc := range []struct {
		name, signedIn, override string
		want                     string // the instance; "" in signedOut
		signedOut                bool
	}{
		{"gitlab.com profile on gitlab.com", "https://gitlab.com", "", "https://gitlab.com", false},
		{"test profile on the test instance", fake, fake, fake, false},
		{"test profile, no override", fake, "", "https://gitlab.com", true},
		{"gitlab.com profile, override set", "https://gitlab.com", fake, fake, true},
		{"another test port", "http://127.0.0.1:10", fake, fake, true},
		{"a host an older build accepted", "https://gitlab.example.com", "", "https://gitlab.com", true},
		{"an unreadable record", "http://gitlab.example.com", "", "https://gitlab.com", true},
		// The keyring is keyed by profile alone, so a stored token under a
		// profile that records no instance may be gitlab.com's.
		{"no record on gitlab.com", "", "", "https://gitlab.com", false},
		{"no record, override set", "", fake, fake, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, k := setup(t, tc.override)
			cfg := load(t, e)
			signIn(t, cfg, k, tc.signedIn, "test-access")
			s, err := Resolve(cfg, Options{Env: e.get, Keyring: k})
			if err != nil {
				t.Fatal(err)
			}
			if s.Instance.String() != tc.want {
				t.Errorf("instance %s, want %s", s.Instance, tc.want)
			}
			if (s.CredentialsErr != nil) != tc.signedOut {
				t.Fatalf("credentials %v, want signed out %v", s.CredentialsErr, tc.signedOut)
			}
			if !tc.signedOut {
				if s.ClientID != gitlabtest.ClientID {
					t.Errorf("client id %q", s.ClientID)
				}
				return
			}
			if !strings.Contains(s.CredentialsErr.Error(), "gitlab-mcp login") {
				t.Errorf("credentials %v do not say what to do", s.CredentialsErr)
			}
			if _, err := s.Tokens().Token(context.Background()); err == nil || !strings.HasPrefix(err.Error(), "[auth]") {
				t.Errorf("token for another instance: %v", err)
			}
		})
	}
}

// A token given in the environment was given for this run, so a profile
// that records no instance still sends it to the test instance.
func TestAnEnvironmentTokenReachesTheTestInstance(t *testing.T) {
	e, k := setup(t, "http://127.0.0.1:9")
	e[credentials.EnvVar] = "test-refresh-from-env"
	e[config.EnvClientID] = gitlabtest.ClientID
	s, err := Resolve(load(t, e), Options{Env: e.get, Keyring: k})
	if err != nil {
		t.Fatal(err)
	}
	if s.CredentialsErr != nil {
		t.Errorf("an environment token was withheld: %v", s.CredentialsErr)
	}
}

// TestTheTestInstanceIsLoggedAtWarn: the override is never silent.
func TestTheTestInstanceIsLoggedAtWarn(t *testing.T) {
	for _, override := range []string{"", "http://127.0.0.1:9"} {
		e, k := setup(t, override)
		var logged bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logged, nil))
		if _, err := Resolve(load(t, e), Options{Env: e.get, Keyring: k, Logger: logger}); err != nil {
			t.Fatal(err)
		}
		got := strings.Count(logged.String(), "level=WARN msg=\""+config.EnvTestInstance)
		if want := map[bool]int{true: 1, false: 0}[override != ""]; got != want {
			t.Errorf("override %q: %d warnings, want %d:\n%s", override, got, want, logged.String())
		}
	}
}

func TestResolveWithNoProfileStartsSignedOut(t *testing.T) {
	e, k := setup(t, "")
	s, err := Resolve(load(t, e), Options{Env: e.get, Keyring: k})
	if err != nil {
		t.Fatal(err)
	}
	if s.Instance.String() != "https://gitlab.com" {
		t.Errorf("instance %s, want gitlab.com", s.Instance)
	}
	if s.CredentialsErr == nil || !strings.Contains(s.CredentialsErr.Error(), "gitlab-mcp login") {
		t.Errorf("credentials %v", s.CredentialsErr)
	}
}

func TestTheClientIDOverrideWins(t *testing.T) {
	e, k := setup(t, "")
	cfg := load(t, e)
	signIn(t, cfg, k, "https://gitlab.com", "test-access")
	e[config.EnvClientID] = "another-application"
	s, err := Resolve(load(t, e), Options{Env: e.get, Keyring: k})
	if err != nil {
		t.Fatal(err)
	}
	if s.ClientID != "another-application" || s.Application().ClientID != "another-application" {
		t.Errorf("client id %q", s.ClientID)
	}
}

func TestSettingsNeverPrintTheHostTheApplicationOrTheToken(t *testing.T) {
	e, k := setup(t, "http://127.0.0.1:9")
	cfg := load(t, e)
	signIn(t, cfg, k, "http://127.0.0.1:9", "test-access-canary")
	s, err := Resolve(cfg, Options{Env: e.get, Keyring: k})
	if err != nil {
		t.Fatal(err)
	}
	// Load the token into the source, so a leak through it would show.
	if _, err := s.Tokens().Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("serving", "settings", s)
	for name, text := range map[string]string{
		"%v": fmt.Sprintf("%v", s), "%+v": fmt.Sprintf("%+v", s), "value %+v": fmt.Sprintf("%+v", *s),
		"String": s.String(), "log": logged.String(),
	} {
		for _, leak := range []string{"127.0.0.1", gitlabtest.ClientID, "test-access-canary", "test-refresh-unused", "alice"} {
			if strings.Contains(text, leak) {
				t.Errorf("%s carries %q: %s", name, leak, text)
			}
		}
		if !strings.Contains(text, "test instance") || !strings.Contains(text, "default") {
			t.Errorf("%s lost the safe fields: %s", name, text)
		}
	}
}

func TestProbeReadsTheScopes(t *testing.T) {
	srv := gitlabtest.New(t, gitlabtest.Options{})
	e, k := setup(t, srv.URL)
	cfg := load(t, e)
	signIn(t, cfg, k, srv.URL, srv.TokenFor("bob", "api"), "read_api")
	s, err := Resolve(cfg, Options{Env: e.get, Keyring: k})
	if err != nil {
		t.Fatal(err)
	}
	st := s.Probe(context.Background(), slog.New(slog.DiscardHandler), "test")
	// Nothing reads the account or the version at startup: registration
	// needs neither, so neither is asked for.
	for _, r := range srv.Requests() {
		if r.EscapedPath == "/api/v4/user" || r.EscapedPath == "/api/v4/metadata" {
			t.Errorf("the probe read %s", r.EscapedPath)
		}
	}
	// Read live, not from the stored login.
	if strings.Join(st.Granted, " ") != "api" {
		t.Errorf("granted %v, want [api]", st.Granted)
	}
}

// TestProbeNeverRefreshes holds #7: a host kills servers before the
// handshake, and one killed mid-refresh leaves the profile signed out.
// With the access token expired, the probe asks GitLab for nothing and
// keeps the scopes of the last login.
func TestProbeNeverRefreshes(t *testing.T) {
	srv := gitlabtest.New(t, gitlabtest.Options{})
	e, k := setup(t, srv.URL)
	cfg := load(t, e)
	st := signIn(t, cfg, k, srv.URL, srv.TokenFor("bob", "api"), "read_api")
	if _, err := st.Save(&oauth2.Token{AccessToken: "test-access-expired", RefreshToken: "test-refresh-unused",
		Expiry: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	s, err := Resolve(cfg, Options{Env: e.get, Keyring: k})
	if err != nil {
		t.Fatal(err)
	}
	got := s.Probe(context.Background(), slog.New(slog.DiscardHandler), "test")
	if reqs := srv.Requests(); len(reqs) != 0 {
		t.Errorf("the probe sent %d requests, the first to %s", len(reqs), reqs[0].EscapedPath)
	}
	if strings.Join(got.Granted, " ") != "read_api" {
		t.Errorf("granted %v, want the last login's [read_api]", got.Granted)
	}
	if tok, _, err := st.Resolve(); err != nil || tok.AccessToken != "test-access-expired" {
		t.Errorf("stored pair changed: %v, %v", tok, err)
	}
}

// With no scopes recorded by a login, only a live read can keep a
// read_api token from the write tools, so that one start refreshes.
func TestProbeRefreshesWhenNoLoginRecordedTheScopes(t *testing.T) {
	srv := gitlabtest.New(t, gitlabtest.Options{})
	e, k := setup(t, srv.URL)
	cfg := load(t, e)
	st := signIn(t, cfg, k, srv.URL, srv.TokenFor("bob", "api"))
	if _, err := st.Save(&oauth2.Token{AccessToken: "test-access-expired", RefreshToken: "test-refresh-unused",
		Expiry: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	s, err := Resolve(cfg, Options{Env: e.get, Keyring: k})
	if err != nil {
		t.Fatal(err)
	}
	s.Probe(context.Background(), slog.New(slog.DiscardHandler), "test")
	sent := false
	for _, r := range srv.Requests() {
		sent = sent || r.EscapedPath == "/oauth/token"
	}
	if !sent {
		t.Error("the probe did not try to refresh")
	}
}

// The scopes a login recorded do not describe a token from the
// environment, so they are not used to register tools for it.
func TestProbeIgnoresTheLoginsScopesForAnEnvironmentToken(t *testing.T) {
	srv := gitlabtest.New(t, gitlabtest.Options{})
	e, k := setup(t, srv.URL)
	cfg := load(t, e)
	signIn(t, cfg, k, srv.URL, srv.TokenFor("bob", "api"), "api")
	e[credentials.EnvVar] = "test-refresh-from-env"
	s, err := Resolve(cfg, Options{Env: e.get, Keyring: k})
	if err != nil {
		t.Fatal(err)
	}
	got := s.Probe(context.Background(), slog.New(slog.DiscardHandler), "test")
	if len(got.Granted) != 0 {
		t.Errorf("granted %v, want none: the login's scopes are not the environment token's", got.Granted)
	}
	sent := false
	for _, r := range srv.Requests() {
		sent = sent || r.EscapedPath == "/oauth/token"
	}
	if !sent {
		t.Error("the probe did not try to read the environment token's scopes")
	}
}

// A token GitLab refuses at startup is dropped from the source, so the
// first call refreshes rather than sending it again.
func TestProbePassesARefusalToTheSource(t *testing.T) {
	srv := gitlabtest.New(t, gitlabtest.Options{})
	e, k := setup(t, srv.URL)
	cfg := load(t, e)
	access := srv.TokenFor("bob", "api")
	signIn(t, cfg, k, srv.URL, access, "api")
	srv.Revoke(access)
	s, err := Resolve(cfg, Options{Env: e.get, Keyring: k})
	if err != nil {
		t.Fatal(err)
	}
	s.Probe(context.Background(), slog.New(slog.DiscardHandler), "test")
	if tok, err := s.tokens.(*auth.TokenSource).Cached(); err != nil || tok != "" {
		t.Errorf("after a refusal the source holds %q, %v; want none", tok, err)
	}
}

func TestAReadAPITokenCannotServeWrites(t *testing.T) {
	srv := gitlabtest.New(t, gitlabtest.Options{})
	e, k := setup(t, srv.URL)
	cfg := load(t, e)
	signIn(t, cfg, k, srv.URL, srv.TokenFor("alice", "read_api"), "read_api")
	restore := stubServer(t)
	defer restore()

	_, err := Assemble(context.Background(), cfg, Options{Env: e.get, Keyring: k})
	if err == nil {
		t.Fatal("a read_api token started a server with writes on")
	}
	if c, _ := gapi.ClassOf(err); c != gapi.ClassAuth {
		t.Errorf("class %q", c)
	}
	for _, want := range []string{"api", "gitlab-mcp login", config.EnvReadOnly} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err %q does not name %q", err, want)
		}
	}

	e[config.EnvReadOnly] = "true"
	rt, err := Assemble(context.Background(), load(t, e), Options{Env: e.get, Keyring: k})
	if err != nil {
		t.Fatalf("read-only with a read_api token: %v", err)
	}
	if strings.Join(rt.Startup.Granted, " ") != "read_api" {
		t.Errorf("granted %v", rt.Startup.Granted)
	}
}

// stubServer replaces server.New with one that records its options and
// returns a bare MCP server, so the assembly is tested up to the call.
func stubServer(t *testing.T) func() {
	t.Helper()
	prev := newServer
	newServer = func(o server.Options) *mcp.Server {
		if o.Client == nil || o.Logger == nil {
			t.Error("assembled without a client or a logger")
		}
		return mcp.NewServer(&mcp.Implementation{Name: "gitlab-mcp", Version: o.Version}, nil)
	}
	return func() { newServer = prev }
}

func TestAnUnreachableInstanceStillGetsAServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + ln.Addr().String()
	_ = ln.Close()
	e, k := setup(t, url)
	cfg := load(t, e)
	signIn(t, cfg, k, url, "test-access", "api")
	defer stubServer(t)()

	rt, err := Assemble(context.Background(), cfg, Options{Env: e.get, Keyring: k})
	if err != nil {
		t.Fatalf("an unreachable instance refused to start: %v", err)
	}
	// The stored grant stands in for the live one.
	if strings.Join(rt.Startup.Granted, " ") != "api" {
		t.Errorf("granted %v", rt.Startup.Granted)
	}
}

func TestServeEndsCleanlyWhenTheClientGoes(t *testing.T) {
	e, k := setup(t, "")
	defer stubServer(t)()
	rt, err := Assemble(context.Background(), load(t, e), Options{Env: e.get, Keyring: k, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",` +
		`"capabilities":{},"clientInfo":{"name":"t","version":"1"}}}` + "\n"
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- rt.Serve(context.Background(), inR, outW) }()
	if _, err := io.WriteString(inW, init); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(outR).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, `"id":1`) || !strings.Contains(line, `"serverInfo"`) {
		t.Errorf("stdout %q, want the initialize answer", line)
	}
	// The client goes away: stdin closes, and that is not an error.
	_ = inW.Close()
	go func() { _, _ = io.Copy(io.Discard, outR) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve after the client left: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not end when stdin closed")
	}
}

func TestServeWithTheRealServer(t *testing.T) {
	e, k := setup(t, "")
	rt, err := Assemble(context.Background(), load(t, e), Options{Env: e.get, Keyring: k, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Serve(context.Background(), strings.NewReader(""), io.Discard); err != nil {
		t.Errorf("serve on a closed stdin: %v", err)
	}
}

func TestIsDisconnectMatchesTheCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"server closing", &jsonrpc.Error{Code: -32004, Message: "server is closing: EOF"}, true},
		{"client closing", &jsonrpc.Error{Code: -32003, Message: "client is closing"}, true},
		{"wrapped", fmt.Errorf("session: %w", &jsonrpc.Error{Code: -32004}), true},
		{"EOF", io.EOF, true},
		{"canceled", context.Canceled, true},
		{"internal error", &jsonrpc.Error{Code: -32603, Message: "EOF"}, false},
		{"other", errors.New("server is closing"), false},
	} {
		if got := IsDisconnect(tc.err); got != tc.want {
			t.Errorf("%s: %t, want %t", tc.name, got, tc.want)
		}
	}
}

func TestInstanceKind(t *testing.T) {
	fake, err := instance.Parse("http://127.0.0.1:9")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		inst instance.Instance
		want string
	}{
		{instance.GitLabCom, "gitlab.com"},
		{fake, "test instance"},
		{instance.Instance{}, ""},
	} {
		if got := InstanceKind(tc.inst); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.inst, got, tc.want)
		}
	}
}
