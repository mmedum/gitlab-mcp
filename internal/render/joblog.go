package render

import (
	"bytes"
	"regexp"
	"strings"
)

// A job log as GitLab stores it is what a terminal received: ANSI
// escapes for color and cursor movement, carriage returns that overwrite
// a line, and the runner's section markers,
//
//	\x1b[0Ksection_start:1712345678:step_script[collapsed=true]\r\x1b[0KExecuting "step_script"
//	…
//	\x1b[0Ksection_end:1712345699:step_script\r\x1b[0K
//
// which GitLab's page turns into folds. A runner that timestamps its
// output (gitlab.com's do) starts each line with the time and a stream
// marker, "2026-09-26T22:30:07.819673Z 01O ", and a "+" after the marker
// continues the line before it. Log shows what the job page shows: the
// timestamps, escapes and markers gone, continued lines joined, each
// section's start as one line naming it, and a line overwritten by a
// carriage return as it was left.

// JobLogBytes is the default window of a job log, in bytes of the stored
// log, and MaxJobLogBytes the most one call reads (§4.8).
const (
	JobLogBytes    = 40000
	MaxJobLogBytes = 100000
)

// ansi matches CSI sequences (colors, erase line) and OSC sequences
// (titles, hyperlinks, whose target a reader never sees).
var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]`)

// sectionMarker matches a runner section marker up to the carriage
// return that ends it.
var sectionMarker = regexp.MustCompile(`section_(start|end):[0-9]+:([A-Za-z0-9_.\-]+)(?:\[[^\]\r\n]*\])?\r`)

// TimestampPrefix matches a runner's per-line timestamp and stream
// marker; group 1 is "+" on a line that continues the one before.
const TimestampPrefix = `[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?Z [0-9a-fA-F]{2}[OE](\+| ?)`

var timestamp = regexp.MustCompile(`(?m)^` + TimestampPrefix)

// untimestamp drops the runner's timestamps and joins continued lines.
func untimestamp(raw []byte) []byte {
	if !timestamp.Match(raw) {
		return raw
	}
	out := make([]byte, 0, len(raw))
	for line := range bytes.SplitAfterSeq(raw, []byte("\n")) {
		m := timestamp.FindSubmatchIndex(line)
		if m == nil {
			out = append(out, line...)
			continue
		}
		if m[3] > m[2] && line[m[2]] == '+' {
			out = bytes.TrimSuffix(out, []byte("\n"))
			out = bytes.TrimSuffix(out, []byte("\r"))
		}
		out = append(out, line[m[1]:]...)
	}
	return out
}

// endMark stands in for an end marker until lines are split, so a line
// that held only one can be told from a line that was empty. A NUL byte
// in the log itself is dropped with them; no reader sees one.
const endMark = 0

// LogSection is one section of a stored log, by byte offsets: Start is
// where its start marker begins, or -1 when it began before the stretch
// read; End is where its end marker ends, or -1 when the stretch has no
// end marker for it.
type LogSection struct {
	Name       string
	Start, End int
}

// LogSections finds the sections of a stretch of a stored log, in the
// order their markers appear. A section whose end marker is in the
// stretch and whose start is not began before it.
func LogSections(raw []byte) []LogSection {
	var out []LogSection
	open := map[string]int{}
	for _, m := range sectionMarker.FindAllSubmatchIndex(raw, -1) {
		kind, name := string(raw[m[2]:m[3]]), string(raw[m[4]:m[5]])
		if kind == "start" {
			open[name] = len(out)
			out = append(out, LogSection{Name: name, Start: m[0], End: -1})
			continue
		}
		if i, ok := open[name]; ok {
			out[i].End = m[1]
			delete(open, name)
			continue
		}
		out = append(out, LogSection{Name: name, Start: -1, End: m[1]})
	}
	return out
}

// Log turns a window of a stored log into the text GitLab's job page
// shows (see above). It does not mask secrets or make hidden characters
// visible; the caller does both, on what this returns.
func Log(raw []byte) string {
	text := ansi.ReplaceAll(untimestamp(raw), nil)
	text = sectionMarker.ReplaceAllFunc(text, func(m []byte) []byte {
		sub := sectionMarker.FindSubmatch(m)
		if string(sub[1]) == "end" {
			return []byte{endMark}
		}
		return []byte("§ section " + string(sub[2]) + ": ")
	})
	var b strings.Builder
	b.Grow(len(text))
	for line := range bytes.SplitAfterSeq(text, []byte("\n")) {
		body, nl := bytes.CutSuffix(line, []byte("\n"))
		body = bytes.TrimSuffix(body, []byte("\r"))
		// A carriage return overwrote what came before it on the line.
		if i := bytes.LastIndexByte(body, '\r'); i >= 0 {
			body = body[i+1:]
		}
		// A line that held only an end marker is dropped.
		marked := bytes.IndexByte(body, endMark) >= 0
		body = bytes.ReplaceAll(body, []byte{endMark}, nil)
		if marked && len(bytes.TrimSpace(body)) == 0 {
			continue
		}
		b.Write(body)
		if nl {
			b.WriteByte('\n')
		}
	}
	return strings.ToValidUTF8(b.String(), "�")
}
