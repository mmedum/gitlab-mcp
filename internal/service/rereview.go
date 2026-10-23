package service

import (
	"context"
	"strings"

	"github.com/mmedum/gitlab-mcp/v3/internal/gapi"
	"github.com/mmedum/gitlab-mcp/v3/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/v3/internal/model"
)

// Re-requesting a review (§7.3, §8a). The REST API has no route for it,
// so the reviewers are read over REST and the one GraphQL mutation goes
// out after. The caller's reviewer_state is the witness. A reviewer who
// approved is refused: GitLab would remove that approval, a sign-off
// this server never takes away from another person. A lost answer is
// settled by the reviewer's state after: back to unreviewed is the
// re-request's. It is never sent twice, since each one notifies.

// maxReviewerPages bounds the read of a merge request's reviewers:
// GitLab allows 200 (Issuable::MAX_NUMBER_OF_ASSIGNEES_OR_REVIEWERS).
const maxReviewerPages = 2

// Rereview is rerequest_review's request.
type Rereview struct {
	Project       string
	IID           int64
	Username      string
	ReviewerState string
}

// RerequestReview asks a reviewer of a merge request for a new review.
func (s *Service) RerequestReview(ctx context.Context, in Rereview) (model.RereviewWrite, error) {
	username := strings.TrimPrefix(strings.TrimSpace(in.Username), "@")
	if username == "" {
		return model.RereviewWrite{}, gapi.Errf(gapi.ClassInvalid, "username is empty: pass the reviewer's username as "+
			"get_merge_request shows it")
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.RereviewWrite{}, err
	}
	before, err := s.reviewer(ctx, t.p, in.IID, username)
	if err != nil {
		return model.RereviewWrite{}, err
	}
	if before.State != in.ReviewerState {
		return model.RereviewWrite{}, gapi.Errf(gapi.ClassStale, "%s's review state on !%d is %s now, not %s; nothing was sent. "+
			"Read the merge request again before deciding", username, in.IID, before.State, in.ReviewerState)
	}
	if before.State == "approved" {
		return model.RereviewWrite{}, gapi.Errf(gapi.ClassBlocked, "%s approved !%d, and GitLab removes that approval when "+
			"it asks for a new review; this server never takes away another person's approval, so nothing was sent", username, in.IID)
	}
	if gapi.IsDryRun(ctx) {
		out := rereviewWrite("dry_run", t, in.IID, before)
		out.DryRun = true
		out.WouldSend = preview("POST", "re-request a review with GraphQL", []string{"projectPath", "iid", "userId"})
		return out, nil
	}
	err = s.client.RereviewMergeRequest(ctx, t.project.PathWithNamespace, in.IID, before.User.ID)
	if gapi.IsClass(err, gapi.ClassAmbiguousOutcome) {
		return s.settleRereview(ctx, t, in.IID, before, err)
	}
	if err != nil {
		return model.RereviewWrite{}, err
	}
	out := rereviewWrite("rerequested", t, in.IID, before)
	if after, err := s.reviewer(ctx, t.p, in.IID, username); err == nil {
		out.StateAfter = &after.State
	} else {
		out.Notes = []string{"GitLab confirmed the re-request; reading the reviewer back failed, so state_after is unknown."}
	}
	return out, nil
}

// settleRereview settles a re-request whose answer was lost by the
// reviewer's state after it. From any state but unreviewed, unreviewed
// after is the re-request's; from unreviewed, no read can tell.
func (s *Service) settleRereview(ctx context.Context, t target, iid int64, before gitlab.MergeRequestReviewer,
	sendErr error) (model.RereviewWrite, error) {
	username := before.User.Username
	if before.State == "unreviewed" {
		return model.RereviewWrite{}, gapi.Wrap(gapi.ClassAmbiguousOutcome, sendErr, "GitLab did not confirm the re-request, "+
			"and %s was already unreviewed, so a read cannot tell whether it was made; do not send it again unless the person "+
			"asks", username)
	}
	after, err := s.reviewer(ctx, t.p, iid, username)
	switch {
	case err != nil:
		return model.RereviewWrite{}, gapi.Wrap(gapi.ClassAmbiguousOutcome, sendErr, "GitLab did not confirm the re-request, "+
			"and reading the reviewer to settle it failed; read the merge request before deciding to send it again")
	case after.State == "unreviewed":
		out := rereviewWrite("rerequested", t, iid, before)
		out.StateAfter = &after.State
		out.Notes = []string{"GitLab did not confirm the re-request, but a read shows the reviewer back at unreviewed, so it was made."}
		return out, nil
	}
	return model.RereviewWrite{}, gapi.Wrap(gapi.ClassAmbiguousOutcome, sendErr, "GitLab did not confirm the re-request, and "+
		"a read shows %s still at %s, so it was not made; calling again is safe", username, after.State)
}

// rereviewWrite is the result for the reviewer as read before the call.
func rereviewWrite(outcome string, t target, iid int64, before gitlab.MergeRequestReviewer) model.RereviewWrite {
	return model.RereviewWrite{Outcome: outcome, Write: model.Write{Target: t.ref}, IID: iid, Reviewer: user(before.User),
		StateBefore: before.State}
}

// reviewer finds one reviewer of a merge request by username.
func (s *Service) reviewer(ctx context.Context, p gapi.Project, iid int64, username string) (gitlab.MergeRequestReviewer, error) {
	opts := gapi.ListOptions{PerPage: gapi.MaxPerPage}
	for range maxReviewerPages {
		rows, page, err := s.client.ListMergeRequestReviewers(ctx, p, iid, opts)
		if err != nil {
			return gitlab.MergeRequestReviewer{}, err
		}
		for _, r := range rows {
			if strings.EqualFold(r.User.Username, username) {
				return r, nil
			}
		}
		if page.NextToken == "" {
			break
		}
		opts.PageToken = page.NextToken
	}
	return gitlab.MergeRequestReviewer{}, gapi.Errf(gapi.ClassInvalid, "%s is not a reviewer of !%d; get_merge_request lists "+
		"its reviewers", username, iid)
}
