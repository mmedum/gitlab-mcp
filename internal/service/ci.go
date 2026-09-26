package service

import (
	"bytes"
	"context"
	"math"
	"regexp"
	"slices"
	"sync"

	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/internal/model"
	"github.com/mmedum/gitlab-mcp/internal/redact"
	"github.com/mmedum/gitlab-mcp/internal/render"
)

// ListPipelines lists a project's pipelines (§7.6).
func (s *Service) ListPipelines(ctx context.Context, raw string, q gapi.PipelineQuery, opts gapi.ListOptions) (model.Pipelines, error) {
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.Pipelines{}, err
	}
	rows, page, err := s.client.ListPipelines(ctx, p, q, opts)
	if err != nil {
		return model.Pipelines{}, err
	}
	out := model.Pipelines{Project: ref, Pipelines: make([]model.PipelineRow, 0, len(rows)), Listing: listing(len(rows), page)}
	for _, pl := range rows {
		out.Pipelines = append(out.Pipelines, model.PipelineRow{ID: pl.ID, IID: pl.IID, Status: pl.Status, Ref: pl.Ref,
			SHA: pl.SHA, Source: pl.Source, CreatedAt: pl.CreatedAt, UpdatedAt: pl.UpdatedAt, WebURL: pl.WebURL})
	}
	return out, nil
}

// GetPipeline reads a pipeline with its failed jobs (§7.6). The two reads
// are independent and run at once.
func (s *Service) GetPipeline(ctx context.Context, raw string, id int64) (model.PipelineDetail, error) {
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.PipelineDetail{}, err
	}
	var (
		wg        sync.WaitGroup
		pl        *gitlab.PipelineDetail
		failed    []gitlab.Job
		page      gapi.Page
		failedErr error
	)
	wg.Go(func() { pl, err = s.client.GetPipeline(ctx, p, id) })
	wg.Go(func() {
		failed, page, failedErr = s.client.ListPipelineJobs(ctx, p, id, gapi.JobQuery{Scope: "failed"},
			gapi.ListOptions{PerPage: gapi.MaxPerPage})
	})
	wg.Wait()
	if err != nil {
		return model.PipelineDetail{}, err
	}
	if failedErr != nil {
		return model.PipelineDetail{}, failedErr
	}
	out := model.PipelineDetail{Project: ref, ID: pl.ID, IID: pl.IID, Status: pl.Status, Ref: pl.Ref, Tag: pl.Tag,
		SHA: pl.SHA, BeforeSHA: pl.BeforeSHA, Source: pl.Source, CreatedAt: pl.CreatedAt, StartedAt: pl.StartedAt,
		FinishedAt: pl.FinishedAt, UpdatedAt: pl.UpdatedAt, Duration: pl.Duration, QueuedDuration: pl.QueuedDuration,
		WebURL: pl.WebURL, FailedJobs: make([]model.JobRow, 0, len(failed)), FailedJobsComplete: page.Complete()}
	if pl.DetailedStatus != nil {
		out.DetailedStatus = pl.DetailedStatus.Text
	}
	if pl.User != nil {
		u := user(*pl.User)
		out.User = &u
	}
	if pl.YAMLErrors != nil {
		out.UntrustedYAMLErrors, _ = render.Line(*pl.YAMLErrors, render.NoteBudget)
	}
	for _, j := range failed {
		out.FailedJobs = append(out.FailedJobs, jobRow(j))
	}
	return out, nil
}

// ListJobs lists a pipeline's jobs.
func (s *Service) ListJobs(ctx context.Context, raw string, pipeline int64, q gapi.JobQuery, opts gapi.ListOptions) (model.Jobs, error) {
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.Jobs{}, err
	}
	rows, page, err := s.client.ListPipelineJobs(ctx, p, pipeline, q, opts)
	if err != nil {
		return model.Jobs{}, err
	}
	out := model.Jobs{Project: ref, PipelineID: pipeline, Jobs: make([]model.JobRow, 0, len(rows)), Listing: listing(len(rows), page)}
	for _, j := range rows {
		out.Jobs = append(out.Jobs, jobRow(j))
	}
	return out, nil
}

func jobRow(j gitlab.Job) model.JobRow {
	return model.JobRow{ID: j.ID, Name: j.Name, Stage: j.Stage, Status: j.Status, AllowFailure: j.AllowFailure,
		FailureReason: j.FailureReason, CreatedAt: j.CreatedAt, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt,
		Duration: j.Duration, WebURL: j.WebURL}
}

// JobLogQuery is get_job_log's query. Offsets are bytes of the log as
// GitLab stores it.
type JobLogQuery struct {
	Project string
	JobID   int64
	// ByteOffset is where the window starts; nil takes the tail.
	ByteOffset *int
	// ByteLimit is the window's size; 0 takes render.JobLogBytes.
	ByteLimit  int
	FailedOnly bool
}

// GetJobLog shows a window of a job's log: the tail by default, from
// byte_offset when given, or the section the job failed in (§7.6). What
// it shows is cleaned as GitLab's page shows it, secret shapes masked
// (§4.1.4) and hidden characters made visible, inside a boundary.
func (s *Service) GetJobLog(ctx context.Context, q JobLogQuery) (model.JobLog, error) {
	limit := q.ByteLimit
	switch {
	case limit == 0:
		limit = render.JobLogBytes
	case limit < 0 || limit > render.MaxJobLogBytes:
		return model.JobLog{}, gapi.Errf(gapi.ClassInvalid, "byte_limit is 1 to %d", render.MaxJobLogBytes)
	}
	if q.FailedOnly && q.ByteOffset != nil {
		return model.JobLog{}, gapi.Errf(gapi.ClassInvalid, "pass byte_offset or failed_only, not both: failed_only finds its own start")
	}
	p, ref, err := s.project(ctx, q.Project)
	if err != nil {
		return model.JobLog{}, err
	}
	var (
		wg     sync.WaitGroup
		job    *gitlab.Job
		trace  []byte
		jobErr error
	)
	wg.Go(func() { job, jobErr = s.client.GetJob(ctx, p, q.JobID) })
	wg.Go(func() { trace, err = s.client.GetJobTrace(ctx, p, q.JobID) })
	wg.Wait()
	if jobErr != nil {
		return model.JobLog{}, jobErr
	}
	if err != nil {
		return model.JobLog{}, err
	}
	out := model.JobLog{Project: ref, Job: jobRow(*job), TotalBytes: len(trace)}

	var start, end int
	switch {
	case q.ByteOffset != nil:
		if *q.ByteOffset < 0 || *q.ByteOffset > len(trace) {
			return model.JobLog{}, gapi.Errf(gapi.ClassInvalid, "byte_offset %d is outside the log, which has %d bytes",
				*q.ByteOffset, len(trace))
		}
		start, end = *q.ByteOffset, min(len(trace), *q.ByteOffset+limit)
	case q.FailedOnly:
		sec, failEnd, inSection, failed := failedSection(trace)
		switch {
		case inSection:
			out.Section = sec.Name
			start, end = max(sec.Start, failEnd-limit), failEnd
		case failed:
			// A runner that writes no sections still ends at the failure.
			start, end = max(0, failEnd-limit), failEnd
		default:
			out.FailureNotFound = true
			start, end = max(0, len(trace)-limit), len(trace)
		}
	default:
		start, end = max(0, len(trace)-limit), len(trace)
	}
	start, end, keyOpen := widen(trace, start, end)
	out.ByteOffset, out.ByteEnd = start, end
	if start > 0 {
		prev := max(0, start-limit)
		out.PrevByteOffset = &prev
	}
	if end < len(trace) {
		next := end
		out.NextByteOffset = &next
	}
	text := render.Log(trace[start:end])
	if keyOpen {
		// The window starts inside a private key block that opened
		// earlier: its header is put back so the block is masked whole,
		// and the window keeps its size.
		text = keyHeader + "\n" + text
	}
	masked, n := redact.MaskSecrets(text)
	out.SecretsMasked = n
	out.UntrustedLog, out.HiddenRemoved = render.Code(masked)
	return out, nil
}

// failureText starts the line a runner writes when a job fails, and
// failureLine is that line from its start: after the runner's timestamp
// when it writes one, and colors.
var (
	failureText = []byte("ERROR: Job failed")
	failureLine = regexp.MustCompile(`^(?:` + render.TimestampPrefix + `)?(?:\x1b\[[0-9;]*m)*ERROR: Job failed`)
)

// failureAt finds the failure line: where it starts and where its text
// ends. The runner writes it last, so the search runs from the end, and a
// script that prints the same words earlier cannot move the window. The
// literal finds candidates, so a long log is not walked by a regular
// expression.
func failureAt(trace []byte) (lineStart, textEnd int, ok bool) {
	for to := len(trace); ; {
		at := bytes.LastIndex(trace[:to], failureText)
		if at < 0 {
			return 0, 0, false
		}
		start := bytes.LastIndexByte(trace[:at], '\n') + 1
		if failureLine.Match(trace[start : at+len(failureText)]) {
			return start, at + len(failureText), true
		}
		to = at
	}
}

// runnerAfterFailure are the sections a runner opens after the job's own
// script failed; the failure is in the section before them.
var runnerAfterFailure = []string{"after_script", "upload_artifacts_on_failure", "upload_artifacts_on_success",
	"cleanup_file_variables", "archive_cache", "archive_cache_on_failure"}

// failedSection finds where a failed job's failure line ends, and the
// section it failed in: of the job's own sections that start before the
// runner's "ERROR: Job failed" line, the one that ended last, or one
// still open. A section a script opens inside step_script ends before
// step_script does, so the script's own step is the one named.
// inSection is false when the log has a failure line but no section of
// the job's own before it.
func failedSection(trace []byte) (sec render.LogSection, failEnd int, inSection, failed bool) {
	lineStart, textEnd, failed := failureAt(trace)
	if !failed {
		return render.LogSection{}, 0, false, false
	}
	failEnd = len(trace)
	if i := bytes.IndexByte(trace[textEnd:], '\n'); i >= 0 {
		failEnd = textEnd + i + 1
	}
	for _, s := range render.LogSections(trace) {
		if s.Start >= lineStart || slices.Contains(runnerAfterFailure, s.Name) {
			continue
		}
		if !inSection || endsLater(s, sec) {
			sec, inSection = s, true
		}
	}
	return sec, failEnd, inSection, true
}

// endsLater reports whether a ended after b, an open section counting as
// ending last, and a later start breaking a tie.
func endsLater(a, b render.LogSection) bool {
	ae, be := a.End, b.End
	if ae < 0 {
		ae = math.MaxInt
	}
	if be < 0 {
		be = math.MaxInt
	}
	return ae > be || (ae == be && a.Start > b.Start)
}

// A window is widened to whole lines, so no single-line secret straddles
// its edge half-masked, as far as these reaches allow; past them, to the
// nearest space, which no token shape contains.
const (
	lineReach  = 4096
	spaceReach = 1024
)

// pemBegin is a private key block's first line, from its start.
var pemBegin = regexp.MustCompile(`^-----BEGIN [A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----`)

// lastKeyBegin finds where the last private key block in b starts, or
// -1, searching back by the literal rather than scanning all of b.
func lastKeyBegin(b []byte) int {
	for end := len(b); ; {
		i := bytes.LastIndex(b[:end], []byte("-----BEGIN "))
		if i < 0 || pemBegin.Match(b[i:]) {
			return i
		}
		end = i
	}
}

// keyHeader opens a private key block for the masking to match.
const keyHeader = "-----BEGIN PRIVATE KEY-----"

// widen moves a window's edges out to line boundaries. keyOpen reports a
// window that starts inside a private key block opened earlier and not
// yet closed, which the caller masks whole rather than shows from its
// middle; the block may have opened megabytes before, so the window is
// not moved back to it.
func widen(trace []byte, start, end int) (int, int, bool) {
	keyOpen := false
	if start > 0 {
		start = edgeBack(trace, start)
		last := lastKeyBegin(trace[:start])
		keyOpen = last >= 0 && !bytes.Contains(trace[last:start], []byte("-----END "))
	}
	if end < len(trace) {
		end = edgeForward(trace, end)
	}
	return start, end, keyOpen
}

// edgeBack moves i back to just after a newline, or failing that a
// space, within reach.
func edgeBack(b []byte, i int) int {
	if i > 0 && b[i-1] == '\n' {
		return i
	}
	lo := max(0, i-lineReach)
	if j := bytes.LastIndexByte(b[lo:i], '\n'); j >= 0 {
		return lo + j + 1
	}
	if lo == 0 {
		return 0
	}
	lo = max(0, i-spaceReach)
	if j := bytes.LastIndexAny(b[lo:i], " \t"); j >= 0 {
		return lo + j + 1
	}
	return i
}

// edgeForward moves i forward to just after a newline, or failing that
// a space, within reach.
func edgeForward(b []byte, i int) int {
	if b[i-1] == '\n' {
		return i
	}
	hi := min(len(b), i+lineReach)
	if j := bytes.IndexByte(b[i:hi], '\n'); j >= 0 {
		return i + j + 1
	}
	if hi == len(b) {
		return len(b)
	}
	hi = min(len(b), i+spaceReach)
	if j := bytes.IndexAny(b[i:hi], " \t"); j >= 0 {
		return i + j + 1
	}
	return i
}

// LintQuery is lint_ci's query.
type LintQuery struct {
	Project     string
	Ref         string
	Simulate    bool
	IncludeJobs bool
	Offset      int
}

// LintCI checks a project's CI configuration at a ref (§7.6). The merged
// configuration is shown under the file budget.
func (s *Service) LintCI(ctx context.Context, q LintQuery) (model.Lint, error) {
	p, ref, err := s.project(ctx, q.Project)
	if err != nil {
		return model.Lint{}, err
	}
	l, err := s.client.LintCI(ctx, p, gapi.LintQuery{Ref: q.Ref, Simulate: q.Simulate, IncludeJobs: q.IncludeJobs})
	if err != nil {
		return model.Lint{}, err
	}
	out := model.Lint{Project: ref, Ref: q.Ref, Simulate: q.Simulate, Valid: l.Valid, UntrustedErrors: lines(l.Errors),
		UntrustedWarnings: lines(l.Warnings), Jobs: []model.LintJob{}}
	for _, j := range l.Jobs {
		out.Jobs = append(out.Jobs, model.LintJob{Name: j.Name, Stage: j.Stage, When: j.When, AllowFailure: j.AllowFailure})
	}
	text, visible := render.Code(l.MergedYAML)
	if out.UntrustedMergedYAML, out.MergedYAMLBudget, err = cut(text, visible, q.Offset, render.FileBudget,
		"the merged configuration", "offset"); err != nil {
		return model.Lint{}, err
	}
	return out, nil
}

// lines prepares messages someone else's text is quoted in, one line
// each.
func lines(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		line, _ := render.Line(s, render.NoteBudget)
		out = append(out, line)
	}
	return out
}
