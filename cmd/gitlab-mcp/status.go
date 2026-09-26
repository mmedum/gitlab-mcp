package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/app"
	"github.com/mmedum/gitlab-mcp/internal/config"
	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/redact"
	"github.com/mmedum/gitlab-mcp/internal/scopes"
	"github.com/mmedum/gitlab-mcp/internal/version"
)

// statusSchemaVersion changes only when a field of `status --json` is
// removed or changes meaning, never when one is added.
const statusSchemaVersion = 1

// statusReport is everything `status` knows, collected once and written
// either for a person or for a script. One collector, two renderers,
// because a script reading the text breaks the day a label is reworded.
//
// Both renderers pass through one masker, so the instance's host, the
// application id and the account come out as {host 1}, {client-id 1}
// and {user 1}: the bug form asks for this output.
type statusReport struct {
	SchemaVersion int    `json:"schema_version"`
	Binary        string `json:"binary"`
	Version       string `json:"version"`
	// Reason is the first thing that stopped the report, or null. It is
	// the field to read after a non-zero exit.
	Reason       *string           `json:"reason"`
	Profile      string            `json:"profile"`
	ConfigDir    string            `json:"config_dir"`
	Instance     *string           `json:"instance"`
	InstanceKind *string           `json:"instance_kind"`
	ClientID     *string           `json:"client_id"`
	Account      *string           `json:"account"`
	Credentials  statusCredentials `json:"credentials"`
	Scopes       statusScopes      `json:"scopes"`
	Settings     statusSettings    `json:"settings"`
	Profiles     []string          `json:"profiles"`
	Probe        statusProbe       `json:"probe"`
}

type statusCredentials struct {
	// Resolved is the field to branch on: false means every tool
	// answers [auth] until login.
	Resolved   bool    `json:"resolved"`
	TokenStore *string `json:"token_store"`
	// Reason says why nothing resolved; null when something did.
	Reason *string `json:"reason"`
}

type statusScopes struct {
	// Granted is what the last login was granted.
	Granted []string `json:"granted"`
	// Required is what this configuration needs.
	Required []string `json:"required"`
	// Missing is Required less what Granted covers; non-empty means a
	// setting changed since the last login.
	Missing []string `json:"missing"`
}

type statusSettings struct {
	ReadOnly          bool     `json:"read_only"`
	EnableShip        bool     `json:"enable_ship"`
	EnableDestructive bool     `json:"enable_destructive"`
	Toolsets          []string `json:"toolsets"`
	WriteNamespaces   int      `json:"write_namespaces"`
	CAFile            bool     `json:"ca_file"`
	AllowHTTP         bool     `json:"allow_http"`
	HTTPTimeout       string   `json:"http_timeout"`
	LogLevel          string   `json:"log_level"`
	LogFormat         string   `json:"log_format"`
}

// statusProbe is the live check. Ran is false when --no-probe skipped
// it, which is neither a pass nor a failure and must not read as either.
type statusProbe struct {
	Ran    bool    `json:"ran"`
	OK     bool    `json:"ok"`
	Reason *string `json:"reason"`
}

// masker registers what identifies this setup, so every line printed
// masks it the same way.
//
// When the settings could not be resolved, the instance as configured is
// still registered, so an error naming it (http refused, say) prints
// masked like every other line.
func masker(s *app.Settings, rawInstance string) *redact.Masker {
	m := redact.NewMasker()
	if s == nil {
		if h := rawHost(rawInstance); h != "" {
			m.Known(redact.KindHost, h)
		}
		return m
	}
	m.Known(redact.KindHost, s.Instance.Hostname())
	m.Known(redact.KindClientID, s.ClientID)
	if s.Profile != nil {
		m.Known(redact.KindUser, s.Profile.User.Username)
	}
	return m
}

// rawHost is the hostname of an instance as a person typed it, before
// it was validated: a bare host, or a URL with or without a scheme.
func rawHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// displayDir shows a directory with the home directory as ~, which
// names the account on the machine.
func displayDir(dir string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return dir
	}
	if rel, err := filepath.Rel(home, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Join("~", rel)
	}
	return dir
}

func newStatusReport(ctx context.Context, cfg config.Config, s *app.Settings, probe bool) (statusReport, int) {
	r := statusReport{
		SchemaVersion: statusSchemaVersion,
		Binary:        "gitlab-mcp",
		Version:       version.String(),
		Profile:       s.Profile.Name,
		ConfigDir:     displayDir(s.Profile.Dir.Profile(s.Profile.Name)),
		Instance:      orNil(s.Instance.String()),
		InstanceKind:  orNil(app.InstanceKind(s.Instance)),
		ClientID:      orNil(s.ClientID),
		Account:       orNil(s.Profile.User.Username),
		Scopes: statusScopes{
			Granted:  orEmpty(s.Profile.User.Scopes),
			Required: cfg.Scopes(),
			Missing:  []string{},
		},
		Settings: statusSettings{
			ReadOnly: cfg.ReadOnly, EnableShip: cfg.EnableShip, EnableDestructive: cfg.EnableDestructive,
			Toolsets: orEmpty(cfg.Toolsets), WriteNamespaces: len(cfg.WriteNamespaces),
			CAFile: cfg.CAFile != "", AllowHTTP: cfg.AllowHTTP, HTTPTimeout: cfg.HTTPTimeout.String(),
			LogLevel: string(cfg.LogLevel), LogFormat: string(cfg.LogFormat),
		},
		Profiles: []string{},
	}
	if s.Profile.HasUser && len(s.Profile.User.Scopes) > 0 {
		r.Scopes.Missing = orEmpty(scopes.Missing(s.Profile.User.Scopes, cfg.Scopes()))
	}
	if names, err := cfg.ConfigDir.Profiles(); err == nil && names != nil {
		r.Profiles = names
	}

	code := 0
	switch _, src, err := s.Profile.Store.Resolve(); {
	case s.CredentialsErr != nil:
		r.Credentials.Reason = orNil(s.CredentialsErr.Error())
		code = 1
	case err != nil:
		r.Credentials.Reason = orNil(err.Error())
		code = 1
	default:
		r.Credentials.Resolved = true
		r.Credentials.TokenStore = orNil(string(src))
	}
	if len(r.Scopes.Missing) > 0 {
		code = 1
	}
	if !probe || !r.Credentials.Resolved {
		return r, code
	}

	r.Probe.Ran = true
	ctx, cancel := context.WithTimeout(ctx, cfg.HTTPTimeout)
	defer cancel()
	c, err := s.Client(nil, version.String())
	if err == nil {
		var u string
		if u, err = userOf(ctx, c); err == nil {
			r.Probe.OK = true
			r.Account = orNil(u)
			return r, code
		}
	}
	r.Probe.Reason = orNil(err.Error())
	return r, 1
}

func userOf(ctx context.Context, c *gapi.Client) (string, error) {
	u, err := c.GetCurrentUser(ctx)
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

func (r statusReport) writeText(w io.Writer, m *redact.Masker) {
	p := func(format string, args ...any) { _, _ = io.WriteString(w, m.Text(fmt.Sprintf(format, args...))) }
	p("%s\n\n", version.Info())
	if r.Reason != nil {
		p("configuration:  %s\n", *r.Reason)
		return
	}
	p("profile:        %s\n", r.Profile)
	p("config dir:     %s\n", r.ConfigDir)
	p("instance:       %s (%s)\n", deref(r.Instance), deref(r.InstanceKind))
	p("application:    %s\n", orUnknown(deref(r.ClientID)))
	p("account:        %s\n", orUnknown(deref(r.Account)))
	if r.Credentials.Resolved {
		p("token store:    %s\n", deref(r.Credentials.TokenStore))
	} else {
		p("token store:    none — %s\n", deref(r.Credentials.Reason))
	}
	p("scopes granted: %s\n", orUnknown(strings.Join(r.Scopes.Granted, " ")))
	p("scopes needed:  %s\n", strings.Join(r.Scopes.Required, " "))
	if len(r.Scopes.Missing) > 0 {
		p("                missing %s: run `gitlab-mcp login` again\n", strings.Join(r.Scopes.Missing, " "))
	}
	p("read-only:      %t\n", r.Settings.ReadOnly)
	p("ship:           %t\n", r.Settings.EnableShip)
	p("destructive:    %t\n", r.Settings.EnableDestructive)
	p("toolsets:       %s\n", orNone(strings.Join(r.Settings.Toolsets, ", ")))
	if r.Settings.WriteNamespaces > 0 {
		p("writes:         confined to %d namespace(s)\n", r.Settings.WriteNamespaces)
	}
	p("http timeout:   %s\n", r.Settings.HTTPTimeout)
	switch {
	case !r.Probe.Ran && r.Credentials.Resolved:
		p("probe:          skipped (--no-probe)\n")
	case !r.Probe.Ran:
		p("probe:          not run (not signed in)\n")
	case r.Probe.OK:
		p("probe:          ok (signed in as %s)\n", orUnknown(deref(r.Account)))
	default:
		p("probe:          fail (%s)\n", deref(r.Probe.Reason))
	}
	if len(r.Profiles) > 1 {
		p("profiles:       %s\n", strings.Join(r.Profiles, ", "))
	}
	p("\n%s\n", m.Footer())
}

// writeJSON writes the report as one JSON value, masked the same way.
func (r statusReport) writeJSON(w io.Writer, m *redact.Masker) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, m.Text(string(b))+"\n")
	return err
}

// cmdStatus reports the profile and the settings, after the same
// configuration load the server runs, and by default one call to the
// instance. Exit 1 when the server would start without a working
// sign-in.
func cmdStatus(args []string, stdout, stderr io.Writer, env func(string) string) int {
	f := newFlags("status", env)
	if code := f.parse(args, stderr); code != nil {
		return *code
	}
	r := statusReport{SchemaVersion: statusSchemaVersion, Binary: "gitlab-mcp", Version: version.String(),
		Profiles: []string{}}
	cfg, err := f.build()
	var s *app.Settings
	if err == nil {
		s, err = app.Resolve(cfg, app.Options{Env: env, Keyring: keyringBackend, Warn: warnTo(stderr)})
	}
	m := masker(s, cfg.Instance)
	code := 1
	if err != nil {
		r.Reason = orNil(err.Error())
	} else {
		r, code = newStatusReport(context.Background(), cfg, s, !f.noProbe)
	}
	if f.asJSON {
		if err := r.writeJSON(stdout, m); err != nil {
			return fail(stderr, "%v", err)
		}
		return code
	}
	r.writeText(stdout, m)
	return code
}

// cmdDoctor walks what goes wrong at setup, in the order it goes wrong,
// and names what is missing: the instance, TLS, the sign-in, the
// version and edition, the application, the granted scopes, and one
// /user. Everything is printed through a masker, with a count of what
// it hid. Exit 1 when anything failed.
func cmdDoctor(args []string, stdout, stderr io.Writer, env func(string) string) int {
	f := newFlags("doctor", env)
	cfg, code := f.config(args, stderr)
	if code != nil {
		return *code
	}
	var warnings []string
	s, err := app.Resolve(cfg, app.Options{Env: env, Keyring: keyringBackend,
		Warn: func(msg string) { warnings = append(warnings, msg) }})
	d := &doctor{w: stdout, m: masker(s, cfg.Instance)}
	d.printf("%s\n", version.Info())
	if err != nil {
		d.report(false, "instance", err.Error())
		return d.finish()
	}
	d.printf("profile: %s\n\n", s.Profile.Name)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d.run(ctx, cfg, s)
	for _, w := range warnings {
		d.printf("warning: %s\n", w)
	}
	return d.finish()
}

type doctor struct {
	w        io.Writer
	m        *redact.Masker
	problems int
}

func (d *doctor) printf(format string, args ...any) {
	_, _ = io.WriteString(d.w, d.m.Text(fmt.Sprintf(format, args...)))
}

func (d *doctor) report(ok bool, label, detail string) {
	mark := "ok  "
	if !ok {
		mark = "FAIL"
		d.problems++
	}
	d.printf("[%s] %s\n", mark, label)
	if detail != "" {
		d.printf("       %s\n", strings.ReplaceAll(detail, "\n", "\n       "))
	}
}

func (d *doctor) finish() int {
	if d.problems == 0 {
		d.printf("\nNo problems found.\n")
	} else {
		d.printf("\n%d problem(s).\n", d.problems)
	}
	d.printf("%s\n", d.m.Footer())
	if d.problems > 0 {
		return 1
	}
	return 0
}

// run stops at the first failure a later check depends on, because the
// checks after it would only repeat it.
func (d *doctor) run(ctx context.Context, cfg config.Config, s *app.Settings) {
	d.report(true, "instance", fmt.Sprintf("%s (%s)", s.Instance, app.InstanceKind(s.Instance)))

	if !d.reach(ctx, cfg, s) {
		return
	}

	if s.ClientID == "" {
		d.report(false, "sign-in", noApplication)
		writeSetup(d.w, cfg.Mode())
		return
	}
	if s.CredentialsErr != nil {
		d.report(false, "sign-in", s.CredentialsErr.Error())
		return
	}
	_, src, err := s.Profile.Store.Resolve()
	if err != nil {
		d.report(false, "sign-in", err.Error())
		return
	}
	if _, err := s.Tokens().Token(ctx); err != nil {
		d.report(false, "sign-in", err.Error())
		return
	}
	d.report(true, "sign-in", "token from the "+string(src))

	c, err := s.Client(nil, version.String())
	if err != nil {
		d.report(false, "version and edition", err.Error())
		return
	}
	if md, err := c.GetMetadata(ctx); err != nil {
		d.report(false, "version and edition", err.Error())
	} else {
		ee := "Community"
		if md.Enterprise {
			ee = "Enterprise"
		}
		d.report(true, "version and edition", fmt.Sprintf("GitLab %s, %s Edition", md.Version, ee))
	}

	info, err := c.TokenInfo(ctx)
	switch {
	case err != nil:
		d.report(false, "application", "the token could not be inspected: "+err.Error())
	case info.Application.UID != "" && info.Application.UID != s.ClientID:
		d.report(false, "application", fmt.Sprintf("the token was issued to %s, not to %s; run `gitlab-mcp login`",
			info.Application.UID, s.ClientID))
	default:
		d.report(true, "application", s.ClientID)
	}
	if err == nil {
		needed := cfg.Scopes()
		if missing := scopes.Missing(info.Scope, needed); len(missing) > 0 {
			d.report(false, "granted scopes", fmt.Sprintf("granted %s; not granted: %s\n"+
				"a setting that changes scopes needs `gitlab-mcp login` again; grant every scope asked for",
				orNone(strings.Join(info.Scope, " ")), strings.Join(missing, " ")))
		} else {
			d.report(true, "granted scopes", strings.Join(info.Scope, " "))
		}
	}
	d.report(true, "writes", writeScope(cfg))

	u, err := userOf(ctx, c)
	if err != nil {
		d.report(false, "/user", err.Error())
		return
	}
	d.m.Known(redact.KindUser, u)
	if stored := s.Profile.User.Username; stored != "" && !strings.EqualFold(stored, u) {
		d.report(false, "/user", fmt.Sprintf("signed in as %s, and the last login recorded %s; run `gitlab-mcp login`",
			u, stored))
		return
	}
	d.report(true, "/user", "signed in as "+u)
}

// writeScope says where writes may go (§4.7): confined by
// GITLAB_MCP_WRITE_NAMESPACES, or anywhere the token can write.
func writeScope(cfg config.Config) string {
	switch {
	case cfg.ReadOnly:
		return "none: read-only"
	case len(cfg.WriteNamespaces) > 0:
		return fmt.Sprintf("confined to %d namespace(s) by %s", len(cfg.WriteNamespaces), config.EnvWriteNamespaces)
	default:
		return fmt.Sprintf("anywhere the token can write; set %s to confine them", config.EnvWriteNamespaces)
	}
}

// reach makes one unauthenticated request, which proves the instance
// answers and its certificate is trusted. Any HTTP answer will do.
func (d *doctor) reach(ctx context.Context, cfg config.Config, s *app.Settings) bool {
	label := "TLS"
	if s.Instance.Scheme() == "http" {
		label = "connection (plain http, no TLS)"
	}
	u := s.Instance.APIRoot()
	u.Path += "/metadata"
	// The configured instance, which is what doctor exists to check.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil) //nolint:gosec // the configured instance
	if err != nil {
		d.report(false, label, "the instance URL cannot be requested")
		return false
	}
	hc := *s.HTTPClient
	hc.Timeout = cfg.HTTPTimeout
	resp, err := hc.Do(req) //nolint:gosec // the configured instance; no redirect is followed
	if err != nil {
		d.report(false, label, unreachable(err))
		return false
	}
	_ = resp.Body.Close()
	detail := "the instance answers"
	if s.Instance.Scheme() == "https" {
		detail = "certificate trusted"
		if cfg.CAFile != "" {
			detail += " (with " + config.EnvCAFile + ")"
		}
	}
	d.report(true, label, detail)
	return true
}

// unreachable says why the instance did not answer, without the URL,
// the host names or the addresses the error carries.
func unreachable(err error) string {
	if redact.IsCertificateError(err) {
		return "the instance's certificate is not trusted: set " + config.EnvCAFile +
			" to the PEM bundle of the authority that signed it"
	}
	return "the instance could not be reached: " + redact.NetError(err)
}

// orNil turns an unset string into JSON null, which a caller cannot
// mistake for a value.
func orNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// orEmpty keeps a list a list: a nil slice marshals as null.
func orEmpty(ss []string) []string {
	if ss == nil {
		return []string{}
	}
	return ss
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
