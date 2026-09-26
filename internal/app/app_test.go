package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"

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
// keyring.
func setup(t *testing.T, instanceURL string) (env, memKeyring) {
	t.Helper()
	e := env{
		config.EnvConfigDir:                 t.TempDir(),
		config.EnvConfigDirAllowOutsideHome: "true",
	}
	if instanceURL != "" {
		e[config.EnvInstance] = instanceURL
	}
	return e, memKeyring{}
}

func load(t *testing.T, e env) config.Config {
	t.Helper()
	cfg, err := config.Load(nil, e.get)
	if err != nil {
		t.Fatal(err)
	}
	if e[config.EnvInstance] == "" {
		// As the command does: an instance nobody named is left to the
		// profile.
		cfg.Instance = ""
	}
	return cfg
}

// signIn stores a profile and a token pair for it as login would.
func signIn(t *testing.T, cfg config.Config, k memKeyring, instanceURL, access string, granted ...string) {
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
}

func TestResolveUsesTheProfilesInstanceUnlessOneIsGiven(t *testing.T) {
	e, k := setup(t, "")
	cfg := load(t, e)
	signIn(t, cfg, k, "https://gitlab.example.com/gitlab", "test-access")

	s, err := Resolve(cfg, Options{Env: e.get, Keyring: k})
	if err != nil {
		t.Fatal(err)
	}
	if s.Instance.String() != "https://gitlab.example.com/gitlab" || s.CredentialsErr != nil {
		t.Errorf("instance %s, credentials %v", s.Instance, s.CredentialsErr)
	}
	if s.ClientID != gitlabtest.ClientID {
		t.Errorf("client id %q", s.ClientID)
	}

	// Another instance named: the token stays away from it.
	e[config.EnvInstance] = "https://other.example.com"
	s, err = Resolve(load(t, e), Options{Env: e.get, Keyring: k})
	if err != nil {
		t.Fatal(err)
	}
	if s.Instance.String() != "https://other.example.com" || s.CredentialsErr == nil {
		t.Fatalf("instance %s, credentials %v; want the named instance and no credentials", s.Instance, s.CredentialsErr)
	}
	if _, err := s.Tokens().Token(context.Background()); err == nil || !strings.HasPrefix(err.Error(), "[auth]") {
		t.Errorf("token for another instance: %v", err)
	}
}

func TestResolveWithNoProfileStartsSignedOut(t *testing.T) {
	e, k := setup(t, "")
	s, err := Resolve(load(t, e), Options{Env: e.get, Keyring: k})
	if err != nil {
		t.Fatal(err)
	}
	if s.Instance.String() != config.DefaultInstance {
		t.Errorf("instance %s, want gitlab.com", s.Instance)
	}
	if s.CredentialsErr == nil || !strings.Contains(s.CredentialsErr.Error(), "gitlab-mcp login") {
		t.Errorf("credentials %v", s.CredentialsErr)
	}
}

func TestTheClientIDOverrideWins(t *testing.T) {
	e, k := setup(t, "")
	cfg := load(t, e)
	signIn(t, cfg, k, "https://gitlab.example.com", "test-access")
	e[config.EnvClientID] = "another-application"
	s, err := Resolve(load(t, e), Options{Env: e.get, Keyring: k})
	if err != nil {
		t.Fatal(err)
	}
	if s.ClientID != "another-application" || s.Application().ClientID != "another-application" {
		t.Errorf("client id %q", s.ClientID)
	}
}

func TestABadInstanceOrCAFileIsAStartupError(t *testing.T) {
	e, k := setup(t, "http://gitlab.example.com")
	if _, err := Resolve(load(t, e), Options{Env: e.get, Keyring: k}); err == nil || !strings.Contains(err.Error(), config.EnvInstance) {
		t.Errorf("plain http to a remote host: %v", err)
	}
	e, _ = setup(t, "https://gitlab.example.com")
	junk := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(junk, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	e[config.EnvCAFile] = junk
	if _, err := Resolve(load(t, e), Options{Env: e.get, Keyring: k}); err == nil || !strings.Contains(err.Error(), "no PEM certificate") {
		t.Errorf("junk CA file: %v", err)
	}
}

func TestTheCAFileIsTrusted(t *testing.T) {
	tlsSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	// The refused handshake is the point of the first half; its log line
	// is noise.
	tlsSrv.Config.ErrorLog = log.New(io.Discard, "", 0)
	tlsSrv.StartTLS()
	t.Cleanup(tlsSrv.Close)
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	block := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsSrv.Certificate().Raw})
	if err := os.WriteFile(caPath, block, 0o600); err != nil {
		t.Fatal(err)
	}
	e, _ := setup(t, tlsSrv.URL)
	plain, err := NewHTTPClient(load(t, e))
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := plain.Get(tlsSrv.URL); err == nil { //nolint:noctx // test
		_ = resp.Body.Close()
		t.Error("an unknown CA was trusted without the CA file")
	}
	e[config.EnvCAFile] = caPath
	trusting, err := NewHTTPClient(load(t, e))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := trusting.Get(tlsSrv.URL) //nolint:noctx // test
	if err != nil {
		t.Fatalf("with the CA file: %v", err)
	}
	_ = resp.Body.Close()
}

func TestSettingsNeverPrintTheHostTheApplicationOrTheToken(t *testing.T) {
	e, k := setup(t, "")
	cfg := load(t, e)
	signIn(t, cfg, k, "https://gitlab.example.com", "test-access-canary")
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
		for _, leak := range []string{"gitlab.example.com", gitlabtest.ClientID, "test-access-canary", "test-refresh-unused", "alice"} {
			if strings.Contains(text, leak) {
				t.Errorf("%s carries %q: %s", name, leak, text)
			}
		}
		if !strings.Contains(text, "self-managed") || !strings.Contains(text, "default") {
			t.Errorf("%s lost the safe fields: %s", name, text)
		}
	}
}

func TestProbeReadsTheInstanceAndTheAccount(t *testing.T) {
	srv := gitlabtest.New(t, gitlabtest.Options{Version: "19.2.1-ee", Enterprise: true})
	e, k := setup(t, srv.URL)
	cfg := load(t, e)
	signIn(t, cfg, k, srv.URL, srv.TokenFor("bob", "api"), "read_api")
	s, err := Resolve(cfg, Options{Env: e.get, Keyring: k})
	if err != nil {
		t.Fatal(err)
	}
	st := s.Probe(context.Background(), slog.New(slog.DiscardHandler), "test")
	if st.Metadata.Version.String() != "19.2.1-ee" || !st.Metadata.Enterprise {
		t.Errorf("metadata %+v", st.Metadata)
	}
	if st.Username != "bob" {
		t.Errorf("username %q", st.Username)
	}
	// Read live, not from the stored login.
	if strings.Join(st.Granted, " ") != "api" {
		t.Errorf("granted %v, want [api]", st.Granted)
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
	newServer = func(o server.Options) (*mcp.Server, error) {
		if o.Client == nil || o.Logger == nil {
			return nil, errors.New("assembled without a client or a logger")
		}
		return mcp.NewServer(&mcp.Implementation{Name: "gitlab-mcp", Version: o.Version}, nil), nil
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
	if rt.Startup.Metadata != (instance.Metadata{}) {
		t.Errorf("metadata %+v from an unreachable instance", rt.Startup.Metadata)
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
	e, _ := setup(t, "")
	for raw, want := range map[string]string{
		"https://gitlab.com":                 "gitlab.com",
		"gitlab.com":                         "gitlab.com",
		"https://gitlab.example.com":         "self-managed",
		"https://gitlab.com.example.invalid": "self-managed",
	} {
		e[config.EnvInstance] = raw
		s, err := Resolve(load(t, e), Options{Env: e.get})
		if err != nil {
			t.Fatal(err)
		}
		if got := InstanceKind(s.Instance); got != want {
			t.Errorf("%s: %q, want %q", raw, got, want)
		}
	}
}
