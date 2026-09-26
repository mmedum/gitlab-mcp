//go:build evals

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/gitlab-mcp/internal/config"
	"github.com/mmedum/gitlab-mcp/internal/server"
	"github.com/mmedum/gitlab-mcp/scripts/internal/redact"
)

// toolFloor is the fewest tools a model must be offered for a task to
// mean anything: phase 1 registers thirty-one reads.
const toolFloor = 28

func main() {
	selfCheck := flag.Bool("self-check", false,
		"build each task's instance and score it untouched: every task must fail. No model, no key")
	bin := flag.String("bin", "./gitlab-mcp", "the server binary the model is given")
	model := flag.String("model", "opus", "the model to score, as `claude --model` takes it")
	only := flag.String("task", "", "run only the tasks whose name contains this")
	budget := flag.Float64("budget", 1.0, "the most one task may spend, in US dollars")
	verbose := flag.Bool("v", false, "print every tool call the model made")
	flag.Parse()

	p := redact.NewPrinter(redact.NewRedactor(false))
	ctx := context.Background()
	if *selfCheck {
		os.Exit(check(ctx, p))
	}
	os.Exit(live(ctx, p, liveOptions{bin: *bin, model: *model, only: *only, budget: *budget, verbose: *verbose}))
}

// check runs the harness with no model in it. A scorer that passes a run
// that did nothing is scoring nothing, and would report a model correct
// for doing nothing; so here every task must FAIL, the model must be
// offered the whole read surface, and nothing may have touched the
// instance.
func check(ctx context.Context, p *redact.Printer) int {
	bad := 0
	for _, t := range Tasks {
		note, err := checkTask(ctx, t)
		if err != nil {
			p.Sayf("FAIL  %-24s %v", t.Name, err)
			bad++
			continue
		}
		p.Sayf("ok    %-24s %s", t.Name, note)
	}
	if bad > 0 {
		p.Sayf("\n%d task(s) cannot be trusted to score anything", bad)
		return 1
	}
	p.Sayf("\n%d tasks, each failing a run that did nothing. `make evals` with a key scores a model.", len(Tasks))
	return 0
}

func checkTask(ctx context.Context, t Task) (string, error) {
	w, err := newWorld(ctx)
	if err != nil {
		return "", err
	}
	defer w.close()
	if _, err := substitute(t.Prompt, w.facts); err != nil {
		return "", fmt.Errorf("the prompt: %w", err)
	}
	before := w.census()
	n, err := offered(ctx, w)
	if err != nil {
		return "", err
	}
	if n < toolFloor {
		return "", fmt.Errorf("the model would be offered %d tools and the floor is %d", n, toolFloor)
	}
	if moved := w.census().since(before); moved != "" {
		return "", fmt.Errorf("listing the tools changed the instance: %s", moved)
	}
	pass, why := t.Score(Run{}, w.facts)
	switch {
	case pass:
		return "", fmt.Errorf("scored a PASS on a run that did nothing: %s", why)
	case why == "":
		return "", fmt.Errorf("failed an empty run without saying why")
	}
	return fmt.Sprintf("%d tools offered; an empty run fails: %s", n, why), nil
}

// offered is how many tools the real server lists against the world,
// built in-process as the binary builds it.
func offered(ctx context.Context, w *world) (int, error) {
	s := server.New(server.Options{Config: config.Config{Instance: w.client.Instance()}, Client: w.client, Version: "evals"})
	ct, st := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, st, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "evals", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = cs.Close() }()
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		return 0, err
	}
	return len(list.Tools), nil
}

type liveOptions struct {
	bin, model, only string
	budget           float64
	verbose          bool
}

// live gives each task to a model through `claude -p`, against a fresh
// world, and scores the calls and the answer; the census before and
// after says whether anything was written or the private project read.
func live(ctx context.Context, p *redact.Printer, o liveOptions) int {
	passed, failed := 0, 0
	for _, t := range Tasks {
		if o.only != "" && !strings.Contains(t.Name, o.only) {
			continue
		}
		pass, note, err := liveTask(ctx, t, o, p)
		switch {
		case err != nil:
			failed++
			p.Sayf("ERROR %-24s %v", t.Name, err)
		case pass:
			passed++
			p.Sayf("ok    %-24s %s", t.Name, note)
		default:
			failed++
			p.Sayf("FAIL  %-24s %s", t.Name, note)
		}
	}
	p.Sayf("\n%d passed, %d failed. A failure is more often a tool description than a model; re-run with -v and read the calls.",
		passed, failed)
	if failed > 0 {
		return 1
	}
	return 0
}

func liveTask(ctx context.Context, t Task, o liveOptions, p *redact.Printer) (bool, string, error) {
	w, err := newWorld(ctx)
	if err != nil {
		return false, "", err
	}
	defer w.close()
	prompt, err := substitute(t.Prompt, w.facts)
	if err != nil {
		return false, "", err
	}
	env, cleanup, err := serverEnv(ctx, w)
	if err != nil {
		return false, "", err
	}
	defer cleanup()
	before := w.census()
	run, err := askClaude(ctx, o, prompt, env)
	if err != nil {
		return false, "", err
	}
	if o.verbose {
		for _, c := range run.Calls {
			p.Sayf("      > %s %v", c.Tool, c.Args)
		}
	}
	if moved := w.census().since(before); moved != "" {
		return false, "the instance changed during a read-only task: " + moved, nil
	}
	if len(run.Calls) > t.MaxCalls {
		return false, fmt.Sprintf("%d calls, over the task's bound of %d", len(run.Calls), t.MaxCalls), nil
	}
	pass, note := t.Score(run, w.facts)
	return pass, note, nil
}
