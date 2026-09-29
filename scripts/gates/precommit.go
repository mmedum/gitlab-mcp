package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mmedum/gitlab-mcp/v2/scripts/internal/gatekit"
	"github.com/mmedum/gitlab-mcp/v2/scripts/internal/gitx"
)

// precommit is what .githooks/pre-commit runs: the checks fast enough
// for every commit that catch what is expensive to undo. A leak reaches
// the history at commit time, and one deleted from the tip is still in
// the log.
//
// Every step runs and every problem is reported before the commit is
// refused, so one run says everything that needs fixing.
//
// gitleaks runs at the version the Makefile pins, which is the one
// `make secrets` runs in CI, so the hook and CI cannot disagree about
// the rules. When it cannot be fetched — offline, a proxy down — the
// hook says so and skips it rather than blocking every commit; CI runs
// it on every pull request regardless.

// precommitStep is one thing the hook runs. skip, when it returns a
// reason, turns a step that cannot run here into a warning.
type precommitStep struct {
	name string
	run  func(out io.Writer) error
	skip func() string
}

func precommit(out io.Writer, _ []string) error {
	steps, err := precommitSteps(".")
	if err != nil {
		return err
	}
	return precommitRunSteps(out, steps)
}

// precommitRunSteps runs every step and collects every failure.
func precommitRunSteps(out io.Writer, steps []precommitStep) error {
	var problems []string
	skipped := 0
	for _, s := range steps {
		_, _ = fmt.Fprintf(out, "-- %s\n", s.name)
		if s.skip != nil {
			if why := s.skip(); why != "" {
				skipped++
				_, _ = fmt.Fprintf(out, "warning: %s skipped: %s\n", s.name, why)
				continue
			}
		}
		if err := s.run(out); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", s.name, err))
		}
	}
	if err := gatekit.Problems(out, "the staged commit", problems); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "precommit ok: %d steps, %d skipped\n", len(steps), skipped)
	return nil
}

// precommitSteps is the list, apart from the loop so a test can read it
// without running a scan.
func precommitSteps(root string) ([]precommitStep, error) {
	mk, err := readMakefile(filepath.Join(root, parityMakefile))
	if err != nil {
		return nil, err
	}
	gitleaks, err := mk.variable("GITLEAKS")
	if err != nil {
		return nil, err
	}
	leaksGate, ok := commands["leaks"]
	if !ok {
		return nil, errors.New("the registry has no leaks gate")
	}
	return []precommitStep{
		{name: "gofmt on staged files", run: func(out io.Writer) error {
			files, err := precommitStaged(root)
			if err != nil {
				return err
			}
			return precommitGofmt(out, root, files)
		}},
		{name: "go vet", run: func(out io.Writer) error {
			return precommitExec(out, root, "go", "vet", "./...")
		}},
		// The same implementation `make leaks` calls, through the
		// registry, rather than a second spelling of it.
		{name: "leaks", run: func(out io.Writer) error { return leaksGate.run(out, nil) }},
		{name: "gitleaks on staged changes",
			skip: func() string { return precommitGitleaksMissing(root, gitleaks) },
			run: func(out io.Writer) error {
				return precommitExec(out, root, "go", "run", gitleaks, "git", "--pre-commit", "--staged",
					"--config", ".gitleaks.toml", "--redact", "--no-banner")
			}},
	}, nil
}

// precommitGitleaksMissing is why gitleaks cannot run here, or "". It
// asks for its version, which fetches and builds it the way the scan
// would.
func precommitGitleaksMissing(root, module string) string {
	cmd := exec.Command("go", "run", module, "version")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		return fmt.Sprintf("go run %s does not build here (%s); CI scans every pull request", module, msg)
	}
	return ""
}

// precommitStaged lists the Go files staged for commit.
func precommitStaged(root string) ([]string, error) {
	raw, err := gitx.Output(root, "diff", "--cached", "--name-only", "--diff-filter=ACMR", "-z")
	if err != nil {
		return nil, err
	}
	var files []string
	for name := range strings.SplitSeq(raw, "\x00") {
		if strings.HasSuffix(name, ".go") {
			files = append(files, name)
		}
	}
	return files, nil
}

// precommitGofmt fails on any file gofmt would change.
func precommitGofmt(out io.Writer, root string, files []string) error {
	if len(files) == 0 {
		_, _ = fmt.Fprintln(out, "no Go files staged")
		return nil
	}
	cmd := exec.Command("gofmt", append([]string{"-l"}, files...)...)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("gofmt: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if bad := strings.Fields(string(raw)); len(bad) > 0 {
		return fmt.Errorf("gofmt would change %s", strings.Join(bad, ", "))
	}
	_, _ = fmt.Fprintf(out, "%d staged Go file(s) formatted\n", len(files))
	return nil
}

func precommitExec(out io.Writer, root, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = out, out
	return cmd.Run()
}
