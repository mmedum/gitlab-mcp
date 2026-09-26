//go:build live

// Command livegitlab drives the built server over stdio against a real
// GitLab instance, which is the check no fake can make: that the tools
// behave against GitLab itself (docs/architecture.md §9.1, §13).
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
//	make live LIVE_ARGS="-instance https://gitlab.example.com -namespace example-group/scratch"
//
// It records what it sent, per tool option, in
// testdata/live-cover-record.tsv, which `gates live-cover` reads.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/app"
	"github.com/mmedum/gitlab-mcp/internal/config"
	"github.com/mmedum/gitlab-mcp/internal/credentials"
	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/scripts/internal/mcpstdio"
	"github.com/mmedum/gitlab-mcp/scripts/internal/redact"
)

// options are what one run was asked to do.
type options struct {
	binary    string
	instance  string
	namespace string
	profile   string
	keep      bool
	record    string
}

func main() {
	var o options
	flag.StringVar(&o.binary, "bin", "./gitlab-mcp", "the server binary to drive")
	flag.StringVar(&o.instance, "instance", "", "the instance, as GITLAB_MCP_INSTANCE takes it (required)")
	flag.StringVar(&o.namespace, "namespace", "", "the group the scratch projects are created in, which you own (required)")
	flag.StringVar(&o.profile, "profile", "", "the signed-in profile to use; empty takes the default")
	flag.BoolVar(&o.keep, "keep", false, "leave the scratch projects in place for a look afterwards")
	flag.StringVar(&o.record, "record", "testdata/live-cover-record.tsv", "where to write what the run sent")
	flag.Parse()

	p := redact.NewPrinter(redact.NewRedactor(false))
	if o.instance == "" || o.namespace == "" {
		p.Fail("livegitlab: -instance and -namespace are required; the run creates its projects in -namespace and reads nothing else")
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
	env := map[string]string{config.EnvInstance: o.instance}
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
	name, err := newRunName()
	if err != nil {
		return err
	}
	s := scratch{Namespace: o.namespace, Name: name, User: user.Username, Default: "main",
		Feature: "feature-" + name[len(name)-6:], Feature2: "draft-" + name[len(name)-6:],
		File: "docs/live.md", Label: "live-" + name[len(name)-6:]}
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

	serverEnv := []string{config.EnvInstance + "=" + o.instance, config.EnvLogLevel + "=info"}
	if o.profile != "" {
		serverEnv = append(serverEnv, config.EnvProfile+"="+o.profile)
	}
	sess, err := mcpstdio.Start(o.binary, mcpstdio.Config{Env: serverEnv})
	if err != nil {
		return err
	}
	defer func() { _ = sess.Close() }()
	rec := newRecorder()
	sess.OnCall(rec.Sent)
	init, err := sess.Initialize("", "livegitlab")
	if err != nil {
		return err
	}
	p.Sayf("protocol %v; %d tools registered", init["protocolVersion"], len(sess.Options()))

	unexpected, err := drive(sess, plan(s), s, p)
	if err != nil {
		return err
	}
	for _, line := range sess.StderrTail(20) {
		p.Say("stderr | " + line)
	}
	if missing := rec.missing(sess.Options()); len(missing) > 0 {
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

// drive sends every step, refusing one that would read outside the
// scratch project before it goes out.
func drive(sess *mcpstdio.Session, steps []step, s scratch, p *redact.Printer) (int, error) {
	unexpected := 0
	for _, st := range steps {
		if err := confined(st, s); err != nil {
			return unexpected, fmt.Errorf("the plan reads outside the scratch project, which §9.1 forbids: %w", err)
		}
		args := st.args
		for {
			p.Sayf("\n=== %s %s ===", st.tool, mcpstdio.Encode(args))
			if st.why != "" {
				p.Sayf("(expecting a refusal: %s)", st.why)
			}
			res, err := sess.CallTool(st.tool, args)
			if err != nil {
				return unexpected, err
			}
			p.Say(res.Text)
			if res.IsError != st.expectError {
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
	id := itoa(s.ID)
	var commit struct {
		ID string `json:"id"`
	}
	for _, c2 := range []struct {
		branch, start, msg, content string
	}{
		{s.Default, "", "Add the live file", "# Live\n\nLine two.\nLine three.\n"},
		{s.Feature, s.Default, "Change the live file", "# Live\n\nLine two, changed.\nLine three.\n"},
		{s.Feature2, s.Default, "Draft a change", "# Live\n\nLine two.\nLine three, drafted.\n"},
	} {
		action := "update"
		if c2.start == "" {
			action = "create"
		}
		body := map[string]any{"branch": c2.branch, "commit_message": c2.msg, "actions": []any{
			map[string]any{"action": action, "file_path": s.File, "content": c2.content},
			map[string]any{"action": action, "file_path": "src/main.txt", "content": "synthetic source for " + c2.branch + "\n"},
		}}
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
	if err := c.Do(ctx, gapi.Call{Method: "POST", Path: "projects/{}/merge_requests/{}/discussions", Args: []string{id, itoa(s.MR)},
		Body: map[string]any{"body": "A synthetic thread on the live merge request."}, Name: "live_discussion"}, nil); err != nil {
		return created, fmt.Errorf("start a thread: %w", err)
	}
	return created, nil
}
