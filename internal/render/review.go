package render

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mmedum/gitlab-mcp/v2/internal/model"
)

// The readable half of the review and history reads.

func mrItem(iid int64) string { return "!" + strconv.FormatInt(iid, 10) }

// noteableItem is an issue as #12 and a merge request as !12.
func noteableItem(typ string, iid int64) string {
	if typ == "merge_request" {
		return mrItem(iid)
	}
	return "#" + strconv.FormatInt(iid, 10)
}

// MRFiles renders list_mr_files.
func MRFiles(f model.MRFiles, _ Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Files changed by %s in %s\n", mrItem(f.IID), projectLine(f.Project))
	b.WriteString(listingLine("files", f.Listing))
	for _, x := range f.Files {
		fmt.Fprintf(&b, "\n- %s %s: +%d -%d", Ident(x.Status), fileName(x.OldPath, x.NewPath), x.Additions, x.Deletions)
		var marks []string
		for _, m := range []struct {
			on   bool
			name string
		}{{x.TooLarge, "too large, no diff sent"}, {x.Collapsed, "collapsed"}, {x.Binary, "binary"},
			{x.Generated != nil && *x.Generated, "generated"}} {
			if m.on {
				marks = append(marks, m.name)
			}
		}
		if len(marks) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(marks, ", "))
		}
	}
	if len(f.Files) > 0 {
		b.WriteString("\nget_mr_diff shows the diffs of the files named.")
	}
	return b.String()
}

// MRDiff renders get_mr_diff.
func MRDiff(d model.MRDiff, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Diffs of %s in %s", mrItem(d.IID), projectLine(d.Project))
	if len(d.Files) > 0 {
		b.WriteString("\n" + bd.Notice())
	}
	writeDiffs(&b, d.Diffs, d.Project.Path, "@"+mrItem(d.IID),
		"The merge request changes more files than were read; list_mr_files lists them.", bd)
	return b.String()
}

// MRCommits renders list_mr_commits.
func MRCommits(c model.MRCommits, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Commits of %s in %s, newest first\n", mrItem(c.IID), projectLine(c.Project))
	b.WriteString(listingLine("commits", c.Listing))
	writeCommitRows(&b, c.Commits, bd)
	return b.String()
}

// writeCommitRows lists commits, one line each.
func writeCommitRows(b *strings.Builder, rows []model.CommitRow, bd Boundary) {
	if len(rows) > 0 {
		b.WriteString("\n" + bd.Notice())
	}
	for _, c := range rows {
		fmt.Fprintf(b, "\n- %s %s by %s", Ident(c.ID), when(c.CommittedAt), person(c.AuthorName))
		if c.Parents > 1 {
			b.WriteString(", a merge")
		}
		fmt.Fprintf(b, ": %s", bd.Inline(c.UntrustedTitle))
	}
}

// DraftNotes renders list_review_comments.
func DraftNotes(d model.DraftNotes, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Your unpublished review comments on %s in %s\n", mrItem(d.IID), projectLine(d.Project))
	b.WriteString(listingLine("drafts", d.Listing))
	fmt.Fprintf(&b, " Draft text is budgeted at %d characters per page.", d.Budget)
	if len(d.Drafts) > 0 {
		b.WriteString("\n" + bd.Notice())
	}
	for _, n := range d.Drafts {
		fmt.Fprintf(&b, "\n\nDraft %d", n.ID)
		switch {
		case n.DiscussionID != "":
			fmt.Fprintf(&b, ", a reply in thread %s", Ident(n.DiscussionID))
			if n.ResolveDiscussion {
				b.WriteString(" that resolves it when published")
			}
		case n.Position != nil:
			b.WriteString(", a new inline thread")
		default:
			b.WriteString(", a new thread")
		}
		if p := n.Position; p != nil {
			fmt.Fprintf(&b, " on %s", Ident(p.NewPath))
			switch {
			case p.NewLine != nil:
				fmt.Fprintf(&b, " new line %d", *p.NewLine)
			case p.OldLine != nil:
				fmt.Fprintf(&b, " old line %d", *p.OldLine)
			}
		}
		b.WriteString(":\n")
		// Drafts are the signed-in person's own text, and still shown in
		// a boundary: they may quote anyone.
		b.WriteString(bd.Block(Origin{Kind: "draft_comment", Project: d.Project.Path, Item: mrItem(d.IID)}, n.UntrustedBody))
		if n.Budget.ContinueOffset != nil || n.Budget.HiddenRemoved > 0 {
			b.WriteString("\n" + budgetLine(fmt.Sprintf("Draft %d", n.ID), n.Budget))
		}
	}
	if len(d.NotShown) > 0 {
		ids := make([]string, len(d.NotShown))
		for i, id := range d.NotShown {
			ids[i] = strconv.FormatInt(id, 10)
		}
		fmt.Fprintf(&b, "\n\nNot shown for the budget; the next page starts with drafts %s.", strings.Join(ids, ", "))
	}
	return b.String()
}

// Compare renders compare_refs.
func Compare(c model.Compare, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Compare %s...%s in %s\n", Ident(c.From), Ident(c.To), projectLine(c.Project))
	how := "from their merge base, as a merge request compares"
	if c.Straight {
		how = "directly"
	}
	fmt.Fprintf(&b, "%s; compared %s.", Ident(c.WebURL), how)
	compareBody(&b, c, bd)
	return b.String()
}

// compareBody renders what a compare found: the commits and the diffs.
func compareBody(b *strings.Builder, c model.Compare, bd Boundary) {
	if c.SameRef {
		b.WriteString(" from and to are the same commit.")
	}
	if c.Timeout {
		b.WriteString("\nGitLab gave up on this comparison: what it returned is not the whole answer. Compare closer refs.")
	}
	fmt.Fprintf(b, "\n%d commits in %s and not in %s", c.CommitsTotal, Ident(c.To), Ident(c.From))
	if len(c.Commits) > 0 {
		fmt.Fprintf(b, ", newest first; %d shown", len(c.Commits))
	}
	b.WriteString(".")
	writeCommitRows(b, c.Commits, bd)
	if c.NextCommitOffset != nil {
		fmt.Fprintf(b, "\nMore commits: pass commit_offset=%d.", *c.NextCommitOffset)
	}
	writeDiffs(b, c.Diffs, c.Project.Path, "@"+c.To, "GitLab stopped before every changed file was compared.", bd)
}

// MRVersions renders list_mr_versions.
func MRVersions(v model.MRVersions, _ Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Diff versions of %s in %s, newest first\n", mrItem(v.IID), projectLine(v.Project))
	b.WriteString(listingLine("versions", v.Listing))
	for _, x := range v.Versions {
		fmt.Fprintf(&b, "\n- version %d, %s: %s; head %s, base %s", x.ID, when(x.CreatedAt), Ident(x.State), Ident(x.HeadSHA),
			orNone(Ident(deref(x.BaseSHA))))
		if x.ChangesCount != nil {
			fmt.Fprintf(&b, "; files changed %s", Ident(*x.ChangesCount))
		}
	}
	if len(v.Versions) > 1 {
		b.WriteString("\ncompare_mr_versions shows what changed between two versions, or since one.")
	}
	return b.String()
}

// MRVersionChanges renders compare_mr_versions.
func MRVersionChanges(c model.MRVersionChanges, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Changes in %s from version %d to version %d, in %s\n", mrItem(c.IID), c.FromVersion.ID, c.ToVersion.ID,
		projectLine(c.Project))
	fmt.Fprintf(&b, "Compared %s...%s directly, as GitLab's version comparison does; %s.", Ident(c.From), Ident(c.To),
		Ident(c.WebURL))
	if c.BaseMoved {
		b.WriteString("\nThe merge base moved between these versions, as after a rebase: the diff includes what the target " +
			"branch gained in between, not only the author's changes.")
	}
	compareBody(&b, c.Compare, bd)
	return b.String()
}

// Tags renders list_tags.
func Tags(t model.Tags, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Tags of %s\n", projectLine(t.Project))
	b.WriteString(listingLine("tags", t.Listing))
	if len(t.Tags) > 0 {
		b.WriteString("\n" + bd.Notice())
	}
	for _, tag := range t.Tags {
		var flags []string
		if tag.Protected {
			flags = append(flags, "protected")
		}
		if tag.Release {
			flags = append(flags, "has a release")
		}
		if tag.CreatedAt == nil {
			flags = append(flags, "lightweight")
		}
		fmt.Fprintf(&b, "\n- %s", Ident(tag.Name))
		if len(flags) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(flags, ", "))
		}
		fmt.Fprintf(&b, ": commit %s at %s, %s", Ident(shortSHA(tag.CommitID)), when(tag.CommittedAt), bd.Inline(tag.UntrustedCommitTitle))
		if tag.UntrustedMessage != "" {
			fmt.Fprintf(&b, "; tag message %s", bd.Inline(tag.UntrustedMessage))
		}
	}
	return b.String()
}
