package gatekit

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
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

func TestMarkdownSection(t *testing.T) {
	doc := "# T\n\n## A\n\none\n\n### A1\n\n```\n## not a heading\n```\n\ntwo\n\n## B\n\nthree\n"
	got, ok := MarkdownSection(doc, "## A")
	if !ok {
		t.Fatal("## A not found")
	}
	if want := "\none\n\n### A1\n\n```\n## not a heading\n```\n\ntwo\n\n"; got != want {
		t.Errorf("section = %q, want %q", got, want)
	}
	if got, _ := MarkdownSection(doc, "## B"); got != "\nthree\n" {
		t.Errorf("last section = %q", got)
	}
	if _, ok := MarkdownSection(doc, "## C"); ok {
		t.Error("found a heading that is not there")
	}
	if _, ok := MarkdownSection(doc, "## "); ok {
		t.Error("matched a heading prefix")
	}
}
