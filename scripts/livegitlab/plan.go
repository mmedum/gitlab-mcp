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

	"github.com/mmedum/gitlab-mcp/scripts/internal/gatekit"
	"github.com/mmedum/gitlab-mcp/scripts/internal/redact"
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
		{tool: "get_commit", args: map[string]any{"project": p, "sha": s.Feature, "file_offset": 1, "message_offset": 7}},
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
		{tool: "get_mr_diff", args: map[string]any{"project": p, "iid": s.MR, "paths": []any{"missing/file.md"}},
			expectError: true, why: "a path the merge request does not change"},
		{tool: "list_mr_commits", args: map[string]any{"project": p, "iid": s.MR, "max": 1}, paged: true},
		{tool: "list_review_comments", args: map[string]any{"project": p, "iid": s.MR}, paged: true},

		{tool: "compare_refs", args: map[string]any{"project": p, "from": s.Default, "to": s.Feature}},
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
		{tool: "lint_ci", args: map[string]any{"project": p, "ref": s.Default, "simulate": true, "include_jobs": true, "offset": 10}},
		{tool: "lint_ci", args: map[string]any{"project": p}},

		{tool: "list_labels", args: map[string]any{"project": p, "search": "live-", "project_only": true, "with_counts": true,
			"max": 1}, paged: true},
		{tool: "list_milestones", args: map[string]any{"project": p, "include_ancestors": false, "max": 1}, paged: true},
		{tool: "list_milestones", args: map[string]any{"project": p, "state": "active", "search": "live", "title": s.Milestone}},
		{tool: "list_milestones", args: map[string]any{"group": s.Namespace, "search": s.Name}},
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
			"confidential": false, "state": "close"},
			save: map[string]string{"issue_at2": "updated_at"}},
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
			save: map[string]string{"issue_thread": "discussion_id"}},
		{tool: "add_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "body": "A reply the live run wrote.",
			"discussion_id": "{{issue_thread}}"}},
		{tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "body": "Done.\n\n/merge"},
			expectError: true, why: "a quick-action line in the body, without escape_commands"},
		{tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "body": "Escaped.\n\n/merge",
			"escape_commands": true}},
		{tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "body": "On an added line.",
			"file": s.File, "line": 3, "side": "new"}, save: map[string]string{"mr_thread": "discussion_id"}},
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

		// To-do items: the run's own, found in the scratch project.
		{tool: "list_todos", args: map[string]any{"project": p, "state": "pending"}, save: map[string]string{"todo": "todos.0.id"}},
		{tool: "mark_todos_done", args: map[string]any{"ids": []any{"{{todo}}"}, "dry_run": true}},
		{tool: "mark_todos_done", args: map[string]any{"ids": []any{"{{todo}}"}}},

		{tool: "lint_ci", args: map[string]any{"project": p, "content": "check:\n  script:\n    - echo live\n", "include_jobs": true}},
		{tool: "lint_ci", args: map[string]any{"project": p, "content": "include:\n  - local: other.yml\n"}, expectError: true,
			why: "supplied content with an include, which GitLab would fetch"},
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
				return fmt.Errorf("%s: ids must be saved from list_todos in the scratch project, not written into the plan", st.tool)
			}
		}
		return nil
	},
	"search_projects":       searchRule,
	"search_issues":         searchRule,
	"search_merge_requests": searchRule,
	"search":                searchRule,
}

func init() {
	for _, tool := range []string{"get_project", "get_issue", "list_discussions", "get_merge_request", "list_mr_files",
		"get_mr_diff", "list_mr_commits", "list_review_comments", "get_file", "list_tree", "list_branches", "list_commits",
		"get_commit", "compare_refs", "list_tags", "list_pipelines", "get_pipeline", "list_jobs", "get_job_log", "lint_ci",
		"list_todos", "create_issue", "update_issue", "add_comment", "resolve_discussion", "add_review_comment",
		"delete_review_comment", "submit_review", "create_merge_request", "update_merge_request", "create_branch",
		"create_commit"} {
		rules[tool] = inProject
	}
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
	var root any
	if json.Unmarshal(structured, &root) != nil {
		return
	}
	for name, path := range paths {
		v := root
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
		if v != nil {
			saved[name] = v
		}
	}
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
