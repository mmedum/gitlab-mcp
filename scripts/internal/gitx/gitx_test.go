package gitx

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// repo makes a throwaway repository with a fixed identity, so a test
// never depends on the machine's git configuration.
func repo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "user.email", "alice@example.com")
	git(t, dir, "config", "user.name", "alice")
	git(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := Output(dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

func write(t *testing.T, dir, name, text string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFilesAndTracked(t *testing.T) {
	dir := repo(t)
	write(t, dir, ".gitignore", "ignored.txt\n")
	write(t, dir, "a.txt", "a")
	write(t, dir, "sub/b.txt", "b")
	write(t, dir, "gone.txt", "g")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "one")
	write(t, dir, "untracked.txt", "u")
	write(t, dir, "ignored.txt", "i")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}

	files, err := Files(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{".gitignore", "a.txt", "sub/b.txt", "untracked.txt"}; !reflect.DeepEqual(files, want) {
		t.Errorf("Files = %q, want %q", files, want)
	}
	tracked, err := Tracked(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{".gitignore", "a.txt", "sub/b.txt"}; !reflect.DeepEqual(tracked, want) {
		t.Errorf("Tracked = %q, want %q", tracked, want)
	}
}

func TestMergeBaseMessagesAndTags(t *testing.T) {
	dir := repo(t)
	write(t, dir, "a.txt", "a")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	base := git(t, dir, "rev-parse", "HEAD")
	if _, err := LatestTag(dir); !errors.Is(err, ErrNoTag) {
		t.Errorf("LatestTag with no tag: err = %v, want ErrNoTag", err)
	}
	git(t, dir, "tag", "v0.1.0")

	git(t, dir, "checkout", "-q", "-b", "topic")
	write(t, dir, "b.txt", "b")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "topic work\n\nSCHEMA-CHANGE: added")
	git(t, dir, "checkout", "-q", "main")
	write(t, dir, "c.txt", "c")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "main moved")

	got, err := MergeBase(dir, "main", "topic")
	if err != nil {
		t.Fatal(err)
	}
	if got != base {
		t.Errorf("MergeBase = %s, want %s", got, base)
	}
	msgs, err := Messages(dir, got, "topic")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0], "SCHEMA-CHANGE: added") {
		t.Errorf("Messages = %q", msgs)
	}
	changed, err := ChangedFiles(dir, got, "topic")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"b.txt"}; !reflect.DeepEqual(changed, want) {
		t.Errorf("ChangedFiles = %q, want %q", changed, want)
	}
	tag, err := LatestTag(dir)
	if err != nil || tag != "v0.1.0" {
		t.Errorf("LatestTag = %q, %v; want v0.1.0", tag, err)
	}
	content, err := Show(dir, "v0.1.0", "a.txt")
	if err != nil || string(content) != "a" {
		t.Errorf("Show = %q, %v", content, err)
	}
	if _, err := Resolve(dir, "no-such-ref"); err == nil {
		t.Error("Resolve of a missing ref succeeded")
	}
	if _, err := MergeBase(dir, "main", "no-such-ref"); err == nil {
		t.Error("MergeBase of a missing ref succeeded")
	}
	root, err := Root(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(dir); root != want && root != dir {
		t.Errorf("Root = %s, want %s", root, dir)
	}
}

func TestIsSHA(t *testing.T) {
	for s, want := range map[string]bool{
		strings.Repeat("a", 40): true,
		strings.Repeat("0", 64): true,
		strings.Repeat("A", 40): false,
		"abc":                   false,
	} {
		if got := isSHA(s); got != want {
			t.Errorf("isSHA(%q) = %v, want %v", s, got, want)
		}
	}
}
