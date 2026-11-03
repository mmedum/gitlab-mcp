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

	"github.com/mmedum/gitlab-mcp/v4/scripts/internal/gatekit"
	"github.com/mmedum/gitlab-mcp/v4/scripts/internal/redact"
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
	// Images: a generated PNG and a text file in the directory the
	// server is started with in GITLAB_MCP_UPLOAD_DIRS, and a PNG in a
	// directory it is not.
	Image, NotImage, Outside string
}

// step is one tool call and what it should do. A refusal that is
// expected proves as much as a result.
type step struct {
	// name says what the step checks. A comment cites the step by it, and
	// the transcript prints it above the call; keep it stable.
	name        string
	tool        string
	args        map[string]any
	expectError bool
	// why is a fact the name leaves out: why GitLab or the server refuses
	// the step, or what decides an anyOutcome step.
	why string
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
	return slices.Concat(readItemsAndCode(s), readReviewCIAndPlanning(s), writePath(s))
}

// readItemsAndCode reads links, projects, issues, merge requests and
// their threads, and the repository: files, the tree, branches and
// commits.
func readItemsAndCode(s scratch) []step {
	p := s.Path
	yesterday := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	tomorrow := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	return []step{
		{name: "the signed-in account and its token are read", tool: "get_me", args: map[string]any{}},
		{name: "an issue link resolves", tool: "resolve_url", args: map[string]any{"url": s.WebURL + "/-/issues/" + itoa(s.Issue)}},
		{name: "an MR link resolves", tool: "resolve_url", args: map[string]any{"url": s.WebURL + "/-/merge_requests/" + itoa(s.MR)}},
		{name: "a file link with a line resolves", tool: "resolve_url", args: map[string]any{"url": s.WebURL + "/-/blob/" + s.Default + "/" + s.File + "#L2"}},
		{name: "a link to another host is refused", tool: "resolve_url", args: map[string]any{"url": "https://elsewhere.invalid/example-group/project/-/issues/1"},
			expectError: true},

		{name: "owned private projects are found by name", tool: "search_projects", args: map[string]any{"search": s.Name, "scope": "owned", "visibility": "private",
			"include_archived": false, "max": 5}},
		{name: "a group's projects page one at a time", tool: "search_projects", args: map[string]any{"group": s.Namespace, "include_subgroups": true, "search": s.Name,
			"scope": "member", "max": 1}, paged: true},
		{name: "a project is read by its path", tool: "get_project", args: map[string]any{"project": p}},
		{name: "a project is read by its numeric id", tool: "get_project", args: map[string]any{"project": s.ID}},
		{name: "a project is read by its URL", tool: "get_project", args: map[string]any{"project": s.WebURL}},
		{name: "a project is read from an offset", tool: "get_project", args: map[string]any{"project": s.ID, "offset": 10}},

		{name: "a project's issues are filtered and paged", tool: "search_issues", args: map[string]any{"project": p, "state": "opened", "labels": []any{s.Label},
			"author": s.User, "search": s.Name, "order_by": "created_at", "sort": "asc", "max": 1,
			"created_after": yesterday, "created_before": tomorrow, "updated_after": yesterday, "updated_before": tomorrow}, paged: true},
		{name: "a group's issues are searched by the run's word", tool: "search_issues", args: map[string]any{"group": s.Namespace, "search": s.Name, "scope": "created_by_me", "state": "all"}},
		{name: "issues are searched instance-wide by the run's word", tool: "search_issues", args: map[string]any{"search": s.Name, "scope": "all", "assignee": s.User, "milestone": "None"}},
		{name: "an issue is read with its links", tool: "get_issue", args: map[string]any{"project": p, "iid": s.Issue}},
		{name: "an issue is read from an offset", tool: "get_issue", args: map[string]any{"project": p, "iid": s.Issue, "offset": 10}},
		{name: "an issue that does not exist is refused", tool: "get_issue", args: map[string]any{"project": p, "iid": 99999}, expectError: true},
		{name: "an issue's threads page with system notes", tool: "list_discussions", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "include_system": true,
			"max": 1}, paged: true},
		{name: "one comment's thread is found by its note id", tool: "list_discussions", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "note_id": s.Note, "offset": 0}},
		{name: "an MR's unresolved threads are listed", tool: "list_discussions", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "unresolved_only": true}},

		{name: "a project's MRs are filtered", tool: "search_merge_requests", args: map[string]any{"project": p, "state": "opened", "source_branch": s.Feature,
			"target_branch": s.Default, "draft": false, "author": s.User, "search": s.Name, "order_by": "updated_at", "sort": "desc",
			"created_after": yesterday, "created_before": tomorrow, "updated_after": yesterday, "updated_before": tomorrow}},
		{name: "a group's MRs page by the run's word", tool: "search_merge_requests", args: map[string]any{"group": s.Namespace, "search": s.Name, "scope": "created_by_me",
			"state": "all", "max": 1}, paged: true},
		{name: "MRs are searched instance-wide by the run's word", tool: "search_merge_requests", args: map[string]any{"search": s.Name, "scope": "all", "reviewer": s.User,
			"assignee": s.User, "labels": []any{s.Label}, "milestone": "None"}},
		{name: "an MR is read with its links", tool: "get_merge_request", args: map[string]any{"project": p, "iid": s.MR}},
		{name: "a draft MR is read from an offset", tool: "get_merge_request", args: map[string]any{"project": p, "iid": s.MR2, "offset": 5}},

		{name: "a file is read", tool: "get_file", args: map[string]any{"project": p, "path": s.File}},
		{name: "a file is read on a branch from an offset", tool: "get_file", args: map[string]any{"project": p, "path": s.File, "ref": s.Feature, "offset": 3}},
		{name: "a file that does not exist is refused", tool: "get_file", args: map[string]any{"project": p, "path": "missing/file.md"}, expectError: true},
		{name: "the tree pages recursively", tool: "list_tree", args: map[string]any{"project": p, "ref": s.Default, "recursive": true, "max": 1}, paged: true},
		{name: "one directory of the tree is listed", tool: "list_tree", args: map[string]any{"project": p, "path": "notes"}},
		{name: "branches page one at a time", tool: "list_branches", args: map[string]any{"project": p, "max": 1}, paged: true},
		{name: "branches are searched by name", tool: "list_branches", args: map[string]any{"project": p, "search": "feature"}},
		{name: "a branch's commits page between two dates", tool: "list_commits", args: map[string]any{"project": p, "ref": s.Feature, "max": 1, "first_parent": true,
			"since": yesterday, "until": tomorrow}, paged: true},
		{name: "a file's commits are listed by author", tool: "list_commits", args: map[string]any{"project": p, "path": s.File, "author": s.User}},
		{name: "a commit is read by its sha", tool: "get_commit", args: map[string]any{"project": p, "sha": s.SHA}},
		// A branch name, resolved to its commit, whose merge request is the
		// scratch one; the offset reads after it skip the links.
		{name: "a commit is read by a branch name, with its MR", tool: "get_commit", args: map[string]any{"project": p, "sha": s.Feature}},
		{name: "a commit's message and files are read from offsets", tool: "get_commit", args: map[string]any{"project": p, "sha": s.Feature, "file_offset": 1, "message_offset": 7}},
		{name: "a commit's diff is read from an offset", tool: "get_commit", args: map[string]any{"project": p, "sha": s.Feature, "file_offset": 0, "diff_offset": 5}},
	}
}

// readReviewCIAndPlanning is the rest of reading: members and users, a
// merge request's review surface, comparisons and tags, pipelines, jobs
// and CI lint, planning, to-do items and search.
func readReviewCIAndPlanning(s scratch) []step {
	p := s.Path
	yesterday := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	tomorrow := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	return []step{
		{name: "a compare link resolves", tool: "resolve_url", args: map[string]any{"url": s.WebURL + "/-/compare/" + s.Default + "..." + s.Feature}},
		{name: "a pipeline link resolves", tool: "resolve_url", args: map[string]any{"url": s.WebURL + "/-/pipelines/" + itoa(s.Pipeline)}},
		{name: "a job link resolves", tool: "resolve_url", args: map[string]any{"url": s.WebURL + "/-/jobs/" + itoa(s.JobFailed)}},

		{name: "the run's account is found among the members", tool: "list_members", args: map[string]any{"project": p, "query": s.User, "max": 1}, paged: true},
		{name: "a user is found by username", tool: "find_users", args: map[string]any{"username": s.User, "max": 1}},
		{name: "users are searched by the run's word", tool: "find_users", args: map[string]any{"search": s.Name, "max": 5}, paged: true},

		{name: "an MR's files page one at a time", tool: "list_mr_files", args: map[string]any{"project": p, "iid": s.MR, "max": 1}, paged: true},
		{name: "an MR's diff is read", tool: "get_mr_diff", args: map[string]any{"project": p, "iid": s.MR}},
		{name: "an MR's diff is read for one path", tool: "get_mr_diff", args: map[string]any{"project": p, "iid": s.MR, "paths": []any{s.File}, "file_offset": 0}},
		{name: "an MR's diff is read from a file offset", tool: "get_mr_diff", args: map[string]any{"project": p, "iid": s.MR, "file_offset": 1}},
		{name: "an MR's diff is read from a diff offset", tool: "get_mr_diff", args: map[string]any{"project": p, "iid": s.MR, "diff_offset": 5}},
		{name: "a diff for a path the MR does not change is refused", tool: "get_mr_diff", args: map[string]any{"project": p, "iid": s.MR, "paths": []any{"missing/file.md"}},
			expectError: true},
		{name: "an MR's commits page one at a time", tool: "list_mr_commits", args: map[string]any{"project": p, "iid": s.MR, "max": 1}, paged: true},
		{name: "review drafts page past the text budget", tool: "list_review_comments", args: map[string]any{"project": p, "iid": s.MR}, paged: true},

		{name: "two refs compare", tool: "compare_refs", args: map[string]any{"project": p, "from": s.Default, "to": s.Feature}},
		{name: "a comparison's diff is read from an offset", tool: "compare_refs", args: map[string]any{"project": p, "from": s.Default, "to": s.Feature, "diff_offset": 5}},
		{name: "a straight comparison is read from commit and file offsets", tool: "compare_refs", args: map[string]any{"project": p, "from": s.Default, "to": s.Feature, "straight": true,
			"commit_offset": 1, "file_offset": 1}},
		{name: "tags page in version order", tool: "list_tags", args: map[string]any{"project": p, "search": "^v0", "order_by": "version", "sort": "asc", "max": 1},
			paged: true},

		{name: "pipelines page one at a time", tool: "list_pipelines", args: map[string]any{"project": p, "max": 1}, paged: true},
		{name: "pipelines are filtered by ref, sha, status and source", tool: "list_pipelines", args: map[string]any{"project": p, "ref": s.Default, "sha": s.SHA, "status": "failed",
			"source": "push", "username": s.User, "updated_after": yesterday, "updated_before": tomorrow, "order_by": "id",
			"sort": "desc"}},
		{name: "a failed pipeline is read with its child pipeline", tool: "get_pipeline", args: map[string]any{"project": p, "pipeline_id": s.Pipeline}},
		{name: "a pipeline's failed jobs page one at a time", tool: "list_jobs", args: map[string]any{"project": p, "pipeline_id": s.Pipeline, "scope": "failed",
			"include_retried": true, "max": 1}, paged: true},
		{name: "a failed job's log is read", tool: "get_job_log", args: map[string]any{"project": p, "job_id": s.JobFailed}},
		{name: "a failed job's log is read for its failure", tool: "get_job_log", args: map[string]any{"project": p, "job_id": s.JobFailed, "failed_only": true}},
		{name: "a job log is read in a byte window", tool: "get_job_log", args: map[string]any{"project": p, "job_id": s.JobFailed, "byte_offset": 0, "byte_limit": 2000}},
		{name: "a passed job's log is read", tool: "get_job_log", args: map[string]any{"project": p, "job_id": s.JobPassed}},
		{name: "a pipeline's test report is read", tool: "get_test_report", args: map[string]any{"project": p, "pipeline_id": s.Pipeline}},
		{name: "a test report is read from an offset", tool: "get_test_report", args: map[string]any{"project": p, "pipeline_id": s.Pipeline, "offset": 1}},
		{name: "a test report offset past its end is refused", tool: "get_test_report", args: map[string]any{"project": p, "pipeline_id": s.Pipeline, "offset": 5}, expectError: true,
			why: "the report has one failed case, so offset 5 is past its end"},
		{name: "the project's CI lints as a simulated run", tool: "lint_ci", args: map[string]any{"project": p, "ref": s.Default, "simulate": true, "include_jobs": true, "offset": 10}},
		{name: "the project's CI lints", tool: "lint_ci", args: map[string]any{"project": p}},

		{name: "the project's labels page with their counts", tool: "list_labels", args: map[string]any{"project": p, "search": "live-", "project_only": true, "with_counts": true,
			"max": 1}, paged: true},
		{name: "milestones page one at a time", tool: "list_milestones", args: map[string]any{"project": p, "include_ancestors": false, "max": 1}, paged: true},
		{name: "a milestone is found by its title", tool: "list_milestones", args: map[string]any{"project": p, "state": "active", "search": "live", "title": s.Milestone}},
		{name: "a group's milestones are searched by the run's word", tool: "list_milestones", args: map[string]any{"group": s.Namespace, "search": s.Name}},
		// Two seeded boards, the first with a list for each label.
		{name: "boards page with their lists", tool: "list_boards", args: map[string]any{"project": p, "max": 1}, paged: true},
		{name: "to-do items page one at a time", tool: "list_todos", args: map[string]any{"project": p, "max": 1}, paged: true},
		{name: "to-do items are filtered by state, action and type", tool: "list_todos", args: map[string]any{"project": p, "state": "pending", "action": "marked", "type": "Issue"}},

		{name: "search pages through the run's issues", tool: "search", args: map[string]any{"scope": "issues", "search": s.Name, "project": p, "state": "opened", "max": 1},
			paged: true},
		{name: "search finds the run's MRs", tool: "search", args: map[string]any{"scope": "merge_requests", "search": s.Name, "project": p}},
		{name: "search finds code on a branch", tool: "search", args: map[string]any{"scope": "blobs", "search": s.Name, "project": p, "ref": s.Feature}},
		// A match by file name, whose row carries the blob's SHA as its id,
		// not a number.
		{name: "search finds a file by its name", tool: "search", args: map[string]any{"scope": "blobs", "search": "main.txt", "project": p}},
		{name: "search finds commits", tool: "search", args: map[string]any{"scope": "commits", "search": "live file", "project": p}},
		{name: "search finds comments", tool: "search", args: map[string]any{"scope": "notes", "search": "synthetic comment", "project": p}},
		{name: "search finds milestones", tool: "search", args: map[string]any{"scope": "milestones", "search": "live milestone", "project": p}},
		{name: "search finds wiki pages", tool: "search", args: map[string]any{"scope": "wiki_blobs", "search": s.Name, "project": p}},
		{name: "search finds users in the project", tool: "search", args: map[string]any{"scope": "users", "search": s.Name, "project": p}},
		{name: "search finds issues across a group", tool: "search", args: map[string]any{"scope": "issues", "search": s.Name, "group": s.Namespace}},
		{name: "code is searched across a group", tool: "search", args: map[string]any{"scope": "blobs", "search": s.Name, "group": s.Namespace}, anyOutcome: true,
			why: "code across a group needs advanced search, which the group's tier decides"},
		{name: "search finds the run's project", tool: "search", args: map[string]any{"scope": "projects", "search": s.Name}},
	}
}

// writePath is the write path. Every write goes to the scratch
// project, names only the run's own account, and is shown first as a dry
// run where the tool takes one. Inline comments land on notes/live.md of
// the run's merge request, whose diff is one hunk:
//
//	@@ -1,4 +1,4 @@
//	 # Live <name>
//
//	-Line two.
//	-Line three.
//	+Line two, changed.
//	+Line three, tidied.
func writePath(s scratch) []step {
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
		{name: "an issue create is previewed", tool: "create_issue", args: map[string]any{"project": p, "title": "Written issue " + s.Name, "dry_run": true}},
		{name: "an issue with a quick action is refused", tool: "create_issue", args: map[string]any{"project": p, "title": "Refused issue " + s.Name, "description": "Text.\n\n/close"},
			expectError: true},
		{name: "an issue is created with every field", tool: "create_issue", args: map[string]any{"project": p, "title": "Written issue " + s.Name,
			"description": "A synthetic issue the live run wrote.\n\n/close\n", "escape_commands": true, "labels": []any{s.Label},
			"assignees": me, "milestone": s.Milestone, "due_date": "2030-01-31", "confidential": true},
			save: map[string]string{"issue": "iid", "issue_at": "updated_at"}},
		{name: "an issue with a label the project lacks is refused", tool: "create_issue", args: map[string]any{"project": p, "title": "x", "labels": []any{"no-such-label-" + s.Name}},
			expectError: true},
		{name: "an issue update is previewed", tool: "update_issue", args: map[string]any{"project": p, "iid": "{{issue}}", "updated_at": "{{issue_at}}", "state": "close",
			"dry_run": true}},
		{name: "an issue is changed and closed", tool: "update_issue", args: map[string]any{"project": p, "iid": "{{issue}}", "updated_at": "{{issue_at}}",
			"title": "Written issue, retitled " + s.Name, "description": "Replaced by the live run.\n\n/label ~x\n", "escape_commands": true,
			"add_labels": []any{s.Label2}, "remove_labels": []any{s.Label}, "milestone": milestone2, "due_date": "2030-02-28",
			"state": "close"},
			save: map[string]string{"issue_at2": "updated_at"}},
		{name: "update_issue refuses to make an issue public", tool: "update_issue", args: map[string]any{"project": p, "iid": "{{issue}}", "updated_at": "{{issue_at2}}", "confidential": false},
			expectError: true, why: "update_issue making a confidential issue public, which only make_issue_public does"},
		{name: "an issue update with a stale witness is refused", tool: "update_issue", args: map[string]any{"project": p, "iid": "{{issue}}", "updated_at": "{{issue_at}}", "title": "Stale"},
			expectError: true},
		{name: "a closed issue reopens", tool: "update_issue", args: map[string]any{"project": p, "iid": "{{issue}}", "updated_at": "{{issue_at2}}", "state": "reopen",
			"clear_milestone": true, "clear_due_date": true, "remove_assignees": me},
			save: map[string]string{"issue_at3": "updated_at"}},
		{name: "an assignee is added to an issue", tool: "update_issue", args: map[string]any{"project": p, "iid": "{{issue}}", "updated_at": "{{issue_at3}}", "add_assignees": me}},

		// Comments and threads, and where an inline comment lands, which the
		// spike "whether diffpos positions land on GitLab's lines" asks too.
		{name: "an issue comment is previewed", tool: "add_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "body": "A comment the live run wrote.",
			"dry_run": true}},
		{name: "a comment is added to an issue", tool: "add_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "body": "A comment the live run wrote."},
			save: map[string]string{"issue_note": "note_id", "issue_note_at": "updated_at"}},
		// The comment, edited in place before the reply: a reply makes it
		// a thread, which moves its updated_at.
		{name: "a comment edit is previewed", tool: "update_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "note_id": "{{issue_note}}",
			"updated_at": "{{issue_note_at}}", "body": "A comment the live run wrote, then edited.", "dry_run": true}},
		{name: "a comment edit with a stale witness is refused", tool: "update_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "note_id": "{{issue_note}}",
			"updated_at": "2020-01-01T00:00:00Z", "body": "Stale."}, expectError: true},
		{name: "a comment edit with a quick action is refused", tool: "update_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "note_id": "{{issue_note}}",
			"updated_at": "{{issue_note_at}}", "body": "Done.\n\n/close"},
			expectError: true},
		{name: "a comment edit keeps an escaped quick action as text", tool: "update_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "note_id": "{{issue_note}}",
			"updated_at": "{{issue_note_at}}", "body": "A comment the live run wrote, then edited.\n\n/close stays text.",
			"escape_commands": true}},
		{name: "a comment edit with the witness it replaced is refused", tool: "update_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "note_id": "{{issue_note}}",
			"updated_at": "{{issue_note_at}}", "body": "Again."}, expectError: true},
		// A standalone comment does not name its thread; the newest one is it.
		{name: "an issue's newest thread is read", tool: "list_discussions", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue},
			save: map[string]string{"issue_thread": "threads.0.id"}},
		{name: "a reply joins an issue thread", tool: "add_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "body": "A reply the live run wrote.",
			"discussion_id": "{{issue_thread}}"}},
		{name: "an MR comment with a quick action is refused", tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "body": "Done.\n\n/merge"},
			expectError: true},
		{name: "an MR comment keeps an escaped quick action as text", tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "body": "Escaped.\n\n/merge",
			"escape_commands": true}},
		{name: "an inline comment lands on an added line", tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "body": "On an added line.",
			"file": s.File, "line": 3, "side": "new"}, save: map[string]string{"mr_thread": "discussion_id", "mr_note": "note_id", "mr_note_at": "updated_at"}},
		// Turned into a suggestion, in its thread on its line.
		{name: "an inline comment becomes a suggestion", tool: "update_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "note_id": "{{mr_note}}",
			"updated_at": "{{mr_note_at}}", "body": "On an added line, as a suggestion:\n\n```suggestion:-0+0\nsuggested by the live run\n```"}},
		// Still the thread it was, on its line, with the new text.
		{name: "an edited inline comment stays on its line", tool: "list_discussions", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR}},
		{name: "an inline comment lands on a removed line", tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "body": "On a removed line.",
			"file": s.File, "line": 3, "side": "old"}},
		{name: "an inline comment lands on an unchanged line", tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "body": "On an unchanged line.",
			"file": s.File, "line": 1, "side": "new"}},
		{name: "an inline comment lands on a range", tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "body": "On a range.",
			"file": s.File, "line": 1, "end_line": 4, "side": "new"}},
		{name: "an inline comment outside every hunk is refused", tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "body": "Outside.",
			"file": s.File, "line": 40, "side": "new"}, expectError: true},
		{name: "a thread resolve is previewed", tool: "resolve_discussion", args: map[string]any{"project": p, "iid": s.MR, "discussion_id": "{{mr_thread}}", "dry_run": true}},
		{name: "an MR thread resolves", tool: "resolve_discussion", args: map[string]any{"project": p, "iid": s.MR, "discussion_id": "{{mr_thread}}"}},
		{name: "a resolved MR thread reopens", tool: "resolve_discussion", args: map[string]any{"project": p, "iid": s.MR, "discussion_id": "{{mr_thread}}", "reopen": true}},
		// A resolvable thread on an issue, resolved and reopened.
		{name: "a resolvable thread starts on an issue", tool: "add_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "body": "A thread the live run started.",
			"thread": true}, save: map[string]string{"issue_new_thread": "discussion_id"}},
		{name: "an issue thread resolves", tool: "resolve_discussion", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue,
			"discussion_id": "{{issue_new_thread}}"}},
		{name: "a resolved issue thread reopens", tool: "resolve_discussion", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue,
			"discussion_id": "{{issue_new_thread}}", "reopen": true}},
		{name: "a general thread starts on an MR", tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "body": "A general thread.",
			"thread": true}, save: map[string]string{"mr_thread2": "discussion_id"}},

		// A review: drafts, one deleted, then submitted.
		{name: "a review draft is previewed", tool: "add_review_comment", args: map[string]any{"project": p, "iid": s.MR, "body": "A draft on a range.", "file": s.File,
			"line": 3, "end_line": 4, "side": "old", "dry_run": true}},
		{name: "a review draft lands on a range", tool: "add_review_comment", args: map[string]any{"project": p, "iid": s.MR, "body": "A draft on a range.", "file": s.File,
			"line": 3, "end_line": 4, "side": "old"}},
		{name: "a draft reply that resolves its thread is added", tool: "add_review_comment", args: map[string]any{"project": p, "iid": s.MR, "body": "A draft reply that resolves.",
			"discussion_id": "{{mr_thread}}", "resolve_thread": true}},
		{name: "a draft keeps an escaped quick action as text", tool: "add_review_comment", args: map[string]any{"project": p, "iid": s.MR, "body": "A draft to delete.\n/approve",
			"escape_commands": true}, save: map[string]string{"draft": "note_id"}},
		{name: "a draft delete is previewed", tool: "delete_review_comment", args: map[string]any{"project": p, "iid": s.MR, "draft_id": "{{draft}}", "dry_run": true}},
		{name: "a draft is deleted", tool: "delete_review_comment", args: map[string]any{"project": p, "iid": s.MR, "draft_id": "{{draft}}"}},
		// One draft edited on its line, then published on its own.
		{name: "a draft to edit is added on a line", tool: "add_review_comment", args: map[string]any{"project": p, "iid": s.MR, "body": "A draft to edit.", "file": s.File,
			"line": 3, "side": "new"}, save: map[string]string{"edit_draft": "note_id", "edit_sha": "note_sha256"}},
		{name: "a draft edit is previewed", tool: "update_review_comment", args: map[string]any{"project": p, "iid": s.MR, "draft_id": "{{edit_draft}}",
			"note_sha256": "{{edit_sha}}", "body": "A draft the live run edited.", "dry_run": true}},
		{name: "a draft edit with another text's witness is refused", tool: "update_review_comment", args: map[string]any{"project": p, "iid": s.MR, "draft_id": "{{edit_draft}}",
			"note_sha256": strings.Repeat("0", 64), "body": "Stale."}, expectError: true},
		{name: "a draft edit with a quick action is refused", tool: "update_review_comment", args: map[string]any{"project": p, "iid": s.MR, "draft_id": "{{edit_draft}}",
			"note_sha256": "{{edit_sha}}", "body": "Done.\n\n/approve"},
			expectError: true},
		{name: "a draft edit keeps the draft on its line", tool: "update_review_comment", args: map[string]any{"project": p, "iid": s.MR, "draft_id": "{{edit_draft}}",
			"note_sha256": "{{edit_sha}}", "body": "A draft the live run edited, still on its line.\n\n/approve stays text.",
			"escape_commands": true}},
		// Still on its line, with the new text and its new note_sha256.
		{name: "an edited draft keeps its line and gets a new hash", tool: "list_review_comments", args: map[string]any{"project": p, "iid": s.MR}},
		{name: "a draft publish is previewed", tool: "publish_review_comment", args: map[string]any{"project": p, "iid": s.MR, "draft_id": "{{edit_draft}}", "dry_run": true}},
		{name: "one draft is published alone", tool: "publish_review_comment", args: map[string]any{"project": p, "iid": s.MR, "draft_id": "{{edit_draft}}"}},
		{name: "a published draft is refused a second publish", tool: "publish_review_comment", args: map[string]any{"project": p, "iid": s.MR, "draft_id": "{{edit_draft}}"},
			expectError: true, why: "a draft already published is gone"},
		// GitLab keeps one draft reply per person per thread, and mr_thread
		// has one.
		{name: "a second draft reply in one thread is refused", tool: "add_review_comment", args: map[string]any{"project": p, "iid": s.MR, "body": "A second draft reply.",
			"discussion_id": "{{mr_thread}}"}, expectError: true},
		// A reply that does not resolve its thread reopens it when published.
		{name: "a draft reply that leaves its thread open is added", tool: "add_review_comment", args: map[string]any{"project": p, "iid": s.MR, "body": "A draft reply that reopens.",
			"discussion_id": "{{mr_thread2}}"}, save: map[string]string{"reply_draft": "note_id"}},
		{name: "the general MR thread resolves", tool: "resolve_discussion", args: map[string]any{"project": p, "iid": s.MR, "discussion_id": "{{mr_thread2}}"}},
		{name: "a published reply reopens its thread", tool: "publish_review_comment", args: map[string]any{"project": p, "iid": s.MR, "draft_id": "{{reply_draft}}"}},
		{name: "an approving review without Ship is refused", tool: "submit_review", args: map[string]any{"project": p, "iid": s.MR, "reviewer_state": "approved"},
			expectError: true},
		{name: "a review submit is previewed", tool: "submit_review", args: map[string]any{"project": p, "iid": s.MR, "summary": "Summary.", "dry_run": true}},
		{name: "a review is submitted with its drafts", tool: "submit_review", args: map[string]any{"project": p, "iid": s.MR, "summary": "A review the live run wrote.\n/approve",
			"escape_commands": true, "reviewer_state": "reviewed"}},

		// Code: a branch, commits on it, a merge request from it.
		{name: "a branch create is previewed", tool: "create_branch", args: map[string]any{"project": p, "branch": branch, "ref": s.Default, "dry_run": true}},
		{name: "a branch is created", tool: "create_branch", args: map[string]any{"project": p, "branch": branch, "ref": s.Default}},
		{name: "a branch that exists is refused", tool: "create_branch", args: map[string]any{"project": p, "branch": branch}, expectError: true},
		{name: "a new branch under a wildcard rule is refused", tool: "create_branch", args: map[string]any{"project": p, "branch": strings.TrimSuffix(guardBranch(s), "one") + "three",
			"ref": branch}, expectError: true},
		{name: "a commit to the default branch is refused", tool: "create_commit", args: map[string]any{"project": p, "branch": s.Default, "message": "Refused",
			"actions": []any{create("refused.txt", "x\n")}}, expectError: true},
		{name: "a commit to a branch a wildcard rule protects is refused", tool: "create_commit", args: map[string]any{"project": p, "branch": guardBranch(s), "message": "Refused",
			"actions": []any{create("refused.txt", "x\n")}}, expectError: true},
		{name: "a commit starting a branch a wildcard rule would protect is refused", tool: "create_commit", args: map[string]any{"project": p, "branch": strings.TrimSuffix(guardBranch(s), "one") + "two",
			"start_branch": s.Default, "message": "Refused", "actions": []any{create("refused.txt", "x\n")}},
			expectError: true},
		{name: "a commit is previewed", tool: "create_commit", args: map[string]any{"project": p, "branch": branch, "message": "Dry", "dry_run": true,
			"actions": []any{create("dry.txt", "x\n")}}},
		{name: "a commit creates text and binary files", tool: "create_commit", args: map[string]any{"project": p, "branch": branch, "message": "Add written files",
			"actions": []any{create("written/a.txt", "a\n"), create("written/b.txt", "b\n"),
				map[string]any{"action": "create", "file_path": "written/c.bin", "content": "AAEC", "encoding": "base64"}}}},
		{name: "a written file's last commit is read", tool: "get_file", args: map[string]any{"project": p, "path": "written/a.txt", "ref": branch},
			save: map[string]string{"a_commit": "last_commit_id"}},
		{name: "a commit changes, moves and deletes files", tool: "create_commit", args: map[string]any{"project": p, "branch": branch, "message": "Change, move and delete",
			"actions": []any{
				map[string]any{"action": "update", "file_path": "written/a.txt", "content": "a, changed\n", "last_commit_id": "{{a_commit}}"},
				map[string]any{"action": "move", "file_path": "written/moved.txt", "previous_path": "written/b.txt", "last_commit_id": "{{a_commit}}"},
				map[string]any{"action": "delete", "file_path": "written/c.bin", "last_commit_id": "{{a_commit}}"}}}},
		{name: "a moved file is read at its new path", tool: "get_file", args: map[string]any{"project": p, "path": "written/moved.txt", "ref": branch}},
		{name: "a commit with a stale last_commit_id is refused", tool: "create_commit", args: map[string]any{"project": p, "branch": branch, "message": "Stale",
			"actions": []any{map[string]any{"action": "update", "file_path": "written/a.txt", "content": "stale\n",
				"last_commit_id": "{{a_commit}}"}}}, expectError: true},
		{name: "a commit starts a new branch", tool: "create_commit", args: map[string]any{"project": p, "branch": branch2, "start_branch": s.Default, "message": "Start a branch",
			"actions": []any{create("written/second.txt", "second\n")}}},
		{name: "an MR create is previewed", tool: "create_merge_request", args: map[string]any{"project": p, "source_branch": branch, "title": "Written merge request " + s.Name,
			"dry_run": true}},
		{name: "an MR is created with every field", tool: "create_merge_request", args: map[string]any{"project": p, "source_branch": branch, "target_branch": s.Default,
			"title": "Written merge request " + s.Name, "description": "The live run wrote this.\n\n/merge\n", "escape_commands": true,
			"labels": []any{s.Label}, "assignees": me, "reviewers": me, "milestone": s.Milestone, "draft": true,
			"remove_source_branch": true, "squash": true}, save: map[string]string{"mr": "iid"}},
		// GitLab moves a new merge request's updated_at a moment after
		// the create answers, so the witness is read afterwards.
		{name: "a new MR's witness is read once it settles", tool: "get_merge_request", args: map[string]any{"project": p, "iid": "{{mr}}"}, pause: 10 * time.Second,
			save: map[string]string{"mr_at": "updated_at"}},
		{name: "a second MR for the same branches is refused", tool: "create_merge_request", args: map[string]any{"project": p, "source_branch": branch, "title": "Again"},
			expectError: true},
		{name: "an MR update is previewed", tool: "update_merge_request", args: map[string]any{"project": p, "iid": "{{mr}}", "updated_at": "{{mr_at}}", "draft": false,
			"dry_run": true}},
		{name: "an MR is changed in every field", tool: "update_merge_request", args: map[string]any{"project": p, "iid": "{{mr}}", "updated_at": "{{mr_at}}", "draft": false,
			"title": "Written merge request, retitled " + s.Name, "description": "Replaced.\n/close", "escape_commands": true,
			"add_labels": []any{s.Label2}, "remove_labels": []any{s.Label}, "remove_assignees": me, "remove_reviewers": me,
			"milestone": milestone2, "remove_source_branch": false, "squash": false, "target_branch": s.Default},
			save: map[string]string{"mr_at2": "updated_at"}},
		{name: "an MR update with a stale witness is refused", tool: "update_merge_request", args: map[string]any{"project": p, "iid": "{{mr}}", "updated_at": "{{mr_at}}", "squash": true},
			expectError: true},
		{name: "an MR closes and gains an assignee and a reviewer", tool: "update_merge_request", args: map[string]any{"project": p, "iid": "{{mr}}", "updated_at": "{{mr_at2}}", "state": "close",
			"clear_milestone": true, "add_assignees": me, "add_reviewers": me}, save: map[string]string{"mr_at3": "updated_at"}},
		{name: "a closed MR reopens", tool: "update_merge_request", args: map[string]any{"project": p, "iid": "{{mr}}", "updated_at": "{{mr_at3}}", "state": "reopen"}},

		// A push after the merge request opened gives it a second diff
		// version, and the changes since the first are that push alone.
		// The run's account is a reviewer again, so get_merge_request below
		// shows its review state.
		{name: "an MR's first diff version is read", tool: "list_mr_versions", args: map[string]any{"project": p, "iid": "{{mr}}"}, save: map[string]string{"mr_v1": "versions.0.id"}},
		{name: "a push after review is committed", tool: "create_commit", args: map[string]any{"project": p, "branch": branch, "message": "A change after review",
			"actions": []any{create("written/after-review.txt", "after review\n")}}},
		// GitLab makes the version in the background after the push.
		{name: "an MR's new diff version appears", tool: "list_mr_versions", args: map[string]any{"project": p, "iid": "{{mr}}", "max": 1},
			until: &waitFor{path: "versions.0.id", saved: "mr_v1", every: 5 * time.Second, within: time.Minute},
			save:  map[string]string{"mr_v2": "versions.0.id"}},
		// Paged on its own: a paged step saves again from the page it
		// follows to, which holds an older version.
		{name: "an MR's diff versions page one at a time", tool: "list_mr_versions", args: map[string]any{"project": p, "iid": "{{mr}}", "max": 1}, paged: true},
		{name: "an MR's changes since a version are read", tool: "compare_mr_versions", args: map[string]any{"project": p, "iid": "{{mr}}", "from_version": "{{mr_v1}}"}},
		{name: "a version comparison is read from offsets", tool: "compare_mr_versions", args: map[string]any{"project": p, "iid": "{{mr}}", "from_version": "{{mr_v1}}",
			"to_version": "{{mr_v2}}", "commit_offset": 1, "file_offset": 0, "diff_offset": 5}},
		{name: "a comparison from the newest version is refused", tool: "compare_mr_versions", args: map[string]any{"project": p, "iid": "{{mr}}", "from_version": "{{mr_v2}}"},
			expectError: true, why: "the newest version: nothing was pushed after it"},

		// Time tracking, every option on the issue and the merge request,
		// from the witnesses a read gives: updated_at, and the total spent,
		// which GitLab does not move updated_at for.
		{name: "an issue's time-tracking witnesses are read", tool: "get_issue", args: map[string]any{"project": p, "iid": "{{issue}}"},
			save: map[string]string{"issue_tt": "updated_at", "issue_spent": "time_stats.total_time_spent"}},
		{name: "issue time tracking is previewed", tool: "track_time", args: map[string]any{"project": p, "type": "issue", "iid": "{{issue}}", "updated_at": "{{issue_tt}}",
			"total_time_spent": "{{issue_spent}}", "estimate": "1d 2h", "add_spent": "1h30m", "dry_run": true}},
		{name: "an issue gets an estimate and spent time", tool: "track_time", args: map[string]any{"project": p, "type": "issue", "iid": "{{issue}}", "updated_at": "{{issue_tt}}",
			"total_time_spent": "{{issue_spent}}", "estimate": "1d 2h", "add_spent": "1h30m"},
			save: map[string]string{"issue_tt2": "updated_at", "issue_spent2": "total_time_spent"}},
		{name: "issue time with a stale updated_at is refused", tool: "track_time", args: map[string]any{"project": p, "type": "issue", "iid": "{{issue}}", "updated_at": "{{issue_tt}}",
			"total_time_spent": "{{issue_spent2}}", "add_spent": "1h"}, expectError: true},
		{name: "spent time is taken off an issue", tool: "track_time", args: map[string]any{"project": p, "type": "issue", "iid": "{{issue}}", "updated_at": "{{issue_tt2}}",
			"total_time_spent": "{{issue_spent2}}", "add_spent": "-30m"},
			save: map[string]string{"issue_tt3": "updated_at", "issue_spent3": "total_time_spent"}},
		{name: "issue time sent again with an old total is refused", tool: "track_time", args: map[string]any{"project": p, "type": "issue", "iid": "{{issue}}", "updated_at": "{{issue_tt3}}",
			"total_time_spent": "{{issue_spent2}}", "add_spent": "-30m"}, expectError: true,
			why: "the same time sent again with the total from before it: updated_at did not move, the total did"},
		// The resets one at a time, so the transcript shows whether a reset
		// of spent time alone moves updated_at.
		{name: "an issue's spent time resets", tool: "track_time", args: map[string]any{"project": p, "type": "issue", "iid": "{{issue}}", "updated_at": "{{issue_tt3}}",
			"total_time_spent": "{{issue_spent3}}", "reset_spent": true}, save: map[string]string{"issue_tt4": "updated_at"}},
		{name: "an issue's estimate resets", tool: "track_time", args: map[string]any{"project": p, "type": "issue", "iid": "{{issue}}", "updated_at": "{{issue_tt4}}",
			"reset_estimate": true}},
		// The run's account, the merge request's only reviewer, is asked
		// for a new review, through GraphQL. Before the time-tracking
		// witness is read, since a re-request may move updated_at.
		{name: "a re-request of a reviewer who has not reviewed is refused", tool: "rerequest_review", args: map[string]any{"project": p, "iid": "{{mr}}", "username": s.User,
			"reviewer_state": "unreviewed"}, expectError: true},
		{name: "the written MR is reviewed", tool: "submit_review", args: map[string]any{"project": p, "iid": "{{mr}}", "reviewer_state": "reviewed"}},
		{name: "an MR's review state is read", tool: "get_merge_request", args: map[string]any{"project": p, "iid": "{{mr}}"},
			save: map[string]string{"mr_review": "reviewer_states.reviewers.0.state"}},
		{name: "a re-request with a review state no read showed is refused", tool: "rerequest_review", args: map[string]any{"project": p, "iid": "{{mr}}", "username": s.User, "reviewer_state": "approved"},
			expectError: true},
		{name: "a review re-request is previewed", tool: "rerequest_review", args: map[string]any{"project": p, "iid": "{{mr}}", "username": s.User,
			"reviewer_state": "{{mr_review}}", "dry_run": true}},
		{name: "a reviewer is asked for a new review", tool: "rerequest_review", args: map[string]any{"project": p, "iid": "{{mr}}", "username": s.User,
			"reviewer_state": "{{mr_review}}"}},
		{name: "an MR's time-tracking witnesses are read", tool: "get_merge_request", args: map[string]any{"project": p, "iid": "{{mr}}"},
			save: map[string]string{"mr_tt": "updated_at", "mr_spent": "time_stats.total_time_spent"}},
		{name: "an MR gets an estimate and spent time", tool: "track_time", args: map[string]any{"project": p, "type": "merge_request", "iid": "{{mr}}", "updated_at": "{{mr_tt}}",
			"total_time_spent": "{{mr_spent}}", "estimate": "2h", "add_spent": "45m"},
			save: map[string]string{"mr_tt2": "updated_at", "mr_spent2": "total_time_spent"}},
		{name: "more spent time is added to an MR", tool: "track_time", args: map[string]any{"project": p, "type": "merge_request", "iid": "{{mr}}", "updated_at": "{{mr_tt2}}",
			"total_time_spent": "{{mr_spent2}}", "add_spent": "15m"},
			save: map[string]string{"mr_tt3": "updated_at", "mr_spent3": "total_time_spent"}},
		{name: "MR time sent again with an old total is refused", tool: "track_time", args: map[string]any{"project": p, "type": "merge_request", "iid": "{{mr}}", "updated_at": "{{mr_tt3}}",
			"total_time_spent": "{{mr_spent2}}", "add_spent": "15m"}, expectError: true,
			why: "the same time sent again with the total from before it: updated_at did not move, the total did"},
		{name: "an MR's spent time resets", tool: "track_time", args: map[string]any{"project": p, "type": "merge_request", "iid": "{{mr}}", "updated_at": "{{mr_tt3}}",
			"total_time_spent": "{{mr_spent3}}", "reset_spent": true}, save: map[string]string{"mr_tt4": "updated_at"}},
		{name: "an MR's estimate resets", tool: "track_time", args: map[string]any{"project": p, "type": "merge_request", "iid": "{{mr}}", "updated_at": "{{mr_tt4}}",
			"reset_estimate": true}},

		// Your own notifications and to-do items on the scratch issue and
		// merge request. The account takes part in both, so it is
		// subscribed until it unsubscribes: the first subscribe is
		// unchanged. Each to-do added is marked done, so the account's
		// list is left as it was.
		{name: "an issue subscribe is previewed", tool: "subscribe", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "dry_run": true}},
		{name: "an issue already subscribed is subscribed again", tool: "subscribe", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue}},
		{name: "an issue unsubscribe is previewed", tool: "subscribe", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "unsubscribe": true, "dry_run": true}},
		{name: "the account unsubscribes from an issue", tool: "subscribe", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "unsubscribe": true}},
		{name: "an issue already unsubscribed is unsubscribed again", tool: "subscribe", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "unsubscribe": true}},
		{name: "the account subscribes to an issue again", tool: "subscribe", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue}},
		{name: "the account unsubscribes from an MR", tool: "subscribe", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "unsubscribe": true}},
		{name: "the account subscribes to an MR", tool: "subscribe", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR}},
		{name: "an MR already subscribed is subscribed again", tool: "subscribe", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR}},
		{name: "an issue to-do is previewed", tool: "add_todo", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "dry_run": true}},
		{name: "a to-do is added on an issue", tool: "add_todo", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue}, save: map[string]string{"todo_issue": "todo_id"}},
		{name: "an issue to-do is previewed while one is pending", tool: "add_todo", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "dry_run": true}},
		{name: "a to-do is added again on an issue", tool: "add_todo", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue}},
		{name: "an issue's to-do is marked done", tool: "mark_todos_done", args: map[string]any{"ids": []any{"{{todo_issue}}"}}},
		{name: "a to-do is added on an MR", tool: "add_todo", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR}, save: map[string]string{"todo_mr": "todo_id"}},
		{name: "a to-do is added again on an MR", tool: "add_todo", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR}},
		// To-do items: the run's own, found in the scratch project while
		// the one just added is pending.
		{name: "pending to-do items are listed in the project", tool: "list_todos", args: map[string]any{"project": p, "state": "pending"}, save: map[string]string{"todo": "todos.0.id"}},
		{name: "marking a to-do done is previewed", tool: "mark_todos_done", args: map[string]any{"ids": []any{"{{todo}}"}, "dry_run": true}},
		// The one listed and the one added, which may be the same: ids
		// are marked once each.
		{name: "to-do items are marked done once each", tool: "mark_todos_done", args: map[string]any{"ids": []any{"{{todo}}", "{{todo_mr}}"}}},

		// The history the writes above made: labels swapped, the milestone
		// set and cleared, closed and reopened.
		{name: "an issue's history pages", tool: "list_item_events", args: map[string]any{"project": p, "type": "issue", "iid": "{{issue}}", "max": 2}, paged: true},
		{name: "an MR's history is read", tool: "list_item_events", args: map[string]any{"project": s.ID, "type": "merge_request", "iid": "{{mr}}"}},

		// Reactions on the scratch issue, merge request and comments. One
		// already there, or none to remove, sends nothing; thumbs_up is an
		// alias GitLab keeps as thumbsup, which only its answer reveals.
		// Every reaction added is removed, so the items end as they began.
		// They come after the to-do steps: a reaction marks the account's
		// pending to-dos on the item done.
		{name: "a reaction is previewed", tool: "react", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "emoji": "thumbsup", "dry_run": true}},
		{name: "a reaction is added to an issue", tool: "react", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "emoji": "+1"}},
		{name: "an issue shows its reactions", tool: "get_issue", args: map[string]any{"project": p, "iid": s.Issue}},
		{name: "a reaction already there is added again", tool: "react", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "emoji": ":thumbsup:"}},
		// Another name of a reaction already there: GitLab refuses it, in the
		// account's language, and its alias table cannot be read back, so
		// GitLab's own reason is passed on.
		{name: "an alias of a reaction already there is refused", tool: "react", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "emoji": "thumbs_up"},
			expectError: true, why: "an alias of a reaction already there, which GitLab refuses with 404"},
		{name: "an emoji GitLab does not know is refused", tool: "react", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "emoji": "no_such_emoji_here"},
			expectError: true, why: "an emoji GitLab does not know, which it answers with 404"},
		{name: "a reaction removal is previewed", tool: "react", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "emoji": "thumbsup", "remove": true, "dry_run": true}},
		{name: "a reaction is removed from an issue", tool: "react", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "emoji": "thumbsup", "remove": true}},
		{name: "a reaction already gone is removed again", tool: "react", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "emoji": "thumbsup", "remove": true}},
		{name: "a reaction is added to an MR", tool: "react", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "emoji": "rocket"}},
		{name: "an MR shows its reactions", tool: "get_merge_request", args: map[string]any{"project": p, "iid": s.MR}},
		{name: "a reaction is removed from an MR", tool: "react", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "emoji": "rocket", "remove": true}},
		{name: "a reaction is added to an MR comment", tool: "react", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "note_id": "{{mr_note}}", "emoji": "eyes"}},
		{name: "a reaction is removed from an MR comment", tool: "react", args: map[string]any{"project": p, "type": "merge_request", "iid": s.MR, "note_id": "{{mr_note}}", "emoji": "eyes",
			"remove": true}},
		// A reaction on a comment moves the comment's updated_at.
		{name: "a comment to react to is added", tool: "add_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "body": "A comment the live run reacts to."},
			save: map[string]string{"react_note": "note_id", "react_note_at": "updated_at"}},
		{name: "a reaction is added to an issue comment", tool: "react", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "note_id": "{{react_note}}", "emoji": "tada"}},
		{name: "a comment edit from before a reaction is refused", tool: "update_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "note_id": "{{react_note}}",
			"updated_at": "{{react_note_at}}", "body": "Edited."}, expectError: true,
			why: "the updated_at from before the reaction, which moved it"},
		{name: "a reaction is removed from an issue comment", tool: "react", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "note_id": "{{react_note}}", "emoji": "tada",
			"remove": true}},

		// An image for a comment, read from the run's own directory, which
		// the server is started with in GITLAB_MCP_UPLOAD_DIRS; its
		// Markdown is posted on the scratch issue, after the reactions and
		// the to-do steps that a comment would disturb.
		{name: "an image upload is previewed", tool: "upload_file", args: map[string]any{"project": p, "path": s.Image, "dry_run": true}},
		{name: "an image uploads from an allowed directory", tool: "upload_file", args: map[string]any{"project": p, "path": s.Image}, save: map[string]string{"image_md": "markdown"}},
		{name: "a text file upload is refused", tool: "upload_file", args: map[string]any{"project": p, "path": s.NotImage}, expectError: true},
		{name: "an image outside the upload directories is refused", tool: "upload_file", args: map[string]any{"project": p, "path": s.Outside}, expectError: true},
		{name: "an uploaded image is posted in a comment", tool: "add_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "body": "{{image_md}}"}},

		{name: "supplied CI content lints", tool: "lint_ci", args: map[string]any{"project": p, "content": "check:\n  script:\n    - echo live\n", "include_jobs": true}},
		{name: "supplied CI content with an include is refused", tool: "lint_ci", args: map[string]any{"project": p, "content": "include:\n  - local: other.yml\n"}, expectError: true,
			why: "supplied content with an include, which GitLab would fetch"},
	}
}

// sg names a suggestion for apply_suggestions by what a read saved: its
// id under name and its text under name_text.
func sg(name string) map[string]any {
	return map[string]any{"id": "{{" + name + "}}", "text": "{{" + name + "_text}}"}
}

// shipDestructiveAndToolsets is sent to a second server with Ship,
// Destructive and every toolset on and its writes confined to the
// maintainer's group: merges, approvals, deletes, CI runs, the wiki,
// snippets, releases, deployments and activity, then linksMovesAndTags.
// It merges and deletes only what the run made, and every delete is
// refused once without confirm or with a stale witness before it is
// made.
func shipDestructiveAndToolsets(s scratch) []step {
	p := s.Path
	w := s.Name[len(s.Name)-6:]
	branch := "ship-" + w
	branch2 := "write2-" + w // the write path's, one commit the default branch lacks
	mrBranch := "mrpipe-" + w
	tag, tag2 := "live-"+w, "live-"+w+"-b"
	stale := strings.Repeat("0", 40)
	now := time.Now().UTC()
	yesterday, tomorrow := now.Add(-24*time.Hour).Format(time.RFC3339), now.Add(24*time.Hour).Format(time.RFC3339)
	return slices.Concat([]step{
		// Making a confidential issue public is make_issue_public's alone,
		// even with every flag on.
		{name: "a confidential issue is created", tool: "create_issue", args: map[string]any{"project": p, "title": "A confidential issue " + s.Name, "confidential": true},
			save: map[string]string{"secret_issue": "iid", "secret_at": "updated_at"}},
		{name: "update_issue refuses to make an issue public with every flag on", tool: "update_issue", args: map[string]any{"project": p, "iid": "{{secret_issue}}", "updated_at": "{{secret_at}}",
			"confidential": false},
			expectError: true},
		{name: "making an issue public without confirm is refused", tool: "make_issue_public", args: map[string]any{"project": p, "iid": "{{secret_issue}}", "updated_at": "{{secret_at}}"},
			expectError: true},
		{name: "making an issue public is previewed", tool: "make_issue_public", args: map[string]any{"project": p, "iid": "{{secret_issue}}", "updated_at": "{{secret_at}}",
			"confirm": true, "dry_run": true}},
		{name: "a confidential issue is made public", tool: "make_issue_public", args: map[string]any{"project": p, "iid": "{{secret_issue}}", "updated_at": "{{secret_at}}",
			"confirm": true}},
		// A merge request to approve and merge.
		{name: "a branch to merge is created", tool: "create_branch", args: map[string]any{"project": p, "branch": branch, "ref": s.Default}},
		{name: "a change to merge is committed", tool: "create_commit", args: map[string]any{"project": p, "branch": branch, "message": "A change to merge",
			"actions": []any{map[string]any{"action": "create", "file_path": "ship/merged.txt", "content": "merged\n"}}}},
		{name: "an MR to merge is opened", tool: "create_merge_request", args: map[string]any{"project": p, "source_branch": branch, "title": "Ship merge request " + s.Name},
			save: map[string]string{"ship_mr": "iid"}},
		// GitLab computes mergeability in the background.
		{name: "the MR to merge is read once GitLab checks it", tool: "get_merge_request", args: map[string]any{"project": p, "iid": "{{ship_mr}}"}, pause: 20 * time.Second,
			save: map[string]string{"ship_sha": "sha"}},
		{name: "an approval with a stale sha is refused", tool: "approve_merge_request", args: map[string]any{"project": p, "iid": "{{ship_mr}}", "sha": stale},
			expectError: true},
		{name: "an approval is previewed", tool: "approve_merge_request", args: map[string]any{"project": p, "iid": "{{ship_mr}}", "sha": "{{ship_sha}}", "dry_run": true}},
		{name: "the author approves their own MR", tool: "approve_merge_request", args: map[string]any{"project": p, "iid": "{{ship_mr}}", "sha": "{{ship_sha}}"}, anyOutcome: true,
			why: "whether the author may approve their own merge request is the project's and the tier's to say"},
		{name: "an unapproval is previewed", tool: "unapprove_merge_request", args: map[string]any{"project": p, "iid": "{{ship_mr}}", "dry_run": true}},
		{name: "an approval is withdrawn", tool: "unapprove_merge_request", args: map[string]any{"project": p, "iid": "{{ship_mr}}"}},
		{name: "a merge with a stale sha is refused", tool: "merge_merge_request", args: map[string]any{"project": p, "iid": "{{ship_mr}}", "sha": stale},
			expectError: true},
		{name: "a squash merge is previewed", tool: "merge_merge_request", args: map[string]any{"project": p, "iid": "{{ship_mr}}", "sha": "{{ship_sha}}", "squash": true,
			"dry_run": true}},
		{name: "an MR merges when it can", tool: "merge_merge_request", args: map[string]any{"project": p, "iid": "{{ship_mr}}", "sha": "{{ship_sha}}", "squash": false,
			"remove_source_branch": false, "merge_commit_message": "Merge the live run's change", "squash_commit_message": "Unused",
			"auto_merge": false}},
		{name: "a merged MR is merged again", tool: "merge_merge_request", args: map[string]any{"project": p, "iid": "{{ship_mr}}", "sha": "{{ship_sha}}"}},

		// Branches: the merged one, then the write path's unmerged one.
		{name: "the merged branch's head is read", tool: "list_branches", args: map[string]any{"project": p, "search": branch}, save: map[string]string{"ship_head": "branches.0.commit_id"}},
		{name: "a branch delete without confirm is refused", tool: "delete_branch", args: map[string]any{"project": p, "branch": branch, "sha": "{{ship_head}}"},
			expectError: true},
		{name: "a branch delete is previewed", tool: "delete_branch", args: map[string]any{"project": p, "branch": branch, "sha": "{{ship_head}}", "dry_run": true}},
		{name: "a merged branch is deleted", tool: "delete_branch", args: map[string]any{"project": p, "branch": branch, "sha": "{{ship_head}}", "confirm": true}},
		{name: "an unmerged branch's head is read", tool: "list_branches", args: map[string]any{"project": p, "search": branch2}, save: map[string]string{"w2_head": "branches.0.commit_id"}},
		{name: "an unmerged branch delete without unmerged is refused", tool: "delete_branch", args: map[string]any{"project": p, "branch": branch2, "sha": "{{w2_head}}", "confirm": true},
			expectError: true},
		{name: "deleting the default branch is refused", tool: "delete_branch", args: map[string]any{"project": p, "branch": s.Default, "sha": "{{w2_head}}", "confirm": true},
			expectError: true},
		{name: "an unmerged branch is deleted", tool: "delete_branch", args: map[string]any{"project": p, "branch": branch2, "sha": "{{w2_head}}", "unmerged": true, "confirm": true}},

		// A merge request pipeline, on a merge request of its own: the
		// CI configuration gives merge request pipelines to mrpipe-
		// branches only.
		{name: "a branch for an MR pipeline is created", tool: "create_branch", args: map[string]any{"project": p, "branch": mrBranch, "ref": s.Default}},
		{name: "a change for an MR pipeline is committed", tool: "create_commit", args: map[string]any{"project": p, "branch": mrBranch, "message": "A change to run a pipeline for",
			"actions": []any{map[string]any{"action": "create", "file_path": "ship/mr-pipeline.txt", "content": "pipeline\nsecond\nthird\n"}}}},
		{name: "an MR for a pipeline is opened", tool: "create_merge_request", args: map[string]any{"project": p, "source_branch": mrBranch,
			"title": "Merge request pipeline " + s.Name}, save: map[string]string{"mrpipe_mr": "iid"}},
		// GitLab prepares the merge request's diff in the background.
		{name: "an MR pipeline run is previewed", tool: "run_merge_request_pipeline", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}", "dry_run": true},
			pause: 20 * time.Second},
		{name: "an MR pipeline runs", tool: "run_merge_request_pipeline", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}"}},
		// A merge request from the default branch, which apply_suggestions
		// does not commit to.
		{name: "an MR from the default branch is opened", tool: "create_merge_request", args: map[string]any{"project": p, "source_branch": s.Default, "target_branch": guardBranch(s),
			"title": "Default into a protected branch " + s.Name}, save: map[string]string{"mrguard_mr": "iid"}},

		// Suggestions on the merge request pipeline's merge request: two
		// applied in one commit, then one more alone once GitLab has moved
		// the comments to the new head.
		{name: "a suggestion is posted on the first line", tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": "{{mrpipe_mr}}",
			"body": "A suggestion the live run wrote.\n\n```suggestion:-0+0\npipeline, suggested\n```\n", "file": "ship/mr-pipeline.txt",
			"line": 1, "side": "new"}},
		{name: "a suggestion is posted on the third line", tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": "{{mrpipe_mr}}",
			"body": "Another suggestion.\n\n```suggestion:-0+0\nthird, suggested\n```\n", "file": "ship/mr-pipeline.txt",
			"line": 3, "side": "new"}},
		{name: "two suggestions are read with their text", tool: "list_discussions", args: map[string]any{"project": p, "type": "merge_request", "iid": "{{mrpipe_mr}}"},
			save: map[string]string{"sg_third": "threads.0.notes.0.suggestions.0.id",
				"sg_third_text": "threads.0.notes.0.suggestions.0.untrusted_to_content",
				"sg_first":      "threads.1.notes.0.suggestions.0.id", "sg_first_text": "threads.1.notes.0.suggestions.0.untrusted_to_content"}},
		{name: "a suggestion on an MR from the default branch is refused", tool: "apply_suggestions", args: map[string]any{"project": p, "iid": "{{mrguard_mr}}", "suggestions": []any{sg("sg_first")}},
			expectError: true},
		{name: "a suggestion given twice is refused", tool: "apply_suggestions", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}", "suggestions": []any{sg("sg_first"),
			sg("sg_first")}}, expectError: true},
		// Each text is held against GitLab's: one that differs is refused,
		// and the batch with the texts read lands.
		{name: "a suggestion with another text is refused", tool: "apply_suggestions", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}", "suggestions": []any{
			map[string]any{"id": "{{sg_first}}", "text": "not what was suggested\n"}}}, expectError: true},
		{name: "applying suggestions is previewed", tool: "apply_suggestions", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}", "suggestions": []any{sg("sg_first"),
			sg("sg_third")}, "commit_message": "Apply %{suggestions_count} suggestions to %{branch_name}", "dry_run": true}},
		{name: "two suggestions apply in one commit", tool: "apply_suggestions", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}", "suggestions": []any{sg("sg_first"),
			sg("sg_third")}, "commit_message": "Apply %{suggestions_count} suggestions to %{branch_name}"}},
		{name: "an applied suggestion is applied again", tool: "apply_suggestions", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}", "suggestions": []any{sg("sg_first")}}},
		{name: "the file shows the applied suggestions", tool: "get_file", args: map[string]any{"project": p, "path": "ship/mr-pipeline.txt", "ref": mrBranch}},
		{name: "a suggestion is posted on the second line", tool: "add_comment", args: map[string]any{"project": p, "type": "merge_request", "iid": "{{mrpipe_mr}}",
			"body": "One more.\n\n```suggestion:-0+0\nsecond, suggested\n```\n", "file": "ship/mr-pipeline.txt", "line": 2, "side": "new"},
			pause: 20 * time.Second},
		{name: "the new suggestion is read with its text", tool: "list_discussions", args: map[string]any{"project": p, "type": "merge_request", "iid": "{{mrpipe_mr}}"},
			save: map[string]string{"sg_second": "threads.0.notes.0.suggestions.0.id",
				"sg_second_text": "threads.0.notes.0.suggestions.0.untrusted_to_content"}},
		{name: "a suggestion applies after the head moved", tool: "apply_suggestions", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}", "suggestions": []any{sg("sg_second")}},
			anyOutcome: true,
			why:        "GitLab refuses a suggestion until it has moved the comments to the branch's new head, which takes a moment"},

		// An auto-merge set while the merge request's pipeline runs, then
		// canceled; a pipeline that already passed merges it instead,
		// and the cancel then finds none set.
		{name: "the pipeline MR's head is read", tool: "get_merge_request", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}"}, pause: 10 * time.Second,
			save: map[string]string{"mrpipe_sha": "sha"}},
		{name: "an auto-merge is set while the pipeline runs", tool: "merge_merge_request", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}", "sha": "{{mrpipe_sha}}", "auto_merge": true},
			anyOutcome: true, why: "GitLab sets the auto-merge, or merges now when the pipeline has already passed"},
		{name: "an auto-merge cancel is previewed", tool: "cancel_auto_merge", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}", "dry_run": true}},
		// The pipeline may pass as the cancel arrives: GitLab then answers
		// success and merges anyway. The driver checks no result field, so
		// the tool holds that the outcome is canceled only for an open
		// merge request with the auto-merge off, merging while locked and
		// merged once merged; the transcript shows which.
		{name: "an auto-merge is canceled", tool: "cancel_auto_merge", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}"}, anyOutcome: true,
			why: "canceled while the pipeline runs, or merging or merged when it passed first; never canceled while locked or merged"},
		{name: "an auto-merge is canceled again", tool: "cancel_auto_merge", args: map[string]any{"project": p, "iid": "{{mrpipe_mr}}"}},

		// CI: a pipeline run and canceled, the failed one retried, a job
		// retried, the manual one started.
		{name: "a pipeline run is previewed", tool: "run_pipeline", args: map[string]any{"project": p, "ref": s.Default, "dry_run": true}},
		{name: "a pipeline runs with inputs and variables", tool: "run_pipeline", args: map[string]any{"project": p, "ref": s.Default, "inputs": map[string]any{"greeting": "live"},
			"variables": []any{map[string]any{"key": "LIVE_VARIABLE", "value": "live-value-" + w, "type": "env_var"}}},
			save: map[string]string{"run_pipeline": "pipeline_id"}},
		{name: "a pipeline cancel is previewed", tool: "cancel_pipeline", args: map[string]any{"project": p, "pipeline_id": "{{run_pipeline}}", "dry_run": true}},
		{name: "a running pipeline is canceled", tool: "cancel_pipeline", args: map[string]any{"project": p, "pipeline_id": "{{run_pipeline}}"}},
		{name: "a pipeline retry is previewed", tool: "retry_pipeline", args: map[string]any{"project": p, "pipeline_id": s.Pipeline, "dry_run": true}},
		{name: "a failed pipeline is retried", tool: "retry_pipeline", args: map[string]any{"project": p, "pipeline_id": s.Pipeline}},
		{name: "a job retry with an input the job lacks is refused", tool: "retry_job", args: map[string]any{"project": p, "job_id": s.JobPassed, "inputs": map[string]any{"undeclared": "x"}},
			expectError: true},
		{name: "a job retry is previewed", tool: "retry_job", args: map[string]any{"project": p, "job_id": s.JobPassed, "dry_run": true,
			"inputs": map[string]any{"target": "retried"}}},
		{name: "a job is retried with an input", tool: "retry_job", args: map[string]any{"project": p, "job_id": s.JobPassed, "inputs": map[string]any{"target": "retried"}}},
		{name: "a manual job start is previewed", tool: "play_job", args: map[string]any{"project": p, "job_id": s.JobManual, "dry_run": true,
			"variables": []any{map[string]any{"key": "LIVE_PLAY", "value": "played-" + w}}, "inputs": map[string]any{"target": "played"}}},
		{name: "a manual job starts with variables and inputs", tool: "play_job", args: map[string]any{"project": p, "job_id": s.JobManual,
			"variables": []any{map[string]any{"key": "LIVE_PLAY", "value": "played-" + w}}, "inputs": map[string]any{"target": "played"}}},
		{name: "a manual job no longer waiting is refused", tool: "play_job", args: map[string]any{"project": p, "job_id": s.JobManual}, expectError: true},

		// A comment of the run's, deleted.
		{name: "a comment to delete is added", tool: "add_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "body": "A comment the live run deletes."}},
		{name: "the comment to delete is read", tool: "list_discussions", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue},
			save: map[string]string{"del_note": "threads.0.notes.0.id", "del_at": "threads.0.notes.0.updated_at"}},
		{name: "a comment delete with a stale witness is refused", tool: "delete_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "note_id": "{{del_note}}",
			"updated_at": "2020-01-01T00:00:00Z", "confirm": true}, expectError: true},
		{name: "a comment delete is previewed", tool: "delete_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "note_id": "{{del_note}}",
			"updated_at": "{{del_at}}", "dry_run": true}},
		{name: "a comment is deleted", tool: "delete_comment", args: map[string]any{"project": p, "type": "issue", "iid": s.Issue, "note_id": "{{del_note}}",
			"updated_at": "{{del_at}}", "confirm": true}},

		// The wiki.
		{name: "a wiki page save is previewed", tool: "save_wiki_page", args: map[string]any{"project": p, "title": "Live page " + w, "content": "# Live\n\n/close stays text.\n",
			"format": "markdown", "dry_run": true}},
		{name: "a wiki page is created", tool: "save_wiki_page", args: map[string]any{"project": p, "title": "Live page " + w, "content": "# Live\n\n/close stays text.\n",
			"format": "markdown"}, save: map[string]string{"page": "slug", "page_sha": "content_sha256"}},
		{name: "wiki pages are listed", tool: "list_wiki_pages", args: map[string]any{"project": p}},
		{name: "a wiki page is read", tool: "get_wiki_page", args: map[string]any{"project": p, "slug": "{{page}}", "offset": 0}},
		{name: "a wiki link resolves", tool: "resolve_url", args: map[string]any{"url": s.WebURL + "/-/wikis/home"}},
		{name: "a wiki page is renamed and changed", tool: "save_wiki_page", args: map[string]any{"project": p, "slug": "{{page}}", "content_sha256": "{{page_sha}}",
			"title": "Live page renamed " + w, "content": "# Live\n\nChanged.\n"}, save: map[string]string{"page2": "slug", "page_sha2": "content_sha256"}},
		{name: "a wiki save with a stale hash is refused", tool: "save_wiki_page", args: map[string]any{"project": p, "slug": "{{page2}}", "content_sha256": "{{page_sha}}", "content": "stale"},
			expectError: true},
		{name: "a wiki page delete is previewed", tool: "delete_wiki_page", args: map[string]any{"project": p, "slug": "{{page2}}", "content_sha256": "{{page_sha2}}", "dry_run": true}},
		{name: "a wiki page is deleted", tool: "delete_wiki_page", args: map[string]any{"project": p, "slug": "{{page2}}", "content_sha256": "{{page_sha2}}", "confirm": true}},

		// Snippets: two, so the listing pages.
		{name: "a snippet create is previewed", tool: "create_snippet", args: map[string]any{"project": p, "title": "Live snippet " + w, "description": "Two files.",
			"files": []any{map[string]any{"path": "a.md", "content": "# A\n"}}, "dry_run": true}},
		{name: "a snippet of two files is created", tool: "create_snippet", args: map[string]any{"project": p, "title": "Live snippet " + w, "description": "Two files.",
			"files": []any{map[string]any{"path": "a.md", "content": "# A\n"}, map[string]any{"path": "b.sh", "content": "echo b\n"}}},
			save: map[string]string{"snippet": "id"}},
		{name: "a snippet of one file is created", tool: "create_snippet", args: map[string]any{"project": p, "title": "Live snippet two " + w,
			"files": []any{map[string]any{"path": "c.txt", "content": "c\n"}}}, save: map[string]string{"snippet2": "id"}},
		{name: "snippets page one at a time", tool: "list_snippets", args: map[string]any{"project": p, "max": 1}, paged: true},
		// A commit to a snippet is followed by GitLab's post-receive job,
		// which moves updated_at after the write answered:
		// the witness is read once it has run.
		{name: "a snippet's witness is read once its commit settles", tool: "get_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet}}"}, pause: 5 * time.Second,
			save: map[string]string{"snippet_at": "updated_at"}},
		{name: "one file of a snippet is read", tool: "get_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet}}", "file": "b.sh", "offset": 0}},

		// The two-file snippet changed through files, from the updated_at a
		// read gives; the one-file snippet through content, then deleted.
		{name: "content for a snippet of two files is refused", tool: "update_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet}}", "updated_at": "{{snippet_at}}",
			"content": "one file only\n"}, expectError: true},
		{name: "a snippet update is previewed", tool: "update_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet}}", "updated_at": "{{snippet_at}}",
			"title": "Live snippet renamed " + w, "description": "Changed.\n/close stays text.\n", "dry_run": true,
			"files": []any{map[string]any{"action": "update", "path": "a.md", "content": "# A, changed\n"}}}},
		{name: "a snippet's files are changed, added and moved", tool: "update_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet}}", "updated_at": "{{snippet_at}}",
			"title": "Live snippet renamed " + w, "description": "Changed.\n/close stays text.\n", "files": []any{
				map[string]any{"action": "update", "path": "a.md", "content": "# A, changed\n"},
				map[string]any{"action": "create", "path": "d.md", "content": "# D\n"},
				map[string]any{"action": "move", "previous_path": "b.sh", "path": "run.sh", "content": "echo moved\n"}}}},
		{name: "a changed snippet's witness is read", tool: "get_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet}}"}, pause: 5 * time.Second,
			save: map[string]string{"snippet_at2": "updated_at"}},
		{name: "a snippet update with a stale witness is refused", tool: "update_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet}}", "updated_at": "{{snippet_at}}",
			"title": "Stale"}, expectError: true},
		{name: "a file is deleted from a snippet", tool: "update_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet}}", "updated_at": "{{snippet_at2}}",
			"files": []any{map[string]any{"action": "delete", "path": "d.md"}}}},
		{name: "a moved snippet file is read", tool: "get_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet}}", "file": "run.sh"}, pause: 5 * time.Second,
			save: map[string]string{"snippet_at3": "updated_at"}},
		// A title alone commits nothing, so the updated_at its answer gives
		// is the next witness.
		{name: "a snippet's title changes without a commit", tool: "update_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet}}", "updated_at": "{{snippet_at3}}",
			"title": "Live snippet titled " + w}, save: map[string]string{"snippet_at4": "updated_at"}},
		{name: "a snippet's title changes again from the last answer", tool: "update_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet}}", "updated_at": "{{snippet_at4}}",
			"title": "Live snippet titled again " + w}, pause: 3 * time.Second},
		{name: "a one-file snippet's witness is read", tool: "get_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet2}}"}, save: map[string]string{"snippet2_at": "updated_at"}},
		{name: "a one-file snippet's content changes", tool: "update_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet2}}", "updated_at": "{{snippet2_at}}",
			"content": "c, changed\n"}},
		{name: "a changed one-file snippet's witness is read", tool: "get_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet2}}"}, pause: 5 * time.Second,
			save: map[string]string{"snippet2_at2": "updated_at"}},
		{name: "a snippet delete with a stale witness is refused", tool: "delete_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet2}}", "updated_at": "{{snippet2_at}}",
			"confirm": true}, expectError: true},
		{name: "a snippet delete without confirm is refused", tool: "delete_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet2}}", "updated_at": "{{snippet2_at2}}"},
			expectError: true},
		{name: "a snippet delete is previewed", tool: "delete_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet2}}", "updated_at": "{{snippet2_at2}}",
			"dry_run": true}},
		{name: "a snippet is deleted", tool: "delete_snippet", args: map[string]any{"project": p, "snippet_id": "{{snippet2}}", "updated_at": "{{snippet2_at2}}",
			"confirm": true}},

		// Releases: two, so the listing pages.
		{name: "a release create is previewed", tool: "create_release", args: map[string]any{"project": p, "tag_name": tag, "ref": s.Default, "dry_run": true}},
		{name: "a release is created with a new tag", tool: "create_release", args: map[string]any{"project": p, "tag_name": tag, "ref": s.Default, "tag_message": "A live tag",
			"name": "Live release " + w, "description": "Release notes.\n/close stays text.\n", "milestones": []any{s.Milestone},
			"released_at": now.Format(time.RFC3339)}},
		{name: "a second release of one tag is refused", tool: "create_release", args: map[string]any{"project": p, "tag_name": tag}, expectError: true},
		{name: "a release link off the instance is refused", tool: "create_release", args: map[string]any{"project": p, "tag_name": tag2, "ref": s.Default,
			"links": []any{map[string]any{"name": "https://example.invalid/steer", "url": "https://example.invalid/steer"}}},
			expectError: true},
		{name: "a release is created with a link on the instance", tool: "create_release", args: map[string]any{"project": p, "tag_name": tag2, "ref": s.Default, "links": []any{
			map[string]any{"name": "The releases", "url": s.WebURL + "/-/releases", "link_type": "other", "direct_asset_path": "/releases"}}}},
		{name: "releases page one at a time", tool: "list_releases", args: map[string]any{"project": p, "order_by": "created_at", "sort": "desc", "max": 1}, paged: true},
		{name: "a release is read", tool: "get_release", args: map[string]any{"project": p, "tag_name": tag, "offset": 0}},

		// Deployments, from the default branch's pipeline, and activity.
		{name: "an environment is found by name", tool: "list_environments", args: map[string]any{"project": p, "name": "live"}},
		{name: "environments page by state", tool: "list_environments", args: map[string]any{"project": p, "search": "live", "states": "available", "max": 1}, paged: true},
		{name: "deployments page one at a time", tool: "list_deployments", args: map[string]any{"project": p, "order_by": "id", "sort": "desc", "max": 1}, paged: true},
		{name: "deployments are filtered by environment, status and date", tool: "list_deployments", args: map[string]any{"project": p, "environment": "live", "status": "success",
			"updated_after": yesterday, "updated_before": tomorrow}},
		{name: "project events page one at a time", tool: "list_events", args: map[string]any{"project": p, "max": 1}, paged: true},
		{name: "project events are filtered by action and date", tool: "list_events", args: map[string]any{"project": p, "action": "created", "target_type": "issue",
			"after": now.Add(-48 * time.Hour).Format(time.DateOnly), "before": now.Add(48 * time.Hour).Format(time.DateOnly), "sort": "asc"}},
	}, linksMovesAndTags(s))
}

// linksMovesAndTags drives issue moves and links, merge request dependencies,
// labels, milestones, rebases, cherry-picks, reverts, blame, job
// artifacts and tags, on the server with every flag and toolset on.
func linksMovesAndTags(s scratch) []step {
	p := s.Path
	w := s.Name[len(s.Name)-6:]
	pick := "pick-" + w
	stale := strings.Repeat("0", 40)
	return []step{
		// Linking two issues of the run's.
		{name: "an issue link is previewed", tool: "link_issues", args: map[string]any{"project": p, "iid": s.Issue, "target_project": p, "target_iid": s.Issue2,
			"link_type": "relates_to", "dry_run": true}},
		{name: "two issues are linked", tool: "link_issues", args: map[string]any{"project": p, "iid": s.Issue, "target_iid": s.Issue2}, save: map[string]string{"link": "link_id"}},
		// Already linked, seen from the other side: unchanged.
		{name: "a link is made again from the other issue", tool: "link_issues", args: map[string]any{"project": p, "iid": s.Issue2, "target_iid": s.Issue}},
		{name: "an issue unlink is previewed", tool: "unlink_issues", args: map[string]any{"project": p, "iid": s.Issue, "target_project": p, "target_iid": s.Issue2, "dry_run": true}},
		{name: "two issues are unlinked", tool: "unlink_issues", args: map[string]any{"project": p, "iid": s.Issue, "target_iid": s.Issue2}},

		// Making the run's draft wait for its other merge request; the
		// scratch group is on Free, where GitLab records the dependency
		// and does not hold the merge.
		{name: "an MR dependency is previewed", tool: "link_merge_requests", args: map[string]any{"project": p, "iid": s.MR2, "target_project": p, "target_iid": s.MR,
			"link_type": "is_blocked_by", "dry_run": true}},
		{name: "an MR is made to wait for another", tool: "link_merge_requests", args: map[string]any{"project": p, "iid": s.MR2, "target_iid": s.MR, "link_type": "is_blocked_by"}},
		// The same dependency, from the other side: unchanged.
		{name: "a dependency is made again from the other MR", tool: "link_merge_requests", args: map[string]any{"project": p, "iid": s.MR, "target_iid": s.MR2, "link_type": "blocks"}},
		{name: "the waiting MR shows what blocks it", tool: "get_merge_request", args: map[string]any{"project": p, "iid": s.MR2}},
		{name: "the blocking MR shows what it blocks", tool: "get_merge_request", args: map[string]any{"project": p, "iid": s.MR}},
		{name: "an MR dependency removal is previewed", tool: "unlink_merge_requests", args: map[string]any{"project": p, "iid": s.MR2, "target_project": p, "target_iid": s.MR,
			"link_type": "is_blocked_by", "dry_run": true}},
		{name: "an MR dependency is removed", tool: "unlink_merge_requests", args: map[string]any{"project": p, "iid": s.MR2, "target_iid": s.MR, "link_type": "is_blocked_by"}},
		// Gone: unchanged.
		{name: "a removed dependency is removed again from the other MR", tool: "unlink_merge_requests", args: map[string]any{"project": p, "iid": s.MR, "target_iid": s.MR2, "link_type": "blocks"}},

		// Blame.
		{name: "a file's blame is read", tool: "get_blame", args: map[string]any{"project": p, "path": s.File}},
		{name: "a line range's blame is read on a branch", tool: "get_blame", args: map[string]any{"project": p, "path": s.File, "ref": s.Feature, "start_line": 2, "end_line": 3}},

		// Artifacts of the failed job, whose report prints the synthetic token.
		{name: "a job's artifacts are listed", tool: "list_job_artifacts", args: map[string]any{"project": p, "job_id": s.JobFailed}},
		{name: "a job's artifacts page under a path", tool: "list_job_artifacts", args: map[string]any{"project": p, "job_id": s.JobFailed, "path": "reports", "recursive": true, "max": 1},
			paged: true},
		{name: "a file in a job's artifacts is read", tool: "get_job_artifact", args: map[string]any{"project": p, "job_id": s.JobFailed, "path": "reports/summary.txt", "offset": 0}},
		{name: "a file the artifacts lack is refused", tool: "get_job_artifact", args: map[string]any{"project": p, "job_id": s.JobFailed, "path": "reports/missing.txt"},
			expectError: true},

		// Cherry-pick a commit of a side branch onto a new branch, then
		// revert it there. The commit adds a file nothing else touches, so
		// it applies whatever the default branch did since.
		{name: "a branch to pick from is created", tool: "create_branch", args: map[string]any{"project": p, "branch": pick + "-src", "ref": s.Default}},
		{name: "a change to pick is committed", tool: "create_commit", args: map[string]any{"project": p, "branch": pick + "-src", "message": "A change to pick",
			"actions": []any{map[string]any{"action": "create", "file_path": "pick/" + w + ".txt", "content": "picked\n"}}},
			save: map[string]string{"feature_sha": "sha"}},
		{name: "a branch to pick onto is created", tool: "create_branch", args: map[string]any{"project": p, "branch": pick, "ref": s.Default}},
		{name: "a cherry-pick is previewed", tool: "cherry_pick_commit", args: map[string]any{"project": p, "commit": "{{feature_sha}}", "branch": pick, "dry_run": true}},
		{name: "a commit is cherry-picked onto a branch", tool: "cherry_pick_commit", args: map[string]any{"project": p, "commit": "{{feature_sha}}", "branch": pick,
			"message": "Pick the feature change"}, save: map[string]string{"picked": "sha"}},
		{name: "a revert is previewed", tool: "revert_commit", args: map[string]any{"project": p, "commit": "{{picked}}", "branch": pick, "dry_run": true}},
		{name: "a picked commit is reverted", tool: "revert_commit", args: map[string]any{"project": p, "commit": "{{picked}}", "branch": pick}},
		{name: "a cherry-pick onto the default branch is refused", tool: "cherry_pick_commit", args: map[string]any{"project": p, "commit": "{{feature_sha}}", "branch": s.Default},
			expectError: true, why: "the default branch takes code only through a merge request"},

		// Rebase the draft merge request.
		{name: "the draft MR's head is read", tool: "get_merge_request", args: map[string]any{"project": p, "iid": s.MR2}, save: map[string]string{"mr2_sha": "sha"}},
		{name: "a rebase with a stale sha is refused", tool: "rebase_merge_request", args: map[string]any{"project": p, "iid": s.MR2, "sha": stale},
			expectError: true},
		{name: "a rebase is previewed", tool: "rebase_merge_request", args: map[string]any{"project": p, "iid": s.MR2, "sha": "{{mr2_sha}}", "dry_run": true}},
		{name: "a draft MR rebases without CI", tool: "rebase_merge_request", args: map[string]any{"project": p, "iid": s.MR2, "sha": "{{mr2_sha}}", "skip_ci": true}},

		// Move an issue of the run's to its second project.
		{name: "an issue to move is created", tool: "create_issue", args: map[string]any{"project": p, "title": "An issue to move " + s.Name}, save: map[string]string{"to_move": "iid"}},
		{name: "the issue to move is read", tool: "get_issue", args: map[string]any{"project": p, "iid": "{{to_move}}"}, save: map[string]string{"to_move_at": "updated_at"}},
		{name: "an issue move is previewed", tool: "move_issue", args: map[string]any{"project": p, "iid": "{{to_move}}", "to_project": p + "-b", "updated_at": "{{to_move_at}}",
			"dry_run": true}},
		{name: "an issue moves to the run's second project", tool: "move_issue", args: map[string]any{"project": p, "iid": "{{to_move}}", "to_project": p + "-b", "updated_at": "{{to_move_at}}"}},

		// A label and a milestone, made, changed and deleted.
		{name: "a label create is previewed", tool: "create_label", args: map[string]any{"project": p, "name": "live-made-" + w, "color": "#336699", "dry_run": true}},
		{name: "a label is created", tool: "create_label", args: map[string]any{"project": p, "name": "live-made-" + w, "color": "#336699",
			"description": "A label the live run made", "priority": 3}, save: map[string]string{"label": "label.id", "label_v": "label.version"}},
		{name: "a label update with a stale version is refused", tool: "update_label", args: map[string]any{"project": p, "label_id": "{{label}}", "version": stale, "color": "#993366"},
			expectError: true},
		{name: "a label update is previewed", tool: "update_label", args: map[string]any{"project": p, "label_id": "{{label}}", "version": "{{label_v}}", "color": "#993366",
			"dry_run": true}},
		{name: "a label is renamed and recolored", tool: "update_label", args: map[string]any{"project": p, "label_id": "{{label}}", "version": "{{label_v}}", "name": "live-renamed-" + w,
			"color": "#993366", "description": "Renamed by the live run", "priority": 4}, save: map[string]string{"label_v2": "label.version"}},
		{name: "a label's description and priority are cleared", tool: "update_label", args: map[string]any{"project": p, "label_id": "{{label}}", "version": "{{label_v2}}", "clear_description": true,
			"clear_priority": true}, save: map[string]string{"label_v3": "label.version"}},
		{name: "a label delete is previewed", tool: "delete_label", args: map[string]any{"project": p, "label_id": "{{label}}", "version": "{{label_v3}}", "confirm": true,
			"dry_run": true}},
		{name: "a label is deleted", tool: "delete_label", args: map[string]any{"project": p, "label_id": "{{label}}", "version": "{{label_v3}}", "confirm": true}},
		{name: "a milestone create is previewed", tool: "create_milestone", args: map[string]any{"project": p, "title": "Live made " + s.Name, "dry_run": true}},
		{name: "a milestone is created", tool: "create_milestone", args: map[string]any{"project": p, "title": "Live made " + s.Name, "description": "A milestone the live run made",
			"start_date": "2030-01-01", "due_date": "2030-03-31"}, save: map[string]string{"ms": "milestone.id", "ms_at": "milestone.updated_at"}},
		{name: "a milestone update is previewed", tool: "update_milestone", args: map[string]any{"project": p, "milestone_id": "{{ms}}", "updated_at": "{{ms_at}}", "state": "close",
			"dry_run": true}},
		{name: "a milestone is changed and closed", tool: "update_milestone", args: map[string]any{"project": p, "milestone_id": "{{ms}}", "updated_at": "{{ms_at}}",
			"title": "Live changed " + s.Name, "description": "Changed", "start_date": "2030-02-01", "due_date": "2030-04-30", "state": "close"},
			save: map[string]string{"ms_at2": "milestone.updated_at"}},
		{name: "a milestone's description and dates are cleared", tool: "update_milestone", args: map[string]any{"project": p, "milestone_id": "{{ms}}", "updated_at": "{{ms_at2}}",
			"clear_description": true, "clear_start_date": true, "clear_due_date": true}, save: map[string]string{"ms_at3": "milestone.updated_at"}},
		{name: "a milestone delete is previewed", tool: "delete_milestone", args: map[string]any{"project": p, "milestone_id": "{{ms}}", "updated_at": "{{ms_at3}}", "confirm": true,
			"dry_run": true}},
		{name: "a milestone is deleted", tool: "delete_milestone", args: map[string]any{"project": p, "milestone_id": "{{ms}}", "updated_at": "{{ms_at3}}", "confirm": true}},

		// A tag, made and deleted.
		{name: "a tag create is previewed", tool: "create_tag", args: map[string]any{"project": p, "tag_name": "live-tag-" + w, "ref": s.Default, "dry_run": true}},
		{name: "an annotated tag is created", tool: "create_tag", args: map[string]any{"project": p, "tag_name": "live-tag-" + w, "ref": s.Default, "message": "A live tag"},
			save: map[string]string{"tag_sha": "commit_sha"}},
		{name: "a tag delete is previewed", tool: "delete_tag", args: map[string]any{"project": p, "tag_name": "live-tag-" + w, "sha": "{{tag_sha}}", "confirm": true, "dry_run": true}},
		{name: "a tag is deleted", tool: "delete_tag", args: map[string]any{"project": p, "tag_name": "live-tag-" + w, "sha": "{{tag_sha}}", "confirm": true}},
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
// the one expected refusal. The driver reads only what it wrote (D-83).
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
	// A re-request notifies the reviewer, so only the run's own account
	// is asked.
	"rerequest_review": func(st step, s scratch) error {
		if err := inProject(st, s); err != nil {
			return err
		}
		if st.args["username"] != s.User {
			return fmt.Errorf("%s: only the run's own account is asked for a review", st.tool)
		}
		return nil
	},
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
	"upload_file": func(st step, s scratch) error {
		if p := st.args["path"]; p != s.Image && p != s.NotImage && p != s.Outside {
			return fmt.Errorf("%s: only the files the run generated are uploaded", st.tool)
		}
		return inProject(st, s)
	},
	"move_issue": func(st step, s scratch) error {
		if st.args["to_project"] != s.Path+"-b" {
			return fmt.Errorf("%s: an issue moves only to the run's second project", st.tool)
		}
		return inProject(st, s)
	},
	"link_issues":           linkRule,
	"unlink_issues":         linkRule,
	"link_merge_requests":   linkRule,
	"unlink_merge_requests": linkRule,
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
		"retry_pipeline", "retry_job", "play_job", "cancel_pipeline", "delete_branch", "delete_comment", "make_issue_public", "list_wiki_pages", "get_wiki_page", "save_wiki_page",
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
		return fmt.Errorf("%s: the other item is the scratch project's", st.tool)
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
// notes, drafts, commits' projects, to-do items.
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
