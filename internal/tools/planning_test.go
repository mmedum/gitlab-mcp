package tools

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/mmedum/gitlab-mcp/v2/internal/gapi/gitlabtest"
)

// The planning and navigation reads against the in-memory instance
// (gitlabtest.fillPlanning).

func column(out map[string]any, list, field string) string {
	var got []string
	for _, row := range get(out, list).([]any) {
		got = append(got, fmt.Sprint(row.(map[string]any)[field]))
	}
	return strings.Join(got, ",")
}

func TestListLabels(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, out := h.ok("list_labels", map[string]any{"project": gitlabtest.ProjectAlpha})
	if got := column(out, "labels", "name"); got != "bug,feature,docs,priority::high,"+gitlabtest.GroupLabel {
		t.Errorf("labels = %s", got)
	}
	if get(out, "labels", 0, "open_issues") != nil || get(out, "labels", 4, "project_label") != false {
		t.Errorf("labels = %v", get(out, "labels"))
	}
	if !strings.Contains(text, "- group-wide (id 96100, from a group") {
		t.Errorf("text:\n%s", text)
	}
	_, own := h.ok("list_labels", map[string]any{"project": gitlabtest.ProjectAlpha, "project_only": true, "with_counts": true,
		"search": "bug"})
	// Of the 25 generated issues, labels cycle through five sets and every
	// fourth is closed: bug is on 1, 3, 6, 8, 11, … and 4, 8, … are closed.
	if column(own, "labels", "name") != "bug" || get(own, "labels", 0, "open_issues") != float64(8) ||
		get(own, "labels", 0, "closed_issues") != float64(2) {
		t.Errorf("counted = %v", get(own, "labels"))
	}
}

func TestListMilestones(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, out := h.ok("list_milestones", map[string]any{"project": gitlabtest.ProjectAlpha})
	if column(out, "milestones", "untrusted_title") != "Sprint 2,Sprint 1" || get(out, "group") != nil {
		t.Errorf("milestones = %v", get(out, "milestones"))
	}
	if !strings.Contains(text, "closed, expired, starts 2025-12-29, due 2026-01-09") {
		t.Errorf("text:\n%s", text)
	}
	_, all := h.ok("list_milestones", map[string]any{"project": gitlabtest.ProjectAlpha, "include_ancestors": true, "state": "active"})
	if column(all, "milestones", "id") != fmt.Sprintf("%d,%d", gitlabtest.MilestoneActive, gitlabtest.MilestoneGroup) {
		t.Errorf("with ancestors = %v", get(all, "milestones"))
	}
	text, group := h.ok("list_milestones", map[string]any{"group": gitlabtest.GroupTop, "title": "Q1 goals"})
	if column(group, "milestones", "id") != fmt.Sprint(gitlabtest.MilestoneGroup) || get(group, "project") != nil ||
		!strings.Contains(text, "Milestones of the group example-group") {
		t.Errorf("group = %v\n%s", group, text)
	}
	_, found := h.ok("list_milestones", map[string]any{"project": gitlabtest.ProjectAlpha, "search": "sprint 1"})
	if column(found, "milestones", "id") != fmt.Sprint(gitlabtest.MilestoneClosed) {
		t.Errorf("search = %v", get(found, "milestones"))
	}
	h.fails("list_milestones", map[string]any{}, "invalid")
	h.fails("list_milestones", map[string]any{"project": gitlabtest.ProjectAlpha, "group": gitlabtest.GroupTop}, "invalid")
}

func TestListMembers(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, out := h.ok("list_members", map[string]any{"project": gitlabtest.ProjectAlpha})
	if column(out, "members", "role") != "maintainer,developer,owner" || get(out, "members", 2, "access_level") != float64(50) {
		t.Errorf("members = %v", get(out, "members"))
	}
	if !strings.Contains(text, "- @carol (Carol Example): owner") {
		t.Errorf("text:\n%s", text)
	}
	_, one := h.ok("list_members", map[string]any{"project": gitlabtest.ProjectAlpha, "query": "bo"})
	if column(one, "members", "username") != "bob" {
		t.Errorf("query = %v", get(one, "members"))
	}
}

func TestFindUsers(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, out := h.ok("find_users", map[string]any{"username": "bob"})
	if column(out, "users", "id") != "1002" || !strings.Contains(text, "- @bob (Bob Example), user id 1002") {
		t.Errorf("exact = %v\n%s", out, text)
	}
	_, some := h.ok("find_users", map[string]any{"search": "o"})
	if column(some, "users", "username") != "bob,carol" {
		t.Errorf("search = %v", get(some, "users"))
	}
	h.fails("find_users", map[string]any{}, "invalid")
	h.fails("find_users", map[string]any{"username": "bob", "search": "b"}, "invalid")
}

func TestListTodos(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, out := h.ok("list_todos", nil)
	if column(out, "todos", "id") != fmt.Sprintf("%d,%d", gitlabtest.TodoReview, gitlabtest.TodoAssigned) {
		t.Fatalf("pending = %v", get(out, "todos"))
	}
	if get(out, "todos", 0, "target_iid") != float64(1) || get(out, "todos", 0, "project", "path") != gitlabtest.ProjectAlpha {
		t.Errorf("todo = %v", get(out, "todos", 0))
	}
	if !strings.Contains(text, "review_requested, MergeRequest iid 1 in example-group/alpha") {
		t.Errorf("text:\n%s", text)
	}
	// A commit's to-do target carries a SHA as its id, which must not
	// fail the listing.
	_, done := h.ok("list_todos", map[string]any{"state": "done", "project": gitlabtest.ProjectAlpha})
	if column(done, "todos", "id") != fmt.Sprintf("%d,%d", gitlabtest.TodoDone+2, gitlabtest.TodoDone) ||
		get(done, "todos", 1, "untrusted_body") != "@alice can you look?" || get(done, "todos", 0, "target_iid") != nil {
		t.Errorf("done = %v", get(done, "todos"))
	}
	_, filtered := h.ok("list_todos", map[string]any{"action": "assigned", "type": "Issue"})
	if column(filtered, "todos", "id") != fmt.Sprint(gitlabtest.TodoAssigned) {
		t.Errorf("filtered = %v", get(filtered, "todos"))
	}
	_, other := h.ok("list_todos", map[string]any{"project": gitlabtest.ProjectBeta})
	if get(other, "listing", "returned") != float64(0) {
		t.Errorf("beta = %v", get(other, "todos"))
	}
}

func TestSearch(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	_, issues := h.ok("search", map[string]any{"scope": "issues", "search": "Generated issue 12", "project": gitlabtest.ProjectAlpha})
	if column(issues, "rows", "iid") != "12" || get(issues, "where") != "project" || get(issues, "rows", 0, "kind") != "issue" {
		t.Errorf("issues = %v", issues)
	}
	text, blobs := h.ok("search", map[string]any{"scope": "blobs", "search": "stub", "project": gitlabtest.ProjectAlpha, "ref": "main"})
	if column(blobs, "rows", "path") != "src/util/strings.go" || get(blobs, "rows", 0, "start_line") != float64(2) {
		t.Errorf("blobs = %v", get(blobs, "rows"))
	}
	if !strings.Contains(text, "kind=search_blob item=src/util/strings.go") || !strings.Contains(text, "// Upper is a stub.") {
		t.Errorf("text:\n%s", text)
	}
	_, notes := h.ok("search", map[string]any{"scope": "notes", "search": "looks fine", "project": gitlabtest.ProjectAlpha, "max": 1})
	if get(notes, "rows", 0, "kind") != "note" || get(notes, "rows", 0, "untrusted_excerpt") != "Looks fine to me." ||
		get(notes, "listing", "complete") != false {
		t.Errorf("notes = %v", notes)
	}
	_, commits := h.ok("search", map[string]any{"scope": "commits", "search": "prepare release", "project": gitlabtest.ProjectAlpha,
		"ref": "release/1.0"})
	if get(commits, "rows", 0, "kind") != "commit" || get(commits, "rows", 0, "untrusted_title") != "Prepare release" ||
		get(commits, "rows", 0, "project_id") != float64(alphaID) {
		t.Errorf("commits = %v", get(commits, "rows"))
	}
	_, users := h.ok("search", map[string]any{"scope": "users", "search": "bob"})
	if column(users, "rows", "author") != "bob" || get(users, "where") != "instance" {
		t.Errorf("users = %v", users)
	}
	_, projects := h.ok("search", map[string]any{"scope": "projects", "search": "alpha", "group": gitlabtest.GroupTop})
	if column(projects, "rows", "path") != gitlabtest.ProjectAlpha {
		t.Errorf("projects = %v", get(projects, "rows"))
	}
	_, mrs := h.ok("search", map[string]any{"scope": "merge_requests", "search": "change 2", "state": "opened"})
	if column(mrs, "rows", "iid") != "2" {
		t.Errorf("merge requests = %v", get(mrs, "rows"))
	}
	_, milestones := h.ok("search", map[string]any{"scope": "milestones", "search": "sprint", "project": gitlabtest.ProjectAlpha})
	if get(milestones, "listing", "returned") != float64(2) {
		t.Errorf("milestones = %v", get(milestones, "rows"))
	}

	text = h.fails("search", map[string]any{"scope": "blobs", "search": "stub", "group": gitlabtest.GroupTop}, "unsupported")
	if !strings.Contains(text, "advanced search across a group") || !strings.Contains(text, "inside one project") {
		t.Errorf("refusal: %s", text)
	}
	h.fails("search", map[string]any{"scope": "commits", "search": "stub"}, "unsupported")
	h.fails("search", map[string]any{"scope": "issues", "search": " "}, "invalid")
	h.fails("search", map[string]any{"scope": "blobs", "search": "stub", "ref": "main"}, "invalid")
	h.fails("search", map[string]any{"scope": "issues", "search": "x", "project": gitlabtest.ProjectAlpha, "group": gitlabtest.GroupTop}, "invalid")
	h.fails("search", map[string]any{"scope": "snippet_titles", "search": "x"}, "invalid")
}

// The two listings the live driver cannot page (testdata/live-cover.tsv)
// page here.
func TestFindUsersAndMembersPage(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	for tool, args := range map[string]map[string]any{
		"find_users":   {"search": "o", "max": 1},
		"list_members": {"project": gitlabtest.ProjectAlpha, "max": 2},
	} {
		_, first := h.ok(tool, args)
		tok, _ := get(first, "listing", "next_page_token").(string)
		if tok == "" {
			t.Fatalf("%s: no second page: %v", tool, get(first, "listing"))
		}
		args["page_token"] = tok
		_, second := h.ok(tool, args)
		if get(second, "listing", "complete") != true || get(second, "listing", "returned") != float64(1) {
			t.Errorf("%s: second page = %v", tool, get(second, "listing"))
		}
	}
}

// Alpha's boards (gitlabtest.fillBoards): one with a list of every kind at
// positions GitLab's order does not follow, one scoped to a milestone, a
// label and a weight, one scoped to the Upcoming filter.
func TestListBoards(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, out := h.ok("list_boards", map[string]any{"project": gitlabtest.ProjectAlpha})
	if column(out, "boards", "id") != fmt.Sprintf("%d,%d,%d", gitlabtest.BoardDevelopment, gitlabtest.BoardSprint,
		gitlabtest.BoardUpcoming) || get(out, "listing", "complete") != true {
		t.Errorf("boards = %v", get(out, "boards"))
	}
	dev := get(out, "boards", 0).(map[string]any)
	// Board order is by position; GitLab answered by kind first.
	if got := column(dev, "lists", "kind"); got != "label,assignee,iteration,label,milestone,unknown" {
		t.Errorf("kinds in board order = %s", got)
	}
	if column(dev, "lists", "position") != "0,1,2,3,4,5" || get(dev, "scope") != nil ||
		get(dev, "open_list") != true || get(dev, "closed_list") != true {
		t.Errorf("development = %v", dev)
	}
	want := []string{
		`map[assignee:<nil> labels:[bug] state:opened untrusted_milestone:<nil>]`,
		`map[assignee:bob labels:[] state:opened untrusted_milestone:<nil>]`,
		`<nil>`,
		`map[assignee:<nil> labels:[feature] state:opened untrusted_milestone:<nil>]`,
		`map[assignee:<nil> labels:[] state:opened untrusted_milestone:Sprint 2]`,
		`<nil>`,
	}
	for i, w := range want {
		if got := fmt.Sprint(get(dev, "lists", i, "search_issues")); got != w {
			t.Errorf("list %d search_issues = %s, want %s", i, got, w)
		}
	}
	if get(dev, "lists", 1, "assignee") != "bob" || get(dev, "lists", 2, "iteration", "untrusted_title") != "Iteration 1" ||
		get(dev, "lists", 4, "milestone", "id") != float64(gitlabtest.MilestoneActive) {
		t.Errorf("list values = %v", get(dev, "lists"))
	}
	for _, s := range []string{
		"- label bug (list id 98012, position 0): search_issues state opened, labels bug.",
		"- assignee @bob (list id 98013, position 1): search_issues state opened, assignee bob.",
		"(list id 98015, iteration id 98100, position 2): search_issues has no filter for it.",
		"- a list of unknown kind, perhaps a status list (list id 98016, position 5): search_issues has no filter for it.",
		"- Open: open issues in none of the lists below.",
		"- Closed: every closed issue; search_issues state closed.",
		"GitLab returns them by kind first",
	} {
		if !strings.Contains(text, s) {
			t.Errorf("text lacks %q:\n%s", s, text)
		}
	}

	// A board scoped to a milestone: its label lists carry it.
	sprint := get(out, "boards", 1).(map[string]any)
	if fmt.Sprint(get(sprint, "scope")) != "map[assignee:<nil> labels:[bug] no_weight:false untrusted_milestone:Sprint 2 weight:3]" ||
		get(sprint, "closed_list") != false || get(sprint, "lists", 0, "search_issues", "untrusted_milestone") != "Sprint 2" {
		t.Errorf("sprint = %v", sprint)
	}
	if !strings.Contains(text, "- Closed: hidden on this board.") || !strings.Contains(text, ", labels bug, weight 3.") {
		t.Errorf("text:\n%s", text)
	}
	// GitLab's Upcoming filter is named as search_issues takes it.
	up := get(out, "boards", 2).(map[string]any)
	if fmt.Sprint(get(up, "scope")) != "map[assignee:carol labels:[] no_weight:true untrusted_milestone:Upcoming weight:<nil>]" ||
		get(up, "open_list") != false || len(get(up, "lists").([]any)) != 0 {
		t.Errorf("upcoming = %v", up)
	}
	// Board names and milestone and iteration titles are inside the
	// boundary; a marker in a name is defused.
	if !regexp.MustCompile(`Board 98002: <<<[0-9a-f]{16}>>>Sprint << <2>>> board<<</[0-9a-f]{16}>>>`).MatchString(text) ||
		!regexp.MustCompile(`milestone <<<[0-9a-f]{16}>>>Sprint 2<<</[0-9a-f]{16}>>> \(list id 98014, milestone id 90001, position 4\)`).MatchString(text) ||
		!strings.Contains(text, "was written by GitLab users") {
		t.Errorf("untrusted text:\n%s", text)
	}

	_, page := h.ok("list_boards", map[string]any{"project": gitlabtest.ProjectAlpha, "max": 2})
	if column(page, "boards", "id") != fmt.Sprintf("%d,%d", gitlabtest.BoardDevelopment, gitlabtest.BoardSprint) ||
		get(page, "listing", "next_page_token") == nil {
		t.Errorf("page = %v", page)
	}
	text, empty := h.ok("list_boards", map[string]any{"project": gitlabtest.ProjectBeta})
	if len(get(empty, "boards").([]any)) != 0 || !strings.Contains(text, "0 boards shown; the listing is complete (total 0).\nThe project has no board yet") ||
		strings.Contains(text, "Open") {
		t.Errorf("empty = %v\n%s", empty, text)
	}
	h.fails("list_boards", map[string]any{"project": gitlabtest.ProjectSecret}, "not_found")
}
