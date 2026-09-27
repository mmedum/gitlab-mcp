package quickaction

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// scanParagraph ports the regular expression GitLab's extractor runs over
// one paragraph's source text with gsub:
//
//	(?<inline_code> `.+?` )
//	| (?<html> ^<[^>]+?>\n .+? \n<\/[^>]+?>$ )
//	| ^\/ (?<cmd> NAMES ) (?: [ ] (?<arg>[^\n]*) )? (?:\s*\n|$)
//
// with Ruby's flags m, i and x: the dot crosses lines, ^ and $ are line
// anchors, and NAMES is every command and substitution GitLab knows.
//
// gsub tries each position in turn and resumes after a match, so what a
// line is depends on what came before it. Whether a /word line matches
// depends on NAMES, which this package does not know, so the scan follows
// both outcomes: the line is consumed as a command, or the scan walks on
// through it as text. A line is reported when some path reaches its start
// outside inline code and the HTML pattern. The set of reachable
// positions is computed in one forward pass, as every step moves forward.
func scanParagraph(s string) []hit {
	n := len(s)
	nextTick := nextIndex(s, '`')
	nextGT := nextIndex(s, '>')
	nextClose := closingTagLines(s, nextGT)

	reach := make([]bool, n+1)
	reach[0] = true
	var hits []hit
	line := 0
	for p := 0; p < n; p++ {
		if p > 0 && s[p-1] == '\n' {
			line++
		}
		if !reach[p] {
			continue
		}
		lineStart := p == 0 || s[p-1] == '\n'

		switch {
		case s[p] == '`':
			// `.+?` : the first backtick after at least one character.
			if p+2 <= n {
				if j := nextTick[p+2]; j >= 0 {
					reach[j+1] = true
					continue
				}
			}
		case s[p] == '<' && lineStart:
			if end := htmlExclusionEnd(s, p, nextGT, nextClose); end >= 0 {
				reach[end] = true
				continue
			}
		case s[p] == '/' && lineStart:
			if name, end, ok := commandAt(s, p); ok {
				hits = append(hits, hit{line: line, command: name})
				reach[end] = true
			}
		}
		reach[p+1] = true
	}
	return hits
}

type hit struct {
	line    int // 0-based, within the paragraph text
	command string
}

// nextIndex returns, for each position i in 0..len(s), the index of the
// first c at or after i, or -1.
func nextIndex(s string, c byte) []int {
	next := make([]int, len(s)+1)
	next[len(s)] = -1
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == c {
			next[i] = i
		} else {
			next[i] = next[i+1]
		}
	}
	return next
}

// closingTagLines returns, for each position i, the first q at or after i
// where "\n</[^>]+?>$" matches, or -1.
func closingTagLines(s string, nextGT []int) []int {
	n := len(s)
	next := make([]int, n+1)
	next[n] = -1
	for q := n - 1; q >= 0; q-- {
		next[q] = next[q+1]
		if s[q] != '\n' || q+3 >= n || s[q+1] != '<' || s[q+2] != '/' || s[q+3] == '>' {
			continue
		}
		if r := nextGT[q+3]; r >= 0 && (r+1 == n || s[r+1] == '\n') {
			next[q] = q
		}
	}
	return next
}

// htmlExclusionEnd matches ^<[^>]+?>\n.+?\n<\/[^>]+?>$ at p and returns
// where the match ends, or -1. The lazy quantifiers make each step the
// first candidate: the first '>' after the '<', then the first closing
// line after at least one character.
func htmlExclusionEnd(s string, p int, nextGT, nextClose []int) int {
	n := len(s)
	if p+1 >= n || s[p+1] == '>' {
		return -1
	}
	g := nextGT[p+1]
	if g < 0 || g+1 >= n || s[g+1] != '\n' {
		return -1
	}
	e := g + 2 // after the opening line
	if e+1 > n {
		return -1
	}
	q := nextClose[e+1]
	if q < 0 {
		return -1
	}
	return nextGT[q+3] + 1
}

// commandAt matches a command line at p, which starts a line with '/'.
// The name is any run of letters, marks, digits and underscores: that
// covers every GitLab command name, and every Unicode character Ruby's
// case-insensitive match would fold onto one (the Kelvin sign, the long
// s). end is where GitLab's match would end: past the line and any
// following lines of only white space.
func commandAt(s string, p int) (name string, end int, ok bool) {
	i := p + 1
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.Is(unicode.M, r) {
			break
		}
		i += size
	}
	if i == p+1 {
		return "", 0, false
	}
	name = strings.ToLower(s[p+1 : i])

	// (?: [ ] (?<arg>[^\n]*) )? takes the rest of the line when a space
	// follows the name.
	t := i
	if t < len(s) && s[t] == ' ' {
		for t < len(s) && s[t] != '\n' {
			t++
		}
	}
	// (?:\s*\n|$): back off from the longest run of white space to its
	// last line feed.
	last := -1
	for j := t; j < len(s) && isRubySpace(s[j]); j++ {
		if s[j] == '\n' {
			last = j
		}
	}
	switch {
	case last >= 0:
		return name, last + 1, true
	case t == len(s):
		return name, t, true
	}
	return "", 0, false
}

// isRubySpace is \s in a Ruby regular expression.
func isRubySpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\v' || b == '\f' || b == '\r'
}
