package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/scripts/internal/gatekit"
	"github.com/mmedum/gitlab-mcp/scripts/internal/mcpstdio"
)

// The smoke gate drives the shipped binary over stdio, the way a client
// does, with no credentials and on a host that cannot resolve. It is the
// check that the artifact speaks the protocol at all (the standard's §11):
//
//   - it negotiates at two protocol revisions, each answered with itself,
//     which is the server's own statement that its SDK supports it;
//   - stdout carries only JSON-RPC frames, with logging at debug;
//   - no tool name has a "." in it, and every name is one a client takes;
//   - a tool call with no credentials is a tool result carrying [auth],
//     not a crash, an exit or a protocol error;
//   - read-only mode registers no tool whose annotations say it writes;
//   - resource templates list, and a filled one read without
//     credentials says [auth];
//   - stdin closed the moment the last message is written, a request in
//     flight, is an ordinary disconnect: exit 0. A server that exits
//     non-zero there is logged by every host as a crash, and a test that
//     writes, sleeps and then closes never sees it.

// smokeProtocols are the revisions a session is opened at: the SDK's
// newest handshake revision and the one before it.
var smokeProtocols = []string{"2025-11-25", "2025-06-18"}

// toolName is what MCP clients accept, less the "." some reject.
var toolName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

const smokeTimeout = 20 * time.Second

func smoke(out io.Writer, args []string) error {
	cfgDir, err := os.MkdirTemp("", "gates-smoke-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(cfgDir) }()
	return runSmoke(out, executable(args[0]), smokeEnv(cfgDir), surfaceFloor)
}

// smokeEnv is a configuration no real account can be behind: a host
// under .invalid, a profile named for this gate, and a configuration
// directory made for the run.
func smokeEnv(cfgDir string) []string {
	return []string{
		"GITLAB_MCP_INSTANCE=https://gitlab.example.invalid",
		"GITLAB_MCP_PROFILE=gates-smoke",
		"GITLAB_MCP_CONFIG_DIR=" + cfgDir,
		"GITLAB_MCP_CONFIG_DIR_ALLOW_OUTSIDE_HOME=true",
		"GITLAB_MCP_LOG_LEVEL=debug",
	}
}

func runSmoke(out io.Writer, binary string, env []string, floor int) error {
	var tools int
	for _, proto := range smokeProtocols {
		n, err := smokeSession(binary, env, proto, floor)
		if err != nil {
			return fmt.Errorf("at %s: %w", proto, err)
		}
		tools = n
	}
	if err := smokeReadOnly(binary, env); err != nil {
		return fmt.Errorf("read-only: %w", err)
	}
	if err := smokeDisconnect(binary, env); err != nil {
		return fmt.Errorf("disconnect: %w", err)
	}
	_, _ = fmt.Fprintf(out, "smoke ok: %s at %s; %d tools, [auth] without credentials, read-only registers no write, "+
		"an abrupt disconnect exits 0, stdout only frames\n", binary, strings.Join(smokeProtocols, " and "), tools)
	return nil
}

// frame is a request or notification the smoke sends. A frame with an
// id is awaited; the ids awaited are the ids sent, never a second list.
type frame map[string]any

func request(id int, method string, params any) frame {
	f := frame{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		f["params"] = params
	}
	return f
}

// initialized is the notification that ends the handshake.
var initialized = frame{"jsonrpc": "2.0", "method": "notifications/initialized"}

// initialize opens a session at a protocol revision; it is always the
// first request, id 1.
func initialize(proto string) frame {
	return request(1, "initialize", map[string]any{
		"protocolVersion": proto, "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "gates-smoke", "version": "0"},
	})
}

// exchange sends the frames in order, awaiting the reply to each one
// that has an id before the next is written, then closes stdin and waits
// for the exit. The ids awaited are the ids sent. A write that fails
// means the server is gone: it is reaped, and its stderr says why.
func exchange(binary string, env []string, frames []frame) (map[int]map[string]any, error) {
	sess, err := mcpstdio.Start(binary, mcpstdio.Config{Env: env, Timeout: smokeTimeout})
	if err != nil {
		return nil, err
	}
	replies := map[int]map[string]any{}
	for _, f := range frames {
		if err := sess.Send(map[string]any(f)); err != nil {
			exit := sess.Close()
			return nil, fmt.Errorf("write: %w (exit: %w); stderr:\n%s", err, exit, strings.Join(sess.StderrTail(20), "\n"))
		}
		id, ok := f["id"].(int)
		if !ok {
			continue
		}
		method, _ := f["method"].(string)
		result, err := sess.Await(method, id)
		var rpc *mcpstdio.RPCError
		switch {
		case errors.As(err, &rpc):
			replies[id] = map[string]any{"error": map[string]any{"code": float64(rpc.Code), "message": rpc.Message}}
		case err != nil:
			_ = sess.Close()
			return nil, err
		default:
			replies[id] = map[string]any{"result": result}
		}
	}
	if err := sess.Close(); err != nil {
		return nil, fmt.Errorf("the server did not exit cleanly when the client hung up: %w; stderr:\n%s",
			err, strings.Join(sess.StderrTail(20), "\n"))
	}
	if stray := sess.Stray(); len(stray) > 0 {
		return nil, fmt.Errorf("stdout carried %d line(s) that are not JSON-RPC frames, the first %q", len(stray), gatekit.Clip(stray[0], 120))
	}
	return replies, nil
}

func smokeSession(binary string, env []string, proto string, floor int) (int, error) {
	frames := []frame{
		initialize(proto),
		initialized,
		request(2, "tools/list", nil),
		request(3, "tools/call", map[string]any{"name": "get_me", "arguments": map[string]any{}}),
		request(4, "resources/templates/list", nil),
	}
	replies, err := exchange(binary, env, frames)
	if err != nil {
		return 0, err
	}
	init := resultOf(replies[1])
	if got, _ := init["protocolVersion"].(string); got != proto {
		return 0, fmt.Errorf("asked for %s and the server answered %q; its SDK does not support the revision", proto, got)
	}
	if info, _ := init["serverInfo"].(map[string]any); info["name"] != "gitlab-mcp" {
		return 0, fmt.Errorf("serverInfo is %v", init["serverInfo"])
	}
	tools, err := listedTools(replies[2])
	if err != nil {
		return 0, err
	}
	if len(tools) < floor {
		return 0, fmt.Errorf("tools/list gave %d tools and the floor is %d", len(tools), floor)
	}
	for _, t := range tools {
		name, _ := t["name"].(string)
		if strings.Contains(name, ".") || !toolName.MatchString(name) {
			return 0, fmt.Errorf("tool name %q: MCP clients take [A-Za-z0-9_-], and several refuse a dot", name)
		}
	}
	call := resultOf(replies[3])
	if call == nil {
		return 0, fmt.Errorf("get_me without credentials was a protocol error, %v, rather than a tool result", replies[3]["error"])
	}
	if isErr, _ := call["isError"].(bool); !isErr || !strings.Contains(contentText(call), "[auth]") {
		return 0, fmt.Errorf("get_me without credentials answered %q (isError %v); it must be a tool error carrying [auth]",
			gatekit.Clip(contentText(call), 160), call["isError"])
	}
	tmpl := resultOf(replies[4])
	if tmpl == nil {
		return 0, fmt.Errorf("resources/templates/list failed: %v", replies[4]["error"])
	}
	list, _ := tmpl["resourceTemplates"].([]any)
	for _, t := range list {
		m, _ := t.(map[string]any)
		uri, _ := m["uriTemplate"].(string)
		if strings.Contains(uri, "{+") {
			return 0, fmt.Errorf("resource template %q uses {+x}, which lets a value carry a slash into the address", uri)
		}
		if err := smokeResource(binary, env, proto, uri); err != nil {
			return 0, err
		}
	}
	return len(tools), nil
}

// smokeResource reads one template, filled with synthetic values, and
// wants [auth] back as a JSON-RPC error.
func smokeResource(binary string, env []string, proto, uri string) error {
	filled := regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(uri, "7")
	replies, err := exchange(binary, env, []frame{
		initialize(proto), initialized,
		request(2, "resources/read", map[string]any{"uri": filled}),
	})
	if err != nil {
		return err
	}
	e, _ := replies[2]["error"].(map[string]any)
	if msg, _ := e["message"].(string); !strings.Contains(msg, "[auth]") {
		return fmt.Errorf("reading %s without credentials answered %v; it must be an error carrying [auth]", filled, replies[2])
	}
	return nil
}

// smokeReadOnly asks the server in read-only mode for its tools and
// holds each to its own annotations: a mode that removes writes must
// actually remove them.
func smokeReadOnly(binary string, env []string) error {
	replies, err := exchange(binary, append(slices.Clone(env), "GITLAB_MCP_READ_ONLY=true"), []frame{
		initialize(smokeProtocols[0]), initialized, request(2, "tools/list", nil),
	})
	if err != nil {
		return err
	}
	tools, err := listedTools(replies[2])
	if err != nil {
		return err
	}
	if len(tools) == 0 {
		return errors.New("read-only mode registered no tools at all")
	}
	for _, t := range tools {
		ann, _ := t["annotations"].(map[string]any)
		if ro, _ := ann["readOnlyHint"].(bool); !ro {
			return fmt.Errorf("read-only mode registered %v, whose annotations do not say readOnlyHint", t["name"])
		}
	}
	return nil
}

// smokeDisconnect writes a session with a request in flight and closes
// stdin the moment the last byte is written. The exit must be 0.
func smokeDisconnect(binary string, env []string) error {
	sess, err := mcpstdio.Start(binary, mcpstdio.Config{Env: env, Timeout: smokeTimeout})
	if err != nil {
		return err
	}
	for _, f := range []frame{
		initialize(smokeProtocols[0]), initialized,
		request(2, "tools/call", map[string]any{"name": "get_me", "arguments": map[string]any{}}),
		request(3, "tools/list", nil),
	} {
		if err := sess.Send(map[string]any(f)); err != nil {
			exit := sess.Close()
			return fmt.Errorf("write: %w (exit: %w); stderr:\n%s", err, exit, strings.Join(sess.StderrTail(20), "\n"))
		}
	}
	if err := sess.Close(); err != nil {
		return fmt.Errorf("the client hung up with a request in flight and the server exited with %w; hosts log that as a crash. stderr:\n%s",
			err, strings.Join(sess.StderrTail(20), "\n"))
	}
	if stray := sess.Stray(); len(stray) > 0 {
		return fmt.Errorf("stdout carried %q, which is not a JSON-RPC frame", gatekit.Clip(stray[0], 120))
	}
	return nil
}

func resultOf(reply map[string]any) map[string]any {
	r, _ := reply["result"].(map[string]any)
	return r
}

func listedTools(reply map[string]any) ([]map[string]any, error) {
	r := resultOf(reply)
	if r == nil {
		return nil, fmt.Errorf("tools/list failed: %v", reply["error"])
	}
	raw, _ := r["tools"].([]any)
	var out []map[string]any
	for _, t := range raw {
		if m, ok := t.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out, nil
}

func contentText(result map[string]any) string {
	var b strings.Builder
	content, _ := result["content"].([]any)
	for _, c := range content {
		if m, ok := c.(map[string]any); ok {
			if t, ok := m["text"].(string); ok {
				b.WriteString(t)
			}
		}
	}
	return b.String()
}
