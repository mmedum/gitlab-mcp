package main

import (
	"bytes"
	"strings"
	"testing"
)

// transcriptTree is a minimal repository that passes: two drivers
// printing through the printer, and the printer redacting through one
// write.
func transcriptTree(overrides map[string]string) map[string]string {
	files := map[string]string{
		"scripts/drv/main.go": `//go:build live

package main

import "example.invalid/scripts/internal/redact"

func main() { redact.NewPrinter(nil).Say("hello") }
`,
		"scripts/evl/main.go": `//go:build evals

package main

import "example.invalid/scripts/internal/redact"

func main() { redact.NewPrinter(nil).Say("hello") }
`,
		"scripts/gates/main.go": "package main\n\nimport _ \"example.invalid/scripts/internal/mcpstdio\"\n",
		"scripts/internal/redact/redact.go": `package redact

import (
	"fmt"
	"io"
	"os"
)

type Redactor struct{}

func (*Redactor) Do(s string) string { return s }

type Printer struct{ out, err io.Writer; red *Redactor }

func NewPrinter(r *Redactor) *Printer { return &Printer{out: os.Stdout, err: os.Stderr, red: r} }

func (p *Printer) Say(s string) { p.write(p.out, s) }

func (p *Printer) write(w io.Writer, s string) { _, _ = fmt.Fprintln(w, p.red.Do(s)) }
`,
	}
	for k, v := range overrides {
		if v == "" {
			delete(files, k)
			continue
		}
		files[k] = v
	}
	return files
}

var transcriptTestPackages = []string{"scripts/drv", "scripts/evl"}

func runTranscript(t *testing.T, files map[string]string, packages []string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := transcriptCheck(&out, repoTree(t, files), packages, "scripts/internal/redact")
	return out.String(), err
}

func TestTranscriptPassesWhenEveryPathRedacts(t *testing.T) {
	files := transcriptTree(map[string]string{"scripts/drv/more.go": "//go:build live\n\npackage main\n"})
	out, err := runTranscript(t, files, transcriptTestPackages)
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if !strings.Contains(out, "transcript ok (3 files in 2 packages; 2 terminal mentions") {
		t.Errorf("the gate does not say how much it read: %s", out)
	}
}

// The floor is one file per package plus one; the clean fixture without
// the extra file fails only on the floor, which proves the floor bites.
func TestTranscriptFloorOnFiles(t *testing.T) {
	_, err := runTranscript(t, transcriptTree(nil), transcriptTestPackages)
	if err == nil || !strings.Contains(err.Error(), "want at least 3") {
		t.Fatalf("err = %v, want the file floor", err)
	}
}

func TestTranscriptRefuses(t *testing.T) {
	extra := map[string]string{"scripts/drv/more.go": "//go:build live\n\npackage main\n"}
	for _, tc := range []struct {
		name     string
		override map[string]string
		packages []string
		want     string
	}{
		{"a Println", map[string]string{"scripts/drv/bad.go": "//go:build live\n\npackage main\n\nimport \"fmt\"\n\nfunc f() { fmt.Println(1) }\n"},
			nil, "bad.go:7: fmt.Println reaches the terminal"},
		{"os.Stderr handed on", map[string]string{"scripts/evl/bad.go": "package main\n\nimport (\"fmt\"; \"os\")\n\nfunc f() { fmt.Fprintln(os.Stderr, 1) }\n"},
			nil, "os.Stderr reaches the terminal"},
		{"log", map[string]string{"scripts/drv/bad.go": "package main\n\nimport \"log\"\n\nfunc f() { log.Print(1) }\n"},
			nil, "the log package"},
		{"slog", map[string]string{"scripts/drv/bad.go": "package main\n\nimport \"log/slog\"\n\nfunc f() { slog.Info(\"x\") }\n"},
			nil, "the log/slog package"},
		{"the builtin", map[string]string{"scripts/drv/bad.go": "package main\n\nfunc f() { println(1) }\n"},
			nil, "the builtin println"},
		{"an unlisted driver", map[string]string{"scripts/other/main.go": "package main\n\nimport _ \"example.invalid/scripts/internal/mcpstdio\"\n"},
			nil, "scripts/other imports"},
		{"a listed package that is missing", nil,
			append([]string{"scripts/gone"}, transcriptTestPackages...), "scripts/gone is listed and cannot be read"},
		{"a printer that does not redact", map[string]string{"scripts/internal/redact/redact.go": `package redact

import ("fmt"; "io"; "os")

type Printer struct{ out io.Writer }

func NewPrinter(any) *Printer { return &Printer{out: os.Stdout} }

func (p *Printer) Say(s string) { _, _ = fmt.Fprintln(p.out, s) }
`}, nil, "1 write(s), 0 of them redacting"},
		{"a second write site", map[string]string{"scripts/internal/redact/extra.go": `package redact

import ("fmt"; "io")

func (p *Printer) Raw(w io.Writer, s string) { _, _ = fmt.Fprint(w, p.red.Do(s)) }
`}, nil, "2 write(s), 2 of them redacting"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			packages := tc.packages
			if packages == nil {
				packages = transcriptTestPackages
			}
			override := map[string]string{}
			for k, v := range extra {
				override[k] = v
			}
			for k, v := range tc.override {
				override[k] = v
			}
			out, err := runTranscript(t, transcriptTree(override), packages)
			if err == nil {
				t.Fatalf("passed:\n%s", out)
			}
			if !strings.Contains(out, tc.want) && !strings.Contains(err.Error(), tc.want) {
				t.Errorf("output does not say %q:\n%s\n%v", tc.want, out, err)
			}
		})
	}
}

// The real list names both drivers and the client they share.
func TestTranscriptCoversTheDrivers(t *testing.T) {
	for _, want := range []string{"scripts/livegitlab", "scripts/evals", "scripts/internal/mcpstdio"} {
		found := false
		for _, p := range transcriptPackages {
			found = found || p == want
		}
		if !found {
			t.Errorf("transcriptPackages does not list %s", want)
		}
	}
	if transcriptExempt != "scripts/internal/redact" {
		t.Errorf("the exempt package is %s", transcriptExempt)
	}
}
