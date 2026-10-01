package gitlabtest

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
)

// The review and history half of the instance: a merge request's diffs,
// commits and drafts, compare, and tags. Generated, like everything here.

// Draft note ids, fixed so a test can state one literally.
const firstDraftID = 80001

// Tags on ProjectAlpha: an annotated, protected tag with a release on
// release/1.0's head, and a lightweight one on main's head.
const (
	TagRelease = "v1.0"
	TagPlain   = "v0.9"
)

// writePage answers one offset page of rows, an empty array for none.
func writePage[T any](s *Server, w http.ResponseWriter, r *http.Request, rows []T) {
	if rows == nil {
		rows = []T{}
	}
	if start, end, ok := s.offsetPage(w, r, len(rows), false); ok {
		writeJSON(w, http.StatusOK, rows[start:end])
	}
}

// firstVersionID is the first diff version's id, fixed so a test can
// state one literally.
const firstVersionID = 120001

// addVersion records a diff version when a merge request's head or base
// moved, as GitLab does on a push. The newest is first.
func (s *Server) addVersion(p *project, mr *gitlab.MergeRequest, at time.Time) {
	base, head := mr.DiffRefs.BaseSHA, mr.DiffRefs.HeadSHA
	if vs := p.mrVersions[mr.IID]; len(vs) > 0 && vs[0].HeadCommitSHA == head && vs[0].BaseCommitSHA == base {
		return
	}
	v := gitlab.MergeRequestVersion{ID: s.nextVersionID, HeadCommitSHA: head, BaseCommitSHA: base,
		StartCommitSHA: mr.DiffRefs.StartSHA, CreatedAt: at, State: "collected"}
	if head == base {
		// An empty version has no diff, so GitLab never sets its size.
		v.State = "empty"
	} else {
		size := mr.ChangesCount
		v.RealSize = &size
	}
	if h, ok := history(p, head); ok {
		p.keptAround[head] = slices.Clone(h)
	}
	s.nextVersionID++
	p.mrVersions[mr.IID] = append([]gitlab.MergeRequestVersion{v}, p.mrVersions[mr.IID]...)
}

// reviewers is a merge request's reviewers with their review states:
// unreviewed until one submits a review with a state.
func (s *Server) reviewers(p *project, mr *gitlab.MergeRequest) []gitlab.MergeRequestReviewer {
	out := make([]gitlab.MergeRequestReviewer, 0, len(mr.Reviewers))
	for _, u := range mr.Reviewers {
		state := s.reviewerStates[reviewerKey(p.PathWithNamespace, mr.IID, u.Username)]
		if state == "" {
			state = "unreviewed"
		}
		out = append(out, gitlab.MergeRequestReviewer{User: u, State: state, CreatedAt: mr.CreatedAt})
	}
	return out
}

// SetMRDiffs replaces the files a merge request changes.
func (s *Server) SetMRDiffs(projectPath string, iid int64, diffs []gitlab.Diff) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil {
		return false
	}
	p.mrDiffs[iid] = diffs
	return true
}

// fillReview gives a merge request a spread of changed files, one of
// each kind GitLab marks, and the default user two drafts on it.
func (s *Server) fillReview(p *project, mr *gitlab.MergeRequest) {
	yes := true
	p.mrDiffs[mr.IID] = []gitlab.Diff{
		{OldPath: "src/login.go", NewPath: "src/login.go", AMode: "0", BMode: "100644", NewFile: true,
			Diff: "@@ -0,0 +1,4 @@\n+package main\n+\n+// login is a stub.\n+func login() {}\n"},
		{OldPath: "README.md", NewPath: "README.md", AMode: "100644", BMode: "100644",
			Diff: "@@ -1,3 +1,3 @@\n # Alpha\n \n-A generated repository.\n+A generated repository with a login.\n"},
		{OldPath: "docs/old-name.md", NewPath: "docs/new-name.md", AMode: "100644", BMode: "100644", RenamedFile: true, Diff: ""},
		{OldPath: "assets/logo.png", NewPath: "assets/logo.png", AMode: "100644", BMode: "100644",
			Diff: "Binary files a/assets/logo.png and b/assets/logo.png differ\n"},
		{OldPath: "vendor/big.txt", NewPath: "vendor/big.txt", AMode: "100644", BMode: "100644", TooLarge: true, Diff: ""},
		{OldPath: "gen/api.pb.go", NewPath: "gen/api.pb.go", AMode: "100644", BMode: "100644", GeneratedFile: &yes,
			Collapsed: true, Diff: ""},
	}
	thread := fakeSHA("mr:"+itoa(mr.IID), "thread")
	three := 3
	// GitLab returns drafts in no stable order, and gives a general draft
	// a position with no paths rather than none (live, 2026-09-26).
	p.drafts[mr.IID] = []gitlab.DraftNote{
		{ID: firstDraftID + (mr.IID-1)*10 + 1, AuthorID: s.user(DefaultUser).ID, Note: "Agreed; resolving.",
			DiscussionID: &thread, ResolveDiscussion: true, Position: &gitlab.Position{PositionType: "text"}},
		{ID: firstDraftID + (mr.IID-1)*10, AuthorID: s.user(DefaultUser).ID, Note: "Consider naming this loginUser.",
			Position: &gitlab.Position{BaseSHA: mr.DiffRefs.BaseSHA, StartSHA: mr.DiffRefs.StartSHA, HeadSHA: mr.DiffRefs.HeadSHA,
				PositionType: "text", OldPath: "src/login.go", NewPath: "src/login.go", NewLine: &three}},
	}
}

// fillTags tags release/1.0's head and main's head, and protects every
// release-* tag.
func (s *Server) fillTags(p *project) {
	p.protectedTags = []gitlab.ProtectedTag{{Name: "release-*"}}
	release := p.commits["release/1.0"][0]
	created := release.CommittedDate.Add(time.Hour)
	p.tags = []gitlab.Tag{
		{Name: TagRelease, Message: "Release 1.0", Target: fakeSHA("tag", TagRelease), Protected: true, CreatedAt: &created,
			Commit: release, Release: &gitlab.TagRelease{TagName: TagRelease}},
		{Name: TagPlain, Target: p.commits["main"][0].ID, Commit: p.commits["main"][0]},
	}
}

func (s *Server) serveMRReview(w http.ResponseWriter, r *http.Request, p *project, mr *gitlab.MergeRequest, user string, rest []string) bool {
	switch {
	case match(rest, "diffs"):
		writePage(s, w, r, p.mrDiffs[mr.IID])
	case match(rest, "commits"):
		writePage(s, w, r, s.mrCommits(p, mr))
	case match(rest, "versions"):
		writePage(s, w, r, p.mrVersions[mr.IID])
	case match(rest, "reviewers"):
		writePage(s, w, r, s.reviewers(p, mr))
	case match(rest, "draft_notes", "*"):
		if i := s.ownDraft(p, mr, user, rest[1]); i >= 0 {
			writeJSON(w, http.StatusOK, p.drafts[mr.IID][i])
		} else {
			message(w, http.StatusNotFound, "404 Not found")
		}
	case match(rest, "draft_notes"):
		// Drafts are their author's alone.
		var mine []gitlab.DraftNote
		for _, d := range p.drafts[mr.IID] {
			if d.AuthorID == s.user(user).ID {
				mine = append(mine, d)
			}
		}
		if mine == nil {
			mine = []gitlab.DraftNote{}
		}
		writeJSON(w, http.StatusOK, mine)
	default:
		return false
	}
	return true
}

// mrCommits is the source branch's commits that the target lacks, newest
// first.
func (s *Server) mrCommits(p *project, mr *gitlab.MergeRequest) []gitlab.Commit {
	var out []gitlab.Commit
	for _, c := range p.commits[mr.SourceBranch] {
		if !slices.ContainsFunc(p.commits[mr.TargetBranch], func(t gitlab.Commit) bool { return t.ID == c.ID }) {
			c.Stats = nil
			out = append(out, c)
		}
	}
	return out
}

// history is the commits reachable from a ref, newest first: a branch, a
// tag, or a commit on a branch.
func history(p *project, ref string) ([]gitlab.Commit, bool) {
	if commits, ok := p.commits[ref]; ok {
		return commits, true
	}
	for _, t := range p.tags {
		if t.Name == ref {
			ref = t.Commit.ID
		}
	}
	for _, commits := range p.commits {
		if i := commitIndex(commits, ref); i >= 0 {
			return commits[i:], true
		}
	}
	for _, commits := range p.keptAround {
		if i := commitIndex(commits, ref); i >= 0 {
			return commits[i:], true
		}
	}
	return nil, false
}

func (s *Server) compare(w http.ResponseWriter, r *http.Request, p *project) {
	q := r.URL.Query()
	from, okFrom := history(p, q.Get("from"))
	to, okTo := history(p, q.Get("to"))
	if !okFrom || !okTo {
		message(w, http.StatusNotFound, "404 Ref Not Found")
		return
	}
	// GitLab lists the commits oldest first: those to reaches and from
	// does not, either way.
	commits := []gitlab.Commit{}
	diffs := []gitlab.Diff{}
	for i := len(to) - 1; i >= 0; i-- {
		c := to[i]
		if slices.ContainsFunc(from, func(f gitlab.Commit) bool { return f.ID == c.ID }) {
			continue
		}
		c.Stats = nil
		commits = append(commits, c)
		diffs = append(diffs, p.diffs[c.ID]...)
	}
	// The diff is the trees' when both are known: straight from from
	// itself (from..to), otherwise from the merge base (from...to), as
	// CompareService does. Without them, the commits' own diffs stand in.
	base := from[0].ID
	if q.Get("straight") != "true" {
		base = mergeBase(p, from[0].ID, to[0].ID)
	}
	if a, okA := snapshotAt(p, base); okA {
		if b, okB := snapshotAt(p, to[0].ID); okB {
			diffs = treeDiffs(a, b)
		}
	}
	out := gitlab.Compare{Commits: commits, Diffs: diffs, CompareSameRef: from[0].ID == to[0].ID,
		WebURL: p.WebURL + "/-/compare/" + url.PathEscape(q.Get("from")) + "..." + url.PathEscape(q.Get("to"))}
	var head any
	if len(commits) > 0 {
		head = commits[len(commits)-1]
	}
	writeJSON(w, http.StatusOK, withExtra(out, map[string]any{"commit": head}))
}

func (s *Server) listTags(w http.ResponseWriter, r *http.Request, p *project) {
	q := r.URL.Query()
	term := q.Get("search")
	rows := make([]gitlab.Tag, 0, len(p.tags))
	for _, t := range p.tags {
		name := t.Name
		switch {
		case strings.HasPrefix(term, "^") && !strings.HasPrefix(name, term[1:]):
			continue
		case strings.HasSuffix(term, "$") && !strings.HasSuffix(name, strings.TrimSuffix(term, "$")):
			continue
		case term != "" && !strings.ContainsAny(term[:1]+term[len(term)-1:], "^$") && !strings.Contains(name, term):
			continue
		}
		rows = append(rows, t)
	}
	asc := q.Get("sort") == "asc"
	slices.SortStableFunc(rows, func(a, b gitlab.Tag) int {
		var c int
		if q.Get("order_by") == "name" || q.Get("order_by") == "version" {
			c = strings.Compare(a.Name, b.Name)
		} else {
			c = a.Commit.CommittedDate.Compare(b.Commit.CommittedDate)
		}
		if !asc {
			c = -c
		}
		return c
	})
	writePage(s, w, r, rows)
}

// mergeBase is the newest commit both refs reach, "" when none: the
// histories here are each branch's commits, newest first.
func mergeBase(p *project, a, b string) string {
	ha, okA := history(p, a)
	hb, okB := history(p, b)
	if !okA || !okB {
		return ""
	}
	for _, c := range hb {
		if commitIndex(ha, c.ID) >= 0 {
			return c.ID
		}
	}
	return ""
}

// snapshotAt is the tree at a commit: its snapshot, or the tree of the
// branch it heads.
func snapshotAt(p *project, sha string) (map[string]string, bool) {
	if t, ok := p.snapshots[sha]; ok {
		return t, true
	}
	for name, commits := range p.commits {
		if len(commits) > 0 && commits[0].ID == sha {
			return p.trees[name], true
		}
	}
	return nil, false
}

// maxReviewers is Issuable::MAX_NUMBER_OF_ASSIGNEES_OR_REVIEWERS.
const maxReviewers = 200

// updateReviewerState is MergeRequests::UpdateReviewerStateService: a
// submitted review (reviewed, approved, requested_changes) by anyone but
// the author makes them a reviewer; an approved reviewer moves only to
// requested_changes or unapproved; a user who is not a reviewer is left
// alone otherwise.
func (s *Server) updateReviewerState(p *project, mr *gitlab.MergeRequest, user, state string) {
	i := slices.IndexFunc(mr.Reviewers, func(u gitlab.UserBasic) bool { return u.Username == user })
	submitted := state == "reviewed" || state == "approved" || state == "requested_changes"
	if i < 0 && submitted && mr.Author.Username != user && len(mr.Reviewers) < maxReviewers {
		mr.Reviewers = append(mr.Reviewers, s.user(user))
		i = len(mr.Reviewers) - 1
	}
	if i < 0 {
		return
	}
	key := reviewerKey(p.PathWithNamespace, mr.IID, user)
	if s.reviewerStates[key] == "approved" && state != "requested_changes" && state != "unapproved" {
		return
	}
	if s.reviewerStates == nil {
		s.reviewerStates = map[string]string{}
	}
	s.reviewerStates[key] = state
}

// resetOnPush is what a push to the source branch does to approvals:
// with the project's reset_approvals_on_push, a Premium setting, every
// approval is removed and each approver's review state becomes
// unapproved (EE MergeRequests::BaseService#delete_approvals). Without
// it nothing changes.
func (s *Server) resetOnPush(p *project, mr *gitlab.MergeRequest) {
	a := p.approvals[mr.IID]
	if !p.resetApprovalsOnPush || a == nil {
		return
	}
	for _, by := range a.ApprovedBy {
		key := reviewerKey(p.PathWithNamespace, mr.IID, by.User.Username)
		if _, reviewer := s.reviewerStates[key]; reviewer || slices.ContainsFunc(mr.Reviewers, func(u gitlab.UserBasic) bool {
			return u.Username == by.User.Username
		}) {
			if s.reviewerStates == nil {
				s.reviewerStates = map[string]string{}
			}
			s.reviewerStates[key] = "unapproved"
		}
	}
	a.ApprovedBy, a.Approved = []gitlab.Approver{}, false
}

// SetResetApprovalsOnPush turns the project's reset_approvals_on_push on
// or off.
func (s *Server) SetResetApprovalsOnPush(projectPath string, on bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil {
		return false
	}
	p.resetApprovalsOnPush = on
	return true
}

// PushFile commits one file's new content to a branch, as someone
// else's push does, and returns the commit's sha.
func (s *Server) PushFile(projectPath, branch, path, content string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil || len(p.commits[branch]) == 0 {
		return "", false
	}
	before := p.trees[branch]
	tree := cloneTree(before)
	tree[path] = content
	s.nextCommit++
	c := s.commit(p, "Change "+path+" ("+itoa(s.nextCommit)+")", "bob", s.opts.Now().UTC(), p.commits[branch][0].ID)
	var old *string
	if v, ok := before[path]; ok {
		old = &v
	}
	d, _, _ := fileDiff(path, path, old, &content)
	p.diffs[c.ID] = []gitlab.Diff{d}
	p.commits[branch] = append([]gitlab.Commit{c}, p.commits[branch]...)
	p.trees[branch] = tree
	p.snapshots[c.ID] = tree
	delete(p.fileCommits, branch)
	setBranch(p, branch)
	for _, mr := range p.mrs {
		if mr.SourceBranch == branch && mr.State == "opened" {
			s.refreshMR(p, mr)
			s.resetOnPush(p, mr)
		}
	}
	return c.ID, true
}

// rebaseMR replays a merge request's own commits onto its target's head,
// as GitLab's rebase does, when the tree at their merge base is known.
// Only the last replayed commit's tree is kept.
func (s *Server) rebaseMR(p *project, mr *gitlab.MergeRequest) {
	target, source := p.commits[mr.TargetBranch], p.commits[mr.SourceBranch]
	base := mergeBase(p, target[0].ID, source[0].ID)
	baseTree, ok := snapshotAt(p, base)
	if !ok || base == target[0].ID {
		return
	}
	tree := cloneTree(p.trees[mr.TargetBranch])
	for path := range baseTree {
		if _, kept := p.trees[mr.SourceBranch][path]; !kept {
			delete(tree, path)
		}
	}
	for path, content := range p.trees[mr.SourceBranch] {
		if was, ok := baseTree[path]; !ok || was != content {
			tree[path] = content
		}
	}
	own := source[:commitIndex(source, base)]
	parent := target[0].ID
	replayed := make([]gitlab.Commit, len(own))
	now := s.opts.Now().UTC()
	for i := len(own) - 1; i >= 0; i-- {
		s.nextCommit++
		c := s.commit(p, own[i].Title, "bob", now, parent)
		c.ID = fakeSHA(c.ID, "rebased", itoa(s.nextCommit))
		c.ShortID, c.WebURL = c.ID[:8], p.WebURL+"/-/commit/"+c.ID
		c.AuthorName, c.AuthorEmail = own[i].AuthorName, own[i].AuthorEmail
		p.diffs[c.ID] = p.diffs[own[i].ID]
		replayed[i] = c
		parent = c.ID
	}
	p.commits[mr.SourceBranch] = slices.Concat(replayed, target)
	p.trees[mr.SourceBranch] = tree
	p.snapshots[replayed[0].ID] = tree
	delete(p.fileCommits, mr.SourceBranch)
	setBranch(p, mr.SourceBranch)
	for _, m := range p.mrs {
		if m.SourceBranch == mr.SourceBranch && m.State == "opened" {
			s.refreshMR(p, m)
			s.resetOnPush(p, m)
		}
	}
}

// Rebase rebases a merge request at once, as rebase_merge_request asks
// GitLab to.
func (s *Server) Rebase(projectPath string, iid int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil {
		return false
	}
	mr := findMR(p, itoa(iid))
	if mr == nil {
		return false
	}
	s.rebaseMR(p, mr)
	return true
}
