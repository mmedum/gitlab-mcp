package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// releaseFixture is a repository whose release wiring agrees: the
// config and workflow in testdata, and the Makefile and go.mod they are
// held to.
func releaseFixture(t *testing.T) map[string]string {
	t.Helper()
	read := func(name string) string {
		raw, err := os.ReadFile(filepath.Join("testdata", "release", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	return map[string]string{
		".goreleaser.yaml":              read("goreleaser.yaml"),
		".github/workflows/release.yml": read("release.yml"),
		"Makefile":                      "VERSION ?= dev\nMCPB_OUT  ?= dist/gitlab-mcp_$(VERSION).mcpb\n",
		"go.mod":                        "module github.com/mmedum/gitlab-mcp\n\ngo 1.27.1\n",
	}
}

func TestReleasePassesOnAWiredRelease(t *testing.T) {
	var out bytes.Buffer
	if err := releaseCheck(&out, repoTree(t, releaseFixture(t))); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	mustSay(t, out.String(), "7 build targets, 4 staged globs")
}

func TestReleaseRefuses(t *testing.T) {
	const gr, wf = ".goreleaser.yaml", ".github/workflows/release.yml"
	cases := []struct{ name, file, old, new, want string }{
		{"a mutating before hook", gr, "    - go mod download\n", "    - go mod tidy\n", `the before hook "go mod tidy"`},
		{"cgo on", gr, "      - CGO_ENABLED=0\n", "      - CGO_ENABLED=1\n", "does not set CGO_ENABLED=0"},
		{"no -trimpath", gr, "flags: [-trimpath]", "flags: []", "does not pass -trimpath"},
		{"no commit timestamp", gr, `mod_timestamp: "{{ .CommitTimestamp }}"`, `mod_timestamp: ""`, "mod_timestamp"},
		{"the tag in ldflags", gr, "version.Version={{ .Version }}", "version.Version={{ .Tag }}", "stamps {{ .Tag }}"},
		{"unstripped", gr, "- -s -w -X", "- -X", "does not strip with -s -w"},
		{"five platforms", gr, "goarch: [amd64, arm64]", "goarch: [amd64]", "builds 3 platform targets"},
		{"a replacing universal binary", gr, "replace: false", "replace: true", "replace is true"},
		{"archives without ids", gr, "    ids: [gitlab-mcp]\n    formats", "    formats", "names no ids"},
		{"an unknown archive file", gr, "files: [LICENSE, NOTICE, README.md]", "files: [LICENSE, NOTICE, README.md, CLAUDE.md]",
			"carries CLAUDE.md"},
		{"no LICENSE in the archive", gr, "files: [LICENSE, NOTICE, README.md]", "files: [NOTICE, README.md]", "does not carry LICENSE"},
		{"Windows not zipped", gr, "formats: [zip]", "formats: [tar.gz]", "does not zip the Windows build"},
		{"the bundle not checksummed", gr, "checksum:\n  name_template: checksums.txt\n  # Being in checksums.txt is what gets the bundle signed, because the\n  # signature is over this file.\n  extra_files:\n    - glob: ./dist/*.mcpb\n",
			"checksum:\n  name_template: checksums.txt\n", "checksum.extra_files does not cover"},
		{"the bundle not uploaded", gr, "  extra_files:\n    - glob: ./dist/*.mcpb\n  footer", "  footer", "release.extra_files does not cover"},
		{"a hook packing elsewhere", gr, "dist/gitlab-mcp_{{ .Version }}.mcpb", "dist/bundle.mcpb", "MCPB_OUT"},
		{"a changelog block", gr, "release:\n  draft: false", "changelog:\n  disable: true\n\nrelease:\n  draft: false", "there is a changelog block"},
		{"a draft", gr, "draft: false", "draft: true", "release.draft is true"},
		{"no prerelease auto", gr, "prerelease: auto", "prerelease: false", `release.prerelease is "false"`},
		{"an identity regexp", gr, "--certificate-identity 'https", "--certificate-identity-regexp 'https", "does not verify with --certificate-identity"},
		{"a footer naming the wrong bundle", gr, "gh attestation verify gitlab-mcp_{{ .Version }}.mcpb", "gh attestation verify bundle.mcpb",
			"`gh attestation verify gitlab-mcp_<version>.mcpb`"},
		{"no SBOMs", gr, "sboms:\n  - artifacts: archive\n", "", "no sboms block"},
		{"signing without --bundle", gr, `      - "--bundle=${signature}"` + "\n", "", "does not pass --bundle"},
		{"no go test", wf, "run: go test ./...", "run: go vet ./...", "runs no `go test`"},
		{"notes in the checkout", wf, `> "${RUNNER_TEMP}/release-notes.md"`, "> release-notes.md", "inside the checkout"},
		{"one reproducible build", wf, "args: build --single-target --snapshot --clean --output ${{ runner.temp }}/repro-2", "args: build --snapshot --clean", "builds one target 1 time(s)"},
		{"cosign unpinned", wf, "          cosign-release: v3.1.3\n", "", "installs no cosign"},
		{"space-separated subjects", wf, `"dist/*.tar.gz,dist/*.zip,dist/checksums.txt,dist/*.mcpb"`,
			`"dist/*.tar.gz dist/*.zip dist/checksums.txt dist/*.mcpb"`, "separates with a space"},
		{"the bundle unattested", wf, ",dist/*.mcpb\"", "\"", "does not name .mcpb"},
		{"no dispatch", wf, "  workflow_dispatch:\n", "", "has no workflow_dispatch"},
		{"write at the top", wf, "permissions:\n  contents: read\n\ndefaults", "permissions:\n  contents: write\n\ndefaults", "top-level permissions"},
		{"publishing without the ci check", wf, "    needs: verify-ci\n", "", "without needing the job that checks ci is green"},
		{"the registry for a prerelease", wf, "    if: ${{ !contains(github.ref_name, '-') }}\n", "", "without skipping a prerelease tag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := releaseFixture(t)
			breakFile(t, files, tc.file, tc.old, tc.new)
			var out bytes.Buffer
			err := releaseCheck(&out, repoTree(t, files))
			if err == nil {
				t.Fatalf("accepted:\n%s", out.String())
			}
			if !strings.Contains(out.String(), tc.want) && !strings.Contains(err.Error(), tc.want) {
				t.Errorf("does not say %q:\n%s", tc.want, out.String())
			}
		})
	}
}

func TestReleaseSubjectProblems(t *testing.T) {
	if p := releaseSubjectProblems("dist/*.tar.gz\ndist/*.zip\ndist/checksums.txt\ndist/*.mcpb", "checksums.txt"); len(p) != 0 {
		t.Errorf("newline-separated subjects refused: %v", p)
	}
}
