package main

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/mmedum/gitlab-mcp/v2/scripts/internal/gatekit"
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
// with the reason GitLab does not evaluate quick actions in it. A row
// with nothing to excuse fails, so the list cannot outlive the tools.
var plainInputs = map[string]string{
	"project":        "addresses the project in the request path; never sent as text",
	"discussion_id":  "addresses a thread in the request path; never sent as text",
	"updated_at":     "the witness, compared with a fresh read and never sent",
	"type":           "a closed value that picks the route, issue or merge_request; never sent as text",
	"state":          "a closed value sent as state_event, close or reopen, which GitLab does not parse as Markdown",
	"reviewer_state": "a closed value, reviewed, requested_changes or approved, which GitLab does not parse as Markdown",
	"side":           "a closed value, new or old, used to compute the diff position; never sent as text",
	"file":           "a path the server looks up in the diff and sends as the position's old_path and new_path, which GitLab stores as a path, not Markdown",
	"title": "GitLab interprets quick actions in the description alone (merge_quick_actions_into_params! in " +
		"app/services/issuable_base_service.rb at 829b21d2); a title is stored as it is",
	"labels":           "label names, each checked to exist and sent as the labels array, never as Markdown",
	"add_labels":       "label names, each checked to exist and sent as the add_labels array, never as Markdown",
	"remove_labels":    "label names sent as the remove_labels array, never as Markdown",
	"assignees":        "usernames, each resolved to a user id before anything is sent",
	"add_assignees":    "usernames, each resolved to a user id before anything is sent",
	"remove_assignees": "usernames, each resolved to a user id before anything is sent",
	"reviewers":        "usernames, each resolved to a user id before anything is sent",
	"add_reviewers":    "usernames, each resolved to a user id before anything is sent",
	"remove_reviewers": "usernames, each resolved to a user id before anything is sent",
	"milestone":        "a milestone title, resolved to its id before anything is sent",
	"due_date":         "a day, checked as YYYY-MM-DD and sent as a date",
	"branch":           "a Git branch name, which GitLab takes as a ref, not Markdown",
	"ref":              "a Git branch, tag or SHA, which GitLab takes as a ref, not Markdown",
	"source_branch":    "a Git branch name, which GitLab takes as a ref, not Markdown",
	"target_branch":    "a Git branch name, which GitLab takes as a ref, not Markdown",
	"start_branch":     "a Git branch name, which GitLab takes as a ref, not Markdown",
	"create_commit.message": "a commit message: the Commits API writes it into Git and runs no quick action from it; " +
		"only issue and merge request descriptions and notes are interpreted",
	"sha":            "a commit SHA, compared with the head GitLab reports and sent as the witness; GitLab takes it as a ref, not Markdown",
	"content_sha256": "the wiki witness, compared with a hash of a fresh read and never sent",
	"slug":           "addresses a wiki page in the request path; never sent as text",
	"format":         "a closed value, markdown, rdoc, asciidoc or org, which GitLab takes as the page's markup and does not interpret",
	"merge_merge_request.merge_commit_message": "a commit message MergeRequests::MergeService writes into Git; it runs no quick " +
		"action (§18 row 67)",
	"merge_merge_request.squash_commit_message": "a commit message MergeRequests::MergeService writes into Git; it runs no quick " +
		"action (§18 row 67)",
	"save_wiki_page.content":     "a wiki page: WikiPages::CreateService and UpdateService run no quick action (§18 row 67)",
	"create_snippet.description": "a snippet's description: Snippets::CreateService runs no quick action (§18 row 67)",
	"update_snippet.description": "a snippet's description: Snippets::UpdateService runs no quick action (§18 row 103)",
	"update_snippet.content": "a snippet file's content, committed to the snippet's repository; Snippets::UpdateService runs no " +
		"quick action (§18 row 103)",
	"create_release.description": "release notes: Releases::CreateService runs no quick action (§18 row 67)",
	"create_release.name":        "a release's name: Releases::CreateService runs no quick action (§18 row 67)",
	"create_release.tag_message": "an annotated tag's message, written into Git; Releases::CreateService runs no quick action (§18 row 67)",
	"create_release.milestones":  "milestone titles GitLab looks up by title, never as Markdown",
	"create_release.released_at": "a time, checked as RFC 3339 and sent as one",
	"to_project":                 "a project the server resolves to its id and sends as to_project_id; never sent as text",
	"target_project":             "a project the server resolves to its id and sends as target_project_id; never sent as text",
	"link_type":                  "a closed value, relates_to, blocks or is_blocked_by, which GitLab takes as the link's type",
	"version":                    "the label witness, compared with a hash of a fresh read and never sent",
	"commit":                     "a commit SHA in the request path, which GitLab takes as a ref, not Markdown",
	"start_date":                 "a day, checked as YYYY-MM-DD and sent as a date",
	"color":                      "a color, #RRGGBB or a CSS name, which GitLab validates as one",
	"create_label.name":          "a label's name: Labels::CreateService runs no quick action (app/services/labels/create_service.rb at v19.4.1-ee)",
	"update_label.name":          "a label's new name: Labels::UpdateService runs no quick action (app/services/labels/update_service.rb at v19.4.1-ee)",
	"create_label.description":   "a label's description: Labels::CreateService runs no quick action (app/services/labels/create_service.rb at v19.4.1-ee)",
	"update_label.description":   "a label's description: Labels::UpdateService runs no quick action (app/services/labels/update_service.rb at v19.4.1-ee)",
	"create_milestone.description": "a milestone's description: Milestones::CreateService runs no quick action " +
		"(app/services/milestones/create_service.rb at v19.4.1-ee)",
	"update_milestone.description": "a milestone's description: Milestones::UpdateService runs no quick action " +
		"(app/services/milestones/update_service.rb at v19.4.1-ee)",
	"tag_name":                   "a Git tag name, which GitLab takes as a ref, not Markdown",
	"create_tag.message":         "an annotated tag's message, written into Git; Tags::CreateService runs no quick action (app/services/tags/create_service.rb at v19.4.1-ee)",
	"cherry_pick_commit.message": "a commit message the Commits API writes into Git; it runs no quick action, as for create_commit",
	"track_time.estimate": "a duration, checked against the server's own grammar and sent as duration, which " +
		"Gitlab::TimeTrackingFormatter parses to seconds; no description is sent, so the update service runs no quick action " +
		"(lib/api/time_tracking_endpoints.rb at v19.4.1-ee)",
	"track_time.add_spent": "a duration, checked against the server's own grammar and sent as duration, which " +
		"Gitlab::TimeTrackingFormatter parses to seconds; no description is sent, so the update service runs no quick action " +
		"(lib/api/time_tracking_endpoints.rb at v19.4.1-ee)",
}

// minWriteTools is the floor on write tools the gate examined: the
// twenty-six of phase 3 with every flag and toolset on. A dump with fewer
// is a dump of the wrong build.
const minWriteTools = 26

func bodies(out io.Writer, args []string) error {
	d, err := readDump(args[0])
	if err != nil {
		return err
	}
	report, problems := checkBodies(d, plainInputs, surfaceFloor, minWriteTools)
	if err := gatekit.Problems(out, "the bodies in "+args[0], problems); err != nil {
		return err
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
