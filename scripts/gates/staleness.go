package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/mmedum/gitlab-mcp/v4/internal/config"
	"github.com/mmedum/gitlab-mcp/v4/internal/scopes"
	"github.com/mmedum/gitlab-mcp/v4/internal/userconfig"
	"github.com/mmedum/gitlab-mcp/v4/scripts/internal/gatekit"
	"github.com/mmedum/gitlab-mcp/v4/scripts/internal/gitx"
	"github.com/mmedum/gitlab-mcp/v4/scripts/internal/mcpstdio"
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

// Documents the rules read by name. CONTRIBUTING.md is where a
// maintainer learns each gate and the development override.
const (
	stalenessConfigDoc       = "docs/configuration.md"
	stalenessContributingDoc = "CONTRIBUTING.md"
	stalenessSetupDoc        = "docs/setup.md"
)

// stalenessInputs is everything the rules read, gathered once so the
// rules are pure functions a test can feed.
type stalenessInputs struct {
	docs map[string]string // repository path → text
	// configVars are the variables internal/config says a person
	// running the server may set; devVars are its development
	// overrides.
	configVars, devVars []string
	// removedVars are variables a release removed, which the document
	// may name to say so.
	removedVars []string
	// modes are the scope modes, each with the setup block
	// internal/scopes generates for it.
	modes []stalenessMode
	// commands are the gate registry's names.
	commands []string
	// newestTag is the newest semver tag, "" for none; changedSince is
	// the shipped Go changed since it, a dependency bump in go.mod aside.
	newestTag    string
	changedSince []string
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

	n, p := stalenessEnv(in.docs[stalenessConfigDoc], in.docs[stalenessContributingDoc], in.configVars, in.devVars, in.removedVars)
	r.add(fmt.Sprintf("settings: %d variables", n), p)

	r.add("profile rule: "+stalenessConfigDoc, stalenessProfileRule(in.docs[stalenessConfigDoc]))

	n, p = stalenessGates(in.docs[stalenessContributingDoc], in.commands)
	r.add(fmt.Sprintf("gates: %d commands", n), p)

	r.add(fmt.Sprintf("changes since %s", cmp.Or(in.newestTag, "the first tag")),
		stalenessChangedSinceTag(in.docs[changelogFile], in.newestTag, in.changedSince))

	n, p = stalenessNoVersionInProse(in.docs)
	r.add(fmt.Sprintf("version in prose: %d documents", n), p)

	n, p = stalenessReadmeTools(in.docs["README.md"], in.tools, in.toolsErr)
	r.add(fmt.Sprintf("README tool table: %d rows", n), p)

	n, p = stalenessProseCounts(in.docs)
	r.add(fmt.Sprintf("prose counts: %d numbers held against the lists beside them", n), p)

	n, p = stalenessSetupBlocks(in.docs[stalenessSetupDoc], in.modes)
	r.add(fmt.Sprintf("setup blocks: %d modes", n), p)
	return r
}

// stalenessDocs are the documents read besides docs/*.md. A rule reads
// only the CHANGELOG's [Unreleased] section unless it needs the release
// sections: older entries are history, correctly describing what was
// true.
var stalenessDocs = []string{"README.md", "CLAUDE.md", "CONTRIBUTING.md", "SECURITY.md", changelogFile}

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
	in.configVars = config.EnvVars()
	in.devVars = config.DevVars()
	in.removedVars = config.RemovedVars()
	for _, m := range scopes.Modes() {
		in.modes = append(in.modes, stalenessMode{Name: string(m.Mode), Block: scopes.SetupBlock(m.Mode)})
	}
	in.commands = slices.Sorted(maps.Keys(commands))
	in.newestTag = stalenessNewestTag(root)
	if in.newestTag != "" {
		var err error
		if in.changedSince, err = stalenessShippedSince(root, "v"+in.newestTag); err != nil {
			return in, err
		}
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

// stalenessShippedSince are the shipped files changed since tag, a
// dependency bump in go.mod aside, as the `changelog` gate counts them.
func stalenessShippedSince(root, tag string) ([]string, error) {
	changed, err := gitx.ChangedFiles(root, tag, "HEAD")
	if err != nil {
		return nil, err
	}
	return withoutGoModBump(root, tag, "HEAD", stalenessShipped(changed))
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

// stalenessUnreleasedFrom is stalenessUnreleased and the file line its
// first line sits on, so a rule reading it reports the file's lines. It
// finds the heading where MarkdownSection does: after a newline, else
// opening the file.
func stalenessUnreleasedFrom(changelog string) (string, int) {
	above := 0
	if i := strings.Index(changelog, "\n## [Unreleased]\n"); i >= 0 {
		above = strings.Count(changelog[:i+1], "\n")
	}
	return stalenessUnreleased(changelog), above + 2
}

// stalenessUnfence blanks fenced code blocks and keeps their line
// breaks, so a line found below one is reported as the file's line.
func stalenessUnfence(text string) string {
	return stalenessFence.ReplaceAllStringFunc(text, func(block string) string {
		return strings.Repeat("\n", strings.Count(block, "\n"))
	})
}

var (
	stalenessCodeSpan = regexp.MustCompile("`([^`\n]+)`")
	stalenessFence    = regexp.MustCompile("(?ms)^\\s*```.*?^\\s*```")
)

var stalenessEnvRef = regexp.MustCompile(`\b` + config.EnvPrefix + `[A-Z0-9_]*[A-Z0-9]\b`)

// stalenessEnvNames is every variable a document names, each a whole
// word: a letter of either case, a digit or _ on either side makes it
// part of a longer word, and a * after it makes it a pattern.
func stalenessEnvNames(doc string) []string {
	var names []string
	for _, at := range stalenessEnvRef.FindAllStringIndex(doc, -1) {
		if !strings.HasPrefix(doc[at[1]:], "*") {
			names = append(names, doc[at[0]:at[1]])
		}
	}
	return names
}

// stalenessEnv holds docs/configuration.md to the variables
// internal/config says the server reads, both ways, and the development
// overrides to CONTRIBUTING.md alone: a person configuring the server is
// never offered one. A removed variable may be named, to say it is gone.
func stalenessEnv(doc, devDoc string, vars, devVars, removed []string) (int, []string) {
	if doc == "" {
		return 0, []string{stalenessConfigDoc + " is missing, and it is where every setting is documented"}
	}
	problems := gatekit.Floor("variables in internal/config.Vars", len(vars), 10)
	for _, v := range devVars {
		if !slices.Contains(stalenessEnvNames(devDoc), v) {
			problems = append(problems, stalenessContributingDoc+" does not document the development override "+v)
		}
	}
	documented := map[string]bool{}
	for _, v := range stalenessEnvNames(doc) {
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
			problems = append(problems, stalenessConfigDoc+" documents "+v+", a development override that belongs in "+stalenessContributingDoc+" only")
		case !slices.Contains(vars, v) && !slices.Contains(removed, v):
			problems = append(problems, stalenessConfigDoc+" documents "+v+", which the server does not read")
		}
	}
	return len(vars) + len(devVars), problems
}

// stalenessProfileRule holds the configuration page to the words a
// refused profile name's error gives, so the page cannot admit a name
// the server stops at.
func stalenessProfileRule(doc string) []string {
	if doc == "" || strings.Contains(doc, userconfig.ProfileRule) {
		return nil // a missing page is stalenessEnv's
	}
	return []string{fmt.Sprintf("%s does not say a profile name is %q", stalenessConfigDoc, userconfig.ProfileRule)}
}

var stalenessGateRef = regexp.MustCompile("`(?:go run \\./scripts/)?gates ([a-z][a-z0-9-]*)")

// stalenessGates holds CONTRIBUTING.md to the gate registry, both
// ways: every command named, in a code span or as `gates <name>`, and
// every `gates <name>` it shows a command the registry has.
func stalenessGates(doc string, names []string) (int, []string) {
	if doc == "" {
		return len(names), []string{stalenessContributingDoc + " is missing, and it is where each gate is documented"}
	}
	problems := gatekit.Floor("gates in the registry", len(names), 20)
	for _, name := range names {
		if !strings.Contains(doc, "`"+name+"`") && !strings.Contains(doc, "gates "+name) {
			problems = append(problems, stalenessContributingDoc+" does not name the "+name+" gate")
		}
	}
	for _, m := range stalenessGateRef.FindAllStringSubmatch(doc, -1) {
		if !slices.Contains(names, m[1]) {
			problems = append(problems, stalenessContributingDoc+" shows `gates "+m[1]+"`, which the registry does not have")
		}
	}
	return len(names), problems
}

var stalenessHeading = regexp.MustCompile(`(?m)^## \[(\d+\.\d+\.\d+)\]`)

// stalenessChangedSinceTag holds that shipped Go changed since the newest
// tag comes with CHANGELOG entries under [Unreleased]: a release cut from
// here would otherwise publish an empty note over real changes. The
// release commit moves them under a version heading the tag does not yet
// have, and those count too.
func stalenessChangedSinceTag(changelog, tag string, changed []string) []string {
	if tag == "" || len(changed) == 0 {
		return nil
	}
	notes := stalenessUnreleased(changelog)
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
// The CHANGELOG is excluded because it is where versions belong.
func stalenessProseDocs(docs map[string]string) []string {
	var out []string
	for name := range docs {
		if name == changelogFile {
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
		text := stalenessUnfence(docs[name])
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

// stalenessReadmeTool is a row of a table whose first column holds a
// tool name in backticks.
var stalenessReadmeTool = regexp.MustCompile("^\\| `([a-z_]+)` \\|")

// stalenessReadmeTools holds the README's tool table — the one whose
// header's first cell is "Tool", not the Safety table keyed "Setting" —
// to the tools the built binary registers, both ways. The README is
// where a person learns what the server does, so a tool it omits does
// not exist for them, and one it lists that the binary lacks is a
// promise nothing keeps.
func stalenessReadmeTools(readme string, tools []string, toolsErr error) (int, []string) {
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
	if toolsErr != nil {
		return len(names), append(problems, "the built binary's tools could not be read: "+toolsErr.Error())
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
	// A Claude Code permission rule for a tool that does not exist matches
	// nothing, so the person it was meant to ask is never asked.
	for _, m := range stalenessPermissionRule.FindAllStringSubmatch(readme, -1) {
		if !slices.Contains(tools, m[1]) {
			problems = append(problems, "README.md gives a permission rule for "+m[1]+", which the binary does not register")
		}
	}
	return len(names), problems
}

// stalenessPermissionRule is a Claude Code permission rule for one of
// this server's tools, under the server name the README uses.
var stalenessPermissionRule = regexp.MustCompile(`"mcp__gitlab__([a-z0-9_]+)"`)

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

// stalenessCountWords are the counts a sentence may spell out. "One" is
// left out; "one of them" is too common to read as a count.
var stalenessCountWords = map[string]int{"two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8,
	"nine": 9, "ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13, "fourteen": 14, "fifteen": 15, "sixteen": 16,
	"seventeen": 17, "eighteen": 18, "nineteen": 19, "twenty": 20}

// stalenessCountWord is a number a sentence can state about the list
// under it: one of stalenessCountWords, or 2 to 99 in digits.
var stalenessCountWord = regexp.MustCompile(`(?i)\b(` + strings.Join(slices.Sorted(maps.Keys(stalenessCountWords)), "|") +
	`|[2-9]|[1-9][0-9])\b`)

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
		text, first := docs[name], 1
		if name == changelogFile {
			text, first = stalenessUnreleasedFrom(text)
		}
		lines := strings.Split(stalenessUnfence(text), "\n")
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
			if n, ok := stalenessNumber(words[0]); ok && n != items {
				problems = append(problems, fmt.Sprintf("%s:%d says %q above a list of %d", name, first+i,
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

// stalenessNumber reads a count stalenessCountWord matched, in digits or
// spelled out in either case.
func stalenessNumber(s string) (int, bool) {
	if n, ok := stalenessCountWords[strings.ToLower(s)]; ok {
		return n, true
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
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
