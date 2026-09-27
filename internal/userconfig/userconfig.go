// Package userconfig stores non-secret, per-profile state between runs:
// which instance a profile signs in to, which OAuth application it
// uses, who signed in, where the token went and which scopes GitLab
// granted.
//
// It lives under a base directory — os.UserConfigDir()/gitlab-mcp
// unless GITLAB_MCP_CONFIG_DIR names another inside the home directory —
// and a non-default profile lives under profiles/<name>/ below it. The
// token is internal/credentials' business, not this package's.
package userconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/fileperm"
)

// AppDir is the directory name under the user's config directory.
const AppDir = "gitlab-mcp"

// DefaultProfile is the profile used when none is named.
const DefaultProfile = "default"

// ErrNotFound means the profile has no config file yet.
var ErrNotFound = errors.New("userconfig: no config file for this profile; run `gitlab-mcp login`")

// ErrOutsideHome means the config directory override resolves outside
// the home directory.
var ErrOutsideHome = errors.New("userconfig: the config directory must be inside your home directory")

// ErrInvalidProfile means a profile name does not match ProfilePattern.
var ErrInvalidProfile = errors.New("userconfig: invalid profile name")

// ProfilePattern is what a profile name may be. The name becomes a
// directory and a keyring account, so it is kept to characters that are
// safe as both on every platform.
var ProfilePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidProfile refuses a name that does not match ProfilePattern.
func ValidProfile(name string) error {
	if !ProfilePattern.MatchString(name) {
		return fmt.Errorf("%w: %q must match %s", ErrInvalidProfile, name, ProfilePattern)
	}
	return nil
}

// Config is the stored, non-secret profile state.
type Config struct {
	// Instance is the normalized base URL the profile signed in to:
	// gitlab.com, or the test instance. The token is sent nowhere else.
	Instance string `json:"instance"`
	// ClientID is the OAuth application id. Not a secret: the
	// application is public and has none.
	ClientID string `json:"client_id"`
	// Username is who signed in, read from /user after login.
	Username string `json:"username,omitempty"`
	// TokenStore is where login put the token: keyring or file.
	TokenStore string `json:"token_store,omitempty"`
	// Scopes is what GitLab granted at the last login, not what was
	// requested. A scope requested and refused is the failure worth
	// seeing, and storing the request would hide it.
	Scopes    []string  `json:"scopes,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// userConfigDir and userHomeDir are the os functions, replaceable in
// tests.
var (
	userConfigDir = os.UserConfigDir
	userHomeDir   = os.UserHomeDir
)

// BaseDir resolves the application directory. An empty override means
// the platform default.
//
// An override is refused outside the home directory, compared by real
// path, unless allowOutsideHome is set. This package creates the
// directory 0700 and writes 0600 files into it, so a mistyped value
// such as /etc or the SSH directory would re-permission something that
// is not ours.
func BaseDir(override string, allowOutsideHome bool) (string, error) {
	override = strings.TrimSpace(override)
	if override == "" {
		d, err := userConfigDir()
		if err != nil {
			return "", fmt.Errorf("userconfig: locate the user config directory: %w", err)
		}
		return filepath.Join(d, AppDir), nil
	}
	abs, err := filepath.Abs(override)
	if err != nil {
		return "", fmt.Errorf("userconfig: resolve %q: %w", override, err)
	}
	if allowOutsideHome {
		return abs, nil
	}
	home, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("%w: the home directory cannot be found (%w)", ErrOutsideHome, err)
	}
	if !withinDir(realPath(home), realPath(abs)) {
		return "", fmt.Errorf("%w: %q", ErrOutsideHome, override)
	}
	return abs, nil
}

// realPath resolves links as far as the file system can, so that two
// names for one directory compare equal: macOS temporary paths under
// /var are /private/var, and Windows hands out 8.3 short names. The
// directory usually does not exist yet, so the deepest existing
// ancestor is resolved and the rest appended.
func realPath(path string) string {
	rest := ""
	for cur := path; ; {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return path
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// withinDir reports whether path is dir or sits under it.
func withinDir(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Dir is one resolved base directory. Every path this package hands out
// is under it.
type Dir string

// Profile returns the directory holding one profile's files.
func (d Dir) Profile(profile string) string {
	if profile == "" || profile == DefaultProfile {
		return string(d)
	}
	return filepath.Join(string(d), "profiles", profile)
}

// ConfigPath is the profile's config file.
func (d Dir) ConfigPath(profile string) string {
	return filepath.Join(d.Profile(profile), "config.json")
}

// TokenFilePath is the plaintext fallback for the token.
func (d Dir) TokenFilePath(profile string) string {
	return filepath.Join(d.Profile(profile), "token.json")
}

// LockPath is the file the token refresh locks, one per profile, so two
// processes sharing a profile never spend one refresh token twice.
func (d Dir) LockPath(profile string) string {
	return filepath.Join(d.Profile(profile), "refresh.lock")
}

// Load reads the profile's config. ErrNotFound if absent.
func (d Dir) Load(profile string) (Config, error) {
	if err := ValidProfile(profile); err != nil {
		return Config{}, err
	}
	var c Config
	if err := readJSON(d.ConfigPath(profile), &c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Save writes the profile's config, keeping CreatedAt from the first
// save and stamping UpdatedAt.
func (d Dir) Save(profile string, c Config) error {
	if err := ValidProfile(profile); err != nil {
		return err
	}
	now := time.Now().UTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
		if prev, err := d.Load(profile); err == nil && !prev.CreatedAt.IsZero() {
			c.CreatedAt = prev.CreatedAt
		}
	}
	c.UpdatedAt = now
	// The file names a person and an instance, so it is restricted like
	// the token.
	return writeJSON(d.ConfigPath(profile), c)
}

// Remove deletes the profile's config file. A missing file is fine.
func (d Dir) Remove(profile string) error {
	if err := ValidProfile(profile); err != nil {
		return err
	}
	if err := os.Remove(d.ConfigPath(profile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("userconfig: remove %s: %w", d.ConfigPath(profile), err)
	}
	return nil
}

// Resolve picks the profile to use: the one named, else DefaultProfile.
func (d Dir) Resolve(named string) (string, error) {
	if named = strings.TrimSpace(named); named != "" {
		return named, ValidProfile(named)
	}
	return DefaultProfile, nil
}

// Profiles lists every configured profile, sorted, the default
// included.
func (d Dir) Profiles() ([]string, error) {
	var out []string
	if _, err := os.Stat(d.ConfigPath(DefaultProfile)); err == nil {
		out = append(out, DefaultProfile)
	}
	entries, err := os.ReadDir(filepath.Join(string(d), "profiles"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return nil, fmt.Errorf("userconfig: list profiles: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() || ValidProfile(e.Name()) != nil {
			continue
		}
		if _, err := os.Stat(d.ConfigPath(e.Name())); err == nil {
			out = append(out, e.Name())
		}
	}
	slices.Sort(out)
	return out, nil
}

// SharingClient returns the other profiles that use this profile's OAuth
// application on the same instance. logout names them, because the
// usual next step after logging out — deleting or renewing the
// application in GitLab — signs every one of them out too.
func (d Dir) SharingClient(profile string) ([]string, error) {
	mine, err := d.Load(profile)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(mine.ClientID) == "" {
		return nil, nil
	}
	names, err := d.Profiles()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, name := range names {
		if name == profile {
			continue
		}
		other, err := d.Load(name)
		if err == nil && other.ClientID == mine.ClientID && other.Instance == mine.Instance {
			out = append(out, name)
		}
	}
	return out, nil
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path) //nolint:gosec // a path composed here from a validated profile name
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("userconfig: read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("userconfig: parse %s: %w", path, err)
	}
	return nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("userconfig: encode %s: %w", path, err)
	}
	if err := fileperm.WriteFile(path, append(data, '\n')); err != nil {
		return fmt.Errorf("userconfig: %w", err)
	}
	return nil
}
