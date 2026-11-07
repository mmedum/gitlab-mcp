package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// One reader for a GitHub workflow, shared by pins, parity and release.
// It parses YAML rather than splitting lines: a line splitter reads a
// `version:` under `env:` as a tool pin, and understands only one of the
// shapes a sequence can be written in, so a file written another way
// yields no steps and therefore no problems.

// workflowDir is where the workflows live, relative to the root.
const workflowDir = ".github/workflows"

// workflow is the part of a workflow file the gates read.
type workflow struct {
	// path is where it was read from, slash-separated, for messages.
	path string
	// text is the raw file, for the checks that are about literal lines:
	// a SHA and the comment after it.
	text string
	// On is left as a node: `on` is a union of shapes.
	On       yaml.Node         `yaml:"on"`
	Env      map[string]string `yaml:"env"`
	Defaults struct {
		Run struct {
			Shell string `yaml:"shell"`
		} `yaml:"run"`
	} `yaml:"defaults"`
	// Permissions is left as a node: it is a mapping or a word such as
	// read-all.
	Permissions yaml.Node              `yaml:"permissions"`
	Jobs        map[string]workflowJob `yaml:"jobs"`
}

// workflowJob is one entry under `jobs:`.
type workflowJob struct {
	Env         map[string]string `yaml:"env"`
	Uses        string            `yaml:"uses"`
	If          string            `yaml:"if"`
	Needs       yaml.Node         `yaml:"needs"`
	Permissions yaml.Node         `yaml:"permissions"`
	Steps       []workflowStep    `yaml:"steps"`
	// Container and each service are an image, or a mapping with an
	// image: key.
	Container yaml.Node            `yaml:"container"`
	Services  map[string]yaml.Node `yaml:"services"`
	// uses is where Uses sits, for its SHA and version comment.
	uses workflowRef
}

// workflowPermissions reads a permissions node as scope → access. A
// word such as read-all comes back under the key "*".
func workflowPermissions(node *yaml.Node) map[string]string {
	out := map[string]string{}
	switch node.Kind {
	case yaml.ScalarNode:
		out["*"] = node.Value
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			out[node.Content[i].Value] = node.Content[i+1].Value
		}
	}
	return out
}

// workflowStep is one entry under a job's `steps:`.
type workflowStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]any    `yaml:"with"`
	Env  map[string]string `yaml:"env"`
	uses workflowRef
	// job names the job the step belongs to.
	job string
}

// workflowRef is where a `uses:` value sits and the comment after it,
// which names the version a SHA is.
type workflowRef struct {
	line    int
	comment string
}

// UnmarshalYAML keeps the `uses:` value's line and comment, which the
// decoded string loses.
func (s *workflowStep) UnmarshalYAML(node *yaml.Node) error {
	type plain workflowStep
	if err := node.Decode((*plain)(s)); err != nil {
		return err
	}
	s.uses = workflowRefOf(node)
	return nil
}

// UnmarshalYAML keeps the job-level `uses:` value's line and comment.
func (j *workflowJob) UnmarshalYAML(node *yaml.Node) error {
	type plain workflowJob
	if err := node.Decode((*plain)(j)); err != nil {
		return err
	}
	j.uses = workflowRefOf(node)
	return nil
}

// workflowRefOf reads the `uses:` value of a step or job mapping. yaml.v3
// gives a trailing comment to the value in block style, and to the
// mapping itself in flow style ({uses: …} # v1.0.0). node is never an
// alias: yaml.v3 resolves one before it calls UnmarshalYAML.
func workflowRefOf(node *yaml.Node) workflowRef {
	v := workflowResolve(workflowMappingValue(node, "uses"))
	if v == nil {
		return workflowRef{}
	}
	comment := v.LineComment
	if comment == "" && node.Style&yaml.FlowStyle != 0 && node.Line == v.Line {
		comment = node.LineComment
	}
	return workflowRef{line: v.Line, comment: strings.TrimSpace(strings.TrimPrefix(comment, "#"))}
}

// input reads one `with:` value as text. Values are not all strings
// (`fetch-depth: 0`), so they are rendered rather than asserted.
func (s workflowStep) input(key string) (string, bool) {
	v, ok := s.With[key]
	if !ok {
		return "", false
	}
	return strings.TrimSpace(fmt.Sprint(v)), true
}

// action is the step's action without its ref: "actions/checkout".
func (s workflowStep) action() string {
	a, _, _ := strings.Cut(s.Uses, "@")
	return a
}

// steps is every step in the file, jobs in name order.
func (w workflow) steps() []workflowStep {
	var out []workflowStep
	for _, name := range slices.Sorted(maps.Keys(w.Jobs)) {
		for _, s := range w.Jobs[name].Steps {
			s.job = name
			out = append(out, s)
		}
	}
	return out
}

// resolveEnv turns `${{ env.NAME }}` into the value the workflow's env
// block gives it. Anything else comes back unchanged, and so does a
// reference to a name nobody set, so it fails as a version rather than
// passing on a value that does not exist.
func (w workflow) resolveEnv(value string) string {
	v := strings.TrimSpace(value)
	inner, ok := strings.CutPrefix(v, "${{")
	if !ok {
		return value
	}
	inner, ok = strings.CutSuffix(inner, "}}")
	if !ok {
		return value
	}
	name, ok := strings.CutPrefix(strings.TrimSpace(inner), "env.")
	if !ok {
		return value
	}
	if got, ok := w.Env[name]; ok {
		return got
	}
	return value
}

// triggersOnTag reports whether pushing a tag starting with prefix
// starts this workflow.
func (w workflow) triggersOnTag(prefix string) bool {
	push := workflowMappingValue(&w.On, "push")
	tags := workflowMappingValue(push, "tags")
	if tags == nil {
		return false
	}
	for _, pattern := range tags.Content {
		if strings.HasPrefix(pattern.Value, prefix) {
			return true
		}
	}
	return strings.HasPrefix(tags.Value, prefix)
}

// workflowImage is the image a job's container or service names, or empty
// when there is none.
func workflowImage(node *yaml.Node) string {
	node = workflowResolve(node)
	if node != nil && node.Kind == yaml.MappingNode {
		node = workflowResolve(workflowMappingValue(node, "image"))
	}
	if node == nil || node.Kind != yaml.ScalarNode {
		return ""
	}
	return strings.TrimSpace(node.Value)
}

// workflowResolve is the node an alias such as *img stands for, or node
// itself when it is not an alias.
func workflowResolve(node *yaml.Node) *yaml.Node {
	for node != nil && node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	return node
}

// workflowMappingValue reads one key out of a YAML mapping node. A value
// that is an alias comes back as one, for the caller to resolve.
func workflowMappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
		node = node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// workflowMergeKey is the line of the first << key under node, or 0. GitHub
// documents anchors and aliases and not merge keys
// (https://docs.github.com/en/actions/reference/workflows-and-actions/reusing-workflow-configurations#yaml-anchors-and-aliases),
// and yaml.v3 merges one, so a step read here could differ from the step
// GitHub runs.
func workflowMergeKey(node *yaml.Node) int {
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			if k := node.Content[i]; k.Kind == yaml.ScalarNode && k.Value == "<<" {
				return k.Line
			}
		}
	}
	for _, child := range node.Content {
		if line := workflowMergeKey(child); line > 0 {
			return line
		}
	}
	return 0
}

// readWorkflow parses one workflow file. name is the path to report.
func readWorkflow(path, name string) (workflow, error) {
	data, err := os.ReadFile(path) //nolint:gosec // a path under .github/workflows
	if err != nil {
		return workflow{}, err
	}
	var w workflow
	if err := yaml.Unmarshal(data, &w); err != nil {
		return workflow{}, fmt.Errorf("%s is not valid YAML: %w", name, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return workflow{}, fmt.Errorf("%s is not valid YAML: %w", name, err)
	}
	if line := workflowMergeKey(&doc); line > 0 {
		return workflow{}, fmt.Errorf("%s:%d: GitHub Actions does not document merge keys; write the mapping out", name, line)
	}
	w.path = name
	w.text = string(data)
	return w, nil
}

// readWorkflows reads every workflow under root, sorted by path.
func readWorkflows(root string) ([]workflow, error) {
	var paths []string
	for _, pattern := range []string{"*.yml", "*.yaml"} {
		found, err := filepath.Glob(filepath.Join(root, workflowDir, pattern))
		if err != nil {
			return nil, err
		}
		paths = append(paths, found...)
	}
	slices.Sort(paths)
	if len(paths) == 0 {
		return nil, fmt.Errorf("no workflows under %s", workflowDir)
	}
	var out []workflow
	for _, p := range paths {
		w, err := readWorkflow(p, workflowDir+"/"+filepath.Base(p))
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}

// workflowNamed finds one workflow by its base name.
func workflowNamed(flows []workflow, base string) (workflow, bool) {
	for _, w := range flows {
		if strings.HasSuffix(w.path, "/"+base) {
			return w, true
		}
	}
	return workflow{}, false
}

// workflowRunLines is the commands of a `run:` block: comments, blank
// lines and continuation breaks removed, so a word in a comment is not
// read as a command.
func workflowRunLines(run string) []string {
	var out []string
	var pending string
	for line := range strings.SplitSeq(run, "\n") {
		t := strings.TrimSpace(line)
		if pending == "" && (t == "" || strings.HasPrefix(t, "#")) {
			continue
		}
		if cont, ok := strings.CutSuffix(t, "\\"); ok {
			pending += strings.TrimRight(cont, " \t") + " "
			continue
		}
		out = append(out, strings.TrimSpace(pending+t))
		pending = ""
	}
	if strings.TrimSpace(pending) != "" {
		out = append(out, strings.TrimSpace(pending))
	}
	return out
}
