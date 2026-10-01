package service

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/mmedum/gitlab-mcp/v2/internal/gapi"
	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/v2/internal/model"
	"github.com/mmedum/gitlab-mcp/v2/internal/render"
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
func (s *Service) GetMRDiff(ctx context.Context, raw string, iid int64, paths []string, fileOffset, diffOffset int) (model.MRDiff, error) {
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.MRDiff{}, err
	}
	if len(paths) == 0 {
		d, err := s.mrDiffsFrom(ctx, p, iid, fileOffset, diffOffset)
		if err != nil {
			return model.MRDiff{}, err
		}
		return model.MRDiff{Project: ref, IID: iid, Diffs: d}, nil
	}
	// Named files stop the reading once every one is found.
	done := func(read []gitlab.Diff) bool {
		return !slices.ContainsFunc(paths, func(want string) bool { return diffIndex(read, want) < 0 })
	}
	diffs, _, err := readPagesUntil(maxMRDiffPages, func(opts gapi.ListOptions) ([]gitlab.Diff, gapi.Page, error) {
		return s.client.ListMergeRequestDiffs(ctx, p, iid, opts)
	}, done)
	if err != nil {
		return model.MRDiff{}, err
	}
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
	d, err := budgetDiffs(picked, true, fileOffset, diffOffset)
	if err != nil {
		return model.MRDiff{}, err
	}
	return model.MRDiff{Project: ref, IID: iid, Diffs: d}, nil
}

// mrDiffsFrom shows a merge request's diffs from the file at fileOffset:
// it reads from the page that holds that file, and on only until the
// budget is spent, so paging through a large merge request reads each
// page about once (§17a).
func (s *Service) mrDiffsFrom(ctx context.Context, p gapi.Project, iid int64, fileOffset, diffOffset int) (model.Diffs, error) {
	if fileOffset < 0 {
		return model.Diffs{}, gapi.Errf(gapi.ClassInvalid, "file_offset is 0 or more")
	}
	first := fileOffset/gapi.MaxPerPage + 1
	if first > maxMRDiffPages {
		return model.Diffs{}, gapi.Errf(gapi.ClassInvalid, "file_offset %d is past the %d files GitLab shows a merge request with",
			fileOffset, maxMRDiffPages*gapi.MaxPerPage)
	}
	base := (first - 1) * gapi.MaxPerPage
	start := fileOffset - base // the first file to show, among those read
	// Pages are read until the budget has cut the files shown, or the
	// listing ends; budgetDiffs decides both.
	var (
		diffs    []gitlab.Diff
		complete bool
		d        model.Diffs
	)
	for page := first; page <= maxMRDiffPages; page++ {
		rows, pg, err := s.client.ListMergeRequestDiffs(ctx, p, iid, gapi.ListOptions{PerPage: gapi.MaxPerPage, Page: page})
		if err != nil {
			return model.Diffs{}, err
		}
		diffs = append(diffs, rows...)
		complete = pg.Complete()
		if start > len(diffs) {
			break
		}
		if d, err = budgetDiffs(diffs, complete, start, diffOffset); err != nil {
			return model.Diffs{}, err
		}
		if complete || d.NextFileOffset != nil {
			break
		}
	}
	if start > len(diffs) {
		return model.Diffs{}, gapi.Errf(gapi.ClassInvalid, "file_offset %d is past the last changed file; list_mr_files counts them", fileOffset)
	}
	if d.NextFileOffset != nil {
		next := *d.NextFileOffset + base
		d.NextFileOffset = &next
	}
	return d, nil
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
	DiffOffset   int
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
	return compared(ref, q, c)
}

// compared bounds one compare's commits and diffs by the query's offsets.
func compared(ref model.ProjectRef, q CompareQuery, c *gitlab.Compare) (model.Compare, error) {
	if q.CommitOffset > len(c.Commits) {
		return model.Compare{}, gapi.Errf(gapi.ClassInvalid, "commit_offset %d is past the %d commits compared", q.CommitOffset,
			len(c.Commits))
	}
	d, err := budgetDiffs(c.Diffs, !c.CompareTimeout, q.FileOffset, q.DiffOffset)
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

// maxVersionPages bounds the versions read to find two of them: ten
// pages of a hundred, newest first.
const maxVersionPages = 10

// ListMRVersions lists a merge request's diff versions, newest first
// (§7.3).
func (s *Service) ListMRVersions(ctx context.Context, raw string, iid int64, opts gapi.ListOptions) (model.MRVersions, error) {
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.MRVersions{}, err
	}
	rows, page, err := s.client.ListMergeRequestVersions(ctx, p, iid, opts)
	if err != nil {
		return model.MRVersions{}, err
	}
	out := model.MRVersions{Project: ref, IID: iid, Versions: make([]model.MRVersion, 0, len(rows)), Listing: listing(len(rows), page)}
	for _, v := range rows {
		out.Versions = append(out.Versions, mrVersion(v))
	}
	return out, nil
}

func mrVersion(v gitlab.MergeRequestVersion) model.MRVersion {
	return model.MRVersion{ID: v.ID, HeadSHA: v.HeadCommitSHA, BaseSHA: nonEmpty(v.BaseCommitSHA),
		StartSHA: nonEmpty(v.StartCommitSHA), CreatedAt: v.CreatedAt, State: v.State, ChangesCount: v.RealSize}
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// MRVersionQuery is compare_mr_versions' query. ToVersion 0 is the
// newest version.
type MRVersionQuery struct {
	Project                  string
	IID                      int64
	FromVersion, ToVersion   int64
	CommitOffset, FileOffset int
	DiffOffset               int
}

// CompareMRVersions shows what changed in a merge request between two of
// its versions (§7.3). GitLab has no route for it; the web page compares
// the repository from the older version's head to the newer's, straight
// rather than from their merge base, so a squash or a rebase between them
// does not hide the change. This does the same, in the target project,
// where GitLab keeps both heads.
func (s *Service) CompareMRVersions(ctx context.Context, q MRVersionQuery) (model.MRVersionChanges, error) {
	if q.FromVersion <= 0 {
		return model.MRVersionChanges{}, gapi.Errf(gapi.ClassInvalid, "from_version is a version id, as list_mr_versions gives it")
	}
	if q.ToVersion < 0 {
		return model.MRVersionChanges{}, gapi.Errf(gapi.ClassInvalid,
			"to_version is a version id, as list_mr_versions gives it, or omitted for the newest")
	}
	p, ref, err := s.project(ctx, q.Project)
	if err != nil {
		return model.MRVersionChanges{}, err
	}
	find := func(rows []gitlab.MergeRequestVersion, id int64) int {
		return slices.IndexFunc(rows, func(v gitlab.MergeRequestVersion) bool { return v.ID == id })
	}
	done := func(rows []gitlab.MergeRequestVersion) bool {
		return find(rows, q.FromVersion) >= 0 && (q.ToVersion == 0 || find(rows, q.ToVersion) >= 0)
	}
	rows, complete, err := readPagesUntil(maxVersionPages, func(o gapi.ListOptions) ([]gitlab.MergeRequestVersion, gapi.Page, error) {
		return s.client.ListMergeRequestVersions(ctx, p, q.IID, o)
	}, done)
	if err != nil {
		return model.MRVersionChanges{}, err
	}
	from, to := find(rows, q.FromVersion), 0
	if q.ToVersion != 0 {
		to = find(rows, q.ToVersion)
	}
	for _, v := range []struct {
		name string
		id   int64
		at   int
	}{{"from_version", q.FromVersion, from}, {"to_version", q.ToVersion, to}} {
		switch {
		case v.at >= 0:
		case complete:
			return model.MRVersionChanges{}, gapi.Errf(gapi.ClassInvalid,
				"%s %d is not a version of this merge request; list_mr_versions lists them", v.name, v.id)
		default:
			return model.MRVersionChanges{}, gapi.Errf(gapi.ClassInvalid,
				"%s %d is not among this merge request's newest %d versions, the most this compares", v.name, v.id, len(rows))
		}
	}
	switch {
	case from == to && q.ToVersion == 0:
		return model.MRVersionChanges{}, gapi.Errf(gapi.ClassInvalid,
			"from_version %d is the newest version: nothing was pushed after it", q.FromVersion)
	case from == to:
		return model.MRVersionChanges{}, gapi.Errf(gapi.ClassInvalid, "from_version and to_version are the same version")
	}
	// Newest first: the older version comes later.
	if from < to {
		return model.MRVersionChanges{}, gapi.Errf(gapi.ClassInvalid,
			"from_version %d is newer than to_version %d; from_version is the older one", q.FromVersion, rows[to].ID)
	}
	older, newer := rows[from], rows[to]
	for _, v := range []gitlab.MergeRequestVersion{older, newer} {
		if v.HeadCommitSHA == "" {
			return model.MRVersionChanges{}, gapi.Errf(gapi.ClassInvalid, "version %d has no commits to compare", v.ID)
		}
	}
	cq := CompareQuery{From: older.HeadCommitSHA, To: newer.HeadCommitSHA, Straight: true, CommitOffset: q.CommitOffset,
		FileOffset: q.FileOffset, DiffOffset: q.DiffOffset}
	c, err := s.client.Compare(ctx, p, cq.From, cq.To, true)
	if err != nil {
		return model.MRVersionChanges{}, err
	}
	changes, err := compared(ref, cq, c)
	if err != nil {
		return model.MRVersionChanges{}, err
	}
	out := model.MRVersionChanges{IID: q.IID, FromVersion: mrVersion(older), ToVersion: mrVersion(newer), Compare: changes}
	if older.BaseCommitSHA != "" && newer.BaseCommitSHA != "" {
		moved := older.BaseCommitSHA != newer.BaseCommitSHA
		out.BaseMoved = &moved
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
