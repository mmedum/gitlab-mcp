package gitlabtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/mmedum/gitlab-mcp/internal/gitlab"
)

// The write half of the instance: issues, merge requests, comments,
// threads, drafts, branches, commits, to-do items and the content lint.
// Each acts as the token's user, and a private project the user cannot
// see answers 404 before any of it runs, as a read does. Error bodies
// follow GitLab's three shapes (§2.14): Grape's {"error": "x is
// missing"} for a parameter, {"message": …} from a model or a service.

// ------------------------------------------------------------ bodies

// body is a decoded JSON request, kept raw so a handler can tell a field
// sent as null or empty from one not sent: GitLab's PUT changes only
// what it is sent.
type body map[string]json.RawMessage

// readBody decodes a JSON object; an empty body is an empty object.
func readBody(w http.ResponseWriter, r *http.Request) (body, bool) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "the request body could not be read"})
		return nil, false
	}
	b := body{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return b, true
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "the request body is not valid JSON"})
		return nil, false
	}
	return b, true
}

func (b body) has(k string) bool { _, ok := b[k]; return ok }

func (b body) isNull(k string) bool { return string(bytes.TrimSpace(b[k])) == "null" }

// str reads a string field; a number or a boolean is read as its text,
// as Grape coerces one.
func (b body) str(k string) (string, bool) {
	raw, ok := b[k]
	if !ok || b.isNull(k) {
		return "", ok
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	return string(bytes.TrimSpace(raw)), true
}

func (b body) boolean(k string) (bool, bool) {
	s, ok := b.str(k)
	v, err := strconv.ParseBool(s)
	return v, ok && err == nil
}

func (b body) integer(k string) (int64, bool) {
	s, ok := b.str(k)
	if !ok {
		return 0, false
	}
	if s == "" {
		return 0, true
	}
	v, err := strconv.ParseInt(s, 10, 64)
	return v, err == nil
}

// strs reads a list: a JSON array, or one comma-separated string.
func (b body) strs(k string) ([]string, bool) {
	raw, ok := b[k]
	if !ok {
		return nil, false
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list, true
	}
	s, _ := b.str(k)
	for part := range strings.SplitSeq(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			list = append(list, part)
		}
	}
	return list, true
}

func (b body) ints(k string) ([]int64, bool) {
	raw, ok := b[k]
	if !ok {
		return nil, false
	}
	var list []int64
	if json.Unmarshal(raw, &list) == nil {
		return list, true
	}
	var one int64
	if json.Unmarshal(raw, &one) == nil {
		return []int64{one}, true
	}
	return []int64{}, true
}

// require answers Grape's 400 for required fields that are absent or
// blank, naming every one as Grape does.
func (b body) require(w http.ResponseWriter, names ...string) bool {
	var missing []string
	for _, n := range names {
		if v, ok := b.str(n); !ok || (strings.TrimSpace(v) == "" && !strings.HasPrefix(string(b[n]), "[")) {
			missing = append(missing, n+" is missing")
		}
	}
	if len(missing) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": strings.Join(missing, ", ")})
		return false
	}
	return true
}

// valid answers Grape's 400 for a field outside its allowed values.
func (b body) valid(w http.ResponseWriter, name string, allowed ...string) bool {
	if v, ok := b.str(name); ok && !slices.Contains(allowed, v) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": name + " does not have a valid value"})
		return false
	}
	return true
}

// ------------------------------------------------------------ routing

// serveProjectWrite routes the writes under a project that do not hang
// off one issue or merge request; it reports whether it answered.
func (s *Server) serveProjectWrite(w http.ResponseWriter, r *http.Request, p *project, user string, seg []string) bool {
	if r.Method == http.MethodDelete && match(seg, "repository", "branches", "*") {
		s.deleteBranch(w, p, seg[2])
		return true
	}
	if s.serveToolsetWrite(w, r, p, user, seg) {
		return true
	}
	if r.Method != http.MethodPost {
		return false
	}
	switch {
	case s.serveCIWrite(w, r, p, user, seg):
	case match(seg, "issues"):
		s.createIssue(w, r, p, user)
	case match(seg, "merge_requests"):
		s.createMR(w, r, p, user)
	case match(seg, "repository", "branches"):
		s.createBranch(w, r, p)
	case match(seg, "repository", "commits"):
		s.createCommit(w, r, p, user)
	case match(seg, "ci", "lint"):
		s.lintContent(w, r)
	default:
		return false
	}
	return true
}

// serveIssueWrite routes the writes on one issue.
func (s *Server) serveIssueWrite(w http.ResponseWriter, r *http.Request, p *project, iss *gitlab.Issue, user string, rest []string) bool {
	t := issueTarget(iss)
	switch {
	case r.Method == http.MethodPut && len(rest) == 0:
		s.updateIssue(w, r, p, iss, user)
	case r.Method == http.MethodPost && match(rest, "notes"):
		s.createNote(w, r, p, t, user)
	case r.Method == http.MethodPost && match(rest, "discussions"):
		s.createDiscussion(w, r, p, t, nil, user)
	case r.Method == http.MethodPost && match(rest, "discussions", "*", "notes"):
		s.replyToDiscussion(w, r, p, t, user, rest[1])
	case r.Method == http.MethodPut && match(rest, "discussions", "*"):
		s.resolveDiscussion(w, r, p, t, user, rest[1])
	default:
		return s.serveNoteEdit(w, r, p, t, user, rest)
	}
	return true
}

// serveMRWrite routes the writes on one merge request.
func (s *Server) serveMRWrite(w http.ResponseWriter, r *http.Request, p *project, mr *gitlab.MergeRequest, user string, rest []string) bool {
	t := mrTarget(mr)
	post, put, del := r.Method == http.MethodPost, r.Method == http.MethodPut, r.Method == http.MethodDelete
	switch {
	case put && len(rest) == 0:
		s.updateMR(w, r, p, mr, user)
	case post && match(rest, "notes"):
		s.createNote(w, r, p, t, user)
	case post && match(rest, "discussions"):
		s.createDiscussion(w, r, p, t, mr, user)
	case post && match(rest, "discussions", "*", "notes"):
		s.replyToDiscussion(w, r, p, t, user, rest[1])
	case put && match(rest, "discussions", "*"):
		s.resolveDiscussion(w, r, p, t, user, rest[1])
	case post && match(rest, "draft_notes"):
		s.createDraft(w, r, p, mr, user)
	case post && match(rest, "draft_notes", "bulk_publish"):
		s.publishDrafts(w, r, p, mr, user)
	case del && match(rest, "draft_notes", "*"):
		s.deleteDraft(w, p, mr, user, rest[1])
	case put && match(rest, "merge"):
		s.merge(w, r, p, mr, user)
	case post && match(rest, "approve"):
		s.approve(w, r, p, mr, user)
	case post && match(rest, "unapprove"):
		s.unapprove(w, p, mr, user)
	default:
		return s.serveNoteEdit(w, r, p, t, user, rest)
	}
	return true
}

// serveNoteEdit routes an edit or a delete of one note, the same on an
// issue and a merge request.
func (s *Server) serveNoteEdit(w http.ResponseWriter, r *http.Request, p *project, t target, user string, rest []string) bool {
	if !match(rest, "notes", "*") {
		return false
	}
	switch r.Method {
	case http.MethodPut:
		s.updateNote(w, r, p, t, user, rest[1])
	case http.MethodDelete:
		s.deleteNote(w, r, p, t, user, rest[1])
	default:
		return false
	}
	return true
}

// ------------------------------------------------------------ shared fields

func (s *Server) userByID(id int64) (gitlab.UserBasic, bool) {
	i := int(id - firstUserID)
	if i < 0 || i >= len(Users) {
		return gitlab.UserBasic{}, false
	}
	return s.user(Users[i]), true
}

// usersByID resolves ids, skipping unknown ones as GitLab does.
func (s *Server) usersByID(ids []int64) []gitlab.UserBasic {
	out := []gitlab.UserBasic{}
	for _, id := range ids {
		if u, ok := s.userByID(id); ok && !slices.ContainsFunc(out, func(x gitlab.UserBasic) bool { return x.ID == id }) {
			out = append(out, u)
		}
	}
	return out
}

// milestoneByID finds a project or ancestor group milestone.
func (s *Server) milestoneByID(p *project, id int64) *gitlab.Milestone {
	rows := slices.Clone(p.milestones)
	for g := p.groupID; g != 0; g = s.parentOf(g) {
		rows = append(rows, s.groupMilestones[g]...)
	}
	for _, m := range rows {
		if m.ID == id {
			return &gitlab.Milestone{ID: m.ID, IID: m.IID, Title: m.Title, State: m.State}
		}
	}
	return nil
}

// common is what an issue and a merge request share that a write sets.
type common struct {
	title, description *string
	labels             *[]string
	assignees          *[]gitlab.UserBasic
	milestone          **gitlab.Milestone
}

var dueDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// setCommon applies the shared fields a body sends and returns the
// description's quick actions, for the caller to run once the record is
// in place. It checks everything before changing anything.
func (s *Server) setCommon(w http.ResponseWriter, p *project, b body, c common) ([]command, bool) {
	if title, ok := b.str("title"); ok && strings.TrimSpace(title) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": map[string]any{"title": []string{"can't be blank"}}})
		return nil, false
	}
	if title, ok := b.str("title"); ok {
		*c.title = title
	}
	var cmds []command
	if desc, ok := b.str("description"); ok {
		cmds, *c.description = extract(desc)
	}
	if labels, ok := b.strs("labels"); ok {
		*c.labels = []string{}
		for _, l := range labels {
			s.ensureLabel(p, l)
			if !slices.Contains(*c.labels, l) {
				*c.labels = append(*c.labels, l)
			}
		}
	}
	if add, ok := b.strs("add_labels"); ok {
		for _, l := range add {
			s.ensureLabel(p, l)
			if !slices.Contains(*c.labels, l) {
				*c.labels = append(*c.labels, l)
			}
		}
	}
	if remove, ok := b.strs("remove_labels"); ok {
		*c.labels = slices.DeleteFunc(*c.labels, func(l string) bool { return slices.Contains(remove, l) })
	}
	if ids, ok := b.ints("assignee_ids"); ok {
		*c.assignees = s.usersByID(ids)
	}
	if id, ok := b.integer("milestone_id"); ok {
		if id == 0 {
			*c.milestone = nil
		} else if m := s.milestoneByID(p, id); m != nil {
			*c.milestone = m
		}
	}
	return cmds, true
}

// changed reports whether v's JSON differs from before, ignoring
// updated_at, so a write that changes nothing leaves updated_at alone.
func changed(before []byte, v any) bool { return !bytes.Equal(before, snapshot(v)) }

func snapshot(v any) []byte {
	m := withExtra(v, nil)
	delete(m, "updated_at")
	raw, _ := json.Marshal(m)
	return raw
}

// ------------------------------------------------------------ issues

func (s *Server) createIssue(w http.ResponseWriter, r *http.Request, p *project, user string) {
	b, ok := readBody(w, r)
	if !ok || !b.require(w, "title") || !s.checkIssueFields(w, b) {
		return
	}
	var iid int64
	for _, i := range p.issues {
		iid = max(iid, i.IID)
	}
	iid++
	now := s.opts.Now().UTC()
	iss := &gitlab.Issue{ID: s.nextIssueID, IID: iid, ProjectID: p.ID, State: "opened", Type: "ISSUE", CreatedAt: now,
		UpdatedAt: now, Labels: []string{}, Author: s.user(user), Assignees: []gitlab.UserBasic{},
		WebURL: fmt.Sprintf("%s/-/issues/%d", p.WebURL, iid),
		References: gitlab.References{Short: fmt.Sprintf("#%d", iid), Relative: fmt.Sprintf("#%d", iid),
			Full: fmt.Sprintf("%s#%d", p.PathWithNamespace, iid)},
		TaskCompletion: &gitlab.TaskCompletion{}}
	cmds, ok := s.setIssue(w, p, b, iss, user)
	if !ok {
		return
	}
	s.nextIssueID++
	p.issues = append(p.issues, iss)
	s.runCommands(p, issueTarget(iss), cmds, user)
	writeJSON(w, http.StatusCreated, iss)
}

// checkIssueFields refuses a value GitLab refuses before anything moves.
func (s *Server) checkIssueFields(w http.ResponseWriter, b body) bool {
	if !b.valid(w, "state_event", "close", "reopen") {
		return false
	}
	if d, ok := b.str("due_date"); ok && d != "" && !dueDate.MatchString(d) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "due_date is invalid"})
		return false
	}
	return true
}

// setIssue applies a body's fields to an issue.
func (s *Server) setIssue(w http.ResponseWriter, p *project, b body, iss *gitlab.Issue, user string) ([]command, bool) {
	cmds, ok := s.setCommon(w, p, b, common{title: &iss.Title, description: &iss.Description, labels: &iss.Labels,
		assignees: &iss.Assignees, milestone: &iss.Milestone})
	if !ok {
		return nil, false
	}
	if d, ok := b.str("due_date"); ok {
		iss.DueDate = d
	}
	if v, ok := b.boolean("confidential"); ok {
		iss.Confidential = v
	}
	now := s.opts.Now().UTC()
	switch ev, _ := b.str("state_event"); {
	case ev == "close" && iss.State != "closed":
		by := s.user(user)
		iss.State, iss.ClosedAt, iss.ClosedBy = "closed", &now, &by
	case ev == "reopen" && iss.State == "closed":
		iss.State, iss.ClosedAt, iss.ClosedBy = "opened", nil, nil
	}
	return cmds, true
}

// issueParams are the fields an issue update takes; a body with none of
// them sent at all is refused.
const issueParams = "assignee_id, assignee_ids, confidential, created_at, description, discussion_locked, due_date, " +
	"labels, add_labels, remove_labels, milestone_id, state_event, title, issue_type"

func (s *Server) updateIssue(w http.ResponseWriter, r *http.Request, p *project, iss *gitlab.Issue, user string) {
	b, ok := readBody(w, r)
	if !ok {
		return
	}
	if len(b) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": issueParams + " are missing, at least one parameter must be provided"})
		return
	}
	if !s.checkIssueFields(w, b) {
		return
	}
	before, old := snapshot(iss), iss.UpdatedAt
	draft := *iss
	draft.Labels = slices.Clone(iss.Labels)
	cmds, ok := s.setIssue(w, p, b, &draft, user)
	if !ok {
		return
	}
	*iss = draft
	s.runCommands(p, issueTarget(iss), cmds, user)
	iss.UpdatedAt = old
	if changed(before, iss) {
		bump(&iss.UpdatedAt, s.opts.Now().UTC())
	}
	writeJSON(w, http.StatusOK, iss)
}

// ------------------------------------------------------------ merge requests

// draftTitle is the title prefix that makes a merge request a draft.
var draftTitle = regexp.MustCompile(`(?i)^\s*(draft:|\[draft\]|\(draft\))`)

func (s *Server) createMR(w http.ResponseWriter, r *http.Request, p *project, user string) {
	b, ok := readBody(w, r)
	if !ok || !b.require(w, "source_branch", "target_branch", "title") || !b.valid(w, "state_event", "close", "reopen") {
		return
	}
	source, _ := b.str("source_branch")
	targetBranch, _ := b.str("target_branch")
	var problems []string
	if _, ok := p.trees[source]; !ok {
		problems = append(problems, fmt.Sprintf("Source branch %q does not exist", source))
	}
	if _, ok := p.trees[targetBranch]; !ok {
		problems = append(problems, fmt.Sprintf("Target branch %q does not exist", targetBranch))
	}
	if source == targetBranch {
		problems = append(problems, "You can't use same project/branch for source and target")
	}
	if len(problems) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": problems})
		return
	}
	var iid int64
	for _, m := range p.mrs {
		iid = max(iid, m.IID)
		if m.State == "opened" && m.SourceBranch == source && m.TargetBranch == targetBranch {
			writeJSON(w, http.StatusConflict, map[string]any{"message": []string{
				fmt.Sprintf("Another open merge request already exists for this source branch: !%d", m.IID)}})
			return
		}
	}
	iid++
	now := s.opts.Now().UTC()
	mr := &gitlab.MergeRequest{ID: s.nextMRID, IID: iid, ProjectID: p.ID, State: "opened", CreatedAt: now, UpdatedAt: now,
		Author: s.user(user), Assignees: []gitlab.UserBasic{}, Reviewers: []gitlab.UserBasic{}, Labels: []string{},
		SourceBranch: source, TargetBranch: targetBranch, SourceProjectID: p.ID, TargetProjectID: p.ID,
		DetailedMergeStatus: "mergeable", BlockingDiscussionsResolved: true,
		WebURL: fmt.Sprintf("%s/-/merge_requests/%d", p.WebURL, iid),
		References: gitlab.References{Short: fmt.Sprintf("!%d", iid), Relative: fmt.Sprintf("!%d", iid),
			Full: fmt.Sprintf("%s!%d", p.PathWithNamespace, iid)}}
	cmds, ok := s.setMR(w, p, b, mr)
	if !ok {
		return
	}
	s.nextMRID++
	p.mrs = append(p.mrs, mr)
	p.approvals[iid] = &gitlab.Approvals{UserCanApprove: true, ApprovedBy: []gitlab.Approver{}}
	s.refreshMR(p, mr)
	s.runCommands(p, mrTarget(mr), cmds, user)
	writeJSON(w, http.StatusCreated, mr)
}

// refreshMR points a merge request at its branches' heads and recomputes
// its diff from their trees, as GitLab does on a push to either.
func (s *Server) refreshMR(p *project, mr *gitlab.MergeRequest) {
	base, head := p.commits[mr.TargetBranch][0].ID, p.commits[mr.SourceBranch][0].ID
	mr.SHA = head
	mr.DiffRefs = &gitlab.DiffRefs{BaseSHA: base, StartSHA: base, HeadSHA: head}
	p.mrDiffs[mr.IID] = treeDiffs(p.trees[mr.TargetBranch], p.trees[mr.SourceBranch])
	mr.ChangesCount = strconv.Itoa(len(p.mrDiffs[mr.IID]))
}

// setMR applies a body's fields to a merge request. The draft state
// follows the title.
func (s *Server) setMR(w http.ResponseWriter, p *project, b body, mr *gitlab.MergeRequest) ([]command, bool) {
	if tb, ok := b.str("target_branch"); ok {
		if _, exists := p.trees[tb]; !exists {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": []string{fmt.Sprintf("Target branch %q does not exist", tb)}})
			return nil, false
		}
	}
	if ev, _ := b.str("state_event"); ev == "reopen" && mr.State == "merged" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": []string{"A merged merge request cannot be reopened"}})
		return nil, false
	}
	cmds, ok := s.setCommon(w, p, b, common{title: &mr.Title, description: &mr.Description, labels: &mr.Labels,
		assignees: &mr.Assignees, milestone: &mr.Milestone})
	if !ok {
		return nil, false
	}
	mr.Draft = draftTitle.MatchString(mr.Title)
	if ids, ok := b.ints("reviewer_ids"); ok {
		mr.Reviewers = s.usersByID(ids)
	}
	if v, ok := b.boolean("squash"); ok {
		mr.Squash = v
	}
	if v, ok := b.boolean("remove_source_branch"); ok {
		mr.ForceRemoveSourceBranch = v
	}
	if tb, ok := b.str("target_branch"); ok {
		mr.TargetBranch = tb
	}
	now := s.opts.Now().UTC()
	switch ev, _ := b.str("state_event"); {
	case ev == "close" && mr.State == "opened":
		mr.State, mr.ClosedAt = "closed", &now
	case ev == "reopen" && mr.State == "closed":
		mr.State, mr.ClosedAt = "opened", nil
	}
	return cmds, true
}

func (s *Server) updateMR(w http.ResponseWriter, r *http.Request, p *project, mr *gitlab.MergeRequest, user string) {
	b, ok := readBody(w, r)
	if !ok {
		return
	}
	if len(b) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "assignee_id, assignee_ids, reviewer_ids, description, labels, " +
			"add_labels, remove_labels, milestone_id, remove_source_branch, state_event, target_branch, title, squash " +
			"are missing, at least one parameter must be provided"})
		return
	}
	if !b.valid(w, "state_event", "close", "reopen") {
		return
	}
	before, old := snapshot(mr), mr.UpdatedAt
	draft := *mr
	draft.Labels = slices.Clone(mr.Labels)
	cmds, ok := s.setMR(w, p, b, &draft)
	if !ok {
		return
	}
	retarget := draft.TargetBranch != mr.TargetBranch
	*mr = draft
	if retarget {
		s.refreshMR(p, mr)
	}
	s.runCommands(p, mrTarget(mr), cmds, user)
	mr.UpdatedAt = old
	if changed(before, mr) {
		bump(&mr.UpdatedAt, s.opts.Now().UTC())
	}
	writeJSON(w, http.StatusOK, mr)
}

// ------------------------------------------------------------ to-do items

func (s *Server) markTodoDone(w http.ResponseWriter, user, id string) {
	for i := range s.todos {
		t := &s.todos[i]
		if strconv.FormatInt(t.ID, 10) == id && t.user == user {
			t.State = "done"
			writeJSON(w, http.StatusCreated, todoJSON(*t))
			return
		}
	}
	message(w, http.StatusNotFound, "404 Todo Not Found")
}

// ------------------------------------------------------------ CI lint

// lintContent is POST ci/lint: the supplied configuration linted in the
// project's context, as the GET lints a branch's.
func (s *Server) lintContent(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody(w, r)
	if !ok || !b.require(w, "content") {
		return
	}
	content, _ := b.str("content")
	dryRun, _ := b.boolean("dry_run")
	includeJobs, _ := b.boolean("include_jobs")
	writeJSON(w, http.StatusOK, lintConfig(content, dryRun, includeJobs))
}

// ------------------------------------------------------------ test accessors

// MergeRequest returns a copy of a merge request, for a test to check
// what a write did.
func (s *Server) MergeRequest(projectPath string, iid int64) (gitlab.MergeRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.projectByPath(projectPath); p != nil {
		if m := findMR(p, itoa(iid)); m != nil {
			return *m, true
		}
	}
	return gitlab.MergeRequest{}, false
}

// Discussions returns a copy of an issue's (kind "issue") or a merge
// request's (kind "mr") discussions.
func (s *Server) Discussions(projectPath, kind string, iid int64) []gitlab.Discussion {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil {
		return nil
	}
	out := slices.Clone(p.discussions[kind+":"+itoa(iid)])
	for i := range out {
		out[i].Notes = slices.Clone(out[i].Notes)
	}
	return out
}

// Comment plants a comment by user on an issue (kind "issue") or a
// merge request (kind "mr"), as content someone else wrote. It runs no
// quick action: it is a fixture, not a write through the API.
func (s *Server) Comment(projectPath, kind string, iid int64, user, body string) (gitlab.Note, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil {
		return gitlab.Note{}, false
	}
	switch kind {
	case "issue":
		if iss := findIssue(p, itoa(iid)); iss != nil {
			return s.storeNote(p, issueTarget(iss), user, body), true
		}
	case "mr":
		if mr := findMR(p, itoa(iid)); mr != nil {
			return s.storeNote(p, mrTarget(mr), user, body), true
		}
	}
	return gitlab.Note{}, false
}

// Drafts returns a copy of every draft on a merge request, whoever wrote
// it.
func (s *Server) Drafts(projectPath string, iid int64) []gitlab.DraftNote {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.projectByPath(projectPath); p != nil {
		return slices.Clone(p.drafts[iid])
	}
	return nil
}

// FileAt returns a file's content on a branch.
func (s *Server) FileAt(projectPath, branch, path string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.projectByPath(projectPath); p != nil {
		c, ok := p.trees[branch][path]
		return c, ok
	}
	return "", false
}

// BranchHead returns a branch's head commit.
func (s *Server) BranchHead(projectPath, branch string) (gitlab.Commit, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.projectByPath(projectPath); p != nil && len(p.commits[branch]) > 0 {
		return p.commits[branch][0], true
	}
	return gitlab.Commit{}, false
}

// ReviewerState is the reviewer state a user last submitted with a
// review of a merge request, "" for none.
func (s *Server) ReviewerState(projectPath string, iid int64, user string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reviewerStates[reviewerKey(projectPath, iid, user)]
}

// TodoState returns a to-do item's state, "" for an unknown one.
func (s *Server) TodoState(id int64) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.todos {
		if t.ID == id {
			return t.State
		}
	}
	return ""
}

func reviewerKey(projectPath string, iid int64, user string) string {
	return projectPath + "\x00" + itoa(iid) + "\x00" + user
}

func (s *Server) projectByPath(path string) *project {
	for _, p := range s.projects {
		if p.PathWithNamespace == path {
			return p
		}
	}
	return nil
}
