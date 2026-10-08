package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/mmedum/gitlab-mcp/v2/scripts/internal/gatekit"
)

// mcpbTestDescription is the server's description in the fixture root.
const mcpbTestDescription = "GitLab over MCP from your own account: issues, merge requests, reviews, the repository and CI."

// mcpbFixture decodes the good manifest fixture as a document.
func mcpbFixture(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "mcpb", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// mcpbRoot writes a manifest document into a fresh repository root,
// beside what the gate reads from the rest of the repository.
func mcpbRoot(t *testing.T, doc map[string]any) string {
	t.Helper()
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return repoTree(t, map[string]string{
		mcpbManifestPath:            string(raw),
		"LICENSE":                   "                                 Apache License\n",
		"NOTICE":                    "gitlab-mcp\n",
		"internal/server/server.go": "package server\n\n// Description is shared.\nconst Description = " + mcpbQuote(mcpbTestDescription) + "\n",
	})
}

func mcpbQuote(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

// mcpbDig returns the object at a dotted path in a decoded document.
func mcpbDig(doc map[string]any, keys ...string) map[string]any {
	cur := doc
	for _, k := range keys {
		cur = cur[k].(map[string]any)
	}
	return cur
}

func TestMcpbFixturePasses(t *testing.T) {
	var out bytes.Buffer
	if err := mcpbCheck(&out, mcpbRoot(t, mcpbFixture(t))); err != nil {
		t.Fatalf("the fixture fails: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "7 staged files, 3 platforms, 4 user_config keys") {
		t.Errorf("the gate does not say what it read: %s", out.String())
	}
}

// Each break, one at a time, refused with its own message.
func TestMcpbRefusesEachBreak(t *testing.T) {
	const pinned = "https://raw.githubusercontent.com/anthropics/mcpb/%s/schemas/mcpb-manifest-v%s.schema.json"
	name := gatekit.BinaryName
	cases := []struct {
		name   string
		mutate func(doc map[string]any)
		want   string
	}{
		{"entry point not staged", func(d map[string]any) {
			mcpbDig(d, "server")["entry_point"] = "server/nothing"
		}, `entry_point "server/nothing"`},
		{"command not staged", func(d map[string]any) {
			mcpbDig(d, "server", "mcp_config")["command"] = "${__dirname}/server/typo"
		}, "mcp_config.command is"},
		{"win32 override typo", func(d map[string]any) {
			mcpbDig(d, "server", "mcp_config", "platform_overrides", "win32")["command"] = "${__dirname}/server/gitlab.exe"
		}, "platform_overrides.win32.command"},
		{"win32 override deleted", func(d map[string]any) {
			delete(mcpbDig(d, "server", "mcp_config", "platform_overrides"), "win32")
		}, `platform "win32" runs "server/` + name + `-darwin"`},
		{"linux runs a binary, not the launcher", func(d map[string]any) {
			mcpbDig(d, "server", "mcp_config", "platform_overrides", "linux")["command"] = "${__dirname}/server/" + name + "-linux-amd64"
		}, `platform "linux" runs "server/` + name + `-linux-amd64", which is staged for "the launcher"`},
		{"override for an unclaimed platform", func(d map[string]any) {
			mcpbDig(d, "compatibility")["platforms"] = []any{"darwin", "win32"}
		}, `platform_overrides has "linux"`},
		{"composed user_config not declared", func(d map[string]any) {
			mcpbDig(d, "server", "mcp_config", "env")["GITLAB_MCP_CONFIG_DIR"] = "${user_config.home}/gitlab"
		}, "spends ${user_config.home}"},
		{"an optional user_config key with no default", func(d map[string]any) {
			delete(mcpbDig(d, "user_config", "upload_dirs"), "default")
		}, "spends ${user_config.upload_dirs}, which is optional with no default"},
		{"a user_config key with multiple values", func(d map[string]any) {
			mcpbDig(d, "user_config", "upload_dirs")["multiple"] = true
		}, "spends ${user_config.upload_dirs}, which takes multiple values"},
		{"an env var the server does not read", func(d map[string]any) {
			mcpbDig(d, "server", "mcp_config", "env")["GITLAB_MCP_NOPE"] = "x"
		}, "sets GITLAB_MCP_NOPE, which the server does not read"},
		{"the development override", func(d map[string]any) {
			mcpbDig(d, "server", "mcp_config", "env")["GITLAB_MCP_TEST_INSTANCE"] = "http://127.0.0.1:1"
		}, "sets GITLAB_MCP_TEST_INSTANCE, a development override no bundle may carry"},
		{"a required user_config key missing", func(d map[string]any) {
			delete(mcpbDig(d, "user_config"), "read_only")
			delete(mcpbDig(d, "server", "mcp_config", "env"), "GITLAB_MCP_READ_ONLY")
		}, "user_config does not ask for read_only"},
		{"a user_config key wired to the wrong variable", func(d map[string]any) {
			mcpbDig(d, "server", "mcp_config", "env")["GITLAB_MCP_PROFILE"] = "${user_config.read_only}"
		}, `mcp_config.env.GITLAB_MCP_PROFILE is "${user_config.read_only}"`},
		{"description drifts from the constant", func(d map[string]any) { d["description"] = "GitLab tools." },
			"one constant feeds both"},
		{"no $schema", func(d map[string]any) { delete(d, "$schema") }, "no $schema"},
		{"unpinned schema path", func(d map[string]any) {
			d["$schema"] = "https://raw.githubusercontent.com/anthropics/mcpb/main/dist/mcpb-manifest.schema.json"
		}, "not the versioned"},
		{"branch ref", func(d map[string]any) {
			d["$schema"] = strings.ReplaceAll(strings.Replace(pinned, "%s", "main", 1), "%s", "0.3")
		}, `ref "main"`},
		{"partial tag", func(d map[string]any) {
			d["$schema"] = strings.ReplaceAll(strings.Replace(pinned, "%s", "v2.1", 1), "%s", "0.3")
		}, `ref "v2.1"`},
		{"somebody else's host", func(d map[string]any) {
			d["$schema"] = "https://example.com/schemas/mcpb-manifest-v0.3.schema.json"
		}, "not upstream's published path"},
		{"version disagrees with the URL", func(d map[string]any) { d["manifest_version"] = "0.4" },
			`manifest_version is "0.4" and $schema pins v0.3`},
		{"below the floor, self-consistent", func(d map[string]any) {
			d["manifest_version"] = "0.2"
			d["$schema"] = strings.ReplaceAll(strings.Replace(pinned, "%s", "v2.1.2", 1), "%s", "0.2")
		}, "below the floor of 0.3"},
		{"no support", func(d map[string]any) { delete(d, "support") }, "support is"},
		{"no no-login sentence", func(d map[string]any) { d["long_description"] = "Read GitLab." },
			`does not say "does not log you in"`},
		{"old desktop", func(d map[string]any) { mcpbDig(d, "compatibility")["claude_desktop"] = ">=0.9.0" },
			"claude_desktop"},
		{"real version committed", func(d map[string]any) { d["version"] = "1.2.3" }, `version is "1.2.3"`},
		{"unknown key the schema refuses", func(d map[string]any) { d["entrypoint"] = "x" },
			"does not satisfy mcpb-manifest-v0.3.schema.json"},
		{"wrong name", func(d map[string]any) { d["name"] = "gitlab" }, `name is "gitlab"`},
		{"wrong license", func(d map[string]any) { d["license"] = "MIT" }, `license is "MIT" and LICENSE is Apache-2.0`},
		{"no keywords", func(d map[string]any) { delete(d, "keywords") }, "keywords is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := mcpbFixture(t)
			tc.mutate(doc)
			var out bytes.Buffer
			err := mcpbCheck(&out, mcpbRoot(t, doc))
			if err == nil {
				t.Fatalf("accepted: %s", out.String())
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Errorf("output does not say %q:\n%s", tc.want, out.String())
			}
		})
	}
}

func TestMcpbRefusesAMissingNotice(t *testing.T) {
	root := mcpbRoot(t, mcpbFixture(t))
	if err := os.Remove(filepath.Join(root, "NOTICE")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := mcpbCheck(&out, root); err == nil || !strings.Contains(out.String(), "copies NOTICE") {
		t.Errorf("a missing NOTICE passed: %v\n%s", err, out.String())
	}
}

// The launcher's names are the packer's; renaming a staged binary in
// one place and not the other fails.
func TestMcpbLauncherNamesMustMatch(t *testing.T) {
	var m mcpbManifest
	raw, _ := json.Marshal(mcpbFixture(t))
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if p := mcpbValidate(m, mcpbFiles, launcherNamesIn(launcherScript(mcpbFiles))); len(p) != 0 {
		t.Fatalf("the generated launcher disagrees: %v", p)
	}
	got := mcpbValidate(m, mcpbFiles, []string{gatekit.BinaryName + "-linux-x64", gatekit.BinaryName + "-linux-arm64"})
	if !slices.ContainsFunc(got, func(s string) bool { return strings.Contains(s, "the launcher runs") }) {
		t.Errorf("a launcher naming another binary passed: %v", got)
	}
}

func TestMcpbBehindIsNumeric(t *testing.T) {
	for v, want := range map[string]bool{"0.2": true, "0.3": false, "0.10": false, "1.0": false, "x": true, "": true} {
		if got := mcpbBehind(v, "0.3"); got != want {
			t.Errorf("mcpbBehind(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestLauncherScriptNamesTheStagedBinaries(t *testing.T) {
	script := launcherScript(mcpbFiles)
	got := launcherNamesIn(script)
	want := []string{"gitlab-mcp-linux-amd64", "gitlab-mcp-linux-arm64"}
	if !slices.Equal(got, want) {
		t.Errorf("launcher names %v, want %v", got, want)
	}
	for _, line := range strings.Split(script, "\n") {
		if strings.Contains(line, "echo ") && !strings.Contains(line, ">&2") {
			t.Errorf("a launcher line writes to stdout, which is the JSON-RPC stream: %q", line)
		}
	}
	for _, want := range []string{"set -eu\n", `exec "$bin" "$@"`, "x86_64 | amd64)", "aarch64 | arm64)", "go install"} {
		if !strings.Contains(script, want) {
			t.Errorf("the launcher does not carry %q", want)
		}
	}
}

// The launcher, run: an unknown architecture and a missing binary each
// go to stderr and exit non-zero; a present binary is exec'd.
func TestLauncherScriptRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the launcher is a POSIX shell script")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	launcher := filepath.Join(dir, "launch-linux.sh")
	if err := os.WriteFile(launcher, []byte(launcherScript(mcpbFiles)), 0o700); err != nil { //nolint:gosec // a test script
		t.Fatal(err)
	}
	fake := t.TempDir()
	runAs := func(arch string) (string, string, error) {
		t.Helper()
		uname := "#!/bin/sh\necho " + arch + "\n"
		if err := os.WriteFile(filepath.Join(fake, "uname"), []byte(uname), 0o700); err != nil { //nolint:gosec // a test script
			t.Fatal(err)
		}
		cmd := exec.Command(sh, launcher, "--flag")
		cmd.Env = append(os.Environ(), "PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"))
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}

	stdout, stderr, err := runAs("sparc64")
	if err == nil || stdout != "" || !strings.Contains(stderr, "no binary for sparc64") {
		t.Errorf("unknown arch: err %v, stdout %q, stderr %q", err, stdout, stderr)
	}
	stdout, stderr, err = runAs("x86_64")
	if err == nil || stdout != "" || !strings.Contains(stderr, "gitlab-mcp-linux-amd64 is missing") {
		t.Errorf("missing binary: err %v, stdout %q, stderr %q", err, stdout, stderr)
	}
	bin := filepath.Join(dir, "gitlab-mcp-linux-arm64")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho ran \"$@\"\n"), 0o700); err != nil { //nolint:gosec // a test script
		t.Fatal(err)
	}
	stdout, stderr, err = runAs("aarch64")
	if err != nil || stdout != "ran --flag\n" {
		t.Errorf("present binary: err %v, stdout %q, stderr %q", err, stdout, stderr)
	}
}
