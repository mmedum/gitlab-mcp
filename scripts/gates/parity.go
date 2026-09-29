package main

import (
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mmedum/gitlab-mcp/v2/scripts/internal/gatekit"
)

// The parity gate holds `make check` and CI to the same set, and every
// gate in the registry to the pipeline its mode declares.
//
// The two lists live in two files and a person only ever edits one of
// them. So targets are mapped to CI steps by what they run, not by name:
// a CI step runs a target when it calls `make <target>`, or when its
// command is the target's recipe. A step's `name:` is not a command and
// is not read, and neither is a commented line. Makefile variables are
// expanded before anything is compared.

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

var (
	// parityGateCall is a call of this program, however it is spelled:
	// the built binary through $(GATES), or `go run`.
	parityGateCall = regexp.MustCompile(`(?:\$\(GATES\)|go run \./scripts/gates|\./\.gates(?:\$\(EXE\)|\.exe)?)\s+([a-z][a-z0-9-]*)(?:\s|$)`)
	parityVetTags  = regexp.MustCompile(`-tags[= ]([A-Za-z0-9_,]+)`)
	parityBuildTag = regexp.MustCompile(`^//go:build (.+)$`)
	parityTagWord  = regexp.MustCompile(`[A-Za-z0-9_.]+`)
)

func parity(out io.Writer, _ []string) error {
	r, err := parityCheck(".", commands)
	if err != nil {
		return err
	}
	if err := gatekit.Problems(out, "make/CI parity", r.problems); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "parity ok: %d check prerequisites, %d CI command lines, %d gates in check, "+
		"%d gate calls, %d vet tags, %d PR-only, %d prerequisite-only and %d release-only entries excused\n",
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
	mk                      *makefile
	ciTargets               map[string]bool
	ciCommands              []string
	ciClosure, checkClosure []string
	prTargets, otherTargets map[string]bool
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

	r := &parityReport{prereqs: len(check.prereqs)}
	p := parityPipelines{mk: mk, ciTargets: map[string]bool{}, prTargets: map[string]bool{}, otherTargets: map[string]bool{}}
	for _, s := range ci.steps() {
		for _, line := range workflowRunLines(s.Run) {
			r.ciLines++
			p.ciCommands = append(p.ciCommands, line)
			targets := parityMakeTargets(line)
			if len(targets) == 0 {
				r.fail("%s job %s runs %q, which is not a make target; every CI step is one, "+
					"so this gate can read what it runs", parityCI, s.job, line)
			}
			for _, t := range targets {
				p.ciTargets[t] = true
				if s.job == "pr" {
					p.prTargets[t] = true
				} else {
					p.otherTargets[t] = true
				}
			}
		}
	}
	p.ciClosure = mk.closure(slices.Sorted(maps.Keys(p.ciTargets))...)
	p.checkClosure = mk.closure("check")

	r.checkPrereqs(p, check.prereqs)
	r.checkCITargets(p)
	r.checkPrereqOnly(p, check.prereqs)
	r.checkArtifact(p, ci)
	r.checkGatesRun(root, p, flows, registry)
	r.checkVetTags(root, p)
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
		if !slices.Contains(p.ciClosure, t) && !parityRecipeInCI(p.mk.recipe(t), p.ciCommands) {
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
			r.fail("%s is excused as PR-only and %s runs it outside the pr job", t, parityCI)
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
		check:   parityGatesIn(p.mk, p.checkClosure, nil),
		ci:      parityGatesIn(p.mk, p.ciClosure, p.ciCommands),
		pr:      parityGatesIn(p.mk, prClosure, nil),
		release: parityGatesInText(parityReleaseText(root, flows)),
		manual:  parityGatesIn(p.mk, allTargets, nil),
		hook:    parityGatesInText(parityStripShellComments(string(hook))),
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

// checkVetTags holds every vet build tag both ways, and every tag the
// code is built with to one that is vetted: a tag vetted only locally
// leaves tagged code compiling only on a maintainer's laptop.
func (r *parityReport) checkVetTags(root string, p parityPipelines) {
	checkTags := parityVetTagsIn(p.mk, p.checkClosure, nil)
	ciTags := parityVetTagsIn(p.mk, p.ciClosure, p.ciCommands)
	r.vetTags = len(checkTags)
	for _, tag := range slices.Sorted(maps.Keys(checkTags)) {
		if !ciTags[tag] {
			r.fail("`make check` vets with -tags=%s and %s does not", tag, parityCI)
		}
	}
	for _, tag := range slices.Sorted(maps.Keys(ciTags)) {
		if !checkTags[tag] {
			r.fail("%s vets with -tags=%s and `make check` does not", parityCI, tag)
		}
	}
	used, err := parityCodeTags(root)
	if err != nil {
		r.fail("reading the build tags the code uses: %v", err)
	}
	for _, tag := range slices.Sorted(maps.Keys(used)) {
		if !checkTags[tag] {
			r.fail("%s is built with -tags=%s and nothing vets it", used[tag], tag)
		}
	}
	lintTags, err := parityLintTags(root)
	if err != nil {
		r.fail("%s: %v", parityLint, err)
		return
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
	r.problems = append(r.problems, gatekit.Floor("vetted build tags (live and evals)", r.vetTags, 2)...)
}

// parityMakeTargets are the targets a `make` command line names.
func parityMakeTargets(line string) []string {
	fields := strings.Fields(line)
	if len(fields) == 0 || (fields[0] != "make" && fields[0] != "$(MAKE)") {
		return nil
	}
	var out []string
	for _, f := range fields[1:] {
		if strings.HasPrefix(f, "-") || strings.Contains(f, "=") {
			continue
		}
		out = append(out, f)
	}
	return out
}

// parityRecipeInCI reports whether every line of a recipe is a command
// CI runs verbatim.
func parityRecipeInCI(recipe, ciCommands []string) bool {
	if len(recipe) == 0 {
		return false
	}
	for _, line := range recipe {
		if !slices.Contains(ciCommands, strings.TrimLeft(line, "@-")) {
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

// parityGatesIn are the gates the targets' recipes and the extra
// command lines call.
func parityGatesIn(mk *makefile, targets, extra []string) map[string]bool {
	return parityGatesInText(strings.Join(parityLines(mk, targets, extra), "\n"))
}

func parityGatesInText(text string) map[string]bool {
	found := map[string]bool{}
	for _, m := range parityGateCall.FindAllStringSubmatch(text, -1) {
		found[m[1]] = true
	}
	return found
}

// parityVetTagsIn are the build tags `go vet` runs with.
func parityVetTagsIn(mk *makefile, targets, extra []string) map[string]bool {
	found := map[string]bool{}
	for _, line := range parityLines(mk, targets, extra) {
		if !strings.Contains(line, " vet") {
			continue
		}
		for _, m := range parityVetTags.FindAllStringSubmatch(line, -1) {
			for tag := range strings.SplitSeq(m[1], ",") {
				found[tag] = true
			}
		}
	}
	return found
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

// parityNotTags are the words a //go:build line uses that are not
// custom tags: operating systems, architectures, toolchain names and
// "ignore", which marks a file nothing builds.
var parityNotTags = map[string]bool{
	"ignore": true, "unix": true, "windows": true, "linux": true, "darwin": true, "freebsd": true,
	"openbsd": true, "netbsd": true, "plan9": true, "js": true, "wasip1": true, "android": true, "ios": true,
	"amd64": true, "arm64": true, "386": true, "arm": true, "wasm": true, "gc": true, "gccgo": true, "cgo": true,
	"race": true,
}

// parityCodeTags are the custom build tags any Go file under root is
// constrained by, each with one file that uses it.
func parityCodeTags(root string) (map[string]string, error) {
	found := map[string]string{}
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
		data, err := os.ReadFile(path) //nolint:gosec // walking the repository
		if err != nil {
			return err
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "package ") {
				break
			}
			m := parityBuildTag.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			for _, word := range parityTagWord.FindAllString(m[1], -1) {
				if parityNotTags[word] || strings.HasPrefix(word, "go1") {
					continue
				}
				if _, seen := found[word]; !seen {
					rel, _ := filepath.Rel(root, path)
					found[word] = filepath.ToSlash(rel)
				}
			}
		}
		return nil
	})
	return found, err
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
