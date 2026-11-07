// Package gatekit is what several gates share that is not git, a TSV
// record or an MCP session: the repository's names, how a list of
// problems becomes one failure, and the few readers more than one gate
// needs.
package gatekit

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	// ModulePath is this module, as go.mod declares it.
	ModulePath = "github.com/mmedum/gitlab-mcp/v4"
	// BinaryName is the server's command and archive name.
	BinaryName = "gitlab-mcp"
	// MainPackage is what `go build` builds for the server.
	MainPackage = "./cmd/" + BinaryName
	// PlaceholderVersion is the version the committed manifest carries.
	// A real version in the tree is a stale version waiting to ship.
	PlaceholderVersion = "0.0.0-dev"
)

// Problems reports a list of problems as one failure, each on its own
// line through out, and nil when there are none. what names the thing
// that has them.
func Problems(out io.Writer, what string, problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	for _, p := range problems {
		_, _ = fmt.Fprintln(out, "  "+p)
	}
	return fmt.Errorf("%d problem(s) in %s", len(problems), what)
}

// Floor is a problem when read is below min: zero findings and zero
// inputs are otherwise the same output.
func Floor(what string, read, min int) []string {
	if read >= min {
		return nil
	}
	return []string{fmt.Sprintf("read %d %s, want at least %d: the reader is not seeing its input", read, what, min)}
}

// Clip shortens s to at most n bytes plus an ellipsis, cutting on a rune
// boundary so a quoted excerpt stays valid UTF-8.
func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// WriteFileAtomic writes through a temporary file in the same directory
// and renames it into place, so a failure half-way leaves the old file
// as it was.
func WriteFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // a no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil { //nolint:gosec // a committed, world-readable record
		return err
	}
	return os.Rename(name, path)
}

// ParseGoDir parses the non-test Go files in dir that the default build
// context, plus any tags given, would compile, in name order. A file
// excluded by a build constraint or a GOOS/GOARCH suffix is skipped, as
// `go build` skips it.
func ParseGoDir(fset *token.FileSet, dir string, mode parser.Mode, tags ...string) ([]*ast.File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	ctx := build.Default
	ctx.BuildTags = append(slices.Clone(ctx.BuildTags), tags...)
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if ok, err := ctx.MatchFile(dir, name); err != nil {
			return nil, err
		} else if !ok {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, mode)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

// GoRun is one `go run` command: the package it runs, such as
// example.com/tool@v1.2.3 or ./scripts/gates, and the words after it.
type GoRun struct {
	Pkg  string
	Args []string
}

// goValueFlags are the flags the go command reads its next word for, as
// go1.27.3's `go help build`, `go help run`, `go help vet` and `go tool vet
// -flags` list them: the build flags and build's -o, run's -exec, vet's
// -vettool and the vet tool's own
// (https://pkg.go.dev/cmd/go#hdr-Compile_packages_and_dependencies). The
// help leaves out three debug flags that take a value too
// (https://github.com/golang/go/blob/go1.27.3/src/cmd/go/internal/work/build.go#L358-L361).
var goValueFlags = map[string]bool{
	"C": true, "p": true, "covermode": true, "coverpkg": true, "asmflags": true, "buildmode": true,
	"compiler": true, "gccgoflags": true, "gcflags": true, "installsuffix": true, "ldflags": true,
	"mod": true, "modfile": true, "overlay": true, "pgo": true, "pkgdir": true, "tags": true,
	"toolexec": true, "o": true, "exec": true, "vettool": true, "c": true, "printf.funcs": true,
	"printfuncs": true, "unusedfuncs": true, "unusedresult.funcs": true, "unusedresult.stringmethods": true,
	"unusedstringmethods": true, "debug-actiongraph": true, "debug-runtime-trace": true, "debug-trace": true,
}

// GoRunsIn is each `go run` a command line holds, its flags skipped, so
// `go run -mod=mod tool@v1` runs tool@v1.
func GoRunsIn(line string) []GoRun {
	var runs []GoRun
	for _, c := range GoCommands(line) {
		if rest := GoAfterFlags(c.Args); c.Sub == "run" && len(rest) > 0 {
			runs = append(runs, GoRun{Pkg: rest[0], Args: rest[1:]})
		}
	}
	return runs
}

// GoAfterFlags is a go command's words after its flags, a flag that takes
// a value skipping that value too: the packages, or for `go run` its one
// package and the program's arguments.
func GoAfterFlags(words []string) []string {
	i := 0
	for i < len(words) && strings.HasPrefix(words[i], "-") {
		name, _, inline := strings.Cut(strings.TrimLeft(words[i], "-"), "=")
		if i++; !inline && goValueFlags[name] {
			i++
		}
	}
	return words[min(i, len(words)):]
}

// GoCommand is one `go <sub>` on a command line: the subcommand, such as
// run or vet, and the words after it to the end of its command.
type GoCommand struct {
	Sub  string
	Args []string
}

// GoCommands is each go command a command line holds, the line split once
// as the shell splits it. A command substitution, backticks and a subshell
// hold commands of their own, so `OUT=$(go run x@v1)` runs x@v1.
func GoCommands(line string) []GoCommand {
	words := shellSplit(line, true)
	var out []GoCommand
	for i := 0; i+1 < len(words); i++ {
		// The go command by any path, such as $GOROOT/bin/go.
		if path.Base(words[i].text) != "go" || words[i+1].op {
			continue
		}
		end := i + 2
		var args []string
		for ; end < len(words) && !words[end].op; end++ {
			args = append(args, words[end].text)
		}
		out = append(out, GoCommand{Sub: words[i+1].text, Args: args})
		i = end
	}
	return out
}

// ShellWords splits a command line into words as a POSIX shell does,
// expanding nothing: blanks separate words, quotes group and are dropped,
// a backslash outside single quotes keeps the next character, a run of ;,
// & or | is a word of its own, which ends a command, and a # that starts a
// word starts a comment
// (https://pubs.opengroup.org/onlinepubs/9799919799/utilities/V3_chap02.html#tag_19_03).
func ShellWords(line string) []string {
	split := shellSplit(line, false)
	words := make([]string, 0, len(split))
	for _, w := range split {
		words = append(words, w.text)
	}
	return words
}

// shellWord is one word of a command line, op when it is an operator that
// ends a command rather than text.
type shellWord struct {
	text string
	op   bool
}

// shellSplit is ShellWords with each word's kind. With subst, $(, (, ) and
// a backtick are operators too, since a command substitution or a subshell
// holds commands of its own
// (https://pubs.opengroup.org/onlinepubs/9799919799/utilities/V3_chap02.html#tag_19_06_03).
func shellSplit(line string, subst bool) []shellWord {
	var s shellSplitter
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case s.quote == '\'':
			if c == '\'' {
				s.quote = 0
			} else {
				s.word.WriteByte(c)
			}
		case c == '\\' && i+1 < len(line) && (s.quote == 0 || strings.IndexByte("\"\\$`", line[i+1]) >= 0):
			i++
			s.word.WriteByte(line[i])
			s.inWord = true
		case subst && s.substitution(line, &i):
		case s.quote == '"':
			if c == '"' {
				s.quote = 0
			} else {
				s.word.WriteByte(c)
			}
		case c == '\'' || c == '"':
			s.quote, s.inWord = c, true
		case c == '#' && !s.inWord:
			return s.words
		case c == ' ' || c == '\t':
			s.flush()
		case strings.IndexByte(";&|", c) >= 0:
			j := i
			for j+1 < len(line) && line[j+1] == c {
				j++
			}
			s.operator(line[i : j+1])
			i = j
		default:
			s.word.WriteByte(c)
			s.inWord = true
		}
	}
	s.flush()
	return s.words
}

// shellSplitter is shellSplit's state: the words read, the one being read,
// the quote the reader is in, and the substitutions and subshells around
// it, each with the character that ends it and the quote it was opened in.
type shellSplitter struct {
	words  []shellWord
	word   strings.Builder
	inWord bool
	quote  byte
	opened []struct{ end, quote byte }
}

func (s *shellSplitter) flush() {
	if s.inWord {
		s.words = append(s.words, shellWord{text: s.word.String()})
		s.word.Reset()
		s.inWord = false
	}
}

func (s *shellSplitter) operator(op string) {
	s.flush()
	s.words = append(s.words, shellWord{text: op, op: true})
}

// substitution reads the start or the end of a substitution or a subshell
// at line[*i], moving *i past a $(, and reports whether there was one. Its
// end goes back to the quote it was opened in.
func (s *shellSplitter) substitution(line string, i *int) bool {
	c, n := line[*i], len(s.opened)
	op, end := string(c), byte(')')
	switch {
	case n > 0 && c == s.opened[n-1].end && (c == '`' || s.quote == 0):
		s.operator(op)
		s.quote, s.opened = s.opened[n-1].quote, s.opened[:n-1]
		s.inWord = s.quote != 0
		return true
	case s.quote == 0 && c == ')':
		s.operator(op)
		return true
	case c == '`':
		end = '`'
	case strings.HasPrefix(line[*i:], "$("):
		op = "$("
		*i++
	case s.quote != 0 || c != '(':
		return false
	}
	s.operator(op)
	s.opened = append(s.opened, struct{ end, quote byte }{end, s.quote})
	s.quote = 0
	return true
}
