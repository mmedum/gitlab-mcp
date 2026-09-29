package service

import (
	"context"
	"strings"

	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/model"
)

// The Destructive kind (§4.3): what GitLab cannot restore. The tools
// that reach here exist only when GITLAB_MCP_ENABLE_DESTRUCTIVE is on,
// and register refuses every call without confirm: true. Each delete
// holds a witness from the caller's read (§4.6) and reads afterwards to
// report what is gone (§4.11).

// BranchDeletion is delete_branch's request.
type BranchDeletion struct {
	Project string
	Branch  string
	// SHA is the head the caller read: a branch pushed to since is not
	// deleted unseen.
	SHA string
	// Unmerged allows a branch whose commits the default branch does not
	// have.
	Unmerged bool
}

// DeleteBranch deletes a branch. It refuses the default branch and every
// protected one, and one GitLab does not count merged unless the caller
// says so.
func (s *Service) DeleteBranch(ctx context.Context, in BranchDeletion) (model.BranchDelete, error) {
	switch {
	case strings.TrimSpace(in.Branch) == "":
		return model.BranchDelete{}, gapi.Errf(gapi.ClassInvalid, "branch is empty")
	case len(strings.TrimSpace(in.SHA)) < 7:
		return model.BranchDelete{}, gapi.Errf(gapi.ClassInvalid, "sha is required: the branch's head commit as list_branches returned it, "+
			"at least its first seven characters, so commits pushed after your read are never deleted unseen")
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.BranchDelete{}, err
	}
	if in.Branch == t.project.DefaultBranch {
		return model.BranchDelete{}, gapi.Errf(gapi.ClassBlocked, "the default branch is never deleted here; nothing was sent")
	}
	b, err := s.client.GetBranch(ctx, t.p, in.Branch)
	if err != nil {
		return model.BranchDelete{}, err
	}
	switch {
	case b.Protected:
		return model.BranchDelete{}, gapi.Errf(gapi.ClassBlocked, "the branch is protected, and a protected branch is never deleted here; "+
			"nothing was sent")
	case !sameHead(b.Commit.ID, in.SHA):
		return model.BranchDelete{}, gapi.Errf(gapi.ClassStale, "the branch moved since it was read: its head is now %s, not %s. "+
			"Look at what was pushed before deleting it", b.Commit.ID, in.SHA)
	case !b.Merged && !in.Unmerged:
		return model.BranchDelete{}, gapi.Errf(gapi.ClassBlocked, "GitLab does not count the branch merged into %s, so deleting it "+
			"may lose commits no other branch has; nothing was sent. compare_refs from %s shows them. Pass unmerged: true to delete it anyway",
			t.project.DefaultBranch, t.project.DefaultBranch)
	}
	out := model.BranchDelete{Outcome: "deleted", Write: model.Write{Target: t.ref}, Branch: in.Branch, SHA: b.Commit.ID, Merged: b.Merged}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun, out.WouldSend = "dry_run", true, preview("DELETE", "delete the branch", nil)
		return out, nil
	}
	err = s.client.DeleteBranch(ctx, t.p, in.Branch)
	_, readErr := s.client.GetBranch(ctx, t.p, in.Branch)
	if out.Notes, err = deleted(err, readErr, "branch"); err != nil {
		return model.BranchDelete{}, err
	}
	return out, nil
}

// sameHead reports whether sha names head: the whole id, or a prefix of
// at least seven characters, as list_branches shows twelve.
func sameHead(head, sha string) bool {
	sha = strings.ToLower(strings.TrimSpace(sha))
	return len(sha) >= 7 && strings.HasPrefix(head, sha)
}

// deleted reads a delete's answer with the read made after it. A delete
// may be repeated after its answer was lost, and the repeat finds
// nothing: a not-found delete of something now gone is reported deleted,
// saying so.
func deleted(deleteErr, readErr error, what string) ([]string, error) {
	switch {
	case deleteErr == nil:
		return confirmGone(readErr, what)
	case gapi.IsClass(deleteErr, gapi.ClassNotFound) && gapi.IsClass(readErr, gapi.ClassNotFound):
		return []string{"GitLab answered the delete with not found, and a read afterwards finds no such " + what +
			": it is gone, whether this call or another deleted it."}, nil
	}
	return nil, deleteErr
}

// confirmGone reads the answer of a read made after a delete: not found
// is gone; a failed read is said, since the delete was accepted; a thing
// still there is a defect to report.
func confirmGone(readErr error, what string) ([]string, error) {
	switch {
	case gapi.IsClass(readErr, gapi.ClassNotFound):
		return nil, nil
	case readErr != nil:
		return []string{"GitLab accepted the delete; reading the " + what + " afterwards failed, so it is not confirmed gone."}, nil //nolint:nilerr // the delete was accepted; a failed read afterwards is said, not a failed call
	}
	return nil, gapi.Errf(gapi.ClassUnexpected, "GitLab accepted the delete, but the %s is still there", what)
}

// CommentDeletion is delete_comment's request.
type CommentDeletion struct {
	Project   string
	Type      string // issue or merge_request
	IID       int64
	NoteID    int64
	UpdatedAt string
}

// DeleteComment deletes one of the signed-in account's own comments.
func (s *Service) DeleteComment(ctx context.Context, in CommentDeletion) (model.CommentDelete, error) {
	witness, err := parseWitness(in.UpdatedAt)
	if err != nil {
		return model.CommentDelete{}, err
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.CommentDelete{}, err
	}
	mr := in.Type == "merge_request"
	note, err := s.ownComment(ctx, t.p, mr, in.IID, in.NoteID, "deletes")
	if err != nil {
		return model.CommentDelete{}, err
	}
	// The read-back goes through one variable; the delete is called by
	// name, so `scripts/gates outcomes` sees that this function writes.
	get := s.client.GetIssueNote
	if mr {
		get = s.client.GetMergeRequestNote
	}
	if err := checkWitness(witness, note.UpdatedAt, "comment"); err != nil {
		return model.CommentDelete{}, err
	}
	out := model.CommentDelete{Outcome: "deleted", Write: model.Write{Target: t.ref}, Type: in.Type, IID: in.IID, NoteID: in.NoteID}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun, out.WouldSend = "dry_run", true, preview("DELETE", "delete the comment", nil)
		return out, nil
	}
	// GitLab refuses the delete with 412 when the comment changed after
	// the time read.
	if mr {
		err = s.client.DeleteMergeRequestNote(ctx, t.p, in.IID, in.NoteID, witness)
	} else {
		err = s.client.DeleteIssueNote(ctx, t.p, in.IID, in.NoteID, witness)
	}
	_, readErr := get(ctx, t.p, in.IID, in.NoteID)
	if out.Notes, err = deleted(err, readErr, "comment"); err != nil {
		return model.CommentDelete{}, err
	}
	return out, nil
}
