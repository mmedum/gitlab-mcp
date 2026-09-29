package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/mmedum/gitlab-mcp/v2/scripts/internal/gatekit"
)

// The classes gate holds the closed error vocabulary (docs/architecture.md
// §6.5, CLAUDE.md rule 16) from both sides: the Class constants in
// internal/gapi against the table in §6.5, the Classes list against the
// constants, and the constants against what the code emits.
//
// A model branches on the class — [ambiguous] means pick one,
// [ambiguous_outcome] means go and look — so a class invented at a call
// site, or documented and never produced, is a lie told to the caller.

const (
	classesSource = "internal/gapi/errors.go"
	classesDoc    = "docs/architecture.md"
)

// plannedClasses are declared and documented but not yet emitted, each
// with the reason. A planned class the code has started emitting fails
// until it leaves this list, so the list cannot outlive its reasons.
var plannedClasses = map[string]string{}

// classesFloors are the least the gate must have read.
type classesFloors struct{ files, classes int }

var realClassesFloors = classesFloors{files: 40, classes: 13}

func classes(out io.Writer, _ []string) error {
	doc, err := os.ReadFile(classesDoc)
	if err != nil {
		return err
	}
	report, problems, err := checkClasses(classesSource, []string{"internal", "cmd"}, string(doc), plannedClasses, realClassesFloors)
	if err != nil {
		return err
	}
	if err := gatekit.Problems(out, "the error classes", problems); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, report)
	return nil
}

func checkClasses(source string, roots []string, doc string, planned map[string]string, fl classesFloors) (string, []string, error) {
	declared, listed, err := declaredClasses(source)
	if err != nil {
		return "", nil, err
	}
	documented, err := documentedClasses(doc)
	if err != nil {
		return "", nil, err
	}
	values := map[string]bool{}
	for _, v := range declared {
		values[v] = true
	}
	emitted, bracketed, files, err := emittedClasses(roots, declared, source)
	if err != nil {
		return "", nil, err
	}
	var problems []string
	if files < fl.files {
		problems = append(problems, fmt.Sprintf("read %d Go files under %s and the floor is %d; this is not the code", files,
			strings.Join(roots, ", "), fl.files))
	}
	if len(values) < fl.classes {
		problems = append(problems, fmt.Sprintf("%s declares %d classes and the floor is %d", source, len(values), fl.classes))
	}
	problems = append(problems, againstDocument(source, values, documented)...)
	problems = append(problems, againstList(declared, listed, documented, len(problems) == 0)...)
	problems = append(problems, againstCode(values, emitted, bracketed, planned)...)
	slices.Sort(problems)
	return fmt.Sprintf("classes ok: %d classes, declared, listed, documented and emitted (%d planned), over %d files",
		len(values), len(planned), files), problems, nil
}

// againstDocument holds the constants and §6.5's table both ways, and
// finds a class the table lists twice by count, which a sorted compare
// cannot see anywhere but beside itself.
func againstDocument(source string, values map[string]bool, documented []string) []string {
	var problems []string
	for _, v := range slices.Sorted(maps.Keys(values)) {
		if !slices.Contains(documented, v) {
			problems = append(problems, fmt.Sprintf("%s declares %q and §6.5 of %s does not document it", source, v, classesDoc))
		}
	}
	counts := map[string]int{}
	for _, v := range documented {
		counts[v]++
		if !values[v] && counts[v] == 1 {
			problems = append(problems, fmt.Sprintf("§6.5 documents %q and %s declares no such class", v, source))
		}
	}
	for _, v := range slices.Sorted(maps.Keys(counts)) {
		if counts[v] > 1 {
			problems = append(problems, fmt.Sprintf("§6.5 lists %q %d times", v, counts[v]))
		}
	}
	return problems
}

// againstList holds the Classes list to the constants: each once, and,
// when everything else agrees, in the table's order, which the comment
// on Classes promises.
func againstList(declared map[string]string, listed, documented []string, compareOrder bool) []string {
	var problems []string
	counts := map[string]int{}
	var listValues []string
	for _, name := range listed {
		counts[name]++
		v, ok := declared[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("Classes lists %s, which is not a declared class", name))
			continue
		}
		listValues = append(listValues, v)
	}
	for _, name := range slices.Sorted(maps.Keys(declared)) {
		switch counts[name] {
		case 0:
			problems = append(problems, fmt.Sprintf("%s is declared and Classes does not list it, so nothing tells a caller it exists", name))
		case 1:
		default:
			problems = append(problems, fmt.Sprintf("Classes lists %s %d times", name, counts[name]))
		}
	}
	if compareOrder && len(problems) == 0 && !slices.Equal(listValues, documented) {
		problems = append(problems, fmt.Sprintf("Classes is in the order %v and §6.5 tabulates %v", listValues, documented))
	}
	return problems
}

// againstCode holds the constants to what the code emits, the planned
// list to its reasons, and a class spelled into a string to the
// vocabulary.
func againstCode(values map[string]bool, emitted, bracketed, planned map[string]string) []string {
	var problems []string
	for _, v := range slices.Sorted(maps.Keys(values)) {
		_, isEmitted := emitted[v]
		reason, isPlanned := planned[v]
		switch {
		case isEmitted && isPlanned:
			problems = append(problems, fmt.Sprintf("%q is emitted (%s) and still listed as planned; take it off plannedClasses", v, emitted[v]))
		case !isEmitted && !isPlanned:
			problems = append(problems, fmt.Sprintf("%q is declared and nothing emits it; emit it, drop it, or list it as planned with the reason", v))
		case isPlanned && len(strings.TrimSpace(reason)) < minReasonLen:
			problems = append(problems, fmt.Sprintf("%q is planned with the reason %q, which is not a sentence", v, reason))
		}
	}
	for _, v := range slices.Sorted(maps.Keys(planned)) {
		if !values[v] {
			problems = append(problems, fmt.Sprintf("plannedClasses lists %q, which is not a declared class", v))
		}
	}
	for _, v := range slices.Sorted(maps.Keys(bracketed)) {
		if !values[v] {
			problems = append(problems, fmt.Sprintf("%s: the text %q starts with [%s], which is not a class", bracketed[v], "["+v+"] …", v))
		}
	}
	return problems
}

// declaredClasses reads the Class constants, name to value, and the
// names the Classes list holds in order.
func declaredClasses(path string) (map[string]string, []string, error) {
	pkg, err := parsePackageDir(filepath.Dir(path))
	if err != nil {
		return nil, nil, err
	}
	declared := map[string]string{}
	var listed []string
	for i, f := range pkg.files {
		if filepath.Clean(pkg.paths[i]) != filepath.Clean(path) {
			continue
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for j, name := range vs.Names {
					if gd.Tok == token.CONST && strings.HasPrefix(name.Name, "Class") && j < len(vs.Values) {
						if v, ok := stringLit(vs.Values[j]); ok {
							declared[name.Name] = v
						}
					}
					if gd.Tok == token.VAR && name.Name == "Classes" && j < len(vs.Values) {
						ast.Inspect(vs.Values[j], func(n ast.Node) bool {
							if id, ok := n.(*ast.Ident); ok && strings.HasPrefix(id.Name, "Class") && id.Name != "Class" {
								listed = append(listed, id.Name)
							}
							return true
						})
					}
				}
			}
		}
	}
	if len(declared) == 0 {
		return nil, nil, fmt.Errorf("no Class constants in %s; has the vocabulary moved?", path)
	}
	if listed == nil {
		return nil, nil, fmt.Errorf("no Classes list in %s; has the vocabulary moved?", path)
	}
	return declared, listed, nil
}

var classRow = regexp.MustCompile("^\\|\\s*`([a-z_]+)`\\s*\\|")

// documentedClasses reads §6.5's table, in order.
func documentedClasses(doc string) ([]string, error) {
	start := strings.Index(doc, "### 6.5 ")
	if start < 0 {
		return nil, fmt.Errorf("%s has no §6.5", classesDoc)
	}
	section := doc[start+len("### 6.5 "):]
	if end := strings.Index(section, "\n#"); end >= 0 {
		section = section[:end]
	}
	var out []string
	for line := range strings.SplitSeq(section, "\n") {
		if m := classRow.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			out = append(out, m[1])
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("§6.5 of %s has no class table", classesDoc)
	}
	return out, nil
}

var bracketPrefix = regexp.MustCompile(`^\[([a-z_]+)\] `)

// emittedClasses finds every class the code uses as a value — a Class
// constant anywhere but its own declaration and the Classes list — and
// every string literal that spells a class out as "[name] ".
func emittedClasses(roots []string, declared map[string]string, source string) (map[string]string, map[string]string, int, error) {
	emitted := map[string]string{}
	bracketed := map[string]string{}
	read := 0
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			pkg := parsedPackage{fset: token.NewFileSet()}
			f, perr := parseOne(pkg.fset, path)
			if perr != nil {
				return perr
			}
			read++
			isSource := filepath.Clean(path) == filepath.Clean(source)
			for _, decl := range f.Decls {
				if gd, ok := decl.(*ast.GenDecl); ok && isSource && (gd.Tok == token.CONST || gd.Tok == token.VAR) {
					// The declarations and the list name every class by
					// definition; counting them would make this vacuous.
					continue
				}
				ast.Inspect(decl, func(n ast.Node) bool {
					switch v := n.(type) {
					case *ast.Ident:
						if val, ok := declared[v.Name]; ok {
							if _, seen := emitted[val]; !seen {
								emitted[val] = pkg.fset.Position(v.Pos()).String()
							}
						}
					case *ast.BasicLit:
						if s, ok := stringLit(v); ok {
							if m := bracketPrefix.FindStringSubmatch(s); m != nil {
								if _, seen := bracketed[m[1]]; !seen {
									bracketed[m[1]] = pkg.fset.Position(v.Pos()).String()
								}
							}
						}
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			return nil, nil, 0, err
		}
	}
	return emitted, bracketed, read, nil
}
