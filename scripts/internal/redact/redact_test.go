package redact

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrinterMasksEveryLine(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		want   string
		absent []string
	}{
		{"token", "got glpat-EXAMPLEabcdefghijklmnopqrst back",
			"got [MASKED gitlab-token] back", []string{"glpat-"}},
		{"address", "assigned to alice@example.com",
			"assigned to {email 1}", []string{"alice@"}},
		{"url", "see https://gitlab.example.com/example-group/app/-/issues/3",
			"see https://{host 1}/{path 1}", []string{"example-group", "gitlab.example.com"}},
		{"client id", "app " + strings.Repeat("a", 64),
			"app {client-id 1}", []string{strings.Repeat("a", 64)}},
		{"job log secret", "Authorization: Bearer abcdefghijklmnopqrstuvwxyz",
			"Authorization: Bearer [MASKED authorization]", []string{"abcdefghijklmnop"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			p := NewPrinterTo(NewRedactor(false), &out, &errOut)
			p.Say(tt.in)
			if got := out.String(); got != tt.want+"\n" {
				t.Errorf("Say(%q) = %q, want %q", tt.in, got, tt.want+"\n")
			}
			for _, a := range tt.absent {
				if strings.Contains(out.String(), a) {
					t.Errorf("output still carries %q: %q", a, out.String())
				}
			}
		})
	}
}

func TestKnownValuesAreStable(t *testing.T) {
	var out, errOut bytes.Buffer
	p := NewPrinterTo(NewRedactor(false), &out, &errOut)
	p.Redactor().Known(KindUser, "alice")
	p.Sayf("%s opened it; %s closed it", "alice", "Alice")
	p.Fail("failed for %s", "alice")
	if got, want := out.String(), "{user 1} opened it; {user 1} closed it\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if got, want := errOut.String(), "failed for {user 1}\n"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
}

func TestOffPrintsVerbatim(t *testing.T) {
	var out, errOut bytes.Buffer
	p := NewPrinterTo(NewRedactor(true), &out, &errOut)
	p.Say("alice@example.com\n\n")
	if got, want := out.String(), "alice@example.com\n"; got != want {
		t.Errorf("Say = %q, want %q", got, want)
	}
	p.Summary()
	if !strings.Contains(errOut.String(), "redaction off") {
		t.Errorf("Summary with redaction off = %q", errOut.String())
	}
}

func TestSummaryCountsAndWarns(t *testing.T) {
	var out, errOut bytes.Buffer
	p := NewPrinterTo(NewRedactor(false), &out, &errOut)
	p.Say("nothing here")
	p.Summary()
	if got := errOut.String(); !strings.Contains(got, "redacted: nothing") || !strings.Contains(got, "never redacted") {
		t.Errorf("empty Summary = %q", got)
	}
	errOut.Reset()
	p.Say("bob@example.com and Bearer abcdefghijklmnopqrstuvwxyz")
	p.Summary()
	if got := errOut.String(); !strings.Contains(got, "1 email") || !strings.Contains(got, "1 secret(s) masked") {
		t.Errorf("Summary = %q", got)
	}
}

func TestID(t *testing.T) {
	if got, want := ID("abcdef123456"), "abcdef…"; got != want {
		t.Errorf("ID = %q, want %q", got, want)
	}
}
