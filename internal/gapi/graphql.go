package gapi

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/mmedum/gitlab-mcp/v3/internal/gitlab"
)

// GraphQL is used only where the REST API has no route for an operation
// (§4.10). Each operation is a literal written here and sent by mutate,
// which `scripts/gates api-coverage` reads and holds against the list in
// §8a. mutate goes through Do like any REST call, so the token, the rate
// model, the refused redirects, a write never repeated and a dry run
// that sends nothing all hold.

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

// mutate sends one mutation and decodes the payload under its root field
// into out. GitLab answers 200 with top-level errors for a mutation it
// refused before running it, and words a missing resource and one the
// token may not change alike; what names the resource for the message.
func (c *Client) mutate(ctx context.Context, name, field, what, query string, vars map[string]any, out any) error {
	var answer struct {
		Data   map[string]json.RawMessage `json:"data"`
		Errors []gitlab.GraphQLError      `json:"errors"`
	}
	if err := c.Do(ctx, Call{Method: "POST", Root: RootWeb, Path: "api/graphql",
		Body: map[string]any{"query": query, "variables": vars}, Name: name}, &answer); err != nil {
		return err
	}
	// A payload means the mutation ran, whatever else the answer says,
	// so it is read before any top-level error.
	if payload := answer.Data[field]; len(payload) > 0 && string(payload) != "null" {
		if err := json.Unmarshal(payload, out); err != nil {
			return Wrap(ClassAmbiguousOutcome, err, "GitLab ran %s, and its answer could not be read", name)
		}
		return nil
	}
	for _, e := range answer.Errors {
		if strings.Contains(e.Message, noAccess) {
			return Errf(ClassNotFound, "GitLab found no %s there that this account may change: it does not say whether it "+
				"is missing or the token may not change it; nothing was changed", what)
		}
	}
	return Errf(ClassUnexpected, "GitLab refused %s with an error this server does not know; nothing was changed", name)
}

// gid is a GraphQL global id, such as gid://gitlab/User/1001.
func gid(kind string, id int64) string {
	return "gid://gitlab/" + kind + "/" + strconv.FormatInt(id, 10)
}

// RereviewMergeRequest asks a reviewer of a merge request for a new
// review. GitLab sets their review state back to unreviewed, removes
// their approval if they gave one, notifies them and adds a to-do and a
// system note (MergeRequests::RequestReviewService). The REST API has no
// route for it.
func (c *Client) RereviewMergeRequest(ctx context.Context, projectPath string, iid, userID int64) error {
	var out gitlab.ReviewerRereview
	vars := map[string]any{"projectPath": projectPath, "iid": strconv.FormatInt(iid, 10), "userId": gid("User", userID)}
	if err := c.mutate(ctx, "rerequest_review", "mergeRequestReviewerRereview", "merge request !"+strconv.FormatInt(iid, 10),
		rereviewMutation, vars, &out); err != nil {
		return err
	}
	for _, e := range out.Errors {
		switch e {
		case "Reviewer not found":
			return Errf(ClassInvalid, "that user is not a reviewer of !%d; nothing was changed", iid)
		case "Invalid permissions":
			return Errf(ClassForbidden, "this account may not change !%d; nothing was changed", iid)
		}
	}
	if len(out.Errors) > 0 {
		return Errf(ClassUnexpected, "GitLab refused re-requesting the review: %s", strings.Join(out.Errors, "; "))
	}
	return nil
}
