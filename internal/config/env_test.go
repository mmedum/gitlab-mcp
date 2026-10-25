package config_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/mmedum/gitlab-mcp/v4/internal/config"
	"github.com/mmedum/gitlab-mcp/v4/internal/credentials"
)

// varsFloor is how many variables Vars lists today, on purpose: the
// floor moves with it, so a registry that lists nothing cannot pass a
// walk over it.
const varsFloor = 15

// registry is every variable the server reads, the removed ones
// included, refused below the floor.
func registry(t *testing.T) []config.Var {
	t.Helper()
	if len(config.Vars) < varsFloor {
		t.Fatalf("Vars has %d variables, want at least %d", len(config.Vars), varsFloor)
	}
	vars := slices.Clone(config.Vars)
	for _, name := range config.RemovedVars() {
		vars = append(vars, config.Var{Name: name})
	}
	return vars
}

// A variable of spaces alone is unset, as an empty one is, and the spaces
// around a value are dropped, so a form field cleared to a space starts
// the server with the default. That holds for every variable the server
// reads: Define's through Build, and each one read outside Define through
// its reader. One read outside Define with no reader here fails, so a
// variable added there cannot miss the rule.
func TestASettingOfSpacesIsUnset(t *testing.T) {
	outside := map[string]func(value string) string{
		config.EnvRefreshToken: func(value string) string {
			s := credentials.Store{Profile: "p", Env: func(string) string { return value }}
			tok, src, err := s.Resolve()
			return fmt.Sprintf("%+v %q %v", tok, src, err)
		},
	}
	for _, v := range registry(t) {
		read := func(value string) string {
			c, err := config.Load(nil, func(name string) string {
				if name == v.Name {
					return value
				}
				return ""
			})
			return fmt.Sprintf("%+v %v", c, err)
		}
		if v.Outside {
			var ok bool
			if read, ok = outside[v.Name]; !ok {
				t.Errorf("%s is read outside Define, and this test has no reader for it", v.Name)
				continue
			}
			delete(outside, v.Name)
		}
		for _, pair := range [][2]string{{"  ", ""}, {" x ", "x"}} {
			if got, want := read(pair[0]), read(pair[1]); got != want {
				t.Errorf("%s=%q: %s\nwant %s, as %s=%q", v.Name, pair[0], got, want, v.Name, pair[1])
			}
		}
	}
	for name := range outside {
		t.Errorf("this test has a reader for %s, which Vars does not mark as read outside Define", name)
	}
}

// codeRoots hold the server's code and the scripts that drive it.
var codeRoots = []string{"../../cmd", "../../internal", "../../scripts"}

// source is one parsed Go file.
type source struct {
	path string
	file *ast.File
}

// codeTree parses every Go file under codeRoots but the tests, and fails
// when a root yields none, since a scan of it would read too little.
func codeTree(t *testing.T) (*token.FileSet, []source) {
	t.Helper()
	fset := token.NewFileSet()
	var files []source
	for _, root := range codeRoots {
		parsed := 0
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			parsed++
			files = append(files, source{path, f})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if parsed == 0 {
			t.Errorf("the scan parsed no Go file under %s; it read too little", root)
		}
	}
	return fset, files
}

// parseSources parses Go sources a test writes, keyed by path.
func parseSources(t *testing.T, srcs map[string]string) (*token.FileSet, []source) {
	t.Helper()
	fset := token.NewFileSet()
	var files []source
	for _, path := range slices.Sorted(maps.Keys(srcs)) {
		f, err := parser.ParseFile(fset, path, srcs[path], 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, source{path, f})
	}
	return fset, files
}

// variableName is a GITLAB_MCP_ variable, as a whole word. A name ends
// in a capital or a digit, so GITLAB_MCP_ENABLE_ is a prefix, not one.
var variableName = regexp.MustCompile(`^` + config.EnvPrefix + `[A-Z0-9_]*[A-Z0-9]$`)

// nameChar is a character a variable's name is made of.
func nameChar(r rune) bool { return r == '_' || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') }

// spelledNames is every GITLAB_MCP_ name a file spells, each with where.
// A name is seen as a string literal, or as a chain of literals and
// EnvPrefix joined by +, read in order: each run of them is one string,
// and any other operand ends the run and is read on its own. Every run
// is read whole: one ending in _, as EnvPrefix + "ENABLE_" + kind does,
// ends in a prefix, and one ending in a name's last character ends in
// that name. One built from any other constant, or through a format
// verb, is not seen.
func spelledNames(fset *token.FileSet, f *ast.File) map[string]string {
	found := map[string]string{}
	spell := func(s string, at token.Pos) {
		for _, word := range strings.FieldsFunc(s, func(r rune) bool { return !nameChar(r) }) {
			if variableName.MatchString(word) {
				found[word] = fset.Position(at).String()
			}
		}
	}
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BinaryExpr:
			if n.Op != token.ADD {
				return true
			}
			run, at := "", n.Pos()
			for _, op := range operands(n) {
				if s, ok := joined(op); ok {
					if run == "" {
						at = op.Pos()
					}
					run += s
					continue
				}
				spell(run, at)
				run = ""
				ast.Inspect(op, visit)
			}
			spell(run, at)
			return false
		case *ast.BasicLit:
			if s, err := strconv.Unquote(n.Value); n.Kind == token.STRING && err == nil {
				spell(s, n.Pos())
			}
		}
		return true
	}
	ast.Inspect(f, visit)
	return found
}

// A name the scan reads is a whole word that a chain does not go on to
// complete.
func TestTheNameScanReadsWholeNames(t *testing.T) {
	for _, tc := range []struct {
		expr string
		want []string
	}{
		{`EnvPrefix + "ENABLE_" + kind`, nil},
		{`config.EnvPrefix + kind`, nil},
		{`"set " + x + EnvPrefix + "NEW"`, []string{"GITLAB_MCP_NEW"}},
		{`"GITLAB_MCP_OLD=" + v`, []string{"GITLAB_MCP_OLD"}},
		{`(EnvPrefix + "A") + " and " + EnvPrefix + "B_" + kind`, []string{"GITLAB_MCP_A"}},
		{`EnvPrefix + "WHOLE" + suffix`, []string{"GITLAB_MCP_WHOLE"}},
	} {
		fset, files := parseSources(t, map[string]string{"p.go": "package p\n\nvar _ = " + tc.expr + "\n"})
		if got := slices.Sorted(maps.Keys(spelledNames(fset, files[0].file))); !slices.Equal(got, tc.want) {
			t.Errorf("%s spells %q, want %q", tc.expr, got, tc.want)
		}
	}
}

// Every GITLAB_MCP_ variable the server's code spells is one the registry
// lists, so a variable read outside Define cannot escape the bundle and
// staleness gates. So is every one scripts/ spells: the live driver, the
// evals and the smoke test set them for the server, and a misspelling
// there tests a configuration nobody asked for. The scan must find a
// name outside the config package, or it read too little.
func TestEveryVariableTheCodeNamesIsInTheRegistry(t *testing.T) {
	known := map[string]bool{}
	for _, v := range registry(t) {
		known[v.Name] = true
	}
	fset, files := codeTree(t)
	found := map[string]string{}
	elsewhere := 0
	for _, src := range files {
		names := spelledNames(fset, src.file)
		if filepath.Dir(src.path) != filepath.Join("..", "..", "internal", "config") {
			elsewhere += len(names)
		}
		maps.Copy(found, names)
	}
	if elsewhere == 0 {
		t.Errorf("the scan found no %s name outside internal/config; it read too little", config.EnvPrefix)
	}
	for _, v := range slices.Sorted(maps.Keys(found)) {
		if !known[v] {
			t.Errorf("%s names %s, which the registry does not list", found[v], v)
		}
	}
}

// lookupFloor is how many reads of a variable outside Define go through
// config.Lookup today: status and doctor read the test instance, the
// credential store the refresh token, and the smoke test's fake server
// the read-only switch.
const lookupFloor = 4

// variableReads is every read of a GITLAB_MCP_ variable in files by a
// name the scan resolves: a string literal, a + chain, or a constant
// declared in files as one of those, an alias of another included. A
// read is a call with that name as its one argument, as env(name),
// os.Getenv(name) and os.LookupEnv(name) are; one through config.Lookup,
// which takes env and the name, is counted as wrapped, and any other is
// raw, by where it is. A name the scan cannot resolve, such as a loop's
// v.Name, is not seen. The config package's own reads are Define's, which
// TestASettingOfSpacesIsUnset drives, and its Settings.get reads what
// Define read, so a raw read there is not counted.
func variableReads(fset *token.FileSet, files []source) (raw []string, wrapped int) {
	consts, ambiguous := map[string]string{}, map[string]bool{}
	var value func(e ast.Expr) (string, bool)
	value = func(e ast.Expr) (string, bool) {
		switch e := e.(type) {
		case *ast.BasicLit:
			s, err := strconv.Unquote(e.Value)
			return s, e.Kind == token.STRING && err == nil
		case *ast.ParenExpr:
			return value(e.X)
		case *ast.BinaryExpr:
			if e.Op != token.ADD {
				return "", false
			}
			x, ok := value(e.X)
			if !ok {
				return "", false
			}
			y, ok := value(e.Y)
			return x + y, ok
		case *ast.Ident:
			s, ok := consts[e.Name]
			return s, ok
		case *ast.SelectorExpr:
			s, ok := consts[e.Sel.Name]
			return s, ok
		}
		return "", false
	}
	// Each pass resolves the constants declared from ones resolved before,
	// so an alias of an alias settles too. A name two packages declare
	// with different values is dropped.
	for changed := true; changed; {
		changed = false
		for _, src := range files {
			for _, decl := range src.file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.CONST {
					continue
				}
				for _, spec := range gen.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, name := range vs.Names {
						if i >= len(vs.Values) || ambiguous[name.Name] {
							continue
						}
						s, ok := value(vs.Values[i])
						prev, seen := consts[name.Name]
						switch {
						case !ok || (seen && prev == s):
						case seen:
							ambiguous[name.Name] = true
							delete(consts, name.Name)
						default:
							consts[name.Name] = s
							changed = true
						}
					}
				}
			}
		}
	}
	for _, src := range files {
		inConfig := src.file.Name.Name == "config"
		ast.Inspect(src.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn := call.Fun
			if sel, ok := fn.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "config" {
					fn = sel.Sel
				}
			}
			if id, ok := fn.(*ast.Ident); ok && id.Name == "Lookup" && len(call.Args) == 2 {
				if name, ok := value(call.Args[1]); ok && variableName.MatchString(name) {
					wrapped++
				}
				return true
			}
			if len(call.Args) == 1 && !inConfig {
				if name, ok := value(call.Args[0]); ok && variableName.MatchString(name) {
					raw = append(raw, fmt.Sprintf("%s reads %s", fset.Position(call.Pos()), name))
				}
			}
			return true
		})
	}
	return raw, wrapped
}

// The read scan sees a variable read by any name it resolves, and tells
// a read through config.Lookup from a raw one and from a mention.
func TestTheReadScanTellsRawReadsFromLookup(t *testing.T) {
	fset, files := parseSources(t, map[string]string{
		"config.go": "package config\n\nconst EnvPrefix = \"GITLAB_MCP_\"\n\nconst EnvLogLevel = EnvPrefix + \"LOG_LEVEL\"\n\n" +
			"func (s *Settings) level() string { return s.get(EnvLogLevel) }\n",
		"reader.go": `package reader

const tokenVar = EnvAlias

const EnvAlias = config.EnvLogLevel

func read(env func(string) string, o options) {
	_ = env(config.EnvLogLevel)
	_ = os.Getenv(config.EnvPrefix + "HTTP_TIMEOUT")
	_, _ = os.LookupEnv("GITLAB_MCP_LOG_FORMAT")
	_ = o.Env(tokenVar)
	_ = config.Lookup(env, config.EnvLogLevel)
	_ = Lookup(env, EnvAlias)
	_ = fmt.Errorf("%s is set", config.EnvLogLevel)
	_ = env("SMOKE_FAKE")
	for _, v := range config.Vars {
		_ = env(v.Name)
	}
}
`,
	})
	raw, wrapped := variableReads(fset, files)
	want := []string{
		"reader.go:8:6 reads GITLAB_MCP_LOG_LEVEL",
		"reader.go:9:6 reads GITLAB_MCP_HTTP_TIMEOUT",
		"reader.go:10:9 reads GITLAB_MCP_LOG_FORMAT",
		"reader.go:11:6 reads GITLAB_MCP_LOG_LEVEL",
	}
	if !slices.Equal(raw, want) || wrapped != 2 {
		t.Errorf("variableReads = %q, %d wrapped\nwant %q, 2 wrapped", raw, wrapped, want)
	}
}

// Every read of a variable goes through config.Lookup, so spaces alone
// are unset and a value is trimmed whichever package reads it: Define's
// reads, which TestASettingOfSpacesIsUnset drives, and every other one,
// which a raw env(name) or os.Getenv(name) would read as given.
func TestEveryReadOfAVariableGoesThroughLookup(t *testing.T) {
	raw, wrapped := variableReads(codeTree(t))
	for _, r := range raw {
		t.Errorf("%s without config.Lookup", r)
	}
	if wrapped < lookupFloor {
		t.Errorf("the scan found %d reads through config.Lookup, want at least %d; it read too little", wrapped, lookupFloor)
	}
}

// operands is a + chain's operands in order, through parentheses.
func operands(e ast.Expr) []ast.Expr {
	switch e := e.(type) {
	case *ast.BinaryExpr:
		if e.Op == token.ADD {
			return append(operands(e.X), operands(e.Y)...)
		}
	case *ast.ParenExpr:
		if b, ok := e.X.(*ast.BinaryExpr); ok && b.Op == token.ADD {
			return operands(b)
		}
	}
	return []ast.Expr{e}
}

// joined is a + chain of string literals and EnvPrefix as one string.
func joined(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		x, ok := joined(e.X)
		if !ok {
			return "", false
		}
		y, ok := joined(e.Y)
		return x + y, ok
	case *ast.ParenExpr:
		return joined(e.X)
	case *ast.BasicLit:
		s, err := strconv.Unquote(e.Value)
		return s, e.Kind == token.STRING && err == nil
	case *ast.Ident:
		return config.EnvPrefix, e.Name == "EnvPrefix"
	case *ast.SelectorExpr:
		return config.EnvPrefix, e.Sel.Name == "EnvPrefix"
	}
	return "", false
}
