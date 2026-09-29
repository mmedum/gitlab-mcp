package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/internal/config"
	"github.com/mmedum/gitlab-mcp/v2/internal/gapi/gitlabtest"
	"github.com/mmedum/gitlab-mcp/v2/internal/version"
)

type result struct {
	code           int
	stdout, stderr string
}

func runWith(env map[string]string, stdin io.Reader, args ...string) result {
	var out, errb bytes.Buffer
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	code := run(args, stdin, &out, &errb, func(k string) string { return env[k] })
	return result{code, out.String(), errb.String()}
}

// home gives the test its own home and config directory inside it.
func home(t *testing.T) map[string]string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Cleanup(keyringBackend.(*packageKeyring).reset)
	return map[string]string{config.EnvConfigDir: filepath.Join(h, "cfg")}
}

// browser stands in for the person's browser: it follows the
// authorization URL through GitLab's redirect to the callback.
type browser struct {
	mu     sync.Mutex
	opened []string
}

func useBrowser(t *testing.T) *browser {
	t.Helper()
	b := &browser{}
	prev, prevTimeout := openBrowser, loginTimeout
	openBrowser = func(raw string) error {
		b.mu.Lock()
		b.opened = append(b.opened, raw)
		b.mu.Unlock()
		go func() {
			resp, err := http.Get(raw) //nolint:noctx,gosec // the test's browser
			if err == nil {
				_ = resp.Body.Close()
			}
		}()
		return nil
	}
	loginTimeout = 10 * time.Second
	t.Cleanup(func() { openBrowser, loginTimeout = prev, prevTimeout })
	return b
}

func (b *browser) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.opened)
}

func TestHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}, {"-help"}} {
		r := runWith(nil, nil, args...)
		if r.code != 0 || !strings.Contains(r.stdout, "Usage:") || !strings.Contains(r.stdout, "gitlab-mcp login") {
			t.Errorf("%v: code %d, stdout %q", args, r.code, r.stdout)
		}
	}
}

func TestACommandsHelpShowsItsFlags(t *testing.T) {
	for _, args := range [][]string{{"help", "login"}, {"login", "--help"}, {"login", "-h"}} {
		r := runWith(nil, nil, args...)
		if r.code != 0 || !strings.Contains(r.stdout, "Usage: gitlab-mcp login") ||
			!strings.Contains(r.stdout, "-no-browser") || !strings.Contains(r.stdout, "-client-id") {
			t.Errorf("%v: code %d, stdout %q", args, r.code, r.stdout)
		}
	}
	r := runWith(nil, nil, "status", "--help")
	if !strings.Contains(r.stdout, "-no-probe") || !strings.Contains(r.stdout, "-json") {
		t.Errorf("status --help: %q", r.stdout)
	}
}

func TestAHelpTokenIsNeverReadAsAValue(t *testing.T) {
	env := home(t)
	srv := gitlabtest.New(t, gitlabtest.Options{})
	env[config.EnvTestInstance] = srv.URL
	b := useBrowser(t)

	// --help after a flag that takes a value is still help, not an
	// application id called "--help".
	r := runWith(env, nil, "login", "--client-id", "--help")
	if r.code != 0 || !strings.Contains(r.stdout, "Usage: gitlab-mcp login") {
		t.Fatalf("login --client-id --help: %+v", r)
	}
	if b.count() != 0 || len(srv.Requests()) != 0 {
		t.Errorf("help started a login: %d browser opens, %d requests", b.count(), len(srv.Requests()))
	}

	// logout --help deletes nothing.
	env[config.EnvClientID] = gitlabtest.ClientID
	if r := runWith(env, nil, "login"); r.code != 0 {
		t.Fatalf("login: %+v", r)
	}
	srv.ResetRequests()
	for _, args := range [][]string{{"logout", "--help"}, {"logout", "-h"}, {"logout", "--profile", "help"}} {
		r := runWith(env, nil, args...)
		if r.code != 0 || !strings.Contains(r.stdout, "Usage: gitlab-mcp logout") {
			t.Errorf("%v: %+v", args, r)
		}
	}
	if _, ok := keyringBackend.(*packageKeyring).get("default"); !ok {
		t.Error("logout --help deleted the token")
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("logout --help made %d requests", n)
	}
}

func TestAnUnknownCommandPrintsUsageToStderr(t *testing.T) {
	for _, args := range [][]string{{"statsu"}, {"statsu", "--help"}} {
		r := runWith(nil, nil, args...)
		if r.code == 0 || !strings.Contains(r.stderr, "Usage:") || !strings.Contains(r.stderr, `unknown command "statsu"`) {
			t.Errorf("%v: code %d, stderr %q", args, r.code, r.stderr)
		}
		if r.stdout != "" {
			t.Errorf("%v: stdout carried %q", args, r.stdout)
		}
	}
}

func TestVersion(t *testing.T) {
	want := version.Info() + "\n"
	for _, args := range [][]string{{"version"}, {"--version"}, {"serve", "--version"}} {
		r := runWith(nil, nil, args...)
		if r.code != 0 || r.stdout != want {
			t.Errorf("%v: code %d, stdout %q, want %q", args, r.code, r.stdout, want)
		}
	}
	if !strings.HasPrefix(want, "gitlab-mcp ") {
		t.Errorf("version line %q", want)
	}
	if r := runWith(nil, nil, "version", "extra"); r.code != 1 {
		t.Errorf("version extra: %+v", r)
	}
}

// TestTheVersionFallsBackToTheBuildInfo checks the line a binary built
// without ldflags prints: `go install` sets none, and the build info
// then names the module version, or the binary says "dev".
func TestTheVersionFallsBackToTheBuildInfo(t *testing.T) {
	prev := version.Version
	t.Cleanup(func() { version.Version = prev })
	version.Version = "dev"
	r := runWith(nil, nil, "version")
	// A test binary's main module is "(devel)", so the fallback lands on
	// "dev".
	if !strings.HasPrefix(r.stdout, "gitlab-mcp dev (") {
		t.Errorf("without ldflags: %q", r.stdout)
	}
	version.Version = "1.2.3"
	r = runWith(nil, nil, "version")
	if !strings.HasPrefix(r.stdout, "gitlab-mcp v1.2.3 (") {
		t.Errorf("with ldflags: %q", r.stdout)
	}
}

func TestArgumentErrors(t *testing.T) {
	env := home(t)
	env[config.EnvReadOnly] = "maybe"
	if r := runWith(env, nil, "doctor"); r.code != 1 || !strings.Contains(r.stderr, config.EnvReadOnly) {
		t.Errorf("bad setting: %+v", r)
	}
	delete(env, config.EnvReadOnly)
	if r := runWith(env, nil, "logout", "extra"); r.code != 1 || !strings.Contains(r.stderr, "takes no arguments") {
		t.Errorf("extra argument: %+v", r)
	}
	if r := runWith(env, nil, "status", "-nope"); r.code != 2 || !strings.Contains(r.stderr, "-nope") {
		t.Errorf("unknown flag: %+v", r)
	}
	if r := runWith(env, nil, "--nope"); r.code != 2 {
		t.Errorf("serve with an unknown flag: %+v", r)
	}
}

func TestDumpSchemasNeedsNoConfigurationOrCredentials(t *testing.T) {
	// A setting that would stop the server does not stop the dump.
	env := map[string]string{config.EnvReadOnly: "maybe"}
	r := runWith(env, nil, "--dump-schemas")
	if r.code != 0 {
		t.Fatalf("dump: %+v", r)
	}
	if !json.Valid([]byte(r.stdout)) {
		t.Errorf("the dump is not JSON: %.200q", r.stdout)
	}
}

func TestServeSignedOutEndsCleanlyOnAClosedStdin(t *testing.T) {
	env := home(t)
	r := runWith(env, strings.NewReader(""))
	if r.code != 0 {
		t.Fatalf("serve: %+v", r)
	}
	if r.stdout != "" {
		t.Errorf("stdout carried %q with no request", r.stdout)
	}
	if !strings.Contains(r.stderr, "no usable sign-in") {
		t.Errorf("stderr %q does not say the server is signed out", r.stderr)
	}
}

func TestServeRefusesATokenThatCannotServeTheMode(t *testing.T) {
	env := home(t)
	srv := gitlabtest.New(t, gitlabtest.Options{})
	env[config.EnvTestInstance] = srv.URL
	env[config.EnvClientID] = gitlabtest.ClientID
	env[config.EnvReadOnly] = "true"
	useBrowser(t)
	if r := runWith(env, nil, "login"); r.code != 0 {
		t.Fatalf("login: %+v", r)
	}
	delete(env, config.EnvReadOnly)
	r := runWith(env, strings.NewReader(""))
	if r.code != 1 || !strings.Contains(r.stderr, "[auth]") || !strings.Contains(r.stderr, "gitlab-mcp login") ||
		!strings.Contains(r.stderr, "needs api") {
		t.Errorf("serve with writes on a read_api sign-in: %+v", r)
	}
	if r.stdout != "" {
		t.Errorf("stdout carried %q", r.stdout)
	}
}

func TestErrorsArePrintedThroughTheRedactor(t *testing.T) {
	var b bytes.Buffer
	fail(&b, "refused for %s at %s", "someone@example.com", "https://gitlab.example.com/example-group/private")
	for _, leak := range []string{"someone@", "gitlab.example.com", "private"} {
		if strings.Contains(b.String(), leak) {
			t.Errorf("%q carries %q", b.String(), leak)
		}
	}
	if !strings.HasPrefix(b.String(), "gitlab-mcp: refused for ") {
		t.Errorf("line %q", b.String())
	}
}

// authorizeQuery is the query of the authorization URL a login opened.
func (b *browser) authorizeQuery(t *testing.T) url.Values {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.opened) == 0 {
		t.Fatal("no browser was opened")
	}
	u, err := url.Parse(b.opened[len(b.opened)-1])
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}
