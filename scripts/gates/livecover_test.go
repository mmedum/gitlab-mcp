package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// liveSurface is a dump of surfaceFloor tools, each with a project
// input, and get_issue with an iid besides.
func liveSurface(t *testing.T) schemaDump {
	t.Helper()
	tools := manyTools()
	tools["get_issue"] = []string{"project", "iid"}
	return mustDump(t, dumpJSON(tools))
}

// fullRecord sends every option of every tool.
func fullRecord(d schemaDump) map[string]int {
	r := map[string]int{}
	for _, tool := range d.Tools {
		r[tool.Name+".*"] = 2
		for _, o := range tool.options() {
			r[tool.Name+"."+o] = 1
		}
	}
	return r
}

func TestLiveCoverHoldsAFullRun(t *testing.T) {
	d := liveSurface(t)
	report, problems := checkLiveCover(d, fullRecord(d), nil, 0)
	if len(problems) > 0 {
		t.Fatalf("%s", strings.Join(problems, "\n"))
	}
	// Every tool called and its project option sent, get_issue's iid too.
	n := 2*(surfaceFloor+1) + 1
	if !strings.Contains(report, fmt.Sprintf("%d of %d tools and options sent", n, n)) {
		t.Errorf("report = %s", report)
	}
}

func TestLiveCoverFailsEveryWay(t *testing.T) {
	why := "needs a second account to review; the scratch project has one"
	cases := []struct {
		name    string
		record  func(map[string]int)
		waivers map[string]liveWaiver
		ceiling int
		want    string
	}{
		{name: "an option neither sent nor waived",
			record: func(r map[string]int) { delete(r, "get_issue.iid") },
			want:   "get_issue.iid is an option no live step sent"},
		{name: "a tool never called",
			record: func(r map[string]int) { delete(r, "get_issue.*") },
			want:   "get_issue.* is a tool the live run never called"},
		{name: "a waiver for what was sent",
			waivers: map[string]liveWaiver{"get_issue.iid": {verdict: "undrivable", reason: why, line: 3}},
			want:    "get_issue.iid is waived as undrivable and the last run sent it"},
		{name: "a waiver for an option the surface lost",
			waivers: map[string]liveWaiver{"get_issue.labels": {verdict: "undrivable", reason: why, line: 4}},
			want:    "get_issue.labels names a tool or option the surface does not have"},
		{name: "a record from an older build",
			record: func(r map[string]int) { r["list_labels.*"] = 1 },
			want:   "records list_labels.*, which the surface does not have"},
		{name: "undriven over the ceiling",
			record:  func(r map[string]int) { delete(r, "get_issue.iid") },
			waivers: map[string]liveWaiver{"get_issue.iid": {verdict: "undriven", reason: why, line: 5}},
			want:    "1 options are waived as undriven and the ceiling is 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := liveSurface(t)
			r := fullRecord(d)
			if tc.record != nil {
				tc.record(r)
			}
			_, problems := checkLiveCover(d, r, tc.waivers, tc.ceiling)
			if joined := strings.Join(problems, "\n"); !strings.Contains(joined, tc.want) {
				t.Errorf("want %q, got:\n%s", tc.want, joined)
			}
		})
	}
	// Under the ceiling, the same waiver passes.
	d := liveSurface(t)
	r := fullRecord(d)
	delete(r, "get_issue.iid")
	if _, problems := checkLiveCover(d, r, map[string]liveWaiver{"get_issue.iid": {verdict: "undriven", reason: why}}, 1); len(problems) > 0 {
		t.Errorf("a waiver under the ceiling failed: %v", problems)
	}
}

func TestLiveRecordFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, problems := readLiveRecord(write("empty.tsv", "# written by scripts/livegitlab\n")); !strings.Contains(strings.Join(problems, ""), "records nothing") {
		t.Errorf("an empty record passed: %v", problems)
	}
	if _, problems := readLiveRecord(filepath.Join(dir, "missing.tsv")); len(problems) == 0 {
		t.Error("a missing record passed")
	}
	if _, problems := readLiveRecord(write("bad.tsv", "get_issue.iid\tmany\n")); len(problems) == 0 {
		t.Error("a bad count passed")
	}
	rec, problems := readLiveRecord(write("ok.tsv", "get_issue.*\t2\nget_issue.iid\t1\n"))
	if len(problems) > 0 || rec["get_issue.*"] != 2 || rec["get_issue.iid"] != 1 {
		t.Errorf("record = %v, %v", rec, problems)
	}
	cases := map[string]string{
		"get_issue.iid\tundriven\tneeds the second account the run does not have": "",
		"get_issue.iid\tskipped\tneeds the second account the run does not have":  `verdict "skipped"`,
		"get_issue.iid\tundriven\tlater":                                          "not a decision",
		"get_issue\tundriven\tneeds the second account the run does not have":     "is not tool.option",
	}
	for line, want := range cases {
		w, problems := readLiveWaivers(write("w.tsv", line+"\n"))
		joined := strings.Join(problems, "\n")
		if want == "" && (len(problems) > 0 || len(w) != 1) {
			t.Errorf("%q: %v", line, problems)
		}
		if want != "" && !strings.Contains(joined, want) {
			t.Errorf("%q: want %q, got %q", line, want, joined)
		}
	}
}
