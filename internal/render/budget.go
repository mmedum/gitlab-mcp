package render

import (
	"strings"

	"github.com/mmedum/gitlab-mcp/internal/model"
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
	r := []rune(text)
	total := len(r)
	offset = min(max(offset, 0), total)
	end := min(total, offset+budget)
	if end < total {
		window := string(r[offset:end])
		if i := lastBreak(window, "\n\n", budget/2); i >= 0 {
			end = offset + i
		} else if i := lastBreak(window, "\n", budget/2); i >= 0 {
			end = offset + i
		}
	}
	b := model.Budget{BudgetChars: budget, TotalChars: total, Offset: offset, ShownChars: end - offset}
	if end < total {
		next := end
		b.ContinueOffset = &next
	}
	return string(r[offset:end]), b
}

// lastBreak returns the rune index just past the last sep in window, if
// that index is at least floor; else -1.
func lastBreak(window, sep string, floor int) int {
	i := strings.LastIndex(window, sep)
	if i < 0 {
		return -1
	}
	at := len([]rune(window[:i+len(sep)]))
	if at < floor {
		return -1
	}
	return at
}
