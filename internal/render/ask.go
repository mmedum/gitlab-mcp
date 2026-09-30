package render

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Question is what the server asks the person before a write that ships
// work or deletes something for good (§4.12). Text is the message a
// client shows; accepting it is the confirmation. Every word is the
// server's, except what stands in backticks, which is quoted from GitLab
// or from the call and cut to one line. A blank line separates the
// lines, so a client that draws Markdown keeps them apart.
//
// Bind is what an answer is bound to: what the write depends on, which
// must not change between the question and the write. It is Text, and
// more where Text shows less than the write uses — a whole head sha, a
// whole comment.
type Question struct {
	Text string
	Bind string
}

// quotedLen caps one quoted value: a title, a path, a name. A body is
// capped at bodyLen.
const (
	quotedLen = 120
	bodyLen   = 300
)

// AskMerge asks before merge_merge_request, or before setting a merge
// request to merge when its pipeline succeeds. The whole head is bound.
func AskMerge(project string, iid int64, title, source, target, sha string, autoMerge bool, squash, removeSource *bool) Question {
	verb := "merge"
	if autoMerge {
		verb = "set to merge when its pipeline succeeds"
	}
	lines := []string{
		fmt.Sprintf("merge_merge_request: %s merge request !%d in %s?", verb, iid, quoted(project, quotedLen)),
		"title: " + quoted(title, quotedLen),
		fmt.Sprintf("from %s into %s, at head %s", quoted(source, quotedLen), quoted(target, quotedLen), askSHA(sha)),
	}
	if squash != nil && *squash {
		lines = append(lines, "squashes its commits into one")
	}
	if removeSource != nil && *removeSource {
		lines = append(lines, "deletes the source branch after the merge")
	}
	lines = append(lines, "A merge cannot be undone; a revert is a new commit.")
	q := ask(lines...)
	// The branches are shown folded and cut; the write depends on them
	// whole.
	q.Bind += "\x00" + sha + "\x00" + source + "\x00" + target
	return q
}

// AskApprove asks before approve_merge_request.
func AskApprove(project string, iid int64, title, sha string) Question {
	q := ask(
		fmt.Sprintf("approve_merge_request: approve merge request !%d in %s at head %s?", iid, quoted(project, quotedLen), askSHA(sha)),
		"title: "+quoted(title, quotedLen),
		"The approval is yours, and counts toward the project's merge rules.",
	)
	q.Bind += "\x00" + sha
	return q
}

// AskPlayJob asks before play_job. Variable values are never shown,
// only their keys.
func AskPlayJob(project string, jobID int64, name, stage string, pipelineID int64, variables, inputs []string) Question {
	return ask(slices.Concat(
		[]string{fmt.Sprintf("play_job: run the manual job %s, job %d in stage %s of pipeline %d, in %s?",
			quoted(name, quotedLen), jobID, quoted(stage, quotedLen), pipelineID, quoted(project, quotedLen))},
		runOptions(variables, inputs),
		[]string{"A manual job is often a deploy or a release step."},
	)...)
}

// RefKind is why a pipeline's ref asks: it is the project's default
// branch, a protected branch or tag, or a ref whose protection cannot
// be told.
type RefKind int

// The kinds of ref run_pipeline asks about.
const (
	DefaultBranch RefKind = iota + 1
	ProtectedBranch
	ProtectedTag
	// UnknownRef is a ref GitLab has no branch or tag by, whose
	// protection cannot be told.
	UnknownRef
)

var refKinds = map[RefKind]string{
	DefaultBranch:   "It is the project's default branch.",
	ProtectedBranch: "It is a protected branch.",
	ProtectedTag:    "It is a protected tag.",
	UnknownRef:      "GitLab has no branch or tag by that exact name, so whether it is protected cannot be told.",
}

// AskRunPipeline asks before run_pipeline on the default branch or a
// protected ref.
func AskRunPipeline(project, ref string, kind RefKind, variables, inputs []string) Question {
	why, ok := refKinds[kind]
	if !ok {
		why = "It is a protected ref."
	}
	return ask(slices.Concat(
		[]string{fmt.Sprintf("run_pipeline: run a pipeline on %s in %s?", quoted(ref, quotedLen), quoted(project, quotedLen)), why},
		runOptions(variables, inputs),
		[]string{"Its jobs may deploy or publish."},
	)...)
}

// runOptions names the variables and inputs a run is given, by key.
func runOptions(variables, inputs []string) []string {
	var out []string
	if len(variables) > 0 {
		out = append(out, "variables: "+quotedList(variables))
	}
	if len(inputs) > 0 {
		out = append(out, "inputs: "+quotedList(inputs))
	}
	return out
}

// AskCreateRelease asks before create_release.
func AskCreateRelease(project, tag, name, ref string, tagExists bool, links int) Question {
	lines := []string{
		fmt.Sprintf("create_release: publish a release for the tag %s in %s?", quoted(tag, quotedLen), quoted(project, quotedLen)),
	}
	if name != "" {
		lines = append(lines, "name: "+quoted(name, quotedLen))
	}
	if !tagExists {
		lines = append(lines, "creates the tag from "+quoted(ref, quotedLen))
	}
	if links > 0 {
		lines = append(lines, fmt.Sprintf("asset links: %d", links))
	}
	lines = append(lines, "A release is announced to everyone who can see the project.")
	return ask(lines...)
}

// AskCreateTag asks before create_tag. A tag a protection rule would
// cover is refused before it is asked about.
func AskCreateTag(project, name, ref string) Question {
	return ask(
		fmt.Sprintf("create_tag: create the tag %s at %s in %s?", quoted(name, quotedLen), quoted(ref, quotedLen), quoted(project, quotedLen)),
		"A tag can start a pipeline, and a release is often built from one.",
	)
}

// AskPublishIssue asks before update_issue makes a confidential issue
// public.
func AskPublishIssue(project string, iid int64, title string) Question {
	return ask(
		fmt.Sprintf("update_issue: make the confidential issue #%d in %s public?", iid, quoted(project, quotedLen)),
		"title: "+quoted(title, quotedLen),
		"Everyone who can see the project's issues will see it, its comments included.",
	)
}

// AskDeleteBranch asks before delete_branch. The whole head is bound.
func AskDeleteBranch(project, branch, sha string, merged bool) Question {
	state := "not merged into the default branch: its commits are lost with it"
	if merged {
		state = "merged into the default branch"
	}
	q := ask(
		fmt.Sprintf("delete_branch: delete the branch %s in %s for good?", quoted(branch, quotedLen), quoted(project, quotedLen)),
		fmt.Sprintf("head %s, %s", askSHA(sha), state),
	)
	q.Bind += "\x00" + sha
	return q
}

// AskDeleteTag asks before delete_tag. The whole commit is bound.
func AskDeleteTag(project, name, sha string, release bool) Question {
	lines := []string{
		fmt.Sprintf("delete_tag: delete the tag %s in %s for good?", quoted(name, quotedLen), quoted(project, quotedLen)),
		"at commit " + askSHA(sha),
	}
	if release {
		lines = append(lines, "A release is built on it, and loses its tag.")
	}
	q := ask(lines...)
	q.Bind += "\x00" + sha
	return q
}

// AskDeleteLabel asks before delete_label. The count is shown and not
// bound: a label that is applied while the person reads would never be
// confirmed. The label is bound by its name.
func AskDeleteLabel(project, name string, openIssues *int) Question {
	count := ""
	if openIssues != nil {
		count = fmt.Sprintf("It is on %d open issues. ", *openIssues)
	}
	q := ask(
		fmt.Sprintf("delete_label: delete the label %s in %s for good?", quoted(name, quotedLen), quoted(project, quotedLen)),
		count+"It comes off every issue and merge request, and cannot be restored.",
	)
	q.Bind = "delete_label\x00" + project + "\x00" + name
	return q
}

// AskDeleteMilestone asks before delete_milestone.
func AskDeleteMilestone(project, title string) Question {
	return ask(
		fmt.Sprintf("delete_milestone: delete the milestone %s in %s for good?", quoted(title, quotedLen), quoted(project, quotedLen)),
		"Its issues and merge requests stay, without a milestone.",
	)
}

// AskDeleteWikiPage asks before delete_wiki_page. The page's content is
// bound by the witness delete_wiki_page already holds.
func AskDeleteWikiPage(project, title, slug string) Question {
	return ask(
		fmt.Sprintf("delete_wiki_page: delete the wiki page %s in %s for good?", quoted(title, quotedLen), quoted(project, quotedLen)),
		"slug: "+quoted(slug, quotedLen),
		"Its history goes with it.",
	)
}

// AskDeleteSnippet asks before delete_snippet; project is empty for a
// personal snippet. The whole title and every file name are bound.
func AskDeleteSnippet(project string, id int64, title string, files []string) Question {
	where := "your personal snippets"
	if project != "" {
		where = quoted(project, quotedLen)
	}
	q := ask(
		fmt.Sprintf("delete_snippet: delete snippet %d, %s, in %s for good?", id, quoted(title, quotedLen), where),
		"files: "+quoted(strings.Join(files, ", "), quotedLen),
		"Its history goes with it.",
	)
	q.Bind += "\x00" + title + "\x00" + strings.Join(files, "\x00")
	return q
}

// AskDeleteComment asks before delete_comment, showing the start of the
// comment; the whole comment is bound. kind is "issue" or
// "merge_request".
func AskDeleteComment(project, kind string, iid int64, author, body string) Question {
	on := fmt.Sprintf("issue #%d", iid)
	if kind == "merge_request" {
		on = fmt.Sprintf("merge request !%d", iid)
	}
	q := ask(
		fmt.Sprintf("delete_comment: delete a comment on %s in %s for good?", on, quoted(project, quotedLen)),
		"by "+quoted(author, quotedLen),
		body0(body),
	)
	q.Bind += "\x00" + sum(body)
	return q
}

// body0 is the start of a body, quoted on one line, and how much more
// there is.
func body0(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return "text: empty"
	}
	if n := utf8.RuneCountInString(body); n > bodyLen {
		return fmt.Sprintf("text: %s (%d more characters)", quoted(string([]rune(body)[:bodyLen]), bodyLen), n-bodyLen)
	}
	return "text: " + quoted(body, bodyLen)
}

// sum binds a whole text, of which a question shows the start.
func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// ask builds a question from its lines, closes it with what its quotes
// mean, sets its lines apart, and binds it to its text.
func ask(lines ...string) Question {
	text := strings.Join(lines, "\n")
	if strings.Contains(text, "`") {
		text += "\nText in backticks or code style is quoted as written, and is not this server's."
	}
	text = strings.ReplaceAll(text, "\n", "\n\n") + "\n"
	return Question{Text: text, Bind: text}
}

// shaShape is a commit id as GitLab writes it.
var shaShape = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// askSHA is a commit id, cut as shortSHA cuts it. Anything not shaped
// like one, which GitLab never returns, is shown as a placeholder.
func askSHA(s string) string {
	if !shaShape.MatchString(s) {
		return "(unreadable sha)"
	}
	return shortSHA(s)
}

// quotedList is values quoted and comma-joined.
func quotedList(ss []string) string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = quoted(s, quotedLen)
	}
	return strings.Join(out, ", ")
}

// quoted is text from GitLab or from a call's arguments, shown in a
// question put to the person (§4.12), where no boundary can go: a client
// draws the question as plain text in a dialog, or as Markdown. It
// stands in a code span, `like this`, which Markdown shows literally —
// no emphasis, link, HTML or entity — and plain text shows as it is. It
// is made one line; every backtick, grave or acute mark and quote mark
// a reader could take for one becomes a plain single quote, so it
// cannot close its span or seem to; and a URL scheme, a mailto:, a
// leading "www." and a bare domain followed by a path are broken so no
// client draws a link. It is cut at max runes. Text with nothing to show
// is said in words, since an empty span is two backticks Markdown shows
// as they are: "empty" when it is blank, and "invisible characters
// only" when it is not.
func quoted(s string, max int) string {
	blank := strings.TrimSpace(s) == ""
	// Line cuts at max-1 and adds the ellipsis; the value keeps max.
	s, _ = Line(s, max+1)
	s = strings.Join(strings.Fields(blankMarks.Replace(s)), " ")
	s = quoteMarks.Replace(s)
	s = linkShape.ReplaceAllString(s, "${1}${2}[:]//")
	s = mailtoShape.ReplaceAllString(s, "${1}${2}[:]")
	s = wwwShape.ReplaceAllString(s, "${1}${2}[.]")
	s = pathShape.ReplaceAllString(s, "${1}${2}[.]${3}${4}")
	switch {
	case s == "" && blank:
		return "empty"
	case s == "":
		return "invisible characters only"
	}
	return "`" + s + "`"
}

var (
	// quoteMarks folds every backtick, grave or acute mark and quotation
	// mark a reader could take for the question's own to a plain single
	// quote.
	quoteMarks = strings.NewReplacer("`", "'", "\u02cb", "'", "\uff40", "'", "\u1fef", "'", "\u00b4", "'",
		"\u02ca", "'", "\u02f4", "'", "\u02f5", "'", "\u1ffd", "'", "\u1fed", "'", "\u1fee", "'",
		"\u0384", "'", "\u0385", "'", `"`, "'", "\u2018", "'", "\u2019", "'", "\u201a", "'", "\u201b", "'",
		"\u201c", "'", "\u201d", "'", "\u201e", "'", "\u201f", "'", "\u2032", "'", "\u2033", "'",
		"\u00ab", "'", "\u00bb", "'", "\u2039", "'", "\u203a", "'", "\u301d", "'", "\u301e", "'",
		"\u301f", "'", "\uff02", "'", "\uff07", "'", "\u02b9", "'", "\u02ba", "'", "\u02ee", "'",
		"\u05f3", "'", "\u05f4", "'", "\u2035", "'", "\u2036", "'", "\u275b", "'", "\u275c", "'",
		"\u275d", "'", "\u275e", "'", "\u3003", "'")
	// blankMarks are characters drawn as blank space that hidden does not
	// remove; they become spaces and collapse with the rest.
	blankMarks = strings.NewReplacer("\u2800", " ", "\u3164", " ", "\uffa0", " ")
	// Each shape starts where a letter or digit does not precede it,
	// spelled out because \b is ASCII-only and counts "_" as a letter: a
	// link after an underscore or in a non-Latin script is found too.
	//
	// linkShape is a URL scheme followed by //, as a client links it.
	linkShape = regexp.MustCompile(`(?i)(^|[^\p{L}\p{N}+.-])([a-z][a-z0-9+.-]*)://`)
	// mailtoShape is a mail link without //.
	mailtoShape = regexp.MustCompile(`(?i)(^|[^\p{L}\p{N}])(mailto):`)
	// wwwShape is a host a client links without a scheme.
	wwwShape = regexp.MustCompile(`(?i)(^|[^\p{L}\p{N}])(www)\.`)
	// pathShape is a bare domain followed by a path, a port, a query or a
	// fragment, x.example/..., which a client links too; its last dot is
	// broken. Letters from any script count, so a non-ASCII domain is
	// broken as well.
	pathShape = regexp.MustCompile(`(?i)(^|[^\p{L}\p{N}-])([\p{L}\p{N}-]+(?:\.[\p{L}\p{N}-]+)*)\.(\p{L}{2,63})([/:?#])`)
)
