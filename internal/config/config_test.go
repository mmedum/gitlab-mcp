package config

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/internal/scopes"
	"github.com/mmedum/gitlab-mcp/v2/internal/userconfig"
)

// envMap is a lookup over a fixed map. The config directory is pointed
// at a temporary directory unless the test says otherwise, so nothing
// depends on the machine's home.
func envMap(t *testing.T, kv map[string]string) func(string) string {
	t.Helper()
	m := map[string]string{
		EnvConfigDir:                 t.TempDir(),
		EnvConfigDirAllowOutsideHome: "true",
	}
	for k, v := range kv {
		m[k] = v
	}
	return func(k string) string { return m[k] }
}

func load(t *testing.T, args []string, kv map[string]string) (Config, error) {
	t.Helper()
	return Load(args, envMap(t, kv))
}

func TestDefaults(t *testing.T) {
	c, err := load(t, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Instance.String() != "https://gitlab.com" || c.Target().String() != "https://gitlab.com" || c.TestInstance {
		t.Errorf("Instance = %q, TestInstance %v", c.Instance, c.TestInstance)
	}
	if (Config{}).Target().String() != "https://gitlab.com" {
		t.Errorf("a zero Config targets %q", (Config{}).Target())
	}
	if c.Profile != "" || c.ClientID != "" {
		t.Errorf("Profile %q, ClientID %q; want both empty", c.Profile, c.ClientID)
	}
	if c.ReadOnly || c.EnableShip || c.EnableDestructive {
		t.Errorf("a switch defaulted on: %+v", c)
	}
	if len(c.Toolsets) != 0 || len(c.WriteNamespaces) != 0 {
		t.Errorf("Toolsets %v, WriteNamespaces %v; want none", c.Toolsets, c.WriteNamespaces)
	}
	if c.LogLevel != LogInfo || c.LogFormat != LogText || c.HTTPTimeout != 60*time.Second {
		t.Errorf("LogLevel %q, LogFormat %q, HTTPTimeout %v", c.LogLevel, c.LogFormat, c.HTTPTimeout)
	}
	if c.Mode() != scopes.ModeDefault || !slices.Equal(c.Scopes(), []string{"api"}) {
		t.Errorf("Mode %q, Scopes %v", c.Mode(), c.Scopes())
	}
}

func TestEnvironmentAndFlags(t *testing.T) {
	env := map[string]string{
		EnvProfile:           "work",
		EnvClientID:          "app-id",
		EnvEnableShip:        "true",
		EnvEnableDestructive: "1",
		EnvToolsets:          "wiki, Releases,,wiki",
		EnvWriteNamespaces:   "example-group/app, /example-group/sub/",
		EnvLogLevel:          "DEBUG",
		EnvLogFormat:         "json",
		EnvHTTPTimeout:       "90s",
	}
	c, err := load(t, nil, env)
	if err != nil {
		t.Fatal(err)
	}
	if c.Profile != "work" || c.ClientID != "app-id" {
		t.Errorf("strings: %+v", c)
	}
	if !c.EnableShip || !c.EnableDestructive || c.ReadOnly {
		t.Errorf("switches: %+v", c)
	}
	if want := []string{"releases", "wiki"}; !slices.Equal(c.Toolsets, want) {
		t.Errorf("Toolsets = %v, want %v", c.Toolsets, want)
	}
	if !c.ToolsetEnabled("wiki") || c.ToolsetEnabled("snippets") {
		t.Errorf("ToolsetEnabled wrong for %v", c.Toolsets)
	}
	if want := []string{"example-group/app", "example-group/sub"}; !slices.Equal(c.WriteNamespaces, want) {
		t.Errorf("WriteNamespaces = %v, want %v", c.WriteNamespaces, want)
	}
	if c.LogLevel != LogDebug || c.LogFormat != LogJSON || c.HTTPTimeout != 90*time.Second {
		t.Errorf("log and timeout: %+v", c)
	}

	// A flag wins over the environment; a bare switch means true.
	c, err = load(t, []string{"--profile=home", "--client-id", "other",
		"--enable-ship=false", "--enable-destructive=false", "--read-only", "--http-timeout", "2m"}, env)
	if err != nil {
		t.Fatal(err)
	}
	if c.Profile != "home" || c.ClientID != "other" {
		t.Errorf("flags did not win: %+v", c)
	}
	if !c.ReadOnly || c.EnableShip || c.HTTPTimeout != 2*time.Minute {
		t.Errorf("flag switches: %+v", c)
	}
	if c.Mode() != scopes.ModeReadOnly || !slices.Equal(c.Scopes(), []string{"read_api"}) {
		t.Errorf("Mode %q, Scopes %v", c.Mode(), c.Scopes())
	}
}

func TestToolsetsAll(t *testing.T) {
	c, err := load(t, nil, map[string]string{EnvToolsets: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"activity", "deployments", "planning", "releases", "snippets", "wiki"}; !slices.Equal(c.Toolsets, want) {
		t.Errorf("Toolsets = %v, want %v", c.Toolsets, want)
	}
}

// TestReadOnlyWithAnEnableFlagNamesBoth is §9.4: refused at startup,
// naming both settings.
func TestReadOnlyWithAnEnableFlagNamesBoth(t *testing.T) {
	for _, flagName := range []string{EnvEnableShip, EnvEnableDestructive} {
		_, err := load(t, nil, map[string]string{EnvReadOnly: "true", flagName: "true"})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: err = %v", flagName, err)
		}
		if !strings.Contains(err.Error(), EnvReadOnly) || !strings.Contains(err.Error(), flagName) {
			t.Errorf("%s: %v does not name both", flagName, err)
		}
	}
}

func TestInvalidValuesReportedTogether(t *testing.T) {
	env := map[string]string{
		EnvTestInstance:    "https://gitlab.example.com",
		EnvProfile:         "Bad/Name",
		EnvClientID:        "has space",
		EnvReadOnly:        "maybe",
		EnvToolsets:        "wiki,issues,pipelines",
		EnvWriteNamespaces: "example-group/../x,ok,a//b",
		EnvLogLevel:        "loud",
		EnvLogFormat:       "xml",
		EnvHTTPTimeout:     "soon",
	}
	_, err := load(t, nil, env)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{
		EnvTestInstance, EnvProfile, EnvClientID, EnvReadOnly,
		"unknown toolset issues, pipelines (want activity, deployments, planning, releases, snippets, wiki, or all)",
		`"example-group/../x"`, `"a//b"`,
		EnvLogLevel, EnvLogFormat, EnvHTTPTimeout,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), `"ok"`) {
		t.Errorf("a valid namespace was reported: %v", err)
	}
}

func TestHTTPTimeoutBounds(t *testing.T) {
	tests := map[string]bool{
		"1s": true, "10m": true, "60s": true,
		"999ms": false, "10m1s": false, "0s": false, "-5s": false,
	}
	for v, ok := range tests {
		_, err := load(t, nil, map[string]string{EnvHTTPTimeout: v})
		if (err == nil) != ok {
			t.Errorf("HTTP_TIMEOUT %s: err = %v, want ok=%v", v, err, ok)
		}
	}
}

// TestTestInstanceIsLoopbackOnly holds that the development override
// can never point a token at a real host.
func TestTestInstanceIsLoopbackOnly(t *testing.T) {
	tests := []struct {
		raw  string
		want string // String(); "" means refused
	}{
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"http://[::1]:8080/", "http://[::1]:8080"},
		{"http://localhost:3000", "http://localhost:3000"},
		{"https://127.0.0.1", "https://127.0.0.1"},
		{"https://gitlab.com", ""},
		{"https://gitlab.example.com", ""},
		{"http://gitlab.example.com", ""},
		{"http://127.0.0.1.example.com", ""},
		{"http://localhost.example.com", ""},
		{"ftp://127.0.0.1", ""},
	}
	for _, tt := range tests {
		c, err := load(t, nil, map[string]string{EnvTestInstance: tt.raw})
		if tt.want == "" {
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), EnvTestInstance) {
				t.Errorf("%s: err = %v, want it refused", tt.raw, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tt.raw, err)
			continue
		}
		if c.Instance.String() != tt.want || c.Target().String() != tt.want || !c.TestInstance {
			t.Errorf("%s: Instance %q, TestInstance %v; want %q", tt.raw, c.Instance, c.TestInstance, tt.want)
		}
	}
}

// TestNoInstanceSetting holds that the retired settings are gone: a
// person cannot point the server anywhere but gitlab.com.
func TestNoInstanceSetting(t *testing.T) {
	for _, f := range []string{"--instance", "--ca-file", "--allow-http", "--test-instance"} {
		if _, err := load(t, []string{f, "x"}, nil); err == nil {
			t.Errorf("%s was accepted", f)
		}
	}
	for _, name := range []string{"GITLAB_MCP_INSTANCE", "GITLAB_MCP_CA_FILE", "GITLAB_MCP_ALLOW_HTTP"} {
		c, err := load(t, nil, map[string]string{name: "http://gitlab.example.com"})
		if err != nil || c.Target().String() != "https://gitlab.com" {
			t.Errorf("%s: Target %q, err %v; want it ignored", name, c.Target(), err)
		}
	}
}

// TestConfigDirOutsideHomeIsRefused holds the guard end to end, with
// the opt-out named in the error.
func TestConfigDirOutsideHomeIsRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	outside := t.TempDir()

	_, err := Load(nil, func(k string) string {
		if k == EnvConfigDir {
			return outside
		}
		return ""
	})
	if !errors.Is(err, userconfig.ErrOutsideHome) || !strings.Contains(err.Error(), EnvConfigDirAllowOutsideHome+"=true") {
		t.Fatalf("err = %v", err)
	}

	c, err := Load(nil, func(k string) string {
		switch k {
		case EnvConfigDir:
			return outside
		case EnvConfigDirAllowOutsideHome:
			return "true"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(c.ConfigDir) != outside {
		t.Errorf("ConfigDir = %q, want %q", c.ConfigDir, outside)
	}

	inside := filepath.Join(home, "cfg")
	c, err = Load([]string{"--config-dir", inside}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if string(c.ConfigDir) != inside {
		t.Errorf("ConfigDir = %q, want %q", c.ConfigDir, inside)
	}
}

func TestUnknownFlag(t *testing.T) {
	if _, err := load(t, []string{"--client-secret", "x"}, nil); err == nil {
		t.Error("an unknown flag was accepted")
	}
}

// TestVarsList is the list the staleness gate reads. Each variable is
// stated here rather than read back from Vars.
func TestVarsList(t *testing.T) {
	want := map[string]struct{ flag, def string }{
		"GITLAB_MCP_PROFILE":                       {"profile", ""},
		"GITLAB_MCP_CLIENT_ID":                     {"client-id", ""},
		"GITLAB_MCP_READ_ONLY":                     {"read-only", "false"},
		"GITLAB_MCP_ENABLE_SHIP":                   {"enable-ship", "false"},
		"GITLAB_MCP_ENABLE_DESTRUCTIVE":            {"enable-destructive", "false"},
		"GITLAB_MCP_REQUIRE_PROMPT":                {"require-prompt", "false"},
		"GITLAB_MCP_TOOLSETS":                      {"toolsets", ""},
		"GITLAB_MCP_WRITE_NAMESPACES":              {"write-namespaces", ""},
		"GITLAB_MCP_LOG_LEVEL":                     {"log-level", "info"},
		"GITLAB_MCP_LOG_FORMAT":                    {"log-format", "text"},
		"GITLAB_MCP_HTTP_TIMEOUT":                  {"http-timeout", "1m0s"},
		"GITLAB_MCP_CONFIG_DIR":                    {"config-dir", ""},
		"GITLAB_MCP_CONFIG_DIR_ALLOW_OUTSIDE_HOME": {"", "false"},
		"GITLAB_MCP_REFRESH_TOKEN":                 {"", ""},
		"GITLAB_MCP_TEST_INSTANCE":                 {"", ""},
	}
	if len(Vars) != len(want) {
		t.Errorf("Vars has %d entries, want %d", len(Vars), len(want))
	}
	for _, v := range Vars {
		w, ok := want[v.Name]
		if !ok {
			t.Errorf("unexpected variable %s", v.Name)
			continue
		}
		if v.Flag != w.flag || v.Default != w.def {
			t.Errorf("%s: flag %q default %q, want %q %q", v.Name, v.Flag, v.Default, w.flag, w.def)
		}
		if strings.TrimSpace(v.Doc) == "" {
			t.Errorf("%s has no doc", v.Name)
		}
		if !strings.HasPrefix(v.Name, "GITLAB_MCP_") {
			t.Errorf("%s lacks the prefix", v.Name)
		}
	}
	names := EnvVars()
	if !slices.IsSorted(names) || len(names) != len(want)-1 || slices.Contains(names, "GITLAB_MCP_TEST_INSTANCE") {
		t.Errorf("EnvVars = %v", names)
	}
	if dev := DevVars(); !slices.Equal(dev, []string{"GITLAB_MCP_TEST_INSTANCE"}) {
		t.Errorf("DevVars = %v", dev)
	}
}

// TestDefineReadsExactlyVars: every variable Define looks up is in Vars
// and every flag it binds is one Vars names, so the list cannot fall
// behind the code.
func TestDefineReadsExactlyVars(t *testing.T) {
	var read []string
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	Define(fs, func(k string) string { read = append(read, k); return "" })
	slices.Sort(read)
	all := append(EnvVars(), DevVars()...)
	slices.Sort(all)
	if !slices.Equal(read, all) {
		t.Errorf("Define read %v, Vars lists %v", read, all)
	}
	var flags []string
	fs.VisitAll(func(f *flag.Flag) {
		flags = append(flags, f.Name)
		if !strings.Contains(f.Usage, "[env GITLAB_MCP_") {
			t.Errorf("flag %s usage does not name its variable: %q", f.Name, f.Usage)
		}
	})
	var want []string
	for _, v := range Vars {
		if v.Flag != "" {
			want = append(want, v.Flag)
		}
	}
	slices.Sort(want)
	if !slices.Equal(flags, want) {
		t.Errorf("flags %v, want %v", flags, want)
	}
}

// TestScopesTableNamesARealVariable: the setup table in internal/scopes
// spells the read-only variable out, and it must be this one.
func TestScopesTableNamesARealVariable(t *testing.T) {
	for _, row := range scopes.Modes() {
		for _, f := range row.Flags {
			name, _, _ := strings.Cut(f, "=")
			if !slices.Contains(EnvVars(), name) {
				t.Errorf("scopes.Modes names %s, which config does not read", name)
			}
		}
	}
}

func TestNewLogger(t *testing.T) {
	var buf bytes.Buffer
	NewLogger(Config{LogLevel: LogWarn, LogFormat: LogJSON}, &buf).Info("hidden")
	NewLogger(Config{LogLevel: LogWarn, LogFormat: LogJSON}, &buf).Warn("shown")
	if out := buf.String(); strings.Contains(out, "hidden") || !strings.Contains(out, `"msg":"shown"`) {
		t.Errorf("json logger wrote %q", out)
	}
	buf.Reset()
	NewLogger(Config{LogLevel: LogDebug, LogFormat: LogText}, &buf).Debug("dbg")
	if out := buf.String(); !strings.Contains(out, "msg=dbg") {
		t.Errorf("text logger wrote %q", out)
	}
	levels := map[LogLevel]slog.Level{
		LogDebug: slog.LevelDebug, LogInfo: slog.LevelInfo, LogWarn: slog.LevelWarn, LogError: slog.LevelError,
	}
	for l, want := range levels {
		if got := l.Slog(); got != want {
			t.Errorf("%q.Slog() = %v, want %v", l, got, want)
		}
	}
}
