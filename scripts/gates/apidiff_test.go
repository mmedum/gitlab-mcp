package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// closedPort is an address nothing listens on: a listener is opened to
// learn a free port and closed before anything connects.
func closedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return "http://" + addr
}

// The standard's own test: point everything at a closed port, and the
// refetch fails loudly while the committed file does not move.
func TestAPIDiffLeavesTheSnapshotOnANetworkFailure(t *testing.T) {
	base := closedPort(t)
	t.Setenv("HTTPS_PROXY", base)
	t.Setenv("HTTP_PROXY", base)
	path := filepath.Join(t.TempDir(), "snapshot.json")
	before := []byte(`{"tag":"v1.0.0-ee","operations":[]}` + "\n")
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"", "v19.4.1-ee"} {
		var out bytes.Buffer
		err := runAPIDiff(context.Background(), &out, &http.Client{Timeout: 5 * time.Second}, base, path, tag, 1, time.Now())
		if err == nil {
			t.Fatalf("tag %q: a refetch against a closed port succeeded", tag)
		}
		after, rerr := os.ReadFile(path)
		if rerr != nil || !bytes.Equal(before, after) {
			t.Fatalf("tag %q: the snapshot moved on a failed fetch: %q", tag, after)
		}
		if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
			t.Errorf("tag %q: a failed fetch left %d files behind", tag, len(entries))
		}
	}
}

// syntheticOpenAPI is a document with n operations, shaped like GitLab's.
func syntheticOpenAPI(n int) string {
	var b strings.Builder
	b.WriteString("openapi: 3.0.0\ninfo:\n  version: '1.0'\npaths:\n")
	for i := range n {
		fmt.Fprintf(&b, "  /api/v4/things%d/{id}:\n    get:\n      tags: [Things]\n", i)
		b.WriteString("      parameters:\n      - name: id\n        in: path\n        schema:\n          oneOf: [{type: string}, {type: integer}]\n")
		b.WriteString("      - name: with_stats\n        in: query\n        schema: {type: boolean}\n")
		b.WriteString("      responses:\n        '200':\n          content:\n            application/json:\n              schema:\n                $ref: '#/components/schemas/Thing'\n")
		b.WriteString("        '404':\n          description: Not Found\n")
		fmt.Fprintf(&b, "  /api/v4/things%d:\n    post:\n      x-gitlab-lifecycle: experiment\n", i)
		b.WriteString("      requestBody:\n        content:\n          application/json:\n            schema:\n              properties:\n                title: {type: string}\n                labels: {type: array, items: {type: string}}\n")
		b.WriteString("      responses:\n        '201':\n          description: Created\n")
	}
	b.WriteString(`components:
  schemas:
    Thing:
      type: object
      properties:
        id: {type: integer}
        author: {$ref: '#/components/schemas/User'}
        assignees: {type: array, items: {$ref: '#/components/schemas/User'}}
    User:
      type: object
      properties:
        username: {type: string}
        friend: {$ref: '#/components/schemas/User'}
`)
	return b.String()
}

func fakeGitLab(t *testing.T, doc string, tags string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/repository/tags"):
			_, _ = w.Write([]byte(tags))
		case strings.HasSuffix(r.URL.Path, "/"+openAPIPath):
			if !strings.Contains(r.URL.Path, "/v2.1.0-ee/") {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(doc))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Release candidates sort among the stable tags in GitLab's version
// order; the newest stable one is picked by number.
const fakeTags = `[{"name":"v2.1.0-rc42-ee"},{"name":"v2.0.9-ee"},{"name":"v2.1.0-ee"},{"name":"v10.0.0-rc1-ee"},{"name":"v2.1.0"}]`

func TestAPIDiffRefusesADocumentBelowTheFloor(t *testing.T) {
	srv := fakeGitLab(t, syntheticOpenAPI(3), fakeTags)
	path := filepath.Join(t.TempDir(), "snapshot.json")
	before := []byte("{}\n")
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runAPIDiff(context.Background(), &out, srv.Client(), srv.URL, path, "", 7, time.Now())
	if err == nil || !strings.Contains(err.Error(), "has 6 operations and a real one has at least 7") {
		t.Fatalf("err = %v", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Errorf("the snapshot moved: %q", after)
	}
}

func TestAPIDiffWritesTheSnapshot(t *testing.T) {
	srv := fakeGitLab(t, syntheticOpenAPI(3), fakeTags)
	path := filepath.Join(t.TempDir(), "testdata", "snapshot.json")
	var out bytes.Buffer
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	if err := runAPIDiff(context.Background(), &out, srv.Client(), srv.URL, path, "", 6, now); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snap apiSnapshot
	if err := jsonUnmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Tag != "v2.1.0-ee" || snap.Fetched != "2026-09-26" || len(snap.SHA256) != 64 || snap.Version != "1.0" {
		t.Errorf("header = %+v", snap)
	}
	if !strings.HasSuffix(snap.Source, "/gitlab-org/gitlab/-/raw/v2.1.0-ee/doc/api/openapi/openapi_v3.yaml") {
		t.Errorf("source = %q", snap.Source)
	}
	if len(snap.Operations) != 6 {
		t.Fatalf("%d operations, want 6", len(snap.Operations))
	}
	var get, post *apiOperation
	for i := range snap.Operations {
		op := &snap.Operations[i]
		if op.Path == "/things0/{id}" {
			get = op
		}
		if op.Path == "/things0" {
			post = op
		}
	}
	if get == nil || post == nil {
		t.Fatalf("operations = %+v", snap.Operations)
	}
	if get.Verb != "GET" || get.Params["path"]["id"] != "integer|string" || get.Params["query"]["with_stats"] != "boolean" {
		t.Errorf("GET params = %+v", get.Params)
	}
	wantFields := map[string]string{
		"id": "integer", "author": "object", "author.username": "string", "author.friend": "object",
		"assignees": "array<object>", "assignees.username": "string",
	}
	for k, v := range wantFields {
		if got := get.Responses["200"][k]; got != v {
			t.Errorf("200 field %s = %q, want %q", k, got, v)
		}
	}
	if _, ok := get.Responses["404"]; !ok || get.Responses["404"] != nil {
		t.Errorf("a status with no schema must be recorded as null: %+v", get.Responses)
	}
	if post.Lifecycle != "experiment" || post.Request["labels"] != "array<string>" || post.Request["title"] != "string" {
		t.Errorf("POST = %+v", post)
	}
	// A recursive schema is cut where it repeats rather than run on.
	if _, ok := get.Responses["200"]["author.friend.username"]; ok {
		t.Error("a schema that refers to itself was expanded a second time")
	}
	if !strings.Contains(out.String(), "6 new, 0 gone, 0 changed") {
		t.Errorf("report = %s", out.String())
	}

	// A second run over the same document reports nothing moved.
	out.Reset()
	if err := runAPIDiff(context.Background(), &out, srv.Client(), srv.URL, path, "v2.1.0-ee", 6, now); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "0 new, 0 gone, 0 changed") {
		t.Errorf("second report = %s", out.String())
	}
}

func TestAPIDiffRefusesATagThatIsNotARelease(t *testing.T) {
	for _, tag := range []string{"master", "v19.4.1", "v19.4.1-rc42-ee", "v19.4-ee"} {
		err := runAPIDiff(context.Background(), &bytes.Buffer{}, http.DefaultClient, closedPort(t),
			filepath.Join(t.TempDir(), "s.json"), tag, 1, time.Now())
		if err == nil || !strings.Contains(err.Error(), "is not a GitLab release tag") {
			t.Errorf("tag %q: err = %v", tag, err)
		}
	}
}

// The committed snapshot is the machine's: the fields a person would
// otherwise type are present on every operation.
func TestTheCommittedSnapshotIsComplete(t *testing.T) {
	t.Chdir("../..")
	snap, err := readSnapshot(snapshotFile)
	if err != nil {
		t.Fatal(err)
	}
	if !stableTag.MatchString(snap.Tag) || len(snap.SHA256) != 64 || snap.Fetched == "" || snap.Source == "" {
		t.Errorf("header = %q %q %q %q", snap.Tag, snap.SHA256, snap.Fetched, snap.Source)
	}
	seen := map[string]bool{}
	for _, op := range snap.Operations {
		if op.Verb == "" || !strings.HasPrefix(op.Path, "/") || op.Responses == nil {
			t.Errorf("incomplete operation %+v", op)
		}
		if seen[op.key()] {
			t.Errorf("%s twice", op.key())
		}
		seen[op.key()] = true
	}
	if raw, _ := os.ReadFile(snapshotFile); !bytes.HasSuffix(raw, []byte("]}\n")) {
		t.Error("the snapshot is not in the shape encodeSnapshot writes; it was edited by hand")
	}
}

func jsonUnmarshal(raw []byte, v any) error { return json.Unmarshal(raw, v) }

// A snapshot below the floor is refused on read, so a truncated file
// cannot pass api-coverage as a complete one.
func TestReadSnapshotRefusesATruncatedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	data, err := encodeSnapshot(apiSnapshot{Tag: "v1.0.0-ee", Operations: []apiOperation{{Verb: "GET", Path: "/metadata"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSnapshot(path); err == nil || !strings.Contains(err.Error(), "lists 1 operations") {
		t.Errorf("err = %v", err)
	}
}
