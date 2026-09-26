//go:build live

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/mmedum/gitlab-mcp/internal/app"
	netredact "github.com/mmedum/gitlab-mcp/internal/redact"
	"github.com/mmedum/gitlab-mcp/scripts/internal/redact"
)

// The spikes of docs/architecture.md §15 this phase owes, against the
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
	u := *c.root
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + path
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
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

func spikes(ctx context.Context, settings *app.Settings, s scratch, p *redact.Printer) {
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
