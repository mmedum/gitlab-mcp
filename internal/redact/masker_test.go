package redact

import (
	"strings"
	"testing"
)

func TestMaskerStablePlaceholders(t *testing.T) {
	m := NewMasker()
	m.Known(KindHost, "gitlab.example.com")
	m.Known(KindUser, "alice")
	m.Known(KindPath, "example-group/app")

	in := "instance gitlab.example.com: signed in as alice (Alice@example.com); " +
		"GET https://gitlab.example.com/api/v4/projects/example-group%2Fapp failed; " +
		"project example-group/app, user alice, other bob"
	want := "instance {host 1}: signed in as {user 1} ({email 1}); " +
		"GET https://{host 1}/api/v4/projects/{path 1} failed; " +
		"project {path 1}, user {user 1}, other bob"
	if got := m.Text(in); got != want {
		t.Errorf("Text =\n%q\nwant\n%q", got, want)
	}
	// Across calls, the same value keeps its number.
	if got := m.Text("again ALICE and a second host gitlab2.example.com/x"); got != "again {user 1} and a second host gitlab2.example.com/x" {
		t.Errorf("second call = %q", got)
	}
	if got, want := m.Footer(), "redacted: 1 email, 1 host, 1 path, 1 user"; got != want {
		t.Errorf("Footer = %q, want %q", got, want)
	}
	if got := m.Count(); got != 4 {
		t.Errorf("Count = %d, want 4", got)
	}
}

func TestMaskerWholeWordsOnly(t *testing.T) {
	m := NewMasker()
	m.Known(KindUser, "al")
	tests := map[string]string{
		"al":                "{user 1}",
		"al.":               "{user 1}.",
		"hello al, bye":     "hello {user 1}, bye",
		"alignment":         "alignment",
		"normal":            "normal",
		"al.b":              "al.b",
		"al-b":              "al-b",
		"@al mentioned you": "@{user 1} mentioned you",
	}
	for in, want := range tests {
		if got := m.Text(in); got != want {
			t.Errorf("Text(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestMaskerLeavesItsOwnPlaceholdersAlone: a registered value that is
// also a placeholder's kind name must not rewrite the placeholder.
func TestMaskerLeavesItsOwnPlaceholdersAlone(t *testing.T) {
	m := NewMasker()
	m.Known(KindUser, "host")
	m.Known(KindHost, "gitlab.example.com")
	got := m.Text("gitlab.example.com host")
	if want := "{host 1} {user 1}"; got != want {
		t.Errorf("Text = %q, want %q", got, want)
	}
	if again := m.Text(got); again != got {
		t.Errorf("a second pass changed %q to %q", got, again)
	}
}

func TestMaskerLongestValueFirst(t *testing.T) {
	m := NewMasker()
	m.Known(KindPath, "example-group")
	m.Known(KindPath, "example-group/app")
	if got, want := m.Text("in example-group/app and example-group"), "in {path 1} and {path 2}"; got != want {
		t.Errorf("Text = %q, want %q", got, want)
	}
}

func TestMaskerShapes(t *testing.T) {
	m := NewMasker()
	in := "token " + fakePAT + " app " + fakeAppID + " url https://gitlab.com/example-group/app?x=1"
	got := m.Text(in)
	want := "token {token 1} app {client-id 1} url https://gitlab.com/{path 1}?{query 1}"
	if got != want {
		t.Errorf("Text =\n%q\nwant\n%q", got, want)
	}
}

func TestMaskerIgnoresEmptyAndPublic(t *testing.T) {
	m := NewMasker()
	m.Known(KindUser, "  ")
	m.Known(KindHost, "gitlab.com")
	if got := m.Text("gitlab.com is public"); got != "gitlab.com is public" {
		t.Errorf("Text = %q", got)
	}
	if got := m.Footer(); got != "redacted: nothing" {
		t.Errorf("Footer = %q", got)
	}
}

func TestMaskerUnicodeAroundAValue(t *testing.T) {
	m := NewMasker()
	m.Known(KindUser, "alice")
	// A rune whose lowercase has a different byte length must not shift
	// the match.
	got := m.Text("İİ alice İ")
	if strings.Contains(got, "alice") || !strings.HasPrefix(got, "İİ ") {
		t.Errorf("Text = %q", got)
	}
}
