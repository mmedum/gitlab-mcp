package main

import (
	"fmt"
	"go/ast"
	"regexp"
	"slices"
	"strings"
)

// The GraphQL operations the client sends, held against the table in
// docs/architecture.md §8a both ways (§4.10). GitLab commits no GraphQL
// schema, so the snapshot cannot cover them, and the table is the record
// of why REST cannot do each one.

// graphQLHeader is §8a's GraphQL table header.
const graphQLHeader = "| Operation | Tool | Why not REST |"

// graphQLFloor is how many operations the client is known to send: a
// gate that reads fewer is not reading mutate calls.
const graphQLFloor = 1

// operationCell is a table row's first cell: the operation as a code
// span, then its kind.
var operationCell = regexp.MustCompile("^`([A-Za-z]+)` \\((mutation|query)\\)$")

// graphQLOperations reads the root field every mutate call in dir sends:
// its third argument, which must be a string literal.
func graphQLOperations(dir string) ([]string, []string, error) {
	pkg, err := parsePackageDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var ops, problems []string
	for _, f := range pkg.files {
		ast.Inspect(f, func(n ast.Node) bool {
			ce, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := ce.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "mutate" {
				return true
			}
			pos := pkg.fset.Position(ce.Pos()).String()
			if len(ce.Args) < 3 {
				problems = append(problems, pos+": a mutate call with no root field")
				return true
			}
			field, ok := stringLit(ce.Args[2])
			if !ok {
				problems = append(problems, pos+": a mutate call whose root field is not a string literal; the gate reads it "+
					"to hold the operation against §8a")
				return true
			}
			ops = append(ops, field)
			return true
		})
	}
	return ops, problems, nil
}

// listedGraphQL reads the operations §8a's GraphQL table lists.
func listedGraphQL(doc string) ([]string, []string) {
	lines := strings.Split(doc, "\n")
	i := slices.Index(lines, graphQLHeader)
	if i < 0 {
		return nil, []string{"§8a has no GraphQL table; its header is " + graphQLHeader}
	}
	var listed, problems []string
	for _, line := range lines[min(i+2, len(lines)):] {
		if !strings.HasPrefix(line, "|") {
			break
		}
		cell := strings.TrimSpace(strings.Split(line, "|")[1])
		m := operationCell.FindStringSubmatch(cell)
		if m == nil {
			problems = append(problems, fmt.Sprintf("§8a's GraphQL table has the row %q, which does not start with an "+
				"operation as `name` (mutation) or `name` (query)", cell))
			continue
		}
		listed = append(listed, m[1])
	}
	return listed, problems
}

// checkGraphQL holds the operations sent against those listed.
func checkGraphQL(sent, listed []string, floor int) []string {
	var problems []string
	if len(sent) < floor {
		problems = append(problems, fmt.Sprintf("read %d GraphQL operations from the client and the floor is %d; the gate "+
			"is not reading mutate calls", len(sent), floor))
	}
	for _, op := range sent {
		if !slices.Contains(listed, op) {
			problems = append(problems, op+" is sent by the client, and §8a's GraphQL table does not list it with why REST "+
				"cannot do it")
		}
	}
	for _, op := range listed {
		if !slices.Contains(sent, op) {
			problems = append(problems, "§8a lists the GraphQL operation "+op+", which the client never sends; drop the row")
		}
	}
	return problems
}
