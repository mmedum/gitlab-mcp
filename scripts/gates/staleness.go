package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/mmedum/gitlab-mcp/v2/internal/config"
	"github.com/mmedum/gitlab-mcp/v2/internal/scopes"
	"github.com/mmedum/gitlab-mcp/v2/scripts/internal/gatekit"
	"github.com/mmedum/gitlab-mcp/v2/scripts/internal/gitx"
	"github.com/mmedum/gitlab-mcp/v2/scripts/internal/mcpstdio"
)

// staleness holds the documents to what the code defines. Each rule
// derives its expected set from the code, the registry or the built
// binary, and asserts a floor on how much it read: a rule that found
// nothing and a rule that read nothing print the same thing otherwise.
func staleness(out io.Writer, args []string) error {
	in, err := stalenessGather(".", args)
	if err != nil {
		return err
	}
	r := stalenessCheck(in)
	for _, line := range r.read {
		_, _ = fmt.Fprintln(out, "  read: "+line)
	}
	if err := gatekit.Problems(out, "the documents", r.problems); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "staleness ok: %d rules\n", len(r.read))
	return nil
}

// Documents the rules read by name.
const (
	stalenessArch      = "docs/architecture.md"
	stalenessConfigDoc = "docs/configuration.md"
	stalenessDevDoc    = "docs/development.md"
	stalenessSetupDoc  = "docs/setup.md"
)

// stalenessInputs is everything the rules read, gathered once so the
// rules are pure functions a test can feed.
type stalenessInputs struct {
	docs     map[string]string // repository path → text
	exists   func(rel string) bool
	topLevel map[string]bool // top-level names git would commit
	// packages are the module's Go package directories, slash-separated
	// and relative to the root; hasGo says which directories hold Go.
	packages []string
	hasGo    func(rel string) bool
	// configVars are the variables internal/config says a person
	// running the server may set; devVars are its development
	// overrides.
	configVars, devVars []string
	// modes are the scope modes, each with the setup block
	// internal/scopes generates for it.
	modes []stalenessMode
	// commands are the gate registry's names.
	commands []string
	// newestTag is the newest semver tag, "" for none; changedSince is
	// the shipped Go changed since it.
	newestTag    string
	changedSince []string
	// built is whether any non-test Go file exists under internal/.
	built bool
	// tools are the built binary's tools with every flag and toolset
	// on; toolsErr says why they could not be read.
	tools    []string
	toolsErr error
}

// stalenessMode is one scope mode and the exact setup text for it.
type stalenessMode struct {
	Name  string
	Block string
}

type stalenessReport struct {
	read     []string
	problems []string
}

func (r *stalenessReport) add(read string, problems []string) {
	r.read = append(r.read, read)
	r.problems = append(r.problems, problems...)
}

func stalenessCheck(in stalenessInputs) stalenessReport {
	var r stalenessReport
	arch := in.docs[stalenessArch]

	n, p := stalenessPackageMap(in.docs["CLAUDE.md"], in.packages, in.exists, in.hasGo)
	r.add(fmt.Sprintf("package map: %d listed paths, %d packages", n, len(in.packages)), p)

	n, p = stalenessPaths(in.docs, in.exists, in.topLevel)
	r.add(fmt.Sprintf("paths: %d repository paths named", n), p)

	n, p = stalenessEnv(in.docs[stalenessConfigDoc], in.docs[stalenessDevDoc], in.configVars, in.devVars)
	r.add(fmt.Sprintf("settings: %d variables", n), p)

	n, p = stalenessGates(in.docs[stalenessDevDoc], in.commands)
	r.add(fmt.Sprintf("gates: %d commands", n), p)

	r.add("status line", stalenessStatus(arch, in.docs["CHANGELOG.md#all"], in.newestTag, in.built))

	r.add(fmt.Sprintf("changes since %s", cmp.Or(in.newestTag, "the first tag")),
		stalenessChangedSinceTag(in.docs["CHANGELOG.md"], in.docs["CHANGELOG.md#all"], in.newestTag, in.changedSince))

	n, p = stalenessNoVersionInProse(in.docs)
	r.add(fmt.Sprintf("version in prose: %d documents", n), p)

	n, p = stalenessToolCounts(arch, in.tools, in.toolsErr)
	r.add(fmt.Sprintf("§8 tool counts: %d table rows", n), p)

	n, p = stalenessReadmeTools(in.docs["README.md"], in.tools)
	r.add(fmt.Sprintf("README tool table: %d rows", n), p)

	n, p = stalenessProseCounts(in.docs)
	r.add(fmt.Sprintf("prose counts: %d numbers held against the lists beside them", n), p)

	n, p = stalenessSetupBlocks(in.docs[stalenessSetupDoc], in.modes)
	r.add(fmt.Sprintf("setup blocks: %d modes", n), p)
	return r
}

// stalenessDocs are the documents read besides docs/*.md. CHANGELOG.md
// is reduced to its [Unreleased] section for every rule but the status
// line: older entries are history, correctly describing what was true.
var stalenessDocs = []string{"README.md", "CLAUDE.md", "CONTRIBUTING.md", "SECURITY.md", "CHANGELOG.md"}

func stalenessGather(root string, args []string) (stalenessInputs, error) {
	var in stalenessInputs
	in.docs = map[string]string{}
	names := slices.Clone(stalenessDocs)
	mds, _ := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	for _, m := range mds {
		rel, _ := filepath.Rel(root, m)
		names = append(names, filepath.ToSlash(rel))
	}
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name))) //nolint:gosec // repository paths
		if err != nil {
			continue // a missing document fails the rule that needs it
		}
		in.docs[name] = string(b)
	}
	if c, ok := in.docs["CHANGELOG.md"]; ok {
		in.docs["CHANGELOG.md"] = stalenessUnreleased(c)
		in.docs["CHANGELOG.md#all"] = c
	}
	in.exists = func(rel string) bool {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		return err == nil
	}
	in.hasGo = func(rel string) bool {
		m, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(rel), "*.go"))
		return len(m) > 0
	}
	files, err := gitx.Files(root)
	if err != nil {
		return in, err
	}
	in.topLevel = map[string]bool{}
	for _, f := range files {
		first, _, _ := strings.Cut(f, "/")
		in.topLevel[first] = true
	}
	in.packages, err = stalenessGoList(root)
	if err != nil {
		return in, err
	}
	in.built = stalenessBuilt(root)
	in.configVars = config.EnvVars()
	in.devVars = config.DevVars()
	for _, m := range scopes.Modes() {
		in.modes = append(in.modes, stalenessMode{Name: string(m.Mode), Block: scopes.SetupBlock(m.Mode)})
	}
	in.commands = slices.Sorted(maps.Keys(commands))
	in.newestTag = stalenessNewestTag(root)
	if in.newestTag != "" {
		changed, err := gitx.ChangedFiles(root, "v"+in.newestTag, "HEAD")
		if err != nil {
			return in, err
		}
		in.changedSince = stalenessShipped(changed)
	}
	in.tools, in.toolsErr = stalenessTools(args)
	return in, nil
}

// stalenessTools asks the built binary for its whole surface. The
// binary is the one `make staleness` builds; without one the rules that
// need it fail rather than pass on nothing.
func stalenessTools(args []string) ([]string, error) {
	if len(args) == 0 || args[0] == "" {
		return nil, errors.New("no binary given; `make staleness` passes the one it built")
	}
	cmd := exec.Command(args[0], "--dump-schemas") //nolint:gosec // the binary the Makefile built
	cmd.Env = mcpstdio.Environ(os.Environ())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s --dump-schemas: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stalenessDumpTools(raw)
}

// stalenessDumpTools reads the tool names out of a schema dump.
func stalenessDumpTools(raw []byte) ([]string, error) {
	var d struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("the schema dump is not JSON: %w", err)
	}
	var names []string
	for _, t := range d.Tools {
		names = append(names, t.Name)
	}
	if len(names) == 0 {
		return nil, errors.New("the schema dump lists no tools")
	}
	slices.Sort(names)
	return names, nil
}

// stalenessGoList is every package in the module, tagged ones included,
// as slash-separated directories relative to root.
func stalenessGoList(root string) ([]string, error) {
	cmd := exec.Command("go", "list", "-tags", "live,evals", "-e", "-f", "{{.Dir}}", "./...")
	cmd.Dir = root
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w", err)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for line := range strings.Lines(string(b)) {
		dir := strings.TrimSpace(line)
		if dir == "" {
			continue
		}
		rel, err := filepath.Rel(abs, dir)
		if err != nil {
			return nil, err
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out, nil
}

// stalenessBuilt reports whether internal/ holds any non-test Go.
func stalenessBuilt(root string) bool {
	built := false
	_ = filepath.WalkDir(filepath.Join(root, "internal"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || built {
			return fs.SkipAll
		}
		if !d.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			built = true
		}
		return nil
	})
	return built
}

// stalenessShipped are the changed files that change what a user runs.
func stalenessShipped(files []string) []string {
	var out []string
	for _, f := range files {
		if f == "go.mod" || (strings.HasSuffix(f, ".go") && !strings.HasSuffix(f, "_test.go") &&
			(strings.HasPrefix(f, "cmd/") || strings.HasPrefix(f, "internal/")) && !strings.Contains(f, "/testdata/")) {
			out = append(out, f)
		}
	}
	return out
}

var stalenessSemver = regexp.MustCompile(`^v(\d+\.\d+\.\d+)$`)

func stalenessNewestTag(root string) string {
	tags, err := gitx.Output(root, "tag", "--sort=-v:refname")
	if err != nil {
		return ""
	}
	for line := range strings.Lines(tags) {
		if m := stalenessSemver.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			return m[1]
		}
	}
	return ""
}

func stalenessUnreleased(changelog string) string {
	s, _ := gatekit.MarkdownSection(changelog, "## [Unreleased]")
	// The link references sit under no heading.
	if i := strings.Index(s, "\n["); i >= 0 {
		s = s[:i]
	}
	return s
}

var stalenessBacktickDir = regexp.MustCompile("`([A-Za-z0-9_./-]+/)`")

// stalenessPackageMap holds CLAUDE.md "Where things go" against the
// module's packages, both ways. A bare `name/` in a bullet is relative
// to the path before it in the same bullet. A listed directory holding
// no Go of its own covers the packages beneath it.
func stalenessPackageMap(claude string, packages []string, exists, hasGo func(string) bool) (int, []string) {
	section, ok := gatekit.MarkdownSection(claude, "## Where things go")
	if !ok {
		return 0, []string{`CLAUDE.md has no "## Where things go" section`}
	}
	var listed []string
	for bullet := range strings.SplitSeq(section, "\n- ") {
		prev := ""
		for _, m := range stalenessBacktickDir.FindAllStringSubmatch(bullet, -1) {
			p := m[1]
			if !strings.Contains(strings.TrimSuffix(p, "/"), "/") && prev != "" && !exists(strings.TrimSuffix(p, "/")) {
				p = prev + p
			}
			listed = append(listed, strings.TrimSuffix(p, "/"))
			prev = p
		}
	}
	var problems []string
	problems = append(problems, gatekit.Floor(`paths in CLAUDE.md "Where things go"`, len(listed), 20)...)
	problems = append(problems, gatekit.Floor("packages from go list", len(packages), 10)...)
	for _, l := range listed {
		if !exists(l) {
			problems = append(problems, "CLAUDE.md \"Where things go\" lists "+l+"/, which does not exist")
		}
	}
	for _, pkg := range packages {
		covered := slices.Contains(listed, pkg)
		for _, l := range listed {
			if !covered && strings.HasPrefix(pkg, l+"/") && !hasGo(l) {
				covered = true
			}
		}
		if !covered {
			problems = append(problems, "CLAUDE.md \"Where things go\" does not name the package "+pkg+"/")
		}
	}
	return len(listed), problems
}

var (
	stalenessCodeSpan = regexp.MustCompile("`([^`\n]+)`")
	stalenessMDLink   = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	stalenessFence    = regexp.MustCompile("(?ms)^\\s*```.*?^\\s*```")
)

// stalenessPaths stats every repository path the documents name, in a
// code span or a relative link. A span's first segment must be a name
// git would commit, which is what keeps `users/me` and `api/v4` from
// reading as paths. Bare file names are checked for .md and .yml only:
// a document and a workflow, the two a sentence names without a
// directory. A bare .yaml is usually somebody else's file.
func stalenessPaths(docs map[string]string, exists func(string) bool, topLevel map[string]bool) (int, []string) {
	var problems []string
	examined := 0
	for _, doc := range slices.Sorted(maps.Keys(docs)) {
		if strings.Contains(doc, "#") {
			continue
		}
		text := stalenessFence.ReplaceAllString(docs[doc], "")
		dir := path.Dir(doc)
		for _, m := range stalenessMDLink.FindAllStringSubmatch(text, -1) {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "#") {
				continue
			}
			target, _, _ = strings.Cut(target, "#")
			examined++
			if !exists(path.Clean(path.Join(dir, target))) {
				problems = append(problems, fmt.Sprintf("%s links to %s, which does not exist", doc, target))
			}
		}
		for _, m := range stalenessCodeSpan.FindAllStringSubmatch(text, -1) {
			p, ok := stalenessPathLike(m[1])
			if !ok {
				continue
			}
			var candidates []string
			if strings.Contains(p, "/") {
				first, _, _ := strings.Cut(p, "/")
				if !topLevel[first] {
					continue
				}
				candidates = []string{p, path.Join(dir, p)}
			} else {
				ext := path.Ext(p)
				if ext != ".md" && ext != ".yml" {
					continue
				}
				candidates = []string{p, path.Join(dir, p), ".github/workflows/" + p, ".github/" + p,
					".github/ISSUE_TEMPLATE/" + p}
			}
			examined++
			if !slices.ContainsFunc(candidates, exists) {
				problems = append(problems, fmt.Sprintf("%s names %s, which does not exist", doc, p))
			}
		}
	}
	problems = append(problems, gatekit.Floor("repository paths named in the documents", examined, 30)...)
	return examined, problems
}

// stalenessPathLike cleans a code span into a candidate path.
func stalenessPathLike(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " ~$*{}<>@=:…,()[]'\"|") || strings.Contains(s, "...") ||
		strings.HasPrefix(s, "-") || strings.HasPrefix(s, "/") || strings.HasPrefix(s, ".") && !strings.HasPrefix(s, ".g") {
		return "", false
	}
	s, _, _ = strings.Cut(s, "#")
	s = strings.TrimSuffix(s, "/")
	return s, s != ""
}

var stalenessEnvRef = regexp.MustCompile(`\b` + config.EnvPrefix + `[A-Z0-9_]*[A-Z0-9]\b`)

// stalenessEnv holds docs/configuration.md to the variables
// internal/config says the server reads, both ways, and the development
// overrides to docs/development.md alone: a person configuring the
// server is never offered one.
func stalenessEnv(doc, devDoc string, vars, devVars []string) (int, []string) {
	if doc == "" {
		return 0, []string{stalenessConfigDoc + " is missing, and it is where every setting is documented"}
	}
	problems := gatekit.Floor("variables in internal/config.Vars", len(vars), 10)
	for _, v := range devVars {
		if !slices.Contains(stalenessEnvRef.FindAllString(devDoc, -1), v) {
			problems = append(problems, stalenessDevDoc+" does not document the development override "+v)
		}
	}
	documented := map[string]bool{}
	for _, v := range stalenessEnvRef.FindAllString(doc, -1) {
		documented[v] = true
	}
	for _, v := range vars {
		if !documented[v] {
			problems = append(problems, stalenessConfigDoc+" does not document "+v)
		}
	}
	for _, v := range slices.Sorted(maps.Keys(documented)) {
		switch {
		case slices.Contains(devVars, v):
			problems = append(problems, stalenessConfigDoc+" documents "+v+", a development override that belongs in "+stalenessDevDoc+" only")
		case !slices.Contains(vars, v):
			problems = append(problems, stalenessConfigDoc+" documents "+v+", which the server does not read")
		}
	}
	return len(vars) + len(devVars), problems
}

var stalenessGateRef = regexp.MustCompile("`(?:go run \\./scripts/)?gates ([a-z][a-z0-9-]*)")

// stalenessGates holds docs/development.md to the gate registry, both
// ways: every command named, in a code span or as `gates <name>`, and
// every `gates <name>` it shows a command the registry has.
func stalenessGates(doc string, names []string) (int, []string) {
	if doc == "" {
		return len(names), []string{stalenessDevDoc + " is missing, and it is where each gate is documented"}
	}
	problems := gatekit.Floor("gates in the registry", len(names), 20)
	for _, name := range names {
		if !strings.Contains(doc, "`"+name+"`") && !strings.Contains(doc, "gates "+name) {
			problems = append(problems, stalenessDevDoc+" does not name the "+name+" gate")
		}
	}
	for _, m := range stalenessGateRef.FindAllStringSubmatch(doc, -1) {
		if !slices.Contains(names, m[1]) {
			problems = append(problems, stalenessDevDoc+" shows `gates "+m[1]+"`, which the registry does not have")
		}
	}
	return len(names), problems
}

var (
	stalenessStatusLine   = regexp.MustCompile(`(?m)^\*\*Status[^*]*\*\*`)
	stalenessVersion      = regexp.MustCompile(`\bv?(\d+\.\d+\.\d+)\b`)
	stalenessHeading      = regexp.MustCompile(`(?m)^## \[(\d+\.\d+\.\d+)\]`)
	stalenessNothingTag   = regexp.MustCompile(`(?i)nothing\b[^.]*\btagged|not (?:yet )?tagged|untagged`)
	stalenessNothingBuilt = regexp.MustCompile(`(?i)design only|nothing\b[^.]*\bbuilt`)
)

// stalenessStatus holds the design document's status line against the
// newest tag or CHANGELOG heading, and against whether code exists. The
// heading is accepted too, or the check fails on the release commit that
// writes the new number before the tag exists.
func stalenessStatus(arch, changelog, tag string, built bool) []string {
	line := stalenessStatusLine.FindString(arch)
	if line == "" {
		return []string{stalenessArch + " has no **Status: ...** line; the check is reading the wrong file"}
	}
	var known []string
	if tag != "" {
		known = append(known, tag)
	}
	if m := stalenessHeading.FindStringSubmatch(changelog); m != nil {
		known = append(known, m[1])
	}
	var claims []string
	for _, m := range stalenessVersion.FindAllStringSubmatch(line, -1) {
		claims = append(claims, m[1])
	}
	var problems []string
	switch {
	case len(known) == 0:
		if len(claims) > 0 {
			problems = append(problems, fmt.Sprintf("the status line claims %v and nothing is tagged or released", claims))
		}
		if !stalenessNothingTag.MatchString(line) {
			problems = append(problems, "nothing is tagged, and the status line does not say so")
		}
	default:
		if stalenessNothingTag.MatchString(line) {
			problems = append(problems, fmt.Sprintf("the status line says nothing is tagged; %v is", known))
		}
		ok := false
		for _, c := range claims {
			ok = ok || slices.Contains(known, c)
		}
		if !ok {
			problems = append(problems, fmt.Sprintf("the status line names %v; the newest release is %s",
				claims, strings.Join(known, " or ")))
		}
	}
	if built && stalenessNothingBuilt.MatchString(line) {
		problems = append(problems, "the status line says nothing is built, and internal/ holds code")
	}
	return problems
}

// stalenessChangedSinceTag holds that shipped Go changed since the newest
// tag comes with CHANGELOG entries under [Unreleased]: a release cut from
// here would otherwise publish an empty note over real changes. The
// release commit moves them under a version heading the tag does not yet
// have, and those count too.
func stalenessChangedSinceTag(unreleased, changelog, tag string, changed []string) []string {
	if tag == "" || len(changed) == 0 {
		return nil
	}
	notes := unreleased
	if loc := stalenessHeading.FindStringSubmatchIndex(changelog); loc != nil && changelog[loc[2]:loc[3]] != tag {
		cut := changelog[loc[1]:]
		if end := strings.Index(cut, "\n## "); end >= 0 {
			cut = cut[:end]
		}
		notes += cut
	}
	for line := range strings.Lines(notes) {
		if strings.HasPrefix(strings.TrimSpace(line), "- ") {
			return nil
		}
	}
	return []string{fmt.Sprintf("%d shipped file(s) changed since v%s (%s) and CHANGELOG.md has no entry under [Unreleased]",
		len(changed), tag, strings.Join(changed[:min(3, len(changed))], ", "))}
}

// stalenessProseDocs are held to carrying no version: a badge shows it.
// architecture.md is excluded because its pin table and evidence log
// record versions as facts about a date, and the CHANGELOG is where
// versions belong.
func stalenessProseDocs(docs map[string]string) []string {
	var out []string
	for name := range docs {
		if name == stalenessArch || strings.HasPrefix(name, "CHANGELOG.md") {
			continue
		}
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

var stalenessProseVersion = regexp.MustCompile(`\bv\d+\.\d+\.\d+\b`)

func stalenessNoVersionInProse(docs map[string]string) (int, []string) {
	names := stalenessProseDocs(docs)
	var problems []string
	for _, name := range names {
		text := stalenessFence.ReplaceAllString(docs[name], "")
		for i, line := range strings.Split(text, "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "[!") || strings.Contains(t, "://") {
				continue
			}
			t = stalenessCodeSpan.ReplaceAllString(t, "")
			if v := stalenessProseVersion.FindString(t); v != "" {
				problems = append(problems, fmt.Sprintf("%s:%d writes %s in prose; a badge shows the version and cannot go stale",
					name, i+1, v))
			}
		}
	}
	problems = append(problems, gatekit.Floor("documents checked for a version in prose", len(names), 4)...)
	return len(names), problems
}

var (
	stalenessToolSentence = regexp.MustCompile(`(?i)\b([a-z0-9-]+) tools\. With the default toolsets: ([a-z0-9-]+) by default, ` +
		`([a-z0-9-]+) in read-only mode, ([a-z0-9-]+) with Ship and Destructive both enabled\. ` +
		`Every toolset and flag on registers all ([a-z0-9-]+)\.`)
	stalenessToolRow = regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\| ([A-Za-z]+) \\| ([a-z]+) \\|")
	stalenessSpace   = regexp.MustCompile(`\s+`)
)

// stalenessToolCounts holds §8's sentence against §8's table, and the
// built binary's tools against the table's names: a tool the binary
// registers that the design does not list is a design nobody updated.
func stalenessToolCounts(arch string, tools []string, toolsErr error) (int, []string) {
	section, _ := gatekit.MarkdownSection(arch, "## 8. Tool surface")
	if i := strings.Index(section, "\n### 8a"); i >= 0 {
		section = section[:i]
	}
	rows := stalenessToolRow.FindAllStringSubmatch(section, -1)
	if len(rows) < 20 {
		return len(rows), []string{fmt.Sprintf("§8's tool table has %d rows; the check is reading the wrong section", len(rows))}
	}
	var names []string
	def, readOnly, shipDestr := 0, 0, 0
	for _, r := range rows {
		names = append(names, r[1])
		if r[3] != "default" {
			continue
		}
		switch r[2] {
		case "Read":
			readOnly++
			def++
			shipDestr++
		case "Write":
			def++
			shipDestr++
		case "Ship", "Destructive":
			shipDestr++
		}
	}
	var problems []string
	m := stalenessToolSentence.FindStringSubmatch(stalenessSpace.ReplaceAllString(section, " "))
	if m == nil {
		problems = append(problems, "§8 states no tool counts in the form \"N tools. With the default toolsets: N by default, "+
			"N in read-only mode, N with Ship and Destructive both enabled. Every toolset and flag on registers all N.\"; "+
			"this rule holds nothing")
	} else {
		want := []int{len(rows), def, readOnly, shipDestr, len(rows)}
		labels := []string{"tools", "by default", "in read-only mode", "with Ship and Destructive", "with everything on"}
		for i, w := range want {
			got, ok := stalenessNumber(m[i+1])
			if !ok || got != w {
				problems = append(problems, fmt.Sprintf("§8 says %q %s; the table says %d", m[i+1], labels[i], w))
			}
		}
	}
	if toolsErr != nil {
		problems = append(problems, "the built binary's tools could not be read: "+toolsErr.Error())
	}
	for _, t := range tools {
		if !slices.Contains(names, t) {
			problems = append(problems, "the binary registers "+t+", which §8's table does not list")
		}
	}
	return len(rows), problems
}

// stalenessReadmeTool is a row of a table whose first column holds a
// tool name in backticks.
var stalenessReadmeTool = regexp.MustCompile("^\\| `([a-z_]+)` \\|")

// stalenessReadmeTools holds the README's tool table — the one whose
// header's first cell is "Tool", not the Safety table keyed "Setting" —
// to the tools the built binary registers, both ways. The README is
// where a person learns what the server does, so a tool it omits does
// not exist for them, and one it lists that the binary lacks is a
// promise nothing keeps.
func stalenessReadmeTools(readme string, tools []string) (int, []string) {
	listed, found := stalenessTable(readme, "Tool")
	if !found {
		return 0, []string{"README.md has no table headed | Tool |; this rule holds nothing"}
	}
	var names []string
	for _, row := range listed {
		if m := stalenessReadmeTool.FindStringSubmatch(row); m != nil {
			names = append(names, m[1])
		}
	}
	problems := gatekit.Floor("rows in README.md's tool table", len(names), 10)
	if tools == nil {
		return len(names), problems // the binary's absence is reported by the §8 rule
	}
	for _, t := range tools {
		if !slices.Contains(names, t) {
			problems = append(problems, "the binary registers "+t+", which README.md's tool table does not list")
		}
	}
	for _, t := range names {
		if !slices.Contains(tools, t) {
			problems = append(problems, "README.md's tool table lists "+t+", which the binary does not register")
		}
	}
	return len(names), problems
}

// stalenessTable is the body rows of the first table whose header's
// first cell is first.
func stalenessTable(text, first string) ([]string, bool) {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if !strings.HasPrefix(strings.TrimSpace(line), "|") || strings.TrimSpace(cells[0]) != first {
			continue
		}
		var rows []string
		for _, l := range lines[i+1:] {
			t := strings.TrimSpace(l)
			if !strings.HasPrefix(t, "|") {
				break
			}
			if strings.HasPrefix(t, "|---") || strings.HasPrefix(t, "| ---") {
				continue
			}
			rows = append(rows, t)
		}
		return rows, true
	}
	return nil, false
}

// stalenessCountWord is a number a sentence can state about the list
// under it: two to twenty spelled out, or 2 to 99 in digits. "One" is
// left out; "one of them" is too common to read as a count.
var stalenessCountWord = regexp.MustCompile(`(?i)\b(two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|` +
	`thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty|[2-9]|[1-9][0-9])\b`)

// stalenessListItem is a top-level bullet or numbered item.
var stalenessListItem = regexp.MustCompile(`^(?:[-*]|\d+\.) `)

// stalenessProseCounts holds a number in the sentence that introduces a
// list — the sentence ending in a colon right above it — against the
// items in that list. "Two of those are worth knowing" above three
// bullets is wrong in a way no reader can act on.
func stalenessProseCounts(docs map[string]string) (int, []string) {
	var problems []string
	checked := 0
	for _, name := range slices.Sorted(maps.Keys(docs)) {
		if strings.Contains(name, "#") {
			continue
		}
		lines := strings.Split(stalenessFence.ReplaceAllString(docs[name], ""), "\n")
		for i := 0; i < len(lines); i++ {
			if !strings.HasSuffix(strings.TrimSpace(lines[i]), ":") {
				continue
			}
			j := i + 1
			for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
				j++
			}
			if j >= len(lines) || !stalenessListItem.MatchString(lines[j]) {
				continue
			}
			items := 0
			for ; j < len(lines); j++ {
				l := lines[j]
				switch {
				case stalenessListItem.MatchString(l):
					items++
				case strings.TrimSpace(l) == "", strings.HasPrefix(l, " "), strings.HasPrefix(l, "\t"):
				default:
					j = len(lines)
				}
			}
			sentence := stalenessIntro(lines, i)
			words := stalenessCountWord.FindAllString(sentence, -1)
			if len(words) != 1 {
				continue
			}
			checked++
			if n, ok := stalenessNumber(strings.ToLower(words[0])); ok && n != items {
				problems = append(problems, fmt.Sprintf("%s:%d says %q above a list of %d", name, i+1,
					strings.TrimSpace(sentence), items))
			}
		}
	}
	return checked, problems
}

// stalenessIntro is the last sentence of the paragraph ending at line i.
func stalenessIntro(lines []string, i int) string {
	start := i
	for start > 0 && strings.TrimSpace(lines[start-1]) != "" && !stalenessListItem.MatchString(lines[start-1]) &&
		!strings.HasPrefix(strings.TrimSpace(lines[start-1]), "#") {
		start--
	}
	para := strings.Join(lines[start:i+1], " ")
	para = stalenessCodeSpan.ReplaceAllString(para, "")
	if k := strings.LastIndex(strings.TrimSuffix(strings.TrimSpace(para), ":"), ". "); k >= 0 {
		para = para[k+2:]
	}
	return para
}

// stalenessNumber reads digits or an English number up to ninety-nine.
func stalenessNumber(s string) (int, bool) {
	if n, err := strconv.Atoi(s); err == nil {
		return n, true
	}
	units := []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten",
		"eleven", "twelve", "thirteen", "fourteen", "fifteen", "sixteen", "seventeen", "eighteen", "nineteen"}
	tens := []string{"", "", "twenty", "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety"}
	s = strings.ToLower(s)
	if i := slices.Index(units, s); i >= 0 {
		return i, true
	}
	t, u, _ := strings.Cut(s, "-")
	ti := slices.Index(tens, t)
	if ti < 2 {
		return 0, false
	}
	if u == "" {
		return ti * 10, true
	}
	ui := slices.Index(units, u)
	if ui < 1 || ui > 9 {
		return 0, false
	}
	return ti*10 + ui, true
}

// stalenessSetupMarker brackets one mode's setup block in docs/setup.md.
func stalenessSetupMarker(edge, mode string) string {
	return "<!-- setup:" + edge + " " + mode + " -->"
}

// stalenessSetupRender is the block docs/setup.md carries for a mode,
// between its markers: the text a person copies into GitLab's
// application form, generated from internal/scopes.
func stalenessSetupRender(m stalenessMode) string {
	return "```text\n" + m.Block + "```"
}

// stalenessSetupBlocks generates the setup block for every mode and
// compares it exactly with the marked block in docs/setup.md. A scope
// missing from the application is not a stale doc; it is a 403 from one
// tool weeks later.
func stalenessSetupBlocks(doc string, modes []stalenessMode) (int, []string) {
	problems := gatekit.Floor("scope modes from internal/scopes", len(modes), 2)
	if doc == "" {
		return len(modes), append(problems, stalenessSetupDoc+" is missing, and it is where the application's settings are")
	}
	for _, m := range modes {
		begin, end := stalenessSetupMarker("begin", m.Name), stalenessSetupMarker("end", m.Name)
		want := stalenessSetupRender(m)
		b := strings.Index(doc, begin)
		e := strings.Index(doc, end)
		if b < 0 || e < b {
			problems = append(problems, fmt.Sprintf("%s has no %s ... %s block; put this between them:\n%s",
				stalenessSetupDoc, begin, end, want))
			continue
		}
		if got := strings.Trim(doc[b+len(begin):e], "\n"); got != want {
			problems = append(problems, fmt.Sprintf("%s's %s block differs from what internal/scopes generates; "+
				"put this between the markers:\n%s", stalenessSetupDoc, m.Name, want))
		}
	}
	return len(modes), problems
}
