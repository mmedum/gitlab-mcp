package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The gate reads each mutate call's root field and the query it sends,
// and refuses what it cannot read, what disagrees, a call from a
// function whose doc comment does not mention REST, and a client that
// sends too few.
func TestGraphQLOperationsReadsMutateCalls(t *testing.T) {
	read := func(t *testing.T, body string, floor int) ([]string, []string) {
		t.Helper()
		dir := t.TempDir()
		src := "package gapi\n\ntype Call struct{ Method, Path string; Root int; Body any }\ntype Client struct{}\n" +
			"func (c *Client) Do(_ any, _ Call, _ any) error { return nil }\n" +
			"func (c *Client) mutate(_ any, name, field, what, query string, _ map[string]any, _ any) error {\n" +
			"\treturn c.Do(nil, Call{Method: \"POST\", Path: \"api/graphql\"}, nil)\n}\n" +
			"const rereview = `mutation($a: ID!) {\n  mergeRequestReviewerRereview(input: {a: $a}) { errors }\n}`\n" +
			"// Rereview asks for a new review. The REST API has no route for it.\n" +
			"func (c *Client) Rereview() error { return c.mutate(nil, \"x\", \"mergeRequestReviewerRereview\", \"\", rereview, nil, nil) }\n" +
			body
		if err := os.WriteFile(filepath.Join(dir, "graphql.go"), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		ops, problems, err := graphQLOperations(dir, floor)
		if err != nil {
			t.Fatal(err)
		}
		return ops, problems
	}
	ops, problems := read(t, "", 1)
	if !slices.Equal(ops, []string{"mergeRequestReviewerRereview"}) || len(problems) != 0 {
		t.Errorf("a documented, well-formed mutate call: operations %v, problems %v", ops, problems)
	}
	// Each row adds a function X; doc is the comment the gate requires.
	const doc = "// X does what REST has no route for.\n"
	for name, c := range map[string]struct{ body, want string }{
		"a computed root field": {doc + `func (c *Client) X(f string) error { return c.mutate(nil, "x", "a"+f, "", rereview, nil, nil) }`,
			"root field is not a string literal"},
		"a root field the query does not select": {doc + `func (c *Client) X() error { return c.mutate(nil, "x", "issueSetSomething", "", rereview, nil, nil) }`,
			"names issueSetSomething, and its query does not select it first"},
		"a query that is not a constant": {doc + `func (c *Client) X(q string) error { return c.mutate(nil, "x", "mergeRequestReviewerRereview", "", q, nil, nil) }`,
			"query is not a string constant"},
		"a Call to the endpoint outside mutate": {`func (c *Client) X() error { return c.Do(nil, Call{Method: "POST", Path: "api/graphql"}, nil) }`,
			"a Call to api/graphql in Client.X"},
		"a GraphQL call with no doc comment": {`func (c *Client) X() error { return c.mutate(nil, "x", "mergeRequestReviewerRereview", "", rereview, nil, nil) }`,
			"Client.X sends a GraphQL operation, and has no doc comment that mentions REST"},
		"a doc comment that does not mention REST": {"// X asks for a new review; the rest is up to GitLab.\n" +
			`func (c *Client) X() error { return c.mutate(nil, "x", "mergeRequestReviewerRereview", "", rereview, nil, nil) }`,
			"Client.X sends a GraphQL operation, and has no doc comment that mentions REST"},
		"a GraphQL call outside a function": {`var X = func(c *Client) error { return c.mutate(nil, "x", "mergeRequestReviewerRereview", "", rereview, nil, nil) }`,
			"a package-level declaration sends a GraphQL operation, and has no doc comment that mentions REST"},
	} {
		if _, problems := read(t, c.body, 1); len(problems) != 1 || !strings.Contains(problems[0], c.want) {
			t.Errorf("%s: problems = %v, want one containing %q", name, problems, c.want)
		}
	}
	if _, problems := read(t, "", 2); len(problems) != 1 || !strings.Contains(problems[0], "read 1 GraphQL operations in ") ||
		!strings.HasSuffix(problems[0], ", want at least 2: the reader is not seeing its input") {
		t.Errorf("one operation against a floor of 2: problems = %v, want the floor's", problems)
	}
}
