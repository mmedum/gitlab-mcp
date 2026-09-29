package render

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mmedum/gitlab-mcp/v2/internal/model"
)

// The readable half of the CI reads.

// Pipelines renders list_pipelines.
func Pipelines(l model.Pipelines, _ Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Pipelines of %s\n", projectLine(l.Project))
	b.WriteString(listingLine("pipelines", l.Listing))
	for _, p := range l.Pipelines {
		fmt.Fprintf(&b, "\n- pipeline %d (#%d): %s on %s at %s, from %s, updated %s", p.ID, p.IID, Ident(p.Status), Ident(p.Ref),
			Ident(shortSHA(p.SHA)), Ident(p.Source), when(p.UpdatedAt))
	}
	return b.String()
}

// jobLine is one job on one line.
func jobLine(j model.JobRow) string {
	s := fmt.Sprintf("job %d %s (stage %s): %s", j.ID, Ident(j.Name), Ident(j.Stage), Ident(j.Status))
	if j.FailureReason != "" {
		s += ", " + Ident(j.FailureReason)
	}
	if j.AllowFailure {
		s += ", allowed to fail"
	}
	if j.Duration != nil {
		s += fmt.Sprintf(", ran %.0fs", *j.Duration)
	}
	if j.FinishedAt != nil {
		s += ", finished " + when(*j.FinishedAt)
	}
	return s
}

// Pipeline renders get_pipeline.
func Pipeline(p model.PipelineDetail, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Pipeline %d (#%d) in %s\n", p.ID, p.IID, projectLine(p.Project))
	status := Ident(p.Status)
	if p.DetailedStatus != "" && p.DetailedStatus != p.Status {
		status += " (" + Ident(p.DetailedStatus) + ")"
	}
	kind := "branch"
	if p.Tag {
		kind = "tag"
	}
	fmt.Fprintf(&b, "%s; %s on %s %s at %s, from %s", Ident(p.WebURL), status, kind, Ident(p.Ref), Ident(p.SHA), Ident(p.Source))
	if p.User != nil {
		fmt.Fprintf(&b, ", started by @%s", Ident(p.User.Username))
	}
	b.WriteString(".\n")
	fmt.Fprintf(&b, "Created %s; started %s; finished %s", when(p.CreatedAt), whenPtr(p.StartedAt), whenPtr(p.FinishedAt))
	if p.Duration != nil {
		fmt.Fprintf(&b, "; ran %ds", *p.Duration)
	}
	if p.QueuedDuration != nil {
		fmt.Fprintf(&b, "; queued %ds", *p.QueuedDuration)
	}
	b.WriteString(".")
	if p.UntrustedYAMLErrors != "" {
		b.WriteString("\n" + bd.Notice())
		fmt.Fprintf(&b, "\nConfiguration errors: %s", bd.Inline(p.UntrustedYAMLErrors))
	}
	switch {
	case len(p.FailedJobs) == 0 && len(p.FailedTriggerJobs) == 0:
		b.WriteString("\nNo job failed.")
	case len(p.FailedJobs) > 0:
		fmt.Fprintf(&b, "\nFailed jobs (%d):", len(p.FailedJobs))
		for _, j := range p.FailedJobs {
			b.WriteString("\n- " + jobLine(j))
		}
		b.WriteString("\nget_job_log with failed_only reads where a job failed.")
	}
	if len(p.FailedTriggerJobs) > 0 {
		fmt.Fprintf(&b, "\nFailed trigger jobs (%d), each failed by the pipeline it started:", len(p.FailedTriggerJobs))
		for _, t := range p.FailedTriggerJobs {
			b.WriteString("\n- " + jobLine(t.JobRow))
			if d := t.Downstream; d != nil {
				fmt.Fprintf(&b, "; started pipeline %d in project id %d, %s", d.ID, d.ProjectID, Ident(d.Status))
			} else {
				b.WriteString("; started no pipeline")
			}
		}
		b.WriteString("\nget_pipeline with that project and pipeline reads the downstream pipeline.")
	}
	switch {
	case !p.FailedTriggerJobsComplete && len(p.FailedTriggerJobs) > 0:
		b.WriteString("\nMore trigger jobs failed than were read.")
	case !p.FailedTriggerJobsComplete:
		b.WriteString("\nThe trigger jobs could not be read, so a downstream pipeline that failed this one is not named.")
	}
	if !p.FailedJobsComplete {
		b.WriteString("\nMore jobs failed than were read; list_jobs with scope failed lists them all.")
	}
	return b.String()
}

// Jobs renders list_jobs.
func Jobs(l model.Jobs, _ Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Jobs of pipeline %d in %s\n", l.PipelineID, projectLine(l.Project))
	b.WriteString(listingLine("jobs", l.Listing))
	for _, j := range l.Jobs {
		b.WriteString("\n- " + jobLine(j))
	}
	return b.String()
}

// JobLog renders get_job_log.
func JobLog(l model.JobLog, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Log of %s in %s\n", jobLine(l.Job), projectLine(l.Project))
	switch {
	case l.TotalBytes == 0:
		b.WriteString("The log is empty.")
		return b.String()
	case l.Section != "":
		fmt.Fprintf(&b, "The job failed in section %s; the window ends at the failure.\n", Ident(l.Section))
	case l.FailureNotFound:
		b.WriteString("The log has no failure line; the tail is shown.\n")
	}
	fmt.Fprintf(&b, "Bytes %d to %d of %d shown.", l.ByteOffset, l.ByteEnd, l.TotalBytes)
	if l.PrevByteOffset != nil {
		fmt.Fprintf(&b, " Earlier: byte_offset=%d.", *l.PrevByteOffset)
	}
	if l.NextByteOffset != nil {
		fmt.Fprintf(&b, " Later: byte_offset=%d.", *l.NextByteOffset)
	}
	b.WriteString(" Colors and section markers are removed; § marks where a section starts.")
	if l.SecretsMasked > 0 {
		fmt.Fprintf(&b, " %d secret shapes were replaced with [MASKED kind].", l.SecretsMasked)
	}
	if l.HiddenRemoved > 0 {
		fmt.Fprintf(&b, " %d hidden characters were made visible.", l.HiddenRemoved)
	}
	b.WriteString("\n" + bd.Notice() + "\n")
	o := Origin{Kind: "job_log", Project: l.Project.Path, Item: "job " + strconv.FormatInt(l.Job.ID, 10)}
	b.WriteString(bd.Block(o, l.UntrustedLog))
	return b.String()
}

// testCounts is one line of counts.
func testCounts(c model.TestCounts) string {
	return fmt.Sprintf("%d cases in %.2fs: %d passed, %d failed, %d errored, %d skipped", c.Total, c.Seconds, c.Success,
		c.Failed, c.Error, c.Skipped)
}

// TestReport renders get_test_report.
func TestReport(r model.TestReport, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Test report of pipeline %d in %s\n", r.PipelineID, projectLine(r.Project))
	if r.Partial {
		fmt.Fprintf(&b, "The pipeline is %s, so the report may be partial: jobs still to run add to it.\n", Ident(r.PipelineStatus))
	}
	if r.SuitesTotal == 0 {
		b.WriteString("The pipeline has no test report: no job uploaded a JUnit report (artifacts:reports:junit) that you may read.")
		return b.String()
	}
	if r.FromSummary {
		b.WriteString("The full report is larger than this server reads. These are GitLab's stored counts, written as each job " +
			"finishes, which can lag behind; no case is listed. get_job_log with failed_only reads a failed job's output.\n")
	}
	b.WriteString("Total: " + testCounts(r.Total) + ".")
	quoted := len(r.Failures) > 0
	for _, s := range r.Suites {
		quoted = quoted || s.UntrustedSuiteError != ""
	}
	if quoted {
		b.WriteString("\n" + bd.Notice())
	}
	fmt.Fprintf(&b, "\nSuites (%d of %d), those with failures first:", len(r.Suites), r.SuitesTotal)
	for _, s := range r.Suites {
		b.WriteString("\n- " + Ident(s.Name) + ": ")
		if s.UntrustedSuiteError != "" {
			b.WriteString("GitLab could not read its report: " + bd.Inline(s.UntrustedSuiteError))
			continue
		}
		b.WriteString(testCounts(s.TestCounts))
	}
	if r.FromSummary || r.FailuresTotal == 0 {
		return b.String()
	}
	b.WriteString("\n" + failuresLine(r))
	for i, f := range r.Failures {
		fmt.Fprintf(&b, "\n%d. %s in suite %s: %s", r.Offset+i+1, Ident(f.Status), Ident(f.Suite), bd.Inline(f.UntrustedName))
		if f.UntrustedClassname != "" {
			b.WriteString(", class " + bd.Inline(f.UntrustedClassname))
		}
		if f.UntrustedFile != "" {
			b.WriteString(", file " + bd.Inline(f.UntrustedFile))
		}
		fmt.Fprintf(&b, ", %.2fs", f.Seconds)
		if f.UntrustedOutput == "" {
			b.WriteString("; no output.")
			continue
		}
		o := Origin{Kind: "test_output", Project: r.Project.Path, Item: "pipeline " + strconv.FormatInt(r.PipelineID, 10)}
		b.WriteString("\n" + bd.Block(o, f.UntrustedOutput))
		if f.OutputCut {
			fmt.Fprintf(&b, "\nOutput cut at %d of %d characters.", TestOutputBudget, f.OutputChars)
		}
	}
	return b.String()
}

// failuresLine says which failed and errored cases are shown.
func failuresLine(r model.TestReport) string {
	s := fmt.Sprintf("Failed and errored cases: %d to %d of %d shown (budget %d characters)", r.Offset+1,
		r.Offset+len(r.Failures), r.FailuresTotal, r.BudgetChars)
	if len(r.Failures) == 0 {
		s = fmt.Sprintf("Failed and errored cases: none shown from offset %d of %d", r.Offset, r.FailuresTotal)
	}
	if r.NextOffset != nil {
		s += fmt.Sprintf("; continue with offset=%d", *r.NextOffset)
	}
	s += "."
	if r.SecretsMasked > 0 {
		s += fmt.Sprintf(" %d secret shapes were replaced with [MASKED kind].", r.SecretsMasked)
	}
	if r.HiddenRemoved > 0 {
		s += fmt.Sprintf(" %d hidden characters were removed or made visible.", r.HiddenRemoved)
	}
	return s
}

// Lint renders lint_ci.
func Lint(l model.Lint, bd Boundary) string {
	var b strings.Builder
	what := "simulated a pipeline for"
	if !l.Simulate {
		what = "checked"
	}
	fmt.Fprintf(&b, "CI lint %s the configuration at %s in %s\n", what, orDefault(l.Ref), projectLine(l.Project))
	if l.Valid {
		b.WriteString("Valid.")
	} else {
		b.WriteString("Not valid.")
	}
	quoted := len(l.UntrustedErrors)+len(l.UntrustedWarnings) > 0 || l.UntrustedMergedYAML != ""
	if quoted {
		b.WriteString("\n" + bd.Notice())
	}
	for _, e := range l.UntrustedErrors {
		fmt.Fprintf(&b, "\nError: %s", bd.Inline(e))
	}
	for _, w := range l.UntrustedWarnings {
		fmt.Fprintf(&b, "\nWarning: %s", bd.Inline(w))
	}
	if len(l.Jobs) > 0 {
		fmt.Fprintf(&b, "\nJobs (%d):", len(l.Jobs))
		for _, j := range l.Jobs {
			fmt.Fprintf(&b, "\n- %s (stage %s, when %s", Ident(j.Name), Ident(j.Stage), orNone(Ident(j.When)))
			if j.AllowFailure {
				b.WriteString(", allowed to fail")
			}
			b.WriteString(")")
		}
	}
	if l.UntrustedMergedYAML != "" {
		b.WriteString("\nThe configuration with every include expanded:\n")
		o := Origin{Kind: "ci_config", Project: l.Project.Path, Item: l.Ref}
		b.WriteString(bd.Block(o, l.UntrustedMergedYAML) + "\n")
		b.WriteString(budgetLine("Merged configuration", l.MergedYAMLBudget))
	}
	return b.String()
}
