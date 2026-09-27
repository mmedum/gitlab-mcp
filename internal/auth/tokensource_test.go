package auth

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/mmedum/gitlab-mcp/internal/credentials"
	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/gapi/gitlabtest"
)

// signIn runs the authorization and exchange without a browser and
// returns the grant.
func signIn(t *testing.T, app *Application, scope string) *Grant {
	t.Helper()
	const redirect = "http://127.0.0.1:41234/callback"
	verifier, _ := randomString(verifierBytes)
	code := authorize(t, app, redirect, challenge(verifier), scope)
	g, err := app.Exchange(context.Background(), code, redirect, verifier)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// fileStore is a credentials store on a file, as a process whose
// keyring is unavailable has, so two of them on one path behave like
// two processes sharing a profile.
func fileStore(dir string) *credentials.Store {
	return &credentials.Store{Profile: "default", FilePath: filepath.Join(dir, "token.json"),
		Env: func(string) string { return "" }}
}

type fixture struct {
	srv   *gitlabtest.Server
	clock *clock
	app   *Application
	dir   string
}

func newFixture(t *testing.T, ttl time.Duration) *fixture {
	t.Helper()
	c := newClock()
	srv := gitlabtest.New(t, gitlabtest.Options{Now: c.Now, AccessTokenTTL: ttl})
	return &fixture{srv: srv, clock: c, app: newApp(t, srv, c), dir: t.TempDir()}
}

func (f *fixture) source(store Store) *TokenSource {
	return NewTokenSource(f.app, store, TokenSourceOptions{
		LockPath: filepath.Join(f.dir, "refresh.lock"), Now: f.clock.Now, LockTimeout: 5 * time.Second,
	})
}

// signedIn stores a fresh pair and returns it.
func (f *fixture) signedIn(t *testing.T) *oauth2.Token {
	t.Helper()
	g := signIn(t, f.app, "api")
	if _, err := fileStore(f.dir).Save(g.Token); err != nil {
		t.Fatal(err)
	}
	return g.Token
}

func stored(t *testing.T, s Store) *oauth2.Token {
	t.Helper()
	tok, _, err := s.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestTheTokenIsRefreshedAheadOfExpiresIn(t *testing.T) {
	// 300 s is the shortest lifetime an administrator can set; a source
	// that assumed GitLab's default two hours would use a dead token.
	f := newFixture(t, 300*time.Second)
	first := f.signedIn(t)
	store := fileStore(f.dir)
	ts := f.source(store)
	ctx := context.Background()

	f.clock.Advance(200 * time.Second)
	got, err := ts.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != first.AccessToken || f.srv.Refreshes() != 0 {
		t.Fatalf("at 200 s: token %q after %d refreshes, want the first with none", got, f.srv.Refreshes())
	}

	f.clock.Advance(50 * time.Second) // 50 s left: inside the margin
	got, err = ts.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got == first.AccessToken || f.srv.Refreshes() != 1 {
		t.Fatalf("at 250 s: token %q after %d refreshes, want a new one after 1", got, f.srv.Refreshes())
	}
	// Rotated and stored: the stored pair is the new one, and the old
	// refresh token is spent.
	now := stored(t, store)
	if now.AccessToken != got || now.RefreshToken == first.RefreshToken {
		t.Errorf("stored pair %q/%q, want the rotated one", now.AccessToken, now.RefreshToken)
	}
	if !now.Expiry.Equal(f.clock.Now().Add(300 * time.Second)) {
		t.Errorf("stored expiry %s, want %s", now.Expiry, f.clock.Now().Add(300*time.Second))
	}
	if _, err := f.app.Refresh(ctx, first.RefreshToken); !IsInvalidGrant(err) {
		t.Errorf("the old refresh token still works: %v", err)
	}
}

func TestTwoProcessesRefreshOnceAndShareTheResult(t *testing.T) {
	f := newFixture(t, 300*time.Second)
	f.signedIn(t)
	// Two token sources, each with its own store on the one file and its
	// own handle on the one lock: two processes on one profile.
	a, b := f.source(fileStore(f.dir)), f.source(fileStore(f.dir))
	ctx := context.Background()
	// Both load the pair while it is valid, then it expires under them.
	if _, err := a.Token(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Token(ctx); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Hour)

	var wg sync.WaitGroup
	results := make([]string, 2)
	errs := make([]error, 2)
	for i, ts := range []*TokenSource{a, b} {
		wg.Go(func() { results[i], errs[i] = ts.Token(ctx) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("source %d: %v", i, err)
		}
	}
	if f.srv.Refreshes() != 1 {
		t.Errorf("%d refreshes, want exactly 1", f.srv.Refreshes())
	}
	if results[0] != results[1] {
		t.Errorf("sources hold %q and %q, want the one new token", results[0], results[1])
	}
	if got := stored(t, fileStore(f.dir)).AccessToken; got != results[0] {
		t.Errorf("stored %q, want %q", got, results[0])
	}
}

func TestARevokedSignInAsksForLoginAndIsNotRetried(t *testing.T) {
	f := newFixture(t, 300*time.Second)
	tok := f.signedIn(t)
	ts := f.source(fileStore(f.dir))
	ctx := context.Background()
	f.srv.Revoke(tok.RefreshToken)
	f.clock.Advance(time.Hour)

	_, err := ts.Token(ctx)
	if !errors.Is(err, ErrReauthorize) {
		t.Fatalf("err = %v, want ErrReauthorize", err)
	}
	if c, ok := gapi.ClassOf(err); !ok || c != gapi.ClassAuth || c.Retryable() {
		t.Errorf("class %q (ok %t), want auth, not retryable", c, ok)
	}
	if !strings.HasPrefix(err.Error(), "[auth] ") || !strings.Contains(err.Error(), "gitlab-mcp login") {
		t.Errorf("err %q", err)
	}

	before := countTokenRequests(f.srv)
	if _, err := ts.Token(ctx); !errors.Is(err, ErrReauthorize) {
		t.Errorf("second call: %v", err)
	}
	if after := countTokenRequests(f.srv); after != before {
		t.Errorf("the dead refresh token was sent again (%d token requests, then %d)", before, after)
	}

	// A login elsewhere writes a new pair, and the source picks it up.
	fresh := f.signedIn(t)
	got, err := ts.Token(ctx)
	if err != nil || got != fresh.AccessToken {
		t.Errorf("after a new login: %q, %v; want %q", got, err, fresh.AccessToken)
	}
}

func countTokenRequests(srv *gitlabtest.Server) int {
	n := 0
	for _, r := range srv.Requests() {
		if r.EscapedPath == "/oauth/token" {
			n++
		}
	}
	return n
}

// racingStore answers the first reads with the pair the source started
// from, and later reads with a pair another process wrote: the window
// between the read inside the lock and the request.
type racingStore struct {
	mu     sync.Mutex
	reads  int
	before *oauth2.Token
	after  *oauth2.Token
	saved  []*oauth2.Token
}

func (r *racingStore) Resolve() (*oauth2.Token, credentials.Source, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	if r.reads <= 2 {
		return r.before, credentials.SourceFile, nil
	}
	return r.after, credentials.SourceFile, nil
}

func (r *racingStore) Save(tok *oauth2.Token) (credentials.Source, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saved = append(r.saved, tok)
	return credentials.SourceFile, nil
}

func TestInvalidGrantRereadsTheStoreBeforeGivingUp(t *testing.T) {
	f := newFixture(t, 300*time.Second)
	old := signIn(t, f.app, "api").Token
	// Another process refreshed: the old refresh token is spent, and
	// its successor is valid.
	newer, err := f.app.Refresh(context.Background(), old.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	expired := *old
	expired.Expiry = f.clock.Now().Add(-time.Minute)
	store := &racingStore{before: &expired, after: newer.Token}
	ts := f.source(store)

	got, err := ts.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != newer.Token.AccessToken {
		t.Errorf("token %q, want the other process's %q", got, newer.Token.AccessToken)
	}
	if store.reads != 3 {
		t.Errorf("%d store reads, want 3: load, inside the lock, after invalid_grant", store.reads)
	}
	if f.srv.Refreshes() != 1 {
		t.Errorf("%d refreshes, want only the other process's", f.srv.Refreshes())
	}
}

// failingSave stores nothing.
type failingSave struct{ tok *oauth2.Token }

func (s failingSave) Resolve() (*oauth2.Token, credentials.Source, error) {
	return s.tok, credentials.SourceKeyring, nil
}

func (failingSave) Save(*oauth2.Token) (credentials.Source, error) {
	return "", errors.New("keyring locked")
}

func TestARotatedPairThatCannotBeStoredIsWarnedAboutAndStillUsed(t *testing.T) {
	f := newFixture(t, 300*time.Second)
	tok := signIn(t, f.app, "api").Token
	f.clock.Advance(time.Hour)
	var warned []string
	ts := NewTokenSource(f.app, failingSave{tok}, TokenSourceOptions{
		LockPath: filepath.Join(f.dir, "refresh.lock"), Now: f.clock.Now,
		Warn: func(m string) { warned = append(warned, m) },
	})
	got, err := ts.Token(context.Background())
	if err != nil || got == tok.AccessToken {
		t.Fatalf("token %q, %v; want a new one", got, err)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "could not be stored") {
		t.Errorf("warnings %q", warned)
	}
}

func TestNoStoredTokenIsAnAuthError(t *testing.T) {
	f := newFixture(t, 0)
	ts := f.source(fileStore(f.dir))
	_, err := ts.Token(context.Background())
	if c, _ := gapi.ClassOf(err); c != gapi.ClassAuth || !strings.Contains(err.Error(), "gitlab-mcp login") {
		t.Errorf("err = %v", err)
	}
	if _, err := (NoCredentials{}).Token(context.Background()); err == nil || !strings.HasPrefix(err.Error(), "[auth]") {
		t.Errorf("NoCredentials: %v", err)
	}
}

func TestARefreshFromAnEnvironmentTokenIsStoredAsItsSuccessor(t *testing.T) {
	f := newFixture(t, 300*time.Second)
	seed := signIn(t, f.app, "api").Token.RefreshToken
	env := func(k string) string {
		if k == credentials.EnvVar {
			return seed
		}
		return ""
	}
	store := &credentials.Store{Profile: "default", FilePath: filepath.Join(f.dir, "token.json"), Env: env}
	ts := f.source(store)
	first, err := ts.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// A restart with the same variable uses the stored successor rather
	// than the spent seed.
	restarted := f.source(&credentials.Store{Profile: "default", FilePath: filepath.Join(f.dir, "token.json"), Env: env})
	second, err := restarted.Token(context.Background())
	if err != nil || second != first {
		t.Errorf("after a restart: %q, %v; want %q", second, err, first)
	}
}

func TestTheLockWaitIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "refresh.lock")
	release, err := lockFile(context.Background(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockFile(context.Background(), path, 50*time.Millisecond); !errors.Is(err, ErrLockTimeout) {
		t.Errorf("while held: err = %v, want ErrLockTimeout", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := lockFile(ctx, path, time.Second); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: err = %v", err)
	}
	release()
	again, err := lockFile(context.Background(), path, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	again()
}

// TestARejectedTokenIsDroppedAndTheStoreReread: after a logout and a
// login in another process, the access token held here is revoked
// while its expiry says it is fresh. The 401 it earns drops it, the
// next token comes from the store, and a read is tried once more.
func TestARejectedTokenIsDroppedAndTheStoreReread(t *testing.T) {
	f := newFixture(t, time.Hour)
	old := f.signedIn(t)
	ts := f.source(fileStore(f.dir))
	ctx := context.Background()
	if _, err := ts.Token(ctx); err != nil {
		t.Fatal(err)
	}
	f.srv.Revoke(old.AccessToken)
	f.srv.Revoke(old.RefreshToken)
	relogin := f.signedIn(t)

	c, err := gapi.New(gapi.Options{Instance: f.app.Instance, Tokens: ts, MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetCurrentUser(ctx); err != nil {
		t.Fatalf("a read after a login elsewhere: %v", err)
	}
	reqs := f.srv.Requests()
	if len(reqs) < 2 || reqs[len(reqs)-2].Authorization != "Bearer "+old.AccessToken ||
		reqs[len(reqs)-1].Authorization != "Bearer "+relogin.AccessToken {
		t.Errorf("requests = %+v, want the old token refused and the new one used once", reqs)
	}
	if f.srv.Refreshes() != 0 {
		t.Errorf("%d refreshes, want none: the store held a fresh pair", f.srv.Refreshes())
	}
}

// TestARejectedTokenWithNoSuccessorAsksForLogin: a revoked pair still
// in the store is refreshed once, refused, and the call says to log in.
// Nothing loops.
func TestARejectedTokenWithNoSuccessorAsksForLogin(t *testing.T) {
	f := newFixture(t, time.Hour)
	old := f.signedIn(t)
	ts := f.source(fileStore(f.dir))
	f.srv.Revoke(old.AccessToken)
	f.srv.Revoke(old.RefreshToken)

	c, err := gapi.New(gapi.Options{Instance: f.app.Instance, Tokens: ts, MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	before := len(f.srv.Requests())
	_, err = c.GetCurrentUser(context.Background())
	if class, _ := gapi.ClassOf(err); class != gapi.ClassAuth || !strings.Contains(err.Error(), "gitlab-mcp login") {
		t.Fatalf("err = %v, want [auth] asking for login", err)
	}
	// One read refused, one refresh refused, nothing more.
	if n := len(f.srv.Requests()) - before; n != 2 {
		t.Errorf("%d requests, want 2: %+v", n, f.srv.Requests()[before:])
	}
}

// TestARejectedCreateIsNotRepeated: a create refused with 401 is not
// sent again, but the next call uses the token from the store.
func TestARejectedCreateIsNotRepeated(t *testing.T) {
	f := newFixture(t, time.Hour)
	old := f.signedIn(t)
	ts := f.source(fileStore(f.dir))
	ctx := context.Background()
	if _, err := ts.Token(ctx); err != nil {
		t.Fatal(err)
	}
	f.srv.Revoke(old.AccessToken)
	relogin := f.signedIn(t)

	c, err := gapi.New(gapi.Options{Instance: f.app.Instance, Tokens: ts, MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	before := len(f.srv.Requests())
	err = c.Do(ctx, gapi.Call{Method: "POST", Path: "projects/{}/issues/{}/notes", Args: []string{"2001", "1"},
		Body: map[string]string{"body": "x"}, Name: "add_comment"}, nil)
	if class, _ := gapi.ClassOf(err); class != gapi.ClassAuth {
		t.Fatalf("err = %v, want [auth]", err)
	}
	if n := len(f.srv.Requests()) - before; n != 1 {
		t.Fatalf("%d requests, want the create sent once", n)
	}
	if _, err := c.GetCurrentUser(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.srv.Requests()[len(f.srv.Requests())-1].Authorization; got != "Bearer "+relogin.AccessToken {
		t.Errorf("next call sent %q, want the new token", got)
	}
}
