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
// them from text: a merge request into the default branch closes the
// issues its title, description and commits name in a closing statement,
// and relates to every issue any of its text mentions. Each list leaves
// out, without saying so, what the user cannot read: here, a
// confidential issue.

// closingStatement is GitLab's default issue_closing_pattern
// (config/initializers/1_settings.rb at v19.4.1-ee) with the issue
// reference narrowed to this project's #12: a list such as "Closes #1,
// #2 and #3" closes each, and only a closing word's first letter may be
// upper case.
var closingStatement = regexp.MustCompile(`\b(?:(?:[Cc]los(?:e[sd]?|ing)|\b[Ff]ix(?:e[sd]|ing)?|\b[Rr]esolv(?:e[sd]?|ing)|` +
	`\b[Ii]mplement(?:s|ed|ing)?)(?::?) +(?:(?:issues? +)?#\d+(?:(?: *,? +and +| *,? *)?)|(?:[A-Z][A-Z0-9_]+-\d+))+)`)

var (
	// issueRef names an issue of this project, #12, or, when
	// Options.ExternalTracker gives ProjectAlpha one, an issue in its
	// external tracker, EXT-7.
	issueRef = regexp.MustCompile(`(?:^|[^\w&/])#(\d+)\b|\b(EXT-\d+)\b`)
	mrRef    = regexp.MustCompile(`(?:^|[^\w/])!(\d+)\b`)
)

// plannerLevel is the least access level that reads a confidential
// issue.
const plannerLevel = 15

// readable is GitLab's rule for a confidential issue: its author, its
// assignees and members at Planner and above, a group's members
// included, read it.
func (s *Server) readable(p *project, iss *gitlab.Issue, user string) bool {
	if !iss.Confidential || iss.Author.Username == user || has(usernames(iss.Assignees), user) {
		return true
	}
	for _, m := range s.members(p, "") {
		if m.Username == user {
			return m.AccessLevel >= plannerLevel
		}
	}
	return false
}

// issueMention is one issue a text names: a GitLab issue by iid, or an
// external tracker's by its id.
type issueMention struct {
	iid      int64
	external string
}

// mentions reads the issues a text names, in order and once each.
func mentions(text string) []issueMention {
	var out []issueMention
	for _, m := range issueRef.FindAllStringSubmatch(text, -1) {
		var im issueMention
		if m[1] != "" {
			im.iid, _ = strconv.ParseInt(m[1], 10, 64)
		} else {
			im.external = m[2]
		}
		if !slices.Contains(out, im) {
			out = append(out, im)
		}
	}
	return out
}

// mrIIDs reads the merge requests a text names, in order and once each.
func mrIIDs(text string) []int64 {
	var out []int64
	for _, m := range mrRef.FindAllStringSubmatch(text, -1) {
		if n, err := strconv.ParseInt(m[1], 10, 64); err == nil && !slices.Contains(out, n) {
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

// commitMessages is the messages of the commits a merge request brings.
func commitMessages(p *project, mr *gitlab.MergeRequest) string {
	var b strings.Builder
	for _, c := range mrCommits(p, mr) {
		b.WriteString(c.Message + "\n")
	}
	return b.String()
}

// closingText is the closing statements GitLab reads from a merge
// request into the default branch: in its title, description and commit
// messages. One into any other branch closes nothing.
func closingText(p *project, mr *gitlab.MergeRequest) string {
	if mr.TargetBranch != p.DefaultBranch {
		return ""
	}
	text := mr.Title + "\n" + mr.Description + "\n" + commitMessages(p, mr)
	return strings.Join(closingStatement.FindAllString(text, -1), "\n")
}

// closes reports whether a merge request closes an issue when merged.
func closes(p *project, mr *gitlab.MergeRequest, iid int64) bool {
	return slices.Contains(mentions(closingText(p, mr)), issueMention{iid: iid})
}

// mrText is all of a merge request's text GitLab reads for mentions: its
// title, description, comments and commit messages.
func mrText(p *project, mr *gitlab.MergeRequest) string {
	return mr.Title + "\n" + mr.Description + "\n" + notesText(p, "mr:"+strconv.FormatInt(mr.IID, 10)) + commitMessages(p, mr)
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
	mentioned := mrIIDs(iss.Description + "\n" + notesText(p, key))
	out := []gitlab.MergeRequest{}
	for _, mr := range p.mrs {
		if slices.Contains(mentioned, mr.IID) || slices.Contains(mentions(mrText(p, mr)), issueMention{iid: iss.IID}) {
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
		if closes(p, mr, iss.IID) {
			out = append(out, mrBasic(mr))
		}
	}
	return out
}

// linkedIssues is GET …/merge_requests/:iid/closes_issues when closing,
// and …/related_issues otherwise. As lib/api/merge_requests.rb does, the
// readable issues and any external tracker's are paged together in the
// order the text names them, and each page is then answered as its
// GitLab issues followed by its external ones, {title, id} with a string
// id.
func (s *Server) linkedIssues(w http.ResponseWriter, r *http.Request, p *project, mr *gitlab.MergeRequest, user string, closing bool) {
	text := mrText(p, mr)
	if closing {
		text = closingText(p, mr)
	}
	tracker := s.opts.ExternalTracker && p.PathWithNamespace == ProjectAlpha
	var all []map[string]any
	for _, m := range mentions(text) {
		if m.external != "" {
			if tracker {
				all = append(all, map[string]any{"title": "External Issue " + m.external, "id": m.external})
			}
			continue
		}
		iss := findIssue(p, strconv.FormatInt(m.iid, 10))
		if iss == nil || !s.readable(p, iss, user) {
			continue
		}
		// The issue rows are GitLab's IssueBasic, which has no references.
		row := withExtra(iss, nil)
		delete(row, "references")
		all = append(all, row)
	}
	start, end, ok := s.offsetPage(w, r, len(all), false)
	if !ok {
		return
	}
	out := []map[string]any{}
	var externals []map[string]any
	for _, row := range all[start:end] {
		if _, issue := row["iid"]; issue {
			out = append(out, row)
		} else {
			externals = append(externals, row)
		}
	}
	writeJSON(w, http.StatusOK, append(out, externals...))
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
