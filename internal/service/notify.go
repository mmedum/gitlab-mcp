package service

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/internal/gapi"
	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/v2/internal/model"
)

// The signed-in account's own notifications and to-do items on an issue
// or a merge request (§7.7, §18 row 102). GitLab answers 304 when the
// state asked for already holds, which is unchanged, not a failure.
// Subscribing sets a state and may repeat; adding a to-do is a create,
// never repeated, and a lost answer is settled by reading (§4.5). Both
// are held to the write allow-list by the item's project, as
// mark_todos_done is (§4.7). register has already held type to its enum.

// checkIID refuses an iid GitLab could not name.
func checkIID(iid int64) error {
	if iid <= 0 {
		return gapi.Errf(gapi.ClassInvalid, "iid is the item's number in its project, a positive number")
	}
	return nil
}

// mrSubscribers is who GitLab lets subscribe to a merge request: its
// finder asks for update_merge_request (lib/api/subscriptions.rb L19).
const mrSubscribers = "subscribing to a merge request, or unsubscribing, needs the rights to update it: " +
	"the Developer role or higher, or its author or an assignee"

// Subscribe subscribes the signed-in account to an issue's or a merge
// request's notifications, or unsubscribes it. The author, the assignees
// and anyone who commented are subscribed until they unsubscribe.
func (s *Service) Subscribe(ctx context.Context, raw, typ string, iid int64, subscribe bool) (model.SubscriptionWrite, error) {
	if err := checkIID(iid); err != nil {
		return model.SubscriptionWrite{}, err
	}
	t, err := s.writeTarget(ctx, raw)
	if err != nil {
		return model.SubscriptionWrite{}, err
	}
	mr := typ == "merge_request"
	state, verb := "subscribed", "subscribe to"
	if !subscribe {
		state, verb = "unsubscribed", "unsubscribe from"
	}
	what := "the issue"
	if mr {
		what = "the merge request"
	}
	out := model.SubscriptionWrite{Outcome: "unchanged", Write: model.Write{Target: t.ref}, Type: typ, IID: iid}
	if gapi.IsDryRun(ctx) {
		now, err := s.readSubscription(ctx, t.p, mr, iid)
		if err != nil {
			return model.SubscriptionWrite{}, err
		}
		out.Outcome, out.DryRun, out.Subscribed, out.WebURL = "dry_run", true, now.Subscribed, now.WebURL
		if now.Subscribed == subscribe {
			out.Notes = []string{"You already are " + state + ", so GitLab would change nothing."}
			return out, nil
		}
		out.WouldSend = preview("POST", verb+" "+what, nil)
		if mr {
			out.Notes = []string{"GitLab's rule for a merge request: " + mrSubscribers + "."}
		}
		return out, nil
	}
	var sub *gitlab.ItemSubscription
	var answer gapi.Answered
	switch {
	case mr && subscribe:
		sub, answer, err = s.client.SubscribeMergeRequest(ctx, t.p, iid)
	case mr:
		sub, answer, err = s.client.UnsubscribeMergeRequest(ctx, t.p, iid)
	case subscribe:
		sub, answer, err = s.client.SubscribeIssue(ctx, t.p, iid)
	default:
		sub, answer, err = s.client.UnsubscribeIssue(ctx, t.p, iid)
	}
	if mr && gapi.IsClass(err, gapi.ClassForbidden) {
		return model.SubscriptionWrite{}, subscribeRefused(err, t.project)
	}
	if err != nil {
		return model.SubscriptionWrite{}, err
	}
	if answer.NotModified {
		// GitLab's 304 carries no body; the item says where it is.
		out.Subscribed = subscribe
		if now, err := s.readSubscription(ctx, t.p, mr, iid); err == nil {
			out.Subscribed, out.WebURL = now.Subscribed, now.WebURL
		}
		if answer.Resent {
			// The first attempt's answer was lost, and it may have made
			// the change the 304 reports.
			out.Outcome = state
			out.Notes = []string{"GitLab's answer to the first attempt was lost, and a second found you " + state +
				": the first most likely made the change."}
			return out, nil
		}
		out.Notes = []string{"GitLab reports you are " + state + "; it made no change on this request."}
		return out, nil
	}
	out.Subscribed, out.WebURL = sub.Subscribed, sub.WebURL
	if sub.Subscribed == subscribe {
		out.Outcome = state
	} else {
		out.Notes = []string{"GitLab answered, but not with you " + state + "."}
	}
	return out, nil
}

// subscribeRefused words GitLab's 403 on a merge request. Its cause is
// not in the answer: the role, an archived project and a hidden merge
// request all refuse alike, so GitLab's text comes first.
func subscribeRefused(err error, project *gitlab.Project) error {
	e := gapi.AsError(err)
	if project != nil && project.Archived {
		return gapi.Wrap(gapi.ClassForbidden, err, "%s. The project is archived, and GitLab refuses changes to an archived project", e.Message)
	}
	return gapi.Wrap(gapi.ClassForbidden, err, "%s. It may be the role: %s", e.Message, mrSubscribers)
}

func (s *Service) readSubscription(ctx context.Context, p gapi.Project, mr bool, iid int64) (*gitlab.ItemSubscription, error) {
	if mr {
		return s.client.GetMergeRequestSubscription(ctx, p, iid)
	}
	return s.client.GetIssueSubscription(ctx, p, iid)
}

// AddTodo adds a to-do item for the signed-in account on an issue or a
// merge request. GitLab adds none while a pending one the account added
// is there; other pending items, and done ones, do not stop it.
func (s *Service) AddTodo(ctx context.Context, raw, typ string, iid int64) (model.TodoWrite, error) {
	if err := checkIID(iid); err != nil {
		return model.TodoWrite{}, err
	}
	t, err := s.writeTarget(ctx, raw)
	if err != nil {
		return model.TodoWrite{}, err
	}
	mr := typ == "merge_request"
	out := model.TodoWrite{Outcome: "unchanged", Write: model.Write{Target: t.ref}, Type: typ, IID: iid}
	const already = "A pending to-do you added is already there, so GitLab "
	if gapi.IsDryRun(ctx) {
		if mr {
			_, err = s.client.GetMergeRequest(ctx, t.p, iid)
		} else {
			_, err = s.client.GetIssue(ctx, t.p, iid)
		}
		if err != nil {
			return model.TodoWrite{}, err
		}
		out.Outcome, out.DryRun = "dry_run", true
		if out.TodoID, err = s.markedTodo(ctx, t, mr, iid, time.Time{}); err != nil {
			return model.TodoWrite{}, err
		}
		if out.TodoID != 0 {
			out.Notes = []string{already + "would add none."}
			return out, nil
		}
		out.WouldSend = preview("POST", "add a to-do for you", nil)
		return out, nil
	}
	start := time.Now()
	var todo *gitlab.Todo
	if mr {
		todo, err = s.client.CreateMergeRequestTodo(ctx, t.p, iid)
	} else {
		todo, err = s.client.CreateIssueTodo(ctx, t.p, iid)
	}
	if err != nil {
		return model.TodoWrite{}, settle(err, "to-do", func() (string, error) {
			id, err := s.markedTodo(ctx, t, mr, iid, start.Add(-settleSkew))
			if id == 0 {
				return "", err
			}
			return fmt.Sprintf("to-do item %d, which mark_todos_done takes", id), err
		})
	}
	if todo != nil {
		out.Outcome, out.TodoID = "created", todo.ID
		return out, nil
	}
	note := already + "added none."
	// Best effort: the 304 carries no body, and the id is what
	// mark_todos_done needs.
	if out.TodoID, err = s.markedTodo(ctx, t, mr, iid, time.Time{}); err != nil || out.TodoID == 0 {
		note += " Its id was not found; list_todos lists your pending items."
	}
	out.Notes = []string{note}
	return out, nil
}

// markedTodo finds the account's pending to-do it added on the item,
// created at or after since when that is set; 0 when there is none.
func (s *Service) markedTodo(ctx context.Context, t target, mr bool, iid int64, since time.Time) (int64, error) {
	q := gapi.TodoQuery{ProjectID: t.project.ID, State: "pending", Action: "marked", Type: "Issue"}
	if mr {
		q.Type = "MergeRequest"
	}
	mine := func(td gitlab.Todo) bool { return td.Target.IID == iid && !td.CreatedAt.Before(since) }
	rows, _, err := readPagesUntil(maxTodoPages, func(o gapi.ListOptions) ([]gitlab.Todo, gapi.Page, error) {
		return s.client.ListTodos(ctx, q, o)
	}, func(rows []gitlab.Todo) bool { return slices.ContainsFunc(rows, mine) })
	if err != nil {
		return 0, err
	}
	if i := slices.IndexFunc(rows, mine); i >= 0 {
		return rows[i].ID, nil
	}
	return 0, nil
}
