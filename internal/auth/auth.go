// Package auth is the family's sign-in on GitLab's OAuth provider: the
// person's own OAuth application, a loopback redirect on 127.0.0.1 with
// a random port, PKCE S256, and a refreshing token source that survives
// GitLab's immediate rotation (docs/architecture.md §10).
//
// It differs from the siblings only where GitLab forces it. The
// application is named by its id, not a file, and is public, so no
// secret is sent (§2.2). A refresh revokes the old pair at once (§2.3),
// so refreshing happens under a cross-process lock and the new pair is
// stored before it is used; see TokenSource. No `resource` parameter is
// ever sent: one ending in /api/v4/mcp silently narrows the grant to
// the mcp scope (§18 row 7).
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/internal/instance"
	"github.com/mmedum/gitlab-mcp/internal/redact"
	"github.com/mmedum/gitlab-mcp/internal/scopes"
)

// DefaultHTTPTimeout bounds one call to the OAuth endpoints when no
// timeout is given.
const DefaultHTTPTimeout = 60 * time.Second

// verifierBytes makes a 64-character PKCE verifier. RFC 7636 allows 43
// to 128; GitLab logs verifiers shorter than 43 and may refuse them
// later (§2.2), so this stays well inside the range.
const verifierBytes = 48

// maxBody bounds an OAuth endpoint's answer.
const maxBody = 1 << 16

// Endpoint paths under the instance's web base.
const (
	pathAuthorize = "/oauth/authorize"
	pathToken     = "/oauth/token" //nolint:gosec // an endpoint path, not a credential
	pathRevoke    = "/oauth/revoke"
	pathTokenInfo = "/oauth/token/info" //nolint:gosec // an endpoint path, not a credential
)

// ErrConfidential means GitLab refused the application as a public
// client. The usual cause is the "Confidential" box, which GitLab ticks
// by default when an application is created.
var ErrConfidential = errors.New(`GitLab refused the application as a public client (invalid_client). ` +
	`Open the application in GitLab (user settings > Applications), untick "Confidential", save, and run ` +
	"`gitlab-mcp login` again. If it is already unticked, check the application id")

// ErrReauthorize means the stored sign-in no longer works: revoked,
// expired, or spent by a refresh whose result was lost. Only a new login
// fixes it, so nothing retries it.
var ErrReauthorize = errors.New("auth: the stored sign-in no longer works; run `gitlab-mcp login`")

// OAuthError is an OAuth endpoint's refusal, with the error code GitLab
// sent. The description is kept short and masked; it is Doorkeeper's
// fixed text, and never trusted to be.
type OAuthError struct {
	Status      int
	Code        string
	Description string
}

func (e *OAuthError) Error() string {
	msg := fmt.Sprintf("auth: GitLab answered %d", e.Status)
	if e.Code != "" {
		msg += " " + e.Code
	}
	if e.Description != "" {
		msg += ": " + e.Description
	}
	return msg
}

// IsInvalidGrant reports a refusal of the grant itself: a code or a
// refresh token that is spent, revoked or unknown.
func IsInvalidGrant(err error) bool {
	var oe *OAuthError
	return errors.As(err, &oe) && oe.Code == "invalid_grant"
}

// Application is the person's OAuth application on one instance.
type Application struct {
	Instance instance.Instance
	// ClientID is the application id GitLab shows. The application is
	// public, so there is no secret.
	ClientID string
	// HTTPClient carries the transport and its proxy. It is
	// copied; redirects are refused and Timeout applied.
	HTTPClient *http.Client
	// Timeout bounds one call. Zero means DefaultHTTPTimeout.
	Timeout time.Duration
	// Now is the clock; tests stub it.
	Now func() time.Time
}

func (a *Application) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Application) endpoint(path string) string {
	u := a.Instance.WebBase()
	u.Path += path
	return u.String()
}

// client is the bounded HTTP client for one call. A redirect is never
// followed: the token endpoint answering with one is a misconfiguration,
// and following it would carry a code or a token to another address.
func (a *Application) client() *http.Client {
	hc := &http.Client{}
	if a.HTTPClient != nil {
		copied := *a.HTTPClient
		hc = &copied
	}
	hc.Timeout = a.Timeout
	if hc.Timeout <= 0 {
		hc.Timeout = DefaultHTTPTimeout
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return hc
}

func (a *Application) check() error {
	if a.Instance.IsZero() {
		return errors.New("auth: no GitLab instance is configured")
	}
	if strings.TrimSpace(a.ClientID) == "" {
		return errors.New("auth: no OAuth application id; pass --client-id")
	}
	return nil
}

// Grant is what a token request returned.
type Grant struct {
	Token *oauth2.Token
	// Scopes is what GitLab granted, which can be less than was asked.
	Scopes []string
}

// tokenResponse is Doorkeeper's token answer.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

// oauthErrorBody is RFC 6749 §5.2.
type oauthErrorBody struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// post sends a form to an OAuth endpoint and returns the body of a 200.
func (a *Application) post(ctx context.Context, path string, form url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint(path), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("auth: build request: %w", gapi.WithoutURL(err))
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return a.send(req)
}

func (a *Application) send(req *http.Request) ([]byte, error) {
	resp, err := a.client().Do(req)
	if err != nil {
		return nil, &TransportError{err: gapi.WithoutURL(err)}
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, &TransportError{err: gapi.WithoutURL(err)}
	}
	if resp.StatusCode == http.StatusOK {
		return body, nil
	}
	var eb oauthErrorBody
	_ = json.Unmarshal(body, &eb)
	return nil, &OAuthError{Status: resp.StatusCode, Code: eb.Error, Description: redact.Clip(eb.ErrorDescription, 200)}
}

// TransportError is a failure to reach an OAuth endpoint at all. Its
// text carries no URL and no host (§9.2): the URL names the instance,
// and on token info it once carried the token itself.
type TransportError struct{ err error }

func (e *TransportError) Error() string { return "auth: GitLab could not be reached: " + e.err.Error() }

// Unwrap exposes the cause.
func (e *TransportError) Unwrap() error { return e.err }

func (a *Application) grant(body []byte) (*Grant, error) {
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		// The parse error can quote the body, which holds tokens.
		return nil, errors.New("auth: GitLab's token answer is not JSON")
	}
	if tr.AccessToken == "" {
		return nil, errors.New("auth: GitLab's token answer carries no access token")
	}
	tok := &oauth2.Token{AccessToken: tr.AccessToken, TokenType: tr.TokenType, RefreshToken: tr.RefreshToken}
	// expires_in is read, never assumed: 7,200 s by default, and an
	// administrator can set it as low as 300 (§2.3).
	if tr.ExpiresIn > 0 {
		tok.Expiry = a.now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return &Grant{Token: tok, Scopes: scopes.Parse(tr.Scope)}, nil
}

// classify turns invalid_client into the advice that fixes it.
func classify(err error) error {
	var oe *OAuthError
	if errors.As(err, &oe) && oe.Code == "invalid_client" {
		return fmt.Errorf("%w (%w)", ErrConfidential, err)
	}
	return err
}

// Exchange trades an authorization code for a token pair.
func (a *Application) Exchange(ctx context.Context, code, redirectURI, verifier string) (*Grant, error) {
	if err := a.check(); err != nil {
		return nil, err
	}
	body, err := a.post(ctx, pathToken, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {a.ClientID},
		"code_verifier": {verifier},
	})
	if err != nil {
		return nil, classify(err)
	}
	return a.grant(body)
}

// Refresh spends a refresh token for a new pair. GitLab revokes the old
// pair the moment the new one exists, so the caller stores the result
// before it does anything else; TokenSource does.
func (a *Application) Refresh(ctx context.Context, refreshToken string) (*Grant, error) {
	if err := a.check(); err != nil {
		return nil, err
	}
	body, err := a.post(ctx, pathToken, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {a.ClientID},
	})
	if err != nil {
		return nil, classify(err)
	}
	return a.grant(body)
}

// Revoke revokes a token at GitLab. Revoking the refresh token revokes
// the access token issued with it.
func (a *Application) Revoke(ctx context.Context, token string) error {
	if err := a.check(); err != nil {
		return err
	}
	_, err := a.post(ctx, pathRevoke, url.Values{"token": {token}, "client_id": {a.ClientID}})
	return classify(err)
}

// TokenInfo is Doorkeeper's view of an access token.
type TokenInfo struct {
	Scopes []string
	// ExpiresIn is the time left; zero for a token that does not expire.
	ExpiresIn time.Duration
	// ApplicationID is the id of the application the token was issued to.
	ApplicationID   string
	ResourceOwnerID int64
}

// TokenInfo reads /oauth/token/info for an access token. The token
// travels in the Authorization header, never in the URL.
func (a *Application) TokenInfo(ctx context.Context, accessToken string) (*TokenInfo, error) {
	if a.Instance.IsZero() {
		return nil, errors.New("auth: no GitLab instance is configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.endpoint(pathTokenInfo), nil)
	if err != nil {
		return nil, fmt.Errorf("auth: build request: %w", gapi.WithoutURL(err))
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	body, err := a.send(req)
	if err != nil {
		return nil, err
	}
	var raw gitlab.TokenInfo
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("auth: GitLab's token info is not JSON")
	}
	info := &TokenInfo{Scopes: raw.Scope, ApplicationID: raw.Application.UID, ResourceOwnerID: raw.ResourceOwnerID}
	if raw.ExpiresIn != nil && *raw.ExpiresIn > 0 {
		info.ExpiresIn = time.Duration(*raw.ExpiresIn) * time.Second
	}
	return info, nil
}

// LoginOptions tune the interactive flow. Zero values are sensible.
type LoginOptions struct {
	// OpenBrowser is called with the authorization URL. nil uses the
	// OS's default browser; a failure is not fatal, since the URL is
	// printed to Out as well.
	OpenBrowser func(url string) error
	// NoBrowser prints the URL and does not try to open anything. It is
	// what makes this flow usable over SSH, where the callback reaches
	// the remote host's loopback and the browser is local.
	NoBrowser bool
	// Out receives the URL and progress messages. nil discards them.
	Out io.Writer
	// Timeout bounds the person's trip through the browser. Default 5m.
	Timeout time.Duration
	// Listener overrides the loopback listener (tests).
	Listener net.Listener
}

// Login runs the loopback authorization-code flow and returns a grant
// that includes a refresh token.
//
// The listener is the IP literal 127.0.0.1 on a random port, not
// "localhost": RFC 8252 §7.3 says the literal "avoids inadvertently
// listening on network interfaces other than the loopback interface",
// and GitLab ignores the port of a loopback IP literal when it compares
// redirect URIs, while it compares localhost exactly (§2.1).
func (a *Application) Login(ctx context.Context, requested []string, opts LoginOptions) (*Grant, error) {
	if err := a.check(); err != nil {
		return nil, err
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ln := opts.Listener
	if ln == nil {
		var err error
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("auth: listen on loopback: %w", err)
		}
	}
	port := ln.Addr().(*net.TCPAddr).Port

	registered, err := url.Parse(scopes.RedirectURI)
	if err != nil {
		return nil, fmt.Errorf("auth: redirect URI: %w", err)
	}
	redirect := *registered
	redirect.Host = fmt.Sprintf("%s:%d", registered.Hostname(), port)
	redirectURI := redirect.String()

	state, err := randomString(24)
	if err != nil {
		return nil, err
	}
	verifier, err := randomString(verifierBytes)
	if err != nil {
		return nil, err
	}
	authURL := a.authorizeURL(redirectURI, state, challenge(verifier), requested)

	type outcome struct {
		code string
		err  error
	}
	results := make(chan outcome, 1)
	deliver := func(o outcome) {
		select {
		case results <- o:
		default:
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc(registered.Path, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			// Not the authorization's redirect: another page or process
			// reached the port. It must not end the login, or anything
			// on this machine could cancel one; the real redirect may
			// still come.
			http.Error(w, "this is not the redirect this login is waiting for", http.StatusBadRequest)
			return
		}
		if e := q.Get("error"); e != "" {
			http.Error(w, "authorization failed: "+e, http.StatusBadRequest)
			deliver(outcome{err: fmt.Errorf("auth: authorization denied: %s", redact.Clip(e, 80))})
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			deliver(outcome{err: errors.New("auth: callback without code")})
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, successPage)
		deliver(outcome{code: code})
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	_, _ = fmt.Fprintf(out, "Open this URL in your browser to authorize gitlab-mcp:\n\n%s\n\n", authURL)
	if opts.NoBrowser {
		// Said here rather than only in the README, because this is the
		// moment somebody on a remote host finds out it does not work.
		_, _ = fmt.Fprintf(out, "The callback goes to 127.0.0.1:%d on THIS machine. If your browser is "+
			"somewhere else, forward the port first:\n\n    ssh -L %d:127.0.0.1:%d <this-host>\n\n", port, port, port)
	} else {
		open := opts.OpenBrowser
		if open == nil {
			open = OpenBrowser
		}
		if err := open(authURL); err != nil {
			_, _ = fmt.Fprintf(out, "(could not open a browser automatically: %v)\n", err)
		}
	}
	_, _ = fmt.Fprintln(out, "Waiting for the browser to finish...")

	var code string
	select {
	case o := <-results:
		if o.err != nil {
			return nil, o.err
		}
		code = o.code
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(timeout):
		return nil, errors.New("auth: timed out waiting for the browser")
	}

	g, err := a.Exchange(ctx, code, redirectURI, verifier)
	if err != nil {
		return nil, fmt.Errorf("auth: exchange code: %w", err)
	}
	if g.Token.RefreshToken == "" {
		return nil, errors.New("auth: GitLab returned no refresh token; check that the application is not " +
			"restricted and run `gitlab-mcp login` again")
	}
	// A token answer without a scope field is read back from token info
	// rather than assumed to be what was asked.
	if len(g.Scopes) == 0 {
		if info, err := a.TokenInfo(ctx, g.Token.AccessToken); err == nil {
			g.Scopes = info.Scopes
		}
	}
	return g, nil
}

// authorizeURL builds the authorization request. Deliberately absent: a
// `resource` parameter (§18 row 7) and a client secret.
func (a *Application) authorizeURL(redirectURI, state, codeChallenge string, requested []string) string {
	q := url.Values{
		"client_id":             {a.ClientID},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"scope":                 {strings.Join(requested, " ")},
		"state":                 {state},
		"code_challenge":        {codeChallenge},
		"code_challenge_method": {"S256"},
	}
	return a.endpoint(pathAuthorize) + "?" + q.Encode()
}

// challenge is the S256 code challenge for a verifier (RFC 7636 §4.2).
func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// OpenBrowser opens u with the platform's default handler.
func OpenBrowser(u string) error {
	// u is the authorization URL this package just built; exec.Command
	// passes it as one argument with no shell, so nothing can be
	// injected into it.
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u) //nolint:gosec // our own URL, no shell
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u) //nolint:gosec // our own URL, no shell
	default:
		cmd = exec.Command("xdg-open", u) //nolint:gosec // our own URL, no shell
	}
	return cmd.Start()
}

// randomString is n random bytes, base64url without padding: the
// alphabet RFC 7636 allows in a verifier.
func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

const successPage = `<!doctype html><meta charset="utf-8"><title>gitlab-mcp</title>
<body style="font-family:system-ui;margin:3rem"><h2>Signed in</h2>
<p>gitlab-mcp received the authorization. You can close this window.</p></body>`
