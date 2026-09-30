package gitlabtest

import (
	"fmt"
	"net/http"

	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
)

// Subscriptions and to-do items one adds oneself, on an issue or a merge
// request, as lib/api/subscriptions.rb and lib/api/todos.rb serve them at
// v19.4.1-ee. Both answer 201 with the item or the new to-do, and 304
// with no body when the state asked for already holds. A participant (the
// author, an assignee, anyone who commented) is subscribed until they
// unsubscribe (Issuable#subscribed_without_subscriptions?). Subscribing
// to a merge request needs update_merge_request: a Developer, or its
// author or an assignee; anyone else gets 403. A to-do one adds is
// "marked", and GitLab adds none while such a pending one is there
// (TodoService#excluded_user_ids); other kinds do not stop it.

// notifiable is an issue or a merge request as these routes see it.
type notifiable struct {
	t         target
	item      any // the item as its own GET serves it
	author    string
	assignees []string
	title     string
	state     string
	webURL    string
}

func issueNotifiable(iss *gitlab.Issue) notifiable {
	return notifiable{t: issueTarget(iss), item: iss, author: iss.Author.Username, assignees: usernames(iss.Assignees),
		title: iss.Title, state: iss.State, webURL: iss.WebURL}
}

func mrNotifiable(mr *gitlab.MergeRequest) notifiable {
	return notifiable{t: mrTarget(mr), item: mr, author: mr.Author.Username, assignees: usernames(mr.Assignees),
		title: mr.Title, state: mr.State, webURL: mr.WebURL}
}

// serveNotify routes subscribe, unsubscribe and todo on one item; it
// reports whether it answered.
func (s *Server) serveNotify(w http.ResponseWriter, r *http.Request, p *project, it notifiable, user string, rest []string) bool {
	if r.Method != http.MethodPost || len(rest) != 1 {
		return false
	}
	switch rest[0] {
	case "subscribe", "unsubscribe":
		s.setSubscription(w, p, it, user, rest[0] == "subscribe")
	case "todo":
		s.addTodo(w, p, it, user)
	default:
		return false
	}
	return true
}

func (s *Server) setSubscription(w http.ResponseWriter, p *project, it notifiable, user string, want bool) {
	if it.t.kind == "mr" && s.accessLevel(p, user) < developerAccess && it.author != user && !has(it.assignees, user) {
		message(w, http.StatusForbidden, "403 Forbidden")
		return
	}
	if s.subscribed(p, it, user) == want {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if s.subscriptions == nil {
		s.subscriptions = map[string]bool{}
	}
	s.subscriptions[subscriptionKey(p, it.t, user)] = want
	writeJSON(w, http.StatusCreated, withExtra(it.item, map[string]any{"subscribed": want}))
}

// subscribed is the user's subscription to an item: the one they set,
// or, when they set none, whether they take part in it.
func (s *Server) subscribed(p *project, it notifiable, user string) bool {
	if v, ok := s.subscriptions[subscriptionKey(p, it.t, user)]; ok {
		return v
	}
	if it.author == user || has(it.assignees, user) {
		return true
	}
	for _, d := range p.discussions[it.t.key()] {
		for _, n := range d.Notes {
			if !n.System && n.Author.Username == user {
				return true
			}
		}
	}
	return false
}

// withSubscribed is an item as its own GET serves it to user.
func (s *Server) withSubscribed(p *project, it notifiable, user string) map[string]any {
	return withExtra(it.item, map[string]any{"subscribed": s.subscribed(p, it, user)})
}

func subscriptionKey(p *project, t target, user string) string {
	return fmt.Sprintf("%d\x00%s\x00%s", p.ID, t.key(), user)
}

func (s *Server) addTodo(w http.ResponseWriter, p *project, it notifiable, user string) {
	id := int64(TodoDone + 100)
	for _, td := range s.todos {
		if td.user == user && td.State == "pending" && td.ActionName == "marked" && td.TargetType == it.t.typ &&
			td.Target.IID == it.t.iid && td.Project != nil && td.Project.ID == p.ID {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		id = max(id, td.ID+1)
	}
	td := todo{user: user, Todo: gitlab.Todo{ID: id, Project: &gitlab.TodoProject{ID: p.ID, PathWithNamespace: p.PathWithNamespace},
		Author: s.user(user), ActionName: "marked", TargetType: it.t.typ,
		Target:    gitlab.TodoTarget{IID: it.t.iid, Title: it.title, State: it.state},
		TargetURL: it.webURL, Body: it.title, State: "pending", CreatedAt: s.opts.Now().UTC()}}
	s.todos = append(s.todos, td)
	writeJSON(w, http.StatusCreated, todoJSON(td))
}

// Subscribed reports whether user is subscribed to an issue (kind
// "issue") or a merge request (kind "mr"), as GitLab would answer them.
func (s *Server) Subscribed(projectPath, kind string, iid int64, user string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil {
		return false
	}
	if kind == "mr" {
		if mr := findMR(p, itoa(iid)); mr != nil {
			return s.subscribed(p, mrNotifiable(mr), user)
		}
		return false
	}
	if iss := findIssue(p, itoa(iid)); iss != nil {
		return s.subscribed(p, issueNotifiable(iss), user)
	}
	return false
}

// PendingTodos returns the ids of user's pending to-do items.
func (s *Server) PendingTodos(user string) []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []int64
	for _, td := range s.todos {
		if td.user == user && td.State == "pending" {
			out = append(out, td.ID)
		}
	}
	return out
}
