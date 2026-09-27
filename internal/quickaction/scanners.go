package quickaction

import (
	"regexp"
	"strings"
)

// Ports of the comrak scanners the block parser uses. comrak's scanners are
// re2c matchers anchored at the start of the string, case-insensitive, and
// treat the end of the string as a line end; the ports keep all three.

func byteAt(s string, i int) byte {
	if i < 0 || i >= len(s) {
		return 0
	}
	return s[i]
}

func isLineEnd(b byte) bool { return b == '\n' || b == '\r' }

func isSpaceOrTab(b byte) bool { return b == ' ' || b == '\t' }

// isSpace is cmark's isspace: tab, line feed, vertical tab, form feed,
// carriage return and space.
func isSpace(b byte) bool { return b == ' ' || (b >= '\t' && b <= '\r') }

func isPunct(b byte) bool {
	return (b >= '!' && b <= '/') || (b >= ':' && b <= '@') || (b >= '[' && b <= '`') || (b >= '{' && b <= '~')
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// atLineEnd reports whether s[i:] is empty or starts with a line end.
func atLineEnd(s string, i int) bool { return i >= len(s) || isLineEnd(s[i]) }

func newlinesOf(s string) int {
	switch {
	case strings.HasSuffix(s, "\r\n"):
		return 2
	case strings.HasSuffix(s, "\n"), strings.HasSuffix(s, "\r"):
		return 1
	}
	return 0
}

func countNewlines(b []byte) int {
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	return n
}

func runOf(s string, c byte) int {
	n := 0
	for n < len(s) && s[n] == c {
		n++
	}
	return n
}

func skipSpaceOrTab(s string, i int) int {
	for i < len(s) && isSpaceOrTab(s[i]) {
		i++
	}
	return i
}

// openCodeFence: [`]{3,} / [^`\r\n]*[\r\n] | [~]{3,} / [^\r\n]*[\r\n].
func openCodeFence(s string) int {
	c := byteAt(s, 0)
	if c != '`' && c != '~' {
		return 0
	}
	n := runOf(s, c)
	if n < 3 {
		return 0
	}
	if c == '`' {
		for i := n; i < len(s) && !isLineEnd(s[i]); i++ {
			if s[i] == '`' {
				return 0
			}
		}
	}
	return n
}

// closeCodeFence: [`]{3,} / [ \t]*[\r\n] and the same for ~.
func closeCodeFence(s string) int {
	c := byteAt(s, 0)
	if c != '`' && c != '~' {
		return 0
	}
	n := runOf(s, c)
	if n < 3 || !atLineEnd(s, skipSpaceOrTab(s, n)) {
		return 0
	}
	return n
}

// multilineQuoteFence opens and closes GitLab's >>> quotes:
// [>]{3,} / [ \t]*[\r\n].
func multilineQuoteFence(s string) int {
	n := runOf(s, '>')
	if n < 3 || !atLineEnd(s, skipSpaceOrTab(s, n)) {
		return 0
	}
	return n
}

var alertTypes = []string{"note", "tip", "important", "warning", "caution"}

// alertStart: [>]{1,} ' [!note]' and the other alert types. It returns the
// number of > characters, which comrak calls the fence length.
func alertStart(s string) (int, bool) {
	n := runOf(s, '>')
	if n == 0 || !strings.HasPrefix(s[n:], " [!") {
		return 0, false
	}
	rest := s[n+3:]
	for _, t := range alertTypes {
		if len(rest) > len(t) && asciiLower(rest[:len(t)]) == t && rest[len(t)] == ']' {
			return n, true
		}
	}
	return 0, false
}

// atxHeadingStart: [#]{1,6} ([ \t]+ | [\r\n] | end).
func atxHeadingStart(s string) int {
	n := runOf(s, '#')
	if n == 0 || n > 6 {
		return 0
	}
	switch {
	case n == len(s):
		return n
	case isSpaceOrTab(s[n]):
		return skipSpaceOrTab(s, n)
	case isLineEnd(s[n]):
		return n + 1
	}
	return 0
}

// setextHeadingLine: [=]+ [ \t]* [\r\n] | [-]+ [ \t]* [\r\n].
func setextHeadingLine(s string) bool {
	c := byteAt(s, 0)
	if c != '=' && c != '-' {
		return false
	}
	return atLineEnd(s, skipSpaceOrTab(s, runOf(s, c)))
}

// thematicBreak is comrak's scan_thematic_break_inner.
func thematicBreak(s string) bool {
	c := byteAt(s, 0)
	if c != '*' && c != '_' && c != '-' {
		return false
	}
	count := 1
	i := 1
	for ; i < len(s); i++ {
		if s[i] == c {
			count++
		} else if !isSpaceOrTab(s[i]) {
			break
		}
	}
	return count >= 3 && atLineEnd(s, i)
}

// footnoteDefinition: '[^' [^\] \r\n\t]+ ']:' [ \t]*.
func footnoteDefinition(s string) int {
	if !strings.HasPrefix(s, "[^") {
		return 0
	}
	i := 2
	for i < len(s) && !strings.ContainsRune("] \r\n\t", rune(s[i])) {
		i++
	}
	if i == 2 || !strings.HasPrefix(s[i:], "]:") {
		return 0
	}
	return skipSpaceOrTab(s, i+2)
}

// descriptionItemStart: [:~] [ \t]+.
func descriptionItemStart(s string) int {
	c := byteAt(s, 0)
	if c != ':' && c != '~' {
		return 0
	}
	n := skipSpaceOrTab(s, 1)
	if n == 1 {
		return 0
	}
	return n
}

// listMarker is the part of comrak's NodeList the parser compares.
type listMarker struct {
	ordered      bool
	bulletChar   byte
	delimiter    byte
	markerOffset int
	padding      int
}

// parseListMarker is comrak's parse_list_marker.
func parseListMarker(line string, pos int, interruptsParagraph bool) (int, listMarker, bool) {
	if pos >= len(line) {
		return 0, listMarker{}, false
	}
	c := line[pos]
	if c == '*' || c == '-' || c == '+' {
		end := pos + 1
		if end < len(line) && !isSpace(line[end]) {
			return 0, listMarker{}, false
		}
		if interruptsParagraph && emptyItem(line, end) {
			return 0, listMarker{}, false
		}
		return end - pos, listMarker{bulletChar: c}, true
	}
	return parseOrderedMarker(line, pos, interruptsParagraph)
}

func parseOrderedMarker(line string, pos int, interruptsParagraph bool) (int, listMarker, bool) {
	start := pos
	if !isDigit(line[pos]) {
		return 0, listMarker{}, false
	}
	number, digits := 0, 0
	for {
		number = number*10 + int(line[pos]-'0')
		pos++
		digits++
		if pos == len(line) {
			return 0, listMarker{}, false
		}
		if digits >= 9 || !isDigit(line[pos]) {
			break
		}
	}
	if interruptsParagraph && number != 1 {
		return 0, listMarker{}, false
	}
	c := line[pos]
	if c != '.' && c != ')' {
		return 0, listMarker{}, false
	}
	pos++
	if pos == len(line) || !isSpace(line[pos]) {
		return 0, listMarker{}, false
	}
	if interruptsParagraph && emptyItem(line, pos) {
		return 0, listMarker{}, false
	}
	return pos - start, listMarker{ordered: true, delimiter: c}, true
}

// emptyItem reports whether nothing but spaces and tabs follows a list
// marker ending at pos. An empty item cannot interrupt a paragraph.
func emptyItem(line string, pos int) bool {
	i := skipSpaceOrTab(line, pos)
	return i == len(line) || isLineEnd(line[i])
}

const blockTagNames = `address|article|aside|base|basefont|blockquote|body|caption|center|col|colgroup|dd|details|dialog|dir|div|dl|dt|fieldset|figcaption|figure|footer|form|frame|frameset|h1|h2|h3|h4|h5|h6|head|header|hr|html|iframe|legend|li|link|main|menu|menuitem|nav|noframes|ol|optgroup|option|p|param|search|section|title|summary|table|tbody|td|tfoot|th|thead|title|tr|track|ul`

// The patterns spell case-insensitivity out as ASCII classes: Go's (?i)
// also folds letters such as the Kelvin sign, and re2c does not.
var htmlBlockStarts = []*regexp.Regexp{
	1: regexp.MustCompile(`\A<(?:` + asciiFold("script|pre|textarea|style") + `)(?:[ \t\v\f\r\n]|>)`),
	2: regexp.MustCompile(`\A<!--`),
	3: regexp.MustCompile(`\A<\?`),
	4: regexp.MustCompile(`\A<![A-Za-z]`),
	5: regexp.MustCompile(`\A<!\[` + asciiFold("cdata") + `\[`),
	6: regexp.MustCompile(`\A</?(?:` + asciiFold(blockTagNames) + `)(?:[ \t\v\f\r\n]|/?>)`),
}

// asciiFold turns each ASCII letter of a pattern into a class of both
// cases.
func asciiFold(pattern string) string {
	var b strings.Builder
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		if c >= 'a' && c <= 'z' {
			b.WriteString("[" + string(c) + string(c-'a'+'A') + "]")
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// asciiLower lowercases ASCII letters only, as re2c's case-insensitive
// matching does.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// htmlBlockStart returns the CommonMark HTML block type 1 to 6 the line
// opens, or 0.
func htmlBlockStart(s string) int {
	if byteAt(s, 0) != '<' {
		return 0
	}
	for t := 1; t <= 6; t++ {
		if htmlBlockStarts[t].MatchString(s) {
			return t
		}
	}
	return 0
}

const (
	reSpacechar  = `[ \t\v\f\r\n]`
	reTagName    = `[A-Za-z][A-Za-z0-9-]*`
	reAttribute  = reSpacechar + `+[a-zA-Z_:][a-zA-Z0-9:._-]*(?:` + reSpacechar + `*=` + reSpacechar + `*(?:[^ \t\r\n\v\f"'=<>` + "`" + `]+|'[^']*'|"[^"]*"))?`
	reOpenTag    = reTagName + `(?:` + reAttribute + `)*` + reSpacechar + `*/?>`
	reCloseTag   = `/` + reTagName + reSpacechar + `*>`
	reHTMLBlock7 = `\A<(?:` + reOpenTag + `|` + reCloseTag + `)[\t\n\f ]*(?:[\r\n]|\z)`
)

var htmlBlockStart7Re = regexp.MustCompile(reHTMLBlock7)

// htmlBlockStart7 reports a type 7 start: one complete open or close tag
// alone on the line. Type 7 cannot interrupt a paragraph.
func htmlBlockStart7(s string) bool {
	return byteAt(s, 0) == '<' && htmlBlockStart7Re.MatchString(s)
}

// htmlBlockEnds reports whether the line ends an HTML block of types 1 to
// 5. Types 6 and 7 end at a blank line instead.
func htmlBlockEnds(htmlType int, s string) bool {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	switch htmlType {
	case 1:
		l := asciiLower(s)
		return strings.Contains(l, "</script>") || strings.Contains(l, "</pre>") ||
			strings.Contains(l, "</textarea>") || strings.Contains(l, "</style>")
	case 2:
		return strings.Contains(s, "-->")
	case 3:
		return strings.Contains(s, "?>")
	case 4:
		return strings.Contains(s, ">")
	case 5:
		return strings.Contains(s, "]]>")
	}
	return false
}

func isTableSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\v' || b == '\f' }

var tableStartRe = regexp.MustCompile(`\A\|?[ \t\v\f]*:?-+:?[ \t\v\f]*(?:\|[ \t\v\f]*:?-+:?[ \t\v\f]*)*\|?[ \t\v\f]*(?:\r\n|\r|\n|\z)`)

// tableStart reports whether the line is a table delimiter row.
func tableStart(s string) bool { return tableStartRe.MatchString(s) }

// tableCell: (escaped_char | [^|\r\n])+.
func tableCell(s string) int {
	i := 0
	for i < len(s) {
		switch {
		case s[i] == '\\' && i+1 < len(s) && isPunct(s[i+1]):
			i += 2
		case s[i] == '|' || isLineEnd(s[i]):
			return i
		default:
			i++
		}
	}
	return i
}

// tableCellEnd: [|] table_spacechar*.
func tableCellEnd(s string) int {
	if byteAt(s, 0) != '|' {
		return 0
	}
	i := 1
	for i < len(s) && isTableSpace(s[i]) {
		i++
	}
	return i
}

// tableRowEnd: table_spacechar* (\r\n | \r | \n).
func tableRowEnd(s string) int {
	i := 0
	for i < len(s) && isTableSpace(s[i]) {
		i++
	}
	switch {
	case strings.HasPrefix(s[i:], "\r\n"):
		return i + 2
	case i < len(s) && isLineEnd(s[i]):
		return i + 1
	}
	return 0
}

// tableRow is comrak's table::row, reduced to what the parser needs: where
// the last row starts in a multi-line string, how many cells it has, and
// whether s parses as a row at all.
func tableRow(s string) (paragraphOffset, cells int, ok bool) {
	offset := tableCellEnd(s)
	for offset < len(s) {
		cell := tableCell(s[offset:])
		pipe := tableCellEnd(s[offset+cell:])
		if cell > 0 || pipe > 0 {
			if cells == maxRowCells {
				return 0, 0, false
			}
			cells++
		}
		offset += cell + pipe
		if pipe == 0 {
			rowEnd := tableRowEnd(s[offset:])
			offset += rowEnd
			if rowEnd == 0 || offset == len(s) {
				break
			}
			paragraphOffset = offset
			cells = 0
			offset += tableCellEnd(s[offset:])
		}
	}
	if offset != len(s) || cells == 0 {
		return 0, 0, false
	}
	return paragraphOffset, cells, true
}

// isBlank is comrak's strings::is_blank: true at the first line end or the
// end of the string, false at anything but a space or tab before that.
func isBlank(s []byte) bool {
	for _, c := range s {
		switch c {
		case '\n', '\r':
			return true
		case ' ', '\t':
		default:
			return false
		}
	}
	return true
}

const maxLinkLabelLength = 1000

// parseReferenceDefinition is comrak's parse_reference_inline. It returns
// how many bytes one link reference definition at the start of s takes.
func parseReferenceDefinition(s []byte) (int, bool) {
	pos := 0
	label, ok := linkLabel(s, &pos)
	if !ok || len(trimSpace(label)) == 0 {
		return 0, false
	}
	if pos >= len(s) || s[pos] != ':' {
		return 0, false
	}
	pos++
	spnl(s, &pos)
	n, ok := scanLinkURL(s[pos:])
	if !ok {
		return 0, false
	}
	pos += n

	beforeTitle := pos
	spnl(s, &pos)
	title := 0
	if pos != beforeTitle {
		title = linkTitle(s[pos:])
	}
	if title > 0 {
		pos += title
	} else {
		pos = beforeTitle
	}

	skipSpaces(s, &pos)
	if !skipLineEnd(s, &pos) {
		if title == 0 {
			return 0, false
		}
		pos = beforeTitle
		skipSpaces(s, &pos)
		if !skipLineEnd(s, &pos) {
			return 0, false
		}
	}
	return pos, true
}

func trimSpace(b []byte) []byte {
	for len(b) > 0 && isSpace(b[0]) {
		b = b[1:]
	}
	for len(b) > 0 && isSpace(b[len(b)-1]) {
		b = b[:len(b)-1]
	}
	return b
}

func linkLabel(s []byte, pos *int) ([]byte, bool) {
	start := *pos
	if start >= len(s) || s[start] != '[' {
		return nil, false
	}
	i := start + 1
	length := 0
	for i < len(s) {
		switch s[i] {
		case ']':
			*pos = i + 1
			return s[start+1 : i], true
		case '[':
			return nil, false
		case '\\':
			i++
			length++
			if i < len(s) && isPunct(s[i]) {
				i++
				length++
			}
		default:
			i++
			length++
		}
		if length > maxLinkLabelLength {
			return nil, false
		}
	}
	return nil, false
}

func skipSpaces(s []byte, pos *int) {
	for *pos < len(s) && (s[*pos] == ' ' || s[*pos] == '\t') {
		*pos++
	}
}

func skipLineEnd(s []byte, pos *int) bool {
	old := *pos
	if *pos < len(s) && s[*pos] == '\r' {
		*pos++
	}
	if *pos < len(s) && s[*pos] == '\n' {
		*pos++
	}
	return *pos > old || *pos >= len(s)
}

func spnl(s []byte, pos *int) {
	skipSpaces(s, pos)
	if skipLineEnd(s, pos) {
		skipSpaces(s, pos)
	}
}

// scanLinkURL is comrak's manual_scan_link_url.
func scanLinkURL(s []byte) (int, bool) {
	if len(s) > 0 && s[0] == '<' {
		i := 1
	angle:
		for i < len(s) {
			switch s[i] {
			case '>':
				i++
				break angle
			case '\\':
				i += 2
			case '\n', '\r', '<':
				return 0, false
			default:
				i++
			}
		}
		if i >= len(s) {
			return 0, false
		}
		return i, true
	}

	i, parens := 0, 0
scan:
	for i < len(s) {
		switch b := s[i]; {
		case b == '\\' && i+1 < len(s) && isPunct(s[i+1]):
			i += 2
		case b == '(':
			parens++
			i++
			if parens > 32 {
				return 0, false
			}
		case b == ')':
			if parens == 0 {
				break scan
			}
			parens--
			i++
		case isSpace(b) || (b != 0 && (b < 0x20 || b == 0x7f)):
			if i == 0 {
				return 0, false
			}
			break scan
		default:
			i++
		}
	}
	if len(s) == 0 || parens != 0 {
		return 0, false
	}
	return i, true
}

const rePunct = "[!-/:-@\\[-`{-~]"

var linkTitleRe = func() *regexp.Regexp {
	re := regexp.MustCompile(`\A(?:"(?:\\` + rePunct + `|[^"])*"|'(?:\\` + rePunct + `|[^'])*'|\((?:\\` + rePunct + `|[^()])*\))`)
	re.Longest()
	return re
}()

// linkTitle returns the length of the longest link title at the start of
// s, as re2c's longest-match scanner does, or 0.
func linkTitle(s []byte) int {
	loc := linkTitleRe.FindIndex(s)
	if loc == nil {
		return 0
	}
	return loc[1]
}
