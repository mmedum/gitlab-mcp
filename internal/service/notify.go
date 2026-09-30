package service

import (
	"context"
	"slices"

	"github.com/mmedum/gitlab-mcp/v2/internal/gapi"
	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/v2/internal/model"
)

// The signed-in account's own notifications and to-do items on an issue
// or a merge request (§7.7, §18 row 102). GitLab answers 304 when the
// state asked for already holds, which is unchanged, not a failure. Both
// writes land the same way twice, so a lost answer is sent again rather
// than settled by a read (§4.5). Both are held to the write allow-list by
// the item's project, as mark_todos_done is (§4.7).

// notifyItem checks an item named by type and iid.
func notifyItem(typ string, iid int64) (mr bool, err error) {
	if typ != "issue" && typ != "merge_request" {
		return false, gapi.Errf(gapi.ClassInvalid, "type is issue or merge_request")
	}
	if iid <= 0 {
		return false, gapi.Errf(gapi.ClassInvalid, "iid is the item's number in its project, a positive number")
	}
	return typ == "merge_request", nil
}

// mrSubscribers is who GitLab lets subscribe to a merge request: its
// finder asks for update_merge_request (lib/api/subscriptions.rb L19).
const mrSubscribers = "GitLab lets only those who may update a merge request subscribe to it or unsubscribe: " +
	"the Developer role or higher, or its author or an assignee"

// Subscribe subscribes the signed-in account to an issue's or a merge
// request's notifications, or unsubscribes it. The author, the assignees
// and anyone who commented are subscribed until they unsubscribe.
func (s *Service) Subscribe(ctx context.Context, raw, typ string, iid int64, subscribe bool) (model.SubscriptionWrite, error) {
	mr, err := notifyItem(typ, iid)
	if err != nil {
		return model.SubscriptionWrite{}, err
	}
	t, err := s.writeTarget(ctx, raw)
	if err != nil {
		return model.SubscriptionWrite{}, err
	}
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
			out.Notes = []string{mrSubscribers + "."}
		}
		return out, nil
	}
	var sub *gitlab.ItemSubscription
	var changed bool
	switch {
	case mr && subscribe:
		sub, changed, err = s.client.SubscribeMergeRequest(ctx, t.p, iid)
	case mr:
		sub, changed, err = s.client.UnsubscribeMergeRequest(ctx, t.p, iid)
	case subscribe:
		sub, changed, err = s.client.SubscribeIssue(ctx, t.p, iid)
	default:
		sub, changed, err = s.client.UnsubscribeIssue(ctx, t.p, iid)
	}
	if mr && gapi.IsClass(err, gapi.ClassForbidden) {
		return model.SubscriptionWrite{}, gapi.Wrap(gapi.ClassForbidden, err, "%s. GitLab said: %s", mrSubscribers, gapi.AsError(err).Message)
	}
	if err != nil {
		return model.SubscriptionWrite{}, err
	}
	if !changed {
		// GitLab's 304 carries no body; the item says where it is.
		out.Subscribed = subscribe
		if now, err := s.readSubscription(ctx, t.p, mr, iid); err == nil {
			out.Subscribed, out.WebURL = now.Subscribed, now.WebURL
		}
		out.Notes = []string{"You already were " + state + "; GitLab changed nothing."}
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
	mr, err := notifyItem(typ, iid)
	if err != nil {
		return model.TodoWrite{}, err
	}
	t, err := s.writeTarget(ctx, raw)
	if err != nil {
		return model.TodoWrite{}, err
	}
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
		if out.TodoID, err = s.markedTodo(ctx, t, mr, iid); err != nil {
			return model.TodoWrite{}, err
		}
		if out.TodoID != 0 {
			out.Notes = []string{already + "would add none."}
			return out, nil
		}
		out.WouldSend = preview("POST", "add a to-do for you", nil)
		return out, nil
	}
	var todo *gitlab.Todo
	if mr {
		todo, err = s.client.CreateMergeRequestTodo(ctx, t.p, iid)
	} else {
		todo, err = s.client.CreateIssueTodo(ctx, t.p, iid)
	}
	if err != nil {
		return model.TodoWrite{}, err
	}
	if todo != nil {
		out.Outcome, out.TodoID = "created", todo.ID
		return out, nil
	}
	note := already + "added none."
	// Best effort: the 304 carries no body, and the id is what
	// mark_todos_done needs.
	if out.TodoID, err = s.markedTodo(ctx, t, mr, iid); err != nil || out.TodoID == 0 {
		note += " Its id was not found; list_todos lists your pending items."
	}
	out.Notes = []string{note}
	return out, nil
}

// markedTodo finds the account's pending to-do it added on the item, 0
// when there is none.
func (s *Service) markedTodo(ctx context.Context, t target, mr bool, iid int64) (int64, error) {
	q := gapi.TodoQuery{ProjectID: t.project.ID, State: "pending", Action: "marked", Type: "Issue"}
	if mr {
		q.Type = "MergeRequest"
	}
	mine := func(td gitlab.Todo) bool { return td.Target.IID == iid }
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
