package gitlabtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
)

// The Ship and Destructive half of the instance: merging and approving,
// running and retrying CI, and deleting branches and comments. Answers
// follow lib/api/merge_requests.rb, merge_request_approvals.rb,
// ci/pipelines.rb, ci/jobs.rb, branches.rb and notes.rb at v19.4.1-ee.

// ------------------------------------------------------------ merging

// merge is PUT …/merge: 409 for a sha that is not the head, 405 for a
// merge request GitLab will not merge now, and the merge otherwise.
func (s *Server) merge(w http.ResponseWriter, r *http.Request, p *project, mr *gitlab.MergeRequest, user string) {
	b, ok := readBody(w, r)
	if !ok {
		return
	}
	if sha, sent := b.str("sha"); sent && sha != mr.SHA {
		message(w, http.StatusConflict, "SHA does not match HEAD of source branch: "+mr.SHA)
		return
	}
	if mr.State != "opened" || mr.Draft || mr.DetailedMergeStatus != "mergeable" {
		message(w, http.StatusMethodNotAllowed, "405 Method Not Allowed")
		return
	}
	if auto, _ := b.boolean("auto_merge"); auto && mr.HeadPipeline != nil && mr.HeadPipeline.Status != "success" {
		mr.MergeWhenPipelineSucceeds = true
		bump(&mr.UpdatedAt, s.opts.Now().UTC())
		writeJSON(w, http.StatusOK, mr)
		return
	}
	now := s.opts.Now().UTC()
	merger := s.user(user)
	commit := s.commit(p, "Merge branch '"+mr.SourceBranch+"' into '"+mr.TargetBranch+"'", user, now, p.commits[mr.TargetBranch][0].ID)
	commit.ParentIDs = append(commit.ParentIDs, mr.SHA)
	p.commits[mr.TargetBranch] = append([]gitlab.Commit{commit}, p.commits[mr.TargetBranch]...)
	p.trees[mr.TargetBranch] = cloneTree(p.trees[mr.SourceBranch])
	delete(p.fileCommits, mr.TargetBranch)
	setBranch(p, mr.TargetBranch)
	before := mrState(mr)
	mr.State, mr.MergedAt, mr.MergeUser, mr.MergeCommitSHA = "merged", &now, &merger, &commit.ID
	s.recordChanges(p, mrTarget(mr).key(), before, mrState(mr), user)
	mr.DetailedMergeStatus = "not_open"
	bump(&mr.UpdatedAt, now)
	if remove, _ := b.boolean("should_remove_source_branch"); remove {
		removeBranch(p, mr.SourceBranch)
	}
	writeJSON(w, http.StatusOK, mr)
}

// ------------------------------------------------------------ approving

// approvalsFor is a merge request's approvals as user sees them.
func approvalsFor(a *gitlab.Approvals, user string) gitlab.Approvals {
	out := *a
	out.ApprovedBy = slices.Clone(a.ApprovedBy)
	if out.ApprovedBy == nil {
		out.ApprovedBy = []gitlab.Approver{}
	}
	out.UserHasApproved = slices.ContainsFunc(a.ApprovedBy, func(ap gitlab.Approver) bool { return ap.User.Username == user })
	return out
}

// approve is POST …/approve: 409 for a sha that is not the head, 401
// when the user may not approve (here, the author, as a project that
// forbids author approval answers) or already has.
func (s *Server) approve(w http.ResponseWriter, r *http.Request, p *project, mr *gitlab.MergeRequest, user string) {
	b, ok := readBody(w, r)
	if !ok {
		return
	}
	if sha, sent := b.str("sha"); sent && sha != mr.SHA {
		message(w, http.StatusConflict, "SHA does not match HEAD of source branch: "+mr.SHA)
		return
	}
	a := p.approvals[mr.IID]
	if a == nil {
		a = &gitlab.Approvals{UserCanApprove: true}
		p.approvals[mr.IID] = a
	}
	if mr.Author.Username == user || approvalsFor(a, user).UserHasApproved {
		message(w, http.StatusUnauthorized, "401 Unauthorized")
		return
	}
	a.ApprovedBy = append(a.ApprovedBy, gitlab.Approver{User: s.user(user)})
	a.Approved = true
	writeJSON(w, http.StatusCreated, approvalsFor(a, user))
}

// unapprove is POST …/unapprove: 404 without the user's approval.
func (s *Server) unapprove(w http.ResponseWriter, p *project, mr *gitlab.MergeRequest, user string) {
	a := p.approvals[mr.IID]
	if a == nil || !approvalsFor(a, user).UserHasApproved {
		message(w, http.StatusNotFound, "404 Not Found")
		return
	}
	a.ApprovedBy = slices.DeleteFunc(a.ApprovedBy, func(ap gitlab.Approver) bool { return ap.User.Username == user })
	a.Approved = len(a.ApprovedBy) > 0
	writeJSON(w, http.StatusCreated, approvalsFor(a, user))
}

// ------------------------------------------------------------------ CI

// serveCIWrite routes the CI writes under a project.
func (s *Server) serveCIWrite(w http.ResponseWriter, r *http.Request, p *project, user string, seg []string) bool {
	switch {
	case match(seg, "pipeline"):
		s.createPipeline(w, r, p, user)
	case match(seg, "pipelines", "*", "retry"), match(seg, "pipelines", "*", "cancel"):
		pl := s.findPipeline(p, seg[1])
		if pl == nil {
			message(w, http.StatusNotFound, "404 Not found")
			return true
		}
		if seg[2] == "retry" {
			s.retryPipeline(w, p, pl, user)
		} else {
			s.cancelPipeline(w, p, pl)
		}
	case match(seg, "jobs", "*", "retry"), match(seg, "jobs", "*", "play"):
		j := findJob(p, seg[1])
		if j == nil {
			message(w, http.StatusNotFound, "404 Not found")
			return true
		}
		if seg[2] == "retry" {
			s.retryJob(w, r, p, j)
		} else {
			s.playJob(w, r, j)
		}
	default:
		return false
	}
	return true
}

func (s *Server) nextPipelineID(p *project) int64 {
	var id int64 = PipelineFailed
	for _, pl := range p.pipelines {
		id = max(id, pl.ID)
	}
	return id + 1
}

func (s *Server) nextJobID() int64 {
	var id int64 = JobManual
	for _, p := range s.projects {
		for _, jobs := range p.jobs {
			for _, j := range jobs {
				id = max(id, j.ID)
			}
		}
	}
	return id + 1
}

// createPipeline is POST …/pipeline: a pipeline on a branch or tag with
// one pending job. A ref that exists nowhere is GitLab's validation 400.
func (s *Server) createPipeline(w http.ResponseWriter, r *http.Request, p *project, user string) {
	b, ok := readBody(w, r)
	if !ok || !b.require(w, "ref") {
		return
	}
	ref, _ := b.str("ref")
	commits, found := history(p, ref)
	if !found {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": map[string]any{"base": []string{"Reference not found"}}})
		return
	}
	now := s.opts.Now().UTC()
	pl := s.addPipeline(p, s.nextPipelineID(p), commits[0].ID, ref, "pending", "api", now)
	u := s.user(user)
	pl.User, pl.StartedAt, pl.FinishedAt, pl.Duration, pl.QueuedDuration = &u, nil, nil, nil, nil
	pl.UpdatedAt = now
	pl.DetailedStatus = &gitlab.PipelineDetailStatus{Text: "pending", Label: "pending", Group: "pending"}
	p.jobs[pl.ID] = append(p.jobs[pl.ID], gitlab.Job{ID: s.nextJobID(), Name: "build", Stage: "build", Status: "pending", Ref: ref,
		CreatedAt: now, WebURL: p.WebURL + "/-/jobs/new", Pipeline: gitlab.JobPipe{ID: pl.ID}})
	writeJSON(w, http.StatusCreated, pl)
}

// retryPipeline is POST …/retry: each failed or canceled job gets a new
// pending job, and a pipeline with none is answered unchanged.
func (s *Server) retryPipeline(w http.ResponseWriter, p *project, pl *gitlab.PipelineDetail, user string) {
	retried := false
	for _, j := range slices.Clone(p.jobs[pl.ID]) {
		if j.Status == "failed" || j.Status == "canceled" {
			s.retryOne(p, j)
			retried = true
		}
	}
	if retried {
		now := s.opts.Now().UTC()
		pl.Status, pl.UpdatedAt, pl.FinishedAt = "running", now, nil
		u := s.user(user)
		pl.User = &u
	}
	writeJSON(w, http.StatusCreated, pl)
}

// retryOne adds a new pending job in j's place.
func (s *Server) retryOne(p *project, j gitlab.Job) gitlab.Job {
	now := s.opts.Now().UTC()
	n := gitlab.Job{ID: s.nextJobID(), Name: j.Name, Stage: j.Stage, Status: "pending", Ref: j.Ref, AllowFailure: j.AllowFailure,
		CreatedAt: now, Pipeline: j.Pipeline}
	n.WebURL = fmt.Sprintf("%s/-/jobs/%d", p.WebURL, n.ID)
	p.jobs[j.Pipeline.ID] = append(p.jobs[j.Pipeline.ID], n)
	return n
}

// cancelPipeline is POST …/cancel. GitLab answers 200 with the pipeline
// whether or not anything could be canceled. It cancels the jobs in the
// request and recomputes the pipeline's status in a worker after, so the
// answer carries the status from before and the next read the new one.
func (s *Server) cancelPipeline(w http.ResponseWriter, p *project, pl *gitlab.PipelineDetail) {
	answer := *pl
	if slices.Contains([]string{"created", "pending", "running", "manual", "scheduled"}, pl.Status) {
		pl.Status, pl.UpdatedAt = "canceled", s.opts.Now().UTC()
		for i, j := range p.jobs[pl.ID] {
			if j.Status == "created" || j.Status == "pending" || j.Status == "running" {
				p.jobs[pl.ID][i].Status = "canceled"
			}
		}
	}
	writeJSON(w, http.StatusOK, answer)
}

// retryJob is POST …/jobs/:id/retry: a new job, or 403 "Job is not
// retryable" for one still running or waiting.
func (s *Server) retryJob(w http.ResponseWriter, r *http.Request, p *project, j *gitlab.Job) {
	b, ok := readBody(w, r)
	if !ok {
		return
	}
	if !slices.Contains([]string{"failed", "canceled", "success"}, j.Status) {
		message(w, http.StatusForbidden, "403 Forbidden - Job is not retryable")
		return
	}
	if !s.takeInputs(w, b, "inputs") {
		return
	}
	writeJSON(w, http.StatusCreated, s.retryOne(p, *j))
}

// jobInputs are the inputs every job of the instance declares.
var jobInputs = []string{"target"}

// takeInputs checks the inputs a retry or play sent against the ones
// every job here declares, as GitLab's Ci::Inputs::ProcessorService does
// for a job with inputs, and keeps them. A refusal is answered.
func (s *Server) takeInputs(w http.ResponseWriter, b body, key string) bool {
	s.inputsSent = nil
	if !b.has(key) {
		return true
	}
	var in map[string]any
	if err := json.Unmarshal(b[key], &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": key + " is invalid"})
		return false
	}
	for name := range in {
		if !slices.Contains(jobInputs, name) {
			message(w, http.StatusBadRequest, "400 Bad request - Unknown input: "+name)
			return false
		}
	}
	s.inputsSent = in
	return true
}

// JobInputsSent are the input values the last job retry or play sent.
func (s *Server) JobInputsSent() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inputsSent
}

// playJob is POST …/jobs/:id/play: a manual job starts, anything else is
// 400 "Unplayable Job".
func (s *Server) playJob(w http.ResponseWriter, r *http.Request, j *gitlab.Job) {
	b, ok := readBody(w, r)
	if !ok {
		return
	}
	if j.Status != "manual" {
		message(w, http.StatusBadRequest, "400 Bad request - Unplayable Job")
		return
	}
	if !s.takeInputs(w, b, "job_inputs") {
		return
	}
	j.Status = "pending"
	writeJSON(w, http.StatusOK, j)
}

// ---------------------------------------------------------- destructive

// removeBranch drops a branch from every place the instance keeps it.
func removeBranch(p *project, name string) {
	p.branches = slices.DeleteFunc(p.branches, func(b gitlab.Branch) bool { return b.Name == name })
	delete(p.trees, name)
	delete(p.commits, name)
	delete(p.fileCommits, name)
}

// deleteBranch is DELETE …/repository/branches/:branch. GitLab's
// pre-receive refuses a protected branch with 400.
func (s *Server) deleteBranch(w http.ResponseWriter, p *project, name string) {
	if _, ok := p.trees[name]; !ok {
		message(w, http.StatusNotFound, "404 Branch Not Found")
		return
	}
	if protectedName(p, name) || name == p.DefaultBranch {
		message(w, http.StatusBadRequest, "You are not allowed to delete protected branch "+name)
		return
	}
	removeBranch(p, name)
	w.WriteHeader(http.StatusNoContent)
}

// findNote finds a note of an issue or merge request by id.
func findNote(p *project, t target, id string) (di, ni int) {
	for i, d := range p.discussions[t.key()] {
		for j, n := range d.Notes {
			if itoa(n.ID) == id {
				return i, j
			}
		}
	}
	return -1, -1
}

// getNote is GET …/notes/:note_id.
func (s *Server) getNote(w http.ResponseWriter, p *project, t target, id string) {
	di, ni := findNote(p, t, id)
	if di < 0 {
		message(w, http.StatusNotFound, "404 Note Not Found")
		return
	}
	writeJSON(w, http.StatusOK, p.discussions[t.key()][di].Notes[ni])
}

// deleteNote is DELETE …/notes/:note_id, conditional on
// If-Unmodified-Since as destroy_conditionally! makes it: a note changed
// after that time is 412. Only its author or a maintainer may delete it.
func (s *Server) deleteNote(w http.ResponseWriter, r *http.Request, p *project, t target, user, id string) {
	di, ni := findNote(p, t, id)
	if di < 0 {
		message(w, http.StatusNotFound, "404 Note Not Found")
		return
	}
	n := p.discussions[t.key()][di].Notes[ni]
	if n.Author.Username != user && p.levels[user] < 40 {
		message(w, http.StatusForbidden, "403 Forbidden")
		return
	}
	if since, err := time.Parse(time.RFC3339Nano, r.Header.Get("If-Unmodified-Since")); err == nil && n.UpdatedAt.After(since) {
		message(w, http.StatusPreconditionFailed, "412 Precondition Failed")
		return
	}
	ds := p.discussions[t.key()]
	ds[di].Notes = slices.Delete(ds[di].Notes, ni, ni+1)
	if len(ds[di].Notes) == 0 {
		ds = slices.Delete(ds, di, di+1)
	}
	p.discussions[t.key()] = ds
	w.WriteHeader(http.StatusNoContent)
}

// updateNote is PUT …/notes/:note_id, as the notes API and
// Notes::UpdateService make it: no If-Unmodified-Since, a note that is
// not editable (a system note) refused 403 by NotePolicy, quick actions
// run, and a body of commands alone deleting the note. Only its author or
// a maintainer may edit it.
func (s *Server) updateNote(w http.ResponseWriter, r *http.Request, p *project, t target, user, id string) {
	b, ok := readBody(w, r)
	if !ok || !b.require(w, "body") {
		return
	}
	di, ni := findNote(p, t, id)
	if di < 0 {
		message(w, http.StatusNotFound, "404 Note Not Found")
		return
	}
	ds := p.discussions[t.key()]
	n := &ds[di].Notes[ni]
	if n.System || n.Author.Username != user && p.levels[user] < 40 {
		message(w, http.StatusForbidden, "403 Forbidden")
		return
	}
	body, _ := b.str("body")
	cmds, kept := extract(body)
	s.runCommands(p, t, cmds, user)
	if kept == "" && len(cmds) > 0 {
		old := *n
		ds[di].Notes = slices.Delete(ds[di].Notes, ni, ni+1)
		if len(ds[di].Notes) == 0 {
			p.discussions[t.key()] = slices.Delete(ds, di, di+1)
		}
		writeJSON(w, http.StatusOK, old)
		return
	}
	n.Body = kept
	n.UpdatedAt = s.opts.Now().UTC()
	bump(t.updatedAt, n.UpdatedAt)
	writeJSON(w, http.StatusOK, *n)
}

// TouchNote moves a note's updated_at, as an edit made elsewhere does.
func (s *Server) TouchNote(projectPath, kind string, iid, noteID int64, at time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil {
		return false
	}
	t := target{kind: kind, iid: iid}
	di, ni := findNote(p, t, itoa(noteID))
	if di < 0 {
		return false
	}
	p.discussions[t.key()][di].Notes[ni].UpdatedAt = at
	return true
}

// PushTo adds a commit to a branch, as someone else's push does.
func (s *Server) PushTo(projectPath, branch string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil || len(p.commits[branch]) == 0 {
		return "", false
	}
	c := s.commit(p, "Pushed by someone else", "bob", s.opts.Now().UTC(), p.commits[branch][0].ID)
	p.commits[branch] = append([]gitlab.Commit{c}, p.commits[branch]...)
	setBranch(p, branch)
	for _, mr := range p.mrs {
		if mr.SourceBranch == branch && mr.State == "opened" {
			s.refreshMR(p, mr)
		}
	}
	return c.ID, true
}

// Approvals is a merge request's approvals as user sees them.
func (s *Server) Approvals(projectPath string, iid int64, user string) (gitlab.Approvals, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil || p.approvals[iid] == nil {
		return gitlab.Approvals{}, false
	}
	return approvalsFor(p.approvals[iid], user), true
}

// Pipeline is a pipeline as the instance holds it.
func (s *Server) Pipeline(projectPath string, id int64) (gitlab.PipelineDetail, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil {
		return gitlab.PipelineDetail{}, false
	}
	pl := s.findPipeline(p, itoa(id))
	if pl == nil {
		return gitlab.PipelineDetail{}, false
	}
	return *pl, true
}

// Job is a job as the instance holds it.
func (s *Server) Job(projectPath string, id int64) (gitlab.Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil {
		return gitlab.Job{}, false
	}
	j := findJob(p, itoa(id))
	if j == nil {
		return gitlab.Job{}, false
	}
	return *j, true
}

// SetMergeStatus sets a merge request's detailed_merge_status, as
// GitLab's checks do.
func (s *Server) SetMergeStatus(projectPath string, iid int64, status string) bool {
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
	mr.DetailedMergeStatus = status
	return true
}

// SetHeadPipelineStatus sets the status of a merge request's head
// pipeline, as CI running or failing does.
func (s *Server) SetHeadPipelineStatus(projectPath string, iid int64, status string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil {
		return false
	}
	mr := findMR(p, itoa(iid))
	if mr == nil || mr.HeadPipeline == nil {
		return false
	}
	mr.HeadPipeline.Status = status
	return true
}
