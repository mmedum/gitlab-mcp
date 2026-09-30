package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/mmedum/gitlab-mcp/v2/internal/config"
	"github.com/mmedum/gitlab-mcp/v2/internal/gapi"
	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/v2/internal/model"
	"github.com/mmedum/gitlab-mcp/v2/internal/redact"
	"github.com/mmedum/gitlab-mcp/v2/internal/render"
	"gopkg.in/yaml.v3"
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
		wg                    sync.WaitGroup
		pl                    *gitlab.PipelineDetail
		failed                []gitlab.Job
		triggers              []gitlab.Bridge
		page, triggerPage     gapi.Page
		failedErr, triggerErr error
	)
	wg.Go(func() { pl, err = s.client.GetPipeline(ctx, p, id) })
	wg.Go(func() {
		failed, page, failedErr = s.client.ListPipelineJobs(ctx, p, id, gapi.JobQuery{Scope: "failed"},
			gapi.ListOptions{PerPage: gapi.MaxPerPage})
	})
	wg.Go(func() {
		triggers, triggerPage, triggerErr = s.client.ListPipelineTriggerJobs(ctx, p, id, gapi.JobQuery{Scope: "failed"},
			gapi.ListOptions{PerPage: gapi.MaxPerPage})
	})
	wg.Wait()
	for _, e := range []error{err, failedErr} {
		if e != nil {
			return model.PipelineDetail{}, e
		}
	}
	// The trigger jobs add to the pipeline; a failure to read them leaves
	// them unknown rather than failing the read, as the thread summary does.
	if triggerErr != nil {
		if !soft(triggerErr) {
			return model.PipelineDetail{}, triggerErr
		}
		triggers, triggerPage = nil, gapi.Page{NextToken: "unread"}
	}
	out := model.PipelineDetail{Project: ref, ID: pl.ID, IID: pl.IID, Status: pl.Status, Ref: pl.Ref, Tag: pl.Tag,
		SHA: pl.SHA, BeforeSHA: pl.BeforeSHA, Source: pl.Source, CreatedAt: pl.CreatedAt, StartedAt: pl.StartedAt,
		FinishedAt: pl.FinishedAt, UpdatedAt: pl.UpdatedAt, Duration: pl.Duration, QueuedDuration: pl.QueuedDuration,
		WebURL: pl.WebURL, FailedJobs: make([]model.JobRow, 0, len(failed)), FailedJobsComplete: page.Complete(),
		FailedTriggerJobsComplete: triggerPage.Complete(),
		FailedTriggerJobs:         make([]model.TriggerJobRow, 0, len(triggers))}
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
	for _, b := range triggers {
		out.FailedTriggerJobs = append(out.FailedTriggerJobs, bridgeRow(b))
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

// bridgeRow is a trigger job as a job row, with the pipeline it started.
func bridgeRow(b gitlab.Bridge) model.TriggerJobRow {
	row := model.TriggerJobRow{JobRow: jobRow(gitlab.Job{ID: b.ID, Name: b.Name, Stage: b.Stage, Status: b.Status,
		AllowFailure: b.AllowFailure, FailureReason: b.FailureReason, CreatedAt: b.CreatedAt, StartedAt: b.StartedAt,
		FinishedAt: b.FinishedAt, Duration: b.Duration, WebURL: b.WebURL})}
	if d := b.DownstreamPipeline; d != nil {
		row.Downstream = &model.DownstreamRow{ID: d.ID, ProjectID: d.ProjectID, Status: d.Status, WebURL: d.WebURL}
	}
	return row
}

// maxTestSuites and maxTestFailures bound one get_test_report result;
// suite rows take at most a quarter of its budget.
const (
	maxTestSuites   = 100
	maxTestFailures = 100
	suiteBudget     = render.TestReportBudget / 4
	// outputReach is how much of a case's output is prepared: far past
	// what is shown, so a secret that straddles its end is never shown.
	outputReach = 4 * render.TestOutputBudget
)

// unfinished are the pipeline states whose test report may still grow.
var unfinished = []string{"created", "waiting_for_resource", "preparing", "pending", "running", "scheduled", "manual",
	"canceling"}

// GetTestReport reads a pipeline's test report (§7.6): the counts, each
// suite's, and the failed and errored cases from offset under the
// budget. GitLab parses the report when read and does not page it, so
// each call reads it again; one larger than the client reads falls back
// to the stored summary, which has counts and no cases. The report takes
// in child pipelines in the project, so a child still running makes it
// partial too. Case names and output are the project's tests' words:
// masked like a job log and prepared for the boundary.
func (s *Service) GetTestReport(ctx context.Context, raw string, pipeline int64, offset int) (model.TestReport, error) {
	if offset < 0 {
		return model.TestReport{}, gapi.Errf(gapi.ClassInvalid, "offset is 0 or more")
	}
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.TestReport{}, err
	}
	var (
		wg                    sync.WaitGroup
		pl                    *gitlab.PipelineDetail
		report                *gitlab.TestReport
		triggers              []gitlab.Bridge
		reportErr, triggerErr error
	)
	wg.Go(func() { pl, err = s.client.GetPipeline(ctx, p, pipeline) })
	wg.Go(func() { report, reportErr = s.client.GetTestReport(ctx, p, pipeline) })
	wg.Go(func() {
		triggers, _, triggerErr = s.client.ListPipelineTriggerJobs(ctx, p, pipeline, gapi.JobQuery{},
			gapi.ListOptions{PerPage: gapi.MaxPerPage})
	})
	wg.Wait()
	if err != nil {
		return model.TestReport{}, err
	}
	// The trigger jobs only tell whether a child is still running; a
	// failure to read them leaves that unsaid, as get_pipeline does.
	if triggerErr != nil && !soft(triggerErr) {
		return model.TestReport{}, triggerErr
	}
	out := model.TestReport{Project: ref, PipelineID: pl.ID, PipelineStatus: pl.Status, Offset: offset,
		BudgetChars: render.TestReportBudget, Suites: []model.TestSuiteRow{}, Failures: []model.TestFailure{}}
	out.Partial, out.PartialReason = partial(pl, triggers)
	switch {
	case gapi.TooLarge(reportErr):
		return s.testSummary(ctx, p, pipeline, out)
	case reportErr != nil:
		return model.TestReport{}, reportErr
	}
	out.Total = model.TestCounts{Total: report.TotalCount, Success: report.SuccessCount, Failed: report.FailedCount,
		Skipped: report.SkippedCount, Error: report.ErrorCount, Seconds: report.TotalTime}
	var used int
	out.Suites, out.SuitesTotal, used = testSuites(report.TestSuites, &out)
	var failing []failingCase
	for _, suite := range report.TestSuites {
		for _, c := range suite.TestCases {
			if c.Status == "failed" || c.Status == "error" {
				failing = append(failing, failingCase{suite: suite.Name, TestCase: c})
			}
		}
	}
	out.FailuresTotal = len(failing)
	if offset > len(failing) {
		return model.TestReport{}, gapi.Errf(gapi.ClassInvalid, "offset %d is past the end of the failed and errored cases, "+
			"which number %d", offset, len(failing))
	}
	for i := offset; i < len(failing); i++ {
		f, masked, hidden := failing[i].prepare()
		cost := utf8.RuneCountInString(f.UntrustedName + f.UntrustedClassname + f.UntrustedFile + f.UntrustedOutput)
		// The first case always fits: suite rows take at most a quarter
		// of the budget, and one case far less than the rest.
		if used+cost > render.TestReportBudget || len(out.Failures) == maxTestFailures {
			next := i
			out.NextOffset = &next
			break
		}
		used += cost
		out.Failures = append(out.Failures, f)
		out.SecretsMasked += masked
		out.HiddenRemoved += hidden
	}
	return out, nil
}

// partial says whether a pipeline's report may still grow, and why: the
// pipeline has not finished, or a child pipeline in its project, whose
// jobs the report takes in, has not.
func partial(pl *gitlab.PipelineDetail, triggers []gitlab.Bridge) (bool, string) {
	if slices.Contains(unfinished, pl.Status) {
		return true, "the pipeline is " + pl.Status
	}
	for _, b := range triggers {
		d := b.DownstreamPipeline
		if d != nil && d.ProjectID == pl.ProjectID && slices.Contains(unfinished, d.Status) {
			return true, fmt.Sprintf("child pipeline %d is %s", d.ID, d.Status)
		}
	}
	return false, ""
}

// testSummary fills a result from GitLab's stored summary, for a report
// too large to read: counts, no cases.
func (s *Service) testSummary(ctx context.Context, p gapi.Project, pipeline int64, out model.TestReport) (model.TestReport, error) {
	if out.Offset > 0 {
		return model.TestReport{}, gapi.Errf(gapi.ClassInvalid, "the report is larger than this server reads, so no case is "+
			"listed and offset does not apply; call without it for the counts")
	}
	sum, err := s.client.GetTestReportSummary(ctx, p, pipeline)
	if err != nil {
		return model.TestReport{}, err
	}
	t := sum.Total
	out.FromSummary = true
	out.Total = model.TestCounts{Total: t.Count, Success: t.Success, Failed: t.Failed, Skipped: t.Skipped, Error: t.Error, Seconds: t.Time}
	out.FailuresTotal = t.Failed + t.Error
	if t.SuiteError != nil {
		var masked, hidden int
		out.UntrustedTotalSuiteError, masked, hidden = untrustedLine(*t.SuiteError)
		out.SecretsMasked += masked
		out.HiddenRemoved += hidden
	}
	out.Suites, out.SuitesTotal, _ = testSuites(sum.TestSuites, &out)
	return out, nil
}

// testSuites lists suites with a failure, an error or a report GitLab
// could not read first, at most maxTestSuites of them and suiteBudget
// characters, and returns how many there are and the characters used.
// What it masks and handles is counted into out.
func testSuites(in []gitlab.TestSuite, out *model.TestReport) ([]model.TestSuiteRow, int, int) {
	rows := make([]model.TestSuiteRow, 0, min(len(in), maxTestSuites))
	failing := func(t gitlab.TestSuite) bool { return t.FailedCount+t.ErrorCount > 0 || t.SuiteError != nil }
	used, full := 0, false
	for _, first := range []bool{true, false} {
		for _, t := range in {
			if failing(t) != first || full {
				continue
			}
			row := model.TestSuiteRow{Name: t.Name, TestCounts: model.TestCounts{Total: t.TotalCount, Success: t.SuccessCount,
				Failed: t.FailedCount, Skipped: t.SkippedCount, Error: t.ErrorCount, Seconds: t.TotalTime}}
			masked, hidden := 0, 0
			if t.SuiteError != nil {
				row.UntrustedSuiteError, masked, hidden = untrustedLine(*t.SuiteError)
			}
			cost := utf8.RuneCountInString(row.Name + row.UntrustedSuiteError)
			if len(rows) == maxTestSuites || used+cost > suiteBudget {
				full = true
				continue
			}
			used += cost
			out.SecretsMasked += masked
			out.HiddenRemoved += hidden
			rows = append(rows, row)
		}
	}
	return rows, len(in), used
}

// untrustedLine prepares one line of a test report someone else wrote:
// colors dropped, hidden characters removed before the masks run, so a
// zero-width space cannot split a token past them, then cut. It returns
// the line and how many secrets and hidden characters it handled.
func untrustedLine(s string) (string, int, int) {
	clean, hidden := render.Line(render.StripANSI(s), 0)
	masked, n := redact.MaskSecrets(clean)
	line, _ := render.Line(masked, render.TitleChars*2)
	return line, n, hidden
}

// failingCase is a failed or errored case and the suite it is in.
type failingCase struct {
	suite string
	gitlab.TestCase
}

// prepare makes a case ready to show: token shapes masked, hidden
// characters handled, the output cut at render.TestOutputBudget. It
// returns how many secrets it masked and hidden characters it handled.
// Only the output's first outputReach characters are prepared; a key
// block that runs past them is masked to their end.
func (c failingCase) prepare() (model.TestFailure, int, int) {
	masked, hidden := 0, 0
	line := func(s *string) string {
		if s == nil {
			return ""
		}
		text, m, h := untrustedLine(*s)
		masked += m
		hidden += h
		return text
	}
	f := model.TestFailure{Suite: c.suite, Status: c.Status, UntrustedName: line(&c.Name), UntrustedClassname: line(c.Classname),
		UntrustedFile: line(c.File), Seconds: c.ExecutionTime}
	var output string
	if c.SystemOutput != nil {
		output = *c.SystemOutput
	}
	head := output[:byteAt(output, outputReach)]
	text, n := redact.MaskSecrets(render.StripANSI(head))
	masked += n
	text, n = render.Code(text)
	hidden += n
	shown, b := render.Cut(text, 0, render.TestOutputBudget)
	f.UntrustedOutput, f.OutputChars, f.OutputCut = shown, b.TotalChars, b.ContinueOffset != nil
	if len(head) < len(output) {
		f.OutputChars, f.OutputCut = utf8.RuneCountInString(output), true
	}
	return f, masked, hidden
}

// byteAt is the byte index n characters into s, or its length.
func byteAt(s string, n int) int {
	for i := range s {
		if n == 0 {
			return i
		}
		n--
	}
	return len(s)
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
// (§4.1.4) and hidden characters made visible, inside a boundary. Only
// the window and what its edges need are read, so a log of any size can
// be shown (§7.6).
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
	job, err := s.client.GetJob(ctx, p, q.JobID)
	if err != nil {
		return model.JobLog{}, err
	}
	r := &logReader{ctx: ctx, client: s.client, p: p, job: q.JobID}
	if err := r.measure(*job); err != nil {
		return model.JobLog{}, err
	}
	out := model.JobLog{Project: ref, Job: jobRow(*job)}

	var start, end int
	switch {
	case q.ByteOffset != nil:
		if *q.ByteOffset < 0 || *q.ByteOffset > r.size {
			return model.JobLog{}, gapi.Errf(gapi.ClassInvalid, "byte_offset %d is outside the log, which has %d bytes",
				*q.ByteOffset, r.size)
		}
		start, end = *q.ByteOffset, min(r.size, *q.ByteOffset+limit)
	case q.FailedOnly:
		f, err := r.failure(slices.Contains(failingStatuses, job.Status))
		if err != nil {
			return model.JobLog{}, err
		}
		switch {
		case f.inSection:
			out.Section = f.section.Name
			// A section begun before what was read has Start -1.
			start, end = max(0, f.section.Start, f.end-limit), f.end
		case f.found:
			// A runner that writes no sections still ends at the failure.
			start, end = max(0, f.end-limit), f.end
		default:
			out.FailureNotFound = true
			start, end = max(0, r.size-limit), r.size
		}
	default:
		start, end = max(0, r.size-limit), r.size
	}
	// The window, as far each edge may move to a line boundary, and before
	// it as far as keyOpen looks, in one read: one byte more on each side
	// tells a reach that ran out from the log's own edge.
	from := max(0, start-keyReach-lineReach-1)
	buf, err := r.span(from, end+lineReach+1)
	if err != nil {
		return model.JobLog{}, err
	}
	// A log shorter than it was measured ends the window sooner.
	end = min(end, from+len(buf))
	start = min(start, end)
	ws, we := widen(buf, start-from, end-from)
	start, end = from+ws, from+we
	keyOpen, err := r.keyOpen(start)
	if err != nil {
		return model.JobLog{}, err
	}
	out.TotalBytes = r.size
	out.ByteOffset, out.ByteEnd = start, end
	if start > 0 {
		prev := max(0, start-limit)
		out.PrevByteOffset = &prev
	}
	if end < r.size {
		next := end
		out.NextByteOffset = &next
	}
	text := render.Log(buf[ws:we])
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

// failingStatuses are the job states whose log ends in the runner's
// failure line; failed_only searches no other.
var failingStatuses = []string{"failed", "canceled", "canceling"}

// logReader reads a job's log in ranges. It holds what it read as one
// stretch, so a walk back from the end reads each byte once.
type logReader struct {
	ctx    context.Context
	client *gapi.Client
	p      gapi.Project
	job    int64
	// size is the log's length in bytes.
	size int
	// have is the stretch read, starting at at.
	at   int
	have []byte
}

// measure learns the log's size. An archived log's size is its trace
// artifact's. Any other is read from its start, one ranged read's worth:
// a shorter answer is the whole log, and a longer log is measured by
// reading past its end (spike O).
func (r *logReader) measure(j gitlab.Job) error {
	for _, a := range j.Artifacts {
		if a.FileType == "trace" && a.Size > 0 {
			r.size = int(a.Size)
			return nil
		}
	}
	head, err := r.client.GetJobTrace(r.ctx, r.p, r.job, 0, gapi.MaxTraceRange)
	if err != nil {
		return err
	}
	r.at, r.have, r.size = 0, head, len(head)
	if len(head) < gapi.MaxTraceRange {
		return nil
	}
	return r.probe()
}

// probe measures a log at least one ranged read long by where a one-byte
// read comes back empty: it doubles, halves the gap until one ranged read
// covers it, and reads that last stretch, which ends the log exactly.
func (r *logReader) probe() error {
	lo, hi := gapi.MaxTraceRange, 2*gapi.MaxTraceRange
	for {
		b, err := r.client.GetJobTrace(r.ctx, r.p, r.job, hi, 1)
		if err != nil {
			return err
		}
		if len(b) == 0 {
			break
		}
		lo, hi = hi, hi*2
	}
	for hi-lo > gapi.MaxTraceRange {
		mid := lo + (hi-lo)/2
		b, err := r.client.GetJobTrace(r.ctx, r.p, r.job, mid, 1)
		if err != nil {
			return err
		}
		if len(b) > 0 {
			lo = mid
		} else {
			hi = mid
		}
	}
	// The log ends after lo and by hi; a log still being written may have
	// grown since, which reading to the end takes in.
	tail, err := r.client.GetJobTrace(r.ctx, r.p, r.job, lo, 0)
	if err != nil {
		return err
	}
	r.at, r.have, r.size = lo, tail, lo+len(tail)
	return nil
}

// span returns the log's bytes from a to b, b clamped to the log's end,
// reading what it does not hold. A range that touches the stretch held
// extends it; any other replaces it.
func (r *logReader) span(a, b int) ([]byte, error) {
	b = min(b, r.size)
	a = max(0, min(a, b))
	held := r.at + len(r.have)
	if a >= r.at && b <= held {
		return r.have[a-r.at : b-r.at], nil
	}
	if a > held || b < r.at {
		r.at, r.have = a, nil
		held = a
	}
	if a < r.at {
		before, err := r.read(a, r.at)
		if err != nil {
			return nil, err
		}
		r.have = append(before, r.have...)
		r.at = a
	}
	if b > held {
		after, err := r.read(held, b)
		if err != nil {
			return nil, err
		}
		r.have = append(r.have, after...)
		if len(after) < b-held {
			// The log is shorter than it was measured.
			r.size = r.at + len(r.have)
		}
	}
	b = min(b, r.at+len(r.have))
	return r.have[a-r.at : b-r.at], nil
}

// read reads the log from a to b, a ranged read at a time.
func (r *logReader) read(a, b int) ([]byte, error) {
	var out []byte
	for a < b {
		n := min(b-a, gapi.MaxTraceRange)
		chunk, err := r.client.GetJobTrace(r.ctx, r.p, r.job, a, n)
		if err != nil {
			return nil, err
		}
		out = append(out, chunk...)
		if len(chunk) < n {
			break
		}
		a += n
	}
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

// failureReads bounds the walk back for the failure line: the runner
// writes it last, after at most its own cleanup, so 2 MB from the end
// holds it. Once found, one more read looks for the section it is in.
const failureReads = 4

// failed is where a failed job's log failed.
type failed struct {
	// found is a failure line found; end is where that line ends.
	found bool
	end   int
	// section is the section the job failed in, when inSection. Its
	// Start is -1 when it began before the stretch read.
	section   render.LogSection
	inSection bool
}

// failure finds the runner's "ERROR: Job failed" line and the section it
// failed in, reading back from the end a ranged read at a time: of the
// job's own sections that start before the line, the one that ended
// last, or one still open. A section a script opens inside step_script
// ends before step_script does, so the script's own step is the one
// named. A section that began before the stretch read is known by its
// end marker; one begun before it and never ended is looked for further
// back, one read further, only when no other is found. search is false
// for a job whose state has no failure line.
func (r *logReader) failure(search bool) (failed, error) {
	if !search {
		return failed{}, nil
	}
	var f failed
	// Reads past the one that found the line, looking for its section.
	after := 0
	for from, reads := r.size, 0; from > 0 && !f.inSection; reads++ {
		if (!f.found && reads == failureReads) || (f.found && after == 1) {
			break
		}
		if f.found {
			after++
		}
		from = max(0, from-gapi.MaxTraceRange)
		b, err := r.span(from, r.size)
		if err != nil {
			return failed{}, err
		}
		lineStart, textEnd, ok := failureAt(b)
		// A line that starts at the stretch's edge may start before it.
		if !ok || (lineStart == 0 && from > 0) {
			continue
		}
		f = failed{found: true, end: len(b)}
		if i := bytes.IndexByte(b[textEnd:], '\n'); i >= 0 {
			f.end = textEnd + i + 1
		}
		for _, s := range render.LogSections(b) {
			if s.Start >= lineStart || slices.Contains(runnerAfterFailure, s.Name) {
				continue
			}
			if !f.inSection || endsLater(s, f.section) {
				f.section, f.inSection = s, true
			}
		}
		f.end += from
		if f.section.Start >= 0 {
			f.section.Start += from
		}
	}
	return f, nil
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

// keyHeader opens a private key block for the masking to match.
const keyHeader = "-----BEGIN PRIVATE KEY-----"

// keyReach is how far before a window keyOpen looks for a key block
// still open: far past any real key, however CI prefixed its lines, in
// one ranged read.
const keyReach = 256 << 10

// lastKeyBegin finds where the last private key block in b starts, or
// -1, searching back by the literal rather than scanning all of b. The
// header counts wherever it sits in its line, so an indented or prefixed
// key is found.
func lastKeyBegin(b []byte) int {
	for end := len(b); ; {
		i := bytes.LastIndex(b[:end], []byte("-----BEGIN "))
		if i < 0 || pemBegin.Match(b[i:]) {
			return i
		}
		end = i
	}
}

// keyOpen reports whether start is inside a private key block opened
// before it and not yet closed, which the caller masks whole rather than
// shows from its middle. It looks keyReach back (§18 row 76).
func (r *logReader) keyOpen(start int) (bool, error) {
	b, err := r.span(max(0, start-keyReach), start)
	if err != nil {
		return false, err
	}
	last := lastKeyBegin(b)
	return last >= 0 && !bytes.Contains(b[last:], []byte("-----END ")), nil
}

// widen moves a window's edges out to line boundaries.
func widen(trace []byte, start, end int) (int, int) {
	if start > 0 {
		start = edgeBack(trace, start)
	}
	if end < len(trace) {
		end = edgeForward(trace, end)
	}
	return start, end
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
	// Content is configuration to lint instead of the committed one.
	Content string
}

// LintCI checks a project's CI configuration at a ref (§7.6). The merged
// configuration is shown under the file budget.
func (s *Service) LintCI(ctx context.Context, q LintQuery) (model.Lint, error) {
	p, ref, err := s.project(ctx, q.Project)
	if err != nil {
		return model.Lint{}, err
	}
	lq := gapi.LintQuery{Ref: q.Ref, Simulate: q.Simulate, IncludeJobs: q.IncludeJobs}
	var l *gitlab.Lint
	if q.Content == "" {
		l, err = s.client.LintCI(ctx, p, lq)
	} else {
		l, err = s.lintContent(ctx, p, q.Content, lq)
	}
	if err != nil {
		return model.Lint{}, err
	}
	out := model.Lint{Project: ref, Ref: q.Ref, Simulate: q.Simulate, Supplied: q.Content != "", Valid: l.Valid, UntrustedErrors: lines(l.Errors),
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

// lintContent lints configuration the caller supplied. GitLab takes it
// only by POST, which a read_api token cannot send (§2.6), so read-only
// mode refuses it before asking.
func (s *Service) lintContent(ctx context.Context, p gapi.Project, content string, q gapi.LintQuery) (*gitlab.Lint, error) {
	if s.cfg.ReadOnly {
		return nil, gapi.Errf(gapi.ClassBlocked, "supplied content is linted by a POST, which read-only mode (%s) does not send; "+
			"without content, lint_ci checks the configuration committed at ref", config.EnvReadOnly)
	}
	if err := refuseIncludes(content); err != nil {
		return nil, err
	}
	return s.client.LintCIContent(ctx, p, content, q)
}

// refuseIncludes refuses supplied content with an include at any depth,
// a job's trigger:include among them. GitLab fetches a remote include
// from its own servers while linting, so a URL in supplied content is a
// channel out that no write control sees (§4.7). The content is parsed
// rather than searched, so an escaped, quoted or anchored key is found;
// content that does not parse is refused too, since GitLab's parser may
// read what this one cannot.
func refuseIncludes(content string) error {
	refusal := func(why string) error {
		return gapi.Errf(gapi.ClassBlocked, "%s; nothing was sent. GitLab fetches what an include names while linting, so supplied "+
			"content may not have one. Commit the configuration to a branch and lint it there with ref", why)
	}
	dec := yaml.NewDecoder(strings.NewReader(content))
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return refusal("the content could not be read as YAML, so an include in it could not be ruled out")
		}
		if hasKey(&doc, "include") {
			return refusal("the content has an include")
		}
	}
}

// hasKey reports whether a mapping anywhere under n has the key.
func hasKey(n *yaml.Node, key string) bool {
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				return true
			}
		}
	}
	for _, c := range n.Content {
		if hasKey(c, key) {
			return true
		}
	}
	return n.Alias != nil && hasKey(n.Alias, key)
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
