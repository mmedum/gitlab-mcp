package gitlabtest

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// The OAuth side models Doorkeeper as GitLab runs it (§2.1–§2.3, §10):
// loopback redirects on an IP literal match on any port, localhost only
// exactly; PKCE S256 is verified when a challenge was sent; a refresh
// rotates at once, revoking the old pair, so a second use of the old
// refresh token is invalid_grant; a confidential application refuses a
// token request without its secret.

type tokenRec struct {
	access, refresh string
	user            string
	scopes          []string
	created         time.Time
	ttl             time.Duration
	revoked         bool
	clientID        string
}

func (t *tokenRec) has(scope string) bool { return slices.Contains(t.scopes, scope) }

type grant struct {
	clientID, redirectURI, challenge, scope, user string
	used                                          bool
}

type oauthState struct {
	// nonce makes this instance's token names its own, so a client that
	// kept a token from another instance never finds it valid here.
	nonce     string
	n         int
	refreshes int
	access    map[string]*tokenRec
	refresh   map[string]*tokenRec
	codes     map[string]*grant
}

func (o *oauthState) init() {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	o.nonce = hex.EncodeToString(b)
	o.access = map[string]*tokenRec{}
	o.refresh = map[string]*tokenRec{}
	o.codes = map[string]*grant{}
}

// issue mints a pair. The values are shaped so no secret scanner reads
// them as a real token.
func (o *oauthState) issue(user string, scopes []string, now time.Time, ttl time.Duration, withRefresh bool) *tokenRec {
	o.n++
	t := &tokenRec{access: fmt.Sprintf("test-access-%s-%06d", o.nonce, o.n), user: user, scopes: scopes, created: now, ttl: ttl,
		clientID: ClientID}
	if withRefresh {
		t.refresh = fmt.Sprintf("test-refresh-%s-%06d", o.nonce, o.n)
		o.refresh[t.refresh] = t
	}
	o.access[t.access] = t
	return t
}

func (o *oauthState) revoke(token string) {
	if t := o.access[token]; t != nil {
		t.revoked = true
	}
	if t := o.refresh[token]; t != nil {
		t.revoked = true
	}
}

// Refreshes counts refresh grants that succeeded, for a test of the
// cross-process lock.
func (s *Server) Refreshes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.oauth.refreshes
}

func (s *Server) serveOAuth(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/oauth/authorize" && r.Method == http.MethodGet:
		s.authorize(w, r)
	case r.URL.Path == "/oauth/token" && r.Method == http.MethodPost:
		s.token(w, r)
	case r.URL.Path == "/oauth/revoke" && r.Method == http.MethodPost:
		s.revokeEndpoint(w, r)
	case r.URL.Path == "/oauth/token/info" && r.Method == http.MethodGet:
		s.tokenInfo(w, r)
	default:
		routeNotFound(w)
	}
}

// redirectMatches is Doorkeeper's comparison: when both URIs are
// loopback IP literals the ports are ignored (RFC 8252 §7.3); anything
// else, localhost included, must match exactly.
func redirectMatches(registered, requested string) bool {
	if registered == requested {
		return true
	}
	a, errA := url.Parse(registered)
	b, errB := url.Parse(requested)
	if errA != nil || errB != nil {
		return false
	}
	if !loopbackIP(a.Hostname()) || !loopbackIP(b.Hostname()) {
		return false
	}
	return a.Scheme == b.Scheme && a.Hostname() == b.Hostname() && a.Path == b.Path && a.RawQuery == b.RawQuery
}

func loopbackIP(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != ClientID {
		http.Error(w, "Client authentication failed due to unknown client (invalid_client)", http.StatusUnauthorized)
		return
	}
	redirect := q.Get("redirect_uri")
	ok := false
	for _, reg := range s.opts.RedirectURIs {
		if redirectMatches(reg, redirect) {
			ok = true
		}
	}
	if !ok {
		// Doorkeeper renders an error page rather than redirecting to an
		// address it does not trust.
		http.Error(w, "The redirect uri included is not valid. (invalid_redirect_uri)", http.StatusBadRequest)
		return
	}
	target, _ := url.Parse(redirect)
	back := target.Query()
	back.Set("state", q.Get("state"))
	fail := func(code string) {
		back.Set("error", code)
		target.RawQuery = back.Encode()
		http.Redirect(w, r, target.String(), http.StatusFound)
	}
	if q.Get("response_type") != "code" {
		fail("unsupported_response_type")
		return
	}
	if q.Get("code_challenge") != "" && q.Get("code_challenge_method") != "S256" {
		fail("invalid_request")
		return
	}
	for sc := range strings.FieldsSeq(q.Get("scope")) {
		if !slices.Contains([]string{"api", "read_api", "read_user", "openid", "profile", "email"}, sc) {
			fail("invalid_scope")
			return
		}
	}
	s.mu.Lock()
	s.oauth.n++
	code := fmt.Sprintf("test-code-%06d", s.oauth.n)
	s.oauth.codes[code] = &grant{clientID: ClientID, redirectURI: redirect, challenge: q.Get("code_challenge"),
		scope: q.Get("scope"), user: s.opts.AuthorizeAs}
	s.mu.Unlock()
	back.Set("code", code)
	target.RawQuery = back.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]any{"error": code, "error_description": desc})
}

const invalidGrant = "The provided authorization grant is invalid, expired, revoked, does not match the redirection URI used in the authorization request, or was issued to another client."

// clientOK authenticates the client: a public one by id, a confidential
// one by id and secret, in the form or by Basic auth.
func (s *Server) clientOK(r *http.Request) bool {
	id, secret, basic := r.BasicAuth()
	if !basic {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != ClientID {
		return false
	}
	return !s.opts.Confidential || (secret != "" && secret == s.opts.ClientSecret)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "The request could not be parsed.")
		return
	}
	if !s.clientOK(r) {
		oauthError(w, http.StatusUnauthorized, "invalid_client",
			"Client authentication failed due to unknown client, no client authentication included, or unsupported authentication method.")
		return
	}
	f := r.PostForm
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opts.Now()
	var issued *tokenRec
	switch f.Get("grant_type") {
	case "authorization_code":
		g := s.oauth.codes[f.Get("code")]
		if g == nil || g.used || g.redirectURI != f.Get("redirect_uri") {
			oauthError(w, http.StatusBadRequest, "invalid_grant", invalidGrant)
			return
		}
		if g.challenge != "" {
			sum := sha256.Sum256([]byte(f.Get("code_verifier")))
			if f.Get("code_verifier") == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
				oauthError(w, http.StatusBadRequest, "invalid_grant", invalidGrant)
				return
			}
		}
		g.used = true
		issued = s.oauth.issue(g.user, strings.Fields(g.scope), now, s.opts.AccessTokenTTL, true)
	case "refresh_token":
		old := s.oauth.refresh[f.Get("refresh_token")]
		if old == nil || old.revoked {
			oauthError(w, http.StatusBadRequest, "invalid_grant", invalidGrant)
			return
		}
		// Rotation is immediate: the old access and refresh tokens stop
		// working the moment the new pair exists.
		old.revoked = true
		s.oauth.refreshes++
		issued = s.oauth.issue(old.user, old.scopes, now, s.opts.AccessTokenTTL, true)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type",
			"The authorization grant type is not supported by the authorization server.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": issued.access, "token_type": "Bearer", "expires_in": int(issued.ttl.Seconds()),
		"refresh_token": issued.refresh, "scope": strings.Join(issued.scopes, " "), "created_at": now.Unix(),
	})
}

func (s *Server) revokeEndpoint(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || !s.clientOK(r) {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "Client authentication failed.")
		return
	}
	s.mu.Lock()
	s.oauth.revoke(r.PostForm.Get("token"))
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) tokenInfo(w http.ResponseWriter, r *http.Request) {
	bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if bearer == "" {
		bearer = r.URL.Query().Get("access_token")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.oauth.access[bearer]
	now := s.opts.Now()
	if t == nil || t.revoked || !now.Before(t.created.Add(t.ttl)) {
		oauthError(w, http.StatusUnauthorized, "invalid_token", "The access token is invalid")
		return
	}
	left := int64(t.created.Add(t.ttl).Sub(now).Seconds())
	var id int64
	for i, u := range Users {
		if u == t.user {
			id = int64(firstUserID + i)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"resource_owner_id": id, "scope": t.scopes, "expires_in": left,
		"application": map[string]any{"uid": t.clientID}, "created_at": t.created.Unix(),
		// GitLab adds these two for older clients.
		"scopes": t.scopes, "expires_in_seconds": left,
	})
}
