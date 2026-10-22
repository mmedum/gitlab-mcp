package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/mmedum/gitlab-mcp/v3/scripts/internal/gatekit"
	"github.com/mmedum/gitlab-mcp/v3/scripts/internal/gitx"
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
	changelogVersionHeading = regexp.MustCompile(`(?m)^## \[(\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?)\]`)
	changelogLinkDefinition = regexp.MustCompile(`(?m)^\[([^\]]+)\]:\s*(\S+)`)
	// changelogCutHeading is a version heading a diff adds: a release cut,
	// which moves the entries rather than adding one.
	changelogCutHeading = regexp.MustCompile(`(?m)^\+## \[(\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?)\]`)
)

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
	if v := changelogCutHeading.FindStringSubmatch(diff); v != nil {
		_, _ = fmt.Fprintf(out, "changelog ok: a release cut, adding ## [%s]\n", v[1])
		return nil
	}
	n := changelogAddedUnderUnreleased(diff)
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
// taken out. A line is read with its comment cut, since the go command
// keeps a comment after `require (` and after `)` (docs/architecture.md
// §18). Only a line shaped like a require entry is taken out, so anything
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

// changelogAddedUnderUnreleased counts added lines under [Unreleased].
//
// A release cut renames [Unreleased] to the version, so its entries no
// longer sit under [Unreleased] and are still where they belong. When
// the diff removes the [Unreleased] heading, the version heading it
// added is read as the unreleased section.
func changelogAddedUnderUnreleased(diff string) int {
	cut := strings.Contains(diff, "\n-## [Unreleased]")
	count, inHunk, inside := 0, false, false
	for line := range strings.SplitSeq(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case !inHunk:
		case strings.HasPrefix(line, "+## [") || strings.HasPrefix(line, " ## ["):
			inside = strings.Contains(line, "[Unreleased]") || (cut && strings.HasPrefix(line, "+## ["))
		case inside && strings.HasPrefix(line, "+") && strings.TrimSpace(line[1:]) != "":
			count++
		}
	}
	return count
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
	if !strings.Contains(text, "\n## [Unreleased]") && !strings.HasPrefix(text, "## [Unreleased]") {
		problems = append(problems, "there is no ## [Unreleased] heading, so there is nowhere for the next entry")
	}
	headings := changelogVersionHeading.FindAllStringSubmatch(text, -1)
	versions := make([]string, 0, len(headings))
	for _, m := range headings {
		versions = append(versions, m[1])
	}
	links := map[string]string{}
	for _, m := range changelogLinkDefinition.FindAllStringSubmatch(text, -1) {
		links[m[1]] = m[2]
	}
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
		if name != "Unreleased" && !slices.Contains(versions, name) && changelogVersionHeading.MatchString("## ["+name+"]") {
			problems = append(problems, fmt.Sprintf("[%s]: is a link reference with no heading", name))
		}
	}
	slices.Sort(problems)
	return len(versions), problems
}
