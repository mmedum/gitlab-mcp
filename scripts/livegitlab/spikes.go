//go:build live

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/v4/internal/app"
	"github.com/mmedum/gitlab-mcp/v4/internal/auth"
	"github.com/mmedum/gitlab-mcp/v4/internal/diffpos"
	"github.com/mmedum/gitlab-mcp/v4/internal/quickaction"
	netredact "github.com/mmedum/gitlab-mcp/v4/internal/redact"
	"github.com/mmedum/gitlab-mcp/v4/scripts/internal/redact"
)

// The spikes run against the scratch project only. They go around the
// client, because they ask what the client does not: headers it does not
// send and answers it does not keep. Only statuses, sizes and header names
// and values GitLab sets are printed; never a token, and never a body.

// raw is one request's answer as the spikes read it.
type raw struct {
	status int
	header http.Header
	body   []byte
}

// spikeClient sends requests to the scratch project's API paths with the
// profile's token, following no redirect.
type spikeClient struct {
	http  *http.Client
	root  *url.URL
	token string
}

func (c spikeClient) get(ctx context.Context, path string, query url.Values, header http.Header) (raw, error) {
	return c.send(ctx, http.MethodGet, path, query, header, nil)
}

// write sends a JSON body with POST or PUT.
func (c spikeClient) write(ctx context.Context, method, path string, body any) (raw, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return raw{}, err
	}
	return c.send(ctx, method, path, nil, http.Header{"Content-Type": {"application/json"}}, payload)
}

func (c spikeClient) send(ctx context.Context, method, path string, query url.Values, header http.Header, payload []byte) (raw, error) {
	// path may carry an escaped segment, a branch with a slash: it is
	// sent as written, and decoded once for Path.
	u := *c.root
	unescaped, err := url.PathUnescape(path)
	if err != nil {
		return raw{}, err
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + unescaped
	u.RawPath = strings.TrimSuffix(c.root.EscapedPath(), "/") + "/" + path
	u.RawQuery = query.Encode()
	var reqBody io.Reader
	if payload != nil {
		reqBody = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reqBody)
	if err != nil {
		return raw{}, err
	}
	for k, vs := range header {
		req.Header[k] = vs
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return raw{}, fmt.Errorf("%s: %s", path, netredact.NetError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	return raw{status: resp.StatusCode, header: resp.Header, body: body}, err
}

// spike is one question GitLab's documentation does not answer, asked
// around the server. Its name is what a comment cites as a live
// step, and what the transcript prints above its answer.
type spike struct {
	name string
	// alone is the -only-spike value that asks it by itself after
	// seeding, driving no tool; "" when it is never asked alone.
	alone string
	// everyRun is whether a full run asks it. One that is not is asked
	// only alone.
	everyRun bool
	// afterCI is whether it reads the default branch's finished pipeline,
	// which asking it alone then waits for.
	afterCI bool
	// ask is the spikeRun method that asks it.
	ask func(spikeRun, context.Context) error
}

// spikeRun is what a spike sees: the raw client, the application that
// signs in, the scratch project and the printer.
type spikeRun struct {
	c   spikeClient
	app *auth.Application
	s   scratch
	p   *redact.Printer
}

// spikes are asked in this order. A full run asks them once the default
// branch's pipeline has finished, before the plan.
var spikes = []spike{
	{name: "whether a conditional read spends the rate limit", everyRun: true, ask: spikeRun.askConditionalReads},
	{name: "what byte_offset and byte_limit cut from a job log", everyRun: true, ask: spikeRun.askJobLogWindows},
	{name: "where a job log's size shows without reading it", alone: "job-log-size", everyRun: true, afterCI: true,
		ask: spikeRun.askJobLogSize},
	{name: "whether diffpos positions land on GitLab's lines", everyRun: true, ask: spikeRun.askInlinePositions},
	{name: "whether a wildcard rule sets a branch's protected flag", everyRun: true, ask: spikeRun.askProtectedFlag},
	{name: "how soon a read finds what a create made", everyRun: true, ask: spikeRun.askSettleByReading},
	{name: "what starts GitLab's merge status check", alone: "merge-status", everyRun: true, ask: spikeRun.askMergeStatusCheck},
	{name: "how a comment delete holds If-Unmodified-Since", everyRun: true, ask: spikeRun.askConditionalDelete},
	{name: "where GitLab runs a quick action from an API body", alone: "quick-actions", ask: spikeRun.askQuickActions},
	{name: "which REST routes an mcp-scoped token reaches", alone: "mcp-scope", ask: spikeRun.askMCPScope},
}

// spikesFor is the spikes a run asks: those of every run when only is
// empty, else the one -only-spike names, or none for a value no spike
// takes.
func spikesFor(only string) []spike {
	var out []spike
	for _, sp := range spikes {
		if only == "" && sp.everyRun || only != "" && sp.alone == only {
			out = append(out, sp)
		}
	}
	return out
}

// aloneValues are the values -only-spike takes, for its help and its
// refusal.
func aloneValues() string {
	var out []string
	for _, sp := range spikes {
		if sp.alone != "" {
			out = append(out, sp.alone)
		}
	}
	return strings.Join(out, ", ")
}

// askSpikes asks each spike in turn, under a banner with its name. A
// spike that fails says so, and the next is asked.
func askSpikes(ctx context.Context, settings *app.Settings, s scratch, which []spike, p *redact.Printer) {
	tok, err := settings.Tokens().Token(ctx)
	if err != nil {
		p.Sayf("!! spikes skipped: no token: %v", err)
		return
	}
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if settings.HTTPClient != nil {
		copied := *settings.HTTPClient
		copied.CheckRedirect = hc.CheckRedirect
		hc = &copied
	}
	x := spikeRun{c: spikeClient{http: hc, root: settings.Instance.APIRoot(), token: tok}, app: settings.Application(), s: s, p: p}
	for _, sp := range which {
		p.Sayf("\n=== spike: %s ===", sp.name)
		if err := sp.ask(x, ctx); err != nil {
			p.Sayf("!! %s: %v", sp.name, err)
		}
	}
}

// askMCPScope asks what a token with only the mcp scope can do over REST:
// the source lets it call the routes tagged route_setting :mcp and nothing
// else, which leaves out every notes route. It signs in a second time asking
// for mcp alone, holds that token in memory only, sends it to tagged and
// untagged routes of the scratch project, and revokes it, which also asks
// whether revocation works for a public client.
func (x spikeRun) askMCPScope(ctx context.Context) error {
	x.p.Say("a browser tab asks to authorize the mcp scope alone; approve it")
	g, err := x.app.Login(ctx, []string{"mcp"}, auth.LoginOptions{})
	if err != nil {
		return fmt.Errorf("sign in with the mcp scope: %w", err)
	}
	x.p.Sayf("granted scopes: %s", strings.Join(g.Scopes, " "))
	// The token is revoked however the probes end, and the revocation is
	// then itself probed.
	revoked := false
	defer func() {
		if !revoked {
			_ = x.app.Revoke(context.Background(), g.Token.AccessToken)
		}
	}()
	mc := x.c
	mc.token = g.Token.AccessToken
	proj := "projects/" + itoa(x.s.ID)
	search := url.Values{"scope": {"issues"}, "search": {x.s.Name}}
	probes := []struct {
		method, route, path string
		query               url.Values
		tagged              bool
	}{
		{http.MethodGet, "user", "user", nil, false},
		{http.MethodGet, "projects/:id", proj, nil, false},
		{http.MethodGet, "projects/:id/issues/:iid", proj + "/issues/" + itoa(x.s.Issue), nil, true},
		{http.MethodGet, "projects/:id/issues", proj + "/issues", nil, false},
		{http.MethodGet, "projects/:id/repository/branches", proj + "/repository/branches", nil, true},
		{http.MethodGet, "projects/:id/merge_requests/:iid", proj + "/merge_requests/" + itoa(x.s.MR), nil, false},
		{http.MethodGet, "projects/:id/merge_requests/:iid/commits", proj + "/merge_requests/" + itoa(x.s.MR) + "/commits", nil, true},
		{http.MethodGet, "projects/:id/pipelines", proj + "/pipelines", nil, true},
		{http.MethodGet, "projects/:id/search", proj + "/search", search, true},
		{http.MethodPost, "projects/:id/issues/:iid/notes", proj + "/issues/" + itoa(x.s.Issue) + "/notes", nil, false},
	}
	agree := 0
	for _, pr := range probes {
		var r raw
		if pr.method == http.MethodPost {
			r, err = mc.write(ctx, pr.method, pr.path, map[string]any{"body": "An mcp-scope probe " + x.s.Name})
		} else {
			r, err = mc.get(ctx, pr.path, pr.query, nil)
		}
		if err != nil {
			return err
		}
		ok := r.status/100 == 2
		if ok == pr.tagged {
			agree++
		}
		x.p.Sayf("%-4s %-45s tagged %-5v status %d %s", pr.method, pr.route, pr.tagged, r.status,
			scopeError(r.header.Get("WWW-Authenticate")))
	}
	x.p.Sayf("%d of %d routes answer as their tag says", agree, len(probes))

	if err := x.app.Revoke(ctx, g.Token.AccessToken); err != nil {
		return fmt.Errorf("revoke the mcp token: %w", err)
	}
	revoked = true
	r, err := mc.get(ctx, proj+"/issues/"+itoa(x.s.Issue), nil, nil)
	if err != nil {
		return err
	}
	x.p.Sayf("after revocation: the access token answers %d %s", r.status, scopeError(r.header.Get("WWW-Authenticate")))
	fresh, err := x.app.Refresh(ctx, g.Token.RefreshToken)
	x.p.Sayf("after revocation: the refresh token is refused invalid_grant: %v", auth.IsInvalidGrant(err))
	if err == nil {
		// Revocation did not reach the refresh token: the pair it just
		// minted is live, and goes the same way.
		if err := x.app.Revoke(ctx, fresh.Token.AccessToken); err != nil {
			return fmt.Errorf("revoke the pair the refresh minted: %w", err)
		}
		x.p.Say("the pair the refresh minted is revoked")
	}
	return nil
}

// authError is the error parameter of a WWW-Authenticate header.
var authError = regexp.MustCompile(`error="([a-z_]+)"`)

// scopeError is the error GitLab names in a WWW-Authenticate header,
// such as insufficient_scope, and nothing else of it.
func scopeError(h string) string {
	m := authError.FindStringSubmatch(h)
	if m == nil {
		return ""
	}
	return "(" + m[1] + ")"
}

// askConditionalDelete asks how GitLab holds If-Unmodified-Since on a
// comment delete: a time before the comment's updated_at should be 412, the
// same second as an HTTP-date too if GitLab compares below the second, and
// the updated_at it shows, stretched to the end of its millisecond in RFC
// 3339, should delete.
func (x spikeRun) askConditionalDelete(ctx context.Context) error {
	notes := "projects/" + itoa(x.s.ID) + "/issues/" + itoa(x.s.Issue2) + "/notes"
	r, err := x.c.write(ctx, http.MethodPost, notes, map[string]any{"body": "A conditional-delete probe comment " + x.s.Name})
	if err != nil {
		return err
	}
	var note struct {
		ID        int64  `json:"id"`
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.Unmarshal(r.body, &note); err != nil || note.ID == 0 {
		return fmt.Errorf("the comment was not created: status %d", r.status)
	}
	x.p.Redactor().Known(redact.KindID, itoa(note.ID))
	shown, err := time.Parse(time.RFC3339Nano, note.UpdatedAt)
	if err != nil {
		return fmt.Errorf("updated_at is not RFC 3339: %w", err)
	}
	_, fraction, _ := strings.Cut(strings.TrimSuffix(note.UpdatedAt, "Z"), ".")
	x.p.Sayf("the comment's updated_at is shown with %d digits after the second", len(fraction))
	path := notes + "/" + itoa(note.ID)
	for _, try := range []struct {
		what   string
		header string
	}{
		{"a second before, RFC 3339", shown.Add(-time.Second).UTC().Format(time.RFC3339Nano)},
		{"the same second, as an HTTP-date", shown.UTC().Format(http.TimeFormat)},
		{"the time shown, unpadded, RFC 3339", shown.UTC().Format(time.RFC3339Nano)},
		{"the time shown stretched to the end of its millisecond, RFC 3339",
			shown.Truncate(time.Millisecond).Add(time.Millisecond - time.Microsecond).UTC().Format(time.RFC3339Nano)},
	} {
		res, err := x.c.send(ctx, http.MethodDelete, path, nil, http.Header{"If-Unmodified-Since": {try.header}}, nil)
		if err != nil {
			return err
		}
		x.p.Sayf("DELETE with If-Unmodified-Since %s: status %d", try.what, res.status)
		if res.status == http.StatusNoContent {
			return nil
		}
	}
	x.p.Say("!! no header deleted the comment; it is left on the second issue")
	return nil
}

// askConditionalReads asks whether If-None-Match earns a 304 on an API
// read, and whether a 304 is counted against the rate limit, for a
// single read and for a listing.
func (x spikeRun) askConditionalReads(ctx context.Context) error {
	paths := []string{"projects/" + itoa(x.s.ID), "projects/" + itoa(x.s.ID) + "/issues"}
	errs := make([]error, 0, len(paths))
	for _, path := range paths {
		errs = append(errs, conditionalRead(ctx, x.c, path, x.p))
	}
	return errors.Join(errs...)
}

// conditionalRead prints RateLimit-Remaining before and after three
// conditional reads of path.
func conditionalRead(ctx context.Context, c spikeClient, path string, p *redact.Printer) error {
	which := "a single read"
	if strings.HasSuffix(path, "/issues") {
		which = "a listing"
	}
	first, err := c.get(ctx, path, nil, nil)
	if err != nil {
		return err
	}
	etag := first.header.Get("ETag")
	p.Sayf("%s: status %d, ETag %s, RateLimit-Remaining %s", which, first.status, present(etag), orNone(first.header.Get("RateLimit-Remaining")))
	if etag == "" {
		return nil
	}
	for i := range 3 {
		r, err := c.get(ctx, path, nil, http.Header{"If-None-Match": {etag}})
		if err != nil {
			return err
		}
		p.Sayf("%s, conditional %d: status %d, %d body bytes, RateLimit-Remaining %s", which, i+1, r.status, len(r.body),
			orNone(r.header.Get("RateLimit-Remaining")))
	}
	return nil
}

// askJobLogWindows asks what byte_offset and byte_limit do to the trace
// endpoint: the window's size against the full log's bytes, the headers
// that describe it, and an offset past the end.
func (x spikeRun) askJobLogWindows(ctx context.Context) error {
	if x.s.JobFailed == 0 {
		x.p.Say("no finished job to read; this spike needs one")
		return nil
	}
	path := "projects/" + itoa(x.s.ID) + "/jobs/" + itoa(x.s.JobFailed) + "/trace"
	full, err := x.c.get(ctx, path, nil, nil)
	if err != nil {
		return err
	}
	x.p.Sayf("full: status %d, %d bytes, Content-Type %s, Content-Range %s", full.status, len(full.body),
		orNone(full.header.Get("Content-Type")), orNone(full.header.Get("Content-Range")))
	for _, q := range []url.Values{
		{"byte_offset": {"100"}, "byte_limit": {"50"}},
		{"byte_limit": {"50"}},
		{"byte_offset": {fmt.Sprint(len(full.body) - 40)}},
		{"byte_offset": {fmt.Sprint(len(full.body) + 10)}},
	} {
		r, err := x.c.get(ctx, path, q, nil)
		if err != nil {
			return err
		}
		verdict := "not a slice of the full log"
		for _, at := range []int{0, 100, len(full.body) - 40} {
			if at >= 0 && at <= len(full.body) && len(r.body) > 0 && bytes.HasPrefix(full.body[at:], r.body) {
				verdict = fmt.Sprintf("the full log's bytes from %d", at)
				break
			}
		}
		if len(r.body) == 0 {
			verdict = "empty"
		}
		x.p.Sayf("%s: status %d, %d bytes, %s; Content-Range %s", q.Encode(), r.status, len(r.body), verdict,
			orNone(r.header.Get("Content-Range")))
	}
	return nil
}

// askJobLogSize asks where a job log's size can be learned without reading
// the log, so get_job_log can read only its window: a finished job's trace
// artifact size against the bytes the trace endpoint serves, and what a HEAD
// of the trace says for a finished log and for one still being written. It
// also asks what byte_limit past GitLab's 500 KB cap answers, and which
// paging headers a merge request's diffs carry.
func (x spikeRun) askJobLogSize(ctx context.Context) error {
	if x.s.JobFailed == 0 {
		x.p.Say("no finished job to read; this spike needs one")
		return nil
	}
	project := "projects/" + itoa(x.s.ID)
	if err := logSize(ctx, x.c, project, x.s.JobFailed, "finished", x.p); err != nil {
		return err
	}
	// Last, once gitlab.com has archived the finished log, whether the
	// trace artifact's size is the log's.
	defer func() {
		if err := archivedSize(ctx, x.c, project, x.s.JobFailed, x.p); err != nil {
			x.p.Sayf("!! the archived log: %v", err)
		}
	}()
	r, err := x.c.get(ctx, project+"/jobs/"+itoa(x.s.JobFailed)+"/trace", url.Values{"byte_limit": {"512001"}}, nil)
	if err != nil {
		return err
	}
	x.p.Sayf("byte_limit 512001: status %d, %d bytes", r.status, len(r.body))

	r, err = x.c.get(ctx, project+"/merge_requests/"+itoa(x.s.MR)+"/diffs", url.Values{"per_page": {"1"}}, nil)
	if err != nil {
		return err
	}
	x.p.Sayf("merge request diffs, per_page 1: status %d, X-Total %s, X-Total-Pages %s, X-Next-Page %s, Link %s", r.status,
		orNone(r.header.Get("X-Total")), orNone(r.header.Get("X-Total-Pages")), orNone(r.header.Get("X-Next-Page")),
		present(r.header.Get("Link")))

	// A log still being written: a pipeline whose slow job prints and
	// then sleeps, read while it runs, then canceled.
	created, err := x.c.write(ctx, http.MethodPost, project+"/pipeline", map[string]any{"ref": x.s.Default, "inputs": map[string]any{"slow": "yes"}})
	if err != nil {
		return err
	}
	var pl struct {
		ID int64 `json:"id"`
	}
	if created.status != http.StatusCreated || json.Unmarshal(created.body, &pl) != nil {
		x.p.Sayf("a pipeline with the slow job: status %d; the running half is not asked", created.status)
		return nil
	}
	x.p.Redactor().Known(redact.KindID, itoa(pl.ID))
	defer func() {
		r, err := x.c.write(ctx, http.MethodPost, project+"/pipelines/"+itoa(pl.ID)+"/cancel", map[string]any{})
		if err != nil {
			x.p.Sayf("!! cancel the slow pipeline: %v", err)
			return
		}
		x.p.Sayf("slow pipeline canceled: status %d", r.status)
	}()
	deadline := time.Now().Add(8 * time.Minute)
	for {
		r, err := x.c.get(ctx, project+"/pipelines/"+itoa(pl.ID)+"/jobs", nil, nil)
		if err != nil {
			return err
		}
		var jobs []struct {
			ID     int64  `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(r.body, &jobs); err != nil {
			return err
		}
		for _, j := range jobs {
			if j.Name != "slow log" || j.Status != "running" {
				continue
			}
			x.p.Redactor().Known(redact.KindID, itoa(j.ID))
			t, err := x.c.get(ctx, project+"/jobs/"+itoa(j.ID)+"/trace", nil, nil)
			if err != nil {
				return err
			}
			// The job prints its lines at once; a log this long is past them.
			if len(t.body) > 20000 {
				return logSize(ctx, x.c, project, j.ID, "running", x.p)
			}
		}
		if time.Now().After(deadline) {
			x.p.Say("the slow job did not print its lines in time; the running half is not asked")
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
}

// archivedSize waits for a finished job's trace artifact and compares
// its size with the bytes the trace endpoint serves.
func archivedSize(ctx context.Context, c spikeClient, project string, job int64, p *redact.Printer) error {
	deadline := time.Now().Add(10 * time.Minute)
	for {
		j, err := c.get(ctx, project+"/jobs/"+itoa(job), nil, nil)
		if err != nil {
			return err
		}
		var jr struct {
			Artifacts []struct {
				FileType string `json:"file_type"`
				Size     int64  `json:"size"`
			} `json:"artifacts"`
		}
		if err := json.Unmarshal(j.body, &jr); err != nil {
			return err
		}
		for _, a := range jr.Artifacts {
			if a.FileType != "trace" {
				continue
			}
			full, err := c.get(ctx, project+"/jobs/"+itoa(job)+"/trace", nil, http.Header{"Accept-Encoding": {"identity"}})
			if err != nil {
				return err
			}
			tail, err := c.get(ctx, project+"/jobs/"+itoa(job)+"/trace", url.Values{"byte_offset": {fmt.Sprint(a.Size - 10)}}, nil)
			if err != nil {
				return err
			}
			p.Sayf("archived log: trace artifact size %d, GET %d bytes, the last ten bytes from its size %s", a.Size, len(full.body),
				map[bool]string{true: "match", false: "differ"}[bytes.Equal(tail.body, full.body[max(0, len(full.body)-10):])])
			return nil
		}
		if time.Now().After(deadline) {
			p.Say("the finished log was not archived in time; the archived half is not asked")
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Second):
		}
	}
}

// logSize prints what the job, a HEAD of its trace and the trace itself
// say about the log's size.
func logSize(ctx context.Context, c spikeClient, project string, job int64, which string, p *redact.Printer) error {
	j, err := c.get(ctx, project+"/jobs/"+itoa(job), nil, nil)
	if err != nil {
		return err
	}
	var jr struct {
		Status    string `json:"status"`
		Artifacts []struct {
			FileType string `json:"file_type"`
			Size     int64  `json:"size"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(j.body, &jr); err != nil {
		return err
	}
	traceSize := "no trace artifact"
	for _, a := range jr.Artifacts {
		if a.FileType == "trace" {
			traceSize = fmt.Sprintf("trace artifact size %d", a.Size)
		}
	}
	// identity keeps Content-Length the log's own size: Go would ask
	// for gzip, and drop the header when it unpacks the answer.
	path := project + "/jobs/" + itoa(job) + "/trace"
	identity := http.Header{"Accept-Encoding": {"identity"}}
	head, err := c.send(ctx, http.MethodHead, path, nil, identity, nil)
	if err != nil {
		return err
	}
	full, err := c.get(ctx, path, nil, identity)
	if err != nil {
		return err
	}
	p.Sayf("%s job (%s): %s; HEAD status %d, Content-Length %s, RateLimit-Remaining %s; GET %d bytes, Content-Length %s",
		which, jr.Status, traceSize, head.status, orNone(head.header.Get("Content-Length")),
		orNone(head.header.Get("RateLimit-Remaining")), len(full.body), orNone(full.header.Get("Content-Length")))
	return nil
}

func present(v string) string {
	if v == "" {
		return "absent"
	}
	return "present"
}

func orNone(v string) string {
	if v == "" {
		return "(none)"
	}
	return v
}

// quickCase is one body shape the quick-action spike sends. Each applies
// its own label, so whether GitLab ran the command shows in the labels of
// the target.
type quickCase struct {
	name string
	body func(cmd string) string
}

// quickCases are the body shapes the quick-action spike sends. The text
// is this driver's.
var quickCases = []quickCase{
	{"bare", func(cmd string) string { return "Quick-action probe text.\n\n" + cmd + "\n" }},
	{"fenced", func(cmd string) string { return "Quick-action probe text.\n\n```\n" + cmd + "\n```\n" }},
	{"indented", func(cmd string) string { return "Quick-action probe text.\n\n    " + cmd + "\n" }},
	{"quoted", func(cmd string) string { return "Quick-action probe text.\n\n> " + cmd + "\n" }},
	{"quoted-multiline", func(cmd string) string { return "Quick-action probe text.\n\n>>>\n" + cmd + "\n>>>\n" }},
	{"inline", func(cmd string) string { return "Quick-action probe text.\n\n`" + cmd + "`\n" }},
	{"html", func(cmd string) string { return "Quick-action probe text.\n\n<div>\n" + cmd + "\n</div>\n" }},
	{"after-list-item", func(cmd string) string { return "Quick-action probe text.\n\n- a list item\n" + cmd + "\n" }},
	{"after-list-blank", func(cmd string) string { return "Quick-action probe text.\n\n- a list item\n\n" + cmd + "\n" }},
	{"in-paragraph", func(cmd string) string { return "Quick-action probe text.\n" + cmd + "\n" }},
	{"crlf", func(cmd string) string { return "Quick-action probe text.\r\n\r\n" + cmd + "\r\n" }},
	{"lone-cr", func(cmd string) string { return "Quick-action probe text.\r\r" + cmd + "\r" }},
}

// quickSurface is one place GitLab takes a body from.
type quickSurface struct {
	name string
	// send writes body to the surface and returns the status and the
	// target's labels afterwards.
	send func(ctx context.Context, body string) (int, []string, error)
}

// askQuickActions asks, per body shape and per surface, whether GitLab
// ran the command, and holds each answer against internal/quickaction: a
// command GitLab ran that Find did not report is a hole in the guard.
// Every shape the guard reports is sent again escaped, which must not
// run, and the escaped text is rendered to show it reads the same. Each
// command is /label with a label of its own, so the effect is visible and
// harmless.
func (x spikeRun) askQuickActions(ctx context.Context) error {
	id := itoa(x.s.ID)
	project := "projects/" + id
	// A fresh issue, so the labels read are only the spike's.
	var issue struct {
		IID int64 `json:"iid"`
	}
	r, err := x.c.write(ctx, http.MethodPost, project+"/issues", map[string]any{"title": "Quick-action probe " + x.s.Name})
	if err != nil || r.status != http.StatusCreated || json.Unmarshal(r.body, &issue) != nil {
		return fmt.Errorf("create the spike's issue: status %d: %w", r.status, err)
	}
	issuePath := project + "/issues/" + itoa(issue.IID)
	surfaces := quickSurfaces(x.c, x.s, project, issuePath, project+"/merge_requests/"+itoa(x.s.MR))

	// One label per case, surface and variant, made before any is used.
	label := func(ci, si int, escaped bool) string {
		l := fmt.Sprintf("quick-action-%d-%d", ci, si)
		if escaped {
			l += "-esc"
		}
		return l
	}
	for ci := range quickCases {
		for si := range surfaces {
			for _, esc := range []bool{false, true} {
				r, err := x.c.write(ctx, http.MethodPost, project+"/labels", map[string]any{"name": label(ci, si, esc), "color": "#6699cc"})
				if err != nil || (r.status != http.StatusCreated && r.status != http.StatusConflict) {
					return fmt.Errorf("create a spike label: status %d: %w", r.status, err)
				}
			}
		}
	}

	holes, escapeHoles := 0, 0
	for si, surf := range surfaces {
		for ci, qc := range quickCases {
			cmd := "/label ~" + label(ci, si, false)
			body := qc.body(cmd)
			found := len(quickaction.Find(body)) > 0
			status, labels, err := surf.send(ctx, body)
			if err != nil {
				return fmt.Errorf("%s, %s: %w", surf.name, qc.name, err)
			}
			ran := slices.Contains(labels, label(ci, si, false))
			verdict := "agrees"
			switch {
			case ran && !found:
				verdict = "!! HOLE: GitLab ran it and the guard missed it"
				holes++
			case !ran && found:
				verdict = "the guard is stricter than GitLab here"
			}
			x.p.Sayf("%s, %s: status %d, GitLab ran it: %t, guard found it: %t — %s", surf.name, qc.name, status, ran, found, verdict)
			if !found {
				continue
			}
			escCmd := "/label ~" + label(ci, si, true)
			escaped, _ := quickaction.Escape(qc.body(escCmd))
			status, labels, err = surf.send(ctx, escaped)
			if err != nil {
				return fmt.Errorf("%s, %s escaped: %w", surf.name, qc.name, err)
			}
			escRan := slices.Contains(labels, label(ci, si, true))
			if escRan {
				escapeHoles++
			}
			x.p.Sayf("%s, %s escaped: status %d, GitLab ran it: %t%s", surf.name, qc.name, status, escRan,
				map[bool]string{true: " — !! the escape did not hold", false: ""}[escRan])
		}
	}

	// A note that is only a command: GitLab answers 202 and saves nothing.
	only := "/label ~" + label(0, 0, false)
	r, err = x.c.write(ctx, http.MethodPost, issuePath+"/notes", map[string]any{"body": only})
	if err != nil {
		return err
	}
	x.p.Sayf("issue note that is only a command: status %d, %d body bytes", r.status, len(r.body))

	// The escaped line renders as the command's own text.
	escaped, _ := quickaction.Escape("/label ~quick-action-render\n")
	r, err = x.c.write(ctx, http.MethodPost, "markdown", map[string]any{"text": escaped, "gfm": true, "project": x.s.Path})
	if err != nil {
		return err
	}
	var md struct {
		HTML string `json:"html"`
	}
	_ = json.Unmarshal(r.body, &md)
	text := htmlTags.ReplaceAllString(md.HTML, "")
	x.p.Sayf("escaped line rendered: status %d, shows the command's text: %t, shows the backslash: %t",
		r.status, strings.Contains(text, "/label"), strings.Contains(text, `\/label`))

	x.p.Sayf("%d hole(s) in the guard, %d escape(s) that did not hold", holes, escapeHoles)
	return nil
}

var htmlTags = regexp.MustCompile(`<[^>]*>`)

// labelsOf reads an issue's or a merge request's labels.
func (c spikeClient) labelsOf(ctx context.Context, path string) ([]string, error) {
	r, err := c.get(ctx, path, nil, nil)
	if err != nil {
		return nil, err
	}
	var item struct {
		Labels []string `json:"labels"`
	}
	if r.status != http.StatusOK || json.Unmarshal(r.body, &item) != nil {
		return nil, fmt.Errorf("read labels: status %d", r.status)
	}
	return item.Labels, nil
}

// quickSurfaces are the four places the quick-action spike sends a body:
// a note on an issue and on a merge request, a new issue's description,
// and a merge request description update. Each answers the target's
// labels, read back rather than taken from the write's own answer.
func quickSurfaces(c spikeClient, s scratch, project, issuePath, mrPath string) []quickSurface {
	postThenRead := func(method, path, target string, body func(string) map[string]any) func(context.Context, string) (int, []string, error) {
		return func(ctx context.Context, text string) (int, []string, error) {
			r, err := c.write(ctx, method, path, body(text))
			if err != nil {
				return 0, nil, err
			}
			l, err := c.labelsOf(ctx, target)
			return r.status, l, err
		}
	}
	noteBody := func(text string) map[string]any { return map[string]any{"body": text} }
	return []quickSurface{
		{"issue note", postThenRead(http.MethodPost, issuePath+"/notes", issuePath, noteBody)},
		{"merge request note", postThenRead(http.MethodPost, mrPath+"/notes", mrPath, noteBody)},
		{"new issue description", func(ctx context.Context, text string) (int, []string, error) {
			r, err := c.write(ctx, http.MethodPost, project+"/issues", map[string]any{"title": "Quick-action probe case " + s.Name, "description": text})
			if err != nil {
				return 0, nil, err
			}
			var made struct {
				IID int64 `json:"iid"`
			}
			if json.Unmarshal(r.body, &made) != nil || made.IID == 0 {
				return r.status, nil, fmt.Errorf("create an issue: status %d", r.status)
			}
			l, err := c.labelsOf(ctx, project+"/issues/"+itoa(made.IID))
			return r.status, l, err
		}},
		{"merge request description update", postThenRead(http.MethodPut, mrPath, mrPath,
			func(text string) map[string]any { return map[string]any{"description": text} })},
	}
}

// ------------------------------------------------------------ inline positions

// diffRow is one file of a merge request's diffs, as the inline-position
// spike reads it.
type diffRow struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	Diff    string `json:"diff"`
}

// askInlinePositions asks whether the positions diffpos computes land
// where GitLab puts the same lines: a draft note answers with the
// line_code GitLab computed from the position, and a range with the codes
// it stored. The drafts are deleted afterwards, so the plan's review sees
// only its own.
func (x spikeRun) askInlinePositions(ctx context.Context) error {
	mrPath := "projects/" + itoa(x.s.ID) + "/merge_requests/" + itoa(x.s.MR)
	r, err := x.c.get(ctx, mrPath, nil, nil)
	if err != nil {
		return err
	}
	var mr struct {
		DiffRefs struct {
			BaseSHA  string `json:"base_sha"`
			StartSHA string `json:"start_sha"`
			HeadSHA  string `json:"head_sha"`
		} `json:"diff_refs"`
	}
	if r.status != http.StatusOK || json.Unmarshal(r.body, &mr) != nil {
		return fmt.Errorf("read the merge request: status %d", r.status)
	}
	r, err = x.c.get(ctx, mrPath+"/diffs", nil, nil)
	if err != nil {
		return err
	}
	var diffs []diffRow
	if r.status != http.StatusOK || json.Unmarshal(r.body, &diffs) != nil {
		return fmt.Errorf("read the diffs: status %d", r.status)
	}
	i := slices.IndexFunc(diffs, func(d diffRow) bool { return d.NewPath == x.s.File })
	if i < 0 {
		return fmt.Errorf("the merge request does not change %s", x.s.File)
	}
	file := diffpos.File{OldPath: diffs[i].OldPath, NewPath: diffs[i].NewPath, Diff: diffs[i].Diff}
	refs := diffpos.Refs{BaseSHA: mr.DiffRefs.BaseSHA, StartSHA: mr.DiffRefs.StartSHA, HeadSHA: mr.DiffRefs.HeadSHA}
	agree := 0
	cases := []struct {
		what      string
		side      diffpos.Side
		line, end int
	}{
		{"an added line", diffpos.New, 3, 0},
		{"a removed line", diffpos.Old, 4, 0},
		{"an unchanged line by its new number", diffpos.New, 1, 0},
		{"an unchanged line by its old number", diffpos.Old, 2, 0},
		{"a new-side range over unchanged and added lines", diffpos.New, 1, 4},
		{"an old-side range over removed lines", diffpos.Old, 3, 4},
	}
	for _, tc := range cases {
		placed, err := diffpos.Compute(refs, file, tc.side, tc.line, tc.end)
		if err != nil {
			x.p.Sayf("%s: diffpos refused it: %v", tc.what, err)
			continue
		}
		r, err := x.c.write(ctx, http.MethodPost, mrPath+"/draft_notes", map[string]any{"note": "An inline position probe on " + tc.what + ".",
			"position": placed.Position})
		if err != nil {
			return err
		}
		var draft struct {
			ID       int64  `json:"id"`
			LineCode string `json:"line_code"`
			Position struct {
				LineRange *struct {
					Start struct {
						LineCode string `json:"line_code"`
					} `json:"start"`
					End struct {
						LineCode string `json:"line_code"`
					} `json:"end"`
				} `json:"line_range"`
			} `json:"position"`
		}
		_ = json.Unmarshal(r.body, &draft)
		x.p.Redactor().Known(redact.KindID, itoa(draft.ID))
		ok := r.status == http.StatusCreated && draft.LineCode == placed.End.Code(file)
		if tc.end != 0 {
			lr := draft.Position.LineRange
			ok = ok && lr != nil && lr.Start.LineCode == placed.Start.Code(file) && lr.End.LineCode == placed.End.Code(file)
		}
		if ok {
			agree++
		}
		x.p.Sayf("%s (%s %d%s): status %d, GitLab's line_code agrees with diffpos: %t", tc.what, tc.side, tc.line,
			map[bool]string{true: fmt.Sprintf("–%d", tc.end), false: ""}[tc.end != 0], r.status, ok)
		if draft.ID != 0 {
			if d, err := x.c.send(ctx, http.MethodDelete, mrPath+"/draft_notes/"+itoa(draft.ID), nil, nil, nil); err != nil || d.status != http.StatusNoContent {
				x.p.Sayf("!! could not delete the spike's draft: status %d", d.status)
			}
		}
	}
	x.p.Sayf("%d of %d positions agree", agree, len(cases))
	return nil
}

// ------------------------------------------------------------ protection

// askProtectedFlag asks whether a branch's own protected flag reflects a
// wildcard rule, which create_commit trusts for a branch that exists. It
// makes the rule and a branch under it; the plan then commits to both that
// branch and a new one under the rule, and expects both refused.
func (x spikeRun) askProtectedFlag(ctx context.Context) error {
	project := "projects/" + itoa(x.s.ID)
	r, err := x.c.write(ctx, http.MethodPost, project+"/protected_branches", map[string]any{"name": guardRule(x.s), "push_access_level": 40})
	if err != nil || r.status != http.StatusCreated {
		return fmt.Errorf("create the rule: status %d: %w", r.status, err)
	}
	r, err = x.c.write(ctx, http.MethodPost, project+"/repository/branches", map[string]any{"branch": guardBranch(x.s), "ref": x.s.Default})
	if err != nil || r.status != http.StatusCreated {
		return fmt.Errorf("create the branch: status %d: %w", r.status, err)
	}
	r, err = x.c.get(ctx, project+"/repository/branches/"+url.PathEscape(guardBranch(x.s)), nil, nil)
	if err != nil {
		return err
	}
	var b struct {
		Protected bool `json:"protected"`
	}
	_ = json.Unmarshal(r.body, &b)
	x.p.Sayf("a branch under a wildcard rule: status %d, protected: %t", r.status, b.Protected)
	return nil
}

// ------------------------------------------------------------ settle by reading

// askSettleByReading asks how soon each read that settles an ambiguous
// create finds what the create made: it creates, then reads at once and
// every 250 ms for up to ten seconds, and prints how long the read took to
// see it.
func (x spikeRun) askSettleByReading(ctx context.Context) error {
	project := "projects/" + itoa(x.s.ID)
	since := time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339)
	me := url.Values{"author_username": {x.s.User}, "created_after": {since}, "state": {"all"}, "per_page": {"100"}}
	branch := "settle-probe-" + x.s.Name[len(x.s.Name)-6:]
	checks := []struct {
		what   string
		create func() (int, error)
		find   func() (bool, error)
	}{
		{"an issue, by author, created_after and title", func() (int, error) {
			r, err := x.c.write(ctx, http.MethodPost, project+"/issues", map[string]any{"title": "Settle probe issue " + x.s.Name})
			return r.status, err
		}, func() (bool, error) {
			return x.c.listed(ctx, project+"/issues", me, "title", "Settle probe issue "+x.s.Name)
		}},
		{"a comment, among the issue's threads", func() (int, error) {
			// The second issue: a comment on the first would mark the run's
			// own to-do item on it done, which a later read looks for.
			r, err := x.c.write(ctx, http.MethodPost, project+"/issues/"+itoa(x.s.Issue2)+"/notes", map[string]any{"body": "Settle probe comment " + x.s.Name})
			return r.status, err
		}, func() (bool, error) {
			return x.c.inThreads(ctx, project+"/issues/"+itoa(x.s.Issue2)+"/discussions", "Settle probe comment "+x.s.Name)
		}},
		{"a draft, among the account's drafts", func() (int, error) {
			r, err := x.c.write(ctx, http.MethodPost, project+"/merge_requests/"+itoa(x.s.MR)+"/draft_notes", map[string]any{"note": "Settle probe draft " + x.s.Name})
			return r.status, err
		}, func() (bool, error) {
			return x.c.listed(ctx, project+"/merge_requests/"+itoa(x.s.MR)+"/draft_notes", nil, "note", "Settle probe draft "+x.s.Name)
		}},
		{"a commit, as the branch's head", func() (int, error) {
			r, err := x.c.write(ctx, http.MethodPost, project+"/repository/commits", map[string]any{"branch": branch, "start_branch": x.s.Default,
				"commit_message": "Settle probe commit " + x.s.Name, "actions": []any{map[string]any{"action": "create", "file_path": "settle-probe.txt", "content": "settle\n"}}})
			return r.status, err
		}, func() (bool, error) {
			r, err := x.c.get(ctx, project+"/repository/branches/"+branch, nil, nil)
			if err != nil || r.status != http.StatusOK {
				return false, err
			}
			var b struct {
				Commit struct {
					Message string `json:"message"`
				} `json:"commit"`
			}
			_ = json.Unmarshal(r.body, &b)
			return strings.TrimSpace(b.Commit.Message) == "Settle probe commit "+x.s.Name, nil
		}},
		{"a merge request, by author, created_after and title", func() (int, error) {
			r, err := x.c.write(ctx, http.MethodPost, project+"/merge_requests", map[string]any{"source_branch": branch,
				"target_branch": x.s.Default, "title": "Settle probe merge request " + x.s.Name})
			return r.status, err
		}, func() (bool, error) {
			return x.c.listed(ctx, project+"/merge_requests", me, "title", "Settle probe merge request "+x.s.Name)
		}},
		{"a pipeline, on the ref by source api, username and created_after", func() (int, error) {
			r, err := x.c.write(ctx, http.MethodPost, project+"/pipeline", map[string]any{"ref": branch})
			return r.status, err
		}, func() (bool, error) {
			return x.c.listed(ctx, project+"/pipelines", url.Values{"ref": {branch}, "source": {"api"}, "username": {x.s.User},
				"created_after": {since}}, "ref", branch)
		}},
		{"a release, by its tag", func() (int, error) {
			r, err := x.c.write(ctx, http.MethodPost, project+"/releases", map[string]any{"tag_name": branch, "ref": branch})
			return r.status, err
		}, func() (bool, error) {
			r, err := x.c.get(ctx, project+"/releases/"+branch, nil, nil)
			return err == nil && r.status == http.StatusOK, err
		}},
	}
	for _, ck := range checks {
		status, err := ck.create()
		if err != nil {
			return err
		}
		start := time.Now()
		found := false
		for !found && time.Since(start) < 10*time.Second {
			if found, err = ck.find(); err != nil {
				return err
			}
			if !found {
				time.Sleep(250 * time.Millisecond)
			}
		}
		x.p.Sayf("%s: created with status %d; found by reading: %t, after %d ms", ck.what, status, found, time.Since(start).Milliseconds())
	}
	// The spike's pipeline runs nothing the plan needs; it is canceled.
	if r, err := x.c.get(ctx, project+"/pipelines", url.Values{"ref": {branch}, "source": {"api"}}, nil); err == nil && r.status == http.StatusOK {
		var rows []struct {
			ID int64 `json:"id"`
		}
		_ = json.Unmarshal(r.body, &rows)
		for _, pl := range rows {
			x.p.Redactor().Known(redact.KindID, itoa(pl.ID))
			_, _ = x.c.write(ctx, http.MethodPost, project+"/pipelines/"+itoa(pl.ID)+"/cancel", nil)
		}
	}
	// The draft would be published by the plan's review; it is removed.
	r, err := x.c.get(ctx, project+"/merge_requests/"+itoa(x.s.MR)+"/draft_notes", nil, nil)
	if err == nil && r.status == http.StatusOK {
		var drafts []struct {
			ID   int64  `json:"id"`
			Note string `json:"note"`
		}
		_ = json.Unmarshal(r.body, &drafts)
		for _, d := range drafts {
			if strings.HasPrefix(d.Note, "Settle probe draft") {
				x.p.Redactor().Known(redact.KindID, itoa(d.ID))
				_, _ = x.c.send(ctx, http.MethodDelete, project+"/merge_requests/"+itoa(x.s.MR)+"/draft_notes/"+itoa(d.ID), nil, nil, nil)
			}
		}
	}
	return nil
}

// ------------------------------------------------------------ the merge status check

// askMergeStatusCheck asks whether reading a merge request is what makes
// GitLab work out whether it can merge. It opens a merge request of its own
// and watches it settle, pushes to it, watches it through the listing, which
// the source says never starts the check, and then through single reads,
// which it says do. Its commits skip CI.
func (x spikeRun) askMergeStatusCheck(ctx context.Context) error {
	project := "projects/" + itoa(x.s.ID)
	branch := "merge-status-probe-" + x.s.Name[len(x.s.Name)-6:]
	commit := func(body map[string]any) error {
		body["branch"], body["commit_message"] = branch, "Merge status probe commit [skip ci]"
		r, err := x.c.write(ctx, http.MethodPost, project+"/repository/commits", body)
		if err == nil && r.status != http.StatusCreated {
			err = fmt.Errorf("a commit answered %d", r.status)
		}
		return err
	}
	if err := commit(map[string]any{"start_branch": x.s.Default,
		"actions": []any{map[string]any{"action": "create", "file_path": "merge-status-probe.txt", "content": "first\n"}}}); err != nil {
		return err
	}
	r, err := x.c.write(ctx, http.MethodPost, project+"/merge_requests", map[string]any{"source_branch": branch,
		"target_branch": x.s.Default, "title": "Merge status probe merge request"})
	if err != nil {
		return err
	}
	var mr struct {
		IID int64 `json:"iid"`
	}
	if r.status != http.StatusCreated || json.Unmarshal(r.body, &mr) != nil || mr.IID == 0 {
		return fmt.Errorf("opening the merge request answered %d", r.status)
	}
	path := project + "/merge_requests/" + itoa(mr.IID)
	single := func() (string, error) {
		var one mergeStatus
		err := x.c.decode(ctx, path, nil, &one)
		return one.String(), err
	}
	// with_labels_details only keeps the listing out of GitLab's cache.
	inListing := func() (string, error) {
		var rows []mergeStatus
		err := x.c.decode(ctx, project+"/merge_requests", url.Values{"iids[]": {itoa(mr.IID)}, "with_labels_details": {"true"}}, &rows)
		if err == nil && len(rows) != 1 {
			err = fmt.Errorf("the listing has %d rows for one merge request", len(rows))
		}
		if err != nil {
			return "", err
		}
		return rows[0].String(), nil
	}
	if err := watchMergeStatus(x.p, "a new merge request, read singly", single); err != nil {
		return err
	}
	if err := commit(map[string]any{"actions": []any{map[string]any{"action": "update", "file_path": "merge-status-probe.txt",
		"content": "second\n"}}}); err != nil {
		return err
	}
	if err := watchMergeStatus(x.p, "after a push, through the listing", inListing); err != nil {
		return err
	}
	if err := watchMergeStatus(x.p, "then read singly", single); err != nil {
		return err
	}
	_, err = x.c.write(ctx, http.MethodPut, path, map[string]any{"state_event": "close"})
	return err
}

// mergeStatus is what the merge-status spike reads of a merge request.
type mergeStatus struct {
	Status       string `json:"detailed_merge_status"`
	HasConflicts bool   `json:"has_conflicts"`
}

func (m mergeStatus) String() string {
	return fmt.Sprintf("%s (has_conflicts %t)", m.Status, m.HasConflicts)
}

// watchMergeStatus reads a merge request's detailed_merge_status every
// second for twenty seconds, and prints each value it shows and when it
// first came.
func watchMergeStatus(p *redact.Printer, what string, read func() (string, error)) error {
	start := time.Now()
	var seen []string
	last := ""
	for time.Since(start) < 20*time.Second {
		status, err := read()
		if err != nil {
			return err
		}
		if status != last || len(seen) == 0 {
			seen = append(seen, fmt.Sprintf("%s at %d ms", status, time.Since(start).Milliseconds()))
			last = status
		}
		time.Sleep(time.Second)
	}
	p.Sayf("%s: %s", what, strings.Join(seen, ", then "))
	return nil
}

// decode reads path and decodes a 200 answer into out.
func (c spikeClient) decode(ctx context.Context, path string, q url.Values, out any) error {
	r, err := c.get(ctx, path, q, nil)
	if err != nil {
		return err
	}
	if r.status != http.StatusOK {
		return fmt.Errorf("a read answered %d", r.status)
	}
	return json.Unmarshal(r.body, out)
}

// listed reports whether a listing has a row whose field equals want.
func (c spikeClient) listed(ctx context.Context, path string, q url.Values, field, want string) (bool, error) {
	r, err := c.get(ctx, path, q, nil)
	if err != nil || r.status != http.StatusOK {
		return false, err
	}
	var rows []map[string]any
	if err := json.Unmarshal(r.body, &rows); err != nil {
		return false, fmt.Errorf("a listing was not a JSON array: %w", err)
	}
	return slices.ContainsFunc(rows, func(row map[string]any) bool { return row[field] == want }), nil
}

// inThreads reports whether a thread listing holds a note with body want.
func (c spikeClient) inThreads(ctx context.Context, path, want string) (bool, error) {
	r, err := c.get(ctx, path, url.Values{"per_page": {"100"}}, nil)
	if err != nil || r.status != http.StatusOK {
		return false, err
	}
	var threads []struct {
		Notes []struct {
			Body string `json:"body"`
		} `json:"notes"`
	}
	_ = json.Unmarshal(r.body, &threads)
	for _, t := range threads {
		for _, n := range t.Notes {
			if n.Body == want {
				return true, nil
			}
		}
	}
	return false, nil
}
