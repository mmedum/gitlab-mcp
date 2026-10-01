package render

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/internal/model"
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
func boolp(b bool) *bool    { return &b }
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
			UntrustedName: "Alpha", UntrustedDescription: "A generated project.",
			DescriptionBudget: model.Budget{BudgetChars: 20000, TotalChars: 20, ShownChars: 20}}, bd),
		"item_list": ItemList(model.ItemList{Items: []model.ItemRow{{Project: alpha, IID: 12,
			Reference: "example-group/alpha#12", State: "opened", Author: "bob", Labels: []string{"bug"}, CreatedAt: t0,
			UpdatedAt: t1, UntrustedTitle: "Crash on start"}}, Listing: model.Listing{Returned: 1, Complete: true, Total: intp(1)}},
			"issues", bd),
		"issue": Issue(model.Issue{Project: alpha, IID: 12, Reference: "example-group/alpha#12",
			WebURL: "https://gitlab.example.com/example-group/alpha/-/issues/12", State: "opened", Type: "ISSUE",
			Author: bob, Assignees: []model.User{alice}, Labels: []string{"bug", "priority::high"},
			Milestone: &model.Milestone{ID: 1, Title: "v1", State: "active"}, CreatedAt: t0, UpdatedAt: t1,
			DueDate: strp("2026-02-01"), Tasks: &model.Tasks{Count: 2, Completed: 1}, Upvotes: 3, Downvotes: 1,
			TimeStats:      model.TimeStats{TimeEstimate: 12600, TotalTimeSpent: 36000, HumanTimeEstimate: "3h 30m", HumanTotalTimeSpent: "1d 2h"},
			Discussions:    model.DiscussionSummary{Known: true, Threads: 2, Unresolved: 1, LastActivity: tp(t1), Complete: true},
			UntrustedTitle: "Crash on start", UntrustedDescription: "Steps:\n\n1. Start it.\n",
			DescriptionBudget: model.Budget{BudgetChars: 20000, TotalChars: 40000, ShownChars: 20000,
				ContinueOffset: intp(20000), HiddenRemoved: 3},
			RelatedMergeRequests: &model.LinkedItems{Items: []model.LinkedItem{{Reference: "example-group/alpha!3", IID: 3,
				ProjectID: 2001, State: "merged", WebURL: "https://gitlab.example.com/example-group/alpha/-/merge_requests/3",
				UntrustedTitle: "Fix crash on start"}}, Total: intp(1)},
			ClosingMergeRequests: &model.LinkedItems{Items: []model.LinkedItem{}, Total: intp(0)}}, bd),
		"merge_request": MergeRequest(model.MergeRequest{Project: alpha, IID: 3, Reference: "example-group/alpha!3",
			WebURL: "https://gitlab.example.com/example-group/alpha/-/merge_requests/3", State: "opened", Draft: true,
			Author: bob, Reviewers: []model.User{alice}, SourceBranch: "feature/login", TargetBranch: "main",
			SourceProjectID: 2001, SHA: "1111111111111111111111111111111111111111",
			DiffRefs: &model.DiffRefs{BaseSHA: "0000000000000000000000000000000000000000",
				StartSHA: "0000000000000000000000000000000000000000", HeadSHA: "1111111111111111111111111111111111111111"},
			DetailedMergeStatus: "not_approved", ChangesCount: "1",
			HeadPipeline: &model.Pipeline{ID: 60003, Status: "success", Ref: "feature/login"},
			Approvals:    &model.Approvals{Approved: false, Required: intp(1), Left: intp(1), ApprovedBy: []string{}},
			ReviewerStates: &model.ReviewerStates{Reviewers: []model.ReviewerState{{User: alice, State: "requested_changes",
				AddedAt: t0}}, Total: intp(1)},
			CreatedAt: t0, UpdatedAt: t1, Discussions: model.DiscussionSummary{Known: true, Complete: true},
			UntrustedTitle: "Add login", UntrustedDescription: "Adds a stub.",
			DescriptionBudget: model.Budget{BudgetChars: 20000, TotalChars: 12, ShownChars: 12},
			ClosesIssues: &model.LinkedItems{Items: []model.LinkedItem{{Reference: "example-group/alpha#12", IID: 12,
				ProjectID: 2001, State: "opened", WebURL: "https://gitlab.example.com/example-group/alpha/-/issues/12",
				UntrustedTitle: "Crash on start"}}, More: true, Total: intp(21)},
			RelatedIssues: &model.LinkedItems{Items: []model.LinkedItem{{External: true, ExternalID: strp("EXT-7"),
				UntrustedTitle: "External Issue EXT-7"}, {External: true, UntrustedTitle: "External Issue not an id"}},
				Total: intp(2)}}, bd),
		"discussions": Discussions(model.Discussions{Project: alpha, IID: 3, Type: "merge_request", Budget: 30000,
			Threads: []model.Thread{{ID: "aaaa", Resolvable: true, LastActivity: t1,
				Position: &model.DiffPosition{NewPath: "src/login.go", NewLine: intp(3), HeadSHA: "1111111111"},
				Notes: []model.Note{{ID: 50001, Author: bob, CreatedAt: t0, UntrustedBody: "Add a test?",
					Budget: model.Budget{BudgetChars: 6000, TotalChars: 9000, ShownChars: 6000, ContinueOffset: intp(6000)}}}}},
			NotShown: []model.ThreadStub{{ID: "bbbb", Author: "carol", Notes: 2, LastActivity: t0}},
			Listing:  model.Listing{Returned: 1, Total: intp(2), NextPageToken: strp("tok")}}, bd),
		"boards": Boards(model.Boards{Project: alpha, BudgetChars: 30000, Listing: model.Listing{Returned: 2, NextPageToken: strp("tok")},
			Boards: []model.Board{
				{ID: 7, UntrustedName: "Team <<<board>>>", OpenList: true, ClosedList: false, Lists: []model.BoardList{
					{ID: 71, Position: intp(0), Kind: "label", Label: strp("workflow::doing"),
						SearchIssues: &model.ListSearch{State: "opened", Labels: []string{"workflow::doing"}}},
					{ID: 72, Position: intp(1), Kind: "iteration", Iteration: &model.BoardTimebox{ID: 5, UntrustedTitle: "Week 2"},
						SearchNote: "search_issues has no iteration filter"},
					{ID: 73, Position: intp(2), Kind: "unknown", SearchNote: "its kind is unknown"},
				}},
				{ID: 8, UntrustedName: "Scoped", OpenList: false, ClosedList: true,
					Scope: &model.BoardScope{Milestone: &model.BoardTimebox{ID: 90001, UntrustedTitle: "Sprint 2"}, Assignee: strp("bob"),
						Labels: []string{"bug"}, NoWeight: true},
					Lists: []model.BoardList{
						{ID: 81, Position: intp(0), Kind: "assignee", Assignee: strp("carol"),
							SearchIssues: &model.ListSearch{State: "opened", Labels: []string{}, Assignee: strp("carol")}},
						{ID: 82, Position: intp(1), Kind: "milestone", Milestone: &model.BoardTimebox{ID: 90001, UntrustedTitle: "Sprint 2"},
							SearchIssues: &model.ListSearch{State: "opened", Labels: []string{}, UntrustedMilestone: strp("Sprint 2")}},
					}},
			}}, bd),
		"item_events": ItemEvents(model.ItemEvents{Project: alpha, IID: 12, Type: "issue", Since: tp(t0),
			Events: []model.ItemEvent{
				{Kind: "weight", ID: 4, CreatedAt: t1.Add(3 * time.Hour), User: &bob, Weight: intp(3)},
				{Kind: "milestone", ID: 3, CreatedAt: t1.Add(2 * time.Hour), User: &bob, Action: "remove",
					Milestone: &model.Milestone{ID: 90001, Title: "Sprint <<<2>>>", State: "active"}},
				{Kind: "state", ID: 2, CreatedAt: t1.Add(time.Hour), User: &bob, State: strp("closed"),
					SourceCommit: strp("1234567890abcdef1234")},
				{Kind: "label", ID: 7, CreatedAt: t1, User: &bob, Action: "add", Label: strp("priority::high")},
				{Kind: "label", ID: 6, CreatedAt: t1, Action: "remove", LabelDeleted: true},
			},
			Listing: model.Listing{Returned: 5, NextPageToken: strp("tok")}}, bd),
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
			NextFileOffset: intp(2), FilesComplete: true, DiffBudget: 40000,
			MergeRequests: &model.LinkedItems{Items: []model.LinkedItem{{Reference: "example-group/alpha!3", IID: 3,
				ProjectID: 2001, State: "opened", WebURL: "https://gitlab.example.com/example-group/alpha/-/merge_requests/3",
				UntrustedTitle: "Add login"}}, More: true}}, bd),
	}
	for name, got := range cases {
		golden(t, name, got)
	}
}

// The phase-1 renderers.
func TestGoldensPhase1(t *testing.T) {
	bob := model.User{Username: "bob", Name: "Bob Example"}
	yes := true
	diffs := model.Diffs{Files: []model.FileDiff{{NewPath: "src/login.go", Status: "added",
		UntrustedDiff: "@@ -0,0 +1 @@\n+package main\n"}},
		NotShown:      []model.FileChange{{NewPath: "vendor/big.txt", Status: "modified", Reason: "too_large"}},
		FilesComplete: false, DiffBudget: 40000, HiddenRemoved: 1}
	job := model.JobRow{ID: 70002, Name: "unit tests", Stage: "test", Status: "failed", FailureReason: "script_failure",
		CreatedAt: t0, StartedAt: tp(t0), FinishedAt: tp(t1), Duration: func() *float64 { d := 120.5; return &d }()}
	cases := map[string]string{
		"mr_files": MRFiles(model.MRFiles{Project: alpha, IID: 3, Files: []model.MRFile{
			{OldPath: "src/login.go", NewPath: "src/login.go", Status: "added", Additions: 4},
			{OldPath: "docs/old.md", NewPath: "docs/new.md", Status: "renamed"},
			{OldPath: "gen/api.pb.go", NewPath: "gen/api.pb.go", Status: "modified", Collapsed: true, Generated: &yes},
			{OldPath: "vendor/big.txt", NewPath: "vendor/big.txt", Status: "modified", TooLarge: true}},
			Listing: model.Listing{Returned: 4, Complete: true, Total: intp(4)}}, bd),
		"mr_diff": MRDiff(model.MRDiff{Project: alpha, IID: 3, Diffs: diffs}, bd),
		"mr_commits": MRCommits(model.MRCommits{Project: alpha, IID: 3, Commits: []model.CommitRow{{ID: "1234567890abcdef",
			AuthorName: "Bob Example", CommittedAt: t0, Parents: 1, UntrustedTitle: "Add login stub"}},
			Listing: model.Listing{Returned: 1, Complete: true, Total: intp(1)}}, bd),
		"drafts": DraftNotes(model.DraftNotes{Project: alpha, IID: 3, Budget: 30000, Drafts: []model.DraftNote{
			{ID: 80001, Position: &model.DiffPosition{NewPath: "src/login.go", NewLine: intp(3)}, UntrustedBody: "Rename this.",
				Budget: model.Budget{BudgetChars: 6000, TotalChars: 12, ShownChars: 12}},
			{ID: 80002, DiscussionID: "aaaa", ResolveDiscussion: true, UntrustedBody: "Agreed.",
				Budget: model.Budget{BudgetChars: 6000, TotalChars: 7, ShownChars: 7}}},
			NotShown: []int64{80003}, Listing: model.Listing{Returned: 2, Total: intp(3), NextPageToken: strp("tok")}}, bd),
		"compare": Compare(model.Compare{Project: alpha, From: "main", To: "feature/login",
			WebURL: "https://gitlab.example.com/example-group/alpha/-/compare/main...feature%2Flogin", CommitsTotal: 101,
			Commits: []model.CommitRow{{ID: "1234567890abcdef", AuthorName: "Bob Example", CommittedAt: t0, Parents: 1,
				UntrustedTitle: "Add login stub"}}, NextCommitOffset: intp(100), Diffs: diffs}, bd),
		"mr_versions": MRVersions(model.MRVersions{Project: alpha, IID: 3, Versions: []model.MRVersion{
			{ID: 120003, HeadSHA: "2222222222222222222222222222222222222222", BaseSHA: strp("0000000000000000000000000000000000000000"),
				StartSHA: strp("0000000000000000000000000000000000000000"), CreatedAt: t1, State: "overflow", ChangesCount: strp("1000+")},
			{ID: 120001, HeadSHA: "1111111111111111111111111111111111111111", CreatedAt: t0, State: "empty"}},
			Listing: model.Listing{Returned: 2, Complete: true, Total: intp(2)}}, bd),
		"mr_version_changes": MRVersionChanges(model.MRVersionChanges{IID: 3,
			FromVersion: model.MRVersion{ID: 120001, HeadSHA: "1111111111111111111111111111111111111111", CreatedAt: t0, State: "collected"},
			ToVersion:   model.MRVersion{ID: 120003, HeadSHA: "2222222222222222222222222222222222222222", CreatedAt: t1, State: "collected"},
			BaseMoved:   func() *bool { b := true; return &b }(),
			Compare: model.Compare{Project: alpha, From: "1111111111111111111111111111111111111111",
				To: "2222222222222222222222222222222222222222", Straight: true,
				WebURL:       "https://gitlab.example.com/example-group/alpha/-/compare/1111111111111111111111111111111111111111...2222222222222222222222222222222222222222",
				CommitsTotal: 101, Commits: []model.CommitRow{{ID: "2222222222222222", AuthorName: "Bob Example", CommittedAt: t1,
					Parents: 1, UntrustedTitle: "Address review"}}, NextCommitOffset: intp(100), Diffs: diffs}}, bd),
		"tags": Tags(model.Tags{Project: alpha, Tags: []model.Tag{
			{Name: "v1.0", CommitID: "1234567890abcdef", CommittedAt: t0, CreatedAt: tp(t1), Protected: true, Release: true,
				UntrustedMessage: "Release 1.0", UntrustedCommitTitle: "Prepare release"},
			{Name: "v0.9", CommitID: "fedcba0987654321", CommittedAt: t0, UntrustedCommitTitle: "Update"}},
			Listing: model.Listing{Returned: 2, Complete: true, Total: intp(2)}}, bd),
		"pipelines": Pipelines(model.Pipelines{Project: alpha, Pipelines: []model.PipelineRow{{ID: 61001, IID: 4,
			Status: "failed", Ref: "main", SHA: "1234567890abcdef", Source: "push", CreatedAt: t0, UpdatedAt: t1}},
			Listing: model.Listing{Returned: 1, NextPageToken: strp("tok"), Total: intp(4)}}, bd),
		"pipeline": Pipeline(model.PipelineDetail{Project: alpha, ID: 61001, IID: 4, Status: "failed",
			DetailedStatus: "failed", Ref: "main", SHA: "1234567890abcdef", Source: "push", User: &bob, CreatedAt: t0,
			StartedAt: tp(t0), FinishedAt: tp(t1), Duration: func() *int64 { d := int64(480); return &d }(),
			WebURL:              "https://gitlab.example.com/example-group/alpha/-/pipelines/61001",
			UntrustedYAMLErrors: "jobs:build config contains unknown keys", FailedJobs: []model.JobRow{job}, FailedTriggerJobsComplete: true}, bd),
		"jobs": Jobs(model.Jobs{Project: alpha, PipelineID: 61001, Jobs: []model.JobRow{job,
			{ID: 70003, Name: "lint", Stage: "test", Status: "failed", AllowFailure: true, CreatedAt: t0}},
			Listing: model.Listing{Returned: 2, Complete: true, Total: intp(2)}}, bd),
		"job_log": JobLog(model.JobLog{Project: alpha, Job: job, TotalBytes: 90000, ByteOffset: 50000, ByteEnd: 90000,
			PrevByteOffset: intp(10000), Section: "step_script", SecretsMasked: 1,
			UntrustedLog: "§ section step_script: Executing\ntoken [MASKED gitlab-token]\nERROR: Job failed: exit code 1\n"}, bd),
		"test_report": TestReport(model.TestReport{Project: alpha, PipelineID: 61001, PipelineStatus: "running", Partial: true,
			PartialReason: "the pipeline is running",
			Total:         model.TestCounts{Total: 5, Success: 2, Failed: 2, Error: 1, Seconds: 1.5}, SuitesTotal: 2,
			Suites: []model.TestSuiteRow{{Name: "unit tests", TestCounts: model.TestCounts{Total: 5, Success: 2, Failed: 2, Error: 1,
				Seconds: 1.5}}, {Name: "lint", UntrustedSuiteError: "JUnit XML parsing failed: 1:1: FATAL: Document is empty."}},
			Failures: []model.TestFailure{{Suite: "unit tests", Status: "failed", UntrustedName: "TestLogin",
				UntrustedClassname: "example.test/login", UntrustedFile: "login/login_test.go", Seconds: 0.5,
				UntrustedOutput: "login_test.go:12: refused token [MASKED gitlab-token]", OutputChars: 9000, OutputCut: true},
				{Suite: "unit tests", Status: "error", UntrustedName: "TestSetup", Seconds: 0.01}},
			FailuresTotal: 3, Offset: 1, NextOffset: intp(3), BudgetChars: 40000, SecretsMasked: 1}, bd),
		"test_report_summary": TestReport(model.TestReport{Project: alpha, PipelineID: 61001, PipelineStatus: "failed",
			FromSummary: true, UntrustedTotalSuiteError: "JUnit XML parsing failed", Total: model.TestCounts{Total: 5, Success: 4, Failed: 1, Seconds: 2}, SuitesTotal: 1,
			Suites: []model.TestSuiteRow{{Name: "unit tests", TestCounts: model.TestCounts{Total: 5, Success: 4, Failed: 1,
				Seconds: 2}}}, FailuresTotal: 1, BudgetChars: 40000}, bd),
		"lint": Lint(model.Lint{Project: alpha, Ref: "main", Simulate: true, Valid: false,
			UntrustedErrors:     []string{"jobs:build:script config should be a string"},
			UntrustedWarnings:   []string{"jobs:deploy may allow multiple pipelines"},
			Jobs:                []model.LintJob{{Name: "deploy", Stage: "deploy", When: "manual"}},
			UntrustedMergedYAML: "build:\n  script: 42\n",
			MergedYAMLBudget:    model.Budget{BudgetChars: 60000, TotalChars: 22, ShownChars: 22}}, bd),
		"labels": Labels(model.Labels{Project: alpha, Labels: []model.Label{
			{ID: 96004, Name: "priority::high", Color: "#428bca", ProjectOnly: true, Priority: intp(1), OpenIssues: intp(3),
				ClosedIssues: intp(1), OpenMergeRequests: intp(0), UntrustedDescription: "Do these first."},
			{ID: 96100, Name: "group-wide", Color: "#ff0000"}},
			Listing: model.Listing{Returned: 2, Complete: true, Total: intp(2)}}, bd),
		"milestones": Milestones(model.Milestones{Project: &alpha, Milestones: []model.MilestoneRow{
			{ID: 90001, IID: 2, State: "active", StartDate: strp("2026-01-12"), DueDate: strp("2026-01-23"), UpdatedAt: t0,
				UntrustedTitle: "Sprint 2"},
			{ID: 90002, IID: 1, State: "closed", Expired: true, UpdatedAt: t0, UntrustedTitle: "Sprint 1"}},
			Listing: model.Listing{Returned: 2, Complete: true, Total: intp(2)}}, bd),
		"members": Members(model.Members{Project: alpha, Members: []model.Member{
			{ID: 1001, Username: "alice", Name: "Alice Example", State: "active", AccessLevel: 40, Role: "maintainer"},
			{ID: 1003, Username: "carol", Name: "Carol Example", State: "blocked", AccessLevel: 50, Role: "owner",
				ExpiresAt: strp("2026-12-31")}},
			Listing: model.Listing{Returned: 2, Complete: true, Total: intp(2)}}, bd),
		"users": Users(model.Users{Users: []model.UserRow{{ID: 1002, Username: "bob", Name: "Bob Example", State: "active"}},
			Listing: model.Listing{Returned: 1, Complete: true, Total: intp(1)}}, bd),
		"todos": Todos(model.Todos{Todos: []model.Todo{{ID: 95002, Project: &alpha, Action: "review_requested",
			TargetType: "MergeRequest", TargetIID: func() *int64 { n := int64(1); return &n }(), State: "pending", Author: "carol",
			CreatedAt: t0, UntrustedTitle: "Add login", UntrustedBody: "@alice please review"}},
			Listing: model.Listing{Returned: 1, Complete: true, Total: intp(1)}}, bd),
		"search": Search(model.Search{Scope: "blobs", Where: "project", Rows: []model.SearchRow{
			{Kind: "blob", ProjectID: func() *int64 { n := int64(2001); return &n }(), Ref: "main", Path: "src/util/strings.go",
				StartLine: intp(2), UntrustedExcerpt: "// Upper is a stub.\nfunc Upper(s string) string { return s }", ExcerptCut: true}},
			Listing: model.Listing{Returned: 1, Complete: true, Total: intp(1)}}, bd),
	}
	for name, got := range cases {
		golden(t, name, got)
	}
}

func TestGoldensPhase2(t *testing.T) {
	public := model.WriteTarget{Project: alpha, Visibility: "public"}
	cases := map[string]string{
		"issue_update": IssueWrite(model.IssueWrite{Outcome: "updated", IID: 12, WebURL: "https://gitlab.example.com/example-group/alpha/-/issues/12",
			State: "closed", LabelsBefore: []string{"bug"}, Labels: []string{"bug", "priority::high"}, Assignees: []string{"bob"},
			Milestone: &model.Milestone{ID: 90001, Title: "Sprint 2", State: "active"}, DueDate: "2026-02-01", UpdatedAt: tp(t1),
			Changed: []string{"state", "labels"}, DescriptionRemoved: &model.Removed{Chars: 42, Lines: 2},
			Write: model.Write{Target: public, Notes: []string{"It was already open, so no state change was sent."},
				EscapedCommands: []model.EscapedLine{{Input: "description", Line: 3, Command: "close"}}}}, bd),
		"time_write": TimeWrite(model.TimeWrite{Outcome: "updated", Type: "merge_request", IID: 4,
			Before: model.TimeStats{TimeEstimate: 3600, HumanTimeEstimate: "1h"},
			After:  &model.TimeStats{TimeEstimate: 7200, TotalTimeSpent: 1800, HumanTimeEstimate: "2h", HumanTotalTimeSpent: "30m"},
			Sent:   []string{"time_estimate 2h", "add_spent_time 30m"}, Changed: []string{"time_estimate", "total_time_spent"},
			UpdatedAt: tp(t1), TotalTimeSpent: 1800, Write: model.Write{Target: public}}, bd),
		"time_write_dry_run": TimeWrite(model.TimeWrite{Outcome: "dry_run", Type: "issue", IID: 12,
			Before: model.TimeStats{TotalTimeSpent: 3600, HumanTotalTimeSpent: "1h"}, UpdatedAt: tp(t1), TotalTimeSpent: 3600,
			Write: model.Write{DryRun: true, Target: public, Notes: []string{"There is no estimate, so resetting it is left out."},
				WouldSend: &model.Preview{Method: "POST", Operation: "subtract 30m of spent time", Fields: []string{"duration"}}}}, bd),
		"reaction_comment": ReactionWrite(model.ReactionWrite{Outcome: "added", Type: "merge_request", IID: 3, NoteID: 50001,
			Emoji: "thumbsup", Reacted: boolp(true), Write: model.Write{Target: public}}, bd),
		"reaction_unknown": ReactionWrite(model.ReactionWrite{Outcome: "dry_run", Type: "issue", IID: 12, Emoji: "tada",
			Write: model.Write{DryRun: true, Target: public, WouldSend: &model.Preview{Method: "POST", Operation: "react with tada",
				Fields: []string{"name"}}}}, bd),
		"reaction_unchanged": ReactionWrite(model.ReactionWrite{Outcome: "unchanged", Type: "issue", IID: 12, Emoji: "tada", Reacted: boolp(false),
			Write: model.Write{Target: public, Notes: []string{"You have no tada reaction there, so nothing was sent."}}}, bd),
		"issue_dry_run": IssueWrite(model.IssueWrite{Outcome: "dry_run", Write: model.Write{DryRun: true, Target: public,
			WouldSend: &model.Preview{Method: "POST", Operation: "create an issue", Fields: []string{"title", "labels"}}}}, bd),
		"merge_request_unchanged": MergeRequestWrite(model.MergeRequestWrite{Outcome: "unchanged", IID: 4, WebURL: "https://gitlab.example.com/example-group/alpha/-/merge_requests/4",
			State: "opened", Draft: true, SourceBranch: "topic", TargetBranch: "main", LabelsBefore: []string{}, Labels: []string{},
			Reviewers: []string{"bob", "carol"}, UpdatedAt: tp(t0), Write: model.Write{Target: model.WriteTarget{Project: alpha, Visibility: "private"}}}, bd),
		"comment_thread": CommentWrite(model.CommentWrite{Outcome: "created", Kind: "thread", NoteID: 50123, DiscussionID: "3f2a9c",
			Position:  &model.DiffPosition{OldPath: "README.md", NewPath: "README.md", NewLine: intp(3), HeadSHA: "1234567890abcdef1234"},
			LineRange: &model.LineSpan{Side: "new", Start: 1, End: 3}, Write: model.Write{Target: public}}, bd),
		"discussion_resolve": DiscussionWrite(model.DiscussionWrite{Outcome: "resolved", DiscussionID: "3f2a9c", Resolved: true,
			Write: model.Write{Target: public}}, bd),
		"draft_delete": DraftDelete(model.DraftDelete{Outcome: "deleted", DraftID: 80012, Remaining: 2, Write: model.Write{Target: public}}, bd),
		"review_submit": ReviewSubmit(model.ReviewSubmit{Outcome: "published", Published: 3, Summary: true, ReviewerState: "requested_changes",
			Write: model.Write{Target: public}}, bd),
		"branch_create": BranchWrite(model.BranchWrite{Outcome: "created", Branch: "topic", CommitSHA: "abcdef1234567890abcd",
			WebURL: "https://gitlab.example.com/example-group/alpha/-/tree/topic", Write: model.Write{Target: public}}, bd),
		"commit_create": CommitWrite(model.CommitWrite{Outcome: "created", SHA: "abcdef1234567890abcd", ShortID: "abcdef12", Branch: "topic",
			BranchHead: "abcdef1234567890abcd", ParentIDs: []string{"1234567890abcdef1234"}, Additions: 3, Deletions: 1,
			Files: []string{"README.md", "docs/new.md"}, WebURL: "https://gitlab.example.com/example-group/alpha/-/commit/abcdef12",
			Write: model.Write{Target: public}}, bd),
		"todos_done": TodosDone(model.TodosDone{Outcome: "partly_done", Items: []model.TodoDone{{ID: 95001, Outcome: "done"},
			{ID: 99999, Outcome: "not_found", Error: "no such to-do item of yours"}}}, bd),
	}
	for name, got := range cases {
		golden(t, name, got)
	}
}
