// Package mcpstdio is a small MCP client over a child process's stdio,
// for the programs under scripts/ that drive the built server: the smoke
// gate, the live driver and the evals. It speaks exactly the frames those
// need and nothing else, but it is one client, because hand-written
// JSON-RPC loops are places for the handshake to drift apart.
//
// Every line the server writes to stdout must be a JSON-RPC frame; a
// line that is not is kept and reported by Stray, because a stray line
// on stdout is a protocol violation the client would otherwise skip.
package mcpstdio

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

// Protocol is the revision Initialize asks for when none is given.
const Protocol = "2025-11-25"

// CallTimeout bounds one request when Config.Timeout is zero.
const CallTimeout = 2 * time.Minute

// envPrefix is the server's settings prefix. No setting a maintainer
// exported is inherited unless the caller passes it, so nothing in the
// shell changes what a run drives.
const envPrefix = "GITLAB_MCP_"

// Config says how to start the server.
type Config struct {
	// Args follow the binary on the command line.
	Args []string
	// Env is added to the environment, which is the parent's minus every
	// GITLAB_MCP_ variable. An MCP client passes command, args and env,
	// and nothing else.
	Env []string
	// Timeout bounds each request; zero means CallTimeout.
	Timeout time.Duration
}

// Session is one stdio conversation with the server.
type Session struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	// wake is signaled whenever a reply arrives or stdout closes; the
	// replies themselves wait in replies, keyed by id, so the reader
	// never blocks on a caller that is not awaiting.
	wake    chan struct{}
	done    chan struct{} // stdout reached EOF
	errDone chan struct{} // stderr reached EOF
	timeout time.Duration
	nextID  int

	// onCall sees every tool invocation, and options is what the server
	// published: together they let a run say how much of the surface it
	// drove, measured where the calls go out, so a call cannot be made
	// without being counted.
	onCall  func(tool string, args map[string]any)
	options map[string][]string

	// descriptions are the published tools' descriptions, by name.
	descriptions map[string]string

	// onElicit answers the server's questions to the person; when set,
	// Initialize declares form elicitation.
	onElicit func(message string) (action string)

	// wmu serializes writes to stdin: the reader answers the server's
	// own requests while a caller may be sending.
	wmu sync.Mutex

	mu      sync.Mutex
	stderr  []string
	stray   []string
	readErr error
	replies map[int]map[string]any
}

// Start launches the server.
func Start(binary string, cfg Config) (*Session, error) {
	cmd := exec.Command(binary, cfg.Args...) //nolint:gosec // the binary this repository built
	cmd.Env = Environ(os.Environ(), cfg.Env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", binary, err)
	}
	s := &Session{
		cmd: cmd, stdin: stdin,
		wake:    make(chan struct{}, 1),
		done:    make(chan struct{}),
		errDone: make(chan struct{}),
		replies: map[int]map[string]any{},
		timeout: cfg.Timeout,
	}
	if s.timeout <= 0 {
		s.timeout = CallTimeout
	}
	go s.readStdout(stdout)
	go s.readStderr(stderr)
	return s, nil
}

// Environ is base without the server's settings, plus extra.
func Environ(base []string, extra ...string) []string {
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		if !strings.HasPrefix(kv, envPrefix) {
			out = append(out, kv)
		}
	}
	return append(out, extra...)
}

func (s *Session) readStdout(r io.Reader) {
	defer s.signal()
	defer close(s.done)
	lines := bufio.NewScanner(r)
	lines.Buffer(make([]byte, 0, 64*1024), 64<<20)
	for lines.Scan() {
		line := strings.TrimSpace(lines.Text())
		if line == "" {
			continue
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(line), &frame); err != nil || frame["jsonrpc"] != "2.0" {
			s.mu.Lock()
			s.stray = append(s.stray, line)
			s.mu.Unlock()
			continue
		}
		if method, ok := frame["method"].(string); ok {
			// A request of the server's own, made while it serves ours —
			// a question to the person, a ping — is answered here; a
			// notification or a log needs nothing.
			if rid, hasID := frame["id"]; hasID {
				if err := s.answer(rid, method, frame["params"]); err != nil {
					s.mu.Lock()
					s.readErr = err
					s.mu.Unlock()
				}
			}
			continue
		}
		// A frame with no numeric id is the server speaking on its own
		// account. Nothing awaits it.
		if id, ok := frame["id"].(float64); ok {
			s.mu.Lock()
			s.replies[int(id)] = frame
			s.mu.Unlock()
			s.signal()
		}
	}
	s.mu.Lock()
	s.readErr = lines.Err()
	s.mu.Unlock()
}

// signal wakes an Await without blocking.
func (s *Session) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Session) readStderr(r io.Reader) {
	defer close(s.errDone)
	lines := bufio.NewScanner(r)
	lines.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for lines.Scan() {
		s.mu.Lock()
		s.stderr = append(s.stderr, strings.TrimRight(lines.Text(), "\r"))
		s.mu.Unlock()
	}
}

// StderrTail returns the last n log lines the server wrote.
func (s *Session) StderrTail(n int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.stderr) <= n {
		return slices.Clone(s.stderr)
	}
	return slices.Clone(s.stderr[len(s.stderr)-n:])
}

// Stray is every stdout line so far that was not a JSON-RPC frame.
func (s *Session) Stray() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.stray)
}

// Close closes stdin and waits for the server to exit, killing it after
// the timeout. It returns the exit error, if any. Both pipes are read to
// the end before Wait, as os/exec requires: Wait closes them, and a read
// still in progress would lose the last lines.
func (s *Session) Close() error {
	_ = s.stdin.Close()
	timer := time.NewTimer(s.timeout)
	defer timer.Stop()
	for _, ch := range []chan struct{}{s.done, s.errDone} {
		select {
		case <-ch:
		case <-timer.C:
			_ = s.cmd.Process.Kill()
			_ = s.cmd.Wait()
			return fmt.Errorf("the server did not exit within %s of stdin closing", s.timeout)
		}
	}
	return s.cmd.Wait()
}

// Send writes one frame as a line.
func (s *Session) Send(frame any) error {
	raw, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err = s.stdin.Write(append(raw, '\n'))
	return err
}

// answer replies to one request the server sent: ping with an empty
// result, elicitation/create through onElicit, anything else with
// method-not-found.
func (s *Session) answer(id any, method string, params any) error {
	frame := map[string]any{"jsonrpc": "2.0", "id": id}
	switch {
	case method == "ping":
		frame["result"] = map[string]any{}
	case method == "elicitation/create" && s.onElicit != nil:
		p, _ := params.(map[string]any)
		message, _ := p["message"].(string)
		result := map[string]any{"action": s.onElicit(message)}
		if result["action"] == "accept" {
			// The question's form has no fields (docs/architecture.md
			// §4.12): the accept is the answer.
			result["content"] = map[string]any{}
		}
		frame["result"] = result
	default:
		frame["error"] = map[string]any{"code": -32601, "message": "this client does not take " + method}
	}
	return s.Send(frame)
}

// Description is a published tool's description, "" for none.
func (s *Session) Description(tool string) string { return s.descriptions[tool] }

// OnElicit makes the session a client that can ask the person: it
// declares form elicitation at Initialize, so it is set before, and f
// answers each question the server puts with an action: accept,
// decline or cancel.
func (s *Session) OnElicit(f func(message string) (action string)) { s.onElicit = f }

// Notify sends a notification, which has no reply.
func (s *Session) Notify(method string, params any) error {
	frame := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		frame["params"] = params
	}
	return s.Send(frame)
}

// RPCError is the server answering with an error object rather than a
// result. A tool call reports a refusal inside its result, but a
// resource read reports one as a JSON-RPC error, and a driver that
// could not tell the two apart would count every expected refusal as a
// broken connection.
type RPCError struct {
	Method  string
	Code    int
	Message string
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("%s: %s (code %d)", e.Method, e.Message, e.Code)
}

// ErrClosed is the server closing stdout before a reply arrived.
var ErrClosed = errors.New("the server closed the connection")

// Request sends one request and waits for its reply, returning the
// result object. Frames the server sends on its own account —
// notifications, logs — are skipped.
func (s *Session) Request(method string, params any) (map[string]any, error) {
	s.nextID++
	id := s.nextID
	frame := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		frame["params"] = params
	}
	if err := s.Send(frame); err != nil {
		return nil, fmt.Errorf("write %s: %w", method, err)
	}
	return s.Await(method, id)
}

// Await waits for the reply to id, which was sent with Send.
func (s *Session) Await(method string, id int) (map[string]any, error) {
	deadline := time.NewTimer(s.timeout)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		reply, ok := s.replies[id]
		delete(s.replies, id)
		s.mu.Unlock()
		if ok {
			if e, ok := reply["error"].(map[string]any); ok {
				code, _ := e["code"].(float64)
				msg, _ := e["message"].(string)
				return nil, &RPCError{Method: method, Code: int(code), Message: msg}
			}
			result, _ := reply["result"].(map[string]any)
			return result, nil
		}
		select {
		case <-s.done:
			s.mu.Lock()
			_, arrived := s.replies[id]
			s.mu.Unlock()
			if arrived {
				continue
			}
			s.mu.Lock()
			readErr := s.readErr
			s.mu.Unlock()
			if readErr != nil {
				return nil, fmt.Errorf("%s: %w: reading stdout: %w", method, ErrClosed, readErr)
			}
			return nil, fmt.Errorf("%s: %w; stderr:\n%s", method, ErrClosed,
				strings.Join(s.StderrTail(20), "\n"))
		case <-s.wake:
		case <-deadline.C:
			return nil, fmt.Errorf("%s: no reply within %s", method, s.timeout)
		}
	}
}

// Initialize completes the handshake at protocol (Protocol when empty)
// and lists the tool surface. It returns the initialize result.
func (s *Session) Initialize(protocol, clientName string) (map[string]any, error) {
	if protocol == "" {
		protocol = Protocol
	}
	capabilities := map[string]any{}
	if s.onElicit != nil {
		capabilities["elicitation"] = map[string]any{"form": map[string]any{}}
	}
	result, err := s.Request("initialize", map[string]any{
		"protocolVersion": protocol,
		"capabilities":    capabilities,
		"clientInfo":      map[string]any{"name": clientName, "version": "0"},
	})
	if err != nil {
		return nil, err
	}
	if err := s.Notify("notifications/initialized", nil); err != nil {
		return nil, err
	}
	tools, err := s.ListTools()
	if err != nil {
		return nil, err
	}
	s.options = map[string][]string{}
	s.descriptions = map[string]string{}
	for _, t := range tools {
		s.options[t.Name] = t.Options
		if d, _ := t.Raw["description"].(string); d != "" {
			s.descriptions[t.Name] = d
		}
	}
	return result, nil
}

// Tool is one listed tool: its name, its input option names sorted,
// and the whole listing entry.
type Tool struct {
	Name    string
	Options []string
	Raw     map[string]any
}

// ListTools lists every tool, following the cursor.
func (s *Session) ListTools() ([]Tool, error) {
	var tools []Tool
	var cursor string
	for {
		var params any
		if cursor != "" {
			params = map[string]any{"cursor": cursor}
		}
		result, err := s.Request("tools/list", params)
		if err != nil {
			return nil, err
		}
		raw, _ := result["tools"].([]any)
		for _, t := range raw {
			m, ok := t.(map[string]any)
			if !ok {
				continue
			}
			name, _ := m["name"].(string)
			schema, _ := m["inputSchema"].(map[string]any)
			props, _ := schema["properties"].(map[string]any)
			opts := make([]string, 0, len(props))
			for option := range props {
				opts = append(opts, option)
			}
			slices.Sort(opts)
			tools = append(tools, Tool{Name: name, Options: opts, Raw: m})
		}
		cursor, _ = result["nextCursor"].(string)
		if cursor == "" {
			return tools, nil
		}
	}
}

// OnCall registers a watcher for every tool call this session makes.
func (s *Session) OnCall(f func(tool string, args map[string]any)) { s.onCall = f }

// Options is the tool surface the server published at initialize: each
// registered tool and the option names its schema declares.
func (s *Session) Options() map[string][]string { return s.options }

// ToolResult is one tool call's reply.
type ToolResult struct {
	// Text is every text content block, joined.
	Text string
	// IsError is the tool refusing, as opposed to the protocol failing.
	IsError bool
	// Structured is structuredContent, raw; nil when absent.
	Structured json.RawMessage
}

// CallTool runs one tool.
func (s *Session) CallTool(name string, args map[string]any) (ToolResult, error) {
	if s.onCall != nil {
		s.onCall(name, args)
	}
	if args == nil {
		args = map[string]any{}
	}
	result, err := s.Request("tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return ToolResult{}, err
	}
	var out ToolResult
	out.IsError, _ = result["isError"].(bool)
	var b strings.Builder
	content, _ := result["content"].([]any)
	for _, c := range content {
		if m, ok := c.(map[string]any); ok {
			if t, ok := m["text"].(string); ok {
				b.WriteString(t)
			}
		}
	}
	out.Text = b.String()
	if sc, ok := result["structuredContent"]; ok && sc != nil {
		out.Structured, _ = json.Marshal(sc)
	}
	return out, nil
}

// ReadResource reads one resource and returns its text and media type.
// A refusal comes back as an *RPCError.
func (s *Session) ReadResource(uri string) (text, mime string, err error) {
	result, err := s.Request("resources/read", map[string]any{"uri": uri})
	if err != nil {
		return "", "", err
	}
	contents, _ := result["contents"].([]any)
	var b strings.Builder
	for _, c := range contents {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if t, ok := m["text"].(string); ok {
			b.WriteString(t)
		}
		if mt, ok := m["mimeType"].(string); ok && mime == "" {
			mime = mt
		}
	}
	return b.String(), mime, nil
}

// Encode renders arguments for a transcript heading.
func Encode(args map[string]any) string {
	raw, err := json.Marshal(args)
	if err != nil {
		return "{}"
	}
	return string(raw)
}
