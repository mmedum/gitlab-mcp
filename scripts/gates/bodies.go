package main

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
)

// The bodies gate holds §4.2 (CLAUDE.md rule 5): GitLab runs every
// quick-action line in a description or a comment the API is sent, so
// every string a Write, Ship or Destructive tool takes either passes
// through internal/quickaction or is listed below as a plain field with
// the reason it cannot carry a command.
//
// It reads the schema dump. A tool is a write unless its annotations say
// readOnlyHint, so a tool with none is asked about. The guarded inputs
// are what the tool declares under _meta["gitlab-mcp/quickaction"],
// which internal/tools writes from the same declaration that routes the
// input through the guard; an input declared guarded that is not a
// string, or does not exist, fails, and so does a guard on a read tool.

// plainInputs are write-tool string inputs that are not Markdown bodies,
// keyed "tool.input", or "input" for every write tool that has it, each
// with the reason GitLab does not evaluate quick actions in it. Empty
// until phase 2 registers the first write: a row with nothing to excuse
// fails, so the list cannot be written ahead of the tools.
var plainInputs = map[string]string{}

// minWriteTools is the floor on write tools the gate examined. Phase 0
// registers none; phase 2 raises it with the first write tool, because
// from then on a dump with none is a dump of the wrong build.
const minWriteTools = 0

func bodies(out io.Writer, args []string) error {
	d, err := readDump(args[0])
	if err != nil {
		return err
	}
	report, problems := checkBodies(d, plainInputs, surfaceFloor, minWriteTools)
	if len(problems) > 0 {
		for _, p := range problems {
			_, _ = fmt.Fprintln(out, p)
		}
		return fmt.Errorf("%d body problem(s) in %s", len(problems), args[0])
	}
	_, _ = fmt.Fprintln(out, report)
	return nil
}

func checkBodies(d schemaDump, plain map[string]string, toolFloor, writeFloor int) (string, []string) {
	var problems []string
	if len(d.Tools) < toolFloor {
		problems = append(problems, fmt.Sprintf("the dump carries %d tools and the floor is %d; this gate would pass while reading nothing",
			len(d.Tools), toolFloor))
	}
	usedPlain := map[string]bool{}
	writes, strs, guardedCount, plainCount := 0, 0, 0, 0
	for _, t := range d.Tools {
		guarded, err := t.guardedInputs()
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if t.readOnly() {
			if len(guarded) > 0 {
				problems = append(problems, fmt.Sprintf("%s is read-only and declares %v guarded; a read sends no body, so the declaration is a mistake",
					t.Name, guarded))
			}
			continue
		}
		writes++
		props := map[string]*dumpSchema{}
		if t.InputSchema != nil {
			props = t.InputSchema.Properties
		}
		for _, g := range guarded {
			switch p, ok := props[g]; {
			case !ok:
				problems = append(problems, fmt.Sprintf("%s declares %q guarded and has no such input", t.Name, g))
			case !p.isString():
				problems = append(problems, fmt.Sprintf("%s.%s is declared guarded and is not a string", t.Name, g))
			}
		}
		for _, name := range slices.Sorted(maps.Keys(props)) {
			if !props[name].isString() {
				continue
			}
			strs++
			if slices.Contains(guarded, name) {
				guardedCount++
				continue
			}
			key := t.Name + "." + name
			switch {
			case plain[key] != "":
				usedPlain[key] = true
			case plain[name] != "":
				usedPlain[name] = true
			default:
				problems = append(problems, fmt.Sprintf("%s is a string input of a %s tool, neither routed through internal/quickaction "+
					"nor listed in plainInputs; a quick-action line in it would run (§4.2)", key, kindWord(t)))
				continue
			}
			plainCount++
		}
	}
	for _, k := range slices.Sorted(maps.Keys(plain)) {
		switch {
		case len(strings.TrimSpace(plain[k])) < minReasonLen:
			problems = append(problems, fmt.Sprintf("plainInputs[%q] gives the reason %q; say why GitLab cannot run a command from it", k, plain[k]))
		case !usedPlain[k]:
			problems = append(problems, fmt.Sprintf("plainInputs[%q] excuses no string input of any write tool; drop it", k))
		}
	}
	if writes < writeFloor {
		problems = append(problems, fmt.Sprintf("the dump carries %d write tools and the floor is %d", writes, writeFloor))
	}
	return fmt.Sprintf("bodies ok: %d tools, %d of them writes, with %d string inputs: %d through the quick-action guard, %d plain",
		len(d.Tools), writes, strs, guardedCount, plainCount), problems
}

// kindWord names what the annotations make a tool, for a message.
func kindWord(t dumpTool) string {
	if t.Annotations != nil && t.Annotations.DestructiveHint != nil && *t.Annotations.DestructiveHint {
		return "Ship or Destructive"
	}
	return "Write"
}
