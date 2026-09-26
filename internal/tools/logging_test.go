package tools

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/gitlab-mcp/internal/gapi/gitlabtest"
)

// The rule this file holds (CLAUDE.md rule 3, docs/architecture.md
// §9.2): a log line may say that a call happened, which tool, how it
// ended, how long it took and the rate bucket. It may not carry what the
// caller sent or what GitLab returned: hosts, paths, branch names, file
// paths, titles, bodies, search terms, usernames. A path and a search
// term reach a log through a request URL, which is why a transport
// error is stripped of its URL before anything logs it.
//
// Every registered tool is driven, not a hand-picked few, with a canary
// in every string argument. Each is driven twice: once with canaries
// everywhere, which mostly ends at the first lookup, and once with a real
// project so the call goes deep enough to read and render GitLab's own
// payload, whose fixture text is forbidden in the log as well.

const canary = "CANARY"

// fixtureText is payload the in-memory GitLab returns: none of it may
// reach a log either.
var fixtureText = []string{
	"example-group",                // project and group paths
	"Generated issue",              // issue titles
	"Generated change",             // merge request titles
	"Can you add a test",           // comment bodies
	"Steps to reproduce",           // descriptions
	"feature/login",                // a branch name
	"src/login.go",                 // a file path
	"alice@example.com",            // an email address
	"Update README.md",             // a commit title
	"fixture-secret-never-decoded", // a secret GitLab leaks through project reads
	"Consider naming this",         // a draft review comment
	"Release 1.0",                  // a tag message
	"Sprint 2",                     // a milestone title
	"Generated label",              // a label description
	"make test",                    // a job log line
	gitlabtest.FakeToken,           // a secret a job log prints
}

func TestLogsNeverCarryThePayload(t *testing.T) {
	var logs bytes.Buffer
	lg := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h := newHarness(t, harnessOptions{logger: lg})

	list, err := h.cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != len(h.reg) || len(list.Tools) == 0 {
		t.Fatalf("listed %d tools, registered %d", len(list.Tools), len(h.reg))
	}
	// Inputs the second pass needs real, or the call ends at a 404
	// before any payload is read. nil drops the input.
	_, branches := h.ok("list_branches", map[string]any{"project": gitlabtest.ProjectAlpha, "search": "main"})
	deep := map[string]map[string]any{
		"get_file":        {"path": "src/main.go", "ref": nil},
		"get_commit":      {"sha": get(branches, "branches", 0, "commit_id")},
		"get_mr_diff":     {"paths": nil},
		"compare_refs":    {"from": "main", "to": "feature/login"},
		"list_tags":       {"search": nil},
		"list_pipelines":  {"ref": nil, "sha": nil, "status": nil, "source": nil, "username": nil},
		"get_pipeline":    {"pipeline_id": gitlabtest.PipelineFailed},
		"list_jobs":       {"pipeline_id": gitlabtest.PipelineFailed, "scope": nil},
		"get_job_log":     {"job_id": gitlabtest.JobFailed, "byte_offset": nil, "byte_limit": nil},
		"lint_ci":         {"ref": nil},
		"list_labels":     {"search": nil},
		"list_milestones": {"group": nil, "search": nil, "title": nil, "state": nil},
		"list_members":    {"query": nil},
		"list_todos":      {"action": nil, "type": nil},
		"search":          {"group": nil, "search": "package", "state": nil, "ref": nil},
	}
	for _, tool := range list.Tools {
		schema := tool.InputSchema.(map[string]any)
		for pass, project := range []any{nil, gitlabtest.ProjectAlpha} {
			args := canaryArgs(t, tool.Name, schema, pass, h.gl.URL)
			if project != nil {
				if _, ok := args["project"]; ok {
					args["project"] = project
				}
				// A canary page token is refused before anything is read.
				delete(args, "page_token")
				for k, v := range deep[tool.Name] {
					if v == nil {
						delete(args, k)
					} else {
						args[k] = v
					}
				}
			}
			// A refusal is as interesting as a success: error paths are
			// where a payload reaches a log. Results are not checked.
			if _, err := h.cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool.Name, Arguments: args}); err != nil {
				t.Fatalf("call %s: %v", tool.Name, err)
			}
		}
	}

	// Lenient decoding logs that it happened, by input name only.
	if _, err := h.cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "search_issues",
		Arguments: map[string]any{"labels": `["` + canary + `-lenient"]`, "max": "5", "search": ""}}); err != nil {
		t.Fatal(err)
	}

	out := logs.String()
	if strings.TrimSpace(out) == "" {
		t.Fatal("nothing was logged; this test would pass on a server that logs nothing")
	}
	if !strings.Contains(out, `"msg":"tool call"`) || !strings.Contains(out, `"msg":"gitlab_request"`) ||
		!strings.Contains(out, `"msg":"lenient arguments"`) {
		t.Fatalf("the per-call line or the request line is missing:\n%s", out)
	}
	// The second pass went deep: GitLab answered the payload reads.
	for _, call := range []string{"get_issue", "list_discussions", "get_merge_request", "get_file", "get_commit_diff",
		"list_mr_diffs", "list_draft_notes", "compare_refs", "list_tags", "get_pipeline", "get_job_log", "lint_ci",
		"list_labels", "list_milestones", "list_members", "list_todos", "search"} {
		if !strings.Contains(out, `"call":"`+call+`","attempt":1,"status":200`) {
			t.Errorf("no successful %s request was logged; the canaries never reached a payload", call)
		}
	}
	host := strings.TrimPrefix(h.gl.URL, "http://")
	for _, bad := range append([]string{canary, host, url.PathEscape(gitlabtest.ProjectAlpha)}, fixtureText...) {
		if strings.Contains(out, bad) {
			t.Errorf("the logs carry %q:\n%s", bad, linesWith(out, bad))
		}
	}
}

// canaryArgs builds arguments from a tool's input schema: a canary in
// every string (each distinct, so a hit names its input), a valid value
// for a closed one, and a small valid number, so each call gets as far
// as it can. The second pass makes resolve_url's URL a real one.
func canaryArgs(t *testing.T, tool string, schema map[string]any, pass int, base string) map[string]any {
	t.Helper()
	props, _ := schema["properties"].(map[string]any)
	args := map[string]any{}
	for name, raw := range props {
		p, _ := raw.(map[string]any)
		value := fmt.Sprintf("%s-%s-%s", canary, tool, name)
		switch {
		case p["enum"] != nil:
			args[name] = p["enum"].([]any)[0]
		case hasType(p, "string"):
			args[name] = value
		case hasType(p, "integer"):
			args[name] = 1
		case hasType(p, "boolean"):
			args[name] = false
		case hasType(p, "array"):
			args[name] = []any{value}
		}
	}
	if tool == "resolve_url" && pass == 1 {
		args["url"] = base + "/" + gitlabtest.ProjectAlpha + "/-/blob/feature/login/src/login.go"
	}
	// Offsets past the end are refused before anything is read.
	for _, name := range []string{"offset", "file_offset", "note_id"} {
		if _, ok := args[name]; ok {
			args[name] = 0
		}
	}
	return args
}

func hasType(p map[string]any, typ string) bool {
	if p["type"] == typ {
		return true
	}
	types, _ := p["type"].([]any)
	return slices.Contains(types, any(typ))
}

func linesWith(out, s string) string {
	var hits []string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, s) {
			hits = append(hits, line)
		}
	}
	return strings.Join(hits, "\n")
}
