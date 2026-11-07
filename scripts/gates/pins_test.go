package main

import (
	"slices"
	"strings"
	"testing"
)

const pinsSHA1 = "1111111111111111111111111111111111111111"

// pinsFixture is a repository whose pins agree.
func pinsFixture() map[string]string {
	checkout := "      - uses: actions/checkout@" + pinsSHA1 + " # v7.0.1\n"
	setup := "      - uses: actions/setup-go@" + pinsSHA1 + " # v7.0.0\n        with:\n          go-version-file: go.mod\n"
	head := "on: [push]\ndefaults:\n  run:\n    shell: bash\n"
	return map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.27.1\n\nrequire (\n\tgithub.com/modelcontextprotocol/go-sdk v1.8.0\n)\n",
		"Makefile": "GOLANGCI_LINT ?= github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0\n" +
			"GOVULNCHECK ?= golang.org/x/vuln/cmd/govulncheck@v1.8.0\n" +
			"GOLICENSES ?= github.com/google/go-licenses@v1.6.0\n" +
			"GITLEAKS ?= github.com/zricethezav/gitleaks/v8@v8.30.1\n" +
			"ACTIONLINT ?= github.com/rhysd/actionlint/cmd/actionlint@v1.7.12\n" +
			"GORELEASER ?= github.com/goreleaser/goreleaser/v2@v2.18.2\n" +
			"lint:\n\tgo run $(GOLANGCI_LINT) run\nvuln:\n\tgo run $(GOVULNCHECK) ./...\n" +
			"licenses:\n\tgo run $(GOLICENSES) check\nsecrets:\n\tgo run $(GITLEAKS) dir .\n" +
			"actionlint:\n\tgo run $(ACTIONLINT)\nrehearse:\n\tgo run $(GORELEASER) release\n",
		".github/workflows/ci.yml": "name: ci\n" + head + "jobs:\n  test:\n    steps:\n" + checkout + setup +
			"      - run: make check\n",
		".github/workflows/codeql.yml": "name: codeql\n" + head + "jobs:\n  a:\n    steps:\n" + checkout +
			"      - uses: github/codeql-action/init@" + pinsSHA1 + " # v4.38.2\n" +
			"      - uses: github/codeql-action/analyze@" + pinsSHA1 + " # v4.38.2\n",
		".github/workflows/release.yml": "name: release\n" + head + "env:\n  GORELEASER_VERSION: v2.18.2\njobs:\n  r:\n    steps:\n" +
			checkout + setup +
			"      - uses: sigstore/cosign-installer@" + pinsSHA1 + " # v4.1.2\n        with:\n          cosign-release: v3.1.3\n" +
			"      - uses: anchore/sbom-action/download-syft@" + pinsSHA1 + " # v0.24.2\n        with:\n          syft-version: v1.52.0\n" +
			"      - uses: goreleaser/goreleaser-action@" + pinsSHA1 + " # v7.2.3\n        with:\n          version: ${{ env.GORELEASER_VERSION }}\n" +
			"      - uses: actions/attest-build-provenance@" + pinsSHA1 + " # v4.2.2\n",
		".github/workflows/publish-mcp.yml": "name: publish\n" + head + "jobs:\n  p:\n    steps:\n" + checkout +
			"      - uses: sigstore/cosign-installer@" + pinsSHA1 + " # v4.1.2\n        with:\n          cosign-release: v3.1.3\n" +
			"      - env:\n          PUBLISHER_VERSION: v1.8.1\n        run: echo\n",
		"scripts/gates/precommit.go": "package main\n\n// reads $(GITLEAKS) from the Makefile\n",
	}
}

func TestPinsPassesWhenTheyAgree(t *testing.T) {
	r, err := pinsCheck(repoTree(t, pinsFixture()))
	if err != nil {
		t.Fatal(err)
	}
	wantClean(t, r.problems)
	// A setup-go in ci.yml and release.yml, cosign in release.yml and
	// publish-mcp.yml, and syft and goreleaser in release.yml.
	if r.workflows != 4 || r.installers != 6 || r.actions != 13 {
		t.Errorf("read %d workflows, %d installers, %d actions; want 4, 6, 13", r.workflows, r.installers, r.actions)
	}
}

func TestPinsRefuses(t *testing.T) {
	cases := []struct{ name, file, old, new, want string }{
		{"a tag, not a SHA", ".github/workflows/ci.yml", "actions/checkout@" + pinsSHA1, "actions/checkout@v7",
			`actions/checkout is pinned to "v7", a tag or branch`},
		{"a SHA with no comment", ".github/workflows/ci.yml", pinsSHA1 + " # v7.0.1", pinsSHA1,
			"with no comment saying which version"},
		{"a bundle tag label", ".github/workflows/codeql.yml", "init@" + pinsSHA1 + " # v4.38.2",
			"init@" + pinsSHA1 + " # codeql-bundle-v2.20.0", "a query-bundle tag"},
		{"an installer without its tool pin", ".github/workflows/release.yml", "          syft-version: v1.52.0\n", "",
			"does not set syft-version"},
		{"a range", ".github/workflows/release.yml", "cosign-release: v3.1.3", "cosign-release: '~> v3'",
			"cosign is pinned to \"~> v3\""},
		{"latest", "Makefile", "govulncheck@v1.8.0", "govulncheck@latest", "GOVULNCHECK is pinned to @latest"},
		{"two versions of one tool", ".github/workflows/publish-mcp.yml", "cosign-release: v3.1.3", "cosign-release: v3.1.4",
			"cosign is v3.1.4 at .github/workflows/publish-mcp.yml"},
		{"env resolved against the Makefile", ".github/workflows/release.yml", "GORELEASER_VERSION: v2.18.2", "GORELEASER_VERSION: v2.18.1",
			"goreleaser is"},
		{"an unclassified action", ".github/workflows/ci.yml", "      - run: make check\n",
			"      - uses: someone/tool@" + pinsSHA1 + " # v1.0.0\n", "someone/tool is not classified"},
		{"an unknown version env", ".github/workflows/ci.yml", "jobs:\n", "env:\n  WIDGET_VERSION: v1.0.0\njobs:\n",
			"WIDGET_VERSION names a version of a tool pinsTools does not know"},
		{"no workflow shell", ".github/workflows/codeql.yml", "defaults:\n  run:\n    shell: bash\n", "",
			"no workflow-level `defaults: run: shell:`"},
		{"a Makefile pin nothing runs", "Makefile", "actionlint:\n\tgo run $(ACTIONLINT)\n", "",
			"Makefile: ACTIONLINT pins github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 and no recipe runs it with the go command, " +
				"so the tool runs from the PATH"},
		{"a copy in Go source", "scripts/gates/precommit.go", "// reads", "const gitleaks = \"github.com/zricethezav/gitleaks/v8@v8.29.0\"\n\n// reads",
			"gitleaks is v8.30.1 at"},
		{"Go source that does not parse", "scripts/gates/precommit.go", "// reads", "func {\n// reads", "precommit.go:3:"},
		// GitHub documents anchors and aliases and not merge keys, which
		// yaml.v3 would merge where GitHub may not.
		{"a step built with a merge key", ".github/workflows/ci.yml", "      - run: make check\n",
			"      - &co\n        uses: actions/checkout@" + pinsSHA1 + " # v7.0.1\n      - <<: *co\n        name: again\n",
			".github/workflows/ci.yml:15: GitHub Actions does not document merge keys; write the mapping out"},
		{"a merge key in a flow mapping", ".github/workflows/ci.yml", "      - run: make check\n",
			"      - <<: {uses: actions/checkout@v4}\n", ".github/workflows/ci.yml:13: GitHub Actions does not document merge keys"},
		{"a merge key in a job's env", ".github/workflows/ci.yml", "  test:\n", "  test:\n    env:\n      <<: {A: b}\n",
			".github/workflows/ci.yml:9: GitHub Actions does not document merge keys"},
		{"a required workflow missing", ".github/workflows/codeql.yml", "name: codeql", "name: codeql\nbroken: [",
			"not valid YAML"},
		{"a tool pinned nowhere", ".github/workflows/publish-mcp.yml", "PUBLISHER_VERSION: v1.8.1", "OTHER: v1.8.1",
			"mcp-publisher is pinned nowhere"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := pinsFixture()
			breakFile(t, files, tc.file, tc.old, tc.new)
			r, err := pinsCheck(repoTree(t, files))
			if err != nil {
				if strings.Contains(err.Error(), tc.want) {
					return
				}
				t.Fatal(err)
			}
			wantProblem(t, r.problems, tc.want)
		})
	}
}

// Every reference is counted on the parse, an image and a flow mapping
// included.
func TestPinsCountsEveryReference(t *testing.T) {
	files := pinsFixture()
	breakFile(t, files, ".github/workflows/ci.yml", "      - run: make check\n",
		"      - uses: docker://alpine@sha256:"+strings.Repeat("0", 64)+"\n      - {uses: actions/checkout@"+pinsSHA1+"} # v7.0.1\n")
	r, err := pinsCheck(repoTree(t, files))
	if err != nil {
		t.Fatal(err)
	}
	wantClean(t, r.problems)
	if r.actions != 15 {
		t.Errorf("read %d action references, want 15", r.actions)
	}
}

func TestPinsFloors(t *testing.T) {
	files := pinsFixture()
	breakFile(t, files, ".github/workflows/release.yml",
		"      - uses: actions/attest-build-provenance@"+pinsSHA1+" # v4.2.2\n", "")
	breakFile(t, files, ".github/workflows/codeql.yml",
		"      - uses: github/codeql-action/analyze@"+pinsSHA1+" # v4.38.2\n", "")
	breakFile(t, files, ".github/workflows/ci.yml",
		"      - uses: actions/checkout@"+pinsSHA1+" # v7.0.1\n", "")
	breakFile(t, files, ".github/workflows/codeql.yml",
		"      - uses: actions/checkout@"+pinsSHA1+" # v7.0.1\n", "")
	breakFile(t, files, ".github/workflows/publish-mcp.yml",
		"      - uses: actions/checkout@"+pinsSHA1+" # v7.0.1\n", "")
	r, err := pinsCheck(repoTree(t, files))
	if err != nil {
		t.Fatal(err)
	}
	wantProblem(t, r.problems, "read 8 action references, want at least 10")
}

// Each row edits the fixture and wants exactly these problems, or none.
func TestPinsNamesEachProblem(t *testing.T) {
	const (
		ci      = ".github/workflows/ci.yml"
		codeql  = ".github/workflows/codeql.yml"
		release = ".github/workflows/release.yml"
		run     = "      - run: make check\n"
		vuln    = "\tgo run $(GOVULNCHECK) ./...\n"
		goStep  = "          go-version-file: go.mod\n"
		digest  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		pathRun = "Makefile: GOVULNCHECK pins golang.org/x/vuln/cmd/govulncheck@v1.8.0 and no recipe runs it with the go command, " +
			"so the tool runs from the PATH"
		nowhere    = "govulncheck is pinned nowhere: a tool this gate names that no file pins is either gone or unread"
		noDigest   = ", which has no sha256 digest, so the image it runs can change"
		checkout   = "      - uses: actions/checkout@" + pinsSHA1 + " # v7.0.1\n"
		goreleaser = "GORELEASER ?= github.com/goreleaser/goreleaser/v2@v2.18.2\n"
	)
	gorelease := "      - uses: goreleaser/goreleaser-action@" + pinsSHA1 + " # v7.2.3\n        with:\n" +
		"          version: ${{ env.GORELEASER_VERSION }}\n"
	for _, tc := range []struct {
		name  string
		edits []docsEdit
		want  []string
	}{
		{name: "a container image by its digest, quoted", edits: []docsEdit{
			replace(ci, run, "      - uses: \"docker://alpine@sha256:"+digest+"\"\n"+run),
		}},
		{name: "a container image by tag, quoted, and by name alone", edits: []docsEdit{
			replace(ci, run, "      - uses: \"docker://alpine:3.20\"\n      - uses: docker://alpine\n"+run),
		}, want: []string{
			".github/workflows/ci.yml: job test: docker://alpine:3.20 is a container image with no sha256 digest, so the image it runs can change",
			".github/workflows/ci.yml: job test: docker://alpine is a container image with no sha256 digest, so the image it runs can change",
		}},
		{name: "a container image a line scan cannot see: on the next line, and in a flow mapping", edits: []docsEdit{
			replace(ci, run, "      - name: unpinned image\n        uses:\n          docker://ghcr.io/example/img:main\n"+
				"      - {uses: \"docker://alpine:3.20\"}\n"+run),
		}, want: []string{
			".github/workflows/ci.yml: job test: docker://ghcr.io/example/img:main is a container image with no sha256 digest, " +
				"so the image it runs can change",
			".github/workflows/ci.yml: job test: docker://alpine:3.20 is a container image with no sha256 digest, so the image it runs can change",
		}},
		{name: "a container image on the next line, by its digest", edits: []docsEdit{
			replace(ci, run, "      - uses:\n          docker://alpine@sha256:"+digest+"\n"+run),
		}},
		{name: "an action by SHA in single quotes", edits: []docsEdit{
			replace(ci, "      - uses: actions/checkout@"+pinsSHA1+" # v7.0.1\n", "      - uses: 'actions/checkout@"+pinsSHA1+"' # v7.0.1\n"),
		}},
		{name: "an action by tag on the line after uses:, and in a flow mapping", edits: []docsEdit{
			replace(ci, run, "      - uses:\n          actions/checkout@v4\n      - {uses: actions/checkout@v4}\n"+run),
		}, want: []string{
			`.github/workflows/ci.yml:14: actions/checkout is pinned to "v4", a tag or branch that can be moved; pin the 40-character commit SHA`,
			`.github/workflows/ci.yml:15: actions/checkout is pinned to "v4", a tag or branch that can be moved; pin the 40-character commit SHA`,
		}},
		{name: "an action by SHA in a flow mapping and on the line after uses:, with their comments", edits: []docsEdit{
			replace(ci, run, "      - {uses: actions/checkout@"+pinsSHA1+"} # v7.0.1\n      - uses:\n          actions/checkout@"+pinsSHA1+" # v7.0.1\n"+run),
		}},
		{name: "an action by SHA in a flow mapping with no comment", edits: []docsEdit{
			replace(ci, run, "      - {uses: actions/checkout@"+pinsSHA1+"}\n"+run),
		}, want: []string{".github/workflows/ci.yml:13: actions/checkout is pinned by SHA with no comment saying which version it is"}},
		{name: "a reusable workflow by tag", edits: []docsEdit{
			replace(ci, run, run+"  reuse:\n    uses: example/repo/.github/workflows/x.yml@v1\n"),
		}, want: []string{
			`.github/workflows/ci.yml:15: example/repo/.github/workflows/x.yml is pinned to "v1", a tag or branch that can be moved; ` +
				"pin the 40-character commit SHA",
		}},
		{name: "a job's container and service by digest", edits: []docsEdit{
			replace(ci, "  test:\n    steps:\n", "  test:\n    container: alpine@sha256:"+digest+"\n    services:\n      db:\n"+
				"        image: postgres@sha256:"+digest+"\n    steps:\n"),
		}},
		{name: "a job's container by tag, as a string and under image:, and a service by tag", edits: []docsEdit{
			replace(ci, "  test:\n    steps:\n", "  test:\n    container: node:18\n    services:\n      db:\n        image: postgres:16\n    steps:\n"),
			replace(codeql, "  a:\n    steps:\n", "  a:\n    container:\n      image: alpine:3.20\n    steps:\n"),
		}, want: []string{
			".github/workflows/ci.yml: job test runs the container from node:18" + noDigest,
			".github/workflows/ci.yml: job test runs the service db from postgres:16" + noDigest,
			".github/workflows/codeql.yml: job a runs the container from alpine:3.20" + noDigest,
		}},
		{name: "a job's container and its image by an alias", edits: []docsEdit{
			replace(ci, "jobs:\n  test:\n    steps:\n", "env:\n  IMAGE: &img node:18\njobs:\n  test:\n    container: *img\n    steps:\n"),
			replace(codeql, "  a:\n    steps:\n", "  a:\n    container:\n      image: &tag alpine:3.20\n"+
				"    services:\n      db:\n        image: *tag\n    steps:\n"),
		}, want: []string{
			".github/workflows/ci.yml: job test runs the container from node:18" + noDigest,
			".github/workflows/codeql.yml: job a runs the container from alpine:3.20" + noDigest,
			".github/workflows/codeql.yml: job a runs the service db from alpine:3.20" + noDigest,
		}},
		{name: "a Makefile pin whose version is another variable", edits: []docsEdit{
			replace("Makefile", "GORELEASER ?= github.com/goreleaser/goreleaser/v2@v2.18.2\n",
				"GORELEASER_VERSION ?= v2.18.2\nGORELEASER ?= github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)\n"),
		}},
		{name: "a Makefile pin whose version is another variable, at another version", edits: []docsEdit{
			replace("Makefile", "GORELEASER ?= github.com/goreleaser/goreleaser/v2@v2.18.2\n",
				"GORELEASER_VERSION ?= v2.18.1\nGORELEASER ?= github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)\n"),
		}, want: []string{"goreleaser is v2.18.2 at .github/workflows/release.yml: goreleaser/goreleaser-action and " +
			"v2.18.1 at Makefile: GORELEASER: one tool, one version"}},
		{name: "setup-go with no go-version-file", edits: []docsEdit{replace(ci, goStep, "          cache: false\n")}, want: []string{
			".github/workflows/ci.yml: actions/setup-go is pinned by SHA and does not set go-version-file, " +
				"so the tool it installs is whatever is current that morning",
		}},
		{name: "an exact toolchain over a go line that floats", edits: []docsEdit{
			replace("go.mod", "go 1.27.1\n", "go 1.27\n\ntoolchain go1.27.1\n"),
		}},
		{name: "a toolchain that floats over an exact go line", edits: []docsEdit{
			replace("go.mod", "go 1.27.1\n", "go 1.27.1\n\ntoolchain go1.27\n"),
		}, want: []string{
			`.github/workflows/ci.yml: actions/setup-go: go is pinned to "1.27", which is not one exact version`,
			`.github/workflows/release.yml: actions/setup-go: go is pinned to "1.27", which is not one exact version`,
			`go.mod: go: go is pinned to "1.27", which is not one exact version`,
		}},
		{name: "a go.mod that names no Go version", edits: []docsEdit{replace("go.mod", "go 1.27.1\n", "")}, want: []string{
			`.github/workflows/ci.yml: actions/setup-go sets go-version-file to "go.mod", which names no Go version`,
			`.github/workflows/release.yml: actions/setup-go sets go-version-file to "go.mod", which names no Go version`,
			`go.mod: go: go is pinned to "", which is not one exact version`,
		}},
		{name: "setup-go reading a file that is not a go.mod", edits: []docsEdit{
			replace(ci, goStep, "          go-version-file: .go-version\n"),
		}, want: []string{
			`.github/workflows/ci.yml: actions/setup-go sets go-version-file to ".go-version", which is not the repository's go.mod`,
		}},
		{name: "setup-go reading another go.mod", edits: []docsEdit{
			replace(ci, goStep, "          go-version-file: tools/go.mod\n"),
			add("tools/go.mod", "module example.com/tools\n\ngo 1.26.0\n"),
		}, want: []string{
			`.github/workflows/ci.yml: actions/setup-go sets go-version-file to "tools/go.mod", which is not the repository's go.mod`,
		}},
		{name: "a tool the Makefile runs from the PATH", edits: []docsEdit{
			replace("Makefile", vuln, "\tgovulncheck ./...\n"),
		}, want: []string{pathRun}},
		{name: "a tool the Makefile names but does not run", edits: []docsEdit{
			replace("Makefile", vuln, "\t@echo $(GOVULNCHECK)\n\tgovulncheck ./...\n"),
		}, want: []string{pathRun}},
		{name: "a run's program arguments and a module the go command downloads, which are no pins", edits: []docsEdit{
			replace("Makefile", vuln, "\tgo run $(GOVULNCHECK) ./... user@example.com\n\tgo mod download golang.org/x/vuln@v1.0.0\n"),
		}},
		{name: "latest in a run line that is not the go command", edits: []docsEdit{
			replace(ci, run, "      - run: npx some-tool@latest\n"+run),
		}, want: []string{".github/workflows/ci.yml:13: something is pinned to @latest"}},
		{name: "a tool the Makefile runs after build flags", edits: []docsEdit{
			replace("Makefile", vuln, "\tgo run -modfile=tools.mod -mod mod $(GOVULNCHECK) ./...\n"),
		}},
		{name: "a package of this module, run by the Makefile and by CI", edits: []docsEdit{
			replace("Makefile", vuln, vuln+"docs:\n\tgo run ./scripts/gates docs\n"),
			replace(ci, run, "      - run: go run ./scripts/gates docs\n"),
		}},
		{name: "a tool CI runs through the go command's full path", edits: []docsEdit{
			replace(ci, run, "      - run: $GOROOT/bin/go run golang.org/x/vuln/cmd/govulncheck@main ./...\n"),
		}, want: []string{
			`.github/workflows/ci.yml: go run golang.org/x/vuln/cmd/govulncheck: govulncheck is pinned to "main", which is not one exact version`,
			"govulncheck is main at .github/workflows/ci.yml: go run golang.org/x/vuln/cmd/govulncheck and v1.8.0 at " +
				"Makefile: GOVULNCHECK: one tool, one version",
		}},
		{name: "a tool CI runs after a build flag, at another version", edits: []docsEdit{
			replace(ci, run, "      - run: go run -mod=mod github.com/zricethezav/gitleaks/v8@v8.29.0 dir .\n"),
		}, want: []string{
			"gitleaks is v8.29.0 at .github/workflows/ci.yml: go run github.com/zricethezav/gitleaks/v8 and v8.30.1 at " +
				"Makefile: GITLEAKS: one tool, one version",
		}},
		{name: "a tool CI installs after a flag, at a branch", edits: []docsEdit{
			replace(ci, run, "      - run: go install -v github.com/rhysd/actionlint/cmd/actionlint@main\n"),
		}, want: []string{
			`.github/workflows/ci.yml: go install github.com/rhysd/actionlint/cmd/actionlint: actionlint is pinned to "main", ` +
				"which is not one exact version",
			"actionlint is main at .github/workflows/ci.yml: go install github.com/rhysd/actionlint/cmd/actionlint and v1.7.12 at " +
				"Makefile: ACTIONLINT: one tool, one version",
		}},
		{name: "a recipe that runs a tool at latest", edits: []docsEdit{
			replace("Makefile", vuln, "\tgo run golang.org/x/vuln/cmd/govulncheck@latest ./...\n"),
		}, want: []string{
			`Makefile: vuln: govulncheck is pinned to "latest", which is not one exact version`,
			pathRun,
			"govulncheck is v1.8.0 at Makefile: GOVULNCHECK and latest at Makefile: vuln: one tool, one version",
		}},
		{name: "a variable at latest that a recipe runs, reported once", edits: []docsEdit{
			replace("Makefile", "govulncheck@v1.8.0", "govulncheck@latest"),
		}, want: []string{
			"Makefile: GOVULNCHECK is pinned to @latest",
			"govulncheck is pinned nowhere: a tool this gate names that no file pins is either gone or unread",
		}},
		{name: "a recipe line continued onto the next, at latest", edits: []docsEdit{
			replace("Makefile", vuln, "\tgo run \\\n\t\tgolang.org/x/vuln/cmd/govulncheck@latest ./...\n"),
		}, want: []string{
			`Makefile: vuln: govulncheck is pinned to "latest", which is not one exact version`,
			pathRun,
			"govulncheck is v1.8.0 at Makefile: GOVULNCHECK and latest at Makefile: vuln: one tool, one version",
		}},
		{name: "a recipe that runs a tool the table does not know", edits: []docsEdit{
			replace("Makefile", vuln, vuln+"\tgo run honnef.co/go/tools/cmd/staticcheck@v0.6.1 ./...\n"),
		}, want: []string{
			"Makefile: vuln runs go run honnef.co/go/tools/cmd/staticcheck@v0.6.1, a tool pinsTools does not know",
		}},
		{name: "a Makefile pin that is not one exact version", edits: []docsEdit{
			replace("Makefile", "govulncheck@v1.8.0", "govulncheck@v1.8"),
		}, want: []string{`Makefile: GOVULNCHECK: govulncheck is pinned to "v1.8", which is not one exact version`}},
		{name: "a tool a recipe runs in a command substitution", edits: []docsEdit{
			replace("Makefile", vuln, "\tOUT=$$(go run $(GOVULNCHECK) ./...)\n"),
		}},
		{name: "a tool CI runs in backticks, at another version", edits: []docsEdit{
			replace(ci, run, "      - run: OUT=`go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...`\n"),
		}, want: []string{
			"govulncheck is v1.7.0 at .github/workflows/ci.yml: go run golang.org/x/vuln/cmd/govulncheck and v1.8.0 at " +
				"Makefile: GOVULNCHECK: one tool, one version",
		}},
		{name: "a tool named in a recipe's comment, which runs nothing", edits: []docsEdit{
			replace("Makefile", vuln, "\tgo run $(GOVULNCHECK) ./... # was: true; go run golang.org/x/vuln/cmd/govulncheck@v1.7.0\n"),
		}},
		{name: "two variables at one version, one of them run by no recipe", edits: []docsEdit{
			replace("Makefile", goreleaser, goreleaser+"VULN_OLD ?= golang.org/x/vuln/cmd/govulncheck@v1.8.0\n"),
		}, want: []string{
			"Makefile: VULN_OLD pins golang.org/x/vuln/cmd/govulncheck@v1.8.0 and no recipe runs it with the go command, " +
				"so the tool runs from the PATH",
		}},
		{name: "a variable holding two modules, which no recipe runs", edits: []docsEdit{
			replace("Makefile", goreleaser, goreleaser+
				"TOOLS ?= golang.org/x/vuln/cmd/govulncheck@v1.8.0 github.com/rhysd/actionlint/cmd/actionlint@v1.7.11\n"),
		}, want: []string{
			"Makefile: TOOLS pins golang.org/x/vuln/cmd/govulncheck@v1.8.0 and no recipe runs it with the go command, " +
				"so the tool runs from the PATH",
			"Makefile: TOOLS pins github.com/rhysd/actionlint/cmd/actionlint@v1.7.11 and no recipe runs it with the go command, " +
				"so the tool runs from the PATH",
			"actionlint is v1.7.12 at Makefile: ACTIONLINT and v1.7.11 at Makefile: TOOLS: one tool, one version",
		}},
		{name: "a variable holding two modules, one through another variable, which a recipe installs", edits: []docsEdit{
			replace("Makefile", goreleaser, goreleaser+"TOOLS ?= $(GOVULNCHECK) github.com/rhysd/actionlint/cmd/actionlint@v1.7.12\n"),
			replace("Makefile", vuln, "\tgo install $(TOOLS)\n"),
		}},
		// An alias is read as the anchored value, its comment and line
		// included.
		{name: "an action by an alias of an anchored uses: value", edits: []docsEdit{
			replace(ci, checkout, "      - uses: &co actions/checkout@"+pinsSHA1+" # v7.0.1\n"),
			replace(ci, run, "      - uses: *co\n"+run),
		}},
		{name: "an action by tag, used again through an alias", edits: []docsEdit{
			replace(ci, checkout, "      - uses: &co actions/checkout@v4\n"),
			replace(ci, run, "      - uses: *co\n"+run),
		}, want: []string{
			`.github/workflows/ci.yml:9: actions/checkout is pinned to "v4", a tag or branch that can be moved; pin the 40-character commit SHA`,
			`.github/workflows/ci.yml:9: actions/checkout is pinned to "v4", a tag or branch that can be moved; pin the 40-character commit SHA`,
		}},
		// A pin is a string the Go source holds; a comment pins nothing.
		{name: "a tool the Makefile runs from the PATH, pinned only in a Go comment", edits: []docsEdit{
			replace("Makefile", "GOVULNCHECK ?= golang.org/x/vuln/cmd/govulncheck@v1.8.0\n", ""),
			replace("Makefile", vuln, "\tgovulncheck ./...\n"),
			add("scripts/gates/run.go", "package main\n\n// GoRun runs a package, such as golang.org/x/vuln/cmd/govulncheck@v1.8.0.\n"),
		}, want: []string{nowhere}},
		{name: "a pin in a Go raw string, at another version", edits: []docsEdit{
			add("scripts/gates/run.go", "package main\n\nconst vuln = `golang.org/x/vuln/cmd/govulncheck@v1.7.0`\n"),
		}, want: []string{"govulncheck is v1.8.0 at Makefile: GOVULNCHECK and v1.7.0 at scripts/gates/run.go: " +
			"golang.org/x/vuln/cmd/govulncheck: one tool, one version"}},
		{name: "a tool CI runs after a debug flag and its value", edits: []docsEdit{
			replace(ci, run, "      - run: go run -debug-trace /tmp/t golang.org/x/vuln/cmd/govulncheck@master ./...\n"+run),
		}, want: []string{
			`.github/workflows/ci.yml: go run golang.org/x/vuln/cmd/govulncheck: govulncheck is pinned to "master", which is not one exact version`,
			"govulncheck is master at .github/workflows/ci.yml: go run golang.org/x/vuln/cmd/govulncheck and v1.8.0 at " +
				"Makefile: GOVULNCHECK: one tool, one version",
		}},
		// The text rule: a known tool's module@version on any line but a
		// comment line is one exact version, however the line runs it.
		{name: "a tool CI runs through bash -c, sh -c, eval and env -S", edits: []docsEdit{
			replace(ci, run, "      - run: bash -c \"go run golang.org/x/vuln/cmd/govulncheck@master ./...\"\n"+
				"      - run: |\n          sh -c 'go run golang.org/x/vuln/cmd/govulncheck@main ./...'\n"+
				"          eval \"go run github.com/rhysd/actionlint/cmd/actionlint@main\"\n"+
				"          env -S \"go run github.com/google/go-licenses@v1 check\"\n"+run),
		}, want: []string{
			`.github/workflows/ci.yml:13: govulncheck is pinned to "master", which is not one exact version`,
			`.github/workflows/ci.yml:15: govulncheck is pinned to "main", which is not one exact version`,
			`.github/workflows/ci.yml:16: actionlint is pinned to "main", which is not one exact version`,
			`.github/workflows/ci.yml:17: go-licenses is pinned to "v1", which is not one exact version`,
		}},
		{name: "a tool CI runs after $'…' quoting", edits: []docsEdit{
			replace(ci, run, "      - run: echo $'\\'' ; go run golang.org/x/vuln/cmd/govulncheck@master ./...\n"+run),
		}, want: []string{`.github/workflows/ci.yml:13: govulncheck is pinned to "master", which is not one exact version`}},
		{name: "a tool CI runs from an env value", edits: []docsEdit{
			replace(ci, run, "      - env:\n          VULN: golang.org/x/vuln/cmd/govulncheck@master\n        run: go run $VULN ./...\n"+run),
		}, want: []string{`.github/workflows/ci.yml:14: govulncheck is pinned to "master", which is not one exact version`}},
		{name: "latest in a command the reader does not read, reported once", edits: []docsEdit{
			replace(ci, run, "      - run: bash -c \"go run golang.org/x/vuln/cmd/govulncheck@latest ./...\"\n"+run),
		}, want: []string{".github/workflows/ci.yml:13: something is pinned to @latest"}},
		{name: "an exact version a reader does not read, and one ended by an operator", edits: []docsEdit{
			replace(ci, run, "      - run: bash -c \"go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...\"\n"+
				"      - run: go run golang.org/x/vuln/cmd/govulncheck@v1.8.0; go run golang.org/x/vuln/cmd/govulncheck@v1.8.0&& true\n"+run),
		}},
		{name: "a Makefile pin behind a # inside $(…)", edits: []docsEdit{
			replace("Makefile", "GOVULNCHECK ?= golang.org/x/vuln/cmd/govulncheck@v1.8.0\n",
				"GOVULNCHECK ?= $(if x#,golang.org/x/vuln/cmd/govulncheck@master)\n"),
		}, want: []string{`Makefile:2: govulncheck is pinned to "master", which is not one exact version`, nowhere}},
		{name: "a Makefile pin at latest behind a # inside $(…)", edits: []docsEdit{
			replace("Makefile", "GOVULNCHECK ?= golang.org/x/vuln/cmd/govulncheck@v1.8.0\n",
				"GOVULNCHECK ?= $(if x#,golang.org/x/vuln/cmd/govulncheck@latest)\n"),
		}, want: []string{`Makefile:2: govulncheck is pinned to "latest", which is not one exact version`, nowhere}},
		{name: "a variable at latest that also holds a version that floats", edits: []docsEdit{
			replace("Makefile", goreleaser, goreleaser+
				"TOOLS ?= github.com/rhysd/actionlint/cmd/actionlint@latest golang.org/x/vuln/cmd/govulncheck@master\n"),
		}, want: []string{
			"Makefile: TOOLS is pinned to @latest",
			"Makefile: TOOLS pins github.com/rhysd/actionlint/cmd/actionlint@latest and no recipe runs it with the go command, " +
				"so the tool runs from the PATH",
			"Makefile: TOOLS pins golang.org/x/vuln/cmd/govulncheck@master and no recipe runs it with the go command, " +
				"so the tool runs from the PATH",
			`Makefile:7: govulncheck is pinned to "master", which is not one exact version`,
		}},
		{name: "a Makefile pin at master after a conditional one", edits: []docsEdit{
			replace("Makefile", "GOVULNCHECK ?= golang.org/x/vuln/cmd/govulncheck@v1.8.0\n",
				"ifeq (1,0)\nGOVULNCHECK = golang.org/x/vuln/cmd/govulncheck@v1.8.0\nendif\n"+
					"GOVULNCHECK ?= golang.org/x/vuln/cmd/govulncheck@master\n"),
		}, want: []string{`Makefile:5: govulncheck is pinned to "master", which is not one exact version`}},
		{name: "a Makefile pin at master before a conditional one", edits: []docsEdit{
			replace("Makefile", "GOVULNCHECK ?= golang.org/x/vuln/cmd/govulncheck@v1.8.0\n",
				"GOVULNCHECK ?= golang.org/x/vuln/cmd/govulncheck@master\nifeq (1,0)\n"+
					"GOVULNCHECK = golang.org/x/vuln/cmd/govulncheck@v1.8.0\nendif\n"),
		}, want: []string{`Makefile:2: govulncheck is pinned to "master", which is not one exact version`}},
		{name: "a tool named on comment lines in the Makefile, a workflow and a run block", edits: []docsEdit{
			replace("Makefile", goreleaser, goreleaser+"  # was golang.org/x/vuln/cmd/govulncheck@master\n"),
			replace(ci, run, "      # go run golang.org/x/vuln/cmd/govulncheck@master\n"+
				"      - run: |\n          # go run golang.org/x/vuln/cmd/govulncheck@main\n          make check\n"),
		}},
		{name: "a module that only starts with a known tool's path", edits: []docsEdit{
			replace(ci, run, "      - run: go run github.com/google/go-licenses-extra@v1.6.0 check\n"+run),
		}, want: []string{".github/workflows/ci.yml: go run github.com/google/go-licenses-extra@v1.6.0 names a tool pinsTools does not know"}},
		// One installer used in every job meets a count of steps.
		{name: "an installer no step uses, with another used twice", edits: []docsEdit{
			replace(release, gorelease, ""),
			replace(ci, run, "      - uses: sigstore/cosign-installer@"+pinsSHA1+" # v4.1.2\n"+
				"        with:\n          cosign-release: v3.1.3\n"+run),
		}, want: []string{
			"no workflow step installs goreleaser through goreleaser/goreleaser-action; the reader is not seeing " +
				"the step, or the table names an installer nothing uses",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := pinsFixture()
			applyEdits(t, files, tc.edits...)
			r, err := pinsCheck(repoTree(t, files))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(r.problems, tc.want) {
				t.Errorf("problems\n got %q\nwant %q", r.problems, tc.want)
			}
		})
	}
}
