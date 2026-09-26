package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// The OpenAPI snapshot is GitLab's published REST surface at one release
// tag, as data this repository owns: every operation with its verb,
// path, tags, lifecycle, parameters by location, request body fields and
// response fields per status. `gates api-diff` writes it and nobody
// edits it. api-coverage and api-fields read it offline in `make check`,
// so completeness is held on every commit rather than only when somebody
// remembers to fetch (docs/architecture.md §8a, the standard's §1).

const (
	snapshotFile = "testdata/openapi-v3.snapshot.json"
	// snapshotFloor is the fewest operations a real fetch can carry. The
	// file had 1,856 at 19.4 and grows every release; a fetch below this
	// is a truncated or wrong document, and writing it would make every
	// verdict on the missing operations read as stale.
	snapshotFloor = 1500
	// openAPIPath is where GitLab keeps the generated file in its tree.
	openAPIPath = "doc/api/openapi/openapi_v3.yaml"
	// fieldDepth bounds how far nested objects are flattened. The wire
	// types nest three deep (approved_by.user.username); GitLab's schemas
	// are recursive, and a bound is what keeps a project inside a
	// namespace inside a project from being written out for ever.
	fieldDepth = 4
)

// gitlabBase is where the file and the tag list are fetched from. A test
// points it at a closed port.
var gitlabBase = "https://gitlab.com"

// apiSnapshot is testdata/openapi-v3.snapshot.json.
type apiSnapshot struct {
	Source  string `json:"source"`
	Tag     string `json:"tag"`
	SHA256  string `json:"sha256"`
	Fetched string `json:"fetched"`
	// Version is the document's own info.version.
	Version    string         `json:"version"`
	Operations []apiOperation `json:"operations"`
}

// apiOperation is one published operation. Field maps go from a dotted
// field name to its declared type: "integer", "string", "boolean",
// "number", "object", "array<string>", "string|integer" for a oneOf,
// or "" when the document does not say.
type apiOperation struct {
	Verb       string   `json:"verb"`
	Path       string   `json:"path"` // as published, without /api/v4
	Tags       []string `json:"tags,omitempty"`
	Lifecycle  string   `json:"lifecycle,omitempty"`
	Tier       string   `json:"tier,omitempty"`
	Deprecated bool     `json:"deprecated,omitempty"`
	// Params is by location: "path", "query", "header".
	Params map[string]map[string]string `json:"params,omitempty"`
	// Request is the body's fields, empty for a body-less operation.
	Request map[string]string `json:"request,omitempty"`
	// Responses is by status. A status with a null map publishes no
	// schema, which is how 526 operations answer; that is recorded
	// rather than dropped, because api-fields must tell "no schema" from
	// "no such status".
	Responses map[string]map[string]string `json:"responses"`
}

// key is how the verdict file names an operation: "GET /projects/{id}".
func (o apiOperation) key() string { return o.Verb + " " + o.Path }

// ------------------------------------------------------------ reading

func readSnapshot(path string) (apiSnapshot, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // a path this repository owns
	if err != nil {
		return apiSnapshot{}, fmt.Errorf("cannot read %s: %w; `make api-diff` writes it", path, err)
	}
	var s apiSnapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return apiSnapshot{}, fmt.Errorf("%s: %w", path, err)
	}
	if len(s.Operations) < snapshotFloor {
		return apiSnapshot{}, fmt.Errorf("%s lists %d operations and a real fetch lists at least %d; "+
			"a gate reading it could not tell a complete record from an empty one", path, len(s.Operations), snapshotFloor)
	}
	return s, nil
}

// ------------------------------------------------------------ writing

// encodeSnapshot writes one operation per line, so a diff of the file
// after a refetch shows which operation moved rather than a wall of
// re-indented JSON.
func encodeSnapshot(s apiSnapshot) ([]byte, error) {
	ops := slices.Clone(s.Operations)
	slices.SortFunc(ops, func(a, b apiOperation) int {
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return strings.Compare(a.Verb, b.Verb)
	})
	var b bytes.Buffer
	head, err := json.Marshal(struct {
		Source  string `json:"source"`
		Tag     string `json:"tag"`
		SHA256  string `json:"sha256"`
		Fetched string `json:"fetched"`
		Version string `json:"version"`
	}{s.Source, s.Tag, s.SHA256, s.Fetched, s.Version})
	if err != nil {
		return nil, err
	}
	// The header object, reopened to append the operations.
	b.Write(head[:len(head)-1])
	b.WriteString(",\n\"operations\": [\n")
	for i, op := range ops {
		line, err := json.Marshal(op)
		if err != nil {
			return nil, err
		}
		b.Write(line)
		if i < len(ops)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString("]}\n")
	return b.Bytes(), nil
}

// writeFileAtomic writes through a temporary file in the same directory
// and renames it over path, so a failure part-way leaves the old file
// whole. A half-written snapshot is worse than a stale one: the next
// `make check` would hold the verdicts to something that is neither.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // a directory of committed files
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil { //nolint:gosec // a committed file, world-readable like its neighbors
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// ------------------------------------------------------------ fetching

// stableTag is a GitLab Enterprise Edition release tag: the tree the
// OpenAPI file is generated in, and the one both editions ship from.
var stableTag = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)-ee$`)

func fetch(ctx context.Context, client *http.Client, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", u, res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, 64<<20))
}

// newestStableTag asks GitLab's own project for its release tags and
// picks the newest vX.Y.Z-ee. Release candidates sort among the stable
// tags in GitLab's version order, so the order is not trusted.
func newestStableTag(ctx context.Context, client *http.Client, base string) (string, error) {
	u := base + "/api/v4/projects/gitlab-org%2Fgitlab/repository/tags?order_by=version&sort=desc&per_page=100"
	raw, err := fetch(ctx, client, u)
	if err != nil {
		return "", err
	}
	var tags []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &tags); err != nil {
		return "", fmt.Errorf("the tag list is not JSON: %w", err)
	}
	best, bestV := "", [3]int{}
	for _, t := range tags {
		m := stableTag.FindStringSubmatch(t.Name)
		if m == nil {
			continue
		}
		var v [3]int
		for i := range v {
			v[i], _ = strconv.Atoi(m[i+1])
		}
		if best == "" || slices.Compare(v[:], bestV[:]) > 0 {
			best, bestV = t.Name, v
		}
	}
	if best == "" {
		return "", errors.New("no vX.Y.Z-ee tag among the newest hundred; name one: make api-diff API_TAG=v19.4.1-ee")
	}
	return best, nil
}

// fetchSnapshot fetches the file at tag and parses it.
func fetchSnapshot(ctx context.Context, client *http.Client, base, tag string, floor int, now time.Time) (apiSnapshot, error) {
	source := base + "/gitlab-org/gitlab/-/raw/" + url.PathEscape(tag) + "/" + openAPIPath
	raw, err := fetch(ctx, client, source)
	if err != nil {
		return apiSnapshot{}, err
	}
	snap, err := parseOpenAPI(raw)
	if err != nil {
		return apiSnapshot{}, err
	}
	if len(snap.Operations) < floor {
		return apiSnapshot{}, fmt.Errorf("the file at %s has %d operations and a real one has at least %d; refusing to write it",
			tag, len(snap.Operations), floor)
	}
	sum := sha256.Sum256(raw)
	snap.Source, snap.Tag, snap.SHA256 = source, tag, hex.EncodeToString(sum[:])
	snap.Fetched = now.UTC().Format("2006-01-02")
	return snap, nil
}

// ------------------------------------------------------------ parsing

type oaSchema struct {
	Ref        string               `yaml:"$ref"`
	Type       string               `yaml:"type"`
	Format     string               `yaml:"format"`
	Properties map[string]*oaSchema `yaml:"properties"`
	Items      *oaSchema            `yaml:"items"`
	AllOf      []*oaSchema          `yaml:"allOf"`
	OneOf      []*oaSchema          `yaml:"oneOf"`
	AnyOf      []*oaSchema          `yaml:"anyOf"`
}

type oaContent map[string]struct {
	Schema *oaSchema `yaml:"schema"`
}

type oaOperation struct {
	Tags       []string `yaml:"tags"`
	Lifecycle  string   `yaml:"x-gitlab-lifecycle"`
	Tier       any      `yaml:"x-gitlab-tier"`
	Deprecated bool     `yaml:"deprecated"`
	Parameters []struct {
		Name   string    `yaml:"name"`
		In     string    `yaml:"in"`
		Schema *oaSchema `yaml:"schema"`
	} `yaml:"parameters"`
	RequestBody *struct {
		Content oaContent `yaml:"content"`
	} `yaml:"requestBody"`
	Responses map[string]struct {
		Content oaContent `yaml:"content"`
	} `yaml:"responses"`
}

type oaDocument struct {
	Info struct {
		Version string `yaml:"version"`
	} `yaml:"info"`
	Paths      map[string]map[string]yaml.Node `yaml:"paths"`
	Components struct {
		Schemas map[string]*oaSchema `yaml:"schemas"`
	} `yaml:"components"`
}

var httpVerbs = map[string]string{
	"get": "GET", "put": "PUT", "post": "POST", "delete": "DELETE",
	"patch": "PATCH", "head": "HEAD", "options": "OPTIONS",
}

// parseOpenAPI reads the document into a snapshot. Paths keep their
// published parameter names so a person reading the verdict file can
// tell {issue_iid} from {merge_request_iid}; the /api/v4 root is dropped,
// because every operation has it.
func parseOpenAPI(raw []byte) (apiSnapshot, error) {
	var doc oaDocument
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return apiSnapshot{}, fmt.Errorf("the OpenAPI file is not YAML this reads: %w", err)
	}
	fl := flattener{schemas: doc.Components.Schemas}
	snap := apiSnapshot{Version: doc.Info.Version}
	for _, path := range slices.Sorted(maps.Keys(doc.Paths)) {
		item := doc.Paths[path]
		for _, method := range slices.Sorted(maps.Keys(item)) {
			verb, ok := httpVerbs[method]
			if !ok {
				continue
			}
			node := item[method]
			var op oaOperation
			if err := node.Decode(&op); err != nil {
				return apiSnapshot{}, fmt.Errorf("%s %s: %w", verb, path, err)
			}
			snap.Operations = append(snap.Operations, fl.operation(verb, strings.TrimPrefix(path, "/api/v4"), op))
		}
	}
	return snap, nil
}

type flattener struct {
	schemas map[string]*oaSchema
}

func (f flattener) operation(verb, path string, op oaOperation) apiOperation {
	out := apiOperation{Verb: verb, Path: path, Tags: op.Tags, Lifecycle: op.Lifecycle, Deprecated: op.Deprecated,
		Responses: map[string]map[string]string{}}
	if op.Tier != nil {
		out.Tier = fmt.Sprint(op.Tier)
	}
	for _, p := range op.Parameters {
		if out.Params == nil {
			out.Params = map[string]map[string]string{}
		}
		if out.Params[p.In] == nil {
			out.Params[p.In] = map[string]string{}
		}
		out.Params[p.In][p.Name] = f.typeOf(p.Schema, 0)
	}
	if op.RequestBody != nil {
		out.Request = f.content(op.RequestBody.Content)
	}
	for status, r := range op.Responses {
		out.Responses[status] = f.content(r.Content)
	}
	return out
}

// content flattens a JSON body when there is one, else a form body.
// nil means the operation publishes no schema for it.
func (f flattener) content(c oaContent) map[string]string {
	for _, media := range []string{"application/json", "multipart/form-data", "application/x-www-form-urlencoded"} {
		if m, ok := c[media]; ok && m.Schema != nil {
			fields := map[string]string{}
			f.fields(m.Schema, "", 0, map[string]bool{}, fields)
			return fields
		}
	}
	return nil
}

func (f flattener) resolve(s *oaSchema, seen map[string]bool) (*oaSchema, map[string]bool, bool) {
	for s != nil && s.Ref != "" {
		name := strings.TrimPrefix(s.Ref, "#/components/schemas/")
		if seen[name] {
			return nil, seen, false
		}
		next := maps.Clone(seen)
		next[name] = true
		seen = next
		s = f.schemas[name]
	}
	return s, seen, s != nil
}

// fields walks one schema's properties into dotted names. Arrays are
// transparent, so assignees[].username is "assignees.username", which is
// how a Go struct field of a slice type is named too.
func (f flattener) fields(s *oaSchema, prefix string, depth int, seen map[string]bool, out map[string]string) {
	s, seen, ok := f.resolve(s, seen)
	if !ok || depth >= fieldDepth {
		return
	}
	for _, group := range [][]*oaSchema{s.AllOf, s.OneOf, s.AnyOf} {
		for _, sub := range group {
			f.fields(sub, prefix, depth, seen, out)
		}
	}
	if s.Items != nil {
		f.fields(s.Items, prefix, depth, seen, out)
	}
	for _, name := range slices.Sorted(maps.Keys(s.Properties)) {
		prop := s.Properties[name]
		out[prefix+name] = f.typeOf(prop, 0)
		f.fields(prop, prefix+name+".", depth+1, seen, out)
	}
}

// typeOf names a schema's type the way api-fields compares it with a Go
// kind.
func (f flattener) typeOf(s *oaSchema, depth int) string {
	s, _, ok := f.resolve(s, map[string]bool{})
	if !ok || depth > 3 {
		return ""
	}
	switch {
	case len(s.OneOf) > 0 || len(s.AnyOf) > 0:
		var parts []string
		for _, sub := range append(slices.Clone(s.OneOf), s.AnyOf...) {
			if t := f.typeOf(sub, depth+1); t != "" && !slices.Contains(parts, t) {
				parts = append(parts, t)
			}
		}
		slices.Sort(parts)
		return strings.Join(parts, "|")
	case s.Type == "array":
		return "array<" + f.typeOf(s.Items, depth+1) + ">"
	case s.Type != "":
		return s.Type
	case len(s.Properties) > 0 || len(s.AllOf) > 0:
		return "object"
	}
	return ""
}
