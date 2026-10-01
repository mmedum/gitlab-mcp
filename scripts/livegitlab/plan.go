package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/scripts/internal/gatekit"
	"github.com/mmedum/gitlab-mcp/v2/scripts/internal/redact"
)

// The pure half of the live driver: the run's name, the plan of calls,
// the rule that confines every call to the scratch project, and the
// record of what the run sent. None of it touches a network, so it is
// built and tested without the live tag.

// namePrefix starts every scratch project's name, so one left behind by
// -keep or a killed run is recognizable as this driver's.
const namePrefix = "gitlab-mcp-live-"

// runName is the scratch project's name: the prefix, the date and six
// random hex digits. The digits double as the run's search word, which
// is what keeps an instance-wide search on the run's own items.
func runName(now time.Time, rnd io.Reader) (string, error) {
	b := make([]byte, 3)
	if _, err := io.ReadFull(rnd, b); err != nil {
		return "", err
	}
	return namePrefix + now.UTC().Format("20060102") + "-" + hex.EncodeToString(b), nil
}

func newRunName() (string, error) { return runName(time.Now(), rand.Reader) }

// scratch is everything the run created, which is everything it may
// read.
type scratch struct {
	Namespace string // the group the maintainer named
	Name      string // the run's name, also its search word
	Path      string // Namespace/Name
	ID        int64
	WebURL    string
	Default   string // the default branch
	Feature   string // a branch with one commit on top of Default
	Feature2  string
	File      string // a file on every branch
	SHA       string // Default's head
	Issue     int64
	Issue2    int64
	MR        int64 // Feature into Default
	MR2       int64 // Feature2 into Default, a draft
	Note      int64 // a comment on Issue
	User      string
	Label     string
	Label2    string // a second label, so a label listing pages
	Milestone string // the first of two milestones' titles
	// CI: the default branch's pipeline, which fails, and two of its
	// jobs. Zero when the pipeline did not finish in time.
	Pipeline  int64
	JobFailed int64
	JobPassed int64
	JobManual int64
}

// step is one tool call and what it should do. A refusal that is
// expected proves as much as a result.
type step struct {
	tool        string
	args        map[string]any
	expectError bool
	why         string
	// paged sends the step again with the page_token its result gave,
	// which is how page_token is driven: a token only a result can mint.
	paged bool
	// anyOutcome accepts a result or a refusal: what the instance answers
	// is the finding, and why says what decides it.
	anyOutcome bool
	// pause waits before the step is sent, for work GitLab finishes in
	// the background after answering.
	pause time.Duration
	// save names values of the result, by dotted path, for later steps:
	// a string argument "{{name}}" is replaced by the value saved under
	// name just before the step goes out. A write needs what only a read
	// can give it: a witness, a thread id, a draft id.
	save map[string]string
	// declines answers the step's question to the person with decline
	// rather than accept; the step expects the refusal that follows.
	declines bool
	// until resends the step first, until a value of its result moves:
	// work GitLab finishes in the background, after the answer.
	until *waitFor
}

// waitFor is a value at path, in a step's result, that must come to
// differ from the one an earlier step saved under saved. The step is
// sent every every, for at most within; a value that never moves fails
// the step.
type waitFor struct {
	path, saved   string
	every, within time.Duration
}

// moved reports whether the result's value at the path differs from the
// saved one.
func (w waitFor) moved(structured json.RawMessage, saved map[string]any) bool {
	v, ok := valueAt(structured, w.path)
	return ok && v != saved[w.saved]
}

// plan is every tool, every option at least once, against the scratch
// project. Group and instance-wide searches carry the run's word, so what
// comes back is the run's own; a listing that could name other people
// (members, users, labels a group defines, to-do items) is narrowed to
// the run's account or project.
func plan(s scratch) []step {
	return slices.Concat(phase0(s), phase1(s), phase2(s))
}

func phase0(s scratch) []step {
	p := s.Path
	yesterday := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	tomorrow := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	return []step{
		{tool: "get_me", args: map[string]any{}},
		{tool: "resolve_url", args: map[string]any{"url": s.WebURL + "/-/issues/" + itoa(s.Issue)}},
		{tool: "resolve_url", args: map[string]any{"url": s.WebURL + "/-/merge_requests/" + itoa(s.MR)}},
		{tool: "resolve_url", args: map[string]any{"url": s.WebURL + "/-/blob/" + s.Default + "/" + s.File + "#L2"}},
		{tool: "resolve_url", args: map[string]any{"url": "https://elsewhere.invalid/example-group/project/-/issues/1"},
			expectError: true, why: "a link to another host"},

		{tool: "search_projects", args: map[string]any{"search": s.Name, "scope": "owned", "visibility": "private",
			"include_archived": false, "max": 5}},
		{tool: "search_projects", args: map[string]any{"group": s.Namespace, "include_subgroups": true, "search": s.Name,
			"scope": "member", "max": 1}, paged: true},
		{tool: "get_project", args: map[string]any{"project": p}},
		{tool: "get_project", args: map[string]any{"project": s.ID}},
		{tool: "get_project", args: map[string]any{"project": s.WebURL}},
		{tool: "get_project", args: map[string]any{"project": s.ID, "offset": 10}},

		{tool: "search_issues", args: map[string]any{"project": p, "state": "opened", "labels": []any{s.Label},
			"author": s.User, "search": s.Name, "order_by": "created_at", "sort": "asc", "max": 1,
			"created_after": yesterday, "created_before": tomorrow, "updated_after": yesterday, "updated_before": tomorrow}, paged: true},
		{tool: "search_issues", args: map[string]any{"group": s.Namespace, "search": s.Name, "scope": "created_by_me", "state": "all"}},
		{tool: "search_issues", args: map[string]any{"search": s.Name, "scope": "all", "assignee": s.User, "milestone": "None"}},
		{tool: "get_issue", args: map[string]any{"project": p, "iid": s.Issue}},
		{tool: "get_issue", args: map[string]any{"project": p, "iid": s.Issue, "offset": 10}},
		{tool: "get_issue", args: map[string]any{"project": p, "iid": 99999}, expectError: true, why: "an issue that does not exist"},
		{tool: "list_discussions", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "include_system": true,
			"max": 1}, paged: true},
		{tool: "list_discussions", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "note_id": s.Note, "offset": 0}},
		{tool: "list_discussions", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "unresolved_only": true}},

		{tool: "search_merge_requests", args: map[string]any{"project": p, "state": "opened", "source_branch": s.Feature,
			"target_branch": s.Default, "draft": false, "author": s.User, "search": s.Name, "order_by": "updated_at", "sort": "desc",
			"created_after": yesterday, "created_before": tomorrow, "updated_after": yesterday, "updated_before": tomorrow}},
		{tool: "search_merge_requests", args: map[string]any{"group": s.Namespace, "search": s.Name, "scope": "created_by_me",
			"state": "all", "max": 1}, paged: true},
		{tool: "search_merge_requests", args: map[string]any{"search": s.Name, "scope": "all", "reviewer": s.User,
			"assignee": s.User, "labels": []any{s.Label}, "milestone": "None"}},
		{tool: "get_merge_request", args: map[string]any{"project": p, "iid": s.MR}},
		{tool: "get_merge_request", args: map[string]any{"project": p, "iid": s.MR2, "offset": 5}},

		{tool: "get_file", args: map[string]any{"project": p, "path": s.File}},
		{tool: "get_file", args: map[string]any{"project": p, "path": s.File, "ref": s.Feature, "offset": 3}},
		{tool: "get_file", args: map[string]any{"project": p, "path": "missing/file.md"}, expectError: true, why: "a path that does not exist"},
		{tool: "list_tree", args: map[string]any{"project": p, "ref": s.Default, "recursive": true, "max": 1}, paged: true},
		{tool: "list_tree", args: map[string]any{"project": p, "path": "docs"}},
		{tool: "list_branches", args: map[string]any{"project": p, "max": 1}, paged: true},
		{tool: "list_branches", args: map[string]any{"project": p, "search": "feature"}},
		{tool: "list_commits", args: map[string]any{"project": p, "ref": s.Feature, "max": 1, "first_parent": true,
			"since": yesterday, "until": tomorrow}, paged: true},
		{tool: "list_commits", args: map[string]any{"project": p, "path": s.File, "author": s.User}},
		{tool: "get_commit", args: map[string]any{"project": p, "sha": s.SHA}},
		// A branch name, resolved to its commit, whose merge request is the
		// scratch one; the offset reads after it skip the links.
		{tool: "get_commit", args: map[string]any{"project": p, "sha": s.Feature}},
		{tool: "get_commit", args: map[string]any{"project": p, "sha": s.Feature, "file_offset": 1, "message_offset": 7}},
		{tool: "get_commit", args: map[string]any{"project": p, "sha": s.Feature, "file_offset": 0, "diff_offset": 5}},
	}
}

// phase1 is the rest of reading (§16 phase 1).
func phase1(s scratch) []step {
	p := s.Path
	yesterday := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	tomorrow := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	return []step{
		{tool: "resolve_url", args: map[string]any{"url": s.WebURL + "/-/compare/" + s.Default + "..." + s.Feature}},
		{tool: "resolve_url", args: map[string]any{"url": s.WebURL + "/-/pipelines/" + itoa(s.Pipeline)}},
		{tool: "resolve_url", args: map[string]any{"url": s.WebURL + "/-/jobs/" + itoa(s.JobFailed)}},

		{tool: "list_members", args: map[string]any{"project": p, "query": s.User, "max": 1}, paged: true},
		{tool: "find_users", args: map[string]any{"username": s.User, "max": 1}},
		{tool: "find_users", args: map[string]any{"search": s.Name, "max": 5}, paged: true},

		{tool: "list_mr_files", args: map[string]any{"project": p, "iid": s.MR, "max": 1}, paged: true},
		{tool: "get_mr_diff", args: map[string]any{"project": p, "iid": s.MR}},
		{tool: "get_mr_diff", args: map[string]any{"project": p, "iid": s.MR, "paths": []any{s.File}, "file_offset": 0}},
		{tool: "get_mr_diff", args: map[string]any{"project": p, "iid": s.MR, "file_offset": 1}},
		{tool: "get_mr_diff", args: map[string]any{"project": p, "iid": s.MR, "diff_offset": 5}},
		{tool: "get_mr_diff", args: map[string]any{"project": p, "iid": s.MR, "paths": []any{"missing/file.md"}},
			expectError: true, why: "a path the merge request does not change"},
		{tool: "list_mr_commits", args: map[string]any{"project": p, "iid": s.MR, "max": 1}, paged: true},
		{tool: "list_review_comments", args: map[string]any{"project": p, "iid": s.MR}, paged: true},

		{tool: "compare_refs", args: map[string]any{"project": p, "from": s.Default, "to": s.Feature}},
		{tool: "compare_refs", args: map[string]any{"project": p, "from": s.Default, "to": s.Feature, "diff_offset": 5}},
		{tool: "compare_refs", args: map[string]any{"project": p, "from": s.Default, "to": s.Feature, "straight": true,
			"commit_offset": 1, "file_offset": 1}},
		{tool: "list_tags", args: map[string]any{"project": p, "search": "^v0", "order_by": "version", "sort": "asc", "max": 1},
			paged: true},

		{tool: "list_pipelines", args: map[string]any{"project": p, "max": 1}, paged: true},
		{tool: "list_pipelines", args: map[string]any{"project": p, "ref": s.Default, "sha": s.SHA, "status": "failed",
			"source": "push", "username": s.User, "updated_after": yesterday, "updated_before": tomorrow, "order_by": "id",
			"sort": "desc"}},
		{tool: "get_pipeline", args: map[string]any{"project": p, "pipeline_id": s.Pipeline}},
		{tool: "list_jobs", args: map[string]any{"project": p, "pipeline_id": s.Pipeline, "scope": "failed",
			"include_retried": true, "max": 1}, paged: true},
		{tool: "get_job_log", args: map[string]any{"project": p, "job_id": s.JobFailed}},
		{tool: "get_job_log", args: map[string]any{"project": p, "job_id": s.JobFailed, "failed_only": true}},
		{tool: "get_job_log", args: map[string]any{"project": p, "job_id": s.JobFailed, "byte_offset": 0, "byte_limit": 2000}},
		{tool: "get_job_log", args: map[string]any{"project": p, "job_id": s.JobPassed}},
		{tool: "get_test_report", args: map[string]any{"project": p, "pipeline_id": s.Pipeline}},
		{tool: "get_test_report", args: map[string]any{"project": p, "pipeline_id": s.Pipeline, "offset": 1}},
		{tool: "get_test_report", args: map[string]any{"project": p, "pipeline_id": s.Pipeline, "offset": 5}, expectError: true,
			why: "the report has one failed case, so offset 5 is past its end"},
		{tool: "lint_ci", args: map[string]any{"project": p, "ref": s.Default, "simulate": true, "include_jobs": true, "offset": 10}},
		{tool: "lint_ci", args: map[string]any{"project": p}},

		{tool: "list_labels", args: map[string]any{"project": p, "search": "live-", "project_only": true, "with_counts": true,
			"max": 1}, paged: true},
		{tool: "list_milestones", args: map[string]any{"project": p, "include_ancestors": false, "max": 1}, paged: true},
		{tool: "list_milestones", args: map[string]any{"project": p, "state": "active", "search": "live", "title": s.Milestone}},
		{tool: "list_milestones", args: map[string]any{"group": s.Namespace, "search": s.Name}},
		// Two seeded boards, the first with a list for each label.
		{tool: "list_boards", args: map[string]any{"project": p, "max": 1}, paged: true},
		{tool: "list_todos", args: map[string]any{"project": p, "max": 1}, paged: true},
		{tool: "list_todos", args: map[string]any{"project": p, "state": "pending", "action": "marked", "type": "Issue"}},

		{tool: "search", args: map[string]any{"scope": "issues", "search": s.Name, "project": p, "state": "opened", "max": 1},
			paged: true},
		{tool: "search", args: map[string]any{"scope": "merge_requests", "search": s.Name, "project": p}},
		{tool: "search", args: map[string]any{"scope": "blobs", "search": s.Name, "project": p, "ref": s.Feature}},
		{tool: "search", args: map[string]any{"scope": "commits", "search": "live file", "project": p}},
		{tool: "search", args: map[string]any{"scope": "notes", "search": "synthetic comment", "project": p}},
		{tool: "search", args: map[string]any{"scope": "milestones", "search": "live milestone", "project": p}},
		{tool: "search", args: map[string]any{"scope": "wiki_blobs", "search": s.Name, "project": p}},
		{tool: "search", args: map[string]any{"scope": "users", "search": s.Name, "project": p}},
		{tool: "search", args: map[string]any{"scope": "issues", "search": s.Name, "group": s.Namespace}},
		{tool: "search", args: map[string]any{"scope": "blobs", "search": s.Name, "group": s.Namespace}, anyOutcome: true,
			why: "code across a group needs advanced search, which the group's tier decides"},
		{tool: "search", args: map[string]any{"scope": "projects", "search": s.Name}},
	}
}

// phase2 is the write path (§16 phase 2). Every write goes to the scratch
// project, names only the run's own account, and is shown first as a dry
// run where the tool takes one. Inline comments land on docs/live.md of
// the run's merge request, whose diff is one hunk:
//
//	@@ -1,4 +1,4 @@
//	 # Live <name>
//
//	-Line two.
//	-Line three.
//	+Line two, changed.
//	+Line three, tidied.
func phase2(s scratch) []step {
	p := s.Path
	me := []any{s.User}
	branch := "write-" + s.Name[len(s.Name)-6:]
	branch2 := "write2-" + s.Name[len(s.Name)-6:]
	milestone2 := "Live milestone two " + s.Name
	create := func(path, content string) map[string]any {
		return map[string]any{"action": "create", "file_path": path, "content": content}
	}
	return []step{
		// Issues.
		{tool: "create_issue", args: map[string]any{"project": p, "title": "Written issue " + s.Name, "dry_run": true}},
		{tool: "create_issue", args: map[string]any{"project": p, "title": "Refused issue " + s.Name, "description": "Text.\n\n/close"},
			expectError: true, why: "a quick-action line in the description, without escape_commands"},
		{tool: "create_issue", args: map[string]any{"project": p, "title": "Written issue " + s.Name,
			"description": "A synthetic issue the live run wrote.\n\n/close\n", "escape_commands": true, "labels": []any{s.Label},
			"assignees": me, "milestone": s.Milestone, "due_date": "2030-01-31", "confidential": true},
			save: map[string]string{"issue": "iid", "issue_at": "updated_at"}},
		{tool: "create_issue", args: map[string]any{"project": p, "title": "x", "labels": []any{"no-such-label-" + s.Name}},
			expectError: true, why: "a label the project does not have"},
		{tool: "update_issue", args: map[string]any{"project": p, "iid": "{{issue}}", "updated_at": "{{issue_at}}", "state": "close",
			"dry_run": true}},
		{tool: "update_issue", args: map[string]any{"project": p, "iid": "{{issue}}", "updated_at": "{{issue_at}}",
			"title": "Written issue, retitled " + s.Name, "description": "Replaced by the live run.\n\n/label ~x\n", "escape_commands": true,
			"add_labels": []any{s.Label2}, "remove_labels": []any{s.Label}, "milestone": milestone2, "due_date": "2030-02-28",
			"state": "close"},
			save: map[string]string{"issue_at2": "updated_at"}},
		{tool: "update_issue", args: map[string]any{"project": p, "iid": "{{issue}}", "updated_at": "{{issue_at2}}", "confidential": false},
			expectError: true, why: "making a confidential issue public, without GITLAB_MCP_ENABLE_SHIP"},
		{tool: "update_issue", args: map[string]any{"project": p, "iid": "{{issue}}", "updated_at": "{{issue_at}}", "title": "Stale"},
			expectError: true, why: "a witness from before the last update"},
		{tool: "update_issue", args: map[string]any{"project": p, "iid": "{{issue}}", "updated_at": "{{issue_at2}}", "state": "reopen",
			"clear_milestone": true, "clear_due_date": true, "remove_assignees": me},
			save: map[string]string{"issue_at3": "updated_at"}},
		{tool: "update_issue", args: map[string]any{"project": p, "iid": "{{issue}}", "updated_at": "{{issue_at3}}", "add_assignees": me}},

		// Comments and threads, and where an inline comment lands (spike K).
		{tool: "add_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "body": "A comment the live run wrote.",
			"dry_run": true}},
		{tool: "add_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "body": "A comment the live run wrote."},
			save: map[string]string{"issue_note": "note_id", "issue_note_at": "updated_at"}},
		// The comment, edited in place before the reply: a reply makes it
		// a thread, which moves its updated_at (§18 row 93).
		{tool: "update_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "note_id": "{{issue_note}}",
			"updated_at": "{{issue_note_at}}", "body": "A comment the live run wrote, then edited.", "dry_run": true}},
		{tool: "update_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "note_id": "{{issue_note}}",
			"updated_at": "2020-01-01T00:00:00Z", "body": "Stale."}, expectError: true, why: "a witness from before the comment"},
		{tool: "update_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "note_id": "{{issue_note}}",
			"updated_at": "{{issue_note_at}}", "body": "Done.\n\n/close"},
			expectError: true, why: "a quick-action line in the body, without escape_commands"},
		{tool: "update_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "note_id": "{{issue_note}}",
			"updated_at": "{{issue_note_at}}", "body": "A comment the live run wrote, then edited.\n\n/close stays text.",
			"escape_commands": true}},
		{tool: "update_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "note_id": "{{issue_note}}",
			"updated_at": "{{issue_note_at}}", "body": "Again."}, expectError: true, why: "the witness the edit replaced"},
		// A standalone comment does not name its thread; the newest one is it.
		{tool: "list_discussions", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue},
			save: map[string]string{"issue_thread": "threads.0.id"}},
		{tool: "add_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "body": "A reply the live run wrote.",
			"discussion_id": "{{issue_thread}}"}},
		{tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "body": "Done.\n\n/merge"},
			expectError: true, why: "a quick-action line in the body, without escape_commands"},
		{tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "body": "Escaped.\n\n/merge",
			"escape_commands": true}},
		{tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "body": "On an added line.",
			"file": s.File, "line": 3, "side": "new"}, save: map[string]string{"mr_thread": "discussion_id", "mr_note": "note_id", "mr_note_at": "updated_at"}},
		// Turned into a suggestion, in its thread on its line.
		{tool: "update_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "note_id": "{{mr_note}}",
			"updated_at": "{{mr_note_at}}", "body": "On an added line, as a suggestion:\n\n```suggestion:-0+0\nsuggested by the live run\n```"}},
		// Still the thread it was, on its line, with the new text.
		{tool: "list_discussions", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR}},
		{tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "body": "On a removed line.",
			"file": s.File, "line": 3, "side": "old"}},
		{tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "body": "On an unchanged line.",
			"file": s.File, "line": 1, "side": "new"}},
		{tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "body": "On a range.",
			"file": s.File, "line": 1, "end_line": 4, "side": "new"}},
		{tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "body": "Outside.",
			"file": s.File, "line": 40, "side": "new"}, expectError: true, why: "a line outside every hunk"},
		{tool: "resolve_discussion", args: map[string]any{"project": p, "iid": s.MR, "discussion_id": "{{mr_thread}}", "dry_run": true}},
		{tool: "resolve_discussion", args: map[string]any{"project": p, "iid": s.MR, "discussion_id": "{{mr_thread}}"}},
		{tool: "resolve_discussion", args: map[string]any{"project": p, "iid": s.MR, "discussion_id": "{{mr_thread}}", "reopen": true}},
		// A resolvable thread on an issue, resolved and reopened.
		{tool: "add_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "body": "A thread the live run started.",
			"thread": true}, save: map[string]string{"issue_new_thread": "discussion_id"}},
		{tool: "resolve_discussion", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue,
			"discussion_id": "{{issue_new_thread}}"}},
		{tool: "resolve_discussion", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue,
			"discussion_id": "{{issue_new_thread}}", "reopen": true}},
		{tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "body": "A general thread.",
			"thread": true}, save: map[string]string{"mr_thread2": "discussion_id"}},

		// A review: drafts, one deleted, then submitted.
		{tool: "add_review_comment", args: map[string]any{"project": p, "iid": s.MR, "body": "A draft on a range.", "file": s.File,
			"line": 3, "end_line": 4, "side": "old", "dry_run": true}},
		{tool: "add_review_comment", args: map[string]any{"project": p, "iid": s.MR, "body": "A draft on a range.", "file": s.File,
			"line": 3, "end_line": 4, "side": "old"}},
		{tool: "add_review_comment", args: map[string]any{"project": p, "iid": s.MR, "body": "A draft reply that resolves.",
			"discussion_id": "{{mr_thread}}", "resolve_thread": true}},
		{tool: "add_review_comment", args: map[string]any{"project": p, "iid": s.MR, "body": "A draft to delete.\n/approve",
			"escape_commands": true}, save: map[string]string{"draft": "note_id"}},
		{tool: "delete_review_comment", args: map[string]any{"project": p, "iid": s.MR, "draft_id": "{{draft}}", "dry_run": true}},
		{tool: "delete_review_comment", args: map[string]any{"project": p, "iid": s.MR, "draft_id": "{{draft}}"}},
		// One draft edited on its line, then published on its own.
		{tool: "add_review_comment", args: map[string]any{"project": p, "iid": s.MR, "body": "A draft to edit.", "file": s.File,
			"line": 3, "side": "new"}, save: map[string]string{"edit_draft": "note_id", "edit_sha": "note_sha256"}},
		{tool: "update_review_comment", args: map[string]any{"project": p, "iid": s.MR, "draft_id": "{{edit_draft}}",
			"note_sha256": "{{edit_sha}}", "body": "A draft the live run edited.", "dry_run": true}},
		{tool: "update_review_comment", args: map[string]any{"project": p, "iid": s.MR, "draft_id": "{{edit_draft}}",
			"note_sha256": strings.Repeat("0", 64), "body": "Stale."}, expectError: true, why: "a witness of another text"},
		{tool: "update_review_comment", args: map[string]any{"project": p, "iid": s.MR, "draft_id": "{{edit_draft}}",
			"note_sha256": "{{edit_sha}}", "body": "Done.\n\n/approve"},
			expectError: true, why: "a quick-action line in the body, without escape_commands"},
		{tool: "update_review_comment", args: map[string]any{"project": p, "iid": s.MR, "draft_id": "{{edit_draft}}",
			"note_sha256": "{{edit_sha}}", "body": "A draft the live run edited, still on its line.\n\n/approve stays text.",
			"escape_commands": true}},
		// Still on its line, with the new text and its new note_sha256.
		{tool: "list_review_comments", args: map[string]any{"project": p, "iid": s.MR}},
		{tool: "publish_review_comment", args: map[string]any{"project": p, "iid": s.MR, "draft_id": "{{edit_draft}}", "dry_run": true}},
		{tool: "publish_review_comment", args: map[string]any{"project": p, "iid": s.MR, "draft_id": "{{edit_draft}}"}},
		{tool: "publish_review_comment", args: map[string]any{"project": p, "iid": s.MR, "draft_id": "{{edit_draft}}"},
			expectError: true, why: "a draft already published is gone"},
		// GitLab keeps one draft reply per person per thread, and mr_thread
		// has one.
		{tool: "add_review_comment", args: map[string]any{"project": p, "iid": s.MR, "body": "A second draft reply.",
			"discussion_id": "{{mr_thread}}"}, expectError: true, why: "a second draft reply in one thread"},
		// A reply that does not resolve its thread reopens it when published.
		{tool: "add_review_comment", args: map[string]any{"project": p, "iid": s.MR, "body": "A draft reply that reopens.",
			"discussion_id": "{{mr_thread2}}"}, save: map[string]string{"reply_draft": "note_id"}},
		{tool: "resolve_discussion", args: map[string]any{"project": p, "iid": s.MR, "discussion_id": "{{mr_thread2}}"}},
		{tool: "publish_review_comment", args: map[string]any{"project": p, "iid": s.MR, "draft_id": "{{reply_draft}}"}},
		{tool: "submit_review", args: map[string]any{"project": p, "iid": s.MR, "reviewer_state": "approved"},
			expectError: true, why: "an approving review without GITLAB_MCP_ENABLE_SHIP"},
		{tool: "submit_review", args: map[string]any{"project": p, "iid": s.MR, "summary": "Summary.", "dry_run": true}},
		{tool: "submit_review", args: map[string]any{"project": p, "iid": s.MR, "summary": "A review the live run wrote.\n/approve",
			"escape_commands": true, "reviewer_state": "reviewed"}},

		// Code: a branch, commits on it, a merge request from it.
		{tool: "create_branch", args: map[string]any{"project": p, "branch": branch, "ref": s.Default, "dry_run": true}},
		{tool: "create_branch", args: map[string]any{"project": p, "branch": branch, "ref": s.Default}},
		{tool: "create_branch", args: map[string]any{"project": p, "branch": branch}, expectError: true, why: "a branch that exists"},
		{tool: "create_branch", args: map[string]any{"project": p, "branch": strings.TrimSuffix(guardBranch(s), "one") + "three",
			"ref": branch}, expectError: true, why: "a new branch a wildcard rule would protect"},
		{tool: "create_commit", args: map[string]any{"project": p, "branch": s.Default, "message": "Refused",
			"actions": []any{create("refused.txt", "x\n")}}, expectError: true, why: "the default branch"},
		{tool: "create_commit", args: map[string]any{"project": p, "branch": guardBranch(s), "message": "Refused",
			"actions": []any{create("refused.txt", "x\n")}}, expectError: true, why: "a branch a wildcard rule protects"},
		{tool: "create_commit", args: map[string]any{"project": p, "branch": strings.TrimSuffix(guardBranch(s), "one") + "two",
			"start_branch": s.Default, "message": "Refused", "actions": []any{create("refused.txt", "x\n")}},
			expectError: true, why: "a new branch a wildcard rule would protect"},
		{tool: "create_commit", args: map[string]any{"project": p, "branch": branch, "message": "Dry", "dry_run": true,
			"actions": []any{create("dry.txt", "x\n")}}},
		{tool: "create_commit", args: map[string]any{"project": p, "branch": branch, "message": "Add written files",
			"actions": []any{create("written/a.txt", "a\n"), create("written/b.txt", "b\n"),
				map[string]any{"action": "create", "file_path": "written/c.bin", "content": "AAEC", "encoding": "base64"}}}},
		{tool: "get_file", args: map[string]any{"project": p, "path": "written/a.txt", "ref": branch},
			save: map[string]string{"a_commit": "last_commit_id"}},
		{tool: "create_commit", args: map[string]any{"project": p, "branch": branch, "message": "Change, move and delete",
			"actions": []any{
				map[string]any{"action": "update", "file_path": "written/a.txt", "content": "a, changed\n", "last_commit_id": "{{a_commit}}"},
				map[string]any{"action": "move", "file_path": "written/moved.txt", "previous_path": "written/b.txt", "last_commit_id": "{{a_commit}}"},
				map[string]any{"action": "delete", "file_path": "written/c.bin", "last_commit_id": "{{a_commit}}"}}}},
		{tool: "get_file", args: map[string]any{"project": p, "path": "written/moved.txt", "ref": branch}},
		{tool: "create_commit", args: map[string]any{"project": p, "branch": branch, "message": "Stale",
			"actions": []any{map[string]any{"action": "update", "file_path": "written/a.txt", "content": "stale\n",
				"last_commit_id": "{{a_commit}}"}}}, expectError: true, why: "a last_commit_id the file has moved past"},
		{tool: "create_commit", args: map[string]any{"project": p, "branch": branch2, "start_branch": s.Default, "message": "Start a branch",
			"actions": []any{create("written/second.txt", "second\n")}}},
		{tool: "create_merge_request", args: map[string]any{"project": p, "source_branch": branch, "title": "Written merge request " + s.Name,
			"dry_run": true}},
		{tool: "create_merge_request", args: map[string]any{"project": p, "source_branch": branch, "target_branch": s.Default,
			"title": "Written merge request " + s.Name, "description": "The live run wrote this.\n\n/merge\n", "escape_commands": true,
			"labels": []any{s.Label}, "assignees": me, "reviewers": me, "milestone": s.Milestone, "draft": true,
			"remove_source_branch": true, "squash": true}, save: map[string]string{"mr": "iid"}},
		// GitLab moves a new merge request's updated_at a moment after
		// the create answers, so the witness is read afterwards.
		{tool: "get_merge_request", args: map[string]any{"project": p, "iid": "{{mr}}"}, pause: 10 * time.Second,
			save: map[string]string{"mr_at": "updated_at"}},
		{tool: "create_merge_request", args: map[string]any{"project": p, "source_branch": branch, "title": "Again"},
			expectError: true, why: "an open merge request for the same branches"},
		{tool: "update_merge_request", args: map[string]any{"project": p, "iid": "{{mr}}", "updated_at": "{{mr_at}}", "draft": false,
			"dry_run": true}},
		{tool: "update_merge_request", args: map[string]any{"project": p, "iid": "{{mr}}", "updated_at": "{{mr_at}}", "draft": false,
			"title": "Written merge request, retitled " + s.Name, "description": "Replaced.\n/close", "escape_commands": true,
			"add_labels": []any{s.Label2}, "remove_labels": []any{s.Label}, "remove_assignees": me, "remove_reviewers": me,
			"milestone": milestone2, "remove_source_branch": false, "squash": false, "target_branch": s.Default},
			save: map[string]string{"mr_at2": "updated_at"}},
		{tool: "update_merge_request", args: map[string]any{"project": p, "iid": "{{mr}}", "updated_at": "{{mr_at}}", "squash": true},
			expectError: true, why: "a witness from before the last update"},
		{tool: "update_merge_request", args: map[string]any{"project": p, "iid": "{{mr}}", "updated_at": "{{mr_at2}}", "state": "close",
			"clear_milestone": true, "add_assignees": me, "add_reviewers": me}, save: map[string]string{"mr_at3": "updated_at"}},
		{tool: "update_merge_request", args: map[string]any{"project": p, "iid": "{{mr}}", "updated_at": "{{mr_at3}}", "state": "reopen"}},

		// A push after the merge request opened gives it a second diff
		// version, and the changes since the first are that push alone.
		// The run's account is a reviewer again, so get_merge_request below
		// shows its review state.
		{tool: "list_mr_versions", args: map[string]any{"project": p, "iid": "{{mr}}"}, save: map[string]string{"mr_v1": "versions.0.id"}},
		{tool: "create_commit", args: map[string]any{"project": p, "branch": branch, "message": "A change after review",
			"actions": []any{create("written/after-review.txt", "after review\n")}}},
		// GitLab makes the version in the background after the push.
		{tool: "list_mr_versions", args: map[string]any{"project": p, "iid": "{{mr}}", "max": 1},
			until: &waitFor{path: "versions.0.id", saved: "mr_v1", every: 5 * time.Second, within: time.Minute},
			save:  map[string]string{"mr_v2": "versions.0.id"}},
		// Paged on its own: a paged step saves again from the page it
		// follows to, which holds an older version.
		{tool: "list_mr_versions", args: map[string]any{"project": p, "iid": "{{mr}}", "max": 1}, paged: true},
		{tool: "compare_mr_versions", args: map[string]any{"project": p, "iid": "{{mr}}", "from_version": "{{mr_v1}}"}},
		{tool: "compare_mr_versions", args: map[string]any{"project": p, "iid": "{{mr}}", "from_version": "{{mr_v1}}",
			"to_version": "{{mr_v2}}", "commit_offset": 1, "file_offset": 0, "diff_offset": 5}},
		{tool: "compare_mr_versions", args: map[string]any{"project": p, "iid": "{{mr}}", "from_version": "{{mr_v2}}"},
			expectError: true, why: "the newest version: nothing was pushed after it"},

		// Time tracking, every option on the issue and the merge request,
		// from the witnesses a read gives: updated_at, and the total spent,
		// which GitLab does not move updated_at for (§18 row 101).
		{tool: "get_issue", args: map[string]any{"project": p, "iid": "{{issue}}"},
			save: map[string]string{"issue_tt": "updated_at", "issue_spent": "time_stats.total_time_spent"}},
		{tool: "track_time", args: map[string]any{"project": p, "type": "issue", "iid": "{{issue}}", "updated_at": "{{issue_tt}}",
			"total_time_spent": "{{issue_spent}}", "estimate": "1d 2h", "add_spent": "1h30m", "dry_run": true}},
		{tool: "track_time", args: map[string]any{"project": p, "type": "issue", "iid": "{{issue}}", "updated_at": "{{issue_tt}}",
			"total_time_spent": "{{issue_spent}}", "estimate": "1d 2h", "add_spent": "1h30m"},
			save: map[string]string{"issue_tt2": "updated_at", "issue_spent2": "total_time_spent"}},
		{tool: "track_time", args: map[string]any{"project": p, "type": "issue", "iid": "{{issue}}", "updated_at": "{{issue_tt}}",
			"total_time_spent": "{{issue_spent2}}", "add_spent": "1h"}, expectError: true, why: "an updated_at from before the estimate changed"},
		{tool: "track_time", args: map[string]any{"project": p, "type": "issue", "iid": "{{issue}}", "updated_at": "{{issue_tt2}}",
			"total_time_spent": "{{issue_spent2}}", "add_spent": "-30m"},
			save: map[string]string{"issue_tt3": "updated_at", "issue_spent3": "total_time_spent"}},
		{tool: "track_time", args: map[string]any{"project": p, "type": "issue", "iid": "{{issue}}", "updated_at": "{{issue_tt3}}",
			"total_time_spent": "{{issue_spent2}}", "add_spent": "-30m"}, expectError: true,
			why: "the same time sent again with the total from before it: updated_at did not move, the total did"},
		// The resets one at a time, so the transcript shows whether a reset
		// of spent time alone moves updated_at.
		{tool: "track_time", args: map[string]any{"project": p, "type": "issue", "iid": "{{issue}}", "updated_at": "{{issue_tt3}}",
			"total_time_spent": "{{issue_spent3}}", "reset_spent": true}, save: map[string]string{"issue_tt4": "updated_at"}},
		{tool: "track_time", args: map[string]any{"project": p, "type": "issue", "iid": "{{issue}}", "updated_at": "{{issue_tt4}}",
			"reset_estimate": true}},
		{tool: "get_merge_request", args: map[string]any{"project": p, "iid": "{{mr}}"},
			save: map[string]string{"mr_tt": "updated_at", "mr_spent": "time_stats.total_time_spent"}},
		{tool: "track_time", args: map[string]any{"project": p, "type": "merge_request", "iid": "{{mr}}", "updated_at": "{{mr_tt}}",
			"total_time_spent": "{{mr_spent}}", "estimate": "2h", "add_spent": "45m"},
			save: map[string]string{"mr_tt2": "updated_at", "mr_spent2": "total_time_spent"}},
		{tool: "track_time", args: map[string]any{"project": p, "type": "merge_request", "iid": "{{mr}}", "updated_at": "{{mr_tt2}}",
			"total_time_spent": "{{mr_spent2}}", "add_spent": "15m"},
			save: map[string]string{"mr_tt3": "updated_at", "mr_spent3": "total_time_spent"}},
		{tool: "track_time", args: map[string]any{"project": p, "type": "merge_request", "iid": "{{mr}}", "updated_at": "{{mr_tt3}}",
			"total_time_spent": "{{mr_spent2}}", "add_spent": "15m"}, expectError: true,
			why: "the same time sent again with the total from before it: updated_at did not move, the total did"},
		{tool: "track_time", args: map[string]any{"project": p, "type": "merge_request", "iid": "{{mr}}", "updated_at": "{{mr_tt3}}",
			"total_time_spent": "{{mr_spent3}}", "reset_spent": true}, save: map[string]string{"mr_tt4": "updated_at"}},
		{tool: "track_time", args: map[string]any{"project": p, "type": "merge_request", "iid": "{{mr}}", "updated_at": "{{mr_tt4}}",
			"reset_estimate": true}},

		// Your own notifications and to-do items on the scratch issue and
		// merge request. The account takes part in both, so it is
		// subscribed until it unsubscribes: the first subscribe is
		// unchanged. Each to-do added is marked done, so the account's
		// list is left as it was.
		{tool: "subscribe", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "dry_run": true}},
		{tool: "subscribe", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue}},
		{tool: "subscribe", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "unsubscribe": true, "dry_run": true}},
		{tool: "subscribe", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "unsubscribe": true}},
		{tool: "subscribe", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "unsubscribe": true}},
		{tool: "subscribe", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue}},
		{tool: "subscribe", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "unsubscribe": true}},
		{tool: "subscribe", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR}},
		{tool: "subscribe", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR}},
		{tool: "add_todo", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "dry_run": true}},
		{tool: "add_todo", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue}, save: map[string]string{"todo_issue": "todo_id"}},
		{tool: "add_todo", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "dry_run": true}},
		{tool: "add_todo", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue}},
		{tool: "mark_todos_done", args: map[string]any{"ids": []any{"{{todo_issue}}"}}},
		{tool: "add_todo", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR}, save: map[string]string{"todo_mr": "todo_id"}},
		{tool: "add_todo", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR}},
		{tool: "mark_todos_done", args: map[string]any{"ids": []any{"{{todo_mr}}"}}},

		// The history the writes above made: labels swapped, the milestone
		// set and cleared, closed and reopened.
		{tool: "list_item_events", args: map[string]any{"project": p, "type": "issue", "iid": "{{issue}}", "max": 2}, paged: true},
		{tool: "list_item_events", args: map[string]any{"project": s.ID, "type": "merge_request", "iid": "{{mr}}"}},

		// To-do items: the run's own, found in the scratch project.
		{tool: "list_todos", args: map[string]any{"project": p, "state": "pending"}, save: map[string]string{"todo": "todos.0.id"}},
		{tool: "mark_todos_done", args: map[string]any{"ids": []any{"{{todo}}"}, "dry_run": true}},
		{tool: "mark_todos_done", args: map[string]any{"ids": []any{"{{todo}}"}}},

		// Reactions on the scratch issue, merge request and comments. One
		// already there, or none to remove, sends nothing; thumbs_up is an
		// alias GitLab keeps as thumbsup, which only its answer reveals.
		// Every reaction added is removed, so the items end as they began.
		// They come after the to-do steps: a reaction marks the account's
		// pending to-dos on the item done.
		{tool: "react", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "emoji": "thumbsup", "dry_run": true}},
		{tool: "react", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "emoji": "+1"}},
		{tool: "get_issue", args: map[string]any{"project": p, "iid": s.Issue}},
		{tool: "react", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "emoji": ":thumbsup:"}},
		// Another name of a reaction already there: GitLab refuses it, in the
		// account's language, and its alias table cannot be read back, so
		// GitLab's own reason is passed on.
		{tool: "react", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "emoji": "thumbs_up"},
			expectError: true, why: "an alias of a reaction already there, which GitLab refuses with 404"},
		{tool: "react", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "emoji": "no_such_emoji_here"},
			expectError: true, why: "an emoji GitLab does not know, which it answers with 404"},
		{tool: "react", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "emoji": "thumbsup", "remove": true, "dry_run": true}},
		{tool: "react", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "emoji": "thumbsup", "remove": true}},
		{tool: "react", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "emoji": "thumbsup", "remove": true}},
		{tool: "react", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "emoji": "rocket"}},
		{tool: "get_merge_request", args: map[string]any{"project": p, "iid": s.MR}},
		{tool: "react", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "emoji": "rocket", "remove": true}},
		{tool: "react", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "note_id": "{{mr_note}}", "emoji": "eyes"}},
		{tool: "react", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "note_id": "{{mr_note}}", "emoji": "eyes",
			"remove": true}},
		// A reaction on a comment moves the comment's updated_at (§18 row 106).
		{tool: "add_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "body": "A comment the live run reacts to."},
			save: map[string]string{"react_note": "note_id", "react_note_at": "updated_at"}},
		{tool: "react", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "note_id": "{{react_note}}", "emoji": "tada"}},
		{tool: "update_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "note_id": "{{react_note}}",
			"updated_at": "{{react_note_at}}", "body": "Edited."}, expectError: true,
			why: "the updated_at from before the reaction, which moved it"},
		{tool: "react", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "note_id": "{{react_note}}", "emoji": "tada",
			"remove": true}},

		{tool: "lint_ci", args: map[string]any{"project": p, "content": "check:\n  script:\n    - echo live\n", "include_jobs": true}},
		{tool: "lint_ci", args: map[string]any{"project": p, "content": "include:\n  - local: other.yml\n"}, expectError: true,
			why: "supplied content with an include, which GitLab would fetch"},
	}
}

// planShip is phase 3 (§16), sent to a second server with Ship,
// Destructive and every toolset on and its writes confined to the
// maintainer's group. It merges and deletes only what the run made, and
// every delete is refused once without confirm or with a stale witness
// before it is made.
func planShip(s scratch) []step {
	p := s.Path
	w := s.Name[len(s.Name)-6:]
	branch := "ship-" + w
	branch2 := "write2-" + w // phase 2's, one commit the default branch lacks
	mrBranch := "mrpipe-" + w
	tag, tag2 := "live-"+w, "live-"+w+"-b"
	stale := strings.Repeat("0", 40)
	now := time.Now().UTC()
	yesterday, tomorrow := now.Add(-24*time.Hour).Format(time.RFC3339), now.Add(24*time.Hour).Format(time.RFC3339)
	return slices.Concat([]step{
		// Making a confidential issue public is Ship's to allow.
		{tool: "create_issue", args: map[string]any{"project": p, "title": "A confidential issue " + s.Name, "confidential": true},
			save: map[string]string{"secret_issue": "iid", "secret_at": "updated_at"}},
		{tool: "update_issue", args: map[string]any{"project": p, "iid": "{{secret_issue}}", "updated_at": "{{secret_at}}",
			"confidential": false}},
		// A merge request to approve and merge.
		{tool: "create_branch", args: map[string]any{"project": p, "branch": branch, "ref": s.Default}},
		{tool: "create_commit", args: map[string]any{"project": p, "branch": branch, "message": "A change to merge",
			"actions": []any{map[string]any{"action": "create", "file_path": "ship/merged.txt", "content": "merged\n"}}}},
		{tool: "create_merge_request", args: map[string]any{"project": p, "source_branch": branch, "title": "Ship merge request " + s.Name},
			save: map[string]string{"ship_mr": "iid"}},
		// GitLab computes mergeability in the background.
		{tool: "get_merge_request", args: map[string]any{"project": p, "iid": "{{ship_mr}}"}, pause: 20 * time.Second,
			save: map[string]string{"ship_sha": "sha"}},
		{tool: "approve_merge_request", args: map[string]any{"project": p, "iid": "{{ship_mr}}", "sha": stale},
			expectError: true, why: "a sha that is not the head"},
		{tool: "approve_merge_request", args: map[string]any{"project": p, "iid": "{{ship_mr}}", "sha": "{{ship_sha}}", "dry_run": true}},
		{tool: "approve_merge_request", args: map[string]any{"project": p, "iid": "{{ship_mr}}", "sha": "{{ship_sha}}"}, anyOutcome: true,
			why: "whether the author may approve their own merge request is the project's and the tier's to say"},
		{tool: "unapprove_merge_request", args: map[string]any{"project": p, "iid": "{{ship_mr}}", "dry_run": true}},
		{tool: "unapprove_merge_request", args: map[string]any{"project": p, "iid": "{{ship_mr}}"}},
		{tool: "merge_merge_request", args: map[string]any{"project": p, "iid": "{{ship_mr}}", "sha": stale},
			expectError: true, why: "a sha that is not the head"},
		{tool: "merge_merge_request", args: map[string]any{"project": p, "iid": "{{ship_mr}}", "sha": "{{ship_sha}}", "squash": true,
			"dry_run": true}},
		{tool: "merge_merge_request", args: map[string]any{"project": p, "iid": "{{ship_mr}}", "sha": "{{ship_sha}}", "squash": false,
			"remove_source_branch": false, "merge_commit_message": "Merge the live run's change", "squash_commit_message": "Unused",
			"auto_merge": false}},
		{tool: "merge_merge_request", args: map[string]any{"project": p, "iid": "{{ship_mr}}", "sha": "{{ship_sha}}"}},

		// Branches: the merged one, then phase 2's unmerged one.
		{tool: "list_branches", args: map[string]any{"project": p, "search": branch}, save: map[string]string{"ship_head": "branches.0.commit_id"}},
		{tool: "delete_branch", args: map[string]any{"project": p, "branch": branch, "sha": "{{ship_head}}"},
			expectError: true, why: "no confirm: true"},
		{tool: "delete_branch", args: map[string]any{"project": p, "branch": branch, "sha": "{{ship_head}}", "dry_run": true}},
		{tool: "delete_branch", args: map[string]any{"project": p, "branch": branch, "sha": "{{ship_head}}", "confirm": true}},
		{tool: "list_branches", args: map[string]any{"project": p, "search": branch2}, save: map[string]string{"w2_head": "branches.0.commit_id"}},
		{tool: "delete_branch", args: map[string]any{"project": p, "branch": branch2, "sha": "{{w2_head}}", "confirm": true},
			expectError: true, why: "a branch GitLab does not count merged, without unmerged"},
		{tool: "delete_branch", args: map[string]any{"project": p, "branch": s.Default, "sha": "{{w2_head}}", "confirm": true},
			expectError: true, why: "the default branch"},
		{tool: "delete_branch", args: map[string]any{"project": p, "branch": branch2, "sha": "{{w2_head}}", "unmerged": true, "confirm": true}},

		// A merge request pipeline, on a merge request of its own: the
		// CI configuration gives merge request pipelines to mrpipe-
		// branches only.
		{tool: "create_branch", args: map[string]any{"project": p, "branch": mrBranch, "ref": s.Default}},
		{tool: "create_commit", args: map[string]any{"project": p, "branch": mrBranch, "message": "A change to run a pipeline for",
			"actions": []any{map[string]any{"action": "create", "file_path": "ship/mr-pipeline.txt", "content": "pipeline\nsecond\nthird\n"}}}},
		{tool: "create_merge_request", args: map[string]any{"project": p, "source_branch": mrBranch,
			"title": "Merge request pipeline " + s.Name}, save: map[string]string{"mrpipe_mr": "iid"}},
		// GitLab prepares the merge request's diff in the background.
		{tool: "run_merge_request_pipeline", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}", "dry_run": true},
			pause: 20 * time.Second},
		{tool: "run_merge_request_pipeline", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}"}},
		// Both branches protected, so it asks; the person declines and
		// nothing runs with the protected variables such a pipeline sees.
		{tool: "create_merge_request", args: map[string]any{"project": p, "source_branch": s.Default, "target_branch": guardBranch(s),
			"title": "Default into a protected branch " + s.Name}, save: map[string]string{"mrguard_mr": "iid"}},
		{tool: "run_merge_request_pipeline", args: map[string]any{"project": p, "iid": "{{mrguard_mr}}"}, pause: 20 * time.Second,
			declines: true, expectError: true, why: "both branches are protected, and the person declines the question"},

		// Suggestions on the merge request pipeline's merge request: two
		// applied in one commit, then one more alone once GitLab has moved
		// the comments to the new head.
		{tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": "{{mrpipe_mr}}",
			"body": "A suggestion the live run wrote.\n\n```suggestion:-0+0\npipeline, suggested\n```\n", "file": "ship/mr-pipeline.txt",
			"line": 1, "side": "new"}},
		{tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": "{{mrpipe_mr}}",
			"body": "Another suggestion.\n\n```suggestion:-0+0\nthird, suggested\n```\n", "file": "ship/mr-pipeline.txt",
			"line": 3, "side": "new"}},
		{tool: "list_discussions", args: map[string]any{"project": p, "type": "merge_request", "iid": "{{mrpipe_mr}}"},
			save: map[string]string{"sg_third": "threads.0.notes.0.suggestions.0.id", "sg_first": "threads.1.notes.0.suggestions.0.id"}},
		{tool: "apply_suggestions", args: map[string]any{"project": p, "iid": "{{mrguard_mr}}", "ids": []any{"{{sg_first}}"}},
			expectError: true, why: "a merge request whose source is the default branch"},
		{tool: "apply_suggestions", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}", "ids": []any{"{{sg_first}}", "{{sg_first}}"}},
			expectError: true, why: "an id given twice"},
		{tool: "apply_suggestions", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}", "ids": []any{"{{sg_first}}", "{{sg_third}}"},
			"commit_message": "Apply %{suggestions_count} suggestions to %{branch_name}", "dry_run": true}},
		// The question quotes each text; declined, nothing is committed,
		// and accepted, the batch lands.
		{tool: "apply_suggestions", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}", "ids": []any{"{{sg_first}}", "{{sg_third}}"}},
			declines: true, expectError: true, why: "the person declines the question"},
		{tool: "apply_suggestions", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}", "ids": []any{"{{sg_first}}", "{{sg_third}}"},
			"commit_message": "Apply %{suggestions_count} suggestions to %{branch_name}"}},
		{tool: "apply_suggestions", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}", "ids": []any{"{{sg_first}}"}}},
		{tool: "get_file", args: map[string]any{"project": p, "path": "ship/mr-pipeline.txt", "ref": mrBranch}},
		{tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": "{{mrpipe_mr}}",
			"body": "One more.\n\n```suggestion:-0+0\nsecond, suggested\n```\n", "file": "ship/mr-pipeline.txt", "line": 2, "side": "new"},
			pause: 20 * time.Second},
		{tool: "list_discussions", args: map[string]any{"project": p, "type": "merge_request", "iid": "{{mrpipe_mr}}"},
			save: map[string]string{"sg_second": "threads.0.notes.0.suggestions.0.id"}},
		{tool: "apply_suggestions", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}", "ids": []any{"{{sg_second}}"}}, anyOutcome: true,
			why: "GitLab refuses a suggestion until it has moved the comments to the branch's new head, which takes a moment"},

		// An auto-merge set while the merge request's pipeline runs, then
		// canceled; a pipeline that already passed merges it instead,
		// and the cancel then finds none set.
		{tool: "get_merge_request", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}"}, pause: 10 * time.Second,
			save: map[string]string{"mrpipe_sha": "sha"}},
		{tool: "merge_merge_request", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}", "sha": "{{mrpipe_sha}}", "auto_merge": true},
			anyOutcome: true, why: "GitLab sets the auto-merge, or merges now when the pipeline has already passed"},
		{tool: "cancel_auto_merge", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}", "dry_run": true}},
		// The pipeline may pass as the cancel arrives: GitLab then answers
		// success and merges anyway. The driver checks no result field, so
		// the tool holds that the outcome is canceled only for an open
		// merge request with the auto-merge off, merging while locked and
		// merged once merged; the transcript shows which.
		{tool: "cancel_auto_merge", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}"}, anyOutcome: true,
			why: "canceled while the pipeline runs, or merging or merged when it passed first; never canceled while locked or merged"},
		{tool: "cancel_auto_merge", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}"}},

		// CI: a pipeline run and canceled, the failed one retried, a job
		// retried, the manual one started.
		{tool: "run_pipeline", args: map[string]any{"project": p, "ref": s.Default, "dry_run": true}},
		{tool: "run_pipeline", args: map[string]any{"project": p, "ref": s.Default, "inputs": map[string]any{"greeting": "live"},
			"variables": []any{map[string]any{"key": "LIVE_VARIABLE", "value": "live-value-" + w, "type": "env_var"}}},
			save: map[string]string{"run_pipeline": "pipeline_id"}},
		{tool: "cancel_pipeline", args: map[string]any{"project": p, "pipeline_id": "{{run_pipeline}}", "dry_run": true}},
		{tool: "cancel_pipeline", args: map[string]any{"project": p, "pipeline_id": "{{run_pipeline}}"}},
		{tool: "retry_pipeline", args: map[string]any{"project": p, "pipeline_id": s.Pipeline, "dry_run": true}},
		{tool: "retry_pipeline", args: map[string]any{"project": p, "pipeline_id": s.Pipeline}},
		{tool: "retry_job", args: map[string]any{"project": p, "job_id": s.JobPassed, "inputs": map[string]any{"undeclared": "x"}},
			expectError: true, why: "an input the job's inputs do not include"},
		{tool: "retry_job", args: map[string]any{"project": p, "job_id": s.JobPassed, "dry_run": true,
			"inputs": map[string]any{"target": "retried"}}},
		{tool: "retry_job", args: map[string]any{"project": p, "job_id": s.JobPassed, "inputs": map[string]any{"target": "retried"}}},
		{tool: "play_job", args: map[string]any{"project": p, "job_id": s.JobManual, "dry_run": true,
			"variables": []any{map[string]any{"key": "LIVE_PLAY", "value": "played-" + w}}, "inputs": map[string]any{"target": "played"}}},
		{tool: "play_job", args: map[string]any{"project": p, "job_id": s.JobManual,
			"variables": []any{map[string]any{"key": "LIVE_PLAY", "value": "played-" + w}}, "inputs": map[string]any{"target": "played"}}},
		{tool: "play_job", args: map[string]any{"project": p, "job_id": s.JobManual}, expectError: true,
			why: "a job no longer waiting to be started"},

		// A comment of the run's, deleted.
		{tool: "add_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "body": "A comment the live run deletes."}},
		{tool: "list_discussions", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue},
			save: map[string]string{"del_note": "threads.0.notes.0.id", "del_at": "threads.0.notes.0.updated_at"}},
		{tool: "delete_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "note_id": "{{del_note}}",
			"updated_at": "2020-01-01T00:00:00Z", "confirm": true}, expectError: true, why: "a witness from before the comment"},
		{tool: "delete_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "note_id": "{{del_note}}",
			"updated_at": "{{del_at}}", "dry_run": true}},
		{tool: "delete_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "note_id": "{{del_note}}",
			"updated_at": "{{del_at}}", "confirm": true}},

		// The wiki.
		{tool: "save_wiki_page", args: map[string]any{"project": p, "title": "Live page " + w, "content": "# Live\n\n/close stays text.\n",
			"format": "markdown", "dry_run": true}},
		{tool: "save_wiki_page", args: map[string]any{"project": p, "title": "Live page " + w, "content": "# Live\n\n/close stays text.\n",
			"format": "markdown"}, save: map[string]string{"page": "slug", "page_sha": "content_sha256"}},
		{tool: "list_wiki_pages", args: map[string]any{"project": p}},
		{tool: "get_wiki_page", args: map[string]any{"project": p, "slug": "{{page}}", "offset": 0}},
		{tool: "resolve_url", args: map[string]any{"url": s.WebURL + "/-/wikis/home"}},
		{tool: "save_wiki_page", args: map[string]any{"project": p, "slug": "{{page}}", "content_sha256": "{{page_sha}}",
			"title": "Live page renamed " + w, "content": "# Live\n\nChanged.\n"}, save: map[string]string{"page2": "slug", "page_sha2": "content_sha256"}},
		{tool: "save_wiki_page", args: map[string]any{"project": p, "slug": "{{page2}}", "content_sha256": "{{page_sha}}", "content": "stale"},
			expectError: true, why: "a content_sha256 from before the change"},
		{tool: "delete_wiki_page", args: map[string]any{"project": p, "slug": "{{page2}}", "content_sha256": "{{page_sha2}}", "dry_run": true}},
		{tool: "delete_wiki_page", args: map[string]any{"project": p, "slug": "{{page2}}", "content_sha256": "{{page_sha2}}", "confirm": true}},

		// Snippets: two, so the listing pages.
		{tool: "create_snippet", args: map[string]any{"project": p, "title": "Live snippet " + w, "description": "Two files.",
			"files": []any{map[string]any{"path": "a.md", "content": "# A\n"}}, "dry_run": true}},
		{tool: "create_snippet", args: map[string]any{"project": p, "title": "Live snippet " + w, "description": "Two files.",
			"files": []any{map[string]any{"path": "a.md", "content": "# A\n"}, map[string]any{"path": "b.sh", "content": "echo b\n"}}},
			save: map[string]string{"snippet": "id"}},
		{tool: "create_snippet", args: map[string]any{"project": p, "title": "Live snippet two " + w,
			"files": []any{map[string]any{"path": "c.txt", "content": "c\n"}}}, save: map[string]string{"snippet2": "id"}},
		{tool: "list_snippets", args: map[string]any{"project": p, "max": 1}, paged: true},
		// A commit to a snippet is followed by GitLab's post-receive job,
		// which moves updated_at after the write answered (§18 row 103):
		// the witness is read once it has run.
		{tool: "get_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet}}"}, pause: 5 * time.Second,
			save: map[string]string{"snippet_at": "updated_at"}},
		{tool: "get_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet}}", "file": "b.sh", "offset": 0}},

		// The two-file snippet changed through files, from the updated_at a
		// read gives; the one-file snippet through content, then deleted.
		{tool: "update_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet}}", "updated_at": "{{snippet_at}}",
			"content": "one file only\n"}, expectError: true, why: "content on a snippet of two files"},
		{tool: "update_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet}}", "updated_at": "{{snippet_at}}",
			"title": "Live snippet renamed " + w, "description": "Changed.\n/close stays text.\n", "dry_run": true,
			"files": []any{map[string]any{"action": "update", "path": "a.md", "content": "# A, changed\n"}}}},
		{tool: "update_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet}}", "updated_at": "{{snippet_at}}",
			"title": "Live snippet renamed " + w, "description": "Changed.\n/close stays text.\n", "files": []any{
				map[string]any{"action": "update", "path": "a.md", "content": "# A, changed\n"},
				map[string]any{"action": "create", "path": "d.md", "content": "# D\n"},
				map[string]any{"action": "move", "previous_path": "b.sh", "path": "run.sh", "content": "echo moved\n"}}}},
		{tool: "get_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet}}"}, pause: 5 * time.Second,
			save: map[string]string{"snippet_at2": "updated_at"}},
		{tool: "update_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet}}", "updated_at": "{{snippet_at}}",
			"title": "Stale"}, expectError: true, why: "a witness from before the change"},
		{tool: "update_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet}}", "updated_at": "{{snippet_at2}}",
			"files": []any{map[string]any{"action": "delete", "path": "d.md"}}}},
		{tool: "get_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet}}", "file": "run.sh"}, pause: 5 * time.Second,
			save: map[string]string{"snippet_at3": "updated_at"}},
		// A title alone commits nothing, so the updated_at its answer gives
		// is the next witness.
		{tool: "update_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet}}", "updated_at": "{{snippet_at3}}",
			"title": "Live snippet titled " + w}, save: map[string]string{"snippet_at4": "updated_at"}},
		{tool: "update_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet}}", "updated_at": "{{snippet_at4}}",
			"title": "Live snippet titled again " + w}, pause: 3 * time.Second},
		{tool: "get_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet2}}"}, save: map[string]string{"snippet2_at": "updated_at"}},
		{tool: "update_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet2}}", "updated_at": "{{snippet2_at}}",
			"content": "c, changed\n"}},
		{tool: "get_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet2}}"}, pause: 5 * time.Second,
			save: map[string]string{"snippet2_at2": "updated_at"}},
		{tool: "delete_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet2}}", "updated_at": "{{snippet2_at}}",
			"confirm": true}, expectError: true, why: "a witness from before the change"},
		{tool: "delete_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet2}}", "updated_at": "{{snippet2_at2}}"},
			expectError: true, why: "no confirm: true"},
		{tool: "delete_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet2}}", "updated_at": "{{snippet2_at2}}",
			"dry_run": true}},
		{tool: "delete_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet2}}", "updated_at": "{{snippet2_at2}}",
			"confirm": true}},

		// Releases: two, so the listing pages.
		{tool: "create_release", args: map[string]any{"project": p, "tag_name": tag, "ref": s.Default, "dry_run": true}},
		{tool: "create_release", args: map[string]any{"project": p, "tag_name": tag, "ref": s.Default, "tag_message": "A live tag",
			"name": "Live release " + w, "description": "Release notes.\n/close stays text.\n", "milestones": []any{s.Milestone},
			"released_at": now.Format(time.RFC3339)}},
		{tool: "create_release", args: map[string]any{"project": p, "tag_name": tag}, expectError: true, why: "a second release of one tag"},
		{tool: "create_release", args: map[string]any{"project": p, "tag_name": tag2, "ref": s.Default,
			"links": []any{map[string]any{"name": "https://example.invalid/steer", "url": "https://example.invalid/steer"}}},
			expectError: true, why: "an asset link off the instance"},
		{tool: "create_release", args: map[string]any{"project": p, "tag_name": tag2, "ref": s.Default, "links": []any{
			map[string]any{"name": "The releases", "url": s.WebURL + "/-/releases", "link_type": "other", "direct_asset_path": "/releases"}}}},
		{tool: "list_releases", args: map[string]any{"project": p, "order_by": "created_at", "sort": "desc", "max": 1}, paged: true},
		{tool: "get_release", args: map[string]any{"project": p, "tag_name": tag, "offset": 0}},

		// Deployments, from the default branch's pipeline, and activity.
		{tool: "list_environments", args: map[string]any{"project": p, "name": "live"}},
		{tool: "list_environments", args: map[string]any{"project": p, "search": "live", "states": "available", "max": 1}, paged: true},
		{tool: "list_deployments", args: map[string]any{"project": p, "order_by": "id", "sort": "desc", "max": 1}, paged: true},
		{tool: "list_deployments", args: map[string]any{"project": p, "environment": "live", "status": "success",
			"updated_after": yesterday, "updated_before": tomorrow}},
		{tool: "list_events", args: map[string]any{"project": p, "max": 1}, paged: true},
		{tool: "list_events", args: map[string]any{"project": p, "action": "created", "target_type": "issue",
			"after": now.Add(-48 * time.Hour).Format(time.DateOnly), "before": now.Add(48 * time.Hour).Format(time.DateOnly), "sort": "asc"}},
	}, phase6(s))
}

// phase6 drives the tools §17.13 added, on the server with every flag and
// toolset on.
func phase6(s scratch) []step {
	p := s.Path
	w := s.Name[len(s.Name)-6:]
	pick := "pick-" + w
	stale := strings.Repeat("0", 40)
	return []step{
		// Linking two issues of the run's.
		{tool: "link_issues", args: map[string]any{"project": p, "iid": s.Issue, "target_project": p, "target_iid": s.Issue2,
			"link_type": "relates_to", "dry_run": true}},
		{tool: "link_issues", args: map[string]any{"project": p, "iid": s.Issue, "target_iid": s.Issue2}, save: map[string]string{"link": "link_id"}},
		// Already linked, seen from the other side: unchanged.
		{tool: "link_issues", args: map[string]any{"project": p, "iid": s.Issue2, "target_iid": s.Issue}},
		{tool: "unlink_issues", args: map[string]any{"project": p, "iid": s.Issue, "target_project": p, "target_iid": s.Issue2, "dry_run": true}},
		{tool: "unlink_issues", args: map[string]any{"project": p, "iid": s.Issue, "target_iid": s.Issue2}},

		// Blame.
		{tool: "get_blame", args: map[string]any{"project": p, "path": s.File}},
		{tool: "get_blame", args: map[string]any{"project": p, "path": s.File, "ref": s.Feature, "start_line": 2, "end_line": 3}},

		// Artifacts of the failed job, whose report prints the synthetic token.
		{tool: "list_job_artifacts", args: map[string]any{"project": p, "job_id": s.JobFailed}},
		{tool: "list_job_artifacts", args: map[string]any{"project": p, "job_id": s.JobFailed, "path": "reports", "recursive": true, "max": 1},
			paged: true},
		{tool: "get_job_artifact", args: map[string]any{"project": p, "job_id": s.JobFailed, "path": "reports/summary.txt", "offset": 0}},
		{tool: "get_job_artifact", args: map[string]any{"project": p, "job_id": s.JobFailed, "path": "reports/missing.txt"},
			expectError: true, why: "a file the artifacts do not have"},

		// Cherry-pick a commit of a side branch onto a new branch, then
		// revert it there. The commit adds a file nothing else touches, so
		// it applies whatever the default branch did since.
		{tool: "create_branch", args: map[string]any{"project": p, "branch": pick + "-src", "ref": s.Default}},
		{tool: "create_commit", args: map[string]any{"project": p, "branch": pick + "-src", "message": "A change to pick",
			"actions": []any{map[string]any{"action": "create", "file_path": "pick/" + w + ".txt", "content": "picked\n"}}},
			save: map[string]string{"feature_sha": "sha"}},
		{tool: "create_branch", args: map[string]any{"project": p, "branch": pick, "ref": s.Default}},
		{tool: "cherry_pick_commit", args: map[string]any{"project": p, "commit": "{{feature_sha}}", "branch": pick, "dry_run": true}},
		{tool: "cherry_pick_commit", args: map[string]any{"project": p, "commit": "{{feature_sha}}", "branch": pick,
			"message": "Pick the feature change"}, save: map[string]string{"picked": "sha"}},
		{tool: "revert_commit", args: map[string]any{"project": p, "commit": "{{picked}}", "branch": pick, "dry_run": true}},
		{tool: "revert_commit", args: map[string]any{"project": p, "commit": "{{picked}}", "branch": pick}},
		{tool: "cherry_pick_commit", args: map[string]any{"project": p, "commit": "{{feature_sha}}", "branch": s.Default},
			expectError: true, why: "the default branch takes code only through a merge request"},

		// Rebase the draft merge request.
		{tool: "get_merge_request", args: map[string]any{"project": p, "iid": s.MR2}, save: map[string]string{"mr2_sha": "sha"}},
		{tool: "rebase_merge_request", args: map[string]any{"project": p, "iid": s.MR2, "sha": stale},
			expectError: true, why: "a head that moved"},
		{tool: "rebase_merge_request", args: map[string]any{"project": p, "iid": s.MR2, "sha": "{{mr2_sha}}", "dry_run": true}},
		{tool: "rebase_merge_request", args: map[string]any{"project": p, "iid": s.MR2, "sha": "{{mr2_sha}}", "skip_ci": true}},

		// Move an issue of the run's to its second project.
		{tool: "create_issue", args: map[string]any{"project": p, "title": "An issue to move " + s.Name}, save: map[string]string{"to_move": "iid"}},
		{tool: "get_issue", args: map[string]any{"project": p, "iid": "{{to_move}}"}, save: map[string]string{"to_move_at": "updated_at"}},
		{tool: "move_issue", args: map[string]any{"project": p, "iid": "{{to_move}}", "to_project": p + "-b", "updated_at": "{{to_move_at}}",
			"dry_run": true}},
		{tool: "move_issue", args: map[string]any{"project": p, "iid": "{{to_move}}", "to_project": p + "-b", "updated_at": "{{to_move_at}}"}},

		// A label and a milestone, made, changed and deleted.
		{tool: "create_label", args: map[string]any{"project": p, "name": "live-made-" + w, "color": "#336699", "dry_run": true}},
		{tool: "create_label", args: map[string]any{"project": p, "name": "live-made-" + w, "color": "#336699",
			"description": "A label the live run made", "priority": 3}, save: map[string]string{"label": "label.id", "label_v": "label.version"}},
		{tool: "update_label", args: map[string]any{"project": p, "label_id": "{{label}}", "version": stale, "color": "#993366"},
			expectError: true, why: "a version that moved"},
		{tool: "update_label", args: map[string]any{"project": p, "label_id": "{{label}}", "version": "{{label_v}}", "color": "#993366",
			"dry_run": true}},
		{tool: "update_label", args: map[string]any{"project": p, "label_id": "{{label}}", "version": "{{label_v}}", "name": "live-renamed-" + w,
			"color": "#993366", "description": "Renamed by the live run", "priority": 4}, save: map[string]string{"label_v2": "label.version"}},
		{tool: "update_label", args: map[string]any{"project": p, "label_id": "{{label}}", "version": "{{label_v2}}", "clear_description": true,
			"clear_priority": true}, save: map[string]string{"label_v3": "label.version"}},
		{tool: "delete_label", args: map[string]any{"project": p, "label_id": "{{label}}", "version": "{{label_v3}}", "confirm": true,
			"dry_run": true}},
		{tool: "delete_label", args: map[string]any{"project": p, "label_id": "{{label}}", "version": "{{label_v3}}", "confirm": true},
			declines: true, expectError: true, why: "the person declines the question"},
		{tool: "delete_label", args: map[string]any{"project": p, "label_id": "{{label}}", "version": "{{label_v3}}", "confirm": true}},
		{tool: "create_milestone", args: map[string]any{"project": p, "title": "Live made " + s.Name, "dry_run": true}},
		{tool: "create_milestone", args: map[string]any{"project": p, "title": "Live made " + s.Name, "description": "A milestone the live run made",
			"start_date": "2030-01-01", "due_date": "2030-03-31"}, save: map[string]string{"ms": "milestone.id", "ms_at": "milestone.updated_at"}},
		{tool: "update_milestone", args: map[string]any{"project": p, "milestone_id": "{{ms}}", "updated_at": "{{ms_at}}", "state": "close",
			"dry_run": true}},
		{tool: "update_milestone", args: map[string]any{"project": p, "milestone_id": "{{ms}}", "updated_at": "{{ms_at}}",
			"title": "Live changed " + s.Name, "description": "Changed", "start_date": "2030-02-01", "due_date": "2030-04-30", "state": "close"},
			save: map[string]string{"ms_at2": "milestone.updated_at"}},
		{tool: "update_milestone", args: map[string]any{"project": p, "milestone_id": "{{ms}}", "updated_at": "{{ms_at2}}",
			"clear_description": true, "clear_start_date": true, "clear_due_date": true}, save: map[string]string{"ms_at3": "milestone.updated_at"}},
		{tool: "delete_milestone", args: map[string]any{"project": p, "milestone_id": "{{ms}}", "updated_at": "{{ms_at3}}", "confirm": true,
			"dry_run": true}},
		{tool: "delete_milestone", args: map[string]any{"project": p, "milestone_id": "{{ms}}", "updated_at": "{{ms_at3}}", "confirm": true}},

		// A tag, made and deleted.
		{tool: "create_tag", args: map[string]any{"project": p, "tag_name": "live-tag-" + w, "ref": s.Default, "dry_run": true}},
		{tool: "create_tag", args: map[string]any{"project": p, "tag_name": "live-tag-" + w, "ref": s.Default, "message": "A live tag"},
			save: map[string]string{"tag_sha": "commit_sha"}},
		{tool: "delete_tag", args: map[string]any{"project": p, "tag_name": "live-tag-" + w, "sha": "{{tag_sha}}", "confirm": true, "dry_run": true}},
		{tool: "delete_tag", args: map[string]any{"project": p, "tag_name": "live-tag-" + w, "sha": "{{tag_sha}}", "confirm": true}},
	}
}

// guardRule and guardBranch are a wildcard protected-branch rule and a
// branch under it, which the driver makes before the plan runs.
func guardRule(s scratch) string   { return "guard-" + s.Name[len(s.Name)-6:] + "/*" }
func guardBranch(s scratch) string { return "guard-" + s.Name[len(s.Name)-6:] + "/one" }

func itoa(n int64) string { return fmt.Sprint(n) }

// confined refuses a step that could read outside the scratch project:
// a project other than it, a group search without the run's word, an
// instance-wide search without it, or a link somewhere else that is not
// the one expected refusal. §9.1: the driver reads only what it wrote.
func confined(st step, s scratch) error {
	for k, v := range st.args {
		switch k {
		case "project":
			if !sameProject(v, s) {
				return fmt.Errorf("%s: project %v is not the scratch project", st.tool, v)
			}
		case "group":
			if v != s.Namespace || st.args["search"] != s.Name {
				return fmt.Errorf("%s: a group search must be the run's namespace and carry the run's word", st.tool)
			}
		case "url":
			u, _ := v.(string)
			inside := strings.HasPrefix(u, s.WebURL+"/")
			refusedElsewhere := st.expectError && strings.Contains(u, ".invalid/")
			if !inside && !refusedElsewhere {
				return fmt.Errorf("%s: %q is not inside the scratch project", st.tool, u)
			}
		}
	}
	for _, k := range []string{"assignees", "reviewers", "add_assignees", "remove_assignees", "add_reviewers", "remove_reviewers"} {
		names, _ := st.args[k].([]any)
		for _, n := range names {
			if n != s.User {
				return fmt.Errorf("%s: %s names %v; a run assigns and asks only its own account, so nobody else is notified", st.tool, k, n)
			}
		}
	}
	rule, ok := rules[st.tool]
	if !ok {
		return fmt.Errorf("%s has no confinement rule; add one to rules before the plan calls it", st.tool)
	}
	return rule(st, s)
}

// rule is what one tool must be sent to stay inside the run, beyond the
// argument checks every step gets.
type rule func(st step, s scratch) error

// rules has an entry for every tool the plan may call, and a tool
// without one is refused: a new tool that reads group- or instance-wide
// data cannot slip through by default. TestEveryToolHasARule holds the
// list against the committed surface.
var rules = map[string]rule{
	"get_me":      anything,
	"resolve_url": anything, // its url is checked with every step
	"find_users": func(st step, s scratch) error {
		if st.args["username"] != nil && st.args["username"] != s.User || st.args["search"] != nil && st.args["search"] != s.Name {
			return fmt.Errorf("%s: users are found by the run's own username or the run's word only", st.tool)
		}
		return nil
	},
	"list_members": func(st step, s scratch) error {
		if err := inProject(st, s); err != nil {
			return err
		}
		if st.args["query"] != s.User {
			return fmt.Errorf("%s: members are listed for the run's own account only; the group's are other people", st.tool)
		}
		return nil
	},
	"list_labels": func(st step, s scratch) error {
		if err := inProject(st, s); err != nil {
			return err
		}
		if st.args["project_only"] != true {
			return fmt.Errorf("%s: labels are listed with project_only; the group's labels are not the run's", st.tool)
		}
		return nil
	},
	"list_milestones": func(st step, _ scratch) error {
		if st.args["include_ancestors"] == true {
			return fmt.Errorf("%s: the group's milestones are not the run's", st.tool)
		}
		if st.args["project"] == nil && st.args["group"] == nil {
			return fmt.Errorf("%s: milestones are listed in the scratch project or with the run's word", st.tool)
		}
		return nil
	},
	"mark_todos_done": func(st step, _ scratch) error {
		ids, _ := st.args["ids"].([]any)
		for _, id := range ids {
			if name, ok := id.(string); !ok || !strings.HasPrefix(name, "{{todo") {
				return fmt.Errorf("%s: ids must be saved from list_todos in the scratch project or from add_todo, not written into the plan", st.tool)
			}
		}
		return nil
	},
	"move_issue": func(st step, s scratch) error {
		if st.args["to_project"] != s.Path+"-b" {
			return fmt.Errorf("%s: an issue moves only to the run's second project", st.tool)
		}
		return inProject(st, s)
	},
	"link_issues":           linkRule,
	"unlink_issues":         linkRule,
	"search_projects":       searchRule,
	"search_issues":         searchRule,
	"search_merge_requests": searchRule,
	"search":                searchRule,
}

func init() {
	for _, tool := range []string{"get_project", "get_issue", "list_discussions", "get_merge_request", "list_mr_files",
		"get_mr_diff", "list_mr_commits", "list_mr_versions", "compare_mr_versions", "list_review_comments", "get_file", "list_tree", "list_branches", "list_commits",
		"get_commit", "compare_refs", "list_tags", "list_pipelines", "get_pipeline", "list_jobs", "get_job_log", "get_test_report",
		"lint_ci", "list_item_events", "list_boards", "list_todos", "add_todo", "subscribe", "react", "create_issue", "update_issue", "add_comment", "update_comment", "resolve_discussion", "add_review_comment",
		"delete_review_comment", "update_review_comment", "publish_review_comment", "submit_review", "create_merge_request", "update_merge_request", "track_time", "create_branch",
		"create_commit",
		"merge_merge_request", "cancel_auto_merge", "approve_merge_request", "unapprove_merge_request", "apply_suggestions",
		"run_pipeline", "run_merge_request_pipeline",
		"retry_pipeline", "retry_job", "play_job", "cancel_pipeline", "delete_branch", "delete_comment", "list_wiki_pages", "get_wiki_page", "save_wiki_page",
		"delete_wiki_page", "list_releases", "get_release", "create_release", "list_environments", "list_deployments",
		// Without a project these read or write the maintainer's own
		// snippets and activity, which are not the run's.
		"list_snippets", "get_snippet", "create_snippet", "update_snippet", "delete_snippet", "list_events",
		"get_blame", "list_job_artifacts", "get_job_artifact", "cherry_pick_commit", "revert_commit", "rebase_merge_request",
		"create_label", "update_label", "delete_label", "create_milestone", "update_milestone", "delete_milestone",
		"create_tag", "delete_tag"} {
		rules[tool] = inProject
	}
}

// linkRule: both issues are the run's.
func linkRule(st step, s scratch) error {
	if t, ok := st.args["target_project"]; ok && t != s.Path {
		return fmt.Errorf("%s: the other issue is the scratch project's", st.tool)
	}
	return inProject(st, s)
}

func anything(step, scratch) error { return nil }

// inProject: the step names the scratch project, which every step's
// argument check holds it to.
func inProject(st step, _ scratch) error {
	if st.args["project"] == nil {
		return fmt.Errorf("%s: this tool reads the scratch project only, and the step names none", st.tool)
	}
	return nil
}

// searchRule: a search is scoped to the scratch project or the run's
// namespace, or carries the run's word.
func searchRule(st step, s scratch) error {
	scoped := st.args["project"] != nil || st.args["group"] != nil
	if !scoped && st.args["search"] != s.Name {
		return fmt.Errorf("%s: an instance-wide search must carry the run's word", st.tool)
	}
	return nil
}

func sameProject(v any, s scratch) bool {
	switch x := v.(type) {
	case string:
		return x == s.Path || x == s.WebURL
	case int64:
		return x == s.ID
	case int:
		return int64(x) == s.ID
	case float64:
		return int64(x) == s.ID
	}
	return false
}

// fill replaces each "{{name}}" string in args, at any depth, by the
// value saved under name. It names the first placeholder with no value.
func fill(args map[string]any, saved map[string]any) (map[string]any, string) {
	missing := ""
	var walk func(v any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			if name, ok := strings.CutPrefix(x, "{{"); ok && strings.HasSuffix(name, "}}") {
				name = strings.TrimSuffix(name, "}}")
				val, found := saved[name]
				if !found && missing == "" {
					missing = name
				}
				return val
			}
			return x
		case map[string]any:
			out := make(map[string]any, len(x))
			for k, e := range x {
				out[k] = walk(e)
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, e := range x {
				out[i] = walk(e)
			}
			return out
		}
		return v
	}
	return walk(args).(map[string]any), missing
}

// save stores the values a step names, read by dotted path from its
// structured result; a numeric segment indexes an array.
func save(paths map[string]string, structured json.RawMessage, saved map[string]any) {
	if len(paths) == 0 {
		return
	}
	for name, path := range paths {
		if v, ok := valueAt(structured, path); ok {
			saved[name] = v
		}
	}
}

// valueAt is the value at a dotted path in a result, false when there is
// none.
func valueAt(structured json.RawMessage, path string) (any, bool) {
	var v any
	if json.Unmarshal(structured, &v) != nil {
		return nil, false
	}
	for seg := range strings.SplitSeq(path, ".") {
		switch x := v.(type) {
		case map[string]any:
			v = x[seg]
		case []any:
			if i, err := strconv.Atoi(seg); err == nil && i < len(x) {
				v = x[i]
			} else {
				v = nil
			}
		default:
			v = nil
		}
	}
	return v, v != nil
}

// learnIDs registers every numeric id of 1,000 or more a result carries
// under an id-named key, so the transcript masks what the run created:
// notes, drafts, commits' projects, to-do items (§9.1).
func learnIDs(red *redact.Redactor, structured json.RawMessage) {
	var root any
	if json.Unmarshal(structured, &root) != nil {
		return
	}
	var walk func(key string, v any)
	walk = func(key string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				walk(k, e)
			}
		case []any:
			for _, e := range x {
				walk(key, e)
			}
		case float64:
			if (key == "id" || key == "ids" || strings.HasSuffix(key, "_id")) && x >= 1000 && x == float64(int64(x)) {
				red.Known(redact.KindID, strconv.FormatInt(int64(x), 10))
			}
		}
	}
	walk("", root)
}

// nextPageToken reads listing.next_page_token from a structured result.
func nextPageToken(structured json.RawMessage) string {
	var r struct {
		Listing struct {
			NextPageToken *string `json:"next_page_token"`
		} `json:"listing"`
	}
	if json.Unmarshal(structured, &r) != nil || r.Listing.NextPageToken == nil {
		return ""
	}
	return *r.Listing.NextPageToken
}

// ------------------------------------------------------------ the record

// recorder counts what the run sent, per tool and per option, at the
// one place calls go out. It is what testdata/live-cover-record.tsv is
// written from, so a step that exists and never ran is not counted.
type recorder struct {
	sent map[string]int
}

func newRecorder() *recorder { return &recorder{sent: map[string]int{}} }

// Sent records one call: the tool as "tool.*", and each option given.
func (r *recorder) Sent(tool string, args map[string]any) {
	r.sent[tool+".*"]++
	for name := range args {
		r.sent[tool+"."+name]++
	}
}

// missing is every tool and option the surface has and this run did not
// send, sorted.
func (r *recorder) missing(surface map[string][]string) []string {
	var out []string
	for _, tool := range slices.Sorted(maps.Keys(surface)) {
		if r.sent[tool+".*"] == 0 {
			out = append(out, tool+".*")
		}
		for _, o := range surface[tool] {
			if r.sent[tool+"."+o] == 0 {
				out = append(out, tool+"."+o)
			}
		}
	}
	return out
}

// write replaces the record through a temporary file, so a run killed
// while writing leaves the last record whole. It holds names of tools
// and options and counts, nothing the instance returned.
func (r *recorder) write(path, header string) error {
	var b strings.Builder
	b.WriteString("# Written by scripts/livegitlab at the end of a run; do not edit.\n")
	for line := range strings.SplitSeq(strings.TrimSpace(header), "\n") {
		b.WriteString("# " + line + "\n")
	}
	b.WriteString("# Columns: tool.option (tool.* for the call itself), times sent.\n")
	for _, k := range slices.Sorted(maps.Keys(r.sent)) {
		fmt.Fprintf(&b, "%s\t%d\n", k, r.sent[k])
	}
	return gatekit.WriteFileAtomic(path, []byte(b.String()))
}
