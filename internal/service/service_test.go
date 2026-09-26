package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/config"
	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/gapi/gitlabtest"
	"github.com/mmedum/gitlab-mcp/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/internal/instance"
	"github.com/mmedum/gitlab-mcp/internal/render"
	"github.com/mmedum/gitlab-mcp/internal/scopes"
)

type staticTokens string

func (s staticTokens) Token(context.Context) (string, error) { return string(s), nil }

const alphaID = 2001

func newService(t *testing.T, o gitlabtest.Options, cfg config.Config) (*Service, *gitlabtest.Server) {
	t.Helper()
	gl := gitlabtest.New(t, o)
	inst, err := instance.Parse(gl.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	c, err := gapi.New(gapi.Options{Instance: inst, Tokens: staticTokens(gl.Token()),
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

func TestMeReadsMetadataWhenStartupCouldNot(t *testing.T) {
	s, _ := newService(t, gitlabtest.Options{Version: "18.2.1", Enterprise: true}, config.Config{WriteNamespaces: []string{"example-group"}})
	s.SetRegistered([]Registered{{Name: "get_me", Kind: scopes.KindRead}, {Name: "x", Kind: scopes.KindWrite}})
	me, err := s.Me(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if me.Instance.Version != "18.2.1" || me.Instance.Edition != "Enterprise" || !me.WriteScope.Confined ||
		fmt.Sprint(me.Registered.Kinds) != "[read write]" || me.Token.ExpiresAt == nil {
		t.Errorf("me = %+v", me)
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
	d, err := s.ListDiscussions(t.Context(), DiscussionQuery{Project: "2001", IID: 1})
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
	next, err := s.ListDiscussions(t.Context(), DiscussionQuery{Project: "2001", IID: 1, PageToken: *d.Listing.NextPageToken})
	if err != nil {
		t.Fatal(err)
	}
	if next.Threads[0].ID != "t2" || !next.Listing.Complete {
		t.Errorf("next page = %v %+v", next.Threads[0].ID, next.Listing)
	}
	// A cut comment continues by note_id and offset.
	inject()
	one, err := s.ListDiscussions(t.Context(), DiscussionQuery{Project: "2001", IID: 1, NoteID: 107,
		Offset: *d.Threads[0].Notes[0].Budget.ContinueOffset})
	if err != nil {
		t.Fatal(err)
	}
	if b := one.Threads[0].Notes[0].Budget; b.Offset != *d.Threads[0].Notes[0].Budget.ContinueOffset || b.ContinueOffset != nil {
		t.Errorf("continued comment budget = %+v", b)
	}
	// A token from another query is refused.
	inject()
	_, err = s.ListDiscussions(t.Context(), DiscussionQuery{Project: "2001", IID: 1, IncludeSystem: true,
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
	out, err := s.GetCommit(t.Context(), "2001", sha, 0, 0)
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
		gl.Inject(gitlabtest.Fault{Method: http.MethodGet, Path: fmt.Sprintf("/projects/%d/repository/commits/%s", alphaID, sha),
			Status: http.StatusOK, Body: string(body)})
	}
	inject()
	out, err := s.GetCommit(t.Context(), "2001", sha, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	b := out.MessageBudget
	if b.TotalChars != 11500 || b.BudgetChars != render.CommitMessageBudget || b.ShownChars != len([]rune(out.UntrustedMessage)) ||
		b.ShownChars > render.CommitMessageBudget || b.ContinueOffset == nil || *b.ContinueOffset != b.ShownChars {
		t.Fatalf("message budget = %+v, shown %d characters", b, len([]rune(out.UntrustedMessage)))
	}
	inject()
	rest, err := s.GetCommit(t.Context(), "2001", sha, 0, *b.ContinueOffset)
	if err != nil {
		t.Fatal(err)
	}
	if out.UntrustedMessage+rest.UntrustedMessage != message || rest.MessageBudget.ContinueOffset != nil {
		t.Errorf("continued message budget = %+v", rest.MessageBudget)
	}
	inject()
	if _, err := s.GetCommit(t.Context(), "2001", sha, 0, 20000); class(err) != gapi.ClassInvalid {
		t.Errorf("offset past the end: %v", err)
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
