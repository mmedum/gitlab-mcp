package gitlabtest

import (
	"fmt"
	"maps"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/mmedum/gitlab-mcp/v4/internal/gitlab"
)

// The phase 6 routes: issue moves and links, label and milestone writes,
// rebase, cherry-pick and revert, blame, protected tags and tag writes,
// and job artifacts. Each answers as GitLab's route does at v19.4.1-ee.

// ConflictSHA is a commit whose cherry-pick or revert conflicts on every
// branch.
const ConflictSHA = "c0ffee0000000000000000000000000000000000"

// issueLink is a link between two issues, which GitLab lists from both.
type issueLink struct {
	id                 int64
	srcProject, srcIID int64
	dstProject, dstIID int64
	linkType           string
}

// serveFeatures serves the phase 6 routes; it reports whether the path
// was one of them.
func (s *Server) serveFeatures(w http.ResponseWriter, r *http.Request, p *project, user string, seg []string) bool {
	return s.serveIssueFeatures(w, r, p, user, seg) || s.serveMRDependencies(w, r, p, user, seg) ||
		s.servePlanningFeatures(w, r, p, seg) || s.serveRepoFeatures(w, r, p, user, seg)
}

func (s *Server) serveIssueFeatures(w http.ResponseWriter, r *http.Request, p *project, user string, seg []string) bool {
	switch {
	case r.Method == http.MethodPost && match(seg, "issues", "*", "move"):
		s.moveIssue(w, r, p, seg[1])
	case r.Method == http.MethodGet && match(seg, "issues", "*", "links"):
		s.listIssueLinks(w, p, user, seg[1])
	case r.Method == http.MethodPost && match(seg, "issues", "*", "links"):
		s.createIssueLink(w, r, p, seg[1])
	case r.Method == http.MethodDelete && match(seg, "issues", "*", "links", "*"):
		s.deleteIssueLink(w, p, seg[1], seg[3])
	case r.Method == http.MethodPut && match(seg, "merge_requests", "*", "rebase"):
		s.rebase(w, r, p, seg[1])
	default:
		return false
	}
	return true
}

func (s *Server) servePlanningFeatures(w http.ResponseWriter, r *http.Request, p *project, seg []string) bool {
	one := r.Method == http.MethodGet || r.Method == http.MethodPut || r.Method == http.MethodDelete
	switch {
	case r.Method == http.MethodPost && match(seg, "labels"):
		s.createLabel(w, r, p)
	case one && match(seg, "labels", "*"):
		s.label(w, r, p, seg[1])
	case r.Method == http.MethodPost && match(seg, "milestones"):
		s.createMilestone(w, r, p)
	case one && match(seg, "milestones", "*"):
		s.milestone(w, r, p, seg[1])
	default:
		return false
	}
	return true
}

func (s *Server) serveRepoFeatures(w http.ResponseWriter, r *http.Request, p *project, user string, seg []string) bool {
	get, post := r.Method == http.MethodGet, r.Method == http.MethodPost
	switch {
	case post && (match(seg, "repository", "commits", "*", "cherry_pick") || match(seg, "repository", "commits", "*", "revert")):
		s.pick(w, r, p, user, seg[2], seg[3] == "revert")
	case get && match(seg, "repository", "files", "*", "blame"):
		s.blame(w, r, p, seg[2])
	case get && match(seg, "protected_tags"):
		writePage(s, w, r, p.protectedTags)
	case post && match(seg, "repository", "tags"):
		s.createTag(w, r, p)
	case r.Method == http.MethodDelete && match(seg, "repository", "tags", "*"):
		s.deleteTag(w, p, seg[2])
	case get && match(seg, "jobs", "*", "artifacts", "tree"):
		s.artifactTree(w, r, p, seg[1])
	case get && len(seg) == 4 && seg[0] == "jobs" && seg[2] == "artifacts":
		s.artifactFile(w, p, seg[1], seg[3])
	default:
		return false
	}
	return true
}

func (s *Server) projectByID(id int64) *project {
	for _, p := range s.projects {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// moveIssue is POST …/issues/:iid/move: a copy in the new project, and
// the original closed and pointing at it.
func (s *Server) moveIssue(w http.ResponseWriter, r *http.Request, p *project, iid string) {
	iss := findIssue(p, iid)
	b, ok := readBody(w, r)
	if iss == nil || !ok {
		if ok {
			message(w, http.StatusNotFound, "404 Issue Not Found")
		}
		return
	}
	to, _ := b.integer("to_project_id")
	dst := s.projectByID(to)
	if dst == nil {
		message(w, http.StatusNotFound, "404 Project Not Found")
		return
	}
	if dst == p {
		message(w, http.StatusBadRequest, "Cannot move work item to its current project")
		return
	}
	var next int64
	for _, i := range dst.issues {
		next = max(next, i.IID)
	}
	next++
	now := s.opts.Now().UTC()
	moved := *iss
	moved.ID, moved.IID, moved.ProjectID, moved.CreatedAt, moved.UpdatedAt = s.nextIssueID, next, dst.ID, now, now
	moved.WebURL = fmt.Sprintf("%s/-/issues/%d", dst.WebURL, next)
	moved.References = gitlab.References{Short: fmt.Sprintf("#%d", next), Relative: fmt.Sprintf("#%d", next),
		Full: fmt.Sprintf("%s#%d", dst.PathWithNamespace, next)}
	s.nextIssueID++
	dst.issues = append(dst.issues, &moved)
	iss.State, iss.UpdatedAt, iss.MovedToID = "closed", now, &moved.ID
	writeJSON(w, http.StatusCreated, moved)
}

// listIssueLinks is GET …/issues/:iid/links: the other issue of each
// link, from either side, with the link.
func (s *Server) listIssueLinks(w http.ResponseWriter, p *project, user, iid string) {
	iss := findIssue(p, iid)
	if iss == nil || !s.readable(p, iss, user) {
		message(w, http.StatusNotFound, "404 Issue Not Found")
		return
	}
	out := []gitlab.RelatedIssue{}
	for _, l := range s.issueLinks {
		otherProject, otherIID, typ := l.dstProject, l.dstIID, l.linkType
		switch {
		case l.srcProject == p.ID && l.srcIID == iss.IID:
		case l.dstProject == p.ID && l.dstIID == iss.IID:
			otherProject, otherIID = l.srcProject, l.srcIID
			typ = map[string]string{"blocks": "is_blocked_by", "is_blocked_by": "blocks"}[l.linkType]
			if typ == "" {
				typ = l.linkType
			}
		default:
			continue
		}
		op := s.projectByID(otherProject)
		other := findIssue(op, itoa(otherIID))
		if !s.visible(op, user) || !s.readable(op, other, user) {
			continue
		}
		out = append(out, gitlab.RelatedIssue{IID: other.IID, ProjectID: op.ID, Title: other.Title, State: other.State,
			WebURL: other.WebURL, IssueLinkID: l.id, LinkType: typ})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createIssueLink(w http.ResponseWriter, r *http.Request, p *project, iid string) {
	iss := findIssue(p, iid)
	b, ok := readBody(w, r)
	if iss == nil || !ok || !b.require(w, "target_project_id", "target_issue_iid") {
		if iss == nil && ok {
			message(w, http.StatusNotFound, "404 Issue Not Found")
		}
		return
	}
	pid, _ := b.str("target_project_id")
	tiid, _ := b.str("target_issue_iid")
	id, _ := strconv.ParseInt(pid, 10, 64)
	dst := s.projectByID(id)
	if dst == nil || findIssue(dst, tiid) == nil {
		message(w, http.StatusNotFound, "404 Not found")
		return
	}
	typ, _ := b.str("link_type")
	if typ == "" {
		typ = "relates_to"
	}
	target := findIssue(dst, tiid)
	for _, l := range s.issueLinks {
		if (l.srcProject == p.ID && l.srcIID == iss.IID && l.dstProject == dst.ID && l.dstIID == target.IID) ||
			(l.dstProject == p.ID && l.dstIID == iss.IID && l.srcProject == dst.ID && l.srcIID == target.IID) {
			message(w, http.StatusConflict, "Issue(s) already assigned")
			return
		}
	}
	s.nextLinkID++
	l := issueLink{id: s.nextLinkID, srcProject: p.ID, srcIID: iss.IID, dstProject: dst.ID, dstIID: target.IID, linkType: typ}
	s.issueLinks = append(s.issueLinks, l)
	basic := func(i *gitlab.Issue) gitlab.IssueBasic {
		return gitlab.IssueBasic{ID: i.ID, IID: i.IID, ProjectID: i.ProjectID, Title: i.Title, State: i.State, WebURL: i.WebURL}
	}
	writeJSON(w, http.StatusCreated, gitlab.IssueLink{ID: l.id, LinkType: typ, SourceIssue: basic(iss), TargetIssue: basic(target)})
}

func (s *Server) deleteIssueLink(w http.ResponseWriter, p *project, iid, id string) {
	iss := findIssue(p, iid)
	i := slices.IndexFunc(s.issueLinks, func(l issueLink) bool {
		return itoa(l.id) == id && iss != nil && ((l.srcProject == p.ID && l.srcIID == iss.IID) || (l.dstProject == p.ID && l.dstIID == iss.IID))
	})
	if i < 0 {
		message(w, http.StatusNotFound, "404 Not found")
		return
	}
	s.issueLinks = slices.Delete(s.issueLinks, i, i+1)
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// IssueLinks counts the links the instance holds.
func (s *Server) IssueLinks() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.issueLinks)
}

// ----------------------------------------------------------- merge request dependencies

// mrBlock is a merge request dependency: the blocked merge request waits
// for the blocking one to merge.
type mrBlock struct {
	id                           int64
	blockedProject, blockedIID   int64
	blockingProject, blockingIID int64
}

// mrBlockRow is ee/lib/api/entities/merge_request_dependency.rb: each
// merge request only when the account may read it, and project_id the
// blocking merge request's project.
type mrBlockRow struct {
	ID        int64                `json:"id"`
	Blocking  *gitlab.MergeRequest `json:"blocking_merge_request,omitempty"`
	Blocked   *gitlab.MergeRequest `json:"blocked_merge_request,omitempty"`
	ProjectID int64                `json:"project_id"`
}

// serveMRDependencies serves ee/lib/api/merge_request_dependencies.rb. The
// routes check no license: on Free they answer as on Premium, and only
// the merge check ignores them.
func (s *Server) serveMRDependencies(w http.ResponseWriter, r *http.Request, p *project, user string, seg []string) bool {
	switch {
	case r.Method == http.MethodGet && match(seg, "merge_requests", "*", "blocks"):
		s.listMRBlocks(w, r, p, user, seg[1], true)
	case r.Method == http.MethodGet && match(seg, "merge_requests", "*", "blockees"):
		s.listMRBlocks(w, r, p, user, seg[1], false)
	case r.Method == http.MethodPost && match(seg, "merge_requests", "*", "blocks"):
		s.createMRBlock(w, r, p, user, seg[1])
	case r.Method == http.MethodDelete && match(seg, "merge_requests", "*", "blocks", "*"):
		s.deleteMRBlock(w, p, user, seg[1], seg[3])
	default:
		return false
	}
	return true
}

// mrUpdatable is update_merge_request: Developer and above, the author,
// or an assignee.
func (s *Server) mrUpdatable(p *project, mr *gitlab.MergeRequest, user string) bool {
	return s.accessLevel(p, user) >= developerAccess || mr.Author.Username == user || has(usernames(mr.Assignees), user)
}

// mrsReadable reports whether user may read the merge requests of the
// project with this id.
func (s *Server) mrsReadable(projectID int64, user string) bool {
	p := s.projectByID(projectID)
	return p != nil && s.visible(p, user) && s.mrReadable(p, user)
}

// blockRow shows a dependency to user.
func (s *Server) blockRow(b mrBlock, user string) mrBlockRow {
	row := mrBlockRow{ID: b.id, ProjectID: b.blockingProject}
	side := func(projectID, iid int64) *gitlab.MergeRequest {
		if !s.mrsReadable(projectID, user) {
			return nil
		}
		return findMR(s.projectByID(projectID), itoa(iid))
	}
	row.Blocking, row.Blocked = side(b.blockingProject, b.blockingIID), side(b.blockedProject, b.blockedIID)
	return row
}

// listMRBlocks is GET …/blocks when blocked, the dependencies the merge
// request waits for, and …/blockees otherwise, those that wait for it;
// in id order.
func (s *Server) listMRBlocks(w http.ResponseWriter, r *http.Request, p *project, user, iid string, blocked bool) {
	mr := findMR(p, iid)
	switch {
	case mr == nil:
		message(w, http.StatusNotFound, "404 Merge Request Not Found")
		return
	case !s.mrReadable(p, user):
		message(w, http.StatusForbidden, "403 Forbidden")
		return
	}
	rows := []mrBlockRow{}
	for _, b := range s.mrBlocks {
		if (blocked && b.blockedProject == p.ID && b.blockedIID == mr.IID) ||
			(!blocked && b.blockingProject == p.ID && b.blockingIID == mr.IID) {
			rows = append(rows, s.blockRow(b, user))
		}
	}
	writePage(s, w, r, rows)
}

// createMRBlock is POST …/blocks by blocking_merge_request_iid and
// blocking_project_id, with CreateBlockService's checks and
// MergeRequestBlock's validations, its caps counted as the model counts
// them.
func (s *Server) createMRBlock(w http.ResponseWriter, r *http.Request, p *project, user, iid string) {
	mr := findMR(p, iid)
	if mr == nil || !s.mrReadable(p, user) {
		message(w, http.StatusNotFound, "404 Not found")
		return
	}
	b, ok := readBody(w, r)
	if !ok || !b.require(w, "blocking_merge_request_iid") {
		return
	}
	other := p
	if id, ok := b.integer("blocking_project_id"); ok {
		other = s.projectByID(id)
		if other == nil || !s.visible(other, user) {
			message(w, http.StatusNotFound, "404 Project Not Found")
			return
		}
	}
	biid, _ := b.str("blocking_merge_request_iid")
	blocking := findMR(other, biid)
	switch {
	case blocking == nil:
		message(w, http.StatusNotFound, "Blocking merge request not found")
		return
	case !s.mrUpdatable(p, mr, user):
		message(w, http.StatusForbidden, "Lacking permissions to update the merge request")
		return
	case !s.mrReadable(other, user):
		message(w, http.StatusForbidden, "Lacking permissions to the blocking merge request")
		return
	case other.ID == p.ID && blocking.IID == mr.IID:
		message(w, http.StatusBadRequest, "This block is self-referential")
		return
	}
	var blocks, blockedBy int
	for _, x := range s.mrBlocks {
		if x.blockingProject == p.ID && x.blockingIID == mr.IID {
			blocks++
		}
		if x.blockedProject == other.ID && x.blockedIID == blocking.IID {
			blockedBy++
		}
		if x.blockedProject == p.ID && x.blockedIID == mr.IID && x.blockingProject == other.ID && x.blockingIID == blocking.IID {
			message(w, http.StatusConflict, "Block already exists")
			return
		}
	}
	switch {
	case blocks >= 10:
		message(w, http.StatusBadRequest, "Merge request blocks the maximum number of merge requests (10)")
		return
	case blockedBy >= 10:
		message(w, http.StatusBadRequest, "Merge request is blocked by the maximum number of merge requests (10)")
		return
	}
	s.nextBlockID++
	block := mrBlock{id: s.nextBlockID, blockedProject: p.ID, blockedIID: mr.IID, blockingProject: other.ID,
		blockingIID: blocking.IID}
	s.mrBlocks = append(s.mrBlocks, block)
	writeJSON(w, http.StatusCreated, s.blockRow(block, user))
}

// deleteMRBlock is DELETE …/blocks/:block_id, found only among the
// dependencies the merge request waits for; the blocking merge request
// must be readable.
func (s *Server) deleteMRBlock(w http.ResponseWriter, p *project, user, iid, id string) {
	mr := findMR(p, iid)
	switch {
	case mr == nil:
		message(w, http.StatusNotFound, "404 Merge Request Not Found")
		return
	case !s.mrReadable(p, user) || !s.mrUpdatable(p, mr, user):
		message(w, http.StatusForbidden, "403 Forbidden")
		return
	}
	i := slices.IndexFunc(s.mrBlocks, func(b mrBlock) bool {
		return itoa(b.id) == id && b.blockedProject == p.ID && b.blockedIID == mr.IID
	})
	if i < 0 {
		message(w, http.StatusNotFound, "404 Not found")
		return
	}
	if !s.mrsReadable(s.mrBlocks[i].blockingProject, user) {
		message(w, http.StatusForbidden, "403 Forbidden")
		return
	}
	s.mrBlocks = slices.Delete(s.mrBlocks, i, i+1)
	w.WriteHeader(http.StatusNoContent)
}

// AddMergeRequest opens a merge request from a new branch at the
// project's main, by author, and returns its iid.
func (s *Server) AddMergeRequest(projectPath, title, author string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil || len(p.commits["main"]) == 0 {
		return 0, false
	}
	head := p.commits["main"][0].ID
	mr := s.addMR(p, title, fmt.Sprintf("topic-%d", len(p.mrs)+1), "main", author, head, head)
	return mr.IID, true
}

// AddMergeRequestBlocks makes a merge request of a project wait for
// others of the same project, as many POST …/blocks would.
func (s *Server) AddMergeRequestBlocks(projectPath string, blocked int64, blocking ...int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil {
		return false
	}
	for _, iid := range blocking {
		s.nextBlockID++
		s.mrBlocks = append(s.mrBlocks, mrBlock{id: s.nextBlockID, blockedProject: p.ID, blockedIID: blocked,
			blockingProject: p.ID, blockingIID: iid})
	}
	return true
}

// MergeRequestBlocks counts the merge request dependencies the instance
// holds.
func (s *Server) MergeRequestBlocks() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.mrBlocks)
}

// ----------------------------------------------------------- labels

func (s *Server) createLabel(w http.ResponseWriter, r *http.Request, p *project) {
	b, ok := readBody(w, r)
	if !ok || !b.require(w, "name", "color") {
		return
	}
	name, _ := b.str("name")
	if slices.ContainsFunc(p.labels, func(l gitlab.Label) bool { return l.Name == name }) {
		message(w, http.StatusConflict, "Label already exists")
		return
	}
	l := gitlab.Label{ID: s.nextLabel(), Name: name, IsProjectLabel: true}
	l.Color, _ = b.str("color")
	l.Description, _ = b.str("description")
	if v, ok := priority(b); ok {
		l.Priority = &v
	}
	p.labels = append(p.labels, l)
	writeJSON(w, http.StatusCreated, l)
}

// priority is a label's priority from a request, as GitLab's Integer
// param takes it.
func priority(b body) (int, bool) {
	n, ok := b.integer("priority")
	if !ok || n < math.MinInt32 || n > math.MaxInt32 {
		return 0, false
	}
	return int(n), true
}

func (s *Server) nextLabel() int64 {
	s.nextLabelID++
	return s.nextLabelID + 90000
}

// label is GET, PUT and DELETE …/labels/:id, by id.
func (s *Server) label(w http.ResponseWriter, r *http.Request, p *project, id string) {
	i := slices.IndexFunc(p.labels, func(l gitlab.Label) bool { return itoa(l.ID) == id || l.Name == id })
	if i < 0 {
		message(w, http.StatusNotFound, "404 Label Not Found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, p.labels[i])
	case http.MethodDelete:
		l := p.labels[i]
		p.labels = slices.Delete(p.labels, i, i+1)
		writeJSON(w, http.StatusOK, l)
	default:
		b, ok := readBody(w, r)
		if !ok {
			return
		}
		l := &p.labels[i]
		if v, ok := b.str("new_name"); ok && v != "" {
			l.Name = v
		}
		if v, ok := b.str("color"); ok && v != "" {
			l.Color = v
		}
		if v, ok := b.str("description"); ok {
			l.Description = v
		}
		if b.isNull("priority") {
			l.Priority = nil
		} else if v, ok := priority(b); ok {
			l.Priority = &v
		}
		writeJSON(w, http.StatusOK, *l)
	}
}

// ----------------------------------------------------------- milestones

func (s *Server) createMilestone(w http.ResponseWriter, r *http.Request, p *project) {
	b, ok := readBody(w, r)
	if !ok || !b.require(w, "title") {
		return
	}
	var iid int64
	for _, m := range p.milestones {
		iid = max(iid, m.IID)
	}
	s.nextMilestoneID++
	now := s.opts.Now().UTC()
	m := gitlab.ProjectMilestone{ID: s.nextMilestoneID + 95000, IID: iid + 1, State: "active", UpdatedAt: now, CreatedAt: now}
	m.Title, _ = b.str("title")
	m.Description, _ = b.str("description")
	m.DueDate, _ = b.str("due_date")
	m.StartDate, _ = b.str("start_date")
	m.WebURL = fmt.Sprintf("%s/-/milestones/%d", p.WebURL, m.IID)
	p.milestones = append(p.milestones, m)
	writeJSON(w, http.StatusCreated, m)
}

// milestone is GET, PUT and DELETE …/milestones/:id.
func (s *Server) milestone(w http.ResponseWriter, r *http.Request, p *project, id string) {
	i := slices.IndexFunc(p.milestones, func(m gitlab.ProjectMilestone) bool { return itoa(m.ID) == id })
	if i < 0 {
		message(w, http.StatusNotFound, "404 Milestone Not Found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, p.milestones[i])
	case http.MethodDelete:
		p.milestones = slices.Delete(p.milestones, i, i+1)
		w.WriteHeader(http.StatusNoContent)
	default:
		b, ok := readBody(w, r)
		if !ok {
			return
		}
		m := &p.milestones[i]
		for key, f := range map[string]*string{"title": &m.Title, "description": &m.Description, "due_date": &m.DueDate,
			"start_date": &m.StartDate} {
			if v, ok := b.str(key); ok {
				*f = v
			}
		}
		switch v, _ := b.str("state_event"); v {
		case "close":
			m.State = "closed"
		case "activate":
			m.State = "active"
		}
		m.UpdatedAt = s.opts.Now().UTC()
		writeJSON(w, http.StatusOK, *m)
	}
}

// ----------------------------------------------------------- history

// rebase is PUT …/merge_requests/:iid/rebase: accepted, and done at once
// here.
func (s *Server) rebase(w http.ResponseWriter, r *http.Request, p *project, iid string) {
	mr := findMR(p, iid)
	if _, ok := readBody(w, r); !ok {
		return
	}
	if mr == nil {
		message(w, http.StatusNotFound, "404 Merge Request Not Found")
		return
	}
	s.rebases++
	if mr.State == "opened" {
		s.rebaseMR(p, mr)
	}
	writeJSON(w, http.StatusAccepted, gitlab.RebaseState{})
}

// Rebases counts the rebases asked for.
func (s *Server) Rebases() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rebases
}

// pick is POST …/commits/:sha/cherry_pick and …/revert: a new commit on
// the branch naming the one applied, or with dry_run nothing.
func (s *Server) pick(w http.ResponseWriter, r *http.Request, p *project, user, sha string, revert bool) {
	b, ok := readBody(w, r)
	if !ok || !b.require(w, "branch") {
		return
	}
	branch, _ := b.str("branch")
	if _, ok := p.trees[branch]; !ok {
		message(w, http.StatusNotFound, "404 Branch Not Found")
		return
	}
	if protectedName(p, branch) {
		message(w, http.StatusForbidden, "403 Forbidden")
		return
	}
	dry, _ := b.boolean("dry_run")
	if strings.HasPrefix(ConflictSHA, sha) {
		out := map[string]any{"message": "Sorry, we cannot apply this commit automatically. It may have already been applied, " +
			"or a more recent commit may have updated some of its content.", "error_code": "conflict"}
		if dry {
			out["dry_run"] = "error"
		}
		writeJSON(w, http.StatusBadRequest, out)
		return
	}
	if dry {
		writeJSON(w, http.StatusOK, map[string]any{"dry_run": "success"})
		return
	}
	msg, _ := b.str("message")
	if revert {
		msg = "Revert \"" + sha[:min(8, len(sha))] + "\"\n\nThis reverts commit " + sha + "."
	} else if msg == "" {
		msg = "Picked " + sha[:min(8, len(sha))] + "\n\n(cherry picked from commit " + sha + ")"
	}
	pinFileCommits(p, branch)
	parent := p.commits[branch][0]
	s.nextCommit++
	now := s.opts.Now().UTC()
	u := s.user(user)
	id := fakeSHA(p.PathWithNamespace, branch, msg, itoa(s.nextCommit))
	title, _, _ := strings.Cut(msg, "\n")
	c := gitlab.Commit{ID: id, ShortID: id[:8], Title: title, Message: msg, AuthorName: u.Name, AuthorEmail: user + "@example.com",
		AuthoredDate: now, CommitterName: u.Name, CommitterEmail: user + "@example.com", CommittedDate: now, CreatedAt: now,
		ParentIDs: []string{parent.ID}, WebURL: p.WebURL + "/-/commit/" + id, Stats: &gitlab.CommitStats{}}
	p.commits[branch] = append([]gitlab.Commit{c}, p.commits[branch]...)
	setBranch(p, branch)
	writeJSON(w, http.StatusCreated, c)
}

// blame is GET …/repository/files/:path/blame: the file's lines, two to
// a range, each range under the ref's head commit.
func (s *Server) blame(w http.ResponseWriter, r *http.Request, p *project, path string) {
	q := r.URL.Query()
	ref := q.Get("ref")
	tree, ok := p.trees[ref]
	if !ok {
		message(w, http.StatusNotFound, "404 Commit Not Found")
		return
	}
	content, ok := tree[path]
	if !ok {
		message(w, http.StatusNotFound, "404 File Not Found")
		return
	}
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	start, end := 1, len(lines)
	if v := q.Get("range[start]"); v != "" {
		start, _ = strconv.Atoi(v)
		end, _ = strconv.Atoi(q.Get("range[end]"))
		end = min(end, len(lines))
	}
	head := p.commits[ref][0]
	out := []gitlab.BlameRange{}
	for i := start - 1; i < end; i += 2 {
		out = append(out, gitlab.BlameRange{Commit: gitlab.BlameCommit{ID: head.ID, AuthorName: head.AuthorName,
			AuthoredDate: head.AuthoredDate, Message: head.Message}, Lines: lines[i:min(i+2, end)]})
	}
	writeJSON(w, http.StatusOK, out)
}

// createTag is POST …/repository/tags.
func (s *Server) createTag(w http.ResponseWriter, r *http.Request, p *project) {
	b, ok := readBody(w, r)
	if !ok || !b.require(w, "tag_name", "ref") {
		return
	}
	name, _ := b.str("tag_name")
	ref, _ := b.str("ref")
	if slices.ContainsFunc(p.tags, func(t gitlab.Tag) bool { return t.Name == name }) {
		message(w, http.StatusBadRequest, "Tag "+name+" already exists")
		return
	}
	commits, ok := p.commits[ref]
	if !ok {
		message(w, http.StatusBadRequest, "Target "+ref+" is invalid")
		return
	}
	msg, _ := b.str("message")
	t := gitlab.Tag{Name: name, Message: msg, Target: commits[0].ID, Commit: commits[0],
		Protected: slices.ContainsFunc(p.protectedTags, func(r gitlab.ProtectedTag) bool { return wildcard(r.Name, name) })}
	p.tags = append(p.tags, t)
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) deleteTag(w http.ResponseWriter, p *project, name string) {
	i := slices.IndexFunc(p.tags, func(t gitlab.Tag) bool { return t.Name == name })
	if i < 0 {
		message(w, http.StatusNotFound, "404 Tag Not Found")
		return
	}
	p.tags = slices.Delete(p.tags, i, i+1)
	w.WriteHeader(http.StatusNoContent)
}

// ----------------------------------------------------------- artifacts

// Artifacts JobFailed keeps: a report that prints the synthetic token,
// a summary that prints it in color, as a teed terminal log does, and a
// binary.
var artifactFiles = map[string]string{
	"reports/junit.xml":   "<testsuite name=\"login\">\n<failure>the server refused token " + FakeToken + "</failure>\n</testsuite>\n",
	"reports/summary.txt": "1 failed, 2499 passed\n\x1b[31m" + FakeToken + "\x1b[0m\n",
	"bin/app":             "\x7fELF\x00\x01\x02\x03",
}

func (s *Server) artifactsOf(p *project, job string) (map[string]string, bool) {
	j := findJob(p, job)
	if j == nil || j.ID != JobFailed {
		return nil, false
	}
	return artifactFiles, true
}

// artifactTree is GET …/jobs/:id/artifacts/tree: the directory's own
// entries, directories first, or every entry below it.
func (s *Server) artifactTree(w http.ResponseWriter, r *http.Request, p *project, job string) {
	files, ok := s.artifactsOf(p, job)
	if !ok {
		message(w, http.StatusNotFound, "404 Artifacts Not Found")
		return
	}
	q := r.URL.Query()
	dir := strings.TrimSuffix(q.Get("path"), "/")
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	recursive := q.Get("recursive") == "true"
	seen := map[string]bool{}
	var dirs, rows []gitlab.ArtifactEntry
	for _, path := range slices.Sorted(maps.Keys(files)) {
		rest, ok := strings.CutPrefix(path, prefix)
		if !ok {
			continue
		}
		if i := strings.IndexByte(rest, '/'); i >= 0 && !recursive {
			if d := prefix + rest[:i]; !seen[d] {
				seen[d] = true
				dirs = append(dirs, gitlab.ArtifactEntry{Name: rest[:i], Path: d + "/", Type: "directory"})
			}
			continue
		}
		size := int64(len(files[path]))
		rows = append(rows, gitlab.ArtifactEntry{Name: rest[strings.LastIndexByte(rest, '/')+1:], Path: path, Type: "file", Size: &size})
	}
	writePage(s, w, r, append(dirs, rows...))
}

// artifactFile is GET …/jobs/:id/artifacts/*path: one file, as
// Workhorse extracts it.
func (s *Server) artifactFile(w http.ResponseWriter, p *project, job, path string) {
	files, ok := s.artifactsOf(p, job)
	content, found := files[path]
	if !ok || !found {
		message(w, http.StatusNotFound, "404 Not found")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(content))
}
