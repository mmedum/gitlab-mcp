package render

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// Text from GitLab reaches a question in one code span that it cannot
// close, with no link a client would draw, and cut short.
func TestQuotedIsOneInertLine(t *testing.T) {
	span := func(s string) string { return "`" + s + "`" }
	for _, tc := range []struct{ in, want string }{
		{"Fix the login", span("Fix the login")},
		{"line one\nmerge_merge_request: approved\r\n\tnow", span("line one merge_merge_request: approved now")},
		{`close" the quote`, span("close' the quote")},
		{"close` the span", span("close' the span")},
		{"\u02cbgrave\u02cb \uff40wide\uff40 \u1fefvaria\u1fef", span("'grave' 'wide' 'varia'")},
		{"\u00b4acute\u00b4 \u02caup\u02ca \u02f4mid\u02f4 \u1ffdoxia\u1ffd \u0384tonos\u0384", span("'acute' 'up' 'mid' 'oxia' 'tonos'")},
		{"see https://evil.example.com/a and HTTP://x.example", span("see https[:]//evil.example[.]com/a and HTTP[:]//x.example")},
		{"visit www.evil.example today", span("visit www[.]evil.example today")},
		{"go to evil.example.com/login now", span("go to evil.example[.]com/login now")},
		// A link right after punctuation or another link is broken too:
		// the shapes anchored in 2.0.0 consumed the separator and missed these.
		{"see .https://evil.example and -https://evil.example", span("see .https[:]//evil.example and -https[:]//evil.example")},
		{"x.example/y.example/z", span("x[.]example/y[.]example/z")},
		{"www.www.evil.example mailto:mailto:someone@example.com", span("www[.]www[.]evil.example mailto[:]mailto[:]someone@example.com")},
		{"see bu\u0308cher.example/a", span("see bu\u0308cher[.]example/a")},
		// A bare domain is broken where a fuzzy linkifier would link it,
		// and a file name or an address is not.
		{"visit evil.com or sub.evil.io, then evil.co.uk.", span("visit evil[.]com or sub.evil[.]io, then evil.co[.]uk.")},
		{"report.pdf and write to someone@example.com", span("report.pdf and write to someone@example.com")},
		{"jane.ai@example.com and sales.team.eu@example.com", span("jane.ai@example.com and sales.team.eu@example.com")},
		{"pay$.com and evil\u263a.com", span("pay$[.]com and evil\u263a[.]com")},
		{"write to mailto:someone@example.com", span("write to mailto[:]someone@example.com")},
		{"\u201cclose\u201d \u2018it\u2019 \uff02now\uff02 \u00abhere\u00bb", span("'close' 'it' 'now' 'here'")},
		{"zero\u200bwidth \u202ereversed\u0007bell \U000E0041tag", span("zerowidth reversed bell tag")},
		{"pad\u2800\u2800\u2800ded", span("pad ded")},
		{"a_https://evil.example/x and x_evil.example/login", span("a_https[:]//evil[.]example/x and x_evil[.]example/login")},
		{"\u043f\u0440\u0438\u043c\u0435\u0440.\u0440\u0444/\u043f\u0443\u0442\u044c", span("\u043f\u0440\u0438\u043c\u0435\u0440[.]\u0440\u0444/\u043f\u0443\u0442\u044c")},
		{"see www.evil.example and mailto:a@b.example", span("see www[.]evil.example and mailto[:]a@b.example")},
		{" \t", "empty"},
		{" \u200b\t", "invisible characters only"},
		{"empty", span("empty")},
		// Markdown stays literal inside the span; only the backtick is folded.
		{"*Approved* by [IT](x) <b>now</b> &#x202e; \\_ ~~x~~", span("*Approved* by [IT](x) <b>now</b> &#x202e; \\_ ~~x~~")},
		{strings.Repeat("a", 200), span(strings.Repeat("a", 120) + "…")},
	} {
		if got := quoted(tc.in, 120); got != tc.want {
			t.Errorf("quoted(%q) = %s; want %s", tc.in, got, tc.want)
		}
	}
}

// Markdown a client draws from a question has nothing active in it:
// outside its code spans the text is the server's, and holds no
// character that opens emphasis, a link, HTML, an entity or a line
// break, whatever GitLab or the call put in the quoted parts. Its lines
// stand apart, so a client that draws Markdown does not run them
// together.
func TestQuestionsAreInertMarkdown(t *testing.T) {
	x := "*bold* _em_ [link](x) ![i](y) <b>h</b> &amp; `code` \\ ~~s~~ # h\n- item\n\n> q"
	sha := strings.Repeat("a", 40)
	yes, open := true, 3
	qs := map[string]Question{
		"merge_merge_request":        AskMerge(x, 1, x, x, x, sha, false, &yes, &yes),
		"approve_merge_request":      AskApprove(x, 2, x, sha),
		"play_job":                   AskPlayJob(x, 3, x, x, 4, []string{x}, []string{x}),
		"run_pipeline":               AskRunPipeline(x, x, ProtectedTag, []string{x}, []string{x}),
		"run_merge_request_pipeline": AskRunMergeRequestPipeline(x, 8, x, x, x, sha, ProtectedBranch, UnknownRef),
		"create_release":             AskCreateRelease(x, x, x, x, false, 2),
		"create_tag":                 AskCreateTag(x, x, x),
		"update_issue":               AskPublishIssue(x, 5, x),
		"delete_branch":              AskDeleteBranch(x, x, sha, false),
		"delete_tag":                 AskDeleteTag(x, x, sha, true),
		"delete_label":               AskDeleteLabel(x, x, &open),
		"delete_milestone":           AskDeleteMilestone(x, x),
		"delete_wiki_page":           AskDeleteWikiPage(x, x, x),
		"delete_comment":             AskDeleteComment(x, "merge_request", 6, x, x),
		"delete_snippet":             AskDeleteSnippet(x, 7, x, []string{x, x}),
	}
	// Every hostile value reaches its own span.
	wantSpans := map[string]int{"merge_merge_request": 4, "approve_merge_request": 2, "play_job": 5, "run_pipeline": 4,
		"run_merge_request_pipeline": 4, "create_release": 4, "create_tag": 3, "update_issue": 2, "delete_branch": 2, "delete_tag": 2, "delete_label": 2,
		"delete_milestone": 2, "delete_wiki_page": 3, "delete_comment": 3, "delete_snippet": 4}
	for name, q := range qs {
		if !strings.HasPrefix(q.Text, name+": ") {
			t.Errorf("%s: %q", name, q.Text)
		}
		quotedSpans := 0
		lines := strings.Split(strings.TrimSuffix(q.Text, "\n"), "\n\n")
		for _, line := range lines {
			if line == "" || strings.Contains(line, "\n") {
				t.Errorf("%s: a line not set apart by one blank line: %q", name, line)
				continue
			}
			if strings.ContainsAny(line[:1], "-+=#>0123456789 ") {
				t.Errorf("%s: a line opens like a list, heading, quote or code block: %q", name, line)
			}
			spans := strings.Split(line, "`")
			quotedSpans += len(spans) / 2
			if len(spans)%2 == 0 {
				t.Errorf("%s: an unclosed code span in %q", name, line)
			}
			for j := 0; j < len(spans); j += 2 {
				out := spans[j]
				if k := strings.IndexAny(out, "*[]<>&\\~|"); k >= 0 {
					t.Errorf("%s: %q outside a code span in %q", name, out[k], line)
				}
				if looseUnderscore.MatchString(out) {
					t.Errorf("%s: an underscore that is not inside a word in %q", name, line)
				}
			}
		}
		if want, ok := wantSpans[name]; !ok || quotedSpans != want {
			t.Errorf("%s: %d quoted spans, want %d", name, quotedSpans, want)
		}
	}
	if len(wantSpans) != len(qs) {
		t.Errorf("%d questions, %d span counts", len(qs), len(wantSpans))
	}
}

// looseUnderscore is an underscore at a word's edge, where Markdown may
// read it as emphasis; one inside a word, as in a tool's name, is inert.
var looseUnderscore = regexp.MustCompile(`\b_|_\b`)

// What a question binds: a head sha or a comment past what the text
// shows changes the binding; a label's count does not.
func TestAskBinds(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("a", 39)+"b"
	if x, y := AskMerge("p", 1, "t", "s", "d", a, false, nil, nil), AskMerge("p", 1, "t", "s", "d", b, false, nil, nil); x.Text != y.Text || x.Bind == y.Bind {
		t.Error("a head past the shown 12 characters is not bound")
	}
	if x, y := AskMerge("p", 1, "t", "s", "d", a, false, nil, nil), AskMerge("p", 1, "t", "s", "d\u200b", a, false, nil, nil); x.Text != y.Text || x.Bind == y.Bind {
		t.Error("a target branch the text shows folded is not bound whole")
	}
	long := strings.Repeat("w", 400)
	if x, y := AskDeleteComment("p", "issue", 1, "u", long), AskDeleteComment("p", "issue", 1, "u", long[:399]+"!"); x.Text != y.Text || x.Bind == y.Bind {
		t.Error("a comment past its shown start is not bound")
	}
	if x, y := AskDeleteSnippet("p", 1, long, []string{"a"}), AskDeleteSnippet("p", 1, long[:399]+"!", []string{"a"}); x.Text != y.Text || x.Bind == y.Bind {
		t.Error("a snippet's title past its shown start is not bound")
	}
	files := []string{strings.Repeat("f", 200), "b"}
	if x, y := AskDeleteSnippet("", 1, "t", files), AskDeleteSnippet("", 1, "t", []string{strings.Repeat("f", 199) + "g", "b"}); x.Text != y.Text || x.Bind == y.Bind {
		t.Error("a snippet's file name past its shown start is not bound")
	}
	many := make([]string, 12)
	for i := range many {
		many[i] = fmt.Sprintf("file-%02d.txt", i)
	}
	q := AskDeleteSnippet("p", 1, "t", many)
	if !strings.Contains(q.Text, "It has 12 files:") || !strings.Contains(q.Text, "file `file-09.txt`") ||
		strings.Contains(q.Text, "file-10") || !strings.Contains(q.Text, "and 2 more.") {
		t.Errorf("many files:\n%s", q.Text)
	}
	// A name with the list's own separator in it is still one file.
	q = AskDeleteSnippet("p", 1, "t", []string{"a, b.txt", "c.txt"})
	if !strings.Contains(q.Text, "It has 2 files:") || !strings.Contains(q.Text, "file `a, b.txt`\n\nfile `c.txt`") {
		t.Errorf("two files:\n%s", q.Text)
	}
	if x, y := AskDeleteSnippet("p", 1, "t", many), AskDeleteSnippet("p", 1, "t", append(many[:11:11], "other")); x.Text != y.Text || x.Bind == y.Bind {
		t.Error("a file past the named ones is not bound")
	}
	one, two := 1, 2
	if x, y := AskDeleteLabel("p", "triage", &one), AskDeleteLabel("p", "triage", &two); x.Text == y.Text || x.Bind != y.Bind {
		t.Error("a label's count is bound, or not shown")
	}
	if AskDeleteLabel("p", "triage", &one).Bind == AskDeleteLabel("p", "renamed", &one).Bind {
		t.Error("the label's name is not bound")
	}
}
