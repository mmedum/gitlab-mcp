package mcpstdio

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as a fake server: with fakeServerEnv set it
// speaks just enough MCP over its own stdio for the client to be driven.
const fakeServerEnv = "MCPSTDIO_FAKE_SERVER"

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeServerEnv); mode != "" {
		fakeServer(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeServer(mode string) {
	in := bufio.NewScanner(os.Stdin)
	reply := func(id any, result any) {
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
		fmt.Println(string(raw))
	}
	for in.Scan() {
		var req map[string]any
		if json.Unmarshal(in.Bytes(), &req) != nil {
			continue
		}
		id, method := req["id"], req["method"]
		if id == nil {
			continue
		}
		// Unrelated traffic before every reply, which the client skips.
		fmt.Println(`{"jsonrpc":"2.0","method":"notifications/message","params":{}}`)
		if mode == "stray" {
			fmt.Println("hello from stdout")
		}
		if mode == "chatty" {
			for range 500 {
				fmt.Println(`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"debug"}}`)
			}
		}
		switch method {
		case "initialize":
			params, _ := req["params"].(map[string]any)
			reply(id, map[string]any{"protocolVersion": params["protocolVersion"],
				"leaked_env": os.Getenv("GITLAB_MCP_TEST_INSTANCE"), "extra_env": os.Getenv("EXTRA")})
		case "tools/list":
			params, _ := req["params"].(map[string]any)
			if params["cursor"] == "page2" {
				reply(id, map[string]any{"tools": []any{map[string]any{"name": "get_issue",
					"inputSchema": map[string]any{"properties": map[string]any{"project": map[string]any{}, "iid": map[string]any{}}}}}})
				continue
			}
			reply(id, map[string]any{"nextCursor": "page2", "tools": []any{map[string]any{"name": "whoami",
				"inputSchema": map[string]any{"properties": map[string]any{}}}}})
		case "tools/call":
			reply(id, map[string]any{"isError": true,
				"content":           []any{map[string]any{"type": "text", "text": "[auth] "}, map[string]any{"type": "text", "text": "sign in"}},
				"structuredContent": map[string]any{"ok": false}})
		case "resources/read":
			raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id,
				"error": map[string]any{"code": -32602, "message": "[not_found] no such issue"}})
			fmt.Println(string(raw))
		case "hang":
			// no reply
		case "exit":
			return
		}
	}
}

func start(t *testing.T, mode string, cfg Config) *Session {
	t.Helper()
	t.Setenv(fakeServerEnv, mode)
	t.Setenv("GITLAB_MCP_TEST_INSTANCE", "http://127.0.0.1:9")
	s, err := Start(os.Args[0], cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSessionDrivesTheSurface(t *testing.T) {
	s := start(t, "normal", Config{Env: []string{"EXTRA=yes"}})
	var calls []string
	s.OnCall(func(tool string, _ map[string]any) { calls = append(calls, tool) })

	init, err := s.Initialize("2025-06-18", "test")
	if err != nil {
		t.Fatal(err)
	}
	if init["protocolVersion"] != "2025-06-18" {
		t.Errorf("protocolVersion = %v, want the one asked for", init["protocolVersion"])
	}
	if init["leaked_env"] != "" {
		t.Errorf("the server inherited GITLAB_MCP_TEST_INSTANCE=%v", init["leaked_env"])
	}
	if init["extra_env"] != "yes" {
		t.Errorf("Config.Env did not reach the server: %v", init["extra_env"])
	}
	want := map[string][]string{"whoami": {}, "get_issue": {"iid", "project"}}
	if got := s.Options(); !reflect.DeepEqual(got, want) {
		t.Errorf("Options = %v, want %v (both pages)", got, want)
	}

	res, err := s.CallTool("whoami", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || res.Text != "[auth] sign in" || string(res.Structured) != `{"ok":false}` {
		t.Errorf("CallTool = %+v", res)
	}
	if !slices.Equal(calls, []string{"whoami"}) {
		t.Errorf("OnCall saw %q", calls)
	}

	_, _, err = s.ReadResource("gitlab://x")
	var rpc *RPCError
	if !errors.As(err, &rpc) || rpc.Code != -32602 || !strings.HasPrefix(rpc.Message, "[not_found]") {
		t.Errorf("ReadResource error = %v, want an RPCError with the code", err)
	}
	if len(s.Stray()) != 0 {
		t.Errorf("Stray = %q, want none", s.Stray())
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close = %v", err)
	}
}

func TestSessionReportsStrayStdout(t *testing.T) {
	s := start(t, "stray", Config{})
	if _, err := s.Initialize("", "test"); err != nil {
		t.Fatal(err)
	}
	if got := s.Stray(); len(got) == 0 || got[0] != "hello from stdout" {
		t.Errorf("Stray = %q, want the non-JSON line", got)
	}
}

func TestSessionTimesOutAndSeesClose(t *testing.T) {
	s := start(t, "normal", Config{Timeout: 300 * time.Millisecond})
	if _, err := s.Request("hang", nil); err == nil || !strings.Contains(err.Error(), "no reply within") {
		t.Errorf("hang: err = %v, want a timeout", err)
	}
	s = start(t, "normal", Config{Timeout: 10 * time.Second})
	if _, err := s.Request("exit", nil); !errors.Is(err, ErrClosed) {
		t.Errorf("exit: err = %v, want ErrClosed", err)
	}
}

func TestEnviron(t *testing.T) {
	got := Environ([]string{"PATH=/bin", "GITLAB_MCP_READ_ONLY=true"}, "GITLAB_MCP_PROFILE=x")
	if want := []string{"PATH=/bin", "GITLAB_MCP_PROFILE=x"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Environ = %q, want %q", got, want)
	}
}

func TestEncode(t *testing.T) {
	if got := Encode(map[string]any{"a": 1}); got != `{"a":1}` {
		t.Errorf("Encode = %s", got)
	}
}

// A server that talks on its own account far more than it answers does
// not stall the reader, and replies can be awaited in any order.
func TestSessionChattyServerAndOutOfOrderReplies(t *testing.T) {
	s := start(t, "chatty", Config{Timeout: 10 * time.Second})
	if _, err := s.Initialize("", "test"); err != nil {
		t.Fatal(err)
	}
	for id := 10; id <= 11; id++ {
		if err := s.Send(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call",
			"params": map[string]any{"name": "whoami"}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int{11, 10} {
		if _, err := s.Await("tools/call", id); err != nil {
			t.Errorf("await %d: %v", id, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close = %v", err)
	}
}
