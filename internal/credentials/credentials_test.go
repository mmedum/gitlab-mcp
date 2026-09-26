package credentials

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

// fakeKeyring is an in-memory Backend. failGet, failSet and failDelete
// make it behave like a locked or unreachable one.
type fakeKeyring struct {
	items      map[string]string
	failGet    error
	failSet    error
	failDelete error
}

func newFake() *fakeKeyring { return &fakeKeyring{items: map[string]string{}} }

func (f *fakeKeyring) Get(service, account string) (string, error) {
	if f.failGet != nil {
		return "", f.failGet
	}
	v, ok := f.items[service+"/"+account]
	if !ok {
		// The keyring's own sentinel, not a lookalike: the store tells a
		// missing entry from a broken keyring by this exact error.
		return "", keyring.ErrNotFound
	}
	return v, nil
}

func (f *fakeKeyring) Set(service, account, secret string) error {
	if f.failSet != nil {
		return f.failSet
	}
	f.items[service+"/"+account] = secret
	return nil
}

func (f *fakeKeyring) Delete(service, account string) error {
	if f.failDelete != nil {
		return f.failDelete
	}
	delete(f.items, service+"/"+account)
	return nil
}

func store(t *testing.T, kr Backend, expectKeyring bool) (*Store, *[]string) {
	t.Helper()
	var warnings []string
	s := &Store{
		Profile:       "default",
		Keyring:       kr,
		FilePath:      filepath.Join(t.TempDir(), "token.json"),
		Env:           func(string) string { return "" },
		Warn:          func(m string) { warnings = append(warnings, m) },
		ExpectKeyring: expectKeyring,
	}
	return s, &warnings
}

func envWith(v string) func(string) string {
	return func(k string) string {
		if k == "GITLAB_MCP_REFRESH_TOKEN" {
			return v
		}
		return ""
	}
}

var expiry = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

func tok(access, refresh string) *oauth2.Token {
	return &oauth2.Token{AccessToken: access, RefreshToken: refresh, TokenType: "Bearer", Expiry: expiry}
}

func sameToken(t *testing.T, got, want *oauth2.Token) {
	t.Helper()
	if got == nil {
		t.Fatal("nil token")
	}
	if got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken ||
		got.TokenType != want.TokenType || !got.Expiry.Equal(want.Expiry) {
		t.Fatalf("token = %+v, want %+v", got, want)
	}
}

func TestConstants(t *testing.T) {
	if ServiceName != "gitlab-mcp" || EnvVar != "GITLAB_MCP_REFRESH_TOKEN" {
		t.Errorf("ServiceName = %q, EnvVar = %q", ServiceName, EnvVar)
	}
}

func TestSaveAndResolveFromKeyring(t *testing.T) {
	kr := newFake()
	s, warnings := store(t, kr, false)
	src, err := s.Save(tok("access-1", "refresh-1"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if src != SourceKeyring {
		t.Fatalf("Save reported %q, want keyring", src)
	}
	if _, ok := kr.items["gitlab-mcp/default"]; !ok {
		t.Fatalf("keyring entry not under service gitlab-mcp, account default: %v", kr.items)
	}
	got, src, err := s.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if src != SourceKeyring {
		t.Fatalf("Resolve source = %q", src)
	}
	sameToken(t, got, tok("access-1", "refresh-1"))
	if len(*warnings) != 0 {
		t.Errorf("keyring use warned: %v", *warnings)
	}
}

func TestProfilesAreSeparate(t *testing.T) {
	kr := newFake()
	a, _ := store(t, kr, false)
	b, _ := store(t, kr, false)
	b.Profile = "work"
	if _, err := a.Save(tok("a", "ra")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Resolve(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("profile work resolved profile default's token: %v", err)
	}
}

func TestEnvironmentWins(t *testing.T) {
	kr := newFake()
	s, _ := store(t, kr, false)
	if _, err := s.Save(tok("stored-access", "stored-refresh")); err != nil {
		t.Fatal(err)
	}
	s.Env = envWith("env-refresh")
	got, src, err := s.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if src != SourceEnv {
		t.Fatalf("source = %q, want env", src)
	}
	sameToken(t, got, &oauth2.Token{RefreshToken: "env-refresh"})

	// ResolveStored ignores it, because that is what logout revokes.
	got, src, err = s.ResolveStored()
	if err != nil {
		t.Fatal(err)
	}
	if src != SourceKeyring {
		t.Fatalf("ResolveStored source = %q", src)
	}
	sameToken(t, got, tok("stored-access", "stored-refresh"))
}

// TestRotatedEnvironmentTokenIsFollowed: GitLab revokes a refresh token
// the moment it is used, so the pair it rotated into must win over the
// variable that still holds the spent one — and only over that value.
func TestRotatedEnvironmentTokenIsFollowed(t *testing.T) {
	kr := newFake()
	s, _ := store(t, kr, false)
	s.Env = envWith("env-refresh-1")
	if _, err := s.Save(tok("rotated-access", "rotated-refresh")); err != nil {
		t.Fatal(err)
	}
	got, src, err := s.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if src != SourceKeyring {
		t.Fatalf("source = %q, want the stored successor", src)
	}
	sameToken(t, got, tok("rotated-access", "rotated-refresh"))

	// A new value in the variable is a new token, and wins again.
	s.Env = envWith("env-refresh-2")
	got, src, err = s.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if src != SourceEnv || got.RefreshToken != "env-refresh-2" {
		t.Fatalf("Resolve = %q from %q, want the new environment value", got.RefreshToken, src)
	}
}

func TestStoredTokenWithoutSeedDoesNotOverrideEnv(t *testing.T) {
	s, _ := store(t, newFake(), false)
	if _, err := s.Save(tok("a", "r")); err != nil {
		t.Fatal(err)
	}
	s.Env = envWith("env-refresh")
	if _, src, err := s.Resolve(); err != nil || src != SourceEnv {
		t.Fatalf("Resolve source = %q, %v; a login's token must not beat the variable", src, err)
	}
}

// TestFileFallbackWarnsEveryTime: the plaintext file is a downgrade, and
// a warning at login only is one the person has forgotten by the time
// it matters.
func TestFileFallbackWarnsEveryTime(t *testing.T) {
	kr := newFake()
	kr.failSet = errors.New("no session bus")
	s, warnings := store(t, kr, false)

	src, err := s.Save(tok("a", "r"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if src != SourceFile {
		t.Fatalf("Save reported %q, want file", src)
	}
	if len(*warnings) != 1 || !strings.Contains((*warnings)[0], "session bus") {
		t.Fatalf("saving to the plaintext file warned %v", *warnings)
	}

	kr.failGet = errors.New("no session bus")
	for i := range 2 {
		before := len(*warnings)
		got, src, err := s.Resolve()
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if src != SourceFile {
			t.Fatalf("source = %q", src)
		}
		sameToken(t, got, tok("a", "r"))
		if len(*warnings) != before+1 {
			t.Fatalf("read %d of the plaintext file did not warn", i+1)
		}
	}
}

func TestNoKeyringAtAllWritesTheFileAndWarns(t *testing.T) {
	s, warnings := store(t, nil, false)
	if src, err := s.Save(tok("a", "r")); err != nil || src != SourceFile {
		t.Fatalf("Save = %q, %v", src, err)
	}
	if len(*warnings) != 1 {
		t.Fatalf("warnings = %v", *warnings)
	}
}

// TestEnvWinningDoesNotWarnAboutAnUnusedFile: the warning is about using
// the file, not about its existence.
func TestEnvWinningDoesNotWarnAboutAnUnusedFile(t *testing.T) {
	s, warnings := store(t, nil, false)
	if _, err := s.Save(tok("a", "r")); err != nil {
		t.Fatal(err)
	}
	*warnings = nil
	s.Env = envWith("env-refresh")
	if _, src, err := s.Resolve(); err != nil || src != SourceEnv {
		t.Fatalf("Resolve = %q, %v", src, err)
	}
	if len(*warnings) != 0 {
		t.Fatalf("warned about a file it did not use: %v", *warnings)
	}
}

func TestFileIsOwnerOnly(t *testing.T) {
	kr := newFake()
	kr.failSet = errors.New("unavailable")
	s, _ := store(t, kr, false)
	if _, err := s.Save(tok("a", "r")); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		// A mode means nothing there; internal/fileperm reads the access
		// list back. This holds only that the warning names the real
		// mechanism.
		if note := fileProtection(); !strings.Contains(note, "ACL") {
			t.Fatalf("fileProtection = %q", note)
		}
		return
	}
	fi, err := os.Stat(s.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("token file mode %o, want 600", mode)
	}
	if note := fileProtection(); note != "mode 0600" {
		t.Fatalf("fileProtection = %q", note)
	}
}

// TestSilentKeyringIsItsOwnError: "log in again" is the wrong advice for
// a keyring that is locked rather than empty.
func TestSilentKeyringIsItsOwnError(t *testing.T) {
	s, _ := store(t, newFake(), true)
	if _, _, err := s.Resolve(); !errors.Is(err, ErrKeyringSilent) {
		t.Fatalf("Resolve = %v, want ErrKeyringSilent when the profile expects a keyring token", err)
	}
	s2, _ := store(t, newFake(), false)
	if _, _, err := s2.Resolve(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve = %v, want ErrNotFound when nothing claims a token exists", err)
	}
}

func TestKeyringTransportFailureIsNotAMissingToken(t *testing.T) {
	kr := newFake()
	kr.failGet = errors.New("no session bus")
	s, _ := store(t, kr, true)
	_, _, err := s.Resolve()
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve = %v", err)
	}
	// It carries the keyring's own reason, or the person is told to log
	// in when the real problem is a locked keyring.
	if !strings.Contains(err.Error(), "session bus") {
		t.Fatalf("the keyring failure was swallowed: %v", err)
	}
}

func TestSaveRefusesATokenWithoutRefresh(t *testing.T) {
	s, _ := store(t, newFake(), false)
	for _, bad := range []*oauth2.Token{nil, {AccessToken: "a"}} {
		if _, err := s.Save(bad); err == nil {
			t.Errorf("Save(%+v) stored a token with no refresh token", bad)
		}
	}
}

func TestKeyringSaveDropsAStalePlaintextCopy(t *testing.T) {
	kr := newFake()
	kr.failSet = errors.New("unavailable")
	s, _ := store(t, kr, false)
	if _, err := s.Save(tok("old", "old")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.FilePath); err != nil {
		t.Fatalf("precondition: the file fallback was not written: %v", err)
	}
	kr.failSet = nil
	if _, err := s.Save(tok("new", "new")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.FilePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a stale plaintext token survived a successful keyring save")
	}
}

// TestFailedKeyringSaveDoesNotLeaveAnOlderPairInCharge: a pair that
// lands in the file because the keyring refused it must be the pair read
// back, whether or not the older keyring entry could be removed.
func TestFailedKeyringSaveDoesNotLeaveAnOlderPairInCharge(t *testing.T) {
	for _, c := range []struct {
		name       string
		failDelete error
		wantEntry  bool
	}{
		{"stale entry deleted", nil, false},
		{"stale entry undeletable", errors.New("locked"), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			kr := newFake()
			s, warnings := store(t, kr, false)
			if src, err := s.Save(tok("old", "old")); err != nil || src != SourceKeyring {
				t.Fatalf("Save old = %q, %v", src, err)
			}
			kr.failSet = errors.New("unavailable")
			kr.failDelete = c.failDelete
			if src, err := s.Save(tok("new", "new")); err != nil || src != SourceFile {
				t.Fatalf("Save new = %q, %v", src, err)
			}
			if len(*warnings) == 0 {
				t.Error("the plaintext save did not warn")
			}
			if _, ok := kr.items["gitlab-mcp/default"]; ok != c.wantEntry {
				t.Errorf("keyring entry present = %v, want %v", ok, c.wantEntry)
			}
			got, src, err := s.Resolve()
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if src != SourceFile {
				t.Errorf("source = %q, want file", src)
			}
			sameToken(t, got, tok("new", "new"))
		})
	}
}

func TestDeleteRemovesEverythingAndIsIdempotent(t *testing.T) {
	kr := newFake()
	s, _ := store(t, kr, false)
	if _, err := s.Save(tok("a", "r")); err != nil {
		t.Fatal(err)
	}
	// A plaintext copy as well, as an older login might have left.
	if err := os.WriteFile(s.FilePath, []byte(`{"refresh_token":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(kr.items) != 0 {
		t.Fatalf("Delete left the keyring entry: %v", kr.items)
	}
	if _, err := os.Stat(s.FilePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Delete left the plaintext file behind")
	}
	if err := s.Delete(); err != nil {
		t.Fatalf("Delete must be idempotent: %v", err)
	}
}

// TestDeleteJoinsErrors: a keyring that refuses must not stop the file
// being removed, and both failures are reported.
func TestDeleteJoinsErrors(t *testing.T) {
	kr := newFake()
	kr.failDelete = errors.New("keyring locked")
	s, _ := store(t, kr, false)
	// A directory where the file is makes its removal fail.
	if err := os.MkdirAll(filepath.Join(s.FilePath, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := s.Delete()
	if err == nil {
		t.Fatal("Delete reported success")
	}
	if !strings.Contains(err.Error(), "keyring locked") || !strings.Contains(err.Error(), "remove") {
		t.Fatalf("Delete = %v; want both failures", err)
	}
}

func TestNoStoreConfigured(t *testing.T) {
	s := &Store{Profile: "default", Env: func(string) string { return "" }}
	if _, err := s.Save(tok("a", "r")); err == nil {
		t.Fatal("Save succeeded with no keyring and no file path")
	}
	if _, _, err := s.Resolve(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve = %v, want ErrNotFound", err)
	}
	kr := newFake()
	kr.failSet = errors.New("no session bus")
	s.Keyring = kr
	if _, err := s.Save(tok("a", "r")); err == nil || !strings.Contains(err.Error(), "session bus") {
		t.Fatalf("Save = %v", err)
	}
}

// TestCorruptStoresAreReportedWithoutTheirContent: a parse error can
// quote its input, and the input is a token.
func TestCorruptStoresAreReportedWithoutTheirContent(t *testing.T) {
	const secret = "not-json-refresh-secret"
	kr := newFake()
	kr.items["gitlab-mcp/default"] = "{" + secret
	s, _ := store(t, kr, false)
	_, _, err := s.Resolve()
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("keyring: Resolve = %v", err)
	}

	s2, _ := store(t, newFake(), false)
	if err := os.WriteFile(s2.FilePath, []byte("{"+secret), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = s2.Resolve()
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("file: Resolve = %v", err)
	}

	kr3 := newFake()
	kr3.items["gitlab-mcp/default"] = `{"access_token":"a"}`
	s3, _ := store(t, kr3, false)
	if _, _, err := s3.Resolve(); err == nil {
		t.Fatal("a record with no refresh token resolved")
	}
}

// TestStoreThroughTheMockKeyring runs the store over the production
// backend, backed by go-keyring's in-memory mock.
func TestStoreThroughTheMockKeyring(t *testing.T) {
	useMockKeyring(t)
	s, _ := store(t, OSKeyring(), false)
	if src, err := s.Save(tok("a", "r")); err != nil || src != SourceKeyring {
		t.Fatalf("Save = %q, %v", src, err)
	}
	got, _, err := s.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	sameToken(t, got, tok("a", "r"))
	if err := s.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Resolve(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve after Delete = %v", err)
	}
}

// TestReadStoredTakesThePairSavedLast: with both stores holding a pair,
// the one saved last is live, and a pair that does not decode loses to
// one that does, whichever store holds it.
func TestReadStoredTakesThePairSavedLast(t *testing.T) {
	older := `{"refresh_token":"old","saved_at":"2026-09-01T00:00:00Z"}`
	newer := `{"refresh_token":"new","saved_at":"2026-09-02T00:00:00Z"}`
	for _, c := range []struct {
		name, keyring, file string
		wantRefresh         string
		wantSource          Source
	}{
		{"keyring newer", newer, older, "new", SourceKeyring},
		{"file newer", older, newer, "new", SourceFile},
		{"same time: keyring", `{"refresh_token":"k","saved_at":"2026-09-01T00:00:00Z"}`, older, "k", SourceKeyring},
		{"broken keyring, good file", "{not json", older, "old", SourceFile},
		{"good keyring, broken file", older, "{not json", "old", SourceKeyring},
		{"keyring without refresh, good file", `{"access_token":"a"}`, older, "old", SourceFile},
	} {
		t.Run(c.name, func(t *testing.T) {
			kr := newFake()
			kr.items["gitlab-mcp/default"] = c.keyring
			s, _ := store(t, kr, false)
			if err := os.WriteFile(s.FilePath, []byte(c.file), 0o600); err != nil {
				t.Fatal(err)
			}
			got, src, err := s.Resolve()
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.RefreshToken != c.wantRefresh || src != c.wantSource {
				t.Errorf("Resolve = %q from %s, want %q from %s", got.RefreshToken, src, c.wantRefresh, c.wantSource)
			}
		})
	}
}
