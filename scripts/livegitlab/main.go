//go:build live

// Command livegitlab drives the built server over stdio against
// gitlab.com, which is the check no fake can make: that the tools behave
// against GitLab itself (docs/architecture.md §9.1, §13).
//
// It reads only what it wrote. It creates two private scratch projects
// named for the run under the group -namespace names, fills them with its
// own files, branches, issues, comments and merge requests, drives every
// tool and option against them, and deletes them at the end unless -keep
// is set. Group and instance-wide searches carry the run's own word, so
// what comes back is the run's. Creating and deleting the projects are
// the driver's own writes, made through the client with the profile's
// token: the server has neither (§8a).
//
// Every line goes through the one redacting printer (`gates transcript`
// refuses any other way out). Hosts, paths, the account and the ids the
// run learns are masked; titles and bodies are not, which is why every
// one of them is text this driver wrote. Read the transcript before
// sharing it.
//
//	make live LIVE_ARGS="-namespace example-group/scratch"
//
// It records what it sent, per tool option, in
// testdata/live-cover-record.tsv, which `gates live-cover` reads.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"maps"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/internal/app"
	"github.com/mmedum/gitlab-mcp/v2/internal/config"
	"github.com/mmedum/gitlab-mcp/v2/internal/credentials"
	"github.com/mmedum/gitlab-mcp/v2/internal/gapi"
	"github.com/mmedum/gitlab-mcp/v2/scripts/internal/mcpstdio"
	"github.com/mmedum/gitlab-mcp/v2/scripts/internal/redact"
)

// options are what one run was asked to do.
type options struct {
	binary    string
	namespace string
	profile   string
	keep      bool
	record    string
	spike     string
}

func main() {
	var o options
	flag.StringVar(&o.binary, "bin", "./gitlab-mcp", "the server binary to drive")
	flag.StringVar(&o.namespace, "namespace", "", "the group the scratch projects are created in, which you own (required)")
	flag.StringVar(&o.profile, "profile", "", "the signed-in profile to use; empty takes the default")
	flag.BoolVar(&o.keep, "keep", false, "leave the scratch projects in place for a look afterwards")
	flag.StringVar(&o.record, "record", "testdata/live-cover-record.tsv", "where to write what the run sent")
	flag.StringVar(&o.spike, "spike", "", "run only this spike after seeding (E, G or O), and drive no tool")
	flag.Parse()

	p := redact.NewPrinter(redact.NewRedactor(false))
	if o.namespace == "" {
		p.Fail("livegitlab: -namespace is required; the run creates its projects in -namespace and reads nothing else")
		os.Exit(2)
	}
	if o.spike != "" && o.spike != "E" && o.spike != "G" && o.spike != "O" {
		p.Fail("livegitlab: -spike takes E, G or O; the other spikes run with every run")
		os.Exit(2)
	}
	err := run(context.Background(), o, p)
	p.Summary()
	if err != nil {
		p.Fail("livegitlab: %v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, o options, p *redact.Printer) error {
	red := p.Redactor()
	red.Known(redact.KindPath, o.namespace)
	// Built from nothing, so a test instance set in the shell cannot
	// redirect the run: it is always gitlab.com.
	env := map[string]string{}
	if o.profile != "" {
		env[config.EnvProfile] = o.profile
	}
	getenv := func(k string) string { return env[k] }
	cfg, err := config.Load(nil, getenv)
	if err != nil {
		return err
	}
	// No logger: the driver prints through its printer and nothing else.
	settings, err := app.Resolve(cfg, app.Options{Env: getenv, Keyring: credentials.OSKeyring()})
	if err != nil {
		return err
	}
	if settings.CredentialsErr != nil {
		return fmt.Errorf("sign in first: %w", settings.CredentialsErr)
	}
	red.Known(redact.KindHost, settings.Instance.Host())
	client, err := settings.Client(nil, "livegitlab")
	if err != nil {
		return err
	}

	user, err := client.GetCurrentUser(ctx)
	if err != nil {
		return fmt.Errorf("read the signed-in account: %w", err)
	}
	red.Known(redact.KindUser, user.Username)
	// The display name and the numeric id identify the person as surely
	// as the username; commit authors print the name.
	red.Known(redact.KindUser, user.Name)
	red.Known(redact.KindID, itoa(user.ID))
	name, err := newRunName()
	if err != nil {
		return err
	}
	s := scratch{Namespace: o.namespace, Name: name, User: user.Username, Default: "main",
		Feature: "feature-" + name[len(name)-6:], Feature2: "draft-" + name[len(name)-6:],
		File: "docs/live.md", Label: "live-" + name[len(name)-6:], Label2: "live-" + name[len(name)-6:] + "-b",
		Milestone: "Live milestone " + name}
	p.Sayf("run %s against the %s instance, in %s", name, app.InstanceKind(settings.Instance), o.namespace)

	projects, err := seed(ctx, client, &s, red)
	defer func() {
		if o.keep {
			p.Sayf("\n-keep: the scratch projects stay in %s; delete them by hand", o.namespace)
			return
		}
		for _, id := range projects {
			if err := client.Do(context.Background(), gapi.Call{Method: "DELETE", Path: "projects/{}",
				Args: []string{itoa(id)}, Name: "live_delete_project"}, nil); err != nil {
				p.Sayf("!! could not delete scratch project %s: %v", redact.ID(itoa(id)), err)
				continue
			}
			p.Sayf("deleted scratch project %s", redact.ID(itoa(id)))
		}
		if len(projects) > 0 && app.InstanceKind(settings.Instance) == "gitlab.com" {
			p.Say("gitlab.com delays a project's deletion: it is marked now and removed after the retention period")
		}
	}()
	if err != nil {
		return fmt.Errorf("seed the scratch projects: %w", err)
	}
	if o.spike == "E" || o.spike == "G" {
		spikes(ctx, settings, s, o.spike, p)
		return nil
	}
	if err := waitForCI(ctx, client, &s, p); err != nil {
		return err
	}
	if o.spike == "O" {
		spikes(ctx, settings, s, o.spike, p)
		return nil
	}
	spikes(ctx, settings, s, "", p)

	serverEnv := []string{config.EnvLogLevel + "=info"}
	if o.profile != "" {
		serverEnv = append(serverEnv, config.EnvProfile+"="+o.profile)
	}
	rec := newRecorder()
	// One scripted person answers for both servers, so a tool that asks
	// only on some calls is judged over the whole run.
	person := &scriptedPerson{p: p}
	// The default surface first, then a second server with Ship,
	// Destructive and every toolset on, its writes confined to the
	// maintainer's group: the first run also shows the flags' tools
	// are not there without them.
	unexpected, _, err := session(o.binary, serverEnv, rec, person, plan(s), s, p)
	if err != nil {
		return err
	}
	p.Say("\n=== with Ship, Destructive and every toolset ===")
	flagged := append(slices.Clone(serverEnv), config.EnvEnableShip+"=true", config.EnvEnableDestructive+"=true",
		config.EnvToolsets+"="+config.ToolsetAll, config.EnvWriteNamespaces+"="+o.namespace)
	more, surface, err := session(o.binary, flagged, rec, person, planShip(s), s, p)
	if err != nil {
		return err
	}
	unexpected += more
	for _, tool := range person.unasked() {
		unexpected++
		p.Sayf("!! %s says it asks the person, and put no question in this run", tool)
	}
	if missing := rec.missing(surface); len(missing) > 0 {
		p.Sayf("\nnot sent this run (%d): %s", len(missing), strings.Join(missing, ", "))
	}
	header := fmt.Sprintf("run of %s against a %s instance", time.Now().UTC().Format("2006-01-02"), app.InstanceKind(settings.Instance))
	if err := rec.write(o.record, header); err != nil {
		return fmt.Errorf("write %s: %w", o.record, err)
	}
	p.Sayf("recorded what was sent in %s", o.record)
	if unexpected > 0 {
		return fmt.Errorf("%d call(s) did not behave as expected; read the transcript", unexpected)
	}
	p.Say("every call behaved as expected")
	return nil
}

// session starts a server with env, drives steps through it, and
// returns how many behaved unexpectedly and the surface it registered.
func session(binary string, env []string, rec *recorder, person *scriptedPerson, steps []step, s scratch, p *redact.Printer) (int, map[string][]string, error) {
	sess, err := mcpstdio.Start(binary, mcpstdio.Config{Env: env})
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = sess.Close() }()
	sess.OnCall(rec.Sent)
	sess.OnElicit(person.answer)
	init, err := sess.Initialize("", "livegitlab")
	if err != nil {
		return 0, nil, err
	}
	p.Sayf("protocol %v; %d tools registered", init["protocolVersion"], len(sess.Options()))
	unexpected, err := drive(sess, person, steps, s, p)
	if err != nil {
		return unexpected, nil, err
	}
	for _, line := range sess.StderrTail(20) {
		p.Say("stderr | " + line)
	}
	return unexpected, sess.Options(), nil
}

// drive sends every step, refusing one that would read outside the
// scratch project before it goes out.
func drive(sess *mcpstdio.Session, person *scriptedPerson, steps []step, s scratch, p *redact.Printer) (int, error) {
	unexpected := 0
	saved := map[string]any{}
	for _, st := range steps {
		if err := confined(st, s); err != nil {
			return unexpected, fmt.Errorf("the plan reads outside the scratch project, which §9.1 forbids: %w", err)
		}
		if st.pause > 0 {
			time.Sleep(st.pause)
		}
		args, missing := fill(st.args, saved)
		if missing != "" {
			unexpected++
			p.Sayf("\n=== %s ===\n!! not sent: %s was never saved by an earlier step", st.tool, missing)
			continue
		}
		for {
			p.Sayf("\n=== %s %s ===", st.tool, mcpstdio.Encode(args))
			if st.why != "" && !st.anyOutcome {
				p.Sayf("(expecting a refusal: %s)", st.why)
			}
			if st.anyOutcome {
				p.Sayf("(either outcome is a finding: %s)", st.why)
			}
			person.step(st.tool, st.declines)
			res, err := sess.CallTool(st.tool, args)
			if err != nil {
				return unexpected, err
			}
			asks := strings.Contains(sess.Description(st.tool), asksThePerson)
			want, exact := expectedQuestions(st, args, res.IsError, outcomeOf(res.Structured), asks)
			if got := person.questions(); got > want || exact && got != want {
				unexpected++
				p.Sayf("!! the server put %d question(s) to the person, not %s%d", got, map[bool]string{true: "", false: "at most "}[exact], want)
			}
			if asks && args["dry_run"] != true && !st.declines {
				person.called(st.tool)
			}
			// Ids the call made are masked before anything is printed.
			learnIDs(p.Redactor(), res.Structured)
			p.Say(res.Text)
			if !res.IsError {
				save(st.save, res.Structured, saved)
			}
			if !st.anyOutcome && res.IsError != st.expectError {
				unexpected++
				p.Say(map[bool]string{true: "!! expected a refusal and got a result", false: "!! unexpected tool error"}[st.expectError])
			}
			next := nextPageToken(res.Structured)
			if !st.paged || res.IsError || next == "" || args["page_token"] != nil {
				break
			}
			args = copyWith(args, "page_token", next)
		}
	}
	return unexpected, nil
}

// scriptedPerson answers the questions the server puts to the person,
// for the maintainer running the driver (docs/architecture.md §4.12):
// accept, unless the step declines. It prints each question. It is
// answered on the session's reader goroutine, so its state is locked.
type scriptedPerson struct {
	p *redact.Printer

	mu       sync.Mutex
	declines bool
	asked    int
	// byTool counts the questions each tool put over the run, and wrote
	// is every tool that asks and was called for real.
	byTool map[string]int
	wrote  map[string]bool
	tool   string
}

// step readies the person for one step's call.
func (sp *scriptedPerson) step(tool string, declines bool) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.tool, sp.declines, sp.asked = tool, declines, 0
}

// called records a call for real of a tool that asks.
func (sp *scriptedPerson) called(tool string) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.wrote == nil {
		sp.wrote = map[string]bool{}
	}
	sp.wrote[tool] = true
}

// unasked is every tool that asks, was called for real, and put no
// question over the run.
func (sp *scriptedPerson) unasked() []string {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	var out []string
	for _, tool := range slices.Sorted(maps.Keys(sp.wrote)) {
		if sp.byTool[tool] == 0 {
			out = append(out, tool)
		}
	}
	return out
}

func (sp *scriptedPerson) questions() int {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return sp.asked
}

func (sp *scriptedPerson) answer(message string) string {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.asked++
	if sp.byTool == nil {
		sp.byTool = map[string]int{}
	}
	sp.byTool[sp.tool]++
	sp.p.Say("--- question put to the person ---\n" + strings.TrimRight(message, "\n"))
	if sp.declines {
		sp.p.Say("--- answered: decline ---")
		return "decline"
	}
	sp.p.Say("--- answered: accept ---")
	return "accept"
}

// asksThePerson is what a published description says of a tool that
// asks (internal/tools/register.go), so the driver learns which tools ask
// from the surface rather than from a list typed here.
const asksThePerson = "the server also asks the person"

// expectedQuestions is how many questions a step may put: exactly one
// on a step that declines, none on a dry run or a call that found
// nothing to change, and at most one otherwise for a tool that asks. It
// returns the bound and whether it is exact; a tool that does not ask
// puts none.
func expectedQuestions(st step, args map[string]any, refused bool, outcome string, asks bool) (int, bool) {
	switch {
	case st.declines:
		return 1, true
	case !asks, args["dry_run"] == true, !refused && outcome == "unchanged":
		return 0, true
	}
	return 1, false
}

// outcomeOf is a write result's outcome, or "".
func outcomeOf(structured json.RawMessage) string {
	var out struct {
		Outcome string `json:"outcome"`
	}
	_ = json.Unmarshal(structured, &out)
	return out.Outcome
}

func copyWith(m map[string]any, k string, v any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for key, val := range m {
		out[key] = val
	}
	out[k] = v
	return out
}

// seed creates the scratch projects and everything the plan reads,
// through the client with the profile's own token. Every title and body
// is written here, so nothing in a transcript is anybody else's words.
// It returns the ids of the projects it created, for the teardown, even
// when it fails part-way.
func seed(ctx context.Context, c *gapi.Client, s *scratch, red *redact.Redactor) ([]int64, error) {
	var ns struct {
		ID   int64  `json:"id"`
		Kind string `json:"kind"`
	}
	if err := c.Do(ctx, gapi.Call{Method: "GET", Path: "namespaces/{}", Args: []string{s.Namespace}, Name: "live_namespace"}, &ns); err != nil {
		return nil, fmt.Errorf("read the namespace: %w", err)
	}
	if ns.Kind != "group" {
		return nil, errors.New("-namespace must be a group: a personal namespace is where the account's own projects live")
	}
	var created []int64
	var project struct {
		ID                int64  `json:"id"`
		PathWithNamespace string `json:"path_with_namespace"`
		WebURL            string `json:"web_url"`
	}
	for i, suffix := range []string{"", "-b"} {
		body := map[string]any{"name": s.Name + suffix, "path": s.Name + suffix, "namespace_id": ns.ID,
			"visibility": "private", "initialize_with_readme": i == 0, "default_branch": s.Default,
			"description": "A scratch project the live driver made for one run."}
		if err := c.Do(ctx, gapi.Call{Method: "POST", Path: "projects", Body: body, Name: "live_create_project"}, &project); err != nil {
			return created, fmt.Errorf("create a scratch project: %w", err)
		}
		created = append(created, project.ID)
		red.Known(redact.KindPath, project.PathWithNamespace)
		red.Known(redact.KindID, itoa(project.ID))
		if i == 0 {
			s.ID, s.Path, s.WebURL = project.ID, project.PathWithNamespace, project.WebURL
		}
	}
	// gitlab.com makes a new project refuse pipeline variables from
	// everyone; run_pipeline and play_job send them.
	if err := c.Do(ctx, gapi.Call{Method: "PUT", Path: "projects/{}", Args: []string{itoa(s.ID)},
		Body: map[string]any{"ci_pipeline_variables_minimum_override_role": "developer"}, Name: "live_project_settings"}, nil); err != nil {
		return created, fmt.Errorf("allow pipeline variables in the scratch project: %w", err)
	}
	id := itoa(s.ID)
	var commit struct {
		ID string `json:"id"`
	}
	for _, c2 := range []struct {
		branch, start, msg, content string
	}{
		{s.Default, "", "Add the live file", "# Live " + s.Name + "\n\nLine two.\nLine three.\n"},
		{s.Feature, s.Default, "Change the live file", "# Live " + s.Name + "\n\nLine two, changed.\nLine three.\n"},
		{s.Feature, "", "Tidy the live file", "# Live " + s.Name + "\n\nLine two, changed.\nLine three, tidied.\n"},
		{s.Feature2, s.Default, "Draft a change", "# Live " + s.Name + "\n\nLine two.\nLine three, drafted.\n"},
	} {
		first := c2.branch == s.Default
		action := "update"
		if first {
			action = "create"
		}
		actions := []any{
			map[string]any{"action": action, "file_path": s.File, "content": c2.content},
			map[string]any{"action": action, "file_path": "src/main.txt", "content": "synthetic source for " + c2.branch + ": " + c2.msg + "\n"},
		}
		if first {
			actions = append(actions, map[string]any{"action": "create", "file_path": ".gitlab-ci.yml", "content": ciConfig(*s)},
				map[string]any{"action": "create", "file_path": "ci/child.yml", "content": childConfig})
		}
		body := map[string]any{"branch": c2.branch, "commit_message": c2.msg, "actions": actions}
		if c2.start != "" {
			body["start_branch"] = c2.start
		}
		if err := c.Do(ctx, gapi.Call{Method: "POST", Path: "projects/{}/repository/commits", Args: []string{id}, Body: body,
			Name: "live_commit"}, &commit); err != nil {
			return created, fmt.Errorf("commit to %s: %w", c2.branch, err)
		}
		if c2.branch == s.Default {
			s.SHA = commit.ID
		}
	}
	var item struct {
		IID int64 `json:"iid"`
		ID  int64 `json:"id"`
	}
	for i, title := range []string{"Live issue " + s.Name, "Second live issue " + s.Name} {
		body := map[string]any{"title": title, "labels": s.Label, "assignee_username": s.User,
			"description": "A synthetic issue for the live run.\n\nIt has two paragraphs so a budget has something to cut."}
		if err := c.Do(ctx, gapi.Call{Method: "POST", Path: "projects/{}/issues", Args: []string{id}, Body: body, Name: "live_issue"}, &item); err != nil {
			return created, fmt.Errorf("create an issue: %w", err)
		}
		if i == 0 {
			s.Issue = item.IID
		} else {
			s.Issue2 = item.IID
		}
	}
	for i, text := range []string{"A synthetic comment for the live run.", "A second synthetic comment."} {
		if err := c.Do(ctx, gapi.Call{Method: "POST", Path: "projects/{}/issues/{}/notes", Args: []string{id, itoa(s.Issue)},
			Body: map[string]any{"body": text}, Name: "live_note"}, &item); err != nil {
			return created, fmt.Errorf("comment on the issue: %w", err)
		}
		red.Known(redact.KindID, itoa(item.ID))
		if i == 0 {
			s.Note = item.ID
		}
	}
	for i, mr := range []struct{ source, title string }{
		{s.Feature, "Live merge request " + s.Name},
		{s.Feature2, "Draft: live merge request " + s.Name},
	} {
		body := map[string]any{"source_branch": mr.source, "target_branch": s.Default, "title": mr.title,
			"labels": s.Label, "description": "A synthetic merge request for the live run."}
		if i == 0 {
			// Links for get_issue, get_merge_request and get_commit to
			// read: the merge request closes the issue, and holds Feature.
			body["description"] = fmt.Sprintf("A synthetic merge request for the live run.\n\nCloses #%d.", s.Issue)
		}
		if err := c.Do(ctx, gapi.Call{Method: "POST", Path: "projects/{}/merge_requests", Args: []string{id}, Body: body,
			Name: "live_merge_request"}, &item); err != nil {
			return created, fmt.Errorf("open a merge request: %w", err)
		}
		if i == 0 {
			s.MR = item.IID
		} else {
			s.MR2 = item.IID
		}
	}
	var thread struct {
		Notes []struct {
			ID int64 `json:"id"`
		} `json:"notes"`
	}
	if err := c.Do(ctx, gapi.Call{Method: "POST", Path: "projects/{}/merge_requests/{}/discussions", Args: []string{id, itoa(s.MR)},
		Body: map[string]any{"body": "A synthetic thread on the live merge request."}, Name: "live_discussion"}, &thread); err != nil {
		return created, fmt.Errorf("start a thread: %w", err)
	}
	for _, n := range thread.Notes {
		red.Known(redact.KindID, itoa(n.ID))
	}
	return created, seedPhase1(ctx, c, s, red)
}

// seedWrite is one POST the seed makes, and what it creates.
type seedWrite struct {
	path string
	args []string
	body map[string]any
	what string
}

// seedPhase1 adds what the phase-1 reads need: a second label, two
// milestones, two tags, to-do items on the run's issue and merge request,
// and enough draft review comments to fill more than one page.
func seedPhase1(ctx context.Context, c *gapi.Client, s *scratch, red *redact.Redactor) error {
	id := itoa(s.ID)
	writes := make([]seedWrite, 0, 14)
	writes = append(writes,
		seedWrite{"projects/{}/labels", []string{id}, map[string]any{"name": s.Label2, "color": "#00aa00",
			"description": "A second synthetic label for the live run."}, "a label"},
		seedWrite{"projects/{}/milestones", []string{id}, map[string]any{"title": s.Milestone,
			"description": "A synthetic milestone for the live run."}, "a milestone"},
		seedWrite{"projects/{}/milestones", []string{id}, map[string]any{"title": "Live milestone two " + s.Name}, "a second milestone"},
		seedWrite{"projects/{}/repository/tags", []string{id}, map[string]any{"tag_name": "v0.1.0", "ref": s.Default,
			"message": "A synthetic annotated tag for " + s.Name}, "an annotated tag"},
		seedWrite{"projects/{}/repository/tags", []string{id}, map[string]any{"tag_name": "v0.2.0", "ref": s.Feature}, "a lightweight tag"},
		seedWrite{"projects/{}/issues/{}/todo", []string{id, itoa(s.Issue)}, nil, "a to-do item on the issue"},
		seedWrite{"projects/{}/merge_requests/{}/todo", []string{id, itoa(s.MR)}, nil, "a to-do item on the merge request"},
	)
	// Seven drafts of about 5,000 characters: six fill the 30,000 budget
	// of list_review_comments, and the seventh starts a second page.
	for i := range 7 {
		writes = append(writes, seedWrite{"projects/{}/merge_requests/{}/draft_notes", []string{id, itoa(s.MR)},
			map[string]any{"note": fmt.Sprintf("Synthetic draft %d. ", i+1) + strings.Repeat("A synthetic draft review comment. ", 150)},
			"a draft review comment"})
	}
	// The first label came with the issues; its id is learned here.
	var labels []struct {
		ID int64 `json:"id"`
	}
	if err := c.Do(ctx, gapi.Call{Method: "GET", Path: "projects/{}/labels", Args: []string{id}, Name: "live_labels"}, &labels); err != nil {
		return fmt.Errorf("read the labels: %w", err)
	}
	for _, l := range labels {
		red.Known(redact.KindID, itoa(l.ID))
	}
	for _, w := range writes {
		// Every id a tool prints is one the run made; each is masked.
		var made struct {
			ID int64 `json:"id"`
		}
		if err := c.Do(ctx, gapi.Call{Method: "POST", Path: w.path, Args: w.args, Body: w.body, Name: "live_seed"}, &made); err != nil {
			return fmt.Errorf("create %s: %w", w.what, err)
		}
		if made.ID != 0 {
			red.Known(redact.KindID, itoa(made.ID))
		}
	}
	return nil
}

// ciConfig is the scratch project's CI: on the default branch a job that
// passes, a long job that prints a synthetic token and fails, and a job
// allowed to fail; on any other branch the passing job alone, so a
// pipeline listing has more than one page at little cost. A trigger job
// starts a child pipeline that fails, so get_pipeline has one to name. The passing
// and the manual job declare an input, and the input slow adds a job that
// keeps its log open for spike O.
func ciConfig(s scratch) string {
	token := "glpat-" + "EXAMPLE" + strings.Repeat("0", 20)
	return `spec:
  inputs:
    greeting:
      default: hello
    slow:
      default: "no"
---
workflow:
  rules:
    - if: $CI_COMMIT_BRANCH

default:
  image: alpine:3.20

variables:
  SLOW: $[[ inputs.slow ]]

build:
  stage: build
  inputs:
    target:
      default: scratch
  script:
    - echo "building ` + s.Name + ` $[[ inputs.greeting ]]"

deploy live:
  stage: build
  environment:
    name: live
  rules:
    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH
  script:
    - echo "deploying ` + s.Name + `"

deploy review:
  stage: build
  environment:
    name: live-review
  rules:
    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH
  script:
    - echo "deploying a review of ` + s.Name + `"

manual step:
  stage: build
  rules:
    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH
      when: manual
      allow_failure: true
  inputs:
    target:
      default: scratch
  script:
    - echo "a step a person starts for ${{ job.inputs.target }}"
    # Still running when the run plays it a second time: GitLab plays a
    # finished manual job again, as a retry, but never a running one.
    - sleep 120

slow log:
  stage: build
  rules:
    - if: $SLOW == "yes"
  script:
    - i=0; while [ $i -lt 2000 ]; do echo "slow line $i"; i=$((i+1)); done
    - sleep 300

unit tests:
  stage: test
  rules:
    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH
  script:
    - printf '\033[0Ksection_start:%s:live_output[collapsed=true]\r\033[0KSynthetic test output\n' "$(date +%s)"
    - i=0; while [ $i -lt 3000 ]; do echo "ok  example.test/pkg$i 0.01s"; i=$((i+1)); done
    - printf '\033[0Ksection_end:%s:live_output\r\033[0K\n' "$(date +%s)"
    - echo "the server refused token ` + token + `"
    - mkdir -p reports
    - echo "the report repeats token ` + token + `" > reports/summary.txt
    - echo "a second report, so a listing pages" > reports/second.txt
    - exit 1
  artifacts:
    when: always
    paths:
      - reports/

downstream:
  stage: test
  rules:
    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH
  trigger:
    include: ci/child.yml
    strategy: depend

# In the build stage, so a second failed job exists even when a build
# job fails and the test stage is skipped: list_jobs pages one at a time.
lint:
  stage: build
  allow_failure: true
  rules:
    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH
  script:
    - echo "lint has nothing to say"
    - exit 1
`
}

// childConfig is the child pipeline the downstream trigger job starts:
// one job that fails, which fails the trigger job.
const childConfig = `child fails:
  image: alpine:3.20
  script:
    - echo "a child pipeline that fails"
    - exit 1
`

// waitForCI waits for the default branch's pipeline to finish and learns
// its id and two of its jobs. A pipeline that does not finish in time,
// or never starts because the account cannot use gitlab.com's runners,
// leaves them zero; the CI steps then show what GitLab says about it.
func waitForCI(ctx context.Context, c *gapi.Client, s *scratch, p *redact.Printer) error {
	id := itoa(s.ID)
	var pipelines []struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}
	deadline := time.Now().Add(10 * time.Minute)
	for {
		if err := c.Do(ctx, gapi.Call{Method: "GET", Path: "projects/{}/pipelines", Args: []string{id},
			Query: url.Values{"sha": {s.SHA}, "ref": {s.Default}}, Name: "live_pipelines"}, &pipelines); err != nil {
			return fmt.Errorf("read the scratch pipelines: %w", err)
		}
		done := len(pipelines) > 0 && slices.Contains([]string{"success", "failed", "canceled", "skipped"}, pipelines[0].Status)
		if done {
			break
		}
		if time.Now().After(deadline) {
			// The ids stay zero, so the CI steps fail loudly rather than
			// read a pipeline still running and pass on empty answers.
			p.Say("!! the default branch's pipeline did not finish in time: the CI steps will fail")
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(15 * time.Second):
		}
	}
	s.Pipeline = pipelines[0].ID
	red := p.Redactor()
	// Every branch's pipeline is printed somewhere: a merge request names
	// its head pipeline.
	var all []struct {
		ID int64 `json:"id"`
	}
	if err := c.Do(ctx, gapi.Call{Method: "GET", Path: "projects/{}/pipelines", Args: []string{id}, Name: "live_pipelines"}, &all); err != nil {
		return fmt.Errorf("read the scratch pipelines: %w", err)
	}
	for _, pl := range all {
		red.Known(redact.KindID, itoa(pl.ID))
	}
	p.Sayf("the default branch's pipeline %s is %s", redact.ID(itoa(s.Pipeline)), pipelines[0].Status)
	var jobs []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if err := c.Do(ctx, gapi.Call{Method: "GET", Path: "projects/{}/pipelines/{}/jobs", Args: []string{id, itoa(s.Pipeline)},
		Name: "live_jobs"}, &jobs); err != nil {
		return fmt.Errorf("read the scratch jobs: %w", err)
	}
	for _, j := range jobs {
		red.Known(redact.KindID, itoa(j.ID))
		switch j.Name {
		case "unit tests":
			s.JobFailed = j.ID
		case "build":
			s.JobPassed = j.ID
		case "manual step":
			s.JobManual = j.ID
		}
	}
	return nil
}
