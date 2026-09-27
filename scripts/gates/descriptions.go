package main

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/mmedum/gitlab-mcp/scripts/internal/gatekit"
)

// The descriptions gate reads the schema dump — what a client is served,
// not the Go registry, which a test can walk while it is empty — and
// holds the rules a description can fail mechanically:
//
//   - every tool, every input and every resource template is described;
//   - at most one IMPORTANT: per description, kept for the trap that
//     returns a wrong answer rather than an error;
//   - a witness input says it is NOT a retry signal. A refusal names the
//     witness it wants, and a model that reads the refusal and re-sends
//     with the value it was just shown has routed around the guard; the
//     input's own description is where that is headed off (§4.6).

// witnessInputs are the inputs §4.6 makes a write carry: the file's
// last_commit_id, the head sha a merge, approval or branch delete was
// decided on, the updated_at an issue, merge request or comment was read
// at, and the hash of a wiki page's content. On a read tool the same
// names are addresses, not witnesses.
var witnessInputs = []string{"content_sha256", "last_commit_id", "sha", "updated_at"}

const retrySentence = "NOT a retry signal"

func descriptions(out io.Writer, args []string) error {
	d, err := readDump(args[0])
	if err != nil {
		return err
	}
	problems := checkDescriptions(d, surfaceFloor)
	if err := gatekit.Problems(out, "the descriptions in "+args[0], problems); err != nil {
		return err
	}
	inputs := 0
	for _, t := range d.Tools {
		inputs += len(t.options())
	}
	_, _ = fmt.Fprintf(out, "descriptions ok: %d tools, %d inputs and %d resource templates described; witnesses warned\n",
		len(d.Tools), inputs, len(d.ResourceTemplates))
	return nil
}

func checkDescriptions(d schemaDump, floor int) []string {
	var problems []string
	if len(d.Tools) < floor {
		problems = append(problems, fmt.Sprintf("the dump carries %d tools and the floor is %d; this gate would pass while reading nothing",
			len(d.Tools), floor))
	}
	for _, t := range d.Tools {
		problems = append(problems, describedOnce(t.Name, t.Description)...)
		for _, name := range t.options() {
			p := t.InputSchema.Properties[name]
			where := t.Name + "." + name
			problems = append(problems, describedOnce(where, p.Description)...)
			if !t.readOnly() && slices.Contains(witnessInputs, name) && !strings.Contains(p.Description, retrySentence) {
				problems = append(problems, fmt.Sprintf("%s is a witness and its description does not say a refusal is %s", where, retrySentence))
			}
		}
	}
	for _, r := range d.ResourceTemplates {
		problems = append(problems, describedOnce("resource template "+r.URITemplate, r.Description)...)
	}
	return problems
}

func describedOnce(where, text string) []string {
	var problems []string
	if strings.TrimSpace(text) == "" {
		problems = append(problems, where+" has no description")
	}
	if n := strings.Count(text, "IMPORTANT:"); n > 1 {
		problems = append(problems, fmt.Sprintf("%s says IMPORTANT: %d times; one is the most there can be", where, n))
	}
	return problems
}
