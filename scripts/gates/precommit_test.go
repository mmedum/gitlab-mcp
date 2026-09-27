package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPrecommitStepsAreTheFourChecks(t *testing.T) {
	root := repoTree(t, map[string]string{"Makefile": "GITLEAKS ?= github.com/zricethezav/gitleaks/v8@v8.30.1\n"})
	steps, err := precommitSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range steps {
		names = append(names, s.name)
	}
	want := []string{"gofmt on staged files", "go vet", "leaks", "gitleaks on staged changes"}
	if !slices.Equal(names, want) {
		t.Errorf("steps %q, want %q", names, want)
	}
	if _, err := precommitSteps(repoTree(t, map[string]string{"Makefile": "GO ?= go\n"})); err == nil {
		t.Error("a Makefile with no GITLEAKS pin passed")
	}
}

// Every step runs and every failure is reported, not only the first; a
// step that cannot run here is a warning.
func TestPrecommitCollectsEveryProblem(t *testing.T) {
	ran := 0
	fails := func(msg string) func(io.Writer) error {
		return func(io.Writer) error { ran++; return errors.New(msg) }
	}
	steps := []precommitStep{
		{name: "one", run: fails("first")},
		{name: "two", run: func(io.Writer) error { ran++; return nil }},
		{name: "three", run: fails("third")},
		{name: "four", run: fails("never"), skip: func() string { return "offline" }},
	}
	var out bytes.Buffer
	err := precommitRunSteps(&out, steps)
	if err == nil || ran != 3 {
		t.Fatalf("err %v after %d steps, want a failure after 3", err, ran)
	}
	for _, want := range []string{"one: first", "three: third", "warning: four skipped: offline"} {
		mustSay(t, out.String(), want)
	}
	if strings.Contains(out.String(), "never") {
		t.Error("a skipped step ran")
	}
	out.Reset()
	if err := precommitRunSteps(&out, steps[1:2]); err != nil {
		t.Fatal(err)
	}
	mustSay(t, out.String(), "precommit ok: 1 steps, 0 skipped")
}

func TestPrecommitGofmtReadsTheStagedFiles(t *testing.T) {
	if _, err := exec.LookPath("gofmt"); err != nil {
		t.Skip("no gofmt")
	}
	dir := gitRepo(t, map[string]string{"ok.go": "package p\n"})
	gitCommit(t, dir, "unformatted", map[string]string{"bad.go": "package p\nfunc  f( ) {}\n"})
	var out bytes.Buffer
	if err := precommitGofmt(&out, dir, []string{"ok.go"}); err != nil {
		t.Errorf("a formatted file failed: %v", err)
	}
	if err := precommitGofmt(&out, dir, []string{"ok.go", "bad.go"}); err == nil || !strings.Contains(err.Error(), "bad.go") {
		t.Errorf("an unformatted file passed: %v", err)
	}
	if err := precommitGofmt(&out, dir, nil); err != nil {
		t.Errorf("no staged files failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "staged.go"), []byte("package p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitDo(t, dir, "add", "staged.go")
	files, err := precommitStaged(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(files, []string{"staged.go"}) {
		t.Errorf("staged %q, want [staged.go]", files)
	}
}
