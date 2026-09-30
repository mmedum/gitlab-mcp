package gapi

import (
	"context"

	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
)

// Time tracking on an issue or a merge request (lib/api/
// time_tracking_endpoints.rb). Each write answers the item's time stats.
// Setting or resetting the estimate lands the same way twice, so it may
// repeat. Adding spent time adds a timelog, which GitLab does not
// deduplicate, and resetting it adds one too, so neither repeats (§4.5).

// durationBody is a duration in GitLab's human form, "1h30m".
type durationBody struct {
	Duration string `json:"duration"`
}

const estimateRepeats = "setting an estimate twice leaves the same estimate"

// SetIssueTimeEstimate sets an issue's estimate.
func (c *Client) SetIssueTimeEstimate(ctx context.Context, p Project, iid int64, duration string) (*gitlab.TimeStats, error) {
	return c.timeStats(ctx, Call{Method: "POST", Path: "projects/{}/issues/{}/time_estimate", Args: []string{p.segment(), idArg(iid)},
		Body: durationBody{duration}, Repeatable: estimateRepeats, Name: "track_time"})
}

// SetMergeRequestTimeEstimate sets a merge request's estimate.
func (c *Client) SetMergeRequestTimeEstimate(ctx context.Context, p Project, iid int64, duration string) (*gitlab.TimeStats, error) {
	return c.timeStats(ctx, Call{Method: "POST", Path: "projects/{}/merge_requests/{}/time_estimate", Args: []string{p.segment(), idArg(iid)},
		Body: durationBody{duration}, Repeatable: estimateRepeats, Name: "track_time"})
}

// ResetIssueTimeEstimate sets an issue's estimate to 0.
func (c *Client) ResetIssueTimeEstimate(ctx context.Context, p Project, iid int64) (*gitlab.TimeStats, error) {
	return c.timeStats(ctx, Call{Method: "POST", Path: "projects/{}/issues/{}/reset_time_estimate", Args: []string{p.segment(), idArg(iid)},
		Repeatable: estimateRepeats, Name: "track_time"})
}

// ResetMergeRequestTimeEstimate sets a merge request's estimate to 0.
func (c *Client) ResetMergeRequestTimeEstimate(ctx context.Context, p Project, iid int64) (*gitlab.TimeStats, error) {
	return c.timeStats(ctx, Call{Method: "POST", Path: "projects/{}/merge_requests/{}/reset_time_estimate",
		Args: []string{p.segment(), idArg(iid)}, Repeatable: estimateRepeats, Name: "track_time"})
}

// AddIssueSpentTime adds a timelog of duration, which may be negative,
// to an issue.
func (c *Client) AddIssueSpentTime(ctx context.Context, p Project, iid int64, duration string) (*gitlab.TimeStats, error) {
	return c.timeStats(ctx, Call{Method: "POST", Path: "projects/{}/issues/{}/add_spent_time", Args: []string{p.segment(), idArg(iid)},
		Body: durationBody{duration}, Name: "track_time"})
}

// AddMergeRequestSpentTime adds a timelog of duration, which may be
// negative, to a merge request.
func (c *Client) AddMergeRequestSpentTime(ctx context.Context, p Project, iid int64, duration string) (*gitlab.TimeStats, error) {
	return c.timeStats(ctx, Call{Method: "POST", Path: "projects/{}/merge_requests/{}/add_spent_time", Args: []string{p.segment(), idArg(iid)},
		Body: durationBody{duration}, Name: "track_time"})
}

// ResetIssueSpentTime adds a timelog that takes an issue's total spent
// time back to 0.
func (c *Client) ResetIssueSpentTime(ctx context.Context, p Project, iid int64) (*gitlab.TimeStats, error) {
	return c.timeStats(ctx, Call{Method: "POST", Path: "projects/{}/issues/{}/reset_spent_time", Args: []string{p.segment(), idArg(iid)},
		Name: "track_time"})
}

// ResetMergeRequestSpentTime adds a timelog that takes a merge request's
// total spent time back to 0.
func (c *Client) ResetMergeRequestSpentTime(ctx context.Context, p Project, iid int64) (*gitlab.TimeStats, error) {
	return c.timeStats(ctx, Call{Method: "POST", Path: "projects/{}/merge_requests/{}/reset_spent_time", Args: []string{p.segment(), idArg(iid)},
		Name: "track_time"})
}

func (c *Client) timeStats(ctx context.Context, call Call) (*gitlab.TimeStats, error) {
	var out gitlab.TimeStats
	if err := c.Do(ctx, call, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
