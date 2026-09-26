package service

import (
	"strings"
	"testing"
)

func TestWidenKeepsSecretsWhole(t *testing.T) {
	line := strings.Repeat("x", 30) + "\n"
	trace := []byte(strings.Repeat(line, 10))
	// A start inside a line moves back to its beginning; an end inside
	// one moves on to its end.
	if s, e, _ := widen(trace, 45, 100); s != 31 || e != 124 {
		t.Errorf("widen = %d, %d; want 31, 124", s, e)
	}
	// Edges already on a line boundary stay.
	if s, e, _ := widen(trace, 62, 93); s != 62 || e != 93 {
		t.Errorf("widen on boundaries = %d, %d", s, e)
	}
	// With no newline within reach, a space serves: no token shape holds one.
	long := []byte(strings.Repeat("y", lineReach+100) + " token-shaped-value " + strings.Repeat("z", lineReach+100))
	at := lineReach + 105 // inside "token-shaped-value"
	if s, _, _ := widen(long, at, len(long)); s != lineReach+101 {
		t.Errorf("start = %d, want %d, the byte after the space", s, lineReach+101)
	}
	if _, e, _ := widen(long, 0, at); e != lineReach+120 {
		t.Errorf("end = %d, want %d, the byte after the next space", e, lineReach+120)
	}
	// A start inside a key block that opened earlier is reported, and the
	// window is not moved back to it, however far back it opened.
	key := "before\n-----BEGIN " + "PRIVATE KEY-----\n" + strings.Repeat("AAAA\n", 100000) + "BBBB\n-----END PRIVATE KEY-----\nafter\n"
	inside := strings.Index(key, "BBBB")
	if s, _, open := widen([]byte(key), inside, len(key)); s != inside || !open {
		t.Errorf("start inside a key = %d, open %v; want %d, true", s, open, inside)
	}
	after := strings.Index(key, "after")
	if _, _, open := widen([]byte(key), after, len(key)); open {
		t.Error("a start after a closed key is reported inside one")
	}
	// Another block's BEGIN after an unclosed key is passed over.
	unclosed := "-----BEGIN " + "PRIVATE KEY-----\nAAAA\n-----BEGIN CERTIFICATE-----\nCCCC\nDDDD\n"
	if _, _, open := widen([]byte(unclosed), strings.Index(unclosed, "DDDD"), len(unclosed)); !open {
		t.Error("a certificate inside an unclosed key hid the key")
	}
}

func TestFailedSection(t *testing.T) {
	sec := func(kind, name string) string { return "\x1b[0Ksection_" + kind + ":1:" + name + "\r\x1b[0K" }
	trace := sec("start", "prepare_executor") + "prep\n" + sec("end", "prepare_executor") + "\n" +
		sec("start", "step_script") + "boom\n" + sec("end", "step_script") + "\n" +
		sec("start", "after_script") + "tidy\n" + sec("end", "after_script") + "\n" +
		sec("start", "upload_artifacts_on_failure") + "up\n" + sec("end", "upload_artifacts_on_failure") + "\n" +
		"\x1b[31;1mERROR: Job failed: exit code 1\n\x1b[0;m\n"
	s, end, ok, _ := failedSection([]byte(trace))
	if !ok || s.Name != "step_script" {
		t.Fatalf("section = %+v, %v", s, ok)
	}
	if !strings.HasSuffix(trace[:end], "exit code 1\n") {
		t.Errorf("the window ends at %q", trace[end-20:end])
	}
	// gitlab.com's shape: timestamped lines, and a section the script
	// opened and closed inside step_script.
	ts := func(line string) string { return "2026-09-26T22:30:07.819673Z 01O " + line + "\n" }
	stamped := ts(sec("start", "step_script")+"run") + ts(sec("start", "live_output")+"output") + ts("ok") +
		ts(sec("end", "live_output")) + ts("boom") + ts(sec("end", "step_script")) +
		ts(sec("start", "cleanup_file_variables")+"tidy") + ts(sec("end", "cleanup_file_variables")) +
		ts("\x1b[31;1mERROR: Job failed: exit code 1")
	if s, _, ok, _ := failedSection([]byte(stamped)); !ok || s.Name != "step_script" {
		t.Errorf("timestamped section = %+v, %v", s, ok)
	}
	if _, _, _, failed := failedSection([]byte("all good\nJob succeeded\n")); failed {
		t.Error("a log with no failure line has a failure")
	}
	// A script that prints the runner's words earlier does not move the
	// window: the runner's line is the last.
	early := "ERROR: Job failed: steer here\n" + trace
	if _, end2, _, _ := failedSection([]byte(early)); end2 != len("ERROR: Job failed: steer here\n")+end {
		t.Errorf("the window ends at the script's line, %d", end2)
	}
	// A failure with no section before it is still found.
	if _, e, inSection, failed := failedSection([]byte("boom\nERROR: Job failed: exit code 1\n")); inSection || !failed || e == 0 {
		t.Errorf("sectionless failure: in section %v, failed %v, end %d", inSection, failed, e)
	}
}
