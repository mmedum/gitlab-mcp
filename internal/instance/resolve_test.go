package instance

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestResolveURL(t *testing.T) {
	const root = "https://gitlab.example.com"
	const sub = "https://gitlab.example.com:8443/gitlab"
	tests := []struct {
		instance string
		url      string
		want     Ref
	}{
		// Projects.
		{root, root + "/example-group/project", Ref{Kind: KindProject, Project: "example-group/project"}},
		{root, root + "/example-group/sub/deeper/project/", Ref{Kind: KindProject, Project: "example-group/sub/deeper/project"}},
		{root, "gitlab.example.com/example-group/project", Ref{Kind: KindProject, Project: "example-group/project"}},
		{root, "http://gitlab.example.com/example-group/project", Ref{Kind: KindProject, Project: "example-group/project"}},
		{root, root + "/example-group/project.git", Ref{Kind: KindProject, Project: "example-group/project"}},
		{root, root + "/example-group/project/-/", Ref{Kind: KindProject, Project: "example-group/project"}},
		{sub, sub + "/example-group/project", Ref{Kind: KindProject, Project: "example-group/project"}},
		{sub, "http://gitlab.example.com:8443/gitlab/example-group/project", Ref{Kind: KindProject, Project: "example-group/project"}},

		// Issues.
		{root, root + "/example-group/project/-/issues/12", Ref{Kind: KindIssue, Project: "example-group/project", IID: 12}},
		{root, root + "/example-group/sub/project/-/issues/incident/12", Ref{Kind: KindIssue, Project: "example-group/sub/project", IID: 12}},
		{root, root + "/example-group/project/-/work_items/7", Ref{Kind: KindIssue, Project: "example-group/project", IID: 7}},
		{root, root + "/example-group/project/-/issues/12/designs", Ref{Kind: KindIssue, Project: "example-group/project", IID: 12}},
		{root, root + "/example-group/project/-/issues/12#note_345", Ref{Kind: KindIssue, Project: "example-group/project", IID: 12, Note: 345}},
		{root, root + "/example-group/project/issues/12", Ref{Kind: KindIssue, Project: "example-group/project", IID: 12}},
		{sub, sub + "/example-group/project/-/issues/3", Ref{Kind: KindIssue, Project: "example-group/project", IID: 3}},

		// Merge requests.
		{root, root + "/example-group/project/-/merge_requests/12", Ref{Kind: KindMergeRequest, Project: "example-group/project", IID: 12}},
		{root, root + "/example-group/project/-/merge_requests/12/diffs", Ref{Kind: KindMergeRequest, Project: "example-group/project", IID: 12}},
		{root, root + "/example-group/project/-/merge_requests/12/commits", Ref{Kind: KindMergeRequest, Project: "example-group/project", IID: 12}},
		{root, root + "/example-group/project/-/merge_requests/12/pipelines", Ref{Kind: KindMergeRequest, Project: "example-group/project", IID: 12}},
		{root, root + "/example-group/project/-/merge_requests/12/diffs#note_9", Ref{Kind: KindMergeRequest, Project: "example-group/project", IID: 12, Note: 9}},
		{root, root + "/example-group/project/merge_requests/12", Ref{Kind: KindMergeRequest, Project: "example-group/project", IID: 12}},

		// Files.
		{root, root + "/example-group/project/-/blob/main/README.md", Ref{Kind: KindFile, Project: "example-group/project", Ref: "main", Path: "README.md", refPath: "main/README.md"}},
		{root, root + "/example-group/project/-/blob/main/docs/guide.md#L10", Ref{Kind: KindFile, Project: "example-group/project", Ref: "main", Path: "docs/guide.md", Line: 10, refPath: "main/docs/guide.md"}},
		{root, root + "/example-group/project/-/blob/main/src/app.go#L10-20", Ref{Kind: KindFile, Project: "example-group/project", Ref: "main", Path: "src/app.go", Line: 10, EndLine: 20, refPath: "main/src/app.go"}},
		{root, root + "/example-group/project/-/blob/main/src/app.go#L10-L20", Ref{Kind: KindFile, Project: "example-group/project", Ref: "main", Path: "src/app.go", Line: 10, EndLine: 20, refPath: "main/src/app.go"}},
		{root, root + "/example-group/project/-/blob/main/src/app.go#L7-7", Ref{Kind: KindFile, Project: "example-group/project", Ref: "main", Path: "src/app.go", Line: 7, refPath: "main/src/app.go"}},
		{root, root + "/example-group/project/-/raw/v1.0.0/my%20notes.txt", Ref{Kind: KindFile, Project: "example-group/project", Ref: "v1.0.0", Path: "my notes.txt", refPath: "v1.0.0/my notes.txt"}},
		{root, root + "/example-group/project/-/blob/feature%2Fsearch/lib/find.go", Ref{Kind: KindFile, Project: "example-group/project", Ref: "feature", Path: "search/lib/find.go", refPath: "feature/search/lib/find.go"}},
		{root, root + "/example-group/project/blob/main/README.md", Ref{Kind: KindFile, Project: "example-group/project", Ref: "main", Path: "README.md", refPath: "main/README.md"}},
		{root, root + "/example-group/project/raw/main/README.md", Ref{Kind: KindFile, Project: "example-group/project", Ref: "main", Path: "README.md", refPath: "main/README.md"}},
		{sub, sub + "/example-group/sub/project/-/blob/main/a.txt#L3", Ref{Kind: KindFile, Project: "example-group/sub/project", Ref: "main", Path: "a.txt", Line: 3, refPath: "main/a.txt"}},

		// Trees and commit listings.
		{root, root + "/example-group/project/-/tree/main", Ref{Kind: KindProject, Project: "example-group/project", Ref: "main", refPath: "main", pathOptional: true}},
		{root, root + "/example-group/project/-/tree/main/docs/api", Ref{Kind: KindProject, Project: "example-group/project", Ref: "main", Path: "docs/api", refPath: "main/docs/api", pathOptional: true}},
		{root, root + "/example-group/project/tree/main/docs", Ref{Kind: KindProject, Project: "example-group/project", Ref: "main", Path: "docs", refPath: "main/docs", pathOptional: true}},
		{root, root + "/example-group/project/-/commits/main", Ref{Kind: KindProject, Project: "example-group/project", Ref: "main", refPath: "main", pathOptional: true}},
		{root, root + "/example-group/project/-/commits/main/src", Ref{Kind: KindProject, Project: "example-group/project", Ref: "main", Path: "src", refPath: "main/src", pathOptional: true}},

		// Commits and compares.
		{root, root + "/example-group/project/-/commit/0123456789abcdef0123456789abcdef01234567", Ref{Kind: KindCommit, Project: "example-group/project", SHA: "0123456789abcdef0123456789abcdef01234567"}},
		{root, root + "/example-group/project/-/commit/0123abc.diff", Ref{Kind: KindCommit, Project: "example-group/project", SHA: "0123abc"}},
		{root, root + "/example-group/project/-/compare/main...feature/search", Ref{Kind: KindCompare, Project: "example-group/project", From: "main", To: "feature/search"}},
		{root, root + "/example-group/project/-/compare/v1.0.0..v1.1.0", Ref{Kind: KindCompare, Project: "example-group/project", From: "v1.0.0", To: "v1.1.0"}},

		// CI.
		{root, root + "/example-group/project/-/pipelines/123", Ref{Kind: KindPipeline, Project: "example-group/project", ID: 123}},
		{root, root + "/example-group/project/-/pipelines/123/failures", Ref{Kind: KindPipeline, Project: "example-group/project", ID: 123}},
		{root, root + "/example-group/project/-/jobs/456", Ref{Kind: KindJob, Project: "example-group/project", ID: 456}},
		{root, root + "/example-group/project/-/jobs/456/raw", Ref{Kind: KindJob, Project: "example-group/project", ID: 456}},

		// Wikis.
		{root, root + "/example-group/project/-/wikis/setup/install", Ref{Kind: KindWiki, Project: "example-group/project", Slug: "setup/install"}},
		{root, root + "/example-group/project/-/wikis", Ref{Kind: KindWiki, Project: "example-group/project", Slug: "home"}},
		{root, root + "/example-group/project/wikis/home", Ref{Kind: KindWiki, Project: "example-group/project", Slug: "home"}},

		// A subgroup named like a legacy route is still a namespace.
		{root, root + "/example-group/issues/project", Ref{Kind: KindProject, Project: "example-group/issues/project"}},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			i, err := Parse(tt.instance)
			if err != nil {
				t.Fatal(err)
			}
			got, err := i.ResolveURL(tt.url)
			if err != nil {
				t.Fatalf("ResolveURL(%q): %v", tt.url, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ResolveURL(%q)\n got %+v\nwant %+v", tt.url, got, tt.want)
			}
		})
	}
}

func TestResolveURLErrors(t *testing.T) {
	const root = "https://gitlab.example.com"
	const sub = "https://gitlab.example.com/gitlab"
	tests := []struct {
		instance, url, wantErr string
	}{
		{root, "https://other.invalid/example-group/project", "configured instance https://gitlab.example.com"},
		{root, "https://gitlab.example.com:8443/example-group/project", "configured instance https://gitlab.example.com"},
		{sub, "https://gitlab.example.com/example-group/project", "configured instance https://gitlab.example.com/gitlab"},
		{root, "", "empty"},
		{root, "ssh://gitlab.example.com/example-group/project", "configured instance"},
		{root, "https://user:s3cr3t@gitlab.example.com/example-group/project", "credentials"},
		{root, root, "names the instance"},
		{root, root + "/example-group", "group or user"},
		{root, root + "/groups/example-group/-/work_items/1", "group"},
		{root, root + "/groups/example-group/sub/-/epics/4", "group"},
		{root, root + "/dashboard/issues", `"dashboard" page`},
		{root, root + "/explore/projects", `"explore" page`},
		{root, root + "/-/profile", `"-" page`},
		{root, root + "/example-group/-/issues/1", "group or user"},
		{root, root + "/example-group/project/-/issues", "issue list"},
		{root, root + "/example-group/project/-/issues/0", "positive number"},
		{root, root + "/example-group/project/-/issues/abc", "positive number"},
		{root, root + "/example-group/project/-/merge_requests/-3", "positive number"},
		{root, root + "/example-group/project/-/pipelines/new", "positive number"},
		{root, root + "/example-group/project/-/jobs", "positive number"},
		{root, root + "/example-group/project/-/blob/main", "no file path"},
		{root, root + "/example-group/project/-/commit", "no SHA"},
		{root, root + "/example-group/project/-/compare/main", "two refs"},
		{root, root + "/example-group/project/-/settings/ci_cd", "resolve_url does not know"},
		{root, root + "/example-group/project/-/blob/main/a.go#L0", "line"},
		{root, root + "/example-group/project/-/blob/main/a.go#L20-10", "ends before"},
		{root, root + "/example-group/project/-/issues/1#note_x", "note"},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			i, err := Parse(tt.instance)
			if err != nil {
				t.Fatal(err)
			}
			got, err := i.ResolveURL(tt.url)
			if err == nil {
				t.Fatalf("ResolveURL(%q) = %+v, want error containing %q", tt.url, got, tt.wantErr)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("error %v does not wrap ErrInvalid", err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), "s3cr3t") {
				t.Errorf("error %q echoes credentials", err)
			}
		})
	}
	if _, err := (Instance{}).ResolveURL("https://gitlab.example.com/example-group/project"); !errors.Is(err, ErrInvalid) {
		t.Errorf("zero Instance: error = %v, want ErrInvalid", err)
	}
}

func TestRefPathCandidates(t *testing.T) {
	i, err := Parse("gitlab.example.com")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		url  string
		want []RefPath
	}{
		{"https://gitlab.example.com/example-group/project/-/blob/feature/search/lib/find.go", []RefPath{
			{Ref: "feature", Path: "search/lib/find.go"},
			{Ref: "feature/search", Path: "lib/find.go"},
			{Ref: "feature/search/lib", Path: "find.go"},
		}},
		{"https://gitlab.example.com/example-group/project/-/tree/release/v2/docs", []RefPath{
			{Ref: "release", Path: "v2/docs"},
			{Ref: "release/v2", Path: "docs"},
			{Ref: "release/v2/docs", Path: ""},
		}},
		{"https://gitlab.example.com/example-group/project/-/blob/main/README.md", []RefPath{
			{Ref: "main", Path: "README.md"},
		}},
		{"https://gitlab.example.com/example-group/project/-/issues/1", nil},
		{"https://gitlab.example.com/example-group/project", nil},
	}
	for _, tt := range tests {
		r, err := i.ResolveURL(tt.url)
		if err != nil {
			t.Fatalf("ResolveURL(%q): %v", tt.url, err)
		}
		if got := r.RefPathCandidates(); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("RefPathCandidates(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}
