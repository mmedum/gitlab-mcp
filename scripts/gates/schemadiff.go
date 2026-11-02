package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/mmedum/gitlab-mcp/v4/scripts/internal/gitx"
	"github.com/mmedum/gitlab-mcp/v4/scripts/internal/mcpstdio"
)

// The released tool surface is a contract (CLAUDE.md rule 17). A client
// written against the last release breaks when a tool, resource template,
// resource or prompt goes, when an input it sends or an output field it
// reads goes, or when an input it never sent becomes required. Adding is
// fine.
//
// schema-diff compares the built binary with the last tag's, built in a
// git worktree that is always removed; before the first tag it compares
// with testdata/schema-baseline.json, which `make schema-baseline`
// records on purpose. schema-ack is the pull-request half: any change at
// all needs a commit footer that names each item it changes.

const (
	baselineFile = "testdata/schema-baseline.json"
	// surfaceFloor is the fewest tools a real surface carries: the
	// sixty-six of the committed baseline, frozen in phase 4 with every
	// flag and toolset on.
	surfaceFloor = 66
)

// A change is one difference between two dumps: the item it is to, which
// a footer or a CHANGELOG entry names, and how the report shows it. The
// item is a tool's name, a resource template's URI template, a list
// item's id, or a top-level key such as instructions. field is, on the
// break of a tool still there, the part it breaks, such as "input iid",
// so a break is matched across two comparisons by item and field, never
// by its text.
type change struct {
	item, text, field string
}

// surfaceChanges is what moved between two dumps.
type surfaceChanges struct {
	breaking, added []change
	// changed is anything else in the surface that differs: a
	// description, an annotation, a _meta key, the instructions. Not
	// breaking, and still a change a pull request acknowledges.
	changed []change
}

func (c surfaceChanges) empty() bool {
	return len(c.breaking)+len(c.added)+len(c.changed) == 0
}

func (c surfaceChanges) write(out io.Writer) {
	for _, s := range c.breaking {
		_, _ = fmt.Fprintln(out, "BREAKING  "+s.text)
	}
	for _, s := range c.added {
		_, _ = fmt.Fprintln(out, "added     "+s.text)
	}
	for _, s := range c.changed {
		_, _ = fmt.Fprintln(out, "changed   "+s.text)
	}
}

// compareSurfaces is the rule over two dumps, so it can be tested
// without building anything. Every item of every list is compared whole,
// and so is every other key: an item gone breaks a caller, and so does a
// tool's input or output field gone or an input newly required. A tool
// that broke also lists every other change to it.
func compareSurfaces(prev, cur schemaDump) surfaceChanges {
	var c surfaceChanges
	oldTools, newTools := toolsByName(prev), toolsByName(cur)
	byListThenID := func(a, b itemKey) int { return cmp.Or(strings.Compare(a.list, b.list), strings.Compare(a.id, b.id)) }
	for _, k := range sortedUnion(prev.items, cur.items, byListThenID) {
		was, inPrev := prev.items[k]
		now, inCur := cur.items[k]
		switch {
		case !inCur:
			c.breaking = append(c.breaking, change{item: k.id, text: k.word() + " removed: " + k.id})
		case !inPrev:
			c.added = append(c.added, change{item: k.id, text: k.word() + " added: " + k.id})
		default:
			if k.list == "tools" {
				c.breaking = append(c.breaking, toolBreaks(oldTools[k.id], newTools[k.id])...)
			}
			if parts := changedParts(was, now); len(parts) > 0 {
				c.changed = append(c.changed, change{item: k.id, text: k.word() + " " + k.id + ": " + strings.Join(parts, ", ")})
			}
		}
	}
	// A key other than a list is its own item, named first as a footer
	// names it: "instructions: default, read_only".
	for _, part := range changedParts(prev.rest, cur.rest) {
		top, sub, _ := strings.Cut(part, ".")
		if n := len(c.changed); n > 0 && c.changed[n-1].item == top {
			c.changed[n-1].text += ", " + sub
			continue
		}
		text := top
		if sub != "" {
			text += ": " + sub
		}
		c.changed = append(c.changed, change{item: top, text: text})
	}
	return c
}

func toolsByName(d schemaDump) map[string]dumpTool {
	out := make(map[string]dumpTool, len(d.Tools))
	for _, t := range d.Tools {
		out[t.Name] = t
	}
	return out
}

// toolBreaks is what breaks a caller in the fields of a tool both
// surfaces have: an input or an output field gone, or an input newly
// required. What a tool adds is a change its changed line names.
func toolBreaks(was, now dumpTool) []change {
	var out []change
	broke := func(side, f, how string) {
		out = append(out, change{now.Name, now.Name + ": " + side + " " + how + ": " + f, side + " " + f})
	}
	wasIn, nowIn := schemaFields(was.InputSchema), schemaFields(now.InputSchema)
	for _, f := range slices.Sorted(maps.Keys(wasIn)) {
		if !nowIn[f] {
			broke("input", f, "removed")
		}
	}
	wasReq, nowReq := requiredOf(was.InputSchema), requiredOf(now.InputSchema)
	for _, f := range slices.Sorted(maps.Keys(nowReq)) {
		if !wasReq[f] {
			broke("input", f, "now required")
		}
	}
	wasOut, nowOut := schemaFields(was.OutputSchema), schemaFields(now.OutputSchema)
	for _, f := range slices.Sorted(maps.Keys(wasOut)) {
		if !nowOut[f] {
			broke("output field", f, "removed")
		}
	}
	return out
}

// changedParts names what differs between two dumped objects, one level
// deep, so a flipped hint reads as annotations.destructiveHint. Values
// are compared decoded, so a reordered key is the same schema.
func changedParts(o, n map[string]any) []string {
	var parts []string
	for _, k := range sortedUnion(o, n, strings.Compare) {
		if reflect.DeepEqual(o[k], n[k]) {
			continue
		}
		om, oMap := o[k].(map[string]any)
		nm, nMap := n[k].(map[string]any)
		if !oMap || !nMap {
			parts = append(parts, k)
			continue
		}
		for _, sub := range sortedUnion(om, nm, strings.Compare) {
			if !reflect.DeepEqual(om[sub], nm[sub]) {
				parts = append(parts, k+"."+sub)
			}
		}
	}
	return parts
}

// sortedUnion is every key of a and b, sorted by compare.
func sortedUnion[K comparable, V any](a, b map[K]V, compare func(K, K) int) []K {
	keys := slices.AppendSeq(slices.Collect(maps.Keys(a)), maps.Keys(b))
	slices.SortFunc(keys, compare)
	return slices.Compact(keys)
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
	tag, err := gitx.LatestTagOn(root, "HEAD")
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
	if len(c.breaking) > 0 {
		// Before the first tag nothing was released, so no entry can
		// declare a break.
		if tag == "" {
			return fmt.Errorf("%d breaking change(s) to the tool surface since %s", len(c.breaking), against)
		}
		changelog, err := os.ReadFile(filepath.Join(root, changelogFile))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		problems, err := undeclaredBreaks(c, string(changelog), tag)
		if err != nil {
			return err
		}
		if len(problems) > 0 {
			for _, p := range problems {
				_, _ = fmt.Fprintln(out, "undeclared  "+p)
			}
			return fmt.Errorf("%d breaking change(s) to the tool surface since %s that the CHANGELOG does not declare; a release "+
				"that makes one names its tool, in backticks, in a **Breaking:** entry above [%s], and bumps the major version",
				len(problems), against, strings.TrimPrefix(tag, "v"))
		}
		_, _ = fmt.Fprintf(out, "%d breaking change(s) since %s, each declared under **Breaking:** for a major release\n",
			len(c.breaking), against)
	}
	_, _ = fmt.Fprintf(out, "schema diff ok against %s: %d tools, %d resource templates; %d additive, %d other change(s)\n",
		against, len(cur.Tools), len(cur.ResourceTemplates), len(c.added), len(c.changed))
	return nil
}

// undeclaredBreaks is each breaking change that the unreleased part of
// the CHANGELOG, everything above the tag's own heading, does not
// declare: a **Breaking:** entry there names its tool, or its resource
// template, in backticks. A version heading there that does not raise
// the tag's major version is one too, since only a major release may
// break the surface (CLAUDE.md rule 17).
func undeclaredBreaks(c surfaceChanges, changelog, tag string) ([]string, error) {
	version := strings.TrimPrefix(tag, "v")
	unreleased, _, found := strings.Cut(changelog, "\n## ["+version+"]")
	if !found {
		// Without the tag's heading nothing tells unreleased from
		// released, so no entry can declare a break.
		return nil, fmt.Errorf("%s has no ## [%s] heading for %s, so no break can be declared against it", changelogFile, version, tag)
	}
	named := map[string]bool{}
	for line := range strings.Lines(unreleased) {
		if !strings.Contains(line, "**Breaking:**") {
			continue
		}
		for _, m := range stalenessCodeSpan.FindAllStringSubmatch(line, -1) {
			named[m[1]] = true
		}
	}
	var out []string
	for _, b := range c.breaking {
		if !named[b.item] {
			out = append(out, b.text)
		}
	}
	major := func(v string) int {
		m, _, _ := strings.Cut(v, ".")
		n, _ := strconv.Atoi(m)
		return n
	}
	for _, m := range changelogVersionHeading.FindAllStringSubmatch(unreleased, -1) {
		if major(m[1]) <= major(version) {
			out = append(out, fmt.Sprintf("release %s breaks the surface without raising the major version from %s", m[1], version))
		}
	}
	return out, nil
}

// dumpAt builds the server at a revision in a scratch worktree, trimmed
// and without cgo as `make build` does so the build cache is shared, and
// dumps it. The worktree is removed on every path out, a failed build or
// checkout included, and pruned so no stale registration survives.
func dumpAt(root, rev string) (schemaDump, error) {
	tmp, err := os.MkdirTemp("", "gates-schema-*")
	if err != nil {
		return schemaDump{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	src := filepath.Join(tmp, "src")
	if _, err := gitx.Output(root, "worktree", "add", "--quiet", "--detach", src, rev); err != nil {
		// A checkout that failed can leave its registration behind.
		_, _ = gitx.Output(root, "worktree", "prune")
		return schemaDump{}, err
	}
	defer func() {
		_, _ = gitx.Output(root, "worktree", "remove", "--force", src)
		_, _ = gitx.Output(root, "worktree", "prune")
	}()
	// A revision from before the server existed, such as the repository's
	// initial commit, has no surface: every tool is added since.
	if _, err := os.Stat(filepath.Join(src, "cmd", "gitlab-mcp")); errors.Is(err, fs.ErrNotExist) {
		return schemaDump{}, nil
	}
	bin := executable(filepath.Join(tmp, "gitlab-mcp"))
	build := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", bin, "./cmd/gitlab-mcp") //nolint:gosec // fixed arguments
	build.Dir = src
	build.Env = mcpstdio.Environ(os.Environ(), "CGO_ENABLED=0")
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
