package main

import (
	"fmt"
	"io"
	"maps"
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
	mk.WriteString("schema-ack: gates\n\t$(GATES) schema-ack $(BASE) $(HEAD)\n\n")
	mk.WriteString("deps: gates ## (manual)\n\t$(GATES) deps\n\n")
	mk.WriteString("# check: nothing\ncheck: " + strings.Join(prereqs, " ") + " vet descriptions\n")

	ci := "name: ci\non: [push]\njobs:\n  test:\n    steps:\n" + strings.Join(steps, "") +
		"      - name: vet\n        run: |\n          # the tagged code too\n          make vet\n" +
		"      - name: descriptions\n        run: make descriptions\n" +
		"      - uses: actions/upload-artifact@0000000000000000000000000000000000000000 # v7\n" +
		"        with:\n          name: schemas\n          path: schemas.json\n" +
		"  pr:\n    if: github.event_name == 'pull_request'\n    steps:\n" +
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
		{name: "a PR-only target on a push", file: ".github/workflows/ci.yml",
			old: "run: make g08\n", new: "run: make g08 changelog\n", want: "changelog is excused as PR-only and ci.yml runs it on a push to main"},
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
			old: "\t$(GO) vet -tags=evals ./...\n", new: "", want: "scripts/evals/main.go builds only with -tags=evals (//go:build evals && !windows) and `make check` vets no pass that builds it"},
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
		{name: "a floor on the tree's tags", file: "scripts/evals/main.go",
			old: "//go:build evals && !windows\n", new: "",
			want: "read 1 //go:build lines that need -tags (live and evals), want at least 2"},
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

// parityCase is one edited fixture and every problem the gate reports for
// it, or none when want is nil.
type parityCase struct {
	name  string
	edits []docsEdit
	want  []string
}

func checkParityCases(t *testing.T, cases []parityCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files, registry := parityFixture()
			applyEdits(t, files, tc.edits...)
			r, err := parityCheck(repoTree(t, files), registry)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(r.problems, tc.want) {
				t.Errorf("problems\n got %q\nwant %q", r.problems, tc.want)
			}
		})
	}
}

// What a tagged file needs is held to a vet pass on each side that sets
// it, read from the //go:build line rather than from either side's text.
func TestParityHoldsTheTreesTagsToAVetPass(t *testing.T) {
	const (
		lint     = ".golangci.yml"
		evals    = "\t$(GO) vet -tags=evals ./...\n"
		taggedBy = "//go:build integration && slow\n\npackage soak\n"
	)
	checkParityCases(t, []parityCase{
		{"a file an OS tag and a custom tag build, which no pass vets", []docsEdit{
			add("internal/e2e/e2e_test.go", "//go:build e2e && linux\n\npackage e2e\n"),
		}, []string{
			"internal/e2e/e2e_test.go builds only with -tags=e2e (//go:build e2e && linux) and `make check` vets no pass that builds it; a contributor cannot see it break",
			"internal/e2e/e2e_test.go builds only with -tags=e2e (//go:build e2e && linux) and ci.yml vets no pass that builds it; it compiles only on a maintainer's machine",
		}},
		{"a file an OS tag and a custom tag build, which a pass with the custom tag vets", []docsEdit{
			add("internal/e2e/e2e_test.go", "//go:build e2e && linux\n\npackage e2e\n"),
			replace("Makefile", evals, evals+"\t$(GO) vet -tags=e2e ./...\n"),
			replace(lint, "[live, evals]", "[live, evals, e2e]"),
		}, nil},
		{"two tags set by one pass, the flag anywhere among vet's arguments", []docsEdit{
			add("internal/soak/soak_test.go", taggedBy),
			replace("Makefile", evals, evals+"\t$(GO) vet -v -tags=integration,slow ./...\n"),
			replace(lint, "[live, evals]", "[live, evals, integration, slow]"),
		}, nil},
		{"two tags set by two passes", []docsEdit{
			add("internal/soak/soak_test.go", taggedBy),
			replace("Makefile", evals, evals+"\t$(GO) vet -tags=integration ./...\n\t$(GO) vet -tags=slow ./...\n"),
			replace(lint, "[live, evals]", "[live, evals, integration, slow]"),
		}, []string{
			"internal/soak/soak_test.go builds only with -tags=integration,slow (//go:build integration && slow) and `make check` vets no pass that builds it; a contributor cannot see it break",
			"internal/soak/soak_test.go builds only with -tags=integration,slow (//go:build integration && slow) and ci.yml vets no pass that builds it; it compiles only on a maintainer's machine",
		}},
		{"a file either of two tags builds, with one of them vetted", []docsEdit{
			replace("scripts/evals/main.go", "//go:build evals && !windows\n", "//go:build live || evals\n"),
			replace("Makefile", evals, ""),
		}, nil},
		{"a file either of two tags builds, with neither vetted", []docsEdit{
			replace("scripts/evals/main.go", "//go:build evals && !windows\n", "//go:build live || evals\n"),
			replace("Makefile", "\t$(GO) vet -tags=live ./...\n"+evals, ""),
		}, []string{
			"scripts/evals/main.go builds only with -tags=evals or -tags=live (//go:build live || evals) and `make check` vets no pass that builds it; a contributor cannot see it break",
			"scripts/evals/main.go builds only with -tags=evals or -tags=live (//go:build live || evals) and ci.yml vets no pass that builds it; it compiles only on a maintainer's machine",
			"scripts/livegitlab/main.go builds only with -tags=live (//go:build live) and `make check` vets no pass that builds it; a contributor cannot see it break",
			"scripts/livegitlab/main.go builds only with -tags=live (//go:build live) and ci.yml vets no pass that builds it; it compiles only on a maintainer's machine",
		}},
		{"a test that turns vet off, which is not a vet pass", []docsEdit{
			replace("Makefile", evals, "\t$(GO) test -vet=off -tags=evals ./...\n"),
		}, []string{
			"scripts/evals/main.go builds only with -tags=evals (//go:build evals && !windows) and `make check` vets no pass that builds it; a contributor cannot see it break",
			"scripts/evals/main.go builds only with -tags=evals (//go:build evals && !windows) and ci.yml vets no pass that builds it; it compiles only on a maintainer's machine",
		}},
		{"a vet in check's own recipe, which CI does not run", []docsEdit{
			add("internal/soak/soak_test.go", "//go:build soak\n\npackage soak\n"),
			replace("Makefile", " vet descriptions\n", " vet descriptions\n\t$(GO) vet -tags=soak ./...\n"),
			replace(lint, "[live, evals]", "[live, evals, soak]"),
		}, []string{
			"internal/soak/soak_test.go builds only with -tags=soak (//go:build soak) and ci.yml vets no pass that builds it; it compiles only on a maintainer's machine",
		}},
		{"a recipe line's prefixes, which are not part of the command", []docsEdit{
			replace("Makefile", "\t$(GO) vet -tags=live ./...\n", "\t@+$(GO) vet -tags=live ./...\n"),
		}, nil},
		{"a recipe line make goes on past", []docsEdit{
			replace("Makefile", "\t$(GO) vet -tags=live ./...\n", "\t-$(GO) vet -tags=live ./...\n"),
		}, []string{"Makefile:107: vet's recipe line starts with -, so make goes on when it fails"}},
		// A pass compiles a file when the file's line holds with exactly the
		// pass's tags: one more tag can keep it out.
		{"a file another tag keeps out, vetted by a pass without that tag", []docsEdit{
			add("internal/probe/probe.go", "//go:build live && !evals\n\npackage probe\n"),
		}, nil},
		{"a file another tag keeps out, vetted only by a pass with that tag", []docsEdit{
			add("internal/probe/probe.go", "//go:build live && !evals\n\npackage probe\n"),
			replace("Makefile", "\t$(GO) vet -tags=live ./...\n", "\t$(GO) vet -tags=live,evals ./...\n"),
		}, []string{
			"internal/probe/probe.go builds only with -tags=live (//go:build live && !evals) and `make check` vets no pass that builds it; a contributor cannot see it break",
			"internal/probe/probe.go builds only with -tags=live (//go:build live && !evals) and ci.yml vets no pass that builds it; it compiles only on a maintainer's machine",
		}},
	})
}

// The Makefile is read as make reads it: a target's rules merge their
// prerequisites.
func TestParityReadsEveryRuleOfATarget(t *testing.T) {
	checkParityCases(t, []parityCase{
		{"a prerequisite a second check rule adds", []docsEdit{
			replace("Makefile", "deps: gates ## (manual)\n", "check: deps\n\ndeps: gates ## (manual)\n"),
		}, []string{
			"`make check` runs deps and ci.yml never does",
			"the deps gate is manual by the registry, and `make check` runs it; declare it modeCheck",
		}},
	})
}

// A gate called through `go run` is found past the run's build flags.
func TestParityReadsAGateCallPastItsFlags(t *testing.T) {
	checkParityCases(t, []parityCase{
		{"the release and hook calls with build flags", []docsEdit{
			replace(".github/workflows/release.yml", "go run ./scripts/gates release-notes", "go run -trimpath ./scripts/gates release-notes"),
			replace(".githooks/pre-commit", "exec go run ./scripts/gates precommit", "exec go run -tags=hook ./scripts/gates precommit"),
		}, nil},
		// Each line is read as the shell splits it: a quoted call is a word
		// of the echo, and a binary at a line's end calls nothing below it.
		{"a call quoted in an echo, and a binary at a line's end", []docsEdit{
			replace("Makefile", "deps: gates ## (manual)\n", "info:\n\t@echo \"run $(GATES) ghost now\"\n\tls $(GATES)\n\tphantom --version\n\ndeps: gates ## (manual)\n"),
		}, nil},
		// A command substitution holds a command; a comment holds none.
		{"a call in a command substitution", []docsEdit{
			replace(".github/workflows/release.yml", "run: go run ./scripts/gates release-notes v1 > notes",
				`run: NOTES="$(go run ./scripts/gates release-notes v1)"`),
		}, nil},
		{"a gate named only in a recipe's comment", []docsEdit{
			replace("Makefile", "\t$(GATES) g05\n", "\t$(GATES) g04 # was: $(GATES) g05\n"),
		}, []string{
			"the g05 gate runs in check by the registry, and no target `make check` runs calls it",
			"the g05 gate runs in check by the registry, and ci.yml never calls it",
		}},
	})
}

// The tags each file needs: none where the go command sets them, and each
// smallest custom set otherwise, once per //go:build expression.
func TestParityTreeTags(t *testing.T) {
	files := map[string]string{
		"a/live.go":         "//go:build live && unix\n\npackage a\n",
		"a/other.go":        "//go:build !windows\n\npackage a\n",
		"a/windows.go":      "//go:build windows\n\npackage a\n",
		"a/solaris.go":      "//go:build solaris || illumos || aix\n\npackage a\n",
		"a/e2e.go":          "//go:build e2e && !experimental\n\npackage a\n",
		"a/race.go":         "//go:build evals || !race\n\npackage a\n",
		"a/arch.go":         "//go:build amd64.v2 && go1.22\n\npackage a\n",
		"a/exp.go":          "//go:build goexperiment.synctest\n\npackage a\n",
		"a/instrumented.go": "//go:build msan && asan\n\npackage a\n",
		"a/racy.go":         "//go:build race\n\npackage a\n",
		"a/mixed.go":        "//go:build (a && b) || c\n\npackage a\n",
		"a/live2.go":        "//go:build live\n\npackage a\n",
		"a/ignored.go":      "//go:build ignore\n\npackage a\n",
		"a/both.go":         "//go:build integration && slow\n\npackage a\n",
		"a/either.go":       "//go:build alpha || beta\n\npackage a\n",
		"a/never.go":        "//go:build stress && linux && windows\n\npackage a\n",
		"a/plain.go":        "package a\n",
		"a/notes.txt":       "//go:build prose\n",
		"a/testdata/x.go":   "//go:build fixture\n\npackage x\n",
	}
	needs, err := parityTreeTags(repoTree(t, files))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range needs {
		got = append(got, n.file+": "+n.String())
	}
	want := []string{
		"a/both.go: -tags=integration,slow",
		"a/e2e.go: -tags=e2e",
		"a/either.go: -tags=alpha or -tags=beta",
		"a/live.go: -tags=live",
		"a/live2.go: -tags=live",
		"a/mixed.go: -tags=c or -tags=a,b",
		"a/never.go: -tags=stress",
	}
	if !slices.Equal(got, want) {
		t.Errorf("parityTreeTags = %q, want %q", got, want)
	}
}

func TestParityVetPassesReadTheTagsFlagAnywhere(t *testing.T) {
	got := parityVetPasses([]string{
		"go vet -tags=live ./...",
		"go vet -v -tags a,b ./... && go test -tags=evals ./...",
		`go vet --tags="x" ./...; go vet ./...`,
		"go vet --tags y ./...",
		"make vet",
		"go test -vet=off -tags=live ./...",
		"go vet ./... -tags=z",
		"go vet -vettool /usr/local/bin/shadow -tags=sh ./...",
		"go vet -c 2 -printf.funcs Wrapf -tags c1 ./...",
	})
	want := [][]string{{"live"}, {"a", "b"}, {"x"}, {"y"}, {"sh"}, {"c1"}}
	if !slices.EqualFunc(got, want, slices.Equal[[]string]) {
		t.Errorf("parityVetPasses = %q, want %q", got, want)
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

func TestMakefileReadsAsMakeDoes(t *testing.T) {
	mk := parseMakefile(textLines(
		"A ?= one",
		"A ?= two",
		"B = x#comment",
		"B += y",
		"C += z",
		"export D := d",
		"override E = e",
		"F := first",
		"F = second",
		`G = docs/architecture.md\#d-88 # note`,
		`H = a\\#b`,
		`I = a\\\#b # c`,
		"check: a",
		"\techo one",
		"check: b a",
		"check:",
		"lint:",
		"\techo old",
		"lint:",
		"\techo new",
		"quiet:",
		"\t@-echo one",
		"\t+ echo two",
	))
	// \# is a literal #, and a run of backslashes before a # is halved, as
	// GNU make 4.4.1 reads them.
	wantVars := map[string]string{"A": "one", "B": "x y", "C": "z", "D": "d", "E": "e", "F": "second",
		"G": "docs/architecture.md#d-88", "H": `a\`, "I": `a\#b`}
	if !maps.Equal(mk.vars, wantVars) {
		t.Errorf("vars = %q, want %q", mk.vars, wantVars)
	}
	if got := mk.targets["check"].prereqs; !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("check's prerequisites = %q, want [a b]: a target's rules merge them, each once", got)
	}
	if got := mk.recipe("check"); !slices.Equal(got, []string{"echo one"}) {
		t.Errorf("check's recipe = %q, want [echo one]: a rule with no recipe leaves the one before", got)
	}
	if got := mk.recipe("lint"); !slices.Equal(got, []string{"echo new"}) {
		t.Errorf("lint's recipe = %q, want [echo new]: the last recipe is the one that runs", got)
	}
	if got := mk.recipe("quiet"); !slices.Equal(got, []string{"echo one", "echo two"}) {
		t.Errorf("quiet's recipe = %q, want [echo one echo two]: @, - and + are not part of a command", got)
	}
}

// The definition-of-done block is read as the docs gate reads a fence.
func TestChecklistReadsTheFence(t *testing.T) {
	for doc, want := range map[string]string{
		"## Definition of done\n\n~~~\na b\n~~~\n":                     "[a b]",
		"## Definition of done\n\n````\na\n```\nb\n````\n":             "[a ``` b]",
		"## Definition of done\n\nNone.\n\n## Next\n\n```\nt99\n```\n": `CLAUDE.md: no fenced block under "## Definition of done"`,
		"## Definition of done\n\n```\na b\n":                          `CLAUDE.md: the fenced block under "## Definition of done" is not closed`,
		"```\n## Definition of done\n```\n":                            `CLAUDE.md has no "## Definition of done" heading`,
	} {
		words, err := checklistDocumented(doc)
		got := fmt.Sprint(words)
		if err != nil {
			got = err.Error()
		}
		if got != want {
			t.Errorf("checklistDocumented(%q) = %s, want %s", doc, got, want)
		}
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

// What a push to main runs is what `make check` is held to, and a CI line
// or a recipe that lets a failure pass is refused.
func TestParityHoldsCheckToWhatAPushRuns(t *testing.T) {
	const ci = ".github/workflows/ci.yml"
	notRun := []string{
		"`make check` runs g00 and ci.yml never does",
		"the g00 gate runs in check by the registry, and ci.yml never calls it",
	}
	checkParityCases(t, []parityCase{
		{"a step a pull request alone runs", []docsEdit{
			replace(ci, "      - name: g00\n", "      - name: g00\n        if: github.event_name == 'pull_request'\n"),
		}, notRun},
		{"a step whose failure passes", []docsEdit{
			replace(ci, "      - name: g00\n", "      - name: g00\n        continue-on-error: true\n"),
		}, notRun},
		{"a step that never runs", []docsEdit{
			replace(ci, "      - name: g00\n", "      - name: g00\n        if: false\n"),
		}, notRun},
		{"a step a matrix filter runs", []docsEdit{
			replace(ci, "      - name: g00\n", "      - name: g00\n        if: matrix.os == 'ubuntu-latest'\n"),
		}, nil},
		{"a CI line that passes whatever it ran", []docsEdit{
			replace(ci, "        run: make g00\n", "        run: make g00 || true\n"),
		}, []string{"ci.yml job test runs `make g00 || true`, where `|| true` passes whatever came before, so a failing check passes"}},
		{"a CI line that runs a check in the background", []docsEdit{
			replace(ci, "        run: make g00\n", "        run: make g00 2>&1 &\n"),
		}, []string{"ci.yml job test runs `make g00 2>&1 &`, where & runs a command in the background, " +
			"where its failure is never seen, so a failing check passes"}},
		{"a recipe that passes whatever it ran", []docsEdit{
			replace("Makefile", "\t$(GATES) g00\n", "\t$(GATES) g00 || true\n"),
		}, []string{"Makefile:11: g00's recipe line is one where `|| true` passes whatever came before, so a failing check passes"}},
		{".IGNORE", []docsEdit{
			replace("Makefile", "vet:\n", ".IGNORE: g00\nvet:\n"),
		}, []string{"Makefile: .IGNORE makes make ignore a failing command; drop it"}},
		{"a line the Makefile reader does not read", []docsEdit{
			replace("Makefile", "vet:\n", "include other.mk\nvet:\n"),
		}, []string{`Makefile:105: "include other.mk" is a directive or an assignment, which this reader does not read`}},
	})
}
