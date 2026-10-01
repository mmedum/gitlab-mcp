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
	Known     bool       `json:"known" jsonschema:"False until GitLab sent RateLimit headers, which not every response carries"`
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
	DescriptionBudget    Budget     `json:"description_budget"`
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
	// RelatedMergeRequests and ClosingMergeRequests are null when GitLab
	// refused the read, and when offset continues the description.
	RelatedMergeRequests *LinkedItems `json:"related_merge_requests" jsonschema:"Merge requests that mention the issue or that it mentions, the first 20. GitLab leaves out any the account cannot read. Null when they could not be read, and on a read with an offset, which does not read them again"`
	ClosingMergeRequests *LinkedItems `json:"closing_merge_requests" jsonschema:"Merge requests that close the issue when merged, the first 20. GitLab lists only those in the issue's own project and leaves out any the account cannot read. Null when they could not be read, and on a read with an offset, which does not read them again"`
	TimeStats            TimeStats    `json:"time_stats"`
	Upvotes              int          `json:"upvotes" jsonschema:"How many reacted with thumbsup; GitLab counts no other emoji here"`
	Downvotes            int          `json:"downvotes" jsonschema:"How many reacted with thumbsdown"`
}

// TimeStats is an issue's or a merge request's time tracking, as GitLab
// reports it.
type TimeStats struct {
	TimeEstimate        int64  `json:"time_estimate" jsonschema:"The estimate in seconds; 0 when none"`
	TotalTimeSpent      int64  `json:"total_time_spent" jsonschema:"The time spent in seconds, summed over every entry"`
	HumanTimeEstimate   string `json:"human_time_estimate" jsonschema:"The estimate as GitLab writes it, such as 10h 30m: gitlab.com writes hours and minutes, not days; empty when none"`
	HumanTotalTimeSpent string `json:"human_total_time_spent" jsonschema:"The time spent as GitLab writes it; empty when none"`
}

// Tasks counts a description's checkboxes.
type Tasks struct {
	Count     int `json:"count"`
	Completed int `json:"completed"`
}

// LinkedItems is the first page of the issues or merge requests an
// item is linked to, exactly as GitLab returned it.
type LinkedItems struct {
	Items []LinkedItem `json:"items"`
	More  bool         `json:"more" jsonschema:"True when GitLab has more than are shown"`
	// Total is GitLab's count, null when GitLab did not say.
	Total *int `json:"total" jsonschema:"GitLab's count, or null when it did not say"`
}

// LinkedItem is one linked issue or merge request.
type LinkedItem struct {
	Reference string `json:"reference" jsonschema:"GitLab's full reference, group/project#12 or group/project!12; empty for an external tracker's issue"`
	IID       int64  `json:"iid"`
	ProjectID int64  `json:"project_id"`
	State     string `json:"state"`
	WebURL    string `json:"web_url"`
	// External marks an issue in an external tracker, which GitLab names
	// by its id and a title alone.
	External bool `json:"external" jsonschema:"True for an issue in an external tracker, which has only a title and an id"`
	// ExternalID is that id, kept only when it is shaped like a tracker's
	// id; the title carries it either way.
	ExternalID *string `json:"external_id" jsonschema:"The id of an issue in an external tracker, such as PROJ-123; null for a GitLab item, and for an id not shaped like one"`
	// UntrustedTitle is shortened to one line.
	UntrustedTitle string `json:"untrusted_title"`
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
	ReviewerStates       *ReviewerStates   `json:"reviewer_states" jsonschema:"Each reviewer's review state, the first 100 as GitLab lists them; null when they could not be read"`
	CreatedAt            time.Time         `json:"created_at"`
	UpdatedAt            time.Time         `json:"updated_at"`
	MergedAt             *time.Time        `json:"merged_at"`
	ClosedAt             *time.Time        `json:"closed_at"`
	MergedBy             *User             `json:"merged_by"`
	Discussions          DiscussionSummary `json:"discussions"`
	UntrustedTitle       string            `json:"untrusted_title"`
	UntrustedDescription string            `json:"untrusted_description"`
	DescriptionBudget    Budget            `json:"description_budget"`
	// ClosesIssues and RelatedIssues are null when GitLab refused the
	// read, and when offset continues the description.
	ClosesIssues  *LinkedItems `json:"closes_issues" jsonschema:"Issues GitLab closes when this merges, the first 20. GitLab leaves out confidential or unreadable issues and those in projects that do not close issues automatically. Null when they could not be read, and on a read with an offset, which does not read them again"`
	RelatedIssues *LinkedItems `json:"related_issues" jsonschema:"Issues the title, description, comments or commits mention, the first 20. GitLab leaves out confidential or unreadable issues. Null when they could not be read, and on a read with an offset, which does not read them again"`
	TimeStats     TimeStats    `json:"time_stats"`
	Upvotes       int          `json:"upvotes" jsonschema:"How many reacted with thumbsup; GitLab counts no other emoji here"`
	Downvotes     int          `json:"downvotes" jsonschema:"How many reacted with thumbsdown"`
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

// ReviewerStates is where each reviewer of a merge request stands.
type ReviewerStates struct {
	Reviewers []ReviewerState `json:"reviewers"`
	More      bool            `json:"more" jsonschema:"True when GitLab has more reviewers than are shown"`
	// Total is GitLab's count, null when GitLab did not say.
	Total *int `json:"total" jsonschema:"GitLab's count, or null when it did not say"`
}

// ReviewerState is one reviewer and their review state.
type ReviewerState struct {
	User    User      `json:"user"`
	State   string    `json:"state" jsonschema:"unreviewed, review_started, reviewed, requested_changes, approved or unapproved; GitLab's own value, and new values appear between releases"`
	AddedAt time.Time `json:"added_at" jsonschema:"When the reviewer was asked"`
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

// ItemEvents is list_item_events' result: GitLab's resource events of
// an issue or a merge request, merged into one timeline.
type ItemEvents struct {
	Project ProjectRef  `json:"project"`
	IID     int64       `json:"iid"`
	Type    string      `json:"type" jsonschema:"issue or merge_request"`
	Events  []ItemEvent `json:"events" jsonschema:"Newest first"`
	// Since is set when GitLab has more events of one kind than one call
	// reads: the history then covers only what came after it.
	Since   *time.Time `json:"since" jsonschema:"Null when the history goes back to the start; otherwise it covers only events after this time, and events at or before it are not shown. It assumes GitLab's event ids follow time, which an imported item's may not"`
	Listing Listing    `json:"listing"`
}

// ItemEvent is one change to an issue or a merge request.
type ItemEvent struct {
	Kind      string    `json:"kind" jsonschema:"label, state, milestone or weight"`
	ID        int64     `json:"id" jsonschema:"GitLab's id for the event, unique within its kind"`
	CreatedAt time.Time `json:"created_at"`
	User      *User     `json:"user" jsonschema:"Who made the change; null when GitLab names no one"`
	Action    string    `json:"action" jsonschema:"For a label or a milestone: add or remove; empty otherwise"`
	// Label is the label's name, null for a label deleted since.
	Label        *string `json:"label" jsonschema:"For a label event, the label's exact name; null otherwise, and for a label deleted since"`
	LabelDeleted bool    `json:"label_deleted" jsonschema:"For a label event, true when the label has been deleted since"`
	State        *string `json:"state" jsonschema:"For a state event, the state it moved to: opened, closed, reopened, merged or locked"`
	// SourceCommit and SourceMergeRequestID say what closed or merged
	// the item, when GitLab recorded it.
	SourceCommit         *string    `json:"source_commit" jsonschema:"For a state event, the commit that caused it, when GitLab recorded one"`
	SourceMergeRequestID *int64     `json:"source_merge_request_id" jsonschema:"For a state event, the merge request that caused it, when GitLab recorded one: its global id, not its !number"`
	Milestone            *Milestone `json:"milestone" jsonschema:"For a milestone event, the milestone added or removed; its title is untrusted text"`
	Weight               *int       `json:"weight" jsonschema:"For a weight event, the new weight; null otherwise, and when the weight was removed"`
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
	MessageBudget    Budget       `json:"message_budget" jsonschema:"What part of the message is shown; pass continue_offset as message_offset to read on"`
	Files            []FileDiff   `json:"files" jsonschema:"Changed files whose diff fit the budget"`
	NotShown         []FileChange `json:"files_not_shown" jsonschema:"Changed files left out by the budget or cut by GitLab"`
	FilesComplete    bool         `json:"files_complete" jsonschema:"False when the commit changes more files than were read"`
	NextFileOffset   *int         `json:"next_file_offset" jsonschema:"Pass as file_offset to see the diffs left out by the budget; null when none were"`
	DiffBudget       int          `json:"diff_budget_chars"`
	// HiddenRemoved counts the hidden characters made visible in the
	// diffs shown; the message's are in MessageBudget.
	HiddenRemoved int `json:"hidden_chars_removed"`
	// FileOffset is the changed file this result starts at.
	FileOffset int `json:"file_offset" jsonschema:"The file_offset this result starts at; 0 unless file_offset was passed"`
	// MergeRequests is null when GitLab refused the read, and when an
	// offset continues the message or the diffs.
	MergeRequests *LinkedItems `json:"merge_requests" jsonschema:"Merge requests in this project that contain the commit, the first 20. GitLab leaves out any the account cannot read. Null when they could not be read, and on a read with file_offset, diff_offset or message_offset, which does not read them again"`
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
	// DiffOffset is where the text shown starts in this file's diff.
	DiffOffset int `json:"diff_offset" jsonschema:"Where the text shown starts in this file's diff, in characters; 0 unless diff_offset was passed"`
	// Truncated marks a single diff larger than the whole budget, cut
	// at it; ContinueDiffOffset reads on.
	Truncated          bool `json:"truncated" jsonschema:"True when this one diff was over the whole budget and was cut"`
	ContinueDiffOffset *int `json:"continue_diff_offset" jsonschema:"When truncated, pass as diff_offset, with the same file_offset, to read the rest of this diff; null otherwise"`
}

// ------------------------------------------------------------ review

// MRFile is one changed file of list_mr_files.
type MRFile struct {
	OldPath   string `json:"old_path"`
	NewPath   string `json:"new_path"`
	Status    string `json:"status" jsonschema:"added, deleted, renamed or modified"`
	Additions int    `json:"additions" jsonschema:"Lines added, counted from the diff; 0 when GitLab sent no diff"`
	Deletions int    `json:"deletions" jsonschema:"Lines removed, counted from the diff; 0 when GitLab sent no diff"`
	// The markers say why a diff may be absent or not worth reading.
	TooLarge  bool  `json:"too_large" jsonschema:"GitLab did not send this file's diff: too large"`
	Collapsed bool  `json:"collapsed" jsonschema:"GitLab collapsed this file's diff"`
	Generated *bool `json:"generated_file" jsonschema:"GitLab marks the file generated; null when it did not say"`
	Binary    bool  `json:"binary" jsonschema:"The diff says binary files differ; there are no lines to read"`
}

// MRFiles is list_mr_files' result.
type MRFiles struct {
	Project ProjectRef `json:"project"`
	IID     int64      `json:"iid"`
	Files   []MRFile   `json:"files"`
	Listing Listing    `json:"listing"`
}

// Diffs is the budgeted diffs of get_mr_diff and compare_refs, as
// get_commit carries them.
type Diffs struct {
	Files          []FileDiff   `json:"files" jsonschema:"Changed files whose diff fit the budget"`
	NotShown       []FileChange `json:"files_not_shown" jsonschema:"Changed files left out by the budget or cut by GitLab"`
	FilesComplete  bool         `json:"files_complete" jsonschema:"False when there are more changed files than were read"`
	NextFileOffset *int         `json:"next_file_offset" jsonschema:"Pass as file_offset to see the diffs left out by the budget; null when none were"`
	DiffBudget     int          `json:"diff_budget_chars"`
	HiddenRemoved  int          `json:"hidden_chars_removed" jsonschema:"Zero-width and bidirectional-control characters in the diffs shown, made visible"`
}

// MRDiff is get_mr_diff's result.
type MRDiff struct {
	Project ProjectRef `json:"project"`
	IID     int64      `json:"iid"`
	Diffs
}

// MRCommits is list_mr_commits' result.
type MRCommits struct {
	Project ProjectRef  `json:"project"`
	IID     int64       `json:"iid"`
	Commits []CommitRow `json:"commits" jsonschema:"Newest first"`
	Listing Listing     `json:"listing"`
}

// DraftNote is one of the signed-in account's unpublished review
// comments.
type DraftNote struct {
	ID int64 `json:"id"`
	// DiscussionID is the thread it replies to, empty for a new one.
	DiscussionID      string        `json:"discussion_id" jsonschema:"The thread this draft replies to; empty for a new thread"`
	ResolveDiscussion bool          `json:"resolve_discussion" jsonschema:"Publishing it resolves the thread it replies to"`
	Position          *DiffPosition `json:"position" jsonschema:"Where an inline draft sits; null for a general one"`
	UntrustedBody     string        `json:"untrusted_body"`
	Budget            Budget        `json:"body_budget"`
}

// DraftNotes is list_review_comments' result.
type DraftNotes struct {
	Project ProjectRef  `json:"project"`
	IID     int64       `json:"iid"`
	Drafts  []DraftNote `json:"drafts"`
	// NotShown names the drafts the page budget left out.
	NotShown []int64 `json:"not_shown" jsonschema:"Ids of drafts left out by the character budget; the next page starts with them"`
	Budget   int     `json:"budget_chars"`
	Listing  Listing `json:"listing"`
}

// Compare is compare_refs' result.
type Compare struct {
	Project  ProjectRef `json:"project"`
	From     string     `json:"from"`
	To       string     `json:"to"`
	Straight bool       `json:"straight" jsonschema:"True: from and to compared directly; false: from their merge base, as a merge request compares"`
	WebURL   string     `json:"web_url"`
	SameRef  bool       `json:"same_ref" jsonschema:"from and to are the same commit"`
	// Timeout is GitLab giving up on the comparison: the commits and
	// diffs are then empty, not absent.
	Timeout bool        `json:"timeout" jsonschema:"GitLab gave up on the comparison; the commits and diffs it returned are not the whole answer"`
	Commits []CommitRow `json:"commits" jsonschema:"Commits in to that are not in from, from commit_offset"`
	// CommitsTotal counts every commit GitLab returned.
	CommitsTotal     int  `json:"commits_total"`
	NextCommitOffset *int `json:"next_commit_offset" jsonschema:"Pass as commit_offset to see more commits; null when none are left"`
	Diffs
}

// MRVersion is one diff version of a merge request: GitLab makes one on
// each push to the source branch, and on a rebase.
type MRVersion struct {
	ID        int64     `json:"id" jsonschema:"Pass as from_version or to_version to compare_mr_versions"`
	HeadSHA   string    `json:"head_commit_sha" jsonschema:"The source branch's head when the version was made"`
	BaseSHA   *string   `json:"base_commit_sha" jsonschema:"The merge base with the target branch; null on an empty or very old version"`
	StartSHA  *string   `json:"start_commit_sha" jsonschema:"The target branch's head when the version was made; null on an empty or very old version"`
	CreatedAt time.Time `json:"created_at"`
	State     string    `json:"state" jsonschema:"collected; empty: no commits; overflow: GitLab kept only part of the diff; without_files: GitLab deleted an old version's stored diff. GitLab's own value, and others appear"`
	// ChangesCount is GitLab's real_size.
	ChangesCount *string `json:"changes_count" jsonschema:"Files changed, as GitLab counts them: 12, or 1000+; null on an empty version"`
}

// MRVersions is list_mr_versions' result.
type MRVersions struct {
	Project  ProjectRef  `json:"project"`
	IID      int64       `json:"iid"`
	Versions []MRVersion `json:"versions" jsonschema:"Newest first"`
	Listing  Listing     `json:"listing"`
}

// MRVersionChanges is compare_mr_versions' result: the repository
// compare from the older version's head to the newer's, straight.
type MRVersionChanges struct {
	IID         int64     `json:"iid"`
	FromVersion MRVersion `json:"from_version"`
	ToVersion   MRVersion `json:"to_version"`
	// BaseMoved is a moved merge base: the diff then carries what the
	// target branch gained in between. It is null when either version
	// has no base.
	BaseMoved *bool `json:"base_moved" jsonschema:"The versions have different merge bases, as after a rebase: the diff then includes the target branch changes the rebase brought in, not only the author's. Null when either version has no merge base to compare"`
	Compare
}

// Tag is one row of list_tags.
type Tag struct {
	Name        string     `json:"name"`
	CommitID    string     `json:"commit_id"`
	CommittedAt time.Time  `json:"committed_at"`
	CreatedAt   *time.Time `json:"created_at" jsonschema:"When an annotated tag was made; null for a lightweight tag"`
	Protected   bool       `json:"protected"`
	Release     bool       `json:"release" jsonschema:"A release is attached"`
	// UntrustedMessage is an annotated tag's message on one line.
	UntrustedMessage     string `json:"untrusted_message"`
	UntrustedCommitTitle string `json:"untrusted_commit_title"`
}

// Tags is list_tags' result.
type Tags struct {
	Project ProjectRef `json:"project"`
	Tags    []Tag      `json:"tags"`
	Listing Listing    `json:"listing"`
}

// ------------------------------------------------------------ CI

// PipelineRow is one row of list_pipelines.
type PipelineRow struct {
	ID        int64     `json:"id"`
	IID       int64     `json:"iid" jsonschema:"The pipeline's number in its project; tools take id, not this"`
	Status    string    `json:"status"`
	Ref       string    `json:"ref"`
	SHA       string    `json:"sha"`
	Source    string    `json:"source" jsonschema:"What started it: push, merge_request_event, schedule, web and others"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	WebURL    string    `json:"web_url"`
}

// Pipelines is list_pipelines' result.
type Pipelines struct {
	Project   ProjectRef    `json:"project"`
	Pipelines []PipelineRow `json:"pipelines"`
	Listing   Listing       `json:"listing"`
}

// JobRow is one job.
type JobRow struct {
	ID            int64      `json:"id"`
	Name          string     `json:"name"`
	Stage         string     `json:"stage"`
	Status        string     `json:"status"`
	AllowFailure  bool       `json:"allow_failure" jsonschema:"A failure of this job does not fail the pipeline"`
	FailureReason string     `json:"failure_reason" jsonschema:"GitLab's reason for a failure, such as script_failure; empty otherwise"`
	CreatedAt     time.Time  `json:"created_at"`
	StartedAt     *time.Time `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at"`
	Duration      *float64   `json:"duration_seconds"`
	WebURL        string     `json:"web_url"`
}

// PipelineDetail is get_pipeline's result.
type PipelineDetail struct {
	Project        ProjectRef `json:"project"`
	ID             int64      `json:"id"`
	IID            int64      `json:"iid"`
	Status         string     `json:"status"`
	DetailedStatus string     `json:"detailed_status" jsonschema:"The status as GitLab shows it, such as passed with warnings"`
	Ref            string     `json:"ref"`
	Tag            bool       `json:"tag" jsonschema:"ref is a tag"`
	SHA            string     `json:"sha"`
	BeforeSHA      string     `json:"before_sha"`
	Source         string     `json:"source"`
	User           *User      `json:"user" jsonschema:"Who started it"`
	CreatedAt      time.Time  `json:"created_at"`
	StartedAt      *time.Time `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	Duration       *int64     `json:"duration_seconds"`
	QueuedDuration *int64     `json:"queued_seconds"`
	WebURL         string     `json:"web_url"`
	// UntrustedYAMLErrors is GitLab's message about a configuration it
	// could not read, which quotes the configuration.
	UntrustedYAMLErrors string `json:"untrusted_yaml_errors"`
	// FailedJobs lists the failed jobs, the latest attempt of each.
	FailedJobs         []JobRow `json:"failed_jobs"`
	FailedJobsComplete bool     `json:"failed_jobs_complete" jsonschema:"False when more jobs failed than were read; list_jobs with scope failed lists them"`
	// FailedTriggerJobs are the failed trigger jobs, which the job
	// listing leaves out.
	FailedTriggerJobs         []TriggerJobRow `json:"failed_trigger_jobs" jsonschema:"Trigger jobs that failed, each with the downstream pipeline it started, whose failure is its own"`
	FailedTriggerJobsComplete bool            `json:"failed_trigger_jobs_complete" jsonschema:"False when more trigger jobs failed than were read, or they could not be read"`
}

// TriggerJobRow is a trigger job and the pipeline it started.
type TriggerJobRow struct {
	JobRow
	Downstream *DownstreamRow `json:"downstream_pipeline" jsonschema:"The pipeline this trigger job started; null when it started none"`
}

// DownstreamRow is a pipeline a trigger job started.
type DownstreamRow struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"project_id" jsonschema:"The project it ran in, which may be another; get_pipeline with this id reads it"`
	Status    string `json:"status"`
	WebURL    string `json:"web_url"`
}

// Jobs is list_jobs' result.
type Jobs struct {
	Project    ProjectRef `json:"project"`
	PipelineID int64      `json:"pipeline_id"`
	Jobs       []JobRow   `json:"jobs"`
	Listing    Listing    `json:"listing"`
}

// JobLog is get_job_log's result: a window of the log, by bytes of the
// log as GitLab stores it.
type JobLog struct {
	Project ProjectRef `json:"project"`
	Job     JobRow     `json:"job"`
	// TotalBytes is the stored log's size.
	TotalBytes int `json:"total_bytes"`
	// ByteOffset and ByteEnd bound the window shown, in the stored log.
	ByteOffset int `json:"byte_offset" jsonschema:"Where the window starts in the stored log"`
	ByteEnd    int `json:"byte_end" jsonschema:"Where the window ends in the stored log"`
	// PrevByteOffset and NextByteOffset continue backwards and forwards.
	PrevByteOffset *int `json:"prev_byte_offset" jsonschema:"Pass as byte_offset to read the part before this window; null at the start"`
	NextByteOffset *int `json:"next_byte_offset" jsonschema:"Pass as byte_offset to read the part after this window; null at the end"`
	// Section names the section failed_only jumped to.
	Section string `json:"section" jsonschema:"With failed_only, the log section the job failed in; empty otherwise"`
	// FailureNotFound is failed_only finding no failure line.
	FailureNotFound bool `json:"failure_not_found" jsonschema:"With failed_only, true when the log has no failure line and the tail is shown instead"`
	// SecretsMasked counts the secret shapes replaced with [MASKED …].
	SecretsMasked int    `json:"secrets_masked" jsonschema:"Token and key shapes replaced with [MASKED kind] in the window shown"`
	HiddenRemoved int    `json:"hidden_chars_removed" jsonschema:"Zero-width and bidirectional-control characters made visible"`
	UntrustedLog  string `json:"untrusted_log"`
}

// LintJob is one job a linted configuration defines.
type LintJob struct {
	Name         string `json:"name"`
	Stage        string `json:"stage"`
	When         string `json:"when"`
	AllowFailure bool   `json:"allow_failure"`
}

// Lint is lint_ci's result.
type Lint struct {
	Project  ProjectRef `json:"project"`
	Ref      string     `json:"ref" jsonschema:"The ref whose configuration was linted; empty for the default branch"`
	Simulate bool       `json:"simulated" jsonschema:"True when GitLab simulated creating a pipeline"`
	// Supplied is true when the caller's content was linted rather than
	// the committed configuration.
	Supplied bool `json:"supplied" jsonschema:"True when the content passed in was linted rather than the configuration committed at ref"`
	Valid    bool `json:"valid"`
	// Errors and warnings quote the configuration, which people other
	// than the caller wrote.
	UntrustedErrors   []string  `json:"untrusted_errors"`
	UntrustedWarnings []string  `json:"untrusted_warnings"`
	Jobs              []LintJob `json:"jobs" jsonschema:"The jobs the configuration defines, with include_jobs"`
	// UntrustedMergedYAML is the configuration with every include
	// expanded, under the file budget.
	UntrustedMergedYAML string `json:"untrusted_merged_yaml"`
	MergedYAMLBudget    Budget `json:"merged_yaml_budget"`
}

// TestCounts counts the cases of a test report or of one suite.
type TestCounts struct {
	Total   int     `json:"total"`
	Success int     `json:"success"`
	Failed  int     `json:"failed"`
	Skipped int     `json:"skipped"`
	Error   int     `json:"error" jsonschema:"Cases that could not run, as the report says"`
	Seconds float64 `json:"seconds" jsonschema:"The time the cases took, as the report states it"`
}

// TestSuiteRow is one suite of a test report: one job's report, the
// reports of parallel jobs merged.
type TestSuiteRow struct {
	Name string `json:"name" jsonschema:"The job's name, from the project's CI configuration"`
	TestCounts
	// UntrustedSuiteError is GitLab's message about a report it could not
	// read, which can quote the report.
	UntrustedSuiteError string `json:"untrusted_suite_error" jsonschema:"Why GitLab could not read the suite's report, whose counts are then 0; empty otherwise"`
}

// TestFailure is one failed or errored case. Its name, class, file and
// output were written by the project's tests.
type TestFailure struct {
	Suite              string  `json:"suite"`
	Status             string  `json:"status" jsonschema:"failed, or error for a case that could not run"`
	UntrustedName      string  `json:"untrusted_name"`
	UntrustedClassname string  `json:"untrusted_classname"`
	UntrustedFile      string  `json:"untrusted_file"`
	Seconds            float64 `json:"execution_seconds"`
	// UntrustedOutput is the failure message and the case's output, cut
	// at render.TestOutputBudget.
	UntrustedOutput string `json:"untrusted_output" jsonschema:"The failure message and what the case printed, token shapes masked"`
	OutputChars     int    `json:"output_chars" jsonschema:"The output's full length in characters; past 16,000, as GitLab sent it"`
	OutputCut       bool   `json:"output_cut" jsonschema:"True when the output is longer than shown"`
}

// TestReport is get_test_report's result.
type TestReport struct {
	Project        ProjectRef `json:"project"`
	PipelineID     int64      `json:"pipeline_id"`
	PipelineStatus string     `json:"pipeline_status"`
	Partial        bool       `json:"partial" jsonschema:"True while the pipeline or a child pipeline in the project has not finished: jobs still to run add to the report. Even a finished pipeline's report may be up to two minutes old, as GitLab caches it"`
	PartialReason  string     `json:"partial_reason" jsonschema:"Why the report may be partial, such as the pipeline is running; empty otherwise"`
	// FromSummary is true when the report was too large to read.
	FromSummary bool `json:"from_summary" jsonschema:"True when the full report was larger than this server reads: the counts are GitLab's stored summary, written by a worker after each job and possibly behind or empty, and no case is listed"`
	// UntrustedTotalSuiteError is the summary's first suite error.
	UntrustedTotalSuiteError string         `json:"untrusted_total_suite_error" jsonschema:"From the summary, the first error GitLab stored for a suite it could not read; empty otherwise"`
	Total                    TestCounts     `json:"total"`
	Suites                   []TestSuiteRow `json:"suites" jsonschema:"Suites with a failure or an error first, at most 100 and a quarter of the budget"`
	SuitesTotal              int            `json:"suites_total"`
	// Failures are the failed and errored cases from Offset, as many as
	// fit the budget.
	Failures      []TestFailure `json:"failures"`
	FailuresTotal int           `json:"failures_total" jsonschema:"Failed and errored cases in the report"`
	Offset        int           `json:"offset" jsonschema:"The index of the first case shown among the failed and errored cases"`
	NextOffset    *int          `json:"next_offset" jsonschema:"Pass as offset to read the next cases; null when the last is shown. Each call reads the report again, so while the pipeline runs the cases can shift"`
	BudgetChars   int           `json:"budget_chars" jsonschema:"The most characters of suite names and errors, case names and output this result shows"`
	SecretsMasked int           `json:"secrets_masked" jsonschema:"Token and key shapes replaced with [MASKED kind] in the cases shown"`
	HiddenRemoved int           `json:"hidden_chars_removed" jsonschema:"Zero-width and bidirectional-control characters removed or made visible"`
}

// ------------------------------------------------------------ planning

// Label is one row of list_labels.
type Label struct {
	ID          int64  `json:"id"`
	Name        string `json:"name" jsonschema:"The exact name to pass as a label, scoped labels such as priority::high included"`
	Color       string `json:"color"`
	ProjectOnly bool   `json:"project_label" jsonschema:"Defined in the project rather than inherited from a group"`
	Priority    *int   `json:"priority"`
	// The counts are null unless with_counts was asked for.
	OpenIssues           *int   `json:"open_issues"`
	ClosedIssues         *int   `json:"closed_issues"`
	OpenMergeRequests    *int   `json:"open_merge_requests"`
	UntrustedDescription string `json:"untrusted_description"`
	// Version is the witness update_label and delete_label take: GitLab
	// keeps no version of a label (§4.6).
	Version string `json:"version" jsonschema:"A hash of the label as read: update_label and delete_label take it, and refuse [stale] if the label changed since"`
}

// Labels is list_labels' result.
type Labels struct {
	Project ProjectRef `json:"project"`
	Labels  []Label    `json:"labels"`
	Listing Listing    `json:"listing"`
}

// MilestoneRow is one row of list_milestones.
type MilestoneRow struct {
	ID             int64     `json:"id"`
	IID            int64     `json:"iid"`
	State          string    `json:"state"`
	DueDate        *string   `json:"due_date" jsonschema:"A date, not an instant: 2026-10-01"`
	StartDate      *string   `json:"start_date" jsonschema:"A date, not an instant: 2026-10-01"`
	Expired        bool      `json:"expired"`
	UpdatedAt      time.Time `json:"updated_at"`
	WebURL         string    `json:"web_url"`
	UntrustedTitle string    `json:"untrusted_title" jsonschema:"The title, which is what issues and merge requests are filtered by"`
}

// Milestones is list_milestones' result.
type Milestones struct {
	// Project or Group names where they were listed, the other null.
	Project    *ProjectRef    `json:"project"`
	Group      *string        `json:"group"`
	Milestones []MilestoneRow `json:"milestones"`
	Listing    Listing        `json:"listing"`
}

// Boards is list_boards' result.
type Boards struct {
	Project ProjectRef `json:"project"`
	Boards  []Board    `json:"boards"`
	// BudgetChars bounds a page with max: a page ends early when the
	// next board's lists would pass it.
	BudgetChars int `json:"budget_chars" jsonschema:"About how many characters of boards and lists one page holds; a page ends early rather than pass it"`
	// BoardsNotRead is set when the project has more boards than one
	// call reads, so those past them are in no page.
	BoardsNotRead bool    `json:"boards_not_read" jsonschema:"True when the project has more than 1000 boards: a call reads the first 1000 by id, and the rest are in no page, so the last page has no next_page_token and is not complete"`
	Listing       Listing `json:"listing"`
}

// Board is one issue board. GitLab never returns its Open and Closed
// lists; OpenList and ClosedList say whether the board shows them.
type Board struct {
	ID            int64  `json:"id"`
	UntrustedName string `json:"untrusted_name"`
	OpenList      bool   `json:"open_list" jsonschema:"Whether the board shows its Open list, first: open issues in none of its label, assignee, milestone, iteration or open-status lists"`
	ClosedList    bool   `json:"closed_list" jsonschema:"Whether the board shows its Closed list, last: every closed issue"`
	// Scope is null on a board without one, and where GitLab does not
	// return scopes, a paid feature.
	Scope *BoardScope `json:"scope" jsonschema:"The filter the board applies to all its lists; null when it has none"`
	Lists []BoardList `json:"lists" jsonschema:"The lists between Open and Closed in board order, left to right"`
	// ListsNotShown counts the lists past the first 100.
	ListsNotShown int `json:"lists_not_shown" jsonschema:"Lists past the first 100, not shown"`
}

// BoardScope is a board's own filter.
type BoardScope struct {
	Milestone *BoardTimebox `json:"milestone" jsonschema:"The milestone the board is limited to; null when it has none or a filter"`
	// MilestoneFilter is GitLab's filter in place of a milestone.
	MilestoneFilter *string  `json:"milestone_filter" jsonschema:"GitLab's milestone filter the board is limited to, spelled as search_issues' milestone takes it: None, Any, #upcoming or #started"`
	Assignee        *string  `json:"assignee" jsonschema:"A username"`
	Labels          []string `json:"labels" jsonschema:"Label names"`
	Weight          *int     `json:"weight" jsonschema:"Only issues of this weight; null when the board does not filter by weight"`
	NoWeight        bool     `json:"no_weight" jsonschema:"Only issues without a weight"`
}

// BoardList is one list of a board.
type BoardList struct {
	ID       int64   `json:"id"`
	Position *int    `json:"position"`
	Kind     string  `json:"kind" jsonschema:"label, assignee, milestone, iteration or unknown. GitLab names no kind, so it is read from the field present; a status list is unknown"`
	Label    *string `json:"label" jsonschema:"For a label list, the label's exact name"`
	Assignee *string `json:"assignee" jsonschema:"For an assignee list, the username"`
	// Milestone and Iteration name the list's timebox.
	Milestone *BoardTimebox `json:"milestone"`
	Iteration *BoardTimebox `json:"iteration"`
	// SearchIssues is null when search_issues cannot return the list.
	SearchIssues *ListSearch `json:"search_issues" jsonschema:"The search_issues arguments, with the project, that return the list's issues; null when search_issues cannot"`
	SearchNote   string      `json:"search_note" jsonschema:"Why search_issues cannot return the list, or what its arguments leave out; empty otherwise"`
}

// BoardTimebox is the milestone or iteration a list names.
type BoardTimebox struct {
	ID             int64  `json:"id"`
	UntrustedTitle string `json:"untrusted_title"`
}

// ListSearch is the search_issues arguments that return a list's issues.
type ListSearch struct {
	State              string   `json:"state"`
	Labels             []string `json:"labels"`
	Assignee           *string  `json:"assignee"`
	UntrustedMilestone *string  `json:"untrusted_milestone" jsonschema:"Pass as milestone: the exact title GitLab holds, not cleaned for display"`
}

// Member is one row of list_members.
type Member struct {
	ID          int64   `json:"id"`
	Username    string  `json:"username"`
	Name        string  `json:"name"`
	State       string  `json:"state"`
	AccessLevel int     `json:"access_level" jsonschema:"GitLab's number: 5 minimal access, 10 guest, 15 planner, 20 reporter, 30 developer, 40 maintainer, 50 owner"`
	Role        string  `json:"role" jsonschema:"The access level's name"`
	ExpiresAt   *string `json:"expires_at" jsonschema:"A date, not an instant; null when the access does not expire"`
}

// Members is list_members' result.
type Members struct {
	Project ProjectRef `json:"project"`
	Members []Member   `json:"members"`
	Listing Listing    `json:"listing"`
}

// UserRow is one row of find_users.
type UserRow struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	State    string `json:"state"`
	WebURL   string `json:"web_url"`
}

// Users is find_users' result.
type Users struct {
	Users   []UserRow `json:"users"`
	Listing Listing   `json:"listing"`
}

// Todo is one row of list_todos.
type Todo struct {
	ID         int64       `json:"id"`
	Project    *ProjectRef `json:"project" jsonschema:"Null for an item outside a project"`
	Action     string      `json:"action" jsonschema:"Why it is on the list: assigned, mentioned, review_requested, build_failed and others"`
	TargetType string      `json:"target_type" jsonschema:"Issue, MergeRequest and others"`
	TargetIID  *int64      `json:"target_iid" jsonschema:"The issue's or merge request's iid, when the item points at one"`
	State      string      `json:"state"`
	Author     string      `json:"author" jsonschema:"The username of whoever caused it"`
	CreatedAt  time.Time   `json:"created_at"`
	TargetURL  string      `json:"target_url"`
	// UntrustedTitle and UntrustedBody are one line each.
	UntrustedTitle string `json:"untrusted_title"`
	UntrustedBody  string `json:"untrusted_body"`
}

// Todos is list_todos' result.
type Todos struct {
	Todos   []Todo  `json:"todos"`
	Listing Listing `json:"listing"`
}

// SearchRow is one search result. Kind says which fields it fills.
type SearchRow struct {
	Kind      string     `json:"kind" jsonschema:"issue, merge_request, project, milestone, user, blob, commit, note or wiki_blob"`
	ProjectID *int64     `json:"project_id"`
	ID        *int64     `json:"id" jsonschema:"A project, milestone, user or note id"`
	IID       *int64     `json:"iid" jsonschema:"An issue's or merge request's iid, or the iid of the item a note is on"`
	SHA       string     `json:"sha" jsonschema:"A commit"`
	Ref       string     `json:"ref" jsonschema:"The ref a blob was found at"`
	Path      string     `json:"path" jsonschema:"A blob's or wiki page's path, or a project's full path"`
	StartLine *int       `json:"start_line" jsonschema:"The first line of a blob excerpt"`
	State     string     `json:"state"`
	Author    string     `json:"author" jsonschema:"A username, or a commit author's name"`
	UpdatedAt *time.Time `json:"updated_at"`
	WebURL    string     `json:"web_url"`
	// UntrustedTitle is a title, a name or a commit title, on one line.
	UntrustedTitle string `json:"untrusted_title"`
	// UntrustedExcerpt is a blob's matching lines or a note's body, cut
	// at the per-row budget.
	UntrustedExcerpt string `json:"untrusted_excerpt"`
	ExcerptCut       bool   `json:"excerpt_cut" jsonschema:"The excerpt was longer than the per-row budget and was cut"`
}

// Search is search's result.
type Search struct {
	Scope   string      `json:"scope"`
	Where   string      `json:"where" jsonschema:"instance, group or project"`
	Rows    []SearchRow `json:"rows"`
	Listing Listing     `json:"listing"`
}

// ---------------------------------------------------------------- writes

// Write is what every write result carries besides its own fields: where
// it went, whether it was only a preview, and what the quick-action guard
// did to the text sent (§4.2, §4.7, §4.11). Each result has its own
// Outcome beside it, because `scripts/gates outcomes` holds every writing
// function to set one.
type Write struct {
	// DryRun is true when nothing was sent: the result says what would
	// have been.
	DryRun bool `json:"dry_run" jsonschema:"True when nothing was written; the result describes what would have been"`
	// WouldSend is the request a dry run stopped before, nil otherwise.
	WouldSend *Preview `json:"would_send" jsonschema:"The request a dry run did not send; null when something was written"`
	// Target is where the write went, or would have.
	Target WriteTarget `json:"target"`
	// EscapedCommands are the lines the guard escaped because
	// escape_commands was true.
	EscapedCommands []EscapedLine `json:"escaped_commands" jsonschema:"Lines GitLab would have run as quick actions, sent with a leading backslash so they show as text"`
	Notes           []string      `json:"notes"`
}

// WriteTarget names the project a write went to and who can see it: a
// write to a public project is visible to everyone (§4.7).
type WriteTarget struct {
	Project    ProjectRef `json:"project"`
	Visibility string     `json:"visibility" jsonschema:"public, internal or private: who can see what was written"`
}

// Preview is the request a dry run stopped before.
type Preview struct {
	Method    string   `json:"method"`
	Operation string   `json:"operation" jsonschema:"What the request does, such as create an issue"`
	Fields    []string `json:"fields" jsonschema:"The request fields that would be sent; only these would change"`
}

// EscapedLine is one line the quick-action guard escaped.
type EscapedLine struct {
	Input   string `json:"input" jsonschema:"The input the line was in"`
	Line    int    `json:"line" jsonschema:"The 1-based line number in that input"`
	Command string `json:"command" jsonschema:"The command name, without the slash"`
}

// Removed counts what replacing a text wholesale took out (§4.6).
type Removed struct {
	Chars int `json:"chars"`
	Lines int `json:"lines"`
}

// IssueWrite is create_issue's and update_issue's result.
type IssueWrite struct {
	Outcome string `json:"outcome" jsonschema:"created, updated, unchanged or dry_run"`
	Write
	IID          int64      `json:"iid"`
	WebURL       string     `json:"web_url"`
	State        string     `json:"state"`
	LabelsBefore []string   `json:"labels_before" jsonschema:"The labels before an update; empty for a create"`
	Labels       []string   `json:"labels" jsonschema:"The labels GitLab reported after the write"`
	Assignees    []string   `json:"assignees" jsonschema:"Usernames"`
	Milestone    *Milestone `json:"milestone"`
	DueDate      string     `json:"due_date"`
	Confidential bool       `json:"confidential"`
	UpdatedAt    *time.Time `json:"updated_at" jsonschema:"Pass as updated_at to the next update_issue"`
	// Changed names the fields that differ from before, as read back.
	Changed []string `json:"changed" jsonschema:"The fields whose value differs after the write, as GitLab reported it"`
	// DescriptionRemoved is set when a description was replaced.
	DescriptionRemoved *Removed `json:"description_removed" jsonschema:"What replacing the description took out; null when it was not replaced"`
}

// MergeRequestWrite is create_merge_request's and update_merge_request's
// result.
type MergeRequestWrite struct {
	Outcome string `json:"outcome" jsonschema:"created, updated, unchanged or dry_run"`
	Write
	IID                int64      `json:"iid"`
	WebURL             string     `json:"web_url"`
	State              string     `json:"state"`
	Draft              bool       `json:"draft"`
	SourceBranch       string     `json:"source_branch"`
	TargetBranch       string     `json:"target_branch"`
	LabelsBefore       []string   `json:"labels_before" jsonschema:"The labels before an update; empty for a create"`
	Labels             []string   `json:"labels"`
	Assignees          []string   `json:"assignees" jsonschema:"Usernames"`
	Reviewers          []string   `json:"reviewers" jsonschema:"Usernames"`
	Milestone          *Milestone `json:"milestone"`
	RemoveSourceBranch bool       `json:"remove_source_branch"`
	Squash             bool       `json:"squash"`
	UpdatedAt          *time.Time `json:"updated_at" jsonschema:"Pass as updated_at to the next update_merge_request. Null after a create: GitLab moves a new merge request's updated_at within seconds, so read it again first"`
	Changed            []string   `json:"changed" jsonschema:"The fields whose value differs after the write, as GitLab reported it"`
	DescriptionRemoved *Removed   `json:"description_removed" jsonschema:"What replacing the description took out; null when it was not replaced"`
}

// CommentWrite is add_comment's and add_review_comment's result.
type CommentWrite struct {
	Outcome string `json:"outcome" jsonschema:"created or dry_run"`
	Write
	// Kind is comment, thread, reply or draft.
	Kind         string        `json:"kind" jsonschema:"comment (a standalone comment), thread (a new resolvable thread), reply, or draft (an unpublished review comment)"`
	NoteID       int64         `json:"note_id" jsonschema:"The comment's id; for a draft, the draft's id"`
	DiscussionID string        `json:"discussion_id" jsonschema:"The thread it is in, for a reply or a new thread; empty for a standalone comment, whose thread list_discussions names, and for a draft that starts one"`
	Position     *DiffPosition `json:"position" jsonschema:"Where on the diff it landed, as GitLab stored it; null for a comment not on a line"`
	LineRange    *LineSpan     `json:"line_range" jsonschema:"The lines a multi-line comment covers; null for one line"`
	LineCode     string        `json:"line_code" jsonschema:"For a draft on a line, GitLab's code for that line, which its web view places the draft by; empty otherwise"`
	UpdatedAt    *time.Time    `json:"updated_at" jsonschema:"Pass as updated_at to update_comment; null for a dry run and a draft"`
}

// CommentUpdate is update_comment's result.
type CommentUpdate struct {
	Outcome string `json:"outcome" jsonschema:"updated, unchanged or dry_run"`
	Write
	Type      string     `json:"type" jsonschema:"issue or merge_request"`
	IID       int64      `json:"iid"`
	NoteID    int64      `json:"note_id"`
	UpdatedAt *time.Time `json:"updated_at" jsonschema:"Pass as updated_at to the next update_comment; null for a dry run"`
	// BodyRemoved is set when the body was replaced.
	BodyRemoved *Removed `json:"body_removed" jsonschema:"What replacing the text took out; null when it was not replaced"`
}

// LineSpan is the first and last line of a multi-line diff comment on
// one side.
type LineSpan struct {
	Side  string `json:"side" jsonschema:"new or old"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// TimeWrite is track_time's result.
type TimeWrite struct {
	Outcome string `json:"outcome" jsonschema:"updated, unchanged or dry_run"`
	Write
	Type   string    `json:"type" jsonschema:"issue or merge_request"`
	IID    int64     `json:"iid"`
	WebURL string    `json:"web_url"`
	Before TimeStats `json:"before" jsonschema:"The time stats read before the write"`
	// After is nil on a dry run.
	After *TimeStats `json:"after" jsonschema:"The time stats read back after the write; null on a dry run"`
	// Sent lists the requests sent, in order.
	Sent      []string   `json:"sent" jsonschema:"The time tracking requests sent, in order, such as add_spent_time 30m; empty when none was needed"`
	Changed   []string   `json:"changed" jsonschema:"time_estimate and total_time_spent when they differ after the write, as read back"`
	UpdatedAt *time.Time `json:"updated_at" jsonschema:"As read after the write, or before it when nothing was sent; pass it as updated_at to the next track_time or update"`
	// TotalTimeSpent is the spent-time witness for the next call.
	TotalTimeSpent int64 `json:"total_time_spent" jsonschema:"The seconds spent now, as GitLab reported them; pass it as total_time_spent to the next track_time that adds or resets spent time"`
}

// DiscussionWrite is resolve_discussion's result.
type DiscussionWrite struct {
	Outcome string `json:"outcome" jsonschema:"resolved, reopened, unchanged or dry_run"`
	Write
	DiscussionID string `json:"discussion_id"`
	Resolved     bool   `json:"resolved" jsonschema:"The thread's state after the call, as GitLab reported it"`
}

// DraftDelete is delete_review_comment's result.
type DraftDelete struct {
	Outcome string `json:"outcome" jsonschema:"deleted or dry_run"`
	Write
	DraftID   int64 `json:"draft_id"`
	Remaining int   `json:"remaining" jsonschema:"Your drafts still on the merge request, read after the delete"`
}

// ReviewSubmit is submit_review's result.
type ReviewSubmit struct {
	Outcome string `json:"outcome" jsonschema:"published or dry_run"`
	Write
	Published     int    `json:"published" jsonschema:"Drafts that were published"`
	Remaining     int    `json:"remaining" jsonschema:"Your drafts still unpublished, read after the call"`
	Summary       bool   `json:"summary" jsonschema:"True when a summary comment was sent with the review"`
	ReviewerState string `json:"reviewer_state" jsonschema:"The reviewer state sent, empty when none"`
}

// BranchWrite is create_branch's result.
type BranchWrite struct {
	Outcome string `json:"outcome" jsonschema:"created or dry_run"`
	Write
	Branch    string `json:"branch"`
	CommitSHA string `json:"commit_sha" jsonschema:"The commit the branch points at"`
	Protected bool   `json:"protected"`
	WebURL    string `json:"web_url"`
}

// CommitWrite is create_commit's result.
type CommitWrite struct {
	Outcome string `json:"outcome" jsonschema:"created or dry_run"`
	Write
	SHA        string   `json:"sha"`
	ShortID    string   `json:"short_id"`
	Branch     string   `json:"branch"`
	BranchHead string   `json:"branch_head" jsonschema:"The branch's head read after the commit"`
	ParentIDs  []string `json:"parent_ids"`
	Additions  int      `json:"additions"`
	Deletions  int      `json:"deletions"`
	Files      []string `json:"files" jsonschema:"The paths the commit touched"`
	WebURL     string   `json:"web_url"`
}

// TodosDone is mark_todos_done's result.
type TodosDone struct {
	Outcome string `json:"outcome" jsonschema:"done, partly_done, none_done or dry_run"`
	Write
	Items []TodoDone `json:"items"`
}

// TodoDone is one to-do item mark_todos_done was given.
type TodoDone struct {
	ID      int64  `json:"id"`
	Outcome string `json:"outcome" jsonschema:"done, not_found, blocked (outside GITLAB_MCP_WRITE_NAMESPACES), failed, or would_mark in a dry run"`
	Error   string `json:"error" jsonschema:"Why it was not marked; empty when it was"`
}

// SubscriptionWrite is subscribe's result.
type SubscriptionWrite struct {
	Outcome string `json:"outcome" jsonschema:"subscribed, unsubscribed, unchanged (it already was so) or dry_run"`
	Write
	Type       string `json:"type" jsonschema:"issue or merge_request"`
	IID        int64  `json:"iid"`
	WebURL     string `json:"web_url"`
	Subscribed bool   `json:"subscribed" jsonschema:"Whether you are subscribed after the call, as GitLab answered; in a dry run, as read now"`
}

// ReactionWrite is react's result.
type ReactionWrite struct {
	Outcome string `json:"outcome" jsonschema:"added, removed, unchanged (it already was so) or dry_run"`
	Write
	Type    string `json:"type" jsonschema:"issue or merge_request"`
	IID     int64  `json:"iid"`
	NoteID  int64  `json:"note_id" jsonschema:"The comment reacted on; 0 for the issue or merge request itself"`
	Emoji   string `json:"emoji" jsonschema:"The emoji's name as GitLab stores it when a read or its answer showed it, which may differ from the one given: +1 is thumbsup"`
	Reacted *bool  `json:"reacted" jsonschema:"Whether your reaction with this emoji is there after the call; in a dry run, as read now. Null when unknown: past 1,000 reactions, a read cannot find yours"`
}

// TodoWrite is add_todo's result.
type TodoWrite struct {
	Outcome string `json:"outcome" jsonschema:"created, unchanged (a pending to-do you added is already there) or dry_run"`
	Write
	Type   string `json:"type" jsonschema:"issue or merge_request"`
	IID    int64  `json:"iid"`
	TodoID int64  `json:"todo_id" jsonschema:"The to-do item's id, which mark_todos_done takes; when unchanged, the one already there; 0 in a dry run that would add one, or when it could not be found"`
}

// ------------------------------------------------------------------ ship

// MergeWrite is merge_merge_request's result.
type MergeWrite struct {
	Outcome string `json:"outcome" jsonschema:"merged, auto_merge_set (it merges when its pipeline succeeds), unchanged (already merged) or dry_run"`
	Write
	IID                 int64      `json:"iid"`
	WebURL              string     `json:"web_url"`
	State               string     `json:"state" jsonschema:"The merge request's state after the call, as GitLab reported it"`
	SourceBranch        string     `json:"source_branch"`
	TargetBranch        string     `json:"target_branch"`
	SHA                 string     `json:"sha" jsonschema:"The source branch's head that was merged, or would be"`
	MergeCommitSHA      string     `json:"merge_commit_sha" jsonschema:"The commit the merge made on the target branch; empty until merged, or when GitLab fast-forwarded"`
	SquashCommitSHA     string     `json:"squash_commit_sha"`
	MergedAt            *time.Time `json:"merged_at"`
	MergedBy            string     `json:"merged_by" jsonschema:"Username"`
	DetailedMergeStatus string     `json:"detailed_merge_status" jsonschema:"GitLab's reason a merge request can or cannot merge now, such as mergeable, ci_must_pass or not_approved"`
}

// ApprovalWrite is approve_merge_request's and unapprove_merge_request's
// result.
type ApprovalWrite struct {
	Outcome string `json:"outcome" jsonschema:"approved, unapproved, unchanged or dry_run"`
	Write
	IID           int64    `json:"iid"`
	SHA           string   `json:"sha" jsonschema:"The head the approval was given at; empty for unapprove"`
	YouApproved   bool     `json:"you_approved" jsonschema:"Whether your approval stands after the call"`
	Approved      bool     `json:"approved" jsonschema:"Whether the merge request's approval rules are met"`
	ApprovedBy    []string `json:"approved_by" jsonschema:"Usernames"`
	ApprovalsLeft *int     `json:"approvals_left" jsonschema:"Approvals still required; null where the tier has no approval rules"`
}

// PipelineWrite is run_pipeline's, retry_pipeline's and cancel_pipeline's
// result.
type PipelineWrite struct {
	Outcome string `json:"outcome" jsonschema:"created, retried, canceled, unchanged or dry_run"`
	Write
	PipelineID   int64    `json:"pipeline_id"`
	IID          int64    `json:"iid"`
	StatusBefore string   `json:"status_before" jsonschema:"The status read before the call; empty for a new pipeline"`
	Status       string   `json:"status" jsonschema:"The status GitLab reported after the call; after a cancel it can still read running while GitLab updates it"`
	Ref          string   `json:"ref"`
	SHA          string   `json:"sha"`
	Source       string   `json:"source"`
	WebURL       string   `json:"web_url"`
	Variables    []string `json:"variables" jsonschema:"The keys of the variables sent; their values are never shown"`
	Inputs       []string `json:"inputs" jsonschema:"The names of the inputs sent; their values are never shown"`
}

// MergeRequestPipelineWrite is run_merge_request_pipeline's result.
type MergeRequestPipelineWrite struct {
	Outcome string `json:"outcome" jsonschema:"created or dry_run"`
	Write
	IID          int64  `json:"iid" jsonschema:"The merge request's number"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	PipelineID   int64  `json:"pipeline_id"`
	PipelineIID  int64  `json:"pipeline_iid" jsonschema:"The pipeline's number in its project, shown as #12"`
	Kind         string `json:"kind" jsonschema:"merged_results (the source merged into the target, on refs/merge-requests/N/merge) or detached (the source branch's head, on refs/merge-requests/N/head); empty for a dry run"`
	Status       string `json:"status"`
	Ref          string `json:"ref"`
	SHA          string `json:"sha" jsonschema:"The commit the pipeline runs: for merged results, a merge commit GitLab made, not the source branch's head"`
	Source       string `json:"source"`
	WebURL       string `json:"web_url"`
}

// JobWrite is retry_job's and play_job's result.
type JobWrite struct {
	Outcome string `json:"outcome" jsonschema:"retried, played or dry_run"`
	Write
	JobID      int64    `json:"job_id" jsonschema:"The job that runs: for a retry, the new job GitLab made; for play, the job itself"`
	FromJobID  int64    `json:"from_job_id" jsonschema:"The job the call was given"`
	Name       string   `json:"name"`
	Stage      string   `json:"stage"`
	Status     string   `json:"status"`
	PipelineID int64    `json:"pipeline_id"`
	WebURL     string   `json:"web_url"`
	Variables  []string `json:"variables" jsonschema:"The keys of the variables sent; their values are never shown"`
	Inputs     []string `json:"inputs" jsonschema:"The names of the job inputs sent"`
}

// ----------------------------------------------------------- destructive

// BranchDelete is delete_branch's result.
type BranchDelete struct {
	Outcome string `json:"outcome" jsonschema:"deleted or dry_run"`
	Write
	Branch string `json:"branch"`
	SHA    string `json:"sha" jsonschema:"The head the branch had when it was deleted"`
	Merged bool   `json:"merged" jsonschema:"Whether GitLab counted it merged into the default branch"`
}

// CommentDelete is delete_comment's result.
type CommentDelete struct {
	Outcome string `json:"outcome" jsonschema:"deleted or dry_run"`
	Write
	Type   string `json:"type" jsonschema:"issue or merge_request"`
	IID    int64  `json:"iid"`
	NoteID int64  `json:"note_id"`
}

// ------------------------------------------------------------------ wiki

// WikiPageRow is one page of a wiki listing.
type WikiPageRow struct {
	Slug   string `json:"slug" jsonschema:"Pass as slug to get_wiki_page"`
	Title  string `json:"untrusted_title"`
	Format string `json:"format"`
}

// WikiPages is list_wiki_pages' result.
type WikiPages struct {
	Project ProjectRef    `json:"project"`
	Pages   []WikiPageRow `json:"pages"`
	Listing Listing       `json:"listing"`
}

// WikiPage is get_wiki_page's result.
type WikiPage struct {
	Project          ProjectRef `json:"project"`
	Slug             string     `json:"slug"`
	UntrustedTitle   string     `json:"untrusted_title"`
	Format           string     `json:"format" jsonschema:"markdown, rdoc, asciidoc or org"`
	ContentSHA256    string     `json:"content_sha256" jsonschema:"A hash of the page's content as GitLab stores it. Pass as content_sha256 to save_wiki_page or delete_wiki_page: GitLab keeps no version a write could check, so the server compares this"`
	UntrustedContent string     `json:"untrusted_content"`
	Budget           Budget     `json:"content_budget"`
}

// WikiWrite is save_wiki_page's result.
type WikiWrite struct {
	Outcome string `json:"outcome" jsonschema:"created, updated, unchanged or dry_run"`
	Write
	Slug           string   `json:"slug" jsonschema:"The page's slug after the write, which a new title changes"`
	Title          string   `json:"title"`
	Format         string   `json:"format"`
	ContentSHA256  string   `json:"content_sha256" jsonschema:"Pass as content_sha256 to the next save_wiki_page"`
	Changed        []string `json:"changed" jsonschema:"The fields whose value differs after the write, as GitLab reported it"`
	ContentRemoved *Removed `json:"content_removed" jsonschema:"What replacing the content took out; null when it was not replaced"`
}

// WikiDelete is delete_wiki_page's result.
type WikiDelete struct {
	Outcome string `json:"outcome" jsonschema:"deleted or dry_run"`
	Write
	Slug string `json:"slug"`
}

// -------------------------------------------------------------- snippets

// SnippetRow is one snippet of a listing.
type SnippetRow struct {
	ID             int64     `json:"id"`
	UntrustedTitle string    `json:"untrusted_title"`
	Visibility     string    `json:"visibility"`
	Author         string    `json:"author" jsonschema:"Username"`
	ProjectID      *int64    `json:"project_id" jsonschema:"The project it is in; null for a personal snippet"`
	Files          []string  `json:"files"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	WebURL         string    `json:"web_url"`
}

// Snippets is list_snippets' result.
type Snippets struct {
	Project  *ProjectRef  `json:"project" jsonschema:"The project listed; null for your personal snippets"`
	Snippets []SnippetRow `json:"snippets"`
	Listing  Listing      `json:"listing"`
}

// Snippet is get_snippet's result.
type Snippet struct {
	Project              *ProjectRef `json:"project" jsonschema:"The project it is in; null for a personal snippet"`
	ID                   int64       `json:"id"`
	UntrustedTitle       string      `json:"untrusted_title"`
	UntrustedDescription string      `json:"untrusted_description"`
	Visibility           string      `json:"visibility"`
	Author               string      `json:"author" jsonschema:"Username"`
	Files                []string    `json:"files" jsonschema:"Every file of the snippet; pass one as file to read it"`
	File                 string      `json:"file" jsonschema:"The file shown"`
	Binary               bool        `json:"binary" jsonschema:"True when the file is binary and its content is not shown"`
	UntrustedContent     string      `json:"untrusted_content"`
	Budget               Budget      `json:"content_budget"`
	CreatedAt            time.Time   `json:"created_at"`
	UpdatedAt            time.Time   `json:"updated_at"`
	WebURL               string      `json:"web_url"`
}

// SnippetWrite is create_snippet's result.
type SnippetWrite struct {
	Outcome string `json:"outcome" jsonschema:"created or dry_run"`
	Write
	ID         int64    `json:"id"`
	Visibility string   `json:"visibility" jsonschema:"Always private"`
	Files      []string `json:"files"`
	WebURL     string   `json:"web_url"`
}

// SnippetUpdate is update_snippet's result.
type SnippetUpdate struct {
	Outcome string `json:"outcome" jsonschema:"updated, unchanged or dry_run"`
	Write
	ID         int64      `json:"id"`
	Visibility string     `json:"visibility"`
	Files      []string   `json:"files" jsonschema:"The snippet's files after the write"`
	Changed    []string   `json:"changed" jsonschema:"Of title, description, files and content, those the write changed: title and description as GitLab reported them; files and content when GitLab accepted a change to them, since its answer shows no content"`
	UpdatedAt  *time.Time `json:"updated_at" jsonschema:"Pass as updated_at to the next update_snippet or delete_snippet; null for a dry run, and null after a change to content or files, which GitLab follows shortly with another move of updated_at: read the snippet with get_snippet for the witness then"`
	// DescriptionRemoved is set when the description was replaced.
	DescriptionRemoved *Removed `json:"description_removed" jsonschema:"What replacing the description took out; null when it was not replaced"`
	WebURL             string   `json:"web_url"`
}

// SnippetDelete is delete_snippet's result.
type SnippetDelete struct {
	Outcome string `json:"outcome" jsonschema:"deleted or dry_run"`
	Write
	ID int64 `json:"id"`
}

// -------------------------------------------------------------- releases

// ReleaseRow is one release of a listing.
type ReleaseRow struct {
	TagName       string     `json:"tag_name"`
	UntrustedName string     `json:"untrusted_name"`
	Author        string     `json:"author" jsonschema:"Username"`
	CommitSHA     string     `json:"commit_sha"`
	CreatedAt     time.Time  `json:"created_at"`
	ReleasedAt    *time.Time `json:"released_at"`
	Upcoming      bool       `json:"upcoming" jsonschema:"True when released_at is in the future"`
}

// Releases is list_releases' result.
type Releases struct {
	Project  ProjectRef   `json:"project"`
	Releases []ReleaseRow `json:"releases"`
	Listing  Listing      `json:"listing"`
}

// Release is get_release's result.
type Release struct {
	Project              ProjectRef `json:"project"`
	TagName              string     `json:"tag_name"`
	UntrustedName        string     `json:"untrusted_name"`
	UntrustedDescription string     `json:"untrusted_description"`
	Budget               Budget     `json:"description_budget"`
	Author               string     `json:"author" jsonschema:"Username"`
	CommitSHA            string     `json:"commit_sha"`
	CreatedAt            time.Time  `json:"created_at"`
	ReleasedAt           *time.Time `json:"released_at"`
	Upcoming             bool       `json:"upcoming"`
	Milestones           []string   `json:"milestones" jsonschema:"Milestone titles"`
	Assets               int        `json:"assets" jsonschema:"How many assets it has: links and source archives"`
}

// ReleaseWrite is create_release's result.
type ReleaseWrite struct {
	Outcome string `json:"outcome" jsonschema:"created or dry_run"`
	Write
	TagName    string     `json:"tag_name"`
	TagCreated bool       `json:"tag_created" jsonschema:"True when the tag did not exist and GitLab created it at ref, which starts the project's tag pipelines"`
	CommitSHA  string     `json:"commit_sha"`
	ReleasedAt *time.Time `json:"released_at"`
	Milestones []string   `json:"milestones"`
	Links      []LinkRow  `json:"links" jsonschema:"The asset links the release was created with"`
}

// LinkRow is an asset link of a release.
type LinkRow struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	LinkType string `json:"link_type"`
}

// ----------------------------------------------------------- deployments

// DeploymentRef is the deployment an environment names last.
type DeploymentRef struct {
	ID        int64     `json:"id"`
	IID       int64     `json:"iid"`
	Status    string    `json:"status"`
	Ref       string    `json:"ref"`
	SHA       string    `json:"sha"`
	CreatedAt time.Time `json:"created_at"`
}

// EnvironmentRow is one environment.
type EnvironmentRow struct {
	ID             int64          `json:"id"`
	Name           string         `json:"name"`
	Slug           string         `json:"slug"`
	State          string         `json:"state" jsonschema:"available, stopping or stopped"`
	Tier           string         `json:"tier"`
	ExternalURL    string         `json:"external_url" jsonschema:"Where the environment is served, as the project configured it; never fetched"`
	UpdatedAt      time.Time      `json:"updated_at"`
	AutoStopAt     *time.Time     `json:"auto_stop_at"`
	LastDeployment *DeploymentRef `json:"last_deployment"`
}

// Environments is list_environments' result.
type Environments struct {
	Project      ProjectRef       `json:"project"`
	Environments []EnvironmentRow `json:"environments"`
	Listing      Listing          `json:"listing"`
}

// DeploymentRow is one deployment.
type DeploymentRow struct {
	ID          int64      `json:"id"`
	IID         int64      `json:"iid"`
	Status      string     `json:"status"`
	Environment string     `json:"environment"`
	Ref         string     `json:"ref"`
	SHA         string     `json:"sha"`
	User        string     `json:"user" jsonschema:"Username of who deployed"`
	JobID       int64      `json:"job_id" jsonschema:"The job that deployed; 0 when none"`
	JobName     string     `json:"job_name"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   *time.Time `json:"updated_at"`
}

// Deployments is list_deployments' result.
type Deployments struct {
	Project     ProjectRef      `json:"project"`
	Deployments []DeploymentRow `json:"deployments"`
	Listing     Listing         `json:"listing"`
}

// -------------------------------------------------------------- activity

// EventRow is one event.
type EventRow struct {
	ID                   int64     `json:"id"`
	Action               string    `json:"action" jsonschema:"GitLab's action name, such as opened, commented on or pushed to"`
	TargetType           string    `json:"target_type" jsonschema:"Issue, MergeRequest, Note, DiffNote, Milestone, WikiPage::Meta and the like; empty for a push"`
	TargetID             *int64    `json:"target_id"`
	TargetIID            *int64    `json:"target_iid"`
	UntrustedTargetTitle string    `json:"untrusted_target_title"`
	Author               string    `json:"author" jsonschema:"Username"`
	ProjectID            *int64    `json:"project_id"`
	CreatedAt            time.Time `json:"created_at"`
	PushRef              string    `json:"push_ref" jsonschema:"For a push: the branch or tag"`
	PushCommits          int       `json:"push_commits" jsonschema:"For a push: how many commits"`
	UntrustedCommitTitle string    `json:"untrusted_commit_title" jsonschema:"For a push: the newest commit's title"`
}

// Events is list_events' result.
type Events struct {
	Project *ProjectRef `json:"project" jsonschema:"The project listed; null for your own activity"`
	Events  []EventRow  `json:"events"`
	Listing Listing     `json:"listing"`
}

// ------------------------------------------------------------ phase 6

// IssueMove is move_issue's result.
type IssueMove struct {
	Outcome string `json:"outcome" jsonschema:"moved or dry_run"`
	Write
	FromIID int64 `json:"from_iid" jsonschema:"The issue in the project it was moved from, which GitLab closes"`
	// To is the project it went to; IID and WebURL are the new issue's.
	To     ProjectRef `json:"to_project"`
	IID    int64      `json:"iid" jsonschema:"The new issue's number in the project it went to; 0 for a dry run"`
	WebURL string     `json:"web_url"`
	// ToVisibility is who can see the new issue.
	ToVisibility string `json:"to_visibility" jsonschema:"public, internal or private: who can see the issue where it went"`
}

// IssueLinkWrite is link_issues' and unlink_issues' result.
type IssueLinkWrite struct {
	Outcome string `json:"outcome" jsonschema:"linked, unlinked, unchanged or dry_run"`
	Write
	IID           int64      `json:"iid"`
	TargetProject ProjectRef `json:"target_project"`
	TargetIID     int64      `json:"target_iid"`
	LinkID        int64      `json:"link_id" jsonschema:"The link's id; 0 when there is none"`
	LinkType      string     `json:"link_type" jsonschema:"relates_to, blocks or is_blocked_by, from the first issue's side"`
}

// LabelWrite is create_label's, update_label's and delete_label's result.
type LabelWrite struct {
	Outcome string `json:"outcome" jsonschema:"created, updated, deleted, unchanged or dry_run"`
	Write
	Label   Label    `json:"label" jsonschema:"The label as GitLab reported it after the write, or before a delete"`
	Changed []string `json:"changed" jsonschema:"The fields whose value differs after an update"`
}

// MilestoneWrite is create_milestone's, update_milestone's and
// delete_milestone's result.
type MilestoneWrite struct {
	Outcome string `json:"outcome" jsonschema:"created, updated, deleted, unchanged or dry_run"`
	Write
	Milestone            MilestoneRow `json:"milestone" jsonschema:"The milestone as GitLab reported it after the write, or before a delete; updated_at is the witness the next write takes"`
	UntrustedDescription string       `json:"untrusted_description"`
	Changed              []string     `json:"changed" jsonschema:"The fields whose value differs after an update"`
}

// RebaseWrite is rebase_merge_request's result.
type RebaseWrite struct {
	Outcome string `json:"outcome" jsonschema:"started or dry_run"`
	Write
	IID              int64  `json:"iid"`
	SHA              string `json:"sha" jsonschema:"The head the rebase started from"`
	RebaseInProgress bool   `json:"rebase_in_progress" jsonschema:"True while GitLab rebases in the background; get_merge_request shows the new head once it is done"`
	// MergeError is GitLab's reason, when it reported one.
	UntrustedMergeError string `json:"untrusted_merge_error" jsonschema:"Why a rebase failed, as GitLab reported it; empty otherwise"`
	SkipCI              bool   `json:"skip_ci"`
}

// PickWrite is cherry_pick_commit's and revert_commit's result.
type PickWrite struct {
	Outcome string `json:"outcome" jsonschema:"created or dry_run"`
	Write
	Action  string `json:"action" jsonschema:"cherry_pick or revert"`
	FromSHA string `json:"from_sha" jsonschema:"The commit picked or reverted"`
	Branch  string `json:"branch"`
	// Applies is GitLab's own dry run: whether the change applies.
	Applies bool   `json:"applies" jsonschema:"For a dry run, whether GitLab found the change applies to the branch cleanly"`
	SHA     string `json:"sha" jsonschema:"The new commit; empty for a dry run"`
	ShortID string `json:"short_id"`
	WebURL  string `json:"web_url"`
	// UntrustedTitle is the new commit's title.
	UntrustedTitle string `json:"untrusted_title"`
}

// BlameRange is a run of lines one commit last changed.
type BlameRange struct {
	StartLine        int       `json:"start_line"`
	EndLine          int       `json:"end_line"`
	CommitSHA        string    `json:"commit_sha"`
	Author           string    `json:"author_name"`
	AuthoredAt       time.Time `json:"authored_at"`
	UntrustedSummary string    `json:"untrusted_summary" jsonschema:"The commit's title"`
	UntrustedLines   string    `json:"untrusted_lines"`
}

// Blame is get_blame's result.
type Blame struct {
	Project ProjectRef   `json:"project"`
	Path    string       `json:"path"`
	Ref     string       `json:"ref"`
	Ranges  []BlameRange `json:"ranges"`
	// NextLine continues the blame after the budget.
	NextLine      *int `json:"next_line" jsonschema:"Pass as start_line to read on; null at the end of what was asked"`
	HiddenRemoved int  `json:"hidden_chars_removed"`
}

// ArtifactEntry is one row of list_job_artifacts.
type ArtifactEntry struct {
	Path string `json:"path"`
	Type string `json:"type" jsonschema:"file or directory"`
	Size *int64 `json:"size_bytes"`
}

// Artifacts is list_job_artifacts' result.
type Artifacts struct {
	Project ProjectRef      `json:"project"`
	JobID   int64           `json:"job_id"`
	Path    string          `json:"path" jsonschema:"The directory listed; empty for the top"`
	Entries []ArtifactEntry `json:"entries"`
	Listing Listing         `json:"listing"`
}

// Artifact is get_job_artifact's result.
type Artifact struct {
	Project ProjectRef `json:"project"`
	JobID   int64      `json:"job_id"`
	Path    string     `json:"path"`
	Size    int        `json:"size_bytes"`
	Binary  bool       `json:"binary" jsonschema:"True when the file is not text; only its size is returned"`
	// SecretsMasked counts the secret shapes replaced, as in a job log.
	SecretsMasked    int    `json:"secrets_masked" jsonschema:"Token and key shapes replaced with [MASKED kind]"`
	UntrustedContent string `json:"untrusted_content"`
	Budget           Budget `json:"content_budget"`
}

// TagWrite is create_tag's and delete_tag's result.
type TagWrite struct {
	Outcome string `json:"outcome" jsonschema:"created, deleted or dry_run"`
	Write
	Tag       string `json:"tag"`
	CommitSHA string `json:"commit_sha" jsonschema:"The commit the tag points at"`
	Annotated bool   `json:"annotated"`
	Protected bool   `json:"protected"`
}
