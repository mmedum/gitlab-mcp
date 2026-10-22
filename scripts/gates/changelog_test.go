package main

import (
	"bytes"
	"cmp"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mmedum/gitlab-mcp/v3/scripts/internal/gitx"
)

// gitRepo makes a throwaway repository with a fixed identity, so a test
// never depends on the machine's git configuration.
func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := repoTree(t, files)
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "alice@example.com"},
		{"config", "user.name", "alice"},
		{"config", "commit.gpgsign", "false"},
		{"config", "tag.gpgsign", "false"},
		{"add", "-A"},
		{"commit", "-q", "-m", "base"},
	} {
		gitDo(t, dir, args...)
	}
	return dir
}

func gitDo(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitx.Output(dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

// gitCommit writes files and commits them.
func gitCommit(t *testing.T, dir, msg string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitDo(t, dir, "add", "-A")
	gitDo(t, dir, "commit", "-q", "--allow-empty", "-m", msg)
}

// changelogGoMod is go.mod as the go command writes it, with both forms
// of require.
const changelogGoMod = "module m\n\ngo 1.27.2\n\nrequire (\n\texample.com/a v1.0.0\n\tgithub.com/modelcontextprotocol/go-sdk v1.8.0\n" +
	"\texample.com/b v0.1.0 // indirect\n)\n\nrequire example.com/c v1.2.0\n"

// changelogGoModCommented carries the comments the go command keeps after
// `require (` and after `)`.
var changelogGoModCommented = strings.NewReplacer("require (\n", "require ( // direct\n", ")\n\n", ") // direct\n\n").
	Replace(changelogGoMod)

const changelogBase = "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- One.\n\n## [0.1.0] - 2026-01-01\n\n### Added\n\n- First.\n\n" +
	"[Unreleased]: https://example.com/compare/v0.1.0...HEAD\n[0.1.0]: https://example.com/releases/tag/v0.1.0\n"

func TestChangelogPR(t *testing.T) {
	bumpA := strings.NewReplacer("a v1.0.0", "a v1.1.0")
	cases := []struct {
		name  string
		goMod string // the base's go.mod; empty: changelogGoMod
		files map[string]string
		want  string // empty: passes
	}{
		{"entry under Unreleased", "", map[string]string{
			"internal/x/x.go": "package x // changed\n",
			"CHANGELOG.md":    strings.Replace(changelogBase, "- One.\n", "- One.\n- Two.\n", 1),
		}, ""},
		{"no shipped change", "", map[string]string{"docs/a.md": "x\n", "internal/x/x_test.go": "package x\n"}, ""},
		{"shipped change, no entry", "", map[string]string{"internal/x/x.go": "package x // changed\n"},
			"did not"},
		{"entry under a released heading", "", map[string]string{
			"internal/x/x.go": "package x // changed\n",
			"CHANGELOG.md":    strings.Replace(changelogBase, "- First.\n", "- First.\n- Late.\n", 1),
		}, "no line was added under [Unreleased]"},
		{"a dependency bump", "", map[string]string{
			"go.mod": strings.NewReplacer("a v1.0.0", "a v1.1.0", "c v1.2.0", "c v1.3.0",
				"// indirect\n)", "// indirect\n\texample.com/d v0.2.0 // indirect\n)").Replace(changelogGoMod),
		}, ""},
		{"a dependency bump between commented parens", changelogGoModCommented, map[string]string{
			"go.mod": bumpA.Replace(changelogGoModCommented),
		}, ""},
		{"a dependency bump with code", "", map[string]string{
			"go.mod":          bumpA.Replace(changelogGoMod),
			"internal/x/x.go": "package x // changed\n",
		}, "1 shipped file(s) changed (internal/x/x.go)"},
		{"a go directive change", "", map[string]string{
			"go.mod": strings.Replace(changelogGoMod, "go 1.27.2", "go 1.27.3", 1),
		}, "1 shipped file(s) changed (go.mod)"},
		{"a toolchain directive", "", map[string]string{
			"go.mod": strings.Replace(changelogGoMod, "go 1.27.2\n", "go 1.27.2\n\ntoolchain go1.27.3\n", 1),
		}, "1 shipped file(s) changed (go.mod)"},
		{"a replace after a commented paren", changelogGoModCommented, map[string]string{
			"go.mod": changelogGoModCommented + "\nreplace example.com/a => example.com/fork v1.0.0\n",
		}, "1 shipped file(s) changed (go.mod)"},
		{"a bump of the MCP SDK", "", map[string]string{
			"go.mod": strings.Replace(changelogGoMod, "go-sdk v1.8.0", "go-sdk v1.9.0", 1),
		}, "1 shipped file(s) changed (go.mod)"},
		{"a require block left open", "", map[string]string{
			"go.mod": bumpA.Replace(strings.Replace(changelogGoMod, ")\n", "", 1)),
		}, "1 shipped file(s) changed (go.mod)"},
		{"a bump in a require the go command would not spell", strings.Replace(changelogGoMod, "require (", "require(", 1),
			map[string]string{"go.mod": bumpA.Replace(strings.Replace(changelogGoMod, "require (", "require(", 1))},
			"1 shipped file(s) changed (go.mod)"},
		{"a release cut", "", map[string]string{
			"go.mod": "module m // changed\n",
			"CHANGELOG.md": strings.Replace(changelogBase, "## [Unreleased]\n\n### Added\n\n- One.\n",
				"## [Unreleased]\n\n## [0.2.0] - 2026-02-01\n\n### Added\n\n- One.\n", 1),
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			goMod := cmp.Or(tc.goMod, changelogGoMod)
			dir := gitRepo(t, map[string]string{"CHANGELOG.md": changelogBase, "internal/x/x.go": "package x\n", "go.mod": goMod})
			gitDo(t, dir, "checkout", "-q", "-b", "topic")
			gitCommit(t, dir, "work", tc.files)
			// The base branch moves on with a shipped change and its own
			// entry; measured from the merge-base, neither is the topic's.
			gitDo(t, dir, "checkout", "-q", "main")
			gitCommit(t, dir, "main moved", map[string]string{"cmd/y.go": "package main\n"})
			var out bytes.Buffer
			err := changelogCheck(&out, dir, "main", "topic")
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("want a pass, got %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("want an error with %q, got %v", tc.want, err)
			}
		})
	}
}

func TestMergeBaseGate(t *testing.T) {
	dir := gitRepo(t, map[string]string{"a": "a"})
	base := gitDo(t, dir, "rev-parse", "HEAD")
	gitDo(t, dir, "checkout", "-q", "-b", "topic")
	gitCommit(t, dir, "t", map[string]string{"b": "b"})
	t.Chdir(dir)
	var out bytes.Buffer
	if err := mergeBase(&out, []string{"main", "topic"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != base {
		t.Errorf("merge-base printed %q, want %s", got, base)
	}
	if err := mergeBase(&out, []string{"main", "no-such-branch"}); err == nil {
		t.Error("a missing head passed")
	}
}

func TestChangelogLinks(t *testing.T) {
	n, problems := changelogLinkProblems(changelogBase)
	wantClean(t, problems)
	if n != 1 {
		t.Errorf("read %d version headings, want 1", n)
	}
	cases := []struct{ name, old, new, want string }{
		{"a heading with no link", "[0.1.0]: https://example.com/releases/tag/v0.1.0\n", "", "## [0.1.0] has no [0.1.0]: link reference"},
		{"Unreleased unlinked", "[Unreleased]: https://example.com/compare/v0.1.0...HEAD\n", "", "[Unreleased] has no link reference"},
		{"Unreleased from an old release", "compare/v0.1.0...HEAD", "compare/v0.0.9...HEAD", "should compare from v0.1.0..."},
		{"no Unreleased heading", "## [Unreleased]\n", "## Unreleased\n", "there is no ## [Unreleased] heading"},
		{"a link with no heading", "[0.1.0]: https", "[0.0.1]: https://example.com/x\n[0.1.0]: https", "[0.0.1]: is a link reference with no heading"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(changelogBase, tc.old) {
				t.Fatalf("nothing to break: %q", tc.old)
			}
			_, problems := changelogLinkProblems(strings.Replace(changelogBase, tc.old, tc.new, 1))
			wantProblem(t, problems, tc.want)
		})
	}
}

func TestReleaseNotes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CHANGELOG.md")
	text := changelogBase + "\n"
	text = strings.Replace(text, "- First.\n", "- First.\n\n```\n## not a heading\n```\n", 1)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := releaseNotesFrom(&out, path, "v0.1.0"); err != nil {
		t.Fatal(err)
	}
	if want := "### Added\n\n- First.\n\n```\n## not a heading\n```\n"; out.String() != want {
		t.Errorf("notes = %q, want %q", out.String(), want)
	}
	empty := strings.Replace(changelogBase, "### Added\n\n- First.\n", "### Added\n", 1)
	if err := os.WriteFile(path, []byte(empty), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := releaseNotesFrom(&out, path, "0.1.0"); err == nil || !strings.Contains(err.Error(), "no entries under ## [0.1.0]") {
		t.Errorf("an empty section: err = %v", err)
	}
	if err := releaseNotesFrom(&out, path, "9.9.9"); err == nil {
		t.Error("a missing version passed")
	}
}
