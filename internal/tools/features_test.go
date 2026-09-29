package tools

import (
	"strings"
	"testing"

	"github.com/mmedum/gitlab-mcp/v2/internal/config"
	"github.com/mmedum/gitlab-mcp/v2/internal/gapi/gitlabtest"
)

// The phase 6 tools against the in-memory instance (§17.13).

var full = config.Config{EnableShip: true, EnableDestructive: true, Toolsets: config.Toolsets}

func TestMoveIssue(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: full})
	_, is := h.ok("get_issue", map[string]any{"project": alpha, "iid": 1})
	at := get(is, "updated_at")
	h.fails("move_issue", map[string]any{"project": alpha, "iid": 1, "to_project": gitlabtest.ProjectBeta,
		"updated_at": "2020-01-01T00:00:00Z"}, "stale")
	_, dry := h.ok("move_issue", map[string]any{"project": alpha, "iid": 1, "to_project": gitlabtest.ProjectBeta, "updated_at": at,
		"dry_run": true})
	if get(dry, "outcome") != "dry_run" || get(dry, "to_visibility") != "private" {
		t.Fatalf("dry run: %v", dry)
	}
	text, out := h.ok("move_issue", map[string]any{"project": alpha, "iid": 1, "to_project": gitlabtest.ProjectBeta, "updated_at": at})
	if get(out, "outcome") != "moved" || get(out, "iid").(float64) < 1 || !strings.Contains(text, "GitLab closed the original") {
		t.Fatalf("move: %v\n%s", out, text)
	}
	if is, _ := h.gl.Issue(alpha, 1); is.State != "closed" || is.MovedToID == nil {
		t.Errorf("the original is %s, moved to %v", is.State, is.MovedToID)
	}
	// From private to public is refused: it would show the issue to
	// people who cannot see it now.
	_, created := h.ok("create_issue", map[string]any{"project": gitlabtest.ProjectBeta, "title": "Private"})
	_, is = h.ok("get_issue", map[string]any{"project": gitlabtest.ProjectBeta, "iid": get(created, "iid")})
	h.fails("move_issue", map[string]any{"project": gitlabtest.ProjectBeta, "iid": get(created, "iid"), "to_project": alpha,
		"updated_at": get(is, "updated_at")}, "blocked")
	// Both projects are held to the allow-list.
	confined := newHarness(t, harnessOptions{cfg: config.Config{EnableShip: true, WriteNamespaces: []string{alpha}}})
	_, is = confined.ok("get_issue", map[string]any{"project": alpha, "iid": 2})
	confined.fails("move_issue", map[string]any{"project": alpha, "iid": 2, "to_project": gitlabtest.ProjectBeta,
		"updated_at": get(is, "updated_at")}, "blocked")
}

func TestLinkAndUnlinkIssues(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	args := map[string]any{"project": alpha, "iid": 1, "target_iid": 2}
	_, out := h.ok("link_issues", args)
	if get(out, "outcome") != "linked" || get(out, "link_type") != "relates_to" || get(out, "link_id").(float64) == 0 {
		t.Fatalf("link: %v", out)
	}
	if _, again := h.ok("link_issues", args); get(again, "outcome") != "unchanged" || h.gl.IssueLinks() != 1 {
		t.Errorf("again: %v, %d links", again, h.gl.IssueLinks())
	}
	// Seen from the other issue, the same link.
	if _, other := h.ok("unlink_issues", map[string]any{"project": alpha, "iid": 2, "target_iid": 1, "dry_run": true}); get(other, "link_id") != get(out, "link_id") {
		t.Errorf("from the other side: %v", other)
	}
	if _, un := h.ok("unlink_issues", args); get(un, "outcome") != "unlinked" || h.gl.IssueLinks() != 0 {
		t.Errorf("unlink: %v", un)
	}
	if _, un := h.ok("unlink_issues", args); get(un, "outcome") != "unchanged" {
		t.Errorf("unlink again: %v", un)
	}
	h.fails("link_issues", map[string]any{"project": alpha, "iid": 1, "target_iid": 1}, "invalid")
	h.fails("link_issues", map[string]any{"project": alpha, "iid": 1, "target_iid": 2, "link_type": "duplicates"}, "invalid")
	_, cross := h.ok("link_issues", map[string]any{"project": alpha, "iid": 1, "target_project": gitlabtest.ProjectBeta, "target_iid": 1})
	if get(cross, "target_project", "path") != gitlabtest.ProjectBeta {
		t.Errorf("cross-project: %v", cross)
	}
}

func TestLabelWrites(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: full})
	_, out := h.ok("create_label", map[string]any{"project": alpha, "name": "triage", "color": "#aa0000", "description": "Needs a look",
		"priority": 2})
	id, version := get(out, "label", "id"), get(out, "label", "version").(string)
	if get(out, "outcome") != "created" || version == "" {
		t.Fatalf("create: %v", out)
	}
	h.fails("update_label", map[string]any{"project": alpha, "label_id": id, "version": "0000", "color": "#00aa00"}, "stale")
	_, up := h.ok("update_label", map[string]any{"project": alpha, "label_id": id, "version": version, "color": "#00aa00"})
	if get(up, "outcome") != "updated" || strings.Join(strs(get(up, "changed")), ",") != "color" {
		t.Fatalf("update: %v", up)
	}
	version = get(up, "label", "version").(string)
	if _, same := h.ok("update_label", map[string]any{"project": alpha, "label_id": id, "version": version, "color": "#00aa00"}); get(same, "outcome") != "unchanged" {
		t.Errorf("no change: %v", same)
	}
	// A clear_ input removes the field, and refuses a value beside it.
	h.fails("update_label", map[string]any{"project": alpha, "label_id": id, "version": version, "description": "x", "clear_description": true}, "invalid")
	h.fails("update_label", map[string]any{"project": alpha, "label_id": id, "version": version, "priority": 1, "clear_priority": true}, "invalid")
	_, dry := h.ok("update_label", map[string]any{"project": alpha, "label_id": id, "version": version, "clear_priority": true, "dry_run": true})
	if get(dry, "outcome") != "dry_run" || strings.Join(strs(get(dry, "would_send", "fields")), ",") != "priority" {
		t.Errorf("dry run clear: %v", dry)
	}
	_, cleared := h.ok("update_label", map[string]any{"project": alpha, "label_id": id, "version": version, "clear_description": true,
		"clear_priority": true})
	if strings.Join(strs(get(cleared, "changed")), ",") != "description,priority" || get(cleared, "label", "priority") != nil ||
		get(cleared, "label", "untrusted_description") != "" {
		t.Fatalf("clear: %v", cleared)
	}
	version = get(cleared, "label", "version").(string)
	if _, again := h.ok("update_label", map[string]any{"project": alpha, "label_id": id, "version": version, "clear_description": true,
		"clear_priority": true}); get(again, "outcome") != "unchanged" {
		t.Errorf("clear again: %v", again)
	}
	// list_labels gives the same version.
	_, list := h.ok("list_labels", map[string]any{"project": alpha, "search": "triage"})
	if get(list, "labels", 0, "version") != version {
		t.Errorf("list_labels version %v, want %s", get(list, "labels", 0, "version"), version)
	}
	h.fails("delete_label", map[string]any{"project": alpha, "label_id": id, "version": version}, "blocked")
	if _, del := h.ok("delete_label", map[string]any{"project": alpha, "label_id": id, "version": version, "confirm": true}); get(del, "outcome") != "deleted" {
		t.Errorf("delete: %v", del)
	}
}

func TestMilestoneWrites(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: full})
	_, out := h.ok("create_milestone", map[string]any{"project": alpha, "title": "Q4", "due_date": "2026-12-31", "start_date": "2026-10-01",
		"description": "The fourth quarter"})
	id, at := get(out, "milestone", "id"), get(out, "milestone", "updated_at")
	if get(out, "outcome") != "created" || get(out, "milestone", "due_date") != "2026-12-31" {
		t.Fatalf("create: %v", out)
	}
	h.fails("create_milestone", map[string]any{"project": alpha, "title": "Bad", "due_date": "31/12/2026"}, "invalid")
	h.fails("update_milestone", map[string]any{"project": alpha, "milestone_id": id, "updated_at": "2020-01-01T00:00:00Z", "state": "close"}, "stale")
	_, up := h.ok("update_milestone", map[string]any{"project": alpha, "milestone_id": id, "updated_at": at, "state": "close"})
	if get(up, "outcome") != "updated" || get(up, "milestone", "state") != "closed" {
		t.Fatalf("close: %v", up)
	}
	at = get(up, "milestone", "updated_at")
	for _, pair := range [][2]string{{"due_date", "2027-01-31"}, {"start_date", "2027-01-01"}, {"description", "x"}} {
		h.fails("update_milestone", map[string]any{"project": alpha, "milestone_id": id, "updated_at": at, pair[0]: pair[1],
			"clear_" + pair[0]: true}, "invalid")
	}
	_, cleared := h.ok("update_milestone", map[string]any{"project": alpha, "milestone_id": id, "updated_at": at, "clear_description": true,
		"clear_start_date": true, "clear_due_date": true})
	if strings.Join(strs(get(cleared, "changed")), ",") != "description,due_date,start_date" ||
		get(cleared, "milestone", "due_date") != nil || get(cleared, "milestone", "start_date") != nil {
		t.Fatalf("clear: %v", cleared)
	}
	_, del := h.ok("delete_milestone", map[string]any{"project": alpha, "milestone_id": id, "updated_at": get(cleared, "milestone", "updated_at"),
		"confirm": true})
	if get(del, "outcome") != "deleted" {
		t.Errorf("delete: %v", del)
	}
}

func TestRebaseMergeRequest(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: full})
	_, mr := h.ok("get_merge_request", map[string]any{"project": alpha, "iid": 1})
	sha := get(mr, "sha").(string)
	h.fails("rebase_merge_request", map[string]any{"project": alpha, "iid": 1, "sha": strings.Repeat("0", 40)}, "stale")
	_, out := h.ok("rebase_merge_request", map[string]any{"project": alpha, "iid": 1, "sha": sha})
	if get(out, "outcome") != "started" || h.gl.Rebases() != 1 {
		t.Errorf("rebase: %v, %d rebases", out, h.gl.Rebases())
	}
}

func TestCherryPickAndRevert(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	_, head := h.ok("list_commits", map[string]any{"project": alpha, "ref": "main", "max": 1})
	sha := get(head, "commits", 0, "id").(string)
	_, dry := h.ok("cherry_pick_commit", map[string]any{"project": alpha, "commit": sha, "branch": "feature/login", "dry_run": true})
	if get(dry, "applies") != true {
		t.Errorf("dry run: %v", dry)
	}
	text, conflict := h.ok("cherry_pick_commit", map[string]any{"project": alpha, "commit": gitlabtest.ConflictSHA, "branch": "feature/login",
		"dry_run": true})
	if get(conflict, "applies") != false || !strings.Contains(text, "does not apply") {
		t.Errorf("conflict: %v\n%s", conflict, text)
	}
	_, out := h.ok("cherry_pick_commit", map[string]any{"project": alpha, "commit": sha, "branch": "feature/login"})
	if get(out, "outcome") != "created" || get(out, "sha") == "" {
		t.Fatalf("pick: %v", out)
	}
	if head, _ := h.gl.BranchHead(alpha, "feature/login"); head.ID != get(out, "sha") {
		t.Errorf("the branch head is %s", head.ID)
	}
	_, rev := h.ok("revert_commit", map[string]any{"project": alpha, "commit": get(out, "sha"), "branch": "feature/login"})
	if get(rev, "outcome") != "created" || !strings.HasPrefix(get(rev, "untrusted_title").(string), "Revert") {
		t.Errorf("revert: %v", rev)
	}
	h.fails("cherry_pick_commit", map[string]any{"project": alpha, "commit": gitlabtest.ConflictSHA, "branch": "feature/login"}, "conflict")
	h.fails("cherry_pick_commit", map[string]any{"project": alpha, "commit": sha, "branch": "main"}, "blocked")
	h.fails("revert_commit", map[string]any{"project": alpha, "commit": sha, "branch": "release/1.0"}, "blocked")
}

func TestGetBlame(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, out := h.ok("get_blame", map[string]any{"project": alpha, "path": "README.md"})
	ranges := get(out, "ranges").([]any)
	if len(ranges) == 0 || get(out, "ranges", 0, "start_line") != float64(1) || get(out, "ref") != "main" ||
		!strings.Contains(text, "UNTRUSTED") {
		t.Fatalf("blame: %v\n%s", out, text)
	}
	_, part := h.ok("get_blame", map[string]any{"project": alpha, "path": "README.md", "start_line": 2, "end_line": 3})
	if get(part, "ranges", 0, "start_line") != float64(2) {
		t.Errorf("from line 2: %v", part)
	}
	h.fails("get_blame", map[string]any{"project": alpha, "path": "README.md", "start_line": 3, "end_line": 2}, "invalid")
}

func TestJobArtifacts(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	_, top := h.ok("list_job_artifacts", map[string]any{"project": alpha, "job_id": gitlabtest.JobFailed})
	if get(top, "entries", 0, "type") != "directory" {
		t.Errorf("top: %v", top)
	}
	_, all := h.ok("list_job_artifacts", map[string]any{"project": alpha, "job_id": gitlabtest.JobFailed, "recursive": true})
	if len(get(all, "entries").([]any)) != 3 {
		t.Errorf("recursive: %v", all)
	}
	text, report := h.ok("get_job_artifact", map[string]any{"project": alpha, "job_id": gitlabtest.JobFailed, "path": "reports/junit.xml"})
	content := get(report, "untrusted_content").(string)
	if strings.Contains(content+text, gitlabtest.FakeToken) || get(report, "secrets_masked") != float64(1) {
		t.Errorf("report: %v\n%s", report, text)
	}
	_, summary := h.ok("get_job_artifact", map[string]any{"project": alpha, "job_id": gitlabtest.JobFailed, "path": "reports/summary.txt"})
	if c := get(summary, "untrusted_content").(string); strings.Contains(c, gitlabtest.FakeToken) || strings.Contains(c, "\x1b") {
		t.Errorf("a colored token escaped the masks: %q", c)
	}
	if _, bin := h.ok("get_job_artifact", map[string]any{"project": alpha, "job_id": gitlabtest.JobFailed, "path": "bin/app"}); get(bin, "binary") != true {
		t.Errorf("binary: %v", bin)
	}
	h.fails("get_job_artifact", map[string]any{"project": alpha, "job_id": gitlabtest.JobFailed, "path": "missing.txt"}, "not_found")
}

func TestTagWrites(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: full})
	_, out := h.ok("create_tag", map[string]any{"project": alpha, "tag_name": "v9", "ref": "main", "message": "Nine"})
	if get(out, "outcome") != "created" || get(out, "commit_sha") == "" {
		t.Fatalf("create: %v", out)
	}
	h.fails("create_tag", map[string]any{"project": alpha, "tag_name": "v9", "ref": "main"}, "conflict")
	h.fails("create_tag", map[string]any{"project": alpha, "tag_name": "release-2", "ref": "main"}, "blocked")
	h.fails("delete_tag", map[string]any{"project": alpha, "tag_name": "v9", "sha": strings.Repeat("0", 40), "confirm": true}, "stale")
	_, del := h.ok("delete_tag", map[string]any{"project": alpha, "tag_name": "v9", "sha": get(out, "commit_sha"), "confirm": true})
	if get(del, "outcome") != "deleted" {
		t.Errorf("delete: %v", del)
	}
	_, tags := h.ok("list_tags", map[string]any{"project": alpha})
	var protectedSHA any
	for _, tg := range get(tags, "tags").([]any) {
		if tg.(map[string]any)["name"] == gitlabtest.TagRelease {
			protectedSHA = tg.(map[string]any)["commit_id"]
		}
	}
	h.fails("delete_tag", map[string]any{"project": alpha, "tag_name": gitlabtest.TagRelease, "sha": protectedSHA, "confirm": true}, "blocked")
}

// A blame window this server chose that comes back full says where to go
// on, and one run of lines over the budget is cut, not shown whole.
func TestGetBlameWindows(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	commit := func(path, content string) {
		t.Helper()
		h.ok("create_commit", map[string]any{"project": alpha, "branch": "feature/login", "message": "Add " + path,
			"actions": []any{map[string]any{"action": "create", "file_path": path, "content": content}}})
	}
	commit("gen/many.txt", strings.Repeat("x\n", 5000))
	_, out := h.ok("get_blame", map[string]any{"project": alpha, "path": "gen/many.txt", "ref": "feature/login", "start_line": 2001})
	if get(out, "next_line") != float64(4001) {
		t.Errorf("a full window: next_line %v", get(out, "next_line"))
	}
	long := strings.Repeat("y", 40000) + "\n"
	commit("gen/long.txt", long+long+long)
	_, out = h.ok("get_blame", map[string]any{"project": alpha, "path": "gen/long.txt", "ref": "feature/login"})
	if get(out, "next_line") != float64(2) || get(out, "ranges", 0, "end_line") != float64(1) {
		t.Errorf("an oversized run: next_line %v, ranges %v", get(out, "next_line"), len(get(out, "ranges").([]any)))
	}
}

func TestCreateLabelThatExists(t *testing.T) {
	h := newHarness(t, harnessOptions{cfg: full})
	h.ok("create_label", map[string]any{"project": alpha, "name": "twice", "color": "red"})
	h.fails("create_label", map[string]any{"project": alpha, "name": "twice", "color": "red"}, "conflict")
}
