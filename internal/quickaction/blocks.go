package quickaction

// This file is a port of the block half of comrak's parser, the Markdown
// engine GitLab renders with, reduced to what decides which lines are
// top-level paragraphs. GitLab's extractor looks for quick actions only in
// those, so every other block — code, quotes, lists, tables, HTML — must be
// recognized exactly as comrak recognizes it, or a command slips through.
//
// Source: comrak v0.55.0 (6fbe87fafde3953a9f3bc582804318593d703805),
// src/parser/mod.rs, src/parser/table.rs and src/scanners.re, the version
// pinned by gitlab-glfm-markdown 0.0.43. The functions below keep comrak's
// names and order so the two can be read side by side. Only the extensions
// GitLab turns on that change block structure are ported: tables,
// footnotes, description lists, multiline block quotes and alerts.

const (
	tabStop      = 4
	codeIndent   = 4
	maxListDepth = 100
	maxRowCells  = 65535
)

type kind uint8

const (
	kindDocument kind = iota
	kindBlockQuote
	kindMultilineQuote
	kindList
	kindItem
	kindDescList
	kindDescItem
	kindDescTerm
	kindDescDetails
	kindFootnote
	kindCodeBlock
	kindHTMLBlock
	kindParagraph
	kindHeading
	kindThematicBreak
	kindTable
	kindTableRow
)

type block struct {
	kind     kind
	parent   *block
	children []*block
	open     bool

	// List, list item and description item.
	ordered      bool
	bulletChar   byte
	delimiter    byte
	markerOffset int
	padding      int

	// Code block and multiline block quote.
	fenced      bool
	fenceChar   byte
	fenceLength int
	fenceOffset int

	htmlType int

	// Paragraph: the 0-based lines it spans and the text comrak keeps.
	firstLine    int
	lastLine     int
	content      []byte
	tableVisited bool
}

func (b *block) lastChild() *block {
	if len(b.children) == 0 {
		return nil
	}
	return b.children[len(b.children)-1]
}

func (b *block) lastChildIsOpen() bool {
	c := b.lastChild()
	return c != nil && c.open
}

func (b *block) acceptsLines() bool {
	return b.kind == kindParagraph || b.kind == kindHeading || b.kind == kindCodeBlock
}

// canContain is comrak's can_contain_type, for block children only.
func (b *block) canContain(child kind) bool {
	switch b.kind {
	case kindDocument, kindBlockQuote, kindMultilineQuote, kindFootnote,
		kindDescTerm, kindDescDetails, kindItem:
		return child != kindItem
	case kindList:
		return child == kindItem
	case kindDescList:
		return child == kindDescItem
	case kindDescItem:
		return child == kindDescTerm || child == kindDescDetails
	case kindTable:
		return child == kindTableRow
	}
	return false
}

func (b *block) detach() {
	p := b.parent
	if p == nil {
		return
	}
	for i := len(p.children) - 1; i >= 0; i-- {
		if p.children[i] == b {
			p.children = append(p.children[:i], p.children[i+1:]...)
			break
		}
	}
	b.parent = nil
}

type parser struct {
	root    *block
	current *block
	line    int // 0-based index of the line being processed

	offset               int
	column               int
	firstNonspace        int
	firstNonspaceColumn  int
	indent               int
	blank                bool
	partiallyConsumedTab bool
}

// parseBlocks parses a body whose carriage returns are already removed.
// Each element of lines carries its trailing "\n", except the last line of
// a body that does not end in one.
func parseBlocks(lines []string) *block {
	root := &block{kind: kindDocument, open: true}
	p := &parser{root: root, current: root}
	for i, l := range lines {
		p.line = i
		p.processLine(l)
	}
	p.finalizeDocument()
	return root
}

func (p *parser) processLine(line string) {
	p.offset = 0
	p.column = 0
	p.firstNonspace = 0
	p.firstNonspaceColumn = 0
	p.indent = 0
	p.blank = false
	p.partiallyConsumedTab = false

	if p.line == 0 && len(line) >= 3 && line[:3] == "\xef\xbb\xbf" {
		p.offset += 3
	}

	container, allMatched, ok := p.checkOpenBlocks(line)
	if !ok {
		return
	}
	lastMatched := container
	current := p.current
	p.openNewBlocks(&container, line, allMatched)
	if current == p.current {
		p.addTextToContainer(container, lastMatched, line)
	}
}

// checkOpenBlocks walks the chain of open blocks from the document down and
// returns the deepest one the line continues. ok is false when the line was
// consumed whole, as a closing fence is.
func (p *parser) checkOpenBlocks(line string) (container *block, allMatched, ok bool) {
	container = p.root
walk:
	for {
		if !container.lastChildIsOpen() {
			allMatched = true
			break
		}
		container = container.lastChild()
		p.findFirstNonspace(line)

		switch container.kind {
		case kindBlockQuote:
			if !p.parseBlockQuotePrefix(line) {
				break walk
			}
		case kindItem, kindDescItem:
			if !p.parseItemPrefix(line, container) {
				break walk
			}
		case kindCodeBlock:
			matched, consumed := p.parseCodeBlockPrefix(line, container)
			if consumed {
				return nil, false, false
			}
			if !matched {
				break walk
			}
		case kindHTMLBlock:
			if !p.parseHTMLBlockPrefix(container.htmlType) {
				break walk
			}
		case kindParagraph:
			if p.blank {
				break walk
			}
		case kindTable:
			if _, _, ok := tableRow(line[p.firstNonspace:]); !ok {
				break walk
			}
		case kindHeading, kindTableRow:
			break walk
		case kindFootnote:
			if !p.parseFootnotePrefix(line) {
				break walk
			}
		case kindMultilineQuote:
			if p.parseMultilineQuotePrefix(line, container) {
				return nil, false, false
			}
		}
	}
	if !allMatched {
		container = container.parent
	}
	return container, allMatched, true
}

func (p *parser) findFirstNonspace(line string) {
	charsToTab := tabStop - (p.column % tabStop)
	if p.firstNonspace <= p.offset {
		p.firstNonspace = p.offset
		p.firstNonspaceColumn = p.column
	scan:
		for p.firstNonspace < len(line) {
			switch line[p.firstNonspace] {
			case ' ':
				p.firstNonspace++
				p.firstNonspaceColumn++
				charsToTab--
				if charsToTab == 0 {
					charsToTab = tabStop
				}
			case '\t':
				p.firstNonspace++
				p.firstNonspaceColumn += charsToTab
				charsToTab = tabStop
			default:
				break scan
			}
		}
	}
	p.indent = p.firstNonspaceColumn - p.column
	p.blank = p.firstNonspace >= len(line) || isLineEnd(line[p.firstNonspace])
}

func (p *parser) advanceOffset(line string, count int, columns bool) {
	for count > 0 && p.offset < len(line) {
		if line[p.offset] == '\t' {
			charsToTab := tabStop - (p.column % tabStop)
			if columns {
				p.partiallyConsumedTab = charsToTab > count
				advance := min(count, charsToTab)
				p.column += advance
				if !p.partiallyConsumedTab {
					p.offset++
				}
				count -= advance
			} else {
				p.partiallyConsumedTab = false
				p.column += charsToTab
				p.offset++
				count--
			}
		} else {
			p.partiallyConsumedTab = false
			p.offset++
			p.column++
			count--
		}
	}
}

func (p *parser) parseBlockQuotePrefix(line string) bool {
	if p.indent <= 3 && byteAt(line, p.firstNonspace) == '>' {
		p.advanceOffset(line, p.indent+1, true)
		if isSpaceOrTab(byteAt(line, p.offset)) {
			p.advanceOffset(line, 1, true)
		}
		return true
	}
	return false
}

func (p *parser) parseItemPrefix(line string, item *block) bool {
	if p.indent >= item.markerOffset+item.padding {
		p.advanceOffset(line, item.markerOffset+item.padding, true)
		return true
	}
	if p.blank && len(item.children) > 0 {
		p.advanceOffset(line, p.firstNonspace-p.offset, false)
		return true
	}
	return false
}

// parseCodeBlockPrefix reports whether the line continues the code block,
// and whether it closed the block and so was consumed.
func (p *parser) parseCodeBlockPrefix(line string, code *block) (matched, consumed bool) {
	if !code.fenced {
		if p.indent >= codeIndent {
			p.advanceOffset(line, codeIndent, true)
			return true, false
		}
		if p.blank {
			p.advanceOffset(line, p.firstNonspace-p.offset, false)
			return true, false
		}
		return false, false
	}

	closing := 0
	if p.indent <= 3 && byteAt(line, p.firstNonspace) == code.fenceChar {
		closing = closeCodeFence(line[p.firstNonspace:])
	}
	if closing > 0 && closing >= code.fenceLength {
		p.advanceOffset(line, closing, false)
		p.current = p.finalize(code)
		return false, true
	}
	for i := code.fenceOffset; i > 0 && isSpaceOrTab(byteAt(line, p.offset)); i-- {
		p.advanceOffset(line, 1, true)
	}
	return true, false
}

func (p *parser) parseHTMLBlockPrefix(htmlType int) bool {
	if htmlType >= 1 && htmlType <= 5 {
		return true
	}
	return !p.blank
}

func (p *parser) parseFootnotePrefix(line string) bool {
	if p.indent >= 4 {
		p.advanceOffset(line, 4, true)
		return true
	}
	return line == "\n"
}

// parseMultilineQuotePrefix reports whether the line closed the quote. A
// multiline quote matches every other line, whatever its indentation, which
// is why a >>> line closes it even from inside a code block it contains.
func (p *parser) parseMultilineQuotePrefix(line string, quote *block) bool {
	closing := 0
	if p.indent <= 3 && byteAt(line, p.firstNonspace) == '>' {
		closing = multilineQuoteFence(line[p.firstNonspace:])
	}
	if closing > 0 && closing >= quote.fenceLength {
		p.advanceOffset(line, closing, false)
		if quote.lastChildIsOpen() {
			child := quote.lastChild()
			for child.lastChildIsOpen() && child.kind != kindList {
				child = child.lastChild()
			}
			p.finalize(child)
		}
		p.current = p.finalize(quote)
		return true
	}
	for i := quote.fenceOffset; i > 0 && isSpaceOrTab(byteAt(line, p.offset)); i-- {
		p.advanceOffset(line, 1, true)
	}
	return false
}

func (p *parser) openNewBlocks(container **block, line string, allMatched bool) {
	maybeLazy := p.current.kind == kindParagraph
	depth := 0

	for (*container).kind != kindCodeBlock && (*container).kind != kindHTMLBlock {
		depth++
		p.findFirstNonspace(line)
		indented := p.indent >= codeIndent

		matched := !indented &&
			(p.handleAlert(container, line) ||
				p.handleMultilineQuote(container, line) ||
				p.handleBlockQuote(container, line) ||
				p.handleATXHeading(container, line) ||
				p.handleCodeFence(container, line) ||
				p.handleHTMLBlock(container, line) ||
				p.handleSetextHeading(container, line) ||
				p.handleThematicBreak(container, line, allMatched) ||
				p.handleFootnote(container, line, depth) ||
				p.handleDescriptionList(container, line))
		if !matched {
			matched = p.handleList(container, line, indented, depth) ||
				p.handleIndentedCode(container, line, indented, maybeLazy) ||
				p.handleTable(container, line, indented)
		}
		if !matched || (*container).acceptsLines() {
			break
		}
		maybeLazy = false
	}
}

func (p *parser) handleAlert(container **block, line string) bool {
	if byteAt(line, p.firstNonspace) != '>' {
		return false
	}
	fenceLength, ok := alertStart(line[p.firstNonspace:])
	if !ok || fenceLength == 2 {
		return false
	}
	fenceOffset := p.firstNonspace - p.offset
	p.advanceOffset(line, len(line)-p.offset-newlinesOf(line), false)
	if fenceLength >= 3 {
		q := p.addChild(*container, kindMultilineQuote)
		q.fenceLength = fenceLength
		q.fenceOffset = fenceOffset
		*container = q
	} else {
		*container = p.addChild(*container, kindBlockQuote)
	}
	return true
}

func (p *parser) handleMultilineQuote(container **block, line string) bool {
	matched := multilineQuoteFence(line[p.firstNonspace:])
	if matched == 0 {
		return false
	}
	q := p.addChild(*container, kindMultilineQuote)
	q.fenceLength = matched
	q.fenceOffset = p.firstNonspace - p.offset
	*container = q
	p.advanceOffset(line, p.firstNonspace+matched-p.offset, false)
	return true
}

func (p *parser) handleBlockQuote(container **block, line string) bool {
	if byteAt(line, p.firstNonspace) != '>' {
		return false
	}
	p.advanceOffset(line, p.firstNonspace+1-p.offset, false)
	if isSpaceOrTab(byteAt(line, p.offset)) {
		p.advanceOffset(line, 1, true)
	}
	*container = p.addChild(*container, kindBlockQuote)
	return true
}

func (p *parser) handleATXHeading(container **block, line string) bool {
	matched := atxHeadingStart(line[p.firstNonspace:])
	if matched == 0 {
		return false
	}
	p.advanceOffset(line, p.firstNonspace+matched-p.offset, false)
	*container = p.addChild(*container, kindHeading)
	return true
}

func (p *parser) handleCodeFence(container **block, line string) bool {
	matched := openCodeFence(line[p.firstNonspace:])
	if matched == 0 {
		return false
	}
	code := p.addChild(*container, kindCodeBlock)
	code.fenced = true
	code.fenceChar = line[p.firstNonspace]
	code.fenceLength = matched
	code.fenceOffset = p.firstNonspace - p.offset
	*container = code
	p.advanceOffset(line, p.firstNonspace+matched-p.offset, false)
	return true
}

func (p *parser) handleHTMLBlock(container **block, line string) bool {
	rest := line[p.firstNonspace:]
	htmlType := htmlBlockStart(rest)
	if htmlType == 0 && (*container).kind != kindParagraph && htmlBlockStart7(rest) {
		htmlType = 7
	}
	if htmlType == 0 {
		return false
	}
	h := p.addChild(*container, kindHTMLBlock)
	h.htmlType = htmlType
	*container = h
	return true
}

func (p *parser) handleSetextHeading(container **block, line string) bool {
	if (*container).kind != kindParagraph || !setextHeadingLine(line[p.firstNonspace:]) {
		return false
	}
	if p.resolveReferenceDefinitions(*container) {
		(*container).kind = kindHeading
		p.advanceOffset(line, len(line)-newlinesOf(line)-p.offset, false)
	}
	return true
}

func (p *parser) handleThematicBreak(container **block, line string, allMatched bool) bool {
	if (*container).kind == kindParagraph && !allMatched {
		return false
	}
	if !thematicBreak(line[p.firstNonspace:]) {
		return false
	}
	*container = p.addChild(*container, kindThematicBreak)
	p.advanceOffset(line, len(line)-newlinesOf(line)-p.offset, false)
	return true
}

func (p *parser) handleFootnote(container **block, line string, depth int) bool {
	if depth >= maxListDepth {
		return false
	}
	matched := footnoteDefinition(line[p.firstNonspace:])
	if matched == 0 {
		return false
	}
	p.advanceOffset(line, p.firstNonspace+matched-p.offset, false)
	*container = p.addChild(*container, kindFootnote)
	return true
}

func (p *parser) handleDescriptionList(container **block, line string) bool {
	matched := descriptionItemStart(line[p.firstNonspace:])
	if matched == 0 || !p.parseDescriptionDetails(container, matched) {
		return false
	}
	p.advanceOffset(line, p.firstNonspace+matched-p.offset, false)
	if isSpaceOrTab(byteAt(line, p.offset)) {
		p.advanceOffset(line, 1, true)
	}
	return true
}

// parseDescriptionDetails turns the paragraph before a ": details" line into
// a description term, or adds another details block to the list.
func (p *parser) parseDescriptionDetails(container **block, matched int) bool {
	last := (*container).lastChild()
	if last == nil {
		if (*container).kind != kindParagraph || (*container).parent == nil {
			return false
		}
		*container = (*container).parent
		last = (*container).lastChild()
	}

	switch last.kind {
	case kindParagraph:
		last.detach()
		list := (*container).lastChild()
		if list != nil && list.kind == kindDescList {
			for n := list; n != nil; n = n.parent {
				n.open = true
			}
		} else {
			list = p.addChild(*container, kindDescList)
		}
		item := p.addChild(list, kindDescItem)
		item.markerOffset = p.indent
		item.padding = matched
		term := p.addChild(item, kindDescTerm)
		details := p.addChild(item, kindDescDetails)
		term.children = append(term.children, last)
		last.parent = term
		*container = details
		return true
	case kindDescItem:
		item := p.addChild(last.parent, kindDescItem)
		item.markerOffset = p.indent
		item.padding = matched
		*container = p.addChild(item, kindDescDetails)
		return true
	}
	return false
}

func (p *parser) handleList(container **block, line string, indented bool, depth int) bool {
	if (indented && (*container).kind != kindList) || p.indent >= 4 || depth >= maxListDepth {
		return false
	}
	matched, item, ok := parseListMarker(line, p.firstNonspace, (*container).kind == kindParagraph)
	if !ok {
		return false
	}

	p.advanceOffset(line, p.firstNonspace+matched-p.offset, false)
	savedTab, savedOffset, savedColumn := p.partiallyConsumedTab, p.offset, p.column
	for p.column-savedColumn <= 5 && isSpaceOrTab(byteAt(line, p.offset)) {
		p.advanceOffset(line, 1, true)
	}
	i := p.column - savedColumn
	if i < 1 || i >= 5 || isLineEnd(byteAt(line, p.offset)) {
		item.padding = matched + 1
		p.offset, p.column, p.partiallyConsumedTab = savedOffset, savedColumn, savedTab
		if i > 0 {
			p.advanceOffset(line, 1, true)
		}
	} else {
		item.padding = matched + i
	}
	item.markerOffset = p.indent

	c := *container
	if c.kind != kindList || c.ordered != item.ordered || c.delimiter != item.delimiter || c.bulletChar != item.bulletChar {
		list := p.addChild(c, kindList)
		list.ordered, list.delimiter, list.bulletChar = item.ordered, item.delimiter, item.bulletChar
		list.markerOffset, list.padding = item.markerOffset, item.padding
		c = list
	}
	n := p.addChild(c, kindItem)
	n.ordered, n.delimiter, n.bulletChar = item.ordered, item.delimiter, item.bulletChar
	n.markerOffset, n.padding = item.markerOffset, item.padding
	*container = n
	return true
}

func (p *parser) handleIndentedCode(container **block, line string, indented, maybeLazy bool) bool {
	if !indented || maybeLazy || p.blank {
		return false
	}
	p.advanceOffset(line, codeIndent, true)
	*container = p.addChild(*container, kindCodeBlock)
	return true
}

// handleTable is comrak's table::try_opening_block. On a paragraph it
// always reports a match, as comrak does, so a paragraph line that is not a
// table still ends the loop.
func (p *parser) handleTable(container **block, line string, indented bool) bool {
	if indented {
		return false
	}
	c := *container
	switch c.kind {
	case kindParagraph:
		p.tryOpeningTableHeader(container, line)
		return true
	case kindTable:
		if p.blank {
			return false
		}
		if _, _, ok := tableRow(line[p.firstNonspace:]); !ok {
			return false
		}
		*container = p.addChild(c, kindTableRow)
		p.advanceOffset(line, len(line)-p.offset-newlinesOf(line), false)
		return true
	}
	return false
}

func (p *parser) tryOpeningTableHeader(container **block, line string) {
	par := *container
	if par.tableVisited || !tableStart(line[p.firstNonspace:]) {
		return
	}
	_, delimiterCells, ok := tableRow(line[p.firstNonspace:])
	if !ok {
		par.tableVisited = true
		return
	}
	headerOffset, headerCells, ok := tableRow(string(par.content))
	if !ok || headerCells != delimiterCells {
		par.tableVisited = true
		return
	}

	// The paragraph's last line becomes the header row. Any lines before it
	// stay a paragraph of their own.
	parent := par.parent
	var replacement []*block
	if headerOffset > 0 {
		preface := &block{
			kind:      kindParagraph,
			parent:    parent,
			open:      true,
			firstLine: par.firstLine,
			lastLine:  par.firstLine + countNewlines(par.content[:headerOffset]) - 1,
			content:   par.content[:headerOffset],
		}
		replacement = append(replacement, preface)
	}
	table := &block{kind: kindTable, parent: parent, open: true, firstLine: par.firstLine, lastLine: p.line}
	header := &block{kind: kindTableRow, parent: table, open: true}
	table.children = []*block{header}
	replacement = append(replacement, table)

	for i := len(parent.children) - 1; i >= 0; i-- {
		if parent.children[i] == par {
			rest := append([]*block(nil), parent.children[i+1:]...)
			parent.children = append(append(parent.children[:i], replacement...), rest...)
			break
		}
	}
	par.parent = nil

	p.advanceOffset(line, len(line)-newlinesOf(line)-p.offset, false)
	*container = table
}

func (p *parser) addTextToContainer(container, lastMatched *block, line string) {
	p.findFirstNonspace(line)

	// A lazy continuation line: the paragraph goes on though its
	// containers did not match.
	if p.current != lastMatched && container == lastMatched && !p.blank && p.current.kind == kindParagraph {
		p.addLine(p.current, line)
		return
	}

	for p.current != lastMatched && p.current.parent != nil {
		p.current = p.finalize(p.current)
	}

	switch container.kind {
	case kindCodeBlock:
		p.addLine(container, line)
	case kindHTMLBlock:
		if htmlBlockEnds(container.htmlType, line[p.firstNonspace:]) {
			container = p.finalize(container)
		}
	default:
		switch {
		case p.blank:
		case container.acceptsLines():
			p.advanceOffset(line, p.firstNonspace-p.offset, false)
			p.addLine(container, line)
		default:
			container = p.addChild(container, kindParagraph)
			p.advanceOffset(line, p.firstNonspace-p.offset, false)
			p.addLine(container, line)
		}
	}
	p.current = container
}

func (p *parser) addLine(b *block, line string) {
	if b.kind != kindParagraph {
		return
	}
	b.lastLine = p.line
	if p.partiallyConsumedTab {
		p.offset++
		charsToTab := tabStop - (p.column % tabStop)
		for range charsToTab {
			b.content = append(b.content, ' ')
		}
	}
	if p.offset < len(line) {
		b.content = append(b.content, line[p.offset:]...)
	}
}

func (p *parser) addChild(parent *block, k kind) *block {
	for !parent.canContain(k) {
		next := p.finalize(parent)
		if next == nil {
			break
		}
		parent = next
	}
	child := &block{kind: k, parent: parent, open: true, firstLine: p.line, lastLine: p.line}
	parent.children = append(parent.children, child)
	return child
}

// finalize closes a block and returns its parent. A paragraph made only of
// link reference definitions leaves the tree, as it renders nothing.
func (p *parser) finalize(b *block) *block {
	b.open = false
	parent := b.parent
	if b.kind == kindParagraph && !p.resolveReferenceDefinitions(b) {
		b.detach()
	}
	return parent
}

func (p *parser) finalizeDocument() {
	for p.current != p.root && p.current != nil {
		p.current = p.finalize(p.current)
	}
	p.root.open = false
}

// resolveReferenceDefinitions strips the link reference definitions that
// open a paragraph and reports whether anything is left.
func (p *parser) resolveReferenceDefinitions(b *block) bool {
	pos := 0
	for pos < len(b.content) && b.content[pos] == '[' {
		n, ok := parseReferenceDefinition(b.content[pos:])
		if !ok {
			break
		}
		pos += n
	}
	if pos != 0 {
		b.content = b.content[pos:]
	}
	return !isBlank(b.content)
}

// paragraphRanges returns the 0-based line ranges of the paragraphs GitLab
// searches for commands: the top-level ones, plus description terms (see
// Find for why terms are included).
func paragraphRanges(root *block) [][2]int {
	var out [][2]int
	for _, c := range root.children {
		switch c.kind {
		case kindParagraph:
			out = append(out, [2]int{c.firstLine, c.lastLine})
		case kindDescList:
			for _, item := range c.children {
				for _, part := range item.children {
					if part.kind != kindDescTerm {
						continue
					}
					for _, t := range part.children {
						if t.kind == kindParagraph {
							out = append(out, [2]int{t.firstLine, t.lastLine})
						}
					}
				}
			}
		}
	}
	return out
}
