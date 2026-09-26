package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheRealRecordHolds runs the gate over the committed snapshot, the
// committed record and the client as it is.
func TestTheRealRecordHolds(t *testing.T) {
	t.Chdir("../..")
	var out strings.Builder
	if err := apiCoverage(&out, nil); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "api coverage ok") {
		t.Errorf("no success line: %s", out.String())
	}
}

// fixtureSnapshot is a published surface small enough to reason about.
func fixtureSnapshot() apiSnapshot {
	return apiSnapshot{Tag: "v1.0.0-ee", Operations: []apiOperation{
		{Verb: "GET", Path: "/projects/{id}"},
		{Verb: "PUT", Path: "/projects/{id}"},
		{Verb: "GET", Path: "/projects/{id}/hooks"},
		{Verb: "POST", Path: "/projects/{id}/hooks"},
		{Verb: "GET", Path: "/groups/{id}/(-/)search"},
		{Verb: "GET", Path: "/projects/{id}/lab", Lifecycle: "experiment"},
	}}
}

func fixtureRows() []coverageRow {
	return []coverageRow{
		{verb: "GET", path: "/projects/{id}", verdict: "used", count: 1, reason: "Client.GetProject: get_project", line: 1},
		{verb: "PUT", path: "/projects/{id}", verdict: "written-off", count: 1, reason: "project update is administration", line: 2},
		{verb: "*", path: "/projects/{id}/hooks", rule: true, verdict: "written-off", count: 2, reason: "webhooks are administration", line: 3},
		{verb: "GET", path: "/groups/{id}/(-/)search", verdict: "used", count: 1, reason: "Client.Search: search inside a group", line: 4},
		{verb: "GET", path: "/projects/{id}/lab", verdict: "written-off", count: 1, reason: "experiment lifecycle, not stable", line: 5},
	}
}

func fixtureCalls() []clientCall {
	return []clientCall{
		{Method: "GET", Path: "projects/{}", Func: "Client.GetProject", Pos: "reads.go:1"},
		{Method: "GET", Path: "groups/{}/search", Func: "Client.Search", Pos: "reads.go:2"},
		{Method: "GET", Path: "oauth/token/info", Web: true, Func: "Client.TokenInfo", Pos: "reads.go:3"},
	}
}

var fixtureFloors = coverageFloors{rows: 1, calls: 1, used: 1}

func TestTheFixtureRecordHolds(t *testing.T) {
	report, problems := checkCoverage(fixtureSnapshot(), fixtureRows(), fixtureCalls(), fixtureFloors)
	if len(problems) > 0 {
		t.Fatalf("problems on a correct fixture:\n%s", strings.Join(problems, "\n"))
	}
	want := "6 operations at v1.0.0-ee (2 used, 0 gated, 0 deferred, 4 written off) from 4 rows and 1 rules; 3 client calls bound"
	if !strings.Contains(report, want) {
		t.Errorf("report = %q, want it to contain %q", report, want)
	}
}

// Each case breaks one thing and names the problem it must produce.
func TestTheCoverageGateFailsEveryWay(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(s *apiSnapshot, rows *[]coverageRow, calls *[]clientCall, fl *coverageFloors)
		want   string
	}{
		{"an operation with no verdict", func(s *apiSnapshot, _ *[]coverageRow, _ *[]clientCall, _ *coverageFloors) {
			s.Operations = append(s.Operations, apiOperation{Verb: "DELETE", Path: "/projects/{id}"})
		}, "DELETE /projects/{id} has no verdict"},
		{"a row about an operation that is gone", func(s *apiSnapshot, _ *[]coverageRow, _ *[]clientCall, _ *coverageFloors) {
			s.Operations = s.Operations[1:]
			// the client still calls it, too
		}, "GET /projects/{id} is not in the snapshot"},
		{"a rule that covers nothing", func(_ *apiSnapshot, rows *[]coverageRow, _ *[]clientCall, _ *coverageFloors) {
			*rows = append(*rows, coverageRow{verb: "*", path: "/runners", rule: true, verdict: "written-off", count: 1,
				reason: "runners are infrastructure", line: 9})
		}, "the rule * /runners* covers no operation"},
		{"a rule whose count moved", func(_ *apiSnapshot, rows *[]coverageRow, _ *[]clientCall, _ *coverageFloors) {
			(*rows)[2].count = 3
		}, "covers 2 operations and says 3"},
		{"two rules on one operation", func(_ *apiSnapshot, rows *[]coverageRow, _ *[]clientCall, _ *coverageFloors) {
			*rows = append(*rows, coverageRow{verb: "POST", path: "/projects/{id}/ho", rule: true, verdict: "deferred", count: 1,
				reason: "a second opinion on hooks", line: 9})
		}, "POST /projects/{id}/hooks is matched by 2 rules"},
		{"a client call with no row", func(_ *apiSnapshot, _ *[]coverageRow, calls *[]clientCall, _ *coverageFloors) {
			*calls = append(*calls, clientCall{Method: "PUT", Path: "projects/{}", Func: "Client.UpdateProject", Pos: "writes.go:1"})
		}, "Client.UpdateProject calls PUT /projects/{id} and testdata/api-coverage.tsv gives it written-off"},
		{"a client call to nothing published", func(_ *apiSnapshot, _ *[]coverageRow, calls *[]clientCall, _ *coverageFloors) {
			*calls = append(*calls, clientCall{Method: "GET", Path: "projects/{}/nowhere", Func: "Client.Nowhere", Pos: "reads.go:9"})
		}, "which is no operation in the snapshot"},
		{"a used row nothing calls", func(_ *apiSnapshot, _ *[]coverageRow, calls *[]clientCall, _ *coverageFloors) {
			*calls = (*calls)[1:]
		}, "GET /projects/{id} is used and no Call literal"},
		{"a used row naming the wrong method", func(_ *apiSnapshot, rows *[]coverageRow, _ *[]clientCall, _ *coverageFloors) {
			(*rows)[0].reason = "Client.ReadProject: get_project"
		}, "is made by Client.GetProject, which the row does not name"},
		{"a call to an experiment", func(_ *apiSnapshot, rows *[]coverageRow, calls *[]clientCall, _ *coverageFloors) {
			(*rows)[4].verdict, (*rows)[4].reason = "used", "Client.Lab: an experiment"
			*calls = append(*calls, clientCall{Method: "GET", Path: "projects/{}/lab", Func: "Client.Lab", Pos: "reads.go:7"})
		}, "which GitLab marks experiment"},
		{"a web call nobody excused", func(_ *apiSnapshot, _ *[]coverageRow, calls *[]clientCall, _ *coverageFloors) {
			*calls = append(*calls, clientCall{Method: "POST", Path: "oauth/revoke", Web: true, Func: "Client.Revoke", Pos: "auth.go:1"})
		}, "list it in offSnapshotCalls"},
		{"an excused web call the client no longer makes", func(_ *apiSnapshot, _ *[]coverageRow, calls *[]clientCall, _ *coverageFloors) {
			*calls = (*calls)[:2]
		}, "offSnapshotCalls lists GET oauth/token/info and the client makes no such call"},
		{"the floor on rows", func(_ *apiSnapshot, _ *[]coverageRow, _ *[]clientCall, fl *coverageFloors) {
			fl.rows = 100
		}, "has 5 rows and the floor is 100"},
		{"the floor on client calls", func(_ *apiSnapshot, _ *[]coverageRow, _ *[]clientCall, fl *coverageFloors) {
			fl.calls = 100
		}, "has 3 Call literals and the floor is 100"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, rows, calls, fl := fixtureSnapshot(), fixtureRows(), fixtureCalls(), fixtureFloors
			tc.break_(&s, &rows, &calls, &fl)
			_, problems := checkCoverage(s, rows, calls, fl)
			joined := strings.Join(problems, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Errorf("want a problem containing %q, got:\n%s", tc.want, joined)
			}
		})
	}
}

func TestReadCoverageRefusesMalformedRows(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"GET /projects\tused\t1\tClient.SearchProjects: search_projects", ""},
		{"GET /projects\tmaybe\t1\ta reason long enough to count", `verdict "maybe"`},
		{"GET /projects\tdeferred\t1\tlater", "say why in a sentence"},
		{"GET /projects\tdeferred\tmany\tphase 1 (§16): a tool reads it", `count "many"`},
		{"GET /projects\tdeferred\t2\tphase 1 (§16): a tool reads it", "names one operation and counts 2"},
		{"* /projects\tdeferred\t1\tphase 1 (§16): a tool reads it", "names every verb of one path"},
		{"* /projects*\tused\t3\tClient.SearchProjects: search_projects", "a rule marked used"},
		{"projects\tdeferred\t1\tphase 1 (§16): a tool reads it", `is not "VERB /path"`},
		{"GET /projects\tdeferred\t1", "want 4 tab-separated columns"},
		// A trailing TAB is an empty reason, not a wrong column count.
		{"GET /projects\tdeferred\t1\t", "say why in a sentence"},
	}
	for _, tc := range cases {
		path := filepath.Join(t.TempDir(), "coverage.tsv")
		if err := os.WriteFile(path, []byte("# comment\n\n"+tc.line+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		rows, problems := readCoverage(path)
		joined := strings.Join(problems, "\n")
		switch {
		case tc.want == "" && (len(problems) > 0 || len(rows) != 1):
			t.Errorf("%q: want one clean row, got %d rows and %s", tc.line, len(rows), joined)
		case tc.want != "" && !strings.Contains(joined, tc.want):
			t.Errorf("%q: want a problem containing %q, got %q", tc.line, tc.want, joined)
		}
	}
}

func TestADuplicatedRowIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coverage.tsv")
	row := "GET /projects\tdeferred\t1\tphase 1 (§16): a tool reads it\n"
	if err := os.WriteFile(path, []byte(row+row), 0o600); err != nil {
		t.Fatal(err)
	}
	_, problems := readCoverage(path)
	if !strings.Contains(strings.Join(problems, "\n"), "listed twice") {
		t.Errorf("a row listed twice was accepted: %v", problems)
	}
}

func TestNormalizedPaths(t *testing.T) {
	cases := map[string][]string{
		"/projects/{id}/issues/{issue_iid}": {"projects/{}/issues/{}"},
		"/groups/{id}/(-/)search":           {"groups/{}/-/search", "groups/{}/search"},
		"/metadata":                         {"metadata"},
		"/projects/{id}/packages/helm/{channel}/charts/{file_name}.tgz": {"projects/{}/packages/helm/{}/charts/{}.tgz"},
	}
	for in, want := range cases {
		got := normalizedPaths(in)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("normalizedPaths(%q) = %q, want %q", in, got, want)
		}
	}
}

// clientCalls reads the literals the way the gates need them, and refuses
// the one shape it cannot read.
func TestClientCallsReadsLiterals(t *testing.T) {
	dir := t.TempDir()
	src := `package gapi

type Call struct{ Method, Path string; Root int; Query, Body any; Args []string }
const RootWeb = 1
type Client struct{}
func (c *Client) Do(_ any, _ Call, _ any) error { return nil }

func (c *Client) GetThing(id string) error {
	var out struct{}
	return c.Do(nil, Call{Method: "GET", Path: "things/{}", Args: []string{id}}, &out)
}

func (c *Client) Info() error {
	return c.Do(nil, Call{Method: "GET", Path: "oauth/token/info", Root: RootWeb}, nil)
}

func (c *Client) Computed(p string) error {
	return c.Do(nil, Call{Method: "GET", Path: "things/" + p}, nil)
}
`
	if err := os.WriteFile(filepath.Join(dir, "reads.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	calls, problems, err := clientCalls(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("read %d calls, want 2: %+v", len(calls), calls)
	}
	if calls[0].Func != "Client.GetThing" || calls[0].op() != "GET things/{}" || calls[0].Out == nil || calls[0].Web {
		t.Errorf("first call = %+v", calls[0])
	}
	if calls[1].Func != "Client.Info" || !calls[1].Web {
		t.Errorf("second call = %+v", calls[1])
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "Client.Computed") {
		t.Errorf("a computed Path was not refused: %v", problems)
	}
}
