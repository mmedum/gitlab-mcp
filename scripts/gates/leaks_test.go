package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tDomain is a domain somebody could own, split so this file does not
// carry what it tests for.
const tDomain = "corp-widgets" + ".io"

// Every exemption is an argued decision, so each carries its reason.
func TestLeaksAllowListHasReasons(t *testing.T) {
	entries := 0
	for _, rule := range leaksRules {
		for _, a := range rule.allow {
			entries++
			if len(strings.TrimSpace(a.reason)) < 10 {
				t.Errorf("%s: the exemption %s has no reason worth the name", rule.name, a.re)
			}
		}
	}
	for _, m := range []map[string]string{leaksPublicHosts, leaksGitLabPaths, leaksSkip, leaksBinaryAllowed} {
		for k, reason := range m {
			entries++
			if len(strings.TrimSpace(reason)) < 10 {
				t.Errorf("%s is exempt with no reason worth the name", k)
			}
		}
	}
	// The count is stated, so a new exemption changes this test too.
	if want := 12 + len(leaksPublicHosts) + len(leaksGitLabPaths) + len(leaksSkip) + len(leaksBinaryAllowed); entries != want {
		t.Errorf("%d exemptions, want %d", entries, want)
	}
	if len(leaksPublicHosts) != 24 || len(leaksGitLabPaths) != 6 || len(leaksSkip) != 4 || len(leaksBinaryAllowed) != 0 {
		t.Errorf("exemption lists are %d hosts, %d paths, %d skipped files, %d binaries; want 24, 6, 4, 0",
			len(leaksPublicHosts), len(leaksGitLabPaths), len(leaksSkip), len(leaksBinaryAllowed))
	}
}

func TestLeaksFind(t *testing.T) {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	b64url := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	cases := []struct {
		name, text, want string // want empty: clean
	}{
		{"a reserved address", "alice@example.com and bob@gitlab.example.com and x@y.invalid and a@b.co.test", ""},
		{"a real-looking address", "mail carol@" + tDomain + " today", "an address at a domain somebody could own"},
		{"the co-author trailer", "Co-Authored-By: Claude <noreply@anthropic.com>", ""},
		{"another vendor address", "ask support@" + "anthropic.com", "an address at a vendor's no-reply domain"},
		{"a GitHub no-reply", "12345+someone@users.noreply.github.com", ""},
		{"a reserved host", "https://gitlab.example.com:8443/api/v4 and http://[::1]:8080/x and http://127.0.0.1:3000/", ""},
		{"a public host", "see https://docs.gitlab.com/security/tokens/ and https://github.com/mmedum/gitlab-mcp", ""},
		{"a host somebody owns", "clone https://" + "git." + tDomain + "/team/app", "a URL to a host outside"},
		{"a single-label host", "https://" + "gitlab/api/v4", "a URL to a host outside"},
		{"userinfo does not fool the host", "https://gitlab.example.com@" + "git." + tDomain + "/x", "a URL to a host outside"},
		{"a lookalike suffix", "https://gitlab.example.com." + tDomain + "/x", "a URL to a host outside"},
		{"a template has no host", "https://{host}/api and https://<instance>/x", ""},
		{"a URL inside a regular expression", `regexp.MustCompile('https://github\.com/mmedum/gitlab-mcp')`, ""},
		{"an escaped host somebody owns", "https://" + `git\.` + tDomain + `/x`, "a URL to a host outside"},
		{"a fixture namespace", "https://gitlab.com/example-group/alpha and gitlab.com/alice", ""},
		{"GitLab's own source", "https://gitlab.com/gitlab-org/gitlab/-/issues/1 and https://docs.gitlab.com/api/", ""},
		{"a real namespace", "https://gitlab.com/" + "corp-widgets/app", "gitlab.com/ followed by a namespace"},
		{"a namespace without the scheme", "at gitlab.com/" + "corp-widgets/app", "gitlab.com/ followed by a namespace"},
		{"another repository of the owner", "github.com/mmedum/" + "other-server", "another repository of this repository's owner"},
		{"a synthetic token", "glpat-EXAMPLE0123456789abcdefghij and GR1348941FAKEabcdefghijklmnopqrst", ""},
		{"a token", "token glpat-" + strings.Repeat("Ab1", 8), "a GitLab token"},
		{"a session cookie", "_gitlab_session=" + strings.Repeat("9f", 16), "a GitLab token"},
		{"a synthetic application id", "client_id: " + strings.Repeat("0", 64), ""},
		{"an application id", "client_id=" + strings.Repeat("3c", 32), "an OAuth application id"},
		{"a bare SHA-256", "sha256 " + strings.Repeat("3c", 32), ""},
		{"a leak inside base64", "content: " + b64("see https://"+"git."+tDomain+"/team/app today"), "inside base64"},
		{"a leak inside base64url", "c=" + b64url("mail carol@"+tDomain+" please, thanks"), "inside base64"},
		{"clean base64", "content: " + b64("package main\n\nfunc main() {}\n"), ""},
		{"a hash is not base64 text", strings.Repeat("a1b2c3d4", 8), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := leaksFind(tc.text)
			switch {
			case tc.want == "" && len(got) > 0:
				t.Errorf("findings on clean text: %q", got)
			case tc.want != "":
				wantProblem(t, got, tc.want)
			}
			for _, f := range got {
				if strings.Contains(f, "corp-widgets") {
					t.Errorf("a finding reprints what it found: %q", f)
				}
			}
		})
	}
}

// leaksFixtureFiles is a clean tree large enough for the floor.
func leaksFixtureFiles(n int) map[string]string {
	files := map[string]string{"go.sum": "example.com/m v1.0.0 h1:carol@" + tDomain + "\n"}
	for i := range n {
		files[fmt.Sprintf("docs/f%02d.md", i)] = "alice@example.com at https://gitlab.example.com\n"
	}
	return files
}

func TestLeaksTree(t *testing.T) {
	dir := gitRepo(t, leaksFixtureFiles(5))
	var out bytes.Buffer
	if err := leaksTree(&out, dir, 5); err != nil {
		t.Fatalf("clean tree: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "5 text files") {
		t.Errorf("the gate did not say how much it read: %q", out.String())
	}
	if err := leaksTree(&out, dir, 6); err == nil || !strings.Contains(err.Error(), "read 5 text file(s), want at least 6") {
		t.Errorf("below the floor: %v", err)
	}

	// An untracked, unignored file is read; an ignored one is not.
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", []byte("ignored.txt\n"))
	write("ignored.txt", []byte("carol@"+tDomain+"\n"))
	out.Reset()
	if err := leaksTree(&out, dir, 5); err != nil {
		t.Fatalf("an ignored file was read: %v\n%s", err, out.String())
	}
	write("new.md", []byte("carol@"+tDomain+"\n"))
	out.Reset()
	if err := leaksTree(&out, dir, 5); err == nil || !strings.Contains(out.String(), "new.md: an address") {
		t.Errorf("an untracked file was not read: %v\n%s", err, out.String())
	}
	if err := os.Remove(filepath.Join(dir, "new.md")); err != nil {
		t.Fatal(err)
	}

	for name, data := range map[string][]byte{
		"built":    append([]byte("\x7fELF\x02\x01\x01"), 0, 0, 0),
		"blob.bin": {0x89, 'P', 'N', 'G', 0, 0, 0},
	} {
		write(name, data)
		out.Reset()
		if err := leaksTree(&out, dir, 5); err == nil || !strings.Contains(out.String(), name+": ") {
			t.Errorf("a binary %s passed: %v\n%s", name, err, out.String())
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLeaksHistory(t *testing.T) {
	dir := gitRepo(t, leaksFixtureFiles(2))
	gitCommit(t, dir, "add a leak", map[string]string{"docs/f00.md": "carol@" + tDomain + "\n"})
	gitCommit(t, dir, "and remove it", map[string]string{"docs/f00.md": "alice@example.com\n"})
	var out bytes.Buffer
	if err := leaksTree(&out, dir, 2); err != nil {
		t.Fatalf("the tip is clean and the tree scan failed: %v", err)
	}
	out.Reset()
	err := leaksScanHistory(&out, dir)
	if err == nil || !strings.Contains(out.String(), "docs/f00.md@") {
		t.Errorf("a leak removed from the tip was not found in the history: %v\n%s", err, out.String())
	}

	clean := gitRepo(t, leaksFixtureFiles(2))
	gitCommit(t, clean, "mention https://"+"git."+tDomain+" in a message", nil)
	out.Reset()
	if err := leaksScanHistory(&out, clean); err == nil || !strings.Contains(out.String(), "message: a URL") {
		t.Errorf("a commit message was not read: %v\n%s", err, out.String())
	}

	tagged := gitRepo(t, leaksFixtureFiles(2))
	gitDo(t, tagged, "tag", "-a", "v0.1.0", "-m", "for carol@"+tDomain+"")
	out.Reset()
	if err := leaksScanHistory(&out, tagged); err == nil || !strings.Contains(out.String(), "tag ") {
		t.Errorf("a tag message was not read: %v\n%s", err, out.String())
	}

	// Author lines are git's own and are skipped.
	quiet := gitRepo(t, leaksFixtureFiles(2))
	gitDo(t, quiet, "-c", "user.email=carol@"+tDomain+"", "commit", "-q", "--allow-empty", "-m", "fine")
	out.Reset()
	if err := leaksScanHistory(&out, quiet); err != nil {
		t.Errorf("an author line was read as a leak: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "2 commits") {
		t.Errorf("the scan did not say it read both commits: %q", out.String())
	}
}
