package service

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/internal/model"
	"github.com/mmedum/gitlab-mcp/internal/render"
)

// maxMRDiffPages bounds the file diffs read for one merge request: ten
// pages of a hundred, the most files GitLab shows a merge request with.
const maxMRDiffPages = 10

// maxCompareCommits is how many commits one compare_refs result lists.
const maxCompareCommits = 100

// ListMRFiles lists a merge request's changed files with their line
// counts and GitLab's markers, and no diff text (§7.3).
func (s *Service) ListMRFiles(ctx context.Context, raw string, iid int64, opts gapi.ListOptions) (model.MRFiles, error) {
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.MRFiles{}, err
	}
	rows, page, err := s.client.ListMergeRequestDiffs(ctx, p, iid, opts)
	if err != nil {
		return model.MRFiles{}, err
	}
	out := model.MRFiles{Project: ref, IID: iid, Files: make([]model.MRFile, 0, len(rows)), Listing: listing(len(rows), page)}
	for _, d := range rows {
		added, removed := lineCounts(d.Diff)
		out.Files = append(out.Files, model.MRFile{OldPath: d.OldPath, NewPath: d.NewPath, Status: diffStatus(d),
			Additions: added, Deletions: removed, TooLarge: d.TooLarge, Collapsed: d.Collapsed, Generated: d.GeneratedFile,
			Binary: strings.HasPrefix(d.Diff, "Binary files ")})
	}
	return out, nil
}

// lineCounts counts a unified diff's added and removed lines. GitLab's
// per-file diffs start at the first hunk, without file headers.
func lineCounts(diff string) (added, removed int) {
	for line := range strings.SplitSeq(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed
}

// GetMRDiff shows a merge request's diffs under the budget: the named
// files, or every file from fileOffset (§7.3). A named file the merge
// request does not change is refused naming it.
func (s *Service) GetMRDiff(ctx context.Context, raw string, iid int64, paths []string, fileOffset int) (model.MRDiff, error) {
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.MRDiff{}, err
	}
	// Named files stop the reading once every one is found.
	var done func([]gitlab.Diff) bool
	if len(paths) > 0 {
		done = func(read []gitlab.Diff) bool {
			return !slices.ContainsFunc(paths, func(want string) bool { return diffIndex(read, want) < 0 })
		}
	}
	diffs, complete, err := readPagesUntil(maxMRDiffPages, func(opts gapi.ListOptions) ([]gitlab.Diff, gapi.Page, error) {
		return s.client.ListMergeRequestDiffs(ctx, p, iid, opts)
	}, done)
	if err != nil {
		return model.MRDiff{}, err
	}
	if len(paths) > 0 {
		var picked []gitlab.Diff
		for _, want := range paths {
			i := diffIndex(diffs, want)
			if i < 0 {
				return model.MRDiff{}, gapi.Errf(gapi.ClassInvalid,
					"%s is not among the files this merge request changes; list_mr_files lists them", render.Ident(strings.Trim(want, "/")))
			}
			picked = append(picked, diffs[i])
		}
		// The named files are all there is to show, read or not.
		diffs, complete = picked, true
	}
	d, err := budgetDiffs(diffs, complete, fileOffset)
	if err != nil {
		return model.MRDiff{}, err
	}
	return model.MRDiff{Project: ref, IID: iid, Diffs: d}, nil
}

// diffIndex finds a file by its new or old path.
func diffIndex(diffs []gitlab.Diff, path string) int {
	path = strings.Trim(path, "/")
	return slices.IndexFunc(diffs, func(d gitlab.Diff) bool { return d.NewPath == path || d.OldPath == path })
}

// ListMRCommits lists a merge request's commits.
func (s *Service) ListMRCommits(ctx context.Context, raw string, iid int64, opts gapi.ListOptions) (model.MRCommits, error) {
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.MRCommits{}, err
	}
	rows, page, err := s.client.ListMergeRequestCommits(ctx, p, iid, opts)
	if err != nil {
		return model.MRCommits{}, err
	}
	out := model.MRCommits{Project: ref, IID: iid, Commits: make([]model.CommitRow, 0, len(rows)), Listing: listing(len(rows), page)}
	for _, c := range rows {
		out.Commits = append(out.Commits, commitRow(c))
	}
	return out, nil
}

// draftBinding names the query a list_review_comments page token belongs
// to. GitLab returns drafts unpaged; the pages are this server's, cut by
// the budget.
func draftBinding(projectID, iid int64) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "drafts/%d/%d", projectID, iid))
	return hex.EncodeToString(sum[:12])
}

// ListReviewComments lists the signed-in account's unpublished review
// comments on a merge request, under the discussion budget (§7.3). Drafts
// that do not fit are named, and the next page starts with them.
func (s *Service) ListReviewComments(ctx context.Context, raw string, iid int64, pageToken string) (model.DraftNotes, error) {
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.DraftNotes{}, err
	}
	drafts, err := s.client.ListDraftNotes(ctx, p, iid)
	if err != nil {
		return model.DraftNotes{}, err
	}
	// GitLab's order is not stable between reads (live, 2026-09-26), and
	// a page token is an index into it.
	slices.SortFunc(drafts, func(a, b gitlab.DraftNote) int { return cmp.Compare(a.ID, b.ID) })
	// The token names the first draft of the next page by id, not by
	// place, so a draft published or deleted between pages moves nothing.
	start := 0
	if pageToken != "" {
		var from int64
		if err := gapi.DecodeToken(pageToken, draftBinding(p.ID(), iid), &from); err != nil {
			return model.DraftNotes{}, err
		}
		start, _ = slices.BinarySearchFunc(drafts, from, func(d gitlab.DraftNote, id int64) int { return cmp.Compare(d.ID, id) })
	}
	out := model.DraftNotes{Project: ref, IID: iid, Drafts: []model.DraftNote{}, NotShown: []int64{},
		Budget: render.DiscussionBudget}
	used, next := 0, len(drafts)
	for i, d := range drafts[start:] {
		clean, removed := render.Markdown(d.Note, s.self())
		body, b, _ := cut(clean, removed, 0, render.NoteBudget, "", "") // offset 0 is never past the end
		if len(out.Drafts) > 0 && used+b.ShownChars > render.DiscussionBudget {
			next = start + i
			for _, rest := range drafts[next:] {
				out.NotShown = append(out.NotShown, rest.ID)
			}
			break
		}
		used += b.ShownChars
		dn := model.DraftNote{ID: d.ID, ResolveDiscussion: d.ResolveDiscussion, UntrustedBody: body, Budget: b}
		if d.DiscussionID != nil {
			dn.DiscussionID = *d.DiscussionID
		}
		dn.Position = diffPosition(d.Position)
		out.Drafts = append(out.Drafts, dn)
	}
	total := len(drafts)
	out.Listing = model.Listing{Returned: len(out.Drafts), Complete: next >= len(drafts), Total: &total}
	if next < len(drafts) {
		tok := gapi.EncodeToken(draftBinding(p.ID(), iid), drafts[next].ID)
		out.Listing.NextPageToken = &tok
	}
	return out, nil
}

// CompareQuery is compare_refs' query.
type CompareQuery struct {
	Project      string
	From, To     string
	Straight     bool
	CommitOffset int
	FileOffset   int
}

// CompareRefs compares two refs: the commits in to and not in from, and
// the diffs, each under its bound (§7.5). GitLab answers unpaged, so the
// offsets continue within one answer.
func (s *Service) CompareRefs(ctx context.Context, q CompareQuery) (model.Compare, error) {
	p, ref, err := s.project(ctx, q.Project)
	if err != nil {
		return model.Compare{}, err
	}
	if strings.TrimSpace(q.From) == "" || strings.TrimSpace(q.To) == "" {
		return model.Compare{}, gapi.Errf(gapi.ClassInvalid, "from and to are both needed: a branch, a tag or a commit SHA each")
	}
	c, err := s.client.Compare(ctx, p, q.From, q.To, q.Straight)
	if err != nil {
		return model.Compare{}, err
	}
	if q.CommitOffset > len(c.Commits) {
		return model.Compare{}, gapi.Errf(gapi.ClassInvalid, "commit_offset %d is past the %d commits compared", q.CommitOffset,
			len(c.Commits))
	}
	d, err := budgetDiffs(c.Diffs, !c.CompareTimeout, q.FileOffset)
	if err != nil {
		return model.Compare{}, err
	}
	out := model.Compare{Project: ref, From: q.From, To: q.To, Straight: q.Straight, WebURL: c.WebURL, SameRef: c.CompareSameRef,
		Timeout: c.CompareTimeout, Commits: []model.CommitRow{}, CommitsTotal: len(c.Commits), Diffs: d}
	// GitLab lists the commits oldest first (live, 2026-09-26); they are
	// shown newest first, as every other commit listing is.
	n := len(c.Commits)
	end := min(n, q.CommitOffset+maxCompareCommits)
	for i := q.CommitOffset; i < end; i++ {
		out.Commits = append(out.Commits, commitRow(c.Commits[n-1-i]))
	}
	if end < n {
		out.NextCommitOffset = &end
	}
	return out, nil
}

// ListTags lists a project's tags.
func (s *Service) ListTags(ctx context.Context, raw string, q gapi.TagQuery, opts gapi.ListOptions) (model.Tags, error) {
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.Tags{}, err
	}
	rows, page, err := s.client.ListTags(ctx, p, q, opts)
	if err != nil {
		return model.Tags{}, err
	}
	out := model.Tags{Project: ref, Tags: make([]model.Tag, 0, len(rows)), Listing: listing(len(rows), page)}
	for _, t := range rows {
		msg, _ := render.Line(t.Message, render.TitleChars)
		title, _ := render.Line(t.Commit.Title, render.TitleChars)
		out.Tags = append(out.Tags, model.Tag{Name: t.Name, CommitID: t.Commit.ID, CommittedAt: t.Commit.CommittedDate,
			CreatedAt: t.CreatedAt, Protected: t.Protected, Release: t.Release != nil, UntrustedMessage: msg,
			UntrustedCommitTitle: title})
	}
	return out, nil
}
