package auth

import (
	"context"
	"errors"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/mmedum/gitlab-mcp/internal/credentials"
	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/redact"
)

// DefaultRefreshMargin is how long before expiry a token is refreshed.
// GitLab's shortest configurable lifetime is 300 s (§2.3), so a minute
// is always less than a token lives.
const DefaultRefreshMargin = time.Minute

// Store is where the token pair lives between processes.
// *credentials.Store is one.
type Store interface {
	Resolve() (*oauth2.Token, credentials.Source, error)
	Save(*oauth2.Token) (credentials.Source, error)
}

// TokenSourceOptions tune a TokenSource. Only LockPath is required.
type TokenSourceOptions struct {
	// LockPath is the file the refresh locks: userconfig's LockPath for
	// the profile, shared by every process using it.
	LockPath string
	// LockTimeout bounds the wait for another process's refresh. Zero
	// means twice the application's timeout, so one slow refresh
	// elsewhere is waited out.
	LockTimeout time.Duration
	// Margin is how long before expiry to refresh. Zero means
	// DefaultRefreshMargin.
	Margin time.Duration
	// Warn receives what the person should hear about and no call
	// should fail over: a rotated pair that could not be stored.
	Warn func(string)
	// Now is the clock; tests stub it.
	Now func() time.Time
}

// TokenSource hands out a valid access token, refreshing it ahead of its
// expiry. It implements gapi.TokenSource and is safe for concurrent use.
//
// GitLab spends a refresh token once and revokes the old pair the moment
// it rotates (§2.3), and Claude Desktop and Claude Code may run a server
// each on one profile. So a refresh happens under a cross-process file
// lock; inside it the store is read again first, since another process
// may have refreshed while this one waited; and the new pair is stored
// before it is used. A refusal of the refresh token is checked against
// the store once more before anyone is told to log in again.
type TokenSource struct {
	app   *Application
	store Store
	opts  TokenSourceOptions

	mu  sync.Mutex
	cur *oauth2.Token
	// dead is the reauthorize error for the refresh token deadRefresh.
	// It is answered without asking GitLab again until the store holds
	// a different refresh token, which a new login writes.
	dead        error
	deadRefresh string
	// rejected is the access token GitLab last refused. It is never
	// fresh again, whatever its expiry says.
	rejected string
}

var (
	_ gapi.TokenSource = (*TokenSource)(nil)
	_ gapi.Invalidator = (*TokenSource)(nil)
)

// NewTokenSource builds a token source over a store. Nothing is read
// until the first Token call.
func NewTokenSource(app *Application, store Store, opts TokenSourceOptions) *TokenSource {
	if opts.Margin <= 0 {
		opts.Margin = DefaultRefreshMargin
	}
	if opts.LockTimeout <= 0 {
		t := app.Timeout
		if t <= 0 {
			t = DefaultHTTPTimeout
		}
		opts.LockTimeout = 2 * t
	}
	return &TokenSource{app: app, store: store, opts: opts}
}

func (s *TokenSource) now() time.Time {
	if s.opts.Now != nil {
		return s.opts.Now()
	}
	return time.Now()
}

func (s *TokenSource) warn(msg string) {
	if s.opts.Warn != nil {
		s.opts.Warn(msg)
	}
}

// fresh reports whether tok can be used without a refresh. A token with
// no expiry is taken as not expiring: GitLab always sends expires_in on
// the tokens it issues now.
func (s *TokenSource) fresh(tok *oauth2.Token) bool {
	if tok == nil || tok.AccessToken == "" || tok.AccessToken == s.rejected {
		return false
	}
	return tok.Expiry.IsZero() || s.now().Add(s.opts.Margin).Before(tok.Expiry)
}

// Token returns a valid access token.
func (s *TokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil || s.dead != nil {
		tok, err := s.load()
		if err != nil {
			return "", err
		}
		if s.dead != nil && tok.RefreshToken == s.deadRefresh {
			return "", s.dead
		}
		s.dead, s.deadRefresh = nil, ""
		s.cur = tok
	}
	if s.fresh(s.cur) {
		return s.cur.AccessToken, nil
	}
	if err := s.refresh(ctx); err != nil {
		return "", err
	}
	return s.cur.AccessToken, nil
}

// Invalidate drops an access token GitLab refused with 401. The next
// Token reads the store again, where a login in another process leaves
// its pair, and refreshes if the store holds the refused token too.
func (s *TokenSource) Invalidate(rejected string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejected = rejected
	if s.cur != nil && s.cur.AccessToken == rejected {
		s.cur = nil
	}
}

// load reads the stored pair.
func (s *TokenSource) load() (*oauth2.Token, error) {
	tok, _, err := s.store.Resolve()
	switch {
	case err == nil:
		return tok, nil
	case errors.Is(err, credentials.ErrNotFound):
		return nil, gapi.Wrap(gapi.ClassAuth, err, "not signed in: run `gitlab-mcp login`")
	case errors.Is(err, credentials.ErrKeyringSilent):
		return nil, gapi.Wrap(gapi.ClassAuth, err, "the OS keyring holds this profile's token and did not "+
			"hand it over; unlock the keyring and try again")
	default:
		return nil, gapi.Wrap(gapi.ClassAuth, err, "the stored sign-in could not be read: %s", redact.Text(err.Error()))
	}
}

// refresh replaces s.cur with a valid pair. The caller holds s.mu.
func (s *TokenSource) refresh(ctx context.Context) error {
	release, err := lockFile(ctx, s.opts.LockPath, s.opts.LockTimeout)
	if err != nil {
		if errors.Is(err, ErrLockTimeout) {
			return gapi.Wrap(gapi.ClassUnavailable, err, "another gitlab-mcp process is refreshing the sign-in "+
				"and has not finished; try again")
		}
		return gapi.Wrap(gapi.ClassUnavailable, err, "the sign-in could not be refreshed: %s", redact.Text(err.Error()))
	}
	defer release()

	// Another process may have refreshed while this one waited, and the
	// refresh token held here is then already spent.
	if stored, _, err := s.store.Resolve(); err == nil && stored.RefreshToken != s.cur.RefreshToken {
		s.cur = stored
		if s.fresh(stored) {
			return nil
		}
	}

	for attempt := 0; ; attempt++ {
		sent := s.cur.RefreshToken
		if sent == "" {
			return s.reauthorize(sent, nil)
		}
		g, err := s.app.Refresh(ctx, sent)
		if err == nil {
			if g.Token.RefreshToken == "" {
				// Doorkeeper keeps the refresh token when it does not
				// rotate; GitLab rotates, but nothing is lost by
				// keeping the one that was sent.
				g.Token.RefreshToken = sent
			}
			// Stored before it is used: the old pair is already revoked,
			// and a process that dies holding the only copy of the new
			// one leaves every other process signed out.
			if _, serr := s.store.Save(g.Token); serr != nil {
				s.warn("the refreshed sign-in could not be stored (" + redact.Text(serr.Error()) + "); this " +
					"process keeps working, and other processes on this profile will need `gitlab-mcp login`")
			}
			s.cur = g.Token
			return nil
		}
		if !IsInvalidGrant(err) {
			return refreshFailure(err)
		}
		// Refused. Another process may have rotated the pair between
		// this one's read and its request, without the lock (a process
		// from an older build) or through a login. Its pair is in the
		// store if so; asked once, never in a loop.
		if attempt == 0 {
			if stored, _, rerr := s.store.Resolve(); rerr == nil && stored.RefreshToken != sent {
				s.cur = stored
				if s.fresh(stored) {
					return nil
				}
				continue
			}
		}
		return s.reauthorize(sent, err)
	}
}

// reauthorize records that the refresh token sent is dead and returns
// the error that says to log in again.
func (s *TokenSource) reauthorize(sent string, cause error) error {
	err := ErrReauthorize
	if cause != nil {
		err = errors.Join(ErrReauthorize, cause)
	}
	s.dead = gapi.Wrap(gapi.ClassAuth, err, "the sign-in was revoked or has expired: run `gitlab-mcp login`")
	s.deadRefresh = sent
	return s.dead
}

// refreshFailure classifies a refresh that failed for a reason other
// than a refused grant.
func refreshFailure(err error) error {
	var te *TransportError
	if errors.As(err, &te) {
		return gapi.Wrap(gapi.ClassUnavailable, err, "the sign-in could not be refreshed: %s", redact.Text(err.Error()))
	}
	if errors.Is(err, ErrConfidential) {
		return gapi.Wrap(gapi.ClassAuth, err, "%s", redact.Text(err.Error()))
	}
	var oe *OAuthError
	if errors.As(err, &oe) && oe.Status >= 500 {
		return gapi.Wrap(gapi.ClassUnavailable, err, "GitLab's token endpoint answered %d; try again", oe.Status)
	}
	return gapi.Wrap(gapi.ClassAuth, err, "the sign-in could not be refreshed: %s; run `gitlab-mcp login` if it "+
		"persists", redact.Text(err.Error()))
}

// NoCredentials is the token source of a server started before anyone
// signed in. The server still starts, so tools/list and the schema dump
// work, and every call answers that login is needed.
type NoCredentials struct{ Reason error }

// Token always fails, naming why there are no credentials.
func (n NoCredentials) Token(context.Context) (string, error) {
	if n.Reason == nil {
		return "", gapi.Errf(gapi.ClassAuth, "not signed in: run `gitlab-mcp login`")
	}
	var ge *gapi.Error
	if errors.As(n.Reason, &ge) {
		return "", ge
	}
	return "", gapi.Wrap(gapi.ClassAuth, n.Reason, "%s", redact.Text(n.Reason.Error()))
}
