package gapi

import (
	"context"

	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
)

// Emoji reactions on an issue, a merge request or a comment on either
// (lib/api/award_emoji.rb). GitLab answers every refused POST with 404,
// a reaction already there included, so the service reads the list
// first. A POST is never repeated: a second would meet the first's
// reaction and fail. A DELETE repeats, and its second answer is 404.

// Awardable is what a reaction is on: an issue or a merge request by
// iid, or a comment on one when NoteID is set.
type Awardable struct {
	MergeRequest bool
	IID          int64
	NoteID       int64
}

// ListAwards reads one page of the reactions on a, oldest first.
func (c *Client) ListAwards(ctx context.Context, p Project, a Awardable, opts ListOptions) ([]gitlab.AwardEmoji, Page, error) {
	var call Call
	switch {
	case a.MergeRequest && a.NoteID != 0:
		call = Call{Method: "GET", Path: "projects/{}/merge_requests/{}/notes/{}/award_emoji",
			Args: []string{p.segment(), idArg(a.IID), idArg(a.NoteID)}, Name: "react"}
	case a.MergeRequest:
		call = Call{Method: "GET", Path: "projects/{}/merge_requests/{}/award_emoji", Args: []string{p.segment(), idArg(a.IID)}, Name: "react"}
	case a.NoteID != 0:
		call = Call{Method: "GET", Path: "projects/{}/issues/{}/notes/{}/award_emoji",
			Args: []string{p.segment(), idArg(a.IID), idArg(a.NoteID)}, Name: "react"}
	default:
		call = Call{Method: "GET", Path: "projects/{}/issues/{}/award_emoji", Args: []string{p.segment(), idArg(a.IID)}, Name: "react"}
	}
	var out []gitlab.AwardEmoji
	page, err := c.list(ctx, call, opts, &out)
	return out, page, err
}

// awardBody is a reaction's POST body.
type awardBody struct {
	Name string `json:"name"`
}

// CreateAward adds the account's reaction named name to a. GitLab
// answers 404 when it refuses, whatever the reason, with the reason in
// the message.
func (c *Client) CreateAward(ctx context.Context, p Project, a Awardable, name string) (*gitlab.AwardEmoji, error) {
	body := awardBody{Name: name}
	var call Call
	switch {
	case a.MergeRequest && a.NoteID != 0:
		call = Call{Method: "POST", Path: "projects/{}/merge_requests/{}/notes/{}/award_emoji",
			Args: []string{p.segment(), idArg(a.IID), idArg(a.NoteID)}, Body: body, Name: "react"}
	case a.MergeRequest:
		call = Call{Method: "POST", Path: "projects/{}/merge_requests/{}/award_emoji", Args: []string{p.segment(), idArg(a.IID)},
			Body: body, Name: "react"}
	case a.NoteID != 0:
		call = Call{Method: "POST", Path: "projects/{}/issues/{}/notes/{}/award_emoji",
			Args: []string{p.segment(), idArg(a.IID), idArg(a.NoteID)}, Body: body, Name: "react"}
	default:
		call = Call{Method: "POST", Path: "projects/{}/issues/{}/award_emoji", Args: []string{p.segment(), idArg(a.IID)},
			Body: body, Name: "react"}
	}
	var out gitlab.AwardEmoji
	if err := c.Do(ctx, call, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteAward removes the reaction with id award from a. GitLab allows
// it only to the reaction's own account, 401 otherwise.
func (c *Client) DeleteAward(ctx context.Context, p Project, a Awardable, award int64) error {
	var call Call
	switch {
	case a.MergeRequest && a.NoteID != 0:
		call = Call{Method: "DELETE", Path: "projects/{}/merge_requests/{}/notes/{}/award_emoji/{}",
			Args: []string{p.segment(), idArg(a.IID), idArg(a.NoteID), idArg(award)}, Name: "react"}
	case a.MergeRequest:
		call = Call{Method: "DELETE", Path: "projects/{}/merge_requests/{}/award_emoji/{}",
			Args: []string{p.segment(), idArg(a.IID), idArg(award)}, Name: "react"}
	case a.NoteID != 0:
		call = Call{Method: "DELETE", Path: "projects/{}/issues/{}/notes/{}/award_emoji/{}",
			Args: []string{p.segment(), idArg(a.IID), idArg(a.NoteID), idArg(award)}, Name: "react"}
	default:
		call = Call{Method: "DELETE", Path: "projects/{}/issues/{}/award_emoji/{}",
			Args: []string{p.segment(), idArg(a.IID), idArg(award)}, Name: "react"}
	}
	return c.Do(ctx, call, nil)
}
