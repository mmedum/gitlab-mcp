package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/mmedum/gitlab-mcp/scripts/internal/gitx"
	"github.com/mmedum/gitlab-mcp/scripts/internal/mcpstdio"
)

// The released tool surface is a contract (CLAUDE.md rule 17). A client
// written against the last release breaks when a tool or resource
// template goes, when an input it sends or an output field it reads
// goes, or when an input it never sent becomes required. Adding is fine.
//
// schema-diff compares the built binary with the last tag's, built in a
// git worktree that is always removed; before the first tag it compares
// with testdata/schema-baseline.json, which `make schema-baseline`
// records on purpose. schema-ack is the pull-request half: any change at
// all needs a commit that says so.

const (
	baselineFile = "testdata/schema-baseline.json"
	// surfaceFloor is the fewest tools a real surface carries: the
	// thirty-one of the committed baseline, recorded at phase 1 and
	// frozen again in phase 4. The default configuration, which the
	// smoke gate sees, has forty-three, and the dump sixty-six.
	surfaceFloor = 31
)

// surfaceChanges is what moved between two dumps.
type surfaceChanges struct {
	Breaking []string
	Additive []string
	// Other is anything else in the surface that differs: a description,
	// an annotation, a _meta key. Not breaking, and still a change a
	// pull request acknowledges.
	Other []string
}

func (c surfaceChanges) empty() bool {
	return len(c.Breaking)+len(c.Additive)+len(c.Other) == 0
}

func (c surfaceChanges) write(out io.Writer) {
	for _, s := range c.Breaking {
		_, _ = fmt.Fprintln(out, "BREAKING  "+s)
	}
	for _, s := range c.Additive {
		_, _ = fmt.Fprintln(out, "added     "+s)
	}
	for _, s := range c.Other {
		_, _ = fmt.Fprintln(out, "changed   "+s)
	}
}

// compareSurfaces is the rule over two dumps, so it can be tested
// without building anything.
func compareSurfaces(prev, cur schemaDump) surfaceChanges {
	var c surfaceChanges
	oldTools := map[string]dumpTool{}
	for _, t := range prev.Tools {
		oldTools[t.Name] = t
	}
	newTools := map[string]dumpTool{}
	for _, t := range cur.Tools {
		newTools[t.Name] = t
	}
	for _, name := range slices.Sorted(maps.Keys(oldTools)) {
		was := oldTools[name]
		now, ok := newTools[name]
		if !ok {
			c.Breaking = append(c.Breaking, "tool removed: "+name)
			continue
		}
		compareTool(&c, name, was, now)
	}
	for _, name := range slices.Sorted(maps.Keys(newTools)) {
		if _, ok := oldTools[name]; !ok {
			c.Additive = append(c.Additive, "tool added: "+name)
		}
	}
	oldT := map[string]dumpTemplate{}
	for _, t := range prev.ResourceTemplates {
		oldT[t.URITemplate] = t
	}
	newT := map[string]dumpTemplate{}
	for _, t := range cur.ResourceTemplates {
		newT[t.URITemplate] = t
	}
	for _, u := range slices.Sorted(maps.Keys(oldT)) {
		now, ok := newT[u]
		switch {
		case !ok:
			c.Breaking = append(c.Breaking, "resource template removed: "+u)
		case canonical(now.raw) != canonical(oldT[u].raw):
			c.Other = append(c.Other, "resource template "+u+": name, description or media type")
		}
	}
	for _, u := range slices.Sorted(maps.Keys(newT)) {
		if _, ok := oldT[u]; !ok {
			c.Additive = append(c.Additive, "resource template added: "+u)
		}
	}
	return c
}

// compareTool adds what changed in one tool that both surfaces have.
func compareTool(c *surfaceChanges, name string, was, now dumpTool) {
	wasIn, nowIn := schemaFields(was.InputSchema), schemaFields(now.InputSchema)
	for _, f := range slices.Sorted(maps.Keys(wasIn)) {
		if _, ok := nowIn[f]; !ok {
			c.Breaking = append(c.Breaking, name+": input removed: "+f)
		}
	}
	for _, f := range slices.Sorted(maps.Keys(nowIn)) {
		if _, ok := wasIn[f]; !ok {
			c.Additive = append(c.Additive, name+": input added: "+f)
		}
	}
	wasReq, nowReq := requiredOf(was.InputSchema), requiredOf(now.InputSchema)
	for _, f := range slices.Sorted(maps.Keys(nowReq)) {
		if !wasReq[f] {
			c.Breaking = append(c.Breaking, name+": input now required: "+f)
		}
	}
	wasOut, nowOut := schemaFields(was.OutputSchema), schemaFields(now.OutputSchema)
	for _, f := range slices.Sorted(maps.Keys(wasOut)) {
		if _, ok := nowOut[f]; !ok {
			c.Breaking = append(c.Breaking, name+": output field removed: "+f)
		}
	}
	for _, f := range slices.Sorted(maps.Keys(nowOut)) {
		if _, ok := wasOut[f]; !ok {
			c.Additive = append(c.Additive, name+": output field added: "+f)
		}
	}
	if canonical(was.raw) != canonical(now.raw) && !touched(*c, name) {
		c.Other = append(c.Other, name+": description, schema detail, annotations or _meta")
	}
}

// touched reports whether a tool already has a breaking or additive line,
// so its other differences are not reported a second time.
func touched(c surfaceChanges, name string) bool {
	for _, s := range slices.Concat(c.Breaking, c.Additive) {
		if strings.HasPrefix(s, name+": ") {
			return true
		}
	}
	return false
}

// canonical is the whole dumped object as sorted JSON.
func canonical(raw map[string]any) string {
	out, _ := json.Marshal(raw)
	return string(out)
}

// schemaFields is every property of a schema as a dotted path, nested
// objects and the elements of arrays included.
func schemaFields(s *dumpSchema) map[string]bool {
	out := map[string]bool{}
	var walk func(*dumpSchema, string, int)
	walk = func(s *dumpSchema, prefix string, depth int) {
		if s == nil || depth > 8 {
			return
		}
		if s.Items != nil {
			walk(s.Items, prefix, depth+1)
		}
		for name, p := range s.Properties {
			out[prefix+name] = true
			walk(p, prefix+name+".", depth+1)
		}
	}
	walk(s, "", 0)
	return out
}

func requiredOf(s *dumpSchema) map[string]bool {
	out := map[string]bool{}
	if s != nil {
		for _, r := range s.Required {
			out[r] = true
		}
	}
	return out
}

// ------------------------------------------------------------ schema-diff

func schemaDiff(out io.Writer, args []string) error {
	cur, err := dumpBinary(args[0])
	if err != nil {
		return err
	}
	if len(cur.Tools) < surfaceFloor {
		return fmt.Errorf("the dump carries %d tools and the floor is %d; it is not the whole surface", len(cur.Tools), surfaceFloor)
	}
	root, err := gitx.Root(".")
	if err != nil {
		return err
	}
	var prev schemaDump
	against := ""
	tag, err := gitx.LatestTag(root)
	switch {
	case errors.Is(err, gitx.ErrNoTag):
		prev, err = readDump(filepath.Join(root, baselineFile))
		if err != nil {
			return fmt.Errorf("no tag yet and no baseline: %w; `make schema-baseline` records one", err)
		}
		against = baselineFile
	case err != nil:
		return err
	default:
		prev, err = dumpAt(root, tag)
		if err != nil {
			return err
		}
		against = tag
	}
	c := compareSurfaces(prev, cur)
	c.write(out)
	if len(c.Breaking) > 0 {
		return fmt.Errorf("%d breaking change(s) to the tool surface since %s; a release that makes them says **Breaking:** in the CHANGELOG and bumps the major version",
			len(c.Breaking), against)
	}
	_, _ = fmt.Fprintf(out, "schema diff ok against %s: %d tools, %d resource templates; %d additive, %d other change(s)\n",
		against, len(cur.Tools), len(cur.ResourceTemplates), len(c.Additive), len(c.Other))
	return nil
}

// dumpAt builds the server at a revision in a scratch worktree and dumps
// it. The worktree is removed on every path out, a failed build included,
// and pruned so no stale registration survives a killed run.
func dumpAt(root, rev string) (schemaDump, error) {
	tmp, err := os.MkdirTemp("", "gates-schema-*")
	if err != nil {
		return schemaDump{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	src := filepath.Join(tmp, "src")
	if _, err := gitx.Output(root, "worktree", "add", "--quiet", "--detach", src, rev); err != nil {
		return schemaDump{}, err
	}
	defer func() {
		_, _ = gitx.Output(root, "worktree", "remove", "--force", src)
		_, _ = gitx.Output(root, "worktree", "prune")
	}()
	bin := executable(filepath.Join(tmp, "gitlab-mcp"))
	build := exec.Command("go", "build", "-o", bin, "./cmd/gitlab-mcp") //nolint:gosec // fixed arguments
	build.Dir = src
	build.Env = mcpstdio.Environ(os.Environ())
	if out, err := build.CombinedOutput(); err != nil {
		return schemaDump{}, fmt.Errorf("build %s: %w: %s", rev, err, strings.TrimSpace(string(out)))
	}
	return dumpBinary(bin)
}

// ------------------------------------------------------------ baseline

// schemaBaseline records the current surface as the baseline. It is
// manual because recording accepts every change in it.
func schemaBaseline(out io.Writer, args []string) error {
	cur, err := dumpBinary(args[0])
	if err != nil {
		return err
	}
	if len(cur.Tools) < surfaceFloor {
		return fmt.Errorf("the dump carries %d tools and the floor is %d; refusing to record it", len(cur.Tools), surfaceFloor)
	}
	data, err := encodeBaseline(cur.whole)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(baselineFile, data); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "%s records %d tools and %d resource templates\n", baselineFile, len(cur.Tools), len(cur.ResourceTemplates))
	return nil
}

// encodeBaseline writes the whole dump, less the build's own version,
// which changes on every commit and is not part of the surface.
func encodeBaseline(whole map[string]any) ([]byte, error) {
	delete(whole, "version")
	raw, err := json.MarshalIndent(whole, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// ------------------------------------------------------------ schema-ack

// ackFooter is a line of a commit message that acknowledges a change to
// the surface: SCHEMA-CHANGE: for an additive one, BREAKING CHANGE: for
// any.
var ackFooter = regexp.MustCompile(`(?m)^(SCHEMA-CHANGE|BREAKING CHANGE):\s*\S`)

// schemaAck holds a pull request: when its surface differs from its
// merge-base's, some commit it adds says so.
func schemaAck(out io.Writer, args []string) error {
	bin, baseRef, head := args[0], args[1], args[2]
	root, err := gitx.Root(".")
	if err != nil {
		return err
	}
	base, err := gitx.MergeBase(root, baseRef, head)
	if err != nil {
		return err
	}
	prev, err := dumpAt(root, base)
	if err != nil {
		return err
	}
	cur, err := dumpBinary(bin)
	if err != nil {
		return err
	}
	msgs, err := gitx.Messages(root, base, head)
	if err != nil {
		return err
	}
	return checkAck(out, compareSurfaces(prev, cur), msgs, base)
}

func checkAck(out io.Writer, c surfaceChanges, messages []string, base string) error {
	if c.empty() {
		_, _ = fmt.Fprintf(out, "schema ack ok: the tool surface is unchanged since %s\n", short(base))
		return nil
	}
	c.write(out)
	var schema, breaking bool
	for _, m := range messages {
		for _, f := range ackFooter.FindAllStringSubmatch(m, -1) {
			if f[1] == "BREAKING CHANGE" {
				breaking = true
			}
			schema = true
		}
	}
	switch {
	case len(c.Breaking) > 0 && !breaking:
		return fmt.Errorf("the pull request breaks the tool surface and no commit since %s says BREAKING CHANGE:; add an empty commit "+
			"(git commit --allow-empty) whose message carries it, never an amend", short(base))
	case !schema:
		return fmt.Errorf("the pull request changes the tool surface and no commit since %s says SCHEMA-CHANGE: or BREAKING CHANGE:; "+
			"add an empty commit (git commit --allow-empty) whose message carries it, never an amend", short(base))
	}
	_, _ = fmt.Fprintf(out, "schema ack ok: the change since %s is acknowledged\n", short(base))
	return nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
