package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestDepsCheck(t *testing.T) {
	now := time.Date(2026, time.September, 26, 0, 0, 0, 0, time.UTC)
	gomod := "module example.com/m\n\nrequire (\n" +
		"\texample.com/a v1.0.0\n\texample.com/b v1.0.0\n\texample.com/c v1.0.0\n\texample.com/d v1.0.0\n" +
		"\texample.com/held v1.0.0 // pinned: v2 drops the API this server uses\n" +
		"\texample.com/lazy v1.0.0 // pinned: x\n)\n"
	row := func(path, update, when string) string {
		if update == "" {
			return `{"Path":"` + path + `","Version":"v1.0.0"}`
		}
		return `{"Path":"` + path + `","Version":"v1.0.0","Update":{"Version":"` + update + `","Time":"` + when + `T00:00:00Z"}}`
	}
	base := []string{
		`{"Path":"example.com/m","Main":true}`,
		row("example.com/a", "", ""),
		row("example.com/b", "v1.1.0", "2026-08-01"),
		row("example.com/c", "", ""),
		row("example.com/d", "", ""),
		row("example.com/held", "v2.0.0", "2024-01-01"),
		`{"Path":"example.com/ind","Version":"v1.0.0","Indirect":true,"Update":{"Version":"v9.0.0","Time":"2020-01-01T00:00:00Z"}}`,
	}
	run := func(rows []string) (string, error) {
		var out bytes.Buffer
		err := depsCheck(&out, gomod, []byte(strings.Join(rows, "\n")), now)
		return out.String(), err
	}
	out, err := run(base)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	mustSay(t, out, "deps ok: 5 direct dependencies")
	mustSay(t, out, "pinned  example.com/held v1.0.0: v2 drops the API")

	cases := []struct {
		name string
		rows []string
		want string
	}{
		{"an update out for seven months", append(append([]string{}, base...), row("example.com/e", "v1.2.0", "2026-02-01")),
			"example.com/e: v1.0.0, and v1.2.0 has been out since 2026-02-01"},
		{"a pin without a reason", append(append([]string{}, base...), row("example.com/lazy", "v2.0.0", "2020-01-01")),
			`example.com/lazy is pinned with "x", which is not a reason`},
		{"too few direct dependencies", base[:3], "read 2 direct dependencies, want at least 5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := run(tc.rows)
			if err == nil {
				t.Fatalf("passed:\n%s", out)
			}
			mustSay(t, out, tc.want)
		})
	}
}
