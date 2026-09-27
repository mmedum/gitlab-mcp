// Package credentials stores and resolves the OAuth token of one
// profile: access token, refresh token, expiry and type.
//
// Resolution order:
//  1. GITLAB_MCP_REFRESH_TOKEN in the environment (CI, automation).
//  2. The OS keyring — Secret Service on Linux, Keychain on macOS,
//     Credential Manager on Windows — keyed by the profile name.
//  3. A file under the profile directory, restricted to the current
//     account (0600, or an ACL on Windows), written only when the
//     keyring was unavailable, and warned about on every use.
//
// A missing keyring entry falls through quietly. A broken keyring (no
// session bus, no secret service) also falls through, so a headless
// machine still works, and its error is reported if nothing else is
// found.
//
// GitLab rotates the refresh token on every refresh and revokes the old
// one at once (§2.3), so a refresh token from the environment works
// exactly once. The pair it rotates into is saved to the keyring or the
// file stamped with a hash of the variable's value, and while the
// variable still holds that value the stored descendant is used in its
// place. Without that, the second start after a refresh would present a
// spent token.
package credentials

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"

	"github.com/mmedum/gitlab-mcp/internal/fileperm"
)

// ServiceName is the keyring service identifier.
const ServiceName = "gitlab-mcp"

// EnvVar is the environment override.
const EnvVar = "GITLAB_MCP_REFRESH_TOKEN"

// Source identifies where a token came from.
type Source string

// Source values.
const (
	SourceEnv     Source = "env"
	SourceKeyring Source = "keyring"
	SourceFile    Source = "file"
)

// ErrNotFound means no token is stored anywhere.
var ErrNotFound = errors.New("credentials: no token found; run `gitlab-mcp login`")

// ErrKeyringSilent is what a keyring that answers "nothing" looks like
// when the profile says a token was stored in it.
//
// The two are not the same and the advice differs. A missing token is
// fixed by logging in. A keyring that has the token and will not hand it
// over — locked, or a session bus that cannot reach the daemon — is
// fixed by unlocking it, and logging in again writes a second token
// beside the first without addressing the cause.
var ErrKeyringSilent = errors.New("credentials: the profile records a token in the OS keyring and the " +
	"keyring returned nothing. It is more likely locked or unreachable than empty: unlock it and try again. " +
	"`gitlab-mcp login` writes a fresh token, which works around this rather than fixing it")

// Backend is the keyring contract. Tests substitute an in-memory one.
type Backend interface {
	Get(service, account string) (string, error)
	Set(service, account, secret string) error
	Delete(service, account string) error
}

type osKeyring struct{}

func (osKeyring) Get(service, account string) (string, error) { return keyring.Get(service, account) }
func (osKeyring) Set(service, account, secret string) error {
	return keyring.Set(service, account, secret)
}
func (osKeyring) Delete(service, account string) error { return keyring.Delete(service, account) }

// OSKeyring returns the production keyring backend.
func OSKeyring() Backend { return osKeyring{} }

// isKeyringNotFound reports whether err is the keyring's "no entry".
func isKeyringNotFound(err error) bool { return errors.Is(err, keyring.ErrNotFound) }

// Store resolves and saves the token for one profile.
type Store struct {
	Profile  string
	Keyring  Backend
	FilePath string
	Env      func(string) string
	// Warn receives human-readable warnings; the plaintext fallback is
	// announced through it on every use rather than once at login.
	Warn func(string)
	// ExpectKeyring says the profile records that a token was saved to
	// the keyring. It changes only the error: with it, a keyring that
	// answers "nothing" is reported as a keyring that will not answer
	// rather than as a token that was never there.
	ExpectKeyring bool

	// lastSaved keeps this store's saves in order where the clock cannot:
	// on Windows two saves can read the same time, and the pair saved
	// last must still compare later.
	lastSaved time.Time
}

// record is what the keyring entry and the file hold.
type record struct {
	AccessToken  string    `json:"access_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry,omitzero"`
	// EnvSeed is the hash of the environment refresh token this pair
	// was rotated from, if it was.
	EnvSeed string    `json:"env_seed,omitempty"`
	SavedAt time.Time `json:"saved_at"`
}

func (r record) token() *oauth2.Token {
	return &oauth2.Token{
		AccessToken:  r.AccessToken,
		TokenType:    r.TokenType,
		RefreshToken: r.RefreshToken,
		Expiry:       r.Expiry,
	}
}

// fileProtection says what the file fallback's permissions actually
// achieve on this platform, so a warning never names a protection the
// platform does not provide.
func fileProtection() string { return fileperm.Describe() }

func (s *Store) warn(msg string) {
	if s.Warn != nil {
		s.Warn(msg)
	}
}

func (s *Store) env(k string) string {
	if s.Env != nil {
		return strings.TrimSpace(s.Env(k))
	}
	return strings.TrimSpace(os.Getenv(k))
}

// seed is the stamp a pair rotated from the environment token carries.
func seed(refresh string) string {
	sum := sha256.Sum256([]byte(refresh))
	return hex.EncodeToString(sum[:])
}

// Resolve returns the token and where it came from.
//
// The environment wins, except over a stored pair rotated from the very
// value it holds: that pair is the environment token's live successor.
// A token from the environment carries only the refresh token, so the
// first call refreshes it.
func (s *Store) Resolve() (*oauth2.Token, Source, error) {
	v := s.env(EnvVar)
	if v == "" {
		return s.ResolveStored()
	}
	if r, src, err := s.readStored(); err == nil && r.EnvSeed == seed(v) {
		s.warnIfFile(src)
		return r.token(), src, nil
	}
	return &oauth2.Token{RefreshToken: v}, SourceEnv, nil
}

// ResolveStored returns the token from the keyring or the file, ignoring
// the environment override: it is the token logout can revoke and delete.
func (s *Store) ResolveStored() (*oauth2.Token, Source, error) {
	r, src, err := s.readStored()
	if err != nil {
		return nil, "", err
	}
	s.warnIfFile(src)
	return r.token(), src, nil
}

// warnIfFile warns on every use of the plaintext file, not once at
// login, when the person has long forgotten it.
func (s *Store) warnIfFile(src Source) {
	if src == SourceFile {
		s.warn(fmt.Sprintf("token read from the plaintext file %s (%s); "+
			"an OS keyring would hold it better", s.FilePath, fileProtection()))
	}
}

// readStored reads the keyring entry and the file, once each, and
// returns the pair saved last. Both exist only when a save failed half
// way: a keyring save that could not remove the file (the file is
// older), or a keyring that refused a save and would not delete its old
// entry either (the file is newer). A pair that does not decode loses to
// one that does, and is reported only when there is no other.
func (s *Store) readStored() (record, Source, error) {
	kr, krOK, krErr := s.readKeyring()
	f, fOK, fErr := s.readFile()
	switch {
	case krOK && fOK && f.SavedAt.After(kr.SavedAt):
		return f, SourceFile, nil
	case krOK:
		return kr, SourceKeyring, nil
	case fOK:
		return f, SourceFile, nil
	}
	// Nothing usable. A store that holds something broken says so first;
	// a keyring that could not be asked is not a missing token.
	var decodeErr *decodeError
	switch {
	case errors.As(krErr, &decodeErr):
		return record{}, "", krErr
	case fErr != nil:
		return record{}, "", fErr
	case krErr != nil:
		return record{}, "", fmt.Errorf("%w (keyring error: %w)", ErrNotFound, krErr)
	case s.ExpectKeyring:
		return record{}, "", ErrKeyringSilent
	}
	return record{}, "", ErrNotFound
}

// readKeyring reads the keyring entry. An entry that is absent is
// neither a pair nor an error.
func (s *Store) readKeyring() (record, bool, error) {
	if s.Keyring == nil {
		return record{}, false, nil
	}
	raw, err := s.Keyring.Get(ServiceName, s.Profile)
	switch {
	case err != nil && isKeyringNotFound(err), err == nil && raw == "":
		return record{}, false, nil
	case err != nil:
		return record{}, false, err
	}
	r, err := decode([]byte(raw), "the keyring entry")
	return r, err == nil, err
}

// readFile reads the file. A file that does not exist is neither a pair
// nor an error.
func (s *Store) readFile() (record, bool, error) {
	if s.FilePath == "" {
		return record{}, false, nil
	}
	data, err := os.ReadFile(s.FilePath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return record{}, false, nil
	case err != nil:
		return record{}, false, fmt.Errorf("credentials: read %s: %w", s.FilePath, err)
	}
	r, err := decode(data, s.FilePath)
	return r, err == nil, err
}

// decodeError is a stored pair that is not one. Its text never quotes
// the input, which is a token.
type decodeError struct{ msg string }

func (e *decodeError) Error() string { return e.msg }

func decode(data []byte, where string) (record, error) {
	var r record
	if err := json.Unmarshal(data, &r); err != nil {
		// The parse error can quote the input, which is a token.
		return record{}, &decodeError{"credentials: " + where + " is not a stored token"}
	}
	if r.RefreshToken == "" {
		return record{}, &decodeError{"credentials: " + where + " holds no refresh token"}
	}
	return r, nil
}

// Save stores the token in the keyring, or in the file when the keyring
// fails, and reports where it landed. A successful keyring save removes
// a stale plaintext copy, so there is one source of truth.
//
// While the environment override is set, the saved pair is stamped as
// its successor (see Resolve); a pair saved without it is not.
func (s *Store) Save(tok *oauth2.Token) (Source, error) {
	if tok == nil || tok.RefreshToken == "" {
		return "", errors.New("credentials: refusing to store a token with no refresh token")
	}
	r := record{
		AccessToken:  tok.AccessToken,
		TokenType:    tok.TokenType,
		RefreshToken: tok.RefreshToken,
		Expiry:       tok.Expiry.UTC(),
		SavedAt:      time.Now().UTC(),
	}
	if !r.SavedAt.After(s.lastSaved) {
		r.SavedAt = s.lastSaved.Add(time.Microsecond)
	}
	s.lastSaved = r.SavedAt
	if v := s.env(EnvVar); v != "" {
		r.EnvSeed = seed(v)
	}
	// Storing the token is what this function is for.
	data, err := json.Marshal(r) //nolint:gosec // the token record, by design
	if err != nil {
		return "", fmt.Errorf("credentials: encode token: %w", err)
	}

	var keyringErr error
	if s.Keyring != nil {
		err := s.Keyring.Set(ServiceName, s.Profile, string(data))
		if err == nil {
			if err := s.removeFile(); err != nil {
				s.warn(fmt.Sprintf("token saved to the keyring, and a stale plaintext copy remains: %v", err))
			}
			return SourceKeyring, nil
		}
		keyringErr = err
	}
	if s.FilePath == "" {
		if keyringErr != nil {
			return "", fmt.Errorf("credentials: keyring unavailable and no file fallback configured: %w", keyringErr)
		}
		return "", errors.New("credentials: no token store configured")
	}
	if err := fileperm.WriteFile(s.FilePath, data); err != nil {
		return "", fmt.Errorf("credentials: %w", err)
	}
	if keyringErr != nil {
		// An older pair left in the keyring would be read before the file.
		// If it cannot be removed either, readStored still prefers the
		// file, being saved later.
		if err := s.Keyring.Delete(ServiceName, s.Profile); err != nil && !isKeyringNotFound(err) {
			s.warn(fmt.Sprintf("an older token remains in the keyring and could not be removed: %v", err))
		}
		s.warn(fmt.Sprintf("keyring unavailable (%v); token saved in plaintext at %s (%s)",
			keyringErr, s.FilePath, fileProtection()))
	} else {
		s.warn(fmt.Sprintf("no keyring configured; token saved in plaintext at %s (%s)",
			s.FilePath, fileProtection()))
	}
	return SourceFile, nil
}

// Delete removes the token from every store and reports every failure.
// Missing entries are fine. The environment is not a store.
func (s *Store) Delete() error {
	var errs []error
	if s.Keyring != nil {
		if err := s.Keyring.Delete(ServiceName, s.Profile); err != nil && !isKeyringNotFound(err) {
			errs = append(errs, fmt.Errorf("credentials: keyring delete: %w", err))
		}
	}
	if err := s.removeFile(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (s *Store) removeFile() error {
	if s.FilePath == "" {
		return nil
	}
	if err := os.Remove(s.FilePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("credentials: remove %s: %w", s.FilePath, err)
	}
	return nil
}
