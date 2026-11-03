package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"io/fs"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/mmedum/gitlab-mcp/v4/scripts/internal/gatekit"
)

// The classes gate holds the closed error vocabulary (D-74) from every
// side the code has: the Class constants in internal/gapi, each carrying
// a comment, whose words it does not read; the Classes list against the
// constants; and the constants against what the code emits.
//
// A model branches on the class — [stale] means read again,
// [ambiguous_outcome] means go and look — so a class invented at a call
// site, or declared and never produced, is a lie told to the caller.

const classesSource = "internal/gapi/errors.go"

// plannedClasses are declared but not yet emitted, each with the reason.
// A planned class the code has started emitting fails until it leaves
// this list, so the list cannot outlive its reasons.
var plannedClasses = map[string]string{}

// classesFloors are the least the gate must have read.
type classesFloors struct{ files, classes int }

var realClassesFloors = classesFloors{files: 40, classes: 13}

func classes(out io.Writer, _ []string) error {
	report, problems, err := checkClasses(classesSource, []string{"internal", "cmd"}, plannedClasses, realClassesFloors)
	if err != nil {
		return err
	}
	if err := gatekit.Problems(out, "the error classes", problems); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, report)
	return nil
}

func checkClasses(source string, roots []string, planned map[string]string, fl classesFloors) (string, []string, error) {
	declared, listed, undocumented, err := declaredClasses(source)
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
	for _, name := range undocumented {
		problems = append(problems, fmt.Sprintf("%s declares %s with no comment; say what the class means and what a caller does next",
			source, name))
	}
	problems = append(problems, againstList(declared, listed)...)
	problems = append(problems, againstCode(values, emitted, bracketed, planned)...)
	slices.Sort(problems)
	return fmt.Sprintf("classes ok: %d classes, each declared with a comment, listed and emitted (%d planned), over %d files",
		len(values), len(planned), files), problems, nil
}

// againstList holds the Classes list to the constants: each once, and
// none left out.
func againstList(declared map[string]string, listed []string) []string {
	var problems []string
	counts := map[string]int{}
	for _, name := range listed {
		counts[name]++
		if _, ok := declared[name]; !ok {
			problems = append(problems, fmt.Sprintf("Classes lists %s, which is not a declared class", name))
		}
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

// declaredClasses reads the Class constants, name to value, the names
// the Classes list holds, and the constants that carry no comment.
func declaredClasses(path string) (map[string]string, []string, []string, error) {
	pkg, err := parsePackageDir(filepath.Dir(path))
	if err != nil {
		return nil, nil, nil, err
	}
	declared := map[string]string{}
	var listed, undocumented []string
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
							if strings.TrimSpace(vs.Doc.Text()) == "" && strings.TrimSpace(vs.Comment.Text()) == "" {
								undocumented = append(undocumented, name.Name)
							}
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
		return nil, nil, nil, fmt.Errorf("no Class constants in %s; has the vocabulary moved?", path)
	}
	if listed == nil {
		return nil, nil, nil, fmt.Errorf("no Classes list in %s; has the vocabulary moved?", path)
	}
	return declared, listed, undocumented, nil
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
