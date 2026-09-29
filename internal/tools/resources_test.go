package tools

import (
	"bytes"
	"errors"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/gitlab-mcp/v2/internal/gapi/gitlabtest"
)

// tokens matches a boundary token, which is drawn per call and so
// differs between a resource and its tool.
var tokens = regexp.MustCompile(`[0-9a-f]{16}`)

func TestResourcesCarryTheToolsText(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	list, err := h.cs.ListResourceTemplates(t.Context(), nil)
	if err != nil || len(list.ResourceTemplates) != 3 {
		t.Fatalf("templates: %v %v", list, err)
	}
	escaped := url.PathEscape(gitlabtest.ProjectAlpha)
	cases := []struct {
		uri, tool string
		args      map[string]any
	}{
		{"gitlab://projects/2001/issues/3", "get_issue", map[string]any{"project": alphaID, "iid": 3}},
		{"gitlab://projects/" + escaped + "/merge_requests/1", "get_merge_request",
			map[string]any{"project": gitlabtest.ProjectAlpha, "iid": 1}},
		{"gitlab://projects/2001/jobs/70002/log", "get_job_log", map[string]any{"project": alphaID, "job_id": gitlabtest.JobFailed}},
	}
	for _, c := range cases {
		res, err := h.cs.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: c.uri})
		if err != nil {
			t.Fatalf("%s: %v", c.uri, err)
		}
		if len(res.Contents) != 1 || res.Contents[0].URI != c.uri || res.Contents[0].MIMEType != "text/plain" {
			t.Fatalf("%s: contents %v", c.uri, res.Contents)
		}
		text, _ := h.ok(c.tool, c.args)
		if got, want := tokens.ReplaceAllString(res.Contents[0].Text, "T"), tokens.ReplaceAllString(text, "T"); got != want {
			t.Errorf("%s differs from %s:\n--- resource\n%s\n--- tool\n%s", c.uri, c.tool, got, want)
		}
	}
}

func TestResourceErrors(t *testing.T) {
	var logs bytes.Buffer
	h := newHarness(t, harnessOptions{logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	for uri, want := range map[string]string{
		"gitlab://projects/2001/issues/999":                                           "[not_found] ",
		"gitlab://projects/" + url.PathEscape(gitlabtest.ProjectSecret) + "/issues/1": "[not_found] ",
		"gitlab://projects/2001/issues/0":                                             "[invalid] ",
		"gitlab://projects/2001/jobs/x/log":                                           "[invalid] ",
	} {
		_, err := h.cs.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: uri})
		var je *jsonrpc.Error
		if !errors.As(err, &je) || !strings.HasPrefix(je.Message, want) {
			t.Errorf("%s: %v, want a JSON-RPC error starting %q", uri, err, want)
		}
	}
	out := logs.String()
	if !strings.Contains(out, `"msg":"resource read"`) || !strings.Contains(out, `"outcome":"not_found"`) {
		t.Fatalf("no resource read was logged:\n%s", out)
	}
	for _, bad := range []string{"gitlab://", gitlabtest.ProjectSecret, url.PathEscape(gitlabtest.ProjectSecret)} {
		if strings.Contains(out, bad) {
			t.Errorf("the logs carry %q:\n%s", bad, linesWith(out, bad))
		}
	}
}
