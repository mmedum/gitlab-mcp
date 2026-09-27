package instance

import (
	"math"
	"net/url"
	"strconv"
	"strings"
)

// Kind is what a resolved web URL points at.
type Kind string

// Kinds resolve_url reports.
const (
	KindProject      Kind = "project"
	KindIssue        Kind = "issue"
	KindMergeRequest Kind = "merge_request"
	KindFile         Kind = "file"
	KindCommit       Kind = "commit"
	KindCompare      Kind = "compare"
	KindPipeline     Kind = "pipeline"
	KindJob          Kind = "job"
	KindWiki         Kind = "wiki"
)

// knownKinds is named in the error for a route resolve_url does not know.
const knownKinds = "project, issue, merge request, file, tree, commit, compare, pipeline, job and wiki pages"

// Ref is a web URL turned into tool arguments. Only the fields its Kind
// uses are set.
type Ref struct {
	Kind    Kind
	Project string // full path, decoded: "example-group/sub/project"
	IID     int64  // issue, merge_request
	ID      int64  // pipeline, job
	SHA     string // commit
	// Ref is the branch, tag or SHA of a file, a tree or a commit
	// listing. See RefPathCandidates for refs with slashes.
	Ref     string
	Path    string // file path, or directory for a tree
	Line    int    // from #L10
	EndLine int    // from #L10-20; 0 for a single line
	From    string // compare
	To      string // compare
	Slug    string // wiki page, may contain slashes
	Note    int64  // from #note_123 on an issue or merge request

	// refPath is the ref and path as one string, since where one ends
	// and the other begins is unknowable without asking GitLab.
	refPath string
	// pathOptional is set for trees and commit listings, where the
	// whole string may be the ref.
	pathOptional bool
}

// RefPath is one way to split a URL's ref-and-path.
type RefPath struct{ Ref, Path string }

// RefPathCandidates lists every split of a blob, raw, tree or commits
// URL's ref and path, shortest ref first, so a caller can check them
// against the project's branches and tags with one lookup. It is nil for
// other kinds.
func (r Ref) RefPathCandidates() []RefPath {
	if r.refPath == "" {
		return nil
	}
	segs := strings.Split(r.refPath, "/")
	last := len(segs) - 1
	if r.pathOptional {
		last = len(segs)
	}
	out := make([]RefPath, 0, last)
	for k := 1; k <= last; k++ {
		out = append(out, RefPath{Ref: strings.Join(segs[:k], "/"), Path: strings.Join(segs[k:], "/")})
	}
	return out
}

// reservedFirst are top-level routes that are never a namespace.
var reservedFirst = map[string]bool{
	"groups": true, "users": true, "dashboard": true, "explore": true,
	"admin": true, "help": true, "search": true, "-": true, "profile": true,
	"api": true, "oauth": true, "uploads": true, "assets": true,
}

// ResolveURL turns a GitLab web URL on this instance into tool
// arguments. A URL on another host is refused, naming the configured
// instance (§6.1).
func (i Instance) ResolveURL(raw string) (Ref, error) {
	if i.IsZero() {
		return Ref{}, invalidf("no GitLab instance is configured")
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Ref{}, invalidf("the URL is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return Ref{}, invalidf("that is not a web URL on the configured instance %s", i)
	}
	if u.User != nil {
		return Ref{}, invalidf("the URL carries credentials; give the page URL without them")
	}
	if !i.sameHost(u) {
		return Ref{}, invalidf("the URL is not on the configured instance %s", i)
	}
	segs, ok := i.relativeSegments(u.Path)
	if !ok {
		return Ref{}, invalidf("the URL is not under the configured instance %s", i)
	}
	ref, err := resolveSegments(segs)
	if err != nil {
		return Ref{}, err
	}
	if err := applyFragment(&ref, u.Fragment); err != nil {
		return Ref{}, err
	}
	return ref, nil
}

// sameHost compares host and port only: a link may say http for an
// https instance. A URL without a port matches only an instance on its
// scheme's default port.
func (i Instance) sameHost(u *url.URL) bool {
	hostname := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if hostname != i.hostname {
		return false
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		return err == nil && strconv.Itoa(n) == i.effectivePort()
	}
	return i.port == ""
}

// relativeSegments drops empty segments and the instance's sub-path.
func (i Instance) relativeSegments(p string) ([]string, bool) {
	var segs []string
	for s := range strings.SplitSeq(p, "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	for base := range strings.SplitSeq(strings.TrimPrefix(i.path, "/"), "/") {
		if base == "" {
			break
		}
		if len(segs) == 0 || segs[0] != base {
			return nil, false
		}
		segs = segs[1:]
	}
	return segs, true
}

func resolveSegments(segs []string) (Ref, error) {
	if len(segs) == 0 {
		return Ref{}, invalidf("the URL names the instance, not a project")
	}
	for _, s := range segs {
		if s == "." || s == ".." {
			return Ref{}, invalidf("the URL path must not contain %q segments", s)
		}
	}
	first := strings.ToLower(segs[0])
	if first == "groups" {
		return Ref{}, invalidf("the URL names a group or something in one, not a project; resolve_url knows %s", knownKinds)
	}
	if reservedFirst[first] {
		return Ref{}, invalidf("the URL is a %q page, not a project; resolve_url knows %s", segs[0], knownKinds)
	}
	for k, s := range segs {
		if s == "-" {
			if k < 2 {
				return Ref{}, invalidf("the URL names a group or user, not a project")
			}
			return resolveRoute(strings.Join(segs[:k], "/"), segs[k+1:])
		}
	}
	// GitLab still serves a few routes without the /-/ separator. A
	// keyword counts only where its shape fits, since a subgroup may be
	// named "issues" or "tree".
	for k := 2; k < len(segs); k++ {
		if legacyShape(segs[k:]) {
			return resolveRoute(strings.Join(segs[:k], "/"), segs[k:])
		}
	}
	if len(segs) == 1 {
		return Ref{}, invalidf("the URL names a group or user, not a project")
	}
	last := len(segs) - 1
	segs[last] = strings.TrimSuffix(segs[last], ".git")
	return Ref{Kind: KindProject, Project: strings.Join(segs, "/")}, nil
}

func legacyShape(r []string) bool {
	switch r[0] {
	case "issues", "merge_requests":
		_, err := positive(at(r, 1))
		return err == nil
	case "blob", "raw":
		return len(r) >= 3
	case "tree", "wikis":
		return len(r) >= 2
	}
	return false
}

func at(r []string, k int) string {
	if k < len(r) {
		return r[k]
	}
	return ""
}

// lineNumber is positive bounded to what an int holds on every
// platform, so a 32-bit build cannot wrap a huge anchor.
func lineNumber(s string) (int, error) {
	n, err := positive(s)
	if err != nil {
		return 0, err
	}
	if n > math.MaxInt32 {
		return 0, invalidf("expected a line number, got %q", s)
	}
	return int(n), nil
}

// positive parses a positive decimal id; signs and zero are refused.
func positive(s string) (int64, error) {
	if s == "" || len(s) > 18 {
		return 0, invalidf("expected a positive number, got %q", s)
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, invalidf("expected a positive number, got %q", s)
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, invalidf("expected a positive number, got %q", s)
	}
	return n, nil
}

// numberedRoutes are routes whose next segment is an iid or id. what
// completes "the URL is ..." when the number is missing or malformed.
var numberedRoutes = map[string]struct {
	kind Kind
	what string
}{
	"issues":         {KindIssue, "an issue list, not an issue"},
	"work_items":     {KindIssue, "a work item list, not an issue"},
	"merge_requests": {KindMergeRequest, "a merge request list, not a merge request"},
	"pipelines":      {KindPipeline, "not a single pipeline"},
	"jobs":           {KindJob, "not a single job"},
}

// resolveRoute reads what follows the project path. r[0] is the route.
func resolveRoute(project string, r []string) (Ref, error) {
	ref := Ref{Project: project}
	if len(r) == 0 {
		ref.Kind = KindProject
		return ref, nil
	}
	if nr, ok := numberedRoutes[r[0]]; ok {
		k := 1
		if r[0] == "issues" && at(r, 1) == "incident" {
			k = 2
		}
		n, err := positive(at(r, k))
		if err != nil {
			return Ref{}, invalidf("the URL is %s: %v", nr.what, err)
		}
		ref.Kind = nr.kind
		if nr.kind == KindIssue || nr.kind == KindMergeRequest {
			ref.IID = n
		} else {
			ref.ID = n
		}
		return ref, nil
	}
	rest := strings.Join(r[1:], "/")
	switch r[0] {
	case "blob", "raw":
		if len(r) < 3 {
			return Ref{}, invalidf("the file URL has no file path after the ref")
		}
		ref.Kind, ref.Ref, ref.Path, ref.refPath = KindFile, r[1], strings.Join(r[2:], "/"), rest
	case "tree", "commits":
		ref.Kind = KindProject
		if len(r) >= 2 {
			ref.Ref, ref.Path, ref.refPath, ref.pathOptional = r[1], strings.Join(r[2:], "/"), rest, true
		}
	case "commit":
		sha := strings.TrimSuffix(strings.TrimSuffix(at(r, 1), ".diff"), ".patch")
		if sha == "" {
			return Ref{}, invalidf("the commit URL has no SHA")
		}
		ref.Kind, ref.SHA = KindCommit, sha
	case "compare":
		from, to, ok := strings.Cut(rest, "...")
		if !ok {
			from, to, ok = strings.Cut(rest, "..")
		}
		if !ok || from == "" || to == "" {
			return Ref{}, invalidf("the compare URL needs two refs, as from...to")
		}
		ref.Kind, ref.From, ref.To = KindCompare, from, to
	case "wikis":
		// config/routes/wiki.rb: pages, templates and new are the wiki's
		// own pages, and a page's edit, history, diff and raw views end
		// in those words. An empty slug is the wiki as a whole.
		ref.Kind, ref.Slug = KindWiki, rest
		for _, view := range []string{"/edit", "/history", "/diff", "/raw"} {
			ref.Slug = strings.TrimSuffix(ref.Slug, view)
		}
		switch ref.Slug {
		case "":
			ref.Slug = "home"
		case "pages", "templates", "new":
			ref.Slug = ""
		}
	default:
		return Ref{}, invalidf("the URL is a %q page, which resolve_url does not know; it knows %s", r[0], knownKinds)
	}
	return ref, nil
}

// applyFragment reads #L10, #L10-20, #L10-L20 on files and #note_123
// on issues and merge requests. Other fragments are ignored.
func applyFragment(ref *Ref, frag string) error {
	switch ref.Kind {
	case KindFile:
		lines, ok := strings.CutPrefix(frag, "L")
		if !ok {
			return nil
		}
		start, end, ranged := strings.Cut(lines, "-")
		n, err := lineNumber(start)
		if err != nil {
			return invalidf("the line anchor is not a line number: %v", err)
		}
		ref.Line = n
		if !ranged {
			return nil
		}
		m, err := lineNumber(strings.TrimPrefix(end, "L"))
		if err != nil {
			return invalidf("the line anchor is not a line range: %v", err)
		}
		if m < n {
			return invalidf("the line range ends before it starts")
		}
		if m > n {
			ref.EndLine = m
		}
	case KindIssue, KindMergeRequest:
		id, ok := strings.CutPrefix(frag, "note_")
		if !ok {
			return nil
		}
		n, err := positive(id)
		if err != nil {
			return invalidf("the note anchor is not a note id: %v", err)
		}
		ref.Note = n
	}
	return nil
}
