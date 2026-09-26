//go:build evals

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mmedum/gitlab-mcp/internal/config"
	"github.com/mmedum/gitlab-mcp/internal/credentials"
	"github.com/mmedum/gitlab-mcp/internal/gapi/gitlabtest"
	"github.com/mmedum/gitlab-mcp/scripts/internal/gatekit"
)

// The live half: the model runs in `claude -p` with this server as its
// only tools.
//
// The built-in tools are turned off at the source, never listed away:
// --tools "" disables every one, and --allowed-tools names the MCP ones
// that remain. A denylist fails open — a sibling's first version listed
// six built-ins and the model used two it had not listed to read the
// maintainer's notes mid-task. --setting-sources "" drops the
// maintainer's settings, hooks and permission rules, and
// --strict-mcp-config keeps their other MCP servers out. What is left is
// the thing being scored. A tool call the harness sees that is not one
// of this server's fails the task: the fence is checked, not assumed.

// serverEnv signs the binary in to the world the way a person would —
// an authorization code for alice, exchanged for a refresh token — and
// returns the environment an MCP client passes it, and a cleanup.
func serverEnv(ctx context.Context, w *world) ([]string, func(), error) {
	refresh, err := refreshToken(ctx, w.srv.URL)
	if err != nil {
		return nil, nil, fmt.Errorf("sign in to the fixture: %w", err)
	}
	dir, err := os.MkdirTemp("", "evals-config-*")
	if err != nil {
		return nil, nil, err
	}
	env := []string{
		config.EnvInstance + "=" + w.srv.URL,
		config.EnvAllowHTTP + "=true",
		config.EnvClientID + "=" + gitlabtest.ClientID,
		config.EnvProfile + "=evals",
		config.EnvConfigDir + "=" + dir,
		config.EnvConfigDirAllowOutsideHome + "=true",
		credentials.EnvVar + "=" + refresh,
	}
	return env, func() { _ = os.RemoveAll(dir) }, nil
}

func refreshToken(ctx context.Context, base string) (string, error) {
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	redirect := "http://127.0.0.1:1/callback"
	q := url.Values{"client_id": {gitlabtest.ClientID}, "redirect_uri": {redirect}, "response_type": {"code"},
		"state": {"evals"}, "scope": {"api"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/oauth/authorize?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	res, err := noRedirect.Do(req)
	if err != nil {
		return "", err
	}
	_ = res.Body.Close()
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil || loc.Query().Get("code") == "" {
		return "", fmt.Errorf("the authorization answered %s with no code", res.Status)
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")},
		"redirect_uri": {redirect}, "client_id": {gitlabtest.ClientID}}
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, base+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	var tok struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&tok); err != nil || tok.RefreshToken == "" {
		return "", fmt.Errorf("the token exchange answered %s with no refresh token", res.Status)
	}
	return tok.RefreshToken, nil
}

func askClaude(ctx context.Context, o liveOptions, prompt string, env []string) (Run, error) {
	dir, err := os.MkdirTemp("", "evals-mcp-*")
	if err != nil {
		return Run{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	envMap := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		envMap[k] = v
	}
	bin, err := filepath.Abs(o.bin)
	if err != nil {
		return Run{}, err
	}
	cfg, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"gitlab": map[string]any{
		"command": bin, "args": []string{}, "env": envMap}}})
	if err != nil {
		return Run{}, err
	}
	cfgPath := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
		return Run{}, err
	}
	cmd := exec.CommandContext(ctx, "claude", claudeArgs(prompt, cfgPath, o.model, o.budget)...) //nolint:gosec // fixed program
	cmd.Dir = dir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Run{}, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return Run{}, fmt.Errorf("start claude: %w", err)
	}
	var run Run
	var fence error
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for sc.Scan() {
		if err := readEvent(&run, sc.Bytes()); err != nil && fence == nil {
			fence = err
		}
	}
	if err := cmd.Wait(); err != nil {
		return run, fmt.Errorf("claude: %w: %s", err, gatekit.Clip(stderr.String(), 300))
	}
	if fence != nil {
		return run, fence
	}
	return run, nil
}
