package main

import (
	"fmt"
	"go/ast"
	"go/token"
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

// rootField is the first field an operation's text selects: the
// operation it sends.
var rootField = regexp.MustCompile(`^\s*(?:mutation|query)\b[^{]*\{\s*([A-Za-z]+)`)

// graphQLOperations reads the root field every mutate call in dir sends:
// its third argument, a string literal that must name the first field of
// the query it sends, a string constant. A Call to the GraphQL endpoint
// anywhere but mutate is a problem, since nothing here would read it.
func graphQLOperations(dir string) ([]string, []string, error) {
	pkg, err := parsePackageDir(dir)
	if err != nil {
		return nil, nil, err
	}
	consts := stringConsts(pkg)
	var ops, problems []string
	for _, f := range pkg.files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || funcName(fn) == "Client.mutate" {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || !isCallType(lit.Type) {
					return true
				}
				if p, ok := keyValue(lit, "Path"); ok {
					if path, _ := stringLit(p); path == "api/graphql" {
						problems = append(problems, pkg.fset.Position(lit.Pos()).String()+": a Call to api/graphql in "+
							funcName(fn)+"; send GraphQL through mutate, which the gate reads")
					}
				}
				return true
			})
		}
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
			query, ok := "", len(ce.Args) > 4
			if ok {
				query, ok = constString(ce.Args[4], consts)
			}
			switch m := rootField.FindStringSubmatch(query); {
			case !ok:
				problems = append(problems, pos+": a mutate call whose query is not a string constant; the gate reads it")
			case m == nil || m[1] != field:
				problems = append(problems, fmt.Sprintf("%s: a mutate call names %s, and its query does not select it first", pos, field))
			}
			return true
		})
	}
	return ops, problems, nil
}

// stringConsts are a package's string constants by name.
func stringConsts(pkg parsedPackage) map[string]string {
	out := map[string]string{}
	for _, f := range pkg.files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if i < len(vs.Values) {
						if v, ok := stringLit(vs.Values[i]); ok {
							out[name.Name] = v
						}
					}
				}
			}
		}
	}
	return out
}

// constString is e's value when it is a string literal or names a
// string constant.
func constString(e ast.Expr, consts map[string]string) (string, bool) {
	if v, ok := stringLit(e); ok {
		return v, true
	}
	if id, ok := e.(*ast.Ident); ok {
		v, ok := consts[id.Name]
		return v, ok
	}
	return "", false
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
