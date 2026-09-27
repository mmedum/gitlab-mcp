// Command gates runs the repository's own checks. It is a Go program
// rather than a shell script so the code holding the gates shut is itself
// held to them: it compiles, it is vetted, it is linted and it has tests.
// GoReleaser builds only ./cmd/..., so this never ships.
package main

import (
	"fmt"
	"io"
	"os"
	"sort"
)

// mode says where a command must run. The zero value is refused by a
// test, so every entry decides.
type mode int

const (
	modeUnset   mode = iota
	modeCheck        // in `make check` and in CI
	modeRelease      // in the release workflow only
	modePR           // on pull requests only
	modeManual       // by hand; the reason says why
)

// command is one thing this program can do.
type command struct {
	run  func(out io.Writer, args []string) error
	args string // how the arguments are spelled in the usage text
	// minArgs and maxArgs bound the argument count; maxArgs < 0 is unbounded.
	minArgs, maxArgs int
	doc              string
	mode             mode
	// reason is required for every mode other than modeCheck: why it is
	// not in `make check`.
	reason string
}

// commands is the one list of what this program does. The dispatch, the
// usage text and the parity gate all read it. Two files contribute:
// repoCommands (repository, release and supply-chain checks) and
// surfaceCommands (the tool surface, the API and the live drivers).
var commands map[string]command

func init() {
	commands = map[string]command{}
	for _, part := range []map[string]command{repoCommands(), surfaceCommands()} {
		for name, c := range part {
			if _, dup := commands[name]; dup {
				panic("gates: command registered twice: " + name)
			}
			commands[name] = c
		}
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		usage(stdout)
		return 0
	}
	c, ok := commands[args[0]]
	if !ok {
		_, _ = fmt.Fprintf(stderr, "gates: unknown command %q\n\n", args[0])
		usage(stderr)
		return 2
	}
	rest := args[1:]
	if len(rest) < c.minArgs || (c.maxArgs >= 0 && len(rest) > c.maxArgs) {
		_, _ = fmt.Fprintf(stderr, "usage: gates %s %s\n", args[0], c.args)
		return 2
	}
	if err := c.run(stdout, rest); err != nil {
		_, _ = fmt.Fprintf(stderr, "gates %s: %v\n", args[0], err)
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	_, _ = fmt.Fprintln(w, "usage: gates <command> [args]")
	for _, n := range names {
		c := commands[n]
		_, _ = fmt.Fprintf(w, "  %-16s %-24s %s\n", n, c.args, c.doc)
	}
}
