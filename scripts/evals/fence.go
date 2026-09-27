package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The fence around the model, untagged so plain `go test` holds it:
// the command line that turns every built-in tool off at the source, and
// the reader that fails a run in which a tool other than this server's
// was called anyway.

// claudeArgs is the command line, apart from the prompt's value. Kept
// in one function so the fence can be read in one place.
func claudeArgs(prompt, cfgPath, model string, budget float64) []string {
	return []string{
		"-p", prompt,
		"--output-format", "stream-json", "--verbose",
		"--mcp-config", cfgPath,
		"--strict-mcp-config",
		"--allowed-tools", "mcp__gitlab__*",
		"--tools", "",
		"--setting-sources", "",
		"--model", model,
		"--max-budget-usd", fmt.Sprintf("%.2f", budget),
	}
}

// readEvent folds one stream-json line into the run. A tool that is not
// this server's is an error: the fence did not hold. So is a tool result
// that says the server is not signed in: the run then scored the
// harness, not the model.
func readEvent(r *Run, line []byte) error {
	var ev struct {
		Type    string `json:"type"`
		Result  string `json:"result"`
		Message struct {
			Content []struct {
				Type    string          `json:"type"`
				Name    string          `json:"name"`
				Input   map[string]any  `json:"input"`
				Content json.RawMessage `json:"content"`
			} `json:"content"`
		} `json:"message"`
	}
	// A line that is not an event is the CLI's own chatter, not a call.
	if !json.Valid(line) {
		return nil
	}
	_ = json.Unmarshal(line, &ev)
	switch ev.Type {
	case "assistant":
		for _, c := range ev.Message.Content {
			if c.Type != "tool_use" {
				continue
			}
			name, ours := bareTool(c.Name)
			if !ours {
				return errors.New("the model called " + c.Name + ", which is not one of this server's tools: the fence did not hold")
			}
			r.Calls = append(r.Calls, Call{Tool: name, Args: c.Input})
		}
	case "user":
		for _, c := range ev.Message.Content {
			if c.Type == "tool_result" && strings.HasPrefix(resultText(c.Content), "[auth]") {
				return errors.New("a tool answered [auth]: the server was not signed in to the world, so the run scored nothing")
			}
		}
	case "result":
		r.Answer = ev.Result
	}
	return nil
}

// resultText is a tool result's text, sent either as a string or as
// text blocks.
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &blocks)
	var b strings.Builder
	for _, bl := range blocks {
		b.WriteString(bl.Text)
	}
	return b.String()
}
