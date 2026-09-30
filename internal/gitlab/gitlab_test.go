package gitlab

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// wireTypes lists every struct in this package, read from its source so
// a new type cannot be left out of the checks below.
func wireTypes(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "gitlab.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, s := range g.Specs {
			ts, ok := s.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if _, ok := ts.Type.(*ast.StructType); ok {
				names = append(names, ts.Name.Name)
			}
		}
	}
	return names
}

var byName = map[string]reflect.Type{
	"Metadata": reflect.TypeFor[Metadata](), "UserBasic": reflect.TypeFor[UserBasic](),
	"User": reflect.TypeFor[User](), "TokenInfo": reflect.TypeFor[TokenInfo](),
	"TokenInfoApplication": reflect.TypeFor[TokenInfoApplication](),
	"Namespace":            reflect.TypeFor[Namespace](), "Project": reflect.TypeFor[Project](),
	"Milestone": reflect.TypeFor[Milestone](), "References": reflect.TypeFor[References](),
	"TaskCompletion": reflect.TypeFor[TaskCompletion](), "Issue": reflect.TypeFor[Issue](), "TimeStats": reflect.TypeFor[TimeStats](),
	"DiffRefs": reflect.TypeFor[DiffRefs](), "PipelineBasic": reflect.TypeFor[PipelineBasic](),
	"MergeRequest": reflect.TypeFor[MergeRequest](), "Approvals": reflect.TypeFor[Approvals](),
	"Approver": reflect.TypeFor[Approver](), "Discussion": reflect.TypeFor[Discussion](),
	"Note": reflect.TypeFor[Note](), "Position": reflect.TypeFor[Position](),
	"File": reflect.TypeFor[File](), "TreeEntry": reflect.TypeFor[TreeEntry](),
	"Branch": reflect.TypeFor[Branch](), "Commit": reflect.TypeFor[Commit](),
	"CommitStats": reflect.TypeFor[CommitStats](), "Diff": reflect.TypeFor[Diff](),
	"ProtectedBranch": reflect.TypeFor[ProtectedBranch](), "AccessLevel": reflect.TypeFor[AccessLevel](),
	"Compare": reflect.TypeFor[Compare](), "Tag": reflect.TypeFor[Tag](), "TagRelease": reflect.TypeFor[TagRelease](),
	"Pipeline": reflect.TypeFor[Pipeline](), "PipelineDetail": reflect.TypeFor[PipelineDetail](),
	"PipelineDetailStatus": reflect.TypeFor[PipelineDetailStatus](),
	"WikiPage":             reflect.TypeFor[WikiPage](), "WikiPageBasic": reflect.TypeFor[WikiPageBasic](), "Snippet": reflect.TypeFor[Snippet](),
	"SnippetFile": reflect.TypeFor[SnippetFile](), "Release": reflect.TypeFor[Release](),
	"ReleaseCommit": reflect.TypeFor[ReleaseCommit](), "ReleaseMilestone": reflect.TypeFor[ReleaseMilestone](),
	"ReleaseAssets": reflect.TypeFor[ReleaseAssets](), "Environment": reflect.TypeFor[Environment](),
	"EnvironmentDeployment": reflect.TypeFor[EnvironmentDeployment](), "Deployment": reflect.TypeFor[Deployment](),
	"DeploymentEnvironment": reflect.TypeFor[DeploymentEnvironment](), "DeploymentJob": reflect.TypeFor[DeploymentJob](),
	"Event": reflect.TypeFor[Event](), "EventPush": reflect.TypeFor[EventPush](),
	"Job": reflect.TypeFor[Job](), "JobPipe": reflect.TypeFor[JobPipe](),
	"TestReport": reflect.TypeFor[TestReport](), "TestSuite": reflect.TypeFor[TestSuite](), "TestCase": reflect.TypeFor[TestCase](),
	"TestReportSummary": reflect.TypeFor[TestReportSummary](), "TestReportTotal": reflect.TypeFor[TestReportTotal](), "JobArtifact": reflect.TypeFor[JobArtifact](),
	"Bridge": reflect.TypeFor[Bridge](), "ReleaseLink": reflect.TypeFor[ReleaseLink](),
	"IssueLink": reflect.TypeFor[IssueLink](), "IssueBasic": reflect.TypeFor[IssueBasic](),
	"RelatedIssue": reflect.TypeFor[RelatedIssue](), "ProtectedTag": reflect.TypeFor[ProtectedTag](),
	"LinkedMergeRequest": reflect.TypeFor[LinkedMergeRequest](), "LinkedIssue": reflect.TypeFor[LinkedIssue](),
	"LabelEvent": reflect.TypeFor[LabelEvent](), "EventLabel": reflect.TypeFor[EventLabel](),
	"StateEvent": reflect.TypeFor[StateEvent](), "MilestoneEvent": reflect.TypeFor[MilestoneEvent](), "WeightEvent": reflect.TypeFor[WeightEvent](),
	"BlameRange": reflect.TypeFor[BlameRange](), "BlameCommit": reflect.TypeFor[BlameCommit](),
	"ArtifactEntry": reflect.TypeFor[ArtifactEntry](), "RebaseState": reflect.TypeFor[RebaseState](), "MergeRequestRebase": reflect.TypeFor[MergeRequestRebase](), "DownstreamPipeline": reflect.TypeFor[DownstreamPipeline](), "Lint": reflect.TypeFor[Lint](),
	"LintJob": reflect.TypeFor[LintJob](), "Label": reflect.TypeFor[Label](),
	"ProjectMilestone": reflect.TypeFor[ProjectMilestone](), "Member": reflect.TypeFor[Member](),
	"Board": reflect.TypeFor[Board](), "BoardList": reflect.TypeFor[BoardList](), "BoardLabel": reflect.TypeFor[BoardLabel](),
	"BoardUser": reflect.TypeFor[BoardUser](), "BoardTimebox": reflect.TypeFor[BoardTimebox](),
	"Todo": reflect.TypeFor[Todo](), "TodoProject": reflect.TypeFor[TodoProject](),
	"TodoTarget": reflect.TypeFor[TodoTarget](), "DraftNote": reflect.TypeFor[DraftNote](),
	"SearchHit": reflect.TypeFor[SearchHit](), "SearchCommit": reflect.TypeFor[SearchCommit](),
	"LineRange": reflect.TypeFor[LineRange](), "LineRangeEnd": reflect.TypeFor[LineRangeEnd](),
}

// A field that is never declared is never decoded, so a token GitLab
// adds to a response cannot reach a result.
func TestNoTokenFields(t *testing.T) {
	names := wireTypes(t)
	if len(names) < 25 {
		t.Fatalf("read only %d wire types from source; the parse is broken", len(names))
	}
	for _, n := range names {
		typ, ok := byName[n]
		if !ok {
			t.Errorf("wire type %s is missing from byName in this test", n)
			continue
		}
		for i := range typ.NumField() {
			f := typ.Field(i)
			tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if tag == "" {
				t.Errorf("%s.%s has no json tag", n, f.Name)
			}
			if strings.HasSuffix(tag, "_token") || strings.HasSuffix(strings.ToLower(f.Name), "token") {
				t.Errorf("%s.%s decodes %q, a token-shaped field", n, f.Name, tag)
			}
		}
	}
}

func TestDecodeMergeRequest(t *testing.T) {
	body := `{"id":90001,"iid":7,"project_id":2001,"title":"Add login","state":"opened",
		"created_at":"2026-09-01T10:00:00.000Z","updated_at":"2026-09-02T11:30:00.000+02:00",
		"merged_at":null,"detailed_merge_status":"some_future_status","changes_count":"1000+",
		"diff_refs":{"base_sha":"a","head_sha":"b","start_sha":"c"},
		"head_pipeline":{"id":5,"status":"success"},"runners_token":"never-decoded"}`
	var mr MergeRequest
	if err := json.Unmarshal([]byte(body), &mr); err != nil {
		t.Fatal(err)
	}
	if mr.IID != 7 || mr.DetailedMergeStatus != "some_future_status" || mr.ChangesCount != "1000+" {
		t.Errorf("decoded %+v", mr)
	}
	if mr.MergedAt != nil {
		t.Errorf("merged_at null decoded as %v", mr.MergedAt)
	}
	want := time.Date(2026, 9, 2, 9, 30, 0, 0, time.UTC)
	if !mr.UpdatedAt.Equal(want) {
		t.Errorf("updated_at = %v, want %v", mr.UpdatedAt, want)
	}
	if mr.DiffRefs == nil || mr.DiffRefs.StartSHA != "c" || mr.HeadPipeline == nil || mr.HeadPipeline.Status != "success" {
		t.Errorf("nested fields: %+v %+v", mr.DiffRefs, mr.HeadPipeline)
	}
}

// A test report's times are fractional seconds, though the OpenAPI file
// says integer.
func TestDecodeTestReport(t *testing.T) {
	body := `{"total_time":1.25,"total_count":2,"success_count":1,"failed_count":1,"skipped_count":0,"error_count":0,
		"test_suites":[{"name":"unit tests","total_time":1.25,"total_count":2,"success_count":1,"failed_count":1,
		"skipped_count":0,"error_count":0,"suite_error":null,"test_cases":[{"status":"failed","name":"TestLogin",
		"classname":"example.test/login","file":null,"execution_time":0.75,"system_output":"refused","stack_trace":null,
		"recent_failures":null}]}]}`
	var r TestReport
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	c := r.TestSuites[0].TestCases[0]
	if r.TotalTime != 1.25 || c.ExecutionTime != 0.75 || c.File != nil || *c.SystemOutput != "refused" {
		t.Errorf("decoded %+v", r)
	}
	summary := `{"total":{"time":1.25,"count":2,"success":1,"failed":1,"skipped":0,"error":0,"suite_error":null},
		"test_suites":[{"name":"unit tests","total_time":1.25,"total_count":2,"success_count":1,"failed_count":1,
		"skipped_count":0,"error_count":0,"build_ids":[70002],"suite_error":null}]}`
	var sum TestReportSummary
	if err := json.Unmarshal([]byte(summary), &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Total.Time != 1.25 || sum.TestSuites[0].FailedCount != 1 {
		t.Errorf("decoded %+v", sum)
	}
}

// closes_issues and related_issues mix GitLab's issues with an external
// tracker's, whose id is a string; both decode.
func TestDecodeLinkedIssues(t *testing.T) {
	body := `[{"id":30001,"iid":1,"project_id":2001,"title":"Crash","state":"opened"},
		{"title":"External Issue EXT-7","id":"EXT-7"}]`
	var rows []LinkedIssue
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].IID != 1 || rows[0].ExternalID() != "" || rows[1].ExternalID() != "EXT-7" {
		t.Errorf("decoded %+v", rows)
	}
}

func TestTokenInfoDecodes(t *testing.T) {
	var ti TokenInfo
	if err := json.Unmarshal([]byte(`{"resource_owner_id":1001,"scope":["read_api"],"expires_in":7100,"application":{"uid":"app"},"created_at":1790000000}`), &ti); err != nil {
		t.Fatal(err)
	}
	if ti.CreatedAt != 1790000000 {
		t.Errorf("created_at = %d", ti.CreatedAt)
	}
	if ti.ExpiresIn == nil || *ti.ExpiresIn != 7100 || ti.Scope[0] != "read_api" {
		t.Errorf("decoded %+v", ti)
	}
}

func TestTokenInfoScopeShapes(t *testing.T) {
	tests := []struct {
		name, body string
		want       []string
	}{
		{"array", `{"scope":["api","read_user"]}`, []string{"api", "read_user"}},
		{"string", `{"scope":"api read_user"}`, []string{"api", "read_user"}},
		{"scopes key", `{"scopes":["read_api"]}`, []string{"read_api"}},
		{"scopes string", `{"scopes":"read_api openid"}`, []string{"read_api", "openid"}},
		{"scope wins", `{"scope":["api"],"scopes":["read_api"]}`, []string{"api"}},
		{"another shape", `{"scope":{"api":true}}`, nil},
		{"neither", `{"resource_owner_id":1}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ti TokenInfo
			if err := json.Unmarshal([]byte(tt.body), &ti); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(ti.Scope, tt.want) {
				t.Errorf("Scope = %q, want %q", ti.Scope, tt.want)
			}
		})
	}
}
