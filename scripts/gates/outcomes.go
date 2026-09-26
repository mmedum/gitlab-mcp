package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/mmedum/gitlab-mcp/scripts/internal/gatekit"
)

// The outcomes gate holds the rule that a result states what happened,
// and only what the response showed happened (§4.11, §5a "outcomes").
// Two halves, both read from the syntax tree:
//
//  1. Every service function that reaches a write of the client — a
//     Call whose Method is not GET or HEAD — returns a model type with an
//     Outcome field, and every literal of that type it builds sets
//     Outcome. A write result without one leaves the caller to infer
//     from silence whether anything changed.
//  2. A branch that tests a boolean field of the caller's request and
//     then puts a sentence in front of the caller — appended to Notes, or
//     set as a Note — must consult GitLab first, end in a refusal, or
//     mark its result a dry run. Otherwise the argument is being treated
//     as its own evidence: "the file is locked" written because the
//     caller asked for a lock, not because GitLab said so.
//
// Phase 0 has no writes, so the first half runs on a floor of zero and
// proves itself on fixtures; the second runs over the reads.

const (
	serviceDir = "internal/service"
	modelDir   = "internal/model"
)

// outcomeExemptions are branches of the second kind that are right
// anyway, keyed "file:Field" with the reason. A key that no longer
// matches a branch fails, and so does one matching two.
var outcomeExemptions = map[string]string{}

type outcomesFloors struct{ files, boolFields, writes int }

// realOutcomesFloors: the service's files, the boolean request fields it
// branches on, and the writes. Phase 2 raises writes with the first one.
var realOutcomesFloors = outcomesFloors{files: 7, boolFields: 12, writes: 0}

func outcomes(out io.Writer, _ []string) error {
	service, err := parsePackageDir(serviceDir)
	if err != nil {
		return err
	}
	model, err := parsePackageDir(modelDir)
	if err != nil {
		return err
	}
	client, err := parsePackageDir(clientDir)
	if err != nil {
		return err
	}
	calls, callProblems, err := clientCalls(clientDir)
	if err != nil {
		return err
	}
	report, problems := checkOutcomes(service, model, client, calls, outcomeExemptions, realOutcomesFloors)
	problems = append(problems, callProblems...)
	if err := gatekit.Problems(out, "the outcomes", problems); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, report)
	return nil
}

func checkOutcomes(service, model, client parsedPackage, calls []clientCall, exempt map[string]string, fl outcomesFloors) (string, []string) {
	var problems []string
	if len(service.files) < fl.files {
		problems = append(problems, fmt.Sprintf("read %d files of %s and the floor is %d", len(service.files), serviceDir, fl.files))
	}
	writers, more := writesStateOutcomes(service, model, calls)
	problems = append(problems, more...)
	if writers < fl.writes {
		problems = append(problems, fmt.Sprintf("found %d service functions that write and the floor is %d", writers, fl.writes))
	}
	fields, more := noOutcomeFromRequest(service, client, exempt)
	problems = append(problems, more...)
	if fields < fl.boolFields {
		problems = append(problems, fmt.Sprintf("found %d boolean request fields in %s and the floor is %d; the gate is not reading the requests",
			fields, serviceDir, fl.boolFields))
	}
	slices.Sort(problems)
	return fmt.Sprintf("outcomes ok: %d service files, %d writing functions each stating an outcome, %d boolean request fields, %d branch(es) exempt",
		len(service.files), writers, fields, len(exempt)), problems
}

// writesStateOutcomes is half 1. It returns how many service functions
// write.
func writesStateOutcomes(service, model parsedPackage, calls []clientCall) (int, []string) {
	var problems []string
	writeMethods := map[string]bool{}
	for _, c := range calls {
		if c.Method != "GET" && c.Method != "HEAD" {
			writeMethods[strings.TrimPrefix(c.Func, "Client.")] = true
		}
	}
	modelStructs := wireStructs(model)
	writers := 0
	for i, f := range service.files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !callsAny(fn.Body, writeMethods) {
				continue
			}
			writers++
			problems = append(problems, writeResult(fmt.Sprintf("%s:%s", service.paths[i], fn.Name.Name), fn, modelStructs, service.fset)...)
		}
	}
	return writers, problems
}

// writeResult holds one writing function: it returns a model type with
// an Outcome, and sets it in every literal it returns with a nil error.
func writeResult(where string, fn *ast.FuncDecl, modelStructs map[string]*ast.StructType, fset *token.FileSet) []string {
	result := firstResultModel(fn)
	st := modelStructs[result]
	switch {
	case result == "":
		return []string{fmt.Sprintf("%s writes to GitLab and returns no model type; a write's result states its outcome", where)}
	case st == nil:
		return []string{fmt.Sprintf("%s writes to GitLab and returns model.%s, which is not a struct of %s", where, result, modelDir)}
	case !hasField(st, "Outcome"):
		return []string{fmt.Sprintf("%s writes to GitLab and model.%s has no Outcome field", where, result)}
	}
	var problems []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		// A refusal's zero result is discarded by every caller; it is
		// the error that speaks.
		if ret, ok := n.(*ast.ReturnStmt); ok && len(ret.Results) > 1 {
			if id, ok := ret.Results[len(ret.Results)-1].(*ast.Ident); !ok || id.Name != "nil" {
				return false
			}
		}
		cl, ok := n.(*ast.CompositeLit)
		if ok && isModelType(cl.Type, result) && !setsKey(cl, "Outcome") {
			problems = append(problems, fmt.Sprintf("%s: a model.%s is returned without its Outcome (%s)", where, result, fset.Position(cl.Pos())))
		}
		return true
	})
	return problems
}

// noOutcomeFromRequest is half 2. It returns how many boolean request
// fields it watched.
func noOutcomeFromRequest(service, client parsedPackage, exempt map[string]string) (int, []string) {
	var problems []string
	requests := requestTypes(service)
	fields := boolFields(requests, service, client)
	used := map[string]int{}
	for i, f := range service.files {
		path := strings.ReplaceAll(service.paths[i], `\`, "/")
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			params := requestParams(fn, requests)
			if len(params) == 0 {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				is, ok := n.(*ast.IfStmt)
				if !ok {
					return true
				}
				field := testedField(is.Cond, fields, params)
				if field == "" || consults(is.Body) || endsInRefusal(is.Body) || marksDryRun(is.Body) {
					return true
				}
				quote, pos := proseIn(is.Body)
				if quote == "" {
					return true
				}
				if key := path + ":" + field; exempt[key] != "" {
					used[key]++
					return true
				}
				problems = append(problems, fmt.Sprintf("%s: this branch tests the request's %s and then tells the caller %q without asking GitLab; "+
					"read it back, word it as what was asked, or exempt it with the reason", service.fset.Position(pos), field, quote))
				return true
			})
		}
	}
	for _, k := range slices.Sorted(maps.Keys(exempt)) {
		switch {
		case used[k] == 0:
			problems = append(problems, fmt.Sprintf("outcomeExemptions[%q] excuses no branch any more; drop it", k))
		case used[k] > 1:
			problems = append(problems, fmt.Sprintf("outcomeExemptions[%q] excuses %d branches; one argument cannot cover two", k, used[k]))
		case len(strings.TrimSpace(exempt[k])) < minReasonLen:
			problems = append(problems, fmt.Sprintf("outcomeExemptions[%q] has no real reason", k))
		}
	}
	return len(fields), problems
}

// callsAny reports whether a body calls a method of one of the names.
func callsAny(body *ast.BlockStmt, names map[string]bool) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if ce, ok := n.(*ast.CallExpr); ok {
			if sel, ok := ce.Fun.(*ast.SelectorExpr); ok && names[sel.Sel.Name] {
				found = true
			}
		}
		return !found
	})
	return found
}

// firstResultModel names the model type a function returns first.
func firstResultModel(fn *ast.FuncDecl) string {
	if fn.Type.Results == nil || len(fn.Type.Results.List) == 0 {
		return ""
	}
	t := fn.Type.Results.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	if sel, ok := t.(*ast.SelectorExpr); ok {
		if x, ok := sel.X.(*ast.Ident); ok && x.Name == "model" {
			return sel.Sel.Name
		}
	}
	return ""
}

func isModelType(e ast.Expr, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == "model" && sel.Sel.Name == name
}

func hasField(st *ast.StructType, name string) bool {
	for _, f := range st.Fields.List {
		for _, n := range f.Names {
			if n.Name == name {
				return true
			}
		}
	}
	return false
}

func setsKey(cl *ast.CompositeLit, key string) bool {
	for _, el := range cl.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			if id, ok := kv.Key.(*ast.Ident); ok && id.Name == key {
				return true
			}
		}
	}
	return false
}

// requestTypes are the types the caller's request arrives in: every
// struct-typed parameter of an exported method on *Service, named as it
// is spelled there ("ItemSearch", "gapi.TreeQuery").
func requestTypes(service parsedPackage) map[string]bool {
	out := map[string]bool{}
	for _, f := range service.files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !fn.Name.IsExported() || funcName(fn) != "Service."+fn.Name.Name {
				continue
			}
			for _, p := range fn.Type.Params.List {
				name := strings.TrimPrefix(exprString(p.Type), "*")
				if name != "" && name != "context.Context" && name != "string" && name != "int" && name != "int64" && name != "bool" {
					out[name] = true
				}
			}
		}
	}
	return out
}

// boolFields is every bool field of the request types, found in the
// service package or, for a gapi.X, in the client's.
func boolFields(requests map[string]bool, service, client parsedPackage) map[string]bool {
	out := map[string]bool{}
	local, remote := wireStructs(service), wireStructs(client)
	for name := range requests {
		st := local[name]
		if after, ok := strings.CutPrefix(name, "gapi."); ok {
			st = remote[after]
		}
		if st == nil {
			continue
		}
		for _, f := range st.Fields.List {
			if id, ok := f.Type.(*ast.Ident); ok && id.Name == "bool" {
				for _, n := range f.Names {
					out[n.Name] = true
				}
			}
		}
	}
	return out
}

// requestParams names a function's parameters of a request type.
func requestParams(fn *ast.FuncDecl, requests map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, p := range fn.Type.Params.List {
		if requests[strings.TrimPrefix(exprString(p.Type), "*")] {
			for _, n := range p.Names {
				out[n.Name] = true
			}
		}
	}
	return out
}

// testedField names the request field a condition tests, if any.
func testedField(cond ast.Expr, fields, params map[string]bool) string {
	name := ""
	ast.Inspect(cond, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || name != "" {
			return name == ""
		}
		if id, ok := sel.X.(*ast.Ident); ok && params[id.Name] && fields[sel.Sel.Name] {
			name = sel.Sel.Name
		}
		return name == ""
	})
	return name
}

// consults reports whether a branch calls anything that can bring back
// an answer: a method on the client (c.X after c, err := s.api()), or on
// the service itself, whose helpers read.
func consults(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		ce, ok := n.(*ast.CallExpr)
		if !ok {
			return !found
		}
		if sel, ok := ce.Fun.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && (id.Name == "c" || id.Name == "s" || id.Name == "client" || id.Name == "api") {
				found = true
			}
		}
		return !found
	})
	return found
}

// endsInRefusal reports a branch whose last statement returns an error:
// a refusal says what this server declined, which is true whatever
// GitLab would have answered.
func endsInRefusal(body *ast.BlockStmt) bool {
	if len(body.List) == 0 {
		return false
	}
	ret, ok := body.List[len(body.List)-1].(*ast.ReturnStmt)
	if !ok || len(ret.Results) == 0 {
		return false
	}
	last := ret.Results[len(ret.Results)-1]
	if id, ok := last.(*ast.Ident); ok {
		return id.Name != "nil"
	}
	return true
}

// marksDryRun reports a branch that sets DryRun: true on its result,
// which is what makes "would" honest.
func marksDryRun(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if kv, ok := n.(*ast.KeyValueExpr); ok {
			if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "DryRun" {
				if v, ok := kv.Value.(*ast.Ident); ok && v.Name == "true" {
					found = true
				}
			}
		}
		return !found
	})
	return found
}

// proseIn finds a sentence a branch puts in front of the caller: a
// literal appended to Notes, or assigned to a Note or Notes field.
func proseIn(body *ast.BlockStmt) (string, token.Pos) {
	quote, pos := "", token.NoPos
	ast.Inspect(body, func(n ast.Node) bool {
		if quote != "" {
			return false
		}
		switch v := n.(type) {
		case *ast.CallExpr:
			if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "append" && len(v.Args) > 1 && isNoteTarget(v.Args[0]) {
				for _, a := range v.Args[1:] {
					if s, p := firstLiteral(a); s != "" {
						quote, pos = s, p
					}
				}
			}
		case *ast.AssignStmt:
			for i, lhs := range v.Lhs {
				if i < len(v.Rhs) && isNoteTarget(lhs) {
					if s, p := firstLiteral(v.Rhs[i]); s != "" {
						quote, pos = s, p
					}
				}
			}
		case *ast.KeyValueExpr:
			if k, ok := v.Key.(*ast.Ident); ok && (k.Name == "Note" || k.Name == "Notes") {
				if s, p := firstLiteral(v.Value); s != "" {
					quote, pos = s, p
				}
			}
		}
		return quote == ""
	})
	return quote, pos
}

func isNoteTarget(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name == "note" || v.Name == "notes"
	case *ast.SelectorExpr:
		return v.Sel.Name == "Note" || v.Sel.Name == "Notes"
	}
	return false
}

func firstLiteral(e ast.Expr) (string, token.Pos) {
	s, pos := "", token.NoPos
	ast.Inspect(e, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && s == "" {
			if v, err := strconv.Unquote(lit.Value); err == nil && strings.TrimSpace(v) != "" {
				s, pos = v, lit.Pos()
			}
		}
		return s == ""
	})
	if len(s) > 60 {
		s = s[:60] + "…"
	}
	return s, pos
}
