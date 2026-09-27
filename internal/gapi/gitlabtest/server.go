// Package gitlabtest is an in-memory GitLab behind httptest, for tests.
//
// It serves the REST paths the client uses and models the platform facts
// of docs/architecture.md §2 the server depends on: 404 for a private
// project, the catch-all route shape, offset and keyset pagination with
// their edges, 429 in both body shapes, a moved project, OAuth with
// immediately rotating refresh tokens, and quick actions run from a note.
// Everything in it is generated (§9.1).
package gitlabtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/gitlab"
)

// Options shape the instance. Zero values take the defaults.
type Options struct {
	// TotalLimit is the count past which X-Total and X-Total-Pages are
	// dropped. GitLab's is 10,000; a test sets a small one.
	TotalLimit int
	// OffsetCap is the offset past which an endpoint with keyset
	// pagination answers 405. GitLab's is 50,000.
	OffsetCap int
	// ExtraProjects adds public projects example-group/bulk-0001… for
	// paging tests.
	ExtraProjects int
	// AlphaIssues, AlphaMergeRequests and AlphaCommits size ProjectAlpha.
	// Defaults 25, 3 and 30.
	AlphaIssues        int
	AlphaMergeRequests int
	AlphaCommits       int
	// Version and Enterprise are what /metadata reports. Default
	// "19.4.0", Community Edition.
	Version    string
	Enterprise bool

	// Confidential makes the OAuth application confidential: the token
	// endpoint then refuses a request without ClientSecret.
	Confidential bool
	ClientSecret string
	// RedirectURIs are the application's registered redirect URIs.
	// Default http://127.0.0.1/callback.
	RedirectURIs []string
	// AuthorizeAs is the user an authorization signs in as. Default alice.
	AuthorizeAs string
	// AccessTokenTTL is expires_in. Default 7,200 s.
	AccessTokenTTL time.Duration
	// Now is the clock. Default time.Now.
	Now func() time.Time
	// MoveDropsQuery makes the redirect for a moved project carry the
	// new path without the request's query string.
	MoveDropsQuery bool
}

// Server is the in-memory instance. URL is its base, and the instance to
// configure is that URL.
type Server struct {
	*httptest.Server
	opts Options

	mu sync.Mutex
	// inputsSent are the input values the last job retry or play sent.
	inputsSent    map[string]any
	groups        []*group
	projects      []*project
	moved         map[string]string
	nextProjectID int64
	nextIssueID   int64
	nextMRID      int64
	nextNoteID    int64
	nextDraftID   int64
	nextCommit    int64

	// reviewerStates is the reviewer state each user last submitted with
	// a review, by project, merge request and user.
	reviewerStates map[string]string

	// Planning state outside any one project: group milestones and
	// labels, group members' access levels, and each user's to-do items.
	groupMilestones map[int64][]gitlab.ProjectMilestone
	groupLabels     map[int64][]gitlab.Label
	groupLevels     map[int64]map[string]int
	todos           []todo
	snippets        []*snippet

	faults   []*Fault
	requests []Request
	oauth    oauthState
}

// ClientID is the OAuth application's id.
const ClientID = "gitlabtest-application-id"

// New starts an instance and stops it when the test ends.
func New(t testing.TB, opts Options) *Server {
	t.Helper()
	if opts.TotalLimit <= 0 {
		opts.TotalLimit = 10000
	}
	if opts.OffsetCap <= 0 {
		opts.OffsetCap = 50000
	}
	if opts.AlphaIssues <= 0 {
		opts.AlphaIssues = 25
	}
	if opts.AlphaMergeRequests <= 0 {
		opts.AlphaMergeRequests = 3
	}
	if opts.AlphaCommits <= 0 {
		opts.AlphaCommits = 30
	}
	if opts.Version == "" {
		opts.Version = "19.4.0"
	}
	if len(opts.RedirectURIs) == 0 {
		opts.RedirectURIs = []string{"http://127.0.0.1/callback"}
	}
	if opts.AuthorizeAs == "" {
		opts.AuthorizeAs = DefaultUser
	}
	if opts.AccessTokenTTL <= 0 {
		opts.AccessTokenTTL = 7200 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	s := &Server{opts: opts}
	s.oauth.init()
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	s.mu.Lock()
	s.generate()
	s.mu.Unlock()
	return s
}

// Token issues an access token for alice with the api scope.
func (s *Server) Token() string { return s.TokenFor(DefaultUser, "api") }

// TokenFor issues an access token for a user with the given scopes.
func (s *Server) TokenFor(user string, scopes ...string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.oauth.issue(user, scopes, s.opts.Now(), s.opts.AccessTokenTTL, false).access
}

// Revoke revokes an access or refresh token.
func (s *Server) Revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.oauth.revoke(token)
}

// ------------------------------------------------------------ faults

// Fault is an injected answer. It replaces the next Times requests whose
// method (empty for any) and escaped path under /api/v4 (prefix match,
// "/projects" matches "/projects/2001/issues") match.
type Fault struct {
	Method string
	Path   string
	Times  int
	Status int
	Header http.Header
	Body   string
	// Delay holds the answer before its headers are written.
	Delay time.Duration
	// StallAfter writes the headers and part of Body, then waits this
	// long before the rest.
	StallAfter time.Duration
	// AfterApply serves the request first, so a write lands, then
	// discards that answer and sends the fault's: the answer to a write
	// that succeeded was lost.
	AfterApply bool
}

// Inject adds a fault.
func (s *Server) Inject(f Fault) {
	if f.Times <= 0 {
		f.Times = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = append(s.faults, &f)
}

// RackAttack429 is the instance throttle's 429: a plain-text body with
// Retry-After and RateLimit-* headers.
func RackAttack429(path string, retryAfter, times int) Fault {
	h := http.Header{}
	h.Set("Content-Type", "text/plain")
	h.Set("Retry-After", strconv.Itoa(retryAfter))
	h.Set("RateLimit-Limit", "2000")
	h.Set("RateLimit-Remaining", "0")
	return Fault{Path: path, Times: times, Status: http.StatusTooManyRequests, Header: h, Body: "Retry later\n"}
}

// Application429 is an application limit's 429: JSON, with Retry-After
// and no RateLimit-* headers.
func Application429(path string, retryAfter, times int) Fault {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Retry-After", strconv.Itoa(retryAfter))
	return Fault{Path: path, Times: times, Status: http.StatusTooManyRequests, Header: h,
		Body: `{"message":{"error":"This endpoint has been requested too many times. Try again later."}}`}
}

func (s *Server) takeFault(method, apiPath string) *Fault {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, f := range s.faults {
		if (f.Method == "" || f.Method == method) && strings.HasPrefix(apiPath, f.Path) {
			f.Times--
			if f.Times <= 0 {
				s.faults = append(s.faults[:i], s.faults[i+1:]...)
			}
			copied := *f
			return &copied
		}
	}
	return nil
}

func writeFault(w http.ResponseWriter, f *Fault) {
	if f.Delay > 0 {
		time.Sleep(f.Delay)
	}
	for k, vs := range f.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(f.Status)
	if f.StallAfter > 0 {
		half := len(f.Body) / 2
		_, _ = w.Write([]byte(f.Body[:half]))
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		time.Sleep(f.StallAfter)
		_, _ = w.Write([]byte(f.Body[half:]))
		return
	}
	_, _ = w.Write([]byte(f.Body))
}

// ------------------------------------------------------------ recording

// Request is one request the instance received.
type Request struct {
	Method        string
	EscapedPath   string
	RawQuery      string
	Authorization string
	UserAgent     string
	// IfUnmodifiedSince is the header a conditional delete sends.
	IfUnmodifiedSince string
}

// Requests returns what the instance received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// ResetRequests forgets the recorded requests.
func (s *Server) ResetRequests() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = nil
}

// ------------------------------------------------------------ routing

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: r.Method, EscapedPath: r.URL.EscapedPath(), RawQuery: r.URL.RawQuery,
		Authorization: r.Header.Get("Authorization"), UserAgent: r.Header.Get("User-Agent"),
		IfUnmodifiedSince: r.Header.Get("If-Unmodified-Since")})
	s.mu.Unlock()

	path := r.URL.EscapedPath()
	switch {
	case strings.HasPrefix(path, "/oauth/"):
		s.serveOAuth(w, r)
	case strings.HasPrefix(path, "/api/v4/"):
		rest := strings.TrimPrefix(path, "/api/v4")
		if f := s.takeFault(r.Method, rest); f != nil {
			if f.AfterApply {
				s.serveAPI(httptest.NewRecorder(), r, rest)
			}
			writeFault(w, f)
			return
		}
		s.serveAPI(w, r, rest)
	default:
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<html><body>Page Not Found</body></html>"))
	}
}

// segments splits an escaped path and unescapes each segment, so a
// %2F inside one stays inside it, as GitLab's router sees it.
func segments(escaped string) ([]string, bool) {
	raw := strings.Split(strings.Trim(escaped, "/"), "/")
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		seg, err := url.PathUnescape(r)
		if err != nil {
			return nil, false
		}
		out = append(out, seg)
	}
	return out, true
}

// ------------------------------------------------------------ answers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func message(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"message": msg})
}

// routeNotFound is GitLab's catch-all for a path no route matches.
func routeNotFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]any{"error": "404 Not Found"})
}

// withExtra marshals v and adds fields the client does not model, as
// GitLab's own answers carry many.
func withExtra(v any, extra map[string]any) map[string]any {
	raw, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	for k, x := range extra {
		m[k] = x
	}
	return m
}

// ------------------------------------------------------------ paging

// offsetPage writes offset pagination headers for n rows and returns the
// slice bounds. keysetCapable endpoints refuse an offset past OffsetCap
// with 405, as GitLab does past 50,000.
func (s *Server) offsetPage(w http.ResponseWriter, r *http.Request, n int, keysetCapable bool) (int, int, bool) {
	q := r.URL.Query()
	page, perPage := atoiDefault(q.Get("page"), 1), atoiDefault(q.Get("per_page"), 20)
	perPage = min(max(perPage, 1), 100)
	page = max(page, 1)
	offset := (page - 1) * perPage
	if keysetCapable && offset >= s.opts.OffsetCap {
		message(w, http.StatusMethodNotAllowed, fmt.Sprintf(
			"Offset pagination has a maximum allowed offset of %d for requests that return objects of type Project. "+
				"Remaining records can be retrieved using keyset pagination.", s.opts.OffsetCap))
		return 0, 0, false
	}
	start := min(offset, n)
	end := min(start+perPage, n)
	h := w.Header()
	h.Set("X-Page", strconv.Itoa(page))
	h.Set("X-Per-Page", strconv.Itoa(perPage))
	var links []string
	if end < n {
		h.Set("X-Next-Page", strconv.Itoa(page+1))
		links = append(links, fmt.Sprintf(`<%s>; rel="next"`, s.link(r, map[string]string{"page": strconv.Itoa(page + 1)})))
	} else {
		h.Set("X-Next-Page", "")
	}
	if page > 1 {
		h.Set("X-Prev-Page", strconv.Itoa(page-1))
		links = append(links, fmt.Sprintf(`<%s>; rel="prev"`, s.link(r, map[string]string{"page": strconv.Itoa(page - 1)})))
	}
	links = append(links, fmt.Sprintf(`<%s>; rel="first"`, s.link(r, map[string]string{"page": "1"})))
	// Past the limit GitLab stops counting, so there is no total and no
	// last page.
	if n <= s.opts.TotalLimit {
		pages := max((n+perPage-1)/perPage, 1)
		h.Set("X-Total", strconv.Itoa(n))
		h.Set("X-Total-Pages", strconv.Itoa(pages))
		links = append(links, fmt.Sprintf(`<%s>; rel="last"`, s.link(r, map[string]string{"page": strconv.Itoa(pages)})))
	}
	h.Set("Link", strings.Join(links, ", "))
	return start, end, true
}

// keysetLink writes a Link rel="next" carrying the given cursor.
func (s *Server) keysetLink(w http.ResponseWriter, r *http.Request, cursor map[string]string) {
	w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, s.link(r, cursor)))
}

// link is the request's own URL with some query parameters replaced.
func (s *Server) link(r *http.Request, set map[string]string) string {
	q := r.URL.Query()
	for k, v := range set {
		q.Set(k, v)
	}
	return s.URL + r.URL.EscapedPath() + "?" + q.Encode()
}

func perPageOf(r *http.Request) int {
	return min(max(atoiDefault(r.URL.Query().Get("per_page"), 20), 1), 100)
}

func atoiDefault(s string, d int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return d
}
