package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/gitlab-mcp/internal/config"
	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/model"
	"github.com/mmedum/gitlab-mcp/internal/quickaction"
	"github.com/mmedum/gitlab-mcp/internal/render"
	"github.com/mmedum/gitlab-mcp/internal/scopes"
	"github.com/mmedum/gitlab-mcp/internal/service"
)

// Kind is what a tool does (docs/architecture.md §4.3). It is one field
// per tool rather than four, because it decides the annotations, whether
// the tool is registered at all, the scope it needs and whether a client
// is asked to put a person in the loop, and four rules kept by hand at
// sixty call sites is four ways to be quietly wrong at one of them
// (CLAUDE.md rule 14).
type Kind int

// The kinds.
const (
	// Read is every GET. Always registered.
	Read Kind = iota
	// Write changes something a person can edit back. Registered unless
	// read-only.
	Write
	// Ship is where a persuaded call becomes an effect: a merge, an
	// approval, a pipeline run. Registered only with
	// GITLAB_MCP_ENABLE_SHIP=true.
	Ship
	// Destructive deletes. Registered only with
	// GITLAB_MCP_ENABLE_DESTRUCTIVE=true, and refused per call without
	// confirm: true.
	Destructive
)

// Scope is the scopes package's view of the kind.
func (k Kind) Scope() scopes.Kind {
	switch k {
	case Write:
		return scopes.KindWrite
	case Ship:
		return scopes.KindShip
	case Destructive:
		return scopes.KindDestructive
	}
	return scopes.KindRead
}

// annotations are the hints a client shows (§8). They are hints and not
// controls: registration is the control. openWorldHint is true where the
// result is visible to other people, which every GitLab write is.
func (k Kind) annotations(idempotent bool) *mcp.ToolAnnotations {
	switch k {
	case Write:
		return &mcp.ToolAnnotations{DestructiveHint: ptr(false), IdempotentHint: idempotent, OpenWorldHint: ptr(true)}
	case Ship, Destructive:
		return &mcp.ToolAnnotations{DestructiveHint: ptr(true), IdempotentHint: idempotent, OpenWorldHint: ptr(true)}
	}
	return &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: ptr(false)}
}

// interactive reports whether a client is asked to put a person in the
// loop. A signal, not a control (§8).
func (k Kind) interactive() bool { return k == Ship || k == Destructive }

// spec is one tool's registration, minus its handler.
type spec struct {
	Name        string
	Description string
	Kind        Kind
	// Toolset is the optional toolset the tool belongs to, "" for the
	// default set.
	Toolset string
	// Idempotent marks a write that lands the same way twice.
	Idempotent bool
	// Bucket is the rate bucket the tool's requests are charged to
	// beyond the general one, for the log line.
	Bucket gapi.Bucket
	// Enums closes a string input's values.
	Enums map[string][]string
	// Guarded are the inputs, by JSON name, that carry Markdown GitLab
	// runs quick actions from. register routes each through
	// internal/quickaction before the handler sees it and declares them
	// under _meta, which `scripts/gates bodies` reads (§4.2).
	Guarded []string
}

// quickActionMeta is the _meta key the guarded inputs are declared under.
const quickActionMeta = "gitlab-mcp/quickaction"

// Deps are what the tools need.
type Deps struct {
	Service *service.Service
	Config  config.Config
	// Granted are the token's scopes, empty when unknown. A tool whose kind
	// needs a scope the token lacks is not registered: a read_api token
	// never gets a write tool, whatever the flags say (§9.4).
	Granted []string
	Logger  *slog.Logger
}

func (d Deps) logger() *slog.Logger {
	if d.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return d.Logger
}

// gate says why a tool is not registered, "" when it is.
func gate(sp spec, cfg config.Config, granted []string) string {
	switch sp.Kind {
	case Write:
		if cfg.ReadOnly {
			return "read-only"
		}
	case Ship:
		if cfg.ReadOnly || !cfg.EnableShip {
			return "ship"
		}
	case Destructive:
		if cfg.ReadOnly || !cfg.EnableDestructive {
			return "destructive"
		}
	}
	if sp.Toolset != "" && !cfg.ToolsetEnabled(sp.Toolset) {
		return "toolset"
	}
	if len(granted) > 0 && !scopes.Satisfied(granted, scopes.Required(sp.Kind.Scope())) {
		return "scope"
	}
	return ""
}

// definition is a tool whose input and output types are fixed.
type definition interface {
	spec() spec
	add(s *mcp.Server, d Deps)
}

// tool binds a spec to its handler and its text rendering. Out is the
// structured half; text renders the readable half from the same value
// under the call's boundary (CLAUDE.md rule 15).
type tool[In, Out any] struct {
	sp   spec
	run  func(ctx context.Context, svc *service.Service, in In) (Out, error)
	text func(out Out, b render.Boundary) string
}

func (t tool[In, Out]) spec() spec { return t.sp }

// add registers the tool with the SDK's untyped AddTool, so the
// arguments pass through lenient decoding (§17.7) before the schema
// sees them, and so errors, logging and both halves of the reply are
// decided here once.
func (t tool[In, Out]) add(s *mcp.Server, d Deps) {
	in := inputSchema[In](t.sp)
	inResolved, err := in.Resolve(nil)
	if err != nil {
		panic("tools: " + t.sp.Name + ": input schema: " + err.Error())
	}
	out := outputSchema[Out]()
	outResolved, err := out.Resolve(nil)
	if err != nil {
		panic("tools: " + t.sp.Name + ": output schema: " + err.Error())
	}
	mt := &mcp.Tool{
		Name:         t.sp.Name,
		Description:  t.sp.Description,
		Annotations:  t.sp.Kind.annotations(t.sp.Idempotent),
		InputSchema:  in,
		OutputSchema: out,
	}
	if t.sp.Kind.interactive() {
		mt.Meta = mcp.Meta{"anthropic/requiresUserInteraction": true}
	}
	c := &caller[In, Out]{t: t, d: d, in: in, inResolved: inResolved, outResolved: outResolved,
		enums: enumsOf(t.sp.Enums), dryRun: boolField[In]("dry_run"), confirm: boolField[In]("confirm"),
		escape: boolField[In]("escape_commands"), guarded: stringFields[In](t.sp.Name, t.sp.Guarded), write: writeField[Out]()}
	if t.sp.Kind == Destructive && c.confirm < 0 {
		panic("tools: " + t.sp.Name + " is Destructive and has no confirm input")
	}
	if t.sp.Kind != Read && c.dryRun < 0 {
		// §8: every tool that changes something can show what it would
		// send first.
		panic("tools: " + t.sp.Name + " writes and has no dry_run input")
	}
	if t.sp.Kind != Read && c.write < 0 {
		panic("tools: " + t.sp.Name + " writes and its result does not carry model.Write")
	}
	if len(t.sp.Guarded) > 0 {
		if t.sp.Kind == Read {
			panic("tools: " + t.sp.Name + " is a read and declares guarded inputs")
		}
		if c.escape < 0 {
			panic("tools: " + t.sp.Name + " guards inputs and has no escape_commands input")
		}
		if mt.Meta == nil {
			mt.Meta = mcp.Meta{}
		}
		mt.Meta[quickActionMeta] = slices.Clone(t.sp.Guarded)
	}
	s.AddTool(mt, c.handle)
}

// caller is one registered tool's call path.
type caller[In, Out any] struct {
	t           tool[In, Out]
	d           Deps
	in          *jsonschema.Schema
	inResolved  *jsonschema.Resolved
	outResolved *jsonschema.Resolved
	enums       []enum
	dryRun      int
	confirm     int
	escape      int
	guarded     []guardedField
	write       int // model.Write's field in Out, -1 for none
}

// handle is every call: decode, gate the call itself, run, render, log.
func (c *caller[In, Out]) handle(ctx context.Context, req *mcp.CallToolRequest) (res *mcp.CallToolResult, err error) {
	start := time.Now()
	ctx = gapi.WithCall(ctx)
	outcome := "ok"
	defer func() {
		// One line per call, and nothing the caller sent or GitLab
		// returned: the method, the tool, how it ended, how long it took
		// and the rate bucket it was charged to (§9.2).
		c.d.logger().Debug("tool call", "method", "tools/call", "tool", c.t.sp.Name, "outcome", outcome,
			"ms", time.Since(start).Milliseconds(), "bucket", c.t.sp.Bucket.String())
	}()
	defer func() {
		if r := recover(); r != nil {
			// The value may carry the payload, so it is not logged or
			// shown; the class says a defect was hit.
			outcome = string(gapi.ClassUnexpected)
			res, err = errorResult(gapi.Errf(gapi.ClassUnexpected, "the server failed while handling %s; this is a defect, please report it", c.t.sp.Name)), nil
		}
	}()

	var raw json.RawMessage
	if req != nil && req.Params != nil {
		raw = req.Params.Arguments
	}
	in, err := c.decode(raw)
	if err != nil {
		outcome = classOf(err)
		return errorResult(err), nil
	}
	v := reflect.ValueOf(&in).Elem()
	dry := c.dryRun >= 0 && v.Field(c.dryRun).Bool()
	if dry {
		ctx = gapi.WithDryRun(ctx)
	}
	if c.t.sp.Kind == Destructive && !dry && !v.Field(c.confirm).Bool() {
		err := refuse(c.t.sp.Name+" deletes something GitLab cannot restore, so every call has to say so", "confirm: true")
		outcome = classOf(err)
		return errorResult(err), nil
	}

	escaped, err := c.guard(v)
	if err != nil {
		outcome = classOf(err)
		return errorResult(err), nil
	}

	out, err := c.t.run(ctx, c.d.Service, in)
	if err != nil {
		outcome = classOf(err)
		return errorResult(err), nil
	}
	c.finishWrite(&out, escaped)
	structured, err := c.structured(out)
	if err != nil {
		outcome = classOf(err)
		return errorResult(err), nil
	}
	// Content is set, so the SDK does not fill it with the JSON a second
	// time: the readable half is a rendering, never the same bytes.
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: c.t.text(out, render.NewBoundary())}},
		StructuredContent: structured,
	}, nil
}

// decode reads the arguments leniently, checks closed values and the
// schema, and decodes them into In.
func (c *caller[In, Out]) decode(raw json.RawMessage) (In, error) {
	var in In
	args, adjusted, err := lenient(raw, c.in)
	if err != nil {
		return in, err
	}
	if len(adjusted) > 0 {
		// Only property names from this server's own schema are logged,
		// never a value.
		c.d.logger().Debug("lenient arguments", "tool", c.t.sp.Name, "fields", adjusted)
	}
	if err := checkEnums(args, c.enums); err != nil {
		return in, err
	}
	data, err := json.Marshal(args)
	if err != nil {
		return in, gapi.Errf(gapi.ClassInvalid, "the arguments could not be read as JSON")
	}
	var generic map[string]any
	if err := json.Unmarshal(data, &generic); err != nil {
		return in, gapi.Errf(gapi.ClassInvalid, "the arguments could not be read as JSON")
	}
	if err := c.inResolved.Validate(generic); err != nil {
		return in, gapi.Errf(gapi.ClassInvalid, "the arguments do not match %s's input schema: %s", c.t.sp.Name, err)
	}
	if err := json.Unmarshal(data, &in); err != nil {
		return in, gapi.Errf(gapi.ClassInvalid, "the arguments do not match %s's input schema: %s", c.t.sp.Name, err)
	}
	return in, nil
}

// structured marshals the output and holds it to the declared schema, so
// a result never promises a shape it does not have.
func (c *caller[In, Out]) structured(out Out) (json.RawMessage, error) {
	data, err := json.Marshal(out)
	if err != nil {
		return nil, gapi.Errf(gapi.ClassUnexpected, "the result of %s could not be encoded", c.t.sp.Name)
	}
	var generic any
	if err := json.Unmarshal(data, &generic); err != nil {
		return nil, gapi.Errf(gapi.ClassUnexpected, "the result of %s could not be encoded", c.t.sp.Name)
	}
	if err := c.outResolved.Validate(generic); err != nil {
		return nil, gapi.Errf(gapi.ClassUnexpected, "the result of %s does not match its output schema: %s", c.t.sp.Name, err)
	}
	return data, nil
}

// ------------------------------------------------------------ guarding

// guardedField is one input routed through the quick-action guard.
type guardedField struct {
	name  string
	index int
}

// stringFields finds the declared inputs among In's string fields, a
// *string included. A name that is not one is a programming error.
func stringFields[In any](tool string, names []string) []guardedField {
	isString := func(t reflect.Type) bool {
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		return t.Kind() == reflect.String
	}
	out := make([]guardedField, 0, len(names))
	for _, name := range names {
		i := fieldByJSON[In](name, isString)
		if i < 0 {
			panic(fmt.Sprintf("tools: %s declares %q guarded and has no string input by that name", tool, name))
		}
		out = append(out, guardedField{name: name, index: i})
	}
	return out
}

// guard puts every guarded input through internal/quickaction: refused
// when it holds a line GitLab would run, unless escape_commands is set,
// in which case each such line is escaped in place (§4.2). The handler
// only ever sees text GitLab will not run.
func (c *caller[In, Out]) guard(v reflect.Value) ([]model.EscapedLine, error) {
	escape := c.escape >= 0 && v.Field(c.escape).Bool()
	escaped := []model.EscapedLine{}
	for _, g := range c.guarded {
		f := v.Field(g.index)
		if f.Kind() == reflect.Pointer {
			if f.IsNil() {
				continue
			}
			f = f.Elem()
		}
		if f.String() == "" {
			continue
		}
		if !escape {
			if _, err := quickaction.Check(f.String(), false); err != nil {
				var be *quickaction.BlockedError
				if errors.As(err, &be) {
					return nil, gapi.Errf(gapi.ClassBlocked, "%s: %s", g.name, be.Message())
				}
				return nil, err
			}
			continue
		}
		body, lines := quickaction.Escape(f.String())
		f.SetString(body)
		for _, l := range lines {
			escaped = append(escaped, model.EscapedLine{Input: g.name, Line: l.Number, Command: l.Command})
		}
	}
	return escaped, nil
}

// writeField is the index of Out's model.Write, or -1 for a result
// that carries none, as a read's does.
func writeField[Out any]() int {
	t := reflect.TypeFor[Out]()
	if t.Kind() != reflect.Struct {
		return -1
	}
	f, ok := t.FieldByName("Write")
	if !ok || len(f.Index) != 1 || f.Type != reflect.TypeFor[model.Write]() {
		return -1
	}
	return f.Index[0]
}

// finishWrite fills a write result's shared half: the lines the guard
// escaped, and empty lists rather than null, as the output schema wants.
func (c *caller[In, Out]) finishWrite(out *Out, escaped []model.EscapedLine) {
	if c.write < 0 {
		return
	}
	w := reflect.ValueOf(out).Elem().Field(c.write).Addr().Interface().(*model.Write)
	w.EscapedCommands = escaped
	if w.Notes == nil {
		w.Notes = []string{}
	}
}

// ------------------------------------------------------------ decoding

// lenient applies §17.7's proposal to the raw arguments: an array or an
// integer sent as JSON inside a string is decoded, and null or "" for an
// optional input is taken as absent. Some clients stringify arguments;
// refusing them costs a turn and teaches nothing. Nothing else is
// coerced, and it returns the names of the inputs it adjusted so the
// call can log that it happened.
func lenient(raw json.RawMessage, schema *jsonschema.Schema) (map[string]any, []string, error) {
	args := map[string]any{}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed != "" && trimmed != "null" {
		dec := json.NewDecoder(strings.NewReader(trimmed))
		dec.UseNumber()
		if err := dec.Decode(&args); err != nil {
			return nil, nil, gapi.Errf(gapi.ClassInvalid, "the arguments are not a JSON object")
		}
	}
	var adjusted []string
	for _, name := range slices.Sorted(maps.Keys(args)) {
		prop := schema.Properties[name]
		if prop == nil {
			continue // the schema refuses it, naming it
		}
		value := args[name]
		optional := !slices.Contains(schema.Required, name)
		if s, isString := value.(string); optional && (value == nil || (isString && s == "")) {
			delete(args, name)
			adjusted = append(adjusted, name)
			continue
		}
		s, isString := value.(string)
		if !isString {
			continue
		}
		if allows(prop, "string") {
			continue // a string is already what it takes
		}
		s = strings.TrimSpace(s)
		switch {
		case allows(prop, "array") && strings.HasPrefix(s, "["):
			var arr []any
			if json.Unmarshal([]byte(s), &arr) == nil {
				args[name] = arr
				adjusted = append(adjusted, name)
			}
		case allows(prop, "integer"):
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				args[name] = n
				adjusted = append(adjusted, name)
			}
		}
	}
	return args, adjusted, nil
}

func allows(s *jsonschema.Schema, typ string) bool {
	return s.Type == typ || slices.Contains(s.Types, typ)
}

// enum is one closed input, its values sorted.
type enum struct {
	name   string
	values []string
}

// enumsOf sorts a spec's closed inputs once, by name and values, so a
// call checks them in a fixed order and names each set sorted.
func enumsOf(m map[string][]string) []enum {
	out := make([]enum, 0, len(m))
	for _, name := range slices.Sorted(maps.Keys(m)) {
		out = append(out, enum{name: name, values: slices.Sorted(slices.Values(m[name]))})
	}
	return out
}

// checkEnums refuses a value outside a closed set, naming the set
// sorted, so the caller can correct itself in one turn.
func checkEnums(args map[string]any, enums []enum) error {
	for _, e := range enums {
		v, ok := args[e.name]
		if !ok {
			continue
		}
		if s, isString := v.(string); !isString || !slices.Contains(e.values, s) {
			return gapi.Errf(gapi.ClassInvalid, "%s must be one of %s", e.name, strings.Join(e.values, "|"))
		}
	}
	return nil
}

// ------------------------------------------------------------- schemas

// timeSchema makes a time.Time a date-time string rather than a plain
// one: the format is what tells a client the string is an instant, and
// the released surface carries it.
var timeSchema = &jsonschema.ForOptions{
	TypeSchemas: map[reflect.Type]*jsonschema.Schema{
		reflect.TypeFor[time.Time](): {Type: "string", Format: "date-time"},
		reflect.TypeFor[idOrPath]():  {Types: []string{"integer", "string"}},
	},
}

// outputSchema builds a tool's output schema. It panics on failure,
// which only a type declared in this server can cause.
func outputSchema[T any]() *jsonschema.Schema {
	s, err := jsonschema.For[T](timeSchema)
	if err != nil {
		panic("tools: output schema for " + reflect.TypeFor[T]().String() + ": " + err.Error())
	}
	return s
}

// inputSchema builds a tool's input schema and applies the rules every
// input with a given name shares, so they cannot differ between tools:
// max is 1 to 100, an integer iid, line or any integer *_id is
// positive, offsets are not negative, and a time filter is a date-time.
func inputSchema[T any](sp spec) *jsonschema.Schema {
	s, err := jsonschema.For[T](timeSchema)
	if err != nil {
		panic("tools: input schema for " + sp.Name + ": " + err.Error())
	}
	for name, p := range s.Properties {
		switch {
		case name == "max":
			p.Minimum, p.Maximum = ptr(1.0), ptr(float64(gapi.MaxPerPage))
		case allows(p, "integer") && (name == "iid" || strings.HasSuffix(name, "_id") || name == "line" || name == "end_line"):
			p.Minimum = ptr(1.0)
		case name == "offset" || strings.HasSuffix(name, "_offset"):
			p.Minimum = ptr(0.0)
		case strings.HasSuffix(name, "_after") || strings.HasSuffix(name, "_before") || name == "since" || name == "until":
			p.Format = "date-time"
		}
		if values, ok := sp.Enums[name]; ok {
			sorted := slices.Sorted(slices.Values(values))
			p.Enum = make([]any, len(sorted))
			for i, v := range sorted {
				p.Enum[i] = v
			}
		}
	}
	for name := range sp.Enums {
		if s.Properties[name] == nil {
			panic(fmt.Sprintf("tools: %s declares an enum for %q, which is not an input", sp.Name, name))
		}
	}
	return s
}

// boolField finds a bool input by its JSON name, or -1. dry_run and
// confirm are found this way rather than declared by each tool, so a
// tool that offers either cannot also have to remember to honor it.
func boolField[In any](name string) int {
	return fieldByJSON[In](name, func(t reflect.Type) bool { return t.Kind() == reflect.Bool })
}

// fieldByJSON finds a top-level field of In by its JSON name whose type
// ok accepts, or -1.
func fieldByJSON[In any](name string, ok func(reflect.Type) bool) int {
	t := reflect.TypeFor[In]()
	if t.Kind() != reflect.Struct {
		return -1
	}
	for i := range t.NumField() {
		tag, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if tag == name && ok(t.Field(i).Type) {
			return i
		}
	}
	return -1
}

func ptr[T any](v T) *T { return &v }

// idOrPath is a project or group as the caller names it: a numeric id,
// sent as a JSON number or a string, or a full path or URL. Its schema
// says both, because "the numeric id" invites a number (§6.1).
type idOrPath string

// UnmarshalJSON accepts a string, or an integer as its decimal text.
func (v *idOrPath) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*v = idOrPath(s)
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("want a numeric id or a path")
	}
	*v = idOrPath(strconv.FormatInt(n, 10))
	return nil
}
