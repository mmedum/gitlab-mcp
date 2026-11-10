package main

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"maps"
	"math/bits"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mmedum/gitlab-mcp/v4/scripts/internal/gatekit"
)

// The parity gate holds `make check` and CI to the same set, and every
// gate in the registry to the pipeline its mode declares.
//
// The two lists live in two files and a person only ever edits one of
// them. So targets are mapped to CI steps by what they run, not by name:
// a CI step runs a target when it calls `make <target>`, or when its
// command is the target's recipe. A step's `name:` is not a command and
// is not read, and neither is a commented line. Makefile variables are
// expanded before anything is compared. The vet passes are held to the
// build tags the tree's files need, read from their //go:build lines.

const (
	parityMakefile = "Makefile"
	parityCI       = "ci.yml"
	parityHook     = ".githooks/pre-commit"
	parityLint     = ".golangci.yml"
)

// parityPROnly are the targets CI's pull-request job runs that `make
// check` does not, each with the reason. An entry CI no longer runs
// fails, so the list cannot outlive its reasons.
var parityPROnly = map[string]string{
	"merge-base": "prints the commit a pull request is measured from, which only a pull request has",
	"changelog":  "measures a pull request's CHANGELOG entry from its merge-base",
	"schema-ack": "measures a pull request's tool-surface change from its merge-base",
}

// parityPrereqOnly are targets that check nothing themselves and are
// reached only as another target's prerequisite, each with the reason.
// Each must be a prerequisite of something `make check` runs and must
// not be named by check: or by a CI step, where it would read as a gate.
var parityPrereqOnly = map[string]string{
	"schemas": "writes the schema dump that descriptions, bodies and schema-diff read and CI uploads",
}

// parityReleaseOnly are the release commands, each with where it runs.
// TestParityExcusesEveryReleaseCommand holds this against the registry.
var parityReleaseOnly = map[string]string{
	"mcpb-pack":     "the universal binary's post hook in .goreleaser.yaml, the one point every binary exists",
	"release-notes": "release.yml writes the release body from the CHANGELOG before goreleaser runs",
}

// parityReleaseFiles are where a release command must be found.
var parityReleaseFiles = []string{".goreleaser.yaml", ".github/workflows/release.yml", ".github/workflows/publish-mcp.yml"}

// parityArtifact is the file CI uploads for a reviewer, which a target
// in CI's closure has to write.
const parityArtifact = "schemas.json"

// parityGateBinary are the words that run the built binary, raw and as
// $(GATES) expands on Windows.
var parityGateBinary = map[string]bool{"$(GATES)": true, "./.gates": true, "./.gates$(EXE)": true, "./.gates.exe": true}

// parityGateName is a gates command's shape. The word after the binary is
// a call only in that shape: `rm $(GATES) cov.out` names a file.
var parityGateName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func parity(out io.Writer, _ []string) error {
	r, err := parityCheck(".", commands)
	if err != nil {
		return err
	}
	if err := gatekit.Problems(out, "make/CI parity", r.problems); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "parity ok: %d check prerequisites, %d CI command lines, %d gates in check, "+
		"%d gate calls, %d tagged //go:build lines vetted on both sides, %d PR-only, %d prerequisite-only and %d release-only entries excused\n",
		r.prereqs, r.ciLines, r.checkGates, r.gateCalls, r.vetTags,
		len(parityPROnly), len(parityPrereqOnly), len(parityReleaseOnly))
	return nil
}

// parityReport is what the gate read and found.
type parityReport struct {
	problems                                         []string
	prereqs, ciLines, checkGates, gateCalls, vetTags int
}

func (r *parityReport) fail(format string, a ...any) {
	r.problems = append(r.problems, fmt.Sprintf(format, a...))
}

// parityPipelines is what the Makefile and CI run, as parityCheck read it.
type parityPipelines struct {
	mk        *makefile
	ciTargets map[string]bool
	// ciCommands are every CI command line, and pushCommands those a push
	// to main runs.
	ciCommands, pushCommands []string
	ciClosure, checkClosure  []string
	// prTargets are what only a pull request runs, and otherTargets what a
	// push to main runs; ciClosure is the second's closure.
	prTargets, otherTargets map[string]bool
	// checkLines are the recipe lines `make check` runs, and ciLines those
	// a push runs, with its command lines, each raw and expanded.
	checkLines, ciLines []string
}

// parityCheck compares the repository at root with a registry.
func parityCheck(root string, registry map[string]command) (*parityReport, error) {
	mk, err := readMakefile(filepath.Join(root, parityMakefile))
	if err != nil {
		return nil, err
	}
	check, ok := mk.targets["check"]
	if !ok {
		return nil, fmt.Errorf("%s has no check: target", parityMakefile)
	}
	flows, err := readWorkflows(root)
	if err != nil {
		return nil, err
	}
	ci, ok := workflowNamed(flows, parityCI)
	if !ok {
		return nil, fmt.Errorf("%s/%s does not exist", workflowDir, parityCI)
	}
	needs, err := parityTreeTags(root)
	if err != nil {
		return nil, fmt.Errorf("reading the build tags the code uses: %w", err)
	}

	r := &parityReport{prereqs: len(check.prereqs), problems: slices.Clone(mk.refused)}
	p := parityPipelines{mk: mk, ciTargets: map[string]bool{}, prTargets: map[string]bool{}, otherTargets: map[string]bool{}}
	// What a push to main runs is what `make check` is held to; a step a
	// pull request alone runs, or whose failure passes, is not.
	for _, s := range ci.steps() {
		onPush := ci.onPush(s)
		for _, line := range workflowRunLines(s.Run) {
			r.ciLines++
			p.ciCommands = append(p.ciCommands, line)
			if onPush {
				p.pushCommands = append(p.pushCommands, line)
			}
			if how := gatekit.Swallowed(line); how != "" {
				r.fail("%s job %s runs `%s`, where %s, so a failing check passes", parityCI, s.job, line, how)
			}
			targets := parityMakeTargets(line)
			if len(targets) == 0 {
				r.fail("%s job %s runs %q, which is not a make target; every CI step is one, "+
					"so this gate can read what it runs", parityCI, s.job, line)
			}
			for _, t := range targets {
				p.ciTargets[t] = true
				if onPush {
					p.otherTargets[t] = true
				} else {
					p.prTargets[t] = true
				}
			}
		}
	}
	p.ciClosure = mk.closure(slices.Sorted(maps.Keys(p.otherTargets))...)
	p.checkClosure = mk.closure("check")
	p.checkLines = parityLines(mk, p.checkClosure, nil)
	p.ciLines = parityLines(mk, p.ciClosure, p.pushCommands)

	r.checkFailuresStop(mk, append(slices.Clone(p.checkClosure), mk.closure(slices.Sorted(maps.Keys(p.ciTargets))...)...))

	r.checkPrereqs(p, check.prereqs)
	r.checkCITargets(p)
	r.checkPrereqOnly(p, check.prereqs)
	r.checkArtifact(p, ci)
	r.checkGatesRun(root, p, flows, registry)
	r.checkVetTags(root, p, needs)
	r.checkFloors()
	return r, nil
}

// checkPrereqs holds that every check prerequisite is run by CI, by name
// or by recipe.
func (r *parityReport) checkPrereqs(p parityPipelines, prereqs []string) {
	for _, t := range prereqs {
		if _, defined := p.mk.targets[t]; !defined {
			r.fail("check: names %s, which the Makefile does not define", t)
			continue
		}
		if !slices.Contains(p.ciClosure, t) && !parityRecipeInCI(p.mk.recipe(t), p.pushCommands) {
			r.fail("`make check` runs %s and %s never does", t, parityCI)
		}
	}
}

// checkCITargets holds that every target CI runs is in check, or excused
// as pull-request-only with a reason, and that every excuse is still
// needed.
func (r *parityReport) checkCITargets(p parityPipelines) {
	for _, t := range slices.Sorted(maps.Keys(p.ciTargets)) {
		if _, defined := p.mk.targets[t]; !defined {
			r.fail("%s runs `make %s`, which the Makefile does not define", parityCI, t)
			continue
		}
		reason, excused := parityPROnly[t]
		inCheck := slices.Contains(p.checkClosure, t)
		switch {
		case inCheck && excused:
			r.fail("%s is excused as PR-only and `make check` runs it; drop the excuse", t)
		case inCheck:
		case !excused:
			r.fail("%s runs `make %s` and `make check` does not; add it to check or to parityPROnly with the reason", parityCI, t)
		case strings.TrimSpace(reason) == "":
			r.fail("%s is excused as PR-only with no reason", t)
		case p.otherTargets[t] || !p.prTargets[t]:
			r.fail("%s is excused as PR-only and %s runs it on a push to main", t, parityCI)
		}
	}
	for _, t := range slices.Sorted(maps.Keys(parityPROnly)) {
		if !p.ciTargets[t] {
			r.fail("%s is excused as PR-only and %s no longer runs it; drop the excuse", t, parityCI)
		}
	}
}

// checkPrereqOnly holds each prerequisite-only target to being exactly
// that.
func (r *parityReport) checkPrereqOnly(p parityPipelines, prereqs []string) {
	for _, t := range slices.Sorted(maps.Keys(parityPrereqOnly)) {
		switch {
		case strings.TrimSpace(parityPrereqOnly[t]) == "":
			r.fail("%s is excused as a prerequisite with no reason", t)
		case p.mk.targets[t] == nil:
			r.fail("%s is excused as a prerequisite and the Makefile does not define it; drop the excuse", t)
		case slices.Contains(prereqs, t):
			r.fail("%s is excused as a prerequisite and check: names it as a gate", t)
		case p.ciTargets[t]:
			r.fail("%s is excused as a prerequisite and %s runs it as a step", t, parityCI)
		case !slices.Contains(p.checkClosure, t) || !slices.Contains(p.ciClosure, t):
			r.fail("%s is excused as a prerequisite and is not one of anything both `make check` and %s run", t, parityCI)
		}
	}
}

// checkArtifact holds that the schema dump CI uploads is written by a
// target CI runs, so the upload step cannot find nothing.
func (r *parityReport) checkArtifact(p parityPipelines, ci workflow) {
	uploads := false
	for _, s := range ci.steps() {
		if s.action() != "actions/upload-artifact" {
			continue
		}
		if path, _ := s.input("path"); path == parityArtifact {
			uploads = true
		}
	}
	if !uploads {
		r.fail("%s does not upload %s for a reviewer", parityCI, parityArtifact)
		return
	}
	for _, t := range p.ciClosure {
		for _, line := range p.mk.recipe(t) {
			if strings.Contains(line, "> "+parityArtifact) {
				return
			}
		}
	}
	r.fail("%s uploads %s and no target it runs writes it", parityCI, parityArtifact)
}

// parityGateSites are the gates each pipeline calls.
type parityGateSites struct {
	check, ci, pr, release, manual, hook map[string]bool
}

// checkGatesRun holds that every gate runs where the registry says it
// does, and that every gate called anywhere is registered.
func (r *parityReport) checkGatesRun(root string, p parityPipelines, flows []workflow, registry map[string]command) {
	prClosure := p.mk.closure(slices.Sorted(maps.Keys(p.prTargets))...)
	allTargets := make([]string, 0, len(p.mk.targets))
	for name := range p.mk.targets {
		allTargets = append(allTargets, name)
	}
	slices.Sort(allTargets)
	hook, _ := os.ReadFile(filepath.Join(root, parityHook)) //nolint:gosec // a repository path
	sites := parityGateSites{
		check:   parityGatesIn(p.checkLines),
		ci:      parityGatesIn(p.ciLines),
		pr:      parityGatesIn(parityLines(p.mk, prClosure, nil)),
		release: parityGatesIn(strings.Split(parityReleaseText(root, flows), "\n")),
		manual:  parityGatesIn(parityLines(p.mk, allTargets, nil)),
		hook:    parityGatesIn(strings.Split(parityStripShellComments(string(hook)), "\n")),
	}
	called := map[string]bool{}
	for _, set := range []map[string]bool{sites.check, sites.ci, sites.release, sites.manual, sites.hook} {
		for name := range set {
			called[name] = true
		}
	}
	r.gateCalls = len(called)
	for _, name := range slices.Sorted(maps.Keys(called)) {
		if _, ok := registry[name]; !ok {
			r.fail("something runs `gates %s`, which the registry does not have", name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(registry)) {
		r.checkGate(name, registry[name], sites)
	}
	for _, name := range slices.Sorted(maps.Keys(parityReleaseOnly)) {
		if c, ok := registry[name]; !ok || c.mode != modeRelease {
			r.fail("parityReleaseOnly excuses %s, which is not a release command in the registry", name)
		}
	}
	if !sites.hook["precommit"] {
		r.fail("%s does not run `go run ./scripts/gates precommit`", parityHook)
	}
}

// checkGate holds one gate to the pipeline its registry entry declares.
func (r *parityReport) checkGate(name string, c command, sites parityGateSites) {
	if c.mode != modeCheck && strings.TrimSpace(c.reason) == "" {
		r.fail("the %s gate is not in check and gives no reason", name)
	}
	switch c.mode {
	case modeCheck:
		r.checkGates++
		if !sites.check[name] {
			r.fail("the %s gate runs in check by the registry, and no target `make check` runs calls it", name)
		}
		if !sites.ci[name] {
			r.fail("the %s gate runs in check by the registry, and %s never calls it", name, parityCI)
		}
	case modePR:
		if !sites.pr[name] {
			r.fail("the %s gate runs on pull requests by the registry, and %s's pr job never calls it", name, parityCI)
		}
		if sites.check[name] {
			r.fail("the %s gate is PR-only by the registry, and `make check` runs it; declare it modeCheck", name)
		}
	case modeRelease:
		if strings.TrimSpace(parityReleaseOnly[name]) == "" {
			r.fail("the %s gate runs in release and parityReleaseOnly gives no reason", name)
		}
		if !sites.release[name] {
			r.fail("the %s gate runs in release by the registry, and none of %s calls it",
				name, strings.Join(parityReleaseFiles, ", "))
		}
		if sites.check[name] {
			r.fail("the %s gate is release-only by the registry, and `make check` runs it", name)
		}
	case modeManual:
		if sites.check[name] {
			r.fail("the %s gate is manual by the registry, and `make check` runs it; declare it modeCheck", name)
		}
		if !sites.manual[name] && !sites.hook[name] {
			r.fail("the %s gate is manual by the registry, and no Makefile target or hook runs it: "+
				"a manual gate nobody can find is a gate that never fires", name)
		}
	default:
		r.fail("the %s gate declares no mode", name)
	}
}

// checkVetTags holds what each tagged file needs to a vet pass on both
// sides that compiles it, and every tag `make check` vets to the linter: a
// file behind build tags compiles only in a pass with them, and one vetted
// only locally compiles only on a maintainer's laptop.
func (r *parityReport) checkVetTags(root string, p parityPipelines, needs []parityTagNeed) {
	checkPasses := parityVetPasses(p.checkLines)
	ciPasses := parityVetPasses(p.ciLines)
	r.vetTags = len(needs)
	for _, need := range needs {
		if !need.vetted(checkPasses) {
			r.fail("%s builds only with %s (//go:build %s) and `make check` vets no pass that builds it; "+
				"a contributor cannot see it break", need.file, need, need.expr)
		}
		if !need.vetted(ciPasses) {
			r.fail("%s builds only with %s (//go:build %s) and %s vets no pass that builds it; "+
				"it compiles only on a maintainer's machine", need.file, need, need.expr, parityCI)
		}
	}
	lintTags, err := parityLintTags(root)
	if err != nil {
		r.fail("%s: %v", parityLint, err)
		return
	}
	checkTags := map[string]bool{}
	for _, pass := range checkPasses {
		for _, tag := range pass {
			checkTags[tag] = true
		}
	}
	for _, tag := range slices.Sorted(maps.Keys(checkTags)) {
		if !lintTags[tag] {
			r.fail("`make check` vets -tags=%s and %s run.build-tags does not lint it", tag, parityLint)
		}
	}
}

// checkFloors fails an empty comparison, which would otherwise be equal.
func (r *parityReport) checkFloors() {
	r.problems = append(r.problems, gatekit.Floor("check: prerequisites", r.prereqs, 25)...)
	r.problems = append(r.problems, gatekit.Floor(parityCI+" command lines", r.ciLines, 20)...)
	r.problems = append(r.problems, gatekit.Floor("gates in check", r.checkGates, 15)...)
	r.problems = append(r.problems, gatekit.Floor("distinct gate calls", r.gateCalls, 20)...)
	r.problems = append(r.problems, gatekit.Floor("//go:build lines that need -tags (live and evals)", r.vetTags, 2)...)
}

// parityMakeTargets are the targets a `make` command line names.
func parityMakeTargets(line string) []string {
	var out []string
	for _, words := range gatekit.Lists(line) {
		// ! inverts a command's status and is not the command.
		if words[0] == "!" {
			words = words[1:]
		}
		if len(words) == 0 || (words[0] != "make" && words[0] != "$(MAKE)") {
			continue
		}
		for _, f := range words[1:] {
			if strings.HasPrefix(f, "-") || strings.Contains(f, "=") {
				continue
			}
			out = append(out, f)
		}
	}
	return out
}

// checkFailuresStop holds that make stops on a failing command in every
// target check or CI runs: a - before a recipe line, a .IGNORE target and
// a command that swallows its own failure each let one pass
// (https://www.gnu.org/software/make/manual/html_node/Errors.html).
func (r *parityReport) checkFailuresStop(mk *makefile, targets []string) {
	if _, ok := mk.targets[".IGNORE"]; ok {
		r.fail("%s: .IGNORE makes make ignore a failing command; drop it", parityMakefile)
	}
	seen := map[string]bool{}
	for _, t := range targets {
		target, ok := mk.targets[t]
		if !ok || seen[t] {
			continue
		}
		seen[t] = true
		for i, prefix := range target.prefixes {
			if strings.Contains(prefix, "-") {
				r.fail("%s:%d: %s's recipe line starts with -, so make goes on when it fails",
					parityMakefile, target.lines[i], t)
			}
			if how := gatekit.Swallowed(mk.expand(target.recipe[i])); how != "" {
				r.fail("%s:%d: %s's recipe line is one where %s, so a failing check passes",
					parityMakefile, target.lines[i], t, how)
			}
		}
	}
}

// parityRecipeInCI reports whether every line of a recipe is a command
// CI runs verbatim.
func parityRecipeInCI(recipe, ciCommands []string) bool {
	if len(recipe) == 0 {
		return false
	}
	for _, line := range recipe {
		if !slices.Contains(ciCommands, line) {
			return false
		}
	}
	return true
}

// parityLines are the targets' recipe lines, raw and expanded, followed
// by the extra command lines. Raw, because $(GATES) is how the Makefile
// spells a gate call; expanded, because a variable can hide one.
func parityLines(mk *makefile, targets, extra []string) []string {
	var out []string
	for _, t := range targets {
		if target, ok := mk.targets[t]; ok {
			out = append(out, target.recipe...)
		}
		out = append(out, mk.recipe(t)...)
	}
	return append(out, extra...)
}

// parityGatesIn are the gates command lines call, through the built
// binary or `go run ./scripts/gates`, each line read as the shell splits
// it: a call quoted in an echo is a word of the echo, and flags before
// ./scripts/gates do not hide one.
func parityGatesIn(lines []string) map[string]bool {
	found := map[string]bool{}
	call := func(name string) {
		if parityGateName.MatchString(name) {
			found[name] = true
		}
	}
	for _, line := range lines {
		words := gatekit.ShellWords(line)
		for i := 0; i+1 < len(words); i++ {
			if parityGateBinary[words[i]] {
				call(words[i+1])
			}
		}
		for _, run := range gatekit.GoRunsIn(line) {
			if run.Pkg == "./scripts/gates" && len(run.Args) > 0 {
				call(run.Args[0])
			}
		}
	}
	return found
}

// parityVetPasses is the tags of each `go vet` in lines that sets any,
// from its -tags flag wherever it sits among the flags: -tags=a,b or
// -tags a,b, with one dash or two (https://pkg.go.dev/flag#hdr-Command_line_flag_syntax).
// Only `go vet` is a vet pass: `go test -vet=off` vets nothing.
func parityVetPasses(lines []string) [][]string {
	var passes [][]string
	for _, line := range lines {
		for _, c := range gatekit.GoCommands(line) {
			if c.Sub != "vet" {
				continue
			}
			// Only the flags before the packages set the build.
			flags := c.Args[:len(c.Args)-len(gatekit.GoAfterFlags(c.Args))]
			for i, arg := range flags {
				name, value, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
				if !strings.HasPrefix(arg, "-") || name != "tags" {
					continue
				}
				if !inline && i+1 < len(flags) {
					value = flags[i+1]
				}
				if value != "" {
					passes = append(passes, strings.Split(value, ","))
				}
			}
		}
	}
	return passes
}

// parityTagNeed is what a tagged file needs from a vet pass: its
// //go:build expression, the free tags in it, and the smallest -tags lists
// that build it, for the message. file is the first file with the line.
type parityTagNeed struct {
	file  string
	expr  constraint.Expr
	free  []string
	lists []string
}

func (n parityTagNeed) String() string { return "-tags=" + strings.Join(n.lists, " or -tags=") }

// vetted reports whether one of the passes compiles the file: its line
// holds with exactly the pass's tags, so `live && !evals` is not compiled
// by -tags=live,evals.
func (n parityTagNeed) vetted(passes [][]string) bool {
	return slices.ContainsFunc(passes, func(pass []string) bool { return parityBuildsWith(n.expr, n.free, pass) })
}

// parityReleaseText is the release pipeline's commands: the goreleaser
// config with its comments stripped, and every run line of the release
// workflows.
func parityReleaseText(root string, flows []workflow) string {
	var b strings.Builder
	for _, p := range parityReleaseFiles {
		if !strings.HasPrefix(p, workflowDir+"/") {
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p))) //nolint:gosec // a repository path
			if err == nil {
				b.WriteString(parityStripShellComments(string(data)))
			}
			continue
		}
		w, ok := workflowNamed(flows, filepath.Base(p))
		if !ok {
			continue
		}
		for _, s := range w.steps() {
			for _, line := range workflowRunLines(s.Run) {
				b.WriteString(line + "\n")
			}
		}
	}
	return b.String()
}

// parityStripShellComments drops every line that is a comment in a
// shell script or YAML file, so a gate named in a comment is not a call.
func parityStripShellComments(text string) string {
	var b strings.Builder
	for line := range strings.SplitSeq(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// parityTreeTags is what each //go:build line under root needs, once per
// expression and in path order: a file that builds with tags and not
// without them, which an untagged vet never compiles.
func parityTreeTags(root string) ([]parityTagNeed, error) {
	fset := token.NewFileSet()
	var needs []parityTagNeed
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "dist") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.PackageClauseOnly|parser.ParseComments)
		if err != nil {
			return err
		}
		need, ok := parityRequiredTags(f)
		if ok && !slices.ContainsFunc(needs, func(n parityTagNeed) bool { return n.expr.String() == need.expr.String() }) {
			rel, _ := filepath.Rel(root, path)
			need.file = filepath.ToSlash(rel)
			needs = append(needs, need)
		}
		return nil
	})
	return needs, err
}

// parityGoBuildExpr is a file's //go:build expression, or nil when it has
// none or the line does not parse
// (https://pkg.go.dev/cmd/go#hdr-Build_constraints).
func parityGoBuildExpr(f *ast.File) constraint.Expr {
	for _, group := range f.Comments {
		if group.Pos() >= f.Package {
			break
		}
		for _, c := range group.List {
			if !constraint.IsGoBuild(c.Text) {
				continue
			}
			expr, err := constraint.Parse(c.Text)
			if err != nil {
				return nil
			}
			return expr
		}
	}
	return nil
}

// parityRequiredTags is what a file's //go:build line needs from a vet
// pass, false when it builds with no custom tag. Its lists are each minimal
// set of custom tags it builds with, joined by commas. A tag the go
// command sets for a target or a toolchain is free to be either value, so
// `e2e && linux` needs e2e, `a && b` needs one pass that sets both,
// `a || b` a pass that sets either, and `!windows` none.
func parityRequiredTags(f *ast.File) (parityTagNeed, bool) {
	expr := parityGoBuildExpr(f)
	if expr == nil {
		return parityTagNeed{}, false
	}
	var free, custom []string
	// Eval calls its function for every tag, even past a decided result.
	expr.Eval(func(tag string) bool {
		switch {
		// ignore keeps a file out of every build, so no -tags can need it.
		case tag == "ignore" || slices.Contains(free, tag) || slices.Contains(custom, tag):
		case parityToolchainTag(tag):
			free = append(free, tag)
		default:
			custom = append(custom, tag)
		}
		return false
	})
	slices.Sort(custom)
	if parityBuildsWith(expr, free, nil) {
		return parityTagNeed{}, false
	}
	var found []int
	var lists []string
	for size := 1; size <= len(custom); size++ {
		for set := range 1 << len(custom) {
			// A set holding one that builds is not minimal.
			if bits.OnesCount(uint(set)) != size || slices.ContainsFunc(found, func(f int) bool { return set&f == f }) {
				continue
			}
			var with []string
			for i, tag := range custom {
				if set&(1<<i) != 0 {
					with = append(with, tag)
				}
			}
			if parityBuildsWith(expr, free, with) {
				found = append(found, set)
				lists = append(lists, strings.Join(with, ","))
			}
		}
	}
	return parityTagNeed{expr: expr, free: free, lists: lists}, lists != nil
}

// parityBuildsWith reports whether a file builds with the custom tags in
// with, and no other, for some value of each free tag.
func parityBuildsWith(expr constraint.Expr, free, with []string) bool {
	for set := range 1 << len(free) {
		if expr.Eval(func(tag string) bool {
			if i := slices.Index(free, tag); i >= 0 {
				return set&(1<<i) != 0
			}
			return slices.Contains(with, tag)
		}) {
			return true
		}
	}
	return false
}

// parityKnownOS and parityKnownArch are the GOOS and GOARCH values
// go/build knows, as https://go.dev/src/internal/syslist/syslist.go lists
// them.
var (
	parityKnownOS = map[string]bool{
		"aix": true, "android": true, "darwin": true, "dragonfly": true, "freebsd": true,
		"hurd": true, "illumos": true, "ios": true, "js": true, "linux": true, "nacl": true,
		"netbsd": true, "openbsd": true, "plan9": true, "solaris": true, "wasip1": true,
		"windows": true, "zos": true,
	}
	parityKnownArch = map[string]bool{
		"386": true, "amd64": true, "amd64p32": true, "arm": true, "armbe": true, "arm64": true,
		"arm64be": true, "loong64": true, "mips": true, "mipsle": true, "mips64": true,
		"mips64le": true, "mips64p32": true, "mips64p32le": true, "ppc": true, "ppc64": true,
		"ppc64le": true, "riscv": true, "riscv64": true, "s390": true, "s390x": true,
		"sparc": true, "sparc64": true, "wasm": true,
	}
	// parityOtherTags are set by the go command for a target and a
	// toolchain, and race, msan and asan by -race, -msan and -asan
	// (https://github.com/golang/go/blob/go1.27.3/src/cmd/go/internal/work/init.go#L212).
	parityOtherTags = map[string]bool{
		"unix": true, "cgo": true, "gc": true, "gccgo": true, "race": true, "msan": true, "asan": true,
	}
	// parityReleaseTag is a release tag, such as go1.22, set for each
	// release up to the toolchain's own.
	parityReleaseTag = regexp.MustCompile(`^go1\.\d+$`)
)

// parityToolchainTag reports whether a build tag is one no -tags flag
// needs to name: an OS, an architecture or one of its features, such as
// amd64.v2, or a tag the go command sets
// (https://pkg.go.dev/cmd/go#hdr-Build_constraints). A goexperiment tag
// is GOEXPERIMENT's to set (https://pkg.go.dev/internal/goexperiment).
func parityToolchainTag(tag string) bool {
	arch, _, _ := strings.Cut(tag, ".")
	return parityKnownOS[tag] || parityKnownArch[arch] || parityOtherTags[tag] || parityReleaseTag.MatchString(tag) ||
		strings.HasPrefix(tag, "goexperiment.")
}

// parityLintTags are .golangci.yml's run.build-tags.
func parityLintTags(root string) (map[string]bool, error) {
	data, err := os.ReadFile(filepath.Join(root, parityLint)) //nolint:gosec // a repository path
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Run struct {
			BuildTags []string `yaml:"build-tags"`
		} `yaml:"run"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, t := range cfg.Run.BuildTags {
		out[t] = true
	}
	return out, nil
}
