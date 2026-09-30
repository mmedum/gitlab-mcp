// Package gitlab holds the wire types of GitLab's REST API v4.
//
// Hand-written, for exactly the fields the server uses (CLAUDE.md rule
// 13, docs/architecture.md §4.10). A field is added when a tool reads
// it, and `scripts/gates api-fields` holds every field here against the
// OpenAPI snapshot. Fields GitLab adds without notice are reported by
// the client as drift rather than failing a call.
//
// No field whose name ends in `_token` is ever declared: a project read
// once carried a runner registration token, and a field that is never
// decoded cannot leak. TestNoTokenFields holds it.
package gitlab

import (
	"encoding/json"
	"strings"
	"time"
)

// Metadata is GET /metadata. It needs authentication.
type Metadata struct {
	Version    string `json:"version"`
	Revision   string `json:"revision"`
	Enterprise bool   `json:"enterprise"`
}

// UserBasic is the user embedded in issues, merge requests and notes.
type UserBasic struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	State    string `json:"state"`
	WebURL   string `json:"web_url"`
}

// User is GET /user, the signed-in account.
type User struct {
	ID        int64      `json:"id"`
	Username  string     `json:"username"`
	Name      string     `json:"name"`
	State     string     `json:"state"`
	WebURL    string     `json:"web_url"`
	Bot       bool       `json:"bot"`
	IsAdmin   bool       `json:"is_admin"`
	CreatedAt *time.Time `json:"created_at"`
}

// TokenInfo is GET /oauth/token/info, Doorkeeper's introspection of the
// bearer token. It lives under the web base, not the API root.
type TokenInfo struct {
	ResourceOwnerID int64 `json:"resource_owner_id"`
	// Scope is what the token was granted. Older Doorkeeper answers name
	// the key "scopes"; either is read.
	Scope ScopeList `json:"scope"`
	// ExpiresIn is the seconds left, nil for a token that does not
	// expire.
	ExpiresIn   *int64               `json:"expires_in"`
	Application TokenInfoApplication `json:"application"`
	// CreatedAt is Unix seconds, as Doorkeeper writes it.
	CreatedAt int64 `json:"created_at"`
}

// UnmarshalJSON reads the answer, taking the scopes from "scopes" when
// "scope" carries none.
func (t *TokenInfo) UnmarshalJSON(data []byte) error {
	type plain TokenInfo // without this method, so the decode does not recurse
	var raw struct {
		plain
		Scopes ScopeList `json:"scopes"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*t = TokenInfo(raw.plain)
	if len(t.Scope) == 0 {
		t.Scope = raw.Scopes
	}
	return nil
}

// ScopeList is a token's scopes, which Doorkeeper writes as an array
// and OAuth as one space-separated string. It reads either; any other
// shape reads as no scopes rather than failing the answer.
type ScopeList []string

// UnmarshalJSON reads an array of scopes or a space-separated string.
func (l *ScopeList) UnmarshalJSON(data []byte) error {
	var list []string
	if json.Unmarshal(data, &list) == nil {
		*l = list
		return nil
	}
	var s string
	if json.Unmarshal(data, &s) == nil {
		*l = strings.Fields(s)
		return nil
	}
	*l = nil
	return nil
}

// TokenInfoApplication names the OAuth application the token came from.
type TokenInfoApplication struct {
	UID string `json:"uid"`
}

// Namespace is the group or user a project lives under.
type Namespace struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	FullPath string `json:"full_path"`
	Kind     string `json:"kind"`
}

// Project is GET /projects/:id and the rows of GET /projects.
//
// Secret fields (runners_token and the rest) are never declared, so they
// are never decoded (§3.19).
type Project struct {
	ID                int64  `json:"id"`
	Name              string `json:"name"`
	NameWithNamespace string `json:"name_with_namespace"`
	Path              string `json:"path"`
	PathWithNamespace string `json:"path_with_namespace"`
	Description       string `json:"description"`
	DefaultBranch     string `json:"default_branch"`
	Visibility        string `json:"visibility"`
	// IssuesAccessLevel narrows who sees the issues: private is members
	// only, whatever the project's visibility.
	IssuesAccessLevel string     `json:"issues_access_level"`
	WebURL            string     `json:"web_url"`
	Archived          bool       `json:"archived"`
	EmptyRepo         bool       `json:"empty_repo"`
	Topics            []string   `json:"topics"`
	StarCount         int        `json:"star_count"`
	ForksCount        int        `json:"forks_count"`
	OpenIssuesCount   *int       `json:"open_issues_count"`
	CreatedAt         time.Time  `json:"created_at"`
	LastActivityAt    *time.Time `json:"last_activity_at"`
	Namespace         Namespace  `json:"namespace"`
}

// Milestone is the milestone embedded in an issue or merge request.
type Milestone struct {
	ID    int64  `json:"id"`
	IID   int64  `json:"iid"`
	Title string `json:"title"`
	State string `json:"state"`
}

// References are the ways GitLab spells an issue or merge request:
// "#12", "group/project#12".
type References struct {
	Short    string `json:"short"`
	Relative string `json:"relative"`
	Full     string `json:"full"`
}

// TaskCompletion counts the checkboxes in a description.
type TaskCompletion struct {
	Count          int `json:"count"`
	CompletedCount int `json:"completed_count"`
}

// Issue is GET /projects/:id/issues/:iid and the rows of the issue lists.
type Issue struct {
	ID             int64           `json:"id"`
	IID            int64           `json:"iid"`
	ProjectID      int64           `json:"project_id"`
	Title          string          `json:"title"`
	Description    string          `json:"description"`
	State          string          `json:"state"`
	Type           string          `json:"type"`
	Confidential   bool            `json:"confidential"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	ClosedAt       *time.Time      `json:"closed_at"`
	ClosedBy       *UserBasic      `json:"closed_by"`
	Labels         []string        `json:"labels"`
	Milestone      *Milestone      `json:"milestone"`
	Author         UserBasic       `json:"author"`
	Assignees      []UserBasic     `json:"assignees"`
	UserNotesCount int             `json:"user_notes_count"`
	Upvotes        int             `json:"upvotes"`
	Downvotes      int             `json:"downvotes"`
	DueDate        string          `json:"due_date"` // a date, "2026-10-01", not an instant
	WebURL         string          `json:"web_url"`
	References     References      `json:"references"`
	TaskCompletion *TaskCompletion `json:"task_completion_status"`
	// MovedToID is the issue it was moved to, once moved.
	MovedToID *int64    `json:"moved_to_id"`
	TimeStats TimeStats `json:"time_stats"`
}

// TimeStats is an issue's or a merge request's time tracking, and the
// answer of each time tracking write (Entities::IssuableTimeStats). The
// counts are seconds. The human forms are GitLab's short ones, "1w 2d",
// and null at zero.
type TimeStats struct {
	TimeEstimate        int64   `json:"time_estimate"`
	TotalTimeSpent      int64   `json:"total_time_spent"`
	HumanTimeEstimate   *string `json:"human_time_estimate"`
	HumanTotalTimeSpent *string `json:"human_total_time_spent"`
}

// DiffRefs are the three SHAs a diff position is computed against.
type DiffRefs struct {
	BaseSHA  string `json:"base_sha"`
	HeadSHA  string `json:"head_sha"`
	StartSHA string `json:"start_sha"`
}

// PipelineBasic is the pipeline embedded in a merge request.
type PipelineBasic struct {
	ID     int64  `json:"id"`
	IID    int64  `json:"iid"`
	SHA    string `json:"sha"`
	Ref    string `json:"ref"`
	Status string `json:"status"`
	WebURL string `json:"web_url"`
}

// MergeRequest is GET /projects/:id/merge_requests/:iid and the rows of
// the merge request lists. DiffRefs and HeadPipeline come only on the
// single read.
type MergeRequest struct {
	ID              int64       `json:"id"`
	IID             int64       `json:"iid"`
	ProjectID       int64       `json:"project_id"`
	Title           string      `json:"title"`
	Description     string      `json:"description"`
	State           string      `json:"state"`
	Draft           bool        `json:"draft"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
	MergedAt        *time.Time  `json:"merged_at"`
	ClosedAt        *time.Time  `json:"closed_at"`
	MergeUser       *UserBasic  `json:"merge_user"`
	Author          UserBasic   `json:"author"`
	Assignees       []UserBasic `json:"assignees"`
	Reviewers       []UserBasic `json:"reviewers"`
	SourceBranch    string      `json:"source_branch"`
	TargetBranch    string      `json:"target_branch"`
	SourceProjectID int64       `json:"source_project_id"`
	TargetProjectID int64       `json:"target_project_id"`
	Labels          []string    `json:"labels"`
	Milestone       *Milestone  `json:"milestone"`
	// DetailedMergeStatus is kept as GitLab's string: the set grows
	// between releases, and an enum here would fail on the next one.
	DetailedMergeStatus         string  `json:"detailed_merge_status"`
	HasConflicts                bool    `json:"has_conflicts"`
	BlockingDiscussionsResolved bool    `json:"blocking_discussions_resolved"`
	SHA                         string  `json:"sha"`
	MergeCommitSHA              *string `json:"merge_commit_sha"`
	SquashCommitSHA             *string `json:"squash_commit_sha"`
	Squash                      bool    `json:"squash"`
	// ForceRemoveSourceBranch is the "delete the source branch when
	// merged" setting; GitLab sends null when it was never set.
	ForceRemoveSourceBranch bool           `json:"force_remove_source_branch"`
	UserNotesCount          int            `json:"user_notes_count"`
	ChangesCount            string         `json:"changes_count"` // "12" or "1000+"
	WebURL                  string         `json:"web_url"`
	References              References     `json:"references"`
	DiffRefs                *DiffRefs      `json:"diff_refs"`
	HeadPipeline            *PipelineBasic `json:"head_pipeline"`
	// MergeWhenPipelineSucceeds is set while an auto-merge waits for the
	// pipeline.
	MergeWhenPipelineSucceeds bool      `json:"merge_when_pipeline_succeeds"`
	TimeStats                 TimeStats `json:"time_stats"`
}

// Approvals is GET /projects/:id/merge_requests/:iid/approvals. The
// counts are Premium fields and absent on Community Edition.
type Approvals struct {
	Approved          bool       `json:"approved"`
	ApprovalsRequired *int       `json:"approvals_required"`
	ApprovalsLeft     *int       `json:"approvals_left"`
	ApprovedBy        []Approver `json:"approved_by"`
	UserHasApproved   bool       `json:"user_has_approved"`
	UserCanApprove    bool       `json:"user_can_approve"`
}

// Approver is one row of Approvals.ApprovedBy.
type Approver struct {
	User UserBasic `json:"user"`
}

// Discussion is one thread of GET …/discussions.
type Discussion struct {
	ID             string `json:"id"`
	IndividualNote bool   `json:"individual_note"`
	Notes          []Note `json:"notes"`
}

// Note is one comment in a discussion.
type Note struct {
	ID           int64      `json:"id"`
	Type         *string    `json:"type"` // "DiffNote", "DiscussionNote" or null
	Body         string     `json:"body"`
	Author       UserBasic  `json:"author"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	System       bool       `json:"system"`
	Internal     bool       `json:"internal"`
	NoteableID   int64      `json:"noteable_id"`   //nolint:misspell // GitLab's wire name
	NoteableType string     `json:"noteable_type"` //nolint:misspell // GitLab's wire name
	NoteableIID  *int64     `json:"noteable_iid"`  //nolint:misspell // GitLab's wire name
	Resolvable   bool       `json:"resolvable"`
	Resolved     bool       `json:"resolved"`
	ResolvedBy   *UserBasic `json:"resolved_by"`
	ResolvedAt   *time.Time `json:"resolved_at"`
	Position     *Position  `json:"position"`
}

// Position is where a diff note sits.
type Position struct {
	BaseSHA      string `json:"base_sha"`
	StartSHA     string `json:"start_sha"`
	HeadSHA      string `json:"head_sha"`
	PositionType string `json:"position_type"`
	OldPath      string `json:"old_path"`
	NewPath      string `json:"new_path"`
	OldLine      *int   `json:"old_line"`
	NewLine      *int   `json:"new_line"`
	// LineRange is a multi-line comment's first and last line.
	LineRange *LineRange `json:"line_range"`
}

// LineRange is the first and last line a multi-line diff note covers.
type LineRange struct {
	Start LineRangeEnd `json:"start"`
	End   LineRangeEnd `json:"end"`
}

// LineRangeEnd is one end of a LineRange.
type LineRangeEnd struct {
	LineCode string `json:"line_code"`
	Type     string `json:"type"`
	OldLine  *int   `json:"old_line"`
	NewLine  *int   `json:"new_line"`
}

// File is GET /projects/:id/repository/files/:file_path. Content is
// base64 when Encoding says so.
type File struct {
	FileName      string `json:"file_name"`
	FilePath      string `json:"file_path"`
	Size          int64  `json:"size"`
	Encoding      string `json:"encoding"`
	Content       string `json:"content"`
	ContentSHA256 string `json:"content_sha256"`
	Ref           string `json:"ref"`
	BlobID        string `json:"blob_id"`
	CommitID      string `json:"commit_id"`
	// LastCommitID is the witness an update or delete of this file
	// carries (§4.6).
	LastCommitID    string `json:"last_commit_id"`
	ExecuteFilemode bool   `json:"execute_filemode"`
}

// TreeEntry is one row of GET /projects/:id/repository/tree.
type TreeEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // "blob", "tree" or "commit" (a submodule)
	Path string `json:"path"`
	Mode string `json:"mode"`
}

// Branch is one row of GET /projects/:id/repository/branches.
type Branch struct {
	Name               string `json:"name"`
	Merged             bool   `json:"merged"`
	Protected          bool   `json:"protected"`
	Default            bool   `json:"default"`
	DevelopersCanPush  bool   `json:"developers_can_push"`
	DevelopersCanMerge bool   `json:"developers_can_merge"`
	CanPush            bool   `json:"can_push"`
	WebURL             string `json:"web_url"`
	Commit             Commit `json:"commit"`
}

// Commit is GET /projects/:id/repository/commits/:sha and the rows of
// the commit list. Stats come only on the single read.
type Commit struct {
	ID             string       `json:"id"`
	ShortID        string       `json:"short_id"`
	Title          string       `json:"title"`
	Message        string       `json:"message"`
	AuthorName     string       `json:"author_name"`
	AuthorEmail    string       `json:"author_email"`
	AuthoredDate   time.Time    `json:"authored_date"`
	CommitterName  string       `json:"committer_name"`
	CommitterEmail string       `json:"committer_email"`
	CommittedDate  time.Time    `json:"committed_date"`
	CreatedAt      time.Time    `json:"created_at"`
	ParentIDs      []string     `json:"parent_ids"`
	WebURL         string       `json:"web_url"`
	Stats          *CommitStats `json:"stats"`
}

// CommitStats counts a commit's changed lines.
type CommitStats struct {
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
	Total     int `json:"total"`
}

// Diff is one file of a commit or merge request diff. TooLarge and
// Collapsed are kept, because a diff GitLab cut is not an empty one
// (§3.10).
type Diff struct {
	OldPath       string `json:"old_path"`
	NewPath       string `json:"new_path"`
	AMode         string `json:"a_mode"`
	BMode         string `json:"b_mode"`
	Diff          string `json:"diff"`
	NewFile       bool   `json:"new_file"`
	RenamedFile   bool   `json:"renamed_file"`
	DeletedFile   bool   `json:"deleted_file"`
	TooLarge      bool   `json:"too_large"`
	Collapsed     bool   `json:"collapsed"`
	GeneratedFile *bool  `json:"generated_file"`
}

// ProtectedBranch is one row of GET /projects/:id/protected_branches. The
// name may be a wildcard such as "release/*".
type ProtectedBranch struct {
	ID                        int64         `json:"id"`
	Name                      string        `json:"name"`
	PushAccessLevels          []AccessLevel `json:"push_access_levels"`
	MergeAccessLevels         []AccessLevel `json:"merge_access_levels"`
	AllowForcePush            bool          `json:"allow_force_push"`
	CodeOwnerApprovalRequired *bool         `json:"code_owner_approval_required"`
}

// AccessLevel is one rule of a protected branch.
type AccessLevel struct {
	ID                     int64  `json:"id"`
	AccessLevel            *int   `json:"access_level"`
	AccessLevelDescription string `json:"access_level_description"`
	UserID                 *int64 `json:"user_id"`
	GroupID                *int64 `json:"group_id"`
}

// Compare is GET /projects/:id/repository/compare. GitLab returns every
// commit and every diff in one answer, unpaged.
type Compare struct {
	Commits        []Commit `json:"commits"`
	Diffs          []Diff   `json:"diffs"`
	CompareTimeout bool     `json:"compare_timeout"`
	CompareSameRef bool     `json:"compare_same_ref"`
	WebURL         string   `json:"web_url"`
}

// Tag is one row of GET /projects/:id/repository/tags.
type Tag struct {
	Name      string      `json:"name"`
	Message   string      `json:"message"` // an annotated tag's message, "" for a lightweight tag
	Target    string      `json:"target"`
	Protected bool        `json:"protected"`
	CreatedAt *time.Time  `json:"created_at"`
	Commit    Commit      `json:"commit"`
	Release   *TagRelease `json:"release"`
}

// TagRelease is the release a tag carries, if any.
type TagRelease struct {
	TagName string `json:"tag_name"`
}

// Pipeline is one row of GET /projects/:id/pipelines.
type Pipeline struct {
	ID        int64     `json:"id"`
	IID       int64     `json:"iid"`
	ProjectID int64     `json:"project_id"`
	SHA       string    `json:"sha"`
	Ref       string    `json:"ref"`
	Status    string    `json:"status"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	WebURL    string    `json:"web_url"`
}

// PipelineDetail is GET /projects/:id/pipelines/:pipeline_id: a list row's
// fields and the ones only the single read carries.
type PipelineDetail struct {
	ID             int64                 `json:"id"`
	IID            int64                 `json:"iid"`
	ProjectID      int64                 `json:"project_id"`
	SHA            string                `json:"sha"`
	Ref            string                `json:"ref"`
	Status         string                `json:"status"`
	Source         string                `json:"source"`
	CreatedAt      time.Time             `json:"created_at"`
	UpdatedAt      time.Time             `json:"updated_at"`
	WebURL         string                `json:"web_url"`
	BeforeSHA      string                `json:"before_sha"`
	Tag            bool                  `json:"tag"`
	YAMLErrors     *string               `json:"yaml_errors"`
	User           *UserBasic            `json:"user"`
	StartedAt      *time.Time            `json:"started_at"`
	FinishedAt     *time.Time            `json:"finished_at"`
	Duration       *int64                `json:"duration"`        // seconds
	QueuedDuration *int64                `json:"queued_duration"` // seconds
	DetailedStatus *PipelineDetailStatus `json:"detailed_status"`
}

// PipelineDetailStatus is the status GitLab shows a person: "passed with
// warnings" where Status says "success".
type PipelineDetailStatus struct {
	Text  string `json:"text"`
	Label string `json:"label"`
	Group string `json:"group"`
}

// Job is one row of GET /projects/:id/pipelines/:pipeline_id/jobs.
type Job struct {
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	Stage          string     `json:"stage"`
	Status         string     `json:"status"`
	Ref            string     `json:"ref"`
	Tag            bool       `json:"tag"`
	AllowFailure   bool       `json:"allow_failure"`
	FailureReason  string     `json:"failure_reason"`
	CreatedAt      time.Time  `json:"created_at"`
	StartedAt      *time.Time `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at"`
	Duration       *float64   `json:"duration"`        // seconds
	QueuedDuration *float64   `json:"queued_duration"` // seconds
	WebURL         string     `json:"web_url"`
	Pipeline       JobPipe    `json:"pipeline"`
	// Artifacts holds the archived log as file_type "trace", once GitLab
	// has archived it; its size is the log's.
	Artifacts []JobArtifact `json:"artifacts"`
}

// JobArtifact is one file a job keeps.
type JobArtifact struct {
	FileType string `json:"file_type"`
	Size     int64  `json:"size"`
}

// Bridge is a trigger job: it starts a downstream pipeline and takes
// that pipeline's outcome. The job listing leaves it out.
type Bridge struct {
	ID                 int64               `json:"id"`
	Name               string              `json:"name"`
	Stage              string              `json:"stage"`
	Status             string              `json:"status"`
	AllowFailure       bool                `json:"allow_failure"`
	FailureReason      string              `json:"failure_reason"`
	CreatedAt          time.Time           `json:"created_at"`
	StartedAt          *time.Time          `json:"started_at"`
	FinishedAt         *time.Time          `json:"finished_at"`
	Duration           *float64            `json:"duration"` // seconds
	WebURL             string              `json:"web_url"`
	DownstreamPipeline *DownstreamPipeline `json:"downstream_pipeline"`
}

// DownstreamPipeline is the pipeline a trigger job started, in its own
// project or another.
type DownstreamPipeline struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"project_id"`
	Status    string `json:"status"`
	WebURL    string `json:"web_url"`
}

// JobPipe is the pipeline a job row names.
type JobPipe struct {
	ID int64 `json:"id"`
}

// TestReport is GET /projects/:id/pipelines/:pipeline_id/test_report:
// every suite with every case, parsed from the jobs' JUnit artifacts
// when read (TestReportEntity at v19.4.1-ee). Times are fractional
// seconds, though the OpenAPI file says integer.
type TestReport struct {
	TotalTime    float64     `json:"total_time"`
	TotalCount   int         `json:"total_count"`
	SuccessCount int         `json:"success_count"`
	FailedCount  int         `json:"failed_count"`
	SkippedCount int         `json:"skipped_count"`
	ErrorCount   int         `json:"error_count"`
	TestSuites   []TestSuite `json:"test_suites"`
}

// TestSuite is one suite of a test report: a job's report, parallel jobs
// merged. With SuiteError set it has no cases and its counts are 0. The
// summary's suites carry no cases, and their build_ids are not decoded.
type TestSuite struct {
	Name         string     `json:"name"`
	TotalTime    float64    `json:"total_time"`
	TotalCount   int        `json:"total_count"`
	SuccessCount int        `json:"success_count"`
	FailedCount  int        `json:"failed_count"`
	SkippedCount int        `json:"skipped_count"`
	ErrorCount   int        `json:"error_count"`
	SuiteError   *string    `json:"suite_error"`
	TestCases    []TestCase `json:"test_cases"`
}

// TestCase is one case of a suite. A failure's or an error's detail is
// in SystemOutput. The case's stack_trace is not decoded: JUnit is
// GitLab's only test report parser, and it never sets one.
type TestCase struct {
	Status        string  `json:"status"` // success, failed, skipped or error
	Name          string  `json:"name"`
	Classname     *string `json:"classname"`
	File          *string `json:"file"`
	ExecutionTime float64 `json:"execution_time"` // seconds
	SystemOutput  *string `json:"system_output"`
}

// TestReportSummary is GET …/pipelines/:pipeline_id/test_report_summary:
// the counts GitLab stored as each job finished, with no cases.
type TestReportSummary struct {
	Total      TestReportTotal `json:"total"`
	TestSuites []TestSuite     `json:"test_suites"`
}

// TestReportTotal is a summary's totals.
type TestReportTotal struct {
	Time       float64 `json:"time"`
	Count      int     `json:"count"`
	Success    int     `json:"success"`
	Failed     int     `json:"failed"`
	Skipped    int     `json:"skipped"`
	Error      int     `json:"error"`
	SuiteError *string `json:"suite_error"`
}

// Lint is GET /projects/:id/ci/lint.
type Lint struct {
	Valid      bool      `json:"valid"`
	Errors     []string  `json:"errors"`
	Warnings   []string  `json:"warnings"`
	MergedYAML string    `json:"merged_yaml"`
	Jobs       []LintJob `json:"jobs"`
}

// LintJob is one job of a linted configuration, with include_jobs.
type LintJob struct {
	Name         string `json:"name"`
	Stage        string `json:"stage"`
	When         string `json:"when"`
	AllowFailure bool   `json:"allow_failure"`
}

// Label is one row of GET /projects/:id/labels.
type Label struct {
	ID                     int64  `json:"id"`
	Name                   string `json:"name"`
	Color                  string `json:"color"`
	Description            string `json:"description"`
	OpenIssuesCount        *int   `json:"open_issues_count"`
	ClosedIssuesCount      *int   `json:"closed_issues_count"`
	OpenMergeRequestsCount *int   `json:"open_merge_requests_count"`
	Priority               *int   `json:"priority"`
	IsProjectLabel         bool   `json:"is_project_label"`
	Archived               bool   `json:"archived"`
}

// IssueLink is POST /projects/:id/issues/:iid/links' answer.
type IssueLink struct {
	ID          int64      `json:"id"`
	LinkType    string     `json:"link_type"`
	SourceIssue IssueBasic `json:"source_issue"`
	TargetIssue IssueBasic `json:"target_issue"`
}

// IssueBasic is an issue as an issue link names it.
type IssueBasic struct {
	ID        int64  `json:"id"`
	IID       int64  `json:"iid"`
	ProjectID int64  `json:"project_id"`
	Title     string `json:"title"`
	State     string `json:"state"`
	WebURL    string `json:"web_url"`
}

// RelatedIssue is one row of GET /projects/:id/issues/:iid/links: the
// linked issue, and the link.
type RelatedIssue struct {
	IID         int64  `json:"iid"`
	ProjectID   int64  `json:"project_id"`
	Title       string `json:"title"`
	State       string `json:"state"`
	WebURL      string `json:"web_url"`
	IssueLinkID int64  `json:"issue_link_id"`
	LinkType    string `json:"link_type"`
}

// LinkedMergeRequest is one row of the merge request lists that link:
// GET /projects/:id/issues/:iid/related_merge_requests and …/closed_by,
// and GET /projects/:id/repository/commits/:sha/merge_requests.
type LinkedMergeRequest struct {
	IID        int64      `json:"iid"`
	ProjectID  int64      `json:"project_id"`
	Title      string     `json:"title"`
	State      string     `json:"state"`
	WebURL     string     `json:"web_url"`
	References References `json:"references"`
}

// LinkedIssue is one row of GET /projects/:id/merge_requests/:iid/
// closes_issues and …/related_issues. A row is an issue, or an issue in
// an external tracker: {title, id} with a string id such as "PROJ-123".
// The issue rows carry no references.
type LinkedIssue struct {
	// ID is a number for an issue and a string for an external one.
	ID        json.RawMessage `json:"id"`
	IID       int64           `json:"iid"`
	ProjectID int64           `json:"project_id"`
	Title     string          `json:"title"`
	State     string          `json:"state"`
	WebURL    string          `json:"web_url"`
}

// ExternalID is an external tracker's id for the issue, "" for a GitLab
// issue.
func (l LinkedIssue) ExternalID() string {
	var id string
	if json.Unmarshal(l.ID, &id) != nil {
		return ""
	}
	return id
}

// ProtectedTag is one row of GET /projects/:id/protected_tags. Name may
// be a wildcard.
type ProtectedTag struct {
	Name string `json:"name"`
}

// BlameRange is one row of GET …/repository/files/:path/blame: a run of
// lines and the commit that last changed them.
type BlameRange struct {
	Commit BlameCommit `json:"commit"`
	Lines  []string    `json:"lines"`
}

// BlameCommit is the commit a blame range names.
type BlameCommit struct {
	ID           string    `json:"id"`
	AuthorName   string    `json:"author_name"`
	AuthoredDate time.Time `json:"authored_date"`
	Message      string    `json:"message"`
}

// ArtifactEntry is one row of GET /projects/:id/jobs/:id/artifacts/tree.
type ArtifactEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"` // file or directory
	Size *int64 `json:"size"`
}

// MergeRequestRebase is what rebase_merge_request reads of a merge
// request afterwards. RebaseInProgress is sent only when asked for with
// include_rebase_in_progress; MergeError is why the last merge or rebase
// failed.
type MergeRequestRebase struct {
	SHA              string  `json:"sha"`
	MergeError       *string `json:"merge_error"`
	RebaseInProgress *bool   `json:"rebase_in_progress"`
}

// RebaseState is PUT …/merge_requests/:iid/rebase's answer.
type RebaseState struct {
	RebaseInProgress bool `json:"rebase_in_progress"`
}

// ProjectMilestone is one row of GET /projects/:id/milestones and
// GET /groups/:id/milestones.
type ProjectMilestone struct {
	ID        int64     `json:"id"`
	IID       int64     `json:"iid"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	DueDate   string    `json:"due_date"`   // a date, not an instant
	StartDate string    `json:"start_date"` // a date, not an instant
	Expired   *bool     `json:"expired"`
	UpdatedAt time.Time `json:"updated_at"`
	WebURL    string    `json:"web_url"`
	// Description and CreatedAt are read only by the milestone writes.
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

// Board is one row of GET /projects/:id/boards, its lists included.
// The scope fields are absent unless the project has scoped boards, a
// paid feature.
type Board struct {
	ID              int64       `json:"id"`
	Name            string      `json:"name"`
	HideBacklogList bool        `json:"hide_backlog_list"`
	HideClosedList  bool        `json:"hide_closed_list"`
	Lists           []BoardList `json:"lists"`
	// Milestone is a milestone, or one of GitLab's filters Any, None,
	// Upcoming and Started, which carry a title and no id.
	Milestone *BoardTimebox `json:"milestone"`
	Assignee  *BoardUser    `json:"assignee"`
	Labels    []BoardLabel  `json:"labels"`
	// Weight is -1 for any weight and -2 for none.
	Weight *int `json:"weight"`
}

// BoardList is one list of a board. GitLab names no kind: the key
// present says which it is. The Open and Closed lists are never here.
type BoardList struct {
	ID        int64         `json:"id"`
	Position  *int          `json:"position"`
	Label     *BoardLabel   `json:"label"`
	Assignee  *BoardUser    `json:"assignee"`
	Milestone *BoardTimebox `json:"milestone"`
	Iteration *BoardTimebox `json:"iteration"`
}

// BoardLabel is a label a board or a list names.
type BoardLabel struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// BoardUser is the user a board or a list names.
type BoardUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

// BoardTimebox is the milestone or iteration a board or a list names.
type BoardTimebox struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

// Member is one row of GET /projects/:id/members/all. The email an
// administrator is shown is never declared.
type Member struct {
	ID          int64   `json:"id"`
	Username    string  `json:"username"`
	Name        string  `json:"name"`
	State       string  `json:"state"`
	AccessLevel int     `json:"access_level"`
	ExpiresAt   *string `json:"expires_at"` // a date, not an instant
}

// Todo is one row of GET /todos.
type Todo struct {
	ID         int64        `json:"id"`
	Project    *TodoProject `json:"project"`
	Author     UserBasic    `json:"author"`
	ActionName string       `json:"action_name"`
	TargetType string       `json:"target_type"`
	Target     TodoTarget   `json:"target"`
	TargetURL  string       `json:"target_url"`
	Body       string       `json:"body"`
	State      string       `json:"state"`
	CreatedAt  time.Time    `json:"created_at"`
}

// TodoProject is the project a to-do item belongs to.
type TodoProject struct {
	ID                int64  `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
}

// TodoTarget is the issue, merge request or other item a to-do item
// points at. Its shape depends on TargetType; only what they share is
// read. The id is not: a commit's is its SHA, an issue's a number.
type TodoTarget struct {
	IID   int64  `json:"iid"`
	Title string `json:"title"`
	State string `json:"state"`
}

// DraftNote is one row of GET …/merge_requests/:iid/draft_notes: a
// review comment the signed-in account has not published.
type DraftNote struct {
	ID                int64     `json:"id"`
	AuthorID          int64     `json:"author_id"`
	Note              string    `json:"note"`
	DiscussionID      *string   `json:"discussion_id"`
	ResolveDiscussion bool      `json:"resolve_discussion"`
	CommitID          *string   `json:"commit_id"`
	Position          *Position `json:"position"`
	// LineCode is the line a draft on the diff is on, as GitLab computed
	// it from the position; null for a general draft.
	LineCode *string `json:"line_code"`
}

// SearchHit is one row of GET /search and its group and project forms,
// for every scope but commits. Its shape depends on the scope; this is
// the union of the fields read, and each scope fills its own.
type SearchHit struct {
	// Issues, merge requests, milestones, projects, users, notes.
	ID        int64      `json:"id"`
	IID       int64      `json:"iid"`
	ProjectID int64      `json:"project_id"`
	Title     string     `json:"title"`
	State     string     `json:"state"`
	WebURL    string     `json:"web_url"`
	UpdatedAt *time.Time `json:"updated_at"`
	Author    *UserBasic `json:"author"`
	// Projects.
	PathWithNamespace string `json:"path_with_namespace"`
	// Projects and users.
	Name string `json:"name"`
	// Users.
	Username string `json:"username"`
	// Blobs and wiki blobs.
	Path      string `json:"path"`
	Ref       string `json:"ref"`
	Startline int    `json:"startline"`
	Data      string `json:"data"`
	// Notes.
	Body         string `json:"body"`
	NoteableType string `json:"noteable_type"` //nolint:misspell // GitLab's wire name
	NoteableIID  *int64 `json:"noteable_iid"`  //nolint:misspell // GitLab's wire name
}

// SearchCommit is one row of a commits search: a commit, and the project
// it is in.
type SearchCommit struct {
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	AuthorName    string    `json:"author_name"`
	CommittedDate time.Time `json:"committed_date"`
	WebURL        string    `json:"web_url"`
	ProjectID     int64     `json:"project_id"`
}

// WikiPageBasic is one row of GET /projects/:id/wikis.
type WikiPageBasic struct {
	Format string `json:"format"`
	Slug   string `json:"slug"`
	Title  string `json:"title"`
}

// WikiPage is GET /projects/:id/wikis/:slug. GitLab exposes no version
// and no updated_at, so the server's witness is a hash of Content
// (§4.6).
type WikiPage struct {
	Format   string `json:"format"`
	Slug     string `json:"slug"`
	Title    string `json:"title"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

// Snippet is GET /snippets/:id and /projects/:id/snippets/:snippet_id.
// ProjectID is null for a personal snippet. raw_url is not decoded: no
// URL a response names is ever called (§11).
type Snippet struct {
	ID          int64         `json:"id"`
	Title       string        `json:"title"`
	Description *string       `json:"description"`
	Visibility  string        `json:"visibility"`
	Author      UserBasic     `json:"author"`
	FileName    string        `json:"file_name"`
	Files       []SnippetFile `json:"files"`
	ProjectID   *int64        `json:"project_id"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
	WebURL      string        `json:"web_url"`
}

// SnippetFile is one file of a snippet's repository.
type SnippetFile struct {
	Path string `json:"path"`
}

// Release is GET /projects/:id/releases/:tag_name and a row of the
// listing.
type Release struct {
	TagName         string             `json:"tag_name"`
	Name            string             `json:"name"`
	Description     *string            `json:"description"`
	CreatedAt       time.Time          `json:"created_at"`
	ReleasedAt      *time.Time         `json:"released_at"`
	UpcomingRelease bool               `json:"upcoming_release"`
	Author          *UserBasic         `json:"author"`
	Commit          *ReleaseCommit     `json:"commit"`
	Milestones      []ReleaseMilestone `json:"milestones"`
	Assets          *ReleaseAssets     `json:"assets"`
}

// ReleaseCommit is the commit a release's tag points at.
type ReleaseCommit struct {
	ID      string `json:"id"`
	ShortID string `json:"short_id"`
	Title   string `json:"title"`
}

// ReleaseMilestone is a milestone a release names.
type ReleaseMilestone struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

// ReleaseAssets counts a release's assets: its links and source archives.
type ReleaseAssets struct {
	Count int           `json:"count"`
	Links []ReleaseLink `json:"links"`
}

// ReleaseLink is an asset link of a release.
type ReleaseLink struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	LinkType string `json:"link_type"`
}

// Environment is one row of GET /projects/:id/environments.
type Environment struct {
	ID             int64                  `json:"id"`
	Name           string                 `json:"name"`
	Slug           string                 `json:"slug"`
	State          string                 `json:"state"`
	Tier           string                 `json:"tier"`
	ExternalURL    *string                `json:"external_url"`
	CreatedAt      time.Time              `json:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
	AutoStopAt     *time.Time             `json:"auto_stop_at"`
	LastDeployment *EnvironmentDeployment `json:"last_deployment"`
}

// EnvironmentDeployment is the last deployment an environment names.
type EnvironmentDeployment struct {
	ID        int64     `json:"id"`
	IID       int64     `json:"iid"`
	Status    string    `json:"status"`
	Ref       string    `json:"ref"`
	SHA       string    `json:"sha"`
	CreatedAt time.Time `json:"created_at"`
}

// Deployment is one row of GET /projects/:id/deployments.
type Deployment struct {
	ID          int64                 `json:"id"`
	IID         int64                 `json:"iid"`
	Status      string                `json:"status"`
	Ref         string                `json:"ref"`
	SHA         string                `json:"sha"`
	CreatedAt   time.Time             `json:"created_at"`
	UpdatedAt   *time.Time            `json:"updated_at"`
	User        *UserBasic            `json:"user"`
	Environment DeploymentEnvironment `json:"environment"`
	Deployable  *DeploymentJob        `json:"deployable"`
}

// DeploymentEnvironment is the environment a deployment went to.
type DeploymentEnvironment struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// DeploymentJob is the job that ran a deployment.
type DeploymentJob struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Event is one row of GET /events and /projects/:id/events.
type Event struct {
	ID             int64      `json:"id"`
	ActionName     string     `json:"action_name"`
	TargetType     *string    `json:"target_type"`
	TargetID       *int64     `json:"target_id"`
	TargetIID      *int64     `json:"target_iid"`
	TargetTitle    *string    `json:"target_title"`
	AuthorUsername string     `json:"author_username"`
	ProjectID      *int64     `json:"project_id"`
	CreatedAt      time.Time  `json:"created_at"`
	PushData       *EventPush `json:"push_data"`
}

// EventPush is what a push event carries.
type EventPush struct {
	Action      string  `json:"action"`
	RefType     string  `json:"ref_type"`
	Ref         *string `json:"ref"`
	CommitCount int     `json:"commit_count"`
	CommitTitle *string `json:"commit_title"`
}

// LabelEvent is one row of GET …/issues|merge_requests/:iid/
// resource_label_events.
type LabelEvent struct {
	ID        int64      `json:"id"`
	User      *UserBasic `json:"user"`
	CreatedAt time.Time  `json:"created_at"`
	// Label is null for a label deleted since.
	Label  *EventLabel `json:"label"`
	Action string      `json:"action"` // add or remove
}

// EventLabel is the label a label event names.
type EventLabel struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// StateEvent is one row of GET …/issues|merge_requests/:iid/
// resource_state_events. SourceMergeRequestID is a global id, not an iid.
type StateEvent struct {
	ID                   int64      `json:"id"`
	User                 *UserBasic `json:"user"`
	CreatedAt            time.Time  `json:"created_at"`
	State                string     `json:"state"`
	SourceCommit         *string    `json:"source_commit"`
	SourceMergeRequestID *int64     `json:"source_merge_request_id"`
}

// MilestoneEvent is one row of GET …/issues|merge_requests/:iid/
// resource_milestone_events.
type MilestoneEvent struct {
	ID        int64      `json:"id"`
	User      *UserBasic `json:"user"`
	CreatedAt time.Time  `json:"created_at"`
	Milestone *Milestone `json:"milestone"`
	Action    string     `json:"action"` // add or remove
}

// WeightEvent is one row of GET …/issues/:iid/resource_weight_events.
// Weight is null when the weight was removed.
type WeightEvent struct {
	ID        int64      `json:"id"`
	User      *UserBasic `json:"user"`
	CreatedAt time.Time  `json:"created_at"`
	Weight    *int       `json:"weight"`
}
