package main

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/mmedum/gitlab-mcp/v2/scripts/internal/gatekit"
	"github.com/mmedum/gitlab-mcp/v2/scripts/internal/tsv"
)

// The live-cover gate says how much of the tool surface the live driver
// actually sent to a real instance (§13, the standard's §1), per tool
// option.
//
// Two files. testdata/live-cover-record.tsv is written by
// scripts/livegitlab at the end of a run, from what went out through the
// stdio client — recorded where the calls are made, so a step that
// exists in the source and never ran is not counted. testdata/live-cover.tsv
// is written by a person: every option the run did not send, as
// undrivable (what blocks it) or undriven (what closing it takes), with
// a ceiling on undriven that only goes down.
//
// It fails on an option neither sent nor waived, a waiver for an option
// that was sent, a row about a tool or option the surface no longer has,
// and an empty record: a record of nothing passes every other check.

const (
	liveCoverFile  = "testdata/live-cover.tsv"
	liveRecordFile = "testdata/live-cover-record.tsv"
	// maxUndriven is the ceiling on undriven rows. Lower it when a run
	// closes some; never raise it to make a build pass.
	maxUndriven = 0
	// wholeTool is the option name a record or waiver uses for the tool
	// itself: called at all, whatever it was sent.
	wholeTool = "*"
)

func liveCover(out io.Writer, args []string) error {
	d, err := dumpBinary(args[0])
	if err != nil {
		return err
	}
	record, problems := readLiveRecord(liveRecordFile)
	waivers, more := readLiveWaivers(liveCoverFile)
	problems = append(problems, more...)
	if err := gatekit.Problems(out, "the live-coverage inputs", problems); err != nil {
		return err
	}
	report, problems := checkLiveCover(d, record, waivers, maxUndriven)
	if err := gatekit.Problems(out, "live coverage", problems); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, report)
	return nil
}

// liveWaiver is one option the driver does not send, and why.
type liveWaiver struct {
	verdict, reason string
	line            int
}

// readLiveRecord reads what the last run sent: "tool.option" rows, or
// "tool.*" for the call itself, each with how many times.
func readLiveRecord(path string) (map[string]int, []string) {
	rows, problems := tsv.Read(path, 2)
	out := map[string]int{}
	for _, r := range rows {
		n, err := strconv.Atoi(r.Fields[1])
		if err != nil || n < 1 || !strings.Contains(r.Fields[0], ".") {
			problems = append(problems, fmt.Sprintf("%s:%d: %q is not \"tool.option<TAB>count\"; the driver writes this file", path, r.Line,
				strings.Join(r.Fields, "\t")))
			continue
		}
		out[r.Fields[0]] = n
	}
	if len(out) == 0 && len(problems) == 0 {
		problems = append(problems, path+" records nothing; run `make live` against a scratch project (docs/architecture.md §9.1). "+
			"An empty record passes every other check there is")
	}
	return out, problems
}

func readLiveWaivers(path string) (map[string]liveWaiver, []string) {
	rows, problems := tsv.Read(path, 3)
	out := map[string]liveWaiver{}
	for _, r := range rows {
		key, verdict, reason := r.Fields[0], r.Fields[1], strings.TrimSpace(r.Fields[2])
		switch {
		case !strings.Contains(key, "."):
			problems = append(problems, fmt.Sprintf("%s:%d: %q is not tool.option", path, r.Line, key))
		case verdict != "undrivable" && verdict != "undriven":
			problems = append(problems, fmt.Sprintf("%s:%d: %s has verdict %q; it is undrivable (what blocks it) or undriven (what closing it takes)",
				path, r.Line, key, verdict))
		case len(reason) < minReasonLen:
			problems = append(problems, fmt.Sprintf("%s:%d: %s gives the reason %q, which is not a decision", path, r.Line, key, reason))
		default:
			out[key] = liveWaiver{verdict: verdict, reason: reason, line: r.Line}
		}
	}
	return out, problems
}

func checkLiveCover(d schemaDump, record map[string]int, waivers map[string]liveWaiver, ceiling int) (string, []string) {
	var problems []string
	if len(d.Tools) < surfaceFloor {
		problems = append(problems, fmt.Sprintf("the dump carries %d tools and the floor is %d", len(d.Tools), surfaceFloor))
	}
	surface := map[string][]string{}
	for _, t := range d.Tools {
		surface[t.Name] = append([]string{wholeTool}, t.options()...)
	}
	total, driven, excused := 0, 0, 0
	for _, tool := range slices.Sorted(maps.Keys(surface)) {
		for _, opt := range surface[tool] {
			key := tool + "." + opt
			total++
			sent := record[key] > 0
			w, waived := waivers[key]
			switch {
			case sent && waived:
				problems = append(problems, fmt.Sprintf("%s:%d: %s is waived as %s and the last run sent it; drop the row",
					liveCoverFile, w.line, key, w.verdict))
				driven++
			case sent:
				driven++
			case waived:
				excused++
			default:
				what := "an option no live step sent"
				if opt == wholeTool {
					what = "a tool the live run never called"
				}
				problems = append(problems, fmt.Sprintf("%s is %s, and %s does not say why: drive it, or waive it as undrivable "+
					"(what blocks it) or undriven (what closing it takes)", key, what, liveCoverFile))
			}
		}
	}
	known := func(key string) bool {
		tool, opt, _ := strings.Cut(key, ".")
		return slices.Contains(surface[tool], opt)
	}
	for _, k := range slices.Sorted(maps.Keys(record)) {
		if !known(k) {
			problems = append(problems, fmt.Sprintf("%s records %s, which the surface does not have; the record is from an older build, run `make live` again",
				liveRecordFile, k))
		}
	}
	undriven := 0
	for _, k := range slices.Sorted(maps.Keys(waivers)) {
		if !known(k) {
			problems = append(problems, fmt.Sprintf("%s:%d: %s names a tool or option the surface does not have; drop the row",
				liveCoverFile, waivers[k].line, k))
		}
		if waivers[k].verdict == "undriven" {
			undriven++
		}
	}
	if undriven > ceiling {
		problems = append(problems, fmt.Sprintf("%d options are waived as undriven and the ceiling is %d; an undriven row is work parked, "+
			"so the number may only go down", undriven, ceiling))
	}
	slices.Sort(problems)
	return fmt.Sprintf("live cover ok: %d of %d tools and options sent by the last run, %d waived (%d undriven, ceiling %d)",
		driven, total, excused, undriven, ceiling), problems
}
