package fileperm_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mmedum/gitlab-mcp/v2/internal/fileperm"
)

// TestRestrictToOwnerNarrowsAWideFile is the case the Unix path exists
// for: a file an earlier version, a umask or a restore left readable.
func TestRestrictToOwnerNarrowsAWideFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fileperm.RestrictToOwner(path); err != nil {
		t.Fatalf("RestrictToOwner: %v", err)
	}
	if runtime.GOOS == "windows" {
		// A mode means nothing here; TestRestrictToOwnerSetsAProtectedACL
		// reads the access list instead.
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		t.Fatalf("mode %o is still readable by others", mode)
	}
}

func TestRestrictToOwnerReportsAMissingFile(t *testing.T) {
	err := fileperm.RestrictToOwner(filepath.Join(t.TempDir(), "nothing-here.json"))
	if err == nil {
		t.Fatal("restricting a file that does not exist reported success")
	}
	if !strings.Contains(err.Error(), "fileperm:") {
		t.Fatalf("the error does not say which layer failed: %v", err)
	}
}

// TestDescribeNamesTheRealMechanism: the warnings quote this, and the
// whole defect was a warning naming a protection the platform did not
// provide.
func TestDescribeNamesTheRealMechanism(t *testing.T) {
	got := fileperm.Describe()
	want := "mode 0600"
	if runtime.GOOS == "windows" {
		want = "ACL"
	}
	if !strings.Contains(got, want) {
		t.Fatalf("Describe() = %q, which does not name %q", got, want)
	}
}

func TestWriteFileReplacesAndRestricts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	path := filepath.Join(dir, "state.json")
	for _, body := range []string{"first", "second"} {
		if err := fileperm.WriteFile(path, []byte(body)); err != nil {
			t.Fatalf("WriteFile(%q): %v", body, err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != body {
			t.Fatalf("file holds %q, want %q", got, body)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("the directory holds %d entries; a temporary file was left behind", len(entries))
	}
	if runtime.GOOS == "windows" {
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode %o, want 600", mode)
	}
}

func TestWriteFileReportsAnUnwritableDirectory(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// A regular file where the directory should be.
	if err := fileperm.WriteFile(filepath.Join(parent, "state.json"), []byte("x")); err == nil {
		t.Fatal("WriteFile succeeded under a regular file")
	}
}

func TestWriteFileFailsWhenTheDirectoryCannotExist(t *testing.T) {
	// A regular file where the parent directory should be.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := fileperm.WriteFile(filepath.Join(blocker, "sub", "config.json"), []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "fileperm: create") {
		t.Errorf("WriteFile under a file = %v, want a create error", err)
	}
}

func TestWriteFileLeavesNoTemporaryFileWhenTheRenameFails(t *testing.T) {
	dir := t.TempDir()
	// A non-empty directory at the target path cannot be replaced by a file.
	target := filepath.Join(dir, "config.json")
	if err := os.MkdirAll(filepath.Join(target, "inside"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := fileperm.WriteFile(target, []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "fileperm: replace") {
		t.Fatalf("WriteFile over a directory = %v, want a replace error", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.json" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v after a failed write, want only the target", names)
	}
}
