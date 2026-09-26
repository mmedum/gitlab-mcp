package render

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/model"
)

// Goldens for the readable half, from synthetic values only (§9.1). Run
// with -update to rewrite them, and read the diff.

var update = flag.Bool("update", false, "rewrite the golden files")

var (
	t0    = time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	t1    = t0.Add(26 * time.Hour)
	bd    = FixedBoundary("0123456789abcdef")
	moved = "example-group/old-alpha"
	alpha = model.ProjectRef{ID: 2001, Path: "example-group/alpha"}
)

func intp(n int) *int       { return &n }
func strp(s string) *string { return &s }
func tp(t time.Time) *time.Time {
	return &t
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from %s:\n--- got\n%s\n--- want\n%s", name, path, got, want)
	}
}

func TestGoldens(t *testing.T) {
	bob := model.User{Username: "bob", Name: "Bob Example"}
	alice := model.User{Username: "alice", Name: "Alice Example"}
	cases := map[string]string{
		"me": Me(model.Me{
			User:       model.MeUser{ID: 1001, Username: "alice", Name: "Alice Example", State: "active"},
			Instance:   model.InstanceInfo{URL: "https://gitlab.example.com", Known: true, Version: "19.4.0", Edition: "Community"},
			Token:      model.TokenInfo{Kind: "oauth", Scopes: []string{"api"}, ExpiresAt: tp(t1)},
			Registered: model.Registration{Kinds: []string{"read"}, Tools: 14},
			Rate:       model.RateReading{Known: true, Limit: 2000, Remaining: 1990, Reset: tp(t1), Observed: tp(t0)},
		}, bd),
		"resolved": Resolved(model.Resolved{Kind: "file", Project: "example-group/alpha", Ref: "feature/login",
			Path: "src/login.go", Line: intp(3), EndLine: intp(5), Tool: "get_file",
			RefCandidates: []model.RefSplit{{Ref: "feature", Path: "login/src/login.go"}},
			Arguments:     map[string]any{"project": "example-group/alpha", "ref": "feature/login", "path": "src/login.go"}}, bd),
		"project_list": ProjectList(model.ProjectList{
			Projects: []model.ProjectRow{{ID: 2001, Path: "example-group/alpha", Visibility: "public", DefaultBranch: "main",
				LastActivityAt: tp(t0), UntrustedName: "Alpha"}},
			Listing: model.Listing{Returned: 1, Total: intp(3), NextPageToken: strp("tok")},
		}, bd),
		"project": Project(model.Project{Project: model.ProjectRef{ID: 2001, Path: "example-group/alpha", MovedFrom: &moved},
			WebURL: "https://gitlab.example.com/example-group/alpha", Visibility: "public", DefaultBranch: "main",
			Topics: []string{"example"}, OpenIssues: intp(4), CreatedAt: t0, Namespace: "example-group", NamespaceKind: "group",
			UntrustedName: "Alpha", UntrustedDescription: "A generated project."}, bd),
		"item_list": ItemList(model.ItemList{Items: []model.ItemRow{{Project: alpha, IID: 12,
			Reference: "example-group/alpha#12", State: "opened", Author: "bob", Labels: []string{"bug"}, CreatedAt: t0,
			UpdatedAt: t1, UntrustedTitle: "Crash on start"}}, Listing: model.Listing{Returned: 1, Complete: true, Total: intp(1)}},
			"issues", bd),
		"issue": Issue(model.Issue{Project: alpha, IID: 12, Reference: "example-group/alpha#12",
			WebURL: "https://gitlab.example.com/example-group/alpha/-/issues/12", State: "opened", Type: "ISSUE",
			Author: bob, Assignees: []model.User{alice}, Labels: []string{"bug", "priority::high"},
			Milestone: &model.Milestone{ID: 1, Title: "v1", State: "active"}, CreatedAt: t0, UpdatedAt: t1,
			DueDate: strp("2026-02-01"), Tasks: &model.Tasks{Count: 2, Completed: 1},
			Discussions:    model.DiscussionSummary{Known: true, Threads: 2, Unresolved: 1, LastActivity: tp(t1), Complete: true},
			UntrustedTitle: "Crash on start", UntrustedDescription: "Steps:\n\n1. Start it.\n",
			DescriptionBudget: model.Budget{BudgetChars: 20000, TotalChars: 40000, ShownChars: 20000,
				ContinueOffset: intp(20000), HiddenRemoved: 3}}, bd),
		"merge_request": MergeRequest(model.MergeRequest{Project: alpha, IID: 3, Reference: "example-group/alpha!3",
			WebURL: "https://gitlab.example.com/example-group/alpha/-/merge_requests/3", State: "opened", Draft: true,
			Author: bob, Reviewers: []model.User{alice}, SourceBranch: "feature/login", TargetBranch: "main",
			SourceProjectID: 2001, SHA: "1111111111111111111111111111111111111111",
			DiffRefs: &model.DiffRefs{BaseSHA: "0000000000000000000000000000000000000000",
				StartSHA: "0000000000000000000000000000000000000000", HeadSHA: "1111111111111111111111111111111111111111"},
			DetailedMergeStatus: "not_approved", ChangesCount: "1",
			HeadPipeline: &model.Pipeline{ID: 60003, Status: "success", Ref: "feature/login"},
			Approvals:    &model.Approvals{Approved: false, Required: intp(1), Left: intp(1), ApprovedBy: []string{}},
			CreatedAt:    t0, UpdatedAt: t1, Discussions: model.DiscussionSummary{Known: true, Complete: true},
			UntrustedTitle: "Add login", UntrustedDescription: "Adds a stub.",
			DescriptionBudget: model.Budget{BudgetChars: 20000, TotalChars: 12, ShownChars: 12}}, bd),
		"discussions": Discussions(model.Discussions{Project: alpha, IID: 3, Type: "merge_request", Budget: 30000,
			Threads: []model.Thread{{ID: "aaaa", Resolvable: true, LastActivity: t1,
				Position: &model.DiffPosition{NewPath: "src/login.go", NewLine: intp(3), HeadSHA: "1111111111"},
				Notes: []model.Note{{ID: 50001, Author: bob, CreatedAt: t0, UntrustedBody: "Add a test?",
					Budget: model.Budget{BudgetChars: 6000, TotalChars: 9000, ShownChars: 6000, ContinueOffset: intp(6000)}}}}},
			NotShown: []model.ThreadStub{{ID: "bbbb", Author: "carol", Notes: 2, LastActivity: t0}},
			Listing:  model.Listing{Returned: 1, Total: intp(2), NextPageToken: strp("tok")}}, bd),
		"file": File(model.File{Project: alpha, Path: "src/main.go", Ref: "main", Size: 29, BlobID: "b10b",
			LastCommitID: "c0ffee", CommitID: "c0ffee", SHA256: "5ha", ContentType: "text/plain; charset=utf-8",
			UntrustedContent: "package main\n\nfunc main() {}\n",
			Budget:           model.Budget{BudgetChars: 60000, TotalChars: 29, ShownChars: 29}}, bd),
		"file_binary": File(model.File{Project: alpha, Path: "assets/logo.png", Ref: "main", Size: 16, Binary: true,
			ContentType: "image/png"}, bd),
		"tree": Tree(model.Tree{Project: alpha, Path: "docs", Entries: []model.TreeEntry{
			{Path: "docs/pages", Type: "tree"}, {Path: "docs/guide.md", Type: "blob"}},
			Listing: model.Listing{Returned: 2, NextPageToken: strp("tok")}}, bd),
		"branches": Branches(model.Branches{Project: alpha, Branches: []model.Branch{{Name: "main", Default: true,
			Protected: true, CanPush: true, CommitID: "1234567890abcdef", CommittedAt: t0, UntrustedCommitTitle: "Update"}},
			Listing: model.Listing{Returned: 1, Complete: true, Total: intp(1)}}, bd),
		"commits": Commits(model.Commits{Project: alpha, Commits: []model.CommitRow{{ID: "1234567890abcdef",
			AuthorName: "Alice Example", CommittedAt: t0, Parents: 2, UntrustedTitle: "Merge branch"}},
			Listing: model.Listing{Returned: 1, Complete: true}}, bd),
		"commit": Commit(model.Commit{Project: alpha, ID: "1234567890abcdef", ShortID: "12345678",
			WebURL: "https://gitlab.example.com/example-group/alpha/-/commit/1234567890abcdef", AuthorName: "Alice Example",
			AuthoredAt: t0, CommitterName: "Alice Example", CommittedAt: t0, ParentIDs: []string{"0000"}, Additions: 1,
			UntrustedMessage: "Add login\n",
			MessageBudget:    model.Budget{BudgetChars: 8000, TotalChars: 9000, ShownChars: 10, ContinueOffset: intp(10)}, Files: []model.FileDiff{{NewPath: "src/login.go", Status: "added",
				UntrustedDiff: "@@ -0,0 +1 @@\n+package main\n"}},
			NotShown:       []model.FileChange{{NewPath: "big.bin", Status: "modified", Reason: "too_large"}, {NewPath: "b.go", Status: "modified", Reason: "budget"}},
			NextFileOffset: intp(2), FilesComplete: true, DiffBudget: 40000}, bd),
	}
	for name, got := range cases {
		golden(t, name, got)
	}
}
