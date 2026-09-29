// Command gitlab-mcp is an MCP server for GitLab.
//
// With no subcommand it serves MCP over stdio. Stdout carries JSON-RPC
// frames and nothing else: this file is the one place that names the
// process's streams, and it passes them down as io.Writer so nothing
// below can print to the wrong one (CLAUDE.md rule 2).
//
//	gitlab-mcp [serve]                       run the MCP server over stdio
//	gitlab-mcp login --client-id <id>        sign in with your own OAuth application
//	gitlab-mcp logout                        revoke and forget the stored token
//	gitlab-mcp status [--no-probe] [--json]  show the profile and settings
//	gitlab-mcp doctor                        check the setup against the instance
//	gitlab-mcp version | --version | --dump-schemas
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/mmedum/gitlab-mcp/internal/app"
	"github.com/mmedum/gitlab-mcp/internal/config"
	"github.com/mmedum/gitlab-mcp/internal/redact"
	"github.com/mmedum/gitlab-mcp/internal/server"
	"github.com/mmedum/gitlab-mcp/internal/version"
)

func main() {
	//nolint:forbidigo // the one place the process's streams are named
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}

// commands is every subcommand, with the line usage prints for it.
var commands = []struct{ name, synopsis, summary string }{
	{"serve", "gitlab-mcp [serve]", "run the MCP server over stdio"},
	{"login", "gitlab-mcp login --client-id <id> [--no-browser]",
		"sign in with your own OAuth application"},
	{"logout", "gitlab-mcp logout", "revoke and delete the stored token"},
	{"status", "gitlab-mcp status [--no-probe] [--json]", "show the profile and settings"},
	{"doctor", "gitlab-mcp doctor", "check the setup against the instance"},
	{"version", "gitlab-mcp version", "print the version"},
	{"help", "gitlab-mcp help [command]", "show this, or a command's flags"},
}

func isCommand(s string) bool {
	return slices.ContainsFunc(commands, func(c struct{ name, synopsis, summary string }) bool { return c.name == s })
}

// isHelpToken reports whether s asks for help. One definition, so the
// tokens mean the same thing wherever they appear.
func isHelpToken(s string) bool {
	switch s {
	case "help", "-h", "--h", "-help", "--help":
		return true
	}
	return false
}

// run is main with its inputs passed in, so a test drives every path,
// the serve path included, and reads the exit code.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer, env func(string) string) int {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") && !isCommand(args[0]) {
		// Anything else that is not a flag was meant as a command.
		// Falling through would start the server, which blocks on stdin
		// and looks like a hang.
		usage(stderr)
		return fail(stderr, "unknown command %q", args[0])
	}
	// Every argument is checked for a help token before anything is
	// dispatched, so `login --client-id --help` never reads --help as an
	// application id and `logout --help` never deletes anything.
	if slices.ContainsFunc(args, isHelpToken) {
		return help(args, stdout, env)
	}
	if len(args) > 0 {
		switch args[0] {
		case "serve":
			return serve(args[1:], stdin, stdout, stderr, env)
		case "login":
			return cmdLogin(args[1:], stdout, stderr, env)
		case "logout":
			return cmdLogout(args[1:], stdout, stderr, env)
		case "status":
			return cmdStatus(args[1:], stdout, stderr, env)
		case "doctor":
			return cmdDoctor(args[1:], stdout, stderr, env)
		case "version":
			if len(args) > 1 {
				return fail(stderr, "version takes no arguments, got %q", args[1])
			}
			outf(stdout, "%s\n", version.Info())
			return 0
		}
	}
	return serve(args, stdin, stdout, stderr, env)
}

// help prints the usage a help token asked for: a command's flags when
// one was named, the overview otherwise. It always exits 0.
func help(args []string, stdout io.Writer, env func(string) string) int {
	name := ""
	switch {
	case len(args) > 1 && args[0] == "help" && isCommand(args[1]):
		name = args[1]
	case len(args) > 0 && isCommand(args[0]) && args[0] != "help":
		name = args[0]
	}
	if name == "" || name == "help" {
		usage(stdout)
		return 0
	}
	f := newFlags(name, env)
	var defaults bytes.Buffer
	f.fs.SetOutput(&defaults)
	f.fs.PrintDefaults()
	for _, c := range commands {
		if c.name == name {
			outf(stdout, "Usage: %s\n\n%s.\n", c.synopsis, upperFirst(c.summary))
		}
	}
	if defaults.Len() > 0 {
		outf(stdout, "\nFlags:\n%s", defaults.String())
	}
	return 0
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func usage(w io.Writer) {
	var b strings.Builder
	b.WriteString("gitlab-mcp — MCP server for GitLab\n\nUsage:\n")
	for _, c := range commands {
		if len(c.synopsis) > 40 {
			fmt.Fprintf(&b, "  %s\n  %-40s %s\n", c.synopsis, "", c.summary)
			continue
		}
		fmt.Fprintf(&b, "  %-40s %s\n", c.synopsis, c.summary)
	}
	fmt.Fprintf(&b, "  %-40s %s\n", "gitlab-mcp --version", "print the version")
	fmt.Fprintf(&b, "  %-40s %s\n", "gitlab-mcp --dump-schemas", "print the tool surface as JSON")
	b.WriteString("\nSettings come from " + config.EnvPrefix + "* environment variables; every command also\n" +
		"accepts the matching flags (run one with --help).\n")
	outf(w, "%s", b.String())
}

// fail prints one error line through the redactor and returns exit 1.
func fail(w io.Writer, format string, args ...any) int {
	outf(w, "gitlab-mcp: %s\n", fmt.Sprintf(format, args...))
	return 1
}

// outf is the only way this command writes to a stream, the login URL
// excepted. Everything goes through the redactor, so a token, an address
// or an application id that arrives inside an error another package
// formatted is masked like one printed here.
func outf(w io.Writer, format string, args ...any) {
	_, _ = io.WriteString(w, redact.Text(fmt.Sprintf(format, args...)))
}

// warnTo prints a credential warning, such as the plaintext file's.
func warnTo(w io.Writer) func(string) {
	return func(msg string) { outf(w, "warning: %s\n", msg) }
}

// flags is one command's flag set: the shared settings and its own.
type flags struct {
	name     string
	fs       *flag.FlagSet
	env      func(string) string
	settings *config.Settings

	noBrowser, noProbe, asJSON bool
	showVersion, dumpSchemas   bool
}

func newFlags(name string, env func(string) string) *flags {
	f := &flags{name: name, env: env, fs: flag.NewFlagSet("gitlab-mcp "+name, flag.ContinueOnError)}
	f.settings = config.Define(f.fs, env)
	switch name {
	case "serve":
		f.fs.BoolVar(&f.showVersion, "version", false, "print the version and exit")
		f.fs.BoolVar(&f.dumpSchemas, "dump-schemas", false, "print the full tool surface as JSON and exit")
	case "login":
		f.fs.BoolVar(&f.noBrowser, "no-browser", false, "print the authorization URL instead of opening a browser")
	case "status":
		f.fs.BoolVar(&f.noProbe, "no-probe", false, "do not contact the instance")
		f.fs.BoolVar(&f.asJSON, "json", false, "print the same state as one JSON object")
	}
	return f
}

// parse reads the command line. A non-nil code means the caller returns
// it: 2 for a flag error, 1 for a stray argument.
func (f *flags) parse(args []string, stderr io.Writer) *int {
	var msgs bytes.Buffer
	f.fs.SetOutput(&msgs)
	f.fs.Usage = func() {}
	err := f.fs.Parse(args)
	if msgs.Len() > 0 {
		outf(stderr, "%s", msgs.String())
	}
	if err != nil {
		code := 2
		outf(stderr, "run `gitlab-mcp %s --help` for its flags\n", f.name)
		return &code
	}
	if f.fs.NArg() > 0 {
		code := fail(stderr, "%s takes no arguments, got %q", f.name, f.fs.Arg(0))
		return &code
	}
	return nil
}

// build validates the settings, the same load the server runs.
func (f *flags) build() (config.Config, error) {
	return f.settings.Build()
}

// config parses and builds, printing any problem; a non-nil code means
// the caller returns it.
func (f *flags) config(args []string, stderr io.Writer) (config.Config, *int) {
	if code := f.parse(args, stderr); code != nil {
		return config.Config{}, code
	}
	cfg, err := f.build()
	if err != nil {
		code := fail(stderr, "%v", err)
		return config.Config{}, &code
	}
	return cfg, nil
}

func serve(args []string, stdin io.Reader, stdout, stderr io.Writer, env func(string) string) int {
	f := newFlags("serve", env)
	if code := f.parse(args, stderr); code != nil {
		return *code
	}
	if f.showVersion {
		outf(stdout, "%s\n", version.Info())
		return 0
	}
	if f.dumpSchemas {
		// The one time stdout carries something that is not a frame, and
		// the one time no session exists to corrupt. No configuration is
		// read and no credential touched.
		if err := server.DumpSchemas(stdout, version.String()); err != nil {
			return fail(stderr, "dump schemas: %v", err)
		}
		return 0
	}
	cfg, err := f.build()
	if err != nil {
		return fail(stderr, "%v", err)
	}
	logger := config.NewLogger(cfg, stderr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rt, err := app.Assemble(ctx, cfg, app.Options{
		Env: env, Keyring: keyringBackend, Logger: logger, Version: version.String(),
	})
	if err != nil {
		return fail(stderr, "%v", err)
	}
	defer rt.Close()
	logger.Info("serving", "version", version.String(), "settings", rt.Settings)
	if err := rt.Serve(ctx, stdin, stdout); err != nil {
		if errors.Is(err, context.Canceled) {
			return 0
		}
		return fail(stderr, "server: %v", err)
	}
	logger.Info("client disconnected")
	return 0
}
