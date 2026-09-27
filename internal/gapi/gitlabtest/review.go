package gitlabtest

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/gitlab"
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

// fillTags tags release/1.0's head and main's head.
func (s *Server) fillTags(p *project) {
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
	// GitLab lists the commits oldest first.
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
