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
	// A redirection is no word of the command it sits in.
	words := slices.DeleteFunc(shellSplit(line, true), func(w shellWord) bool { return w.redir })
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
// A redirection, such as 2>&1, is one word with its target.
func ShellWords(line string) []string {
	split := shellSplit(line, false)
	words := make([]string, 0, len(split))
	for _, w := range split {
		words = append(words, w.text)
	}
	return words
}

// Words is a command line's words, read through command substitutions
// and subshells, operators and redirections dropped: each word a command
// may run.
func Words(line string) []string {
	var out []string
	for _, w := range shellSplit(line, true) {
		if !w.op && !w.redir {
			out = append(out, w.text)
		}
	}
	return out
}

// Uncommented is a command line up to the # that starts a comment: one
// that begins a word, outside quotes
// (https://pubs.opengroup.org/onlinepubs/9799919799/utilities/V3_chap02.html#tag_19_03).
func Uncommented(line string) string {
	quote := byte(0)
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case c == '\\' && i+1 < len(line):
			i++
		case quote == '"':
			if c == '"' {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
			return line[:i]
		}
	}
	return line
}

// listOps are the operators that end a command in a list
// (https://pubs.opengroup.org/onlinepubs/9799919799/utilities/V3_chap02.html#tag_19_09_03).
var listOps = map[string]bool{";": true, "&": true, "&&": true, "||": true}

// Lists is a command line's commands, split at its list operators, each
// as its words with any redirection dropped. A pipeline stays one command.
func Lists(line string) [][]string {
	words := shellSplit(line, false)
	var out [][]string
	start := 0
	for i := 0; i <= len(words); i++ {
		if i < len(words) && (!words[i].op || !listOps[words[i].text]) {
			continue
		}
		command := make([]string, 0, i-start)
		for _, w := range words[start:i] {
			if !w.redir {
				command = append(command, w.text)
			}
		}
		if len(command) > 0 {
			out = append(out, command)
		}
		start = i + 1
	}
	return out
}

// Swallowed names how a command line lets a failure pass, or is "": ||
// with true, : or exit 0 after it, a last command of true, : or exit 0
// after ;, a command run in the background with &, ! before a command, and
// set +e (https://pubs.opengroup.org/onlinepubs/9799919799/utilities/V3_chap02.html#tag_19_09).
func Swallowed(line string) string {
	words := slices.DeleteFunc(shellSplit(line, true), func(w shellWord) bool { return w.redir })
	op := ""
	for i := 0; i < len(words); {
		if words[i].op {
			if words[i].text == "&" {
				return "& runs a command in the background, where its failure is never seen"
			}
			op = words[i].text
			i++
			continue
		}
		end := i
		var command []string
		for ; end < len(words) && !words[end].op; end++ {
			command = append(command, words[end].text)
		}
		passes := command[0] == "true" || command[0] == ":" || command[0] == "exit" && len(command) == 2 && command[1] == "0"
		switch {
		case op == "||" && passes:
			return "`|| " + strings.Join(command, " ") + "` passes whatever came before"
		case op == ";" && end == len(words) && passes:
			return "`; " + strings.Join(command, " ") + "` ends it with success whatever came before"
		case command[0] == "!":
			return "! turns a failure into success"
		case command[0] == "set" && slices.ContainsFunc(command[1:], func(a string) bool {
			return strings.HasPrefix(a, "+") && strings.Contains(a, "e")
		}):
			return "set +e lets the commands after it fail"
		}
		i = end
	}
	return ""
}

// shellWord is one word of a command line, op when it is an operator that
// ends a command rather than text, and redir when it is a redirection.
type shellWord struct {
	text  string
	op    bool
	redir bool
}

// shellSplit is ShellWords with each word's kind. With subst, $(, (, ) and
// a backtick are operators too, since a command substitution or a subshell
// holds commands of its own
// (https://pubs.opengroup.org/onlinepubs/9799919799/utilities/V3_chap02.html#tag_19_06_03).
// A redirection, such as 2>&1, < in or bash's &>log, is one word with its
// target, and neither a word of its command nor an operator that ends it
// (https://pubs.opengroup.org/onlinepubs/9799919799/utilities/V3_chap02.html#tag_19_07,
// https://www.gnu.org/software/bash/manual/bash.html#Redirecting-Standard-Output-and-Standard-Error).
func shellSplit(line string, subst bool) []shellWord {
	var s shellSplitter
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case s.quote == '\'':
			s.inQuote(c)
		case s.escaped(line, i):
			i++
			s.add(line[i])
		case subst && s.substitution(line, &i):
		case s.quote == '"':
			s.inQuote(c)
		case strings.IndexByte(`'"`, c) >= 0:
			s.quote, s.inWord = c, true
		case c == '#' && !s.inWord:
			return s.finish()
		case strings.IndexByte(" \t", c) >= 0:
			s.flush()
		case s.redirection(line, &i):
		case s.operatorRun(line, &i):
		default:
			s.add(c)
		}
	}
	return s.finish()
}

// shellSplitter is shellSplit's state: the words read, the one being read,
// the quote the reader is in, the substitutions and subshells around it,
// each with the character that ends it and the quote it was opened in, and
// a redirection's operator waiting for its target.
type shellSplitter struct {
	words    []shellWord
	word     strings.Builder
	inWord   bool
	quote    byte
	opened   []struct{ end, quote byte }
	redirect string
}

// inQuote reads c inside a quote, whose own character ends it.
func (s *shellSplitter) inQuote(c byte) {
	if c == s.quote {
		s.quote = 0
	} else {
		s.word.WriteByte(c)
	}
}

// escaped reports whether line[i] is a backslash that keeps the next
// character: any outside quotes, and in double quotes one before ", \, $
// or a backtick.
func (s *shellSplitter) escaped(line string, i int) bool {
	return line[i] == '\\' && i+1 < len(line) && (s.quote == 0 || strings.IndexByte("\"\\$`", line[i+1]) >= 0)
}

// add appends c to the word being read.
func (s *shellSplitter) add(c byte) {
	s.word.WriteByte(c)
	s.inWord = true
}

// operatorRun reads a run of one of ;, & or | at line[*i] as one
// operator, moving *i to its last character, and reports whether there
// was one.
func (s *shellSplitter) operatorRun(line string, i *int) bool {
	c := line[*i]
	if strings.IndexByte(";&|", c) < 0 {
		return false
	}
	j := *i
	for j+1 < len(line) && line[j+1] == c {
		j++
	}
	s.operator(line[*i : j+1])
	*i = j
	return true
}

// flush ends the word being read, the target of a waiting redirection
// when there is one.
func (s *shellSplitter) flush() {
	if s.inWord {
		w := shellWord{text: s.word.String()}
		if s.redirect != "" {
			w = shellWord{text: s.redirect + w.text, redir: true}
			s.redirect = ""
		}
		s.words = append(s.words, w)
		s.word.Reset()
		s.inWord = false
	}
}

// finish ends the line: the word being read, and a redirection that never
// got its target, as it is.
func (s *shellSplitter) finish() []shellWord {
	s.flush()
	if s.redirect != "" {
		s.words = append(s.words, shellWord{text: s.redirect, redir: true})
		s.redirect = ""
	}
	return s.words
}

func (s *shellSplitter) operator(op string) {
	s.words = append(s.finish(), shellWord{text: op, op: true})
}

// redirection reads a redirection operator at line[*i], moving *i to its
// last character, and reports whether there was one. The operator waits
// for the next word as its target. A word of digits alone and unquoted
// before it is the file descriptor it redirects, part of the redirection
// (https://pubs.opengroup.org/onlinepubs/9799919799/utilities/V3_chap02.html#tag_19_10_01).
func (s *shellSplitter) redirection(line string, i *int) bool {
	c := line[*i]
	if c != '<' && c != '>' && !strings.HasPrefix(line[*i:], "&>") {
		return false
	}
	j := *i + 1
	for j < len(line) && strings.IndexByte("<>&|", line[j]) >= 0 {
		j++
	}
	op := line[*i:j]
	if fd := s.word.String(); s.inWord && s.redirect == "" && c != '&' && fd != "" &&
		strings.Trim(fd, "0123456789") == "" && strings.HasSuffix(line[:*i], fd) {
		op = fd + op
		s.word.Reset()
		s.inWord = false
	}
	s.finish()
	s.redirect, *i = op, j-1
	return true
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
