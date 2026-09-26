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

// Created is CreatedAt as a time.
func (t TokenInfo) Created() time.Time { return time.Unix(t.CreatedAt, 0).UTC() }

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
	ID                int64      `json:"id"`
	Name              string     `json:"name"`
	NameWithNamespace string     `json:"name_with_namespace"`
	Path              string     `json:"path"`
	PathWithNamespace string     `json:"path_with_namespace"`
	Description       string     `json:"description"`
	DefaultBranch     string     `json:"default_branch"`
	Visibility        string     `json:"visibility"`
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
	DetailedMergeStatus         string         `json:"detailed_merge_status"`
	HasConflicts                bool           `json:"has_conflicts"`
	BlockingDiscussionsResolved bool           `json:"blocking_discussions_resolved"`
	SHA                         string         `json:"sha"`
	MergeCommitSHA              *string        `json:"merge_commit_sha"`
	SquashCommitSHA             *string        `json:"squash_commit_sha"`
	Squash                      bool           `json:"squash"`
	UserNotesCount              int            `json:"user_notes_count"`
	ChangesCount                string         `json:"changes_count"` // "12" or "1000+"
	WebURL                      string         `json:"web_url"`
	References                  References     `json:"references"`
	DiffRefs                    *DiffRefs      `json:"diff_refs"`
	HeadPipeline                *PipelineBasic `json:"head_pipeline"`
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
