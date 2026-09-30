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

// SetTimeEstimate sets an issue's or a merge request's estimate.
func (c *Client) SetTimeEstimate(ctx context.Context, p Project, mr bool, iid int64, duration string) (*gitlab.TimeStats, error) {
	call := Call{Method: "POST", Path: "projects/{}/issues/{}/time_estimate", Args: []string{p.segment(), idArg(iid)},
		Body: durationBody{duration}, Repeatable: estimateRepeats, Name: "track_time"}
	if mr {
		call = Call{Method: "POST", Path: "projects/{}/merge_requests/{}/time_estimate", Args: []string{p.segment(), idArg(iid)},
			Body: durationBody{duration}, Repeatable: estimateRepeats, Name: "track_time"}
	}
	return c.timeStats(ctx, call)
}

// ResetTimeEstimate sets an issue's or a merge request's estimate to 0.
func (c *Client) ResetTimeEstimate(ctx context.Context, p Project, mr bool, iid int64) (*gitlab.TimeStats, error) {
	call := Call{Method: "POST", Path: "projects/{}/issues/{}/reset_time_estimate", Args: []string{p.segment(), idArg(iid)},
		Repeatable: estimateRepeats, Name: "track_time"}
	if mr {
		call = Call{Method: "POST", Path: "projects/{}/merge_requests/{}/reset_time_estimate", Args: []string{p.segment(), idArg(iid)},
			Repeatable: estimateRepeats, Name: "track_time"}
	}
	return c.timeStats(ctx, call)
}

// AddSpentTime adds a timelog of duration, which may be negative, to an
// issue or a merge request.
func (c *Client) AddSpentTime(ctx context.Context, p Project, mr bool, iid int64, duration string) (*gitlab.TimeStats, error) {
	call := Call{Method: "POST", Path: "projects/{}/issues/{}/add_spent_time", Args: []string{p.segment(), idArg(iid)},
		Body: durationBody{duration}, Name: "track_time"}
	if mr {
		call = Call{Method: "POST", Path: "projects/{}/merge_requests/{}/add_spent_time", Args: []string{p.segment(), idArg(iid)},
			Body: durationBody{duration}, Name: "track_time"}
	}
	return c.timeStats(ctx, call)
}

// ResetSpentTime adds a timelog that takes an issue's or a merge
// request's total spent time back to 0.
func (c *Client) ResetSpentTime(ctx context.Context, p Project, mr bool, iid int64) (*gitlab.TimeStats, error) {
	call := Call{Method: "POST", Path: "projects/{}/issues/{}/reset_spent_time", Args: []string{p.segment(), idArg(iid)},
		Name: "track_time"}
	if mr {
		call = Call{Method: "POST", Path: "projects/{}/merge_requests/{}/reset_spent_time", Args: []string{p.segment(), idArg(iid)},
			Name: "track_time"}
	}
	return c.timeStats(ctx, call)
}

func (c *Client) timeStats(ctx context.Context, call Call) (*gitlab.TimeStats, error) {
	var out gitlab.TimeStats
	if err := c.Do(ctx, call, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
