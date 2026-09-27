// Package quickaction finds and neutralizes the lines of a Markdown body
// that GitLab would run as quick actions.
//
// GitLab executes /close, /merge, /assign and the rest from note bodies and
// issue and merge request descriptions sent through the REST API, with no
// parameter to turn it off. Every body this server sends passes Check.
//
// Detection follows GitLab's extractor, read at gitlab-org/gitlab
// 829b21d284863743bde419836a053626309d02f6 (2026-09-25):
// lib/gitlab/quick_actions/extractor.rb, and the quick_action Banzai
// pipeline it calls (lib/banzai/pipeline/quick_action_pipeline.rb,
// lib/banzai/filter/quick_action_filter.rb,
// lib/banzai/filter/markdown_filter.rb and
// lib/banzai/filter/markdown_engines/glfm_markdown.rb). The extractor
//
//  1. deletes every carriage return;
//  2. renders the body with comrak and keeps the top-level paragraphs,
//     which leaves out code blocks, block quotes (> and >>>), HTML blocks,
//     lists, headings and tables;
//  3. runs one regular expression over the source lines of each such
//     paragraph, which skips inline code and a paragraph-level HTML
//     pattern and matches a command as ^/name, then a space and
//     arguments, or only white space to the end of the line.
//
// This package ports step 2 from comrak (blocks.go) and step 3 from the
// extractor (scan.go). It deviates from GitLab on purpose in four places,
// each toward finding more lines, never fewer:
//
//   - Any name counts, not only the commands GitLab knows. GitLab's list
//     grows every release and differs by edition and target, and a
//     command that is harmless today is not tomorrow.
//   - Because GitLab consumes a known command's line and not an unknown
//     one's, which backticks pair into inline code depends on the names.
//     scanParagraph follows both possibilities and reports a line if
//     either reaches it.
//   - GitLab first discards paragraphs whose rendered text has no line
//     starting with a slash. That filter is not modeled.
//   - A description list term ("term" above ": details") is searched as a
//     paragraph, since GitLab versions that render without the description
//     list extension see a paragraph there.
package quickaction

import (
	"fmt"
	"slices"
	"strings"
)

// Line is one line of a body that GitLab would run as a quick action.
type Line struct {
	// Number is the 1-based line number in the body.
	Number int
	// Text is the line without its line ending.
	Text string
	// Command is the command name, lowercased, without the slash.
	Command string
}

// Find returns every line of body that GitLab would treat as a quick
// action, in order. CRLF and lone CR line endings are handled as GitLab
// handles them: every carriage return is dropped before parsing.
func Find(body string) []Line {
	lines := splitLines(normalize(body))
	root := parseBlocks(lines)

	var out []Line
	for _, r := range paragraphRanges(root) {
		first, last := r[0], r[1]
		// GitLab takes the blank line after a paragraph along with it.
		if last+1 < len(lines) && lines[last+1] == "\n" {
			last++
		}
		text := strings.Join(lines[first:last+1], "")
		for _, h := range scanParagraph(text) {
			n := first + h.line
			out = append(out, Line{
				Number:  n + 1,
				Text:    strings.TrimSuffix(lines[n], "\n"),
				Command: h.command,
			})
		}
	}
	slices.SortFunc(out, func(a, b Line) int { return a.Number - b.Number })
	return slices.CompactFunc(out, func(a, b Line) bool { return a.Number == b.Number })
}

// Escape puts a backslash before the slash of every line Find reports.
// CommonMark renders "\/" as "/", so the text reads the same, and the
// extractor, which wants the slash first on the line, no longer matches.
// It returns the new body and the lines it escaped, numbered as in body.
// Nothing else in the body changes, line endings included.
//
// The backslash shows only where GitLab runs a command that CommonMark
// renders as code. GitLab pairs backticks one to one, so when a code span
// opens with two backticks and holds one, a /close line inside it runs,
// and the reader sees the backslash in the span. Escaping it is still
// right; the command must not run.
func Escape(body string) (string, []Line) {
	var escaped []Line
	for {
		found := Find(body)
		if len(found) == 0 {
			break
		}
		// One pass suffices: an escaped line reads like a line with an
		// unknown command, a case Find already follows. The loop makes
		// the guarantee not depend on that argument.
		body = escapeLines(body, found)
		escaped = append(escaped, found...)
	}
	slices.SortFunc(escaped, func(a, b Line) int { return a.Number - b.Number })
	return body, escaped
}

// Check is the guard every Markdown body goes through before it is sent.
// With escape false, a body with quick-action lines is refused with a
// *BlockedError and nothing should be sent. With escape true, the body
// comes back escaped. A body without such lines comes back unchanged.
func Check(body string, escape bool) (string, error) {
	if escape {
		out, _ := Escape(body)
		return out, nil
	}
	if lines := Find(body); len(lines) > 0 {
		return "", &BlockedError{Lines: lines}
	}
	return body, nil
}

// maxListed caps how many lines a BlockedError names.
const maxListed = 10

// BlockedError refuses a body that GitLab would run quick actions from.
type BlockedError struct {
	Lines []Line
}

// Error renders the message under the blocked class of
// docs/architecture.md §6.5.
func (e *BlockedError) Error() string { return "[blocked] " + e.Message() }

// Message is the text without the class, for a caller that attaches the
// class itself. It names line numbers and commands, never other text of
// the body.
func (e *BlockedError) Message() string {
	var b strings.Builder
	b.WriteString("GitLab would run ")
	if len(e.Lines) == 1 {
		b.WriteString("a quick action in this text: ")
	} else {
		fmt.Fprintf(&b, "%d quick actions in this text: ", len(e.Lines))
	}
	for i, l := range e.Lines {
		if i == maxListed {
			fmt.Fprintf(&b, " and %d more", len(e.Lines)-maxListed)
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "line %d /%s", l.Number, l.Command)
	}
	b.WriteString(". Nothing was sent. To send these lines as plain text, pass escape_commands: true; ")
	b.WriteString("each gets a leading backslash, which renders the same. To make the change they describe, use the tool for it.")
	return b.String()
}

// Class is the error class, "blocked".
func (e *BlockedError) Class() string { return "blocked" }

// normalize is what the JSON encoder and then the extractor do to a body
// before GitLab parses it: replace invalid UTF-8, and drop every carriage
// return.
func normalize(body string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(body, "\uFFFD"), "\r", "")
}

// splitLines splits after each "\n"; the last line has none when the body
// does not end in one.
func splitLines(s string) []string {
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// escapeLines inserts a backslash before the slash that starts each given
// line. Carriage returns before the slash are kept where they are, as
// GitLab ignores them.
func escapeLines(body string, lines []Line) string {
	parts := strings.SplitAfter(body, "\n")
	for _, l := range lines {
		i := l.Number - 1
		if i < 0 || i >= len(parts) {
			continue
		}
		p := parts[i]
		j := 0
		for j < len(p) && p[j] == '\r' {
			j++
		}
		if j < len(p) && p[j] == '/' {
			parts[i] = p[:j] + `\` + p[j:]
		}
	}
	return strings.Join(parts, "")
}
