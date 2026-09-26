package tools

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/mmedum/gitlab-mcp/internal/gapi/gitlabtest"
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
	_, green := h.ok("get_pipeline", map[string]any{"project": gitlabtest.ProjectAlpha, "pipeline_id": 60001})
	if len(get(green, "failed_jobs").([]any)) != 0 {
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
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodGet, Path: fmt.Sprintf("/projects/%d/jobs/%d/trace", alphaID, gitlabtest.JobPassed),
		Status: http.StatusOK, Body: log, Header: http.Header{"Content-Type": {"text/plain"}}})
	_, out := h.ok("get_job_log", map[string]any{"project": alphaID, "job_id": gitlabtest.JobPassed, "byte_limit": 1000})
	shown := get(out, "untrusted_log").(string)
	if strings.Contains(shown, "MIIE") || !strings.HasPrefix(shown, "[MASKED private-key]") || get(out, "secrets_masked") != float64(1) {
		t.Errorf("shown:\n%s", shown)
	}
	if n := get(out, "byte_end").(float64) - get(out, "byte_offset").(float64); n > 1000+4096 {
		t.Errorf("the window grew to %v bytes", n)
	}
}
