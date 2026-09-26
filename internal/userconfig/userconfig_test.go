package userconfig

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	prevHome, prevCfg := userHomeDir, userConfigDir
	userHomeDir = func() (string, error) { return home, nil }
	userConfigDir = func() (string, error) { return filepath.Join(home, ".config"), nil }
	t.Cleanup(func() { userHomeDir, userConfigDir = prevHome, prevCfg })
	return home
}

func TestBaseDirDefault(t *testing.T) {
	home := fakeHome(t)
	got, err := BaseDir("", false)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".config", "gitlab-mcp"); got != want {
		t.Errorf("BaseDir = %q, want %q", got, want)
	}
}

func TestBaseDirOverrideInsideHome(t *testing.T) {
	home := fakeHome(t)
	want := filepath.Join(home, "not", "yet", "created")
	got, err := BaseDir(want, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("BaseDir = %q, want %q", got, want)
	}
}

func TestBaseDirRefusesOutsideHome(t *testing.T) {
	home := fakeHome(t)
	for _, v := range []string{t.TempDir(), filepath.Join(home, "..", "elsewhere")} {
		if _, err := BaseDir(v, false); !errors.Is(err, ErrOutsideHome) {
			t.Errorf("BaseDir(%q) = %v, want ErrOutsideHome", v, err)
		}
	}
}

func TestBaseDirAllowsOutsideHomeWhenAsked(t *testing.T) {
	fakeHome(t)
	want := t.TempDir()
	got, err := BaseDir(want, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("BaseDir = %q, want %q", got, want)
	}
}

// A link inside home pointing outside it is outside home.
func TestBaseDirRefusesALinkOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	home := fakeHome(t)
	link := filepath.Join(home, "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if _, err := BaseDir(filepath.Join(link, "cfg"), false); !errors.Is(err, ErrOutsideHome) {
		t.Errorf("a link out of home was accepted: %v", err)
	}
}

func TestBaseDirErrors(t *testing.T) {
	prevHome, prevCfg := userHomeDir, userConfigDir
	t.Cleanup(func() { userHomeDir, userConfigDir = prevHome, prevCfg })
	userHomeDir = func() (string, error) { return "", errors.New("no home") }
	userConfigDir = func() (string, error) { return "", errors.New("no config dir") }
	if _, err := BaseDir("", false); err == nil {
		t.Error("no config dir was not reported")
	}
	if _, err := BaseDir("/x", false); !errors.Is(err, ErrOutsideHome) {
		t.Errorf("no home: %v", err)
	}
}

func TestValidProfile(t *testing.T) {
	good := []string{"default", "work", "a", "gitlab-com_2", "0abc"}
	bad := []string{"", "Work", "-lead", "_lead", "a/b", "..", "a b", string(make([]byte, 65))}
	for _, n := range good {
		if err := ValidProfile(n); err != nil {
			t.Errorf("ValidProfile(%q) = %v", n, err)
		}
	}
	for _, n := range bad {
		if err := ValidProfile(n); !errors.Is(err, ErrInvalidProfile) {
			t.Errorf("ValidProfile(%q) = %v, want ErrInvalidProfile", n, err)
		}
	}
}

func TestPaths(t *testing.T) {
	d := Dir(filepath.FromSlash("/base"))
	tests := map[string]string{
		d.Profile(""):              "/base",
		d.Profile("default"):       "/base",
		d.Profile("work"):          "/base/profiles/work",
		d.ConfigPath("work"):       "/base/profiles/work/config.json",
		d.TokenFilePath("default"): "/base/token.json",
		d.LockPath("work"):         "/base/profiles/work/refresh.lock",
	}
	for got, want := range tests {
		if got != filepath.FromSlash(want) {
			t.Errorf("path = %q, want %q", got, want)
		}
	}
}

func TestSaveLoadRemove(t *testing.T) {
	d := Dir(t.TempDir())
	if _, err := d.Load("work"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load before Save = %v, want ErrNotFound", err)
	}
	c := Config{
		Instance: "https://gitlab.example.com",
		ClientID: "app-id-1",
		Username: "alice",
		Scopes:   []string{"api"},
	}
	if err := d.Save("work", c); err != nil {
		t.Fatal(err)
	}
	got, err := d.Load("work")
	if err != nil {
		t.Fatal(err)
	}
	if got.Instance != c.Instance || got.ClientID != c.ClientID || got.Username != "alice" || !slices.Equal(got.Scopes, []string{"api"}) {
		t.Errorf("Load = %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("timestamps not set: %+v", got)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(d.ConfigPath("work"))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("config mode %o, want 600", fi.Mode().Perm())
		}
	}

	// A later save keeps the creation time.
	created := got.CreatedAt
	time.Sleep(2 * time.Millisecond)
	got.Username = "bob"
	got.CreatedAt = time.Time{}
	if err := d.Save("work", got); err != nil {
		t.Fatal(err)
	}
	again, err := d.Load("work")
	if err != nil {
		t.Fatal(err)
	}
	if !again.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt moved from %v to %v", created, again.CreatedAt)
	}
	if !again.UpdatedAt.After(created) {
		t.Errorf("UpdatedAt %v not after %v", again.UpdatedAt, created)
	}

	if err := d.Remove("work"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Load("work"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Load after Remove = %v", err)
	}
	if err := d.Remove("work"); err != nil {
		t.Errorf("a second Remove failed: %v", err)
	}
}

func TestInvalidProfileRefusedEverywhere(t *testing.T) {
	d := Dir(t.TempDir())
	const bad = "../escape"
	if _, err := d.Load(bad); !errors.Is(err, ErrInvalidProfile) {
		t.Errorf("Load: %v", err)
	}
	if err := d.Save(bad, Config{}); !errors.Is(err, ErrInvalidProfile) {
		t.Errorf("Save: %v", err)
	}
	if err := d.Remove(bad); !errors.Is(err, ErrInvalidProfile) {
		t.Errorf("Remove: %v", err)
	}
	if _, err := d.Resolve(bad); !errors.Is(err, ErrInvalidProfile) {
		t.Errorf("Resolve: %v", err)
	}
}

func TestLoadRejectsBadJSON(t *testing.T) {
	d := Dir(t.TempDir())
	if err := os.WriteFile(d.ConfigPath("default"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Load("default"); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("Load of bad JSON = %v", err)
	}
}

func TestResolveNamesTheProfileOrDefault(t *testing.T) {
	d := Dir(t.TempDir())
	if got, err := d.Resolve(""); err != nil || got != "default" {
		t.Fatalf("Resolve(\"\") = %q, %v", got, err)
	}
	if got, err := d.Resolve(" work "); err != nil || got != "work" {
		t.Fatalf("Resolve(work) = %q, %v", got, err)
	}
	// A profile that exists is never picked unless it is named.
	if err := d.Save("work", Config{Instance: "https://gitlab.example.com"}); err != nil {
		t.Fatal(err)
	}
	if got, err := d.Resolve(""); err != nil || got != "default" {
		t.Fatalf("Resolve(\"\") after saving work = %q, %v", got, err)
	}
}

func TestProfilesAndSharingClient(t *testing.T) {
	d := Dir(t.TempDir())
	const inst, other = "https://gitlab.example.com", "https://gitlab.com"
	for name, c := range map[string]Config{
		"default":   {Instance: inst, ClientID: "app-1"},
		"work":      {Instance: inst, ClientID: "app-1"},
		"public":    {Instance: other, ClientID: "app-1"},
		"separate":  {Instance: inst, ClientID: "app-2"},
		"unclaimed": {Instance: inst},
	} {
		if err := d.Save(name, c); err != nil {
			t.Fatal(err)
		}
	}
	// A directory with no config is not a profile.
	if err := os.MkdirAll(filepath.Join(string(d), "profiles", "empty"), 0o700); err != nil {
		t.Fatal(err)
	}

	names, err := d.Profiles()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"default", "public", "separate", "unclaimed", "work"}; !slices.Equal(names, want) {
		t.Errorf("Profiles = %v, want %v", names, want)
	}

	tests := map[string][]string{
		"default":   {"work"},
		"work":      {"default"},
		"public":    nil,
		"separate":  nil,
		"unclaimed": nil,
	}
	for name, want := range tests {
		got, err := d.SharingClient(name)
		if err != nil {
			t.Fatalf("SharingClient(%q): %v", name, err)
		}
		if !slices.Equal(got, want) {
			t.Errorf("SharingClient(%q) = %v, want %v", name, got, want)
		}
	}
	if _, err := d.SharingClient("missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SharingClient(missing) = %v", err)
	}
}

func TestProfilesInAnEmptyDir(t *testing.T) {
	got, err := Dir(t.TempDir()).Profiles()
	if err != nil || len(got) != 0 {
		t.Errorf("Profiles = %v, %v", got, err)
	}
}
