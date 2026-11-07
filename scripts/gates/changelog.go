package main

import (
	"cmp"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/mmedum/gitlab-mcp/v4/scripts/internal/gatekit"
	"github.com/mmedum/gitlab-mcp/v4/scripts/internal/gitx"
)

// The CHANGELOG gates: a pull request that changes what ships adds an
// entry under [Unreleased] (`changelog`, CI only, since it needs the
// pull request's base), and every heading has its link reference
// (`changelog-links`, in check, since it reads one file).

const changelogFile = "CHANGELOG.md"

// changelogWatched are the trees a reader of the changelog expects to
// see named there. Tests and fixtures under them are not: they change
// nothing a user runs.
var changelogWatched = []string{"cmd/", "internal/", "packaging/", "go.mod", ".goreleaser.yaml"}

var (
	// changelogVersion is a release's version, a prerelease suffix allowed.
	changelogVersion = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?$`)
	// changelogLink is a link-reference line, `[label]: url`, its URL on
	// the same line or not yet written: either ends a section.
	changelogLink = regexp.MustCompile(`^\[([^\]]+)\]:[ \t]*(\S*)`)
)

// changelogDoc is a CHANGELOG read once, fenced code as an example: a
// heading, a link reference or an entry inside a fence is none of them.
type changelogDoc struct {
	// lines are the file's lines, CRLF read as LF, and prose the same
	// lines with fenced code blanked.
	lines, prose []string
	// headings are the `## [label]` headings, in file order.
	headings []changelogHeading
	// links are the link references that give a URL, by label.
	links map[string]string
}

// changelogHeading is a `## [label]` heading and the index of its line.
type changelogHeading struct {
	label string
	line  int
}

func readChangelog(text string) changelogDoc {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	c := changelogDoc{lines: strings.Split(text, "\n"), prose: docsProse(text), links: map[string]string{}}
	for i, line := range c.prose {
		if label, ok := changelogLabel(line); ok {
			c.headings = append(c.headings, changelogHeading{label, i})
		}
		if m := changelogLink.FindStringSubmatch(line); m != nil && m[2] != "" {
			c.links[m[1]] = m[2]
		}
	}
	return c
}

// changelogLabel is the label of a `## [label]` heading line.
func changelogLabel(line string) (string, bool) {
	if docsHeadingLevel(line) != 2 {
		return "", false
	}
	title := docsATXHeading.FindStringSubmatch(line)[1]
	label, _, ok := strings.Cut(strings.TrimPrefix(title, "["), "]")
	return label, ok && strings.HasPrefix(title, "[")
}

// changelogCompare orders two versions changelogVersion matches by semver
// precedence: by number, a prerelease before its release, and prerelease
// fields compared in turn, numbers below words (https://semver.org/#spec-item-11).
func changelogCompare(a, b string) int {
	coreA, preA, _ := strings.Cut(a, "-")
	coreB, preB, _ := strings.Cut(b, "-")
	if c := slices.CompareFunc(strings.Split(coreA, "."), strings.Split(coreB, "."), changelogCompareNumbers); c != 0 {
		return c
	}
	switch {
	case preA == preB:
		return 0
	case preA == "":
		return 1
	case preB == "":
		return -1
	}
	return slices.CompareFunc(strings.Split(preA, "."), strings.Split(preB, "."), func(x, y string) int {
		xNum, yNum := changelogDigits(x), changelogDigits(y)
		switch {
		case xNum && yNum:
			return changelogCompareNumbers(x, y)
		case xNum:
			return -1
		case yNum:
			return 1
		}
		return strings.Compare(x, y)
	})
}

// changelogCompareNumbers orders two runs of digits by the number they
// write, however long. Semver allows no leading zero.
func changelogCompareNumbers(x, y string) int {
	return cmp.Or(cmp.Compare(len(x), len(y)), strings.Compare(x, y))
}

// changelogDigits reports whether a prerelease field is a number.
func changelogDigits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

// section is the range of lines under `## [label]`, empty when there is
// no such heading. It ends at the next heading of its level or above and
// at the link-reference footer.
func (c changelogDoc) section(label string) (from, to int) {
	i := slices.IndexFunc(c.headings, func(h changelogHeading) bool { return h.label == label })
	if i < 0 {
		return 0, 0
	}
	from = c.headings[i].line + 1
	for to = from; to < len(c.prose); to++ {
		if level := docsHeadingLevel(c.prose[to]); (level > 0 && level <= 2) || changelogLink.MatchString(c.prose[to]) {
			break
		}
	}
	return from, to
}

// versions are the headings that name a release, in file order.
func (c changelogDoc) versions() []changelogHeading {
	var out []changelogHeading
	for _, h := range c.headings {
		if changelogVersion.MatchString(h.label) {
			out = append(out, h)
		}
	}
	return out
}

func changelog(out io.Writer, args []string) error {
	return changelogCheck(out, ".", args[0], args[1])
}

// mergeBase prints the commit a pull request is measured from, for a
// workflow step or a person to read.
func mergeBase(out io.Writer, args []string) error {
	sha, err := gitx.MergeBase(".", args[0], args[1])
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, sha)
	return err
}

// changelogCheck fails when the pull request touches watched source
// without adding a line under [Unreleased]. It is measured from the
// merge-base of base and head, never from base itself: in a stack of
// pull requests the base has moved, and diffing against it would count
// the base branch's own changes as this pull request's.
func changelogCheck(out io.Writer, root, base, head string) error {
	from, err := gitx.MergeBase(root, base, head)
	if err != nil {
		return err
	}
	files, err := gitx.ChangedFiles(root, from, head)
	if err != nil {
		return err
	}
	touched := changelogTouched(files)
	watched, err := withoutGoModBump(root, from, head, touched)
	if err != nil {
		return err
	}
	switch {
	case len(watched) == 0 && len(touched) > 0:
		_, _ = fmt.Fprintf(out, "changelog: %d files changed, the only shipped one go.mod and only in the modules it requires; "+
			"a dependency bump needs no entry\n", len(files))
		return nil
	case len(watched) == 0:
		_, _ = fmt.Fprintf(out, "changelog: %d files changed, none of them shipped source; no entry needed\n", len(files))
		return nil
	}
	// A large context, so the [Unreleased] heading is in the hunk however
	// far below it the entry sits.
	diff, err := gitx.Output(root, "diff", "--unified=99999", from, head, "--", changelogFile)
	if err != nil {
		return err
	}
	if strings.TrimSpace(diff) == "" {
		return fmt.Errorf("%d shipped file(s) changed (%s) and %s did not", len(watched),
			strings.Join(watched[:min(3, len(watched))], ", "), changelogFile)
	}
	cut, n := changelogDiff(diff)
	if cut != "" {
		_, _ = fmt.Fprintf(out, "changelog ok: a release cut, adding ## [%s]\n", cut)
		return nil
	}
	if n == 0 {
		return fmt.Errorf("%s changed and no line was added under [Unreleased]; an entry under a released "+
			"heading is invisible to the release it belongs to", changelogFile)
	}
	_, _ = fmt.Fprintf(out, "changelog ok: %d shipped file(s) changed, %d line(s) added under [Unreleased]\n", len(watched), n)
	return nil
}

// changelogTouched are the changed files a changelog reader cares about.
func changelogTouched(files []string) []string {
	var out []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || strings.Contains(f, "/testdata/") {
			continue
		}
		for _, p := range changelogWatched {
			if strings.HasPrefix(f, p) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// changelogNotedModules need an entry whenever their version moves: the
// MCP SDK decides what the protocol does, and a client sees that.
var changelogNotedModules = []string{"github.com/modelcontextprotocol/go-sdk"}

// withoutGoModBump drops go.mod from the shipped files when it changed
// between from and to only in the modules it requires: the bump
// Dependabot opens every week. It changes no code and no build directive,
// and a line naming it tells nobody deciding whether to upgrade anything.
// A bump that fixes an advisory is found at release time
// (docs/release.md).
func withoutGoModBump(root, from, to string, files []string) ([]string, error) {
	if !slices.Contains(files, "go.mod") {
		return files, nil
	}
	before, err := gitx.Show(root, from, "go.mod")
	if err != nil {
		return nil, err
	}
	after, err := gitx.Show(root, to, "go.mod")
	if err != nil {
		return nil, err
	}
	was, wasRead := goModOutsideRequires(string(before))
	is, isRead := goModOutsideRequires(string(after))
	if !wasRead || !isRead || was != is {
		return files, nil
	}
	return slices.DeleteFunc(slices.Clone(files), func(f string) bool { return f == "go.mod" }), nil
}

// goModOutsideRequires is go.mod with its require entries and blank lines
// taken out. A line is read with its comment cut, because Go 1.27's
// `go mod edit -fmt` keeps a comment after `require (` and after `)` in
// a block of two or more entries, and a block closed by `) // comment`
// must still close.
// The Go reference allows a comment on any line and is silent on which
// ones the formatter keeps.
// https://go.dev/ref/mod#go-mod-file-lexical
//
// Only a line shaped like a require entry is taken out, so anything
// else stays in wherever it sits. read is false unless the module line was
// seen and every require block closed: a comparison that read less waives
// nothing.
func goModOutsideRequires(gomod string) (outside string, read bool) {
	var b strings.Builder
	inRequire, sawModule := false, false
	for line := range strings.Lines(gomod) {
		t := strings.TrimSpace(line)
		code, _, _ := strings.Cut(t, "//")
		f := strings.Fields(code)
		sawModule = sawModule || (len(f) == 2 && f[0] == "module")
		switch {
		case inRequire && len(f) == 1 && f[0] == ")":
			inRequire = false
		case inRequire && goModBumpable(f):
		case len(f) == 2 && f[0] == "require" && f[1] == "(":
			inRequire = true
		case len(f) == 3 && f[0] == "require" && goModBumpable(f[1:]):
		case t == "":
		default:
			b.WriteString(t + "\n")
		}
	}
	return b.String(), sawModule && !inRequire
}

// goModBumpable is a require entry, a module and its version, whose
// version may move without an entry.
func goModBumpable(f []string) bool {
	return len(f) == 2 && strings.HasPrefix(f[1], "v") &&
		!slices.Contains(changelogNotedModules, strings.Trim(f[0], `"`))
}

// changelogDiff reads a CHANGELOG diff's hunk as each side of the file
// reads, fenced code as an example: the first release heading it adds,
// which makes it a release cut, and how many lines it adds under
// [Unreleased].
//
// A release cut renames [Unreleased] to the version, so its entries no
// longer sit under [Unreleased] and are still where they belong. When
// the diff removes the [Unreleased] heading, a heading it adds is read as
// the unreleased section.
func changelogDiff(diff string) (cut string, added int) {
	var hunk []string
	if _, body, ok := strings.Cut(diff, "\n@@"); ok {
		hunk = strings.Split(body, "\n")[1:]
	}
	// side is each hunk line's text on one side, with whether it is outside
	// a fence and whether the diff changes it there.
	side := func(mark byte, line func(text string, prose, changed bool)) {
		var fence docsFence
		for _, l := range hunk {
			if l != "" && (l[0] == ' ' || l[0] == mark) {
				line(l[1:], !fence.code(l[1:]), l[0] == mark)
			}
		}
	}
	removed := false
	side('-', func(text string, prose, changed bool) {
		label, ok := changelogLabel(text)
		removed = removed || (prose && changed && ok && label == "Unreleased")
	})
	inside := false
	side('+', func(text string, prose, changed bool) {
		if label, ok := changelogLabel(text); prose && ok {
			if changed && cut == "" && changelogVersion.MatchString(label) {
				cut = label
			}
			inside = label == "Unreleased" || (removed && changed)
			return
		}
		if inside && changed && strings.TrimSpace(text) != "" {
			added++
		}
	})
	return cut, added
}

func changelogLinks(out io.Writer, _ []string) error {
	data, err := os.ReadFile(changelogFile)
	if err != nil {
		return err
	}
	versions, problems := changelogLinkProblems(string(data))
	if err := gatekit.Problems(out, changelogFile, problems); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "changelog-links ok: [Unreleased] and %d version heading(s), every one linked\n", versions)
	return nil
}

// changelogLinkProblems holds the link references against the headings.
// Before the first release there are no version headings, and
// [Unreleased] must still be there and linked.
func changelogLinkProblems(text string) (int, []string) {
	var problems []string
	doc := readChangelog(text)
	if !slices.ContainsFunc(doc.headings, func(h changelogHeading) bool { return h.label == "Unreleased" }) {
		problems = append(problems, "there is no ## [Unreleased] heading, so there is nowhere for the next entry")
	}
	headings := doc.versions()
	versions := make([]string, 0, len(headings))
	for _, h := range headings {
		versions = append(versions, h.label)
	}
	links := doc.links
	unreleased, ok := links["Unreleased"]
	if !ok {
		problems = append(problems, "[Unreleased] has no link reference, so the heading renders as literal brackets")
	} else if !strings.HasPrefix(unreleased, "https://") {
		problems = append(problems, fmt.Sprintf("[Unreleased] links to %q, which is not an https URL", unreleased))
	}
	for _, v := range versions {
		if _, ok := links[v]; !ok {
			problems = append(problems, fmt.Sprintf("## [%s] has no [%s]: link reference", v, v))
		}
	}
	// [Unreleased] compares from the newest release, or it claims that
	// release's work has not shipped.
	if len(versions) > 0 && ok {
		if want := "v" + versions[0] + "..."; !strings.Contains(unreleased, want) {
			problems = append(problems, fmt.Sprintf("[Unreleased] compares from %q and the newest release is %s; "+
				"it should compare from %s", unreleased, versions[0], want))
		}
	}
	for name := range links {
		if name != "Unreleased" && !slices.Contains(versions, name) && changelogVersion.MatchString(name) {
			problems = append(problems, fmt.Sprintf("[%s]: is a link reference with no heading", name))
		}
	}
	slices.Sort(problems)
	return len(versions), problems
}
