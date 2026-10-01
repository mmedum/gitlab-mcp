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
//
// Each route comes in four forms, on an issue or a merge request and on
// a comment on either. Every method spells the four Calls out, since the
// gates bind a request to an operation by its literal Method and Path,
// and awardCall picks the one that addresses the awardable.

// Awardable is what a reaction is on: an issue or a merge request by
// iid, or a comment on one when NoteID is set.
type Awardable struct {
	MergeRequest bool
	IID          int64
	NoteID       int64
}

// awardBody is a reaction's POST body.
type awardBody struct {
	Name string `json:"name"`
}

// awardCall is the one of four Calls that addresses a, its Args filled:
// the project, the iid, the comment when there is one, and award when it
// is not 0. out is where the caller decodes the answer; it is passed
// here only so the gates bind each literal to it.
func awardCall(p Project, a Awardable, award int64, issue, issueNote, mr, mrNote Call, _ any) Call {
	call := issue
	switch {
	case a.MergeRequest && a.NoteID != 0:
		call = mrNote
	case a.MergeRequest:
		call = mr
	case a.NoteID != 0:
		call = issueNote
	}
	call.Args = []string{p.segment(), idArg(a.IID)}
	if a.NoteID != 0 {
		call.Args = append(call.Args, idArg(a.NoteID))
	}
	if award != 0 {
		call.Args = append(call.Args, idArg(award))
	}
	call.Name = "react"
	return call
}

// ListAwards reads one page of the reactions on a, oldest first.
func (c *Client) ListAwards(ctx context.Context, p Project, a Awardable, opts ListOptions) ([]gitlab.AwardEmoji, Page, error) {
	var out []gitlab.AwardEmoji
	call := awardCall(p, a, 0,
		Call{Method: "GET", Path: "projects/{}/issues/{}/award_emoji"},
		Call{Method: "GET", Path: "projects/{}/issues/{}/notes/{}/award_emoji"},
		Call{Method: "GET", Path: "projects/{}/merge_requests/{}/award_emoji"},
		Call{Method: "GET", Path: "projects/{}/merge_requests/{}/notes/{}/award_emoji"}, &out)
	page, err := c.list(ctx, call, opts, &out)
	return out, page, err
}

// GetAward reads one reaction on a by its id; one that is gone is
// [not_found].
func (c *Client) GetAward(ctx context.Context, p Project, a Awardable, award int64) (*gitlab.AwardEmoji, error) {
	var out gitlab.AwardEmoji
	call := awardCall(p, a, award,
		Call{Method: "GET", Path: "projects/{}/issues/{}/award_emoji/{}"},
		Call{Method: "GET", Path: "projects/{}/issues/{}/notes/{}/award_emoji/{}"},
		Call{Method: "GET", Path: "projects/{}/merge_requests/{}/award_emoji/{}"},
		Call{Method: "GET", Path: "projects/{}/merge_requests/{}/notes/{}/award_emoji/{}"}, &out)
	if err := c.Do(ctx, call, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateAward adds the account's reaction named name to a. GitLab
// answers 404 when it refuses, whatever the reason, with the reason in
// the message.
func (c *Client) CreateAward(ctx context.Context, p Project, a Awardable, name string) (*gitlab.AwardEmoji, error) {
	var out gitlab.AwardEmoji
	body := awardBody{Name: name}
	call := awardCall(p, a, 0,
		Call{Method: "POST", Path: "projects/{}/issues/{}/award_emoji", Body: body},
		Call{Method: "POST", Path: "projects/{}/issues/{}/notes/{}/award_emoji", Body: body},
		Call{Method: "POST", Path: "projects/{}/merge_requests/{}/award_emoji", Body: body},
		Call{Method: "POST", Path: "projects/{}/merge_requests/{}/notes/{}/award_emoji", Body: body}, &out)
	if err := c.Do(ctx, call, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteAward removes the reaction with id award from a. GitLab allows
// it only to the reaction's own account, 401 otherwise.
func (c *Client) DeleteAward(ctx context.Context, p Project, a Awardable, award int64) error {
	call := awardCall(p, a, award,
		Call{Method: "DELETE", Path: "projects/{}/issues/{}/award_emoji/{}"},
		Call{Method: "DELETE", Path: "projects/{}/issues/{}/notes/{}/award_emoji/{}"},
		Call{Method: "DELETE", Path: "projects/{}/merge_requests/{}/award_emoji/{}"},
		Call{Method: "DELETE", Path: "projects/{}/merge_requests/{}/notes/{}/award_emoji/{}"}, nil)
	return c.Do(ctx, call, nil)
}
