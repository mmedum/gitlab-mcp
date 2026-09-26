// Command smokefake is a stand-in server for the smoke gate's own tests:
// a correct one by default, and in each SMOKE_FAKE mode broken in
// exactly one of the ways the gate exists to catch.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

func main() {
	mode := os.Getenv("SMOKE_FAKE")
	readOnly := os.Getenv("GITLAB_MCP_READ_ONLY") == "true"
	if mode == "stray" {
		fmt.Println("starting up")
	}
	in := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	var mu sync.Mutex
	pending := 0
	reply := func(id any, result any) {
		mu.Lock()
		defer mu.Unlock()
		_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	for in.Scan() {
		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &req) != nil || req.ID == nil {
			continue
		}
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if mode == "oldproto" {
				p.ProtocolVersion = "2024-11-05"
			}
			reply(req.ID, map[string]any{"protocolVersion": p.ProtocolVersion, "capabilities": map[string]any{},
				"serverInfo": map[string]any{"name": "gitlab-mcp", "version": "0"}})
		case "tools/list":
			read := map[string]any{"readOnlyHint": true}
			tools := []any{
				map[string]any{"name": "get_me", "annotations": read},
				map[string]any{"name": "get_issue", "annotations": read},
			}
			if mode == "dotted" {
				tools = append(tools, map[string]any{"name": "gitlab.search", "annotations": read})
			}
			if !readOnly || mode == "rowrites" {
				tools = append(tools, map[string]any{"name": "create_issue", "annotations": map[string]any{"destructiveHint": false}})
			}
			reply(req.ID, map[string]any{"tools": tools})
		case "tools/call":
			result := map[string]any{"isError": mode != "noauth",
				"content": []any{map[string]any{"type": "text", "text": "[auth] not signed in: run login"}}}
			if mode != "inflight" {
				reply(req.ID, result)
				continue
			}
			// Answered a moment later, so a client that hangs up at
			// once leaves it in flight.
			mu.Lock()
			pending++
			mu.Unlock()
			go func(id any) {
				time.Sleep(300 * time.Millisecond)
				reply(id, result)
				mu.Lock()
				pending--
				mu.Unlock()
			}(req.ID)
		case "resources/templates/list":
			uri := "gitlab://projects/{id}/issues/{iid}"
			if mode == "plus" {
				uri = "gitlab://projects/{id}/files/{+path}"
			}
			reply(req.ID, map[string]any{"resourceTemplates": []any{map[string]any{"uriTemplate": uri, "name": "issue"}}})
		case "resources/read":
			msg := "[auth] not signed in: run login"
			if mode == "readopen" {
				msg = "[not_found] no such issue"
			}
			mu.Lock()
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32602, "message": msg}})
			mu.Unlock()
		}
	}
	mu.Lock()
	inFlight := pending > 0
	mu.Unlock()
	// The defect the standard's §11 names: a disconnect with a request
	// in flight reported as a failure rather than the end of a session.
	if mode == "crash" || (mode == "inflight" && inFlight) {
		fmt.Fprintln(os.Stderr, "connection closed: EOF")
		os.Exit(1)
	}
}
