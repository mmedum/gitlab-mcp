package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// dumpJSON builds a dump with the given tools, each {name: [inputs]},
// every input a string, the first one required, and one output field.
func dumpJSON(tools map[string][]string, templates ...string) string {
	type prop struct {
		Type        string `json:"type"`
		Description string `json:"description"`
	}
	var list []map[string]any
	for name, inputs := range tools {
		props := map[string]prop{}
		for _, in := range inputs {
			props[in] = prop{Type: "string", Description: "the " + in}
		}
		schema := map[string]any{"type": "object", "properties": props}
		if len(inputs) > 0 {
			schema["required"] = []string{inputs[0]}
		}
		list = append(list, map[string]any{
			"name": name, "description": "Reads " + name + ".", "inputSchema": schema,
			"outputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"id": map[string]any{"type": "integer"},
				"items": map[string]any{"type": "array", "items": map[string]any{"type": "object",
					"properties": map[string]any{"title": map[string]any{"type": "string"}}}},
			}},
			"annotations": map[string]any{"readOnlyHint": true},
		})
	}
	var tmpl []map[string]any
	for _, u := range templates {
		tmpl = append(tmpl, map[string]any{"uriTemplate": u, "name": u, "description": "the same text"})
	}
	raw, _ := json.Marshal(map[string]any{"server": "gitlab-mcp", "version": "v0.0.0", "sdk": "v1.8.0",
		"tools": list, "resourceTemplates": tmpl})
	return string(raw)
}

func mustDump(t *testing.T, raw string) schemaDump {
	t.Helper()
	d, err := decodeDump([]byte(raw), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// report is what compareSurfaces finds between two dumps, as written.
func report(t *testing.T, prev, cur string) string {
	t.Helper()
	var out strings.Builder
	compareSurfaces(mustDump(t, prev), mustDump(t, cur)).write(&out)
	return out.String()
}

func TestCompareSurfaces(t *testing.T) {
	const tmpl = "gitlab://projects/{id}/issues/{iid}"
	base := dumpJSON(map[string][]string{"get_issue": {"project", "iid"}, "list_tree": {"project"}}, tmpl)
	cases := []struct {
		name, cur, want string
	}{
		{name: "unchanged", cur: base},
		{name: "a tool removed", cur: dumpJSON(map[string][]string{"get_issue": {"project", "iid"}}, tmpl),
			want: "BREAKING  tool removed: list_tree\n"},
		{name: "an input removed", cur: dumpJSON(map[string][]string{"get_issue": {"project"}, "list_tree": {"project"}}, tmpl),
			want: "BREAKING  get_issue: input removed: iid\nchanged   tool get_issue: inputSchema.properties\n"},
		{name: "a tool and an input added", cur: dumpJSON(map[string][]string{"get_issue": {"project", "iid", "fields"}, "list_tree": {"project"},
			"get_file": {"project"}}, tmpl),
			want: "added     tool added: get_file\nchanged   tool get_issue: inputSchema.properties\n"},
		{name: "a resource template removed", cur: dumpJSON(map[string][]string{"get_issue": {"project", "iid"}, "list_tree": {"project"}}),
			want: "BREAKING  resource template removed: " + tmpl + "\n"},
		{name: "a resource template changed", cur: strings.Replace(base, `"the same text"`, `"other text"`, 1),
			want: "changed   resource template " + tmpl + ": description\n"},
		{name: "a description changed", cur: strings.Replace(base, "Reads get_issue.", "Reads one issue.", 1),
			want: "changed   tool get_issue: description\n"},
		{name: "a title added", cur: strings.Replace(base, `"name":"list_tree"`, `"name":"list_tree","title":"Tree"`, 1),
			want: "changed   tool list_tree: title\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := report(t, base, tc.cur); got != tc.want {
				t.Errorf("the report is\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

// The rules that need a hand-edited dump: a new required input, a lost
// output field, a change only the whole object shows, and a tool that
// broke still showing its other changes.
func TestCompareSurfacesOnDetail(t *testing.T) {
	base := dumpJSON(map[string][]string{"get_issue": {"project", "iid"}})
	const required = `"required":["project"]`
	cases := []struct {
		name  string
		edits [][2]string
		want  string
	}{
		{"a new required input", [][2]string{{required, `"required":["project","iid"]`}},
			"BREAKING  get_issue: input now required: iid\nchanged   tool get_issue: inputSchema.required\n"},
		{"a nested output field removed", [][2]string{{`"title":{"type":"string"}`, `"name":{"type":"string"}`}},
			"BREAKING  get_issue: output field removed: items.title\nchanged   tool get_issue: outputSchema.properties\n"},
		{"an input's minimum", [][2]string{{`"description":"the iid"`, `"description":"the iid","minLength":1`}},
			"changed   tool get_issue: inputSchema.properties\n"},
		{"a broken tool's flipped hint", [][2]string{
			{`"readOnlyHint":true`, `"readOnlyHint":false`}, {required, `"required":["project","iid"]`},
		}, "BREAKING  get_issue: input now required: iid\nchanged   tool get_issue: annotations.readOnlyHint, inputSchema.required\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cur := base
			for _, e := range tc.edits {
				if !strings.Contains(cur, e[0]) {
					t.Fatalf("fixture lacks %s", e[0])
				}
				cur = strings.Replace(cur, e[0], e[1], 1)
			}
			if got := report(t, base, cur); got != tc.want {
				t.Errorf("the report is\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

// Every list is compared item by item, and every other key whole: a
// removed prompt breaks a caller as a removed tool does, and a reworded
// instruction is a change to the instructions.
func TestCompareSurfacesReadsTheWholeDump(t *testing.T) {
	const base = `{"server":"gitlab-mcp","version":"v1","sdk":"v1.8.0","instructions":{"default":"Read first.","full":"Read all."},"tools":[],` +
		`"resources":[{"uri":"gitlab://about","name":"about","description":"what"}],"prompts":[{"name":"review"}]}`
	cases := []struct {
		name, from, to, want string
		item                 string
	}{
		{"a prompt removed", `"prompts":[{"name":"review"}]`, `"prompts":[]`, "BREAKING  prompt removed: review\n", "review"},
		{"a resource changed", `"description":"what"`, `"description":"why"`, "changed   resource gitlab://about: description\n",
			"gitlab://about"},
		{"the instructions changed", "Read first.", "Read it first.", "changed   instructions: default\n", "instructions"},
		{"two configurations' instructions changed", `"Read first.","full":"Read all."`, `"Read it first.","full":"Read it all."`,
			"changed   instructions: default, full\n", "instructions"},
		{"the instructions removed", `"instructions":{"default":"Read first.","full":"Read all."},`, ``, "changed   instructions\n", "instructions"},
		{"the build stamp is not the surface", `"version":"v1","sdk":"v1.8.0"`, `"version":"v2","sdk":"v1.9.0"`, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cur := strings.Replace(base, tc.from, tc.to, 1)
			if got := report(t, base, cur); got != tc.want {
				t.Errorf("the report is\n%s\nwant\n%s", got, tc.want)
			}
			c := compareSurfaces(mustDump(t, base), mustDump(t, cur))
			if items := unnamed(slices.Concat(c.breaking, c.added, c.changed), nil); strings.Join(items, " ") != tc.item {
				t.Errorf("the change is to %q, want %q", items, tc.item)
			}
		})
	}
}

// Two items of one list with one id are one item twice, which no
// comparison can tell apart.
func TestADumpNamingAnItemTwiceIsRefused(t *testing.T) {
	_, err := decodeDump([]byte(`{"tools":[{"name":"get_issue"},{"name":"get_issue"}]}`), "fixture")
	if err == nil || err.Error() != `fixture: the tools list names "get_issue" twice` {
		t.Errorf("err = %v", err)
	}
}

func TestExeFor(t *testing.T) {
	cases := []struct{ goos, in, want string }{
		{"windows", "./gitlab-mcp", "./gitlab-mcp.exe"},
		{"windows", "./gitlab-mcp.exe", "./gitlab-mcp.exe"},
		{"windows", `C:\bin\gitlab-mcp.EXE`, `C:\bin\gitlab-mcp.EXE`},
		{"linux", "./gitlab-mcp", "./gitlab-mcp"},
		{"darwin", "./gitlab-mcp", "./gitlab-mcp"},
	}
	for _, tc := range cases {
		if got := exeFor(tc.goos, tc.in); got != tc.want {
			t.Errorf("exeFor(%q, %q) = %q, want %q", tc.goos, tc.in, got, tc.want)
		}
	}
}

// ------------------------------------------------------------ in a repository

// fakeRepo is a git repository whose cmd/gitlab-mcp prints a fixed dump,
// so the worktree build is exercised without building the real server.
type fakeRepo struct {
	t    *testing.T
	root string
}

func newFakeRepo(t *testing.T) *fakeRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	r := &fakeRepo{t: t, root: t.TempDir()}
	r.git("init", "--quiet", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(r.root, "go.mod"), []byte("module example.invalid/fake\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *fakeRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", slices.Concat(gitQuiet, []string{"-c", "user.name=alice", "-c", "user.email=alice@example.com",
		"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args)...)
	cmd.Dir = r.root
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit commits a main that prints dump, or does not compile when dump
// is empty.
func (r *fakeRepo) commit(dump, message string) string {
	r.t.Helper()
	r.write(dump)
	r.git("add", "-A")
	r.git("commit", "--quiet", "-m", message)
	return r.git("rev-parse", "HEAD")
}

// write puts a main that prints dump in the working tree, uncommitted,
// or one that does not compile when dump is empty.
func (r *fakeRepo) write(dump string) {
	r.t.Helper()
	dir := filepath.Join(r.root, "cmd", "gitlab-mcp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.t.Fatal(err)
	}
	src := fmt.Sprintf("package main\n\nimport \"os\"\n\nfunc main() { os.Stdout.WriteString(%q) }\n", dump)
	if dump == "" {
		src = "package main\n\nfunc main() { undefined() }\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

// build builds the checked-out main into a binary outside the tree.
func (r *fakeRepo) build() string {
	r.t.Helper()
	bin := executable(filepath.Join(r.t.TempDir(), "gitlab-mcp"))
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/gitlab-mcp")
	cmd.Dir = r.root
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func (r *fakeRepo) worktrees() int {
	return strings.Count(r.git("worktree", "list", "--porcelain"), "worktree ")
}

func manyTools(extra ...string) map[string][]string {
	tools := map[string][]string{}
	for i := range surfaceFloor {
		tools[fmt.Sprintf("tool_%02d", i)] = []string{"project"}
	}
	for _, e := range extra {
		tools[e] = []string{"project"}
	}
	return tools
}

func TestSchemaDiffAgainstTheLastTagInAWorktree(t *testing.T) {
	r := newFakeRepo(t)
	r.commit(dumpJSON(manyTools("list_tree")), "first")
	r.git("tag", "v0.1.0")
	r.commit(dumpJSON(manyTools()), "drop list_tree")
	bin := r.build()
	t.Chdir(r.root)
	if err := os.WriteFile(filepath.Join(r.root, "CHANGELOG.md"), []byte("# Changelog\n\n## [Unreleased]\n\n## [0.1.0] - 2026-01-01\n"),
		0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := schemaDiff(&out, []string{bin})
	if err == nil || !strings.Contains(err.Error(), "breaking change(s) to the tool surface since v0.1.0 that the CHANGELOG does not declare") {
		t.Fatalf("err = %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "BREAKING  tool removed: list_tree") {
		t.Errorf("out = %s", out.String())
	}
	if n := r.worktrees(); n != 1 {
		t.Errorf("%d worktrees after the diff; the scratch one was not removed", n)
	}
}

// A break passes once the unreleased CHANGELOG declares it under
// **Breaking:**, naming its tool.
func TestSchemaDiffAcceptsADeclaredBreak(t *testing.T) {
	r := newFakeRepo(t)
	r.commit(dumpJSON(manyTools("list_tree")), "first")
	r.git("tag", "v1.4.0")
	r.commit(dumpJSON(manyTools()), "drop list_tree")
	bin := r.build()
	t.Chdir(r.root)
	changelog := "# Changelog\n\n## [Unreleased]\n\n### Removed\n\n- **Breaking:** `list_tree` is gone.\n\n## [1.4.0] - 2026-01-01\n"
	if err := os.WriteFile(filepath.Join(r.root, "CHANGELOG.md"), []byte(changelog), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := schemaDiff(&out, []string{bin}); err != nil {
		t.Fatalf("err = %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "1 breaking change(s) since v1.4.0, each declared") {
		t.Errorf("out = %s", out.String())
	}
}

func TestUndeclaredBreaks(t *testing.T) {
	c := surfaceChanges{breaking: []change{
		{item: "list_tree", text: "tool removed: list_tree"},
		{"apply_suggestions", "apply_suggestions: input now required: suggestions", "input suggestions"},
		{item: "gitlab://projects/{id}/wiki", text: "resource template removed: gitlab://projects/{id}/wiki"},
	}}
	texts := []string{"tool removed: list_tree", "apply_suggestions: input now required: suggestions",
		"resource template removed: gitlab://projects/{id}/wiki"}
	all := "`list_tree`, `apply_suggestions` and `gitlab://projects/{id}/wiki`"
	released := "\n## [1.4.0] - 2026-01-01\n\n- **Breaking:** " + all + ".\n"
	for _, tc := range []struct {
		name, changelog string
		want            []string
	}{
		{"every one declared", "## [Unreleased]\n\n- **Breaking:** " + all + ".\n" + released, nil},
		{"one not named", "## [Unreleased]\n\n- **Breaking:** `list_tree` and `gitlab://projects/{id}/wiki`.\n" + released,
			[]string{"apply_suggestions: input now required: suggestions"}},
		{"named outside a Breaking entry", "## [Unreleased]\n\n- " + all + " changed.\n" + released, texts},
		{"declared only in a release made", "## [Unreleased]\n" + released, texts},
		{"a release that raises the major", "## [2.0.0] - 2026-02-01\n\n- **Breaking:** " + all + ".\n" + released, nil},
		{"a release that does not", "## [1.5.0] - 2026-02-01\n\n- **Breaking:** " + all + ".\n" + released,
			[]string{"release 1.5.0 breaks the surface without raising the major version from 1.4.0"}},
		{"a prerelease that does not", "## [1.5.0-rc.1] - 2026-02-01\n\n- **Breaking:** " + all + ".\n" + released,
			[]string{"release 1.5.0-rc.1 breaks the surface without raising the major version from 1.4.0"}},
		// A fence holds an example: its Breaking entry declares nothing, and
		// its headings are neither the tag's nor a release.
		{"a Breaking entry in a fence", "## [Unreleased]\n\n```markdown\n- **Breaking:** " + all + ".\n```\n" + released, texts},
		{"the tag's heading and a release in a fence", "## [Unreleased]\n\n```markdown\n## [1.4.0]\n## [1.5.0]\n```\n\n" +
			"- **Breaking:** " + all + ".\n" + released, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := undeclaredBreaks(c, "# Changelog\n\n"+tc.changelog, "v1.4.0")
			if err != nil || fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("undeclared = %v, %v, want %v", got, err, tc.want)
			}
		})
	}
	// Without the tag's own heading nothing is known to be unreleased.
	if _, err := undeclaredBreaks(c, "# Changelog\n\n## [Unreleased]\n\n- **Breaking:** "+all+".\n", "v1.4.0"); err == nil ||
		!strings.Contains(err.Error(), "no ## [1.4.0] heading") {
		t.Errorf("no heading for the tag: err = %v", err)
	}
}

func TestSchemaDiffRemovesTheWorktreeWhenTheTagDoesNotBuild(t *testing.T) {
	r := newFakeRepo(t)
	r.commit("", "broken")
	r.git("tag", "v0.1.0")
	r.commit(dumpJSON(manyTools()), "fixed")
	bin := r.build()
	t.Chdir(r.root)
	err := schemaDiff(&bytes.Buffer{}, []string{bin})
	if err == nil || !strings.Contains(err.Error(), "build v0.1.0") {
		t.Fatalf("err = %v", err)
	}
	if n := r.worktrees(); n != 1 {
		t.Errorf("%d worktrees after a failed build; the scratch one was not removed", n)
	}
}

func TestSchemaDiffBeforeTheFirstTagUsesTheBaseline(t *testing.T) {
	r := newFakeRepo(t)
	r.commit(dumpJSON(manyTools()), "first")
	bin := r.build()
	t.Chdir(r.root)

	if err := schemaDiff(&bytes.Buffer{}, []string{bin}); err == nil || !strings.Contains(err.Error(), "no tag yet and no baseline") {
		t.Fatalf("with no baseline: err = %v", err)
	}
	var out bytes.Buffer
	if err := schemaBaseline(&out, []string{bin}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(baselineFile)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"version": "v0.0.0"`)) {
		t.Error("the baseline records the build's version")
	}
	out.Reset()
	if err := schemaDiff(&out, []string{bin}); err != nil {
		t.Fatalf("against its own baseline: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "against "+baselineFile) {
		t.Errorf("out = %s", out.String())
	}
}

func TestSchemaDiffRefusesASmallSurface(t *testing.T) {
	r := newFakeRepo(t)
	r.commit(dumpJSON(map[string][]string{"get_me": nil}), "tiny")
	bin := r.build()
	t.Chdir(r.root)
	for name, run := range map[string]func() error{
		"schema-diff":     func() error { return schemaDiff(&bytes.Buffer{}, []string{bin}) },
		"schema-baseline": func() error { return schemaBaseline(&bytes.Buffer{}, []string{bin}) },
	} {
		if err := run(); err == nil || !strings.Contains(err.Error(), "the floor is") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// The committed baseline is a real dump of the whole surface.
func TestTheCommittedBaselineIsADump(t *testing.T) {
	t.Chdir("../..")
	d, err := readDump(baselineFile)
	if err != nil {
		t.Fatal(err)
	}
	if d.Server != "gitlab-mcp" || len(d.Tools) < surfaceFloor || d.Version != "" {
		t.Errorf("baseline: server %q, %d tools, version %q", d.Server, len(d.Tools), d.Version)
	}
}
