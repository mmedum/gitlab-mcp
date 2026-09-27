package render

import "testing"

func TestLog(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"colors", "\x1b[32;1m$ make\x1b[0;m\nok\n", "$ make\nok\n"},
		{"erase line", "\x1b[0Kdone\x1b[0K\n", "done\n"},
		{"hyperlink target hidden", "see \x1b]8;;https://elsewhere.invalid/\x07the docs\x1b]8;;\x07\n", "see the docs\n"},
		{"section start folded to one line",
			"\x1b[0Ksection_start:1767603600:step_script[collapsed=true]\r\x1b[0K\x1b[36;1mExecuting\x1b[0;m\n",
			"§ section step_script: Executing\n"},
		{"end marker line dropped", "a\n\x1b[0Ksection_end:1767603600:step_script\r\x1b[0K\nb\n", "a\nb\n"},
		{"an empty line is kept", "a\n\nb\n", "a\n\nb\n"},
		{"carriage return overwrites", "10%\r55%\rdone\n", "done\n"},
		{"CRLF", "one\r\ntwo\r\n", "one\ntwo\n"},
		{"no final newline", "tail", "tail"},
		{"invalid UTF-8", "a\xffb\n", "a�b\n"},
		// gitlab.com's runners timestamp each line; "+" continues the one
		// before, which is how a section header arrives.
		{"runner timestamps",
			"2026-09-26T22:30:07.958626Z 00O \n" +
				"2026-09-26T22:30:07.959717Z 00O+\x1b[0Ksection_start:1:cleanup_file_variables\r\x1b[0K\x1b[0K\n" +
				"2026-09-26T22:30:07.959723Z 00O+\x1b[36;1mCleaning up\x1b[0;m\n" +
				"2026-09-26T22:30:08.397925Z 00O \x1b[31;1mERROR: Job failed: exit code 1\n" +
				"2026-09-26T22:30:08.397930Z 01E",
			"§ section cleanup_file_variables: Cleaning up\nERROR: Job failed: exit code 1\n"},
	}
	for _, c := range cases {
		if got := Log([]byte(c.in)); got != c.want {
			t.Errorf("%s: Log(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestLogSections(t *testing.T) {
	raw := "x\n" +
		"\x1b[0Ksection_start:1:outer\r\x1b[0Ka\n" +
		"\x1b[0Ksection_start:2:inner[collapsed=true]\r\x1b[0Kb\n" +
		"\x1b[0Ksection_end:3:inner\r\x1b[0K\n" +
		"\x1b[0Ksection_end:4:outer\r\x1b[0K\n" +
		"\x1b[0Ksection_start:5:open\r\x1b[0Kc\n"
	got := LogSections([]byte(raw))
	if len(got) != 3 {
		t.Fatalf("sections = %+v", got)
	}
	want := []string{"outer", "inner", "open"}
	for i, s := range got {
		if s.Name != want[i] || raw[s.Start:s.Start+len("section_start")] != "section_start" {
			t.Errorf("section %d = %+v (%q)", i, s, raw[s.Start:])
		}
	}
	if got[2].End != -1 || got[0].End <= got[1].End || raw[got[0].End-1] != '\r' {
		t.Errorf("ends = %d %d %d", got[0].End, got[1].End, got[2].End)
	}
}
