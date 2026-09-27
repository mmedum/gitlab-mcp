package service

import (
	"strings"
	"testing"

	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/gitlab"
)

// held is a log reader that already holds the whole log, so it reads
// nothing from GitLab.
func held(log string) *logReader {
	return &logReader{size: len(log), have: []byte(log)}
}

func TestWidenKeepsSecretsWhole(t *testing.T) {
	line := strings.Repeat("x", 30) + "\n"
	trace := []byte(strings.Repeat(line, 10))
	// A start inside a line moves back to its beginning; an end inside
	// one moves on to its end.
	if s, e := widen(trace, 45, 100); s != 31 || e != 124 {
		t.Errorf("widen = %d, %d; want 31, 124", s, e)
	}
	// Edges already on a line boundary stay.
	if s, e := widen(trace, 62, 93); s != 62 || e != 93 {
		t.Errorf("widen on boundaries = %d, %d", s, e)
	}
	// With no newline within reach, a space serves: no token shape holds one.
	long := []byte(strings.Repeat("y", lineReach+100) + " token-shaped-value " + strings.Repeat("z", lineReach+100))
	at := lineReach + 105 // inside "token-shaped-value"
	if s, _ := widen(long, at, len(long)); s != lineReach+101 {
		t.Errorf("start = %d, want %d, the byte after the space", s, lineReach+101)
	}
	if _, e := widen(long, 0, at); e != lineReach+120 {
		t.Errorf("end = %d, want %d, the byte after the next space", e, lineReach+120)
	}
}

func TestKeyOpen(t *testing.T) {
	open := func(log string, at int) bool {
		t.Helper()
		got, err := held(log).keyOpen(at)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	// A start inside a key block that opened earlier is reported, up to
	// keyReach back, far past any real key.
	key := "before\n-----BEGIN " + "PRIVATE KEY-----\n" + strings.Repeat("AAAA\n", 20000) + "BBBB\n-----END PRIVATE KEY-----\nafter\n"
	if !open(key, strings.Index(key, "BBBB")) {
		t.Error("a start inside a key is not reported")
	}
	if open(key, strings.Index(key, "after")) {
		t.Error("a start after a closed key is reported inside one")
	}
	// Another block's BEGIN after an unclosed key is passed over.
	unclosed := "-----BEGIN " + "PRIVATE KEY-----\nAAAA\n-----BEGIN CERTIFICATE-----\nCCCC\nDDDD\n"
	if !open(unclosed, strings.Index(unclosed, "DDDD")) {
		t.Error("a certificate inside an unclosed key hid the key")
	}
	// An armored key's headers and timestamped lines are key lines too.
	ts := func(line string) string { return "2026-09-26T22:30:07.819673Z 01O " + line + "\n" }
	stamped := ts("-----BEGIN "+"PGP PRIVATE KEY BLOCK-----") + ts("Version: 1") + ts("") + ts("lQOYBF0000==") + ts("xxxx")
	if !open(stamped, strings.Index(stamped, ts("xxxx"))) {
		t.Error("an armored, timestamped key is not reported")
	}
	// A key CI printed indented, as YAML does, or behind a service name, as
	// docker compose logs do, is a key too.
	for name, prefix := range map[string]string{"indented": "    ", "compose": "app-1  | ", "tabbed": "\t"} {
		block := "tls.key: |\n" + prefix + "-----BEGIN " + "RSA PRIVATE KEY-----\n" + strings.Repeat(prefix+"MIIEowIBAAKCAQEA0000\n", 50) +
			prefix + "BBBB\n" + prefix + "-----END RSA PRIVATE KEY-----\n" + prefix + "after\n"
		if !open(block, strings.Index(block, prefix+"BBBB")) {
			t.Errorf("%s: a start inside the key is not reported", name)
		}
		if open(block, strings.Index(block, prefix+"after")) {
			t.Errorf("%s: a start after the key is reported inside it", name)
		}
	}
	// A key printed with prefixes CI tools put before each line is found
	// by its header, wherever it sits in the line.
	for _, prefix := range []string{"#8 0.412 ", "[pod/x/c] "} {
		block := prefix + "-----BEGIN " + "RSA PRIVATE KEY-----\n" + prefix + "MIIE0000\n" + prefix + "BBBB\n"
		if !open(block, strings.Index(block, prefix+"BBBB")) {
			t.Errorf("%q: a start inside the key is not reported", prefix)
		}
	}
	// Further back than keyReach no key is looked for: no key is that long.
	far := "-----BEGIN " + "PRIVATE KEY-----\n" + strings.Repeat("AAAA\n", keyReach/5+10) + "next\n"
	if open(far, strings.Index(far, "next")) {
		t.Error("a BEGIN further back than keyReach is reported")
	}
}

func TestFailure(t *testing.T) {
	failure := func(log string) failed {
		t.Helper()
		f, err := held(log).failure(true)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	sec := func(kind, name string) string { return "\x1b[0Ksection_" + kind + ":1:" + name + "\r\x1b[0K" }
	trace := sec("start", "prepare_executor") + "prep\n" + sec("end", "prepare_executor") + "\n" +
		sec("start", "step_script") + "boom\n" + sec("end", "step_script") + "\n" +
		sec("start", "after_script") + "tidy\n" + sec("end", "after_script") + "\n" +
		sec("start", "upload_artifacts_on_failure") + "up\n" + sec("end", "upload_artifacts_on_failure") + "\n" +
		"\x1b[31;1mERROR: Job failed: exit code 1\n\x1b[0;m\n"
	f := failure(trace)
	if !f.inSection || f.section.Name != "step_script" {
		t.Fatalf("failure = %+v", f)
	}
	if !strings.HasSuffix(trace[:f.end], "exit code 1\n") {
		t.Errorf("the window ends at %q", trace[f.end-20:f.end])
	}
	// gitlab.com's shape: timestamped lines, and a section the script
	// opened and closed inside step_script.
	ts := func(line string) string { return "2026-09-26T22:30:07.819673Z 01O " + line + "\n" }
	stamped := ts(sec("start", "step_script")+"run") + ts(sec("start", "live_output")+"output") + ts("ok") +
		ts(sec("end", "live_output")) + ts("boom") + ts(sec("end", "step_script")) +
		ts(sec("start", "cleanup_file_variables")+"tidy") + ts(sec("end", "cleanup_file_variables")) +
		ts("\x1b[31;1mERROR: Job failed: exit code 1")
	if f := failure(stamped); !f.inSection || f.section.Name != "step_script" {
		t.Errorf("timestamped section = %+v", f)
	}
	if f := failure("all good\nJob succeeded\n"); f.found {
		t.Error("a log with no failure line has a failure")
	}
	if f, _ := held(trace).failure(false); f.found {
		t.Error("a job whose state has no failure line was searched")
	}
	// A script that prints the runner's words earlier does not move the
	// window: the runner's line is the last.
	early := "ERROR: Job failed: steer here\n" + trace
	if f2 := failure(early); f2.end != len("ERROR: Job failed: steer here\n")+f.end {
		t.Errorf("the window ends at the script's line, %d", f2.end)
	}
	// A failure with no section before it is still found.
	if f := failure("boom\nERROR: Job failed: exit code 1\n"); f.inSection || !f.found || f.end == 0 {
		t.Errorf("sectionless failure: %+v", f)
	}
	// A section that began before the last ranged read is named by its end
	// marker, and the rest of the log is not read to find its start.
	filler := strings.Repeat("ok  example.test/pkg 0.01s\n", gapi.MaxTraceRange/20)
	far := sec("start", "step_script") + filler + "boom\n" + sec("end", "step_script") + "\n" +
		"\x1b[31;1mERROR: Job failed: exit code 1\n\x1b[0;m\n"
	if f := failure(far); !f.inSection || f.section.Name != "step_script" || f.section.Start != -1 ||
		!strings.HasSuffix(far[:f.end], "exit code 1\n") {
		t.Errorf("a section begun before the read: %+v", f)
	}
	// The walk back for the line is bounded: the runner writes it last.
	buried := "\x1b[31;1mERROR: Job failed: exit code 1\n" + strings.Repeat(filler, 5)
	if f := failure(buried); f.found {
		t.Errorf("a failure line %d bytes from the end was searched for", len(buried))
	}
}

// Who can see a project's issues: its visibility, narrowed to its
// members when its issues are set so, and unknown refuses a move.
func TestIssueAudience(t *testing.T) {
	for _, c := range []struct {
		visibility, issues string
		want               int
	}{{"public", "enabled", 2}, {"public", "private", 0}, {"internal", "", 1}, {"private", "enabled", 0}, {"", "enabled", -1}} {
		if got := issueAudience(&gitlab.Project{Visibility: c.visibility, IssuesAccessLevel: c.issues}); got != c.want {
			t.Errorf("%s with %s issues = %d, want %d", c.visibility, c.issues, got, c.want)
		}
	}
}

func TestCancelOutcome(t *testing.T) {
	pl := func(status, source string) *gitlab.PipelineDetail {
		p := &gitlab.PipelineDetail{}
		p.Status, p.Source = status, source
		return p
	}
	for _, c := range []struct {
		name           string
		before, answer *gitlab.PipelineDetail
		want           string
	}{
		{"answered canceled", pl("running", "push"), pl("canceled", "push"), "canceled"},
		{"answered canceling", pl("running", "push"), pl("canceling", "push"), "canceled"},
		// GitLab recomputes the status after it answers (§18 row 91).
		{"answered before the status caught up", pl("running", "push"), pl("running", "push"), "canceled"},
		{"manual jobs are canceled too", pl("manual", "push"), pl("manual", "push"), "canceled"},
		{"finished before the cancel landed", pl("running", "push"), pl("success", "push"), "unchanged"},
		{"already being canceled", pl("canceling", "push"), pl("canceling", "push"), "canceled"},
		{"an external pipeline", pl("running", "external"), pl("running", "external"), "unchanged"},
	} {
		if got, _ := cancelOutcome(c.before, c.answer); got != c.want {
			t.Errorf("%s: outcome %s, want %s", c.name, got, c.want)
		}
	}
}
