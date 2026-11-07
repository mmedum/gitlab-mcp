package gatekit

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestProblems(t *testing.T) {
	var out bytes.Buffer
	if err := Problems(&out, "x", nil); err != nil || out.Len() != 0 {
		t.Errorf("no problems: err = %v, out = %q", err, out.String())
	}
	err := Problems(&out, "the thing", []string{"a", "b"})
	if err == nil || err.Error() != "2 problem(s) in the thing" {
		t.Errorf("err = %v", err)
	}
	if got, want := out.String(), "  a\n  b\n"; got != want {
		t.Errorf("out = %q, want %q", got, want)
	}
}

func TestFloor(t *testing.T) {
	if got := Floor("rows", 5, 5); got != nil {
		t.Errorf("at the floor: %q", got)
	}
	if got := Floor("rows", 4, 5); len(got) != 1 || !strings.Contains(got[0], "read 4 rows, want at least 5") {
		t.Errorf("below the floor: %q", got)
	}
}

func TestClipCutsOnARuneBoundary(t *testing.T) {
	if got := Clip(strings.Repeat("é", 10), 5); got != "éé…" {
		t.Errorf("Clip = %q, want %q", got, "éé…")
	}
	if got := Clip("short", 10); got != "short" {
		t.Errorf("Clip changed a short string: %q", got)
	}
	if got := Clip("abcdef", 3); got != "abc…" {
		t.Errorf("Clip = %q, want %q", got, "abc…")
	}
}

func TestWriteFileAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.json")
	if err := WriteFileAtomic(path, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("two")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "two" {
		t.Errorf("content = %q, %v", got, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("left %d files behind, want 1", len(entries))
	}
	if err := WriteFileAtomic(filepath.Join(t.TempDir(), "missing", "f"), nil); err == nil {
		t.Error("writing into a missing directory succeeded")
	}
}

func TestParseGoDir(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"a.go":       "package p\n",
		"a_test.go":  "package p\n",
		"live.go":    "//go:build live\n\npackage p\n",
		"README.md":  "x",
		"other_x.go": "package p\n",
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fset := token.NewFileSet()
	got, err := ParseGoDir(fset, dir, parser.PackageClauseOnly)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("parsed %d files without tags, want 2 (a.go, other_x.go)", len(got))
	}
	got, err = ParseGoDir(fset, dir, parser.PackageClauseOnly, "live")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("parsed %d files with -tags=live, want 3", len(got))
	}
}

// The go command's words are read as the shell splits them, its flags
// and their values skipped, so a flag never reads as the package.
func TestGoRunsInSkipsTheFlags(t *testing.T) {
	for line, want := range map[string][]GoRun{
		"go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...": {
			{Pkg: "golang.org/x/vuln/cmd/govulncheck@v1.8.0", Args: []string{"./..."}}},
		"go run -mod=mod -modfile tools.mod -trimpath ./scripts/gates pins": {
			{Pkg: "./scripts/gates", Args: []string{"pins"}}},
		`CGO_ENABLED=0 go run "x.org/y@v1" && go run -C dir ./z a`: {
			{Pkg: "x.org/y@v1"},
			{Pkg: "./z", Args: []string{"a"}}},
		`go run -ldflags "-s -w" ./scripts/gates docs`: {
			{Pkg: "./scripts/gates", Args: []string{"docs"}}},
		`go run -ldflags="-X a=b -s" tool.org/x@v1 x`: {
			{Pkg: "tool.org/x@v1", Args: []string{"x"}}},
		`go run -ldflags=-X\ a=b ./scripts/gates pins`: {
			{Pkg: "./scripts/gates", Args: []string{"pins"}}},
		`go run -ldflags '-X a=b c' ./scripts/gates pins`: {
			{Pkg: "./scripts/gates", Args: []string{"pins"}}},
		`go run -gcflags 'all=-N -l' ./cmd/x; echo done`: {
			{Pkg: "./cmd/x"}},
		`$GOROOT/bin/go run x.org/y@main`: {
			{Pkg: "x.org/y@main"}},
		"go run -mod":              nil,
		"cargo run x":              nil,
		`echo "go run x.org/y@v1"`: nil,
		"echo go run":              nil,
		"go vet ./...":             nil,
		// A command substitution and a subshell hold commands of their own,
		// and a quoted ")" is a word, not the end of one.
		"OUT=$(go run x.org/y@v1 ./...)": {
			{Pkg: "x.org/y@v1", Args: []string{"./..."}}},
		"OUT=`go run x.org/y@v1` && echo \"$(go run ./scripts/gates docs)\"": {
			{Pkg: "x.org/y@v1"},
			{Pkg: "./scripts/gates", Args: []string{"docs"}}},
		"echo $(echo $(go run x.org/y@v1 a) b)": {
			{Pkg: "x.org/y@v1", Args: []string{"a"}}},
		"(go run x.org/y@v1)": {
			{Pkg: "x.org/y@v1"}},
		`go run x.org/y@v1 ")" a`: {
			{Pkg: "x.org/y@v1", Args: []string{")", "a"}}},
		`echo '$(go run x.org/y@v1)' "\$(go run x.org/y@v2)"`: nil,
		// A substitution's end goes back to the quote it was opened in.
		`echo "$(go version) go run x.org/y@v1"`: nil,
		`X="$(date)a" go run x.org/y@v1`: {
			{Pkg: "x.org/y@v1"}},
		"go run x.org/y@v1 # go run x.org/z@v2": {
			{Pkg: "x.org/y@v1"}},
	} {
		got := GoRunsIn(line)
		if !slices.EqualFunc(got, want, func(a, b GoRun) bool { return a.Pkg == b.Pkg && slices.Equal(a.Args, b.Args) }) {
			t.Errorf("GoRunsIn(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestGoCommandsEndAtTheCommand(t *testing.T) {
	got := GoCommands(`go install -v a.org/b@v1 c.org/d@v2 | tee log && go vet -tags=live ./...; echo go; go`)
	want := []GoCommand{{"install", []string{"-v", "a.org/b@v1", "c.org/d@v2"}}, {"vet", []string{"-tags=live", "./..."}}}
	if !slices.EqualFunc(got, want, func(a, b GoCommand) bool { return a.Sub == b.Sub && slices.Equal(a.Args, b.Args) }) {
		t.Errorf("GoCommands = %q, want %q", got, want)
	}
	if got, want := GoAfterFlags([]string{"-tags", "x", "-v", "a.org/b@v1"}), []string{"a.org/b@v1"}; !slices.Equal(got, want) {
		t.Errorf("GoAfterFlags = %q, want %q", got, want)
	}
	got = GoCommands("X=$(go env GOPATH)/bin; go vet ./...")
	want = []GoCommand{{"env", []string{"GOPATH"}}, {"vet", []string{"./..."}}}
	if !slices.EqualFunc(got, want, func(a, b GoCommand) bool { return a.Sub == b.Sub && slices.Equal(a.Args, b.Args) }) {
		t.Errorf("GoCommands = %q, want %q: a substitution's ) ends its command", got, want)
	}
}

// A flag build, run or vet reads a value for takes the next word when the
// value is written apart from it.
func TestGoAfterFlagsSkipsTheValues(t *testing.T) {
	for _, words := range [][]string{
		{"-o", "bin/gates", "./scripts/gates"},
		{"-vettool", "/usr/local/bin/shadow", "./scripts/gates"},
		{"-c", "2", "./scripts/gates"},
		{"-printf.funcs", "Wrapf", "./scripts/gates"},
		{"-unusedresult.funcs", "pkg.F", "./scripts/gates"},
		{"-exec", "wrapper", "./scripts/gates"},
		{"-debug-trace", "/tmp/trace.json", "./scripts/gates"},
		{"-debug-actiongraph", "graph.json", "./scripts/gates"},
		{"-debug-runtime-trace", "runtime.trace", "./scripts/gates"},
	} {
		if got := GoAfterFlags(words); !slices.Equal(got, []string{"./scripts/gates"}) {
			t.Errorf("GoAfterFlags(%q) = %q, want [./scripts/gates]", words, got)
		}
	}
}

func TestShellWords(t *testing.T) {
	for line, want := range map[string][]string{
		`a "b c" 'd e' f\ g`: {"a", "b c", "d e", "f g"},
		`"a\"b" 'a\b' "a\b"`: {`a"b`, `a\b`, `a\b`},
		"a;b&&c||d|e":        {"a", ";", "b", "&&", "c", "||", "d", "|", "e"},
		`x="" y`:             {"x=", "y"},
		"\t a \t":            {"a"},
		// A # that starts a word starts a comment; inside a word, quoted or
		// escaped, it is a character.
		"a # b c":        {"a"},
		"a;#b":           {"a", ";"},
		`a#b "#" \# ""#`: {"a#b", "#", "#", "#"},
		"${#x} y":        {"${#x}", "y"},
		"# all of it":    nil,
	} {
		if got := ShellWords(line); !slices.Equal(got, want) {
			t.Errorf("ShellWords(%q) = %q, want %q", line, got, want)
		}
	}
}
