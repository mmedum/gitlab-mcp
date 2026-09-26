// Package model is the server's view of what GitLab returns: the shapes
// every tool result carries as structuredContent, under its output
// schema.
//
// These are not the wire types (internal/gitlab). A wire type is what
// GitLab sends; a model type is what this server promises, and the
// released surface is a contract `scripts/gates schema-diff` holds
// (CLAUDE.md rule 17). Fields are added, never renamed or removed.
//
// Text someone other than the signed-in person wrote — titles,
// descriptions, comments, commit messages, file contents — sits in
// fields named untrusted_* (docs/architecture.md §4.1.1), so a client
// that reads only the structured half still knows which parts are data.
package model

import "time"

// ProjectRef names a project by both its id and its path (§6.1).
type ProjectRef struct {
	ID   int64  `json:"id" jsonschema:"The numeric project id; the stable way to address it"`
	Path string `json:"path" jsonschema:"The full path, such as group/sub/project"`
	// MovedFrom is set when the project was addressed by a path GitLab
	// reported as renamed or transferred.
	MovedFrom *string `json:"moved_from" jsonschema:"The old path the project was addressed by, when GitLab reported it moved; null otherwise"`
}

// User is a GitLab account as embedded in issues, merge requests and
// notes.
type User struct {
	Username string `json:"username"`
	Name     string `json:"name"`
}

// Milestone is a compact milestone.
type Milestone struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
}

// Listing says what part of a listing a result holds (§4.8, §7.1).
type Listing struct {
	Returned int  `json:"returned" jsonschema:"Rows in this result"`
	Complete bool `json:"complete" jsonschema:"True when there is no further page"`
	// Total is GitLab's count, null when GitLab did not say: past
	// 10,000 rows and on keyset pages it stops counting.
	Total         *int    `json:"total" jsonschema:"GitLab's total count, or null when it is unknown"`
	NextPageToken *string `json:"next_page_token" jsonschema:"Pass as page_token to continue; null when complete"`
}

// Budget reports how much of one text was shown (§4.8).
type Budget struct {
	BudgetChars int `json:"budget_chars" jsonschema:"The most characters this result shows of the text"`
	TotalChars  int `json:"total_chars" jsonschema:"The text's length in characters, after hidden text was removed"`
	Offset      int `json:"offset" jsonschema:"The character offset the shown part starts at"`
	ShownChars  int `json:"shown_chars"`
	// ContinueOffset is where to continue from, null when the text was
	// shown to its end.
	ContinueOffset *int `json:"continue_offset" jsonschema:"Pass as offset to read on; null when shown to the end"`
	// HiddenRemoved counts characters dropped or made visible because a
	// reader of the rendered page would not see them (§4.1.2).
	HiddenRemoved int `json:"hidden_chars_removed" jsonschema:"Zero-width, bidirectional-control and HTML-comment characters removed or made visible"`
}

// DiscussionSummary counts an issue's or merge request's threads.
type DiscussionSummary struct {
	// Known is false when the threads could not be read; the counts are
	// then zero and mean nothing.
	Known        bool       `json:"known"`
	Threads      int        `json:"threads" jsonschema:"Threads with at least one comment written by a person"`
	Unresolved   int        `json:"unresolved" jsonschema:"Resolvable threads not yet resolved"`
	LastActivity *time.Time `json:"last_activity"`
	// Complete is false when there were more threads than were read.
	Complete bool `json:"complete"`
}

// ---------------------------------------------------------------- get_me

// Me is get_me's result (§7.9).
type Me struct {
	User       MeUser       `json:"user"`
	Instance   InstanceInfo `json:"instance"`
	Token      TokenInfo    `json:"token"`
	Registered Registration `json:"registered"`
	WriteScope WriteScope   `json:"write_namespaces"`
	Rate       RateReading  `json:"rate_limit"`
	Notes      []string     `json:"notes"`
}

// MeUser is the signed-in account.
type MeUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	State    string `json:"state"`
	Bot      bool   `json:"bot"`
	Admin    bool   `json:"admin"`
}

// InstanceInfo is the configured instance and what it reported.
type InstanceInfo struct {
	URL string `json:"url"`
	// Known is false when /metadata could not be read.
	Known   bool   `json:"known"`
	Version string `json:"version"`
	Edition string `json:"edition" jsonschema:"Community or Enterprise"`
}

// TokenInfo describes the signed-in token, never its value.
type TokenInfo struct {
	Kind      string     `json:"kind" jsonschema:"How the token was obtained: oauth"`
	Scopes    []string   `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at" jsonschema:"When the access token expires; it is refreshed before then"`
}

// Registration is what this server registered.
type Registration struct {
	ReadOnly bool     `json:"read_only"`
	Kinds    []string `json:"kinds" jsonschema:"read, write, ship, destructive: the kinds with at least one tool registered"`
	Toolsets []string `json:"toolsets" jsonschema:"The optional toolsets turned on"`
	Tools    int      `json:"tools"`
}

// WriteScope is the write allow-list (§4.7).
type WriteScope struct {
	Confined   bool     `json:"confined" jsonschema:"False means writes may go wherever the token can write"`
	Namespaces []string `json:"namespaces"`
}

// RateReading is the last rate-limit reading GitLab sent (§2.13).
type RateReading struct {
	Known     bool       `json:"known" jsonschema:"False until GitLab sent RateLimit headers; many instances never do"`
	Limit     int        `json:"limit"`
	Remaining int        `json:"remaining"`
	Reset     *time.Time `json:"reset"`
	Observed  *time.Time `json:"observed"`
}

// ------------------------------------------------------------ resolve_url

// Resolved is resolve_url's result: a web URL as tool arguments (§6.1).
type Resolved struct {
	Kind    string `json:"kind" jsonschema:"project, issue, merge_request, file, commit, compare, pipeline, job or wiki"`
	Project string `json:"project" jsonschema:"The project's full path"`
	IID     *int64 `json:"iid"`
	ID      *int64 `json:"id" jsonschema:"A pipeline or job id"`
	SHA     string `json:"sha"`
	Ref     string `json:"ref"`
	Path    string `json:"path"`
	Line    *int   `json:"line"`
	EndLine *int   `json:"end_line"`
	From    string `json:"from"`
	To      string `json:"to"`
	Slug    string `json:"slug"`
	Note    *int64 `json:"note_id"`
	// RefCandidates lists the ways a ref with slashes could split from
	// the path when no lookup settled it.
	RefCandidates []RefSplit `json:"ref_candidates" jsonschema:"Other ways the URL's ref and path could split, when a lookup did not settle it"`
	// Tool is the registered tool that reads this kind, empty when none
	// is.
	Tool      string         `json:"tool" jsonschema:"The tool that reads this, or empty when none is registered"`
	Arguments map[string]any `json:"arguments" jsonschema:"The arguments to pass that tool"`
}

// RefSplit is one way to split a ref from a path.
type RefSplit struct {
	Ref  string `json:"ref"`
	Path string `json:"path"`
}

// ---------------------------------------------------------------- projects

// ProjectRow is one row of search_projects.
type ProjectRow struct {
	ID             int64      `json:"id"`
	Path           string     `json:"path"`
	Visibility     string     `json:"visibility"`
	DefaultBranch  string     `json:"default_branch"`
	Archived       bool       `json:"archived"`
	LastActivityAt *time.Time `json:"last_activity_at"`
	// UntrustedName is the display name, which any maintainer sets.
	UntrustedName string `json:"untrusted_name"`
}

// ProjectList is search_projects' result.
type ProjectList struct {
	Projects []ProjectRow `json:"projects"`
	Listing  Listing      `json:"listing"`
}

// Project is get_project's result.
type Project struct {
	Project              ProjectRef `json:"project"`
	WebURL               string     `json:"web_url"`
	Visibility           string     `json:"visibility"`
	DefaultBranch        string     `json:"default_branch"`
	Archived             bool       `json:"archived"`
	EmptyRepo            bool       `json:"empty_repo"`
	Topics               []string   `json:"topics"`
	Stars                int        `json:"star_count"`
	Forks                int        `json:"forks_count"`
	OpenIssues           *int       `json:"open_issues_count" jsonschema:"Null when issues are turned off"`
	CreatedAt            time.Time  `json:"created_at"`
	LastActivityAt       *time.Time `json:"last_activity_at"`
	Namespace            string     `json:"namespace" jsonschema:"The group or user path the project lives under"`
	NamespaceKind        string     `json:"namespace_kind" jsonschema:"group or user"`
	UntrustedName        string     `json:"untrusted_name"`
	UntrustedDescription string     `json:"untrusted_description"`
}

// ---------------------------------------------------------------- issues

// ItemRow is one row of search_issues or search_merge_requests.
type ItemRow struct {
	Project   ProjectRef `json:"project"`
	IID       int64      `json:"iid"`
	Reference string     `json:"reference" jsonschema:"GitLab's full reference, group/project#12 or group/project!12"`
	State     string     `json:"state"`
	Draft     bool       `json:"draft"`
	Author    string     `json:"author" jsonschema:"The author's username"`
	Labels    []string   `json:"labels"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	// UntrustedTitle is shortened to one line.
	UntrustedTitle string `json:"untrusted_title"`
}

// ItemList is search_issues' and search_merge_requests' result.
type ItemList struct {
	Items   []ItemRow `json:"items"`
	Listing Listing   `json:"listing"`
}

// Issue is get_issue's result.
type Issue struct {
	Project        ProjectRef        `json:"project"`
	IID            int64             `json:"iid"`
	Reference      string            `json:"reference"`
	WebURL         string            `json:"web_url"`
	State          string            `json:"state"`
	Type           string            `json:"issue_type"`
	Confidential   bool              `json:"confidential"`
	Author         User              `json:"author"`
	Assignees      []User            `json:"assignees"`
	Labels         []string          `json:"labels"`
	Milestone      *Milestone        `json:"milestone"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at" jsonschema:"The witness a later update carries"`
	ClosedAt       *time.Time        `json:"closed_at"`
	ClosedBy       *User             `json:"closed_by"`
	DueDate        *string           `json:"due_date" jsonschema:"A date, not an instant: 2026-10-01"`
	Tasks          *Tasks            `json:"tasks"`
	Discussions    DiscussionSummary `json:"discussions"`
	UntrustedTitle string            `json:"untrusted_title"`
	// UntrustedDescription is the part of the description the budget
	// allowed, with hidden text removed.
	UntrustedDescription string `json:"untrusted_description"`
	DescriptionBudget    Budget `json:"description_budget"`
}

// Tasks counts a description's checkboxes.
type Tasks struct {
	Count     int `json:"count"`
	Completed int `json:"completed"`
}

// ------------------------------------------------------------ merge requests

// MergeRequest is get_merge_request's result.
type MergeRequest struct {
	Project              ProjectRef        `json:"project"`
	IID                  int64             `json:"iid"`
	Reference            string            `json:"reference"`
	WebURL               string            `json:"web_url"`
	State                string            `json:"state"`
	Draft                bool              `json:"draft"`
	Author               User              `json:"author"`
	Assignees            []User            `json:"assignees"`
	Reviewers            []User            `json:"reviewers"`
	Labels               []string          `json:"labels"`
	Milestone            *Milestone        `json:"milestone"`
	SourceBranch         string            `json:"source_branch"`
	TargetBranch         string            `json:"target_branch"`
	SourceProjectID      int64             `json:"source_project_id" jsonschema:"Differs from the project id for a merge request from a fork"`
	SHA                  string            `json:"sha" jsonschema:"The head commit; the witness merging and approving carry"`
	DiffRefs             *DiffRefs         `json:"diff_refs"`
	DetailedMergeStatus  string            `json:"detailed_merge_status" jsonschema:"GitLab's own value, such as mergeable or not_approved; new values appear between releases"`
	HasConflicts         bool              `json:"has_conflicts"`
	ChangesCount         string            `json:"changes_count" jsonschema:"Files changed, as GitLab counts them: 12, or 1000+"`
	HeadPipeline         *Pipeline         `json:"head_pipeline"`
	Approvals            *Approvals        `json:"approvals" jsonschema:"Null when the approval state could not be read"`
	CreatedAt            time.Time         `json:"created_at"`
	UpdatedAt            time.Time         `json:"updated_at"`
	MergedAt             *time.Time        `json:"merged_at"`
	ClosedAt             *time.Time        `json:"closed_at"`
	MergedBy             *User             `json:"merged_by"`
	Discussions          DiscussionSummary `json:"discussions"`
	UntrustedTitle       string            `json:"untrusted_title"`
	UntrustedDescription string            `json:"untrusted_description"`
	DescriptionBudget    Budget            `json:"description_budget"`
}

// DiffRefs are the three SHAs a diff position is computed against.
type DiffRefs struct {
	BaseSHA  string `json:"base_sha"`
	StartSHA string `json:"start_sha"`
	HeadSHA  string `json:"head_sha"`
}

// Pipeline is a compact pipeline.
type Pipeline struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	Ref    string `json:"ref"`
	SHA    string `json:"sha"`
}

// Approvals is a merge request's approval state. Required and Left are
// Premium fields, null on Community Edition.
type Approvals struct {
	Approved   bool     `json:"approved"`
	Required   *int     `json:"required"`
	Left       *int     `json:"left"`
	ApprovedBy []string `json:"approved_by" jsonschema:"Usernames"`
}

// ------------------------------------------------------------ discussions

// Discussions is list_discussions' result.
type Discussions struct {
	Project ProjectRef `json:"project"`
	IID     int64      `json:"iid"`
	Type    string     `json:"type" jsonschema:"issue or merge_request"`
	Threads []Thread   `json:"threads" jsonschema:"Newest activity first"`
	// NotShown lists the threads of this page the budget left out; they
	// come first on the next page.
	NotShown []ThreadStub `json:"not_shown" jsonschema:"Threads of this page left out by the character budget; the next page starts with them"`
	Budget   int          `json:"budget_chars"`
	Listing  Listing      `json:"listing"`
}

// Thread is one discussion.
type Thread struct {
	ID           string        `json:"id"`
	Individual   bool          `json:"individual_note" jsonschema:"A single comment rather than a thread"`
	Resolvable   bool          `json:"resolvable"`
	Resolved     bool          `json:"resolved"`
	Position     *DiffPosition `json:"position" jsonschema:"Where a diff thread sits; null for a general one"`
	LastActivity time.Time     `json:"last_activity"`
	Notes        []Note        `json:"notes"`
}

// ThreadStub names a thread that was not shown.
type ThreadStub struct {
	ID           string    `json:"id"`
	Author       string    `json:"author"`
	Notes        int       `json:"notes"`
	LastActivity time.Time `json:"last_activity"`
}

// DiffPosition is where a diff note sits.
type DiffPosition struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	OldLine *int   `json:"old_line"`
	NewLine *int   `json:"new_line"`
	HeadSHA string `json:"head_sha"`
}

// Note is one comment.
type Note struct {
	ID            int64     `json:"id"`
	Author        User      `json:"author"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	System        bool      `json:"system" jsonschema:"Written by GitLab to record an event, not by a person"`
	Internal      bool      `json:"internal"`
	UntrustedBody string    `json:"untrusted_body"`
	Budget        Budget    `json:"body_budget"`
}

// ------------------------------------------------------------ repository

// File is get_file's result.
type File struct {
	Project ProjectRef `json:"project"`
	Path    string     `json:"path"`
	Ref     string     `json:"ref"`
	Size    int64      `json:"size_bytes"`
	BlobID  string     `json:"blob_id"`
	// LastCommitID is the witness an update or delete of this file
	// carries (§4.6).
	LastCommitID string `json:"last_commit_id" jsonschema:"The last commit that changed this file; the witness a later edit carries"`
	CommitID     string `json:"commit_id" jsonschema:"The commit the ref resolved to"`
	SHA256       string `json:"content_sha256"`
	Binary       bool   `json:"binary" jsonschema:"True when the content is not text; only metadata is returned"`
	ContentType  string `json:"content_type" jsonschema:"A guess from the first bytes"`
	// UntrustedContent is empty for a binary file.
	UntrustedContent string `json:"untrusted_content"`
	Budget           Budget `json:"content_budget"`
}

// TreeEntry is one row of list_tree.
type TreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type" jsonschema:"blob (a file), tree (a directory) or commit (a submodule)"`
	Mode string `json:"mode"`
	ID   string `json:"id"`
}

// Tree is list_tree's result.
type Tree struct {
	Project ProjectRef  `json:"project"`
	Ref     string      `json:"ref"`
	Path    string      `json:"path"`
	Entries []TreeEntry `json:"entries"`
	Listing Listing     `json:"listing"`
}

// Branch is one row of list_branches.
type Branch struct {
	Name                 string    `json:"name"`
	Default              bool      `json:"default"`
	Protected            bool      `json:"protected"`
	Merged               bool      `json:"merged"`
	CanPush              bool      `json:"can_push" jsonschema:"Whether the signed-in account may push to it"`
	CommitID             string    `json:"commit_id"`
	CommittedAt          time.Time `json:"committed_at"`
	UntrustedCommitTitle string    `json:"untrusted_commit_title"`
}

// Branches is list_branches' result.
type Branches struct {
	Project  ProjectRef `json:"project"`
	Branches []Branch   `json:"branches"`
	Listing  Listing    `json:"listing"`
}

// CommitRow is one row of list_commits.
type CommitRow struct {
	ID             string    `json:"id"`
	ShortID        string    `json:"short_id"`
	AuthorName     string    `json:"author_name"`
	AuthoredAt     time.Time `json:"authored_at"`
	CommittedAt    time.Time `json:"committed_at"`
	Parents        int       `json:"parents"`
	UntrustedTitle string    `json:"untrusted_title"`
}

// Commits is list_commits' result.
type Commits struct {
	Project ProjectRef  `json:"project"`
	Ref     string      `json:"ref"`
	Commits []CommitRow `json:"commits"`
	Listing Listing     `json:"listing"`
}

// Commit is get_commit's result.
type Commit struct {
	Project          ProjectRef   `json:"project"`
	ID               string       `json:"id"`
	ShortID          string       `json:"short_id"`
	WebURL           string       `json:"web_url"`
	AuthorName       string       `json:"author_name"`
	AuthoredAt       time.Time    `json:"authored_at"`
	CommitterName    string       `json:"committer_name"`
	CommittedAt      time.Time    `json:"committed_at"`
	ParentIDs        []string     `json:"parent_ids"`
	Additions        int          `json:"additions"`
	Deletions        int          `json:"deletions"`
	UntrustedMessage string       `json:"untrusted_message"`
	Files            []FileDiff   `json:"files" jsonschema:"Changed files whose diff fit the budget"`
	NotShown         []FileChange `json:"files_not_shown" jsonschema:"Changed files left out by the budget or cut by GitLab"`
	FilesComplete    bool         `json:"files_complete" jsonschema:"False when the commit changes more files than were read"`
	NextFileOffset   *int         `json:"next_file_offset" jsonschema:"Pass as file_offset to see the diffs left out by the budget; null when none were"`
	DiffBudget       int          `json:"diff_budget_chars"`
	HiddenRemoved    int          `json:"hidden_chars_removed"`
}

// FileChange names a changed file.
type FileChange struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	Status  string `json:"status" jsonschema:"added, deleted, renamed or modified"`
	// Reason says why its diff is not shown: budget, too_large or
	// collapsed.
	Reason string `json:"reason"`
}

// FileDiff is one file's diff.
type FileDiff struct {
	OldPath       string `json:"old_path"`
	NewPath       string `json:"new_path"`
	Status        string `json:"status"`
	UntrustedDiff string `json:"untrusted_diff"`
	// Truncated marks a single diff larger than the whole budget, cut
	// at it; get_file reads the file itself.
	Truncated bool `json:"truncated" jsonschema:"True when this one diff was over the whole budget and was cut; get_file reads the file"`
}
