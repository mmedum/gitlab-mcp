package gapi

import (
	"context"
	"net/http"

	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
)

// The signed-in account's own notifications and to-do items on an issue
// or a merge request (lib/api/subscriptions.rb, lib/api/todos.rb). GitLab
// answers 304 with no body when the state asked for already holds, so
// each method reports whether the call changed anything. Sending one
// twice changes nothing the first did not, so each may repeat: GitLab
// keeps one pending to-do you added per item and answers 304 to another
// (TodoService#excluded_user_ids).

const (
	subscribeRepeats   = "subscribing twice leaves you subscribed"
	unsubscribeRepeats = "unsubscribing twice leaves you unsubscribed"
	todoRepeats        = "GitLab keeps one pending to-do you added per item and answers 304 to a second"
)

// SubscribeIssue subscribes the account to an issue's notifications.
// changed is false when it already was subscribed.
func (c *Client) SubscribeIssue(ctx context.Context, p Project, iid int64) (sub *gitlab.ItemSubscription, changed bool, err error) {
	return c.subscription(ctx, Call{Method: "POST", Path: "projects/{}/issues/{}/subscribe", Args: []string{p.segment(), idArg(iid)},
		Repeatable: subscribeRepeats, NotModified: "already subscribed", Name: "subscribe"})
}

// UnsubscribeIssue unsubscribes the account from an issue's
// notifications. changed is false when it was not subscribed.
func (c *Client) UnsubscribeIssue(ctx context.Context, p Project, iid int64) (sub *gitlab.ItemSubscription, changed bool, err error) {
	return c.subscription(ctx, Call{Method: "POST", Path: "projects/{}/issues/{}/unsubscribe", Args: []string{p.segment(), idArg(iid)},
		Repeatable: unsubscribeRepeats, NotModified: "not subscribed", Name: "subscribe"})
}

// SubscribeMergeRequest subscribes the account to a merge request's
// notifications, which GitLab allows only to those who may update it.
func (c *Client) SubscribeMergeRequest(ctx context.Context, p Project, iid int64) (sub *gitlab.ItemSubscription, changed bool, err error) {
	return c.subscription(ctx, Call{Method: "POST", Path: "projects/{}/merge_requests/{}/subscribe", Args: []string{p.segment(), idArg(iid)},
		Repeatable: subscribeRepeats, NotModified: "already subscribed", Name: "subscribe"})
}

// UnsubscribeMergeRequest unsubscribes the account from a merge
// request's notifications, which takes the same rights.
func (c *Client) UnsubscribeMergeRequest(ctx context.Context, p Project, iid int64) (sub *gitlab.ItemSubscription, changed bool, err error) {
	return c.subscription(ctx, Call{Method: "POST", Path: "projects/{}/merge_requests/{}/unsubscribe", Args: []string{p.segment(), idArg(iid)},
		Repeatable: unsubscribeRepeats, NotModified: "not subscribed", Name: "subscribe"})
}

// GetIssueSubscription reads whether the account is subscribed to an
// issue.
func (c *Client) GetIssueSubscription(ctx context.Context, p Project, iid int64) (*gitlab.ItemSubscription, error) {
	var out gitlab.ItemSubscription
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/issues/{}", Args: []string{p.segment(), idArg(iid)}, Name: "subscribe"}, &out)
	return &out, err
}

// GetMergeRequestSubscription reads whether the account is subscribed to
// a merge request.
func (c *Client) GetMergeRequestSubscription(ctx context.Context, p Project, iid int64) (*gitlab.ItemSubscription, error) {
	var out gitlab.ItemSubscription
	err := c.Do(ctx, Call{Method: "GET", Path: "projects/{}/merge_requests/{}", Args: []string{p.segment(), idArg(iid)},
		Name: "subscribe"}, &out)
	return &out, err
}

func (c *Client) subscription(ctx context.Context, call Call) (*gitlab.ItemSubscription, bool, error) {
	var out gitlab.ItemSubscription
	res, err := c.do(ctx, call, &out)
	if err != nil {
		return nil, false, err
	}
	return &out, res.status != http.StatusNotModified, nil
}

// CreateIssueTodo adds a to-do item for the account on an issue. todo is
// nil when a pending one it added is already there.
func (c *Client) CreateIssueTodo(ctx context.Context, p Project, iid int64) (*gitlab.Todo, error) {
	return c.todo(ctx, Call{Method: "POST", Path: "projects/{}/issues/{}/todo", Args: []string{p.segment(), idArg(iid)},
		Repeatable: todoRepeats, NotModified: "a pending to-do you added is already there", Name: "add_todo"})
}

// CreateMergeRequestTodo adds a to-do item for the account on a merge
// request. todo is nil when a pending one it added is already there.
func (c *Client) CreateMergeRequestTodo(ctx context.Context, p Project, iid int64) (*gitlab.Todo, error) {
	return c.todo(ctx, Call{Method: "POST", Path: "projects/{}/merge_requests/{}/todo", Args: []string{p.segment(), idArg(iid)},
		Repeatable: todoRepeats, NotModified: "a pending to-do you added is already there", Name: "add_todo"})
}

func (c *Client) todo(ctx context.Context, call Call) (*gitlab.Todo, error) {
	var out gitlab.Todo
	res, err := c.do(ctx, call, &out)
	if err != nil || res.status == http.StatusNotModified {
		return nil, err
	}
	return &out, nil
}
