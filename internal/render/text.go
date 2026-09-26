package render

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/model"
)

// The readable half of each result. It carries the same facts as the
// structured half in a different shape — never the same bytes twice
// (CLAUDE.md rule 15) — and every piece someone else wrote goes through
// a Boundary.

// when renders an instant as RFC 3339 in UTC (§6.4).
func when(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func whenPtr(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return when(*t)
}

func users(us []model.User) string {
	names := make([]string, len(us))
	for i, u := range us {
		names[i] = u.Username
	}
	return atList(names, "none")
}

// atList lists usernames with their @, or says empty when there are
// none.
func atList(names []string, empty string) string {
	if len(names) == 0 {
		return empty
	}
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = "@" + Ident(n)
	}
	return strings.Join(out, ", ")
}

func list(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	return strings.Join(xs, ", ")
}

// idents lists names someone else chose, such as labels, each through
// Ident.
func idents(xs []string) string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = Ident(x)
	}
	return list(out)
}

// person is a display name someone else chose, such as a commit author,
// on one line.
func person(s string) string {
	v, _ := Line(s, 200)
	return v
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// projectLine names a project by path and id, and says when it moved.
func projectLine(p model.ProjectRef) string {
	s := Ident(p.Path) + " (project id " + strconv.FormatInt(p.ID, 10) + ")"
	if p.MovedFrom != nil {
		s += "\nNote: " + Ident(*p.MovedFrom) + " has moved to " + Ident(p.Path) + "; use the new path or the id from now on."
	}
	return s
}

// listingLine says how much of a listing this is (§4.8, §7.1).
func listingLine(noun string, l model.Listing) string {
	s := fmt.Sprintf("%d %s shown", l.Returned, noun)
	switch {
	case l.Complete && l.Total != nil:
		s += fmt.Sprintf("; the listing is complete (total %d).", *l.Total)
	case l.Complete:
		s += "; the listing is complete."
	case l.NextPageToken == nil:
		s += "; GitLab has more than this server reads in one call, and they are not shown."
	case l.Total != nil:
		s += fmt.Sprintf(" of %d; more: pass page_token=%q.", *l.Total, *l.NextPageToken)
	default:
		s += fmt.Sprintf("; the total is unknown; more: pass page_token=%q.", *l.NextPageToken)
	}
	return s
}

// budgetLine says what part of a text was shown.
func budgetLine(what string, b model.Budget) string { return budgetLineFor(what, "offset", b) }

// budgetLineFor is budgetLine for a text continued by the input param.
func budgetLineFor(what, param string, b model.Budget) string {
	var s string
	switch {
	case b.TotalChars == 0:
		s = what + " is empty."
	case b.ContinueOffset == nil && b.Offset == 0:
		s = fmt.Sprintf("%s: all %d characters shown (budget %d).", what, b.TotalChars, b.BudgetChars)
	case b.ContinueOffset == nil:
		s = fmt.Sprintf("%s: characters %d to %d of %d shown, to the end (budget %d).",
			what, b.Offset, b.Offset+b.ShownChars, b.TotalChars, b.BudgetChars)
	default:
		s = fmt.Sprintf("%s: characters %d to %d of %d shown (budget %d); continue with %s=%d.",
			what, b.Offset, b.Offset+b.ShownChars, b.TotalChars, b.BudgetChars, param, *b.ContinueOffset)
	}
	if b.HiddenRemoved > 0 {
		s += fmt.Sprintf(" %d hidden characters were removed or made visible.", b.HiddenRemoved)
	}
	return s
}

// --------------------------------------------------------------- get_me

// Me renders get_me. It shows nothing that needs a boundary.
func Me(m model.Me, _ Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Signed in as @%s (%s), user id %d", Ident(m.User.Username), person(m.User.Name), m.User.ID)
	if m.User.Bot {
		b.WriteString(", a bot account")
	}
	if m.User.Admin {
		b.WriteString(", an instance administrator")
	}
	b.WriteString(".\n")
	if m.Instance.Known {
		fmt.Fprintf(&b, "Instance: %s, GitLab %s, %s Edition.\n", m.Instance.URL, Ident(m.Instance.Version), m.Instance.Edition)
	} else {
		fmt.Fprintf(&b, "Instance: %s; its version and edition could not be read.\n", m.Instance.URL)
	}
	fmt.Fprintf(&b, "Token: %s, scopes %s", m.Token.Kind, idents(m.Token.Scopes))
	if m.Token.ExpiresAt != nil {
		fmt.Fprintf(&b, ", expires %s and is refreshed before then", when(*m.Token.ExpiresAt))
	}
	b.WriteString(".\n")
	fmt.Fprintf(&b, "Registered: %d tools; kinds %s; optional toolsets %s; read-only %s.\n",
		m.Registered.Tools, list(m.Registered.Kinds), list(m.Registered.Toolsets), yesNo(m.Registered.ReadOnly))
	if m.WriteScope.Confined {
		fmt.Fprintf(&b, "Writes are confined to %s.\n", list(m.WriteScope.Namespaces))
	} else {
		b.WriteString("Writes are not confined: they may go wherever the token can write.\n")
	}
	if m.Rate.Known {
		fmt.Fprintf(&b, "Rate limit: %d of %d requests left, resets %s (read %s).",
			m.Rate.Remaining, m.Rate.Limit, whenPtr(m.Rate.Reset), whenPtr(m.Rate.Observed))
	} else {
		b.WriteString("Rate limit: GitLab has not reported one yet.")
	}
	for _, n := range m.Notes {
		b.WriteString("\nNote: " + n)
	}
	return b.String()
}

// ---------------------------------------------------------- resolve_url

// Resolved renders resolve_url. It shows nothing that needs a boundary.
func Resolved(r model.Resolved, _ Boundary) string {
	var b strings.Builder
	kind := strings.ReplaceAll(r.Kind, "_", " ")
	fmt.Fprintf(&b, "%s %s in %s", article(kind), kind, Ident(r.Project))
	switch {
	case r.IID != nil:
		fmt.Fprintf(&b, ", iid %d", *r.IID)
	case r.ID != nil:
		fmt.Fprintf(&b, ", id %d", *r.ID)
	case r.SHA != "":
		fmt.Fprintf(&b, ", commit %s", Ident(r.SHA))
	case r.From != "":
		fmt.Fprintf(&b, ", from %s to %s", Ident(r.From), Ident(r.To))
	case r.Slug != "":
		fmt.Fprintf(&b, ", page %s", Ident(r.Slug))
	}
	if r.Ref != "" {
		fmt.Fprintf(&b, ", ref %s", Ident(r.Ref))
	}
	if r.Path != "" {
		fmt.Fprintf(&b, ", path %s", Ident(r.Path))
	}
	if r.Line != nil {
		fmt.Fprintf(&b, ", line %d", *r.Line)
		if r.EndLine != nil {
			fmt.Fprintf(&b, " to %d", *r.EndLine)
		}
	}
	if r.Note != nil {
		fmt.Fprintf(&b, ", comment %d", *r.Note)
	}
	b.WriteString(".")
	if len(r.RefCandidates) > 0 {
		b.WriteString("\nThe ref could not be told from the path; other ways to split them:")
		for _, c := range r.RefCandidates {
			fmt.Fprintf(&b, "\n- ref %s, path %s", Ident(c.Ref), orNone(Ident(c.Path)))
		}
	}
	if r.Tool == "" {
		b.WriteString("\nNo tool registered here reads this kind.")
		return b.String()
	}
	fmt.Fprintf(&b, "\nRead it with %s:", r.Tool)
	for _, k := range slices.Sorted(maps.Keys(r.Arguments)) {
		fmt.Fprintf(&b, " %s=%v", k, quoteIfString(r.Arguments[k]))
	}
	return b.String()
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func quoteIfString(v any) any {
	if s, ok := v.(string); ok {
		return strconv.Quote(s)
	}
	return v
}

// ------------------------------------------------------------- projects

// ProjectList renders search_projects.
func ProjectList(l model.ProjectList, bd Boundary) string {
	var b strings.Builder
	b.WriteString(listingLine("projects", l.Listing))
	if len(l.Projects) > 0 {
		b.WriteString("\n" + bd.Notice())
	}
	for _, p := range l.Projects {
		fmt.Fprintf(&b, "\n- %d %s: %s, default branch %s", p.ID, Ident(p.Path), Ident(p.Visibility), orNone(Ident(p.DefaultBranch)))
		if p.Archived {
			b.WriteString(", archived")
		}
		if p.LastActivityAt != nil {
			fmt.Fprintf(&b, ", active %s", when(*p.LastActivityAt))
		}
		fmt.Fprintf(&b, "; name %s", bd.Inline(p.UntrustedName))
	}
	return b.String()
}

// Project renders get_project.
func Project(p model.Project, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Project %s\n", projectLine(p.Project))
	fmt.Fprintf(&b, "%s, %s; default branch %s; archived %s; empty repository %s.\n",
		Ident(p.WebURL), Ident(p.Visibility), orNone(Ident(p.DefaultBranch)), yesNo(p.Archived), yesNo(p.EmptyRepo))
	fmt.Fprintf(&b, "Under %s (%s). Created %s, last activity %s.\n", Ident(p.Namespace), Ident(p.NamespaceKind), when(p.CreatedAt),
		whenPtr(p.LastActivityAt))
	fmt.Fprintf(&b, "Stars %d, forks %d", p.Stars, p.Forks)
	if p.OpenIssues != nil {
		fmt.Fprintf(&b, ", open issues %d", *p.OpenIssues)
	}
	fmt.Fprintf(&b, "; topics %s.\n", idents(p.Topics))
	b.WriteString(bd.Notice() + "\n")
	fmt.Fprintf(&b, "Name: %s\n", bd.Inline(p.UntrustedName))
	o := Origin{Kind: "project_description", Project: p.Project.Path}
	b.WriteString(bd.Block(o, p.UntrustedDescription))
	return b.String()
}

// ------------------------------------------------------ issues and MRs

// ItemList renders search_issues and search_merge_requests. noun is
// "issues" or "merge requests".
func ItemList(l model.ItemList, noun string, bd Boundary) string {
	var b strings.Builder
	b.WriteString(listingLine(noun, l.Listing))
	if len(l.Items) > 0 {
		b.WriteString("\n" + bd.Notice())
	}
	for _, it := range l.Items {
		state := Ident(it.State)
		if it.Draft {
			state += ", draft"
		}
		fmt.Fprintf(&b, "\n- %s (project id %d, iid %d): %s, by @%s, updated %s", Ident(it.Reference), it.Project.ID, it.IID,
			state, Ident(it.Author), when(it.UpdatedAt))
		if len(it.Labels) > 0 {
			fmt.Fprintf(&b, ", labels %s", idents(it.Labels))
		}
		fmt.Fprintf(&b, "; title %s", bd.Inline(it.UntrustedTitle))
	}
	return b.String()
}

func discussionLine(d model.DiscussionSummary) string {
	if !d.Known {
		return "Discussions: could not be read; list_discussions reads them."
	}
	s := fmt.Sprintf("Discussions: %d threads, %d unresolved, last activity %s", d.Threads, d.Unresolved, whenPtr(d.LastActivity))
	if !d.Complete {
		s += " (counted over the first threads only; there are more)"
	}
	return s + "; list_discussions reads them."
}

func milestone(m *model.Milestone) string {
	if m == nil {
		return "none"
	}
	return fmt.Sprintf("%q (%s)", m.Title, Ident(m.State))
}

// Issue renders get_issue.
func Issue(is model.Issue, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Issue %s, iid %d, in %s\n", Ident(is.Reference), is.IID, projectLine(is.Project))
	fmt.Fprintf(&b, "%s; %s; type %s; confidential %s.\n", Ident(is.WebURL), Ident(is.State), Ident(is.Type), yesNo(is.Confidential))
	fmt.Fprintf(&b, "Author @%s; assignees %s; labels %s; milestone %s.\n", Ident(is.Author.Username), users(is.Assignees),
		idents(is.Labels), milestone(is.Milestone))
	fmt.Fprintf(&b, "Created %s; updated %s", when(is.CreatedAt), when(is.UpdatedAt))
	if is.ClosedAt != nil {
		fmt.Fprintf(&b, "; closed %s", when(*is.ClosedAt))
		if is.ClosedBy != nil {
			fmt.Fprintf(&b, " by @%s", Ident(is.ClosedBy.Username))
		}
	}
	if is.DueDate != nil {
		fmt.Fprintf(&b, "; due %s", Ident(*is.DueDate))
	}
	b.WriteString(".\n")
	if is.Tasks != nil && is.Tasks.Count > 0 {
		fmt.Fprintf(&b, "Tasks: %d of %d done.\n", is.Tasks.Completed, is.Tasks.Count)
	}
	b.WriteString(discussionLine(is.Discussions) + "\n")
	b.WriteString(bd.Notice() + "\n")
	fmt.Fprintf(&b, "Title: %s\n", bd.Inline(is.UntrustedTitle))
	o := Origin{Kind: "issue_description", Project: is.Project.Path, Item: "#" + strconv.FormatInt(is.IID, 10), Author: is.Author.Username}
	b.WriteString(bd.Block(o, is.UntrustedDescription) + "\n")
	b.WriteString(budgetLine("Description", is.DescriptionBudget))
	return b.String()
}

// MergeRequest renders get_merge_request.
func MergeRequest(mr model.MergeRequest, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Merge request %s, iid %d, in %s\n", Ident(mr.Reference), mr.IID, projectLine(mr.Project))
	state := Ident(mr.State)
	if mr.Draft {
		state += ", draft"
	}
	fmt.Fprintf(&b, "%s; %s; %s into %s", Ident(mr.WebURL), state, Ident(mr.SourceBranch), Ident(mr.TargetBranch))
	if mr.SourceProjectID != mr.Project.ID {
		fmt.Fprintf(&b, " (from the fork with project id %d)", mr.SourceProjectID)
	}
	b.WriteString(".\n")
	fmt.Fprintf(&b, "Head %s; merge status %s; conflicts %s; files changed %s.\n", Ident(mr.SHA), Ident(mr.DetailedMergeStatus),
		yesNo(mr.HasConflicts), Ident(mr.ChangesCount))
	if d := mr.DiffRefs; d != nil {
		fmt.Fprintf(&b, "Diff refs: base %s, start %s, head %s.\n", Ident(d.BaseSHA), Ident(d.StartSHA), Ident(d.HeadSHA))
	}
	if mr.HeadPipeline != nil {
		fmt.Fprintf(&b, "Head pipeline %d: %s.\n", mr.HeadPipeline.ID, Ident(mr.HeadPipeline.Status))
	} else {
		b.WriteString("No head pipeline.\n")
	}
	if a := mr.Approvals; a == nil {
		b.WriteString("Approvals: could not be read.\n")
	} else {
		fmt.Fprintf(&b, "Approvals: approved %s; approved by %s", yesNo(a.Approved), atList(a.ApprovedBy, "nobody"))
		if a.Required != nil && a.Left != nil {
			fmt.Fprintf(&b, "; %d required, %d left", *a.Required, *a.Left)
		}
		b.WriteString(".\n")
	}
	fmt.Fprintf(&b, "Author @%s; assignees %s; reviewers %s; labels %s; milestone %s.\n", Ident(mr.Author.Username),
		users(mr.Assignees), users(mr.Reviewers), idents(mr.Labels), milestone(mr.Milestone))
	fmt.Fprintf(&b, "Created %s; updated %s", when(mr.CreatedAt), when(mr.UpdatedAt))
	if mr.MergedAt != nil {
		fmt.Fprintf(&b, "; merged %s", when(*mr.MergedAt))
		if mr.MergedBy != nil {
			fmt.Fprintf(&b, " by @%s", Ident(mr.MergedBy.Username))
		}
	}
	if mr.ClosedAt != nil {
		fmt.Fprintf(&b, "; closed %s", when(*mr.ClosedAt))
	}
	b.WriteString(".\n")
	b.WriteString(discussionLine(mr.Discussions) + "\n")
	b.WriteString(bd.Notice() + "\n")
	fmt.Fprintf(&b, "Title: %s\n", bd.Inline(mr.UntrustedTitle))
	o := Origin{Kind: "merge_request_description", Project: mr.Project.Path, Item: mrItem(mr.IID), Author: mr.Author.Username}
	b.WriteString(bd.Block(o, mr.UntrustedDescription) + "\n")
	b.WriteString(budgetLine("Description", mr.DescriptionBudget))
	return b.String()
}

// ---------------------------------------------------------- discussions

// Discussions renders list_discussions.
func Discussions(d model.Discussions, bd Boundary) string {
	var b strings.Builder
	sigil := "#"
	if d.Type == "merge_request" {
		sigil = "!"
	}
	item := sigil + strconv.FormatInt(d.IID, 10)
	fmt.Fprintf(&b, "Discussions on %s in %s, newest activity first.\n", item, projectLine(d.Project))
	b.WriteString(listingLine("threads", d.Listing))
	fmt.Fprintf(&b, " Comment text is budgeted at %d characters per page.", d.Budget)
	if len(d.Threads) > 0 {
		b.WriteString("\n" + bd.Notice())
	}
	for _, t := range d.Threads {
		fmt.Fprintf(&b, "\n\nThread %s", Ident(t.ID))
		switch {
		case t.Individual:
			b.WriteString(" (a single comment)")
		case t.Resolvable && t.Resolved:
			b.WriteString(" (resolved)")
		case t.Resolvable:
			b.WriteString(" (unresolved)")
		}
		if p := t.Position; p != nil {
			fmt.Fprintf(&b, " on %s", Ident(p.NewPath))
			switch {
			case p.NewLine != nil:
				fmt.Fprintf(&b, " new line %d", *p.NewLine)
			case p.OldLine != nil:
				fmt.Fprintf(&b, " old line %d", *p.OldLine)
			}
			fmt.Fprintf(&b, " at %s", Ident(p.HeadSHA))
		}
		fmt.Fprintf(&b, ", last activity %s:", when(t.LastActivity))
		for _, n := range t.Notes {
			fmt.Fprintf(&b, "\nComment %d by @%s at %s", n.ID, Ident(n.Author.Username), when(n.CreatedAt))
			if n.System {
				b.WriteString(", a system note")
			}
			if n.Internal {
				b.WriteString(", internal")
			}
			b.WriteString(":\n")
			b.WriteString(bd.Block(Origin{Kind: "comment", Project: d.Project.Path, Item: item, Author: n.Author.Username}, n.UntrustedBody))
			// A comment shown whole and clean needs no budget line.
			if n.Budget.ContinueOffset != nil || n.Budget.Offset > 0 || n.Budget.HiddenRemoved > 0 {
				b.WriteString("\n" + budgetLineFor(fmt.Sprintf("Comment %d", n.ID), fmt.Sprintf("note_id=%d offset", n.ID), n.Budget))
			}
		}
	}
	if len(d.NotShown) > 0 {
		b.WriteString("\n\nNot shown for the budget; the next page starts with these:")
		for _, t := range d.NotShown {
			fmt.Fprintf(&b, "\n- thread %s, started by @%s, %d comments, last activity %s", Ident(t.ID), Ident(t.Author), t.Notes,
				when(t.LastActivity))
		}
	}
	return b.String()
}

// ----------------------------------------------------------- repository

// File renders get_file.
func File(f model.File, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "File %s at %s in %s\n", Ident(f.Path), Ident(f.Ref), projectLine(f.Project))
	fmt.Fprintf(&b, "%d bytes; blob %s; last commit %s; ref resolved to %s; sha256 %s.\n", f.Size, Ident(f.BlobID),
		Ident(f.LastCommitID), Ident(f.CommitID), Ident(f.SHA256))
	if f.Binary {
		fmt.Fprintf(&b, "Binary content (%s): not shown.", Ident(f.ContentType))
		return b.String()
	}
	b.WriteString(bd.Notice() + "\n")
	b.WriteString(bd.Block(Origin{Kind: "file", Project: f.Project.Path, Item: f.Path + "@" + f.Ref}, f.UntrustedContent) + "\n")
	b.WriteString(budgetLine("Content", f.Budget))
	return b.String()
}

// Tree renders list_tree.
func Tree(t model.Tree, _ Boundary) string {
	var b strings.Builder
	where := Ident(t.Path)
	if where == "" {
		where = "the root"
	}
	fmt.Fprintf(&b, "Tree of %s at %s in %s\n", where, orDefault(t.Ref), projectLine(t.Project))
	b.WriteString(listingLine("entries", t.Listing))
	for _, e := range t.Entries {
		kind := map[string]string{"tree": "dir", "blob": "file", "commit": "submodule"}[e.Type]
		if kind == "" {
			kind = Ident(e.Type)
		}
		fmt.Fprintf(&b, "\n%-9s %s", kind, Ident(e.Path))
	}
	return b.String()
}

func orDefault(ref string) string {
	if ref == "" {
		return "the default branch"
	}
	return Ident(ref)
}

// Branches renders list_branches.
func Branches(l model.Branches, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Branches of %s\n", projectLine(l.Project))
	b.WriteString(listingLine("branches", l.Listing))
	if len(l.Branches) > 0 {
		b.WriteString("\n" + bd.Notice())
	}
	for _, br := range l.Branches {
		var flags []string
		for _, f := range []struct {
			on   bool
			name string
		}{{br.Default, "default"}, {br.Protected, "protected"}, {br.Merged, "merged"}, {!br.CanPush, "you cannot push"}} {
			if f.on {
				flags = append(flags, f.name)
			}
		}
		fmt.Fprintf(&b, "\n- %s", Ident(br.Name))
		if len(flags) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(flags, ", "))
		}
		fmt.Fprintf(&b, ": head %s at %s, %s", Ident(shortSHA(br.CommitID)), when(br.CommittedAt), bd.Inline(br.UntrustedCommitTitle))
	}
	return b.String()
}

func shortSHA(s string) string { return s[:min(len(s), 12)] }

// Commits renders list_commits.
func Commits(l model.Commits, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Commits on %s in %s\n", orDefault(l.Ref), projectLine(l.Project))
	b.WriteString(listingLine("commits", l.Listing))
	writeCommitRows(&b, l.Commits, bd)
	return b.String()
}

// Commit renders get_commit.
func Commit(c model.Commit, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Commit %s in %s\n", Ident(c.ID), projectLine(c.Project))
	fmt.Fprintf(&b, "%s\nAuthored by %s at %s; committed by %s at %s; parents %s.\n", Ident(c.WebURL), person(c.AuthorName),
		when(c.AuthoredAt), person(c.CommitterName), when(c.CommittedAt), idents(c.ParentIDs))
	fmt.Fprintf(&b, "%d lines added, %d removed.\n", c.Additions, c.Deletions)
	b.WriteString(bd.Notice() + "\n")
	o := Origin{Kind: "commit_message", Project: c.Project.Path, Item: c.ShortID, Author: ""}
	b.WriteString(bd.Block(o, c.UntrustedMessage) + "\n")
	b.WriteString(budgetLineFor("Message", "message_offset", c.MessageBudget))
	writeDiffs(&b, model.Diffs{Files: c.Files, NotShown: c.NotShown, FilesComplete: c.FilesComplete,
		NextFileOffset: c.NextFileOffset, DiffBudget: c.DiffBudget, HiddenRemoved: c.HiddenRemoved},
		c.Project.Path, "@"+c.ShortID, "The commit changes more files than were read.", bd)
	return b.String()
}

// writeDiffs renders budgeted diffs: each shown file in a boundary, then
// what was not shown and how to continue. at names the version the diffs
// are of, "@abc123"; incomplete is the sentence for more files than were
// read.
func writeDiffs(b *strings.Builder, d model.Diffs, project, at, incomplete string, bd Boundary) {
	for _, f := range d.Files {
		fmt.Fprintf(b, "\n\n%s %s", Ident(f.Status), fileName(f.OldPath, f.NewPath))
		b.WriteString(":\n" + bd.Block(Origin{Kind: "diff", Project: project, Item: f.NewPath + at}, f.UntrustedDiff))
		if f.Truncated {
			b.WriteString("\nThis diff was cut at the budget; get_file reads the file itself.")
		}
	}
	fmt.Fprintf(b, "\n\nDiffs are budgeted at %d characters.", d.DiffBudget)
	if len(d.NotShown) > 0 {
		b.WriteString(" Not shown:")
		for _, f := range d.NotShown {
			fmt.Fprintf(b, "\n- %s %s (%s)", Ident(f.Status), fileName(f.OldPath, f.NewPath), notShownReason(f.Reason))
		}
	}
	if d.NextFileOffset != nil {
		fmt.Fprintf(b, "\nContinue with file_offset=%d.", *d.NextFileOffset)
	}
	if !d.FilesComplete {
		b.WriteString("\n" + incomplete)
	}
	if d.HiddenRemoved > 0 {
		fmt.Fprintf(b, "\n%d hidden characters in the diffs were made visible.", d.HiddenRemoved)
	}
}

func fileName(oldPath, newPath string) string {
	if oldPath != "" && oldPath != newPath {
		return Ident(oldPath) + " -> " + Ident(newPath)
	}
	return Ident(newPath)
}

func notShownReason(r string) string {
	switch r {
	case "too_large":
		return "GitLab did not return a diff: too large"
	case "collapsed":
		return "GitLab collapsed the diff"
	case "budget":
		return "over the budget"
	}
	return r
}

// article is "An" before a vowel sound and "A" otherwise, for the kinds
// resolve_url names.
func article(word string) string {
	if word != "" && strings.ContainsRune("aeiou", rune(word[0])) {
		return "An"
	}
	return "A"
}
