package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// stalenessTestTools is a surface the fixture documents describe.
var stalenessTestTools = []string{"get_me", "get_issue", "get_file", "list_tree", "list_branches",
	"list_commits", "get_commit", "get_project", "search_projects", "search_issues"}

// stalenessFixture is a set of inputs every rule passes on. A test
// breaks one thing in it.
func stalenessFixture() stalenessInputs {
	files := map[string]bool{
		"cmd": true, "cmd/gitlab-mcp": true, "internal": true, "scripts": true, "scripts/internal": true,
		"docs": true, "docs/setup.md": true, "docs/development.md": true, "docs/configuration.md": true,
		"README.md": true, "CLAUDE.md": true, "Makefile": true, ".github/workflows/ci.yml": true,
	}
	var bullets, packages []string
	for i := range 20 {
		p := fmt.Sprintf("internal/p%02d", i)
		files[p] = true
		bullets = append(bullets, "- `"+p+"/` a package")
		packages = append(packages, p)
	}
	bullets = append(bullets, "- `cmd/gitlab-mcp/` the binary", "- `scripts/internal/` shared tooling")
	packages = append(packages, "cmd/gitlab-mcp", "scripts/internal/tsv", "scripts/internal/gitx")

	var spans []string
	for i := range 30 {
		spans = append(spans, fmt.Sprintf("`internal/p%02d`", i%20))
	}

	var toolRows []string
	kinds := []struct{ kind, set string }{{"Read", "default"}, {"Write", "default"}, {"Ship", "default"},
		{"Destructive", "default"}, {"Read", "wiki"}}
	for i := range 25 {
		k := kinds[i%len(kinds)]
		name := fmt.Sprintf("tool_%c", 'a'+i)
		if i < len(stalenessTestTools) {
			name = stalenessTestTools[i]
			k = kinds[0]
		}
		toolRows = append(toolRows, fmt.Sprintf("| `%s` | %s | %s | `GET /x` |", name, k.kind, k.set))
	}
	// 10 Read default, then of the other 15: i=10..24 cycling kinds from
	// i%5: Read default 3 (i=10,15,20), Write 3, Ship 3, Destructive 3,
	// wiki Read 3. Default 10+3+3 = 16, read-only 13, with Ship and
	// Destructive 22, total 25.
	arch := "# Architecture\n\n**Status: phase 0 of 5, nothing tagged yet.**\n\n## 8. Tool surface\n\n" +
		"Twenty-five tools. With the default toolsets: sixteen by default,\nthirteen in read-only mode, twenty-two with Ship and " +
		"Destructive both enabled. Every toolset and flag on registers all twenty-five.\n\n" +
		"| Tool | Kind | Toolset | Main operations |\n|---|---|---|---|\n" + strings.Join(toolRows, "\n") + "\n\n### 8a. Next\n"

	var readme strings.Builder
	readme.WriteString("# gitlab-mcp\n\n[![Release](https://img.shields.io/github/v/release/x)](https://example.com/x)\n\n## Tools\n\n| Tool | What it does |\n|---|---|\n")
	for _, t := range stalenessTestTools {
		readme.WriteString("| `" + t + "` | does a thing |\n")
	}
	readme.WriteString("\n## Safety\n\n| Setting | Registers |\n|---|---|\n| `GITLAB_MCP_READ_ONLY=true` | reads |\n\n" +
		"Three things hold:\n\n- one\n- two\n  continued\n- three\n\n" + strings.Join(spans, " ") + "\n")

	vars := []string{"GITLAB_MCP_PROFILE", "GITLAB_MCP_CLIENT_ID", "GITLAB_MCP_READ_ONLY",
		"GITLAB_MCP_ENABLE_SHIP", "GITLAB_MCP_ENABLE_DESTRUCTIVE", "GITLAB_MCP_TOOLSETS", "GITLAB_MCP_WRITE_NAMESPACES",
		"GITLAB_MCP_LOG_LEVEL", "GITLAB_MCP_LOG_FORMAT", "GITLAB_MCP_HTTP_TIMEOUT"}
	var configDoc strings.Builder
	configDoc.WriteString("# Configuration\n\n| Variable | Flag |\n|---|---|\n")
	for _, v := range vars {
		configDoc.WriteString("| `" + v + "` | x |\n")
	}

	var commandNames []string
	var devDoc strings.Builder
	devDoc.WriteString("# Development\n\n")
	for i := range 20 {
		name := fmt.Sprintf("gate%02d", i)
		commandNames = append(commandNames, name)
		devDoc.WriteString("- `" + name + "` checks a thing.\n")
	}
	devDoc.WriteString("\nRun one with `go run ./scripts/gates gate03`.\n")
	devDoc.WriteString("\n`GITLAB_MCP_TEST_INSTANCE` points the binary at the fake.\n")

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
			"README.md":             readme.String(),
			"CLAUDE.md":             "# CLAUDE\n\n## Where things go\n\n" + strings.Join(bullets, "\n") + "\n\n## Next\n",
			stalenessArch:           arch,
			stalenessConfigDoc:      configDoc.String(),
			stalenessDevDoc:         devDoc.String(),
			stalenessSetupDoc:       setup.String(),
			"CONTRIBUTING.md":       "See `Makefile` and `ci.yml`.\n",
			"CHANGELOG.md":          "\n### Added\n\n- A thing.\n",
			"CHANGELOG.md#all":      "## [Unreleased]\n\n### Added\n\n- A thing.\n",
			"docs/extra.md":         "Nothing.\n",
			"docs/linked.md#ignore": "",
		},
		exists:     func(rel string) bool { return files[rel] },
		topLevel:   map[string]bool{"cmd": true, "internal": true, "scripts": true, "docs": true},
		packages:   packages,
		hasGo:      func(rel string) bool { return rel != "scripts/internal" },
		configVars: vars,
		devVars:    []string{"GITLAB_MCP_TEST_INSTANCE"},
		modes:      modes,
		commands:   commandNames,
		built:      true,
		tools:      slices.Clone(stalenessTestTools),
	}
}

func TestStalenessPassesOnTheFixture(t *testing.T) {
	r := stalenessCheck(stalenessFixture())
	wantClean(t, r.problems)
	if len(r.read) != 11 {
		t.Errorf("%d rules reported what they read, want 11: %q", len(r.read), r.read)
	}
	joined := strings.Join(r.read, "\n")
	for _, want := range []string{"package map: 22 listed paths, 23 packages", "§8 tool counts: 25 table rows",
		"README tool table: 10 rows", "prose counts: 1 numbers", "setup blocks: 2 modes", "settings: 11 variables"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the report does not say %q:\n%s", want, joined)
		}
	}
}

// A release commit moves [Unreleased] under the new version before the
// tag exists; its entries count as the changes since the last tag.
func TestStalenessPassesOnAReleaseCut(t *testing.T) {
	in := stalenessFixture()
	in.docs[stalenessArch] = strings.Replace(in.docs[stalenessArch], "phase 0 of 5, nothing tagged yet", "v0.2.0", 1)
	in.newestTag = "0.1.0"
	in.changedSince = []string{"internal/p01/a.go"}
	in.docs["CHANGELOG.md"] = "\n"
	in.docs["CHANGELOG.md#all"] = "## [Unreleased]\n\n## [0.2.0] - 2026-02-01\n\n### Added\n\n- A thing.\n\n## [0.1.0] - 2026-01-01\n"
	wantClean(t, stalenessCheck(in).problems)
}

func TestStalenessRefuses(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(in *stalenessInputs)
		want   string
	}{
		{"a package the map does not name", func(in *stalenessInputs) {
			in.packages = append(in.packages, "internal/new")
		}, "does not name the package internal/new/"},
		{"a listed path that does not exist", func(in *stalenessInputs) {
			in.docs["CLAUDE.md"] = strings.Replace(in.docs["CLAUDE.md"], "## Next", "- `internal/gone/` planned\n\n## Next", 1)
		}, "lists internal/gone/, which does not exist"},
		{"a named path that does not exist", func(in *stalenessInputs) {
			in.docs["docs/extra.md"] = "See `internal/p99/x.go` and [the plan](plan.md).\n"
		}, "docs/extra.md names internal/p99/x.go"},
		{"a relative link that does not resolve", func(in *stalenessInputs) {
			in.docs["docs/extra.md"] = "See [the plan](plan.md).\n"
		}, "docs/extra.md links to plan.md"},
		{"a bare workflow name that does not exist", func(in *stalenessInputs) {
			in.docs["CONTRIBUTING.md"] = "See `Makefile` and `lint.yml`.\n"
		}, "CONTRIBUTING.md names lint.yml"},
		{"a floor on paths", func(in *stalenessInputs) {
			in.docs["README.md"] = strings.ReplaceAll(in.docs["README.md"], "`", "")
		}, "want at least 30"},
		{"an undocumented variable", func(in *stalenessInputs) {
			in.configVars = append(in.configVars, "GITLAB_MCP_NEW")
		}, "docs/configuration.md does not document GITLAB_MCP_NEW"},
		{"a documented variable nothing reads", func(in *stalenessInputs) {
			in.docs[stalenessConfigDoc] += "| `GITLAB_MCP_OLD` | x |\n"
		}, "documents GITLAB_MCP_OLD, which the server does not read"},
		{"a development override in the configuration document", func(in *stalenessInputs) {
			in.docs[stalenessConfigDoc] += "| `GITLAB_MCP_TEST_INSTANCE` | x |\n"
		}, "documents GITLAB_MCP_TEST_INSTANCE, a development override that belongs in docs/development.md only"},
		{"an undocumented development override", func(in *stalenessInputs) {
			in.docs[stalenessDevDoc] = strings.ReplaceAll(in.docs[stalenessDevDoc], "GITLAB_MCP_TEST_INSTANCE", "the override")
		}, "docs/development.md does not document the development override GITLAB_MCP_TEST_INSTANCE"},
		{"an undocumented gate", func(in *stalenessInputs) {
			in.commands = append(in.commands, "newgate")
		}, "does not name the newgate gate"},
		{"a documented gate nothing registers", func(in *stalenessInputs) {
			in.docs[stalenessDevDoc] += "Also `gates oldgate`.\n"
		}, "shows `gates oldgate`, which the registry does not have"},
		{"a status line claiming a version nothing released", func(in *stalenessInputs) {
			in.docs[stalenessArch] = strings.Replace(in.docs[stalenessArch], "phase 0 of 5, nothing tagged yet", "v0.2.0", 1)
		}, "claims [0.2.0] and nothing is tagged"},
		{"a status line behind the tag", func(in *stalenessInputs) {
			in.newestTag = "0.3.0"
		}, "the status line says nothing is tagged"},
		{"a design-only status line over code", func(in *stalenessInputs) {
			in.docs[stalenessArch] = strings.Replace(in.docs[stalenessArch], "phase 0 of 5, nothing tagged yet",
				"design only; nothing tagged", 1)
		}, "the status line says nothing is built"},
		{"shipped Go since the tag with no entry", func(in *stalenessInputs) {
			in.docs[stalenessArch] = strings.Replace(in.docs[stalenessArch], "phase 0 of 5, nothing tagged yet", "v0.1.0", 1)
			in.newestTag = "0.1.0"
			in.changedSince = []string{"internal/p01/a.go"}
			in.docs["CHANGELOG.md"] = "\n### Added\n\n"
		}, "1 shipped file(s) changed since v0.1.0"},
		{"shipped Go since the tag with only the tag's own entries", func(in *stalenessInputs) {
			in.docs[stalenessArch] = strings.Replace(in.docs[stalenessArch], "phase 0 of 5, nothing tagged yet", "v0.1.0", 1)
			in.newestTag = "0.1.0"
			in.changedSince = []string{"internal/p01/a.go"}
			in.docs["CHANGELOG.md"] = "\n"
			in.docs["CHANGELOG.md#all"] = "## [Unreleased]\n\n## [0.1.0] - 2026-01-01\n\n### Added\n\n- A thing.\n"
		}, "1 shipped file(s) changed since v0.1.0"},
		{"a version in prose", func(in *stalenessInputs) {
			in.docs["docs/extra.md"] = "This is v1.2.3 of the server.\n"
		}, "docs/extra.md:1 writes v1.2.3 in prose"},
		{"§8's sentence disagrees with its table", func(in *stalenessInputs) {
			in.docs[stalenessArch] = strings.Replace(in.docs[stalenessArch], "thirteen in read-only", "fourteen in read-only", 1)
		}, `§8 says "fourteen" in read-only mode; the table says 13`},
		{"§8 without its sentence", func(in *stalenessInputs) {
			in.docs[stalenessArch] = strings.Replace(in.docs[stalenessArch], "Twenty-five tools. With", "With", 1)
		}, "§8 states no tool counts"},
		{"a tool §8 does not list", func(in *stalenessInputs) {
			in.tools = append(in.tools, "new_tool")
		}, "the binary registers new_tool, which §8's table does not list"},
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
	for s, want := range map[string]int{"66": 66, "Sixty-six": 66, "thirteen": 13, "forty": 40, "two": 2} {
		if got, ok := stalenessNumber(s); !ok || got != want {
			t.Errorf("stalenessNumber(%q) = %d, %v; want %d", s, got, ok, want)
		}
	}
	for _, s := range []string{"sixty-ten", "many", "ten-one"} {
		if _, ok := stalenessNumber(s); ok {
			t.Errorf("stalenessNumber(%q) read a number", s)
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
