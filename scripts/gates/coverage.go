package main

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/mmedum/gitlab-mcp/scripts/internal/gatekit"
)

// The coverage gate holds a statement floor per package, not on the
// average: an average hides the one package that carries a guarantee.
//
// A package is scored on its own files only. The profile is built with
// -coverpkg over everything that ships, so a block appears once per test
// binary that loaded it; it is counted once, and is covered if any
// binary covered it. Packages come from `go list`, cmd/ included, so a
// new package is under the floor from its first commit and an untested
// one shows as 0%.

// coverDefaultFloor is the floor for any package not named below.
const coverDefaultFloor = 80.0

// coverFloors are packages held to another floor, each with why. A
// lower floor is printed on every run, unlike an exemption.
var coverFloors = map[string]struct {
	floor  float64
	reason string
}{
	"cmd/gitlab-mcp": {60, "the loopback login, the serve loop and the live doctor walk need a browser, " +
		"a process or the network; smoke and the live driver cover them"},
}

// coverExempt are packages the floor does not apply to, each with why.
// A key ending in "/..." exempts a tree.
var coverExempt = map[string]string{
	"scripts/...": "maintainer tooling that never ships; tested against fixtures rather than scored against a floor",
}

// coverMinPackages and coverMinBlocks are floors on what the gate
// read: an empty profile and a clean one print the same table otherwise.
const (
	coverMinPackages = 10
	coverMinBlocks   = 500
)

// coverBlock is one statement block of a profile.
type coverBlock struct {
	dir, id    string
	statements int
	covered    bool
}

func coverage(out io.Writer, args []string) error {
	profile := args[0]
	f, err := os.Open(profile) //nolint:gosec // a profile path a maintainer or the Makefile names
	if err != nil {
		return fmt.Errorf("read the coverage profile: %w (make cover runs the tests first)", err)
	}
	defer func() { _ = f.Close() }()
	blocks, err := coverRead(f)
	if err != nil {
		return fmt.Errorf("%s: %w", profile, err)
	}
	packages, err := coverList()
	if err != nil {
		return err
	}
	lines, problems := coverScore(blocks, packages, coverHasCode)
	for _, l := range lines {
		_, _ = fmt.Fprintln(out, l)
	}
	if err := gatekit.Problems(out, "coverage", problems); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "coverage ok: %d packages, %d profile blocks\n", len(packages), len(blocks))
	return nil
}

// coverRead parses a profile. The first line is the mode.
func coverRead(r io.Reader) ([]coverBlock, error) {
	var out []coverBlock
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for n := 1; sc.Scan(); n++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "mode:") {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) != 3 {
			return nil, fmt.Errorf("line %d: want three fields, got %d", n, len(fields))
		}
		file, _, ok := strings.Cut(fields[0], ":")
		if !ok {
			return nil, fmt.Errorf("line %d: no file in %q", n, fields[0])
		}
		statements, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		hits, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		dir := file
		if i := strings.LastIndex(file, "/"); i >= 0 {
			dir = file[:i]
		}
		out = append(out, coverBlock{dir: strings.TrimPrefix(dir, gatekit.ModulePath+"/"), id: fields[0],
			statements: statements, covered: hits > 0})
	}
	return out, sc.Err()
}

// coverList is every package in the module, relative to it.
func coverList() ([]string, error) {
	cmd := exec.Command("go", "list", "-f", "{{.ImportPath}}", "./...")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var pkgs []string
	for _, line := range strings.Fields(string(raw)) {
		pkgs = append(pkgs, strings.TrimPrefix(strings.TrimPrefix(line, gatekit.ModulePath), "/"))
	}
	slices.Sort(pkgs)
	return pkgs, nil
}

// coverScore is the table and the problems, from the profile's blocks
// and the package list.
func coverScore(blocks []coverBlock, packages []string, hasCode func(pkg string) bool) (lines, problems []string) {
	statements := map[string]map[string]int{}
	hit := map[string]bool{}
	for _, b := range blocks {
		if statements[b.dir] == nil {
			statements[b.dir] = map[string]int{}
		}
		statements[b.dir][b.id] = b.statements
		if b.covered {
			hit[b.id] = true
		}
	}
	for pkg, reason := range coverExempt {
		if strings.TrimSpace(reason) == "" {
			problems = append(problems, "the exemption for "+pkg+" has no reason")
		}
		if !slices.ContainsFunc(packages, func(p string) bool { return coverMatches(pkg, p) }) {
			problems = append(problems, "the exemption for "+pkg+" names no package; drop it")
		}
	}
	for pkg, f := range coverFloors {
		if strings.TrimSpace(f.reason) == "" {
			problems = append(problems, "the floor for "+pkg+" has no reason")
		}
		if !slices.Contains(packages, pkg) {
			problems = append(problems, "the floor for "+pkg+" names no package; drop it")
		}
	}

	scored := 0
	for _, pkg := range packages {
		if reason, ok := coverExemption(pkg); ok {
			lines = append(lines, fmt.Sprintf("%-32s  exempt: %s", pkg, reason))
			continue
		}
		total, covered := 0, 0
		for id, n := range statements[pkg] {
			total += n
			if hit[id] {
				covered += n
			}
		}
		if total == 0 {
			// Declarations only, which has nothing to cover, or a package
			// -coverpkg left out, which is a hole: its tests could be
			// gone and nothing would say so.
			lines = append(lines, fmt.Sprintf("%-32s  no statements in the profile", pkg))
			if hasCode(pkg) {
				problems = append(problems, fmt.Sprintf("%s has functions and no statements in the profile; "+
					"-coverpkg does not reach it", pkg))
			}
			continue
		}
		scored++
		pct := 100 * float64(covered) / float64(total)
		floor := coverDefaultFloor
		if f, ok := coverFloors[pkg]; ok {
			floor = f.floor
		}
		lines = append(lines, fmt.Sprintf("%-32s  %5.1f%%  (floor %.0f%%)", pkg, pct, floor))
		if pct < floor {
			problems = append(problems, fmt.Sprintf("%s is at %.1f%%, under its floor of %.0f%%", pkg, pct, floor))
		}
	}
	if scored < coverMinPackages {
		problems = append(problems, fmt.Sprintf("scored %d packages, want at least %d; the profile is not "+
			"covering the module", scored, coverMinPackages))
	}
	if len(blocks) < coverMinBlocks {
		problems = append(problems, fmt.Sprintf("the profile holds %d blocks, want at least %d", len(blocks), coverMinBlocks))
	}
	return lines, problems
}

func coverExemption(pkg string) (string, bool) {
	for key, reason := range coverExempt {
		if coverMatches(key, pkg) {
			return reason, true
		}
	}
	return "", false
}

// coverMatches reports whether an exemption key names a package.
func coverMatches(key, pkg string) bool {
	if tree, ok := strings.CutSuffix(key, "/..."); ok {
		return pkg == tree || strings.HasPrefix(pkg, tree+"/")
	}
	return key == pkg
}

// coverHasCode reports whether a package's non-test Go declares a
// function with a body, which is what puts statements in a profile.
func coverHasCode(pkg string) bool {
	files, err := gatekit.ParseGoDir(token.NewFileSet(), filepath.FromSlash(pkg), parser.SkipObjectResolution)
	if err != nil {
		return true // unreadable is not evidence of nothing to cover
	}
	for _, f := range files {
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Body != nil {
				return true
			}
		}
	}
	return false
}
