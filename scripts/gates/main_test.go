package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// Every command runs from the module root, found from any directory
// under it, so a gate reads the same files wherever it is started.
func TestModuleRootIsTheDirectoryHoldingGoMod(t *testing.T) {
	root, err := filepath.EvalSymlinks(repoTree(t, map[string]string{
		"go.mod": "module example.com/m\n", "internal/a/go.txt": "not a module\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, from := range []string{root, filepath.Join(root, "internal", "a")} {
		t.Chdir(from)
		if got, err := moduleRoot(); err != nil || got != root {
			t.Errorf("from %s: moduleRoot() = %q, %v; want %q", from, got, err, root)
		}
	}
	// With no go.mod above it there is no root to run from.
	bare, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(bare)
	if got, err := moduleRoot(); err == nil || !strings.HasPrefix(err.Error(), "no go.mod in "+bare+" or above it") {
		t.Errorf("moduleRoot() = %q, %v; want no go.mod found", got, err)
	}
}
