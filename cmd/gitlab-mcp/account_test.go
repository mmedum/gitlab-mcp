package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/mmedum/gitlab-mcp/internal/config"
	"github.com/mmedum/gitlab-mcp/internal/gapi/gitlabtest"
	"github.com/mmedum/gitlab-mcp/internal/userconfig"
)

// signedIn is a home, an instance and a completed login.
func signedIn(t *testing.T, opts gitlabtest.Options, extra ...string) (map[string]string, *gitlabtest.Server, *browser) {
	t.Helper()
	env := home(t)
	srv := gitlabtest.New(t, opts)
	env[config.EnvInstance] = srv.URL
	env[config.EnvClientID] = gitlabtest.ClientID
	b := useBrowser(t)
	r := runWith(env, nil, append([]string{"login"}, extra...)...)
	if r.code != 0 {
		t.Fatalf("login: %+v", r)
	}
	return env, srv, b
}

func TestLoginEndToEnd(t *testing.T) {
	env := home(t)
	srv := gitlabtest.New(t, gitlabtest.Options{AuthorizeAs: "bob"})
	env[config.EnvInstance] = srv.URL
	env[config.EnvClientID] = gitlabtest.ClientID
	b := useBrowser(t)

	r := runWith(env, nil, "login")
	if r.code != 0 {
		t.Fatalf("login: %+v", r)
	}
	// The scopes are printed before the browser opens.
	scopesAt := strings.Index(r.stdout, "asking for:\n  api\n")
	urlAt := strings.Index(r.stdout, "Open this URL in your browser")
	if scopesAt < 0 || urlAt < 0 || scopesAt > urlAt {
		t.Errorf("stdout does not list the scopes before the URL:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout, "Signed in as bob (profile \"default\", token in the keyring).") {
		t.Errorf("stdout:\n%s", r.stdout)
	}
	q := b.authorizeQuery(t)
	if q.Get("client_id") != gitlabtest.ClientID || q.Get("scope") != "api" || q.Get("code_challenge_method") != "S256" {
		t.Errorf("authorize query %v", q)
	}
	if q.Has("resource") {
		t.Error("the authorize request carries a resource parameter")
	}

	uc, err := userconfig.Dir(env[config.EnvConfigDir]).Load("default")
	if err != nil {
		t.Fatal(err)
	}
	if uc.Instance != srv.URL || uc.ClientID != gitlabtest.ClientID || uc.Username != "bob" ||
		strings.Join(uc.Scopes, " ") != "api" || uc.TokenStore != "keyring" {
		t.Errorf("profile %+v", uc)
	}
	if _, ok := keyringBackend.(*packageKeyring).get("default"); !ok {
		t.Error("no token in the keyring")
	}
}

func TestLoginWithoutAnApplicationSaysHowToRegisterOne(t *testing.T) {
	env := home(t)
	env[config.EnvReadOnly] = "true"
	r := runWith(env, nil, "login")
	if r.code != 1 {
		t.Fatalf("code %d", r.code)
	}
	for _, want := range []string{"Redirect URI:  http://127.0.0.1/callback", "Confidential:  unchecked",
		"Scopes:        read_api", "--client-id"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, r.stderr)
		}
	}
}

func TestLoginToAConfidentialApplication(t *testing.T) {
	env := home(t)
	srv := gitlabtest.New(t, gitlabtest.Options{Confidential: true, ClientSecret: "not-sent"})
	env[config.EnvInstance] = srv.URL
	env[config.EnvClientID] = gitlabtest.ClientID
	useBrowser(t)
	r := runWith(env, nil, "login")
	if r.code != 1 || !strings.Contains(r.stderr, `untick "Confidential"`) {
		t.Errorf("login to a confidential application: %+v", r)
	}
	if _, ok := keyringBackend.(*packageKeyring).get("default"); ok {
		t.Error("a failed login stored a token")
	}
}

func TestANamedProfileIsUsedOnlyWhenNamed(t *testing.T) {
	env, _, _ := signedIn(t, gitlabtest.Options{}, "--profile", "work")
	status := func(extra ...string) (profile string, resolved bool) {
		r := runWith(env, nil, append([]string{"status", "--json", "--no-probe"}, extra...)...)
		var got struct {
			Profile     string `json:"profile"`
			Credentials struct {
				Resolved bool `json:"resolved"`
			} `json:"credentials"`
		}
		if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
			t.Fatalf("%v: %s", err, r.stdout)
		}
		return got.Profile, got.Credentials.Resolved
	}
	// As in the sibling servers: no pointer, so an unnamed run is "default".
	if p, ok := status(); p != "default" || ok {
		t.Errorf("unnamed: profile %q resolved %v, want default and not signed in", p, ok)
	}
	if p, ok := status("--profile", "work"); p != "work" || !ok {
		t.Errorf("named: profile %q resolved %v, want work and signed in", p, ok)
	}
}

func TestLogoutRevokesAndNamesProfilesOnTheSameApplication(t *testing.T) {
	env, srv, _ := signedIn(t, gitlabtest.Options{})
	if r := runWith(env, nil, "login", "--profile", "second"); r.code != 0 {
		t.Fatalf("second login: %+v", r)
	}
	stored, _ := keyringBackend.(*packageKeyring).get("default")
	var pair struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal([]byte(stored), &pair); err != nil || pair.RefreshToken == "" {
		t.Fatalf("stored pair %q: %v", stored, err)
	}

	r := runWith(env, nil, "logout")
	if r.code != 0 {
		t.Fatalf("logout: %+v", r)
	}
	for _, want := range []string{"same OAuth application", "second", "Revoked the token at GitLab.",
		`Signed out of profile "default".`} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, r.stdout)
		}
	}
	if _, ok := keyringBackend.(*packageKeyring).get("default"); ok {
		t.Error("the token is still in the keyring")
	}
	if _, ok := keyringBackend.(*packageKeyring).get("second"); !ok {
		t.Error("logout removed another profile's token")
	}
	if _, err := userconfig.Dir(env[config.EnvConfigDir]).Load("default"); err == nil {
		t.Error("the profile is still there")
	}
	// Revoked at the instance, not only forgotten here.
	resp, err := http.PostForm(srv.URL+"/oauth/token", url.Values{ //nolint:noctx // test
		"grant_type": {"refresh_token"}, "refresh_token": {pair.RefreshToken}, "client_id": {gitlabtest.ClientID}})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("the revoked refresh token answered %d", resp.StatusCode)
	}

	r = runWith(env, nil, "logout")
	if r.code != 0 || !strings.Contains(r.stdout, "No stored token to revoke.") {
		t.Errorf("second logout: %+v", r)
	}
}
