package service

import (
	"context"
	"encoding/base64"
	"strings"
	"unicode/utf8"

	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/internal/model"
	"github.com/mmedum/gitlab-mcp/internal/render"
)

// maxDiffPages bounds the file diffs read for one commit.
const maxDiffPages = 3

// GetFile reads a file at a ref (§7.5). Text is shown inside a boundary
// under the budget; binary content is described, never shown.
func (s *Service) GetFile(ctx context.Context, raw, path, ref string, offset int) (model.File, error) {
	p, pref, err := s.project(ctx, raw)
	if err != nil {
		return model.File{}, err
	}
	path = strings.Trim(path, "/")
	if path == "" {
		return model.File{}, gapi.Errf(gapi.ClassInvalid, "path is empty: pass a file path relative to the repository root")
	}
	f, err := s.client.GetFile(ctx, p, path, ref)
	if err != nil {
		return model.File{}, err
	}
	content := []byte(f.Content)
	if f.Encoding == "base64" {
		if content, err = base64.StdEncoding.DecodeString(f.Content); err != nil {
			return model.File{}, gapi.Errf(gapi.ClassUnexpected, "GitLab's file content was not valid base64")
		}
	}
	out := model.File{Project: pref, Path: f.FilePath, Ref: f.Ref, Size: f.Size, BlobID: f.BlobID,
		LastCommitID: f.LastCommitID, CommitID: f.CommitID, SHA256: f.ContentSHA256, ContentType: render.ContentType(content)}
	if out.UntrustedContent, out.Binary, out.Budget, err = fileContent(content, offset); err != nil {
		return model.File{}, err
	}
	return out, nil
}

// fileContent is a file's text from offset under the file budget, with
// hidden characters made visible, or, for binary content, nothing and a
// flag that says why.
func fileContent(content []byte, offset int) (string, bool, model.Budget, error) {
	if render.IsBinary(content) {
		return "", true, model.Budget{BudgetChars: render.FileBudget}, nil
	}
	text, visible := render.Code(string(content))
	shown, budget, err := cut(text, visible, offset, render.FileBudget, "the file", "offset")
	return shown, false, budget, err
}

// ListTree lists a directory.
func (s *Service) ListTree(ctx context.Context, raw string, q gapi.TreeQuery, opts gapi.ListOptions) (model.Tree, error) {
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.Tree{}, err
	}
	q.Path = strings.Trim(q.Path, "/")
	rows, page, err := s.client.ListTree(ctx, p, q, opts)
	if err != nil {
		return model.Tree{}, err
	}
	out := model.Tree{Project: ref, Ref: q.Ref, Path: q.Path, Entries: make([]model.TreeEntry, 0, len(rows)),
		Listing: listing(len(rows), page)}
	for _, e := range rows {
		out.Entries = append(out.Entries, model.TreeEntry{Path: e.Path, Type: e.Type, Mode: e.Mode, ID: e.ID})
	}
	return out, nil
}

// ListBranches lists branches.
func (s *Service) ListBranches(ctx context.Context, raw, search string, opts gapi.ListOptions) (model.Branches, error) {
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.Branches{}, err
	}
	rows, page, err := s.client.ListBranches(ctx, p, search, opts)
	if err != nil {
		return model.Branches{}, err
	}
	out := model.Branches{Project: ref, Branches: make([]model.Branch, 0, len(rows)), Listing: listing(len(rows), page)}
	for _, b := range rows {
		title, _ := render.Line(b.Commit.Title, render.TitleChars)
		out.Branches = append(out.Branches, model.Branch{Name: b.Name, Default: b.Default, Protected: b.Protected,
			Merged: b.Merged, CanPush: b.CanPush, CommitID: b.Commit.ID, CommittedAt: b.Commit.CommittedDate,
			UntrustedCommitTitle: title})
	}
	return out, nil
}

// ListCommits lists commits reachable from a ref.
func (s *Service) ListCommits(ctx context.Context, raw string, q gapi.CommitQuery, opts gapi.ListOptions) (model.Commits, error) {
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.Commits{}, err
	}
	rows, page, err := s.client.ListCommits(ctx, p, q, opts)
	if err != nil {
		return model.Commits{}, err
	}
	out := model.Commits{Project: ref, Ref: q.Ref, Commits: make([]model.CommitRow, 0, len(rows)), Listing: listing(len(rows), page)}
	for _, c := range rows {
		out.Commits = append(out.Commits, commitRow(c))
	}
	return out, nil
}

// commitRow is a commit as a listing shows it.
func commitRow(c gitlab.Commit) model.CommitRow {
	title, _ := render.Line(c.Title, render.TitleChars)
	return model.CommitRow{ID: c.ID, ShortID: c.ShortID, AuthorName: c.AuthorName, AuthoredAt: c.AuthoredDate,
		CommittedAt: c.CommittedDate, Parents: len(c.ParentIDs), UntrustedTitle: title}
}

// GetCommit reads a commit and its diffs under the budget, starting at
// the fileOffset-th changed file. A diff that does not fit is named, and
// file_offset continues from it. The message is shown from
// messageOffset, and a cut one says where to continue.
func (s *Service) GetCommit(ctx context.Context, raw, sha string, fileOffset, diffOffset, messageOffset int) (model.Commit, error) {
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.Commit{}, err
	}
	c, err := s.client.GetCommit(ctx, p, sha)
	if err != nil {
		return model.Commit{}, err
	}
	diffs, complete, err := s.commitDiffs(ctx, p, c.ID)
	if err != nil {
		return model.Commit{}, err
	}
	d, err := budgetDiffs(diffs, complete, fileOffset, diffOffset)
	if err != nil {
		return model.Commit{}, err
	}
	msg, hiddenMsg := render.Code(c.Message)
	msg, msgBudget, err := cut(msg, hiddenMsg, messageOffset, render.CommitMessageBudget, "the message", "message_offset")
	if err != nil {
		return model.Commit{}, err
	}
	// The message's hidden characters are counted in its budget, and the
	// diffs' in HiddenRemoved: each once.
	out := model.Commit{Project: ref, ID: c.ID, ShortID: c.ShortID, WebURL: c.WebURL, AuthorName: c.AuthorName,
		AuthoredAt: c.AuthoredDate, CommitterName: c.CommitterName, CommittedAt: c.CommittedDate,
		ParentIDs: nonNil(c.ParentIDs), UntrustedMessage: msg, MessageBudget: msgBudget, Files: d.Files,
		NotShown: d.NotShown, FilesComplete: d.FilesComplete, NextFileOffset: d.NextFileOffset, DiffBudget: d.DiffBudget,
		HiddenRemoved: d.HiddenRemoved}
	if c.Stats != nil {
		out.Additions, out.Deletions = c.Stats.Additions, c.Stats.Deletions
	}
	return out, nil
}

// budgetDiffs shows the diffs from the fileOffset-th under the diff
// budget (§4.8). A diff that does not fit is named, and file_offset
// continues from it; a diff GitLab itself left out is named with GitLab's
// reason rather than shown as empty. complete says whether diffs is every
// changed file.
func budgetDiffs(diffs []gitlab.Diff, complete bool, fileOffset, diffOffset int) (model.Diffs, error) {
	if fileOffset > len(diffs) {
		return model.Diffs{}, gapi.Errf(gapi.ClassInvalid, "file_offset %d is past the %d changed files read", fileOffset, len(diffs))
	}
	if diffOffset < 0 || (diffOffset > 0 && fileOffset == len(diffs)) {
		return model.Diffs{}, gapi.Errf(gapi.ClassInvalid, "diff_offset continues the diff of the file at file_offset: "+
			"pass both as a truncated result gave them")
	}
	out := model.Diffs{Files: []model.FileDiff{}, NotShown: []model.FileChange{}, FilesComplete: complete,
		DiffBudget: render.DiffBudget}
	used := 0
	for i, d := range diffs[fileOffset:] {
		status := diffStatus(d)
		change := model.FileChange{OldPath: d.OldPath, NewPath: d.NewPath, Status: status}
		if i == 0 && diffOffset > 0 && (d.TooLarge || d.Collapsed) {
			return model.Diffs{}, gapi.Errf(gapi.ClassInvalid, "the file at file_offset %d has no diff to continue: GitLab left it out", fileOffset)
		}
		switch {
		case d.TooLarge:
			change.Reason = "too_large"
			out.NotShown = append(out.NotShown, change)
			continue
		case d.Collapsed:
			change.Reason = "collapsed"
			out.NotShown = append(out.NotShown, change)
			continue
		}
		// Once the budget is spent, the rest are named without preparing
		// their text.
		if out.NextFileOffset != nil {
			change.Reason = "budget"
			out.NotShown = append(out.NotShown, change)
			continue
		}
		text, visible := render.Code(d.Diff)
		fd := model.FileDiff{OldPath: d.OldPath, NewPath: d.NewPath, Status: status}
		if i == 0 && diffOffset > 0 {
			// A diff continued: from where the last result cut it.
			total := utf8.RuneCountInString(text)
			if diffOffset > total {
				return model.Diffs{}, gapi.Errf(gapi.ClassInvalid, "diff_offset %d is past the end of this file's diff, which has %d characters",
					diffOffset, total)
			}
			text, _ = render.Cut(text, diffOffset, total)
			fd.DiffOffset = diffOffset
		}
		size := utf8.RuneCountInString(text)
		if used+size > render.DiffBudget && len(out.Files) > 0 {
			next := fileOffset + i
			out.NextFileOffset = &next
			change.Reason = "budget"
			out.NotShown = append(out.NotShown, change)
			continue
		}
		if size > render.DiffBudget {
			// The first diff alone is over the budget: it is cut, and
			// diff_offset reads on from the cut.
			var b model.Budget
			text, b = render.Cut(text, 0, render.DiffBudget)
			fd.Truncated = true
			if b.ContinueOffset != nil {
				next := fd.DiffOffset + *b.ContinueOffset
				fd.ContinueDiffOffset = &next
			}
		}
		fd.UntrustedDiff = text
		if fd.DiffOffset > 0 || fd.Truncated {
			// Only part is shown: count what render.Code marked in it.
			visible = strings.Count(text, "<U+")
		}
		out.HiddenRemoved += visible
		used += size
		out.Files = append(out.Files, fd)
	}
	return out, nil
}

// commitDiffs reads a commit's per-file diffs, up to maxDiffPages.
func (s *Service) commitDiffs(ctx context.Context, p gapi.Project, sha string) ([]gitlab.Diff, bool, error) {
	return readPages(maxDiffPages, func(opts gapi.ListOptions) ([]gitlab.Diff, gapi.Page, error) {
		return s.client.GetCommitDiff(ctx, p, sha, opts)
	})
}

func diffStatus(d gitlab.Diff) string {
	switch {
	case d.NewFile:
		return "added"
	case d.DeletedFile:
		return "deleted"
	case d.RenamedFile:
		return "renamed"
	}
	return "modified"
}
