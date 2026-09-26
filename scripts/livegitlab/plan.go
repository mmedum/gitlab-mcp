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
}

// plan is every phase-0 tool, every option at least once, against the
// scratch project. Group and instance-wide searches carry the run's
// word, so what comes back is the run's own.
func plan(s scratch) []step {
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
	scoped := st.args["project"] != nil || st.args["group"] != nil
	if strings.HasPrefix(st.tool, "search_") && !scoped && st.args["search"] != s.Name {
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
