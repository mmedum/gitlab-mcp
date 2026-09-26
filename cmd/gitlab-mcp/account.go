package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/app"
	"github.com/mmedum/gitlab-mcp/internal/auth"
	"github.com/mmedum/gitlab-mcp/internal/config"
	"github.com/mmedum/gitlab-mcp/internal/credentials"
	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/instance"
	"github.com/mmedum/gitlab-mcp/internal/scopes"
	"github.com/mmedum/gitlab-mcp/internal/version"
)

// keyringBackend is the credential store's keyring. A var so the tests
// replace it package-wide in TestMain: a test can isolate the config
// directory and the environment, and cannot isolate the OS keyring, so
// `go test` against the real one would revoke and delete the person's
// own token.
var keyringBackend = credentials.OSKeyring()

// openBrowser is how login reaches a browser. nil means the platform's
// handler; tests substitute one that follows the redirect.
var openBrowser func(string) error

// loginTimeout bounds the person's trip through the browser.
var loginTimeout = 10 * time.Minute

// noApplication is what login and doctor say when no application id is
// known.
const noApplication = "no OAuth application id: register an application in GitLab, then run " +
	"`gitlab-mcp login --client-id <Application ID>` (add --instance <url> for a self-managed instance)"

// writeSetup prints the exact form to fill in GitLab. It is written
// unmasked: it is generated from internal/scopes, names nothing of the
// person's, and a masked redirect URI would be copied wrong.
func writeSetup(w io.Writer, mode scopes.Mode) {
	_, _ = io.WriteString(w, "\nIn GitLab, open user settings > Applications and add an application with:\n\n"+
		scopes.SetupBlock(mode)+"\n")
}

// cmdLogin runs the loopback OAuth flow and stores the token pair.
func cmdLogin(args []string, stdout, stderr io.Writer, env func(string) string) int {
	f := newFlags("login", env)
	cfg, code := f.config(args, stderr)
	if code != nil {
		return *code
	}
	s, err := app.Resolve(cfg, app.Options{Env: env, Keyring: keyringBackend, Warn: warnTo(stderr)})
	if err != nil {
		return fail(stderr, "%v", err)
	}
	if s.ClientID == "" {
		code := fail(stderr, "%s", noApplication)
		writeSetup(stderr, cfg.Mode())
		return code
	}
	p := s.Profile
	if p.HasUser && p.User.Instance != "" && p.User.Instance != s.Instance.String() {
		outf(stdout, "Note: profile %q was signed in to another instance; this login replaces it.\n", p.Name)
	}

	requested := cfg.Scopes()
	outf(stdout, "Signing in to profile %q on %s, asking for:\n", p.Name, s.Instance)
	for _, sc := range requested {
		outf(stdout, "  %s\n", sc)
	}
	outf(stdout, "\n")

	ctx, cancel := context.WithTimeout(context.Background(), loginTimeout)
	defer cancel()
	// The URL is written unmasked: it is what the person opens.
	g, err := s.Application().Login(ctx, requested, auth.LoginOptions{
		Out: stdout, NoBrowser: f.noBrowser, OpenBrowser: openBrowser, Timeout: loginTimeout,
	})
	if errors.Is(err, auth.ErrConfidential) {
		// The instruction is the whole message; the layers it came
		// through and GitLab's generic wording add nothing to act on.
		return fail(stderr, "login failed: %v", auth.ErrConfidential)
	}
	if err != nil {
		return fail(stderr, "login failed: %v", err)
	}
	src, err := p.Store.Save(g.Token)
	if err != nil {
		return fail(stderr, "store the token: %v", err)
	}

	// The profile records what GitLab GRANTED. A scope requested and
	// refused is the failure worth seeing, and storing the request would
	// present it as a grant.
	granted := requested
	if len(g.Scopes) > 0 {
		granted = g.Scopes
	}
	if missing := scopes.Missing(granted, requested); len(missing) > 0 {
		outf(stderr, "warning: GitLab granted %s, not %s, so the tools that need it will not work; "+
			"run `gitlab-mcp login` again and grant every scope asked for\n",
			strings.Join(granted, " "), strings.Join(missing, " "))
	}

	uc := p.User
	uc.Instance = s.Instance.String()
	uc.ClientID = s.ClientID
	uc.TokenStore = string(src)
	uc.Scopes = granted
	uc.Username = ""
	if u, err := currentUser(ctx, s, g.Token.AccessToken); err == nil {
		uc.Username = u
	} else {
		outf(stderr, "warning: signed in, and the account could not be read: %v\n", err)
	}
	if err := cfg.ConfigDir.Save(p.Name, uc); err != nil {
		return fail(stderr, "save the profile: %v", err)
	}
	outf(stdout, "\nSigned in as %s (profile %q, token in the %s).\n", orUnknown(uc.Username), p.Name, src)
	return 0
}

// currentUser reads the account a fresh access token acts as.
func currentUser(ctx context.Context, s *app.Settings, access string) (string, error) {
	c, err := s.ClientWith(gapi.Options{Tokens: gapi.StaticToken(access), Version: version.String()})
	if err != nil {
		return "", err
	}
	return userOf(ctx, c)
}

// cmdLogout revokes the stored token at GitLab, then removes the local
// copy and the profile. A token supplied through the environment is
// neither revoked nor removed: it is not this command's.
func cmdLogout(args []string, stdout, stderr io.Writer, env func(string) string) int {
	f := newFlags("logout", env)
	cfg, code := f.config(args, stderr)
	if code != nil {
		return *code
	}
	p, err := app.OpenProfile(cfg, keyringBackend, env, warnTo(stderr))
	if err != nil {
		return fail(stderr, "%v", err)
	}
	// Revoking one token signs out only this profile. Deleting or
	// renewing the application in GitLab, the usual next step, signs
	// out every profile that uses it, so they are named first.
	if others, err := cfg.ConfigDir.SharingClient(p.Name); err == nil && len(others) > 0 {
		outf(stdout, "Note: these profiles use the same OAuth application; deleting or renewing it in GitLab "+
			"signs them out too: %s\n", strings.Join(others, ", "))
	}

	tok, _, err := p.Store.ResolveStored()
	switch {
	case err == nil:
		revoke(stdout, cfg, p, tok.RefreshToken)
	case errors.Is(err, credentials.ErrNotFound):
		outf(stdout, "No stored token to revoke.\n")
	default:
		outf(stdout, "Could not read the stored token (%v); removing what is there.\n", err)
	}
	if env(credentials.EnvVar) != "" {
		outf(stderr, "warning: %s is set; logout neither revokes nor removes it\n", credentials.EnvVar)
	}

	if err := p.Store.Delete(); err != nil {
		return fail(stderr, "%v", err)
	}
	if err := cfg.ConfigDir.Remove(p.Name); err != nil {
		return fail(stderr, "%v", err)
	}
	outf(stdout, "Signed out of profile %q.\n", p.Name)
	return 0
}

// revoke revokes the refresh token at the instance and with the
// application the profile signed in with, which revokes its access
// token too. Failing is not fatal: the local copy goes either way.
func revoke(stdout io.Writer, cfg config.Config, p *app.Profile, refresh string) {
	clientID := p.User.ClientID
	if clientID == "" {
		clientID = cfg.ClientID
	}
	// The stored instance passed the http check when login stored it.
	inst, err := instance.Parse(p.User.Instance, true)
	if err != nil || clientID == "" {
		outf(stdout, "The profile does not record its instance and application, so the token was not revoked; "+
			"removing the local copy anyway.\n")
		return
	}
	hc, err := app.NewHTTPClient(cfg)
	if err != nil {
		outf(stdout, "Could not revoke the token at GitLab (%v); removing the local copy anyway.\n", err)
		return
	}
	a := &auth.Application{Instance: inst, ClientID: clientID, HTTPClient: hc, Timeout: cfg.HTTPTimeout}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.HTTPTimeout)
	defer cancel()
	if err := a.Revoke(ctx, refresh); err != nil {
		outf(stdout, "Could not revoke the token at GitLab (%v); removing the local copy anyway.\n", err)
		return
	}
	outf(stdout, "Revoked the token at GitLab.\n")
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}
