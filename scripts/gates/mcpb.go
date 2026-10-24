package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/mmedum/gitlab-mcp/v4/internal/config"
	"github.com/mmedum/gitlab-mcp/v4/scripts/internal/gatekit"
)

// The bundle gate. Its checks are referential — does every name in the
// manifest resolve to a file the packer stages, for the right platform —
// and the staged names are static, so they run on every commit against
// the committed manifest with no build. Only packing waits for a release.

const (
	// mcpbManifestPath is the committed manifest.
	mcpbManifestPath = "packaging/mcpb/manifest.json"
	// mcpbMinManifestVersion is the floor. Claims about $schema hold the
	// manifest against itself; a stale manifest is self-consistent.
	mcpbMinManifestVersion = "0.3"
	// mcpbNoLoginPhrase must appear in long_description: the bundle
	// cannot sign anybody in, and an install that omits this appears to
	// work and then fails on the first call.
	mcpbNoLoginPhrase = "does not log you in"
	// mcpbMinDesktop is the claude_desktop requirement the manifest
	// declares.
	mcpbMinDesktop = ">=0.10.0"
)

// mcpbStaged is one file the packer puts in the bundle. The table below
// is the single source of the staged names: the gate reads it without a
// build, the packer reads it to find the files, and the Linux launcher
// is generated from it.
type mcpbStaged struct {
	// path is where the file lands in the bundle.
	path string
	// glob finds it under the release's dist directory; empty for a
	// generated file.
	glob string
	// launcher marks the generated Linux launcher, written at pack time
	// from this table.
	launcher bool
	// platform is the one this file is the entry point for; empty for a
	// binary the launcher chooses.
	platform string
	// goos and goarch say where the file runs, for reading back its
	// --version on the host. goarch "all" is a universal binary.
	goos, goarch string
	// uname lists the `uname -m` values the launcher maps to this file.
	uname []string
	// repo is a file copied from the repository root rather than dist.
	repo string
}

// mcpbFiles is what a bundle contains. goreleaser names directories
// <build id>_<goos>_<goarch>[_<variant>], and the universal binary's
// <id>_darwin_all.
var mcpbFiles = []mcpbStaged{
	{path: "server/" + gatekit.BinaryName + "-darwin", glob: "*_darwin_all/" + gatekit.BinaryName,
		platform: "darwin", goos: "darwin", goarch: "all"},
	{path: "server/" + gatekit.BinaryName + ".exe", glob: "*_windows_amd64*/" + gatekit.BinaryName + ".exe",
		platform: "win32", goos: "windows", goarch: "amd64"},
	{path: "server/launch-linux.sh", launcher: true,
		platform: "linux"},
	{path: "server/" + gatekit.BinaryName + "-linux-amd64", glob: "*_linux_amd64*/" + gatekit.BinaryName,
		goos: "linux", goarch: "amd64", uname: []string{"x86_64", "amd64"}},
	{path: "server/" + gatekit.BinaryName + "-linux-arm64", glob: "*_linux_arm64*/" + gatekit.BinaryName,
		goos: "linux", goarch: "arm64", uname: []string{"aarch64", "arm64"}},
	// Apache-2.0 section 4(d): a redistribution carries the NOTICE, and the
	// bundle redistributes the binaries as the archives do.
	{path: "LICENSE", repo: "LICENSE"},
	{path: "NOTICE", repo: "NOTICE"},
	// The README goes with the binaries, as in the archives.
	{path: "README.md", repo: "README.md"},
}

// mcpbManifest is the part of the manifest the checks read. The whole
// document is decoded a second time as a map, so unknown keys survive.
type mcpbManifest struct {
	Schema          string   `json:"$schema"`
	ManifestVersion string   `json:"manifest_version"`
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	Description     string   `json:"description"`
	LongDescription string   `json:"long_description"`
	Support         string   `json:"support"`
	License         string   `json:"license"`
	Documentation   string   `json:"documentation"`
	Keywords        []string `json:"keywords"`
	PrivacyPolicies []string `json:"privacy_policies"`
	Author          struct {
		Name string `json:"name"`
	} `json:"author"`
	Repository struct {
		URL string `json:"url"`
	} `json:"repository"`
	Server struct {
		Type       string `json:"type"`
		EntryPoint string `json:"entry_point"`
		MCPConfig  struct {
			Command           string            `json:"command"`
			Env               map[string]string `json:"env"`
			PlatformOverrides map[string]struct {
				Command string            `json:"command"`
				Env     map[string]string `json:"env"`
			} `json:"platform_overrides"`
		} `json:"mcp_config"`
	} `json:"server"`
	UserConfig    map[string]json.RawMessage `json:"user_config"`
	Compatibility struct {
		ClaudeDesktop string   `json:"claude_desktop"`
		Platforms     []string `json:"platforms"`
	} `json:"compatibility"`
}

// mcpb is the gate: the committed manifest, in the working directory.
func mcpb(out io.Writer, _ []string) error {
	return mcpbCheck(out, ".")
}

func mcpbCheck(out io.Writer, root string) error {
	path := filepath.Join(root, mcpbManifestPath)
	m, raw, err := mcpbRead(path)
	if err != nil {
		return err
	}
	problems := mcpbValidate(m, mcpbFiles, launcherNamesIn(launcherScript(mcpbFiles)))
	problems = append(problems, mcpbDocumentProblems(m, raw)...)
	if m.Version != gatekit.PlaceholderVersion {
		problems = append(problems, fmt.Sprintf("version is %q; the committed manifest carries %q and the "+
			"packer writes the real one", m.Version, gatekit.PlaceholderVersion))
	}
	if want := mcpbLicense(root); want != "" && m.License != want {
		problems = append(problems, fmt.Sprintf("license is %q and LICENSE is %s", m.License, want))
	}
	problems = append(problems, mcpbRepoProblems(root, m)...)
	slices.Sort(problems)
	if err := gatekit.Problems(out, mcpbManifestPath, problems); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "mcpb: %s: %d staged files, %d platforms, %d user_config keys; every name resolves\n",
		mcpbManifestPath, len(mcpbFiles), len(m.Compatibility.Platforms), len(m.UserConfig))
	return nil
}

// mcpbRead decodes the manifest twice: typed for the checks, and as a
// document for the schema and the packer, which must keep every key.
func mcpbRead(path string) (mcpbManifest, []byte, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // a repository path
	if err != nil {
		return mcpbManifest{}, nil, err
	}
	var m mcpbManifest
	var doc map[string]any
	for _, into := range []any{&m, &doc} {
		if err := json.Unmarshal(raw, into); err != nil {
			return mcpbManifest{}, nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
		}
	}
	return m, raw, nil
}

// mcpbDocumentProblems holds what the manifest says about itself: its
// schema reference, its version floor, the vendored schema, the no-login
// sentence, support and the desktop requirement.
func mcpbDocumentProblems(m mcpbManifest, raw []byte) []string {
	problems := mcpbShapeProblems(m.Schema, m.ManifestVersion, m.Support)
	if name, err := vendorManifestSchemaFile(m.ManifestVersion); err != nil {
		problems = append(problems, err.Error())
	} else if err := vendorValidate(name, "the manifest", raw); err != nil {
		problems = append(problems, err.Error())
	}
	if !strings.Contains(strings.ToLower(m.LongDescription), mcpbNoLoginPhrase) {
		problems = append(problems, fmt.Sprintf("long_description does not say %q: the bundle cannot sign "+
			"anybody in, and that is the first thing a person who installs it meets", mcpbNoLoginPhrase))
	}
	if m.Compatibility.ClaudeDesktop != mcpbMinDesktop {
		problems = append(problems, fmt.Sprintf("compatibility.claude_desktop is %q, want %q",
			m.Compatibility.ClaudeDesktop, mcpbMinDesktop))
	}
	if m.Name != gatekit.BinaryName {
		problems = append(problems, fmt.Sprintf("name is %q, want %q", m.Name, gatekit.BinaryName))
	}
	if len(m.PrivacyPolicies) == 0 {
		problems = append(problems, "privacy_policies is empty; the manifest specification requires one for a "+
			"server that connects to a service processing user data, as this one does to gitlab.com")
	}
	return problems
}

// mcpbUserConfigRef matches every ${user_config.x} in a value, including
// one composed into a longer string.
var mcpbUserConfigRef = regexp.MustCompile(`\$\{user_config\.([A-Za-z0-9_]+)\}`)

// mcpbOption is the part of one user_config entry the checks read.
type mcpbOption struct {
	Type        string          `json:"type"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Required    bool            `json:"required"`
	Default     json.RawMessage `json:"default"`
	Multiple    bool            `json:"multiple"`
}

// options decodes each user_config entry the checks can read. An entry
// that does not decode is left out here: the schema refuses it, and so
// does mcpbValidate when an env value spends it.
func (m mcpbManifest) options() map[string]mcpbOption {
	out := map[string]mcpbOption{}
	for key, raw := range m.UserConfig {
		var opt mcpbOption
		if json.Unmarshal(raw, &opt) == nil {
			out[key] = opt
		}
	}
	return out
}

// mcpbSubstituted refuses a user_config key an env value spends that a
// host may leave unsubstituted: optional with no default, or multiple.
func mcpbSubstituted(where, key string, opt mcpbOption) []string {
	var problems []string
	if !opt.Required && opt.Default == nil {
		problems = append(problems, fmt.Sprintf("%s spends ${user_config.%s}, which is optional with no default; "+
			"a host leaves it as written when the person sets nothing, so give it a default", where, key))
	}
	if opt.Multiple {
		problems = append(problems, fmt.Sprintf("%s spends ${user_config.%s}, which takes multiple values; "+
			"a host does not substitute an array in a string", where, key))
	}
	return problems
}

// mcpbEnvRefProblems holds every ${user_config.x} in an env value to a
// declared key that is substituted whatever the person does: the
// reference host leaves the reference as written for an optional key
// with no default, and for an array of values in a string
// (src/shared/config.ts in anthropics/mcpb at v2.1.2), and the server
// would then refuse it.
func mcpbEnvRefProblems(m mcpbManifest) []string {
	var problems []string
	envs := map[string]map[string]string{"mcp_config": m.Server.MCPConfig.Env}
	for platform, over := range m.Server.MCPConfig.PlatformOverrides {
		envs["platform_overrides."+platform] = over.Env
	}
	for where, env := range envs {
		for key, value := range env {
			for _, ref := range mcpbUserConfigRef.FindAllStringSubmatch(value, -1) {
				raw, ok := m.UserConfig[ref[1]]
				if !ok {
					problems = append(problems, fmt.Sprintf("%s.env.%s spends ${user_config.%s}, which user_config "+
						"does not declare", where, key, ref[1]))
					continue
				}
				var opt mcpbOption
				if err := json.Unmarshal(raw, &opt); err != nil {
					problems = append(problems, fmt.Sprintf("user_config.%s is not an option: %v", ref[1], err))
					continue
				}
				problems = append(problems, mcpbSubstituted(where+".env."+key, ref[1], opt)...)
			}
		}
	}
	return problems
}

// mcpbValidate is the referential checks. It takes the staged table and
// the launcher's names as arguments so a test can break each one.
func mcpbValidate(m mcpbManifest, files []mcpbStaged, launcher []string) []string {
	var problems []string
	staged := map[string]mcpbStaged{}
	for _, f := range files {
		staged[f.path] = f
	}
	resolve := func(cmd string) string {
		return strings.TrimPrefix(strings.TrimPrefix(cmd, "${__dirname}"), "/")
	}

	// entry_point names a staged file.
	if _, ok := staged[m.Server.EntryPoint]; !ok {
		problems = append(problems, fmt.Sprintf("entry_point %q is not a file the packer stages", m.Server.EntryPoint))
	}

	// Every command names one too.
	commands := map[string]string{"": m.Server.MCPConfig.Command}
	for platform, over := range m.Server.MCPConfig.PlatformOverrides {
		if over.Command != "" {
			commands[platform] = over.Command
		}
	}
	for _, platform := range slices.Sorted(maps.Keys(commands)) {
		where := "mcp_config.command"
		if platform != "" {
			where = "platform_overrides." + platform + ".command"
		}
		if _, ok := staged[resolve(commands[platform])]; !ok {
			problems = append(problems, fmt.Sprintf("%s is %q, which the packer does not stage", where, commands[platform]))
		}
	}

	problems = append(problems, mcpbEnvRefProblems(m)...)

	// Every override is a claimed platform.
	claimed := map[string]bool{}
	for _, p := range m.Compatibility.Platforms {
		claimed[p] = true
	}
	for platform, over := range m.Server.MCPConfig.PlatformOverrides {
		if !claimed[platform] {
			problems = append(problems, fmt.Sprintf("platform_overrides has %q, which compatibility.platforms "+
				"does not claim", platform))
		}
		// The reference host replaces mcp_config.env with an override's
		// env rather than merging the two (anthropics/mcpb v2.1.2), so the
		// settings would not reach that platform, and the defaults checks
		// below read mcp_config.env alone.
		if len(over.Env) > 0 {
			problems = append(problems, fmt.Sprintf("platform_overrides.%s.env is set; a host replaces "+
				"mcp_config.env with it, so put every variable in mcp_config.env", platform))
		}
	}
	if len(m.Compatibility.Platforms) == 0 {
		problems = append(problems, "compatibility.platforms claims no platform")
	}

	// Each claimed platform spawns the file staged for it, not merely a
	// staged file: without its override, a platform runs the default
	// command, which is another platform's binary.
	for _, platform := range m.Compatibility.Platforms {
		cmd, ok := commands[platform]
		if !ok {
			cmd = commands[""]
		}
		if f, found := staged[resolve(cmd)]; found && f.platform != platform {
			problems = append(problems, fmt.Sprintf("platform %q runs %q, which is staged for %q",
				platform, f.path, cmp.Or(f.platform, "the launcher")))
		}
	}

	// The launcher chooses between the packer's names.
	var launchable []string
	for _, f := range files {
		if len(f.uname) > 0 {
			launchable = append(launchable, filepath.Base(f.path))
		}
	}
	chosen := slices.Clone(launcher)
	slices.Sort(launchable)
	slices.Sort(chosen)
	if !slices.Equal(launchable, chosen) {
		problems = append(problems, fmt.Sprintf("the launcher runs [%s]; the packer stages [%s]",
			strings.Join(chosen, " "), strings.Join(launchable, " ")))
	}
	return problems
}

var (
	// mcpbPinnedSchema captures the format version a schema URL names.
	mcpbPinnedSchema = regexp.MustCompile(`/mcpb-manifest-v(\d+\.\d+)\.schema\.json$`)
	// mcpbUpstreamSchema is the whole upstream URL, capturing its ref.
	// An allow-list on the whole URL: a blacklist of branch names passes
	// any other branch, a partial tag and somebody else's host.
	mcpbUpstreamSchema = regexp.MustCompile(
		`^https://raw\.githubusercontent\.com/anthropics/mcpb/([^/]+)/schemas/mcpb-manifest-v\d+\.\d+\.schema\.json$`)
	// mcpbImmutableRef is a full release tag or a commit SHA.
	mcpbImmutableRef = regexp.MustCompile(`^(v[0-9]+\.[0-9]+\.[0-9]+|[0-9a-f]{40})$`)
)

// mcpbShapeProblems holds the manifest's declaration of its own format.
func mcpbShapeProblems(schema, manifestVersion, support string) []string {
	var problems []string
	switch {
	case schema == "":
		problems = append(problems, "the manifest has no $schema, so nothing says which format it is")
	case !mcpbPinnedSchema.MatchString(schema):
		problems = append(problems, fmt.Sprintf("$schema %q is not the versioned mcpb-manifest-v<x.y>.schema.json "+
			"file; the unpinned path serves whatever upstream publishes today", schema))
	case !mcpbUpstreamSchema.MatchString(schema):
		problems = append(problems, fmt.Sprintf("$schema %q is not upstream's published path", schema))
	case !mcpbImmutableRef.MatchString(mcpbUpstreamSchema.FindStringSubmatch(schema)[1]):
		problems = append(problems, fmt.Sprintf("$schema is served from ref %q, which can move; name a full tag "+
			"or a commit SHA", mcpbUpstreamSchema.FindStringSubmatch(schema)[1]))
	default:
		if declared := mcpbPinnedSchema.FindStringSubmatch(schema)[1]; declared != manifestVersion {
			problems = append(problems, fmt.Sprintf("manifest_version is %q and $schema pins v%s", manifestVersion, declared))
		}
	}
	if mcpbBehind(manifestVersion, mcpbMinManifestVersion) {
		problems = append(problems, fmt.Sprintf("manifest_version is %q, below the floor of %s",
			manifestVersion, mcpbMinManifestVersion))
	}
	if !strings.HasPrefix(support, "https://") {
		problems = append(problems, fmt.Sprintf("support is %q; a bundle that fails on somebody's desktop "+
			"must say where to report it, as an https URL", support))
	}
	return problems
}

// mcpbBehind reports whether a major.minor version is below the floor,
// numerically: "0.10" is not older than "0.3". An unreadable version is
// behind.
func mcpbBehind(version, floor string) bool {
	parse := func(v string) (int, int, bool) {
		a, b, ok := strings.Cut(v, ".")
		if !ok {
			return 0, 0, false
		}
		x, err1 := strconv.Atoi(a)
		y, err2 := strconv.Atoi(b)
		return x, y, err1 == nil && err2 == nil
	}
	major, minor, ok := parse(version)
	fMajor, fMinor, _ := parse(floor)
	if !ok {
		return true
	}
	if major != fMajor {
		return major < fMajor
	}
	return minor < fMinor
}

// mcpbLicense is the SPDX id of the repository's LICENSE, or "" when
// there is none or it is not recognized.
func mcpbLicense(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "LICENSE")) //nolint:gosec // a repository path
	if err != nil {
		return ""
	}
	switch text := string(data); {
	case strings.Contains(text, "Apache License"):
		return "Apache-2.0"
	case strings.Contains(text, "MIT License"):
		return "MIT"
	}
	return ""
}

// mcpbExcluded are the settings the server reads that the bundle does
// not ask for, each with its reason. Every other setting is asked for,
// development overrides aside (§12).
var mcpbExcluded = map[string]string{
	config.EnvLogLevel:                  "a diagnostic: Claude Desktop keeps the server's stderr in its own log",
	config.EnvLogFormat:                 "a diagnostic, as the log level is",
	config.EnvHTTPTimeout:               "tuning with a bounded default that a bundle install has no reason to change",
	config.EnvConfigDir:                 "where profiles live; a bundle uses the store login wrote to",
	config.EnvConfigDirAllowOutsideHome: "meaningful only with a configuration directory, which the bundle does not set",
	config.EnvRefreshToken:              "a secret the keyring holds; a bundle never carries a token",
}

// mcpbUserConfig is what the bundle asks for at install, each key with
// the setting it sets.
var mcpbUserConfig, mcpbExclusionProblems = mcpbUserConfigFrom(config.Vars, mcpbExcluded)

// mcpbUserConfigFrom derives the bundle's keys from the settings the
// server reads: each one not a development override and not excluded,
// keyed by its name without the prefix, lowercased. It also refuses an
// exclusion that names no setting, so a removed one cannot linger.
func mcpbUserConfigFrom(vars []config.Var, excluded map[string]string) (map[string]config.Var, []string) {
	keys := map[string]config.Var{}
	read := map[string]bool{}
	for _, v := range vars {
		read[v.Name] = true
		if v.Dev || excluded[v.Name] != "" {
			continue
		}
		keys[strings.ToLower(strings.TrimPrefix(v.Name, config.EnvPrefix))] = v
	}
	var problems []string
	for _, name := range slices.Sorted(maps.Keys(excluded)) {
		if !read[name] {
			problems = append(problems, fmt.Sprintf("the bundle excludes %s, which the server does not read", name))
		}
	}
	return keys, problems
}

// mcpbRefused are the combinations of the bundle's switches the server
// refuses at startup, each sorted; the fields involved say so in their
// descriptions. The gate starts every combination and holds the refused
// ones to this list both ways.
var mcpbRefused = [][]string{{"enable_destructive", "read_only"}, {"enable_ship", "read_only"}}

// mcpbHostEnv is the environment a host builds from the bundle's
// defaults, with the keys in on set to true. A required key with no
// default is left out, since the person must give it a value.
func mcpbHostEnv(m mcpbManifest, on ...string) map[string]string {
	opts := m.options()
	env := map[string]string{}
	for name, value := range m.Server.MCPConfig.Env {
		ref := mcpbUserConfigRef.FindStringSubmatch(value)
		if ref == nil {
			env[name] = value
			continue
		}
		if slices.Contains(on, ref[1]) {
			env[name] = "true"
			continue
		}
		var def any
		if opt := opts[ref[1]]; opt.Default == nil || json.Unmarshal(opt.Default, &def) != nil {
			continue
		}
		env[name] = fmt.Sprint(def)
	}
	return env
}

// mcpbLoad builds the server's configuration from an environment alone.
func mcpbLoad(env map[string]string) (config.Config, error) {
	return config.Load(nil, func(name string) string { return env[name] })
}

// mcpbDefaultsProblems starts the configuration from the bundle's
// defaults and from no settings at all, and refuses any difference: a
// bundle install turns on nothing an unconfigured server leaves off.
// Claude Desktop approves each tool, and a person can always allow one,
// so this is the guard a bundle install keeps (§18 row 130). A switch
// is also asked as a boolean, so Desktop shows it as one.
func mcpbDefaultsProblems(m mcpbManifest) []string {
	var problems []string
	opts := m.options()
	for _, key := range slices.Sorted(maps.Keys(mcpbUserConfig)) {
		v, opt := mcpbUserConfig[key], opts[key]
		if _, ok := opts[key]; !ok {
			continue
		}
		if v.Switch && opt.Type != "boolean" {
			problems = append(problems, fmt.Sprintf("user_config.%s is a %s; %s is a switch, so ask for a boolean",
				key, cmp.Or(opt.Type, "field with no type"), v.Name))
		}
		if v.Name == config.EnvToolsets {
			for _, name := range append(slices.Clone(config.Toolsets), config.ToolsetAll) {
				if !strings.Contains(opt.Description, name) {
					problems = append(problems, fmt.Sprintf("user_config.%s does not name the %q toolset", key, name))
				}
			}
		}
	}
	got, err := mcpbLoad(mcpbHostEnv(m))
	if err != nil {
		return append(problems, fmt.Sprintf("the bundle's defaults do not start the server: %v", err))
	}
	want, err := mcpbLoad(nil)
	if err != nil {
		return append(problems, fmt.Sprintf("an unconfigured server does not start: %v", err))
	}
	if diff := mcpbConfigDiff(got, want); len(diff) > 0 {
		problems = append(problems, fmt.Sprintf("the bundle's defaults start the server differently from no "+
			"settings, in %s; a bundle install may turn on nothing an unconfigured server leaves off",
			strings.Join(diff, ", ")))
	}
	return problems
}

// mcpbConfigDiff names the fields two configurations disagree on.
func mcpbConfigDiff(a, b config.Config) []string {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	var fields []string
	for i := range va.NumField() {
		if !reflect.DeepEqual(va.Field(i).Interface(), vb.Field(i).Interface()) {
			fields = append(fields, va.Type().Field(i).Name)
		}
	}
	return fields
}

// mcpbRefusalProblems starts every combination of the bundle's switches
// and holds the ones the server refuses to the declared list, both
// ways: a refusal the list does not declare, which no description then
// warns of, and a declared one the server accepts.
func mcpbRefusalProblems(m mcpbManifest, declared [][]string) []string {
	if _, err := mcpbLoad(mcpbHostEnv(m)); err != nil {
		return nil // the defaults check reports it
	}
	opts := m.options()
	var switches []string
	for _, key := range slices.Sorted(maps.Keys(mcpbUserConfig)) {
		if opts[key].Type == "boolean" {
			switches = append(switches, key)
		}
	}
	var problems []string
	for _, pair := range declared {
		for _, key := range pair {
			if !slices.Contains(switches, key) {
				problems = append(problems, fmt.Sprintf("mcpbRefused names %s, which is not a switch the bundle asks for", key))
				continue
			}
			for _, other := range pair {
				if other != key && !strings.Contains(opts[key].Description, opts[other].Title) {
					problems = append(problems, fmt.Sprintf("user_config.%s does not name %q, which the server "+
						"refuses it with", key, opts[other].Title))
				}
			}
		}
	}
	for set := 1; set < 1<<len(switches); set++ {
		var on []string
		for i, key := range switches {
			if set&(1<<i) != 0 {
				on = append(on, key)
			}
		}
		_, err := mcpbLoad(mcpbHostEnv(m, on...))
		expected := slices.ContainsFunc(declared, func(pair []string) bool {
			return !slices.ContainsFunc(pair, func(k string) bool { return !slices.Contains(on, k) })
		})
		switch {
		case err != nil && !expected:
			problems = append(problems, fmt.Sprintf("the server refuses %s together, and mcpbRefused does not "+
				"declare it, so no field's description warns of it: %v", strings.Join(on, ", "), err))
		case err == nil && expected:
			problems = append(problems, fmt.Sprintf("mcpbRefused declares a refusal among %s, and the server "+
				"starts with them", strings.Join(on, ", ")))
		}
	}
	return problems
}

// mcpbServerPackage is where the one description constant lives, which
// the manifest and the registry entry both carry.
const mcpbServerPackage = "internal/server"

// mcpbRepoProblems holds the manifest to the rest of the repository:
// the server's description constant, the settings the server reads, the
// files the packer copies from the root, and the metadata a desktop
// shows.
func mcpbRepoProblems(root string, m mcpbManifest) []string {
	var problems []string
	desc, err := serverDescription(root)
	switch {
	case err != nil:
		problems = append(problems, err.Error())
	case m.Description != desc:
		problems = append(problems, fmt.Sprintf("description is %q and %s.Description is %q; one constant feeds both",
			m.Description, mcpbServerPackage, desc))
	}
	known := map[string]bool{}
	for _, v := range config.EnvVars() {
		known[v] = true
	}
	envs := map[string]map[string]string{"mcp_config": m.Server.MCPConfig.Env}
	for platform, over := range m.Server.MCPConfig.PlatformOverrides {
		envs["platform_overrides."+platform] = over.Env
	}
	for _, where := range slices.Sorted(maps.Keys(envs)) {
		for _, key := range slices.Sorted(maps.Keys(envs[where])) {
			switch {
			case slices.Contains(config.DevVars(), key):
				problems = append(problems, fmt.Sprintf("%s.env sets %s, a development override no bundle may carry", where, key))
			case !known[key]:
				problems = append(problems, fmt.Sprintf("%s.env sets %s, which the server does not read", where, key))
			case mcpbExcluded[key] != "":
				problems = append(problems, fmt.Sprintf("%s.env sets %s, which the bundle excludes: %s",
					where, key, mcpbExcluded[key]))
			}
		}
	}
	problems = append(problems, mcpbExclusionProblems...)
	for _, key := range slices.Sorted(maps.Keys(mcpbUserConfig)) {
		if _, ok := m.UserConfig[key]; !ok {
			problems = append(problems, fmt.Sprintf("user_config does not ask for %s", key))
			continue
		}
		want := "${user_config." + key + "}"
		if got := m.Server.MCPConfig.Env[mcpbUserConfig[key].Name]; got != want {
			problems = append(problems, fmt.Sprintf("mcp_config.env.%s is %q, want %q", mcpbUserConfig[key].Name, got, want))
		}
	}
	problems = append(problems, mcpbDefaultsProblems(m)...)
	problems = append(problems, mcpbRefusalProblems(m, mcpbRefused)...)
	for _, f := range mcpbFiles {
		if f.repo == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, f.repo)); err != nil {
			problems = append(problems, fmt.Sprintf("the packer copies %s from the repository and it is not there", f.repo))
		}
	}
	for field, value := range map[string]string{
		"author.name": m.Author.Name, "repository.url": m.Repository.URL, "documentation": m.Documentation,
	} {
		if strings.TrimSpace(value) == "" {
			problems = append(problems, fmt.Sprintf("%s is empty", field))
		}
	}
	if len(m.Keywords) == 0 {
		problems = append(problems, "keywords is empty")
	}
	return problems
}

// serverDescription reads the Description constant out of the server
// package's source, so the gate holds the manifest to the code without
// compiling the server into itself.
func serverDescription(root string) (string, error) {
	fset := token.NewFileSet()
	files, err := gatekit.ParseGoDir(fset, filepath.Join(root, filepath.FromSlash(mcpbServerPackage)), 0)
	if err != nil {
		return "", fmt.Errorf("%s: %w", mcpbServerPackage, err)
	}
	for _, f := range files {
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if name.Name != "Description" || i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						return "", fmt.Errorf("%s.Description is not a string literal", mcpbServerPackage)
					}
					return strconv.Unquote(lit.Value)
				}
			}
		}
	}
	return "", fmt.Errorf("%s declares no Description constant", mcpbServerPackage)
}
