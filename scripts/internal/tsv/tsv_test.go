package tsv

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		cols     int
		rows     [][]string
		problems []string
	}{
		{"rows, comments and blanks", "# header\n\na\tone\r\nb\ttwo  \n", 2,
			[][]string{{"a", "one"}, {"b", "two"}}, nil},
		{"trailing tab is an empty last field", "a\t\n", 2,
			[][]string{{"a", ""}}, nil},
		{"trailing tab counts as a column", "a\tb\t\n", 2,
			nil, []string{"f:1: want 2 tab-separated columns, got 3"}},
		{"too few columns", "a\n", 2,
			nil, []string{"f:1: want 2 tab-separated columns, got 1"}},
		{"duplicate key", "a\tone\n# x\na\ttwo\n", 2,
			[][]string{{"a", "one"}, {"a", "two"}}, []string{"f:3: a is listed twice (first on line 1)"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, problems := Parse(strings.NewReader(tt.in), "f", tt.cols)
			var got [][]string
			for _, r := range rows {
				got = append(got, r.Fields)
			}
			if !reflect.DeepEqual(got, tt.rows) {
				t.Errorf("rows = %q, want %q", got, tt.rows)
			}
			if !reflect.DeepEqual(problems, tt.problems) {
				t.Errorf("problems = %q, want %q", problems, tt.problems)
			}
		})
	}
}

func TestReadKeepsLineNumbers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.tsv")
	if err := os.WriteFile(path, []byte("# c\n\nk\tv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rows, problems := Read(path, 2)
	if len(problems) != 0 || len(rows) != 1 || rows[0].Line != 3 {
		t.Fatalf("Read = %+v, %q; want one row on line 3", rows, problems)
	}
}

func TestReadMissingFile(t *testing.T) {
	_, problems := Read(filepath.Join(t.TempDir(), "absent.tsv"), 2)
	if len(problems) != 1 || !strings.Contains(problems[0], "cannot read") {
		t.Fatalf("problems = %q", problems)
	}
}
