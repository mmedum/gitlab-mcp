package render

import (
	"strings"
	"unicode/utf8"

	"github.com/mmedum/gitlab-mcp/v2/internal/model"
)

// Budgets in characters (§4.8). A result states the one it used, so a
// cut text is never mistaken for a short one.
const (
	// DescriptionBudget bounds an issue or merge request description.
	DescriptionBudget = 20000
	// FileBudget bounds the text of one file.
	FileBudget = 60000
	// DiscussionBudget bounds the comment text of one page of threads.
	DiscussionBudget = 30000
	// NoteBudget bounds one comment within that.
	NoteBudget = 6000
	// DiffBudget bounds the diffs of one commit.
	DiffBudget = 40000
	// CommitMessageBudget bounds a commit message.
	CommitMessageBudget = 8000
	// TestReportBudget bounds the failed cases of one test report
	// result, and TestOutputBudget one case's output within that.
	TestReportBudget = 40000
	TestOutputBudget = 4000
	// BoardsBudget bounds one page of list_boards, by an estimate of
	// each board's lists.
	BoardsBudget = 30000
	// TitleChars bounds a one-line title.
	TitleChars = 200
)

// Cut returns the part of text that starts at offset and fits budget,
// ending at a paragraph boundary when one falls in the second half of
// the window, else at a line boundary there, else at the budget. Offsets
// and counts are in characters (runes), never bytes, so a continue
// offset never lands inside one.
//
// An offset past the end yields nothing and a budget whose
// ContinueOffset is null; the caller decides whether that is an error.
func Cut(text string, offset, budget int) (string, model.Budget) {
	total := utf8.RuneCountInString(text)
	offset = min(max(offset, 0), total)
	end := min(total, offset+budget)
	from := byteIndex(text, 0, offset)
	to := byteIndex(text, from, end-offset)
	if end < total {
		window := text[from:to]
		if i := lastBreak(window, "\n\n", budget/2); i >= 0 {
			to = from + i
		} else if i := lastBreak(window, "\n", budget/2); i >= 0 {
			to = from + i
		}
		end = offset + utf8.RuneCountInString(text[from:to])
	}
	b := model.Budget{BudgetChars: budget, TotalChars: total, Offset: offset, ShownChars: end - offset}
	if end < total {
		next := end
		b.ContinueOffset = &next
	}
	return text[from:to], b
}

// byteIndex returns the byte index n characters past the byte index
// from, or the end of s.
func byteIndex(s string, from, n int) int {
	for i := range s[from:] {
		if n == 0 {
			return from + i
		}
		n--
	}
	return len(s)
}

// lastBreak returns the byte index just past the last sep in window, if
// that is at least floor characters in; else -1.
func lastBreak(window, sep string, floor int) int {
	i := strings.LastIndex(window, sep)
	if i < 0 {
		return -1
	}
	at := i + len(sep)
	if utf8.RuneCountInString(window[:at]) < floor {
		return -1
	}
	return at
}
