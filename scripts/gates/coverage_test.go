package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// coverProfile is a profile over the packages: each has sixty one-statement
// blocks, covered where pct says.
func coverProfile(pkgs map[string]int) string {
	var b strings.Builder
	b.WriteString("mode: atomic\n")
	for pkg, pct := range pkgs {
		for i := range 60 {
			hits := 0
			if i*100 < pct*60 {
				hits = 1
			}
			fmt.Fprintf(&b, "github.com/mmedum/gitlab-mcp/v2/%s/f.go:%d.1,%d.2 1 %d\n", pkg, i+1, i+1, hits)
		}
	}
	return b.String()
}

// coverNoCode says only internal/hascode declares functions.
func coverNoCode(pkg string) bool { return pkg == "internal/hascode" }

func coverPackages(n int) (map[string]int, []string) {
	pcts := map[string]int{}
	var list []string
	for i := range n {
		p := fmt.Sprintf("internal/p%02d", i)
		pcts[p] = 90
		list = append(list, p)
	}
	return pcts, list
}

func TestCoverScore(t *testing.T) {
	pcts, pkgs := coverPackages(10)
	pcts["cmd/gitlab-mcp"] = 65
	pkgs = append(pkgs, "cmd/gitlab-mcp", "scripts/gates")
	blocks, err := coverRead(strings.NewReader(coverProfile(pcts)))
	if err != nil {
		t.Fatal(err)
	}
	lines, problems := coverScore(blocks, pkgs, coverNoCode)
	wantClean(t, problems)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"internal/p00", "90.0%  (floor 80%)", "cmd/gitlab-mcp", "65.0%  (floor 60%)", "scripts/gates", "exempt: maintainer tooling"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the table does not say %q:\n%s", want, joined)
		}
	}

	// A block loaded by two test binaries counts once, covered by either.
	dup := "mode: atomic\n" +
		"github.com/mmedum/gitlab-mcp/v2/internal/x/f.go:1.1,1.2 4 0\n" +
		"github.com/mmedum/gitlab-mcp/v2/internal/x/f.go:1.1,1.2 4 1\n"
	blocks, err = coverRead(strings.NewReader(dup))
	if err != nil {
		t.Fatal(err)
	}
	lines, _ = coverScore(blocks, []string{"internal/x"}, coverNoCode)
	if !strings.Contains(strings.Join(lines, "\n"), "100.0%") {
		t.Errorf("a block covered by one binary of two is not covered: %q", lines)
	}
}

func TestCoverRefuses(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(pcts map[string]int, pkgs *[]string)
		want   string
	}{
		{"a package under the floor", func(p map[string]int, _ *[]string) { p["internal/p03"] = 70 },
			"internal/p03 is at 70.0%, under its floor of 80%"},
		{"cmd under its override", func(p map[string]int, pkgs *[]string) {
			p["cmd/gitlab-mcp"] = 55
			*pkgs = append(*pkgs, "cmd/gitlab-mcp")
		}, "cmd/gitlab-mcp is at 55.0%, under its floor of 60%"},
		{"too few packages", func(p map[string]int, pkgs *[]string) {
			for _, k := range []string{"internal/p00", "internal/p01"} {
				delete(p, k)
			}
			*pkgs = (*pkgs)[2:]
		}, "scored 8 packages, want at least 10"},
		{"an untested package is scored as nothing", func(_ map[string]int, pkgs *[]string) {
			*pkgs = append(*pkgs, "internal/new")
		}, ""},
		{"a package with code outside -coverpkg", func(_ map[string]int, pkgs *[]string) {
			*pkgs = append(*pkgs, "internal/hascode")
		}, "internal/hascode has functions and no statements in the profile"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pcts, pkgs := coverPackages(10)
			tc.mutate(pcts, &pkgs)
			blocks, err := coverRead(strings.NewReader(coverProfile(pcts)))
			if err != nil {
				t.Fatal(err)
			}
			lines, problems := coverScore(blocks, pkgs, coverNoCode)
			if tc.want == "" {
				if !strings.Contains(strings.Join(lines, "\n"), "internal/new") {
					t.Errorf("a package with no statements is not in the table: %q", lines)
				}
				return
			}
			wantProblem(t, problems, tc.want)
		})
	}
}

func TestCoverAllExemptFails(t *testing.T) {
	_, problems := coverScore(nil, []string{"scripts/gates", "scripts/internal/tsv"}, coverNoCode)
	wantProblem(t, problems, "scored 0 packages")
	wantProblem(t, problems, "the profile holds 0 blocks, want at least 500")
}

func TestCoverExemptionsCarryReasons(t *testing.T) {
	for pkg, reason := range coverExempt {
		if len(strings.TrimSpace(reason)) < 20 {
			t.Errorf("the exemption for %s has no reason worth the name", pkg)
		}
	}
	for pkg, f := range coverFloors {
		if len(strings.TrimSpace(f.reason)) < 20 || f.floor >= coverDefaultFloor {
			t.Errorf("the floor for %s is %.0f with reason %q", pkg, f.floor, f.reason)
		}
	}
	if coverDefaultFloor != 80 {
		t.Errorf("the default floor is %.0f, want 80", coverDefaultFloor)
	}
}

func TestCoverReadRefusesAMalformedProfile(t *testing.T) {
	if _, err := coverRead(strings.NewReader("mode: set\nnot a block\n")); err == nil {
		t.Error("a malformed line passed")
	}
}

func TestCoverageGateOnAProfileFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cov.out")
	if err := os.WriteFile(path, []byte("mode: set\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := coverage(&out, []string{path}); err == nil {
		t.Error("an empty profile passed")
	}
	if err := coverage(&out, []string{filepath.Join(t.TempDir(), "absent.out")}); err == nil {
		t.Error("a missing profile passed")
	}
}

// The profile the gate scores is built with cmd/ and internal/ in
// -coverpkg, and CI builds it the same way because it runs `make cover`.
func TestCoverpkgIsTheMakefiles(t *testing.T) {
	mk, err := readMakefile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Skip("no Makefile beside the module:", err)
	}
	if got := mk.vars["COVERPKG"]; got != "./cmd/...,./internal/..." {
		t.Errorf("COVERPKG is %q, want ./cmd/...,./internal/...", got)
	}
	recipe := strings.Join(mk.targets["test"].recipe, "\n")
	if !strings.Contains(recipe, "-coverpkg=$(COVERPKG)") || !strings.Contains(recipe, "-coverprofile=cov.out") {
		t.Errorf("the test target does not build cov.out over $(COVERPKG): %q", recipe)
	}
	if got := strings.Join(mk.targets["cover"].recipe, "\n"); !strings.Contains(got, "$(GATES) coverage cov.out") {
		t.Errorf("the cover target does not score cov.out: %q", got)
	}
	ci, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Skip("no ci.yml beside the module:", err)
	}
	if !strings.Contains(string(ci), "run: make cover") || strings.Contains(string(ci), "-coverpkg") {
		t.Error("ci.yml does not measure coverage through `make cover` alone")
	}
}

func TestCoverHasCode(t *testing.T) {
	root := repoTree(t, map[string]string{
		"decl/a.go": "package decl\n\ntype T struct{ A int }\n\nconst C = 1\n",
		"code/a.go": "package code\n\nfunc F() int { return 1 }\n",
	})
	t.Chdir(root)
	if coverHasCode("decl") {
		t.Error("a package of declarations has code")
	}
	if !coverHasCode("code") {
		t.Error("a package with a function has no code")
	}
}
