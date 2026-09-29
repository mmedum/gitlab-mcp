package gitlabtest

import (
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
)

// The lists that link issues, merge requests and commits. GitLab derives
// them from text: a merge request closes the issues its title and
// description name after a closing word, and relates to every issue any
// of its text mentions. Each list leaves out, without saying so, what the
// user cannot read: here, a confidential issue.

// closingWord is GitLab's default closing pattern, less its rarer
// spellings.
const closingWord = `(?i:\b(?:clos(?:e|es|ed|ing)|fix(?:es|ed|ing)?|resolv(?:e|es|ed|ing))\b:?\s+)`

var (
	closingRef = regexp.MustCompile(closingWord + `#(\d+)\b`)
	issueRef   = regexp.MustCompile(`(?:^|[^\w&/])#(\d+)\b`)
	mrRef      = regexp.MustCompile(`(?:^|[^\w/])!(\d+)\b`)
	// externalRef and closingExternalRef name an issue in ProjectAlpha's
	// external tracker, when Options.ExternalTracker gives it one.
	externalRef        = regexp.MustCompile(`\b(EXT-\d+)\b`)
	closingExternalRef = regexp.MustCompile(closingWord + `(EXT-\d+)\b`)
)

// plannerLevel is the least access level that reads a confidential
// issue.
const plannerLevel = 15

// readable is GitLab's rule for a confidential issue: its author, its
// assignees and members at Planner and above read it.
func readable(p *project, iss *gitlab.Issue, user string) bool {
	return !iss.Confidential || iss.Author.Username == user || has(usernames(iss.Assignees), user) ||
		p.levels[user] >= plannerLevel
}

// refs reads what pattern's first group captures, in order and once
// each.
func refs(pattern *regexp.Regexp, text string) []string {
	var out []string
	for _, m := range pattern.FindAllStringSubmatch(text, -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

// iids is refs for references by number.
func iids(pattern *regexp.Regexp, text string) []int64 {
	var out []int64
	for _, r := range refs(pattern, text) {
		if n, err := strconv.ParseInt(r, 10, 64); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// notesText is the text of every comment on an issue or merge request;
// key is "issue:12" or "mr:3".
func notesText(p *project, key string) string {
	var b strings.Builder
	for _, d := range p.discussions[key] {
		for _, n := range d.Notes {
			b.WriteString(n.Body + "\n")
		}
	}
	return b.String()
}

// closes reports whether a merge request closes an issue when merged.
func closes(mr *gitlab.MergeRequest, iid int64) bool {
	return slices.Contains(iids(closingRef, mr.Title+"\n"+mr.Description), iid)
}

// mrText is all of a merge request's text GitLab reads for mentions: its
// title, description, comments and commit messages.
func mrText(p *project, mr *gitlab.MergeRequest) string {
	var b strings.Builder
	b.WriteString(mr.Title + "\n" + mr.Description + "\n" + notesText(p, "mr:"+strconv.FormatInt(mr.IID, 10)))
	for _, c := range mrCommits(p, mr) {
		b.WriteString(c.Message + "\n")
	}
	return b.String()
}

// mrCommits are the commits a merge request brings: those on its source
// branch that are not on its target.
func mrCommits(p *project, mr *gitlab.MergeRequest) []gitlab.Commit {
	var out []gitlab.Commit
	for _, c := range p.commits[mr.SourceBranch] {
		if commitIndex(p.commits[mr.TargetBranch], c.ID) < 0 {
			out = append(out, c)
		}
	}
	return out
}

// mrBasic is a merge request as the lists show it, without what only
// the single read carries.
func mrBasic(mr *gitlab.MergeRequest) map[string]any {
	row := withExtra(mr, nil)
	delete(row, "diff_refs")
	delete(row, "head_pipeline")
	return row
}

// relatedMRs is GET …/issues/:iid/related_merge_requests: the merge
// requests the issue's text mentions, and those whose text mentions the
// issue (GitLab notes each such mention on the issue), by iid. It
// answers the full merge request entity.
func relatedMRs(p *project, iss *gitlab.Issue) []gitlab.MergeRequest {
	key := "issue:" + strconv.FormatInt(iss.IID, 10)
	mentioned := iids(mrRef, iss.Description+"\n"+notesText(p, key))
	out := []gitlab.MergeRequest{}
	for _, mr := range p.mrs {
		if slices.Contains(mentioned, mr.IID) || slices.Contains(iids(issueRef, mrText(p, mr)), iss.IID) {
			out = append(out, *mr)
		}
	}
	return out
}

// closedBy is GET …/issues/:iid/closed_by: the merge requests in the
// issue's own project that close it, by id.
func closedBy(p *project, iss *gitlab.Issue) []map[string]any {
	out := []map[string]any{}
	for _, mr := range p.mrs {
		if closes(mr, iss.IID) {
			out = append(out, mrBasic(mr))
		}
	}
	return out
}

// linkedIssues is GET …/merge_requests/:iid/closes_issues when closing,
// and …/related_issues otherwise: the issues the user can read, then any
// external tracker's issues as {title, id} with a string id.
func (s *Server) linkedIssues(p *project, mr *gitlab.MergeRequest, user string, closing bool) []map[string]any {
	issues, external := issueRef, externalRef
	text := mrText(p, mr)
	if closing {
		issues, external = closingRef, closingExternalRef
		text = mr.Title + "\n" + mr.Description
	}
	out := []map[string]any{}
	for _, iid := range iids(issues, text) {
		iss := findIssue(p, strconv.FormatInt(iid, 10))
		if iss == nil || !readable(p, iss, user) {
			continue
		}
		// The issue rows are GitLab's IssueBasic, which has no references.
		row := withExtra(iss, nil)
		delete(row, "references")
		out = append(out, row)
	}
	if s.opts.ExternalTracker && p.PathWithNamespace == ProjectAlpha {
		for _, id := range refs(external, text) {
			out = append(out, map[string]any{"title": "External Issue " + id, "id": id})
		}
	}
	return out
}

// commitMRs is GET …/repository/commits/:sha/merge_requests: the merge
// requests in the project that bring the commit or merged it, by id.
func commitMRs(w http.ResponseWriter, r *http.Request, p *project, sha string) ([]map[string]any, bool) {
	c := findCommit(p, sha)
	if c == nil {
		message(w, http.StatusNotFound, "404 Commit Not Found")
		return nil, false
	}
	state := r.URL.Query().Get("state")
	out := []map[string]any{}
	for _, mr := range p.mrs {
		brings := commitIndex(mrCommits(p, mr), c.ID) >= 0
		merged := (mr.MergeCommitSHA != nil && *mr.MergeCommitSHA == c.ID) || (mr.SquashCommitSHA != nil && *mr.SquashCommitSHA == c.ID)
		if (brings || merged) && (state == "" || state == mr.State) {
			out = append(out, mrBasic(mr))
		}
	}
	return out, true
}
