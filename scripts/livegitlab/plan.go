package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/scripts/internal/gatekit"
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
}

// plan is every tool, every option at least once, against the scratch
// project. Group and instance-wide searches carry the run's word, so what
// comes back is the run's own; a listing that could name other people
// (members, users, labels a group defines, to-do items) is narrowed to
// the run's account or project.
func plan(s scratch) []step {
	return append(phase0(s), phase1(s)...)
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
	"search_projects":       searchRule,
	"search_issues":         searchRule,
	"search_merge_requests": searchRule,
	"search":                searchRule,
}

func init() {
	for _, tool := range []string{"get_project", "get_issue", "list_discussions", "get_merge_request", "list_mr_files",
		"get_mr_diff", "list_mr_commits", "list_review_comments", "get_file", "list_tree", "list_branches", "list_commits",
		"get_commit", "compare_refs", "list_tags", "list_pipelines", "get_pipeline", "list_jobs", "get_job_log", "lint_ci",
		"list_todos"} {
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
