// Package app is the startup assembly every entry point shares: open the
// profile, settle the OAuth application, build the HTTP transport, the
// token source and the GitLab client, refuse a mode the sign-in cannot
// serve, and wire the MCP server.
//
// It lives outside package main so the commands, the tests and any
// later driver assemble the server the way the binary does, rather than
// each re-deriving the sequence and dropping a step on the way. Nothing
// here prints or exits; the caller decides what a failure means.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/gitlab-mcp/internal/auth"
	"github.com/mmedum/gitlab-mcp/internal/config"
	"github.com/mmedum/gitlab-mcp/internal/credentials"
	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/instance"
	"github.com/mmedum/gitlab-mcp/internal/redact"
	"github.com/mmedum/gitlab-mcp/internal/scopes"
	"github.com/mmedum/gitlab-mcp/internal/server"
	"github.com/mmedum/gitlab-mcp/internal/userconfig"
)

// Options are the process-level inputs to Resolve and Assemble.
type Options struct {
	// Env reads the environment; the credential store takes its
	// refresh-token override from it.
	Env func(string) string
	// Keyring is the OS keyring, or a fake in tests. nil means no
	// keyring: the token lives in the profile's file.
	Keyring credentials.Backend
	// Logger receives startup findings. Never the payload.
	Logger *slog.Logger
	// Version is this build's version.
	Version string
	// Warn receives credential warnings, such as every use of the
	// plaintext file. nil logs them.
	Warn func(string)
}

func (o Options) logger() *slog.Logger {
	if o.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return o.Logger
}

// Profile is one configured profile: its name, what the last login
// stored, and its credential store.
type Profile struct {
	Name string
	Dir  userconfig.Dir
	// User is the stored profile state; HasUser says a file existed.
	User    userconfig.Config
	HasUser bool
	Store   *credentials.Store
}

// OpenProfile resolves the profile name (the one named, else "default"), reads its stored state and builds its
// credential store. warn receives the plaintext-file warning on every
// use of it.
func OpenProfile(cfg config.Config, keyring credentials.Backend, env func(string) string, warn func(string)) (*Profile, error) {
	dir := cfg.ConfigDir
	name, err := dir.Resolve(cfg.Profile)
	if err != nil {
		return nil, err
	}
	p := &Profile{Name: name, Dir: dir}
	p.User, err = dir.Load(name)
	switch {
	case err == nil:
		p.HasUser = true
	case !errors.Is(err, userconfig.ErrNotFound):
		return nil, err
	}
	p.Store = &credentials.Store{
		Profile: name, Keyring: keyring, FilePath: dir.TokenFilePath(name),
		Env: env, Warn: warn,
		ExpectKeyring: p.User.TokenStore == string(credentials.SourceKeyring),
	}
	return p, nil
}

// Settings is the configuration settled against the profile: which
// application, which transport. The token source is
// unexported, and String and LogValue both render the same safe fields,
// because %+v reads unexported fields and a log line reads String.
type Settings struct {
	Config  config.Config
	Profile *Profile
	// Instance is gitlab.com, or the loopback stand-in of
	// GITLAB_MCP_TEST_INSTANCE.
	Instance instance.Instance
	// ClientID is the OAuth application: the override, else the
	// profile's.
	ClientID string
	// HTTPClient carries the proxy.
	HTTPClient *http.Client
	// CredentialsErr is why the stored sign-in will not be used, or nil.
	// The server still starts without one, so tools/list works before
	// anyone signs in and every call says to log in.
	CredentialsErr error

	app    *auth.Application
	tokens gapi.TokenSource
}

// Resolve settles the configuration against the profile. It reads the
// profile and touches no network.
//
// A profile signed in to another instance than this one keeps its token
// to itself, since a token is only ever sent to the instance that issued
// it: one signed in to the test instance never reaches gitlab.com, and
// the reverse.
func Resolve(cfg config.Config, o Options) (*Settings, error) {
	logger := o.logger()
	warn := o.Warn
	if warn == nil {
		warn = func(msg string) { logger.Warn(redact.Text(msg)) }
	}
	p, err := OpenProfile(cfg, o.Keyring, o.Env, warn)
	if err != nil {
		return nil, err
	}
	s := &Settings{Config: cfg, Profile: p, Instance: cfg.Target()}
	if cfg.TestInstance {
		logger.Warn(config.EnvTestInstance + " is set: this server talks to a loopback test instance, not gitlab.com")
	}
	if !SameInstance(p.User.Instance, s.Instance) {
		s.CredentialsErr = fmt.Errorf("profile %q is signed in to another instance than %s; "+
			"run `gitlab-mcp login` to sign it in to this one", p.Name, InstanceKind(s.Instance))
	}

	s.ClientID = cfg.ClientID
	if s.ClientID == "" {
		s.ClientID = p.User.ClientID
	}
	s.HTTPClient = NewHTTPClient()
	s.app = &auth.Application{Instance: s.Instance, ClientID: s.ClientID, HTTPClient: s.HTTPClient, Timeout: cfg.HTTPTimeout}

	if s.CredentialsErr == nil && s.ClientID == "" && o.env(credentials.EnvVar) == "" && !p.HasUser {
		s.CredentialsErr = errors.New("not signed in: run `gitlab-mcp login --client-id <application id>`")
	}
	if s.CredentialsErr == nil && s.ClientID == "" {
		s.CredentialsErr = fmt.Errorf("no OAuth application id: set %s or run `gitlab-mcp login --client-id <application id>`",
			config.EnvClientID)
	}
	s.tokens = s.tokenSource(warn)
	return s, nil
}

// tokenSource is the refreshing source over the profile's store, or,
// without usable credentials, one that says why on every call.
func (s *Settings) tokenSource(warn func(string)) gapi.TokenSource {
	if s.CredentialsErr != nil {
		return auth.NoCredentials{Reason: s.CredentialsErr}
	}
	return auth.NewTokenSource(s.app, s.Profile.Store, auth.TokenSourceOptions{
		LockPath: s.Profile.Dir.LockPath(s.Profile.Name), Warn: warn,
	})
}

func (o Options) env(k string) string {
	if o.Env == nil {
		return ""
	}
	return strings.TrimSpace(o.Env(k))
}

// Application is the OAuth application these settings sign in with.
func (s *Settings) Application() *auth.Application { return s.app }

// Tokens is the token source: the refreshing one, or one that answers
// every call with the reason there are no credentials.
func (s *Settings) Tokens() gapi.TokenSource { return s.tokens }

// Client builds the GitLab client for these settings.
func (s *Settings) Client(logger *slog.Logger, version string) (*gapi.Client, error) {
	return s.ClientWith(gapi.Options{Logger: logger, Version: version})
}

// ClientWith builds a GitLab client for these settings with o's tuning.
// The instance and the transport are always these settings'; the token
// source and the header timeout are theirs unless o sets them.
func (s *Settings) ClientWith(o gapi.Options) (*gapi.Client, error) {
	o.Instance, o.HTTPClient = s.Instance, s.HTTPClient
	if o.Tokens == nil {
		o.Tokens = s.tokens
	}
	if o.HeaderTimeout == 0 {
		o.HeaderTimeout = s.Config.HTTPTimeout
	}
	return gapi.New(o)
}

// String keeps %v and %+v to the fields LogValue shows.
func (s Settings) String() string { return s.LogValue().String() }

// LogValue renders the settings for slog: the profile, the kind of
// instance and the flags. Never the host, the application id in full,
// the account or the token.
func (s Settings) LogValue() slog.Value {
	profile, store := "", ""
	if s.Profile != nil {
		profile, store = s.Profile.Name, s.Profile.User.TokenStore
	}
	return slog.GroupValue(
		slog.String("profile", profile),
		slog.String("instance", InstanceKind(s.Instance)),
		slog.String("client_id", maskedID(s.ClientID)),
		slog.Bool("signed_in", s.CredentialsErr == nil),
		slog.String("token_store", store),
		slog.Bool("read_only", s.Config.ReadOnly),
		slog.Bool("ship", s.Config.EnableShip),
		slog.Bool("destructive", s.Config.EnableDestructive),
		slog.String("toolsets", strings.Join(s.Config.Toolsets, ",")),
		slog.Int("write_namespaces", len(s.Config.WriteNamespaces)),
	)
}

func maskedID(id string) string {
	if id == "" {
		return ""
	}
	return redact.ID(id)
}

// InstanceKind names an instance without naming its host: "gitlab.com",
// or "test instance" for the loopback stand-in.
func InstanceKind(i instance.Instance) string {
	switch {
	case i.IsZero():
		return ""
	case i == instance.GitLabCom:
		return "gitlab.com"
	}
	return "test instance"
}

// SameInstance reports whether a profile's recorded instance is inst.
// A profile that records none has not signed in, and matches. One that
// records something unreadable, such as a URL an older build accepted,
// does not.
func SameInstance(recorded string, inst instance.Instance) bool {
	if recorded == "" {
		return true
	}
	r, err := instance.Parse(recorded)
	return err == nil && r == inst
}

// NewHTTPClient builds the transport every call to the instance uses:
// proxies from the standard environment variables and the system's
// certificate authorities. No redirect is followed.
func NewHTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = http.ProxyFromEnvironment
	// Every call goes to one host, as many at once as the rate model
	// allows; fewer idle connections would close and reopen them.
	t.MaxIdleConnsPerHost = gapi.DefaultConcurrency
	return &http.Client{
		Transport:     t,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Startup is what the server learned from the instance before serving.
type Startup struct {
	// Granted is the token's scopes, read live or else from the last
	// login.
	Granted []string
}

// startupBudget bounds the startup reads, so a slow instance delays the
// server's start by at most this.
const startupBudget = 15 * time.Second

// Probe reads the token's scopes. Registration needs them before the
// server exists, so they are read before serving; a failure is logged
// and left for the calls to report, so an instance that is down at start
// still gets a server.
//
// The token is warmed first, so a refresh that is due happens here
// rather than on the first tool call.
func (s *Settings) Probe(ctx context.Context, logger *slog.Logger, version string) Startup {
	var st Startup
	if s.Profile != nil {
		st.Granted = s.Profile.User.Scopes
	}
	if s.CredentialsErr != nil {
		return st
	}
	budget := min(s.Config.HTTPTimeout, startupBudget)
	if budget <= 0 {
		budget = startupBudget
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	c, err := s.ClientWith(gapi.Options{Logger: logger, Version: version, HeaderTimeout: budget, MaxAttempts: 1})
	if err != nil {
		logger.Warn("startup read skipped", "reason", redact.Text(err.Error()))
		return st
	}
	if _, err := s.tokens.Token(ctx); err != nil {
		logger.Warn("the sign-in could not be used; tools answer [auth] until `gitlab-mcp login` succeeds",
			"reason", redact.Text(err.Error()))
		return st
	}
	if info, err := c.TokenInfo(ctx); err != nil {
		logger.Warn("could not read the token's scopes; using those of the last login", "reason", redact.Text(err.Error()))
	} else if len(info.Scope) > 0 {
		st.Granted = info.Scope
	}
	return st
}

// CheckScopes refuses a configuration the sign-in cannot serve: a
// read_api token with the write tools on, above all (§9.4). An unknown
// grant passes, and the calls report what is missing.
func (s *Settings) CheckScopes(granted []string) error {
	if len(granted) == 0 {
		return nil
	}
	needed := s.Config.Scopes()
	missing := scopes.Missing(granted, needed)
	if len(missing) == 0 {
		return nil
	}
	name := ""
	if s.Profile != nil {
		name = s.Profile.Name
	}
	msg := fmt.Sprintf("profile %q was granted %s, and this configuration needs %s: run `gitlab-mcp login` to sign in again",
		name, strings.Join(granted, " "), strings.Join(missing, " "))
	if !s.Config.ReadOnly {
		msg += fmt.Sprintf(", or set %s=true to serve the read tools only", config.EnvReadOnly)
	}
	return gapi.Errf(gapi.ClassAuth, "%s", msg)
}

// newServer is server.New; a test replaces it to reach the serve path
// on its own.
var newServer = server.New

// Runtime is an assembled server.
type Runtime struct {
	Settings *Settings
	Client   *gapi.Client
	Startup  Startup
	Server   *mcp.Server
}

// Assemble builds the server. It fails on a configuration that cannot
// work (a sign-in whose scopes the mode exceeds), and not on a missing sign-in or an unreachable instance:
// those surface per call.
func Assemble(ctx context.Context, cfg config.Config, o Options) (*Runtime, error) {
	logger := o.logger()
	s, err := Resolve(cfg, o)
	if err != nil {
		return nil, err
	}
	if s.CredentialsErr != nil {
		logger.Warn("no usable sign-in; every tool answers [auth] until `gitlab-mcp login` succeeds",
			"reason", redact.Text(s.CredentialsErr.Error()))
	}
	client, err := s.Client(logger, o.Version)
	if err != nil {
		return nil, err
	}
	st := s.Probe(ctx, logger, o.Version)
	if err := s.CheckScopes(st.Granted); err != nil {
		return nil, err
	}
	srv := newServer(server.Options{
		Config: cfg, Client: client, Granted: st.Granted,
		Logger: logger, Version: o.Version,
	})
	return &Runtime{Settings: s, Client: client, Startup: st, Server: srv}, nil
}

// Serve runs one MCP session over the given streams. An ordinary
// disconnect is not an error.
func (r *Runtime) Serve(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	transport := &mcp.IOTransport{Reader: readCloser(stdin), Writer: writeCloser(stdout)}
	if err := r.Server.Run(ctx, transport); err != nil && !IsDisconnect(err) {
		return err
	}
	return nil
}

// readCloser and writeCloser adapt the streams to the transport, which
// wants closers. The host owns stdin and stdout, so where a Close has to
// be invented it does nothing.
func readCloser(r io.Reader) io.ReadCloser {
	if rc, ok := r.(io.ReadCloser); ok {
		return rc
	}
	return io.NopCloser(r)
}

func writeCloser(w io.Writer) io.WriteCloser {
	if wc, ok := w.(io.WriteCloser); ok {
		return wc
	}
	return nopWriteCloser{w}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// JSON-RPC codes the SDK uses for a closing connection.
const (
	codeServerClosing = -32004
	codeClientClosing = -32003
)

// IsDisconnect reports the ordinary end of a stdio session. The SDK
// reports a closed connection as JSON-RPC -32004 or -32003 with the EOF
// only as message text, so errors.Is(err, io.EOF) misses it and the
// process would exit non-zero, which hosts log as a crash. The code is
// matched, never the text.
func IsDisconnect(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
		return true
	}
	var je *jsonrpc.Error
	if errors.As(err, &je) {
		return je.Code == codeServerClosing || je.Code == codeClientClosing
	}
	return false
}
