package diffpos

import (
	"crypto/sha1" //nolint:gosec // the line_code definition
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

var refs = Refs{BaseSHA: "b", StartSHA: "s", HeadSHA: "h"}

func sha(path string) string {
	sum := sha1.Sum([]byte(path)) //nolint:gosec // the line_code definition
	return hex.EncodeToString(sum[:])
}

// The diff GitLab's API returns for one file: hunks only.
const oneHunk = `@@ -3,6 +3,7 @@ func main() {
 three
 four
-five
+FIVE
+five and a half
 six
 seven
 eight
`

func TestComputePlacesEachKindOfLine(t *testing.T) {
	f := File{OldPath: "src/a.go", NewPath: "src/a.go", Diff: oneHunk}
	for _, tc := range []struct {
		name     string
		side     Side
		line     int
		kind     Kind
		old, new *int
		code     string
	}{
		{"context by new", New, 4, Unchanged, ptr(4), ptr(4), "_4_4"},
		{"context by old", Old, 4, Unchanged, ptr(4), ptr(4), "_4_4"},
		{"removed", Old, 5, Removed, ptr(5), nil, "_5_5"},
		{"added", New, 5, Added, nil, ptr(5), "_6_5"},
		{"second added", New, 6, Added, nil, ptr(6), "_6_6"},
		{"context after the change", New, 7, Unchanged, ptr(6), ptr(7), "_6_7"},
		{"last line", Old, 8, Unchanged, ptr(8), ptr(9), "_8_9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Compute(refs, f, tc.side, tc.line, 0)
			if err != nil {
				t.Fatal(err)
			}
			if got.End.Kind != tc.kind {
				t.Errorf("kind %s, want %s", got.End.Kind, tc.kind)
			}
			p := got.Position
			if !eq(p.OldLine, tc.old) || !eq(p.NewLine, tc.new) {
				t.Errorf("old %v new %v, want %v %v", deref(p.OldLine), deref(p.NewLine), deref(tc.old), deref(tc.new))
			}
			if code := got.End.Code(f); code != sha("src/a.go")+tc.code {
				t.Errorf("line_code %s, want sha%s", code, tc.code)
			}
			if p.PositionType != "text" || p.BaseSHA != "b" || p.StartSHA != "s" || p.HeadSHA != "h" || p.LineRange != nil {
				t.Errorf("position %+v", p)
			}
		})
	}
}

// GitLab's DiffHunk reads each start as written and ignores the count,
// so an insertion's old counter is the line before it, and a new file's
// is 0 (lib/gitlab/word_diff/segments/diff_hunk.rb).
func TestComputeTakesZeroCountStartsAsGitLabDoes(t *testing.T) {
	for _, tc := range []struct {
		name, diff string
		side       Side
		line       int
		code       string
	}{
		{"new file", "@@ -0,0 +1,2 @@\n+one\n+two\n", New, 2, "_0_2"},
		{"insertion", "@@ -5,0 +6,2 @@\n+six\n+seven\n", New, 7, "_5_7"},
		{"deletion", "@@ -6,2 +5,0 @@\n-six\n-seven\n", Old, 7, "_7_5"},
		{"deleted file", "@@ -1,2 +0,0 @@\n-one\n-two\n", Old, 1, "_1_0"},
		{"single-line counts omitted", "@@ -4 +4 @@\n-four\n+FOUR\n", New, 4, "_5_4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := File{OldPath: "x", NewPath: "x", Diff: tc.diff}
			got, err := Compute(refs, f, tc.side, tc.line, 0)
			if err != nil {
				t.Fatal(err)
			}
			if code := got.End.Code(f); code != sha("x")+tc.code {
				t.Errorf("line_code %s, want sha%s", code, tc.code)
			}
		})
	}
}

func TestLineCodeHashesTheNewPathOfARename(t *testing.T) {
	f := File{OldPath: "old/name.txt", NewPath: "new/name.txt", Diff: oneHunk}
	got, err := Compute(refs, f, New, 4, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.End.Code(f), sha("new/name.txt")+"_") {
		t.Errorf("line_code %s hashes the wrong path", got.End.Code(f))
	}
	if got.Position.OldPath != "old/name.txt" || got.Position.NewPath != "new/name.txt" {
		t.Errorf("paths %q %q", got.Position.OldPath, got.Position.NewPath)
	}
	if (File{OldPath: "gone.txt"}).path() != "gone.txt" {
		t.Error("a file with no new path hashes its old one")
	}
}

func TestNoNewlineMarkersAdvanceNothing(t *testing.T) {
	diff := "@@ -1,2 +1,2 @@\n one\n-two\n\\ No newline at end of file\n+two\n\\ No newline at end of file\n"
	f := File{OldPath: "x", NewPath: "x", Diff: diff}
	got, err := Compute(refs, f, New, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.End.Kind != Added || got.End.Code(f) != sha("x")+"_3_2" {
		t.Errorf("got %+v %s", got.End, got.End.Code(f))
	}
}

func TestFileHeadersBeforeTheFirstHunkAreSkipped(t *testing.T) {
	diff := "--- a/x\n+++ b/x\n" + oneHunk
	hunks, err := Parse(diff)
	if err != nil || len(hunks) != 1 || hunks[0].NewStart != 3 || hunks[0].NewEnd != 9 || hunks[0].OldEnd != 8 {
		t.Fatalf("hunks %+v, %v", hunks, err)
	}
}

func TestARangeAnchorsOnItsLastLine(t *testing.T) {
	f := File{OldPath: "x", NewPath: "x", Diff: oneHunk}
	got, err := Compute(refs, f, New, 4, 6)
	if err != nil {
		t.Fatal(err)
	}
	p := got.Position
	if p.NewLine == nil || *p.NewLine != 6 || p.OldLine != nil {
		t.Errorf("anchor old %v new %v, want the last line, new 6", deref(p.OldLine), deref(p.NewLine))
	}
	lr := p.LineRange
	if lr == nil {
		t.Fatal("no line_range")
	}
	if lr.Start.LineCode != sha("x")+"_4_4" || lr.Start.Type != "" || deref(lr.Start.OldLine) != 4 || deref(lr.Start.NewLine) != 4 {
		t.Errorf("start %+v", lr.Start)
	}
	if lr.End.LineCode != sha("x")+"_6_6" || lr.End.Type != "new" || lr.End.OldLine != nil || deref(lr.End.NewLine) != 6 {
		t.Errorf("end %+v", lr.End)
	}
	if got.Start.New != 4 || got.End.New != 6 {
		t.Errorf("covers %d–%d", got.Start.New, got.End.New)
	}

	same, err := Compute(refs, f, New, 4, 4)
	if err != nil || same.Position.LineRange != nil {
		t.Errorf("a range of one line is a single-line comment: %+v %v", same.Position.LineRange, err)
	}
	removed, err := Compute(refs, f, Old, 4, 5)
	if err != nil || removed.Position.LineRange.End.Type != "old" || removed.Position.NewLine != nil {
		t.Errorf("an old-side range ending on a removed line: %+v %v", removed.Position, err)
	}
}

func TestThePositionMarshalsAsTheAPITakesIt(t *testing.T) {
	f := File{OldPath: "x", NewPath: "x", Diff: oneHunk}
	got, err := Compute(refs, f, New, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got.Position)
	want := `{"base_sha":"b","start_sha":"s","head_sha":"h","position_type":"text","old_path":"x","new_path":"x","new_line":5}`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
}

const twoHunks = `@@ -1,3 +1,3 @@
 one
-two
+TWO
 three
@@ -20,3 +20,4 @@
 twenty
+twenty and a half
 twenty-one
 twenty-two
`

func TestComputeRefusesWhatItCannotPlace(t *testing.T) {
	f := File{OldPath: "x", NewPath: "x", Diff: twoHunks}
	for _, tc := range []struct {
		name       string
		file       File
		side       Side
		line, end  int
		wantInText []string
	}{
		{"between hunks", f, New, 10, 0, []string{"new line 10 is not in the diff", "new lines 1–3", "new lines 20–23"}},
		{"removed line asked on the new side", f, New, 24, 0, []string{"new line 24"}},
		{"range across hunks", f, New, 2, 21, []string{"different hunks", "new lines 1–3"}},
		{"range end outside", f, New, 2, 9, []string{"new line 9 is not in the diff"}},
		{"range backwards", f, New, 3, 2, []string{"end_line 2 is before line 3"}},
		{"bad side", f, Side("left"), 1, 0, []string{"side must be new or old"}},
		{"zero line", f, New, 0, 0, []string{"start at 1"}},
		{"no diff", File{OldPath: "bin", NewPath: "bin"}, New, 1, 0, []string{"no diff lines"}},
		{"broken header", File{Diff: "@@ -x +1 @@\n+a\n"}, New, 1, 0, []string{"hunk header"}},
		{"broken count", File{Diff: "@@ -1,y +1 @@\n+a\n"}, New, 1, 0, []string{"hunk header"}},
		{"unterminated header", File{Diff: "@@ -1 +1\n+a\n"}, New, 1, 0, []string{"hunk header"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compute(refs, tc.file, tc.side, tc.line, tc.end)
			var e *Error
			if err == nil || !asError(err, &e) {
				t.Fatalf("got %v, want a *diffpos.Error", err)
			}
			for _, w := range tc.wantInText {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("%q does not say %q", err, w)
				}
			}
		})
	}
}

func TestTheNearestHunksAreNamedWhenThereAreMany(t *testing.T) {
	var b strings.Builder
	for i := range 6 {
		start := 1 + i*20
		fmt.Fprintf(&b, "@@ -%d,1 +%d,1 @@\n-a\n+b\n", start, start)
	}
	_, err := Compute(refs, File{Diff: b.String()}, New, 62, 0)
	if err == nil {
		t.Fatal("placed a line outside every hunk")
	}
	for _, w := range []string{"new line 41", "new line 61", "new line 81", "the 3 nearest of 6 hunks"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("%q does not say %q", err, w)
		}
	}
	if strings.Contains(err.Error(), "new line 1,") {
		t.Errorf("%q names a far hunk", err)
	}
	_, err = Compute(refs, File{Diff: "@@ -5,0 +6,2 @@\n+a\n+b\n"}, Old, 5, 0)
	if err == nil || !strings.Contains(err.Error(), "no old lines near 5") {
		t.Errorf("an insertion-only hunk on the old side: %v", err)
	}
}

// ----------------------------------------------------------- generated

// TestComputeAgreesWithTheAlignmentOnGeneratedDiffs builds git-style
// diffs from random files and checks every line on both sides: a line in
// a hunk is placed at its own number, as added, removed or unchanged by
// the alignment the diff came from, an unchanged line carries its partner
// on the other side, and line_code carries GitLab's counters; a line in
// no hunk is refused.
func TestComputeAgreesWithTheAlignmentOnGeneratedDiffs(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	for iter := range 400 {
		oldLines, newLines := randomFiles(rng)
		d := unified(oldLines, newLines, 3)
		f := File{OldPath: "gen.txt", NewPath: "gen.txt", Diff: d.text}
		for _, side := range []Side{Old, New} {
			n := len(newLines)
			if side == Old {
				n = len(oldLines)
			}
			for line := 1; line <= n; line++ {
				want, inHunk := d.at(side, line)
				got, err := Compute(refs, f, side, line, 0)
				if !inHunk {
					if err == nil {
						t.Fatalf("iter %d: %s line %d is outside every hunk and was placed\n%s", iter, side, line, d.text)
					}
					continue
				}
				if err != nil {
					t.Fatalf("iter %d: %s line %d: %v\n%s", iter, side, line, err, d.text)
				}
				if got.End != want {
					t.Fatalf("iter %d: %s line %d: got %+v, want %+v\n%s", iter, side, line, got.End, want, d.text)
				}
				if got.End.Kind == Unchanged && oldLines[want.Old-1] != newLines[want.New-1] {
					t.Fatalf("iter %d: unchanged line pairs different text", iter)
				}
			}
		}
	}
}

func randomFiles(rng *rand.Rand) ([]string, []string) {
	n := rng.IntN(40)
	old := make([]string, n)
	for i := range old {
		old[i] = fmt.Sprintf("line %d", rng.IntN(1000))
	}
	var nw []string
	for _, l := range old {
		switch rng.IntN(8) {
		case 0: // removed
		case 1: // changed
			nw = append(nw, l+" changed")
		case 2: // inserted before
			nw = append(nw, fmt.Sprintf("new %d", rng.IntN(1000)), l)
		default:
			nw = append(nw, l)
		}
	}
	if rng.IntN(3) == 0 {
		nw = append(nw, "appended")
	}
	return old, nw
}

// genDiff is a generated diff and, per side, what each line in a hunk is
// by the generator's own reckoning.
type genDiff struct {
	text     string
	old, new map[int]Line
}

func (d genDiff) at(side Side, n int) (Line, bool) {
	m := d.new
	if side == Old {
		m = d.old
	}
	l, ok := m[n]
	return l, ok
}

type op struct {
	kind   Kind
	oi, ni int // 0-based indexes; the one a kind lacks is where it sits
}

// unified writes a diff as git does: an LCS alignment, hunks of changes
// with ctx lines of context merged when they touch, and a zero count's
// start written as the line before. Its expectations follow GitLab's
// counters from those headers, computed here from the alignment rather
// than by parsing.
func unified(a, b []string, ctx int) genDiff {
	ops := align(a, b)
	d := genDiff{old: map[int]Line{}, new: map[int]Line{}}
	var changed []int
	for i, o := range ops {
		if o.kind != Unchanged {
			changed = append(changed, i)
		}
	}
	var sb strings.Builder
	for k := 0; k < len(changed); {
		lo := max(changed[k]-ctx, 0)
		hi := changed[k] + ctx
		k++
		for k < len(changed) && changed[k]-ctx <= hi+1 {
			hi = changed[k] + ctx
			k++
		}
		hi = min(hi, len(ops)-1)
		hunk := ops[lo : hi+1]
		oldCount, newCount := 0, 0
		for _, o := range hunk {
			if o.kind != Added {
				oldCount++
			}
			if o.kind != Removed {
				newCount++
			}
		}
		oldStart, newStart := hunk[0].oi+1, hunk[0].ni+1
		if oldCount == 0 {
			oldStart--
		}
		if newCount == 0 {
			newStart--
		}
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		oc, nc := oldStart, newStart
		for _, o := range hunk {
			switch o.kind {
			case Unchanged:
				fmt.Fprintf(&sb, " %s\n", a[o.oi])
				l := Line{Kind: Unchanged, Old: o.oi + 1, New: o.ni + 1}
				d.old[l.Old], d.new[l.New] = l, l
				oc, nc = o.oi+2, o.ni+2
			case Removed:
				fmt.Fprintf(&sb, "-%s\n", a[o.oi])
				l := Line{Kind: Removed, Old: o.oi + 1, New: nc}
				d.old[l.Old] = l
				oc = o.oi + 2
			case Added:
				fmt.Fprintf(&sb, "+%s\n", b[o.ni])
				l := Line{Kind: Added, Old: oc, New: o.ni + 1}
				d.new[l.New] = l
				nc = o.ni + 2
			}
		}
	}
	d.text = sb.String()
	return d
}

// align is a longest-common-subsequence edit script, removals before
// additions at each change, as git prints them.
func align(a, b []string) []op {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var ops []op
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			ops = append(ops, op{Unchanged, i, j})
			i++
			j++
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			ops = append(ops, op{Removed, i, j})
			i++
		default:
			ops = append(ops, op{Added, i, j})
			j++
		}
	}
	return ops
}

// ----------------------------------------------------------- helpers

func ptr(n int) *int { return &n }

func eq(a, b *int) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func asError(err error, target **Error) bool { return errors.As(err, target) }
