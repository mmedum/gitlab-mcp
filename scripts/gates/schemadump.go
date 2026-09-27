package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/mmedum/gitlab-mcp/scripts/internal/mcpstdio"
)

// The tool surface as `gitlab-mcp --dump-schemas` writes it: every tool
// every flag and toolset can register, the whole mcp.Tool each, and the
// resource templates. Four gates read it — schema-diff, descriptions,
// bodies and live-cover — and they read the built artifact rather than
// the Go registry, because the dump is what a client is served.

// quickActionMeta is the _meta key a tool lists its guarded inputs
// under: the string inputs internal/tools routes through
// internal/quickaction before the handler runs (§4.2). The tools package
// writes it from the same declaration that does the routing; the bodies
// gate reads it here.
const quickActionMeta = "gitlab-mcp/quickaction"

// schemaDump is the shape --dump-schemas writes.
type schemaDump struct {
	Server            string         `json:"server"`
	Version           string         `json:"version"`
	SDK               string         `json:"sdk"`
	Tools             []dumpTool     `json:"tools"`
	ResourceTemplates []dumpTemplate `json:"resourceTemplates"`
	// whole is the dump as written, for the baseline.
	whole map[string]any
}

type dumpTool struct {
	// raw is the whole tool as dumped, fields this struct does not name
	// included, so a comparison sees every change.
	raw          map[string]any
	Name         string          `json:"name"`
	Title        string          `json:"title,omitempty"`
	Description  string          `json:"description"`
	InputSchema  *dumpSchema     `json:"inputSchema"`
	OutputSchema *dumpSchema     `json:"outputSchema,omitempty"`
	Annotations  *dumpAnnotation `json:"annotations,omitempty"`
	Meta         map[string]any  `json:"_meta,omitempty"`
}

type dumpAnnotation struct {
	ReadOnlyHint    bool  `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool `json:"destructiveHint,omitempty"`
	IdempotentHint  bool  `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool `json:"openWorldHint,omitempty"`
}

type dumpSchema struct {
	Type        any                    `json:"type,omitempty"`
	Description string                 `json:"description,omitempty"`
	Properties  map[string]*dumpSchema `json:"properties,omitempty"`
	Required    []string               `json:"required,omitempty"`
	Items       *dumpSchema            `json:"items,omitempty"`
	Enum        []any                  `json:"enum,omitempty"`
	Format      string                 `json:"format,omitempty"`
}

type dumpTemplate struct {
	raw         map[string]any
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MIMEType    string `json:"mimeType,omitempty"`
}

// types lists a schema's type, which JSON Schema spells as a string or
// an array of strings.
func (s *dumpSchema) types() []string {
	if s == nil {
		return nil
	}
	switch t := s.Type.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, v := range t {
			if str, ok := v.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

// isString reports a string input, or a list of strings: both reach
// GitLab as text somebody could have written a command into.
func (s *dumpSchema) isString() bool {
	if slices.Contains(s.types(), "string") {
		return true
	}
	return slices.Contains(s.types(), "array") && s.Items != nil && slices.Contains(s.Items.types(), "string")
}

// readOnly reports whether a tool's annotations say it only reads. A
// tool with none is treated as writing: the gates err toward asking.
func (t dumpTool) readOnly() bool { return t.Annotations != nil && t.Annotations.ReadOnlyHint }

// options is the tool's input names, sorted.
func (t dumpTool) options() []string {
	if t.InputSchema == nil {
		return nil
	}
	return slices.Sorted(maps.Keys(t.InputSchema.Properties))
}

// guardedInputs is what the tool declares routed through the guard.
func (t dumpTool) guardedInputs() ([]string, error) {
	v, ok := t.Meta[quickActionMeta]
	if !ok {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: _meta[%q] is %T, want a list of input names", t.Name, quickActionMeta, v)
	}
	var out []string
	for _, e := range list {
		s, ok := e.(string)
		if !ok {
			return nil, fmt.Errorf("%s: _meta[%q] holds %T, want input names", t.Name, quickActionMeta, e)
		}
		out = append(out, s)
	}
	return out, nil
}

func decodeDump(raw []byte, name string) (schemaDump, error) {
	var d schemaDump
	if err := json.Unmarshal(raw, &d); err != nil {
		return schemaDump{}, fmt.Errorf("%s is not a schema dump: %w", name, err)
	}
	var whole struct {
		Tools             []map[string]any `json:"tools"`
		ResourceTemplates []map[string]any `json:"resourceTemplates"`
	}
	if err := json.Unmarshal(raw, &whole); err != nil {
		return schemaDump{}, fmt.Errorf("%s is not a schema dump: %w", name, err)
	}
	if err := json.Unmarshal(raw, &d.whole); err != nil {
		return schemaDump{}, err
	}
	for i := range d.Tools {
		d.Tools[i].raw = whole.Tools[i]
	}
	for i := range d.ResourceTemplates {
		d.ResourceTemplates[i].raw = whole.ResourceTemplates[i]
	}
	return d, nil
}

func readDump(path string) (schemaDump, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // a path the Makefile names
	if err != nil {
		return schemaDump{}, err
	}
	return decodeDump(raw, path)
}

// dumpBinary runs a build of the server and decodes its surface. The
// environment carries no GITLAB_MCP_ setting from the shell, so a flag
// exported in a terminal cannot change what a baseline records; the dump
// is the full surface by construction.
func dumpBinary(binary string) (schemaDump, error) {
	cmd := exec.Command(executable(binary), "--dump-schemas") //nolint:gosec // the binary this repository built
	cmd.Env = mcpstdio.Environ(os.Environ())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		return schemaDump{}, fmt.Errorf("%s --dump-schemas: %w: %s", binary, err, strings.TrimSpace(stderr.String()))
	}
	return decodeDump(raw, binary+" --dump-schemas")
}

// executable is a binary path as the OS runs it: on Windows a path with
// no .exe gets one, because a file without it cannot be executed there
// and a path typed by hand usually lacks it.
func executable(binary string) string { return exeFor(runtime.GOOS, binary) }

func exeFor(goos, binary string) string {
	if goos == "windows" && !strings.EqualFold(filepath.Ext(binary), ".exe") {
		return binary + ".exe"
	}
	return binary
}
