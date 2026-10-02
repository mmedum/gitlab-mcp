// Package render turns the server's model into the readable half of a
// tool result, and prepares text other people wrote before anything
// shows it (docs/architecture.md §4.1, §4.8).
//
// The preparation is the part that matters for safety: hidden text is
// removed and counted, links show where they go, long text is cut at a
// budget that is stated, and everything someone else wrote is rendered
// inside a boundary whose token is drawn per call, so the content
// cannot close it.
package render

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// hidden reports a rune a reader of the rendered page would not see:
// zero-width characters, bidirectional controls (the Trojan Source
// shapes), invisible operators, the soft hyphen and the Unicode tag
// block, which spells ASCII no font draws (§4.1.2).
func hidden(r rune) bool {
	switch r {
	case 0x00AD, // soft hyphen
		0x061C,                 // Arabic letter mark
		0x180E,                 // Mongolian vowel separator
		0x200B, 0x200C, 0x200D, // zero-width space, non-joiner, joiner
		0x200E, 0x200F, // left-to-right and right-to-left marks
		0x2060, 0x2061, 0x2062, 0x2063, // word joiner, invisible operators
		0x2064, 0xFEFF: // invisible plus, byte-order mark
		return true
	}
	return (r >= 0x202A && r <= 0x202E) || // embeddings and overrides
		(r >= 0x2066 && r <= 0x2069) || // isolates
		(r >= 0xE0000 && r <= 0xE007F) // tags
}

// Markdown prepares Markdown someone else wrote for display: HTML
// comments and reference definitions no link uses, outside code fences,
// are dropped, hidden characters are
// dropped everywhere, and links show their destination. It returns the
// text and how many characters were dropped.
//
// Comments inside a fence are left, because there they are visible: the
// rule is to remove what a person reading the page would not see and
// the model would.
func Markdown(s string, self string) (string, int) {
	s, n := dropComments(s)
	s, defs := dropUnusedDefinitions(s)
	n += defs
	out := strings.Builder{}
	out.Grow(len(s))
	for _, r := range s {
		if hidden(r) {
			n++
			continue
		}
		out.WriteRune(r)
	}
	return Links(strings.ToValidUTF8(out.String(), "\uFFFD"), self), n
}

// Code prepares text that is not rendered as Markdown — file contents,
// diffs, commit messages. Nothing is dropped, because the text may be
// edited later on the strength of what was shown; each hidden character
// is written out as <U+202E> instead, which is also how a reviewer
// would want to see it. It returns the text and how many were made
// visible.
func Code(s string) (string, int) {
	n := 0
	out := strings.Builder{}
	out.Grow(len(s))
	for _, r := range strings.ToValidUTF8(s, "\uFFFD") {
		if hidden(r) {
			n++
			fmt.Fprintf(&out, "<U+%04X>", r)
			continue
		}
		out.WriteRune(r)
	}
	return out.String(), n
}

// SuggestionLen caps each text of a suggestion a result shows.
const SuggestionLen = 2000

// SuggestionText prepares a suggestion's two texts as Code prepares
// code, each cut at SuggestionLen characters: what a reader sees is what
// applying it commits.
func SuggestionText(from, to string) (fromShown, toShown string, cut bool, hidden int) {
	one := func(s string) string {
		s, n := Code(s)
		hidden += n
		if r := []rune(s); len(r) > SuggestionLen {
			cut = true
			return string(r[:SuggestionLen])
		}
		return s
	}
	return one(from), one(to), cut, hidden
}

// Invisible lists the characters of s a reader would not see, each once,
// in order: hidden and bidirectional ones, characters drawn as blank
// space, and controls other than tab, line feed and carriage return.
// Unicode's other default-ignorable characters, the Hangul fillers among
// them, and the variation selectors are included: a filler is an
// identifier that reads as nothing, and a selector makes two names
// that read the same (\u00A74.12).
func Invisible(s string) []rune {
	var out []rune
	for _, r := range strings.ToValidUTF8(s, "\uFFFD") {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
		case hidden(r), control(r), r == 0x2800,
			unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r), unicode.Is(unicode.Variation_Selector, r):
			if !slices.Contains(out, r) {
				out = append(out, r)
			}
		}
	}
	return out
}

// Line prepares a one-line field someone else wrote, such as a title:
// hidden characters dropped, runs of spaces and control characters (line
// and paragraph separators included) folded to one space, and the result
// cut to max characters. It returns the line and how many characters
// were dropped.
func Line(s string, max int) (string, int) {
	n := 0
	var b strings.Builder
	space := false
	for _, r := range strings.ToValidUTF8(s, "\uFFFD") {
		switch {
		case hidden(r):
			n++
		case r == ' ' || control(r):
			space = b.Len() > 0
		default:
			if space {
				b.WriteByte(' ')
				space = false
			}
			b.WriteRune(r)
		}
	}
	out := b.String()
	if max > 0 && utf8.RuneCountInString(out) > max {
		out = string([]rune(out)[:max-1]) + "…"
	}
	return out, n
}

// Ident prepares a name someone else chose — a file path, a branch, a
// label, a topic — for one line of text. Nothing is dropped, folded or
// cut, since the name may be passed back to a tool; each control or
// hidden character is written out as <U+000A> instead, so a newline in a
// file name cannot start a line that reads as the server's.
func Ident(s string) string {
	clean := true
	for _, r := range s {
		if hidden(r) || control(r) || r == utf8.RuneError {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var out strings.Builder
	for _, r := range strings.ToValidUTF8(s, "\uFFFD") {
		if hidden(r) || control(r) {
			fmt.Fprintf(&out, "<U+%04X>", r)
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

// control reports a C0 or C1 control character or a Unicode line or
// paragraph separator: anything that can break a line or move the
// cursor.
func control(r rune) bool {
	return r < 0x20 || (r >= 0x7f && r < 0xa0) || r == 0x2028 || r == 0x2029
}

// dropComments removes HTML comments outside fenced code blocks and
// counts the characters removed. An unterminated comment runs to the
// end, as CommonMark reads it and GitLab hides it.
func dropComments(s string) (string, int) {
	if !strings.Contains(s, "<!--") {
		return s, 0
	}
	var out strings.Builder
	removed := 0
	fence := ""
	inComment := false
	for line := range strings.SplitAfterSeq(s, "\n") {
		if inComment {
			end := strings.Index(line, "-->")
			if end < 0 {
				removed += utf8.RuneCountInString(line)
				continue
			}
			removed += utf8.RuneCountInString(line[:end+3])
			line = line[end+3:]
			inComment = false
		} else if fence != "" {
			out.WriteString(line)
			if strings.HasPrefix(strings.TrimLeft(line, " "), fence) {
				fence = ""
			}
			continue
		} else if f := fenceOpen(line); f != "" {
			fence = f
			out.WriteString(line)
			continue
		}
		for {
			start := strings.Index(line, "<!--")
			if start < 0 {
				out.WriteString(line)
				break
			}
			out.WriteString(line[:start])
			rest := line[start+4:]
			end := strings.Index(rest, "-->")
			if end < 0 {
				removed += utf8.RuneCountInString(line[start:])
				inComment = true
				break
			}
			removed += 4 + utf8.RuneCountInString(rest[:end+3])
			line = rest[end+3:]
		}
	}
	return out.String(), removed
}

// refDefinitionLine matches a reference definition, [label]: dest, and
// captures its label and what follows the destination.
var refDefinitionLine = regexp.MustCompile(`^ {0,3}\[([^\]\n]+)\]:[ \t]*(?:<[^>\n]*>|\S+)(.*)$`)

// refUse matches what may use a definition: [label], [text][label].
var refUse = regexp.MustCompile(`\[([^\]\n]+)\]`)

// dropUnusedDefinitions removes reference definitions outside fences
// that no link uses, and counts their characters. GitLab renders one as
// nothing, which makes [//]: # (text) a comment by another name. A title
// left open runs on to the line that closes it or to a blank line.
func dropUnusedDefinitions(s string) (string, int) {
	if !strings.Contains(s, "]:") {
		return s, 0
	}
	lines := strings.SplitAfter(s, "\n")
	label := make([]string, len(lines)) // "" where the line is no definition
	fence := ""
	for i, line := range lines {
		switch {
		case fence != "":
			if strings.HasPrefix(strings.TrimLeft(line, " "), fence) {
				fence = ""
			}
		case fenceOpen(line) != "":
			fence = fenceOpen(line)
		default:
			if m := refDefinitionLine.FindStringSubmatch(strings.TrimRight(line, "\r\n")); m != nil {
				label[i] = refLabel(m[1])
			}
		}
	}
	used := map[string]bool{}
	for i, line := range lines {
		if label[i] != "" {
			continue
		}
		for _, m := range refUse.FindAllStringSubmatch(line, -1) {
			used[refLabel(m[1])] = true
		}
	}
	var out strings.Builder
	removed := 0
	for i := 0; i < len(lines); i++ {
		if label[i] == "" || used[label[i]] {
			out.WriteString(lines[i])
			continue
		}
		removed += utf8.RuneCountInString(lines[i])
		closer := openTitle(lines[i])
		for closer != 0 && i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != "" && label[i+1] == "" {
			i++
			removed += utf8.RuneCountInString(lines[i])
			if strings.ContainsRune(lines[i], closer) {
				closer = 0
			}
		}
	}
	return out.String(), removed
}

// refLabel normalizes a label as CommonMark matches it: case and runs
// of whitespace do not count.
func refLabel(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// openTitle returns the character that closes a definition's title the
// line opens and does not close, or 0.
func openTitle(line string) rune {
	m := refDefinitionLine.FindStringSubmatch(strings.TrimRight(line, "\r\n"))
	if m == nil {
		return 0
	}
	rest := strings.TrimSpace(m[2])
	if rest == "" {
		return 0
	}
	closer := map[byte]rune{'"': '"', '\'': '\'', '(': ')'}[rest[0]]
	if closer == 0 || strings.ContainsRune(rest[1:], closer) {
		return 0
	}
	return closer
}

// fenceOpen returns the fence a line opens, "```" or "~~~", or "". Up
// to three spaces of indent are allowed, as CommonMark allows.
func fenceOpen(line string) string {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return ""
	}
	for _, f := range []string{"```", "~~~"} {
		if strings.HasPrefix(trimmed, f) {
			// A backtick fence's info string may not hold a backtick; a
			// line with one opens nothing (CommonMark 4.5), and text after
			// it is rendered, comments hidden.
			if f == "```" && strings.Contains(strings.TrimLeft(trimmed, "`"), "`") {
				return ""
			}
			return f
		}
	}
	return ""
}

// inlineLink matches [text](dest "title") and ![alt](dest). The
// destination may be wrapped in <>.
var inlineLink = regexp.MustCompile(`(!?)\[([^\]\n]*)\]\(\s*<?([^\s)>]+)>?(?:\s+(?:"[^"\n]*"|'[^'\n]*'))?\s*\)`)

// refDefinition matches a reference definition line: [id]: dest.
var refDefinition = regexp.MustCompile(`(?m)^( {0,3}\[[^\]\n]+\]:[ \t]*)<?([^\s>]+)>?`)

// Links rewrites Markdown links so the text shows where they go
// (§4.1.3). A link whose text says one thing and whose destination is
// another is the cheapest deception there is, and nothing is ever
// fetched, so what the reader needs is the host. A link on the
// configured instance, self, keeps its path, because that is how an
// issue points at another issue.
func Links(s, self string) string {
	if !strings.Contains(s, "](") && !strings.Contains(s, "]:") {
		return s
	}
	s = replaceMatches(inlineLink, s, func(p []string) string {
		image, text, dest := p[1] == "!", p[2], p[3]
		where := destination(dest, self)
		if image {
			return "[image: " + text + " (" + where + ")]"
		}
		return text + " (" + where + ")"
	})
	return replaceMatches(refDefinition, s, func(p []string) string {
		return p[1] + destination(p[2], self)
	})
}

// replaceMatches replaces every match of re in s with what repl makes of
// its submatches, matching each once.
func replaceMatches(re *regexp.Regexp, s string, repl func(submatches []string) string) string {
	matches := re.FindAllStringSubmatchIndex(s, -1)
	if matches == nil {
		return s
	}
	var out strings.Builder
	last := 0
	for _, m := range matches {
		p := make([]string, len(m)/2)
		for i := range p {
			if m[2*i] >= 0 {
				p[i] = s[m[2*i]:m[2*i+1]]
			}
		}
		out.WriteString(s[last:m[0]])
		out.WriteString(repl(p))
		last = m[1]
	}
	out.WriteString(s[last:])
	return out.String()
}

// destination describes a link target without its path, unless it is
// on this instance. self is the instance's host[:port], with the port
// only when it is not the scheme's default, as Instance.Host writes it.
func destination(dest, self string) string {
	// A browser reads a backslash as a slash in an http(s) link, so /\host
	// is another host and the authority can end at a backslash.
	dest = strings.ReplaceAll(dest, `\`, "/")
	switch {
	case strings.HasPrefix(strings.ToLower(dest), "mailto:"):
		return "email link"
	case strings.HasPrefix(dest, "#"):
		return "link within this text"
	case strings.HasPrefix(dest, "/") && !strings.HasPrefix(dest, "//"):
		return "link on this instance: " + dest
	}
	// url.Parse reads the authority as a browser does: it ends at the
	// first of / ? #, and userinfo runs to the last @ inside it.
	u, err := url.Parse(dest)
	switch {
	case err != nil:
		return "link to an address that could not be read"
	case u.Host == "" && u.Scheme == "":
		return "relative link: " + dest
	case u.Host == "":
		return u.Scheme + " link"
	}
	if onInstance(u, self) {
		path := u.EscapedPath()
		if u.ForceQuery || u.RawQuery != "" {
			path += "?" + u.RawQuery
		}
		if u.Fragment != "" {
			path += "#" + u.EscapedFragment()
		}
		return "link on this instance: /" + strings.TrimPrefix(path, "/")
	}
	host := strings.ToLower(u.Host)
	if u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https" {
		return u.Scheme + " link to " + host
	}
	return "link to " + host
}

// onInstance reports whether u is on the host self names, on the same
// port once each side's default is filled in.
func onInstance(u *url.URL, self string) bool {
	if self == "" {
		return false
	}
	s, err := url.Parse("//" + self)
	if err != nil {
		return false
	}
	hostname := func(v *url.URL) string { return strings.TrimSuffix(strings.ToLower(v.Hostname()), ".") }
	port := func(v *url.URL) string {
		if p := v.Port(); p != "" {
			return p
		}
		return map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	return hostname(u) == hostname(s) && port(u) == port(s)
}
