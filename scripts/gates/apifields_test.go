package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixture client: a list call whose query comes from a method with a
// bool-decided branch, a single read decoding a nested struct, a call
// with a url.Values literal, and a write with a body.
const fixtureClient = `package gapi

import "net/url"

type Call struct{ Method, Path string; Args []string; Query url.Values; Body any }
type Client struct{}
type ListOptions struct{}
func (c *Client) Do(_ any, _ Call, _ any) error { return nil }
func (c *Client) list(_ any, _ Call, _ ListOptions, _ any) error { return nil }

type ItemQuery struct{ State, Reviewer string }

func (q ItemQuery) values(mergeRequests bool) url.Values {
	v := url.Values{}
	setString(v, "state", q.State)
	if mergeRequests {
		setString(v, "reviewer_username", q.Reviewer)
	}
	return v
}

func setString(v url.Values, k, s string) { v.Set(k, s) }

func (c *Client) SearchIssues(q ItemQuery) error {
	var out []gitlab.Issue
	return c.list(nil, Call{Method: "GET", Path: "issues", Query: q.values(false)}, ListOptions{}, &out)
}

func (c *Client) GetIssue(id string) error {
	var out gitlab.Issue
	return c.Do(nil, Call{Method: "GET", Path: "issues/{}", Args: []string{id}, Query: url.Values{"render": {"1"}}}, &out)
}

type issueBody struct {
	Title  string   ` + "`json:\"title\"`" + `
	Labels []string ` + "`json:\"labels\"`" + `
	Weight string   ` + "`json:\"weight\"`" + `
}

func (c *Client) GetTrace(id string) error {
	var out []byte
	return c.Do(nil, Call{Method: "GET", Path: "issues/{}/trace", Args: []string{id}}, &out)
}

func (c *Client) CreateIssue(title string) error {
	var out gitlab.Issue
	return c.Do(nil, Call{Method: "POST", Path: "issues", Body: &issueBody{Title: title}}, &out)
}
`

const fixtureWire = `package gitlab

type User struct {
	Username string ` + "`json:\"username\"`" + `
}

type Pipeline struct {
	ID  int64  ` + "`json:\"id\"`" + `
	SHA string ` + "`json:\"sha\"`" + `
}

type Position struct {
	OldPath string ` + "`json:\"old_path\"`" + `
}

type Issue struct {
	ID        int64     ` + "`json:\"id\"`" + `
	Title     string    ` + "`json:\"title\"`" + `
	Author    User      ` + "`json:\"author\"`" + `
	Assignees []User    ` + "`json:\"assignees\"`" + `
	Pipeline  *Pipeline ` + "`json:\"head_pipeline\"`" + `
	Position  *Position ` + "`json:\"position\"`" + `
	Internal  string    ` + "`json:\"-\"`" + `
}
`

func fixtureFieldSnapshot() apiSnapshot {
	issue := map[string]string{
		"id": "integer", "title": "string", "author": "object", "author.username": "string",
		"assignees": "array<object>", "assignees.username": "string", "position": "object",
	}
	return apiSnapshot{Tag: "v1.0.0-ee", Operations: []apiOperation{
		{Verb: "GET", Path: "/issues", Params: map[string]map[string]string{"query": {"state": "string", "per_page": "integer"}},
			Responses: map[string]map[string]string{"200": issue}},
		{Verb: "GET", Path: "/issues/{id}", Params: map[string]map[string]string{"query": {"render": "boolean"}},
			Responses: map[string]map[string]string{"200": issue, "404": nil}},
		{Verb: "POST", Path: "/issues", Request: map[string]string{"title": "string", "labels": "array<string>", "weight": "integer"},
			Responses: map[string]map[string]string{"201": issue}},
		// A text answer the file publishes as if it were JSON, as it
		// publishes a job log: []byte takes it as it is.
		{Verb: "GET", Path: "/issues/{id}/trace", Responses: map[string]map[string]string{"200": issue}},
	}}
}

// fixturePackages writes the two packages and parses them.
func fixturePackages(t *testing.T, client, wire string) ([]clientCall, parsedPackage, parsedPackage) {
	t.Helper()
	root := t.TempDir()
	for dir, src := range map[string]string{"gapi": client, "gitlab": wire} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "x.go"), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	calls, problems, err := clientCalls(filepath.Join(root, "gapi"))
	if err != nil || len(problems) > 0 {
		t.Fatalf("%v %v", err, problems)
	}
	c, err := parsePackageDir(filepath.Join(root, "gapi"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := parsePackageDir(filepath.Join(root, "gitlab"))
	if err != nil {
		t.Fatal(err)
	}
	return calls, c, w
}

// The excuses a correct record of the fixture needs.
func fixtureFieldRows() []fieldRow {
	why := "a reason long enough to be read as one"
	return []fieldRow{
		{op: "GET /issues", dir: "response", field: "head_pipeline", verdict: "reasoned", reason: why, line: 1},
		{op: "GET /issues/{id}", dir: "response", field: "head_pipeline", verdict: "reasoned", reason: why, line: 2},
		{op: "POST /issues", dir: "response", field: "head_pipeline", verdict: "reasoned", reason: why, line: 3},
		{op: "GET /issues", dir: "response", field: "position", verdict: "reasoned", reason: why, line: 4},
		{op: "GET /issues/{id}", dir: "response", field: "position", verdict: "reasoned", reason: why, line: 5},
		{op: "POST /issues", dir: "response", field: "position", verdict: "reasoned", reason: why, line: 6},
		{op: "POST /issues", dir: "body", field: "weight", verdict: "verified-live", reason: why, line: 7},
	}
}

var fixtureFieldFloors = fieldsFloors{ops: 3, sent: 5, decoded: 20, rows: 1}

func TestTheFixtureFieldsHold(t *testing.T) {
	calls, c, w := fixturePackages(t, fixtureClient, fixtureWire)
	report, problems := checkFields(fixtureFieldSnapshot(), fixtureFieldRows(), calls, c, w, fixtureFieldFloors)
	if len(problems) > 0 {
		t.Fatalf("problems on a correct fixture:\n%s", strings.Join(problems, "\n"))
	}
	if !strings.Contains(report, "4 operations, 6 sent and 33 decoded fields") {
		t.Errorf("report = %s", report)
	}
}

func TestTheFieldsGateFailsEveryWay(t *testing.T) {
	cases := []struct {
		name   string
		client string
		wire   string
		snap   func(*apiSnapshot)
		rows   func([]fieldRow) []fieldRow
		fl     fieldsFloors
		want   string
	}{
		{name: "a query parameter the operation does not publish",
			snap: func(s *apiSnapshot) { delete(s.Operations[0].Params["query"], "state") },
			want: "GET /issues query state: sent and not published"},
		{name: "per_page on a list that does not publish it",
			snap: func(s *apiSnapshot) { delete(s.Operations[0].Params["query"], "per_page") },
			want: "GET /issues query per_page: sent and not published"},
		{name: "the merge-request branch when the literal says true",
			client: strings.Replace(fixtureClient, "q.values(false)", "q.values(true)", 1),
			want:   "GET /issues query reviewer_username: sent and not published"},
		{name: "a body field not published",
			snap: func(s *apiSnapshot) { delete(s.Operations[2].Request, "title") },
			want: "POST /issues body title: sent and not published"},
		{name: "a body field whose kind cannot hold the published type",
			rows: func(r []fieldRow) []fieldRow { return r[:6] },
			want: "POST /issues body weight: sent as string and published as integer"},
		{name: "a decoded field not published",
			snap: func(s *apiSnapshot) { delete(s.Operations[1].Responses["200"], "title") },
			want: "GET /issues/{id} response title: decoded and not published"},
		{name: "a decoded kind that cannot hold the published type",
			snap: func(s *apiSnapshot) {
				r := map[string]string{}
				for k, v := range s.Operations[1].Responses["200"] {
					r[k] = v
				}
				r["assignees"] = "object"
				s.Operations[1].Responses["200"] = r
			},
			want: "GET /issues/{id} response assignees: decoded as array<object> and published as object"},
		{name: "an unpublished object is asked about once, at the object",
			rows: func(r []fieldRow) []fieldRow { return r[1:] },
			want: "GET /issues response head_pipeline: decoded and not published"},
		{name: "an opaque published object is asked about at the object",
			rows: func(r []fieldRow) []fieldRow { return append(r[:3:3], r[4:]...) },
			want: "GET /issues response position: published as an object with no fields"},
		{name: "a row that excuses nothing",
			rows: func(r []fieldRow) []fieldRow {
				return append(r, fieldRow{op: "GET /issues", dir: "response", field: "title", verdict: "reasoned",
					reason: "a reason long enough to be read as one", line: 9})
			},
			want: "GET /issues response title excuses nothing"},
		{name: "a wire field named like a token",
			wire: strings.Replace(fixtureWire, "`json:\"sha\"`", "`json:\"runners_token\"`", 1),
			want: "Pipeline.SHA decodes \"runners_token\""},
		{name: "the floor on decoded fields",
			fl:   fieldsFloors{ops: 3, sent: 5, decoded: 1000, rows: 1},
			want: "compared 33 decoded fields and the floor is 1000"},
		{name: "the floor on the record",
			fl:   fieldsFloors{ops: 3, sent: 5, decoded: 1, rows: 100},
			want: "has 7 rows and the floor is 100"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, wire := fixtureClient, fixtureWire
			if tc.client != "" {
				client = tc.client
			}
			if tc.wire != "" {
				wire = tc.wire
			}
			calls, c, w := fixturePackages(t, client, wire)
			snap := fixtureFieldSnapshot()
			if tc.snap != nil {
				tc.snap(&snap)
			}
			rows := fixtureFieldRows()
			if tc.rows != nil {
				rows = tc.rows(rows)
			}
			fl := fixtureFieldFloors
			if tc.fl != (fieldsFloors{}) {
				fl = tc.fl
			}
			_, problems := checkFields(snap, rows, calls, c, w, fl)
			if joined := strings.Join(problems, "\n"); !strings.Contains(joined, tc.want) {
				t.Errorf("want a problem containing %q, got:\n%s", tc.want, joined)
			}
		})
	}
}

func TestKindHolds(t *testing.T) {
	cases := []struct {
		goK, pub string
		want     bool
	}{
		{"string", "string", true},
		{"integer", "integer|string", true},
		{"number", "integer", true},
		{"integer", "number", false},
		{"string", "integer", false},
		{"array<string>", "array<string>", true},
		{"array<object>", "object", false},
		{"object", "array<object>", false},
		{"array<integer>", "array<string>", false},
		{"", "integer", true},
		{"boolean", "", true},
	}
	for _, tc := range cases {
		if got := kindHolds(tc.goK, tc.pub); got != tc.want {
			t.Errorf("kindHolds(%q, %q) = %v, want %v", tc.goK, tc.pub, got, tc.want)
		}
	}
}

func TestReadFieldRowsRefusesMalformedRows(t *testing.T) {
	cases := map[string]string{
		"GET /issues response title\treasoned\ta reason long enough to count": "",
		"GET /issues answer title\treasoned\ta reason long enough to count":   "is not \"VERB /path",
		"GET /issues response\treasoned\ta reason long enough to count":       "is not \"VERB /path",
		"GET /issues response title\tguessed\ta reason long enough to count":  `verdict "guessed"`,
		"GET /issues response title\treasoned\tshort":                         "is not a sentence",
	}
	for line, want := range cases {
		path := filepath.Join(t.TempDir(), "f.tsv")
		if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		rows, problems := readFieldRows(path)
		joined := strings.Join(problems, "\n")
		if want == "" && (len(rows) != 1 || joined != "") {
			t.Errorf("%q: want one clean row, got %v %s", line, rows, joined)
		}
		if want != "" && !strings.Contains(joined, want) {
			t.Errorf("%q: want %q, got %q", line, want, joined)
		}
	}
}

// The committed record parses, and every row names an operation the
// snapshot has. Whether each row still excuses something is the gate's
// question over the real client, which `make api-fields` asks.
func TestTheCommittedFieldRecordParses(t *testing.T) {
	t.Chdir("../..")
	rows, problems := readFieldRows(fieldsFile)
	if len(problems) > 0 {
		t.Fatal(strings.Join(problems, "\n"))
	}
	snap, err := readSnapshot(snapshotFile)
	if err != nil {
		t.Fatal(err)
	}
	ops := map[string]bool{}
	for _, op := range snap.Operations {
		ops[op.key()] = true
	}
	if len(rows) < realFieldsFloors.rows {
		t.Errorf("%d rows, under the floor of %d", len(rows), realFieldsFloors.rows)
	}
	for _, r := range rows {
		if !ops[r.op] {
			t.Errorf("line %d: %s is not an operation in the snapshot", r.line, r.op)
		}
	}
}

// A negated test on a bool parameter is decided by the literal too:
// values(true) under `if !mergeRequests` does not send what the branch
// sets. Walking the branch anyway would report reviewer_username here.
func TestANegatedBranchIsDecidedByTheLiteral(t *testing.T) {
	client := strings.Replace(fixtureClient, "if mergeRequests {", "if !mergeRequests {", 1)
	client = strings.Replace(client, "q.values(false)", "q.values(true)", 1)
	calls, c, w := fixturePackages(t, client, fixtureWire)
	_, problems := checkFields(fixtureFieldSnapshot(), fixtureFieldRows(), calls, c, w, fixtureFieldFloors)
	if joined := strings.Join(problems, "\n"); strings.Contains(joined, "reviewer_username") {
		t.Errorf("the negated branch was walked:\n%s", joined)
	}
}

// A body the method is handed is read from the parameter's type, as a
// body built inside it is read from its declaration.
func TestADeclaredTypeIsFoundInTheSignature(t *testing.T) {
	src := `package p
func (c *Client) Create(in IssueCreate, n int) { var local Other; _ = local }`
	f, err := parser.ParseFile(token.NewFileSet(), "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	fn := f.Decls[0].(*ast.FuncDecl)
	for name, want := range map[string]string{"in": "IssueCreate", "local": "Other"} {
		if got, ok := declaredType(fn, name).(*ast.Ident); !ok || got.Name != want {
			t.Errorf("%s: got %v, want %s", name, declaredType(fn, name), want)
		}
	}
	if declaredType(fn, "missing") != nil {
		t.Error("an undeclared name was given a type")
	}
}
