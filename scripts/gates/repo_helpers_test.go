package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoTree writes files (slash-separated path → content) under a fresh
// temporary directory and returns it.
func repoTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// breakFile replaces the first old in files[name] with new, failing the
// test when there is nothing to break: a break that misses its target
// leaves a passing fixture that proves nothing.
func breakFile(t *testing.T, files map[string]string, name, old, new string) {
	t.Helper()
	if !strings.Contains(files[name], old) {
		t.Fatalf("%s has no %q to break", name, old)
	}
	files[name] = strings.Replace(files[name], old, new, 1)
}

// wantProblem fails unless want is among problems.
func wantProblem(t *testing.T, problems []string, want string) {
	t.Helper()
	if joined := strings.Join(problems, "\n"); !strings.Contains(joined, want) {
		t.Errorf("want %q among the problems, got:\n%s", want, joined)
	}
}

// wantClean fails on any problem.
func wantClean(t *testing.T, problems []string) {
	t.Helper()
	if len(problems) > 0 {
		t.Errorf("problems on a clean fixture:\n%s", strings.Join(problems, "\n"))
	}
}

// mustSay fails unless a gate's output says want. Every gate says how
// much it read, and that sentence is the only difference between "found
// nothing" and "looked at nothing".
func mustSay(t *testing.T, out, want string) {
	t.Helper()
	if !strings.Contains(out, want) {
		t.Errorf("the gate printed %q, which does not say %q", out, want)
	}
}
