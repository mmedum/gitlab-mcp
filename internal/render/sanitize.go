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
	"regexp"
	"strings"
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
// comments outside code fences are dropped, hidden characters are
// dropped everywhere, and links show their destination. It returns the
// text and how many characters were dropped.
//
// Comments inside a fence are left, because there they are visible: the
// rule is to remove what a person reading the page would not see and
// the model would.
func Markdown(s string, self string) (string, int) {
	s, n := dropComments(s)
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

// Line prepares a one-line field someone else wrote, such as a title:
// hidden characters dropped, whitespace runs folded to one space, and
// the result cut to max characters. It returns the line and how many
// characters were dropped.
func Line(s string, max int) (string, int) {
	n := 0
	var b strings.Builder
	space := false
	for _, r := range strings.ToValidUTF8(s, "\uFFFD") {
		switch {
		case hidden(r):
			n++
		case r == '\n' || r == '\r' || r == '\t' || r == ' ' || r < 0x20 || r == 0x7f:
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

// fenceOpen returns the fence a line opens, "```" or "~~~", or "". Up
// to three spaces of indent are allowed, as CommonMark allows.
func fenceOpen(line string) string {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return ""
	}
	for _, f := range []string{"```", "~~~"} {
		if strings.HasPrefix(trimmed, f) {
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
	s = inlineLink.ReplaceAllStringFunc(s, func(m string) string {
		p := inlineLink.FindStringSubmatch(m)
		image, text, dest := p[1] == "!", p[2], p[3]
		where := destination(dest, self)
		if image {
			return "[image: " + text + " (" + where + ")]"
		}
		return text + " (" + where + ")"
	})
	return refDefinition.ReplaceAllStringFunc(s, func(m string) string {
		p := refDefinition.FindStringSubmatch(m)
		return p[1] + destination(p[2], self)
	})
}

// destination describes a link target without its path, unless it is
// on this instance.
func destination(dest, self string) string {
	lower := strings.ToLower(dest)
	switch {
	case strings.HasPrefix(lower, "mailto:"):
		return "email link"
	case strings.HasPrefix(dest, "#"):
		return "link within this text"
	case strings.HasPrefix(dest, "/") && !strings.HasPrefix(dest, "//"):
		return "link on this instance: " + dest
	}
	scheme, rest, ok := strings.Cut(dest, "://")
	if !ok {
		if strings.HasPrefix(dest, "//") {
			scheme, rest = "", dest[2:]
		} else {
			return "relative link: " + dest
		}
	}
	host, path, _ := strings.Cut(rest, "/")
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	host = strings.ToLower(host)
	if self != "" && host == strings.ToLower(self) {
		return "link on this instance: /" + path
	}
	if scheme != "" && scheme != "http" && scheme != "https" {
		return scheme + " link to " + host
	}
	return "link to " + host
}
