package gapi

import (
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Call is one request to GitLab.
//
// Every call site builds one as a composite literal whose Method and
// Path are literals: "GET", "projects/{}/issues/{}". `scripts/gates
// api-coverage` reads those pairs from this package's syntax tree and
// binds each to an operation of the OpenAPI snapshot, so a call nobody
// judged fails the build. Do not compute Method or Path.
type Call struct {
	// Method is the HTTP verb. It decides whether the call writes and
	// whether it may be repeated.
	Method string
	// Path is a template under the API root, written in this package and
	// never taken from a caller. Each {} is filled, in order, by one
	// element of Args, escaped as a single path segment exactly once.
	Path string
	// Args fill Path's placeholders. They are caller-supplied: ids, full
	// paths, file paths, refs.
	Args []string
	// Query is the query string. It may carry a search term, so it never
	// reaches a log or an error message.
	Query url.Values
	// Body is marshaled as JSON when not nil.
	Body any
	// Root is where Path starts. The zero value is the API root.
	Root Root
	// Repeatable is the reason a POST may be sent twice without applying
	// twice, such as "marking a todo done twice leaves it done". Empty
	// means the method decides: GET, PUT and DELETE repeat, POST does
	// not. A create is never declared repeatable (§4.5).
	Repeatable string
	// ReadOnly is the reason a POST changes nothing, such as "linting
	// creates nothing". Such a call may repeat and may run under a dry
	// run, and `scripts/gates outcomes` asks it for no outcome. Empty
	// means the method decides.
	ReadOnly string
	// Witness names the witness this write carries ("sha"). A 409 on a
	// call with a witness means the witness moved, so it is stale rather
	// than a conflict (§4.6).
	Witness string
	// UnmodifiedSince is sent as If-Unmodified-Since when set. GitLab's
	// conditional deletes answer 412 when the resource changed after it,
	// which is [stale] (§4.6).
	UnmodifiedSince time.Time
	// Bucket is the rate bucket the call is charged to, beyond the
	// instance's own. The zero value is the general bucket only.
	Bucket Bucket
	// Name is a short label for logs and messages, "get_issue". Never a
	// path.
	Name string
}

// Root is the base a Call's Path is resolved against.
type Root int

const (
	// RootAPI is <instance>/api/v4/.
	RootAPI Root = iota
	// RootWeb is the instance's web base, for the OAuth endpoints
	// Doorkeeper serves outside the API, such as /oauth/token/info.
	RootWeb
)

// Bucket names a known application limit with a bucket of its own.
type Bucket int

const (
	// BucketGeneral is the instance-wide bucket every call is charged to.
	BucketGeneral Bucket = iota
	// BucketNotes is note creation: 60 a minute on gitlab.com (§2.13).
	BucketNotes
)

func (b Bucket) String() string {
	if b == BucketNotes {
		return "notes"
	}
	return "general"
}

// isWrite reports whether a call may change something: any method but
// GET and HEAD, unless the call says why it changes nothing.
func isWrite(call Call) bool {
	return call.Method != "GET" && call.Method != "HEAD" && call.ReadOnly == ""
}

// fillPath fills a template's {} placeholders with escaped args. A count
// mismatch is a programming error. An empty argument, or one with a "."
// or ".." segment, would address a different resource than it names and
// is refused as the caller's mistake.
func fillPath(template string, args []string) (string, error) {
	if n := strings.Count(template, "{}"); n != len(args) {
		return "", Errf(ClassUnexpected, "path template has %d placeholders and %d arguments", n, len(args))
	}
	var b strings.Builder
	rest := template
	for _, a := range args {
		if err := checkSegment(a); err != nil {
			return "", err
		}
		i := strings.Index(rest, "{}")
		b.WriteString(rest[:i])
		b.WriteString(url.PathEscape(a))
		rest = rest[i+2:]
	}
	b.WriteString(rest)
	return b.String(), nil
}

// checkSegment refuses an argument that is empty, holds a control
// character, or has a "." or ".." segment. Escaping turns "/" into %2F,
// but a proxy that decodes it would then walk the dots.
func checkSegment(a string) error {
	if a == "" {
		return Errf(ClassInvalid, "an id, path or ref is empty")
	}
	if strings.IndexFunc(a, unicode.IsControl) >= 0 {
		return Errf(ClassInvalid, "an id, path or ref holds a control character")
	}
	for seg := range strings.SplitSeq(a, "/") {
		if seg == "." || seg == ".." {
			return Errf(ClassInvalid, "a path may not contain a %q segment", seg)
		}
	}
	return nil
}

// locator addresses a project or group by numeric id or by full path.
type locator struct {
	id   int64
	path string
}

// ID is the numeric id, 0 when addressed by path.
func (l locator) ID() int64 { return l.id }

// Path is the full path, "" when addressed by id only.
func (l locator) Path() string { return l.path }

// IsZero reports an unset locator.
func (l locator) IsZero() bool { return l.id == 0 && l.path == "" }

// segment is the unescaped path argument: the id when known, which
// survives a proxy that decodes %2F, else the full path.
func (l locator) segment() string {
	if l.id != 0 {
		return strconv.FormatInt(l.id, 10)
	}
	return l.path
}

// String is the id when known, else the path.
func (l locator) String() string { return l.segment() }

func parseLocator(kind, s string) (locator, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return locator{}, Errf(ClassInvalid, "the %s is empty: pass its numeric id or full path", kind)
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n <= 0 {
			return locator{}, Errf(ClassInvalid, "a %s id is a positive number", kind)
		}
		return locator{id: n}, nil
	}
	// A path someone already encoded, "group%2Fproject", is decoded once
	// here, so the client's own escaping is the only one on the wire.
	if !strings.Contains(s, "/") && strings.Contains(strings.ToLower(s), "%2f") {
		if dec, err := url.PathUnescape(s); err == nil {
			s = dec
		}
	}
	s = strings.Trim(s, "/")
	if err := checkSegment(s); err != nil {
		return locator{}, err
	}
	for seg := range strings.SplitSeq(s, "/") {
		if seg == "" {
			return locator{}, Errf(ClassInvalid, "a %s path has an empty segment", kind)
		}
	}
	return locator{path: s}, nil
}

// Project addresses a project by numeric id or full path
// ("group/sub/project"). The zero value is unset.
type Project struct{ locator }

// ProjectByID addresses a project by numeric id.
func ProjectByID(id int64) Project { return Project{locator{id: id}} }

// ParseProject reads a numeric id or a full path. A path is kept
// unescaped; the client escapes it once when it builds a request.
func ParseProject(s string) (Project, error) {
	l, err := parseLocator("project", s)
	return Project{l}, err
}

// withPath returns p carrying both its id and its full path.
func (p Project) withPath(path string) Project {
	p.path = path
	return p
}

// Group addresses a group by numeric id or full path.
type Group struct{ locator }

// ParseGroup reads a numeric id or a full path.
func ParseGroup(s string) (Group, error) {
	l, err := parseLocator("group", s)
	return Group{l}, err
}
