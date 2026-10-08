// Package config loads and validates the runtime configuration.
//
// Environment variables (GITLAB_MCP_*) are the source of truth, because
// an MCP client passes only command, args and env to a stdio server.
// Most settings also have a flag bound to the same name; a flag given on
// the command line wins over the environment, and the environment over
// the default. Build validates once and reports every problem together,
// so a misconfigured server fails before it announces itself.
//
// Vars lists every variable. Define binds from it, and the staleness
// gate holds docs/configuration.md (and, for the development override,
// docs/development.md) against it, so a setting cannot be added without
// being documented.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/internal/instance"
	"github.com/mmedum/gitlab-mcp/v2/internal/scopes"
	"github.com/mmedum/gitlab-mcp/v2/internal/userconfig"
)

// EnvPrefix is prepended to every environment variable name. Not
// GITLAB_: CI jobs already set GITLAB_CI, GITLAB_USER_LOGIN and more
// (§18 row 30).
const EnvPrefix = "GITLAB_MCP_"

// DefaultHTTPTimeout bounds one attempt at an API call.
const DefaultHTTPTimeout = 60 * time.Second

// HTTP timeout bounds. Below a second nothing completes; above ten
// minutes a hung call holds the client longer than anyone waits.
const (
	MinHTTPTimeout = time.Second
	MaxHTTPTimeout = 10 * time.Minute
)

// Var is one configuration variable.
type Var struct {
	// Name is the environment variable.
	Name string
	// Flag is the command-line flag bound to it, without dashes; empty
	// for a variable read from the environment only.
	Flag string
	// Default is the value used when neither is given.
	Default string
	// Doc says what it does.
	Doc string
	// Switch marks a boolean: a bare --flag means true.
	Switch bool
	// Dev marks a development override: documented in
	// docs/development.md rather than docs/configuration.md, and never
	// offered to the people running the server.
	Dev bool
}

// Variable names.
const (
	EnvProfile                   = EnvPrefix + "PROFILE"
	EnvClientID                  = EnvPrefix + "CLIENT_ID"
	EnvReadOnly                  = EnvPrefix + "READ_ONLY"
	EnvEnableShip                = EnvPrefix + "ENABLE_SHIP"
	EnvEnableDestructive         = EnvPrefix + "ENABLE_DESTRUCTIVE"
	EnvRequirePrompt             = EnvPrefix + "REQUIRE_PROMPT"
	EnvToolsets                  = EnvPrefix + "TOOLSETS"
	EnvWriteNamespaces           = EnvPrefix + "WRITE_NAMESPACES"
	EnvUploadDirs                = EnvPrefix + "UPLOAD_DIRS"
	EnvLogLevel                  = EnvPrefix + "LOG_LEVEL"
	EnvLogFormat                 = EnvPrefix + "LOG_FORMAT"
	EnvHTTPTimeout               = EnvPrefix + "HTTP_TIMEOUT"
	EnvConfigDir                 = EnvPrefix + "CONFIG_DIR"
	EnvConfigDirAllowOutsideHome = EnvPrefix + "CONFIG_DIR_ALLOW_OUTSIDE_HOME"
	// EnvRefreshToken is read by internal/credentials, never by Build,
	// and has no flag: a secret on a command line is visible to every
	// process on the machine.
	EnvRefreshToken = EnvPrefix + "REFRESH_TOKEN"
	// EnvTestInstance points the server at a loopback stand-in for
	// gitlab.com: the in-memory instance the tests, the evals and the
	// smoke gate run. It has no flag and is refused for any other host,
	// so it cannot send a token to a real one.
	EnvTestInstance = EnvPrefix + "TEST_INSTANCE"
)

// Vars is every variable this server reads, in documentation order.
var Vars = []Var{
	{Name: EnvProfile, Flag: "profile",
		Doc: "named sign-in profile; unset means \"default\""},
	{Name: EnvClientID, Flag: "client-id",
		Doc: "the OAuth application id (overrides the stored profile's)"},
	{Name: EnvReadOnly, Flag: "read-only", Default: "false", Switch: true,
		Doc: "register only the read tools and request read_api"},
	{Name: EnvEnableShip, Flag: "enable-ship", Default: "false", Switch: true,
		Doc: "register merging, approving, running CI and creating releases"},
	{Name: EnvEnableDestructive, Flag: "enable-destructive", Default: "false", Switch: true,
		Doc: "register deletions, each of which also needs confirm: true"},
	{Name: EnvRequirePrompt, Flag: "require-prompt", Default: "false", Switch: true,
		Doc: "refuse the writes that ask the person when the client cannot ask them"},
	{Name: EnvToolsets, Flag: "toolsets",
		Doc: "comma-separated optional toolsets: " + strings.Join(Toolsets, ", ") + ", or all"},
	{Name: EnvWriteNamespaces, Flag: "write-namespaces",
		Doc: "comma-separated groups or projects that writes are confined to; unset means anywhere"},
	{Name: EnvUploadDirs, Flag: "upload-dirs",
		Doc: "absolute directories upload_file may read images from, separated as PATH is; unset means none"},
	{Name: EnvLogLevel, Flag: "log-level", Default: string(LogInfo),
		Doc: "log level: debug, info, warn, error"},
	{Name: EnvLogFormat, Flag: "log-format", Default: string(LogText),
		Doc: "log format: text, json"},
	{Name: EnvHTTPTimeout, Flag: "http-timeout", Default: DefaultHTTPTimeout.String(),
		Doc: "deadline for one attempt at an API call, between 1s and 10m"},
	{Name: EnvConfigDir, Flag: "config-dir",
		Doc: "directory for profiles; must be inside your home directory"},
	{Name: EnvConfigDirAllowOutsideHome, Default: "false", Switch: true,
		Doc: "accept a config directory outside your home directory"},
	{Name: EnvRefreshToken,
		Doc: "a refresh token to use instead of the stored one (CI, automation)"},
	{Name: EnvTestInstance, Dev: true,
		Doc: "a loopback base URL that stands in for gitlab.com, for tests only"},
}

// EnvVars is every variable a person running the server may set,
// sorted: Vars less the development overrides.
func EnvVars() []string { return names(false) }

// DevVars is every development override, sorted.
func DevVars() []string { return names(true) }

func names(dev bool) []string {
	var out []string
	for _, v := range Vars {
		if v.Dev == dev {
			out = append(out, v.Name)
		}
	}
	slices.Sort(out)
	return out
}

// Toolsets are the optional toolsets, sorted, which are off unless
// named (§4.3).
var Toolsets = []string{"activity", "deployments", "planning", "releases", "snippets", "wiki"}

// ToolsetAll names every toolset at once.
const ToolsetAll = "all"

// LogLevel is a typed enum constrained at load time.
type LogLevel string

// Allowed LogLevel values.
const (
	LogDebug LogLevel = "debug"
	LogInfo  LogLevel = "info"
	LogWarn  LogLevel = "warn"
	LogError LogLevel = "error"
)

// Slog returns the slog.Level for this level.
func (l LogLevel) Slog() slog.Level {
	switch l {
	case LogDebug:
		return slog.LevelDebug
	case LogWarn:
		return slog.LevelWarn
	case LogError:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// LogFormat is a typed enum constrained at load time.
type LogFormat string

// Allowed LogFormat values.
const (
	LogText LogFormat = "text"
	LogJSON LogFormat = "json"
)

// Config is the validated runtime configuration.
type Config struct {
	// Instance is gitlab.com, or the loopback stand-in of
	// GITLAB_MCP_TEST_INSTANCE. Zero means gitlab.com; use Target.
	Instance instance.Instance
	// TestInstance says GITLAB_MCP_TEST_INSTANCE is set.
	TestInstance bool
	// Profile is the profile named, or empty for "default".
	Profile string
	// ClientID overrides the profile's stored OAuth application id.
	ClientID string
	// ReadOnly registers the Read kind only and requests read_api.
	ReadOnly bool
	// EnableShip registers the Ship kind. It changes no scope (§9.4).
	EnableShip bool
	// EnableDestructive registers the Destructive kind. It changes no
	// scope either.
	EnableDestructive bool
	// RequirePrompt refuses the writes that ask the person when the
	// client cannot ask (§4.12).
	RequirePrompt bool
	// Toolsets are the optional toolsets turned on, sorted, with "all"
	// expanded.
	Toolsets []string
	// WriteNamespaces confine Write, Ship and Destructive (§4.7). Empty
	// means no confinement.
	WriteNamespaces []string
	// UploadDirs are the only directories upload_file may read an image
	// from (§7.10). Empty means none: the tool refuses every path.
	UploadDirs  []string
	LogLevel    LogLevel
	LogFormat   LogFormat
	HTTPTimeout time.Duration
	// ConfigDir is the resolved base directory for profiles.
	ConfigDir userconfig.Dir
}

// Target is the instance this configuration talks to: gitlab.com unless
// the test override names another.
func (c Config) Target() instance.Instance {
	if c.Instance.IsZero() {
		return instance.GitLabCom
	}
	return c.Instance
}

// Mode is the scope mode this configuration is in.
func (c Config) Mode() scopes.Mode { return scopes.ModeFor(c.ReadOnly) }

// Scopes is what login requests under this configuration.
func (c Config) Scopes() []string { return scopes.ForMode(c.Mode()) }

// ToolsetEnabled reports whether an optional toolset is on.
func (c Config) ToolsetEnabled(name string) bool { return slices.Contains(c.Toolsets, name) }

// Settings holds the raw values before validation, keyed by variable.
type Settings struct {
	values map[string]*string
}

// Define registers one flag per flagged variable on fs, each defaulting
// to its environment variable read through env, and reads the
// environment-only variables.
func Define(fs *flag.FlagSet, env func(string) string) *Settings {
	s := &Settings{values: map[string]*string{}}
	for _, v := range Vars {
		val := env(v.Name)
		if val == "" {
			val = v.Default
		}
		p := new(string)
		*p = val
		s.values[v.Name] = p
		if v.Flag == "" {
			continue
		}
		usage := v.Doc + " [env " + v.Name + "]"
		if v.Switch {
			// A switch takes a bare --read-only as well as
			// --read-only=true. Build parses the text either way, so a
			// bad value from the environment is reported with the others
			// rather than by the flag package.
			fs.Var((*switchText)(p), v.Flag, usage)
		} else {
			fs.StringVar(p, v.Flag, val, usage)
		}
	}
	return s
}

func (s *Settings) get(name string) string {
	if p := s.values[name]; p != nil {
		return *p
	}
	return ""
}

// switchText is a flag.Value that holds its text for Build to parse and
// tells the flag package it may be given bare.
type switchText string

func (s *switchText) String() string {
	if s == nil {
		return ""
	}
	return string(*s)
}

func (s *switchText) Set(v string) error { *s = switchText(v); return nil }

// IsBoolFlag lets the flag be given without a value.
func (s *switchText) IsBoolFlag() bool { return true }

var (
	logLevels  = []LogLevel{LogDebug, LogInfo, LogWarn, LogError}
	logFormats = []LogFormat{LogText, LogJSON}
	// namespacePattern is a group or project path: segments of GitLab's
	// path characters separated by single slashes.
	namespacePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.\-]*(/[A-Za-z0-9_][A-Za-z0-9_.\-]*)*$`)
)

// ErrInvalid wraps every validation failure.
var ErrInvalid = errors.New("config: invalid")

// Build validates the settings and returns a Config, or every problem
// at once.
func (s *Settings) Build() (Config, error) {
	var c Config
	var errs []error
	add := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	var err error
	c.Instance, c.TestInstance, err = parseTestInstance(s.get(EnvTestInstance))
	add(err)

	c.Profile = strings.TrimSpace(s.get(EnvProfile))
	if c.Profile != "" {
		if err := userconfig.ValidProfile(c.Profile); err != nil {
			add(fmt.Errorf("%w: %s: %w", ErrInvalid, EnvProfile, err))
		}
	}
	c.ClientID = strings.TrimSpace(s.get(EnvClientID))
	if strings.ContainsAny(c.ClientID, " \t\r\n/") {
		add(fmt.Errorf("%w: %s is not an application id", ErrInvalid, EnvClientID))
	}

	c.ReadOnly, err = parseBool(EnvReadOnly, s.get(EnvReadOnly))
	add(err)
	c.EnableShip, err = parseBool(EnvEnableShip, s.get(EnvEnableShip))
	add(err)
	c.EnableDestructive, err = parseBool(EnvEnableDestructive, s.get(EnvEnableDestructive))
	add(err)
	c.RequirePrompt, err = parseBool(EnvRequirePrompt, s.get(EnvRequirePrompt))
	add(err)
	// Read-only with an enable flag has no coherent meaning, and guessing
	// which was meant would drop either a guard or a tool.
	for _, on := range []struct {
		set  bool
		name string
	}{{c.EnableShip, EnvEnableShip}, {c.EnableDestructive, EnvEnableDestructive}} {
		if c.ReadOnly && on.set {
			add(fmt.Errorf("%w: %s and %s are both set; choose one", ErrInvalid, EnvReadOnly, on.name))
		}
	}

	c.Toolsets, err = parseToolsets(s.get(EnvToolsets))
	add(err)
	c.WriteNamespaces, err = parseNamespaces(s.get(EnvWriteNamespaces))
	add(err)
	c.UploadDirs, err = parseUploadDirs(s.get(EnvUploadDirs))
	add(err)

	c.LogLevel = LogLevel(strings.ToLower(strings.TrimSpace(s.get(EnvLogLevel))))
	if !slices.Contains(logLevels, c.LogLevel) {
		add(fmt.Errorf("%w: %s %q (want debug, info, warn, error)", ErrInvalid, EnvLogLevel, s.get(EnvLogLevel)))
	}
	c.LogFormat = LogFormat(strings.ToLower(strings.TrimSpace(s.get(EnvLogFormat))))
	if !slices.Contains(logFormats, c.LogFormat) {
		add(fmt.Errorf("%w: %s %q (want text, json)", ErrInvalid, EnvLogFormat, s.get(EnvLogFormat)))
	}

	c.HTTPTimeout, err = parseTimeout(s.get(EnvHTTPTimeout))
	add(err)

	allowOutside, err := parseBool(EnvConfigDirAllowOutsideHome, s.get(EnvConfigDirAllowOutsideHome))
	add(err)
	if err == nil {
		dir, err := userconfig.BaseDir(s.get(EnvConfigDir), allowOutside)
		if err != nil {
			if errors.Is(err, userconfig.ErrOutsideHome) {
				err = fmt.Errorf("%w; set %s=true to use it anyway", err, EnvConfigDirAllowOutsideHome)
			}
			add(fmt.Errorf("%w: %s: %w", ErrInvalid, EnvConfigDir, err))
		}
		c.ConfigDir = userconfig.Dir(dir)
	}

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return c, nil
}

// Load is Define, Parse and Build for a caller with no flags of its own.
func Load(args []string, env func(string) string) (Config, error) {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	s := Define(fs, env)
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	return s.Build()
}

func parseBool(name, v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false, nil
	case "1", "true", "yes", "on":
		return true, nil
	}
	return false, fmt.Errorf("%w: %s %q (want true or false)", ErrInvalid, name, v)
}

// splitList splits a comma list, trimming each item and dropping empty
// ones, so "a, b," is two items.
func splitList(v string) []string {
	var out []string
	for item := range strings.SplitSeq(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func parseToolsets(v string) ([]string, error) {
	var out, bad []string
	for _, name := range splitList(strings.ToLower(v)) {
		switch {
		case name == ToolsetAll:
			out = append(out, Toolsets...)
		case slices.Contains(Toolsets, name):
			out = append(out, name)
		default:
			bad = append(bad, name)
		}
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("%w: %s: unknown toolset %s (want %s, or %s)",
			ErrInvalid, EnvToolsets, strings.Join(bad, ", "), strings.Join(Toolsets, ", "), ToolsetAll)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// parseNamespaces accepts group and project paths. A leading or trailing
// slash is dropped; "..", an empty segment and anything outside GitLab's
// path characters are refused, because a confinement that cannot match
// anything is a mistake worth hearing about at start.
func parseNamespaces(v string) ([]string, error) {
	var out, bad []string
	for _, ns := range splitList(v) {
		ns = strings.Trim(ns, "/")
		if !namespacePattern.MatchString(ns) || slices.Contains(strings.Split(ns, "/"), "..") {
			bad = append(bad, fmt.Sprintf("%q", ns))
			continue
		}
		out = append(out, ns)
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("%w: %s: not a group or project path: %s", ErrInvalid, EnvWriteNamespaces, strings.Join(bad, ", "))
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// parseUploadDirs reads the directories upload_file may read from, split
// as PATH is: on colons, or semicolons on Windows. Each must be absolute,
// because the server's working directory is whatever the client started
// it in, and a relative directory would mean that.
func parseUploadDirs(v string) ([]string, error) {
	var out, bad []string
	for _, dir := range filepath.SplitList(v) {
		dir = strings.TrimSpace(dir)
		switch {
		case dir == "":
		case !filepath.IsAbs(dir):
			bad = append(bad, fmt.Sprintf("%q", dir))
		default:
			out = append(out, filepath.Clean(dir))
		}
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("%w: %s: not an absolute directory: %s", ErrInvalid, EnvUploadDirs, strings.Join(bad, ", "))
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// parseTestInstance reads the development override. Unset means
// gitlab.com. Set, it must be loopback: the override exists for the
// in-memory instance, and a token sent anywhere else would reach a
// host that did not issue it.
func parseTestInstance(v string) (instance.Instance, bool, error) {
	raw := strings.TrimSpace(v)
	if raw == "" {
		return instance.GitLabCom, false, nil
	}
	inst, err := instance.Parse(raw)
	if err != nil {
		return instance.Instance{}, false, fmt.Errorf("%w: %s: %w", ErrInvalid, EnvTestInstance, err)
	}
	if !instance.IsLoopback(inst.Hostname()) {
		return instance.Instance{}, false, fmt.Errorf("%w: %s must be a loopback address (127.0.0.1, ::1 or localhost); "+
			"this server serves gitlab.com only", ErrInvalid, EnvTestInstance)
	}
	return inst, true, nil
}

func parseTimeout(v string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("%w: %s %q: %w", ErrInvalid, EnvHTTPTimeout, v, err)
	}
	if d < MinHTTPTimeout || d > MaxHTTPTimeout {
		return 0, fmt.Errorf("%w: %s %s must be between %s and %s", ErrInvalid, EnvHTTPTimeout, d, MinHTTPTimeout, MaxHTTPTimeout)
	}
	return d, nil
}

// NewLogger builds the process logger. w must be stderr on the server
// path: stdout carries only JSON-RPC frames.
func NewLogger(c Config, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: c.LogLevel.Slog()}
	if c.LogFormat == LogJSON {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}
