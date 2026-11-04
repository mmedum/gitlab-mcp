package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"regexp"

	"github.com/mmedum/gitlab-mcp/v4/scripts/internal/gatekit"
)

// The GraphQL operations the client sends (D-09). GitLab commits no
// GraphQL schema, so the snapshot cannot cover them. The function that
// sends each one says in its doc comment why REST cannot do it, and the
// gate holds that comment to mentioning REST.

// graphQLFloor is how many operations the client is known to send: a
// gate that reads fewer is not reading mutate calls.
const graphQLFloor = 1

// rootField is the first field an operation's text selects: the
// operation it sends.
var rootField = regexp.MustCompile(`^\s*(?:mutation|query)\b[^{]*\{\s*([A-Za-z]+)`)

// mentionsREST matches REST as a word, so "the rest" and "RESTful" do
// not stand in for the reason.
var mentionsREST = regexp.MustCompile(`\bREST\b`)

// graphQLOperations reads the root field every mutate call in dir sends:
// its third argument, a string literal that must name the first field of
// the query it sends, a string constant. The function making the call
// must have a doc comment that mentions REST, where it says why REST
// cannot do it; the gate does not judge the reason. A Call to the
// GraphQL endpoint anywhere but mutate is a problem, since nothing here
// would read it, and so is reading fewer operations than floor.
func graphQLOperations(dir string, floor int) ([]string, []string, error) {
	pkg, err := parsePackageDir(dir)
	if err != nil {
		return nil, nil, err
	}
	consts := stringConsts(pkg)
	var ops, problems []string
	for _, f := range pkg.files {
		for _, decl := range f.Decls {
			fn, _ := decl.(*ast.FuncDecl)
			where := "a package-level declaration"
			if fn != nil {
				where = funcName(fn)
			}
			sends := false
			ast.Inspect(decl, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CompositeLit:
					if where != "Client.mutate" && isGraphQLCall(n) {
						problems = append(problems, pkg.fset.Position(n.Pos()).String()+": a Call to api/graphql in "+
							where+"; send GraphQL through mutate, which the gate reads")
					}
				case *ast.CallExpr:
					if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "mutate" {
						sends = true
						field, problem := mutateOperation(n, consts)
						if field != "" {
							ops = append(ops, field)
						}
						if problem != "" {
							problems = append(problems, pkg.fset.Position(n.Pos()).String()+": "+problem)
						}
					}
				}
				return true
			})
			if sends && (fn == nil || !mentionsREST.MatchString(fn.Doc.Text())) {
				problems = append(problems, fmt.Sprintf("%s: %s sends a GraphQL operation, and has no doc comment "+
					"that mentions REST; say there why REST cannot do it", pkg.fset.Position(decl.Pos()), where))
			}
		}
	}
	return ops, append(problems, gatekit.Floor("GraphQL operations in "+dir, len(ops), floor)...), nil
}

// isGraphQLCall reports whether lit is a Call to the GraphQL endpoint.
func isGraphQLCall(lit *ast.CompositeLit) bool {
	if !isCallType(lit.Type) {
		return false
	}
	p, ok := keyValue(lit, "Path")
	if !ok {
		return false
	}
	path, _ := stringLit(p)
	return path == "api/graphql"
}

// mutateOperation reads the root field one mutate call sends, "" when it
// cannot, and what is wrong with the call, "" when nothing is.
func mutateOperation(ce *ast.CallExpr, consts map[string]string) (field, problem string) {
	if len(ce.Args) < 3 {
		return "", "a mutate call with no root field"
	}
	field, ok := stringLit(ce.Args[2])
	if !ok {
		return "", "a mutate call whose root field is not a string literal; the gate reads it"
	}
	query, ok := "", len(ce.Args) > 4
	if ok {
		query, ok = constString(ce.Args[4], consts)
	}
	switch m := rootField.FindStringSubmatch(query); {
	case !ok:
		return field, "a mutate call whose query is not a string constant; the gate reads it"
	case m == nil || m[1] != field:
		return field, fmt.Sprintf("a mutate call names %s, and its query does not select it first", field)
	}
	return field, ""
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
