// Package gapi is the raw REST client for GitLab's API v4.
//
// Raw, because CLAUDE.md rule 13 forbids a client library. Every request
// goes through Client.do, which owns the rules that must hold for every
// call: the token goes only to the configured instance and no redirect
// is followed (§11); a write never runs under a dry run; every call is
// charged to the per-instance rate model; only what cannot apply twice
// is retried, and a create never is (§4.5); and every failure comes back
// as a classified *Error whose text carries no URL, path or query.
package gapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/internal/instance"
	"github.com/mmedum/gitlab-mcp/v2/internal/redact"
)

// Defaults for Options.
const (
	DefaultHeaderTimeout = 60 * time.Second
	DefaultMaxAttempts   = 4
)

// MaxResponseBytes bounds one response body (§11).
const MaxResponseBytes = 32 << 20

// maxBackoff caps one jittered wait.
const maxBackoff = 32 * time.Second

// maxRetryAfter is the longest Retry-After worth waiting for inside one
// call. A longer one is reported instead of slept through.
const maxRetryAfter = 64 * time.Second

// TokenSource supplies the bearer token. It refreshes as it needs to;
// the client asks once per call.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
	// Invalidate drops an access token GitLab refused as invalid_token,
	// so the next Token reads the store again or refreshes. A logout and
	// a login in another process revoke the token held here long before
	// its expiry says so. A source with nothing to drop does nothing.
	Invalidate(rejected string)
}

// StaticToken is a TokenSource that always hands out the same token,
// for a token that was just issued and a test. Nothing replaces it, so
// Invalidate does nothing.
type StaticToken string

// Token returns the token.
func (t StaticToken) Token(context.Context) (string, error) { return string(t), nil }

// Invalidate does nothing: there is no other token to move to.
func (StaticToken) Invalidate(string) {}

// Options configure a Client. Only Instance is required.
type Options struct {
	// Instance is the normalized GitLab the client talks to. The token
	// is sent nowhere else.
	Instance instance.Instance
	// HTTPClient carries the transport and its proxy. It is
	// copied; its redirect policy is replaced and its Timeout cleared,
	// because the client bounds each attempt itself.
	HTTPClient *http.Client
	// Tokens supplies the access token. nil answers every call [auth].
	Tokens TokenSource
	// Logger receives one debug line per attempt and drift reports.
	// Never the payload.
	Logger *slog.Logger
	// Version is this build's version, sent as "gitlab-mcp/<version>".
	Version string
	// HeaderTimeout bounds the wait for response headers per attempt.
	HeaderTimeout time.Duration
	// StallTimeout bounds each read of a response body. Zero takes
	// HeaderTimeout: a body that stops arriving is cut, a slow one is not.
	StallTimeout time.Duration
	// MaxAttempts is the most attempts a call gets, the first included.
	MaxAttempts int
	// RequestsPerMinute, NotesPerMinute and Concurrency set the rate
	// model; zero takes the defaults.
	RequestsPerMinute int
	NotesPerMinute    int
	Concurrency       int
	// Sleep waits between attempts; tests stub it.
	Sleep func(ctx context.Context, d time.Duration) error
	// Now is the clock; tests stub it.
	Now func() time.Time
}

// Client calls one GitLab instance.
type Client struct {
	inst          instance.Instance
	apiRoot       *url.URL
	webBase       *url.URL
	http          *http.Client
	tokens        TokenSource
	log           *slog.Logger
	userAgent     string
	headerTimeout time.Duration
	stallTimeout  time.Duration
	maxAttempts   int
	sleep         func(ctx context.Context, d time.Duration) error
	rate          *rateModel
	projects      *projectCache
	drift         sync.Map // "Type.path" reported once per process
	driftWalked   sync.Map // reflect.Type walked for drift once per process
}

// New builds a Client.
func New(o Options) (*Client, error) {
	if o.Instance.IsZero() {
		return nil, Errf(ClassInvalid, "no GitLab instance is configured")
	}
	c := &Client{
		inst:          o.Instance,
		apiRoot:       o.Instance.APIRoot(),
		webBase:       o.Instance.WebBase(),
		tokens:        o.Tokens,
		log:           o.Logger,
		headerTimeout: o.HeaderTimeout,
		stallTimeout:  o.StallTimeout,
		maxAttempts:   o.MaxAttempts,
		sleep:         o.Sleep,
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	c.userAgent = "gitlab-mcp"
	if o.Version != "" {
		c.userAgent += "/" + o.Version
	}
	if c.headerTimeout <= 0 {
		c.headerTimeout = DefaultHeaderTimeout
	}
	if c.stallTimeout <= 0 {
		c.stallTimeout = c.headerTimeout
	}
	if c.maxAttempts <= 0 {
		c.maxAttempts = DefaultMaxAttempts
	}
	if c.sleep == nil {
		c.sleep = sleepCtx
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	c.rate = newRateModel(o.RequestsPerMinute, o.NotesPerMinute, o.Concurrency, now, c.sleep)
	c.projects = newProjectCache(now)

	hc := &http.Client{}
	if o.HTTPClient != nil {
		copied := *o.HTTPClient
		hc = &copied
	}
	if hc.Transport == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.MaxIdleConnsPerHost = DefaultConcurrency
		hc.Transport = t
	}
	hc.Timeout = 0
	// No redirect is followed (§11, CLAUDE.md rule 10). net/http keeps
	// the Authorization header on a redirect to the same host and to a
	// subdomain, so following one could carry the token somewhere this
	// client never chose. The moved-project case is handled in do.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.http = hc
	return c, nil
}

// Instance is the instance this client talks to.
func (c *Client) Instance() instance.Instance { return c.inst }

// RateReading is the last rate-limit reading GitLab reported.
func (c *Client) RateReading() Reading { return c.rate.reading() }

// ------------------------------------------------------------ dry run

type dryRunKey struct{}

// WithDryRun returns a context under which the client refuses every
// write, before anything reaches the network.
//
// It is what dry_run is (CLAUDE.md rule 14). A tool still shapes its own
// preview, but the guarantee that a preview changes nothing is kept
// here, where every request passes, so a handler that forgets its
// preview branch fails loudly instead of writing.
func WithDryRun(ctx context.Context) context.Context {
	return context.WithValue(ctx, dryRunKey{}, true)
}

// IsDryRun reports whether ctx refuses writes.
func IsDryRun(ctx context.Context) bool {
	v, _ := ctx.Value(dryRunKey{}).(bool)
	return v
}

// ErrDryRunWrite is the cause of a write refused under WithDryRun.
// Reaching it is a bug in this server, never something a caller did.
var ErrDryRunWrite = errors.New("gapi: a write was attempted under a dry run")

// ------------------------------------------------------------ per call

type callKey struct{}

// callState is what one tool call accumulates across its requests.
type callState struct {
	requests atomic.Int64
	mu       sync.Mutex
	projects map[string]Project
	moves    []Move
}

// Move is a project GitLab reported as renamed or transferred: a GET to
// From answered with a redirect to To, which the client followed once.
type Move struct {
	From, To string
}

// WithCall returns a context that counts the requests made under it,
// remembers project paths resolved to ids, and records moved projects.
// The server installs one per tool call, so the log line reports what
// GitLab was actually asked, retries included, and two calls in flight
// do not share state.
func WithCall(ctx context.Context) context.Context {
	return context.WithValue(ctx, callKey{}, &callState{projects: map[string]Project{}})
}

func stateOf(ctx context.Context) *callState {
	s, _ := ctx.Value(callKey{}).(*callState)
	return s
}

// Requests is how many HTTP requests were made under ctx.
func Requests(ctx context.Context) int {
	if s := stateOf(ctx); s != nil {
		return int(s.requests.Load())
	}
	return 0
}

// Moves lists the moved projects met under ctx, for the result to report.
func Moves(ctx context.Context) []Move {
	s := stateOf(ctx)
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Move(nil), s.moves...)
}

// ------------------------------------------------------------ per process

// projectTTL is how long the process trusts a project's id and path. A
// rename GitLab reports forgets them sooner; one it does not report is
// believed for at most this long.
const projectTTL = 5 * time.Minute

// maxProjects bounds the cache. A server rarely touches more projects
// than this in a few minutes, and when it does it starts over.
const maxProjects = 256

// projectCache remembers project ids and paths across calls, so a path
// a person repeats is not read again on every call.
type projectCache struct {
	now   func() time.Time
	mu    sync.Mutex
	paths map[string]projectEntry // lowercased full path
	ids   map[int64]projectEntry
}

type projectEntry struct {
	p       Project
	expires time.Time
}

func newProjectCache(now func() time.Time) *projectCache {
	return &projectCache{now: now, paths: map[string]projectEntry{}, ids: map[int64]projectEntry{}}
}

// remember records a project carrying both its id and its path.
func (pc *projectCache) remember(p Project) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if len(pc.ids) >= maxProjects {
		clear(pc.paths)
		clear(pc.ids)
	}
	e := projectEntry{p: p, expires: pc.now().Add(projectTTL)}
	pc.paths[strings.ToLower(p.Path())] = e
	pc.ids[p.ID()] = e
}

func (pc *projectCache) byPath(lower string) (Project, bool) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return pc.live(pc.paths[lower])
}

func (pc *projectCache) byID(id int64) (Project, bool) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return pc.live(pc.ids[id])
}

func (pc *projectCache) live(e projectEntry) (Project, bool) {
	if e.p.IsZero() || !pc.now().Before(e.expires) {
		return Project{}, false
	}
	return e.p, true
}

// forget drops everything: a project moved, and which paths it made
// stale is not worth working out.
func (pc *projectCache) forget() {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	clear(pc.paths)
	clear(pc.ids)
}

// ------------------------------------------------------------ the call

// Do sends one call and decodes a JSON response into out, which may be
// nil.
func (c *Client) Do(ctx context.Context, call Call, out any) error {
	_, err := c.do(ctx, call, out)
	return err
}

// response is what a successful call left besides the decoded body.
type response struct {
	status  int
	header  http.Header
	request *url.URL // the URL finally answered, after a moved project
}

// prepared is a call checked and built, ready to send.
type prepared struct {
	call       Call
	name       string
	endpoint   *url.URL
	payload    []byte
	token      string
	repeatable bool
}

// do sends a call under the retry policy and decodes the answer.
func (c *Client) do(ctx context.Context, call Call, out any) (*response, error) {
	p, err := c.prepare(ctx, call)
	if err != nil {
		return nil, err
	}
	return c.send(ctx, p, out)
}

// prepare checks a call and builds its request. Everything it refuses is
// refused before anything reaches the network.
func (c *Client) prepare(ctx context.Context, call Call) (*prepared, error) {
	switch call.Method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete:
	default:
		return nil, Errf(ClassUnexpected, "unsupported method %q", call.Method)
	}
	p := &prepared{call: call, name: call.Name}
	if p.name == "" {
		p.name = "this request"
	}
	path, err := fillPath(call.Path, call.Args)
	if err != nil {
		return nil, err
	}
	if isWrite(call) && IsDryRun(ctx) {
		return nil, Wrap(ClassBlocked, ErrDryRunWrite,
			"this was a dry run and %s would have written to GitLab, so it was refused before it was sent", p.name)
	}
	if p.endpoint, err = c.endpoint(call.Root, path, call.Query); err != nil {
		return nil, err
	}
	if call.Body != nil {
		if p.payload, err = json.Marshal(call.Body); err != nil {
			return nil, Wrap(ClassInvalid, err, "the request for %s could not be encoded", p.name)
		}
	}
	if p.token, err = c.token(ctx); err != nil {
		return nil, err
	}
	// A POST fails closed: it repeats after a failure that may have
	// followed the write only when its call site says why that is safe.
	p.repeatable = call.Method != http.MethodPost || call.Repeatable != "" || call.ReadOnly != ""
	return p, nil
}

// send makes the attempts.
func (c *Client) send(ctx context.Context, p *prepared, out any) (*response, error) {
	redirected, reauthorized := false, false
	var last verdict
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		if attempt > 1 {
			wait := backoff(attempt-1, last.after)
			if dl, ok := ctx.Deadline(); ok && time.Now().Add(wait).After(dl) {
				return nil, last.err
			}
			if err := c.sleep(ctx, wait); err != nil {
				return nil, last.err
			}
		}
		release, err := c.rate.acquire(ctx, p.call.Bucket)
		if err != nil {
			return nil, err
		}
		if s := stateOf(ctx); s != nil {
			s.requests.Add(1)
		}
		start := time.Now()
		res, sendErr := c.attempt(ctx, p.call.Method, p.endpoint, p.payload, p.token, p.call.UnmodifiedSince)
		release()
		status := 0
		if res != nil {
			status = res.status
			c.rate.observe(res.header)
		}
		logAttempt := func(outcome string) {
			c.log.Debug("gitlab_request", "call", p.name, "attempt", attempt, "status", status,
				"ms", time.Since(start).Milliseconds(), "bucket", p.call.Bucket.String(), "outcome", outcome)
		}

		if sendErr == nil && isRedirect(status) {
			logAttempt("redirect")
			if err := c.followMove(ctx, p, res, &redirected); err != nil {
				return nil, err
			}
			attempt-- // the redirect was an answer, not a failure
			continue
		}

		v := c.decide(ctx, p.call, p.name, p.repeatable, res, sendErr)
		logAttempt(v.outcome())
		if v.reauth && !reauthorized {
			reauthorized = true
			next, ok, err := c.reauthorize(ctx, p)
			if err != nil {
				return nil, err
			}
			if ok {
				p.token = next
				attempt-- // a refused token is not a failure of the call
				continue
			}
		}
		if v.err == nil && p.call.Bucket == BucketNotes && res.status == http.StatusAccepted {
			// GitLab ran the body as quick actions and saved nothing. The
			// guard exists so that never happens, so reaching it is a
			// defect (§4.2), whichever route carried the note.
			c.log.Error("gitlab ran quick actions from a comment and saved nothing; the quick-action guard missed them", "call", p.name)
			return nil, &Error{Class: ClassUnexpected, Status: res.status, Message: "GitLab ran the comment as quick actions and saved no " +
				"comment, which the quick-action guard exists to prevent. This is a defect in this server; please report it"}
		}
		if v.err == nil {
			if err := c.decode(ctx, res, p.name, out); err != nil {
				return nil, err
			}
			return &response{status: res.status, header: res.header, request: p.endpoint}, nil
		}
		if v.throttle {
			// The instance throttle holds every call, not only this one.
			c.rate.pause(v.after)
		}
		if !v.retry || attempt == c.maxAttempts {
			return nil, v.err
		}
		last = v
	}
	return nil, last.err
}

// reauthorize drops a token GitLab refused and, for a call that may
// repeat, returns a different one to try once more. A 401 means GitLab
// did nothing, but a create still is not sent twice (§4.5): the next
// call gets the new token.
func (c *Client) reauthorize(ctx context.Context, p *prepared) (string, bool, error) {
	c.tokens.Invalidate(p.token)
	if !p.repeatable {
		return "", false, nil
	}
	next, err := c.token(ctx)
	if err != nil {
		return "", false, err
	}
	return next, next != p.token, nil
}

// followMove handles a redirect. A moved project answers a GET with a
// redirect to its new path; that is re-requested once, and only when the
// new address is on this instance under the API root. Any other
// redirect is an error and is never followed.
func (c *Client) followMove(ctx context.Context, p *prepared, res *attemptResult, redirected *bool) error {
	next, ok := c.movedTo(p.call, p.endpoint, res.header.Get("Location"))
	if !ok || *redirected {
		return &Error{Class: ClassUnexpected, Status: res.status, Message: "GitLab redirected " + p.name +
			" to an address outside the instance's API root, and redirects are never followed because they would carry the token." +
			" If every call does this, check that the instance URL is the one GitLab serves: scheme, host and sub-path"}
	}
	*redirected = true
	c.recordMove(ctx, p.endpoint, next)
	c.projects.forget()
	p.endpoint = next
	return nil
}

// endpoint builds the absolute URL of a call. The path was escaped by
// fillPath; it is set as RawPath so it is sent exactly as escaped.
func (c *Client) endpoint(root Root, path string, q url.Values) (*url.URL, error) {
	base := c.apiRoot
	if root == RootWeb {
		base = c.webBase
	}
	u := *base
	unescaped, err := url.PathUnescape(path)
	if err != nil {
		return nil, Errf(ClassUnexpected, "a request path could not be built")
	}
	u.Path = strings.TrimSuffix(base.Path, "/") + "/" + unescaped
	u.RawPath = strings.TrimSuffix(base.EscapedPath(), "/") + "/" + path
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}
	return &u, nil
}

// token resolves the access token once per call.
func (c *Client) token(ctx context.Context) (string, error) {
	if c.tokens == nil {
		return "", Errf(ClassAuth, "not signed in: run `gitlab-mcp login`")
	}
	tok, err := c.tokens.Token(ctx)
	if err != nil {
		var e *Error
		if errors.As(err, &e) {
			return "", e
		}
		return "", Wrap(ClassAuth, err,
			"the stored sign-in could not be used: run `gitlab-mcp login`, then `gitlab-mcp doctor` if it persists")
	}
	if tok == "" {
		return "", Errf(ClassAuth, "not signed in: run `gitlab-mcp login`")
	}
	return tok, nil
}

// attemptResult is one answered request.
type attemptResult struct {
	status int
	header http.Header
	body   []byte
}

// TooLarge reports whether err is a response refused for being larger
// than MaxResponseBytes.
func TooLarge(err error) bool { return errors.Is(err, errTooLarge) }

var (
	errTooLarge      = fmt.Errorf("the response is larger than %d bytes", MaxResponseBytes)
	errHeaderTimeout = errors.New("no response headers within the timeout")
	errStalled       = errors.New("the response stopped arriving")
	errOffInstance   = errors.New("refusing to send the token outside the configured instance")
)

// attempt makes one HTTP request. The address is checked against the
// instance before the token is attached.
func (c *Client) attempt(ctx context.Context, method string, endpoint *url.URL, payload []byte, token string, unmodifiedSince time.Time) (*attemptResult, error) {
	if !c.inst.SameOrigin(endpoint) {
		return nil, errOffInstance
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var headersLate atomic.Bool
	headers := time.AfterFunc(c.headerTimeout, func() { headersLate.Store(true); cancel() })

	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
	if err != nil {
		headers.Stop()
		return nil, WithoutURL(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if !unmodifiedSince.IsZero() {
		// RFC 3339 with the fraction, not an HTTP-date: GitLab reads the
		// header with Ruby's Time.parse and compares it with a time kept to
		// the microsecond, so a date cut to the second would refuse nearly
		// every delete (check_unmodified_since! in lib/api/helpers.rb).
		req.Header.Set("If-Unmodified-Since", unmodifiedSince.UTC().Format(time.RFC3339Nano))
	}
	resp, err := c.http.Do(req)
	headers.Stop()
	if err != nil {
		if headersLate.Load() {
			return nil, errHeaderTimeout
		}
		return nil, WithoutURL(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var stalled atomic.Bool
	guard := newStallGuard(resp.Body, c.stallTimeout, func() { stalled.Store(true); cancel() })
	defer guard.stop()
	body, err := io.ReadAll(io.LimitReader(guard, MaxResponseBytes+1))
	res := &attemptResult{status: resp.StatusCode, header: resp.Header}
	if err != nil {
		if stalled.Load() {
			return res, errStalled
		}
		return res, WithoutURL(err)
	}
	if len(body) > MaxResponseBytes {
		return res, errTooLarge
	}
	res.body = body
	return res, nil
}

// stallGuard cancels a body whose next Read makes no progress within
// the limit. The clock runs only while a Read is outstanding.
type stallGuard struct {
	r     io.Reader
	timer *time.Timer
	limit time.Duration
}

func newStallGuard(r io.Reader, limit time.Duration, fire func()) *stallGuard {
	t := time.AfterFunc(limit, fire)
	t.Stop()
	return &stallGuard{r: r, timer: t, limit: limit}
}

func (g *stallGuard) Read(p []byte) (int, error) {
	g.timer.Reset(g.limit)
	n, err := g.r.Read(p)
	g.timer.Stop()
	return n, err
}

func (g *stallGuard) stop() { g.timer.Stop() }

func isRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// movedTo decides whether a redirect is a moved project the client may
// re-request: a GET, under /projects/, to an address on this instance
// under the API root. Anything else is refused by the caller.
//
// Only the new project is taken from the Location. The request is sent
// again as it was, with the same resource under the project and the
// same query: a Location naming only the new path would drop the ref
// and the filters, and one naming another resource is not a move.
func (c *Client) movedTo(call Call, from *url.URL, location string) (*url.URL, bool) {
	if call.Method != http.MethodGet || call.Root != RootAPI || location == "" {
		return nil, false
	}
	loc, err := from.Parse(location)
	if err != nil || !c.inst.UnderAPIRoot(loc) {
		return nil, false
	}
	project := projectSegment(c.apiRoot, loc)
	prefix := c.projectsPrefix()
	rest, ok := strings.CutPrefix(from.EscapedPath(), prefix)
	if !ok || project == "" || projectSegment(c.apiRoot, from) == "" {
		return nil, false
	}
	suffix := ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		suffix = rest[i:]
	}
	next := *from
	next.RawPath = prefix + url.PathEscape(project) + suffix
	if next.Path, err = url.PathUnescape(next.RawPath); err != nil {
		return nil, false
	}
	return &next, true
}

func (c *Client) projectsPrefix() string {
	return strings.TrimSuffix(c.apiRoot.EscapedPath(), "/") + "/projects/"
}

// projectSegment is the decoded project id or path of a URL under
// <api root>/projects/, or "".
func projectSegment(apiRoot, u *url.URL) string {
	prefix := strings.TrimSuffix(apiRoot.EscapedPath(), "/") + "/projects/"
	rest, ok := strings.CutPrefix(u.EscapedPath(), prefix)
	if !ok {
		return ""
	}
	seg, _, _ := strings.Cut(rest, "/")
	dec, err := url.PathUnescape(seg)
	if err != nil {
		return ""
	}
	return dec
}

func (c *Client) recordMove(ctx context.Context, from, to *url.URL) {
	s := stateOf(ctx)
	if s == nil {
		return
	}
	m := Move{From: projectSegment(c.apiRoot, from), To: projectSegment(c.apiRoot, to)}
	s.mu.Lock()
	s.moves = append(s.moves, m)
	s.mu.Unlock()
}

// ------------------------------------------------------------ decoding

// decode unmarshals a successful body into out and reports fields out
// does not model. An empty body decodes to nothing: a write that
// answers 204 landed, and telling the caller otherwise invites a retry.
func (c *Client) decode(ctx context.Context, res *attemptResult, name string, out any) error {
	// A *[]byte takes the body as it is: a job log is text, not JSON.
	if raw, ok := out.(*[]byte); ok {
		*raw = res.body
		return nil
	}
	if out == nil || len(bytes.TrimSpace(res.body)) == 0 {
		return nil
	}
	if !looksJSON(res.header.Get("Content-Type"), res.body) {
		return &Error{Class: ClassUnexpected, Status: res.status,
			Message: "GitLab's answer to " + name + " was not JSON: " + describeBody(res.status, res.header, res.body)}
	}
	var err error
	if c.walkDrift(ctx, out) {
		err = decodeReporting(res.body, out, func(path string) {
			if _, seen := c.drift.LoadOrStore(path, true); !seen {
				c.log.Debug("gitlab_drift", "field", path)
			}
		})
	} else {
		err = json.Unmarshal(res.body, out)
	}
	if err != nil {
		return &Error{Class: ClassUnexpected, Status: res.status, err: err,
			Message: "GitLab's answer to " + name + " did not have the shape this server expects: " + stripURL(err.Error())}
	}
	return nil
}

func looksJSON(contentType string, body []byte) bool {
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "json") {
		return true
	}
	t := bytes.TrimSpace(body)
	return ct == "" && len(t) > 0 && (t[0] == '{' || t[0] == '[')
}

// describeBody names a body that is not what was expected by status,
// content type and a short printable prefix, so a proxy's login page is
// recognizable without being pasted whole.
func describeBody(status int, h http.Header, body []byte) string {
	ct := h.Get("Content-Type")
	if ct == "" {
		ct = "no content type"
	}
	prefix := body
	if len(prefix) > 64 {
		prefix = prefix[:64]
	}
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '�' {
			return ' '
		}
		return r
	}, string(prefix))
	clean = strings.Join(strings.Fields(clean), " ")
	return fmt.Sprintf("status %d, %s, starting %q", status, ct, clean)
}

// walkDrift reports whether this answer is walked for drift: only when
// the report would be logged, and once per Go type per process. The walk
// decodes the body a second time, which no call should pay for when
// nobody reads the report.
func (c *Client) walkDrift(ctx context.Context, out any) bool {
	if !c.log.Enabled(ctx, slog.LevelDebug) {
		return false
	}
	_, walked := c.driftWalked.LoadOrStore(reflect.TypeOf(out), true)
	return !walked
}

// ------------------------------------------------------------ outcomes

// verdict is what one attempt came to.
type verdict struct {
	err   *Error
	after time.Duration // Retry-After, a floor on the next backoff
	retry bool
	// throttle marks a 429 from the instance-wide throttle, as opposed to
	// one endpoint's application limit.
	throttle bool
	// reauth marks a 401 that refuses the token itself, which a new token
	// may clear.
	reauth bool
}

func (v verdict) outcome() string {
	if v.err == nil {
		return "ok"
	}
	return string(v.err.Class)
}

func (c *Client) decide(ctx context.Context, call Call, name string, repeatable bool, res *attemptResult, sendErr error) verdict {
	if sendErr != nil {
		return classifyTransport(ctx, name, repeatable, sendErr)
	}
	if res.status >= 200 && res.status < 300 {
		return verdict{}
	}
	return classifyStatus(call, name, repeatable, res.status, res.header, res.body)
}

// classifyTransport classifies a request that got no complete answer.
func classifyTransport(ctx context.Context, name string, repeatable bool, err error) verdict {
	switch {
	case errors.Is(err, errOffInstance):
		return verdict{err: Wrap(ClassBlocked, err, "%s was refused: its address is outside the configured instance", name)}
	case errors.Is(err, errTooLarge) && repeatable:
		// A create's answer too large to read may still have landed, so
		// it falls through to ambiguous below (§4.5).
		return verdict{err: Wrap(ClassUnavailable, err, "GitLab's answer to %s was larger than %d MiB and was not read", name, MaxResponseBytes>>20)}
	case redact.IsCertificateError(err):
		return verdict{err: Wrap(ClassUnavailable, err,
			"the instance's TLS certificate was not trusted for %s: %s. A proxy that inspects TLS needs its authority in this machine's trust store", name, err)}
	case ctx.Err() != nil && (repeatable || neverSent(err)):
		return verdict{err: Wrap(ClassUnavailable, err, "%s was canceled or ran out of time", name)}
	case neverSent(err):
		// The connection was never made, so nothing reached GitLab.
		return verdict{err: Wrap(ClassUnavailable, err, "could not reach GitLab for %s: %s", name, err), retry: true}
	case !repeatable:
		// Canceled or not, a create that may have been written may have
		// landed (§4.5).
		return verdict{err: Wrap(ClassAmbiguousOutcome, err, ambiguousText+" (%s)", name, err)}
	default:
		return verdict{err: Wrap(ClassUnavailable, err, "GitLab did not answer %s: %s", name, err), retry: true}
	}
}

const ambiguousText = "GitLab did not confirm whether %s took effect, and it was not repeated: read before doing anything again"

// neverSent reports a failure before any byte of the request could have
// reached GitLab: a dial or a name lookup that failed.
func neverSent(err error) bool {
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	var dns *net.DNSError
	return errors.As(err, &dns)
}

// ------------------------------------------------------------ waiting

// backoff is full jitter over min(2^n s, 32 s), with a Retry-After
// honored as a floor: GitLab's figure is a minimum, not a target to
// jitter below.
func backoff(retry int, retryAfter time.Duration) time.Duration {
	ceiling := min(time.Second<<min(retry, 6), maxBackoff)
	d := time.Duration(rand.Int64N(int64(ceiling) + 1)) //nolint:gosec // jitter, not a secret
	return max(d, retryAfter)
}

func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil && secs > 0 {
		return time.Duration(secs * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// ------------------------------------------------------------ URLs

// WithoutURL strips the request URL, and the host names and addresses,
// from a transport error. net/http wraps every transport failure in a
// *url.Error whose text renders the whole URL, and a path or a search
// travels in it (§9.2); net and crypto/x509 name hosts. The result keeps
// the cause for errors.As and prints it with redact.NetError. nil stays
// nil.
func WithoutURL(err error) error {
	if err == nil {
		return nil
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return &strippedError{op: ue.Op, err: ue.Err}
	}
	return &strippedError{err: err}
}

// strippedError is a transport failure without its URL or its hosts.
type strippedError struct {
	op  string
	err error
}

func (e *strippedError) Error() string {
	if e.op == "" {
		return redact.NetError(e.err)
	}
	return e.op + ": " + redact.NetError(e.err)
}

func (e *strippedError) Unwrap() error { return e.err }

// stripURL cuts anything URL-shaped out of free text.
func stripURL(s string) string {
	for {
		i := strings.Index(s, "http://")
		if j := strings.Index(s, "https://"); j >= 0 && (i < 0 || j < i) {
			i = j
		}
		if i < 0 {
			return s
		}
		j := strings.IndexAny(s[i:], " \"'")
		if j < 0 {
			return strings.TrimSpace(s[:i]) + " <url>"
		}
		s = s[:i] + "<url>" + s[i+j:]
	}
}
