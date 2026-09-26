package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mmedum/gitlab-mcp/internal/config"
	"github.com/mmedum/gitlab-mcp/internal/gapi/gitlabtest"
)

type statusJSON struct {
	SchemaVersion int     `json:"schema_version"`
	Reason        *string `json:"reason"`
	Profile       string  `json:"profile"`
	Instance      *string `json:"instance"`
	InstanceKind  *string `json:"instance_kind"`
	ClientID      *string `json:"client_id"`
	Account       *string `json:"account"`
	Credentials   struct {
		Resolved   bool    `json:"resolved"`
		TokenStore *string `json:"token_store"`
		Reason     *string `json:"reason"`
	} `json:"credentials"`
	Scopes struct {
		Granted  []string `json:"granted"`
		Required []string `json:"required"`
		Missing  []string `json:"missing"`
	} `json:"scopes"`
	Probe struct {
		Ran    bool    `json:"ran"`
		OK     bool    `json:"ok"`
		Reason *string `json:"reason"`
	} `json:"probe"`
}

func statusOf(t *testing.T, r result) statusJSON {
	t.Helper()
	var s statusJSON
	if err := json.Unmarshal([]byte(r.stdout), &s); err != nil {
		t.Fatalf("status --json is not one JSON object: %v\n%s", err, r.stdout)
	}
	return s
}

func deref0(s *string) string {
	if s == nil {
		return "<null>"
	}
	return *s
}

func TestStatusAfterLogin(t *testing.T) {
	env, srv, _ := signedIn(t, gitlabtest.Options{})
	srv.ResetRequests()

	r := runWith(env, nil, "status", "--json")
	if r.code != 0 {
		t.Fatalf("status: %+v", r)
	}
	s := statusOf(t, r)
	if s.SchemaVersion != 1 || s.Profile != "default" || !s.Credentials.Resolved ||
		deref0(s.Credentials.TokenStore) != "keyring" || !s.Probe.Ran || !s.Probe.OK {
		t.Errorf("status %+v", s)
	}
	// The account is masked; the loopback instance names nobody.
	if deref0(s.Account) != "{user 1}" || deref0(s.Instance) != srv.URL {
		t.Errorf("account %s, instance %s", deref0(s.Account), deref0(s.Instance))
	}
	if strings.Join(s.Scopes.Granted, " ") != "api" || len(s.Scopes.Missing) != 0 {
		t.Errorf("scopes %+v", s.Scopes)
	}
	if strings.Contains(r.stdout, "alice") {
		t.Error("status --json names the account")
	}
	if n := len(srv.Requests()); n != 1 {
		t.Errorf("status made %d requests, want the one probe", n)
	}

	srv.ResetRequests()
	r = runWith(env, nil, "status", "--no-probe")
	if r.code != 0 || !strings.Contains(r.stdout, "probe:          skipped (--no-probe)") {
		t.Errorf("status --no-probe: %+v", r)
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("--no-probe made %d requests", n)
	}
	for _, want := range []string{"profile:        default", "account:        {user 1}", "token store:    keyring",
		"scopes needed:  api", "redacted: 1 client-id, 1 user"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("status lacks %q:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, "alice") {
		t.Error("status names the account")
	}
}

func TestStatusSignedOut(t *testing.T) {
	env := home(t)
	r := runWith(env, nil, "status", "--json")
	if r.code != 1 {
		t.Errorf("code %d, want 1 for a server that would start signed out", r.code)
	}
	s := statusOf(t, r)
	if s.Credentials.Resolved || s.Credentials.Reason == nil || s.Probe.Ran {
		t.Errorf("status %+v", s)
	}
	if deref0(s.InstanceKind) != "gitlab.com" {
		t.Errorf("instance kind %s", deref0(s.InstanceKind))
	}
}

func TestStatusRunsTheServersConfigurationLoad(t *testing.T) {
	env := home(t)
	env[config.EnvLogLevel] = "loud"
	r := runWith(env, nil, "status", "--json")
	s := statusOf(t, r)
	if r.code != 1 || s.Reason == nil || !strings.Contains(*s.Reason, config.EnvLogLevel) {
		t.Errorf("a setting the server refuses: code %d, reason %s", r.code, deref0(s.Reason))
	}
}

func TestStatusNamesAScopeTheLastLoginLacks(t *testing.T) {
	env, _, _ := signedIn(t, gitlabtest.Options{}, "--read-only")
	r := runWith(env, nil, "status", "--no-probe", "--json")
	s := statusOf(t, r)
	if r.code != 1 || strings.Join(s.Scopes.Missing, " ") != "api" {
		t.Errorf("code %d, scopes %+v", r.code, s.Scopes)
	}
}

func TestDoctorAfterLogin(t *testing.T) {
	env, _, _ := signedIn(t, gitlabtest.Options{Version: "19.3.2-ee", Enterprise: true})
	r := runWith(env, nil, "doctor")
	if r.code != 0 {
		t.Fatalf("doctor: %+v", r)
	}
	order := []string{"[ok  ] instance", "[ok  ] connection", "[ok  ] sign-in", "[ok  ] version and edition",
		"GitLab 19.3.2-ee, Enterprise Edition", "[ok  ] application", "[ok  ] granted scopes", "[ok  ] writes", "anywhere the token can write", "[ok  ] /user",
		"signed in as {user 1}", "No problems found.", "redacted: "}
	last := -1
	for _, want := range order {
		at := strings.Index(r.stdout, want)
		if at < 0 || at < last {
			t.Errorf("doctor output lacks %q in order:\n%s", want, r.stdout)
			break
		}
		last = at
	}
	for _, leak := range []string{"alice", gitlabtest.ClientID} {
		if strings.Contains(r.stdout, leak) {
			t.Errorf("doctor printed %q", leak)
		}
	}
}

func TestDoctorSignedOut(t *testing.T) {
	env := home(t)
	srv := gitlabtest.New(t, gitlabtest.Options{})
	env[config.EnvTestInstance] = srv.URL
	r := runWith(env, nil, "doctor")
	if r.code != 1 || !strings.Contains(r.stdout, "[FAIL] sign-in") || !strings.Contains(r.stdout, "--client-id") ||
		!strings.Contains(r.stdout, "1 problem(s).") {
		t.Errorf("doctor signed out: %+v", r)
	}
}

func TestDoctorAfterRevocationSaysToLogIn(t *testing.T) {
	env, srv, _ := signedIn(t, gitlabtest.Options{})
	stored, _ := keyringBackend.(*packageKeyring).get("default")
	var pair struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal([]byte(stored), &pair); err != nil {
		t.Fatal(err)
	}
	srv.Revoke(pair.AccessToken)
	srv.Revoke(pair.RefreshToken)
	r := runWith(env, nil, "doctor")
	if r.code != 1 || !strings.Contains(r.stdout, "gitlab-mcp login") {
		t.Errorf("doctor after revocation: %+v", r)
	}
}

func TestDoctorNamesAnUntrustedCertificate(t *testing.T) {
	env := home(t)
	tlsSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	tlsSrv.Config.ErrorLog = log.New(io.Discard, "", 0)
	tlsSrv.StartTLS()
	t.Cleanup(tlsSrv.Close)
	env[config.EnvTestInstance] = tlsSrv.URL
	r := runWith(env, nil, "doctor")
	if r.code != 1 || !strings.Contains(r.stdout, "[FAIL] TLS") || !strings.Contains(r.stdout, "trust store") {
		t.Errorf("doctor on an untrusted certificate: %+v", r)
	}
}

func TestDoctorNamesAnUnreachableInstance(t *testing.T) {
	env := home(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	env[config.EnvTestInstance] = "http://" + ln.Addr().String()
	_ = ln.Close()
	r := runWith(env, nil, "doctor")
	if r.code != 1 || !strings.Contains(r.stdout, "could not be reached") {
		t.Errorf("doctor: %+v", r)
	}
}

// TestDoctorRefusesATestInstanceThatIsNotLoopback: the override can
// never point the token at a real host, and the refusal does not name
// the host it refused.
func TestDoctorRefusesATestInstanceThatIsNotLoopback(t *testing.T) {
	for _, raw := range []string{"http://gitlab.example.invalid", "https://gitlab.example.invalid"} {
		env := home(t)
		env[config.EnvTestInstance] = raw
		r := runWith(env, nil, "doctor")
		if r.code != 1 || !strings.Contains(r.stderr, config.EnvTestInstance) {
			t.Fatalf("%s: doctor: %+v", raw, r)
		}
		if strings.Contains(r.stdout+r.stderr, "gitlab.example.invalid") {
			t.Errorf("%s: the refused host is named:\n%s%s", raw, r.stdout, r.stderr)
		}
	}
}

func TestDoctorSaysWhereWritesAreConfined(t *testing.T) {
	env, _, _ := signedIn(t, gitlabtest.Options{})
	env[config.EnvWriteNamespaces] = "example-group,example-other"
	r := runWith(env, nil, "doctor")
	if r.code != 0 || !strings.Contains(r.stdout, "confined to 2 namespace(s) by "+config.EnvWriteNamespaces) {
		t.Errorf("doctor: %+v", r)
	}
}

// TestUnreachableNamesNoHost: net/http wraps a transport failure in a
// *url.Error carrying the URL, a lookup names the host and the resolver,
// and a certificate for the wrong host names both hosts. None of it may
// reach doctor's output, which the bug form asks for.
func TestUnreachableNamesNoHost(t *testing.T) {
	const host = "canary-host.example.net"
	inURL := func(err error) error {
		return &url.Error{Op: "Get", URL: "https://" + host + "/api/v4/metadata", Err: err}
	}
	for name, c := range map[string]struct {
		err  error
		want string
	}{
		"lookup": {inURL(&net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Name: host, Server: "10.9.8.7:53",
			Err: "no such host", IsNotFound: true}}), "could not be reached: lookup: no such host"},
		"wrong host name": {inURL(&tls.CertificateVerificationError{Err: x509.HostnameError{
			Certificate: &x509.Certificate{DNSNames: []string{"canary-cert.example.net"}}, Host: host}}), "trust store"},
		"url error in a url error": {inURL(inURL(errors.New("proxyconnect " + host))), "could not be reached: the connection failed"},
		"deadline":                 {inURL(context.DeadlineExceeded), "could not be reached: timed out"},
	} {
		got := unreachable(c.err)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: %q, want it to contain %q", name, got, c.want)
		}
		if strings.Contains(got, "canary") || strings.Contains(got, "10.9.8.7") {
			t.Errorf("%s: %q names a host", name, got)
		}
	}
}
