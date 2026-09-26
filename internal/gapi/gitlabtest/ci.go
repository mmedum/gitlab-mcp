package gitlabtest

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/gitlab"
)

// The CI half of the instance: pipelines, jobs, their stored logs and
// the CI lint. Every log is generated here, and the one secret in them
// is a synthetic token of the shape the masking catches.

// Fixture CI ids tests address.
const (
	// PipelineFailed is a failed pipeline on ProjectAlpha's main.
	PipelineFailed = 61001
	// JobPassed passed; JobFailed failed its script and has a long log
	// with the synthetic token in it; JobAllowed failed and was allowed
	// to; JobManual waits for a person.
	JobPassed  = 70001
	JobFailed  = 70002
	JobAllowed = 70003
	JobManual  = 70004
	// FakeToken is the synthetic token JobFailed's log prints.
	FakeToken = "glpat-" + "EXAMPLE0000000000000000000"
	// LogFiller is how many lines of test output JobFailed's log holds,
	// enough to be longer than one default window.
	LogFiller = 2500
)

// fillCI gives each merge request's head pipeline two passing jobs, and
// main one failed pipeline with a job of each outcome.
func (s *Server) fillCI(p *project) {
	nextJob := int64(JobManual + 1)
	for _, mr := range p.mrs {
		hp := mr.HeadPipeline
		pl := s.addPipeline(p, hp.ID, hp.SHA, hp.Ref, "success", "merge_request_event", mr.CreatedAt)
		for _, name := range []string{"build", "test"} {
			s.addJob(p, pl, nextJob, name, name, "success", "", false)
			nextJob++
		}
	}
	main := p.commits["main"][0]
	pl := s.addPipeline(p, PipelineFailed, main.ID, "main", "failed", "push", main.CommittedDate.Add(10*time.Minute))
	pl.BeforeSHA = p.commits["main"][1].ID
	s.addJob(p, pl, JobPassed, "build", "build", "success", "", false)
	s.addJob(p, pl, JobFailed, "unit tests", "test", "failed", "script_failure", false)
	s.addJob(p, pl, JobAllowed, "lint", "test", "failed", "script_failure", true)
	s.addJob(p, pl, JobManual, "deploy", "deploy", "manual", "", false)

	p.ciConfig["main"] = "stages: [build, test, deploy]\n\nbuild:\n  stage: build\n  script: make build\n\n" +
		"unit tests:\n  stage: test\n  script: make test\n\nlint:\n  stage: test\n  script: make lint\n  allow_failure: true\n\n" +
		"deploy:\n  stage: deploy\n  script: make deploy\n  when: manual\n"
	p.ciConfig["release/1.0"] = "build:\n  script: 42\n"
}

func (s *Server) addPipeline(p *project, id int64, sha, ref, status, source string, at time.Time) *gitlab.PipelineDetail {
	started, finished := at.Add(time.Minute), at.Add(9*time.Minute)
	duration, queued := int64(480), int64(60)
	u := s.user(DefaultUser)
	pl := &gitlab.PipelineDetail{ID: id, IID: int64(len(p.pipelines) + 1), ProjectID: p.ID, SHA: sha, Ref: ref, Status: status,
		Source: source, CreatedAt: at, UpdatedAt: finished, WebURL: fmt.Sprintf("%s/-/pipelines/%d", p.WebURL, id),
		User: &u, StartedAt: &started, FinishedAt: &finished, Duration: &duration, QueuedDuration: &queued,
		DetailedStatus: &gitlab.PipelineDetailStatus{Text: status, Label: status, Group: status}}
	p.pipelines = append(p.pipelines, pl)
	return pl
}

func (s *Server) addJob(p *project, pl *gitlab.PipelineDetail, id int64, name, stage, status, reason string, allowed bool) {
	j := gitlab.Job{ID: id, Name: name, Stage: stage, Status: status, Ref: pl.Ref, AllowFailure: allowed,
		FailureReason: reason, CreatedAt: pl.CreatedAt, WebURL: fmt.Sprintf("%s/-/jobs/%d", p.WebURL, id),
		Pipeline: gitlab.JobPipe{ID: pl.ID}}
	if status != "manual" {
		started, finished := pl.CreatedAt.Add(time.Minute), pl.CreatedAt.Add(3*time.Minute)
		d := 120.5
		j.StartedAt, j.FinishedAt, j.Duration = &started, &finished, &d
		p.traces[id] = jobLog(id, name, status == "failed")
		if id == JobAllowed {
			p.traces[id] = timestamped(p.traces[id], started)
		}
	}
	p.jobs[pl.ID] = append(p.jobs[pl.ID], j)
}

// jobLog writes a log as a runner stores it: ANSI colors, erase-line
// escapes, section markers, a progress line redrawn by carriage returns,
// and for a failure the runner's closing error line.
func jobLog(id int64, name string, failed bool) string {
	ts := 1767603600 + id
	var b strings.Builder
	section := func(kind, sec string) {
		fmt.Fprintf(&b, "\x1b[0Ksection_%s:%d:%s\r\x1b[0K", kind, ts, sec)
	}
	b.WriteString("\x1b[0KRunning with gitlab-runner 18.4.0\x1b[0;m\n")
	section("start", "prepare_executor")
	b.WriteString("\x1b[0K\x1b[36;1mPreparing the \"docker\" executor\x1b[0;m\n")
	b.WriteString("Pulling image 10%\rPulling image 55%\rPulling image done\n")
	section("end", "prepare_executor")
	b.WriteString("\n")
	section("start", "step_script")
	fmt.Fprintf(&b, "\x1b[0K\x1b[36;1mExecuting \"step_script\" stage of the job script\x1b[0;m\n\x1b[32;1m$ make %s\x1b[0;m\n", name)
	if failed && id == JobFailed {
		for i := range LogFiller {
			fmt.Fprintf(&b, "ok  \texample.test/pkg%04d\t0.0%ds\n", i, i%10)
		}
		b.WriteString("--- FAIL: TestLogin (0.00s)\n    login_test.go:12: the server refused token " + FakeToken + "\n")
		b.WriteString("FAIL\texample.test/login\t0.01s\n")
	} else {
		b.WriteString("all checks passed\n")
	}
	section("end", "step_script")
	b.WriteString("\n")
	section("start", "cleanup_file_variables")
	b.WriteString("\x1b[0K\x1b[36;1mCleaning up project directory and file based variables\x1b[0;m\n")
	section("end", "cleanup_file_variables")
	b.WriteString("\n")
	if failed {
		b.WriteString("\x1b[31;1mERROR: Job failed: exit code 1\n\x1b[0;m\n")
	} else {
		b.WriteString("\x1b[32;1mJob succeeded\x1b[0;m\n")
	}
	return b.String()
}

// timestamped writes a log as gitlab.com's runners do: each line after
// its time and stream marker, a line the runner flushed in two parts as a
// second line whose marker ends "+".
func timestamped(log string, at time.Time) string {
	var b strings.Builder
	for i, line := range strings.SplitAfter(log, "\n") {
		if line == "" {
			continue
		}
		stamp := at.Add(time.Duration(i) * time.Millisecond).UTC().Format("2006-01-02T15:04:05.000000Z")
		// A section marker and the header after it arrive as two writes.
		if j := strings.Index(line, "\r\x1b[0K"); j >= 0 && strings.Contains(line[:j], "section_start") {
			fmt.Fprintf(&b, "%s 00O %s\n%s 00O+%s", stamp, line[:j+len("\r\x1b[0K")], stamp, line[j+len("\r\x1b[0K"):])
			continue
		}
		fmt.Fprintf(&b, "%s 01O %s", stamp, line)
	}
	return b.String()
}

func (s *Server) findPipeline(p *project, id string) *gitlab.PipelineDetail {
	for _, pl := range p.pipelines {
		if strconv.FormatInt(pl.ID, 10) == id {
			return pl
		}
	}
	return nil
}

func findJob(p *project, id string) *gitlab.Job {
	for _, jobs := range p.jobs {
		for i := range jobs {
			if strconv.FormatInt(jobs[i].ID, 10) == id {
				return &jobs[i]
			}
		}
	}
	return nil
}

// serveCI serves GETs under /pipelines, /jobs and /ci; it reports
// whether the path was one of them.
func (s *Server) serveCI(w http.ResponseWriter, r *http.Request, p *project, seg []string) bool {
	switch {
	case match(seg, "pipelines"):
		s.listPipelines(w, r, p)
	case match(seg, "pipelines", "*"):
		pl := s.findPipeline(p, seg[1])
		if pl == nil {
			message(w, http.StatusNotFound, "404 Not found")
			return true
		}
		writeJSON(w, http.StatusOK, pl)
	case match(seg, "pipelines", "*", "jobs"):
		pl := s.findPipeline(p, seg[1])
		if pl == nil {
			message(w, http.StatusNotFound, "404 Not found")
			return true
		}
		scope := r.URL.Query().Get("scope")
		var rows []gitlab.Job
		for _, j := range p.jobs[pl.ID] {
			if scope == "" || j.Status == scope {
				rows = append(rows, j)
			}
		}
		writePage(s, w, r, rows)
	case match(seg, "jobs", "*"):
		j := findJob(p, seg[1])
		if j == nil {
			message(w, http.StatusNotFound, "404 Not found")
			return true
		}
		writeJSON(w, http.StatusOK, j)
	case match(seg, "jobs", "*", "trace"):
		j := findJob(p, seg[1])
		if j == nil {
			message(w, http.StatusNotFound, "404 Not found")
			return true
		}
		// GitLab serves the stored log as text, whatever was asked for.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(p.traces[j.ID]))
	case match(seg, "ci", "lint"):
		s.lint(w, r, p)
	default:
		return false
	}
	return true
}

func (s *Server) listPipelines(w http.ResponseWriter, r *http.Request, p *project) {
	q := r.URL.Query()
	var rows []*gitlab.PipelineDetail
	for _, pl := range p.pipelines {
		keep := true
		for key, v := range map[string]string{"ref": pl.Ref, "sha": pl.SHA, "status": pl.Status, "source": pl.Source,
			"username": pl.User.Username} {
			if want := q.Get(key); want != "" && want != v {
				keep = false
			}
		}
		if v, err := time.Parse(time.RFC3339, q.Get("updated_after")); err == nil && pl.UpdatedAt.Before(v) {
			keep = false
		}
		if v, err := time.Parse(time.RFC3339, q.Get("updated_before")); err == nil && pl.UpdatedAt.After(v) {
			keep = false
		}
		if keep {
			rows = append(rows, pl)
		}
	}
	asc := q.Get("sort") == "asc"
	slices.SortStableFunc(rows, func(a, b *gitlab.PipelineDetail) int {
		c := int(a.ID - b.ID)
		if q.Get("order_by") == "updated_at" {
			c = a.UpdatedAt.Compare(b.UpdatedAt)
		}
		if !asc {
			c = -c
		}
		return c
	})
	// The list carries only the first block of a pipeline's fields.
	list := make([]gitlab.Pipeline, 0, len(rows))
	for _, pl := range rows {
		list = append(list, gitlab.Pipeline{ID: pl.ID, IID: pl.IID, ProjectID: pl.ProjectID, SHA: pl.SHA, Ref: pl.Ref,
			Status: pl.Status, Source: pl.Source, CreatedAt: pl.CreatedAt, UpdatedAt: pl.UpdatedAt, WebURL: pl.WebURL})
	}
	writePage(s, w, r, list)
}

// lint answers the CI lint of a branch's configuration. main's is valid;
// release/1.0's gives a script as a number, which GitLab refuses.
func (s *Server) lint(w http.ResponseWriter, r *http.Request, p *project) {
	q := r.URL.Query()
	ref := q.Get("content_ref")
	if ref == "" {
		ref = p.DefaultBranch
	}
	cfg, ok := p.ciConfig[ref]
	if !ok {
		writeJSON(w, http.StatusOK, gitlab.Lint{Valid: false, Errors: []string{"Please provide content of .gitlab-ci.yml"},
			Warnings: []string{}, Jobs: []gitlab.LintJob{}})
		return
	}
	out := gitlab.Lint{Valid: true, Errors: []string{}, Warnings: []string{}, MergedYAML: cfg, Jobs: []gitlab.LintJob{}}
	if strings.Contains(cfg, "script: 42") {
		out = gitlab.Lint{Valid: false, Errors: []string{"jobs:build:script config should be a string or a nested array of strings up to 10 levels deep"},
			Warnings: []string{}, Jobs: []gitlab.LintJob{}}
	}
	if q.Get("dry_run") == "true" && out.Valid {
		out.Warnings = append(out.Warnings, "jobs:deploy may allow multiple pipelines to run for a single action due to `rules:when` clause with no `workflow:rules`")
	}
	if q.Get("include_jobs") == "true" && out.Valid {
		for _, j := range p.jobs[PipelineFailed] {
			when := "on_success"
			if j.Status == "manual" {
				when = "manual"
			}
			out.Jobs = append(out.Jobs, gitlab.LintJob{Name: j.Name, Stage: j.Stage, When: when, AllowFailure: j.AllowFailure})
		}
	}
	writeJSON(w, http.StatusOK, out)
}
