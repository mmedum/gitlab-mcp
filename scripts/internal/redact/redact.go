// Package redact is how the programs under scripts/ that drive a real
// instance print: through one Printer, which masks what a run can carry.
//
// `gates transcript` forbids fmt.Print*, os.Stdout, os.Stderr, log and
// log/slog in every driver, which leaves Printer.write as the one place a
// line reaches a terminal. So a print that bypasses the masking is not
// something to remember; the gate refuses it.
//
// The masking is the server's own (internal/redact): tokens, URLs with
// their hosts and namespace paths, addresses and application ids, with
// stable placeholders so a transcript still reads as a story — {host 1}
// in one result is {host 1} in the next. Secrets a job log can carry
// are masked on top. Values with no shape — the username the run signed
// in as, the scratch project's path — are registered with Known.
//
// What it cannot hide: titles, bodies, comments and file contents. The
// live driver reads only the scratch project it created
// (docs/architecture.md §9.1), which is what keeps them harmless, and
// Summary says so on every run.
package redact

import (
	"fmt"
	"io"
	"os"
	"strings"

	core "github.com/mmedum/gitlab-mcp/internal/redact"
)

// Kinds a driver registers with Known. They are the server's, so a
// transcript and a doctor report name things the same way.
const (
	KindHost = core.KindHost
	KindPath = core.KindPath
	KindUser = core.KindUser
	// KindID is a numeric id the run learned from the instance: the
	// scratch project's, a user's.
	KindID = "id"
)

// A Redactor replaces instance-specific values with stable placeholders.
type Redactor struct {
	// Off returns text unchanged. Only for a terminal nobody else sees.
	Off bool

	masker  *core.Masker
	secrets int
}

// NewRedactor returns a redactor with nothing registered.
func NewRedactor(off bool) *Redactor {
	return &Redactor{Off: off, masker: core.NewMasker()}
}

// Known registers a value to mask wherever it appears as a whole word.
func (r *Redactor) Known(kind, value string) { r.masker.Known(kind, value) }

// Do masks text. Secrets first, so a token inside a URL is one mask
// rather than half of one.
func (r *Redactor) Do(text string) string {
	if r.Off {
		return text
	}
	text, n := core.MaskSecrets(text)
	r.secrets += n
	return r.masker.Text(text)
}

// Summary says what was hidden, and what cannot be.
func (r *Redactor) Summary() string {
	const caveat = "Titles, bodies, comments and file contents are never redacted: " +
		"read the transcript before sharing it."
	if r.Off {
		return "redaction off: this transcript carries real hosts, paths and names"
	}
	footer := r.masker.Footer()
	if r.secrets > 0 {
		footer += fmt.Sprintf("; %d secret(s) masked", r.secrets)
	}
	return footer + ".\n" + caveat
}

// ID is the server's own truncation, so a report and a log line show the
// same key for the same id.
func ID(id string) string { return core.ID(id) }

// A Printer is the only way a driver writes anything.
type Printer struct {
	red *Redactor
	out io.Writer
	err io.Writer
}

// NewPrinter writes the run to the terminal.
func NewPrinter(red *Redactor) *Printer {
	return &Printer{red: red, out: os.Stdout, err: os.Stderr}
}

// NewPrinterTo writes somewhere else, which is how a test reads what a
// run would have printed.
func NewPrinterTo(red *Redactor, out, err io.Writer) *Printer {
	return &Printer{red: red, out: out, err: err}
}

// Redactor is the printer's redactor, for registering Known values.
func (p *Printer) Redactor() *Redactor { return p.red }

// Say prints one line.
func (p *Printer) Say(text string) { p.write(p.out, text) }

// Sayf prints one formatted line. The whole formatted line is masked,
// not only the arguments somebody remembered to wrap.
func (p *Printer) Sayf(format string, args ...any) { p.write(p.out, fmt.Sprintf(format, args...)) }

// Fail prints the run's own failure, to stderr.
func (p *Printer) Fail(format string, args ...any) { p.write(p.err, fmt.Sprintf(format, args...)) }

// Summary prints what was hidden and what could not be, to stderr.
func (p *Printer) Summary() { p.write(p.err, p.red.Summary()) }

// write is the one place a driver reaches a terminal. Every line ends in
// exactly one newline.
func (p *Printer) write(w io.Writer, text string) {
	_, _ = fmt.Fprintln(w, p.red.Do(strings.TrimRight(text, "\n")))
}
