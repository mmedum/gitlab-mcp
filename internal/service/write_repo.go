package service

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/internal/model"
)

// Branches, commits and to-do items (§7.5, §7.7, §4.4).

// CreateBranch creates a branch at ref, the default branch when ref is
// empty. A name a protected rule covers is refused as create_commit
// refuses it: a new protected branch at a ref of the caller's choosing
// would put code there that no merge request showed (§4.4).
func (s *Service) CreateBranch(ctx context.Context, raw, branch, ref string) (model.BranchWrite, error) {
	if strings.TrimSpace(branch) == "" {
		return model.BranchWrite{}, gapi.Errf(gapi.ClassInvalid, "branch is empty")
	}
	t, err := s.writeTarget(ctx, raw)
	if err != nil {
		return model.BranchWrite{}, err
	}
	if ref == "" {
		ref = t.project.DefaultBranch
	}
	if err := s.guardBranch(ctx, t, branch, nil); err != nil {
		return model.BranchWrite{}, err
	}
	if gapi.IsDryRun(ctx) {
		return model.BranchWrite{Outcome: "dry_run", Branch: branch, Write: model.Write{DryRun: true, Target: t.ref,
			WouldSend: preview("POST", "create a branch", []string{"branch", "ref"})}}, nil
	}
	b, err := s.client.CreateBranch(ctx, t.p, branch, ref)
	if err != nil {
		return model.BranchWrite{}, settle(err, "branch", func() (string, error) {
			got, err := s.client.GetBranch(ctx, t.p, branch)
			if gapi.IsClass(err, gapi.ClassNotFound) {
				return "", nil
			}
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("the branch exists at %s", got.Commit.ID), nil
		})
	}
	return model.BranchWrite{Outcome: "created", Branch: b.Name, CommitSHA: b.Commit.ID, Protected: b.Protected, WebURL: b.WebURL,
		Write: model.Write{Target: t.ref}}, nil
}

// CommitAction is one file change of create_commit.
type CommitAction struct {
	Action       string // create, update, delete or move
	FilePath     string
	PreviousPath string
	Content      string
	Encoding     string // text or base64
	LastCommitID string
}

// CommitCreate is create_commit's request.
type CommitCreate struct {
	Project     string
	Branch      string
	StartBranch string
	Message     string
	Actions     []CommitAction
}

// MaxCommitActions caps one commit's file changes.
const MaxCommitActions = 100

func (in CommitCreate) check() error {
	switch {
	case strings.TrimSpace(in.Branch) == "":
		return gapi.Errf(gapi.ClassInvalid, "branch is empty")
	case strings.TrimSpace(in.Message) == "":
		return gapi.Errf(gapi.ClassInvalid, "message is empty")
	case len(in.Actions) == 0:
		return gapi.Errf(gapi.ClassInvalid, "actions is empty: a commit changes at least one file")
	case len(in.Actions) > MaxCommitActions:
		return gapi.Errf(gapi.ClassInvalid, "a commit takes at most %d actions", MaxCommitActions)
	}
	for i, a := range in.Actions {
		n := i + 1
		switch {
		case strings.TrimSpace(a.FilePath) == "":
			return gapi.Errf(gapi.ClassInvalid, "action %d has no file_path", n)
		case a.Action != "create" && a.Action != "update" && a.Action != "delete" && a.Action != "move":
			return gapi.Errf(gapi.ClassInvalid, "action %d: action must be create, update, delete or move", n)
		case a.Action != "create" && a.LastCommitID == "":
			// The witness (§4.6): GitLab refuses the change when the file
			// moved on since this commit id.
			return gapi.Errf(gapi.ClassInvalid, "action %d (%s) needs the file's last_commit_id, as get_file returned it, so a change "+
				"made since is not overwritten", n, a.Action)
		case a.Action == "move" && a.PreviousPath == "":
			return gapi.Errf(gapi.ClassInvalid, "action %d (move) needs previous_path, the file's path before the move", n)
		case a.Encoding != "" && a.Encoding != "text" && a.Encoding != "base64":
			return gapi.Errf(gapi.ClassInvalid, "action %d: encoding must be text or base64", n)
		}
	}
	return nil
}

// CreateCommit commits file changes to a branch. It refuses the default
// branch and every protected branch, read at call time: code reaches a
// protected branch only through a merge request (§4.4).
func (s *Service) CreateCommit(ctx context.Context, in CommitCreate) (model.CommitWrite, error) {
	if err := in.check(); err != nil {
		return model.CommitWrite{}, err
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.CommitWrite{}, err
	}
	before, err := s.client.GetBranch(ctx, t.p, in.Branch)
	exists := err == nil
	if err != nil && !gapi.IsClass(err, gapi.ClassNotFound) {
		return model.CommitWrite{}, err
	}
	if !exists {
		before = nil
	}
	if err := s.guardBranch(ctx, t, in.Branch, before); err != nil {
		return model.CommitWrite{}, err
	}
	switch {
	case !exists && in.StartBranch == "":
		return model.CommitWrite{}, gapi.Errf(gapi.ClassInvalid, "the branch does not exist: pass start_branch to create it from another "+
			"branch, such as %s", t.project.DefaultBranch)
	case exists && in.StartBranch != "":
		return model.CommitWrite{}, gapi.Errf(gapi.ClassInvalid, "the branch already exists, and start_branch only names where a new one "+
			"starts: leave start_branch out to commit on top of it")
	}
	body := gapi.CommitCreate{Branch: in.Branch, StartBranch: in.StartBranch, CommitMessage: in.Message}
	files := make([]string, 0, len(in.Actions))
	for _, a := range in.Actions {
		action := gapi.CommitAction{Action: a.Action, FilePath: a.FilePath, PreviousPath: a.PreviousPath, Encoding: a.Encoding,
			LastCommitID: a.LastCommitID}
		switch {
		case a.Action == "create" || a.Action == "update":
			action.Content = &a.Content
		case a.Action == "move" && a.Content != "":
			// A move without content keeps the file's own.
			action.Content = &a.Content
		}
		body.Actions = append(body.Actions, action)
		files = append(files, a.FilePath)
	}
	if gapi.IsDryRun(ctx) {
		return model.CommitWrite{Outcome: "dry_run", Branch: in.Branch, Files: files, ParentIDs: []string{}, Write: model.Write{DryRun: true,
			Target: t.ref, WouldSend: preview("POST", "commit to a branch", fieldsOf(body))}}, nil
	}
	c, err := s.client.CreateCommit(ctx, t.p, body)
	if err != nil {
		return model.CommitWrite{}, settle(err, "commit", func() (string, error) {
			return s.findCommit(ctx, t.p, in.Branch, in.Message, before, exists)
		})
	}
	out := model.CommitWrite{Outcome: "created", SHA: c.ID, ShortID: c.ShortID, Branch: in.Branch, ParentIDs: nonNil(c.ParentIDs),
		Files: files, WebURL: c.WebURL, Write: model.Write{Target: t.ref}}
	if c.Stats != nil {
		out.Additions, out.Deletions = c.Stats.Additions, c.Stats.Deletions
	}
	head, err := s.client.GetBranch(ctx, t.p, in.Branch)
	if err != nil {
		out.Notes = append(out.Notes, "The commit was made; reading the branch afterwards failed, so its head is not reported.")
		return out, nil //nolint:nilerr // the commit exists; a failed read afterwards is said, not a failed call
	}
	out.BranchHead = head.Commit.ID
	if head.Commit.ID != c.ID {
		out.Notes = append(out.Notes, "The branch has moved past this commit already: someone pushed after it.")
	}
	return out, nil
}

// findCommit settles an ambiguous commit: the branch's head moved, to a
// commit with this message.
func (s *Service) findCommit(ctx context.Context, p gapi.Project, branch, message string, before *gitlab.Branch, existed bool) (string, error) {
	head, err := s.client.GetBranch(ctx, p, branch)
	if gapi.IsClass(err, gapi.ClassNotFound) && !existed {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if existed && head.Commit.ID == before.Commit.ID {
		return "", nil
	}
	if sameText(head.Commit.Message, message) {
		return fmt.Sprintf("commit %s, the branch's head", head.Commit.ID), nil
	}
	return "", fmt.Errorf("the branch moved to a commit with another message")
}

// maxProtectedPages bounds the protected-branch rules read for the guard:
// a thousand rules, far past any project's.
const maxProtectedPages = 10

// guardBranch refuses the default branch and any protected branch (§4.4).
// A branch that exists says itself whether a rule protects it, as
// GitLab matched it; one the commit would create is held to the rules,
// read at call time, since it would be protected once made.
func (s *Service) guardBranch(ctx context.Context, t target, branch string, existing *gitlab.Branch) error {
	if branch == t.project.DefaultBranch {
		return gapi.Errf(gapi.ClassBlocked, "the default branch takes code only through a merge request, so this server does not commit to it; "+
			"nothing was sent. Commit to a new branch (start_branch %s), then open a merge request with create_merge_request", branch)
	}
	const refusal = "a protected branch takes code only through a merge request; nothing was sent. " +
		"Commit to a new branch, then open a merge request with create_merge_request"
	if existing != nil {
		if existing.Protected {
			return gapi.Errf(gapi.ClassBlocked, "the branch is protected, and %s", refusal)
		}
		return nil
	}
	rules, complete, err := readPages(maxProtectedPages, func(o gapi.ListOptions) ([]gitlab.ProtectedBranch, gapi.Page, error) {
		return s.client.ListProtectedBranches(ctx, t.p, o)
	})
	if err != nil {
		return err
	}
	if !complete {
		// A rule past those read could protect the branch.
		return gapi.Errf(gapi.ClassBlocked, "the project has more protected-branch rules than one read covers (%d), so whether this "+
			"branch would be protected cannot be told; nothing was sent", len(rules))
	}
	for _, r := range rules {
		if protectedMatch(r.Name, branch) {
			return gapi.Errf(gapi.ClassBlocked, "the branch would be protected (rule %q) once created, and %s", r.Name, refusal)
		}
	}
	return nil
}

// protectedMatch applies a protected-branch rule: an exact name, or a
// pattern whose * matches any characters, slashes included, as GitLab's
// RefMatcher does.
func protectedMatch(rule, branch string) bool {
	parts := strings.Split(rule, "*")
	if len(parts) == 1 {
		return rule == branch
	}
	rest, ok := strings.CutPrefix(branch, parts[0])
	if !ok {
		return false
	}
	last := parts[len(parts)-1]
	for _, part := range parts[1 : len(parts)-1] {
		i := strings.Index(rest, part)
		if i < 0 {
			return false
		}
		rest = rest[i+len(part):]
	}
	return len(rest) >= len(last) && strings.HasSuffix(rest, last)
}

// MaxTodos caps mark_todos_done.
const MaxTodos = 100

// MarkTodosDone marks the signed-in account's to-do items done by id.
// They are the account's own list, seen by nobody else, so the write
// allow-list, which is about projects, does not apply.
func (s *Service) MarkTodosDone(ctx context.Context, ids []int64) (model.TodosDone, error) {
	if _, err := s.api(); err != nil {
		return model.TodosDone{}, err
	}
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	switch {
	case len(ids) == 0:
		return model.TodosDone{}, gapi.Errf(gapi.ClassInvalid, "ids is empty; list_todos lists the ids")
	case len(ids) > MaxTodos:
		return model.TodosDone{}, gapi.Errf(gapi.ClassInvalid, "at most %d to-do items at a time", MaxTodos)
	case ids[0] <= 0:
		return model.TodosDone{}, gapi.Errf(gapi.ClassInvalid, "a to-do item id is a positive number")
	}
	out := model.TodosDone{Outcome: "none_done", Items: make([]model.TodoDone, 0, len(ids))}
	if gapi.IsDryRun(ctx) {
		for _, id := range ids {
			out.Items = append(out.Items, model.TodoDone{ID: id, Outcome: "would_mark"})
		}
		out.Outcome, out.DryRun = "dry_run", true
		out.WouldSend = preview("POST", fmt.Sprintf("mark %d to-do item(s) done", len(ids)), nil)
		return out, nil
	}
	// Each item answers for itself, so they are marked at once; the
	// client's concurrency bound paces them.
	items := make([]model.TodoDone, len(ids))
	marks := make([]func() error, len(ids))
	for i, id := range ids {
		marks[i] = func() error {
			t, err := s.client.MarkTodoDone(ctx, id)
			switch {
			case gapi.IsClass(err, gapi.ClassNotFound):
				items[i] = model.TodoDone{ID: id, Outcome: "not_found", Error: "no such to-do item of yours"}
			case err != nil:
				items[i] = model.TodoDone{ID: id, Outcome: "failed", Error: gapi.AsError(err).Error()}
			case t.State != "done":
				items[i] = model.TodoDone{ID: id, Outcome: "failed", Error: "GitLab answered with the item still " + t.State}
			default:
				items[i] = model.TodoDone{ID: id, Outcome: "done"}
			}
			return nil
		}
	}
	_ = parallel(marks...) // every mark reports into items
	out.Items = items
	done := 0
	for _, it := range items {
		if it.Outcome == "done" {
			done++
		}
	}
	if done == len(ids) {
		out.Outcome = "done"
	} else if done > 0 {
		out.Outcome = "partly_done"
	}
	return out, nil
}
