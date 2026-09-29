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
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	// ModulePath is this module, as go.mod declares it.
	ModulePath = "github.com/mmedum/gitlab-mcp/v2"
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

// MarkdownSection is the text under a heading line, to the next heading
// of the same or a higher level: a subsection stays inside its parent.
// ok is false when the heading is not there.
func MarkdownSection(text, heading string) (section string, ok bool) {
	i := strings.Index(text, "\n"+heading+"\n")
	switch {
	case i >= 0:
		text = text[i+1:]
	case strings.HasPrefix(text, heading+"\n"):
	default:
		return "", false
	}
	level := len(heading) - len(strings.TrimLeft(heading, "#"))
	rest := text[len(heading)+1:]
	offset := 0
	fenced := false
	for _, line := range strings.SplitAfter(rest, "\n") {
		if strings.HasPrefix(line, "```") {
			fenced = !fenced
		}
		trimmed := strings.TrimLeft(line, "#")
		if n := len(line) - len(trimmed); !fenced && n > 0 && n <= level && strings.HasPrefix(trimmed, " ") {
			return rest[:offset], true
		}
		offset += len(line)
	}
	return rest, true
}
