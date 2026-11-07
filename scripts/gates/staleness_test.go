package main

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/mmedum/gitlab-mcp/v4/internal/userconfig"
)

// stalenessTestTools is a surface the fixture documents describe.
var stalenessTestTools = []string{"get_me", "get_issue", "get_file", "list_tree", "list_branches",
	"list_commits", "get_commit", "get_project", "search_projects", "search_issues"}

// stalenessFixture is a set of inputs every rule passes on. A test
// breaks one thing in it.
func stalenessFixture() stalenessInputs {
	var readme strings.Builder
	readme.WriteString("# gitlab-mcp\n\n[![Release](https://img.shields.io/github/v/release/x)](https://example.com/x)\n\n## Tools\n\n| Tool | What it does |\n|---|---|\n")
	for _, t := range stalenessTestTools {
		readme.WriteString("| `" + t + "` | does a thing |\n")
	}
	readme.WriteString("\n## Safety\n\n| Setting | Registers |\n|---|---|\n| `GITLAB_MCP_READ_ONLY=true` | reads |\n\n" +
		"Three things hold:\n\n- one\n- two\n  continued\n- three\n")

	vars := []string{"GITLAB_MCP_PROFILE", "GITLAB_MCP_CLIENT_ID", "GITLAB_MCP_READ_ONLY",
		"GITLAB_MCP_ENABLE_SHIP", "GITLAB_MCP_ENABLE_DESTRUCTIVE", "GITLAB_MCP_TOOLSETS", "GITLAB_MCP_WRITE_NAMESPACES",
		"GITLAB_MCP_LOG_LEVEL", "GITLAB_MCP_LOG_FORMAT", "GITLAB_MCP_HTTP_TIMEOUT"}
	var configDoc strings.Builder
	configDoc.WriteString("# Configuration\n\n| Variable | Flag |\n|---|---|\n")
	for _, v := range vars {
		configDoc.WriteString("| `" + v + "` | x |\n")
	}
	configDoc.WriteString("\nA profile name is " + userconfig.ProfileRule + ".\n")

	var commandNames []string
	var contributing strings.Builder
	contributing.WriteString("# Contributing\n\n")
	for i := range 20 {
		name := fmt.Sprintf("gate%02d", i)
		commandNames = append(commandNames, name)
		contributing.WriteString("- `" + name + "` checks a thing.\n")
	}
	contributing.WriteString("\nRun one with `go run ./scripts/gates gate03`.\n")
	contributing.WriteString("\n`GITLAB_MCP_TEST_INSTANCE` points the binary at the fake.\n")

	modes := []stalenessMode{
		{Name: "default", Block: "Name:          gitlab-mcp\nScopes:        api\n"},
		{Name: "read-only", Block: "Name:          gitlab-mcp\nScopes:        read_api\n"},
	}
	var setup strings.Builder
	setup.WriteString("# Setup\n\n")
	for _, m := range modes {
		setup.WriteString(stalenessSetupMarker("begin", m.Name) + "\n" + stalenessSetupRender(m) + "\n" +
			stalenessSetupMarker("end", m.Name) + "\n\n")
	}

	return stalenessInputs{
		docs: map[string]string{
			"README.md":              readme.String() + "\n```json\n{\"permissions\": {\"ask\": [\"mcp__gitlab__get_issue\"]}}\n```\n",
			"CLAUDE.md":              "# CLAUDE\n",
			stalenessConfigDoc:       configDoc.String() + "GITLAB_MCP_GONE was removed.\n",
			stalenessContributingDoc: contributing.String(),
			stalenessSetupDoc:        setup.String(),
			"CHANGELOG.md":           "## [Unreleased]\n\n### Added\n\n- A thing.\n",
			"docs/architecture.md":   "# Architecture\n\nGitLab 19.6 added a field.\n",
			"docs/extra.md":          "Nothing.\n",
		},
		configVars:  vars,
		devVars:     []string{"GITLAB_MCP_TEST_INSTANCE"},
		removedVars: []string{"GITLAB_MCP_GONE"},
		modes:       modes,
		commands:    commandNames,
		tools:       slices.Clone(stalenessTestTools),
	}
}

func TestStalenessPassesOnTheFixture(t *testing.T) {
	r := stalenessCheck(stalenessFixture())
	wantClean(t, r.problems)
	if len(r.read) != 8 {
		t.Errorf("%d rules reported what they read, want 8: %q", len(r.read), r.read)
	}
	joined := strings.Join(r.read, "\n")
	for _, want := range []string{"README tool table: 10 rows", "prose counts: 1 numbers", "setup blocks: 2 modes",
		"settings: 11 variables", "gates: 20 commands", "version in prose: 7 documents"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the report does not say %q:\n%s", want, joined)
		}
	}
}

// A release commit moves [Unreleased] under the new version before the
// tag exists; its entries count as the changes since the last tag.
func TestStalenessPassesOnAReleaseCut(t *testing.T) {
	in := stalenessFixture()
	in.newestTag = "0.1.0"
	in.changedSince = []string{"internal/p01/a.go"}
	in.docs["CHANGELOG.md"] = "## [Unreleased]\n\n## [0.2.0] - 2026-02-01\n\n### Added\n\n- A thing.\n\n## [0.1.0] - 2026-01-01\n"
	wantClean(t, stalenessCheck(in).problems)
	// A release candidate is a release cut too.
	in.docs["CHANGELOG.md"] = "## [Unreleased]\n\n## [0.2.0-rc.1] - 2026-02-01\n\n### Added\n\n- A thing.\n\n## [0.1.0] - 2026-01-01\n"
	wantClean(t, stalenessCheck(in).problems)
	// A heading in a fence is an example, not a release cut.
	in.docs["CHANGELOG.md"] = "## [Unreleased]\n\n```markdown\n## [0.2.0]\n\n- A thing.\n```\n\n## [0.1.0] - 2026-01-01\n"
	wantProblem(t, stalenessCheck(in).problems, "1 shipped file(s) changed since v0.1.0 (internal/p01/a.go) and CHANGELOG.md has no entry under [Unreleased]")
}

// A release candidate's tag is a release: the changes since it need entries
// of their own, and its section is not read as one waiting for a tag.
func TestStalenessReadsAReleaseCandidateAsReleased(t *testing.T) {
	changelog := "## [Unreleased]\n\n## [2.0.0-rc.1] - 2026-02-01\n\n### Added\n\n- A thing.\n\n## [1.9.0] - 2026-01-01\n"
	dir := gitRepo(t, map[string]string{"CHANGELOG.md": changelog, "internal/x/x.go": "package x\n"})
	gitDo(t, dir, "tag", "v1.9.0")
	gitCommit(t, dir, "rc", map[string]string{"internal/x/x.go": "package x // rc\n"})
	gitDo(t, dir, "tag", "v2.0.0-rc.1")
	gitCommit(t, dir, "after", map[string]string{"internal/x/y.go": "package x\n"})
	in, err := stalenessGather(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := stalenessChangedSinceTag(in.docs[changelogFile], in.newestTag, in.changedSince)
	want := []string{"1 shipped file(s) changed since v2.0.0-rc.1 (internal/x/y.go) and CHANGELOG.md has no entry under [Unreleased]"}
	if !slices.Equal(got, want) {
		t.Errorf("newest tag %q; problems\n got %q\nwant %q", in.newestTag, got, want)
	}
}

// The newest tag is the newest by semver order, a prerelease before its
// release, and a tag that is no version is not one.
func TestStalenessNewestTagOrdersAsSemver(t *testing.T) {
	dir := gitRepo(t, map[string]string{"a": "a"})
	for _, step := range []struct{ tag, want string }{
		{"v1.9.0", "1.9.0"},
		{"v2.0.0-rc.2", "2.0.0-rc.2"},
		{"v2.0.0-rc.10", "2.0.0-rc.10"},
		{"v2.0.0-beta.3", "2.0.0-rc.10"},
		{"v2.0.0", "2.0.0"},
		{"v2.0.1-rc.1", "2.0.1-rc.1"},
		{"v10.0.0-alpha.1", "10.0.0-alpha.1"},
		{"v10.0.0-alpha", "10.0.0-alpha.1"},
		{"v11", "10.0.0-alpha.1"},
		{"latest", "10.0.0-alpha.1"},
	} {
		gitDo(t, dir, "tag", step.tag)
		if got, err := stalenessNewestTag(dir); err != nil || got != step.want {
			t.Errorf("after tagging %s: newest tag %q, %v; want %q", step.tag, got, err, step.want)
		}
	}
}

// Outside a git checkout the changes since the newest tag cannot be read,
// so the gate could not run: exit 2, not a finding's 1.
func TestStalenessNeedsAGitCheckout(t *testing.T) {
	code, out, errOut := runOutsideGit(t, "staleness")
	if code != 2 || out.String() != "" || !strings.HasPrefix(errOut.String(), "gates staleness: could not run: git tag") {
		t.Errorf("exit %d, stdout %q, stderr %q; want 2, nothing and a git tag failure", code, out.String(), errOut.String())
	}
}

// A tag git cannot diff from leaves the changes since it unread, so the
// gate could not run.
func TestStalenessNeedsTheChangesSinceTheTag(t *testing.T) {
	dir := gitRepo(t, map[string]string{"README.md": "# x\n"})
	gitDo(t, dir, "tag", "v0.1.0", gitDo(t, dir, "hash-object", "-w", "README.md"))
	t.Chdir(dir)
	var out, errOut bytes.Buffer
	code := run([]string{"staleness"}, &out, &errOut)
	if code != 2 || out.String() != "" || !strings.HasPrefix(errOut.String(), "gates staleness: could not run: git diff") {
		t.Errorf("exit %d, stdout %q, stderr %q; want 2, nothing and a git diff failure", code, out.String(), errOut.String())
	}
}

// Inside one, what the rules find is a refusal, which exits 1.
func TestStalenessRefusesInAGitCheckout(t *testing.T) {
	t.Chdir(gitRepo(t, map[string]string{"README.md": "# x\n"}))
	var out, errOut bytes.Buffer
	code := run([]string{"staleness"}, &out, &errOut)
	if code != 1 || !regexp.MustCompile(`^gates staleness: \d+ problem\(s\) in the documents\n$`).MatchString(errOut.String()) {
		t.Errorf("exit %d, stderr %q; want 1 and the documents' problems", code, errOut.String())
	}
}

func TestStalenessRefuses(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(in *stalenessInputs)
		want   string
	}{
		{"an undocumented variable", func(in *stalenessInputs) {
			in.configVars = append(in.configVars, "GITLAB_MCP_NEW")
		}, "docs/configuration.md does not document GITLAB_MCP_NEW"},
		{"a permission rule for no tool", func(in *stalenessInputs) {
			in.docs["README.md"] = strings.Replace(in.docs["README.md"], "mcp__gitlab__get_issue", "mcp__gitlab__get_issues", 1)
		}, "README.md gives a permission rule for get_issues, which the binary does not register"},
		{"a documented variable nothing reads", func(in *stalenessInputs) {
			in.docs[stalenessConfigDoc] += "| `GITLAB_MCP_OLD` | x |\n"
		}, "documents GITLAB_MCP_OLD, which the server does not read"},
		{"a development override in the configuration document", func(in *stalenessInputs) {
			in.docs[stalenessConfigDoc] += "| `GITLAB_MCP_TEST_INSTANCE` | x |\n"
		}, "documents GITLAB_MCP_TEST_INSTANCE, a development override that belongs in CONTRIBUTING.md only"},
		{"an undocumented development override", func(in *stalenessInputs) {
			in.docs[stalenessContributingDoc] = strings.ReplaceAll(in.docs[stalenessContributingDoc], "GITLAB_MCP_TEST_INSTANCE", "the override")
		}, "CONTRIBUTING.md does not document the development override GITLAB_MCP_TEST_INSTANCE"},
		{"an undocumented gate", func(in *stalenessInputs) {
			in.commands = append(in.commands, "newgate")
		}, "does not name the newgate gate"},
		{"a documented gate nothing registers", func(in *stalenessInputs) {
			in.docs[stalenessContributingDoc] += "Also `gates oldgate`.\n"
		}, "shows `gates oldgate`, which the registry does not have"},
		{"shipped Go since the tag with no entry", func(in *stalenessInputs) {
			in.newestTag = "0.1.0"
			in.changedSince = []string{"internal/p01/a.go"}
			in.docs["CHANGELOG.md"] = "## [Unreleased]\n\n### Added\n\n"
		}, "1 shipped file(s) changed since v0.1.0"},
		{"shipped Go since a release, with only its candidate's entries", func(in *stalenessInputs) {
			in.newestTag = "2.0.0"
			in.changedSince = []string{"internal/p01/a.go"}
			in.docs["CHANGELOG.md"] = "## [Unreleased]\n\n## [2.0.0-rc.1] - 2026-02-01\n\n### Added\n\n- A thing.\n"
		}, "1 shipped file(s) changed since v2.0.0"},
		{"shipped Go since the tag with only the tag's own entries", func(in *stalenessInputs) {
			in.newestTag = "0.1.0"
			in.changedSince = []string{"internal/p01/a.go"}
			in.docs["CHANGELOG.md"] = "## [Unreleased]\n\n## [0.1.0] - 2026-01-01\n\n### Added\n\n- A thing.\n"
		}, "1 shipped file(s) changed since v0.1.0"},
		{"a version in prose", func(in *stalenessInputs) {
			in.docs["docs/extra.md"] = "This is v1.2.3 of the server.\n"
		}, "docs/extra.md:1 writes v1.2.3 in prose"},
		{"a version in prose below a fence", func(in *stalenessInputs) {
			in.docs["docs/extra.md"] = "```\nx\n```\n\nThis is v1.2.3 of the server.\n"
		}, "docs/extra.md:5 writes v1.2.3 in prose"},
		{"a version in the architecture document's prose", func(in *stalenessInputs) {
			in.docs["docs/architecture.md"] += "The snapshot is v19.4.1-ee.\n"
		}, "docs/architecture.md:4 writes v19.4.1 in prose"},
		{"a tool the README does not list", func(in *stalenessInputs) {
			in.tools = append(in.tools, "tool_z")
		}, "the binary registers tool_z, which README.md's tool table does not list"},
		{"a README row the binary lacks", func(in *stalenessInputs) {
			in.tools = in.tools[1:]
		}, "README.md's tool table lists get_me, which the binary does not register"},
		{"no binary", func(in *stalenessInputs) {
			in.tools, in.toolsErr = nil, errors.New("no binary given")
		}, "the built binary's tools could not be read: no binary given"},
		{"a count above a longer list", func(in *stalenessInputs) {
			in.docs["README.md"] = strings.Replace(in.docs["README.md"], "- three\n", "- three\n- four\n", 1)
		}, `says "Three things hold:" above a list of 4`},
		{"a setup block that drifted", func(in *stalenessInputs) {
			in.docs[stalenessSetupDoc] = strings.Replace(in.docs[stalenessSetupDoc], "Scopes:        read_api", "Scopes:        api", 1)
		}, "docs/setup.md's read-only block differs"},
		{"a mode with no block", func(in *stalenessInputs) {
			in.modes = append(in.modes, stalenessMode{Name: "ship", Block: "x\n"})
		}, "has no <!-- setup:begin ship --> ... <!-- setup:end ship --> block"},
		{"a configuration page silent on the profile rule", func(in *stalenessInputs) {
			in.docs[stalenessConfigDoc] = strings.Replace(in.docs[stalenessConfigDoc], userconfig.ProfileRule, "lowercase letters and digits", 1)
		}, `docs/configuration.md does not say a profile name is "up to 64 characters from a to z, 0 to 9, - and _, starting with a letter or digit"`},
		{"the tool table missing", func(in *stalenessInputs) {
			in.docs["README.md"] = strings.Replace(in.docs["README.md"], "| Tool | What it does |", "| Name | What it does |", 1)
		}, "README.md has no table headed | Tool |"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := stalenessFixture()
			tc.mutate(&in)
			wantProblem(t, stalenessCheck(in).problems, tc.want)
		})
	}
}

// A variable is documented by its whole name: a longer name that holds
// one is that longer name, in either document, a letter of either case
// on either side included, and a name followed by * is a pattern.
// A version in a fence of either kind is an example, and the lines below
// it keep their numbers.
func TestStalenessReadsVersionsOutsideFences(t *testing.T) {
	_, got := stalenessNoVersionInProse(map[string]string{
		"docs/extra.md": "~~~\ngo install example.com/x@v0.9.9\n~~~\n\n```\nv0.9.8\n```\n\nThis is v1.2.3 of the server.\n",
		"README.md":     "", "CLAUDE.md": "", "SECURITY.md": "",
	})
	if want := []string{"docs/extra.md:9 writes v1.2.3 in prose; a badge shows the version and cannot go stale"}; !slices.Equal(got, want) {
		t.Errorf("problems %q, want %q", got, want)
	}
}

func TestStalenessEnvReadsWholeNames(t *testing.T) {
	in := stalenessFixture()
	doc := strings.NewReplacer("`GITLAB_MCP_PROFILE`", "`GITLAB_MCP_PROFILE_NAME`",
		"`GITLAB_MCP_CLIENT_ID`", "`GITLAB_MCP_CLIENT_IDs`", "`GITLAB_MCP_READ_ONLY`", "`xGITLAB_MCP_READ_ONLY`",
		"`GITLAB_MCP_TOOLSETS`", "`GITLAB_MCP_TOOLSETS*` and `GITLAB_MCP_TOOL*`").Replace(in.docs[stalenessConfigDoc])
	devDoc := strings.Replace(in.docs[stalenessContributingDoc], "`GITLAB_MCP_TEST_INSTANCE`", "`GITLAB_MCP_TEST_INSTANCE_URL`", 1)
	_, got := stalenessEnv(doc, devDoc, in.configVars, in.devVars, in.removedVars)
	want := []string{
		"CONTRIBUTING.md does not document the development override GITLAB_MCP_TEST_INSTANCE",
		"docs/configuration.md does not document GITLAB_MCP_PROFILE",
		"docs/configuration.md does not document GITLAB_MCP_CLIENT_ID",
		"docs/configuration.md does not document GITLAB_MCP_READ_ONLY",
		"docs/configuration.md does not document GITLAB_MCP_TOOLSETS",
		"docs/configuration.md documents GITLAB_MCP_PROFILE_NAME, which the server does not read",
	}
	if !slices.Equal(got, want) {
		t.Errorf("stalenessEnv = %q\nwant %q", got, want)
	}
}

// The Safety table keyed "Setting" is not the tool table, however many
// backticked names it has.
func TestStalenessReadmeReadsTheToolTable(t *testing.T) {
	readme := "| Setting | Registers |\n|---|---|\n| `read_only` | x |\n\n| Tool | What |\n|---|---|\n| `get_me` | x |\n"
	rows, found := stalenessTable(readme, "Tool")
	if !found || len(rows) != 1 || !strings.Contains(rows[0], "get_me") {
		t.Errorf("rows %q, found %v", rows, found)
	}
}

func TestStalenessNumber(t *testing.T) {
	for s, want := range map[string]int{"66": 66, "Thirteen": 13, "two": 2, "twenty": 20} {
		if got, ok := stalenessNumber(s); !ok || got != want {
			t.Errorf("stalenessNumber(%q) = %d, %v; want %d", s, got, ok, want)
		}
	}
	for _, s := range []string{"one", "many"} {
		if _, ok := stalenessNumber(s); ok {
			t.Errorf("stalenessNumber(%q) read a number", s)
		}
	}
}

// A CHANGELOG's release sections are history, so a count there is not
// held; one under [Unreleased] is, reported at its line in the file.
func TestStalenessCountsOnlyTheUnreleasedChangelog(t *testing.T) {
	const entry = "Two things changed:\n\n- a\n- b\n- c\n"
	for name, c := range map[string]struct {
		changelog string
		want      []string
	}{
		"released":   {"## [Unreleased]\n\n## [0.1.0] - 2026-01-01\n\n" + entry, nil},
		"unreleased": {"## [Unreleased]\n\n" + entry + "\n## [0.1.0] - 2026-01-01\n", []string{`CHANGELOG.md:3 says "Two things changed:" above a list of 3`}},
		"unreleased below a preamble and a fence": {"# Changelog\n\nIntro.\n\n## [Unreleased]\n\n```\nx\n```\n\n" + entry +
			"\n## [0.1.0] - 2026-01-01\n", []string{`CHANGELOG.md:11 says "Two things changed:" above a list of 3`}},
	} {
		if _, got := stalenessProseCounts(map[string]string{"CHANGELOG.md": c.changelog}); !slices.Equal(got, c.want) {
			t.Errorf("%s: problems %q, want %q", name, got, c.want)
		}
	}
}

func TestStalenessDumpTools(t *testing.T) {
	got, err := stalenessDumpTools([]byte(`{"tools":[{"name":"b"},{"name":"a"}],"resources":[]}`))
	if err != nil || !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("tools %q, %v", got, err)
	}
	if _, err := stalenessDumpTools([]byte(`{"tools":[]}`)); err == nil {
		t.Error("an empty dump passed")
	}
	if _, err := stalenessTools(nil); err == nil {
		t.Error("no binary passed")
	}
}

func TestStalenessShipped(t *testing.T) {
	got := stalenessShipped([]string{"go.mod", "internal/a/a.go", "internal/a/a_test.go", "scripts/gates/x.go",
		"docs/a.md", "cmd/gitlab-mcp/main.go", "internal/a/testdata/x.go"})
	if want := []string{"go.mod", "internal/a/a.go", "cmd/gitlab-mcp/main.go"}; !slices.Equal(got, want) {
		t.Errorf("shipped %q, want %q", got, want)
	}
}

func TestStalenessShippedSince(t *testing.T) {
	bump := strings.Replace(changelogGoMod, "a v1.0.0", "a v1.1.0", 1)
	cases := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{"a dependency bump", map[string]string{"go.mod": bump}, nil},
		{"a dependency bump with code", map[string]string{"go.mod": bump, "internal/x/x.go": "package x // changed\n"},
			[]string{"internal/x/x.go"}},
		{"a go directive change", map[string]string{"go.mod": strings.Replace(changelogGoMod, "go 1.27.2", "go 1.27.3", 1)},
			[]string{"go.mod"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := gitRepo(t, map[string]string{"go.mod": changelogGoMod, "internal/x/x.go": "package x\n"})
			gitDo(t, dir, "tag", "v0.1.0")
			gitCommit(t, dir, "work", tc.files)
			got, err := stalenessShippedSince(dir, "v0.1.0")
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("shipped since the tag %q, want %q", got, tc.want)
			}
		})
	}
}
