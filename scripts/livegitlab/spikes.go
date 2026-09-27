//go:build live

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/app"
	"github.com/mmedum/gitlab-mcp/internal/diffpos"
	"github.com/mmedum/gitlab-mcp/internal/quickaction"
	netredact "github.com/mmedum/gitlab-mcp/internal/redact"
	"github.com/mmedum/gitlab-mcp/scripts/internal/redact"
)

// The spikes of docs/architecture.md §15 the phases owe, against the
// scratch project only. They go around the client, because they ask what
// the client does not: headers it does not send and answers it does not
// keep. Only statuses, sizes and header names and values GitLab sets are
// printed; never a token, and never a body.

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

func spikes(ctx context.Context, settings *app.Settings, s scratch, only string, p *redact.Printer) {
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
	c := spikeClient{http: hc, root: settings.Instance.APIRoot(), token: tok}
	if only == "E" {
		p.Say("\n=== spike E: quick actions ===")
		if err := spikeE(ctx, c, s, p); err != nil {
			p.Sayf("!! spike E: %v", err)
		}
		return
	}
	p.Say("\n=== spike H: conditional reads ===")
	for _, path := range []string{"projects/" + itoa(s.ID), "projects/" + itoa(s.ID) + "/issues"} {
		if err := spikeH(ctx, c, path, p); err != nil {
			p.Sayf("!! spike H: %v", err)
		}
	}
	p.Say("\n=== spike J: job log windows ===")
	if err := spikeJ(ctx, c, s, p); err != nil {
		p.Sayf("!! spike J: %v", err)
	}
	p.Say("\n=== spike K: inline positions ===")
	if err := spikeK(ctx, c, s, p); err != nil {
		p.Sayf("!! spike K: %v", err)
	}
	p.Say("\n=== the protected flag of a branch a wildcard rule covers ===")
	if err := protectedFlag(ctx, c, s, p); err != nil {
		p.Sayf("!! protected flag: %v", err)
	}
	p.Say("\n=== spike M: settle by reading ===")
	if err := spikeM(ctx, c, s, p); err != nil {
		p.Sayf("!! spike M: %v", err)
	}
	p.Say("\n=== spike N: a conditional comment delete ===")
	if err := spikeN(ctx, c, s, p); err != nil {
		p.Sayf("!! spike N: %v", err)
	}
}

// spikeN asks how GitLab holds If-Unmodified-Since on a comment delete
// (§4.6, §18 row 62): a time before the comment's updated_at should be
// 412, the same second as an HTTP-date too if GitLab compares below the
// second, and the updated_at it shows, stretched to the end of its
// millisecond in RFC 3339, should delete.
func spikeN(ctx context.Context, c spikeClient, s scratch, p *redact.Printer) error {
	notes := "projects/" + itoa(s.ID) + "/issues/" + itoa(s.Issue2) + "/notes"
	r, err := c.write(ctx, http.MethodPost, notes, map[string]any{"body": "Spike N comment " + s.Name})
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
	p.Redactor().Known(redact.KindID, itoa(note.ID))
	shown, err := time.Parse(time.RFC3339Nano, note.UpdatedAt)
	if err != nil {
		return fmt.Errorf("updated_at is not RFC 3339: %w", err)
	}
	_, fraction, _ := strings.Cut(strings.TrimSuffix(note.UpdatedAt, "Z"), ".")
	p.Sayf("the comment's updated_at is shown with %d digits after the second", len(fraction))
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
		res, err := c.send(ctx, http.MethodDelete, path, nil, http.Header{"If-Unmodified-Since": {try.header}}, nil)
		if err != nil {
			return err
		}
		p.Sayf("DELETE with If-Unmodified-Since %s: status %d", try.what, res.status)
		if res.status == http.StatusNoContent {
			return nil
		}
	}
	p.Say("!! no header deleted the comment; it is left on the second issue")
	return nil
}

// spikeH asks whether If-None-Match earns a 304 on an API read, and
// whether a 304 is counted against the rate limit: RateLimit-Remaining
// before and after three conditional reads.
func spikeH(ctx context.Context, c spikeClient, path string, p *redact.Printer) error {
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

// spikeJ asks what byte_offset and byte_limit do to the trace endpoint:
// the window's size against the full log's bytes, the headers that
// describe it, and an offset past the end.
func spikeJ(ctx context.Context, c spikeClient, s scratch, p *redact.Printer) error {
	if s.JobFailed == 0 {
		p.Say("no finished job to read; spike J needs one")
		return nil
	}
	path := "projects/" + itoa(s.ID) + "/jobs/" + itoa(s.JobFailed) + "/trace"
	full, err := c.get(ctx, path, nil, nil)
	if err != nil {
		return err
	}
	p.Sayf("full: status %d, %d bytes, Content-Type %s, Content-Range %s", full.status, len(full.body),
		orNone(full.header.Get("Content-Type")), orNone(full.header.Get("Content-Range")))
	for _, q := range []url.Values{
		{"byte_offset": {"100"}, "byte_limit": {"50"}},
		{"byte_limit": {"50"}},
		{"byte_offset": {fmt.Sprint(len(full.body) - 40)}},
		{"byte_offset": {fmt.Sprint(len(full.body) + 10)}},
	} {
		r, err := c.get(ctx, path, q, nil)
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
		p.Sayf("%s: status %d, %d bytes, %s; Content-Range %s", q.Encode(), r.status, len(r.body), verdict,
			orNone(r.header.Get("Content-Range")))
	}
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

// quickCase is one body shape spike E sends. Each applies its own label,
// so whether GitLab ran the command shows in the labels of the target.
type quickCase struct {
	name string
	body func(cmd string) string
}

// quickCases are the shapes of §15's spike E. The text is this driver's.
var quickCases = []quickCase{
	{"bare", func(cmd string) string { return "Spike E text.\n\n" + cmd + "\n" }},
	{"fenced", func(cmd string) string { return "Spike E text.\n\n```\n" + cmd + "\n```\n" }},
	{"indented", func(cmd string) string { return "Spike E text.\n\n    " + cmd + "\n" }},
	{"quoted", func(cmd string) string { return "Spike E text.\n\n> " + cmd + "\n" }},
	{"quoted-multiline", func(cmd string) string { return "Spike E text.\n\n>>>\n" + cmd + "\n>>>\n" }},
	{"inline", func(cmd string) string { return "Spike E text.\n\n`" + cmd + "`\n" }},
	{"html", func(cmd string) string { return "Spike E text.\n\n<div>\n" + cmd + "\n</div>\n" }},
	{"after-list-item", func(cmd string) string { return "Spike E text.\n\n- a list item\n" + cmd + "\n" }},
	{"after-list-blank", func(cmd string) string { return "Spike E text.\n\n- a list item\n\n" + cmd + "\n" }},
	{"in-paragraph", func(cmd string) string { return "Spike E text.\n" + cmd + "\n" }},
	{"crlf", func(cmd string) string { return "Spike E text.\r\n\r\n" + cmd + "\r\n" }},
	{"lone-cr", func(cmd string) string { return "Spike E text.\r\r" + cmd + "\r" }},
}

// quickSurface is one place GitLab takes a body from.
type quickSurface struct {
	name string
	// send writes body to the surface and returns the status and the
	// target's labels afterwards.
	send func(ctx context.Context, body string) (int, []string, error)
}

// spikeE asks, per body shape and per surface, whether GitLab ran the
// command, and holds each answer against internal/quickaction: a command
// GitLab ran that Find did not report is a hole in the guard. Every shape
// the guard reports is sent again escaped, which must not run, and the
// escaped text is rendered to show it reads the same. Each command is
// /label with a label of its own, so the effect is visible and harmless.
func spikeE(ctx context.Context, c spikeClient, s scratch, p *redact.Printer) error {
	id := itoa(s.ID)
	project := "projects/" + id
	// A fresh issue, so the labels read are only the spike's.
	var issue struct {
		IID int64 `json:"iid"`
	}
	r, err := c.write(ctx, http.MethodPost, project+"/issues", map[string]any{"title": "Spike E " + s.Name})
	if err != nil || r.status != http.StatusCreated || json.Unmarshal(r.body, &issue) != nil {
		return fmt.Errorf("create the spike's issue: status %d: %w", r.status, err)
	}
	issuePath := project + "/issues/" + itoa(issue.IID)
	surfaces := quickSurfaces(c, s, project, issuePath, project+"/merge_requests/"+itoa(s.MR))

	// One label per case, surface and variant, made before any is used.
	label := func(ci, si int, escaped bool) string {
		l := fmt.Sprintf("spike-e-%d-%d", ci, si)
		if escaped {
			l += "-esc"
		}
		return l
	}
	for ci := range quickCases {
		for si := range surfaces {
			for _, esc := range []bool{false, true} {
				r, err := c.write(ctx, http.MethodPost, project+"/labels", map[string]any{"name": label(ci, si, esc), "color": "#6699cc"})
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
			p.Sayf("%s, %s: status %d, GitLab ran it: %t, guard found it: %t — %s", surf.name, qc.name, status, ran, found, verdict)
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
			p.Sayf("%s, %s escaped: status %d, GitLab ran it: %t%s", surf.name, qc.name, status, escRan,
				map[bool]string{true: " — !! the escape did not hold", false: ""}[escRan])
		}
	}

	// A note that is only a command: §2.8 says 202 and nothing saved.
	only := "/label ~" + label(0, 0, false)
	r, err = c.write(ctx, http.MethodPost, issuePath+"/notes", map[string]any{"body": only})
	if err != nil {
		return err
	}
	p.Sayf("issue note that is only a command: status %d, %d body bytes", r.status, len(r.body))

	// The escaped line renders as the command's own text.
	escaped, _ := quickaction.Escape("/label ~spike-e-render\n")
	r, err = c.write(ctx, http.MethodPost, "markdown", map[string]any{"text": escaped, "gfm": true, "project": s.Path})
	if err != nil {
		return err
	}
	var md struct {
		HTML string `json:"html"`
	}
	_ = json.Unmarshal(r.body, &md)
	text := htmlTags.ReplaceAllString(md.HTML, "")
	p.Sayf("escaped line rendered: status %d, shows the command's text: %t, shows the backslash: %t",
		r.status, strings.Contains(text, "/label"), strings.Contains(text, `\/label`))

	p.Sayf("spike E: %d hole(s) in the guard, %d escape(s) that did not hold", holes, escapeHoles)
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

// quickSurfaces are the four places spike E sends a body: a note on an
// issue and on a merge request, a new issue's description, and a merge
// request description update. Each answers the target's labels, read
// back rather than taken from the write's own answer.
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
			r, err := c.write(ctx, http.MethodPost, project+"/issues", map[string]any{"title": "Spike E case " + s.Name, "description": text})
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

// ------------------------------------------------------------ spike K

// diffRow is one file of a merge request's diffs, as spike K reads it.
type diffRow struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	Diff    string `json:"diff"`
}

// spikeK asks whether the positions diffpos computes land where GitLab
// puts the same lines: a draft note answers with the line_code GitLab
// computed from the position, and a range with the codes it stored. The
// drafts are deleted afterwards, so the plan's review sees only its own.
func spikeK(ctx context.Context, c spikeClient, s scratch, p *redact.Printer) error {
	mrPath := "projects/" + itoa(s.ID) + "/merge_requests/" + itoa(s.MR)
	r, err := c.get(ctx, mrPath, nil, nil)
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
	r, err = c.get(ctx, mrPath+"/diffs", nil, nil)
	if err != nil {
		return err
	}
	var diffs []diffRow
	if r.status != http.StatusOK || json.Unmarshal(r.body, &diffs) != nil {
		return fmt.Errorf("read the diffs: status %d", r.status)
	}
	i := slices.IndexFunc(diffs, func(d diffRow) bool { return d.NewPath == s.File })
	if i < 0 {
		return fmt.Errorf("the merge request does not change %s", s.File)
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
			p.Sayf("%s: diffpos refused it: %v", tc.what, err)
			continue
		}
		r, err := c.write(ctx, http.MethodPost, mrPath+"/draft_notes", map[string]any{"note": "Spike K: " + tc.what + ".",
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
		p.Redactor().Known(redact.KindID, itoa(draft.ID))
		ok := r.status == http.StatusCreated && draft.LineCode == placed.End.Code(file)
		if tc.end != 0 {
			lr := draft.Position.LineRange
			ok = ok && lr != nil && lr.Start.LineCode == placed.Start.Code(file) && lr.End.LineCode == placed.End.Code(file)
		}
		if ok {
			agree++
		}
		p.Sayf("%s (%s %d%s): status %d, GitLab's line_code agrees with diffpos: %t", tc.what, tc.side, tc.line,
			map[bool]string{true: fmt.Sprintf("–%d", tc.end), false: ""}[tc.end != 0], r.status, ok)
		if draft.ID != 0 {
			if d, err := c.send(ctx, http.MethodDelete, mrPath+"/draft_notes/"+itoa(draft.ID), nil, nil, nil); err != nil || d.status != http.StatusNoContent {
				p.Sayf("!! could not delete the spike's draft: status %d", d.status)
			}
		}
	}
	p.Sayf("spike K: %d of %d positions agree", agree, len(cases))
	return nil
}

// ------------------------------------------------------------ protection

// protectedFlag asks whether a branch's own protected flag reflects a
// wildcard rule, which create_commit trusts for a branch that exists
// (§4.4). It makes the rule and a branch under it; the plan then commits
// to both that branch and a new one under the rule, and expects both
// refused.
func protectedFlag(ctx context.Context, c spikeClient, s scratch, p *redact.Printer) error {
	project := "projects/" + itoa(s.ID)
	r, err := c.write(ctx, http.MethodPost, project+"/protected_branches", map[string]any{"name": guardRule(s), "push_access_level": 40})
	if err != nil || r.status != http.StatusCreated {
		return fmt.Errorf("create the rule: status %d: %w", r.status, err)
	}
	r, err = c.write(ctx, http.MethodPost, project+"/repository/branches", map[string]any{"branch": guardBranch(s), "ref": s.Default})
	if err != nil || r.status != http.StatusCreated {
		return fmt.Errorf("create the branch: status %d: %w", r.status, err)
	}
	r, err = c.get(ctx, project+"/repository/branches/"+url.PathEscape(guardBranch(s)), nil, nil)
	if err != nil {
		return err
	}
	var b struct {
		Protected bool `json:"protected"`
	}
	_ = json.Unmarshal(r.body, &b)
	p.Sayf("a branch under a wildcard rule: status %d, protected: %t", r.status, b.Protected)
	return nil
}

// ------------------------------------------------------------ spike M

// spikeM asks how soon each read that settles an ambiguous create (§4.5)
// finds what the create made: it creates, then reads at once and every
// 250 ms for up to ten seconds, and prints how long the read took to see
// it.
func spikeM(ctx context.Context, c spikeClient, s scratch, p *redact.Printer) error {
	project := "projects/" + itoa(s.ID)
	since := time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339)
	me := url.Values{"author_username": {s.User}, "created_after": {since}, "state": {"all"}, "per_page": {"100"}}
	branch := "spike-m-" + s.Name[len(s.Name)-6:]
	checks := []struct {
		what   string
		create func() (int, error)
		find   func() (bool, error)
	}{
		{"an issue, by author, created_after and title", func() (int, error) {
			r, err := c.write(ctx, http.MethodPost, project+"/issues", map[string]any{"title": "Spike M issue " + s.Name})
			return r.status, err
		}, func() (bool, error) {
			return c.listed(ctx, project+"/issues", me, "title", "Spike M issue "+s.Name)
		}},
		{"a comment, among the issue's threads", func() (int, error) {
			// The second issue: a comment on the first would mark the run's
			// own to-do item on it done, which a later read looks for.
			r, err := c.write(ctx, http.MethodPost, project+"/issues/"+itoa(s.Issue2)+"/notes", map[string]any{"body": "Spike M comment " + s.Name})
			return r.status, err
		}, func() (bool, error) {
			return c.inThreads(ctx, project+"/issues/"+itoa(s.Issue2)+"/discussions", "Spike M comment "+s.Name)
		}},
		{"a draft, among the account's drafts", func() (int, error) {
			r, err := c.write(ctx, http.MethodPost, project+"/merge_requests/"+itoa(s.MR)+"/draft_notes", map[string]any{"note": "Spike M draft " + s.Name})
			return r.status, err
		}, func() (bool, error) {
			return c.listed(ctx, project+"/merge_requests/"+itoa(s.MR)+"/draft_notes", nil, "note", "Spike M draft "+s.Name)
		}},
		{"a commit, as the branch's head", func() (int, error) {
			r, err := c.write(ctx, http.MethodPost, project+"/repository/commits", map[string]any{"branch": branch, "start_branch": s.Default,
				"commit_message": "Spike M commit " + s.Name, "actions": []any{map[string]any{"action": "create", "file_path": "spike-m.txt", "content": "m\n"}}})
			return r.status, err
		}, func() (bool, error) {
			r, err := c.get(ctx, project+"/repository/branches/"+branch, nil, nil)
			if err != nil || r.status != http.StatusOK {
				return false, err
			}
			var b struct {
				Commit struct {
					Message string `json:"message"`
				} `json:"commit"`
			}
			_ = json.Unmarshal(r.body, &b)
			return strings.TrimSpace(b.Commit.Message) == "Spike M commit "+s.Name, nil
		}},
		{"a merge request, by author, created_after and title", func() (int, error) {
			r, err := c.write(ctx, http.MethodPost, project+"/merge_requests", map[string]any{"source_branch": branch,
				"target_branch": s.Default, "title": "Spike M merge request " + s.Name})
			return r.status, err
		}, func() (bool, error) {
			return c.listed(ctx, project+"/merge_requests", me, "title", "Spike M merge request "+s.Name)
		}},
		{"a pipeline, on the ref by source api, username and created_after", func() (int, error) {
			r, err := c.write(ctx, http.MethodPost, project+"/pipeline", map[string]any{"ref": branch})
			return r.status, err
		}, func() (bool, error) {
			return c.listed(ctx, project+"/pipelines", url.Values{"ref": {branch}, "source": {"api"}, "username": {s.User},
				"created_after": {since}}, "ref", branch)
		}},
		{"a release, by its tag", func() (int, error) {
			r, err := c.write(ctx, http.MethodPost, project+"/releases", map[string]any{"tag_name": branch, "ref": branch})
			return r.status, err
		}, func() (bool, error) {
			r, err := c.get(ctx, project+"/releases/"+branch, nil, nil)
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
		p.Sayf("%s: created with status %d; found by reading: %t, after %d ms", ck.what, status, found, time.Since(start).Milliseconds())
	}
	// The spike's pipeline runs nothing the plan needs; it is canceled.
	if r, err := c.get(ctx, project+"/pipelines", url.Values{"ref": {branch}, "source": {"api"}}, nil); err == nil && r.status == http.StatusOK {
		var rows []struct {
			ID int64 `json:"id"`
		}
		_ = json.Unmarshal(r.body, &rows)
		for _, pl := range rows {
			p.Redactor().Known(redact.KindID, itoa(pl.ID))
			_, _ = c.write(ctx, http.MethodPost, project+"/pipelines/"+itoa(pl.ID)+"/cancel", nil)
		}
	}
	// The draft would be published by the plan's review; it is removed.
	r, err := c.get(ctx, project+"/merge_requests/"+itoa(s.MR)+"/draft_notes", nil, nil)
	if err == nil && r.status == http.StatusOK {
		var drafts []struct {
			ID   int64  `json:"id"`
			Note string `json:"note"`
		}
		_ = json.Unmarshal(r.body, &drafts)
		for _, d := range drafts {
			if strings.HasPrefix(d.Note, "Spike M draft") {
				p.Redactor().Known(redact.KindID, itoa(d.ID))
				_, _ = c.send(ctx, http.MethodDelete, project+"/merge_requests/"+itoa(s.MR)+"/draft_notes/"+itoa(d.ID), nil, nil, nil)
			}
		}
	}
	return nil
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
