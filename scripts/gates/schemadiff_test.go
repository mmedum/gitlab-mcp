package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

func TestCompareSurfaces(t *testing.T) {
	base := dumpJSON(map[string][]string{"get_issue": {"project", "iid"}, "list_tree": {"project"}}, "gitlab://projects/{id}/issues/{iid}")
	cases := []struct {
		name     string
		cur      string
		breaking []string
		additive []string
		other    int
	}{
		{name: "unchanged", cur: base},
		{name: "a tool removed", cur: dumpJSON(map[string][]string{"get_issue": {"project", "iid"}}, "gitlab://projects/{id}/issues/{iid}"),
			breaking: []string{"tool removed: list_tree"}},
		{name: "an input removed", cur: dumpJSON(map[string][]string{"get_issue": {"project"}, "list_tree": {"project"}}, "gitlab://projects/{id}/issues/{iid}"),
			breaking: []string{"get_issue: input removed: iid"}},
		{name: "a tool and an input added", cur: dumpJSON(map[string][]string{"get_issue": {"project", "iid", "fields"}, "list_tree": {"project"},
			"get_file": {"project"}}, "gitlab://projects/{id}/issues/{iid}"),
			additive: []string{"get_issue: input added: fields", "tool added: get_file"}},
		{name: "a resource template removed", cur: dumpJSON(map[string][]string{"get_issue": {"project", "iid"}, "list_tree": {"project"}}),
			breaking: []string{"resource template removed: gitlab://projects/{id}/issues/{iid}"}},
		{name: "a description changed", cur: strings.Replace(base, "Reads get_issue.", "Reads one issue.", 1), other: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := compareSurfaces(mustDump(t, base), mustDump(t, tc.cur))
			if strings.Join(c.Breaking, "|") != strings.Join(tc.breaking, "|") {
				t.Errorf("breaking = %q, want %q", c.Breaking, tc.breaking)
			}
			if strings.Join(c.Additive, "|") != strings.Join(tc.additive, "|") {
				t.Errorf("additive = %q, want %q", c.Additive, tc.additive)
			}
			if len(c.Other) != tc.other {
				t.Errorf("other = %q, want %d", c.Other, tc.other)
			}
		})
	}
}

// The rules that need a hand-edited dump: a new required input, a lost
// output field, and a change only the whole object shows.
func TestCompareSurfacesOnDetail(t *testing.T) {
	base := dumpJSON(map[string][]string{"get_issue": {"project", "iid"}})
	cases := []struct {
		name, from, to, want string
		breaking             bool
	}{
		{"a new required input", `"required":["project"]`, `"required":["project","iid"]`, "get_issue: input now required: iid", true},
		{"a nested output field removed", `"title":{"type":"string"}`, `"name":{"type":"string"}`, "get_issue: output field removed: items.title", true},
		{"an input's minimum", `"type":"string","description":"the iid"`, `"type":"string","description":"the iid","minLength":1`,
			"get_issue: description, schema detail, annotations or _meta", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(base, tc.from) {
				t.Fatalf("fixture lacks %s", tc.from)
			}
			c := compareSurfaces(mustDump(t, base), mustDump(t, strings.Replace(base, tc.from, tc.to, 1)))
			got := strings.Join(append(append(c.Breaking, c.Additive...), c.Other...), "|")
			if !strings.Contains(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			if tc.breaking != (len(c.Breaking) > 0) {
				t.Errorf("breaking = %v, want %v", c.Breaking, tc.breaking)
			}
		})
	}
}

func TestCheckAck(t *testing.T) {
	additive := surfaceChanges{Additive: []string{"tool added: get_file"}}
	breaking := surfaceChanges{Breaking: []string{"tool removed: list_tree"}}
	cases := []struct {
		name     string
		c        surfaceChanges
		messages []string
		wantErr  string
	}{
		{"no change needs nothing", surfaceChanges{}, nil, ""},
		{"an additive change acknowledged", additive, []string{"add get_file\n\nSCHEMA-CHANGE: get_file added\n"}, ""},
		{"an additive change unacknowledged", additive, []string{"add get_file"}, "no commit since"},
		{"a mention in prose is not a footer", additive, []string{"this adds a SCHEMA-CHANGE: nothing"}, "no commit since"},
		{"an empty footer is not one", additive, []string{"x\n\nSCHEMA-CHANGE:\n"}, "no commit since"},
		{"a breaking change acknowledged", breaking, []string{"drop list_tree\n\nBREAKING CHANGE: list_tree is gone"}, ""},
		{"a breaking change called additive", breaking, []string{"drop list_tree\n\nSCHEMA-CHANGE: list_tree is gone"}, "says BREAKING CHANGE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkAck(&bytes.Buffer{}, tc.c, tc.messages, strings.Repeat("a", 40))
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("err = %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("err = %v, want %q", err, tc.wantErr)
			}
		})
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
	cmd := exec.Command("git", append([]string{"-c", "user.name=alice", "-c", "user.email=alice@example.com",
		"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
	cmd.Dir = r.root
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes a main that prints dump, or does not compile when dump
// is empty.
func (r *fakeRepo) commit(dump, message string) string {
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
	r.git("add", "-A")
	r.git("commit", "--quiet", "-m", message)
	return r.git("rev-parse", "HEAD")
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

	var out bytes.Buffer
	err := schemaDiff(&out, []string{bin})
	if err == nil || !strings.Contains(err.Error(), "breaking change(s) to the tool surface since v0.1.0") {
		t.Fatalf("err = %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "BREAKING  tool removed: list_tree") {
		t.Errorf("out = %s", out.String())
	}
	if n := r.worktrees(); n != 1 {
		t.Errorf("%d worktrees after the diff; the scratch one was not removed", n)
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

func TestSchemaAckInAPullRequest(t *testing.T) {
	r := newFakeRepo(t)
	base := r.commit(dumpJSON(manyTools()), "base")
	r.commit(dumpJSON(manyTools("get_file")), "add get_file")
	bin := r.build()
	t.Chdir(r.root)

	if err := schemaAck(&bytes.Buffer{}, []string{bin, base, "HEAD"}); err == nil || !strings.Contains(err.Error(), "SCHEMA-CHANGE") {
		t.Fatalf("unacknowledged: err = %v", err)
	}
	r.git("commit", "--quiet", "--allow-empty", "-m", "acknowledge\n\nSCHEMA-CHANGE: get_file added")
	var out bytes.Buffer
	if err := schemaAck(&out, []string{bin, base, "HEAD"}); err != nil {
		t.Fatalf("acknowledged: %v\n%s", err, out.String())
	}
	if n := r.worktrees(); n != 1 {
		t.Errorf("%d worktrees left", n)
	}
}

// A pull request from before the server existed compares with an empty
// surface: every tool is added, and has to be acknowledged.
func TestSchemaAckFromBeforeTheServer(t *testing.T) {
	r := newFakeRepo(t)
	r.git("add", "-A")
	r.git("commit", "--quiet", "-m", "initial")
	base := r.git("rev-parse", "HEAD")
	r.commit(dumpJSON(manyTools()), "the server")
	bin := r.build()
	t.Chdir(r.root)
	if err := schemaAck(&bytes.Buffer{}, []string{bin, base, "HEAD"}); err == nil || !strings.Contains(err.Error(), "SCHEMA-CHANGE") {
		t.Fatalf("unacknowledged: err = %v", err)
	}
	r.git("commit", "--quiet", "--allow-empty", "-m", "acknowledge\n\nSCHEMA-CHANGE: the first tools")
	if err := schemaAck(&bytes.Buffer{}, []string{bin, base, "HEAD"}); err != nil {
		t.Fatalf("acknowledged: %v", err)
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
