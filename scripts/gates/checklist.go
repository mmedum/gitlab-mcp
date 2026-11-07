package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mmedum/gitlab-mcp/v4/scripts/internal/gatekit"
)

// The checklist gate holds CLAUDE.md's definition of done to the
// Makefile's check: prerequisites, both ways. A reader who runs the
// documented list by hand must run what `make check` runs.

const (
	checklistDoc     = "CLAUDE.md"
	checklistHeading = "## Definition of done"
)

func checklist(out io.Writer, _ []string) error {
	n, problems, err := checklistCheck(".")
	if err != nil {
		return err
	}
	if err := gatekit.Problems(out, checklistDoc+"'s definition of done", problems); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "checklist ok: %s names all %d of check:'s prerequisites, in order\n", checklistDoc, n)
	return nil
}

func checklistCheck(root string) (int, []string, error) {
	mk, err := readMakefile(filepath.Join(root, parityMakefile))
	if err != nil {
		return 0, nil, err
	}
	check, ok := mk.targets["check"]
	if !ok {
		return 0, nil, fmt.Errorf("%s has no check: target", parityMakefile)
	}
	doc, err := os.ReadFile(filepath.Join(root, checklistDoc))
	if err != nil {
		return 0, nil, err
	}
	listed, err := checklistDocumented(string(doc))
	if err != nil {
		return 0, nil, err
	}
	want := check.prereqs

	var problems []string
	for _, t := range want {
		if !slices.Contains(listed, t) {
			problems = append(problems, fmt.Sprintf("check: runs %s and %s does not name it", t, checklistDoc))
		}
	}
	seen := map[string]bool{}
	for _, t := range listed {
		if seen[t] {
			problems = append(problems, fmt.Sprintf("%s names %s twice", checklistDoc, t))
		}
		seen[t] = true
		if !slices.Contains(want, t) {
			problems = append(problems, fmt.Sprintf("%s names %s and check: does not run it", checklistDoc, t))
		}
	}
	if len(problems) == 0 && !slices.Equal(listed, want) {
		problems = append(problems, fmt.Sprintf("%s lists check:'s prerequisites in another order; "+
			"keep one order so the two read the same", checklistDoc))
	}
	problems = append(problems, gatekit.Floor("check: prerequisites", len(want), 25)...)
	return len(want), problems, nil
}

// checklistDocumented is the words of the first fenced block under the
// definition-of-done heading, read as the docs gate reads a fence.
func checklistDocumented(doc string) ([]string, error) {
	lines, prose := strings.Split(doc, "\n"), docsProse(doc)
	start := slices.Index(prose, checklistHeading)
	if start < 0 {
		return nil, fmt.Errorf("%s has no %q heading", checklistDoc, checklistHeading)
	}
	var fence docsFence
	var block []string
	opened, closed := false, false
	for _, line := range lines[start+1:] {
		if !fence.code(line) {
			if level := docsHeadingLevel(line); level == 1 || level == 2 {
				break
			}
			continue
		}
		if !opened {
			opened = true // the opening line, with its info string
			continue
		}
		if fence.open == "" {
			closed = true
			break
		}
		block = append(block, line)
	}
	switch {
	case !opened:
		return nil, fmt.Errorf("%s: no fenced block under %q", checklistDoc, checklistHeading)
	case !closed:
		return nil, fmt.Errorf("%s: the fenced block under %q is not closed", checklistDoc, checklistHeading)
	}
	words := strings.Fields(strings.Join(block, "\n"))
	if len(words) == 0 {
		return nil, fmt.Errorf("%s: the fenced block under %q is empty", checklistDoc, checklistHeading)
	}
	return words, nil
}
