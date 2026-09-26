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

func TestVersionCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"17.3.1", "17.3.1", 0},
		{"17.3.1", "17.3.2", -1},
		{"17.4.0", "17.3.9", 1},
		{"18.0.0", "17.11.0", 1},
		{"16.11.10", "16.2.0", 1},
		{"19.5.0-pre", "19.5.0", -1},
		{"19.5.0", "19.5.0-rc2", 1},
		{"19.5.0-pre", "19.4.9", 1},
		{"17.3.1-ee", "17.3.1", 0},
		{"17.3.1-ee", "17.3.1-pre", 1},
		{"17.3.1-rc1", "17.3.1-pre", 0},
	}
	for _, tt := range tests {
		a, _ := ParseVersion(tt.a)
		b, _ := ParseVersion(tt.b)
		if got := a.Compare(b); got != tt.want {
			t.Errorf("%s.Compare(%s) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestVersionAtLeast(t *testing.T) {
	tests := []struct {
		v            string
		major, minor int
		want         bool
	}{
		{"19.5.0-pre", 19, 5, true},
		{"19.5.0-pre", 19, 6, false},
		{"17.3.1-ee", 17, 3, true},
		{"17.3.1", 17, 4, false},
		{"18.0.0", 17, 11, true},
		{"16.11.10", 17, 0, false},
	}
	for _, tt := range tests {
		v, _ := ParseVersion(tt.v)
		if got := v.AtLeast(tt.major, tt.minor); got != tt.want {
			t.Errorf("%s.AtLeast(%d, %d) = %v, want %v", tt.v, tt.major, tt.minor, got, tt.want)
		}
	}
}

func TestMetadata(t *testing.T) {
	m, err := NewMetadata("17.3.1-ee", "abc1234", true)
	if err != nil {
		t.Fatal(err)
	}
	if m.Edition() != "Enterprise" || m.Revision != "abc1234" || !m.AtLeast(17, 3) || m.AtLeast(17, 4) {
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
