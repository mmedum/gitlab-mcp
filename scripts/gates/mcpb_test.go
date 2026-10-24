package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/mmedum/gitlab-mcp/v4/internal/config"
	"github.com/mmedum/gitlab-mcp/v4/scripts/internal/gatekit"
)

// mcpbTestDescription is the server's description in the fixture root.
const mcpbTestDescription = "GitLab over MCP from your own account: issues, merge requests, reviews, the repository and CI."

// mcpbFixture decodes the committed manifest as a document. Each test
// breaks its own copy.
func mcpbFixture(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", mcpbManifestPath))
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
		"README.md":                 "# gitlab-mcp\n",
		"go.mod":                    "module " + gatekit.ModulePath + "\n\ngo 1.27\n",
		"internal/server/server.go": "package server\n\n// Description is shared.\nconst Description = " + mcpbQuote(mcpbTestDescription) + "\n",
	})
}

// mcpbManifestOf decodes a manifest document as the checks read it.
func mcpbManifestOf(t *testing.T, doc map[string]any) mcpbManifest {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var m mcpbManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
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
	if !strings.Contains(out.String(), "8 staged files, 3 platforms, 8 user_config keys") {
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
		}, `entry_point "server/nothing" is not a program the packer stages`},
		{"entry point a document", func(d map[string]any) {
			mcpbDig(d, "server")["entry_point"] = "LICENSE"
		}, `entry_point "LICENSE" is not a program the packer stages`},
		{"a command naming a document", func(d map[string]any) {
			mcpbDig(d, "server", "mcp_config", "platform_overrides", "win32")["command"] = "${__dirname}/README.md"
		}, `platform_overrides.win32.command is "${__dirname}/README.md", which the packer does not stage`},
		{"a profile field silent on the rule", func(d map[string]any) {
			mcpbDig(d, "user_config", "profile")["description"] = "Which stored login to use."
		}, `user_config.profile does not say a name is "up to 64 characters from a to z`},
		{"a refused field with no title", func(d map[string]any) {
			mcpbDig(d, "user_config", "enable_ship")["title"] = ""
		}, "user_config.enable_ship has no title"},
		{"a command relative to the bundle", func(d map[string]any) {
			mcpbDig(d, "server", "mcp_config")["command"] = "server/gitlab-mcp-darwin"
		}, `mcp_config.command is "server/gitlab-mcp-darwin"; a command starts with ${__dirname}/`},
		{"a command from the root", func(d map[string]any) {
			mcpbDig(d, "server", "mcp_config", "platform_overrides", "win32")["command"] = "/server/gitlab-mcp.exe"
		}, `platform_overrides.win32.command is "/server/gitlab-mcp.exe"; a command starts with ${__dirname}/`},
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
		}, `platform "linux" runs "server/` + name + `-linux-amd64", which is a binary the launcher chooses`},
		{"win32 runs the launcher", func(d map[string]any) {
			mcpbDig(d, "server", "mcp_config", "platform_overrides", "win32")["command"] = "${__dirname}/server/launch-linux.sh"
		}, `platform "win32" runs "server/launch-linux.sh", which is the launcher, staged for "linux"`},
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
		{"a node server", func(d map[string]any) { mcpbDig(d, "server")["type"] = "node" },
			`server.type is "node"; the bundle carries a binary`},
		// Every other check passes this: no claimed platform is left to
		// spawn the wrong file.
		{"Linux dropped with its override", func(d map[string]any) {
			mcpbDig(d, "compatibility")["platforms"] = []any{"darwin", "win32"}
			delete(mcpbDig(d, "server", "mcp_config", "platform_overrides"), "linux")
		}, "compatibility.platforms is [darwin win32]; the packer stages entry points for [darwin linux win32]"},
		{"a platform the packer stages nothing for", func(d map[string]any) {
			mcpbDig(d, "compatibility")["platforms"] = []any{"darwin", "win32", "linux", "freebsd"}
		}, "compatibility.platforms is [darwin freebsd linux win32]; the packer stages entry points for [darwin linux win32]"},
		{"a platform claimed twice", func(d map[string]any) {
			mcpbDig(d, "compatibility")["platforms"] = []any{"darwin", "win32", "linux", "linux"}
		}, "compatibility.platforms is [darwin linux linux win32]; the packer stages entry points for [darwin linux win32]"},
		{"an entry point the default command does not run", func(d map[string]any) {
			mcpbDig(d, "server")["entry_point"] = "server/" + name + ".exe"
		}, `entry_point is "server/` + name + `.exe", and mcp_config.command runs "server/` + name + `-darwin"`},
		{"wrong license", func(d map[string]any) { d["license"] = "MIT" }, `license is "MIT" and LICENSE is Apache-2.0`},
		{"no keywords", func(d map[string]any) { delete(d, "keywords") }, "keywords is empty"},
		{"no privacy policy", func(d map[string]any) { delete(d, "privacy_policies") }, "privacy_policies is empty"},
		{"a switch the bundle turns on", func(d map[string]any) {
			mcpbDig(d, "user_config", "enable_ship")["default"] = true
		}, "the bundle's defaults start the server differently from no settings, in EnableShip"},
		{"a required switch the bundle turns on", func(d map[string]any) {
			mcpbDig(d, "user_config", "enable_destructive")["default"] = true
			mcpbDig(d, "user_config", "enable_destructive")["required"] = true
		}, "the bundle's defaults start the server differently from no settings, in EnableDestructive"},
		{"a toolset the bundle turns on", func(d map[string]any) {
			mcpbDig(d, "user_config", "toolsets")["default"] = "all"
		}, "the bundle's defaults start the server differently from no settings, in Toolsets"},
		{"a default a host may substitute in", func(d map[string]any) {
			mcpbDig(d, "user_config", "client_id")["default"] = "${HOME}"
		}, `user_config.client_id's default holds "${HOME}"; a host may substitute`},
		{"a list default a host may substitute in", func(d map[string]any) {
			mcpbDig(d, "user_config", "upload_dirs")["default"] = []any{"/x", "${user_config.client_id}"}
		}, `user_config.upload_dirs's default holds "[/x ${user_config.client_id}]"`},
		{"defaults the server refuses", func(d map[string]any) {
			mcpbDig(d, "user_config", "toolsets")["default"] = "nope"
		}, "the bundle's defaults do not start the server"},
		{"an override's env, which replaces the base", func(d map[string]any) {
			mcpbDig(d, "server", "mcp_config", "platform_overrides", "win32")["env"] = map[string]any{"GITLAB_MCP_ENABLE_SHIP": "true"}
		}, "platform_overrides.win32.env is set"},
		{"no base args", func(d map[string]any) {
			delete(mcpbDig(d, "server", "mcp_config"), "args")
		}, "mcp_config.args is missing"},
		{"null base args", func(d map[string]any) {
			mcpbDig(d, "server", "mcp_config")["args"] = nil
		}, "mcp_config.args is null"},
		{"args not a list", func(d map[string]any) {
			mcpbDig(d, "server", "mcp_config")["args"] = map[string]any{}
		}, "mcp_config.args is not a list"},
		{"a flag in args", func(d map[string]any) {
			mcpbDig(d, "server", "mcp_config")["args"] = []any{"--enable-destructive=true"}
		}, "mcp_config.args is not empty"},
		{"a flag in an override's args", func(d map[string]any) {
			mcpbDig(d, "server", "mcp_config", "platform_overrides", "linux")["args"] = []any{"--enable-ship=true"}
		}, "platform_overrides.linux.args is set"},
		{"an empty override env, which still replaces the base", func(d map[string]any) {
			mcpbDig(d, "server", "mcp_config", "platform_overrides", "linux")["env"] = map[string]any{}
		}, "platform_overrides.linux.env is set"},
		{"an excluded setting in env", func(d map[string]any) {
			mcpbDig(d, "server", "mcp_config", "env")["GITLAB_MCP_REFRESH_TOKEN"] = "x"
		}, "sets GITLAB_MCP_REFRESH_TOKEN, which the bundle excludes"},
		{"a refused combination the description does not warn of", func(d map[string]any) {
			mcpbDig(d, "user_config", "read_only")["description"] = "Read only."
		}, `user_config.read_only does not name "Merging, approving, CI and releases"`},
		{"a toolset the description leaves out", func(d map[string]any) {
			mcpbDig(d, "user_config", "toolsets")["description"] = "Comma-separated toolsets to add: activity, " +
				"deployments, releases, snippets, wiki, or all. Leave it empty for none; planning is its own word here."
		}, `user_config.toolsets does not list the toolsets as "activity, deployments, planning, releases, snippets, wiki, or all"`},
		{"an option that does not decode", func(d map[string]any) {
			mcpbDig(d, "user_config", "upload_dirs")["required"] = "false"
		}, "user_config.upload_dirs is not an option"},
		{"a switch asked as text", func(d map[string]any) {
			mcpbDig(d, "user_config", "enable_destructive")["type"] = "string"
			mcpbDig(d, "user_config", "enable_destructive")["default"] = "false"
		}, "user_config.enable_destructive is a string; GITLAB_MCP_ENABLE_DESTRUCTIVE is a switch, so ask for a boolean"},
		{"a setting the bundle no longer asks for", func(d map[string]any) {
			delete(mcpbDig(d, "user_config"), "write_namespaces")
			delete(mcpbDig(d, "server", "mcp_config", "env"), "GITLAB_MCP_WRITE_NAMESPACES")
		}, "user_config does not ask for write_namespaces"},
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

// A key repeated in one object is refused, named by its path, before any
// other check reads the document: the typed decode merges the two, so
// every check sees what the first said, while the schema and the host
// keep the second. Each break is an edit of the committed text, since a
// decoded document cannot hold one.
func TestMcpbRefusesARepeatedKey(t *testing.T) {
	committed, err := os.ReadFile(filepath.Join("..", "..", mcpbManifestPath))
	if err != nil {
		t.Fatal(err)
	}
	name := gatekit.BinaryName
	for _, tc := range []struct{ name, old, new, want string }{
		{"a second server object", `  "user_config": {`,
			`  "server": {"type": "binary", "entry_point": "server/` + name + `-darwin", ` +
				`"mcp_config": {"command": "${__dirname}/server/` + name + `-darwin", "args": []}},` +
				"\n" + `  "user_config": {`,
			"the manifest has server more than once"},
		{"a variable mapped twice", `"GITLAB_MCP_PROFILE": "${user_config.profile}",`,
			`"GITLAB_MCP_PROFILE": "${user_config.profile}", "GITLAB_MCP_PROFILE": "${user_config.profile}",`,
			"the manifest has server.mcp_config.env.GITLAB_MCP_PROFILE more than once"},
		// The gate would read the second command, and the host the first.
		{"a key in another case", `"args": [],`,
			`"args": [], "Command": "${__dirname}/server/` + name + `.exe",`,
			`the manifest has server.mcp_config.Command beside "command", which differs only in case`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !bytes.Contains(committed, []byte(tc.old)) {
				t.Fatalf("the manifest does not say %q", tc.old)
			}
			root := mcpbRoot(t, mcpbFixture(t))
			raw := bytes.Replace(committed, []byte(tc.old), []byte(tc.new), 1)
			if err := os.WriteFile(filepath.Join(root, mcpbManifestPath), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, problems, err := mcpbCommitted(root)
			if err != nil || len(problems) != 1 || !strings.HasPrefix(problems[0], tc.want) {
				t.Errorf("mcpbCommitted = %q, %v; want the one problem starting %q", problems, err, tc.want)
			}
		})
	}
}

// A key is repeated only within one object, equal or equal but for case,
// and each repeated key is named once, by a path that says which key it
// is: a list's elements, a repeated object's own keys and a key holding
// a dot or a bracket included.
func TestMcpbRepeatedKeysNameEachPathOnce(t *testing.T) {
	for _, tc := range []struct {
		doc  string
		want []mcpbRepeat
	}{
		{`{"a": [{"b": 1, "b": 2, "b": 3}], "c": {"d": 1}, "d": 1}`, []mcpbRepeat{{"a[0].b", ""}}},
		{`{"x": {"y": 1}, "x": {"y": 1, "y": 2}}`, []mcpbRepeat{{"x", ""}, {"x.y", ""}}},
		{`{"server": {"type": "binary"}, "user_config": {"type": {"type": "boolean"}}}`, nil},
		{"{\r\n  \"a\": 1,\r\n  \"a\": 2\r\n}\r\n", []mcpbRepeat{{"a", ""}}},
		{`{"m": {"command": "a", "Command": "b", "COMMAND": "c"}}`, []mcpbRepeat{{"m.Command", "command"}}},
		{`{"k": 1, "\u212a": 2}`, []mcpbRepeat{{"\u212a", "k"}}},
		{`{"env": {"a.b": 1, "a.b": 2}, "a": {"b": 1}}`, []mcpbRepeat{{`env["a.b"]`, ""}}},
		{`{"x[0]": 1, "x[0]": 2, "": 1, "": 2}`, []mcpbRepeat{{`["x[0]"]`, ""}, {`[""]`, ""}}},
	} {
		got, err := mcpbRepeatedKeys([]byte(tc.doc))
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("mcpbRepeatedKeys(%q) = %q, %v; want %q", tc.doc, got, err, tc.want)
		}
	}
	// A walk that cannot read the document refuses it rather than finding
	// nothing repeated in it.
	if got, err := mcpbRepeatedKeys([]byte(`{"a": 1,`)); err == nil {
		t.Errorf("mcpbRepeatedKeys of a cut document = %q, nil; want an error", got)
	}
}

// mcp_config.args is held as a list, however it is spaced: an empty
// list over two lines is empty.
func TestMcpbArgsAreReadAsAList(t *testing.T) {
	committed, err := os.ReadFile(filepath.Join("..", "..", mcpbManifestPath))
	if err != nil {
		t.Fatal(err)
	}
	for args, want := range map[string]string{
		"[ ]":               "",
		"[\r\n      ]":      "",
		`[ "--read-only" ]`: "mcp_config.args is not empty",
		"[\n  null\n]":      "mcp_config.args is not empty",
		` null`:             "mcp_config.args is null",
	} {
		root := mcpbRoot(t, mcpbFixture(t))
		raw := bytes.Replace(committed, []byte(`"args": [],`), []byte(`"args": `+args+`,`), 1)
		if err := os.WriteFile(filepath.Join(root, mcpbManifestPath), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, problems, err := mcpbCommitted(root)
		switch {
		case err != nil:
			t.Errorf("args %q: %v", args, err)
		case want == "" && len(problems) != 0:
			t.Errorf("args %q refused: %q", args, problems)
		case want != "" && !slices.ContainsFunc(problems, func(p string) bool { return strings.HasPrefix(p, want) }):
			t.Errorf("args %q: %q, want a problem starting %q", args, problems, want)
		}
	}
}

// A field that sets a credential is sensitive, which settings are
// credentials coming from config's Secret flag, and a registry that marks
// none is refused, since the check would then hold nothing.
func TestMcpbACredentialFieldIsSensitive(t *testing.T) {
	secret := config.Var{Name: config.EnvPrefix + "TOKEN", Secret: true}
	keys := map[string]config.Var{"token": secret, "profile": {Name: config.EnvProfile}}
	for _, tc := range []struct {
		name string
		vars []config.Var
		opts map[string]mcpbOption
		want []string
	}{
		{"sensitive", []config.Var{secret}, map[string]mcpbOption{"token": {Sensitive: true}, "profile": {}}, nil},
		{"shown", []config.Var{secret}, map[string]mcpbOption{"token": {}, "profile": {}},
			[]string{"user_config.token sets GITLAB_MCP_TOKEN, a credential, and is not sensitive, so a host shows " +
				"and stores it as plain text"}},
		{"not asked for", []config.Var{secret}, map[string]mcpbOption{"profile": {}}, nil},
		{"no credential marked", []config.Var{{Name: config.EnvProfile}}, map[string]mcpbOption{"profile": {}},
			[]string{"read 0 credentials marked Secret in internal/config.Vars, want at least 1: the reader is not seeing its input"}},
	} {
		if got := mcpbSensitiveProblems(tc.vars, keys, tc.opts); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := mcpbSensitiveProblems(config.Vars, mcpbUserConfig, mcpbManifestOf(t, mcpbFixture(t)).options()); len(got) != 0 {
		t.Errorf("the committed manifest and config.Vars: %q", got)
	}
}

// The launcher's install hint is gatekit.ModulePath, held to go.mod,
// which moves at every major version.
func TestMcpbRefusesALauncherHintGoModDoesNotDeclare(t *testing.T) {
	root := mcpbRoot(t, mcpbFixture(t))
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/mmedum/gitlab-mcp/v9\r\n\r\ngo 1.27\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	want := `the launcher's install hint names ` + gatekit.ModulePath + `, and go.mod's module is "github.com/mmedum/gitlab-mcp/v9"`
	if err := mcpbCheck(&out, root); err == nil || !strings.Contains(out.String(), want) {
		t.Errorf("mcpbCheck = %v\n%s\nwant it refused, saying %q", err, out.String(), want)
	}
}

// Every field the defaults change is named, each unexported one
// included, whatever its type, beside an exported one that differs too.
// A pointer differs by what it points to, never by its address.
func TestMcpbFieldDiffNamesTheFieldsThatDiffer(t *testing.T) {
	type cfg struct {
		Shown  int
		hidden int
		rest   []string
		held   any
		at     *int
		by     map[string][]int
	}
	one, alsoOne, two := 1, 1, 2
	base := cfg{Shown: 1, hidden: 1, at: &one}
	for _, tc := range []struct {
		name string
		b    cfg
		want []string
	}{
		{"an exported field", cfg{Shown: 2, hidden: 1, at: &one}, []string{"Shown"}},
		{"an unexported field", cfg{Shown: 1, hidden: 2, at: &one}, []string{"hidden"}},
		{"an unexported slice", cfg{Shown: 1, hidden: 1, rest: []string{"x"}, at: &one}, []string{"rest"}},
		{"an exported field and an unexported slice", cfg{Shown: 2, hidden: 1, rest: []string{"x"}, at: &one},
			[]string{"Shown", "rest"}},
		{"an interface holding a slice", cfg{Shown: 1, hidden: 1, held: []string{"x"}, at: &one}, []string{"held"}},
		{"a pointer to an equal value elsewhere", cfg{Shown: 1, hidden: 1, at: &alsoOne}, nil},
		{"a pointer to another value", cfg{Shown: 1, hidden: 1, at: &two}, []string{"at"}},
		{"a map of lists", cfg{Shown: 1, hidden: 1, at: &one, by: map[string][]int{"a": {1}}}, []string{"by"}},
		{"nothing", cfg{Shown: 1, hidden: 1, at: &alsoOne}, nil},
	} {
		if got := mcpbFieldDiff(base, tc.b); !slices.Equal(got, tc.want) {
			t.Errorf("%s: mcpbFieldDiff = %q, want %q", tc.name, got, tc.want)
		}
	}
	// Two interfaces holding equal slices are the same.
	if got := mcpbFieldDiff(cfg{held: []string{"x"}}, cfg{held: []string{"x"}}); len(got) != 0 {
		t.Errorf("two interfaces holding equal slices give %q, want none", got)
	}
	if got := mcpbFieldDiff(config.Config{Profile: "a", EnableShip: true}, config.Config{Profile: "a"}); !slices.Equal(got,
		[]string{"EnableShip"}) {
		t.Errorf("two configurations give %q, want [EnableShip]", got)
	}
}

// Each file the packer copies from the repository must be there.
func TestMcpbRefusesAMissingRepositoryFile(t *testing.T) {
	copied := 0
	for _, f := range mcpbFiles {
		if f.repo == "" {
			continue
		}
		copied++
		name := f.repo
		root := mcpbRoot(t, mcpbFixture(t))
		if err := os.Remove(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := mcpbCheck(&out, root); err == nil || !strings.Contains(out.String(), "copies "+name) {
			t.Errorf("a missing %s passed: %v\n%s", name, err, out.String())
		}
	}
	if copied != 3 {
		t.Errorf("the packer copies %d files from the repository, want LICENSE, NOTICE and README.md", copied)
	}
}

// The bundle's keys are the settings the server reads, less the
// development overrides and the exclusions, and an exclusion of a
// setting that is gone is refused.
func TestMcpbUserConfigIsDerivedFromTheSettings(t *testing.T) {
	vars := []config.Var{
		{Name: config.EnvPrefix + "ASKED"},
		{Name: config.EnvPrefix + "DEV", Dev: true},
		{Name: config.EnvPrefix + "EXCLUDED"},
	}
	keys, problems := mcpbUserConfigFrom(vars, map[string]string{
		config.EnvPrefix + "EXCLUDED": "a reason",
		config.EnvPrefix + "GONE":     "a reason",
	})
	if want := map[string]config.Var{"asked": vars[0]}; !maps.Equal(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
	want := []string{"the bundle excludes GITLAB_MCP_GONE, which the server does not read"}
	if !slices.Equal(problems, want) {
		t.Errorf("problems = %q, want %q", problems, want)
	}
}

// The refused combinations of the bundle's switches are declared, both
// ways: one the server refuses and the list leaves out, and one the list
// declares that the server accepts.
func TestMcpbRefusedCombinationsAreDeclared(t *testing.T) {
	m := mcpbManifestOf(t, mcpbFixture(t))
	if problems := mcpbRefusalProblems(m, mcpbRefused, nil); len(problems) != 0 {
		t.Errorf("the declared list does not match the server: %q", problems)
	}
	missing := mcpbRefusalProblems(m, [][]string{{"enable_ship", "read_only"}}, nil)
	if len(missing) == 0 || !strings.Contains(missing[0], "the server refuses enable_destructive, read_only together") {
		t.Errorf("an undeclared refusal passed: %q", missing)
	}
	// read_only is in both pairs, and a missing title is one problem, not
	// one per pair.
	untitled := mcpbManifestOf(t, mcpbFixture(t))
	untitled.UserConfig["read_only"] = json.RawMessage(`{"type": "boolean", "title": "", "default": false}`)
	got := mcpbRefusalProblems(untitled, mcpbRefused, nil)
	if n := len(slices.DeleteFunc(got, func(p string) bool {
		return !strings.HasPrefix(p, "user_config.read_only has no title")
	})); n != 1 {
		t.Errorf("an untitled read_only is reported %d times, want 1", n)
	}
	// It is reported when the defaults do not start, too, and no
	// combination is started then, so none is refused.
	failed := errors.New("the defaults do not start")
	if got := mcpbRefusalProblems(untitled, mcpbRefused, failed); !slices.ContainsFunc(got, func(p string) bool {
		return strings.HasPrefix(p, "user_config.read_only has no title")
	}) {
		t.Errorf("an untitled read_only passed beside defaults that do not start: %q", got)
	}
	if got := mcpbRefusalProblems(m, [][]string{{"enable_ship", "read_only"}}, failed); len(got) != 0 {
		t.Errorf("combinations were started beside defaults that do not: %q", got)
	}
	// A title with a space around it is named without the space.
	padded := mcpbManifestOf(t, mcpbFixture(t))
	opt := padded.options()["read_only"]
	opt.Title += " "
	raw, err := json.Marshal(opt)
	if err != nil {
		t.Fatal(err)
	}
	padded.UserConfig["read_only"] = raw
	if got := mcpbRefusalProblems(padded, mcpbRefused, nil); len(got) != 0 {
		t.Errorf("a padded title is not found in descriptions that name it: %q", got)
	}
	stale := mcpbRefusalProblems(m, append(slices.Clone(mcpbRefused), []string{"enable_shipp", "read_only"}), nil)
	if !slices.Contains(stale, "mcpbRefused names enable_shipp, which is not a switch the bundle asks for") {
		t.Errorf("a declared name that is no switch passed: %q", stale)
	}
	extra := mcpbRefusalProblems(m, append(slices.Clone(mcpbRefused), []string{"enable_destructive", "enable_ship"}), nil)
	if !slices.ContainsFunc(extra, func(p string) bool {
		return strings.Contains(p, "declares a refusal among enable_destructive, enable_ship, and the server starts")
	}) {
		t.Errorf("a declared refusal the server accepts passed: %q", extra)
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

// Defaults the server refuses are reported once, not again as a refusal
// of every combination of the switches.
func TestMcpbDefaultsThatFailAreReportedOnce(t *testing.T) {
	doc := mcpbFixture(t)
	mcpbDig(doc, "user_config", "toolsets")["default"] = "nope"
	var out bytes.Buffer
	if err := mcpbCheck(&out, mcpbRoot(t, doc)); err == nil {
		t.Fatalf("accepted: %s", out.String())
	}
	if !strings.Contains(out.String(), "the bundle's defaults do not start the server") {
		t.Errorf("the failure is not named:\n%s", out.String())
	}
	if strings.Contains(out.String(), "together") {
		t.Errorf("the failure is also blamed on the switches:\n%s", out.String())
	}
}
