package main

import (
	"errors"
	"fmt"
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

// The pins gate holds every third-party tool to one exact version, the
// same version everywhere it is named.
//
// The ways a version floats, each a rule:
//   - an action used by tag or branch rather than a 40-hex commit SHA,
//     or a SHA with no comment saying which version it is, or a comment
//     naming a CodeQL bundle rather than the action's release;
//   - an action that installs a tool without naming the tool's version:
//     a SHA pins the wrapper, not the tool;
//   - a version input, env value or `go run module@v` that is a range, a
//     major line or `latest`;
//   - one tool pinned to two versions in two files, so a rehearsal or a
//     pre-commit scan runs something other than CI;
//   - a Makefile pin no recipe uses, which is a version nothing holds.
//
// Every action is classified as an installer or not, and an unknown one
// fails: being unclassified is how an unpinned tool slips through. And
// every workflow pins its shell at the workflow level: on Windows the
// default is PowerShell, which reads -coverprofile=cov.out as a file
// called `cov`.
//
// The §5a pin table is not compared: it records what was checked on a
// date, and a gate holding code to a copy in prose is machinery to
// maintain a duplicate.

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
	// fromGoMod reads the version from go.mod: "go" or a module path.
	fromGoMod string
}

var pinsTools = []pinsTool{
	{name: "go", fromGoMod: "go"},
	{name: "mcp-go-sdk", fromGoMod: "github.com/modelcontextprotocol/go-sdk"},
	{name: "golangci-lint", modules: []string{"github.com/golangci/golangci-lint"},
		actions: map[string]string{"golangci/golangci-lint-action": "version"}},
	{name: "goreleaser", modules: []string{"github.com/goreleaser/goreleaser"}, envKeys: []string{"GORELEASER_VERSION"},
		actions: map[string]string{"goreleaser/goreleaser-action": "version"}},
	{name: "cosign", actions: map[string]string{"sigstore/cosign-installer": "cosign-release"}},
	{name: "syft", actions: map[string]string{
		"anchore/sbom-action/download-syft": "syft-version", "anchore/sbom-action": "syft-version"}},
	{name: "mcp-publisher", envKeys: []string{"PUBLISHER_VERSION"}},
	{name: "gitleaks", modules: []string{"github.com/zricethezav/gitleaks", "github.com/gitleaks/gitleaks"},
		envKeys: []string{"GITLEAKS_VERSION"}, actions: map[string]string{"gitleaks/gitleaks-action": "version"}},
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
	"actions/setup-go":                "installs Go from go-version-file, which is go.mod: the pin is the file",
	"github/codeql-action/init":       "CodeQL's query bundle is GitHub's to manage; the action's own version is held by comment",
	"github/codeql-action/autobuild":  "builds with the Go already installed",
	"github/codeql-action/analyze":    "uploads results",
}

var (
	pinsUsesLine  = regexp.MustCompile(`^\s*-?\s*uses:\s*([^\s#]+)\s*(#.*)?$`)
	pinsSHA       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	pinsExact     = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)
	pinsModuleAt  = regexp.MustCompile(`([A-Za-z0-9_.\-]+(?:/[A-Za-z0-9_.\-]+)+)@([^\s"'` + "`" + `)]+)`)
	pinsSemverIn  = regexp.MustCompile(`v?\d+\.\d+\.\d+`)
	pinsVersionEn = regexp.MustCompile(`_VERSION$`)
	pinsVarRef    = regexp.MustCompile(`\$\(([A-Za-z_][A-Za-z0-9_]*)\)`)
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
}

func (r *pinsReport) fail(format string, a ...any) {
	r.problems = append(r.problems, fmt.Sprintf(format, a...))
}

// add records one pin, refusing anything but one exact version.
func (r *pinsReport) add(tool, version, at string) {
	r.tools[tool] = append(r.tools[tool], pinsFound{version, at})
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
	gomod, err := os.ReadFile(filepath.Join(root, pinsGoMod)) //nolint:gosec // a repository path
	if err != nil {
		return nil, err
	}

	r := &pinsReport{tools: map[string][]pinsFound{}, workflows: len(flows)}
	for _, base := range pinsRequiredWorkflows {
		if _, ok := workflowNamed(flows, base); !ok {
			r.fail("%s/%s does not exist", workflowDir, base)
		}
	}
	for _, w := range flows {
		pinsWorkflow(w, r)
	}
	pinsMakefileVars(mk, r)
	pinsGoModVersions(string(gomod), r)
	if err := pinsGoSource(root, r); err != nil {
		return nil, err
	}

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
	r.problems = append(r.problems, gatekit.Floor("tool-installing steps (goreleaser, cosign twice, syft)", r.installers, 4)...)
	r.problems = append(r.problems, gatekit.Floor("pinned versions", r.places, 12)...)
	return r, nil
}

// pinsWorkflow applies the workflow rules to one file.
func pinsWorkflow(w workflow, r *pinsReport) {
	if strings.TrimSpace(w.Defaults.Run.Shell) == "" {
		r.fail("%s: no workflow-level `defaults: run: shell:`. Every run step goes to a shell, "+
			"so a job-level block only fixes the jobs that exist today", w.path)
	}
	pinsUsesLines(w, r)

	envs := []map[string]string{w.Env}
	for _, name := range slices.Sorted(maps.Keys(w.Jobs)) {
		envs = append(envs, w.Jobs[name].Env)
	}
	for _, s := range w.steps() {
		envs = append(envs, s.Env)
		if s.Uses == "" || strings.HasPrefix(s.Uses, "./") {
			pinsRunLines(s.Run, w.path, r)
			continue
		}
		pinsStep(w, s, r)
	}
	for _, env := range envs {
		pinsEnv(w.path, env, r)
	}
}

// pinsUsesLines reads the literal `uses:` lines, for the SHA and its
// comment.
func pinsUsesLines(w workflow, r *pinsReport) {
	for i, line := range strings.Split(w.text, "\n") {
		at := fmt.Sprintf("%s:%d", w.path, i+1)
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(trimmed, "@latest") {
			r.fail("%s: something is pinned to @latest", at)
		}
		m := pinsUsesLine.FindStringSubmatch(line)
		if m == nil || strings.HasPrefix(m[1], "./") {
			continue
		}
		r.actions++
		action, ref, ok := strings.Cut(m[1], "@")
		comment := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(m[2]), "#"))
		switch {
		case !ok:
			r.fail("%s: %s has no ref at all", at, action)
		case !pinsSHA.MatchString(ref):
			r.fail("%s: %s is pinned to %q, a tag or branch that can be moved; pin the 40-character commit SHA",
				at, action, ref)
		case comment == "":
			r.fail("%s: %s is pinned by SHA with no comment saying which version it is", at, action)
		case strings.Contains(comment, "bundle"):
			r.fail("%s: %s is labeled %q, a query-bundle tag; label it with the action's own release tag",
				at, action, comment)
		case !pinsExact.MatchString(strings.Fields(comment)[0]):
			r.fail("%s: %s is labeled %q; the comment starts with the exact version the SHA is", at, action, comment)
		}
		for _, t := range pinsTools {
			for _, prefix := range t.commentOf {
				if strings.HasPrefix(action, prefix) {
					r.add(t.name, pinsSemverIn.FindString(comment), at)
				}
			}
		}
	}
}

// pinsStep checks one step that uses an action: an installer sets the
// input that pins its tool, any other action is classified, and every
// version input names one exact version.
func pinsStep(w workflow, s workflowStep, r *pinsReport) {
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
		v, set := s.input(key)
		if !set {
			r.fail("%s is pinned by SHA and does not set %s, so the tool it installs is whatever "+
				"is current that morning", at, key)
			continue
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

// pinsRunLines reads `go run module@version` out of a run block.
func pinsRunLines(run, path string, r *pinsReport) {
	for _, line := range workflowRunLines(run) {
		if !strings.Contains(line, "go run") && !strings.Contains(line, "go install") {
			continue
		}
		for _, m := range pinsModuleAt.FindAllStringSubmatch(line, -1) {
			if tool := pinsToolByModule(m[1]); tool != "" {
				r.add(tool, m[2], path+": go run "+m[1])
			} else {
				r.fail("%s: go run %s@%s names a tool pinsTools does not know", path, m[1], m[2])
			}
		}
	}
}

// pinsMakefileVars reads every `module@version` variable, and refuses
// one no recipe uses: a pin nothing runs is a version nothing holds.
func pinsMakefileVars(mk *makefile, r *pinsReport) {
	used := map[string]bool{}
	for _, t := range mk.targets {
		for _, line := range t.recipe {
			for _, m := range pinsVarRef.FindAllStringSubmatch(line, -1) {
				used[m[1]] = true
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(mk.vars)) {
		value := mk.vars[name]
		if strings.Contains(value, "@latest") {
			r.fail("%s: %s is pinned to @latest", pinsMakefile, name)
			continue
		}
		m := pinsModuleAt.FindStringSubmatch(value)
		if m == nil {
			continue
		}
		tool := pinsToolByModule(m[1])
		if tool == "" {
			r.fail("%s: %s names %s, which pinsTools does not know", pinsMakefile, name, m[1])
			continue
		}
		if !used[name] {
			r.fail("%s: %s pins %s and no recipe runs $(%s)", pinsMakefile, name, tool, name)
		}
		r.add(tool, m[2], pinsMakefile+": "+name)
	}
}

// pinsGoModVersions reads the Go directive and required modules.
func pinsGoModVersions(gomod string, r *pinsReport) {
	for line := range strings.SplitSeq(gomod, "\n") {
		fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "require")))
		if len(fields) < 2 {
			continue
		}
		for _, t := range pinsTools {
			if t.fromGoMod != "" && fields[0] == t.fromGoMod {
				r.add(t.name, fields[1], pinsGoMod+": "+t.fromGoMod)
			}
		}
	}
}

// pinsGoSource reads every `module@version` of a known tool out of the
// maintainer tooling's Go source, so a copy of a pin in a gate is held
// to the Makefile's like any other.
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
		data, err := os.ReadFile(path) //nolint:gosec // walking the repository
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, m := range pinsModuleAt.FindAllStringSubmatch(string(data), -1) {
			if tool := pinsToolByModule(m[1]); tool != "" {
				r.add(tool, m[2], filepath.ToSlash(rel)+": "+m[1])
			}
		}
		return nil
	})
}

func pinsToolByModule(module string) string {
	for _, t := range pinsTools {
		for _, m := range t.modules {
			if strings.HasPrefix(module, m) {
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
