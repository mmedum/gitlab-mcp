package gitlabtest

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
)

// Suggestions and auto-merge. A diff comment on a merge request carries
// suggestion blocks, and applying them commits to the source branch as
// the user; answers follow lib/api/suggestions.rb,
// app/services/suggestions/apply_service.rb,
// lib/gitlab/suggestions/suggestion_set.rb and app/models/suggestion.rb
// at v19.4.1-ee. GitLab moves a comment to a new head some time after a
// push; this instance never does, so a suggestion made before a push is
// refused after it, as GitLab refuses it until then.

// firstSuggestionID is the first id AddSuggestion hands out.
const firstSuggestionID = 130001

// suggestionAt addresses a suggestion: its project, its merge request,
// and its place in the merge request's threads.
type suggestionAt struct {
	p          *project
	mr         *gitlab.MergeRequest
	key        string
	d, n, i    int
	suggestion *gitlab.Suggestion
	note       *gitlab.Note
}

func (s *Server) findSuggestion(id int64) (suggestionAt, bool) {
	for _, p := range s.projects {
		for key, threads := range p.discussions {
			if !strings.HasPrefix(key, "mr:") {
				continue
			}
			for d := range threads {
				for n := range threads[d].Notes {
					note := &threads[d].Notes[n]
					for i := range note.Suggestions {
						if note.Suggestions[i].ID == id {
							return suggestionAt{p: p, mr: findMR(p, strings.TrimPrefix(key, "mr:")), key: key, d: d, n: n, i: i,
								suggestion: &note.Suggestions[i], note: note}, true
						}
					}
				}
			}
		}
	}
	return suggestionAt{}, false
}

// AddSuggestion adds a diff thread by author on a merge request, on path
// at the merge request's head, whose comment suggests replacement for
// lines from to to of the source branch's file. It returns the comment's
// and the suggestion's ids.
func (s *Server) AddSuggestion(projectPath string, iid int64, author, path string, from, to int, replacement string) (int64, int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil {
		return 0, 0, false
	}
	mr := findMR(p, itoa(iid))
	if mr == nil {
		return 0, 0, false
	}
	lines := splitLines(p.trees[mr.SourceBranch][path])
	if from < 1 || to < from || to > len(lines) {
		return 0, 0, false
	}
	if s.nextSuggestionID == 0 {
		s.nextSuggestionID = firstSuggestionID
	}
	sg := gitlab.Suggestion{ID: s.nextSuggestionID, FromLine: from, ToLine: to, Appliable: true,
		FromContent: strings.Join(lines[from-1:to], "\n") + "\n", ToContent: replacement}
	s.nextSuggestionID++
	now := s.opts.Now().UTC()
	typ := "DiffNote"
	iidCopy := iid
	note := gitlab.Note{ID: s.nextNoteID, Type: &typ, Body: fmt.Sprintf("Suggested change:\n```suggestion:-%d+0\n%s```\n", to-from,
		replacement), Author: s.user(author), CreatedAt: now, UpdatedAt: now, NoteableID: mr.ID, NoteableType: "MergeRequest",
		NoteableIID: &iidCopy, Resolvable: true, Suggestions: []gitlab.Suggestion{sg},
		Position: &gitlab.Position{BaseSHA: mr.DiffRefs.BaseSHA, StartSHA: mr.DiffRefs.StartSHA, HeadSHA: mr.SHA,
			PositionType: "text", OldPath: path, NewPath: path, NewLine: intPtr(to)}}
	s.nextNoteID++
	key := "mr:" + itoa(iid)
	p.discussions[key] = append(p.discussions[key], gitlab.Discussion{ID: fakeSHA(key, "suggestion", itoa(sg.ID)),
		Notes: []gitlab.Note{note}})
	return note.ID, sg.ID, true
}

// serveSuggestions routes PUT /suggestions/:id/apply and
// /suggestions/batch_apply; it reports whether it answered.
func (s *Server) serveSuggestions(w http.ResponseWriter, r *http.Request, user string, seg []string) bool {
	if r.Method != http.MethodPut || len(seg) < 2 || seg[0] != "suggestions" {
		return false
	}
	b, ok := readBody(w, r)
	if !ok {
		return true
	}
	var ids []int64
	batch := match(seg, "suggestions", "batch_apply")
	switch {
	case batch:
		if !b.require(w, "ids") {
			return true
		}
		ids, _ = b.ints("ids")
	case match(seg, "suggestions", "*", "apply"):
		id, err := strconv.ParseInt(seg[1], 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "id is invalid"})
			return true
		}
		ids = []int64{id}
	default:
		return false
	}
	// Suggestion.id_in drops a repeated id and the count no longer
	// matches, which GitLab answers 404.
	var found []suggestionAt
	for _, id := range ids {
		if at, ok := s.findSuggestion(id); ok && !slices.ContainsFunc(found, func(f suggestionAt) bool { return f.suggestion.ID == id }) {
			found = append(found, at)
		}
	}
	if len(found) != len(ids) {
		msg := "Suggestion is not applicable as the suggestion was not found."
		if batch {
			msg = "Suggestions are not applicable as one or more suggestions were not found."
		}
		message(w, http.StatusNotFound, msg)
		return true
	}
	msg, _ := b.str("commit_message")
	s.applySuggestions(w, user, found, msg, batch)
	return true
}

// applySuggestions checks the suggestions as GitLab does, first failure
// first, and commits them in one commit.
func (s *Server) applySuggestions(w http.ResponseWriter, user string, found []suggestionAt, msg string, batch bool) {
	first := found[0]
	src := first.p
	if first.mr.SourceProjectID != src.ID {
		src = s.projectByID(first.mr.SourceProjectID)
	}
	branch := first.mr.SourceBranch
	for _, at := range found {
		// SuggestionPolicy: the user may push to the source branch.
		need := developerAccess
		if src != nil && protectedName(src, branch) {
			need = maintainerAccess
		}
		if src == nil || s.accessLevel(src, user) < need {
			message(w, http.StatusForbidden, "403 Forbidden")
			return
		}
		if problem := suggestionProblem(src, first.mr, at); problem != "" {
			message(w, http.StatusBadRequest, problem)
			return
		}
	}
	if overlapping(found) {
		message(w, http.StatusBadRequest, "Suggestions are not applicable as their lines cannot overlap.")
		return
	}
	before := cloneTree(src.trees[branch])
	tree := cloneTree(before)
	var actions []commitAction
	var paths []string
	byFile := map[string][]suggestionAt{}
	for _, at := range found {
		path := at.note.Position.NewPath
		if byFile[path] == nil {
			paths = append(paths, path)
		}
		byFile[path] = append(byFile[path], at)
	}
	for _, path := range paths {
		group := byFile[path]
		// Bottom up, so a replacement does not move the lines of the next.
		slices.SortFunc(group, func(a, b suggestionAt) int { return b.suggestion.FromLine - a.suggestion.FromLine })
		lines := splitLines(tree[path])
		for _, at := range group {
			sg := at.suggestion
			repl := splitLines(sg.ToContent)
			lines = slices.Concat(lines[:sg.FromLine-1], repl, lines[sg.ToLine:])
		}
		tree[path] = strings.Join(lines, "\n") + "\n"
		actions = append(actions, commitAction{Action: "update", FilePath: path})
	}
	if msg == "" {
		msg = "Apply %{suggestions_count} suggestion(s) to %{files_count} file(s)"
	}
	msg = strings.NewReplacer("%{suggestions_count}", strconv.Itoa(len(found)), "%{files_count}", strconv.Itoa(len(paths)),
		"%{branch_name}", branch, "%{username}", user, "%{co_authored_by}", "").Replace(msg)
	// GitLab answers with the suggestions it read before the commit.
	answer := make([]gitlab.Suggestion, 0, len(found))
	for _, at := range found {
		answer = append(answer, *at.suggestion)
	}
	s.writeCommit(src, branch, msg, user, actions, before, tree)
	for _, at := range found {
		at.suggestion.Applied, at.suggestion.Appliable = true, false
	}
	if batch {
		writeJSON(w, http.StatusOK, answer)
		return
	}
	writeJSON(w, http.StatusOK, answer[0])
}

// suggestionProblem is GitLab's reason one suggestion cannot apply now,
// "" when it can (SuggestionSet#error_for_suggestion).
func suggestionProblem(src *project, mr *gitlab.MergeRequest, at suggestionAt) string {
	sg := at.suggestion
	commits, branchExists := src.commits[mr.SourceBranch]
	content, fileExists := src.trees[mr.SourceBranch][at.note.Position.NewPath]
	lines := splitLines(content)
	switch {
	case branchExists && !fileExists:
		return "A file was not found."
	case at.mr != mr:
		return "Suggestions must all be on the same branch."
	case sg.Applied:
		return "Can't apply this suggestion."
	case mr.State == "merged":
		return "This merge request was merged. To apply this suggestion, edit this file directly."
	case mr.State == "closed":
		return "This merge request is closed. To apply this suggestion, edit this file directly."
	case !branchExists || len(commits) == 0:
		return "Can't apply as the source branch was deleted."
	case sg.ToLine > len(lines) || strings.Join(lines[sg.FromLine-1:sg.ToLine], "\n")+"\n" != sg.FromContent:
		if sg.FromLine == sg.ToLine {
			return "Can't apply as this line was changed in a more recent version."
		}
		return "Can't apply as these lines were changed in a more recent version."
	case sg.FromContent == sg.ToContent:
		return "This suggestion already matches its content."
	case at.note.Position.HeadSHA != commits[0].ID:
		return "A file has been changed."
	}
	return ""
}

// overlapping reports two suggestions on one file whose lines meet.
func overlapping(found []suggestionAt) bool {
	for i, a := range found {
		for _, b := range found[i+1:] {
			if a.note.Position.NewPath == b.note.Position.NewPath &&
				a.suggestion.FromLine <= b.suggestion.ToLine && b.suggestion.FromLine <= a.suggestion.ToLine {
				return true
			}
		}
	}
	return false
}

// Suggestion is a suggestion as the instance holds it.
func (s *Server) Suggestion(id int64) (gitlab.Suggestion, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.findSuggestion(id)
	if !ok {
		return gitlab.Suggestion{}, false
	}
	return *at.suggestion, true
}

// ------------------------------------------------------------ auto-merge

// cancelAutoMerge is POST …/cancel_merge_when_pipeline_succeeds: 401
// for a user who may not merge the merge request and did not write it,
// and otherwise 201 with the service's result as the body, an error
// included (lib/api/merge_requests.rb, app/services/auto_merge_service.rb).
func (s *Server) cancelAutoMerge(w http.ResponseWriter, p *project, mr *gitlab.MergeRequest, user string) {
	need := developerAccess
	if protectedName(p, mr.TargetBranch) {
		need = maintainerAccess
	}
	if mr.Author.Username != user && s.accessLevel(p, user) < need {
		message(w, http.StatusUnauthorized, "401 Unauthorized")
		return
	}
	if mr.State != "opened" || !mr.MergeWhenPipelineSucceeds {
		writeJSON(w, http.StatusCreated, map[string]any{"status": "error", "message": "Can't cancel the automatic merge",
			"http_status": 406})
		return
	}
	mr.MergeWhenPipelineSucceeds, mr.MergeUser = false, nil
	bump(&mr.UpdatedAt, s.opts.Now().UTC())
	writeJSON(w, http.StatusCreated, map[string]any{"status": "success"})
}

// SetAutoMerge sets a merge request to merge when its pipeline succeeds,
// as user, as a merge with auto_merge does while the pipeline runs.
func (s *Server) SetAutoMerge(projectPath string, iid int64, user string) bool {
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
	u := s.user(user)
	mr.MergeWhenPipelineSucceeds, mr.MergeUser = true, &u
	return true
}
