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

// The gate reads each mutate call's root field, and refuses one it cannot
// read.
func TestGraphQLOperationsReadsMutateCalls(t *testing.T) {
	dir := t.TempDir()
	src := `package gapi

type Client struct{}
func (c *Client) mutate(_ any, name, field, what, query string, _ map[string]any, _ any) error { return nil }

func (c *Client) Rereview() error { return c.mutate(nil, "rerequest_review", "mergeRequestReviewerRereview", "", "", nil, nil) }
func (c *Client) Other() error { return c.mutate(nil, "other", "issueSetSomething", "", "", nil, nil) }
func (c *Client) Computed(f string) error { return c.mutate(nil, "x", "prefix"+f, "", "", nil, nil) }
`
	if err := os.WriteFile(filepath.Join(dir, "graphql.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	ops, problems, err := graphQLOperations(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"mergeRequestReviewerRereview", "issueSetSomething"}; !slices.Equal(ops, want) {
		t.Errorf("operations = %v, want %v", ops, want)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "root field is not a string literal") {
		t.Errorf("problems = %v, want one about the computed root field", problems)
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
