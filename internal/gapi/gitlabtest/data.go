package gitlabtest

import (
	"crypto/sha1" //nolint:gosec // fake object ids, not security
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/gitlab"
)

// The instance is generated from code, never recorded (docs/architecture.md
// §9.1): users from a fixed synthetic list, projects under example-group,
// bodies and diffs from templates, ids from counters in fixed ranges.

// Fixture names tests address.
const (
	// ProjectAlpha is public; alice maintains it. It has issues, merge
	// requests, discussions, files, branches and commits.
	ProjectAlpha = "example-group/alpha"
	// ProjectBeta is private under a subgroup; alice is a member.
	ProjectBeta = "example-group/sub/beta"
	// ProjectSecret is private and alice is not a member, so it answers
	// 404 exactly as a missing project does.
	ProjectSecret = "example-group/secret"
	// ProjectMoved is the old path of ProjectAlpha: a GET redirects, any
	// other method is 405.
	ProjectMoved = "example-group/old-alpha"
	// GroupTop and GroupSub are the two groups.
	GroupTop = "example-group"
	GroupSub = "example-group/sub"
	// DefaultUser is who a default token and an authorization act as.
	DefaultUser = "alice"
)

// Id ranges, fixed so a test can state an id literally.
const (
	firstUserID    = 1001
	firstProjectID = 2001
	firstGroupID   = 3001
	firstIssueID   = 30001
	firstMRID      = 40001
	firstNoteID    = 50001
)

// Users is the synthetic user list, in id order from 1001.
var Users = []string{"alice", "bob", "carol", "dave"}

// epoch is the fixture clock's start; every generated time is after it.
var epoch = time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)

type group struct {
	id       int64
	path     string
	name     string
	parentID int64
}

type project struct {
	gitlab.Project
	private bool
	members map[string]bool
	groupID int64

	issues      []*gitlab.Issue
	mrs         []*gitlab.MergeRequest
	approvals   map[int64]*gitlab.Approvals
	discussions map[string][]gitlab.Discussion // "issue:12", "mr:3"

	// trees maps a branch to its files. A commit sha addresses the tree
	// of the branch it heads.
	trees     map[string]map[string]string
	branches  []gitlab.Branch
	commits   map[string][]gitlab.Commit // branch → newest first
	diffs     map[string][]gitlab.Diff   // sha → diffs
	protected []gitlab.ProtectedBranch
	// fileCommits is, per branch a write has moved, the last commit that
	// touched each file; a branch absent here counts its head (§2.10).
	fileCommits map[string]map[string]string

	// Review: each merge request's diffs, and the default user's drafts.
	mrDiffs map[int64][]gitlab.Diff
	drafts  map[int64][]gitlab.DraftNote
	tags    []gitlab.Tag
	// protectedTags are the project's protected-tag rules.
	protectedTags []gitlab.ProtectedTag

	// CI: pipelines newest last, their jobs, each job's stored log, and
	// the CI configuration per branch.
	pipelines []*gitlab.PipelineDetail
	jobs      map[int64][]gitlab.Job
	bridges   map[int64][]gitlab.Bridge
	traces    map[int64]string
	ciConfig  map[string]string

	// Planning: the project's own labels and milestones, and each
	// member's access level; members above says only who may see it.
	labels     []gitlab.Label
	milestones []gitlab.ProjectMilestone
	levels     map[string]int

	// The optional toolsets: wiki pages, releases, environments,
	// deployments and events. Snippets are the server's, since a
	// personal one has no project.
	wiki         []gitlab.WikiPage
	releases     []gitlab.Release
	environments []gitlab.Environment
	deployments  []gitlab.Deployment
	events       []event
}

// fakeSHA is a stable 40-hex id for a name.
func fakeSHA(parts ...string) string {
	sum := sha1.Sum([]byte(strings.Join(parts, "\x00"))) //nolint:gosec // fake ids
	return hex.EncodeToString(sum[:])
}

func (s *Server) user(name string) gitlab.UserBasic {
	for i, u := range Users {
		if u == name {
			return gitlab.UserBasic{ID: int64(firstUserID + i), Username: u, Name: titleCase(u) + " Example",
				State: "active", WebURL: s.URL + "/" + u}
		}
	}
	return gitlab.UserBasic{}
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (s *Server) generate() {
	s.groups = []*group{
		{id: firstGroupID, path: GroupTop, name: "Example Group"},
		{id: firstGroupID + 1, path: GroupSub, name: "Sub", parentID: firstGroupID},
	}
	s.nextProjectID = firstProjectID
	s.nextIssueID = firstIssueID
	s.nextMRID = firstMRID
	s.nextNoteID = firstNoteID

	alpha := s.newProject(GroupTop, "alpha", "Alpha", "public", firstGroupID, "alice", "bob")
	s.fillAlpha(alpha)
	beta := s.newProject(GroupSub, "beta", "Beta", "private", firstGroupID+1, "alice")
	s.fillSmall(beta)
	secret := s.newProject(GroupTop, "secret", "Secret", "private", firstGroupID, "bob")
	s.fillSmall(secret)
	for i := range s.opts.ExtraProjects {
		p := s.newProject(GroupTop, fmt.Sprintf("bulk-%04d", i+1), fmt.Sprintf("Bulk %d", i+1), "public", firstGroupID, "carol")
		s.fillSmall(p)
	}
	s.moved = map[string]string{ProjectMoved: ProjectAlpha}
}

func (s *Server) newProject(namespace, path, name, visibility string, groupID int64, members ...string) *project {
	id := s.nextProjectID
	s.nextProjectID++
	full := namespace + "/" + path
	created := epoch.Add(time.Duration(id-firstProjectID) * time.Hour)
	var ns gitlab.Namespace
	for _, g := range s.groups {
		if g.id == groupID {
			ns = gitlab.Namespace{ID: g.id, Name: g.name, Path: g.path[strings.LastIndex(g.path, "/")+1:], FullPath: g.path, Kind: "group"}
		}
	}
	open := 0
	p := &project{
		Project: gitlab.Project{
			ID: id, Name: name, NameWithNamespace: "Example Group / " + name, Path: path, PathWithNamespace: full,
			Description: "A generated project for tests.", DefaultBranch: "main", Visibility: visibility,
			WebURL: s.URL + "/" + full, Topics: []string{"example"}, CreatedAt: created, LastActivityAt: &created,
			Namespace: ns, OpenIssuesCount: &open,
		},
		private:     visibility == "private",
		members:     map[string]bool{},
		groupID:     groupID,
		approvals:   map[int64]*gitlab.Approvals{},
		discussions: map[string][]gitlab.Discussion{},
		trees:       map[string]map[string]string{},
		commits:     map[string][]gitlab.Commit{},
		diffs:       map[string][]gitlab.Diff{},
		fileCommits: map[string]map[string]string{},
		mrDiffs:     map[int64][]gitlab.Diff{},
		drafts:      map[int64][]gitlab.DraftNote{},
		jobs:        map[int64][]gitlab.Job{},
		bridges:     map[int64][]gitlab.Bridge{},
		traces:      map[int64]string{},
		ciConfig:    map[string]string{},
		levels:      map[string]int{},
	}
	// The first member maintains the project; the rest develop it.
	for i, m := range members {
		p.members[m] = true
		p.levels[m] = 30
		if i == 0 {
			p.levels[m] = 40
		}
	}
	s.projects = append(s.projects, p)
	return p
}

// fillSmall gives a project one issue and a one-file repository.
func (s *Server) fillSmall(p *project) {
	s.addIssue(p, "First issue", "Generated description.", "bob", nil, nil)
	s.addCommits(p, "main", 1, map[string]string{"README.md": "# " + p.Name + "\n"})
	s.addBranch(p, "main", true, true)
}

// fillAlpha is the rich project most tests read.
func (s *Server) fillAlpha(p *project) {
	labels := [][]string{{"bug"}, {"feature", "priority::high"}, {"bug", "docs"}, {"docs"}, {"feature"}}
	authors := []string{"alice", "bob", "carol", "alice", "dave"}
	for i := range s.opts.AlphaIssues {
		assignee := []string{Users[(i+1)%len(Users)]}
		iss := s.addIssue(p, fmt.Sprintf("Generated issue %d", i+1),
			fmt.Sprintf("Steps to reproduce item %d.\n\n- [ ] first\n- [x] second\n", i+1),
			authors[i%len(authors)], labels[i%len(labels)], assignee)
		if i%4 == 3 {
			closed := iss.CreatedAt.Add(48 * time.Hour)
			iss.State, iss.ClosedAt = "closed", &closed
			by := s.user("alice")
			iss.ClosedBy = &by
		}
		s.addDiscussions(p, "issue", iss.IID, iss.ID, "Issue", iss.CreatedAt, nil)
	}

	files := map[string]string{
		"README.md":           "# Alpha\n\nA generated repository.\n",
		"src/main.go":         "package main\n\nfunc main() {}\n",
		"src/util/strings.go": "package util\n\n// Upper is a stub.\nfunc Upper(s string) string { return s }\n",
		"docs/guide.md":       "# Guide\n\nRead me.\n",
		"docs/with space.md":  "# Spaced name\n",
		"assets/logo.png":     "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR",
	}
	for i := range 12 {
		files[fmt.Sprintf("docs/pages/page-%02d.md", i+1)] = fmt.Sprintf("# Page %d\n", i+1)
	}
	s.addCommits(p, "main", s.opts.AlphaCommits, files)
	s.addBranch(p, "main", true, true)

	feature := cloneTree(files)
	feature["src/login.go"] = "package main\n\n// login is a stub.\nfunc login() {}\n"
	s.branchFrom(p, "main", "feature/login", feature, "Add login stub", "bob")
	s.addBranch(p, "feature/login", false, false)
	s.branchFrom(p, "main", "release/1.0", cloneTree(files), "Prepare release", "alice")
	s.addBranch(p, "release/1.0", false, true)

	forty := 40
	p.protected = []gitlab.ProtectedBranch{
		{ID: 1, Name: "main", PushAccessLevels: []gitlab.AccessLevel{{ID: 11, AccessLevel: &forty, AccessLevelDescription: "Maintainers"}},
			MergeAccessLevels: []gitlab.AccessLevel{{ID: 12, AccessLevel: &forty, AccessLevelDescription: "Maintainers"}}},
		{ID: 2, Name: "release/*", PushAccessLevels: []gitlab.AccessLevel{{ID: 21, AccessLevel: &forty, AccessLevelDescription: "Maintainers"}},
			MergeAccessLevels: []gitlab.AccessLevel{{ID: 22, AccessLevel: &forty, AccessLevelDescription: "Maintainers"}}},
	}

	head := p.commits["feature/login"][0]
	base := p.commits["main"][0]
	s.fillTags(p)
	for i := range s.opts.AlphaMergeRequests {
		mr := s.addMR(p, fmt.Sprintf("Generated change %d", i+1), "feature/login", "main", authors[(i+1)%len(authors)], base.ID, head.ID)
		s.fillReview(p, mr)
		pos := &gitlab.Position{BaseSHA: base.ID, StartSHA: base.ID, HeadSHA: head.ID, PositionType: "text",
			OldPath: "src/login.go", NewPath: "src/login.go", NewLine: intPtr(3)}
		s.addDiscussions(p, "mr", mr.IID, mr.ID, "MergeRequest", mr.CreatedAt, pos)
		p.approvals[mr.IID] = &gitlab.Approvals{Approved: i%2 == 0, UserCanApprove: true}
		if i%2 == 0 {
			p.approvals[mr.IID].ApprovedBy = []gitlab.Approver{{User: s.user("carol")}}
		}
	}
	s.fillCI(p)
	s.fillPlanning(p)
	s.fillToolsets(p)
}

func intPtr(n int) *int { return &n }

func cloneTree(t map[string]string) map[string]string {
	out := make(map[string]string, len(t))
	for k, v := range t {
		out[k] = v
	}
	return out
}

func (s *Server) addIssue(p *project, title, desc, author string, labels, assignees []string) *gitlab.Issue {
	iid := int64(len(p.issues) + 1)
	created := p.CreatedAt.Add(time.Duration(iid) * 6 * time.Hour)
	iss := &gitlab.Issue{
		ID: s.nextIssueID, IID: iid, ProjectID: p.ID, Title: title, Description: desc, State: "opened", Type: "ISSUE",
		CreatedAt: created, UpdatedAt: created.Add(time.Hour), Labels: append([]string{}, labels...),
		Author: s.user(author), Assignees: []gitlab.UserBasic{}, WebURL: fmt.Sprintf("%s/-/issues/%d", p.WebURL, iid),
		References: gitlab.References{Short: fmt.Sprintf("#%d", iid), Relative: fmt.Sprintf("#%d", iid),
			Full: fmt.Sprintf("%s#%d", p.PathWithNamespace, iid)},
		TaskCompletion: &gitlab.TaskCompletion{Count: 2, CompletedCount: 1},
	}
	for _, a := range assignees {
		iss.Assignees = append(iss.Assignees, s.user(a))
	}
	s.nextIssueID++
	p.issues = append(p.issues, iss)
	return iss
}

func (s *Server) addMR(p *project, title, source, target, author, baseSHA, headSHA string) *gitlab.MergeRequest {
	iid := int64(len(p.mrs) + 1)
	created := p.CreatedAt.Add(time.Duration(iid) * 5 * time.Hour)
	mr := &gitlab.MergeRequest{
		ID: s.nextMRID, IID: iid, ProjectID: p.ID, Title: title, Description: "Generated merge request body.",
		State: "opened", CreatedAt: created, UpdatedAt: created.Add(time.Hour), Author: s.user(author),
		Assignees: []gitlab.UserBasic{s.user(author)}, Reviewers: []gitlab.UserBasic{s.user("carol")},
		SourceBranch: source, TargetBranch: target, SourceProjectID: p.ID, TargetProjectID: p.ID,
		Labels: []string{"feature"}, DetailedMergeStatus: "mergeable", SHA: headSHA, ChangesCount: "1",
		WebURL: fmt.Sprintf("%s/-/merge_requests/%d", p.WebURL, iid),
		References: gitlab.References{Short: fmt.Sprintf("!%d", iid), Relative: fmt.Sprintf("!%d", iid),
			Full: fmt.Sprintf("%s!%d", p.PathWithNamespace, iid)},
		DiffRefs:     &gitlab.DiffRefs{BaseSHA: baseSHA, HeadSHA: headSHA, StartSHA: baseSHA},
		HeadPipeline: &gitlab.PipelineBasic{ID: 60000 + iid, IID: iid, SHA: headSHA, Ref: source, Status: "success"},
	}
	s.nextMRID++
	p.mrs = append(p.mrs, mr)
	return mr
}

// addDiscussions adds a two-note thread, a single comment and a system
// note. A position makes the thread a diff thread.
func (s *Server) addDiscussions(p *project, kind string, iid, noteableID int64, noteableType string, at time.Time, pos *gitlab.Position) {
	key := fmt.Sprintf("%s:%d", kind, iid)
	iidCopy := iid
	note := func(author, body string, offset time.Duration, system bool) gitlab.Note {
		n := gitlab.Note{ID: s.nextNoteID, Body: body, Author: s.user(author), CreatedAt: at.Add(offset),
			UpdatedAt: at.Add(offset), System: system, NoteableID: noteableID, NoteableType: noteableType,
			NoteableIID: &iidCopy}
		s.nextNoteID++
		return n
	}
	thread := gitlab.Discussion{ID: fakeSHA(key, "thread"), Notes: []gitlab.Note{
		note("bob", "Can you add a test for this?", time.Hour, false),
		note("alice", "Added one.", 2*time.Hour, false),
	}}
	typ := "DiscussionNote"
	if pos != nil {
		typ = "DiffNote"
	}
	for i := range thread.Notes {
		thread.Notes[i].Type = &typ
		thread.Notes[i].Resolvable = true
		thread.Notes[i].Position = pos
	}
	single := gitlab.Discussion{ID: fakeSHA(key, "single"), IndividualNote: true,
		Notes: []gitlab.Note{note("carol", "Looks fine to me.", 3*time.Hour, false)}}
	system := gitlab.Discussion{ID: fakeSHA(key, "system"), IndividualNote: true,
		Notes: []gitlab.Note{note("alice", "added ~bug label", 4*time.Hour, true)}}
	p.discussions[key] = append(p.discussions[key], thread, single, system)
}

// addCommits writes n commits on branch, the last of which holds files.
func (s *Server) addCommits(p *project, branch string, n int, files map[string]string) {
	var list []gitlab.Commit
	var parent string
	paths := make([]string, 0, len(files))
	for k := range files {
		paths = append(paths, k)
	}
	sort.Strings(paths)
	for i := range n {
		when := p.CreatedAt.Add(time.Duration(i+1) * time.Hour)
		author := Users[i%2]
		file := paths[i%len(paths)]
		c := s.commit(p, fmt.Sprintf("Update %s (%d)", file, i+1), author, when, parent)
		p.diffs[c.ID] = []gitlab.Diff{{OldPath: file, NewPath: file, AMode: "100644", BMode: "100644",
			Diff: fmt.Sprintf("@@ -1,2 +1,3 @@\n line one\n+generated change %d\n line two\n", i+1)}}
		parent = c.ID
		list = append([]gitlab.Commit{c}, list...)
	}
	p.commits[branch] = list
	p.trees[branch] = files
}

func (s *Server) commit(p *project, title, author string, when time.Time, parent string) gitlab.Commit {
	id := fakeSHA(p.PathWithNamespace, title, when.String())
	c := gitlab.Commit{ID: id, ShortID: id[:8], Title: title, Message: title + "\n",
		AuthorName: titleCase(author) + " Example", AuthorEmail: author + "@example.com", AuthoredDate: when,
		CommitterName: titleCase(author) + " Example", CommitterEmail: author + "@example.com", CommittedDate: when,
		CreatedAt: when, ParentIDs: []string{}, WebURL: p.WebURL + "/-/commit/" + id,
		Stats: &gitlab.CommitStats{Additions: 1, Deletions: 0, Total: 1}}
	if parent != "" {
		c.ParentIDs = []string{parent}
	}
	return c
}

// branchFrom starts branch at from's head with one more commit.
func (s *Server) branchFrom(p *project, from, branch string, files map[string]string, title, author string) {
	parent := p.commits[from][0]
	c := s.commit(p, title, author, parent.CommittedDate.Add(30*time.Minute), parent.ID)
	var changed string
	for k := range files {
		if _, ok := p.trees[from][k]; !ok {
			changed = k
		}
	}
	if changed == "" {
		changed = "README.md"
	}
	p.diffs[c.ID] = []gitlab.Diff{{OldPath: changed, NewPath: changed, AMode: "0", BMode: "100644", NewFile: true,
		Diff: "@@ -0,0 +1,3 @@\n+package main\n+\n+// generated\n"}}
	p.commits[branch] = append([]gitlab.Commit{c}, p.commits[from]...)
	p.trees[branch] = files
}

func (s *Server) addBranch(p *project, name string, isDefault, protected bool) {
	p.branches = append(p.branches, gitlab.Branch{Name: name, Default: isDefault, Protected: protected,
		CanPush: true, DevelopersCanPush: !protected, DevelopersCanMerge: !protected,
		WebURL: p.WebURL + "/-/tree/" + name, Commit: p.commits[name][0]})
}
