package tools

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/mmedum/gitlab-mcp/v2/internal/gapi"
	"github.com/mmedum/gitlab-mcp/v2/internal/gapi/gitlabtest"
)

// The CI reads against the in-memory instance: three merge request
// pipelines that passed and one failed pipeline on main with a job of
// each outcome (gitlabtest.fillCI).

func TestListPipelines(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, out := h.ok("list_pipelines", map[string]any{"project": gitlabtest.ProjectAlpha})
	if get(out, "listing", "total") != float64(4) || get(out, "pipelines", 0, "id") != float64(gitlabtest.PipelineFailed) {
		t.Fatalf("pipelines = %v", get(out, "pipelines"))
	}
	if !strings.Contains(text, fmt.Sprintf("pipeline %d (#4): failed on main", gitlabtest.PipelineFailed)) {
		t.Errorf("text:\n%s", text)
	}
	_, failed := h.ok("list_pipelines", map[string]any{"project": gitlabtest.ProjectAlpha, "status": "failed", "source": "push",
		"ref": "main", "username": "alice"})
	if get(failed, "listing", "total") != float64(1) {
		t.Errorf("failed on main = %v", get(failed, "pipelines"))
	}
	_, asc := h.ok("list_pipelines", map[string]any{"project": gitlabtest.ProjectAlpha, "sort": "asc", "order_by": "id"})
	if get(asc, "pipelines", 0, "id") != float64(60001) {
		t.Errorf("ascending = %v", get(asc, "pipelines", 0))
	}
	h.fails("list_pipelines", map[string]any{"project": gitlabtest.ProjectAlpha, "status": "broken"}, "invalid")
	h.fails("list_pipelines", map[string]any{"project": gitlabtest.ProjectAlpha, "updated_after": "yesterday"}, "invalid")
}

func TestGetPipeline(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, out := h.ok("get_pipeline", map[string]any{"project": gitlabtest.ProjectAlpha, "pipeline_id": gitlabtest.PipelineFailed})
	if get(out, "status") != "failed" || get(out, "user", "username") != "alice" || get(out, "duration_seconds") != float64(480) {
		t.Errorf("pipeline = %v", out)
	}
	jobs := get(out, "failed_jobs").([]any)
	if len(jobs) != 2 || get(out, "failed_jobs_complete") != true {
		t.Fatalf("failed jobs = %v", jobs)
	}
	for _, want := range []string{"job 70002 unit tests (stage test): failed, script_failure", "job 70003 lint (stage test): failed, " +
		"script_failure, allowed to fail", "get_job_log with failed_only"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	// The trigger job the job listing leaves out, and what it started.
	if get(out, "failed_trigger_jobs", 0, "id") != float64(gitlabtest.BridgeFailed) ||
		get(out, "failed_trigger_jobs", 0, "downstream_pipeline", "id") != float64(gitlabtest.DownstreamFailed) ||
		!strings.Contains(text, fmt.Sprintf("started pipeline %d in project id 2002, failed", gitlabtest.DownstreamFailed)) {
		t.Errorf("trigger jobs = %v\n%s", get(out, "failed_trigger_jobs"), text)
	}
	_, green := h.ok("get_pipeline", map[string]any{"project": gitlabtest.ProjectAlpha, "pipeline_id": 60001})
	if len(get(green, "failed_jobs").([]any)) != 0 || len(get(green, "failed_trigger_jobs").([]any)) != 0 {
		t.Errorf("a passing pipeline lists failed jobs: %v", get(green, "failed_jobs"))
	}
	h.fails("get_pipeline", map[string]any{"project": gitlabtest.ProjectAlpha, "pipeline_id": 1}, "not_found")
	h.fails("get_pipeline", map[string]any{"project": gitlabtest.ProjectAlpha, "pipeline_id": 0}, "invalid")
}

func TestListJobs(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	_, out := h.ok("list_jobs", map[string]any{"project": gitlabtest.ProjectAlpha, "pipeline_id": gitlabtest.PipelineFailed,
		"include_retried": true})
	if get(out, "listing", "total") != float64(4) || get(out, "jobs", 3, "status") != "manual" {
		t.Errorf("jobs = %v", get(out, "jobs"))
	}
	_, failed := h.ok("list_jobs", map[string]any{"project": gitlabtest.ProjectAlpha, "pipeline_id": gitlabtest.PipelineFailed,
		"scope": "failed"})
	if get(failed, "listing", "total") != float64(2) {
		t.Errorf("failed jobs = %v", get(failed, "jobs"))
	}
}

func TestGetJobLogTail(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	args := map[string]any{"project": gitlabtest.ProjectAlpha, "job_id": gitlabtest.JobFailed}
	text, out := h.ok("get_job_log", args)
	total := int(get(out, "total_bytes").(float64))
	if total < 50000 || get(out, "byte_end") != float64(total) || get(out, "next_byte_offset") != nil {
		t.Fatalf("window %v..%v of %d, next %v", get(out, "byte_offset"), get(out, "byte_end"), total, get(out, "next_byte_offset"))
	}
	start := int(get(out, "byte_offset").(float64))
	if total-start < 40000 || total-start > 40000+4096 {
		t.Errorf("the tail is %d bytes, want about 40,000", total-start)
	}
	if prev := get(out, "prev_byte_offset"); prev != float64(max(0, start-40000)) {
		t.Errorf("prev_byte_offset = %v", prev)
	}
	log := get(out, "untrusted_log").(string)
	for _, bad := range []string{gitlabtest.FakeToken, "\x1b", "section_start", "section_end", "\r"} {
		if strings.Contains(log+text, bad) {
			t.Errorf("the log shows %q", bad)
		}
	}
	for _, want := range []string{"[MASKED gitlab-token]", "ERROR: Job failed: exit code 1", "§ section cleanup_file_variables: Cleaning up"} {
		if !strings.Contains(log, want) {
			t.Errorf("the log lacks %q", want)
		}
	}
	if get(out, "secrets_masked") != float64(1) || !strings.Contains(text, "1 secret shapes were replaced") {
		t.Errorf("secrets_masked = %v", get(out, "secrets_masked"))
	}
	// A window starts at a line, never inside one.
	if !strings.HasPrefix(log, "ok  \texample.test/pkg") {
		t.Errorf("the window starts mid-line: %q", log[:40])
	}
}

func TestGetJobLogFromOffset(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, out := h.ok("get_job_log", map[string]any{"project": gitlabtest.ProjectAlpha, "job_id": gitlabtest.JobFailed,
		"byte_offset": 0, "byte_limit": 600})
	log := get(out, "untrusted_log").(string)
	for _, want := range []string{"Running with gitlab-runner", "§ section prepare_executor: Preparing the \"docker\" executor",
		"Pulling image done", "§ section step_script: Executing"} {
		if !strings.Contains(log, want) {
			t.Errorf("the log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "Pulling image 10%") {
		t.Errorf("a line a carriage return overwrote is shown:\n%s", log)
	}
	next := get(out, "next_byte_offset")
	if get(out, "prev_byte_offset") != nil || next == nil || next != get(out, "byte_end") {
		t.Errorf("prev %v next %v end %v", get(out, "prev_byte_offset"), next, get(out, "byte_end"))
	}
	if !strings.Contains(text, fmt.Sprintf("Later: byte_offset=%v.", next)) {
		t.Errorf("text does not say how to read on:\n%s", text)
	}
	// The next window starts where this one ended.
	_, more := h.ok("get_job_log", map[string]any{"project": gitlabtest.ProjectAlpha, "job_id": gitlabtest.JobFailed,
		"byte_offset": next, "byte_limit": 600})
	if get(more, "byte_offset") != next {
		t.Errorf("continued at %v, want %v", get(more, "byte_offset"), next)
	}
}

func TestGetJobLogFailedOnly(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, out := h.ok("get_job_log", map[string]any{"project": gitlabtest.ProjectAlpha, "job_id": gitlabtest.JobAllowed,
		"failed_only": true})
	log := get(out, "untrusted_log").(string)
	if get(out, "section") != "step_script" || !strings.HasPrefix(log, "§ section step_script:") ||
		!strings.HasSuffix(strings.TrimSpace(log), "ERROR: Job failed: exit code 1") {
		t.Errorf("section %v, log:\n%s", get(out, "section"), log)
	}
	if !strings.Contains(text, "The job failed in section step_script") {
		t.Errorf("text:\n%s", text)
	}
	// JobAllowed's log is timestamped, as gitlab.com's runners write it:
	// none of the stamps shows.
	if strings.Contains(log, "Z 01O") || strings.Contains(log, "Z 00O") || strings.Contains(log, "\n\n§") {
		t.Errorf("timestamps shown:\n%s", log)
	}
	// A long failing section is shown from its tail to the failure.
	_, long := h.ok("get_job_log", map[string]any{"project": gitlabtest.ProjectAlpha, "job_id": gitlabtest.JobFailed,
		"failed_only": true, "byte_limit": 2000})
	log = get(long, "untrusted_log").(string)
	if !strings.Contains(log, "--- FAIL: TestLogin") || strings.Contains(log, "§ section step_script") ||
		get(long, "prev_byte_offset") == nil {
		t.Errorf("long section:\n%s", log)
	}
	// A job that passed has no failure to find: the tail is shown.
	_, passed := h.ok("get_job_log", map[string]any{"project": gitlabtest.ProjectAlpha, "job_id": gitlabtest.JobPassed,
		"failed_only": true})
	if get(passed, "section") != "" || get(passed, "failure_not_found") != true ||
		!strings.Contains(get(passed, "untrusted_log").(string), "Job succeeded") {
		t.Errorf("passed job: %v", passed)
	}
}

func TestGetJobLogRefusals(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	base := map[string]any{"project": gitlabtest.ProjectAlpha, "job_id": gitlabtest.JobFailed}
	with := func(k string, v any) map[string]any {
		m := map[string]any{k: v}
		for key, val := range base {
			m[key] = val
		}
		return m
	}
	h.fails("get_job_log", with("byte_offset", 10_000_000), "invalid")
	h.fails("get_job_log", with("byte_limit", 100_001), "invalid")
	both := with("byte_offset", 0)
	both["failed_only"] = true
	h.fails("get_job_log", both, "invalid")
	h.fails("get_job_log", map[string]any{"project": gitlabtest.ProjectAlpha, "job_id": 1}, "not_found")
	// A manual job that never ran has an empty log.
	text, empty := h.ok("get_job_log", map[string]any{"project": gitlabtest.ProjectAlpha, "job_id": gitlabtest.JobManual})
	if get(empty, "total_bytes") != float64(0) || !strings.Contains(text, "The log is empty.") {
		t.Errorf("manual job: %v\n%s", empty, text)
	}
}

// PipelineFailed's report: build passes, unit tests fails 24 cases and
// errors one, and lint's report did not parse (gitlabtest.fillTestReports).
func TestGetTestReport(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	args := map[string]any{"project": gitlabtest.ProjectAlpha, "pipeline_id": gitlabtest.PipelineFailed}
	text, out := h.ok("get_test_report", args)
	for path, want := range map[string]any{"total.total": float64(30), "total.success": float64(4),
		"total.failed": float64(gitlabtest.ReportFailures), "total.error": float64(1), "total.skipped": float64(1),
		"failures_total": float64(gitlabtest.ReportFailures + 1), "suites_total": float64(3), "partial": false,
		"from_summary": false, "pipeline_status": "failed", "offset": float64(0)} {
		keys := strings.Split(path, ".")
		args := make([]any, len(keys))
		for i, k := range keys {
			args[i] = k
		}
		if got := get(out, args...); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	// Failing suites first, the one GitLab could not read among them.
	if get(out, "suites", 0, "name") != "unit tests" || get(out, "suites", 1, "untrusted_suite_error") != gitlabtest.ReportSuiteError ||
		get(out, "suites", 2, "name") != "build" || get(out, "suites", 2, "success") != float64(2) {
		t.Errorf("suites = %v", get(out, "suites"))
	}
	// The error first, then the failures slowest first; a stack trace is
	// kept, and JUnit's system_output is the output.
	if get(out, "failures", 0, "status") != "error" || !strings.Contains(get(out, "failures", 0, "untrusted_output").(string),
		"Stack trace:\nsetup_test.go:9") || get(out, "failures", 1, "untrusted_name") != "TestLogin" ||
		get(out, "failures", 1, "untrusted_file") != "login/login_test.go" || get(out, "failures", 1, "execution_seconds") != 9.5 {
		t.Errorf("failures = %v", get(out, "failures"))
	}
	// Token shapes are masked, and the output is inside the boundary.
	if strings.Contains(text, gitlabtest.FakeToken) || !strings.Contains(text, "refused token [MASKED") ||
		get(out, "secrets_masked") != float64(1) {
		t.Errorf("the token was not masked:\n%s", text)
	}
	if !strings.Contains(text, "kind=test_output") || !strings.Contains(text, "was written by GitLab users") ||
		strings.Contains(text, "<<<END 0000000000000000>>>") || get(out, "hidden_chars_removed") != float64(1) {
		t.Errorf("the output is not bounded:\n%s", text)
	}
	if !strings.Contains(text, "GitLab could not read its report: <<<") {
		t.Errorf("the suite error is not bounded:\n%s", text)
	}

	// The budget stops the first result, and offset continues it to the
	// end.
	shown := len(get(out, "failures").([]any))
	next, ok := get(out, "next_offset").(float64)
	if !ok || int(next) != shown || shown >= gitlabtest.ReportFailures {
		t.Fatalf("next_offset = %v after %d cases", get(out, "next_offset"), shown)
	}
	if !strings.Contains(text, fmt.Sprintf("continue with offset=%d", shown)) {
		t.Errorf("text does not say how to continue:\n%s", text)
	}
	args["offset"] = next
	text, rest := h.ok("get_test_report", args)
	if get(rest, "next_offset") != nil || shown+len(get(rest, "failures").([]any)) != gitlabtest.ReportFailures+1 ||
		get(rest, "failures", 0, "untrusted_name") == get(out, "failures", 0, "untrusted_name") {
		t.Errorf("rest = %v", rest)
	}
	if !strings.Contains(text, fmt.Sprintf("%d. failed in suite unit tests", shown+1)) {
		t.Errorf("continued text:\n%s", text)
	}
	args["offset"] = gitlabtest.ReportFailures + 2
	h.fails("get_test_report", args, "invalid")
	args["offset"] = -1
	h.fails("get_test_report", args, "invalid")
	h.fails("get_test_report", map[string]any{"project": gitlabtest.ProjectAlpha, "pipeline_id": 1}, "not_found")
}

func TestGetTestReportRunningAndEmpty(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, empty := h.ok("get_test_report", map[string]any{"project": gitlabtest.ProjectAlpha, "pipeline_id": 60001})
	if get(empty, "suites_total") != float64(0) || len(get(empty, "failures").([]any)) != 0 ||
		!strings.Contains(text, "The pipeline has no test report") || strings.Contains(text, "partial") {
		t.Errorf("empty report = %v\n%s", empty, text)
	}
	h.gl.SetPipelineStatus(gitlabtest.ProjectAlpha, gitlabtest.PipelineFailed, "running")
	text, running := h.ok("get_test_report", map[string]any{"project": gitlabtest.ProjectAlpha, "pipeline_id": gitlabtest.PipelineFailed})
	if get(running, "partial") != true || !strings.Contains(text, "The pipeline is running, so the report may be partial") {
		t.Errorf("running = %v\n%s", get(running, "partial"), text)
	}
}

// A report larger than the client reads falls back to GitLab's stored
// summary: counts, no cases.
func TestGetTestReportTooLarge(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodGet, Path: fmt.Sprintf("/projects/%d/pipelines/%d/test_report", alphaID,
		gitlabtest.PipelineFailed), Status: http.StatusOK, Body: `{"test_suites":["` + strings.Repeat("a", gapi.MaxResponseBytes) + `"]}`})
	text, out := h.ok("get_test_report", map[string]any{"project": alphaID, "pipeline_id": gitlabtest.PipelineFailed})
	if get(out, "from_summary") != true || get(out, "total", "failed") != float64(gitlabtest.ReportFailures) ||
		get(out, "failures_total") != float64(gitlabtest.ReportFailures+1) || len(get(out, "failures").([]any)) != 0 ||
		get(out, "suites_total") != float64(3) || get(out, "suites", 0, "total") != float64(28) {
		t.Errorf("summary = %v", out)
	}
	if !strings.Contains(text, "larger than this server reads") || strings.Contains(text, "Failed and errored cases") {
		t.Errorf("text:\n%s", text)
	}
}

func TestLintCI(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	text, out := h.ok("lint_ci", map[string]any{"project": gitlabtest.ProjectAlpha, "include_jobs": true, "simulate": true})
	if get(out, "valid") != true || len(get(out, "jobs").([]any)) != 4 || get(out, "simulated") != true ||
		len(get(out, "untrusted_warnings").([]any)) != 1 {
		t.Errorf("lint = %v", out)
	}
	if !strings.Contains(text, "- deploy (stage deploy, when manual)") || !strings.Contains(text, "unit tests:\n  stage: test") {
		t.Errorf("text:\n%s", text)
	}
	text, bad := h.ok("lint_ci", map[string]any{"project": gitlabtest.ProjectAlpha, "ref": "release/1.0"})
	if get(bad, "valid") != false || !strings.Contains(text, "Error: <<<") || !strings.Contains(text, "jobs:build:script config") {
		t.Errorf("invalid lint = %v\n%s", bad, text)
	}
	_, cut := h.ok("lint_ci", map[string]any{"project": gitlabtest.ProjectAlpha, "offset": 20})
	if get(cut, "merged_yaml_budget", "offset") != float64(20) {
		t.Errorf("offset = %v", get(cut, "merged_yaml_budget"))
	}
	h.fails("lint_ci", map[string]any{"project": gitlabtest.ProjectAlpha, "offset": 100_000}, "invalid")
}

// A key block that opened long before the tail and never closed is
// masked in the tail, and the window keeps its size.
func TestGetJobLogMasksAKeyOpenedEarlier(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	log := "start\n-----BEGIN " + "RSA PRIVATE KEY-----\n" + strings.Repeat("MIIEowIBAAKCAQEA0000000000\n", 5000) + "tail line\n"
	h.gl.SetJobLog(gitlabtest.ProjectAlpha, gitlabtest.JobPassed, "success", log, true)
	_, out := h.ok("get_job_log", map[string]any{"project": alphaID, "job_id": gitlabtest.JobPassed, "byte_limit": 1000})
	shown := get(out, "untrusted_log").(string)
	if strings.Contains(shown, "MIIE") || !strings.HasPrefix(shown, "[MASKED private-key]") || get(out, "secrets_masked") != float64(1) {
		t.Errorf("shown:\n%s", shown)
	}
	if n := get(out, "byte_end").(float64) - get(out, "byte_offset").(float64); n > 1000+4096 {
		t.Errorf("the window grew to %v bytes", n)
	}
}

// traceCalls lists the job log requests since the last reset, as
// method and query.
func traceCalls(h *harness, job int64) []string {
	var out []string
	suffix := fmt.Sprintf("/jobs/%d/trace", job)
	for _, r := range h.gl.Requests() {
		if strings.HasSuffix(r.EscapedPath, suffix) {
			out = append(out, r.Method+" "+r.RawQuery)
		}
	}
	return out
}

func TestGetJobLogReadsOnlyTheWindow(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	// Past the 32 MiB a whole read could take: the old limit.
	line := "ok  \texample.test/pkg\t0.01s\n"
	big := strings.Repeat(line, (33<<20)/len(line)) + "the last line\n"
	for _, c := range []struct {
		name, status string
		archived     bool
		// first is the request that learns the size.
		first string
	}{
		{"an archived log, sized by its artifact", "success", true, ""},
		{"a finished log not archived yet, sized by reading past its end", "success", false, "GET byte_limit=512000"},
		{"a running log, sized by reading past its end", "running", false, "GET byte_limit=512000"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h.gl.SetJobLog(gitlabtest.ProjectAlpha, gitlabtest.JobPassed, c.status, big, c.archived)
			h.gl.ResetRequests()
			_, out := h.ok("get_job_log", map[string]any{"project": alphaID, "job_id": gitlabtest.JobPassed})
			if get(out, "total_bytes") != float64(len(big)) || get(out, "byte_end") != float64(len(big)) ||
				!strings.HasSuffix(get(out, "untrusted_log").(string), "the last line\n") {
				t.Fatalf("total %v, end %v", get(out, "total_bytes"), get(out, "byte_end"))
			}
			calls := traceCalls(h, gitlabtest.JobPassed)
			if c.first != "" && (len(calls) == 0 || calls[0] != c.first) {
				t.Errorf("sized by %v, want %q first", calls, c.first)
			}
			if len(calls) > 20 {
				t.Errorf("%d requests: %v", len(calls), calls)
			}
			for _, call := range calls {
				if strings.HasPrefix(call, "GET") && !strings.Contains(call, "byte_offset=") && !strings.Contains(call, "byte_limit=") {
					t.Errorf("the whole log was read: %q", call)
				}
			}
			// From an offset in the middle, one ranged read.
			h.gl.ResetRequests()
			mid := len(big) / 2
			_, out = h.ok("get_job_log", map[string]any{"project": alphaID, "job_id": gitlabtest.JobPassed, "byte_offset": mid})
			if s := int(get(out, "byte_offset").(float64)); s > mid || mid-s > len(line) {
				t.Errorf("the window starts at %d for an offset of %d", s, mid)
			}
			if !c.archived {
				return
			}
			if calls := traceCalls(h, gitlabtest.JobPassed); len(calls) > 1 {
				t.Errorf("an offset read made %d requests: %v", len(calls), calls)
			}
		})
	}
}

func TestGetJobLogFailedOnlyInALongLog(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	sec := func(kind, name string) string { return "\x1b[0Ksection_" + kind + ":1:" + name + "\r\x1b[0K" }
	filler := strings.Repeat("ok  \texample.test/pkg\t0.01s\n", 100000)
	log := sec("start", "step_script") + filler + "--- FAIL: TestLogin\n" + sec("end", "step_script") + "\n" +
		sec("start", "cleanup_file_variables") + "tidy\n" + sec("end", "cleanup_file_variables") + "\n" +
		"\x1b[31;1mERROR: Job failed: exit code 1\n\x1b[0;m\n"
	h.gl.SetJobLog(gitlabtest.ProjectAlpha, gitlabtest.JobPassed, "failed", log, true)
	h.gl.ResetRequests()
	_, out := h.ok("get_job_log", map[string]any{"project": alphaID, "job_id": gitlabtest.JobPassed, "failed_only": true,
		"byte_limit": 2000})
	shown := get(out, "untrusted_log").(string)
	if get(out, "section") != "step_script" || !strings.Contains(shown, "--- FAIL: TestLogin") ||
		!strings.HasSuffix(strings.TrimSpace(shown), "ERROR: Job failed: exit code 1") {
		t.Errorf("section %v, shown:\n%s", get(out, "section"), shown)
	}
	if calls := traceCalls(h, gitlabtest.JobPassed); len(calls) > 2 {
		t.Errorf("%d requests: %v", len(calls), calls)
	}
}

// A log shorter than one ranged read, not archived yet, costs one read,
// as a whole read did.
func TestGetJobLogOfAShortLogIsOneRead(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.gl.ResetRequests()
	h.ok("get_job_log", map[string]any{"project": alphaID, "job_id": gitlabtest.JobFailed, "failed_only": true})
	if calls := traceCalls(h, gitlabtest.JobFailed); len(calls) != 1 {
		t.Errorf("%d reads: %v", len(calls), calls)
	}
}

// A key printed indented, as a YAML block, that opened before the window
// is masked whole too.
func TestGetJobLogMasksAnIndentedKeyOpenedEarlier(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	log := "start\ntls.key: |\n    -----BEGIN " + "RSA PRIVATE KEY-----\n" + strings.Repeat("    MIIEowIBAAKCAQEA0000000000\n", 5000) +
		"    -----END RSA PRIVATE KEY-----\ntail line\n"
	h.gl.SetJobLog(gitlabtest.ProjectAlpha, gitlabtest.JobPassed, "success", log, true)
	_, out := h.ok("get_job_log", map[string]any{"project": alphaID, "job_id": gitlabtest.JobPassed, "byte_limit": 1000})
	shown := get(out, "untrusted_log").(string)
	if strings.Contains(shown, "MIIE") || !strings.Contains(shown, "tail line") {
		t.Errorf("shown:\n%s", shown)
	}
}

// A section known only by its end marker starts before what was read; on
// a short log the window starts at 0 rather than before it.
func TestGetJobLogFailedOnlyWithAStrayEndMarker(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	log := "\x1b[0Ksection_end:1:stray\r\x1b[0K\nboom\n\x1b[31;1mERROR: Job failed: exit code 1\n\x1b[0;m\n"
	h.gl.SetJobLog(gitlabtest.ProjectAlpha, gitlabtest.JobPassed, "failed", log, true)
	_, out := h.ok("get_job_log", map[string]any{"project": alphaID, "job_id": gitlabtest.JobPassed, "failed_only": true})
	if get(out, "byte_offset") != float64(0) || !strings.Contains(get(out, "untrusted_log").(string), "boom") {
		t.Errorf("window %v: %v", get(out, "byte_offset"), get(out, "untrusted_log"))
	}
}

// Trigger jobs that cannot be read leave the pipeline readable, and say so.
func TestGetPipelineWhenTriggerJobsCannotBeRead(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.gl.Inject(gitlabtest.Fault{Method: "GET", Path: fmt.Sprintf("/projects/%d/pipelines/%d/trigger_jobs", alphaID, gitlabtest.PipelineFailed),
		Status: 403, Body: `{"message":"403 Forbidden"}`})
	text, out := h.ok("get_pipeline", map[string]any{"project": alphaID, "pipeline_id": gitlabtest.PipelineFailed})
	if len(get(out, "failed_jobs").([]any)) != 2 || get(out, "failed_trigger_jobs_complete") != false ||
		!strings.Contains(text, "The trigger jobs could not be read") {
		t.Errorf("out %v\n%s", out, text)
	}
}
