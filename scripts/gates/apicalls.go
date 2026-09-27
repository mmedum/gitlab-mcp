package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The client's requests, read from internal/gapi's syntax tree. Every
// request is a Call composite literal whose Method and Path are string
// literals ("GET", "projects/{}/issues/{}"); internal/gapi/call.go says
// so where Call is declared. api-coverage binds each to an operation of
// the snapshot, api-fields reads what it sends and decodes, and outcomes
// tells a write from a read by its Method.

const clientDir = "internal/gapi"

// clientCall is one Call literal.
type clientCall struct {
	Method string
	Path   string // the template, "projects/{}/issues/{}"
	// Web is a call under the instance's web root rather than /api/v4,
	// which the REST OpenAPI file does not describe.
	Web bool
	// ReadOnly is the literal's reason a non-GET changes nothing, "" when
	// it gives none.
	ReadOnly string
	// Func is the function the literal sits in: "Client.GetIssue" for a
	// method on *Client, else the function's own name.
	Func string
	Pos  string // file:line, for a message a person can open

	// Query and Body are the literal's fields, nil when absent. Out is
	// the argument the response is decoded into: the last argument of
	// the c.Do or c.list call the literal is passed to.
	Query, Body, Out ast.Expr
	fn               *ast.FuncDecl
	file             *ast.File
}

// op is the call as the verdict file spells an operation's verb, and
// its path in the normalized form paths are compared in.
func (c clientCall) op() string { return c.Method + " " + c.Path }

// parsedPackage is one directory's non-test Go files, parsed once.
type parsedPackage struct {
	fset  *token.FileSet
	files []*ast.File
	paths []string
}

// parsePackageDir parses a directory's non-test Go files the way the
// compiler would see them. It does not descend.
func parsePackageDir(dir string) (parsedPackage, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return parsedPackage{}, fmt.Errorf("read %s: %w", dir, err)
	}
	pkg := parsedPackage{fset: token.NewFileSet()}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		f, err := parser.ParseFile(pkg.fset, path, nil, parser.ParseComments)
		if err != nil {
			return parsedPackage{}, fmt.Errorf("parse %s: %w", path, err)
		}
		pkg.files = append(pkg.files, f)
		pkg.paths = append(pkg.paths, path)
	}
	return pkg, nil
}

// clientCalls reads every Call literal in dir. A literal whose Method or
// Path is not a string literal is a problem: the gate cannot bind what it
// cannot read, and computing either is what call.go forbids.
func clientCalls(dir string) ([]clientCall, []string, error) {
	pkg, err := parsePackageDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var calls []clientCall
	var problems []string
	for _, f := range pkg.files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			name := funcName(fn)
			// Each literal handed to a call as an argument gets that
			// call's last argument as its destination.
			outs := map[*ast.CompositeLit]ast.Expr{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				ce, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				for i, a := range ce.Args {
					if lit, ok := a.(*ast.CompositeLit); ok && isCallType(lit.Type) && i < len(ce.Args)-1 {
						outs[lit] = ce.Args[len(ce.Args)-1]
					}
				}
				return true
			})
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || !isCallType(lit.Type) {
					return true
				}
				c := clientCall{Func: name, Pos: pkg.fset.Position(lit.Pos()).String(), Out: outs[lit], fn: fn, file: f}
				if methodOK, pathOK := c.readFields(lit); !methodOK || !pathOK {
					problems = append(problems, fmt.Sprintf("%s: a Call in %s whose Method or Path is not a string literal; "+
						"the gates bind a request to an operation by reading both", c.Pos, name))
					return true
				}
				calls = append(calls, c)
				return true
			})
		}
	}
	return calls, problems, nil
}

func isCallType(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "Call"
}

// funcName is "Client.X" for a method on *Client or Client, else the
// name the function is declared with.
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	t := fn.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	return v, err == nil
}

// ------------------------------------------------------------ paths

var pathParam = regexp.MustCompile(`\{[^}]*\}`)

// normalizedPaths is the forms a published path takes on the wire,
// without the leading slash and with every parameter as {}. GitLab
// publishes a few routes with an optional "-/" segment, "(-/)search",
// and both spellings reach the same operation.
func normalizedPaths(published string) []string {
	p := strings.TrimPrefix(pathParam.ReplaceAllString(published, "{}"), "/")
	if !strings.Contains(p, "(-/)") {
		return []string{p}
	}
	return []string{strings.ReplaceAll(p, "(-/)", "-/"), strings.ReplaceAll(p, "(-/)", "")}
}

// operationIndex finds an operation by a client call's verb and path.
type operationIndex map[string]*apiOperation

func indexOperations(snap apiSnapshot) operationIndex {
	idx := operationIndex{}
	for i := range snap.Operations {
		op := &snap.Operations[i]
		for _, p := range normalizedPaths(op.Path) {
			idx[op.Verb+" "+p] = op
		}
	}
	return idx
}

func (idx operationIndex) find(c clientCall) *apiOperation {
	return idx[c.Method+" "+strings.TrimPrefix(c.Path, "/")]
}

// sortedCalls orders calls by position so a report reads top to bottom.
func sortedCalls(calls []clientCall) []clientCall {
	out := slices.Clone(calls)
	slices.SortFunc(out, func(a, b clientCall) int { return strings.Compare(a.Pos, b.Pos) })
	return out
}

// parseOne parses one file.
func parseOne(fset *token.FileSet, path string) (*ast.File, error) {
	return parser.ParseFile(fset, path, nil, 0)
}

// readFields reads a Call literal's fields into c, and reports whether
// Method and Path were string literals.
func (c *clientCall) readFields(lit *ast.CompositeLit) (methodOK, pathOK bool) {
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, _ := kv.Key.(*ast.Ident)
		if key == nil {
			continue
		}
		switch key.Name {
		case "Method":
			c.Method, methodOK = stringLit(kv.Value)
		case "Path":
			c.Path, pathOK = stringLit(kv.Value)
		case "Root":
			if id, ok := kv.Value.(*ast.Ident); ok && id.Name == "RootWeb" {
				c.Web = true
			}
		case "Query":
			c.Query = kv.Value
		case "Body":
			c.Body = kv.Value
		case "ReadOnly":
			c.ReadOnly, _ = stringLit(kv.Value)
		}
	}
	return methodOK, pathOK
}
