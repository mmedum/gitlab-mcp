package main

import (
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
	if r.workflows != 4 || r.installers != 4 || r.actions != 13 {
		t.Errorf("read %d workflows, %d installers, %d actions; want 4, 4, 13", r.workflows, r.installers, r.actions)
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
			"ACTIONLINT pins actionlint and no recipe runs"},
		{"a copy in Go source", "scripts/gates/precommit.go", "// reads", "// github.com/zricethezav/gitleaks/v8@v8.29.0 reads",
			"gitleaks is v8.30.1 at"},
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
