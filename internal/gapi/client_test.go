package gapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/gapi/gitlabtest"
	"github.com/mmedum/gitlab-mcp/internal/instance"
)

// sleeps records the waits between attempts without waiting.
type sleeps struct {
	mu sync.Mutex
	d  []time.Duration
}

func (s *sleeps) sleep(_ context.Context, d time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d = append(s.d, d)
	return nil
}

func (s *sleeps) all() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.d...)
}

func mustInstance(t *testing.T, raw string) instance.Instance {
	t.Helper()
	inst, err := instance.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return inst
}

type fixture struct {
	token  string
	srv    *gitlabtest.Server
	client *Client
	sleeps *sleeps
	logs   *bytes.Buffer
}

func newFixture(t *testing.T, opts gitlabtest.Options, mod ...func(*Options)) *fixture {
	t.Helper()
	srv := gitlabtest.New(t, opts)
	f := &fixture{srv: srv, token: srv.Token(), sleeps: &sleeps{}, logs: &bytes.Buffer{}}
	o := Options{
		Instance: mustInstance(t, srv.URL),
		Tokens:   StaticToken(f.token),
		Version:  "1.2.3",
		Sleep:    f.sleeps.sleep,
		Logger:   slog.New(slog.NewTextHandler(&lockedWriter{w: f.logs}, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	for _, m := range mod {
		m(&o)
	}
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	f.client = c
	return f
}

type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func wantClass(t *testing.T, err error, want Class) *Error {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want [%s]", want)
	}
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v (%T), want a *Error of class %s", err, err, want)
	}
	if e.Class != want {
		t.Fatalf("class = %s, want %s: %v", e.Class, want, err)
	}
	if !strings.HasPrefix(err.Error(), "["+string(want)+"] ") {
		t.Fatalf("text %q does not start with [%s]", err.Error(), want)
	}
	return e
}

func mustProject(t *testing.T, s string) Project {
	t.Helper()
	p, err := ParseProject(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadsSendBearerAndUserAgent(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{})
	ctx := context.Background()
	u, err := f.client.GetCurrentUser(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "alice" || u.ID != 1001 {
		t.Errorf("user = %+v, want alice 1001", u)
	}
	m, err := f.client.GetMetadata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "19.4.0" || m.Enterprise {
		t.Errorf("metadata = %+v", m)
	}
	reqs := f.srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	for _, r := range reqs {
		if r.Authorization != "Bearer "+f.token {
			t.Errorf("authorization = %q", r.Authorization)
		}
		if r.UserAgent != "gitlab-mcp/1.2.3" {
			t.Errorf("user agent = %q, want gitlab-mcp/1.2.3", r.UserAgent)
		}
	}
	if reqs[0].EscapedPath != "/api/v4/user" || reqs[1].EscapedPath != "/api/v4/metadata" {
		t.Errorf("paths = %q, %q", reqs[0].EscapedPath, reqs[1].EscapedPath)
	}
}

func TestNoTokenIsAuthWithoutARequest(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{}, func(o *Options) { o.Tokens = nil })
	_, err := f.client.GetCurrentUser(context.Background())
	wantClass(t, err, ClassAuth)
	if n := len(f.srv.Requests()); n != 0 {
		t.Errorf("requests = %d, want 0", n)
	}
}

func TestTokenSourceFailureIsAuth(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{}, func(o *Options) { o.Tokens = failingToken{} })
	e := wantClass(t, errOf(f.client.GetCurrentUser(context.Background())), ClassAuth)
	if !strings.Contains(e.Message, "gitlab-mcp login") {
		t.Errorf("message %q does not say to log in", e.Message)
	}
}

type failingToken struct{}

func (failingToken) Token(context.Context) (string, error) { return "", errors.New("keyring locked") }
func (failingToken) Invalidate(string)                     {}

func errOf[T any](_ T, err error) error { return err }

func TestRevokedTokenIsAuth(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{})
	tok := f.srv.TokenFor("alice", "api")
	f.srv.Revoke(tok)
	f.client.tokens = StaticToken(tok)
	wantClass(t, errOf(f.client.GetCurrentUser(context.Background())), ClassAuth)
}

func TestPrivateProjectIsNotFoundNamingAccess(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{})
	_, err := f.client.GetProject(context.Background(), mustProject(t, gitlabtest.ProjectSecret))
	e := wantClass(t, err, ClassNotFound)
	if !strings.Contains(e.Message, "the project was not found, or you do not have access") {
		t.Errorf("message = %q", e.Message)
	}
	if e.Status != 404 {
		t.Errorf("status = %d", e.Status)
	}
}

func TestMissingRouteIsUnsupported(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{})
	err := f.client.Do(context.Background(), Call{Method: "GET", Path: "projects/{}/no_such_feature",
		Args: []string{"2001"}, Name: "probe"}, nil)
	e := wantClass(t, err, ClassUnsupported)
	if !strings.Contains(e.Message, "no API route for probe") {
		t.Errorf("message = %q", e.Message)
	}
}

func TestReadAPITokenWriteIsAuthNamingScope(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{})
	f.client.tokens = StaticToken(f.srv.TokenFor("alice", "read_api"))
	err := f.client.Do(context.Background(), Call{Method: "POST", Path: "projects/{}/issues/{}/notes",
		Args: []string{"2001", "1"}, Body: map[string]string{"body": "hello"}, Name: "add_comment"}, nil)
	e := wantClass(t, err, ClassAuth)
	if !strings.Contains(e.Message, `"api" scope`) {
		t.Errorf("message = %q, want it to name the api scope", e.Message)
	}
}

func TestProjectPathEscapedOnceAndSecretsNeverDecoded(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{})
	p, err := f.client.GetProject(context.Background(), mustProject(t, gitlabtest.ProjectAlpha))
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != 2001 || p.PathWithNamespace != "example-group/alpha" {
		t.Errorf("project = %d %q", p.ID, p.PathWithNamespace)
	}
	if got := f.srv.Requests()[0].EscapedPath; got != "/api/v4/projects/example-group%2Falpha" {
		t.Errorf("path = %q, want the full path escaped once", got)
	}
	// runners_token is served and has no field; drift names it once.
	_, _ = f.client.GetProject(context.Background(), mustProject(t, gitlabtest.ProjectAlpha))
	logs := f.logs.String()
	if n := strings.Count(logs, "field=Project.runners_token"); n != 1 {
		t.Errorf("drift for runners_token logged %d times, want 1:\n%s", n, logs)
	}
	if strings.Contains(logs, "fixture-secret-never-decoded") || strings.Contains(logs, "example-group") {
		t.Errorf("logs carry payload:\n%s", logs)
	}
}

func TestResolveProjectOncePerCall(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{})
	ctx := WithCall(context.Background())
	p := mustProject(t, "Example-Group/Alpha")
	for range 3 {
		got, err := f.client.ResolveProject(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID() != 2001 || got.Path() != "example-group/alpha" {
			t.Errorf("resolved = %d %q", got.ID(), got.Path())
		}
	}
	if n := Requests(ctx); n != 1 {
		t.Errorf("Requests = %d, want 1", n)
	}
	// A new call is answered from what the process remembers.
	ctx2 := WithCall(context.Background())
	if got, err := f.client.ResolveProject(ctx2, p); err != nil || got.ID() != 2001 {
		t.Fatalf("second call = %v, %v", got, err)
	}
	if n := Requests(ctx2); n != 0 {
		t.Errorf("second call Requests = %d, want 0", n)
	}
	// An id is never read; its path comes along when the process knows it.
	byID, err := f.client.ResolveProject(ctx2, ProjectByID(7))
	if err != nil || byID.ID() != 7 || byID.Path() != "" || Requests(ctx2) != 0 {
		t.Errorf("an id resolved with a request: %v %v %d", byID, err, Requests(ctx2))
	}
	known, err := f.client.ResolveProject(ctx2, ProjectByID(2001))
	if err != nil || known.Path() != "example-group/alpha" || Requests(ctx2) != 0 {
		t.Errorf("a known id = %v %v %d", known, err, Requests(ctx2))
	}
}

// TestResolvedProjectsExpireAndAMoveForgetsThem: the process trusts a
// resolved path for a few minutes, and not past a move GitLab reports.
func TestResolvedProjectsExpireAndAMoveForgetsThem(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	f := newFixture(t, gitlabtest.Options{}, func(o *Options) { o.Now = func() time.Time { return now } })
	p := mustProject(t, gitlabtest.ProjectAlpha)
	resolve := func() int {
		t.Helper()
		ctx := WithCall(context.Background())
		if _, err := f.client.ResolveProject(ctx, p); err != nil {
			t.Fatal(err)
		}
		return Requests(ctx)
	}
	if n := resolve(); n != 1 {
		t.Fatalf("first resolve made %d requests, want 1", n)
	}
	if n := resolve(); n != 0 {
		t.Errorf("a second resolve made %d requests, want 0", n)
	}
	// A move anywhere forgets what the process remembered.
	if _, err := f.client.GetIssue(WithCall(context.Background()), mustProject(t, gitlabtest.ProjectMoved), 1); err != nil {
		t.Fatal(err)
	}
	if n := resolve(); n != 1 {
		t.Errorf("resolve after a move made %d requests, want 1", n)
	}
	now = now.Add(projectTTL)
	if n := resolve(); n != 1 {
		t.Errorf("resolve after the TTL made %d requests, want 1", n)
	}
}

// TestMovedToKeepsTheRequestButTheProject: only the project is taken
// from the Location; the resource under it and the query are the ones
// the request carried.
func TestMovedToKeepsTheRequestButTheProject(t *testing.T) {
	c, err := New(Options{Instance: mustInstance(t, "https://gitlab.example.com"), Tokens: StaticToken("t")})
	if err != nil {
		t.Fatal(err)
	}
	from, err := url.Parse("https://gitlab.example.com/api/v4/projects/example-group%2Fold/repository/files/a%2Fb.go?ref=feature%2Fx")
	if err != nil {
		t.Fatal(err)
	}
	get := Call{Method: "GET"}
	for _, c2 := range []struct {
		name, location, want string
	}{
		{"new path only", "/api/v4/projects/example-group%2Fnew",
			"https://gitlab.example.com/api/v4/projects/example-group%2Fnew/repository/files/a%2Fb.go?ref=feature%2Fx"},
		{"other resource and query", "https://gitlab.example.com/api/v4/projects/example-group%2Fnew/issues?state=all",
			"https://gitlab.example.com/api/v4/projects/example-group%2Fnew/repository/files/a%2Fb.go?ref=feature%2Fx"},
	} {
		t.Run(c2.name, func(t *testing.T) {
			next, ok := c.movedTo(get, from, c2.location)
			if !ok {
				t.Fatal("not followed")
			}
			if next.String() != c2.want {
				t.Errorf("next = %s, want %s", next, c2.want)
			}
			if next.Path != "/api/v4/projects/example-group/new/repository/files/a/b.go" {
				t.Errorf("Path = %q", next.Path)
			}
		})
	}
	if _, ok := c.movedTo(get, from, "/api/v4/user"); ok {
		t.Error("a redirect away from /projects/ was followed")
	}
}

// TestDriftIsWalkedOnlyAtDebugAndOncePerType: the walk decodes a body a
// second time, so it runs only when its report would be logged, and
// once per Go type.
func TestDriftIsWalkedOnlyAtDebugAndOncePerType(t *testing.T) {
	type wire struct {
		A int `json:"a"`
	}
	body := func(extra string) *attemptResult {
		return &attemptResult{status: 200, header: http.Header{"Content-Type": {"application/json"}},
			body: []byte(`{"a":1,"` + extra + `":2}`)}
	}
	var logs bytes.Buffer
	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
		logs.Reset()
		c, err := New(Options{Instance: mustInstance(t, "https://gitlab.example.com"), Tokens: StaticToken("t"),
			Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: level}))})
		if err != nil {
			t.Fatal(err)
		}
		for _, extra := range []string{"first", "second"} {
			var out wire
			if err := c.decode(context.Background(), body(extra), "x", &out); err != nil || out.A != 1 {
				t.Fatalf("decode = %+v, %v", out, err)
			}
		}
		got := logs.String()
		wantFirst := level == slog.LevelDebug
		if strings.Contains(got, "wire.first") != wantFirst {
			t.Errorf("level %v: first drift logged = %v, want %v:\n%s", level, !wantFirst, wantFirst, got)
		}
		if strings.Contains(got, "wire.second") {
			t.Errorf("level %v: a type was walked twice:\n%s", level, got)
		}
	}
}

func TestGetFileEscapesPathAndRef(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{})
	ctx := context.Background()
	alpha := ProjectByID(2001)
	file, err := f.client.GetFile(ctx, alpha, "src/login.go", "feature/login")
	if err != nil {
		t.Fatal(err)
	}
	content, _ := base64.StdEncoding.DecodeString(file.Content)
	if string(content) != "package main\n\n// login is a stub.\nfunc login() {}\n" || file.LastCommitID == "" {
		t.Errorf("file = %+v, content %q", file, content)
	}
	if _, err := f.client.GetFile(ctx, alpha, "docs/with space.md", ""); err != nil {
		t.Fatal(err)
	}
	reqs := f.srv.Requests()
	if reqs[0].EscapedPath != "/api/v4/projects/2001/repository/files/src%2Flogin.go" || reqs[0].RawQuery != "ref=feature%2Flogin" {
		t.Errorf("request = %s ? %s", reqs[0].EscapedPath, reqs[0].RawQuery)
	}
	if reqs[1].EscapedPath != "/api/v4/projects/2001/repository/files/docs%2Fwith%20space.md" || reqs[1].RawQuery != "ref=HEAD" {
		t.Errorf("request = %s ? %s", reqs[1].EscapedPath, reqs[1].RawQuery)
	}
	_, err = f.client.GetFile(ctx, alpha, "missing.txt", "main")
	e := wantClass(t, err, ClassNotFound)
	if !strings.Contains(e.Message, "the file was not found") {
		t.Errorf("message = %q", e.Message)
	}
}

func TestDotSegmentsRefusedBeforeSending(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{})
	for _, path := range []string{"../etc/passwd", "a/../../b", "a/./b", ".", ""} {
		_, err := f.client.GetFile(context.Background(), ProjectByID(2001), path, "main")
		wantClass(t, err, ClassInvalid)
	}
	if n := len(f.srv.Requests()); n != 0 {
		t.Errorf("requests = %d, want 0", n)
	}
}

func TestOffsetPagingWalksEveryIssue(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{})
	ctx := context.Background()
	q := ItemQuery{Project: ProjectByID(2001)}
	seen := map[int64]bool{}
	opts := ListOptions{PerPage: 10}
	pages := 0
	for {
		rows, page, err := f.client.SearchIssues(ctx, q, opts)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if page.Total != 25 || !page.TotalKnown() {
			t.Errorf("total = %d, want 25", page.Total)
		}
		for _, r := range rows {
			seen[r.IID] = true
		}
		if page.Complete() {
			break
		}
		opts.PageToken = page.NextToken
	}
	if pages != 3 || len(seen) != 25 {
		t.Errorf("pages = %d, issues = %d; want 3 and 25", pages, len(seen))
	}
}

func TestPageTokenBoundToItsQuery(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{})
	ctx := context.Background()
	_, page, err := f.client.SearchIssues(ctx, ItemQuery{Project: ProjectByID(2001)}, ListOptions{PerPage: 5})
	if err != nil || page.Complete() {
		t.Fatalf("first page: %v %+v", err, page)
	}
	before := len(f.srv.Requests())
	cases := []struct {
		name  string
		q     ItemQuery
		token string
	}{
		{"another filter", ItemQuery{Project: ProjectByID(2001), State: "opened"}, page.NextToken},
		{"another project", ItemQuery{Project: ProjectByID(2002)}, page.NextToken},
		{"garbage", ItemQuery{Project: ProjectByID(2001)}, "not-a-token"},
		{"forged filter key", ItemQuery{Project: ProjectByID(2001)},
			base64.RawURLEncoding.EncodeToString([]byte(`{"b":"x","p":{"sudo":"root"}}`))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := f.client.SearchIssues(ctx, c.q, ListOptions{PerPage: 5, PageToken: c.token})
			wantClass(t, err, ClassInvalid)
		})
	}
	if n := len(f.srv.Requests()); n != before {
		t.Errorf("a refused token reached GitLab: %d requests", n-before)
	}
	// The right query with a different page size is the same query.
	if _, _, err := f.client.SearchIssues(ctx, ItemQuery{Project: ProjectByID(2001)}, ListOptions{PerPage: 7, PageToken: page.NextToken}); err != nil {
		t.Errorf("same query refused: %v", err)
	}
}

func TestTotalUnknownPastTheLimit(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{TotalLimit: 5, ExtraProjects: 10})
	_, page, err := f.client.SearchProjects(context.Background(), ProjectQuery{}, ListOptions{PerPage: 3})
	if err != nil {
		t.Fatal(err)
	}
	if page.TotalKnown() || page.Total != -1 || page.Complete() {
		t.Errorf("page = %+v, want an unknown total and a next page", page)
	}
}

func TestOffsetCapIs405AndKeysetWalksPastIt(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{OffsetCap: 4, ExtraProjects: 10})
	ctx := context.Background()
	opts := ListOptions{PerPage: 2}
	var err error
	for range 5 {
		var page Page
		_, page, err = f.client.SearchProjects(ctx, ProjectQuery{}, opts)
		if err != nil {
			break
		}
		opts.PageToken = page.NextToken
	}
	e := wantClass(t, err, ClassInvalid)
	if e.Status != 405 || !strings.Contains(e.Message, "keyset") {
		t.Errorf("error = %+v", e)
	}

	seen := map[int64]bool{}
	opts = ListOptions{PerPage: 2}
	for {
		rows, page, err := f.client.SearchProjects(ctx, ProjectQuery{Keyset: true}, opts)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			seen[r.ID] = true
		}
		if page.TotalKnown() {
			t.Errorf("keyset reported a total")
		}
		if page.Complete() {
			break
		}
		opts.PageToken = page.NextToken
	}
	// alpha, beta and ten bulk projects; secret is invisible to alice.
	if len(seen) != 12 || seen[2003] {
		t.Errorf("keyset saw %d projects (secret seen: %v), want 12", len(seen), seen[2003])
	}
}

func TestTreeKeysetPaging(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{})
	ctx := context.Background()
	opts := ListOptions{PerPage: 4}
	var paths []string
	for {
		rows, page, err := f.client.ListTree(ctx, ProjectByID(2001), TreeQuery{Path: "docs/pages"}, opts)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			paths = append(paths, r.Path)
		}
		if page.Complete() {
			break
		}
		opts.PageToken = page.NextToken
	}
	if len(paths) != 12 || paths[0] != "docs/pages/page-01.md" || paths[11] != "docs/pages/page-12.md" {
		t.Errorf("paths = %v", paths)
	}
	for _, r := range f.srv.Requests()[1:] {
		if !strings.Contains(r.RawQuery, "page_token=") || !strings.Contains(r.RawQuery, "pagination=keyset") {
			t.Errorf("follow-up query %q lacks the keyset cursor", r.RawQuery)
		}
	}
}

func TestRateLimited429IsRetriedHonoringRetryAfter(t *testing.T) {
	for _, shape := range []struct {
		name  string
		fault gitlabtest.Fault
		which string
	}{
		{"rack attack plain text", gitlabtest.RackAttack429("/user", 3, 1), "request throttle"},
		{"application json", gitlabtest.Application429("/user", 3, 1), "application limit"},
	} {
		t.Run(shape.name, func(t *testing.T) {
			f := newFixture(t, gitlabtest.Options{})
			f.srv.Inject(shape.fault)
			if _, err := f.client.GetCurrentUser(context.Background()); err != nil {
				t.Fatal(err)
			}
			// The backoff honors Retry-After; a throttle also holds the
			// instance, which the stubbed sleep records as a second wait.
			if got := f.sleeps.all(); len(got) == 0 || len(got) > 2 || got[0] < 3*time.Second {
				t.Errorf("waits = %v, want a first of at least 3s", got)
			}
			if n := len(f.srv.Requests()); n != 2 {
				t.Errorf("requests = %d, want 2", n)
			}
			// With one attempt, the message names the shape and the wait.
			f.srv.Inject(shape.fault)
			f.client.maxAttempts = 1
			e := wantClass(t, errOf(f.client.GetCurrentUser(context.Background())), ClassRateLimited)
			if !strings.Contains(e.Message, shape.which) || !strings.Contains(e.Message, "retry after 3s") {
				t.Errorf("message = %q", e.Message)
			}
		})
	}
}

func TestRateLimitedPostIsRetried(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{})
	f.srv.Inject(gitlabtest.Application429("/projects/2001/issues/1/notes", 1, 1))
	err := f.client.Do(context.Background(), Call{Method: "POST", Path: "projects/{}/issues/{}/notes",
		Args: []string{"2001", "1"}, Body: map[string]string{"body": "Thanks."}, Bucket: BucketNotes, Name: "add_comment"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(f.srv.Requests()); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
}

func TestRetryAfterPastDeadlineIsNotSlept(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{})
	f.srv.Inject(gitlabtest.RackAttack429("/user", 30, 1))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	e := wantClass(t, errOf(f.client.GetCurrentUser(ctx)), ClassRateLimited)
	if !strings.Contains(e.Message, "retry after 30s") {
		t.Errorf("message = %q", e.Message)
	}
	if len(f.sleeps.all()) != 0 || len(f.srv.Requests()) != 1 {
		t.Errorf("slept %v and sent %d", f.sleeps.all(), len(f.srv.Requests()))
	}
}

func TestServerErrorsRetriedForGetNeverForCreate(t *testing.T) {
	notes := Call{Method: "POST", Path: "projects/{}/issues/{}/notes", Args: []string{"2001", "1"},
		Body: map[string]string{"body": "Thanks."}, Name: "add_comment"}
	cases := []struct {
		name     string
		call     Call
		fault    gitlabtest.Fault
		class    Class
		requests int
	}{
		{"GET recovers", Call{Method: "GET", Path: "user", Name: "get_me"},
			gitlabtest.Fault{Path: "/user", Status: 502, Times: 2, Body: "bad gateway"}, "", 3},
		{"GET gives up after four", Call{Method: "GET", Path: "user", Name: "get_me"},
			gitlabtest.Fault{Path: "/user", Status: 500, Times: 10, Body: "{}"}, ClassUnavailable, 4},
		{"create is ambiguous", notes,
			gitlabtest.Fault{Path: "/projects/2001/issues/1/notes", Status: 500, Body: "{}"}, ClassAmbiguousOutcome, 1},
		{"declared repeatable POST recovers", func() Call { c := notes; c.Repeatable = "test"; return c }(),
			gitlabtest.Fault{Path: "/projects/2001/issues/1/notes", Status: 503, Body: "{}"}, "", 2},
		{"PUT recovers", Call{Method: "PUT", Path: "projects/{}", Args: []string{"2001"}, Name: "update"},
			gitlabtest.Fault{Path: "/projects/2001", Method: "PUT", Status: 502, Times: 1, Body: "{}"}, ClassUnsupported, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, gitlabtest.Options{})
			f.srv.Inject(c.fault)
			err := f.client.Do(context.Background(), c.call, nil)
			if c.class == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				wantClass(t, err, c.class)
			}
			if n := len(f.srv.Requests()); n != c.requests {
				t.Errorf("requests = %d, want %d", n, c.requests)
			}
		})
	}
}

func TestDryRunRefusesEveryWrite(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{})
	ctx := WithDryRun(context.Background())
	if !IsDryRun(ctx) || IsDryRun(context.Background()) {
		t.Fatal("IsDryRun is wrong")
	}
	for _, m := range []string{"POST", "PUT", "DELETE"} {
		err := f.client.Do(ctx, Call{Method: m, Path: "projects/{}", Args: []string{"2001"}, Repeatable: "x", Name: "w"}, nil)
		e := wantClass(t, err, ClassBlocked)
		if !errors.Is(e, ErrDryRunWrite) {
			t.Errorf("%s: cause is not ErrDryRunWrite", m)
		}
	}
	if n := len(f.srv.Requests()); n != 0 {
		t.Errorf("requests = %d, want 0", n)
	}
	if _, err := f.client.GetCurrentUser(ctx); err != nil {
		t.Errorf("a read under a dry run failed: %v", err)
	}
}

func TestMovedProjectFollowedOnceForGet(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{})
	ctx := WithCall(context.Background())
	iss, err := f.client.GetIssue(ctx, mustProject(t, gitlabtest.ProjectMoved), 1)
	if err != nil {
		t.Fatal(err)
	}
	if iss.ProjectID != 2001 || iss.IID != 1 {
		t.Errorf("issue = %d/%d", iss.ProjectID, iss.IID)
	}
	moves := Moves(ctx)
	if len(moves) != 1 || moves[0] != (Move{From: "example-group/old-alpha", To: "2001"}) {
		t.Errorf("moves = %+v", moves)
	}
	reqs := f.srv.Requests()
	if len(reqs) != 2 || reqs[1].EscapedPath != "/api/v4/projects/2001/issues/1" {
		t.Errorf("requests = %+v", reqs)
	}
	if Requests(ctx) != 2 {
		t.Errorf("Requests = %d, want 2", Requests(ctx))
	}

	err = f.client.Do(ctx, Call{Method: "POST", Path: "projects/{}/issues/{}/notes",
		Args: []string{gitlabtest.ProjectMoved, "1"}, Body: map[string]string{"body": "x"}, Name: "add_comment"}, nil)
	e := wantClass(t, err, ClassInvalid)
	if !strings.Contains(e.Message, "renamed or moved") {
		t.Errorf("message = %q", e.Message)
	}
}

// TestMovedProjectKeepsTheQuery: a redirect that names only the project
// must not drop the filters and the ref the request carried.
func TestMovedProjectKeepsTheQuery(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{MoveDropsQuery: true})
	_, _, err := f.client.SearchIssues(context.Background(), ItemQuery{Project: mustProject(t, gitlabtest.ProjectMoved),
		State: "closed", Search: "flaky"}, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reqs := f.srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %+v", reqs)
	}
	if reqs[1].EscapedPath != "/api/v4/projects/2001/issues" || reqs[1].RawQuery != reqs[0].RawQuery ||
		!strings.Contains(reqs[1].RawQuery, "state=closed") {
		t.Errorf("followed request = %+v, want the query %q", reqs[1], reqs[0].RawQuery)
	}
}

func TestRedirectsElsewhereAreNeverFollowed(t *testing.T) {
	for _, loc := range []string{
		"https://other.invalid/api/v4/user",
		"/users/sign_in",
		"/api/v4/projects/2001",
	} {
		t.Run(loc, func(t *testing.T) {
			f := newFixture(t, gitlabtest.Options{})
			f.srv.Inject(gitlabtest.Fault{Path: "/user", Status: 302, Header: http.Header{"Location": {loc}}})
			e := wantClass(t, errOf(f.client.GetCurrentUser(context.Background())), ClassUnexpected)
			if strings.Contains(e.Error(), "other.invalid") || strings.Contains(e.Error(), "sign_in") {
				t.Errorf("message names the redirect target: %q", e.Error())
			}
			if n := len(f.srv.Requests()); n != 1 {
				t.Errorf("requests = %d, want 1", n)
			}
		})
	}
}

func TestNextLinkOffInstanceIsNotFollowed(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{})
	h := http.Header{"Link": {`<https://other.invalid/api/v4/projects?page=2>; rel="next"`}, "Content-Type": {"application/json"}}
	f.srv.Inject(gitlabtest.Fault{Path: "/projects", Status: 200, Header: h, Body: "[]"})
	_, _, err := f.client.SearchProjects(context.Background(), ProjectQuery{}, ListOptions{})
	wantClass(t, err, ClassUnexpected)
}

func TestNonJSONBodyIsDescribed(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{})
	f.srv.Inject(gitlabtest.Fault{Path: "/user", Status: 200, Header: http.Header{"Content-Type": {"text/html"}},
		Body: "<html><body>Sign in to continue\n\n</body></html>"})
	e := wantClass(t, errOf(f.client.GetCurrentUser(context.Background())), ClassUnexpected)
	if !strings.Contains(e.Message, `status 200, text/html, starting "<html><body>Sign in to continue </body></html>"`) {
		t.Errorf("message = %q", e.Message)
	}
	f.srv.Inject(gitlabtest.Fault{Path: "/user", Status: 400, Header: http.Header{"Content-Type": {"text/html"}}, Body: "<p>nope</p>"})
	e = wantClass(t, errOf(f.client.GetCurrentUser(context.Background())), ClassInvalid)
	if !strings.Contains(e.Message, `status 400, text/html, starting "<p>nope</p>"`) {
		t.Errorf("message = %q", e.Message)
	}
}

func TestHeaderTimeout(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{}, func(o *Options) {
		o.HeaderTimeout = 50 * time.Millisecond
		o.MaxAttempts = 2
	})
	f.srv.Inject(gitlabtest.Fault{Path: "/user", Status: 200, Body: "{}", Delay: 300 * time.Millisecond, Times: 2})
	e := wantClass(t, errOf(f.client.GetCurrentUser(context.Background())), ClassUnavailable)
	if !strings.Contains(e.Message, "no response headers") {
		t.Errorf("message = %q", e.Message)
	}
	f.srv.Inject(gitlabtest.Fault{Path: "/projects/2001/issues/1/notes", Status: 201, Body: "{}", Delay: 300 * time.Millisecond})
	err := f.client.Do(context.Background(), Call{Method: "POST", Path: "projects/{}/issues/{}/notes",
		Args: []string{"2001", "1"}, Body: map[string]string{"body": "x"}, Name: "add_comment"}, nil)
	wantClass(t, err, ClassAmbiguousOutcome)
}

// TestCanceledCreateIsAmbiguous: a create canceled after it was sent may
// have landed, so it is [ambiguous_outcome], not a plain failure (§4.5).
// A canceled read is [unavailable].
func TestCanceledCreateIsAmbiguous(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{}, func(o *Options) { o.MaxAttempts = 1 })
	f.srv.Inject(gitlabtest.Fault{Path: "/projects/2001/issues/1/notes", Status: 201, Body: "{}", Delay: 300 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := f.client.Do(ctx, Call{Method: "POST", Path: "projects/{}/issues/{}/notes",
		Args: []string{"2001", "1"}, Body: map[string]string{"body": "x"}, Name: "add_comment"}, nil)
	wantClass(t, err, ClassAmbiguousOutcome)

	f.srv.Inject(gitlabtest.Fault{Path: "/user", Status: 200, Body: "{}", Delay: 300 * time.Millisecond})
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	wantClass(t, errOf(f.client.GetCurrentUser(ctx)), ClassUnavailable)
}

func TestStalledBodyIsCut(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{}, func(o *Options) {
		o.StallTimeout = 50 * time.Millisecond
		o.MaxAttempts = 1
	})
	f.srv.Inject(gitlabtest.Fault{Path: "/user", Status: 200, Body: `{"id":1,"username":"alice"}`, StallAfter: 400 * time.Millisecond})
	start := time.Now()
	e := wantClass(t, errOf(f.client.GetCurrentUser(context.Background())), ClassUnavailable)
	if !strings.Contains(e.Message, "stopped arriving") {
		t.Errorf("message = %q", e.Message)
	}
	if time.Since(start) > 350*time.Millisecond {
		t.Errorf("the stall guard took %s", time.Since(start))
	}
}

func TestBodyCap(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{}, func(o *Options) { o.MaxAttempts = 1 })
	big := `{"username":"` + strings.Repeat("a", MaxResponseBytes) + `"}`
	f.srv.Inject(gitlabtest.Fault{Path: "/user", Status: 200, Body: big})
	e := wantClass(t, errOf(f.client.GetCurrentUser(context.Background())), ClassUnavailable)
	if !strings.Contains(e.Message, "larger than 32 MiB") {
		t.Errorf("message = %q", e.Message)
	}
}

func TestTransportErrorsCarryNoPathOrQuery(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	raw := dead.URL
	dead.Close()
	s := &sleeps{}
	c, err := New(Options{Instance: mustInstance(t, raw), Tokens: StaticToken("t"), Sleep: s.sleep, MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.SearchIssues(context.Background(), ItemQuery{Project: mustProject(t, "example-group/hidden-name"),
		Search: "canary-term"}, ListOptions{})
	e := wantClass(t, err, ClassUnavailable)
	for _, leak := range []string{"hidden-name", "canary-term", "/api/v4", "127.0.0.1"} {
		if strings.Contains(e.Error(), leak) {
			t.Errorf("error carries %q: %s", leak, e.Error())
		}
	}
	// A refused connection never reached GitLab, so even a create may
	// try again.
	if len(s.all()) != 1 {
		t.Errorf("waits = %v, want one retry", s.all())
	}
}

func TestTokenInfo(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{})
	f.client.tokens = StaticToken(f.srv.TokenFor("bob", "read_api"))
	ti, err := f.client.TokenInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ti.ResourceOwnerID != 1002 || len(ti.Scope) != 1 || ti.Scope[0] != "read_api" ||
		ti.Application.UID != gitlabtest.ClientID || ti.ExpiresIn == nil || *ti.ExpiresIn <= 0 {
		t.Errorf("token info = %+v", ti)
	}
	if got := f.srv.Requests()[0].EscapedPath; got != "/oauth/token/info" {
		t.Errorf("path = %q", got)
	}
}

func TestPhaseZeroReads(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{Enterprise: true})
	ctx := context.Background()
	alpha := ProjectByID(2001)

	mr, err := f.client.GetMergeRequest(ctx, alpha, 1)
	if err != nil {
		t.Fatal(err)
	}
	if mr.DiffRefs == nil || mr.HeadPipeline == nil || mr.DetailedMergeStatus != "mergeable" || mr.SourceBranch != "feature/login" {
		t.Errorf("merge request = %+v", mr)
	}
	ap, err := f.client.GetMergeRequestApprovals(ctx, alpha, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !ap.Approved || ap.ApprovalsRequired == nil || *ap.ApprovalsLeft != 0 || ap.ApprovedBy[0].User.Username != "carol" {
		t.Errorf("approvals = %+v", ap)
	}
	ds, _, err := f.client.ListMergeRequestDiscussions(ctx, alpha, 1, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 3 || ds[0].Notes[0].Position == nil || *ds[0].Notes[0].Type != "DiffNote" {
		t.Errorf("discussions = %+v", ds)
	}
	ids, _, err := f.client.ListIssueDiscussions(ctx, alpha, 2, ListOptions{})
	if err != nil || len(ids) != 3 || !ids[2].Notes[0].System {
		t.Errorf("issue discussions = %+v, %v", ids, err)
	}
	iss, err := f.client.GetIssue(ctx, alpha, 4)
	if err != nil || iss.State != "closed" || iss.ClosedAt == nil {
		t.Errorf("issue 4 = %+v, %v", iss, err)
	}
	wantClass(t, errOf(f.client.GetIssue(ctx, alpha, 999)), ClassNotFound)
	wantClass(t, errOf(f.client.GetIssue(ctx, alpha, 0)), ClassInvalid)

	branches, _, err := f.client.ListBranches(ctx, alpha, "", ListOptions{})
	if err != nil || len(branches) != 3 || branches[0].Name != "feature/login" || !branches[1].Default {
		t.Errorf("branches = %+v, %v", branches, err)
	}
	commits, page, err := f.client.ListCommits(ctx, alpha, CommitQuery{Ref: "main"}, ListOptions{PerPage: 100})
	if err != nil || len(commits) != 30 || page.Total != 30 || commits[0].Stats != nil {
		t.Errorf("commits = %d, total %d, %v", len(commits), page.Total, err)
	}
	c, err := f.client.GetCommit(ctx, alpha, commits[0].ShortID)
	if err != nil || c.ID != commits[0].ID || c.Stats == nil {
		t.Errorf("commit = %+v, %v", c, err)
	}
	diffs, _, err := f.client.GetCommitDiff(ctx, alpha, c.ID, ListOptions{})
	if err != nil || len(diffs) != 1 || !strings.Contains(diffs[0].Diff, "+generated change 30") {
		t.Errorf("diffs = %+v, %v", diffs, err)
	}
	pbs, _, err := f.client.ListProtectedBranches(ctx, alpha, ListOptions{})
	if err != nil || len(pbs) != 2 || pbs[1].Name != "release/*" {
		t.Errorf("protected = %+v, %v", pbs, err)
	}

	// The instance-wide lists default to what alice created.
	mrs, _, err := f.client.SearchMergeRequests(ctx, ItemQuery{}, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mrs {
		if m.Author.Username != "alice" || m.DiffRefs != nil {
			t.Errorf("row = %+v", m)
		}
	}
	all, _, err := f.client.SearchMergeRequests(ctx, ItemQuery{Scope: "all", ReviewerUsername: "carol"}, ListOptions{})
	if err != nil || len(all) != 3 {
		t.Errorf("all mrs = %d, %v", len(all), err)
	}
	grp, err := ParseGroup(gitlabtest.GroupSub)
	if err != nil {
		t.Fatal(err)
	}
	gi, _, err := f.client.SearchIssues(ctx, ItemQuery{Group: grp}, ListOptions{})
	if err != nil || len(gi) != 1 || gi[0].ProjectID != 2002 {
		t.Errorf("group issues = %+v, %v", gi, err)
	}
	labeled, _, err := f.client.SearchIssues(ctx, ItemQuery{Project: alpha, Labels: []string{"bug", "docs"}, State: "opened"}, ListOptions{PerPage: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range labeled {
		if i.State != "opened" || !contains(i.Labels, "bug") || !contains(i.Labels, "docs") {
			t.Errorf("filtered row = %+v", i)
		}
	}
	if len(labeled) == 0 {
		t.Error("label filter matched nothing")
	}
	_, _, err = f.client.SearchIssues(ctx, ItemQuery{Project: alpha, Group: grp}, ListOptions{})
	wantClass(t, err, ClassInvalid)
	projs, _, err := f.client.SearchProjects(ctx, ProjectQuery{Group: mustGroup(t, gitlabtest.GroupTop), IncludeSubgroups: true}, ListOptions{})
	if err != nil || len(projs) != 2 {
		t.Errorf("group projects = %d, %v", len(projs), err)
	}
}

func mustGroup(t *testing.T, s string) Group {
	t.Helper()
	g, err := ParseGroup(s)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func TestNewRequiresInstance(t *testing.T) {
	_, err := New(Options{})
	wantClass(t, err, ClassInvalid)
}

func TestTokenNeverLeavesTheInstance(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{})
	u, _ := url.Parse("https://other.invalid/api/v4/user")
	_, err := f.client.attempt(context.Background(), "GET", u, nil, "secret", time.Time{})
	if !errors.Is(err, errOffInstance) {
		t.Errorf("err = %v, want errOffInstance", err)
	}
}

// failWith is a transport that fails every request with err, as a
// dialer or a TLS handshake would.
type failWith struct{ err error }

func (f failWith) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

// TestTransportErrorsCarryNoHostName: a failed lookup names the host it
// looked up, and a certificate for the wrong host names both hosts.
// Neither name may reach the error text, which reaches logs.
func TestTransportErrorsCarryNoHostName(t *testing.T) {
	for name, c := range map[string]struct {
		cause error
		class Class
	}{
		"lookup": {&net.OpError{Op: "dial", Net: "tcp",
			Err: &net.DNSError{Name: "canary-host.example.net", Server: "10.9.8.7:53", Err: "no such host", IsNotFound: true}},
			ClassUnavailable},
		"certificate host": {&tls.CertificateVerificationError{Err: x509.HostnameError{
			Certificate: &x509.Certificate{DNSNames: []string{"canary-cert.example.net"}}, Host: "canary-host.example.net"}},
			ClassUnavailable},
		"unknown authority": {&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{
			Cert: &x509.Certificate{Subject: pkix.Name{CommonName: "canary-ca.example.net"}}}},
			ClassUnavailable},
		// A proxy or a wrapping transport can hand back a *url.Error of its
		// own, which net/http wraps once more.
		"lookup in a url error": {&url.Error{Op: "Get", URL: "https://canary-host.example.net/api/v4/user",
			Err: &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Name: "canary-host.example.net",
				Server: "10.9.8.7:53", Err: "no such host", IsNotFound: true}}}, ClassUnavailable},
		"certificate in a url error": {&url.Error{Op: "Get", URL: "https://canary-host.example.net/api/v4/user",
			Err: &tls.CertificateVerificationError{Err: x509.HostnameError{
				Certificate: &x509.Certificate{DNSNames: []string{"canary-cert.example.net"}}, Host: "canary-host.example.net"}}},
			ClassUnavailable},
		"an unknown shape": {errors.New("proxyconnect tcp: canary-proxy.example.net 10.9.8.7:3128 refused"), ClassUnavailable},
	} {
		s := &sleeps{}
		cl, err := New(Options{Instance: mustInstance(t, "https://canary-host.example.net"), Tokens: StaticToken("t"),
			HTTPClient: &http.Client{Transport: failWith{c.cause}}, Sleep: s.sleep, MaxAttempts: 1})
		if err != nil {
			t.Fatal(err)
		}
		_, err = cl.GetMergeRequest(context.Background(), ProjectByID(2001), 1)
		e := wantClass(t, err, c.class)
		for _, leak := range []string{"canary", "10.9.8.7"} {
			if strings.Contains(e.Error(), leak) {
				t.Errorf("%s: %q carries %q", name, e.Error(), leak)
			}
		}
	}
}

// rotating hands out the token it holds and counts the tokens dropped.
type rotating struct {
	mu      sync.Mutex
	current string
	next    string
	dropped []string
}

func (r *rotating) Token(context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current, nil
}

func (r *rotating) Invalidate(rejected string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dropped = append(r.dropped, rejected)
	if r.current == rejected {
		r.current = r.next
	}
}

// TestARefusedTokenIsDroppedOnce: invalid_token drops the token, and a
// read is sent once more with the next one. A 401 without that mark,
// such as GitLab's answer to a merge the account may not make, keeps it.
func TestARefusedTokenIsDroppedOnce(t *testing.T) {
	invalid := `{"error":"invalid_token","error_description":"Token was revoked."}`
	for _, c := range []struct {
		name     string
		body     string
		times    int
		wantDrop int
		wantReqs int
		wantErr  bool
	}{
		{"invalid_token, then accepted", invalid, 1, 1, 2, false},
		{"invalid_token twice", invalid, 2, 1, 2, true},
		{"a 401 about something else", `{"message":"401 Unauthorized"}`, 1, 0, 1, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			var tokens *rotating
			f := newFixture(t, gitlabtest.Options{}, func(o *Options) { o.MaxAttempts = 3 })
			tokens = &rotating{current: "old-token", next: f.token}
			f.client.tokens = tokens
			f.srv.Inject(gitlabtest.Fault{Path: "/user", Status: 401, Body: c.body, Times: c.times,
				Header: http.Header{"Content-Type": {"application/json"}}})
			_, err := f.client.GetCurrentUser(context.Background())
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, want error %v", err, c.wantErr)
			}
			if err != nil {
				wantClass(t, err, ClassAuth)
			}
			if len(tokens.dropped) != c.wantDrop {
				t.Errorf("dropped %v, want %d", tokens.dropped, c.wantDrop)
			}
			if n := len(f.srv.Requests()); n != c.wantReqs {
				t.Errorf("%d requests, want %d", n, c.wantReqs)
			}
		})
	}
}

func TestAListingJumpsToAPageByNumber(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{})
	p := ProjectByID(2001)
	first, page, err := f.client.SearchIssues(t.Context(), ItemQuery{Project: p, State: "all"}, ListOptions{PerPage: 10})
	if err != nil || page.Pages < 2 {
		t.Fatalf("pages = %d, %v", page.Pages, err)
	}
	last, _, err := f.client.SearchIssues(t.Context(), ItemQuery{Project: p, State: "all"}, ListOptions{PerPage: 10, Page: page.Pages})
	if err != nil || len(last) == 0 || last[0].IID == first[0].IID {
		t.Fatalf("the last page = %v, %v", last, err)
	}
	if _, _, err := f.client.SearchIssues(t.Context(), ItemQuery{Project: p}, ListOptions{Page: 2, PageToken: page.NextToken}); err == nil {
		t.Error("a page number and a page token together were accepted")
	}
}

// A POST that says it changes nothing may run under a dry run; any
// other is refused before it is sent.
func TestADryRunLetsAReadOnlyPostThrough(t *testing.T) {
	f := newFixture(t, gitlabtest.Options{})
	ctx := WithDryRun(t.Context())
	if _, err := f.client.LintCIContent(ctx, ProjectByID(2001), "build:\n  script: [x]\n", LintQuery{}); err != nil {
		t.Errorf("a read-only POST was refused under a dry run: %v", err)
	}
	if _, err := f.client.CreateIssue(ctx, ProjectByID(2001), IssueCreate{Title: "x"}); !errors.Is(err, ErrDryRunWrite) {
		t.Errorf("a create under a dry run: %v", err)
	}
}
