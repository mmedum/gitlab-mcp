package main

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
)

// parityTestGates is how many gates the fixture's check runs: enough to
// clear every floor.
const parityTestGates = 24

// parityFixture is a repository and registry that agree. A test edits
// one of them to break one thing.
func parityFixture() (map[string]string, map[string]command) {
	noop := func(io.Writer, []string) error { return nil }
	registry := map[string]command{
		"changelog":     {run: noop, mode: modePR, reason: "pr"},
		"merge-base":    {run: noop, mode: modePR, reason: "pr"},
		"schema-ack":    {run: noop, mode: modePR, reason: "pr"},
		"release-notes": {run: noop, mode: modeRelease, reason: "release"},
		"mcpb-pack":     {run: noop, mode: modeRelease, reason: "release"},
		"deps":          {run: noop, mode: modeManual, reason: "network"},
		"precommit":     {run: noop, mode: modeManual, reason: "hook"},
		"descriptions":  {run: noop, mode: modeCheck},
	}
	var mk strings.Builder
	mk.WriteString("GO ?= go\nEXE := $(if $(filter Windows_NT,$(OS)),.exe,)\nGATES ?= ./.gates$(EXE)\nSCHEMAS ?= schemas.json\n\n")
	mk.WriteString("gates:\n\t$(GO) build -o $(GATES) ./scripts/gates\n\n")
	var prereqs, steps []string
	for i := range parityTestGates {
		name := fmt.Sprintf("g%02d", i)
		registry[name] = command{run: noop, mode: modeCheck}
		prereqs = append(prereqs, name)
		fmt.Fprintf(&mk, ".PHONY: %s\n%s: gates ## a gate\n\t$(GATES) %s\n\n", name, name, name)
		steps = append(steps, fmt.Sprintf("      - name: %s\n        run: make %s\n", name, name))
	}
	mk.WriteString("vet:\n\t$(GO) vet ./...\n\t$(GO) vet -tags=live ./...\n\t$(GO) vet -tags=evals ./...\n\n")
	mk.WriteString("schemas:\n\t./bin --dump-schemas > $(SCHEMAS)\n\n")
	mk.WriteString("descriptions: schemas gates\n\t$(GATES) descriptions $(SCHEMAS)\n\n")
	mk.WriteString("merge-base: gates\n\t$(GATES) merge-base $(BASE) $(HEAD)\n\n")
	mk.WriteString("changelog: gates\n\t$(GATES) changelog $(BASE) $(HEAD)\n\n")
	mk.WriteString("schema-ack: gates\n\t$(GATES) schema-ack ./bin $(BASE) $(HEAD)\n\n")
	mk.WriteString("deps: gates ## (manual)\n\t$(GATES) deps\n\n")
	mk.WriteString("# check: nothing\ncheck: " + strings.Join(prereqs, " ") + " vet descriptions\n")

	ci := "name: ci\non: [push]\njobs:\n  test:\n    steps:\n" + strings.Join(steps, "") +
		"      - name: vet\n        run: |\n          # the tagged code too\n          make vet\n" +
		"      - name: descriptions\n        run: make descriptions\n" +
		"      - uses: actions/upload-artifact@0000000000000000000000000000000000000000 # v7\n" +
		"        with:\n          name: schemas\n          path: schemas.json\n" +
		"  pr:\n    steps:\n" +
		"      - name: base\n        run: make merge-base\n" +
		"      - name: changelog\n        run: make changelog\n" +
		"      - name: ack\n        run: make schema-ack\n"
	return map[string]string{
		"Makefile":                      mk.String(),
		".github/workflows/ci.yml":      ci,
		".github/workflows/release.yml": "on: [push]\njobs:\n  r:\n    steps:\n      - run: go run ./scripts/gates release-notes v1 > notes\n",
		".goreleaser.yaml":              "# go run ./scripts/gates nothing\npost: go run ./scripts/gates mcpb-pack dist 1 out\n",
		".githooks/pre-commit":          "#!/bin/sh\nexec go run ./scripts/gates precommit\n",
		".golangci.yml":                 "run:\n  build-tags: [live, evals]\n",
		"scripts/livegitlab/main.go":    "//go:build live\n\npackage main\n",
		"scripts/evals/main.go":         "//go:build evals && !windows\n\npackage main\n",
		"internal/x/x_unix.go":          "//go:build unix\n\npackage x\n",
	}, registry
}

func TestParityPassesWhenTheyAgree(t *testing.T) {
	files, registry := parityFixture()
	r, err := parityCheck(repoTree(t, files), registry)
	if err != nil {
		t.Fatal(err)
	}
	wantClean(t, r.problems)
	if r.prereqs != parityTestGates+2 || r.checkGates != parityTestGates+1 || r.vetTags != 2 {
		t.Errorf("read %d prerequisites, %d gates, %d vet tags", r.prereqs, r.checkGates, r.vetTags)
	}
}

func TestParityRefuses(t *testing.T) {
	cases := []struct {
		name     string
		file     string
		old, new string
		registry func(map[string]command)
		want     string
	}{
		{name: "a check target CI does not run", file: ".github/workflows/ci.yml",
			old: "run: make g03\n", new: "run: make g04\n", want: "`make check` runs g03 and ci.yml never does"},
		{name: "a step that is not a make target", file: ".github/workflows/ci.yml",
			old: "run: make g03\n", new: "run: go run ./scripts/gates g03\n", want: "which is not a make target"},
		{name: "a step's name is not a command", file: ".github/workflows/ci.yml",
			old: "      - name: g04\n        run: make g04\n", new: "      - name: make g04\n        run: make g05\n",
			want: "`make check` runs g04 and ci.yml never does"},
		{name: "a comment is not a command", file: ".github/workflows/ci.yml",
			old: "run: make g05\n", new: "run: |\n          # make g05\n          make g06\n",
			want: "`make check` runs g05 and ci.yml never does"},
		{name: "a CI target check does not run", file: ".github/workflows/ci.yml",
			old: "run: make g07\n", new: "run: make g07 deps\n",
			want: "ci.yml runs `make deps` and `make check` does not"},
		{name: "a stale PR-only excuse", file: ".github/workflows/ci.yml",
			old: "run: make merge-base\n", new: "run: make g01\n", want: "merge-base is excused as PR-only and ci.yml no longer runs it"},
		{name: "a PR-only target outside the pr job", file: ".github/workflows/ci.yml",
			old: "run: make g08\n", new: "run: make g08 changelog\n", want: "changelog is excused as PR-only and ci.yml runs it outside the pr job"},
		{name: "a prerequisite named as a gate", file: "Makefile",
			old: "vet descriptions\n", new: "vet descriptions schemas\n", want: "schemas is excused as a prerequisite and check: names it"},
		{name: "a check gate no target calls", registry: func(r map[string]command) {
			r["ghost"] = command{mode: modeCheck}
		}, want: "the ghost gate runs in check by the registry, and no target"},
		{name: "a gate called and not registered", registry: func(r map[string]command) {
			delete(r, "g09")
		}, want: "something runs `gates g09`, which the registry does not have"},
		{name: "a manual gate that check runs", registry: func(r map[string]command) {
			r["g10"] = command{mode: modeManual, reason: "x"}
		}, want: "the g10 gate is manual by the registry, and `make check` runs it"},
		{name: "a manual gate nothing runs", registry: func(r map[string]command) {
			r["refetch"] = command{mode: modeManual, reason: "x"}
		}, want: "the refetch gate is manual by the registry, and no Makefile target or hook runs it"},
		{name: "a PR gate CI does not run", file: "Makefile",
			old: "$(GATES) changelog", new: "$(GATES) g01", want: "the changelog gate runs on pull requests by the registry"},
		{name: "a release gate named only in a comment", file: ".goreleaser.yaml",
			old: "post: go run ./scripts/gates mcpb-pack", new: "# go run ./scripts/gates mcpb-pack", want: "the mcpb-pack gate runs in release by the registry"},
		{name: "a release gate with no reason", registry: func(r map[string]command) {
			r["sign"] = command{mode: modeRelease, reason: "x"}
		}, want: "the sign gate runs in release and parityReleaseOnly gives no reason"},
		{name: "a gate out of check without a reason", registry: func(r map[string]command) {
			r["deps"] = command{mode: modeManual}
		}, want: "the deps gate is not in check and gives no reason"},
		{name: "a vet tag check does not have", file: "Makefile",
			old: "\t$(GO) vet -tags=evals ./...\n", new: "", want: "scripts/evals/main.go is built with -tags=evals and nothing vets it"},
		{name: "a vet tag lint does not see", file: ".golangci.yml",
			old: "[live, evals]", new: "[live]", want: "`make check` vets -tags=evals and .golangci.yml"},
		{name: "a file after the binary is not a gate", file: "Makefile",
			old: "gates:\n", new: "clean:\n\trm $(GATES) cov.out\n\ngates:\n", want: ""},
		{name: "the zero mode", registry: func(r map[string]command) {
			r["g01"] = command{}
		}, want: "the g01 gate declares no mode"},
		{name: "the hook", file: ".githooks/pre-commit", old: "exec go run ./scripts/gates precommit", new: "# go run ./scripts/gates precommit",
			want: "does not run `go run ./scripts/gates precommit`"},
		{name: "the artifact nobody writes", file: "Makefile",
			old: "> $(SCHEMAS)", new: "", want: "ci.yml uploads schemas.json and no target it runs writes it"},
		{name: "a floor on prerequisites", file: "Makefile",
			old: "check: g00 g01 g02 g03", new: "check:", want: "read 22 check: prerequisites, want at least 25"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files, registry := parityFixture()
			if tc.file != "" {
				breakFile(t, files, tc.file, tc.old, tc.new)
			}
			if tc.registry != nil {
				tc.registry(registry)
			}
			r, err := parityCheck(repoTree(t, files), registry)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				wantClean(t, r.problems)
				return
			}
			wantProblem(t, r.problems, tc.want)
		})
	}
}

// Every release command in the real registry names where it runs.
func TestParityExcusesEveryReleaseCommand(t *testing.T) {
	var release []string
	for name, c := range commands {
		if c.mode == modeRelease {
			release = append(release, name)
		}
	}
	if len(release) == 0 {
		t.Fatal("the registry has no release commands; this test would hold nothing")
	}
	for _, name := range release {
		if strings.TrimSpace(parityReleaseOnly[name]) == "" {
			t.Errorf("%s runs in release and parityReleaseOnly gives no reason", name)
		}
	}
	for name := range parityReleaseOnly {
		if !slices.Contains(release, name) {
			t.Errorf("parityReleaseOnly excuses %s, which is not a release command", name)
		}
	}
	for name, reason := range parityPROnly {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%s is excused as PR-only with no reason", name)
		}
	}
}

func TestParityMakeTargets(t *testing.T) {
	got := parityMakeTargets(`make changelog PR_BASE_REF="origin/${BASE_REF}" -j2 cover`)
	if !slices.Equal(got, []string{"changelog", "cover"}) {
		t.Errorf("parityMakeTargets = %v", got)
	}
	if parityMakeTargets("go test ./...") != nil {
		t.Error("a go command was read as make")
	}
}

func TestMakefileParse(t *testing.T) {
	mk := parseMakefile("A ?= x\nB = $(A)/y # note\nC := $(B) \\\n  z\n.PHONY: t\nt: u v ## help\n\t$(C) run\n\t# comment\nu:\n\techo\n")
	if got := mk.expand("$(B)"); got != "x/y" {
		t.Errorf("expand = %q", got)
	}
	if got := mk.recipe("t"); !slices.Equal(got, []string{"x/y z run"}) {
		t.Errorf("recipe = %q", got)
	}
	if got := mk.closure("t"); !slices.Equal(got, []string{"t", "u", "v"}) {
		t.Errorf("closure = %v", got)
	}
	if _, err := mk.variable("NONE"); err == nil {
		t.Error("a missing variable was found")
	}
}

func TestChecklist(t *testing.T) {
	var targets []string
	for i := range 26 {
		targets = append(targets, fmt.Sprintf("t%02d", i))
	}
	files := map[string]string{
		"Makefile":  "check: " + strings.Join(targets, " ") + "\n",
		"CLAUDE.md": "# x\n\n## Definition of done\n\n`make check`:\n\n```\n" + strings.Join(targets, " ") + "\n```\n\n## Next\n\n```\nt99\n```\n",
	}
	n, problems, err := checklistCheck(repoTree(t, files))
	if err != nil {
		t.Fatal(err)
	}
	wantClean(t, problems)
	if n != 26 {
		t.Errorf("read %d prerequisites, want 26", n)
	}

	cases := []struct{ name, file, old, new, want string }{
		{"missing from the doc", "CLAUDE.md", "t03 ", "", "check: runs t03 and CLAUDE.md does not name it"},
		{"extra in the doc", "CLAUDE.md", "t03 ", "t03 lint ", "CLAUDE.md names lint and check: does not run it"},
		{"named twice", "CLAUDE.md", "t03 ", "t03 t03 ", "CLAUDE.md names t03 twice"},
		{"another order", "CLAUDE.md", "t03 t04", "t04 t03", "in another order"},
		{"a floor", "Makefile", "t00 t01 t02 ", "", "read 23 check: prerequisites, want at least 25"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := map[string]string{"Makefile": files["Makefile"], "CLAUDE.md": files["CLAUDE.md"]}
			breakFile(t, f, tc.file, tc.old, tc.new)
			if tc.file == "Makefile" {
				breakFile(t, f, "CLAUDE.md", "t00 t01 t02 ", "")
			}
			_, problems, err := checklistCheck(repoTree(t, f))
			if err != nil {
				t.Fatal(err)
			}
			wantProblem(t, problems, tc.want)
		})
	}
}
