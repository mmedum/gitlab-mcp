package gitlabtest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// noRedirect is a client that reports redirects instead of following
// them, as the authorization tests need to read Location.
var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func do(t *testing.T, method, rawURL, token string, body io.Reader, contentType string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp, m
}

func TestMetadataNeedsAuth(t *testing.T) {
	s := New(t, Options{})
	resp, body := do(t, "GET", s.URL+"/api/v4/metadata", "", nil, "")
	if resp.StatusCode != 401 || body["message"] != "401 Unauthorized" {
		t.Errorf("unauthenticated = %d %v", resp.StatusCode, body)
	}
	resp, body = do(t, "GET", s.URL+"/api/v4/metadata", s.Token(), nil, "")
	if resp.StatusCode != 200 || body["version"] != "19.4.0" || body["enterprise"] != false {
		t.Errorf("authenticated = %d %v", resp.StatusCode, body)
	}
}

func TestErrorShapes(t *testing.T) {
	s := New(t, Options{})
	tok := s.Token()
	cases := []struct {
		path   string
		status int
		key    string
		want   string
	}{
		{"/api/v4/projects/example-group%2Fsecret", 404, "message", "404 Project Not Found"},
		{"/api/v4/projects/example-group%2Fnothing-here", 404, "message", "404 Project Not Found"},
		{"/api/v4/projects/2001/no_such_route", 404, "error", "404 Not Found"},
		{"/api/v4/nothing", 404, "error", "404 Not Found"},
		{"/api/v4/projects/2001/issues/999", 404, "message", "404 Issue Not Found"},
		{"/api/v4/projects/2001/repository/files/README.md", 400, "error", "ref is missing"},
		{"/api/v4/projects/2001/repository/files/README.md?ref=nope", 404, "message", "404 Commit Not Found"},
		// A path escaped twice names a file that does not exist.
		{"/api/v4/projects/2001/repository/files/src%252Fmain.go?ref=main", 404, "message", "404 File Not Found"},
		// A path not escaped at all is a route that does not exist.
		{"/api/v4/projects/2001/repository/files/src/main.go?ref=main", 404, "error", "404 Not Found"},
	}
	for _, c := range cases {
		resp, body := do(t, "GET", s.URL+c.path, tok, nil, "")
		if resp.StatusCode != c.status || body[c.key] != c.want {
			t.Errorf("%s = %d %v, want %d %s=%q", c.path, resp.StatusCode, body, c.status, c.key, c.want)
		}
	}
	resp, _ := do(t, "GET", s.URL+"/api/v4/projects/2001/repository/files/src%2Fmain.go?ref=main", tok, nil, "")
	if resp.StatusCode != 200 {
		t.Errorf("escaped once = %d", resp.StatusCode)
	}
}

func TestInsufficientScope(t *testing.T) {
	s := New(t, Options{})
	resp, body := do(t, "POST", s.URL+"/api/v4/projects/2001/issues/1/notes", s.TokenFor("alice", "read_api"),
		strings.NewReader(`{"body":"hi"}`), "application/json")
	if resp.StatusCode != 403 || body["error"] != "insufficient_scope" || body["scope"] != "api" ||
		!strings.Contains(resp.Header.Get("WWW-Authenticate"), `scope="api"`) {
		t.Errorf("= %d %v %q", resp.StatusCode, body, resp.Header.Get("WWW-Authenticate"))
	}
}

func TestOffsetHeadersAndTheirEdges(t *testing.T) {
	s := New(t, Options{TotalLimit: 5, OffsetCap: 6, ExtraProjects: 8})
	tok := s.Token()
	resp, _ := do(t, "GET", s.URL+"/api/v4/projects/2001/issues?per_page=10&page=2", tok, nil, "")
	h := resp.Header
	if h.Get("X-Total") != "" || h.Get("X-Total-Pages") != "" {
		t.Errorf("a total past the limit was sent: %q", h.Get("X-Total"))
	}
	if h.Get("X-Next-Page") != "3" || !strings.Contains(h.Get("Link"), `rel="next"`) || strings.Contains(h.Get("Link"), `rel="last"`) {
		t.Errorf("headers = %v", h)
	}
	resp, _ = do(t, "GET", s.URL+"/api/v4/projects/2001/protected_branches", tok, nil, "")
	if resp.Header.Get("X-Total") != "2" || resp.Header.Get("X-Next-Page") != "" {
		t.Errorf("small list headers = %v", resp.Header)
	}
	resp, body := do(t, "GET", s.URL+"/api/v4/projects?per_page=3&page=3", tok, nil, "")
	if resp.StatusCode != 405 || !strings.Contains(body["message"].(string), "keyset") {
		t.Errorf("past the offset cap = %d %v", resp.StatusCode, body)
	}
	resp, _ = do(t, "GET", s.URL+"/api/v4/projects?pagination=keyset&order_by=id&sort=asc&per_page=3", tok, nil, "")
	next := resp.Header.Get("Link")
	if !strings.Contains(next, "id_after=2004") || !strings.HasPrefix(next, "<"+s.URL+"/api/v4/projects?") {
		t.Errorf("keyset link = %q", next)
	}
}

func TestMovedProject(t *testing.T) {
	s := New(t, Options{})
	tok := s.Token()
	resp, _ := do(t, "GET", s.URL+"/api/v4/projects/example-group%2Fold-alpha/issues?state=opened", tok, nil, "")
	if resp.StatusCode != 301 || resp.Header.Get("Location") != s.URL+"/api/v4/projects/2001/issues?state=opened" {
		t.Errorf("GET = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, _ = do(t, "POST", s.URL+"/api/v4/projects/example-group%2Fold-alpha/issues/1/notes", tok, strings.NewReader(`{"body":"x"}`), "application/json")
	if resp.StatusCode != 405 {
		t.Errorf("POST = %d", resp.StatusCode)
	}
}

func TestFaults(t *testing.T) {
	s := New(t, Options{})
	tok := s.Token()
	s.Inject(RackAttack429("/user", 9, 1))
	resp, _ := do(t, "GET", s.URL+"/api/v4/user", tok, nil, "")
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "9" || resp.Header.Get("Content-Type") != "text/plain" {
		t.Errorf("rack attack = %d %v", resp.StatusCode, resp.Header)
	}
	s.Inject(Application429("/user", 4, 1))
	resp, body := do(t, "GET", s.URL+"/api/v4/user", tok, nil, "")
	if resp.StatusCode != 429 || body["message"].(map[string]any)["error"] == nil || resp.Header.Get("RateLimit-Remaining") != "" {
		t.Errorf("application = %d %v", resp.StatusCode, body)
	}
	resp, _ = do(t, "GET", s.URL+"/api/v4/user", tok, nil, "")
	if resp.StatusCode != 200 {
		t.Errorf("after the faults = %d", resp.StatusCode)
	}
	if n := len(s.Requests()); n != 3 {
		t.Errorf("recorded %d requests", n)
	}
}

func TestQuickActions(t *testing.T) {
	s := New(t, Options{})
	tok := s.Token()
	post := func(body string) (*http.Response, map[string]any) {
		raw, _ := json.Marshal(map[string]string{"body": body})
		return do(t, "POST", s.URL+"/api/v4/projects/2001/issues/1/notes", tok, strings.NewReader(string(raw)), "application/json")
	}
	resp, body := post("/close")
	if resp.StatusCode != 202 || body["id"] != nil {
		t.Errorf("command-only note = %d %v", resp.StatusCode, body)
	}
	if iss, _ := s.Issue(ProjectAlpha, 1); iss.State != "closed" || iss.ClosedBy == nil || iss.UserNotesCount != 0 {
		t.Errorf("issue after /close = %+v", iss)
	}
	resp, body = post("Reopening this.\n/reopen")
	if resp.StatusCode != 201 || body["body"] != "Reopening this." {
		t.Errorf("mixed note = %d %v", resp.StatusCode, body)
	}
	if iss, _ := s.Issue(ProjectAlpha, 1); iss.State != "opened" || iss.UserNotesCount != 1 {
		t.Errorf("issue after /reopen = %+v", iss)
	}
	resp, body = post("Example:\n```\n/close\n```")
	if resp.StatusCode != 201 || !strings.Contains(body["body"].(string), "/close") {
		t.Errorf("fenced note = %d %v", resp.StatusCode, body)
	}
	if iss, _ := s.Issue(ProjectAlpha, 1); iss.State != "opened" {
		t.Errorf("a fenced command ran")
	}
	resp, body = post("/unknowncommand stays")
	if resp.StatusCode != 201 || body["body"] != "/unknowncommand stays" {
		t.Errorf("unknown command = %d %v", resp.StatusCode, body)
	}
}

// ------------------------------------------------------------ OAuth

func pkce(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func authorize(t *testing.T, s *Server, redirect, challenge string) *http.Response {
	t.Helper()
	q := url.Values{"client_id": {ClientID}, "redirect_uri": {redirect}, "response_type": {"code"},
		"state": {"st-1"}, "scope": {"api"}}
	if challenge != "" {
		q.Set("code_challenge", challenge)
		q.Set("code_challenge_method", "S256")
	}
	resp, _ := do(t, "GET", s.URL+"/oauth/authorize?"+q.Encode(), "", nil, "")
	return resp
}

func tokenRequest(t *testing.T, s *Server, form url.Values) (int, map[string]any) {
	t.Helper()
	resp, body := do(t, "POST", s.URL+"/oauth/token", "", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	return resp.StatusCode, body
}

func TestLoopbackRedirectMatching(t *testing.T) {
	s := New(t, Options{RedirectURIs: []string{"http://127.0.0.1/callback", "http://[::1]/callback", "http://localhost:8080/callback"}})
	cases := []struct {
		redirect string
		ok       bool
	}{
		{"http://127.0.0.1/callback", true},
		{"http://127.0.0.1:49152/callback", true},
		{"http://[::1]:5000/callback", true},
		{"http://localhost:8080/callback", true},
		{"http://localhost:9090/callback", false},
		{"http://localhost/callback", false},
		{"http://127.0.0.1:49152/other", false},
		{"https://127.0.0.1:49152/callback", false},
		{"http://gitlab.example.com/callback", false},
	}
	for _, c := range cases {
		resp := authorize(t, s, c.redirect, "")
		if c.ok {
			loc, _ := url.Parse(resp.Header.Get("Location"))
			if resp.StatusCode != 302 || loc.Query().Get("code") == "" || loc.Query().Get("state") != "st-1" ||
				!strings.HasPrefix(resp.Header.Get("Location"), c.redirect+"?") {
				t.Errorf("%s = %d %q", c.redirect, resp.StatusCode, resp.Header.Get("Location"))
			}
		} else if resp.StatusCode != 400 || resp.Header.Get("Location") != "" {
			t.Errorf("%s = %d, want 400 without a redirect", c.redirect, resp.StatusCode)
		}
	}
}

func codeFrom(t *testing.T, resp *http.Response) string {
	t.Helper()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Query().Get("code") == "" {
		t.Fatalf("no code in %q", resp.Header.Get("Location"))
	}
	return loc.Query().Get("code")
}

func TestCodeExchangeVerifiesPKCEAndRefreshRotates(t *testing.T) {
	s := New(t, Options{})
	redirect := "http://127.0.0.1:43210/callback"
	verifier := strings.Repeat("v", 64)
	code := codeFrom(t, authorize(t, s, redirect, pkce(verifier)))
	base := url.Values{"grant_type": {"authorization_code"}, "client_id": {ClientID}, "redirect_uri": {redirect}, "code": {code}}

	wrong := url.Values{}
	for k, v := range base {
		wrong[k] = v
	}
	wrong.Set("code_verifier", strings.Repeat("w", 64))
	if status, body := tokenRequest(t, s, wrong); status != 400 || body["error"] != "invalid_grant" {
		t.Errorf("wrong verifier = %d %v", status, body)
	}
	base.Set("code_verifier", verifier)
	status, body := tokenRequest(t, s, base)
	if status != 200 || body["token_type"] != "Bearer" || body["expires_in"] != float64(7200) || body["scope"] != "api" {
		t.Fatalf("exchange = %d %v", status, body)
	}
	if status, _ := tokenRequest(t, s, base); status != 400 {
		t.Errorf("a code was accepted twice: %d", status)
	}
	access1, refresh1 := body["access_token"].(string), body["refresh_token"].(string)
	if resp, _ := do(t, "GET", s.URL+"/api/v4/user", access1, nil, ""); resp.StatusCode != 200 {
		t.Errorf("new access token refused: %d", resp.StatusCode)
	}

	refresh := url.Values{"grant_type": {"refresh_token"}, "client_id": {ClientID}, "refresh_token": {refresh1}}
	status, body = tokenRequest(t, s, refresh)
	if status != 200 || body["refresh_token"] == refresh1 || body["access_token"] == access1 {
		t.Fatalf("refresh = %d %v", status, body)
	}
	// Rotation is immediate: the old pair is dead at once.
	if status, body := tokenRequest(t, s, refresh); status != 400 || body["error"] != "invalid_grant" {
		t.Errorf("reused refresh = %d %v", status, body)
	}
	if resp, _ := do(t, "GET", s.URL+"/api/v4/user", access1, nil, ""); resp.StatusCode != 401 {
		t.Errorf("old access token after rotation = %d", resp.StatusCode)
	}
	if s.Refreshes() != 1 {
		t.Errorf("Refreshes = %d", s.Refreshes())
	}
}

func TestCodeWithoutChallengeNeedsNoVerifier(t *testing.T) {
	s := New(t, Options{})
	redirect := "http://127.0.0.1/callback"
	code := codeFrom(t, authorize(t, s, redirect, ""))
	status, _ := tokenRequest(t, s, url.Values{"grant_type": {"authorization_code"}, "client_id": {ClientID},
		"redirect_uri": {redirect}, "code": {code}})
	if status != 200 {
		t.Errorf("exchange = %d", status)
	}
}

func TestRedirectURIMustMatchAuthorization(t *testing.T) {
	s := New(t, Options{})
	code := codeFrom(t, authorize(t, s, "http://127.0.0.1:1111/callback", ""))
	status, body := tokenRequest(t, s, url.Values{"grant_type": {"authorization_code"}, "client_id": {ClientID},
		"redirect_uri": {"http://127.0.0.1:2222/callback"}, "code": {code}})
	if status != 400 || body["error"] != "invalid_grant" {
		t.Errorf("= %d %v", status, body)
	}
}

func TestConfidentialClientNeedsSecret(t *testing.T) {
	s := New(t, Options{Confidential: true, ClientSecret: "fixture-client-secret"})
	redirect := "http://127.0.0.1/callback"
	code := codeFrom(t, authorize(t, s, redirect, ""))
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {ClientID}, "redirect_uri": {redirect}, "code": {code}}
	if status, body := tokenRequest(t, s, form); status != 401 || body["error"] != "invalid_client" {
		t.Errorf("without secret = %d %v", status, body)
	}
	form.Set("client_secret", "fixture-client-secret")
	if status, body := tokenRequest(t, s, form); status != 200 {
		t.Errorf("with secret = %d %v", status, body)
	}
}

func TestUnknownClientAndBadRequests(t *testing.T) {
	s := New(t, Options{})
	resp, _ := do(t, "GET", s.URL+"/oauth/authorize?client_id=other&redirect_uri=http://127.0.0.1/callback&response_type=code", "", nil, "")
	if resp.StatusCode != 401 {
		t.Errorf("unknown client = %d", resp.StatusCode)
	}
	resp, _ = do(t, "GET", s.URL+"/oauth/authorize?client_id="+ClientID+"&redirect_uri=http://127.0.0.1/callback&response_type=code&scope=sudo", "", nil, "")
	if loc, _ := url.Parse(resp.Header.Get("Location")); resp.StatusCode != 302 || loc.Query().Get("error") != "invalid_scope" {
		t.Errorf("bad scope = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if status, body := tokenRequest(t, s, url.Values{"grant_type": {"password"}, "client_id": {ClientID}}); status != 400 || body["error"] != "unsupported_grant_type" {
		t.Errorf("password grant = %d %v", status, body)
	}
}

func TestRevokeAndTokenInfo(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	s := New(t, Options{Now: func() time.Time { return now }, AccessTokenTTL: time.Hour})
	tok := s.TokenFor("bob", "read_api")
	resp, body := do(t, "GET", s.URL+"/oauth/token/info", tok, nil, "")
	if resp.StatusCode != 200 || body["resource_owner_id"] != float64(1002) || body["expires_in"] != float64(3600) ||
		body["application"].(map[string]any)["uid"] != ClientID || body["created_at"] != float64(now.Unix()) {
		t.Errorf("token info = %d %v", resp.StatusCode, body)
	}
	resp, _ = do(t, "POST", s.URL+"/oauth/revoke", "", strings.NewReader(url.Values{"token": {tok}, "client_id": {ClientID}}.Encode()),
		"application/x-www-form-urlencoded")
	if resp.StatusCode != 200 {
		t.Errorf("revoke = %d", resp.StatusCode)
	}
	if resp, body := do(t, "GET", s.URL+"/oauth/token/info", tok, nil, ""); resp.StatusCode != 401 || body["error"] != "invalid_token" {
		t.Errorf("revoked token info = %d %v", resp.StatusCode, body)
	}
}

func TestExpiredTokenIs401(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	s := New(t, Options{Now: func() time.Time { return now }, AccessTokenTTL: time.Minute})
	tok := s.Token()
	now = now.Add(2 * time.Minute)
	resp, body := do(t, "GET", s.URL+"/api/v4/user", tok, nil, "")
	if resp.StatusCode != 401 || body["error"] != "invalid_token" {
		t.Errorf("= %d %v", resp.StatusCode, body)
	}
}

func TestGeneratedDataIsSynthetic(t *testing.T) {
	s := New(t, Options{})
	resp, body := do(t, "GET", s.URL+"/api/v4/projects/2001", s.Token(), nil, "")
	if resp.StatusCode != 200 || body["path_with_namespace"] != ProjectAlpha || body["id"] != float64(2001) {
		t.Errorf("alpha = %v", body)
	}
	if s.ProjectID(ProjectBeta) != 2002 || s.ProjectID(ProjectSecret) != 2003 {
		t.Error("project ids moved")
	}
	resp, _ = do(t, "GET", s.URL+"/api/v4/projects/2003", s.TokenFor("bob", "api"), nil, "")
	if resp.StatusCode != 200 {
		t.Errorf("a member cannot read a private project: %d", resp.StatusCode)
	}
}
