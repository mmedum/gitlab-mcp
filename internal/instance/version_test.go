package instance

import (
	"errors"
	"testing"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in                  string
		major, minor, patch int
		suffix, str         string
	}{
		{"19.5.0-pre", 19, 5, 0, "pre", "19.5.0-pre"},
		{"17.3.1-ee", 17, 3, 1, "ee", "17.3.1-ee"},
		{"16.11.10", 16, 11, 10, "", "16.11.10"},
		{"v17.0.0", 17, 0, 0, "", "17.0.0"},
		{"17.3", 17, 3, 0, "", "17.3.0"},
		{" 18.2.0-rc1 ", 18, 2, 0, "rc1", "18.2.0-rc1"},
	}
	for _, tt := range tests {
		v, err := ParseVersion(tt.in)
		if err != nil {
			t.Fatalf("ParseVersion(%q): %v", tt.in, err)
		}
		if v.Major != tt.major || v.Minor != tt.minor || v.Patch != tt.patch || v.Suffix != tt.suffix {
			t.Errorf("ParseVersion(%q) = %+v", tt.in, v)
		}
		if v.String() != tt.str {
			t.Errorf("ParseVersion(%q).String() = %q, want %q", tt.in, v.String(), tt.str)
		}
	}
	for _, bad := range []string{"", "garbage", "17", "17.x.1", "17.3.1.4", "17.3.1-", "+17.3.1", "17..1", "-17.3"} {
		if _, err := ParseVersion(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseVersion(%q) error = %v, want ErrInvalid", bad, err)
		}
	}
}

func TestMetadata(t *testing.T) {
	m, err := NewMetadata("17.3.1-ee", "abc1234", true)
	if err != nil {
		t.Fatal(err)
	}
	if m.Edition() != "Enterprise" || m.Revision != "abc1234" || m.Version.String() != "17.3.1-ee" {
		t.Errorf("NewMetadata = %+v, edition %q", m, m.Edition())
	}
	m, err = NewMetadata("16.11.10", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if m.Edition() != "Community" {
		t.Errorf("Edition = %q, want Community", m.Edition())
	}
	if _, err := NewMetadata("unknown", "", false); !errors.Is(err, ErrInvalid) {
		t.Errorf("NewMetadata(unknown) error = %v, want ErrInvalid", err)
	}
}
