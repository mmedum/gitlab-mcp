package quickaction

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// findCases are the lines GitLab would run, by rule. The expected values
// were checked against comrak v0.55.0 with GitLab's options and a copy of
// the extractor's regular expression, except where a case says it holds a
// deliberate deviation.
var findCases = []struct {
	name string
	body string
	want []int
}{
	// Paragraph lines.
	{"only a command", "/close", []int{1}},
	{"command with arguments", "/assign @alice", []int{1}},
	{"command between text", "hello\n/close\nworld", []int{2}},
	{"every line a command", "/close\n/label ~bug\n/merge\n", []int{1, 2, 3}},
	{"separate paragraphs", "/close\n\n\n/merge", []int{1, 4}},
	{"unknown name counts", "/frobnicate", []int{1}},
	{"uppercase name", "/CLOSE", []int{1}},
	{"trailing white space", "/close \t\nnext", []int{1}},
	{"tab then line end", "/close\t\nnext", []int{1}},
	{"tab then end of body is not a command", "/close\t", nil},
	{"tab then text is not a command", "/close\tnow", nil},
	{"digits and underscores", "/remove_due_date\n/h2o", []int{1, 2}},

	// Lines that are not commands.
	{"slash alone", "/", nil},
	{"slash then space", "/ close", nil},
	{"double slash", "//close", nil},
	{"path at line start", "/path/to/file", nil},
	{"path mid-line", "see /close and /merge", nil},
	{"punctuation after the name", "/close.\n/merge,", nil},
	{"one leading space", " /close", nil},
	{"three leading spaces", "   /close", nil},
	{"leading tab", "\t/close", nil},
	{"indented continuation line", "text\n    /close", nil},
	{"already escaped", "\\/close", nil},
	{"emphasis", "*/close*", nil},
	{"no-break space first", "\u00a0/close", nil},
	{"byte order mark first", "\ufeff/close", nil},
	{"byte order mark line then command", "\ufeff\n/close", []int{2}},

	// Headings.
	{"ATX heading", "# /close", nil},
	{"setext heading, equals", "/close\n===", nil},
	{"setext heading, hyphens", "/close\n---", nil},
	{"thematic break then command", "---\n/close", []int{2}},
	{"heading interrupts a paragraph", "text\n# h\n/close", []int{3}},
	{"seven hashes is no heading", "####### x\n/close", []int{2}},

	// Lists.
	{"bullet item", "- /close", nil},
	{"star and plus items", "* /close\n+ /merge", nil},
	{"ordered item", "1. /close", nil},
	{"lazy continuation of an item", "- item\n/close", nil},
	{"after a list and a blank line", "- item\n\n/close", []int{3}},
	{"loose list item", "- a\n\n  /close\n\n- b", nil},

	// Block quotes.
	{"quote", "> /close", nil},
	{"quote without space", ">/close", nil},
	{"lazy continuation of a quote", "> quote\n/close", nil},
	{"alert", "> [!note]\n> /close", nil},

	// GitLab's multiline block quotes.
	{"multiline quote", ">>>\n/close\n>>>", nil},
	{"after a multiline quote", ">>>\n/close\n>>>\n/merge", []int{4}},
	{"unclosed multiline quote", ">>>\n/close", nil},
	{"longer fence needs a long close", ">>>>\n/close\n>>>\n/merge\n>>>>\n/done", []int{6}},
	{"multiline quote closes through a fence", ">>>\n```\n>>>\n/close", []int{4}},
	{"multiline alert", ">>> [!warning]\n/close\n>>>\n/merge", []int{4}},

	// Fenced code.
	{"backtick fence", "```\n/close\n```", nil},
	{"tilde fence", "~~~\n/close\n~~~", nil},
	{"info string", "```ruby title\n/close\n```", nil},
	{"indented fence", "   ```\n/close\n   ```", nil},
	{"after a closed fence", "```\n```\n/close", []int{3}},
	{"unclosed fence", "```\n/close", nil},
	{"short close does not close", "````\n/close\n```\n/merge\n````", nil},
	{"other character does not close", "```\n/close\n~~~\n/merge", nil},
	{"nested fences", "````\n```\n/close\n```\n````\n/merge", []int{6}},
	{"backtick in info is no fence", "``` a`b\n/close", []int{2}},
	{"tilde fence allows backticks", "~~~ a`b\n/close\n~~~", nil},
	{"fence interrupts a paragraph", "text\n```\n/close\n```", nil},
	{"four spaces is no fence", "text\n    ```\n/close", []int{3}},
	{"fence in a list item closes with the item", "- ```\n/close\n```", []int{2}},
	{"fence in a quote closes with the quote", "> ```\n/close\n> ```", []int{2}},
	{"after a fence closed inside an item", "1. ```\n   ```\n/close", []int{3}},
	{"ordered item 1 interrupts a paragraph", "text\n1. ```\n   ```\n/close", []int{4}},
	{"ordered item 2 does not", "text\n2. ```\n   ```\n/close", nil},
	{"tab after a list marker", "-\t```\n\t```\n/close", []int{3}},
	{"fence in a nested item", "- a\n  - ```\n    ```\n/close", []int{4}},
	{"fence in an item in a quote", "> - ```\n>   ```\n/close", []int{3}},

	// Indented code.
	{"indented code", "    /close", nil},
	{"indented code after a blank", "text\n\n    /close", nil},
	{"after indented code", "    code\n/close", []int{2}},

	// HTML blocks.
	{"div block", "<div>\n/close\n</div>", nil},
	{"div block ends at a blank line", "<div>\n/close\n\n/merge", []int{4}},
	{"comment spans blank lines", "<!--\n/close\n\n/merge\n-->", nil},
	{"after a comment", "<!--\n/close\n-->\n/merge", []int{4}},
	{"pre ends at its closing tag", "<pre>\n/close\n</pre>\n/merge", []int{4}},
	{"custom tag alone on a line", "<custom-tag>\n/close\n</custom-tag>", nil},
	{"tag pair inside a paragraph", "text\n<span>\n/close\n</span>", nil},
	{"unclosed tag inside a paragraph", "text\n<span>\n/close", []int{3}},
	{"script ends at its closing tag", "<script>\n/close\n</script>\n/merge", []int{4}},
	{"processing instruction", "<?php\n/close\n?>\n/merge", []int{4}},
	{"declaration", "<!DOCTYPE html\n/close\n>\n/merge", []int{4}},
	{"CDATA", "<![CDATA[\n/close\n]]>\n/merge", []int{4}},

	// Inline code.
	{"code span on one line", "`/close`", nil},
	{"code span across lines", "`\n/close\n`", nil},
	{"code span across a paragraph", "text `code\n/close\nmore` text", nil},
	{"closed code span before", "a `b`\n/close", []int{2}},
	{"unpaired backtick", "a ` b\n/close", []int{2}},
	{"either reading of an unknown name", "/xyz `\nx `\n/close\n`", []int{1, 3}},

	// Tables.
	{"table row", "| a | b |\n|---|---|\n/close", nil},
	{"after a table", "| a |\n|---|\n\n/close", []int{4}},
	{"header row", "/close | x\n---|---", nil},
	{"paragraph before a table", "text\n/close\n| a |\n| - |", []int{2}},

	// Footnotes and description lists.
	{"lazy continuation of a footnote", "[^1]: note\n/close", nil},
	{"after a footnote's fence", "[^1]: ```\n    ```\n/close", []int{3}},
	{"description details", "term\n: /close", nil},
	{"lazy continuation of details", "term\n: details\n/close", nil},
	{"tilde details marker", "~ ```\n  ```\n/close", nil},
	{"second details block", "term\n~ x\n: y\n  ```\n```\n/close", nil},
	// Deviation: comrak makes this a description term; engines without
	// the extension see a paragraph.
	{"description term", "/close\n: details", []int{1}},

	// Link reference definitions.
	{"reference definition", "[a]: /close", nil},
	{"reference definition destination", "[a]:\n/close", nil},
	{"a definition leaves the tree", "[a]: /u\n\n: x\n  ```\n```\n/close", []int{6}},
	{"definition with a title on the next line", "[a]: /u\n'title'\n/close", []int{3}},
	{"definition with trailing text is none", "[a]: /u \"t\" x\n/close", []int{2}},
	{"definition with an angle-bracket destination", "[a]: <u> 'x'\n\n: d\n  ```\n```\n/close", []int{6}},
	{"definition with parentheses", "[a]: /u(x)\n\n: d\n  ```\n```\n/close", []int{6}},
	{"definition with an escaped bracket", "[a\\]]: /u\n\n: d\n  ```\n```\n/close", []int{6}},
	{"definition with an escaped parenthesis", "[a]: \\(u\n\n: d\n  ```\n```\n/close", []int{6}},
	{"label without destination is text", "[a]:\n\n: d\n  ```\n```\n/close", nil},

	// Line endings.
	{"CRLF", "/close\r\n/merge\r\n", []int{1, 2}},
	{"CRLF after text", "text\r\n/close", []int{2}},
	{"CRLF fence", "```\r\n/close\r\n```\r\n", nil},
	{"lone CR is dropped, not a line end", "a\r/close", nil},
	{"lone CR at line start", "\r/close", []int{1}},
	{"last line without newline", "text\n/close", []int{2}},
	{"last line with newline", "text\n/close\n", []int{2}},
	{"empty body", "", nil},

	// Unicode.
	{"letters beyond ASCII", "/cl\u00f6se", []int{1}},
	{"long s folds to s in Ruby", "/clo\u017fe", []int{1}},
	{"Kelvin sign folds to k in Ruby", "/\u212aeep", []int{1}},
	{"combining mark", "/close\u0301", []int{1}},
	{"invalid UTF-8", "/close \xff\n\xff/merge", []int{1}},
}

func TestFind(t *testing.T) {
	for _, tc := range findCases {
		t.Run(tc.name, func(t *testing.T) {
			var got []int
			for _, l := range Find(tc.body) {
				got = append(got, l.Number)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("Find(%q) lines = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

func TestFindReportsTextAndCommand(t *testing.T) {
	got := Find("Done.\r\n/Label ~bug ~\"needs review\"\r\n/CLOSE\n/clo\u017fe")
	want := []Line{
		{Number: 2, Text: "/Label ~bug ~\"needs review\"", Command: "label"},
		{Number: 3, Text: "/CLOSE", Command: "close"},
		{Number: 4, Text: "/clo\u017fe", Command: "clo\u017fe"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Find = %#v, want %#v", got, want)
	}
}

func TestEscape(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		want  string
		lines []int
	}{
		{"one command", "/close", `\/close`, []int{1}},
		{"keeps other lines", "Done.\n/close\n`/merge`\n", "Done.\n\\/close\n`/merge`\n", []int{2}},
		{"keeps CRLF", "a\r\n/close\r\n/merge\r\n", "a\r\n\\/close\r\n\\/merge\r\n", []int{2, 3}},
		{"after a leading CR", "\r/close", "\r\\/close", []int{1}},
		{"leaves code alone", "```\n/close\n```\n/merge", "```\n/close\n```\n\\/merge", []int{4}},
		{"both readings of an unknown name", "/xyz `\nx `\n/close\n`", "\\/xyz `\nx `\n\\/close\n`", []int{1, 3}},
		{"nothing to escape", "plain text", "plain text", nil},
		{"already escaped", `\/close`, `\/close`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, lines := Escape(tc.body)
			if got != tc.want {
				t.Errorf("Escape(%q) = %q, want %q", tc.body, got, tc.want)
			}
			var nums []int
			for _, l := range lines {
				nums = append(nums, l.Number)
			}
			if !slices.Equal(nums, tc.lines) {
				t.Errorf("Escape(%q) lines = %v, want %v", tc.body, nums, tc.lines)
			}
			if again := Find(got); len(again) > 0 {
				t.Errorf("Find(Escape(%q)) = %v, want none", tc.body, again)
			}
		})
	}
}

func TestCheck(t *testing.T) {
	t.Run("clean body passes unchanged", func(t *testing.T) {
		body := "Looks good.\n\n```\n/close\n```\n"
		for _, escape := range []bool{false, true} {
			got, err := Check(body, escape)
			if err != nil || got != body {
				t.Errorf("Check(escape=%v) = %q, %v; want the body, nil", escape, got, err)
			}
		}
	})

	t.Run("refuses by default", func(t *testing.T) {
		got, err := Check("Done.\n/close\n/assign @bob", false)
		if got != "" {
			t.Errorf("Check returned body %q with an error", got)
		}
		var blocked *BlockedError
		if !errors.As(err, &blocked) {
			t.Fatalf("Check error = %v, want *BlockedError", err)
		}
		want := []Line{
			{Number: 2, Text: "/close", Command: "close"},
			{Number: 3, Text: "/assign @bob", Command: "assign"},
		}
		if !slices.Equal(blocked.Lines, want) {
			t.Errorf("Lines = %v, want %v", blocked.Lines, want)
		}
		msg := err.Error()
		for _, part := range []string{"[blocked] ", "line 2 /close", "line 3 /assign", "escape_commands: true", "Nothing was sent"} {
			if !strings.Contains(msg, part) {
				t.Errorf("message %q lacks %q", msg, part)
			}
		}
		if strings.Contains(msg, "@bob") {
			t.Errorf("message %q carries body text beyond the command name", msg)
		}
		if blocked.Class() != "blocked" {
			t.Errorf("Class() = %q, want blocked", blocked.Class())
		}
		if !strings.HasPrefix(msg, "[blocked] ") || msg != "[blocked] "+blocked.Message() {
			t.Errorf("Error() = %q, want [blocked] and Message()", msg)
		}
	})

	t.Run("escapes on request", func(t *testing.T) {
		got, err := Check("Done.\n/close", true)
		if err != nil || got != "Done.\n\\/close" {
			t.Errorf("Check = %q, %v; want the escaped body", got, err)
		}
	})

	t.Run("caps the lines it names", func(t *testing.T) {
		body := strings.Repeat("/close\n", 13)
		_, err := Check(body, false)
		msg := err.Error()
		if !strings.Contains(msg, "13 quick actions") || !strings.Contains(msg, "line 10 /close and 3 more") || strings.Contains(msg, "line 11") {
			t.Errorf("message %q does not cap at ten lines", msg)
		}
	})
}

// unescape removes the backslash Escape inserted at the start of each
// reported line, after any carriage returns.
func unescape(t *testing.T, escaped string, lines []Line) string {
	t.Helper()
	parts := strings.SplitAfter(escaped, "\n")
	for _, l := range lines {
		p := parts[l.Number-1]
		j := 0
		for j < len(p) && p[j] == '\r' {
			j++
		}
		if !strings.HasPrefix(p[j:], `\/`) {
			t.Fatalf("line %d of %q does not start with an inserted backslash", l.Number, escaped)
		}
		parts[l.Number-1] = p[:j] + p[j+1:]
	}
	return strings.Join(parts, "")
}

func FuzzEscapeNeverYieldsCommand(f *testing.F) {
	for _, tc := range findCases {
		f.Add(tc.body)
	}
	f.Add("/a `\n`\n/b `\nx `\n/c\n`\n<d>\n/e\n</d>")
	f.Fuzz(func(t *testing.T, body string) {
		escaped, lines := Escape(body)
		if left := Find(escaped); len(left) > 0 {
			t.Fatalf("Find(Escape(%q)) = %v", body, left)
		}
		if strings.Count(escaped, "\n") != strings.Count(body, "\n") {
			t.Fatalf("Escape(%q) changed the line count", body)
		}
		if len(escaped) != len(body)+len(lines) {
			t.Fatalf("Escape(%q) inserted %d bytes for %d lines", body, len(escaped)-len(body), len(lines))
		}
		if back := unescape(t, escaped, lines); back != body {
			t.Fatalf("removing the inserted backslashes from %q gives %q, want %q", escaped, back, body)
		}
		found := Find(body)
		if len(found) != len(lines) {
			t.Fatalf("Escape(%q) escaped %d lines, Find reports %d", body, len(lines), len(found))
		}
	})
}

// A row of more than 65535 cells is no row to comrak, so it ends the table
// and what follows is a paragraph again.
func TestFindAfterOversizedTableRow(t *testing.T) {
	body := "a|b\n-|-\n" + strings.Repeat("|", 70000) + "\n/close\n/merge\n"
	var got []int
	for _, l := range Find(body) {
		got = append(got, l.Number)
	}
	if want := []int{4, 5}; !slices.Equal(got, want) {
		t.Errorf("Find lines = %v, want %v", got, want)
	}
}
