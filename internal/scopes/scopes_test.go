package scopes

import (
	"slices"
	"testing"
)

func TestForMode(t *testing.T) {
	tests := []struct {
		readOnly bool
		mode     Mode
		want     []string
	}{
		{true, ModeReadOnly, []string{"read_api"}},
		{false, ModeDefault, []string{"api"}},
	}
	for _, tt := range tests {
		m := ModeFor(tt.readOnly)
		if m != tt.mode {
			t.Errorf("ModeFor(%v) = %q, want %q", tt.readOnly, m, tt.mode)
		}
		if got := ForMode(m); !slices.Equal(got, tt.want) {
			t.Errorf("ForMode(%q) = %v, want %v", m, got, tt.want)
		}
	}
}

func TestRequired(t *testing.T) {
	tests := map[Kind]string{
		KindRead:        "read_api",
		KindWrite:       "api",
		KindShip:        "api",
		KindDestructive: "api",
		Kind("unknown"): "api",
	}
	for k, want := range tests {
		if got := Required(k); got != want {
			t.Errorf("Required(%q) = %q, want %q", k, got, want)
		}
	}
}

func TestSatisfied(t *testing.T) {
	tests := []struct {
		name    string
		granted []string
		needed  string
		want    bool
	}{
		{"api covers read_api", []string{"api"}, "read_api", true},
		{"api covers api", []string{"api"}, "api", true},
		{"read_api covers read_api", []string{"read_api"}, "read_api", true},
		{"read_api does not cover api", []string{"read_api"}, "api", false},
		{"nothing granted", nil, "read_api", false},
		{"unrelated scopes", []string{"read_user", "openid"}, "read_api", false},
		{"one of several", []string{"read_user", "api"}, "read_api", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Satisfied(tt.granted, tt.needed); got != tt.want {
				t.Errorf("Satisfied(%v, %q) = %v, want %v", tt.granted, tt.needed, got, tt.want)
			}
		})
	}
}

// TestEveryKindUnderEveryMode is the §9.4 table: a read_api token gets
// Read and nothing else, an api token gets every kind.
func TestEveryKindUnderEveryMode(t *testing.T) {
	want := map[Mode][]Kind{
		ModeReadOnly: {KindRead},
		ModeDefault:  {KindRead, KindWrite, KindShip, KindDestructive},
	}
	for mode, kinds := range want {
		var got []Kind
		for _, k := range Kinds() {
			if Satisfied(ForMode(mode), Required(k)) {
				got = append(got, k)
			}
		}
		if !slices.Equal(got, kinds) {
			t.Errorf("mode %q covers %v, want %v", mode, got, kinds)
		}
	}
}

func TestMissing(t *testing.T) {
	tests := []struct {
		granted, needed, want []string
	}{
		{[]string{"read_api"}, []string{"api"}, []string{"api"}},
		{[]string{"api"}, []string{"read_api", "api"}, nil},
		{nil, []string{"read_api", "api"}, []string{"read_api", "api"}},
		{[]string{"read_api"}, []string{"api", "read_api"}, []string{"api"}},
	}
	for _, tt := range tests {
		if got := Missing(tt.granted, tt.needed); !slices.Equal(got, tt.want) {
			t.Errorf("Missing(%v, %v) = %v, want %v", tt.granted, tt.needed, got, tt.want)
		}
	}
}

func TestParse(t *testing.T) {
	if got, want := Parse("  api   read_user\tread_api "), []string{"api", "read_user", "read_api"}; !slices.Equal(got, want) {
		t.Errorf("Parse = %v, want %v", got, want)
	}
	if got := Parse(""); len(got) != 0 {
		t.Errorf("Parse(\"\") = %v, want empty", got)
	}
}

func TestModesTable(t *testing.T) {
	rows := Modes()
	if len(rows) != 2 {
		t.Fatalf("Modes has %d rows, want 2", len(rows))
	}
	for _, r := range rows {
		if !slices.Equal(r.Scopes, ForMode(r.Mode)) {
			t.Errorf("row %q says %v, login asks for %v", r.Mode, r.Scopes, ForMode(r.Mode))
		}
	}
	if rows[1].Mode != ModeReadOnly || !slices.Equal(rows[1].Flags, []string{"GITLAB_MCP_READ_ONLY=true"}) {
		t.Errorf("read-only row = %+v", rows[1])
	}
}

func TestSetupBlock(t *testing.T) {
	tests := map[Mode]string{
		ModeDefault: "Name:          gitlab-mcp\n" +
			"Redirect URI:  http://127.0.0.1/callback\n" +
			"Confidential:  unchecked\n" +
			"Scopes:        api\n",
		ModeReadOnly: "Name:          gitlab-mcp\n" +
			"Redirect URI:  http://127.0.0.1/callback\n" +
			"Confidential:  unchecked\n" +
			"Scopes:        read_api\n",
	}
	for m, want := range tests {
		if got := SetupBlock(m); got != want {
			t.Errorf("SetupBlock(%q) =\n%s\nwant\n%s", m, got, want)
		}
	}
}

func TestRedirectURIHasNoPort(t *testing.T) {
	if RedirectURI != "http://127.0.0.1/callback" {
		t.Errorf("RedirectURI = %q", RedirectURI)
	}
}
