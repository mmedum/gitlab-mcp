package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// Every entry decides where it runs; the zero value is refused, so a
// command added without a mode cannot read as manual and leave parity
// green.
func TestEveryCommandDeclaresAMode(t *testing.T) {
	if len(commands) == 0 {
		t.Fatal("the registry is empty")
	}
	for name, c := range commands {
		if c.mode == modeUnset {
			t.Errorf("%s declares no mode", name)
		}
		if c.mode != modeCheck && strings.TrimSpace(c.reason) == "" {
			t.Errorf("%s is not in check and gives no reason", name)
		}
		if c.run == nil {
			t.Errorf("%s has nothing to run", name)
		}
		if c.maxArgs >= 0 && c.minArgs > c.maxArgs {
			t.Errorf("%s takes at least %d and at most %d arguments", name, c.minArgs, c.maxArgs)
		}
		if strings.TrimSpace(c.doc) == "" {
			t.Errorf("%s has no doc", name)
		}
	}
}

// The repository half of the registry, by name and mode. Stated here
// rather than read from repoCommands, so moving a gate out of check is a
// change to this list too.
func TestRepoCommandModes(t *testing.T) {
	want := map[string]mode{
		"parity": modeCheck, "checklist": modeCheck, "pins": modeCheck, "coverage": modeCheck,
		"staleness": modeCheck, "changelog-links": modeCheck, "leaks": modeCheck, "transcript": modeCheck,
		"mcpb": modeCheck, "release": modeCheck, "server-json": modeCheck,
		"changelog": modePR, "merge-base": modePR,
		"release-notes": modeRelease, "mcpb-pack": modeRelease,
		"precommit": modeManual, "leaks-history": modeManual, "schema-refetch": modeManual, "deps": modeManual,
	}
	got := repoCommands()
	if len(got) != len(want) {
		t.Errorf("repoCommands has %d entries, want %d", len(got), len(want))
	}
	for name, m := range want {
		c, ok := got[name]
		if !ok {
			t.Errorf("%s is not registered", name)
			continue
		}
		if c.mode != m {
			t.Errorf("%s has mode %d, want %d", name, c.mode, m)
		}
	}
}

func TestRunDispatch(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code != 0 || !strings.Contains(out.String(), "parity") {
		t.Errorf("no arguments: code %d, usage %q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"no-such-gate"}, &out, &errOut); code != 2 {
		t.Errorf("unknown command: code %d, want 2", code)
	}
	errOut.Reset()
	if code := run([]string{"merge-base", "only-one"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "BASE HEAD") {
		t.Errorf("too few arguments: code %d, stderr %q", code, errOut.String())
	}
	saved := commands["parity"]
	defer func() { commands["parity"] = saved }()
	c := saved
	c.run = func(io.Writer, []string) error { return io.ErrUnexpectedEOF }
	commands["parity"] = c
	errOut.Reset()
	if code := run([]string{"parity"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "gates parity:") {
		t.Errorf("failing gate: code %d, stderr %q", code, errOut.String())
	}
}
