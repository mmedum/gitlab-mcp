package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/mmedum/gitlab-mcp/v4/scripts/internal/gatekit"
)

// The pins gate holds every third-party tool to one exact version, the
// same version everywhere it is named.
//
// The ways a version floats, each a rule:
//   - an action used by tag or branch rather than a 40-hex commit SHA,
//     or a SHA with no comment saying which version it is, or a comment
//     naming a CodeQL bundle rather than the action's release, and a
//     container image, a step's, a job's or a service's, without its
//     sha256 digest;
//   - an action that installs a tool without naming the tool's version:
//     a SHA pins the wrapper, not the tool;
//   - a version input, env value or `go run module@v` that is a range, a
//     major line or `latest`;
//   - one tool pinned to two versions in two files, so a rehearsal or a
//     pre-commit scan runs something other than CI;
//   - a Makefile variable that pins a tool no recipe runs with the go
//     command, so the tool runs from the PATH;
//   - a known tool's module@version, on any line of a workflow or the
//     Makefile but a comment line, that is not one exact version: the
//     readers do not follow bash -c, eval, $'…' or a make conditional, and
//     the text holds what they miss.
//
// Every action is classified as an installer or not, and an unknown one
// fails: being unclassified is how an unpinned tool slips through. And
// every workflow pins its shell at the workflow level: on Windows the
// default is PowerShell, which reads -coverprofile=cov.out as a file
// called `cov`.
//
// No table of pins in the docs is compared: a gate holding code to a
// copy in prose is machinery to maintain a duplicate.

const (
	pinsMakefile = "Makefile"
	pinsGoMod    = "go.mod"
	pinsScripts  = "scripts"
)

// pinsRequiredWorkflows must exist; a gate that counted files would
// pass with the wrong four.
var pinsRequiredWorkflows = []string{"ci.yml", "codeql.yml", "release.yml", "publish-mcp.yml"}

// pinsTool is one tool this repository runs, and every way it can be
// named.
type pinsTool struct {
	name string
	// modules match a `module@version` in the Makefile, a run line or
	// Go source under scripts/.
	modules []string
	// envKeys are env names whose value is this tool's version.
	envKeys []string
	// actions install this tool, with the input that pins it.
	actions map[string]string
	// commentOf reads the version from the trailing comment on these
	// actions' SHA pins, for an action that is the tool itself.
	commentOf []string
	// fromGoMod reads the version from go.mod: "go" or a module path. The
	// go row's action input names go.mod rather than a version.
	fromGoMod string
}

// pinsTools is every tool, with the installer actions a workflow uses. A
// row with installers fails the floor when no workflow step uses one.
var pinsTools = []pinsTool{
	{name: "go", fromGoMod: "go", actions: map[string]string{"actions/setup-go": "go-version-file"}},
	{name: "mcp-go-sdk", fromGoMod: "github.com/modelcontextprotocol/go-sdk"},
	{name: "golangci-lint", modules: []string{"github.com/golangci/golangci-lint"}},
	{name: "goreleaser", modules: []string{"github.com/goreleaser/goreleaser"}, envKeys: []string{"GORELEASER_VERSION"},
		actions: map[string]string{"goreleaser/goreleaser-action": "version"}},
	{name: "cosign", actions: map[string]string{"sigstore/cosign-installer": "cosign-release"}},
	{name: "syft", actions: map[string]string{
		"anchore/sbom-action/download-syft": "syft-version", "anchore/sbom-action": "syft-version"}},
	{name: "mcp-publisher", envKeys: []string{"PUBLISHER_VERSION"}},
	{name: "gitleaks", modules: []string{"github.com/zricethezav/gitleaks", "github.com/gitleaks/gitleaks"},
		envKeys: []string{"GITLEAKS_VERSION"}},
	{name: "govulncheck", modules: []string{"golang.org/x/vuln/cmd/govulncheck"}},
	{name: "go-licenses", modules: []string{"github.com/google/go-licenses"}},
	{name: "actionlint", modules: []string{"github.com/rhysd/actionlint"}},
	{name: "codeql-action", commentOf: []string{"github/codeql-action/"}},
}

// pinsNotInstallers are actions that install no tool, each with the
// reason, so adding one is a decision rather than an omission.
var pinsNotInstallers = map[string]string{
	"actions/checkout":                "checks out the repository",
	"actions/upload-artifact":         "uploads, installs nothing",
	"actions/download-artifact":       "downloads, installs nothing",
	"actions/attest-build-provenance": "calls the attestation API",
	"github/codeql-action/init":       "CodeQL's query bundle is GitHub's to manage; the action's own version is held by comment",
	"github/codeql-action/autobuild":  "builds with the Go already installed",
	"github/codeql-action/analyze":    "uploads results",
}

var (
	pinsSHA   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	pinsExact = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)
	// pinsModuleAt is a module@version in text. The version ends where a
	// shell word or command does.
	pinsModuleAt  = regexp.MustCompile(`([A-Za-z0-9_.\-]+(?:/[A-Za-z0-9_.\-]+)+)@([^\s"'` + "`" + `);&|\\]+)`)
	pinsSemverIn  = regexp.MustCompile(`v?\d+\.\d+\.\d+`)
	pinsVersionEn = regexp.MustCompile(`_VERSION$`)
	// pinsImageDigest is a container image pinned by its content digest.
	pinsImageDigest = regexp.MustCompile(`^[^@\s]+@sha256:[0-9a-f]{64}$`)
	// pinsGoModToolchain and pinsGoModGo are setup-go's own patterns for
	// the toolchain directive and the go line.
	pinsGoModToolchain = regexp.MustCompile(`(?m)^toolchain go(1\.\d+(?:\.\d+|rc\d+)?)`)
	pinsGoModGo        = regexp.MustCompile(`(?m)^go (\d+(?:\.\d+)*)`)
)

// pinsFound is one place a tool's version is written.
type pinsFound struct {
	version, at string
}

func pins(out io.Writer, _ []string) error {
	r, err := pinsCheck(".")
	if err != nil {
		return err
	}
	if err := gatekit.Problems(out, "the pins", r.problems); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "pins ok: %d workflows with their shell pinned, %d actions by SHA, "+
		"%d installers name their tool, %d tools pinned in %d places, each one version\n",
		r.workflows, r.actions, r.installers, len(r.tools), r.places)
	return nil
}

// pinsReport is what the gate read and found.
type pinsReport struct {
	problems                               []string
	workflows, actions, installers, places int
	tools                                  map[string][]pinsFound
	// installed names each tool a workflow step installs.
	installed map[string]bool
	// judged is each tool@version a rule has recorded or refused, which the
	// text rule does not report again.
	judged map[string]bool
}

func (r *pinsReport) fail(format string, a ...any) {
	r.problems = append(r.problems, fmt.Sprintf(format, a...))
}

// judgeLatest marks each known tool's module@latest in text as refused.
func (r *pinsReport) judgeLatest(text string) {
	for _, m := range pinsModuleAt.FindAllStringSubmatch(text, -1) {
		if tool := pinsToolByModule(m[1]); tool != "" && m[2] == "latest" {
			r.judged[tool+"@"+m[2]] = true
		}
	}
}

// add records one pin, refusing anything but one exact version.
func (r *pinsReport) add(tool, version, at string) {
	r.tools[tool] = append(r.tools[tool], pinsFound{version, at})
	r.judged[tool+"@"+version] = true
	r.places++
	if !pinsExact.MatchString(version) {
		r.fail("%s: %s is pinned to %q, which is not one exact version", at, tool, version)
	}
}

// pinsCheck runs every rule over the repository at root.
func pinsCheck(root string) (*pinsReport, error) {
	flows, err := readWorkflows(root)
	if err != nil {
		return nil, err
	}
	mk, err := readMakefile(filepath.Join(root, pinsMakefile))
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(root, pinsGoMod)) //nolint:gosec // a repository path
	if err != nil {
		return nil, err
	}
	gomod := string(raw)

	r := &pinsReport{tools: map[string][]pinsFound{}, installed: map[string]bool{}, judged: map[string]bool{},
		workflows: len(flows)}
	for _, base := range pinsRequiredWorkflows {
		if _, ok := workflowNamed(flows, base); !ok {
			r.fail("%s/%s does not exist", workflowDir, base)
		}
	}
	for _, w := range flows {
		pinsWorkflow(gomod, w, r)
	}
	pinsMakefileRecipes(mk, pinsMakefileVars(mk, r), r)
	pinsGoModVersions(gomod, r)
	if err := pinsGoSource(root, r); err != nil {
		return nil, err
	}
	// Last, so a version a reader recorded is not reported twice.
	for _, w := range flows {
		pinsTextVersions(w.path, w.text, func(s string) string { return s }, r)
	}
	pinsTextVersions(pinsMakefile, mk.text, mk.expand, r)

	for _, t := range pinsTools {
		found := r.tools[t.name]
		if len(found) == 0 {
			r.fail("%s is pinned nowhere: a tool this gate names that no file pins is either gone or unread", t.name)
			continue
		}
		for _, f := range found[1:] {
			if pinsNormal(f.version) != pinsNormal(found[0].version) {
				r.fail("%s is %s at %s and %s at %s: one tool, one version",
					t.name, found[0].version, found[0].at, f.version, f.at)
			}
		}
	}

	// Floors: zero findings and zero inputs look the same otherwise.
	r.problems = append(r.problems, gatekit.Floor("workflows", r.workflows, len(pinsRequiredWorkflows))...)
	r.problems = append(r.problems, gatekit.Floor("action references", r.actions, 10)...)
	// A floor per installer, not a count of steps: one installer used in
	// every job would meet a count alone.
	for _, t := range pinsTools {
		if len(t.actions) > 0 && !r.installed[t.name] {
			r.fail("no workflow step installs %s through %s; the reader is not seeing the step, "+
				"or the table names an installer nothing uses", t.name, strings.Join(slices.Sorted(maps.Keys(t.actions)), " or "))
		}
	}
	r.problems = append(r.problems, gatekit.Floor("pinned versions", r.places, 12)...)
	return r, nil
}

// pinsWorkflow applies the workflow rules to one file, with gomod the
// repository's go.mod.
func pinsWorkflow(gomod string, w workflow, r *pinsReport) {
	if strings.TrimSpace(w.Defaults.Run.Shell) == "" {
		r.fail("%s: no workflow-level `defaults: run: shell:`. Every run step goes to a shell, "+
			"so a job-level block only fixes the jobs that exist today", w.path)
	}
	pinsLatestLines(w, r)

	// Every `uses:` is held on the parse, which sees it in every shape YAML
	// allows: on the line after its key, quoted, or in a flow mapping.
	steps := w.steps()
	envs := make([]map[string]string, 0, 1+len(w.Jobs)+len(steps))
	envs = append(envs, w.Env)
	for _, name := range slices.Sorted(maps.Keys(w.Jobs)) {
		job := w.Jobs[name]
		envs = append(envs, job.Env)
		if job.Uses != "" && !strings.HasPrefix(job.Uses, "./") {
			pinsActionRef(w, job.Uses, job.uses, r)
		}
	}
	for _, s := range steps {
		envs = append(envs, s.Env)
		switch {
		case s.Uses == "" || strings.HasPrefix(s.Uses, "./"):
			pinsRunLines(s.Run, w.path, r)
		case strings.HasPrefix(s.Uses, "docker://"):
			r.actions++
			if !pinsImageDigest.MatchString(s.Uses) {
				r.fail("%s: job %s: %s is a container image with no sha256 digest, so the image it runs can change",
					w.path, s.job, s.Uses)
			}
		default:
			pinsActionRef(w, s.Uses, s.uses, r)
			pinsStep(gomod, w, s, r)
		}
	}
	for _, env := range envs {
		pinsEnv(w.path, env, r)
	}
	pinsJobImages(w, r)
}

// pinsJobImages holds each job's container and service images to a
// sha256 digest, as a step's docker:// image is held
// (https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idcontainer).
func pinsJobImages(w workflow, r *pinsReport) {
	for _, id := range slices.Sorted(maps.Keys(w.Jobs)) {
		job := w.Jobs[id]
		images := map[string]string{"the container": workflowImage(&job.Container)}
		for name, service := range job.Services {
			images["the service "+name] = workflowImage(&service)
		}
		for _, what := range slices.Sorted(maps.Keys(images)) {
			if image := images[what]; image != "" && !pinsImageDigest.MatchString(image) {
				r.fail("%s: job %s runs %s from %s, which has no sha256 digest, so the image it runs can change",
					w.path, id, what, image)
			}
		}
	}
}

// pinsLatestLines refuses @latest anywhere outside a comment, in a run
// line or an input as well as a `uses:`.
func pinsLatestLines(w workflow, r *pinsReport) {
	for i, line := range strings.Split(w.text, "\n") {
		if trimmed := strings.TrimSpace(line); !strings.HasPrefix(trimmed, "#") && strings.Contains(trimmed, "@latest") {
			r.fail("%s:%d: something is pinned to @latest", w.path, i+1)
			r.judgeLatest(trimmed)
		}
	}
}

// pinsTextVersions holds a known tool's module@version on any line of text
// but a comment line to one exact version, whatever the readers made of
// the line, expanded by expand first. The readers do not follow bash -c,
// eval, $'…', a # inside $(…) or a make conditional, and are not grown to:
// a command there is read here as text. A version a rule already recorded
// or refused is not reported again.
func pinsTextVersions(path, text string, expand func(string) string, r *pinsReport) {
	for i, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, m := range pinsModuleAt.FindAllStringSubmatch(expand(line), -1) {
			tool, version := pinsToolByModule(m[1]), m[2]
			if tool == "" || pinsExact.MatchString(version) || r.judged[tool+"@"+version] {
				continue
			}
			r.fail("%s:%d: %s is pinned to %q, which is not one exact version", path, i+1, tool, version)
		}
	}
}

// pinsActionRef holds one action reference to a commit SHA, with the
// version it is in the comment after it.
func pinsActionRef(w workflow, uses string, ref workflowRef, r *pinsReport) {
	r.actions++
	at := fmt.Sprintf("%s:%d", w.path, ref.line)
	action, sha, ok := strings.Cut(uses, "@")
	switch {
	case !ok:
		r.fail("%s: %s has no ref at all", at, action)
	case !pinsSHA.MatchString(sha):
		r.fail("%s: %s is pinned to %q, a tag or branch that can be moved; pin the 40-character commit SHA",
			at, action, sha)
	case ref.comment == "":
		r.fail("%s: %s is pinned by SHA with no comment saying which version it is", at, action)
	case strings.Contains(ref.comment, "bundle"):
		r.fail("%s: %s is labeled %q, a query-bundle tag; label it with the action's own release tag",
			at, action, ref.comment)
	case !pinsExact.MatchString(strings.Fields(ref.comment)[0]):
		r.fail("%s: %s is labeled %q; the comment starts with the exact version the SHA is", at, action, ref.comment)
	}
	for _, t := range pinsTools {
		for _, prefix := range t.commentOf {
			if strings.HasPrefix(action, prefix) {
				r.add(t.name, pinsSemverIn.FindString(ref.comment), at)
			}
		}
	}
}

// pinsStep checks one step that uses an action: an installer sets the
// input that pins its tool, any other action is classified, and every
// version input names one exact version.
func pinsStep(gomod string, w workflow, s workflowStep, r *pinsReport) {
	action := s.action()
	at := w.path + ": " + action
	installer := false
	for _, t := range pinsTools {
		key, ok := t.actions[action]
		if !ok {
			continue
		}
		installer = true
		r.installers++
		r.installed[t.name] = true
		v, set := s.input(key)
		if !set {
			r.fail("%s is pinned by SHA and does not set %s, so the tool it installs is whatever "+
				"is current that morning", at, key)
			continue
		}
		if t.fromGoMod == "go" {
			version, problem := pinsGoVersionFile(v, gomod)
			if problem != "" {
				r.fail("%s sets %s to %q, %s", at, key, v, problem)
				continue
			}
			v = version
		}
		r.add(t.name, w.resolveEnv(v), at)
	}
	if _, known := pinsNotInstallers[action]; !installer && !known {
		r.fail("%s is not classified: add it to pinsTools with the input that pins its tool, "+
			"or to pinsNotInstallers with the reason", at)
	}
	for _, key := range slices.Sorted(maps.Keys(s.With)) {
		if key == "go-version-file" || (!strings.HasSuffix(key, "version") && !strings.HasSuffix(key, "release")) {
			continue
		}
		if v, _ := s.input(key); !pinsExact.MatchString(w.resolveEnv(v)) {
			r.fail("%s: %s is %q, which is not one exact version", at, key, v)
		}
	}
}

// pinsEnv reads the tool versions an env block names.
func pinsEnv(path string, env map[string]string, r *pinsReport) {
	for _, key := range slices.Sorted(maps.Keys(env)) {
		if !pinsVersionEn.MatchString(key) {
			continue
		}
		tool := pinsToolByEnv(key)
		if tool == "" {
			r.fail("%s: the env value %s names a version of a tool pinsTools does not know", path, key)
			continue
		}
		r.add(tool, env[key], path+": "+key)
	}
}

// pinsGoRun is one module@version the go command runs or installs.
type pinsGoRun struct{ verb, pkg string }

// pinsGoRuns is each module@version a command line runs or installs with
// the go command, read as the shell splits it, its flags skipped. A
// package of this module has no version and is not one.
func pinsGoRuns(line string) []pinsGoRun {
	var out []pinsGoRun
	for _, c := range gatekit.GoCommands(line) {
		pkgs := gatekit.GoAfterFlags(c.Args)
		switch {
		case c.Sub == "run" && len(pkgs) > 0:
			// Run's words after its one package are the program's.
			pkgs = pkgs[:1]
		case c.Sub != "install":
			continue
		}
		for _, pkg := range pkgs {
			if strings.Contains(pkg, "@") {
				out = append(out, pinsGoRun{c.Sub, pkg})
			}
		}
	}
	return out
}

// pinsRunLines reads `go run module@version` and `go install
// module@version` out of a run block.
func pinsRunLines(run, path string, r *pinsReport) {
	for _, line := range workflowRunLines(run) {
		for _, g := range pinsGoRuns(line) {
			module, version, _ := strings.Cut(g.pkg, "@")
			if tool := pinsToolByModule(module); tool != "" {
				r.add(tool, version, path+": go "+g.verb+" "+module)
			} else {
				r.fail("%s: go %s %s names a tool pinsTools does not know", path, g.verb, g.pkg)
			}
		}
	}
}

// pinsGoVersionFile is the Go version a go-version-file gives, read as
// setup-go reads a go.mod, or why it gives none. gomod is the
// repository's go.mod, the one file the Go version is held to
// (https://github.com/actions/setup-go/blob/b7ad1dad31e06c5925ef5d2fc7ad053ef454303e/src/installer.ts#L645,
// setup-go v7.0.0).
func pinsGoVersionFile(file, gomod string) (version, problem string) {
	if path.Clean(file) != pinsGoMod {
		return "", "which is not the repository's " + pinsGoMod
	}
	if v := pinsGoVersion(gomod); v != "" {
		return v, ""
	}
	return "", "which names no Go version"
}

// pinsGoVersion is the Go a go.mod runs: its toolchain directive, or else
// its go line. setup-go installs that, and the go command switches to it.
func pinsGoVersion(gomod string) string {
	if m := pinsGoModToolchain.FindStringSubmatch(gomod); m != nil {
		return m[1]
	}
	if m := pinsGoModGo.FindStringSubmatch(gomod); m != nil {
		return m[1]
	}
	return ""
}

// pinsMakefileVars reads every `module@version` variable, and returns each
// module@version every such variable's value holds, by variable.
func pinsMakefileVars(mk *makefile, r *pinsReport) map[string][]string {
	pinnedBy := map[string][]string{}
	for _, name := range slices.Sorted(maps.Keys(mk.vars)) {
		// Recipes are read expanded, so a version kept in another
		// variable is compared as the recipe runs it.
		value := mk.expand(mk.vars[name])
		found := pinsModuleAt.FindAllStringSubmatch(value, -1)
		for _, m := range found {
			pinnedBy[name] = append(pinnedBy[name], m[0])
		}
		if strings.Contains(value, "@latest") {
			r.fail("%s: %s is pinned to @latest", pinsMakefile, name)
			r.judgeLatest(value)
			continue
		}
		for _, m := range found {
			if tool := pinsToolByModule(m[1]); tool != "" {
				r.add(tool, m[2], pinsMakefile+": "+name)
			} else {
				r.fail("%s: %s names %s, which pinsTools does not know", pinsMakefile, name, m[1])
			}
		}
	}
	return pinnedBy
}

// pinsMakefileRecipes holds what the recipes run with the go command. A
// module written into a recipe is a pin of its own, and a variable in
// pinnedBy whose module@version no recipe naming it runs pins nothing: the
// tool then runs from the PATH.
func pinsMakefileRecipes(mk *makefile, pinnedBy map[string][]string, r *pinsReport) {
	// ran is each variable and module@version a recipe naming it runs.
	ran := map[[2]string]bool{}
	for _, target := range slices.Sorted(maps.Keys(mk.targets)) {
		for _, raw := range mk.targets[target].recipe {
			refs := mk.refs(raw)
			for _, g := range pinsGoRuns(mk.expand(raw)) {
				named := false
				for name := range refs {
					if slices.Contains(pinnedBy[name], g.pkg) {
						named = true
						ran[[2]string{name, g.pkg}] = true
					}
				}
				if named {
					continue
				}
				module, version, _ := strings.Cut(g.pkg, "@")
				if tool := pinsToolByModule(module); tool != "" {
					r.add(tool, version, pinsMakefile+": "+target)
				} else {
					r.fail("%s: %s runs go %s %s, a tool pinsTools does not know", pinsMakefile, target, g.verb, g.pkg)
				}
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(pinnedBy)) {
		for _, pkg := range pinnedBy[name] {
			if !ran[[2]string{name, pkg}] {
				r.fail("%s: %s pins %s and no recipe runs it with the go command, so the tool runs from the PATH",
					pinsMakefile, name, pkg)
			}
		}
	}
}

// pinsGoModVersions reads the Go a go.mod runs and its required modules.
func pinsGoModVersions(gomod string, r *pinsReport) {
	for _, t := range pinsTools {
		if t.fromGoMod == "go" {
			r.add(t.name, pinsGoVersion(gomod), pinsGoMod+": go")
		}
	}
	for line := range strings.SplitSeq(gomod, "\n") {
		fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "require")))
		if len(fields) < 2 {
			continue
		}
		for _, t := range pinsTools {
			if t.fromGoMod != "" && t.fromGoMod != "go" && fields[0] == t.fromGoMod {
				r.add(t.name, fields[1], pinsGoMod+": "+t.fromGoMod)
			}
		}
	}
}

// pinsGoSource reads every `module@version` of a known tool out of the
// string literals in the maintainer tooling's Go source, so a copy of a
// pin in a gate is held to the Makefile's like any other. A comment pins
// nothing, so an example in one is not read.
func pinsGoSource(root string, r *pinsReport) error {
	dir := filepath.Join(root, pinsScripts)
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			text, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			for _, m := range pinsModuleAt.FindAllStringSubmatch(text, -1) {
				if tool := pinsToolByModule(m[1]); tool != "" {
					r.add(tool, m[2], filepath.ToSlash(rel)+": "+m[1])
				}
			}
			return true
		})
		return nil
	})
}

// pinsToolByModule is the tool a module path is, or one of its packages
// is: a path that only starts with the same text is another module.
func pinsToolByModule(module string) string {
	for _, t := range pinsTools {
		for _, m := range t.modules {
			if module == m || strings.HasPrefix(module, m+"/") {
				return t.name
			}
		}
	}
	return ""
}

func pinsToolByEnv(key string) string {
	for _, t := range pinsTools {
		if slices.Contains(t.envKeys, key) {
			return t.name
		}
	}
	return ""
}

func pinsNormal(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }
