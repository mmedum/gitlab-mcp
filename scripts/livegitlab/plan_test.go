package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func sampleScratch() scratch {
	return scratch{
		Namespace: "example-group/scratch", Name: "gitlab-mcp-live-20260926-0a0b0c",
		Path: "example-group/scratch/gitlab-mcp-live-20260926-0a0b0c", ID: 1234,
		WebURL:  "https://gitlab.example.com/example-group/scratch/gitlab-mcp-live-20260926-0a0b0c",
		Default: "main", Feature: "feature-0a0b0c", Feature2: "draft-0a0b0c", File: "docs/live.md",
		SHA: strings.Repeat("a", 40), Issue: 1, Issue2: 2, MR: 1, MR2: 2, Note: 77, User: "alice", Label: "live-0a0b0c",
	}
}

func TestRunName(t *testing.T) {
	got, err := runName(time.Date(2026, 9, 26, 23, 30, 0, 0, time.FixedZone("x", -3*3600)), bytes.NewReader([]byte{0x0a, 0x0b, 0x0c}))
	if err != nil {
		t.Fatal(err)
	}
	if got != "gitlab-mcp-live-20260927-0a0b0c" {
		t.Errorf("runName = %q", got)
	}
	if _, err := runName(time.Now(), bytes.NewReader(nil)); err == nil {
		t.Error("a name without randomness was accepted")
	}
}

// The plan sends every option of every tool the committed surface has.
// The surface is read from the baseline, so a tool added without a step
// fails here before it fails live-cover after a run.
func TestThePlanDrivesEveryOption(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "schema-baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Properties map[string]any `json:"properties"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Tools) < 10 {
		t.Fatalf("the baseline has %d tools", len(d.Tools))
	}
	sent := map[string]bool{}
	for _, st := range plan(sampleScratch()) {
		sent[st.tool+".*"] = true
		for k := range st.args {
			sent[st.tool+"."+k] = true
		}
		if st.paged {
			sent[st.tool+".page_token"] = true
		}
	}
	var missing []string
	for _, tool := range d.Tools {
		if !sent[tool.Name+".*"] {
			missing = append(missing, tool.Name+".*")
		}
		for opt := range tool.InputSchema.Properties {
			if !sent[tool.Name+"."+opt] {
				missing = append(missing, tool.Name+"."+opt)
			}
		}
	}
	slices.Sort(missing)
	if len(missing) > 0 {
		t.Errorf("the plan never sends: %s", strings.Join(missing, ", "))
	}
}

func TestEveryStepStaysInTheScratchProject(t *testing.T) {
	s := sampleScratch()
	for _, st := range plan(s) {
		if err := confined(st, s); err != nil {
			t.Errorf("%v", err)
		}
	}
}

func TestConfinedRefusesAStepOutside(t *testing.T) {
	s := sampleScratch()
	cases := []struct {
		st   step
		want string
	}{
		{step{tool: "get_project", args: map[string]any{"project": "example-group/other"}}, "not the scratch project"},
		{step{tool: "get_project", args: map[string]any{"project": 99}}, "not the scratch project"},
		{step{tool: "search_issues", args: map[string]any{"group": "example-group/scratch"}}, "carry the run's word"},
		{step{tool: "search_issues", args: map[string]any{"group": "example-group", "search": s.Name}}, "carry the run's word"},
		{step{tool: "search_merge_requests", args: map[string]any{"state": "opened"}}, "instance-wide search must carry"},
		{step{tool: "search_projects", args: map[string]any{"search": "other"}}, "instance-wide search must carry"},
		{step{tool: "resolve_url", args: map[string]any{"url": "https://gitlab.example.com/example-group/other/-/issues/1"}}, "not inside"},
		{step{tool: "resolve_url", args: map[string]any{"url": s.WebURL + "-evil/-/issues/1"}}, "not inside"},
	}
	for _, tc := range cases {
		err := confined(tc.st, s)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %v: err = %v, want %q", tc.st.tool, tc.st.args, err, tc.want)
		}
	}
	for _, v := range []any{s.Path, s.WebURL, int64(1234), 1234, float64(1234)} {
		if err := confined(step{tool: "get_project", args: map[string]any{"project": v}}, s); err != nil {
			t.Errorf("project %v (%T) was refused: %v", v, v, err)
		}
	}
}

func TestNextPageToken(t *testing.T) {
	cases := map[string]string{
		`{"listing":{"next_page_token":"abc","complete":false}}`: "abc",
		`{"listing":{"next_page_token":null,"complete":true}}`:   "",
		`{"items":[]}`: "",
		`not json`:     "",
	}
	for in, want := range cases {
		if got := nextPageToken(json.RawMessage(in)); got != want {
			t.Errorf("nextPageToken(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestTheRecorderWritesWhatWasSent(t *testing.T) {
	r := newRecorder()
	r.Sent("get_issue", map[string]any{"project": "p", "iid": 1})
	r.Sent("get_issue", map[string]any{"project": "p", "iid": 2, "offset": 5})
	r.Sent("get_me", map[string]any{})
	surface := map[string][]string{"get_issue": {"iid", "offset", "project"}, "get_me": nil, "list_tree": {"project"}}
	if got := strings.Join(r.missing(surface), ","); got != "list_tree.*,list_tree.project" {
		t.Errorf("missing = %s", got)
	}
	path := filepath.Join(t.TempDir(), "record.tsv")
	if err := r.write(path, "run of 2026-09-26 against a self-managed instance"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "# Written by scripts/livegitlab at the end of a run; do not edit.\n" +
		"# run of 2026-09-26 against a self-managed instance\n" +
		"# Columns: tool.option (tool.* for the call itself), times sent.\n" +
		"get_issue.*\t2\nget_issue.iid\t2\nget_issue.offset\t1\nget_issue.project\t2\nget_me.*\t1\n"
	if string(raw) != want {
		t.Errorf("record =\n%s\nwant\n%s", raw, want)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("the write left %d files", len(entries))
	}
}
