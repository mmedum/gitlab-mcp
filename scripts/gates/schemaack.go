package main

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/mmedum/gitlab-mcp/v4/scripts/internal/gitx"
)

// schemaAck fails a pull request whose tool surface differs from its
// merge-base's, unless the commits it adds name every item that changed
// in a footer. Annotations count: a flipped destructiveHint changes
// whether a client asks the person. Both sides are built from commits,
// the merge-base and the head, never from the checkout, which in CI is
// the merge commit and would blame the base branch's changes on the pull
// request.
//
// A gate that could not run, a failed build or git call, says so and
// exits 2; a refusal exits 1.
func schemaAck(out io.Writer, args []string) error {
	c, messages, base, err := ackInputs(args[0], args[1])
	if err != nil {
		return couldNotRun{err}
	}
	return checkAck(out, c, messages, base)
}

// ackInputs is what checkAck judges: the changes from the merge-base of
// baseRef and head to head, with each break kept as one only if it also
// breaks the last release; the messages of the commits in between; and
// the merge-base.
func ackInputs(baseRef, head string) (surfaceChanges, []string, string, error) {
	root, err := gitx.Root(".")
	if err != nil {
		return surfaceChanges{}, nil, "", err
	}
	base, err := gitx.MergeBase(root, baseRef, head)
	if err != nil {
		return surfaceChanges{}, nil, "", err
	}
	prev, err := dumpAt(root, base)
	if err != nil {
		return surfaceChanges{}, nil, "", err
	}
	cur, err := dumpAt(root, head)
	if err != nil {
		return surfaceChanges{}, nil, "", err
	}
	c := compareSurfaces(prev, cur)
	if len(c.breaking) > 0 {
		released, err := releasedBreaks(root, baseRef, cur)
		if err != nil {
			return surfaceChanges{}, nil, "", err
		}
		c = releaseBreaksOnly(c, released)
	}
	messages, err := gitx.Messages(root, base, head)
	if err != nil {
		return surfaceChanges{}, nil, "", err
	}
	return c, messages, base, nil
}

// releasedBreaks is what head breaks of the last release's surface: the
// build of the tag git describe finds for the base branch, the nearest
// it reaches, not the head's, which a branch cut before the last tag
// would leave behind. Before the first tag nothing is released, so
// nothing breaks a caller.
func releasedBreaks(root, baseRef string, head schemaDump) ([]change, error) {
	tag, err := gitx.LatestTagOn(root, baseRef)
	if errors.Is(err, gitx.ErrNoTag) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	release, err := dumpAt(root, tag)
	if err != nil {
		return nil, err
	}
	return compareSurfaces(release, head).breaking, nil
}

// releaseBreaksOnly keeps as breaks only those the head also makes
// against the released surface. Any other break narrows something no
// release shipped, so it breaks no caller and is a plain change. A break
// is matched by its item and field, never its text.
func releaseBreaksOnly(c surfaceChanges, released []change) surfaceChanges {
	type spot struct{ item, field string }
	inRelease := map[spot]bool{}
	for _, b := range released {
		inRelease[spot{b.item, b.field}] = true
	}
	var breaking []change
	for _, b := range c.breaking {
		if inRelease[spot{b.item, b.field}] {
			breaking = append(breaking, b)
		} else {
			c.changed = append(c.changed, b)
		}
	}
	c.breaking = breaking
	return c
}

// checkAck is the rule, over the changes and the commit messages, so a
// test can hand it both. Every added or changed item must be named in an
// acknowledging footer, and every break in a BREAKING CHANGE: one. It
// reports the changes on out, and a refusal names the footer that fixes
// it.
func checkAck(out io.Writer, c surfaceChanges, messages []string, base string) error {
	if c.empty() {
		_, _ = fmt.Fprintf(out, "schema ack ok: the tool surface is unchanged since %s\n", short(base))
		return nil
	}
	c.write(out)
	named, breaks := acknowledged(messages)
	var problems, fix []string
	breakItems := unnamed(c.breaking, breaks)
	if len(breakItems) > 0 {
		problems = append(problems, "no BREAKING CHANGE: footer names "+strings.Join(breakItems, ", "))
		fix = append(fix, "BREAKING CHANGE: "+footerList(breakItems)+": what broke")
	}
	// A BREAKING CHANGE: footer names an item for both checks, so an
	// unnamed break is not asked for twice.
	items := unnamed(slices.Concat(c.added, c.changed), named)
	if rest := slices.DeleteFunc(items, func(i string) bool { return slices.Contains(breakItems, i) }); len(rest) > 0 {
		problems = append(problems, "no footer names "+strings.Join(rest, ", "))
		fix = append(fix, "SCHEMA-CHANGE: "+footerList(rest)+": what changed")
	}
	if len(problems) > 0 {
		return fmt.Errorf("the tool surface changed since %s, and %s. Add an empty commit (git commit --allow-empty), "+
			"never an amend, whose message ends with:\n\n%s", short(base), strings.Join(problems, ", and "), strings.Join(fix, "\n"))
	}
	_, _ = fmt.Fprintf(out, "schema ack ok: the change since %s is acknowledged\n", short(base))
	return nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// footerList is names as a footer has to write them: a name the footer
// would split, or trim, in backticks, so the footer printed is one that
// passes.
func footerList(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = n
		if nameSeparator.MatchString(n) || strings.TrimRight(n, ":.") != n {
			out[i] = "`" + n + "`"
		}
	}
	return strings.Join(out, ", ")
}

// unnamed is each item of cs that names does not hold, sorted, once.
func unnamed(cs []change, names map[string]bool) []string {
	var out []string
	for _, c := range cs {
		if !names[c.item] {
			out = append(out, c.item)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// footerStart is a line that starts a footer: a token, then ": " or
// " #", then its value, as Co-Authored-By: and Closes #12 are
// (Conventional Commits 1.0.0, items 8 and 9).
var footerStart = regexp.MustCompile(`^(BREAKING CHANGE|[A-Za-z][A-Za-z0-9-]*)(:[ \t]+| #)(\S.*)$`)

// paragraphBreak is one or more blank lines, which end a paragraph.
var paragraphBreak = regexp.MustCompile(`\n([ \t]*\n)+`)

// A footer is one token and its value, the lines that continue it joined
// on, each on its own line. ref marks a "Token #value" footer, which
// refers to something, such as an issue, rather than describing.
type footer struct {
	token, text string
	ref         bool
}

// footers is a commit message's footer block: the paragraphs at its end
// whose first line starts a footer, never the subject. Inside one, a line
// that starts a footer ends the value before it, and any other line
// continues that value, wrapped at the margin or indented (item 10). A
// paragraph whose first line is prose ends the block, so a line of prose
// that happens to start "BREAKING CHANGE:" is not a footer.
func footers(message string) []footer {
	paras := paragraphBreak.Split(strings.TrimSpace(strings.ReplaceAll(message, "\r\n", "\n")), -1)
	var out []footer
	for i := len(paras) - 1; i > 0; i-- {
		var block []footer
		for _, l := range strings.Split(strings.Trim(paras[i], "\n"), "\n") {
			switch m := footerStart.FindStringSubmatch(l); {
			case m != nil:
				block = append(block, footer{m[1], m[3], m[2] == " #"})
			case len(block) > 0:
				block[len(block)-1].text += "\n" + strings.TrimSpace(l)
			default:
				return out
			}
		}
		out = append(block, out...)
	}
	return out
}

// ackToken says whether a footer's token acknowledges a change to the
// tool surface, and whether a break of a release. Conventional Commits
// reads a token in any case but BREAKING CHANGE, which must be
// uppercase, and BREAKING-CHANGE is its synonym.
func ackToken(token string) (isBreak, ok bool) {
	switch {
	case token == "BREAKING CHANGE" || token == "BREAKING-CHANGE":
		return true, true
	case strings.EqualFold(token, "SCHEMA-CHANGE"):
		return false, true
	}
	return false, false
}

// nameSeparator is what separates the names in a footer's list.
var nameSeparator = regexp.MustCompile("[\\s,;()'\"]+")

// descriptionStart is the colon and white space that end a footer's list
// of names.
var descriptionStart = regexp.MustCompile(`:\s`)

// footerNames is every name a footer's value lists: the part before its
// first colon and white space, ending sooner at a line that does not end
// with a comma, each an item between commas. What follows is the description, free
// text that names nothing, so a tool whose name is an ordinary word is
// not acknowledged by a sentence that uses it. A colon and a full stop
// are trimmed from a name rather than split on, since a URI holds both;
// a name in backticks is taken whole, trimmed of nothing.
func footerNames(text string) []string {
	list := text
	if at := descriptionStart.FindStringIndex(text); at != nil {
		list = text[:at[0]]
	}
	// A list wraps only after a comma: a line that does not end with one
	// ends it, and what follows describes, so prose on the next line of a
	// bare list names nothing.
	lines := strings.Split(list, "\n")
	for i, l := range lines {
		if !strings.HasSuffix(strings.TrimSpace(l), ",") {
			list = strings.Join(lines[:i+1], "\n")
			break
		}
	}
	var out []string
	for _, m := range stalenessCodeSpan.FindAllStringSubmatch(list, -1) {
		out = append(out, m[1])
	}
	// The rest is split on commas alone, so prose is one item that names
	// nothing rather than words that each might.
	for _, n := range strings.Split(stalenessCodeSpan.ReplaceAllString(list, ""), ",") {
		n = strings.TrimRight(strings.Trim(n, " \t\n;()'\""), ":.")
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

// acknowledged reads every message's footers for the items they name:
// named holds each one a SCHEMA-CHANGE: or BREAKING CHANGE: footer
// names, breaks each one a BREAKING CHANGE: footer names. A name is a
// whole token, so get_issue does not name get_issues.
func acknowledged(messages []string) (named, breaks map[string]bool) {
	named, breaks = map[string]bool{}, map[string]bool{}
	for _, message := range messages {
		for _, f := range footers(message) {
			// A "Token #value" footer acknowledges nothing: Conventional
			// Commits writes BREAKING CHANGE with a colon and a space (item
			// 12), and its value has no description to tell from names.
			if f.ref {
				continue
			}
			isBreak, ok := ackToken(f.token)
			if !ok {
				continue
			}
			for _, n := range footerNames(f.text) {
				named[n] = true
				if isBreak {
					breaks[n] = true
				}
			}
		}
	}
	return named, breaks
}
