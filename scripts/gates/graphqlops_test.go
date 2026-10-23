package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const graphQLDoc = "### 8a\n\n" + graphQLHeader + "\n|---|---|---|\n" +
	"| `mergeRequestReviewerRereview` (mutation) | `rerequest_review` | no REST route |\n\nAfter.\n"

// The gate reads each mutate call's root field and the query it sends,
// and refuses what it cannot read or what disagrees.
func TestGraphQLOperationsReadsMutateCalls(t *testing.T) {
	read := func(t *testing.T, body string) ([]string, []string) {
		t.Helper()
		dir := t.TempDir()
		src := "package gapi\n\ntype Call struct{ Method, Path string; Root int; Body any }\ntype Client struct{}\n" +
			"func (c *Client) Do(_ any, _ Call, _ any) error { return nil }\n" +
			"func (c *Client) mutate(_ any, name, field, what, query string, _ map[string]any, _ any) error {\n" +
			"\treturn c.Do(nil, Call{Method: \"POST\", Path: \"api/graphql\"}, nil)\n}\n" +
			"const rereview = `mutation($a: ID!) {\n  mergeRequestReviewerRereview(input: {a: $a}) { errors }\n}`\n" + body
		if err := os.WriteFile(filepath.Join(dir, "graphql.go"), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		ops, problems, err := graphQLOperations(dir)
		if err != nil {
			t.Fatal(err)
		}
		return ops, problems
	}
	ops, problems := read(t, `func (c *Client) Rereview() error { return c.mutate(nil, "x", "mergeRequestReviewerRereview", "", rereview, nil, nil) }`)
	if !slices.Equal(ops, []string{"mergeRequestReviewerRereview"}) || len(problems) != 0 {
		t.Errorf("a well-formed mutate call: operations %v, problems %v", ops, problems)
	}
	for name, c := range map[string]struct{ body, want string }{
		"a computed root field": {`func (c *Client) X(f string) error { return c.mutate(nil, "x", "a"+f, "", rereview, nil, nil) }`,
			"root field is not a string literal"},
		"a root field the query does not select": {`func (c *Client) X() error { return c.mutate(nil, "x", "issueSetSomething", "", rereview, nil, nil) }`,
			"names issueSetSomething, and its query does not select it first"},
		"a query that is not a constant": {`func (c *Client) X(q string) error { return c.mutate(nil, "x", "mergeRequestReviewerRereview", "", q, nil, nil) }`,
			"query is not a string constant"},
		"a Call to the endpoint outside mutate": {`func (c *Client) X() error { return c.Do(nil, Call{Method: "POST", Path: "api/graphql"}, nil) }`,
			"a Call to api/graphql in Client.X"},
	} {
		if _, problems := read(t, c.body); len(problems) != 1 || !strings.Contains(problems[0], c.want) {
			t.Errorf("%s: problems = %v, want one containing %q", name, problems, c.want)
		}
	}
}

func TestListedGraphQLReadsTheTable(t *testing.T) {
	listed, problems := listedGraphQL(graphQLDoc)
	if !slices.Equal(listed, []string{"mergeRequestReviewerRereview"}) || len(problems) != 0 {
		t.Errorf("listed = %v, problems = %v", listed, problems)
	}
	for name, c := range map[string]struct{ doc, want string }{
		"no table":             {"### 8a\n\nNo table.\n", "§8a has no GraphQL table"},
		"a row without a kind": {strings.Replace(graphQLDoc, " (mutation)", "", 1), "does not start with an operation"},
	} {
		if _, problems := listedGraphQL(c.doc); len(problems) != 1 || !strings.Contains(problems[0], c.want) {
			t.Errorf("%s: problems = %v, want one containing %q", name, problems, c.want)
		}
	}
}

func TestCheckGraphQLHoldsBothWays(t *testing.T) {
	op := []string{"mergeRequestReviewerRereview"}
	if problems := checkGraphQL(op, op, 1); len(problems) != 0 {
		t.Errorf("a sent operation the table lists: %v", problems)
	}
	for name, c := range map[string]struct {
		sent, listed []string
		floor        int
		want         string
	}{
		"sent, not listed": {append(slices.Clone(op), "issueSetSomething"), op, 1, "issueSetSomething is sent by the client, and §8a"},
		"listed, not sent": {op, append(slices.Clone(op), "issueSetSomething"), 1, "§8a lists the GraphQL operation issueSetSomething"},
		"below the floor":  {nil, nil, 1, "read 0 GraphQL operations from the client and the floor is 1"},
	} {
		if problems := checkGraphQL(c.sent, c.listed, c.floor); len(problems) != 1 || !strings.Contains(problems[0], c.want) {
			t.Errorf("%s: problems = %v, want one containing %q", name, problems, c.want)
		}
	}
}
