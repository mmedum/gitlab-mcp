package gapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/mmedum/gitlab-mcp/internal/instance"
)

func TestClassesAreTheClosedThirteen(t *testing.T) {
	want := []string{"invalid", "not_found", "auth", "forbidden", "conflict", "stale", "ambiguous", "blocked",
		"rate_limited", "unavailable", "unsupported", "ambiguous_outcome", "unexpected"}
	got := make([]string, 0, len(Classes))
	for _, c := range Classes {
		got = append(got, string(c))
		if !c.Valid() {
			t.Errorf("%s not Valid", c)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("Classes = %v, want %v", got, want)
	}
	if Class("nope").Valid() {
		t.Error("an unknown class is Valid")
	}
	for _, c := range Classes {
		want := c == ClassUnavailable || c == ClassRateLimited
		if c.Retryable() != want {
			t.Errorf("%s.Retryable() = %v", c, c.Retryable())
		}
	}
}

func TestErrorText(t *testing.T) {
	e := Errf(ClassStale, "the file moved")
	if e.Error() != "[stale] the file moved" {
		t.Errorf("Error() = %q", e.Error())
	}
	cause := errors.New("cause")
	w := Wrap(ClassUnavailable, cause, "x %d", 1)
	if !errors.Is(w, cause) || w.Message != "x 1" {
		t.Errorf("Wrap = %+v", w)
	}
	if c, ok := ClassOf(fmt.Errorf("outer: %w", w)); !ok || c != ClassUnavailable {
		t.Errorf("ClassOf = %v %v", c, ok)
	}
	_, instErr := instance.Parse("ftp://gitlab.example.com")
	if c, ok := ClassOf(instErr); !ok || c != ClassInvalid {
		t.Errorf("ClassOf(instance error) = %v %v", c, ok)
	}
	if AsError(instErr).Class != ClassInvalid || AsError(errors.New("x")).Class != ClassUnexpected || AsError(nil) != nil {
		t.Error("AsError misclassifies")
	}
}

func TestClassifyStatus(t *testing.T) {
	get := Call{Method: "GET", Name: "get_thing"}
	post := Call{Method: "POST", Name: "create_thing"}
	merge := Call{Method: "PUT", Name: "merge", Witness: "sha"}
	json := http.Header{"Content-Type": {"application/json"}}
	cases := []struct {
		name       string
		call       Call
		repeatable bool
		status     int
		header     http.Header
		body       string
		class      Class
		contains   string
		retry      bool
	}{
		{"message string 404", get, true, 404, json, `{"message":"404 Project Not Found"}`, ClassNotFound,
			"the project was not found, or you do not have access to it", false},
		{"message 404 without resource", get, true, 404, json, `{"message":"404 Not found"}`, ClassNotFound,
			"the resource was not found, or you do not have access", false},
		{"merge request 404", get, true, 404, json, `{"message":"404 Merge Request Not Found"}`, ClassNotFound,
			"the merge request was not found", false},
		{"catch-all route", get, true, 404, json, `{"error":"404 Not Found"}`, ClassUnsupported,
			"no API route for get_thing", false},
		{"field errors 400", post, false, 400, json, `{"message":{"title":["can't be blank","is too short"],"base":["no"]}}`,
			ClassInvalid, "base: no; title: can't be blank, is too short", false},
		{"field errors 422", post, false, 422, json, `{"message":{"labels":"is invalid"}}`, ClassInvalid, "labels: is invalid", false},
		{"parameter validation", get, true, 400, json, `{"error":"state does not have a valid value"}`, ClassInvalid,
			"state does not have a valid value", false},
		{"message.error spam", post, false, 400, json, `{"message":{"error":"Your issue has been recognized as spam."}}`,
			ClassInvalid, "recognized as spam", false},
		{"files api stale", post, false, 400, json,
			`{"message":"You are attempting to update a file that has changed since you started editing it."}`,
			ClassStale, "last_commit_id", false},
		{"401", get, true, 401, json, `{"message":"401 Unauthorized"}`, ClassAuth, "gitlab-mcp login", false},
		{"401 expired", get, true, 401, json, `{"error":"invalid_token","error_description":"Token is expired."}`,
			ClassAuth, "invalid_token: Token is expired.", false},
		{"403 forbidden", get, true, 403, json, `{"message":"403 Forbidden"}`, ClassForbidden, "403 Forbidden", false},
		{"403 insufficient scope body", post, false, 403, json,
			`{"error":"insufficient_scope","error_description":"The request requires higher privileges.","scope":"api"}`,
			ClassAuth, `lacks the "api" scope`, false},
		{"403 insufficient scope header", post, false, 403,
			http.Header{"Www-Authenticate": {`Bearer realm="GitLab", error="insufficient_scope", scope="read_api"`}}, ``,
			ClassAuth, `lacks the "read_api" scope`, false},
		{"409 conflict", post, false, 409, json, `{"message":["Another open merge request already exists for this source branch"]}`,
			ClassConflict, "Another open merge request", false},
		{"409 with witness", merge, true, 409, json, `{"message":"SHA does not match HEAD of source branch"}`,
			ClassStale, "the sha you passed is no longer current", false},
		{"405 GET offset cap", get, true, 405, json, `{"message":"Offset pagination has a maximum allowed offset of 50000"}`,
			ClassInvalid, "narrow the filters", false},
		{"405 write moved", post, false, 405, json, `{"message":"405 Method Not Allowed"}`, ClassInvalid, "renamed or moved", false},
		{"413", post, false, 413, json, `{"message":"413 Request Entity Too Large"}`, ClassInvalid, "larger than GitLab accepts", false},
		{"429 plain", post, false, 429, http.Header{"Retry-After": {"7"}}, "Retry later\n", ClassRateLimited,
			"request throttle) on create_thing; retry after 7s", true},
		{"429 json", get, true, 429, http.Header{"Retry-After": {"2"}, "Content-Type": {"application/json"}},
			`{"message":{"error":"This endpoint has been requested too many times. Try again later."}}`, ClassRateLimited,
			"application limit on this endpoint) on get_thing; retry after 2s", true},
		{"429 no retry-after", get, true, 429, nil, "", ClassRateLimited, "wait a minute and retry", true},
		{"429 wait too long", get, true, 429, http.Header{"Retry-After": {"600"}}, "", ClassRateLimited, "retry after 10m0s", false},
		{"500 GET", get, true, 500, json, `{"message":"500 Internal Server Error"}`, ClassUnavailable, "status 500", true},
		{"503 POST", post, false, 503, nil, "", ClassAmbiguousOutcome, "did not confirm whether create_thing took effect", false},
		{"418 other", get, true, 418, json, `{"message":"teapot"}`, ClassInvalid, "teapot", false},
		{"long detail cut", get, true, 403, json, `{"message":"` + strings.Repeat("x", 400) + `"}`, ClassForbidden, "…", false},
		{"url in detail stripped", get, true, 403, json, `{"message":"see https://gitlab.example.com/secret/path now"}`,
			ClassForbidden, "see <url> now", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := c.header
			if h == nil {
				h = http.Header{}
			}
			v := classifyStatus(c.call, c.call.Name, c.repeatable, c.status, h, []byte(c.body))
			if v.err == nil || v.err.Class != c.class {
				t.Fatalf("verdict = %+v, want class %s", v.err, c.class)
			}
			if !strings.Contains(v.err.Message, c.contains) {
				t.Errorf("message %q lacks %q", v.err.Message, c.contains)
			}
			if v.retry != c.retry {
				t.Errorf("retry = %v, want %v", v.retry, c.retry)
			}
			if v.err.Status != c.status {
				t.Errorf("status = %d", v.err.Status)
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"":                              0,
		"5":                             5 * time.Second,
		"1.5":                           1500 * time.Millisecond,
		"-3":                            0,
		"Sat, 26 Sep 2026 12:00:30 GMT": 30 * time.Second,
		"Sat, 26 Sep 2026 11:00:00 GMT": 0,
		"soon":                          0,
	}
	for in, want := range cases {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestBackoffIsJitteredWithRetryAfterFloor(t *testing.T) {
	for retry := 1; retry <= 8; retry++ {
		for range 50 {
			d := backoff(retry, 0)
			ceiling := min(time.Second<<min(retry, 6), 32*time.Second)
			if d < 0 || d > ceiling {
				t.Fatalf("backoff(%d) = %s, ceiling %s", retry, d, ceiling)
			}
		}
	}
	if d := backoff(1, 10*time.Second); d < 10*time.Second {
		t.Errorf("backoff below Retry-After: %s", d)
	}
}

func TestFillPath(t *testing.T) {
	cases := []struct {
		template string
		args     []string
		want     string
		class    Class
	}{
		{"projects/{}/issues/{}", []string{"example-group/sub/p", "12"}, "projects/example-group%2Fsub%2Fp/issues/12", ""},
		{"projects/{}/repository/files/{}", []string{"1", "dir/a b#c?.go"}, "projects/1/repository/files/dir%2Fa%20b%23c%3F.go", ""},
		{"projects/{}/repository/branches/{}", []string{"1", "feature/x%2Fy"}, "projects/1/repository/branches/feature%2Fx%252Fy", ""},
		{"projects/{}", []string{".."}, "", ClassInvalid},
		{"projects/{}", []string{"a/../b"}, "", ClassInvalid},
		{"projects/{}", []string{"a/./b"}, "", ClassInvalid},
		{"projects/{}", []string{""}, "", ClassInvalid},
		{"projects/{}", []string{"a\nb"}, "", ClassInvalid},
		{"projects/{}", nil, "", ClassUnexpected},
	}
	for _, c := range cases {
		got, err := fillPath(c.template, c.args)
		if c.class != "" {
			if cl, _ := ClassOf(err); cl != c.class {
				t.Errorf("fillPath(%q, %q) err = %v, want %s", c.template, c.args, err, c.class)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("fillPath(%q, %q) = %q, %v; want %q", c.template, c.args, got, err, c.want)
		}
	}
}

func TestParseProject(t *testing.T) {
	cases := []struct {
		in       string
		id       int64
		path     string
		invalid  bool
		asString string
	}{
		{"2001", 2001, "", false, "2001"},
		{" example-group/alpha ", 0, "example-group/alpha", false, "example-group/alpha"},
		{"/example-group/sub/beta/", 0, "example-group/sub/beta", false, "example-group/sub/beta"},
		{"example-group%2Falpha", 0, "example-group/alpha", false, "example-group/alpha"},
		{"", 0, "", true, ""},
		{"0", 0, "", true, ""},
		{"-4", 0, "", true, ""},
		{"a/../b", 0, "", true, ""},
		{"a//b", 0, "", true, ""},
	}
	for _, c := range cases {
		p, err := ParseProject(c.in)
		if c.invalid {
			if cl, _ := ClassOf(err); cl != ClassInvalid {
				t.Errorf("ParseProject(%q) err = %v, want invalid", c.in, err)
			}
			continue
		}
		if err != nil || p.ID() != c.id || p.Path() != c.path || p.String() != c.asString {
			t.Errorf("ParseProject(%q) = %d %q %v", c.in, p.ID(), p.Path(), err)
		}
	}
	if !(Project{}).IsZero() || ProjectByID(3).IsZero() || (Group{locator{id: 3}}).segment() != "3" {
		t.Error("zero or id locators wrong")
	}
}

func TestLinkNext(t *testing.T) {
	cases := map[string]string{
		`<https://gitlab.example.com/api/v4/x?page=2>; rel="next", <https://gitlab.example.com/api/v4/x?page=1>; rel="first"`: "https://gitlab.example.com/api/v4/x?page=2",
		`<https://gitlab.example.com/a?page=1>; rel="first", <https://gitlab.example.com/a?page=3>; rel="next"`:               "https://gitlab.example.com/a?page=3",
		`<https://gitlab.example.com/a?page=1>; rel="prev"`:                                                                   "",
		`<https://gitlab.example.com/a?id_after=5>;rel=next`:                                                                  "https://gitlab.example.com/a?id_after=5",
		`garbage`: "",
	}
	for in, want := range cases {
		if got := linkNext([]string{in}); got != want {
			t.Errorf("linkNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripURL(t *testing.T) {
	cases := map[string]string{
		`Get "https://gitlab.example.com/api/v4/projects?search=x": EOF`: `Get "<url>": EOF`,
		"dial http://127.0.0.1:1/x failed":                               "dial <url> failed",
		"trailing https://gitlab.example.com/p":                          "trailing <url>",
		"no url here":                                                    "no url here",
	}
	for in, want := range cases {
		if got := stripURL(in); got != want {
			t.Errorf("stripURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeReportsDriftByPath(t *testing.T) {
	type inner struct {
		A int `json:"a"`
	}
	type outer struct {
		X     string           `json:"x"`
		In    inner            `json:"in"`
		List  []inner          `json:"list"`
		Map   map[string]inner `json:"map"`
		Free  any              `json:"free"`
		skip  int
		Named int
	}
	var got []string
	var out outer
	err := decodeReporting([]byte(`{"x":"1","new":1,"in":{"a":1,"deep":2},"list":[{"a":1,"row":3}],
		"map":{"k":{"a":1,"v":4}},"free":{"any":1},"Named":2}`), &out, func(p string) { got = append(got, p) })
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	want := []string{"outer.in.deep", "outer.list.row", "outer.map.v", "outer.new"}
	if !slices.Equal(got, want) {
		t.Errorf("drift = %v, want %v", got, want)
	}
	_ = out.skip
}

func TestRateModelLowersOnHeaders(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	m := newRateModel(600, 60, 2, func() time.Time { return now }, sleepCtx)
	if m.general.Limit() != rate.Limit(10) {
		t.Fatalf("base limit = %v", m.general.Limit())
	}
	if m.reading().Known {
		t.Error("reading known before any header")
	}
	h := http.Header{}
	h.Set("RateLimit-Remaining", "20")
	h.Set("RateLimit-Limit", "600")
	h.Set("RateLimit-Reset", fmt.Sprint(now.Add(10*time.Second).Unix()))
	m.observe(h)
	if got := m.general.Limit(); got != rate.Limit(2) {
		t.Errorf("lowered limit = %v, want 2/s", got)
	}
	r := m.reading()
	if !r.Known || r.Remaining != 20 || r.Limit != 600 || !r.Reset.Equal(now.Add(10*time.Second)) {
		t.Errorf("reading = %+v", r)
	}
	// Past the reset, the bucket returns to base on the next acquire.
	now = now.Add(11 * time.Second)
	release, err := m.acquire(context.Background(), BucketGeneral)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if got := m.general.Limit(); got != rate.Limit(10) {
		t.Errorf("restored limit = %v", got)
	}
	// Remaining 0 pauses until the reset; a deadline before it is refused
	// at once.
	h.Set("RateLimit-Remaining", "0")
	h.Set("RateLimit-Reset", fmt.Sprint(now.Add(30*time.Second).Unix()))
	m.observe(h)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = m.acquire(ctx, BucketGeneral)
	if cl, _ := ClassOf(err); cl != ClassRateLimited {
		t.Errorf("acquire during pause = %v, want rate_limited", err)
	}
	// Absent headers change nothing.
	m.observe(http.Header{})
	if !m.reading().Known {
		t.Error("an absent header erased the reading")
	}
}

func TestConcurrencyCap(t *testing.T) {
	m := newRateModel(60000, 60, 2, time.Now, sleepCtx)
	r1, err1 := m.acquire(context.Background(), BucketGeneral)
	r2, err2 := m.acquire(context.Background(), BucketGeneral)
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := m.acquire(ctx, BucketGeneral); err == nil {
		t.Error("a third slot was granted under a cap of two")
	}
	r1()
	r3, err := m.acquire(context.Background(), BucketGeneral)
	if err != nil {
		t.Fatal(err)
	}
	r2()
	r3()
}

func TestNotesBucketIsSeparate(t *testing.T) {
	m := newRateModel(60000, 12, 4, time.Now, sleepCtx)
	// The notes burst is 1 at 12 a minute, so a second note waits five
	// seconds; a deadline shorter than that is refused.
	r, err := m.acquire(context.Background(), BucketNotes)
	if err != nil {
		t.Fatal(err)
	}
	r()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := m.acquire(ctx, BucketNotes); err == nil {
		t.Error("a second note was admitted inside the notes bucket's interval")
	}
	r, err = m.acquire(ctx, BucketGeneral)
	if err != nil {
		t.Errorf("the general bucket was held by the notes bucket: %v", err)
	} else {
		r()
	}
	if BucketNotes.String() != "notes" || BucketGeneral.String() != "general" {
		t.Error("bucket names")
	}
}
