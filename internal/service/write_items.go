package service

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/config"
	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/internal/model"
)

// Creating and updating issues and merge requests (§7.2, §4.6). Bodies
// arrive already through the quick-action guard.

// dateOnly is GitLab's due date: a day, not an instant.
var dateOnly = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func checkDate(name, v string) error {
	if v != "" && !dateOnly.MatchString(v) {
		return gapi.Errf(gapi.ClassInvalid, "%s is a day written YYYY-MM-DD", name)
	}
	return nil
}

// IssueCreate is create_issue's request.
type IssueCreate struct {
	Project      string
	Title        string
	Description  string
	Labels       []string
	Assignees    []string
	Milestone    string
	DueDate      string
	Confidential bool
}

// CreateIssue creates an issue.
func (s *Service) CreateIssue(ctx context.Context, in IssueCreate) (model.IssueWrite, error) {
	if strings.TrimSpace(in.Title) == "" {
		return model.IssueWrite{}, gapi.Errf(gapi.ClassInvalid, "title is empty")
	}
	if err := checkDate("due_date", in.DueDate); err != nil {
		return model.IssueWrite{}, err
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.IssueWrite{}, err
	}
	body := gapi.IssueCreate{Title: in.Title, Description: in.Description, Labels: in.Labels, DueDate: in.DueDate, Confidential: in.Confidential}
	if err := parallel(
		func() error { return s.checkLabels(ctx, t.p, in.Labels) },
		func() (err error) { body.AssigneeIDs, err = s.userIDs(ctx, in.Assignees); return err },
		func() (err error) { body.MilestoneID, err = s.milestoneID(ctx, t.p, in.Milestone); return err },
	); err != nil {
		return model.IssueWrite{}, err
	}
	if gapi.IsDryRun(ctx) {
		return model.IssueWrite{Outcome: "dry_run", Write: model.Write{DryRun: true, Target: t.ref,
			WouldSend: preview("POST", "create an issue", fieldsOf(body))},
			Labels: nonNil(in.Labels), LabelsBefore: []string{}, Assignees: nonNil(in.Assignees), Changed: []string{}}, nil
	}
	start := time.Now()
	iss, err := s.client.CreateIssue(ctx, t.p, body)
	if err != nil {
		return model.IssueWrite{}, settle(err, "issue", func() (string, error) {
			return findItem(ctx, s, t.p, start, func(q gapi.ItemQuery) (string, error) {
				rows, _, err := s.client.SearchIssues(ctx, q, gapi.ListOptions{PerPage: gapi.MaxPerPage})
				for _, r := range rows {
					if r.Title == in.Title {
						return fmt.Sprintf("issue #%d, %s", r.IID, r.WebURL), nil
					}
				}
				return "", err
			})
		})
	}
	return issueWrite("created", t, iss), nil
}

// findItem settles an ambiguous create of an issue or a merge request:
// search lists what this account made in the project since the call
// started, and names the one with the same title.
func findItem(ctx context.Context, s *Service, p gapi.Project, start time.Time, search func(gapi.ItemQuery) (string, error)) (string, error) {
	me, err := s.me(ctx)
	if err != nil {
		return "", err
	}
	return search(gapi.ItemQuery{Project: p, State: "all", AuthorUsername: me.Username, CreatedAfter: start.Add(-settleSkew)})
}

func issueWrite(outcome string, t target, iss *gitlab.Issue) model.IssueWrite {
	updated := iss.UpdatedAt
	return model.IssueWrite{Outcome: outcome, Write: model.Write{Target: t.ref}, IID: iss.IID, WebURL: iss.WebURL, State: iss.State,
		LabelsBefore: []string{}, Labels: nonNil(iss.Labels), Assignees: usernames(iss.Assignees), Milestone: milestone(iss.Milestone), DueDate: iss.DueDate,
		Confidential: iss.Confidential, UpdatedAt: &updated, Changed: []string{}}
}

// IssueUpdate is update_issue's request. A nil pointer or empty list is
// a field left as it is.
type IssueUpdate struct {
	Project         string
	IID             int64
	UpdatedAt       string
	Title           *string
	Description     *string
	AddLabels       []string
	RemoveLabels    []string
	AddAssignees    []string
	RemoveAssignees []string
	Milestone       *string
	ClearMilestone  bool
	State           string // close or reopen
	DueDate         *string
	ClearDueDate    bool
	Confidential    *bool
}

func (in IssueUpdate) empty() bool {
	return in.Title == nil && in.Description == nil && len(in.AddLabels)+len(in.RemoveLabels)+len(in.AddAssignees)+len(in.RemoveAssignees) == 0 &&
		in.Milestone == nil && !in.ClearMilestone && in.State == "" && in.DueDate == nil && !in.ClearDueDate && in.Confidential == nil
}

// UpdateIssue changes the fields given, after checking the witness.
func (s *Service) UpdateIssue(ctx context.Context, in IssueUpdate) (model.IssueWrite, error) {
	if in.empty() {
		return model.IssueWrite{}, gapi.Errf(gapi.ClassInvalid, "nothing to change: pass at least one field to set")
	}
	witness, err := parseWitness(in.UpdatedAt)
	if err != nil {
		return model.IssueWrite{}, err
	}
	if in.DueDate, err = clearing("due_date", in.DueDate, in.ClearDueDate); err != nil {
		return model.IssueWrite{}, err
	}
	if in.DueDate != nil {
		if err := checkDate("due_date", *in.DueDate); err != nil {
			return model.IssueWrite{}, err
		}
	}
	if in.Title != nil && strings.TrimSpace(*in.Title) == "" {
		return model.IssueWrite{}, gapi.Errf(gapi.ClassInvalid, "title is empty")
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.IssueWrite{}, err
	}
	body := gapi.IssueUpdate{Title: in.Title, Description: in.Description, AddLabels: in.AddLabels, RemoveLabels: in.RemoveLabels,
		DueDate: in.DueDate, Confidential: in.Confidential}
	var before *gitlab.Issue
	var addIDs []int64
	if err := parallel(
		func() (err error) { before, err = s.client.GetIssue(ctx, t.p, in.IID); return err },
		func() error { return s.checkLabels(ctx, t.p, in.AddLabels) },
		func() (err error) { addIDs, err = s.userIDs(ctx, in.AddAssignees); return err },
		func() (err error) {
			body.MilestoneID, err = s.milestoneChange(ctx, t.p, in.Milestone, in.ClearMilestone)
			return err
		},
	); err != nil {
		return model.IssueWrite{}, err
	}
	if err := checkWitness(witness, before.UpdatedAt, "issue"); err != nil {
		return model.IssueWrite{}, err
	}
	// Making a confidential issue public shows it to everyone who can see
	// the project's issues, as a move to a more visible project would,
	// and an injected "it was confidential by mistake" asks for exactly
	// that. It is Ship's to allow, as an approving review is.
	if in.Confidential != nil && !*in.Confidential && before.Confidential && !s.cfg.EnableShip {
		return model.IssueWrite{}, gapi.Errf(gapi.ClassBlocked, "making a confidential issue public shows it to everyone "+
			"who can see the project's issues, which this server does only when %s=true; nothing was sent", config.EnvEnableShip)
	}
	body.AssigneeIDs = assigneeSet(before.Assignees, addIDs, in.AddAssignees, in.RemoveAssignees)
	var notes []string
	body.StateEvent, notes = stateEvent(in.State, before.State)

	if gapi.IsDryRun(ctx) {
		out := issueWrite("dry_run", t, before)
		out.DryRun, out.Notes = true, notes
		out.WouldSend = preview("PUT", "update an issue", fieldsOf(body))
		out.LabelsBefore = nonNil(before.Labels)
		return out, nil
	}
	after := before
	if len(fieldsOf(body)) > 0 {
		if after, err = s.client.UpdateIssue(ctx, t.p, in.IID, body); err != nil {
			return model.IssueWrite{}, err
		}
	}
	out := issueWrite("unchanged", t, after)
	out.Notes = notes
	out.LabelsBefore = nonNil(before.Labels)
	out.Changed = names(field{"title", before.Title != after.Title}, field{"description", before.Description != after.Description},
		field{"state", before.State != after.State}, field{"labels", !sameSet(before.Labels, after.Labels)},
		field{"assignees", !sameSet(usernames(before.Assignees), usernames(after.Assignees))},
		field{"milestone", milestoneTitle(before.Milestone) != milestoneTitle(after.Milestone)},
		field{"due_date", before.DueDate != after.DueDate}, field{"confidential", before.Confidential != after.Confidential})
	if len(out.Changed) > 0 {
		out.Outcome = "updated"
	}
	if in.Description != nil && before.Description != after.Description {
		out.DescriptionRemoved = removedFrom(before.Description, after.Description)
	}
	return out, nil
}

// assigneeSet is the assignee or reviewer ids after adding and removing,
// nil when neither was asked. A removal is matched against the fresh
// read, so it costs no lookup; one not in it changes nothing.
func assigneeSet(current []gitlab.UserBasic, addIDs []int64, add, remove []string) *[]int64 {
	if len(add) == 0 && len(remove) == 0 {
		return nil
	}
	set := withSet(current, addIDs, remove)
	return &set
}

// milestoneChange is the milestone id to send: nil to leave it, 0 to
// clear it.
func (s *Service) milestoneChange(ctx context.Context, p gapi.Project, title *string, clear bool) (*int64, error) {
	if err := notBoth("milestone", title != nil, clear); err != nil {
		return nil, err
	}
	switch {
	case clear:
		return new(int64), nil
	case title == nil:
		return nil, nil
	}
	id, err := s.milestoneID(ctx, p, *title)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// stateEvent is the state_event to send for close or reopen, none when
// the item is already there, with a note saying so.
func stateEvent(want, current string) (string, []string) {
	switch {
	case want == "close" && current == "closed":
		return "", []string{"It was already closed, so no state change was sent."}
	case want == "reopen" && current == "opened":
		return "", []string{"It was already open, so no state change was sent."}
	case want == "reopen" && current == "merged":
		return "", []string{"A merged merge request cannot be reopened, so no state change was sent."}
	}
	return want, nil
}

// ------------------------------------------------------ merge requests

// draftPrefix is GitLab's draft title prefix, "[Draft]", "(Draft)" or
// "Draft:" in any case, possibly repeated (Gitlab::Regex
// merge_request_draft and MergeRequest::DRAFT_REGEX at 829b21d2).
var draftPrefix = regexp.MustCompile(`(?i)^(\[draft\]|\(draft\)|draft:)+\s*`)

// maxDraftStrips bounds stripping, as GitLab's draftless_title does.
const maxDraftStrips = 20

// withDraft sets a title's draft state the way GitLab's draft_title and
// draftless_title do: a draft title is kept as it is, another gets
// "Draft: "; making it ready strips every leading prefix.
func withDraft(title string, draft bool) string {
	if draft {
		if draftPrefix.MatchString(title) {
			return title
		}
		return "Draft: " + title
	}
	for range maxDraftStrips {
		next := draftPrefix.ReplaceAllString(title, "")
		if next == title {
			break
		}
		title = next
	}
	return title
}

// MergeRequestCreate is create_merge_request's request.
type MergeRequestCreate struct {
	Project            string
	SourceBranch       string
	TargetBranch       string
	Title              string
	Description        string
	Labels             []string
	Assignees          []string
	Reviewers          []string
	Milestone          string
	Draft              bool
	RemoveSourceBranch bool
	Squash             bool
}

// CreateMergeRequest opens a merge request.
func (s *Service) CreateMergeRequest(ctx context.Context, in MergeRequestCreate) (model.MergeRequestWrite, error) {
	if strings.TrimSpace(in.Title) == "" {
		return model.MergeRequestWrite{}, gapi.Errf(gapi.ClassInvalid, "title is empty")
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.MergeRequestWrite{}, err
	}
	targetBranch := in.TargetBranch
	if targetBranch == "" {
		targetBranch = t.project.DefaultBranch
	}
	title := in.Title
	if in.Draft {
		title = withDraft(title, true)
	}
	body := gapi.MergeRequestCreate{SourceBranch: in.SourceBranch, TargetBranch: targetBranch, Title: title, Description: in.Description,
		Labels: in.Labels, RemoveSourceBranch: in.RemoveSourceBranch, Squash: in.Squash}
	if err := parallel(
		func() error { return s.checkLabels(ctx, t.p, in.Labels) },
		func() (err error) { body.AssigneeIDs, err = s.userIDs(ctx, in.Assignees); return err },
		func() (err error) { body.ReviewerIDs, err = s.userIDs(ctx, in.Reviewers); return err },
		func() (err error) { body.MilestoneID, err = s.milestoneID(ctx, t.p, in.Milestone); return err },
	); err != nil {
		return model.MergeRequestWrite{}, err
	}
	if gapi.IsDryRun(ctx) {
		return model.MergeRequestWrite{Outcome: "dry_run", Write: model.Write{DryRun: true, Target: t.ref,
			WouldSend: preview("POST", "open a merge request", fieldsOf(body))},
			Draft: in.Draft, SourceBranch: in.SourceBranch, TargetBranch: targetBranch, LabelsBefore: []string{}, Labels: nonNil(in.Labels),
			Assignees: nonNil(in.Assignees), Reviewers: nonNil(in.Reviewers), Changed: []string{}}, nil
	}
	start := time.Now()
	mr, err := s.client.CreateMergeRequest(ctx, t.p, body)
	if err != nil {
		return model.MergeRequestWrite{}, settle(err, "merge request", func() (string, error) {
			return findItem(ctx, s, t.p, start, func(q gapi.ItemQuery) (string, error) {
				rows, _, err := s.client.SearchMergeRequests(ctx, q, gapi.ListOptions{PerPage: gapi.MaxPerPage})
				for _, r := range rows {
					if r.Title == title {
						return fmt.Sprintf("merge request !%d, %s", r.IID, r.WebURL), nil
					}
				}
				return "", err
			})
		})
	}
	out := mergeRequestWrite("created", t, mr)
	// A create's updated_at is no witness: seen live: gitlab.com moves a new merge request's updated_at about a
	// second after answering, as it computes the diff (§18).
	out.UpdatedAt = nil
	out.Notes = append(out.Notes, "GitLab keeps working on a new merge request for a few seconds and moves its updated_at, "+
		"so read it with get_merge_request before changing it with update_merge_request.")
	return out, nil
}

func mergeRequestWrite(outcome string, t target, mr *gitlab.MergeRequest) model.MergeRequestWrite {
	updated := mr.UpdatedAt
	return model.MergeRequestWrite{Outcome: outcome, Write: model.Write{Target: t.ref}, IID: mr.IID, WebURL: mr.WebURL, State: mr.State,
		Draft: mr.Draft, SourceBranch: mr.SourceBranch, TargetBranch: mr.TargetBranch, LabelsBefore: []string{}, Labels: nonNil(mr.Labels),
		Assignees: usernames(mr.Assignees), Reviewers: usernames(mr.Reviewers), Milestone: milestone(mr.Milestone),
		RemoveSourceBranch: mr.ForceRemoveSourceBranch, Squash: mr.Squash, UpdatedAt: &updated, Changed: []string{}}
}

// MergeRequestUpdate is update_merge_request's request.
type MergeRequestUpdate struct {
	Project            string
	IID                int64
	UpdatedAt          string
	Title              *string
	Description        *string
	AddLabels          []string
	RemoveLabels       []string
	AddAssignees       []string
	RemoveAssignees    []string
	AddReviewers       []string
	RemoveReviewers    []string
	Milestone          *string
	ClearMilestone     bool
	State              string
	TargetBranch       *string
	Draft              *bool
	RemoveSourceBranch *bool
	Squash             *bool
}

func (in MergeRequestUpdate) empty() bool {
	return in.Title == nil && in.Description == nil &&
		len(in.AddLabels)+len(in.RemoveLabels)+len(in.AddAssignees)+len(in.RemoveAssignees)+len(in.AddReviewers)+len(in.RemoveReviewers) == 0 &&
		in.Milestone == nil && !in.ClearMilestone && in.State == "" && in.TargetBranch == nil && in.Draft == nil &&
		in.RemoveSourceBranch == nil && in.Squash == nil
}

// UpdateMergeRequest changes the fields given, after checking the
// witness.
func (s *Service) UpdateMergeRequest(ctx context.Context, in MergeRequestUpdate) (model.MergeRequestWrite, error) {
	if in.empty() {
		return model.MergeRequestWrite{}, gapi.Errf(gapi.ClassInvalid, "nothing to change: pass at least one field to set")
	}
	witness, err := parseWitness(in.UpdatedAt)
	if err != nil {
		return model.MergeRequestWrite{}, err
	}
	if in.Title != nil && strings.TrimSpace(*in.Title) == "" {
		return model.MergeRequestWrite{}, gapi.Errf(gapi.ClassInvalid, "title is empty")
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.MergeRequestWrite{}, err
	}
	body := gapi.MergeRequestUpdate{Title: in.Title, Description: in.Description, AddLabels: in.AddLabels, RemoveLabels: in.RemoveLabels,
		TargetBranch: in.TargetBranch, RemoveSourceBranch: in.RemoveSourceBranch, Squash: in.Squash}
	var before *gitlab.MergeRequest
	var assigneeIDs, reviewerIDs []int64
	if err := parallel(
		func() (err error) { before, err = s.client.GetMergeRequest(ctx, t.p, in.IID); return err },
		func() error { return s.checkLabels(ctx, t.p, in.AddLabels) },
		func() (err error) { assigneeIDs, err = s.userIDs(ctx, in.AddAssignees); return err },
		func() (err error) { reviewerIDs, err = s.userIDs(ctx, in.AddReviewers); return err },
		func() (err error) {
			body.MilestoneID, err = s.milestoneChange(ctx, t.p, in.Milestone, in.ClearMilestone)
			return err
		},
	); err != nil {
		return model.MergeRequestWrite{}, err
	}
	if err := checkWitness(witness, before.UpdatedAt, "merge request"); err != nil {
		return model.MergeRequestWrite{}, err
	}
	// The draft state is the title's prefix; GitLab takes no field. A new
	// title keeps the current state unless draft says otherwise, since an
	// omitted field is unchanged (§4.6).
	switch {
	case in.Draft != nil:
		title := before.Title
		if in.Title != nil {
			title = *in.Title
		}
		if next := withDraft(title, *in.Draft); next != before.Title || in.Title != nil {
			body.Title = &next
		}
	case in.Title != nil:
		next := withDraft(*in.Title, before.Draft)
		body.Title = &next
	}
	body.AssigneeIDs = assigneeSet(before.Assignees, assigneeIDs, in.AddAssignees, in.RemoveAssignees)
	body.ReviewerIDs = assigneeSet(before.Reviewers, reviewerIDs, in.AddReviewers, in.RemoveReviewers)
	var notes []string
	body.StateEvent, notes = stateEvent(in.State, before.State)

	if gapi.IsDryRun(ctx) {
		out := mergeRequestWrite("dry_run", t, before)
		out.DryRun, out.Notes = true, notes
		out.WouldSend = preview("PUT", "update a merge request", fieldsOf(body))
		out.LabelsBefore = nonNil(before.Labels)
		return out, nil
	}
	after := before
	if len(fieldsOf(body)) > 0 {
		if after, err = s.client.UpdateMergeRequest(ctx, t.p, in.IID, body); err != nil {
			return model.MergeRequestWrite{}, err
		}
	}
	out := mergeRequestWrite("unchanged", t, after)
	out.Notes = notes
	out.LabelsBefore = nonNil(before.Labels)
	out.Changed = names(field{"title", before.Title != after.Title}, field{"description", before.Description != after.Description},
		field{"state", before.State != after.State}, field{"draft", before.Draft != after.Draft},
		field{"labels", !sameSet(before.Labels, after.Labels)},
		field{"assignees", !sameSet(usernames(before.Assignees), usernames(after.Assignees))},
		field{"reviewers", !sameSet(usernames(before.Reviewers), usernames(after.Reviewers))},
		field{"milestone", milestoneTitle(before.Milestone) != milestoneTitle(after.Milestone)},
		field{"target_branch", before.TargetBranch != after.TargetBranch},
		field{"remove_source_branch", before.ForceRemoveSourceBranch != after.ForceRemoveSourceBranch},
		field{"squash", before.Squash != after.Squash})
	if len(out.Changed) > 0 {
		out.Outcome = "updated"
	}
	if in.Description != nil && before.Description != after.Description {
		out.DescriptionRemoved = removedFrom(before.Description, after.Description)
	}
	return out, nil
}
