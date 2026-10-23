package gapi

import (
	"context"
	"strconv"
	"strings"

	"github.com/mmedum/gitlab-mcp/v3/internal/gitlab"
)

// GraphQL is used only where the REST API has no route for an operation
// (§8a). Each operation is a literal written here, sent through Do like
// any REST call, so the token, the rate model, the refused redirects, a
// write never repeated and a dry run that sends nothing all hold.

// rereviewMutation is mergeRequestReviewerRereview, asking for its own
// errors only.
const rereviewMutation = `mutation($projectPath: ID!, $iid: String!, $userId: UserID!) {
  mergeRequestReviewerRereview(input: {projectPath: $projectPath, iid: $iid, userId: $userId}) {
    errors
  }
}`

// noAccess is how GitLab words a mutation refused on a resource that is
// missing or that the token may not change: it does not say which.
const noAccess = "does not exist or you don't have permission"

// RereviewMergeRequest asks a reviewer of a merge request for a new
// review. GitLab sets their review state back to unreviewed, removes
// their approval if they gave one, notifies them and adds a to-do and a
// system note (MergeRequests::RequestReviewService). The REST API has no
// route for it.
func (c *Client) RereviewMergeRequest(ctx context.Context, projectPath string, iid, userID int64) error {
	var out struct {
		Data struct {
			Rereview *gitlab.ReviewerRereview `json:"mergeRequestReviewerRereview"`
		} `json:"data"`
		Errors []gitlab.GraphQLError `json:"errors"`
	}
	vars := map[string]any{"projectPath": projectPath, "iid": strconv.FormatInt(iid, 10),
		"userId": "gid://gitlab/User/" + strconv.FormatInt(userID, 10)}
	if err := c.Do(ctx, Call{Method: "POST", Root: RootWeb, Path: "api/graphql",
		Body: map[string]any{"query": rereviewMutation, "variables": vars}, Name: "rerequest_review"}, &out); err != nil {
		return err
	}
	for _, e := range out.Errors {
		if strings.Contains(e.Message, noAccess) {
			return Errf(ClassNotFound, "GitLab found no merge request !%d there that this account may change: it does not say "+
				"whether the merge request is missing or the token may not change it; nothing was changed", iid)
		}
	}
	if len(out.Errors) > 0 || out.Data.Rereview == nil {
		return Errf(ClassUnexpected, "GitLab refused re-requesting the review with an error this server does not know; "+
			"nothing was changed")
	}
	for _, e := range out.Data.Rereview.Errors {
		switch e {
		case "Reviewer not found":
			return Errf(ClassInvalid, "that user is not a reviewer of !%d; nothing was changed", iid)
		case "Invalid permissions":
			return Errf(ClassForbidden, "this account may not change !%d; nothing was changed", iid)
		}
	}
	if len(out.Data.Rereview.Errors) > 0 {
		return Errf(ClassUnexpected, "GitLab refused re-requesting the review: %s", strings.Join(out.Data.Rereview.Errors, "; "))
	}
	return nil
}
