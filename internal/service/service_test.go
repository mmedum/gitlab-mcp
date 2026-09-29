package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/internal/config"
	"github.com/mmedum/gitlab-mcp/v2/internal/gapi"
	"github.com/mmedum/gitlab-mcp/v2/internal/gapi/gitlabtest"
	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/v2/internal/instance"
	"github.com/mmedum/gitlab-mcp/v2/internal/render"
	"github.com/mmedum/gitlab-mcp/v2/internal/scopes"
)

const alphaID = 2001

func newService(t *testing.T, o gitlabtest.Options, cfg config.Config) (*Service, *gitlabtest.Server) {
	t.Helper()
	gl := gitlabtest.New(t, o)
	inst, err := instance.Parse(gl.URL)
	if err != nil {
		t.Fatal(err)
	}
	c, err := gapi.New(gapi.Options{Instance: inst, Tokens: gapi.StaticToken(gl.Token()),
		Sleep: func(context.Context, time.Duration) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{Client: c, Config: cfg}), gl
}

func class(err error) gapi.Class {
	c, _ := gapi.ClassOf(err)
	return c
}

func TestParseProject(t *testing.T) {
	s, gl := newService(t, gitlabtest.Options{}, config.Config{})
	host := strings.TrimPrefix(gl.URL, "http://")
	cases := []struct {
		in       string
		id       int64
		path     string
		wantFail bool
	}{
		{"2001", 2001, "", false},
		{"example-group/alpha", 0, "example-group/alpha", false},
		{gl.URL + "/example-group/sub/beta/-/issues/1", 0, "example-group/sub/beta", false},
		{host + "/example-group/alpha", 0, "example-group/alpha", false},
		{"https://other.example.com/example-group/alpha", 0, "", true},
		{"", 0, "", true},
	}
	for _, c := range cases {
		p, err := s.parseProject(c.in)
		if (err != nil) != c.wantFail || (err == nil && (p.ID() != c.id || p.Path() != c.path)) {
			t.Errorf("parseProject(%q) = %v %q, %v", c.in, p.ID(), p.Path(), err)
		}
	}
}

func TestResolveURLListsCandidatesItCannotSettle(t *testing.T) {
	s, gl := newService(t, gitlabtest.Options{}, config.Config{})
	s.SetRegistered([]Registered{{Name: "get_file", Kind: scopes.KindRead}})
	// "v1/docs" is no branch: the shortest split is returned, with the
	// others listed.
	r, err := s.ResolveURL(t.Context(), gl.URL+"/example-group/alpha/-/blob/v1/docs/guide.md")
	if err != nil {
		t.Fatal(err)
	}
	if r.Ref != "v1" || r.Path != "docs/guide.md" || len(r.RefCandidates) != 1 || r.RefCandidates[0].Ref != "v1/docs" {
		t.Errorf("resolved = %+v", r)
	}
	if r.Tool != "get_file" {
		t.Errorf("tool = %q", r.Tool)
	}
	// A SHA needs no lookup.
	gl.ResetRequests()
	sha := strings.Repeat("a", 40)
	if r, err = s.ResolveURL(t.Context(), gl.URL+"/example-group/alpha/-/blob/"+sha+"/src/main.go"); err != nil || r.Ref != sha {
		t.Errorf("sha ref = %+v, %v", r, err)
	}
	if n := len(gl.Requests()); n != 0 {
		t.Errorf("%d requests for a SHA ref, want none", n)
	}
	// Not registered: no tool is suggested.
	r, _ = s.ResolveURL(t.Context(), gl.URL+"/example-group/alpha/-/issues/1")
	if r.Tool != "" || len(r.Arguments) != 0 {
		t.Errorf("suggested an unregistered tool: %+v", r)
	}
}

// GitLab's expires_in is the seconds left, so a token read an hour into
// its two expires an hour after the read, not an hour after it was issued.
func TestMeReportsExpiryFromTheSecondsLeft(t *testing.T) {
	issued := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := issued
	s, _ := newService(t, gitlabtest.Options{Now: func() time.Time { return clock }, AccessTokenTTL: 2 * time.Hour}, config.Config{})
	clock = issued.Add(time.Hour)
	before := time.Now()
	me, err := s.Me(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now()
	lo, hi := before.Add(time.Hour).Truncate(time.Second), after.Add(time.Hour)
	if me.Token.ExpiresAt == nil || me.Token.ExpiresAt.Before(lo) || me.Token.ExpiresAt.After(hi) {
		t.Errorf("expires_at = %v, want between %v and %v", me.Token.ExpiresAt, lo, hi)
	}
}

func TestMeReadsMetadataWhenStartupCouldNot(t *testing.T) {
	s, gl := newService(t, gitlabtest.Options{Version: "18.2.1", Enterprise: true}, config.Config{WriteNamespaces: []string{"example-group"}})
	s.SetRegistered([]Registered{{Name: "get_me", Kind: scopes.KindRead}, {Name: "x", Kind: scopes.KindWrite}})
	me, err := s.Me(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if me.Instance.Version != "18.2.1" || me.Instance.Edition != "Enterprise" || !me.WriteScope.Confined ||
		fmt.Sprint(me.Registered.Kinds) != "[read write]" || me.Token.ExpiresAt == nil {
		t.Errorf("me = %+v", me)
	}
	// Once read, the metadata is kept.
	gl.ResetRequests()
	if me, err = s.Me(t.Context()); err != nil || me.Instance.Version != "18.2.1" {
		t.Fatalf("second get_me: %+v, %v", me, err)
	}
	for _, r := range gl.Requests() {
		if strings.HasSuffix(r.EscapedPath, "/metadata") {
			t.Errorf("the second get_me read the metadata again")
		}
	}
	// Without a client every call is [auth].
	if _, err := New(Options{}).Me(t.Context()); class(err) != gapi.ClassAuth {
		t.Errorf("signed out: %v", err)
	}
}

func TestDiscussionsOverTheBudget(t *testing.T) {
	s, gl := newService(t, gitlabtest.Options{}, config.Config{})
	long := strings.Repeat("word ", render.NoteBudget/5+100) // over one note's budget
	var threads []gitlab.Discussion
	for i := range 8 {
		at := time.Date(2026, 1, 5, 9, i, 0, 0, time.UTC)
		threads = append(threads, gitlab.Discussion{ID: fmt.Sprintf("t%d", i), Notes: []gitlab.Note{{ID: int64(100 + i),
			Body: long, Author: gitlab.UserBasic{Username: "bob"}, CreatedAt: at, UpdatedAt: at}}})
	}
	body, _ := json.Marshal(threads)
	path := fmt.Sprintf("/projects/%d/issues/1/discussions", alphaID)
	inject := func() {
		gl.Inject(gitlabtest.Fault{Method: http.MethodGet, Path: path, Status: http.StatusOK, Body: string(body)})
	}
	inject()
	d, err := s.ListDiscussions(t.Context(), DiscussionQuery{Project: "2001", Type: "issue", IID: 1})
	if err != nil {
		t.Fatal(err)
	}
	// Each note is cut to 6,000 characters; five fit in 30,000.
	if len(d.Threads) != 5 || len(d.NotShown) != 3 || d.NotShown[0].ID != "t2" || d.Listing.NextPageToken == nil {
		t.Fatalf("threads %d, not shown %v, listing %+v", len(d.Threads), d.NotShown, d.Listing)
	}
	if d.Threads[0].ID != "t7" || d.Threads[0].Notes[0].Budget.ContinueOffset == nil {
		t.Errorf("first thread = %+v", d.Threads[0])
	}
	// The next page starts with the first thread not shown.
	inject()
	next, err := s.ListDiscussions(t.Context(), DiscussionQuery{Project: "2001", Type: "issue", IID: 1, PageToken: *d.Listing.NextPageToken})
	if err != nil {
		t.Fatal(err)
	}
	if next.Threads[0].ID != "t2" || !next.Listing.Complete {
		t.Errorf("next page = %v %+v", next.Threads[0].ID, next.Listing)
	}
	// A cut comment continues by note_id and offset.
	inject()
	one, err := s.ListDiscussions(t.Context(), DiscussionQuery{Project: "2001", Type: "issue", IID: 1, NoteID: 107,
		Offset: *d.Threads[0].Notes[0].Budget.ContinueOffset})
	if err != nil {
		t.Fatal(err)
	}
	if b := one.Threads[0].Notes[0].Budget; b.Offset != *d.Threads[0].Notes[0].Budget.ContinueOffset || b.ContinueOffset != nil {
		t.Errorf("continued comment budget = %+v", b)
	}
	// A token from another query is refused.
	inject()
	_, err = s.ListDiscussions(t.Context(), DiscussionQuery{Project: "2001", Type: "issue", IID: 1, IncludeSystem: true,
		PageToken: *d.Listing.NextPageToken})
	if class(err) != gapi.ClassInvalid {
		t.Errorf("foreign token: %v", err)
	}
}

func TestCommitDiffBudget(t *testing.T) {
	s, gl := newService(t, gitlabtest.Options{}, config.Config{})
	c, _, err := s.client.ListCommits(t.Context(), gapi.ProjectByID(alphaID), gapi.CommitQuery{}, gapi.ListOptions{PerPage: 1})
	if err != nil {
		t.Fatal(err)
	}
	sha := c[0].ID
	big := "@@ -1 +1 @@\n" + strings.Repeat("+line\n", render.DiffBudget/6/3) // three are just over the budget
	diffs := []gitlab.Diff{
		{OldPath: "a", NewPath: "a", Diff: big},
		{OldPath: "b", NewPath: "b", TooLarge: true},
		{OldPath: "c", NewPath: "c", Diff: big},
		{OldPath: "d", NewPath: "d", Diff: big + big},
	}
	body, _ := json.Marshal(diffs)
	gl.Inject(gitlabtest.Fault{Method: http.MethodGet, Path: fmt.Sprintf("/projects/%d/repository/commits/%s/diff", alphaID, sha),
		Status: http.StatusOK, Body: string(body)})
	out, err := s.GetCommit(t.Context(), "2001", sha, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// a and c fit, b is too large, d is over the budget.
	if len(out.Files) != 2 || len(out.NotShown) != 2 || out.NotShown[0].Reason != "too_large" ||
		out.NotShown[1].Reason != "budget" || out.NextFileOffset == nil || *out.NextFileOffset != 3 {
		t.Errorf("files %d, not shown %+v, next %v", len(out.Files), out.NotShown, out.NextFileOffset)
	}
}

// TestCommitMessageBudget: a message cut at the budget says so, with its
// length and where to continue, so it is never mistaken for a short one.
func TestCommitMessageBudget(t *testing.T) {
	s, gl := newService(t, gitlabtest.Options{}, config.Config{})
	c, _, err := s.client.ListCommits(t.Context(), gapi.ProjectByID(alphaID), gapi.CommitQuery{}, gapi.ListOptions{PerPage: 1})
	if err != nil {
		t.Fatal(err)
	}
	sha := c[0].ID
	message := strings.Repeat("A line of the message.\n", 500) // 11,500 characters
	body, _ := json.Marshal(gitlab.Commit{ID: sha, ShortID: sha[:8], Message: message})
	inject := func() {
		// The merge requests' fault goes first: the commit's path is a
		// prefix of it.
		gl.Inject(gitlabtest.Fault{Method: http.MethodGet, Path: fmt.Sprintf("/projects/%d/repository/commits/%s/merge_requests",
			alphaID, sha), Status: http.StatusOK, Body: "[]"})
		gl.Inject(gitlabtest.Fault{Method: http.MethodGet, Path: fmt.Sprintf("/projects/%d/repository/commits/%s", alphaID, sha),
			Status: http.StatusOK, Body: string(body)})
	}
	inject()
	out, err := s.GetCommit(t.Context(), "2001", sha, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	b := out.MessageBudget
	if b.TotalChars != 11500 || b.BudgetChars != render.CommitMessageBudget || b.ShownChars != len([]rune(out.UntrustedMessage)) ||
		b.ShownChars > render.CommitMessageBudget || b.ContinueOffset == nil || *b.ContinueOffset != b.ShownChars {
		t.Fatalf("message budget = %+v, shown %d characters", b, len([]rune(out.UntrustedMessage)))
	}
	inject()
	rest, err := s.GetCommit(t.Context(), "2001", sha, 0, 0, *b.ContinueOffset)
	if err != nil {
		t.Fatal(err)
	}
	if out.UntrustedMessage+rest.UntrustedMessage != message || rest.MessageBudget.ContinueOffset != nil {
		t.Errorf("continued message budget = %+v", rest.MessageBudget)
	}
	inject()
	if _, err := s.GetCommit(t.Context(), "2001", sha, 0, 0, 20000); class(err) != gapi.ClassInvalid {
		t.Errorf("offset past the end: %v", err)
	}
}

// TestCommitCountsHiddenTextOnce: the message's hidden characters are
// in its budget, the diffs' in the commit's own count.
func TestCommitCountsHiddenTextOnce(t *testing.T) {
	s, gl := newService(t, gitlabtest.Options{}, config.Config{})
	c, _, err := s.client.ListCommits(t.Context(), gapi.ProjectByID(alphaID), gapi.CommitQuery{}, gapi.ListOptions{PerPage: 1})
	if err != nil {
		t.Fatal(err)
	}
	sha := c[0].ID
	commit, _ := json.Marshal(gitlab.Commit{ID: sha, ShortID: sha[:8], Message: "Fix\u202e it\n"})
	diffs, _ := json.Marshal([]gitlab.Diff{{OldPath: "a", NewPath: "a", Diff: "+x\u200b\u2066\n"}})
	// The diff and merge request faults go first: the commit's path is a
	// prefix of both.
	gl.Inject(gitlabtest.Fault{Method: http.MethodGet, Path: fmt.Sprintf("/projects/%d/repository/commits/%s/diff", alphaID, sha),
		Status: http.StatusOK, Body: string(diffs)})
	gl.Inject(gitlabtest.Fault{Method: http.MethodGet, Path: fmt.Sprintf("/projects/%d/repository/commits/%s/merge_requests",
		alphaID, sha), Status: http.StatusOK, Body: "[]"})
	gl.Inject(gitlabtest.Fault{Method: http.MethodGet, Path: fmt.Sprintf("/projects/%d/repository/commits/%s", alphaID, sha),
		Status: http.StatusOK, Body: string(commit)})
	out, err := s.GetCommit(t.Context(), "2001", sha, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if out.MessageBudget.HiddenRemoved != 1 || out.HiddenRemoved != 2 {
		t.Errorf("message %d, diffs %d; want 1 and 2", out.MessageBudget.HiddenRemoved, out.HiddenRemoved)
	}
}

// TestProjectIDIsReadOncePerProcess: a numeric id's path is read once,
// and a later call takes it from the process cache.
func TestProjectIDIsReadOncePerProcess(t *testing.T) {
	s, gl := newService(t, gitlabtest.Options{}, config.Config{})
	count := func() int {
		n := 0
		for _, r := range gl.Requests() {
			if r.EscapedPath == fmt.Sprintf("/api/v4/projects/%d", alphaID) {
				n++
			}
		}
		return n
	}
	for range 2 {
		if _, err := s.ListBranches(t.Context(), "2001", "", gapi.ListOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(); n != 1 {
		t.Errorf("the project was read %d times, want once", n)
	}
}

func TestSummaryCounts(t *testing.T) {
	yes := gitlab.Note{Resolvable: true, Resolved: true}
	no := gitlab.Note{Resolvable: true}
	plain := gitlab.Note{}
	cases := []struct {
		notes                []gitlab.Note
		resolvable, resolved bool
	}{
		{[]gitlab.Note{yes, yes}, true, true},
		{[]gitlab.Note{yes, no}, true, false},
		{[]gitlab.Note{plain}, false, false},
	}
	for i, c := range cases {
		a, b := resolution(gitlab.Discussion{Notes: c.notes})
		if a != c.resolvable || b != c.resolved {
			t.Errorf("case %d: %v %v", i, a, b)
		}
	}
}

func TestProtectedMatchIsGitLabsWildcard(t *testing.T) {
	for _, c := range []struct {
		rule, branch string
		want         bool
	}{
		{"main", "main", true}, {"main", "main2", false},
		{"release/*", "release/1.0", true}, {"release/*", "release/a/b", true}, {"release/*", "release", false},
		{"*-stable", "13-stable", true}, {"*-stable", "13-stable-x", false},
		{"*", "anything", true}, {"a*b*c", "axxbyyc", true}, {"a*b*c", "axxcyyb", false}, {"ab*ab", "ab", false},
	} {
		if got := protectedMatch(c.rule, c.branch); got != c.want {
			t.Errorf("%q against %q = %v", c.rule, c.branch, got)
		}
	}
}

func TestParallelRunsAllAndReportsTheFirstErrorInOrder(t *testing.T) {
	var ran [3]bool
	first, second := errors.New("first"), errors.New("second")
	err := parallel(
		func() error { time.Sleep(20 * time.Millisecond); ran[0] = true; return first },
		func() error { ran[1] = true; return second },
		func() error { ran[2] = true; return nil },
	)
	if !errors.Is(err, first) || ran != [3]bool{true, true, true} {
		t.Errorf("err %v, ran %v", err, ran)
	}
	if parallel() != nil {
		t.Error("nothing to run failed")
	}
}

func TestSuppliedLintContentMayNotInclude(t *testing.T) {
	for name, content := range map[string]string{
		"top level":       "include:\n  - remote: https://example.invalid/x.yml\n",
		"shorthand":       "include: 'https://example.invalid/x.yml'\n",
		"trigger":         "deploy:\n  trigger:\n    include: https://example.invalid/x.yml\n",
		"escaped key":     "\"\\u0069nclude\": https://example.invalid/x.yml\n",
		"flow mapping":    "{include: https://example.invalid/x.yml}\n",
		"anchored":        ".base: &b\n  include: x.yml\njob:\n  <<: *b\n",
		"second document": "a: 1\n---\ninclude: x.yml\n",
		"does not parse":  "include: [unclosed\n",
		"tab indentation": "job:\n\tinclude: x.yml\n",
	} {
		if err := refuseIncludes(content); !gapi.IsClass(err, gapi.ClassBlocked) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, content := range map[string]string{
		"plain":           "build:\n  script:\n    - echo hi\n",
		"include as text": "build:\n  script:\n    - echo include me\n",
		"empty":           "",
	} {
		if err := refuseIncludes(content); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestWithDraftIsGitLabsRule(t *testing.T) {
	for _, c := range []struct {
		title string
		draft bool
		want  string
	}{
		{"Fix it", true, "Draft: Fix it"},
		{"Draft: Fix it", true, "Draft: Fix it"},
		{"[Draft] Fix it", true, "[Draft] Fix it"},
		{"Draft release notes", true, "Draft: Draft release notes"},
		{"WIP: cleanup", true, "Draft: WIP: cleanup"},
		{"Draft: Fix it", false, "Fix it"},
		{"(draft) [DRAFT] draft: Fix it", false, "Fix it"},
		{"Draft release notes", false, "Draft release notes"},
		{"WIP: cleanup", false, "WIP: cleanup"},
	} {
		if got := withDraft(c.title, c.draft); got != c.want {
			t.Errorf("withDraft(%q, %v) = %q, want %q", c.title, c.draft, got, c.want)
		}
	}
}

// An issue row in closes_issues and related_issues has no references;
// the full one is spelled from its web URL, or left empty.
func TestIssueReference(t *testing.T) {
	cases := map[string]string{
		"https://gitlab.example.com/example-group/sub/beta/-/issues/12":      "example-group/sub/beta#12",
		"https://gitlab.example.com/example-group/alpha/-/work_items/12":     "example-group/alpha#12",
		"https://gitlab.example.com/example-group/alpha/-/issues/13":         "",
		"https://gitlab.example.com/groups/example-group/-/work_items/12":    "",
		"https://gitlab.example.com/example-group/alpha/-/merge_requests/12": "",
		"": "",
	}
	for raw, want := range cases {
		if got := issueReference(raw, 12); got != want {
			t.Errorf("issueReference(%q) = %q, want %q", raw, got, want)
		}
	}
}
