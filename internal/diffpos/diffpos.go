// Package diffpos computes where an inline comment lands: GitLab's diff
// position for a line of a merge request's diff, from the file's unified
// diff and the diff version's refs (docs/architecture.md §7.4). It makes
// no request.
//
// The numbering follows GitLab's own parser, read at gitlab-org/gitlab
// 829b21d284863743bde419836a053626309d02f6 (lib/gitlab/diff/parser.rb,
// lib/gitlab/diff/line.rb, lib/gitlab/git.rb): both counters start at a
// hunk header's positions; a context line advances both, an added line
// the new one and a removed line the old one; a "\ No newline" line
// advances neither. Every line carries both counters, so a removed
// line's line_code names the new side's position too:
//
//	line_code = sha1(new_path, or old_path when empty) "_" old "_" new
package diffpos

import (
	"crypto/sha1" //nolint:gosec // GitLab's line_code is defined with SHA-1; nothing here is secret
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Side is which version of the file a line number counts in.
type Side string

// The sides.
const (
	// New counts lines of the file after the change.
	New Side = "new"
	// Old counts lines of the file before the change.
	Old Side = "old"
)

// Refs are the diff version's commits.
type Refs struct {
	BaseSHA, StartSHA, HeadSHA string
}

// File is one file of the diff version.
type File struct {
	OldPath, NewPath string
	// Diff is GitLab's unified diff for the file: hunks, each starting
	// "@@ -a,b +c,d @@". Empty when GitLab withheld it.
	Diff string
}

// path is the file's path as GitLab's line_code hashes it.
func (f File) path() string {
	if f.NewPath != "" {
		return f.NewPath
	}
	return f.OldPath
}

// Kind is what the change did to a line.
type Kind string

// The kinds.
const (
	Added     Kind = "added"
	Removed   Kind = "removed"
	Unchanged Kind = "unchanged"
)

// Line is one line of the diff as GitLab numbers it.
type Line struct {
	Kind Kind
	// Old and New are GitLab's counters at the line. For an added line
	// Old is where it sits between old lines, and for a removed line New
	// is where it sits between new lines: both go into line_code, and
	// only the side the line exists on goes into a position.
	Old, New int
}

// OldLine is the old-side line number a position carries, nil for an
// added line.
func (l Line) OldLine() *int {
	if l.Kind == Added {
		return nil
	}
	return &l.Old
}

// NewLine is the new-side line number a position carries, nil for a
// removed line.
func (l Line) NewLine() *int {
	if l.Kind == Removed {
		return nil
	}
	return &l.New
}

// rangeType is the line_range type GitLab's web client sends: "new" for
// an added line, "old" for a removed one, none for context.
func (l Line) rangeType() string {
	switch l.Kind {
	case Added:
		return "new"
	case Removed:
		return "old"
	}
	return ""
}

// Code is the line's line_code in file f.
func (l Line) Code(f File) string {
	sum := sha1.Sum([]byte(f.path())) //nolint:gosec // see the import
	return hex.EncodeToString(sum[:]) + "_" + strconv.Itoa(l.Old) + "_" + strconv.Itoa(l.New)
}

// Position is GitLab's position for a diff note, as the draft notes and
// discussions APIs take it.
type Position struct {
	BaseSHA      string     `json:"base_sha"`
	StartSHA     string     `json:"start_sha"`
	HeadSHA      string     `json:"head_sha"`
	PositionType string     `json:"position_type"`
	OldPath      string     `json:"old_path"`
	NewPath      string     `json:"new_path"`
	OldLine      *int       `json:"old_line,omitempty"`
	NewLine      *int       `json:"new_line,omitempty"`
	LineRange    *LineRange `json:"line_range,omitempty"`
}

// LineRange is a multi-line comment's first and last line.
type LineRange struct {
	Start RangeEnd `json:"start"`
	End   RangeEnd `json:"end"`
}

// RangeEnd is one end of a LineRange.
type RangeEnd struct {
	LineCode string `json:"line_code"`
	Type     string `json:"type,omitempty"`
	OldLine  *int   `json:"old_line,omitempty"`
	NewLine  *int   `json:"new_line,omitempty"`
}

// Placed is where a comment was computed to land: the position to send
// and the lines it covers, first to last.
type Placed struct {
	Position Position
	Start    Line
	End      Line
}

// Hunk is one hunk's line ranges, for a caller to name.
type Hunk struct {
	OldStart, OldEnd, NewStart, NewEnd int
	lines                              []Line
}

// Error is a line the diff cannot place. Its message names what to pass
// instead; the caller attaches the class.
type Error struct {
	msg string
}

func (e *Error) Error() string { return e.msg }

func errorf(format string, args ...any) *Error { return &Error{msg: fmt.Sprintf(format, args...)} }

// Compute places a comment on line (and through endLine, when it is not
// zero) on side of file f. Both ends must be in the same hunk of the
// diff: a line the diff does not show has no position GitLab can draw.
func Compute(refs Refs, f File, side Side, line, endLine int) (Placed, error) {
	if side != New && side != Old {
		return Placed{}, errorf("side must be new or old")
	}
	if line < 1 || endLine < 0 {
		return Placed{}, errorf("line numbers start at 1")
	}
	if endLine != 0 && endLine < line {
		return Placed{}, errorf("end_line %d is before line %d", endLine, line)
	}
	hunks, err := Parse(f.Diff)
	if err != nil {
		return Placed{}, err
	}
	if len(hunks) == 0 {
		return Placed{}, errorf("GitLab sent no diff lines for this file (it may be binary, too large or only renamed), so no line of it can take a comment")
	}
	h, start, ok := find(hunks, side, line)
	if !ok {
		return Placed{}, errorf("%s line %d is not in the diff of this file; the diff shows %s", side, line, nearest(hunks, side, line))
	}
	end := start
	if endLine != 0 && endLine != line {
		var eh int
		if eh, end, ok = find(hunks, side, endLine); !ok {
			return Placed{}, errorf("%s line %d is not in the diff of this file; the diff shows %s", side, endLine, nearest(hunks, side, endLine))
		}
		if eh != h {
			return Placed{}, errorf("%s lines %d and %d are in different hunks; a range must stay within one: %s",
				side, line, endLine, describe(hunks[h], side))
		}
	}
	pos := Position{
		BaseSHA: refs.BaseSHA, StartSHA: refs.StartSHA, HeadSHA: refs.HeadSHA, PositionType: "text",
		OldPath: f.OldPath, NewPath: f.NewPath,
		// GitLab anchors a multi-line comment on its last line.
		OldLine: end.OldLine(), NewLine: end.NewLine(),
	}
	if endLine != 0 && endLine != line {
		pos.LineRange = &LineRange{
			Start: RangeEnd{LineCode: start.Code(f), Type: start.rangeType(), OldLine: start.OldLine(), NewLine: start.NewLine()},
			End:   RangeEnd{LineCode: end.Code(f), Type: end.rangeType(), OldLine: end.OldLine(), NewLine: end.NewLine()},
		}
	}
	return Placed{Position: pos, Start: start, End: end}, nil
}

// find returns the hunk and the line that is number n on side.
func find(hunks []Hunk, side Side, n int) (int, Line, bool) {
	for i, h := range hunks {
		for _, l := range h.lines {
			if side == New && l.Kind != Removed && l.New == n {
				return i, l, true
			}
			if side == Old && l.Kind != Added && l.Old == n {
				return i, l, true
			}
		}
	}
	return 0, Line{}, false
}

// maxNamed caps how many hunks an error names.
const maxNamed = 3

// nearest names the hunks closest to line n on side.
func nearest(hunks []Hunk, side Side, n int) string {
	type near struct {
		i, dist int
	}
	ranked := make([]near, 0, len(hunks))
	for i, h := range hunks {
		lo, hi := h.span(side)
		d := 0
		switch {
		case n < lo:
			d = lo - n
		case n > hi:
			d = n - hi
		}
		ranked = append(ranked, near{i, d})
	}
	slices.SortStableFunc(ranked, func(a, b near) int { return a.dist - b.dist })
	ranked = ranked[:min(len(ranked), maxNamed)]
	slices.SortFunc(ranked, func(a, b near) int { return a.i - b.i })
	parts := make([]string, 0, len(ranked))
	for _, r := range ranked {
		parts = append(parts, describe(hunks[r.i], side))
	}
	s := strings.Join(parts, ", ")
	if len(hunks) > maxNamed {
		s += fmt.Sprintf(" (the %d nearest of %d hunks)", maxNamed, len(hunks))
	}
	return s
}

// span is the hunk's first and last line on side. A hunk with no line on
// that side is a point where lines were only added or only removed.
func (h Hunk) span(side Side) (int, int) {
	if side == Old {
		return h.OldStart, max(h.OldEnd, h.OldStart)
	}
	return h.NewStart, max(h.NewEnd, h.NewStart)
}

func describe(h Hunk, side Side) string {
	lo, hi := h.span(side)
	if side == Old && h.OldEnd < h.OldStart || side == New && h.NewEnd < h.NewStart {
		return fmt.Sprintf("no %s lines near %d", side, lo)
	}
	if lo == hi {
		return fmt.Sprintf("%s line %d", side, lo)
	}
	return fmt.Sprintf("%s lines %d–%d", side, lo, hi)
}

// Parse reads a unified diff into hunks. Lines before the first hunk
// header, such as "---" and "+++" file headers, are skipped.
func Parse(diff string) ([]Hunk, error) {
	var hunks []Hunk
	var cur *Hunk
	oldN, newN := 0, 0
	lines := strings.Split(diff, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for _, text := range lines {
		if strings.HasPrefix(text, "@@ -") {
			o, n, err := header(text)
			if err != nil {
				return nil, err
			}
			hunks = append(hunks, Hunk{OldStart: o, NewStart: n, OldEnd: o - 1, NewEnd: n - 1})
			cur = &hunks[len(hunks)-1]
			oldN, newN = o, n
			continue
		}
		if cur == nil {
			continue
		}
		var l Line
		switch {
		case strings.HasPrefix(text, `\`):
			continue // "\ No newline at end of file" advances neither
		case strings.HasPrefix(text, "+"):
			l = Line{Kind: Added, Old: oldN, New: newN}
			cur.NewEnd = newN
			newN++
		case strings.HasPrefix(text, "-"):
			l = Line{Kind: Removed, Old: oldN, New: newN}
			cur.OldEnd = oldN
			oldN++
		default:
			l = Line{Kind: Unchanged, Old: oldN, New: newN}
			cur.OldEnd, cur.NewEnd = oldN, newN
			oldN++
			newN++
		}
		cur.lines = append(cur.lines, l)
	}
	return hunks, nil
}

// header reads "@@ -a[,b] +c[,d] @@" into a and c. The counts are not
// read: GitLab's DiffHunk takes each start as written, so after
// "@@ -5,0 +6,2 @@" an added line's old counter is 5, and after
// "@@ -0,0 +1,3 @@" a new file's is 0. line_code carries both.
func header(text string) (int, int, error) {
	rest := strings.TrimPrefix(text, "@@ -")
	oldPart, rest, ok := strings.Cut(rest, " +")
	if !ok {
		return 0, 0, errorf("a hunk header in this file's diff could not be read")
	}
	newPart, _, ok := strings.Cut(rest, " @@")
	if !ok {
		return 0, 0, errorf("a hunk header in this file's diff could not be read")
	}
	o, err := startOf(oldPart)
	if err != nil {
		return 0, 0, err
	}
	n, err := startOf(newPart)
	if err != nil {
		return 0, 0, err
	}
	return o, n, nil
}

func startOf(part string) (int, error) {
	startText, countText, hasCount := strings.Cut(part, ",")
	start, err := strconv.Atoi(startText)
	if err != nil || start < 0 {
		return 0, errorf("a hunk header in this file's diff could not be read")
	}
	if c, err := strconv.Atoi(countText); hasCount && (err != nil || c < 0) {
		return 0, errorf("a hunk header in this file's diff could not be read")
	}
	return start, nil
}
