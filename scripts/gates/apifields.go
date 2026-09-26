package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/mmedum/gitlab-mcp/scripts/internal/gatekit"
	"github.com/mmedum/gitlab-mcp/scripts/internal/tsv"
)

// The API-fields gate is api-coverage one level down (§8b): for every
// operation the client calls, every query parameter and body field it
// sends exists on that operation, and every response field a wire struct
// decodes exists in the operation's schema, with a Go kind that can hold
// the declared type. What does not is excused in testdata/api-fields.tsv,
// verified live or reasoned, and a row that excuses nothing fails.
//
// Both halves are derived: what is sent is read from the client's
// syntax tree — the keys its query builders set and the struct a body
// is marshaled from — and what is decoded from the variable each call
// decodes into, followed through internal/gitlab's structs. A field is
// added to a wire type, and the gate asks about it on that commit.
//
// And one rule with no exception: no wire struct declares a field whose
// name ends in _token. A project read once carried a runner registration
// token, and a field that is never decoded cannot leak (§8b).

const (
	fieldsFile = "testdata/api-fields.tsv"
	wireDir    = "internal/gitlab"
)

// fieldsFloors are the least the gate must have compared.
type fieldsFloors struct{ ops, sent, decoded, rows int }

var realFieldsFloors = fieldsFloors{ops: 40, sent: 180, decoded: 900, rows: 100}

var fieldVerdicts = []string{"verified-live", "reasoned"}

// fieldRow excuses one field of one operation in one direction.
type fieldRow struct {
	op, dir, field, verdict, reason string
	line                            int
}

func (r fieldRow) key() string { return r.op + " " + r.dir + " " + r.field }

func apiFields(out io.Writer, _ []string) error {
	snap, err := readSnapshot(snapshotFile)
	if err != nil {
		return err
	}
	rows, problems := readFieldRows(fieldsFile)
	calls, callProblems, err := clientCalls(clientDir)
	if err != nil {
		return err
	}
	problems = append(problems, callProblems...)
	client, err := parsePackageDir(clientDir)
	if err != nil {
		return err
	}
	wire, err := parsePackageDir(wireDir)
	if err != nil {
		return err
	}
	report, more := checkFields(snap, rows, calls, client, wire, realFieldsFloors)
	problems = append(problems, more...)
	if err := gatekit.Problems(out, "the API fields", problems); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, report)
	return nil
}

func readFieldRows(path string) ([]fieldRow, []string) {
	raw, problems := tsv.Read(path, 3)
	var rows []fieldRow
	for _, r := range raw {
		where := fmt.Sprintf("%s:%d", path, r.Line)
		parts := strings.Fields(r.Fields[0])
		if len(parts) != 4 || !slices.Contains([]string{"query", "body", "response"}, parts[2]) {
			problems = append(problems, fmt.Sprintf("%s: %q is not \"VERB /path query|body|response field\"", where, r.Fields[0]))
			continue
		}
		row := fieldRow{op: parts[0] + " " + parts[1], dir: parts[2], field: parts[3],
			verdict: r.Fields[1], reason: strings.TrimSpace(r.Fields[2]), line: r.Line}
		if !slices.Contains(fieldVerdicts, row.verdict) {
			problems = append(problems, fmt.Sprintf("%s: verdict %q; want verified-live or reasoned", where, row.verdict))
			continue
		}
		if len(row.reason) < minReasonLen {
			problems = append(problems, fmt.Sprintf("%s: the reason %q is not a sentence", where, row.reason))
			continue
		}
		rows = append(rows, row)
	}
	return rows, problems
}

// checkFields is the rule over parsed inputs, so fixtures can drive it.
func checkFields(snap apiSnapshot, rows []fieldRow, calls []clientCall, client, wire parsedPackage, fl fieldsFloors) (string, []string) {
	fc := &fieldChecker{client: client, structs: wireStructs(wire), rows: map[string]fieldRow{}, used: map[string]bool{}}
	fc.problems = append(fc.problems, tokenFields(fc.structs)...)
	for _, r := range rows {
		fc.rows[r.key()] = r
	}
	idx := indexOperations(snap)
	opsCompared := map[string]bool{}
	for _, c := range sortedCalls(calls) {
		if c.Web {
			continue
		}
		op := idx.find(c)
		if op == nil {
			continue // api-coverage says so
		}
		opsCompared[op.key()] = true
		fc.query(c, *op)
		fc.body(c, *op)
		fc.response(c, *op)
	}
	problems := fc.problems
	for _, r := range rows {
		if !fc.used[r.key()] {
			problems = append(problems, fmt.Sprintf("%s:%d: %s excuses nothing: the field is published with a kind that holds it, "+
				"or the client no longer sends or decodes it; drop the row", fieldsFile, r.line, r.key()))
		}
	}
	if len(opsCompared) < fl.ops {
		problems = append(problems, fmt.Sprintf("compared %d operations and the floor is %d", len(opsCompared), fl.ops))
	}
	if fc.sent < fl.sent {
		problems = append(problems, fmt.Sprintf("compared %d sent parameters and fields and the floor is %d", fc.sent, fl.sent))
	}
	if fc.decoded < fl.decoded {
		problems = append(problems, fmt.Sprintf("compared %d decoded fields and the floor is %d", fc.decoded, fl.decoded))
	}
	if len(rows) < fl.rows {
		problems = append(problems, fmt.Sprintf("%s has %d rows and the floor is %d; 526 operations publish no response schema, "+
			"so a short list is a list that stopped being read", fieldsFile, len(rows), fl.rows))
	}
	slices.Sort(problems)
	problems = slices.Compact(problems)
	return fmt.Sprintf("api fields ok: %d operations, %d sent and %d decoded fields compared with %s; %d excused in %s",
		len(opsCompared), fc.sent, fc.decoded, snap.Tag, len(rows), fieldsFile), problems
}

// fieldChecker carries one run of the rule.
type fieldChecker struct {
	client        parsedPackage
	structs       map[string]*ast.StructType
	rows          map[string]fieldRow
	used          map[string]bool
	problems      []string
	sent, decoded int
}

// excuse finds the row for one field, or records the problem.
func (fc *fieldChecker) excuse(op, dir, field, what string) {
	k := op + " " + dir + " " + field
	if _, ok := fc.rows[k]; ok {
		fc.used[k] = true
		return
	}
	fc.problems = append(fc.problems, fmt.Sprintf("%s: %s; add the field to the wire type or the request, or a row in %s saying why",
		k, what, fieldsFile))
}

// query holds every parameter the call sends to the operation's.
func (fc *fieldChecker) query(c clientCall, op apiOperation) {
	keys, unread := queryKeys(c, fc.client)
	for _, u := range unread {
		fc.problems = append(fc.problems, fmt.Sprintf("%s: %s builds its query in a way this gate cannot read (%s); set keys with "+
			"a string literal", c.Pos, c.Func, u))
	}
	for _, k := range keys {
		fc.sent++
		t, ok := op.Params["query"][k]
		switch {
		case !ok:
			fc.excuse(op.key(), "query", k, "sent and not published")
		case t != "" && !strings.Contains(t, "string") && !strings.Contains(t, "integer") &&
			!strings.Contains(t, "boolean") && !strings.HasPrefix(t, "array"):
			fc.excuse(op.key(), "query", k, "sent as text and published as "+t)
		}
	}
}

// body holds every field of the body the call marshals.
func (fc *fieldChecker) body(c clientCall, op apiOperation) {
	if c.Body == nil {
		return
	}
	fields, err := exprFields(c.Body, c, fc.client, fc.structs)
	if err != "" {
		fc.problems = append(fc.problems, fmt.Sprintf("%s: %s sends a body this gate cannot read: %s", c.Pos, c.Func, err))
	}
	for _, f := range slices.Sorted(maps.Keys(fields)) {
		fc.sent++
		t, ok := op.Request[f]
		switch {
		case !ok:
			fc.excuse(op.key(), "body", f, "sent and not published")
		case !kindHolds(fields[f], t):
			fc.excuse(op.key(), "body", f, fmt.Sprintf("sent as %s and published as %s", fields[f], t))
		}
	}
}

// response holds every field the call decodes to a 2xx schema.
func (fc *fieldChecker) response(c clientCall, op apiOperation) {
	if c.Out == nil || rawBody(c) {
		return
	}
	fields, err := exprFields(c.Out, c, fc.client, fc.structs)
	if err != "" {
		fc.problems = append(fc.problems, fmt.Sprintf("%s: %s decodes into something this gate cannot read: %s", c.Pos, c.Func, err))
	}
	published := successFields(op)
	for _, f := range slices.Sorted(maps.Keys(fields)) {
		fc.decoded++
		t, ok := published[f]
		switch {
		case !ok:
			// One question per subtree: a field inside an object that
			// is itself unpublished, or published with nothing inside
			// it, is asked about once, at that object.
			if at, opaque := unpublishedRoot(f, published); at != f {
				if opaque {
					fc.excuse(op.key(), "response", at, "published as an object with no fields, and the wire type decodes fields inside it")
				}
				continue
			}
			fc.excuse(op.key(), "response", f, "decoded and not published")
		case !kindHolds(fields[f], t):
			fc.excuse(op.key(), "response", f, fmt.Sprintf("decoded as %s and published as %s", fields[f], t))
		}
	}
}

// unpublishedRoot names where an unpublished field's question belongs:
// its outermost unpublished ancestor, or its nearest published ancestor
// when that one publishes no fields inside it (opaque). A field with
// neither is its own root.
func unpublishedRoot(f string, published map[string]string) (string, bool) {
	parts := strings.Split(f, ".")
	for i := 1; i < len(parts); i++ {
		prefix := strings.Join(parts[:i], ".")
		if _, ok := published[prefix]; !ok {
			return prefix, false
		}
	}
	for i := len(parts) - 1; i >= 1; i-- {
		prefix := strings.Join(parts[:i], ".")
		inside := false
		for k := range published {
			if strings.HasPrefix(k, prefix+".") {
				inside = true
				break
			}
		}
		if !inside {
			return prefix, true
		}
	}
	return f, false
}

// successFields is every field a 2xx answer publishes.
func successFields(op apiOperation) map[string]string {
	out := map[string]string{}
	for status, fields := range op.Responses {
		if strings.HasPrefix(status, "2") {
			maps.Copy(out, fields)
		}
	}
	return out
}

// ------------------------------------------------------------ kinds

// goKind names a Go type the way the snapshot names a JSON type.
func goKind(e ast.Expr, structs map[string]*ast.StructType) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return goKind(t.X, structs)
	case *ast.ArrayType:
		return "array<" + goKind(t.Elt, structs) + ">"
	case *ast.MapType:
		return "object"
	case *ast.SelectorExpr:
		if x, ok := t.X.(*ast.Ident); ok {
			switch x.Name + "." + t.Sel.Name {
			case "time.Time":
				return "string"
			case "json.RawMessage":
				return ""
			}
			if _, ok := structs[t.Sel.Name]; ok {
				return "object"
			}
		}
		return ""
	case *ast.Ident:
		switch t.Name {
		case "string":
			return "string"
		case "bool":
			return "boolean"
		case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64":
			return "integer"
		case "float32", "float64":
			return "number"
		case "any":
			return ""
		}
		if _, ok := structs[t.Name]; ok {
			return "object"
		}
	case *ast.InterfaceType:
		return ""
	}
	return ""
}

// kindHolds reports whether a Go kind can hold a published type.
// Unknown on either side holds.
func kindHolds(goK, published string) bool {
	if goK == "" || published == "" {
		return true
	}
	for alt := range strings.SplitSeq(published, "|") {
		if kindHoldsOne(goK, alt) {
			return true
		}
	}
	return false
}

func kindHoldsOne(goK, pub string) bool {
	if strings.HasPrefix(goK, "array<") || strings.HasPrefix(pub, "array<") {
		if !strings.HasPrefix(goK, "array<") || !strings.HasPrefix(pub, "array<") {
			return false
		}
		return kindHolds(strings.TrimSuffix(strings.TrimPrefix(goK, "array<"), ">"),
			strings.TrimSuffix(strings.TrimPrefix(pub, "array<"), ">"))
	}
	switch goK {
	case "number":
		return pub == "number" || pub == "integer"
	default:
		return goK == pub
	}
}

// ------------------------------------------------------------ wire structs

func wireStructs(pkg parsedPackage) map[string]*ast.StructType {
	out := map[string]*ast.StructType{}
	for _, f := range pkg.files {
		ast.Inspect(f, func(n ast.Node) bool {
			if ts, ok := n.(*ast.TypeSpec); ok {
				if st, ok := ts.Type.(*ast.StructType); ok {
					out[ts.Name.Name] = st
				}
			}
			return true
		})
	}
	return out
}

// tokenFields refuses any wire field named like a token.
func tokenFields(structs map[string]*ast.StructType) []string {
	var problems []string
	for _, name := range slices.Sorted(maps.Keys(structs)) {
		for _, f := range structs[name].Fields.List {
			if tag := jsonTag(f); strings.HasSuffix(tag, "_token") || tag == "token" {
				problems = append(problems, fmt.Sprintf("%s.%s decodes %q: no wire type may carry a field named like a token (§8b)",
					name, fieldName(f), tag))
			}
		}
	}
	return problems
}

func fieldName(f *ast.Field) string {
	if len(f.Names) > 0 {
		return f.Names[0].Name
	}
	return "(embedded)"
}

// jsonTag is the field's wire name, "" for one the wire never sees.
func jsonTag(f *ast.Field) string {
	if f.Tag == nil {
		return ""
	}
	raw := strings.Trim(f.Tag.Value, "`")
	_, rest, ok := strings.Cut(raw, `json:"`)
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, `"`)
	name, _, _ = strings.Cut(name, ",")
	if name == "-" {
		return ""
	}
	return name
}

// flattenStruct writes a struct's wire fields as dotted names with their
// kinds, following nested structs of the wire package. Slices are
// transparent, as they are in the snapshot.
func flattenStruct(st *ast.StructType, prefix string, structs map[string]*ast.StructType, depth int, out map[string]string) {
	if depth >= fieldDepth {
		return
	}
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			// Embedded: promoted fields are on the wire as if declared.
			if nested := structOf(f.Type, structs); nested != nil {
				flattenStruct(nested, prefix, structs, depth, out)
			}
			continue
		}
		tag := jsonTag(f)
		if tag == "" {
			continue
		}
		out[prefix+tag] = goKind(f.Type, structs)
		if nested := structOf(f.Type, structs); nested != nil {
			flattenStruct(nested, prefix+tag+".", structs, depth+1, out)
		}
	}
}

// structOf finds the wire struct a field's type names, through pointers
// and slices, or nil.
func structOf(e ast.Expr, structs map[string]*ast.StructType) *ast.StructType {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.ArrayType:
			e = t.Elt
		case *ast.Ident:
			return structs[t.Name]
		case *ast.SelectorExpr:
			if x, ok := t.X.(*ast.Ident); ok && x.Name == "gitlab" {
				return structs[t.Sel.Name]
			}
			return nil
		default:
			return nil
		}
	}
}

// exprFields is the wire fields of what an expression marshals or
// decodes: &out, out, &T{...}, T{...}. The type is taken from the
// variable's declaration in the call's function.
func exprFields(e ast.Expr, c clientCall, client parsedPackage, structs map[string]*ast.StructType) (map[string]string, string) {
	if u, ok := e.(*ast.UnaryExpr); ok {
		e = u.X
	}
	var typ ast.Expr
	switch v := e.(type) {
	case *ast.Ident:
		if v.Name == "nil" {
			return nil, ""
		}
		typ = declaredType(c.fn, v.Name)
	case *ast.CompositeLit:
		typ = v.Type
	}
	if typ == nil {
		return nil, fmt.Sprintf("the type of %s is not declared in %s", exprString(e), c.Func)
	}
	st := structOf(typ, structs)
	if st == nil {
		if gapiStruct := localStruct(client, typ); gapiStruct != nil {
			st = gapiStruct
		}
	}
	if st == nil {
		return nil, fmt.Sprintf("%s is not a struct of %s or %s", exprString(typ), wireDir, clientDir)
	}
	out := map[string]string{}
	flattenStruct(st, "", structs, 0, out)
	return out, ""
}

// rawBody reports a call that takes its answer as bytes, as a job log is
// taken: text, with no fields to hold against the snapshot.
func rawBody(c clientCall) bool {
	e := c.Out
	if u, ok := e.(*ast.UnaryExpr); ok {
		e = u.X
	}
	id, ok := e.(*ast.Ident)
	if !ok {
		return false
	}
	arr, ok := declaredType(c.fn, id.Name).(*ast.ArrayType)
	if !ok || arr.Len != nil {
		return false
	}
	elt, ok := arr.Elt.(*ast.Ident)
	return ok && elt.Name == "byte"
}

// localStruct finds a struct declared in the client package itself, for
// a request body built there.
func localStruct(pkg parsedPackage, typ ast.Expr) *ast.StructType {
	id, ok := typ.(*ast.Ident)
	if !ok {
		return nil
	}
	return wireStructs(pkg)[id.Name]
}

// declaredType finds `var name T` or `name := T{}` in a function.
func declaredType(fn *ast.FuncDecl, name string) ast.Expr {
	var found ast.Expr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		switch d := n.(type) {
		case *ast.ValueSpec:
			for _, id := range d.Names {
				if id.Name == name && d.Type != nil {
					found = d.Type
				}
			}
		case *ast.AssignStmt:
			for i, lhs := range d.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && id.Name == name && i < len(d.Rhs) {
					rhs := d.Rhs[i]
					if u, ok := rhs.(*ast.UnaryExpr); ok {
						rhs = u.X
					}
					if cl, ok := rhs.(*ast.CompositeLit); ok {
						found = cl.Type
					}
				}
			}
		}
		return true
	})
	return found
}

func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	case *ast.StarExpr:
		return "*" + exprString(v.X)
	case *ast.ArrayType:
		return "[]" + exprString(v.Elt)
	}
	return fmt.Sprintf("%T", e)
}

// ------------------------------------------------------------ query keys

// queryKeys is every query parameter a call can send: the keys its Query
// expression sets, and per_page for a call made through list, which sets
// it on every page. The parameters a next page changes are GitLab's own
// and arrive in a Link header, so they are not the client's to send.
func queryKeys(c clientCall, pkg parsedPackage) ([]string, []string) {
	keys := map[string]bool{}
	var unread []string
	if listed(c) {
		keys["per_page"] = true
	}
	switch q := c.Query.(type) {
	case nil:
	case *ast.CompositeLit:
		literalKeys(q, keys)
	case *ast.Ident:
		keysSetOn(c.fn.Body, q.Name, nil, keys)
		ast.Inspect(c.fn.Body, func(n ast.Node) bool {
			if as, ok := n.(*ast.AssignStmt); ok {
				for i, lhs := range as.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && id.Name == q.Name && i < len(as.Rhs) {
						if cl, ok := as.Rhs[i].(*ast.CompositeLit); ok {
							literalKeys(cl, keys)
						}
					}
				}
			}
			return true
		})
	case *ast.CallExpr:
		sel, ok := q.Fun.(*ast.SelectorExpr)
		if !ok {
			unread = append(unread, exprString(q.Fun))
			break
		}
		recvType := paramType(c.fn, sel.X)
		m := methodDecl(pkg, recvType, sel.Sel.Name)
		if m == nil {
			unread = append(unread, "the method "+sel.Sel.Name+" on "+recvType)
			break
		}
		// A bool argument that is a literal decides the branches that
		// test its parameter: values(false) does not send what
		// `if mergeRequests { … }` sets.
		fixed := map[string]bool{}
		var params []string
		for _, p := range m.Type.Params.List {
			for _, n := range p.Names {
				params = append(params, n.Name)
			}
		}
		for i, a := range q.Args {
			if id, ok := a.(*ast.Ident); ok && (id.Name == "true" || id.Name == "false") && i < len(params) {
				fixed[params[i]] = id.Name == "true"
			}
		}
		keysSetOn(m.Body, "", fixed, keys)
		ast.Inspect(m.Body, func(n ast.Node) bool {
			if cl, ok := n.(*ast.CompositeLit); ok {
				literalKeys(cl, keys)
			}
			return true
		})
	default:
		unread = append(unread, exprString(q))
	}
	return slices.Sorted(maps.Keys(keys)), unread
}

// listed reports whether the call's literal is handed to the list helper.
func listed(c clientCall) bool {
	found := false
	ast.Inspect(c.fn.Body, func(n ast.Node) bool {
		ce, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := ce.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "list" {
			for _, a := range ce.Args {
				if a == c.Out || (c.Out != nil && a.Pos() == c.Out.Pos()) {
					found = true
				}
			}
		}
		return !found
	})
	return found
}

// literalKeys reads url.Values{"k": …}.
func literalKeys(cl *ast.CompositeLit, keys map[string]bool) {
	sel, ok := cl.Type.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Values" {
		return
	}
	for _, el := range cl.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			if k, ok := stringLit(kv.Key); ok {
				keys[k] = true
			}
		}
	}
}

// keysSetOn reads setX(v, "k", …) and v.Set("k", …) in a body. With a
// variable name, only calls on that variable count. fixed names the bool
// parameters whose branches are decided.
func keysSetOn(body *ast.BlockStmt, variable string, fixed map[string]bool, keys map[string]bool) {
	var walk func(n ast.Node) bool
	walk = func(n ast.Node) bool {
		if is, ok := n.(*ast.IfStmt); ok {
			cond, negated := is.Cond, false
			if u, ok := cond.(*ast.UnaryExpr); ok && u.Op == token.NOT {
				cond, negated = u.X, true
			}
			if id, ok := cond.(*ast.Ident); ok {
				if on, decided := fixed[id.Name]; decided {
					on = on != negated
					if on {
						ast.Inspect(is.Body, walk)
					} else if is.Else != nil {
						ast.Inspect(is.Else, walk)
					}
					return false
				}
			}
			return true
		}
		ce, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := ce.Fun.(type) {
		case *ast.Ident:
			if strings.HasPrefix(fn.Name, "set") && len(ce.Args) >= 2 && onVariable(ce.Args[0], variable) {
				if k, ok := stringLit(ce.Args[1]); ok {
					keys[k] = true
				}
			}
		case *ast.SelectorExpr:
			if (fn.Sel.Name == "Set" || fn.Sel.Name == "Add") && len(ce.Args) >= 1 && onVariable(fn.X, variable) {
				if k, ok := stringLit(ce.Args[0]); ok {
					keys[k] = true
				}
			}
		}
		return true
	}
	ast.Inspect(body, walk)
}

func onVariable(e ast.Expr, variable string) bool {
	if variable == "" {
		return true
	}
	id, ok := e.(*ast.Ident)
	return ok && id.Name == variable
}

// paramType names the type of a function parameter or receiver used as
// a method's receiver, "ItemQuery" for q in `q ItemQuery`.
func paramType(fn *ast.FuncDecl, e ast.Expr) string {
	id, ok := e.(*ast.Ident)
	if !ok {
		return ""
	}
	var lists []*ast.Field
	if fn.Recv != nil {
		lists = append(lists, fn.Recv.List...)
	}
	lists = append(lists, fn.Type.Params.List...)
	for _, f := range lists {
		for _, n := range f.Names {
			if n.Name == id.Name {
				return strings.TrimPrefix(exprString(f.Type), "*")
			}
		}
	}
	return ""
}

func methodDecl(pkg parsedPackage, recvType, name string) *ast.FuncDecl {
	for _, f := range pkg.files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Name.Name != name || fn.Body == nil {
				continue
			}
			if strings.TrimPrefix(exprString(fn.Recv.List[0].Type), "*") == recvType {
				return fn
			}
		}
	}
	return nil
}
