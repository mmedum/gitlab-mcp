package render

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mmedum/gitlab-mcp/v2/internal/model"
)

// The readable half of the optional toolsets: wiki pages, snippets,
// releases, environments, deployments and events. Titles, page content,
// snippet files and release notes were written by other people and sit
// inside the call's boundary.

// ------------------------------------------------------------------ wiki

// WikiPages renders list_wiki_pages.
func WikiPages(l model.WikiPages, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Wiki pages of %s\n", projectLine(l.Project))
	b.WriteString(listingLine("pages", l.Listing))
	if len(l.Pages) > 0 {
		b.WriteString("\n" + bd.Notice())
	}
	for _, p := range l.Pages {
		fmt.Fprintf(&b, "\n- slug %s (%s): %s", Ident(p.Slug), Ident(p.Format), bd.Inline(p.Title))
	}
	return b.String()
}

// WikiPage renders get_wiki_page.
func WikiPage(p model.WikiPage, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Wiki page %s in %s\n", Ident(p.Slug), projectLine(p.Project))
	fmt.Fprintf(&b, "Format %s; content_sha256 %s, which save_wiki_page and delete_wiki_page take.\n", Ident(p.Format), p.ContentSHA256)
	b.WriteString(bd.Notice() + "\n")
	fmt.Fprintf(&b, "Title: %s\n", bd.Inline(p.UntrustedTitle))
	o := Origin{Kind: "wiki_page", Project: p.Project.Path, Item: p.Slug}
	b.WriteString(bd.Block(o, p.UntrustedContent) + "\n")
	b.WriteString(budgetLine("Content", p.Budget))
	return b.String()
}

// WikiWrite renders save_wiki_page.
func WikiWrite(w model.WikiWrite, _ Boundary) string {
	var b strings.Builder
	writeHead(&b, outcomeLine(w.Outcome, "wiki page "+Ident(w.Slug), "Created"), w.Write)
	if w.DryRun {
		return b.String()
	}
	fmt.Fprintf(&b, "\nSlug %s; format %s.", Ident(w.Slug), Ident(w.Format))
	changedLine(&b, w.Outcome, w.Changed)
	removedLine(&b, "content", w.ContentRemoved)
	fmt.Fprintf(&b, "\ncontent_sha256 is %s; pass it to save_wiki_page for the next change.", w.ContentSHA256)
	return b.String()
}

// WikiDelete renders delete_wiki_page.
func WikiDelete(w model.WikiDelete, _ Boundary) string {
	var b strings.Builder
	writeHead(&b, fmt.Sprintf("Deleted wiki page %s.", Ident(w.Slug)), w.Write)
	goneLine(&b, w.Write, "page")
	return b.String()
}

// -------------------------------------------------------------- snippets

func snippetWhere(p *model.ProjectRef) string {
	if p == nil {
		return "your personal snippets"
	}
	return projectLine(*p)
}

// Snippets renders list_snippets.
func Snippets(l model.Snippets, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Snippets in %s\n", snippetWhere(l.Project))
	b.WriteString(listingLine("snippets", l.Listing))
	if len(l.Snippets) > 0 {
		b.WriteString("\n" + bd.Notice())
	}
	for _, s := range l.Snippets {
		fmt.Fprintf(&b, "\n- snippet %d, %s, by @%s, updated %s; files %s: %s", s.ID, Ident(s.Visibility), Ident(s.Author),
			when(s.UpdatedAt), idents(s.Files), bd.Inline(s.UntrustedTitle))
	}
	return b.String()
}

// Snippet renders get_snippet.
func Snippet(s model.Snippet, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Snippet %d in %s\n", s.ID, snippetWhere(s.Project))
	fmt.Fprintf(&b, "%s; %s; by @%s; created %s; updated %s.\n", Ident(s.WebURL), Ident(s.Visibility), Ident(s.Author),
		when(s.CreatedAt), when(s.UpdatedAt))
	fmt.Fprintf(&b, "Files: %s; shown: %s.\n", idents(s.Files), Ident(s.File))
	b.WriteString(bd.Notice() + "\n")
	fmt.Fprintf(&b, "Title: %s\n", bd.Inline(s.UntrustedTitle))
	project := ""
	if s.Project != nil {
		project = s.Project.Path
	}
	if s.UntrustedDescription != "" {
		b.WriteString(bd.Block(Origin{Kind: "snippet_description", Project: project, Item: "$" + strconv.FormatInt(s.ID, 10), Author: s.Author},
			s.UntrustedDescription) + "\n")
	}
	if s.Binary {
		b.WriteString("The file is binary; its content is not shown.")
		return b.String()
	}
	b.WriteString(bd.Block(Origin{Kind: "snippet_file", Project: project, Item: s.File, Author: s.Author}, s.UntrustedContent) + "\n")
	b.WriteString(budgetLine("File", s.Budget))
	return b.String()
}

// SnippetWrite renders create_snippet.
func SnippetWrite(w model.SnippetWrite, _ Boundary) string {
	var b strings.Builder
	head := fmt.Sprintf("Created private snippet %d.", w.ID)
	writeHead(&b, head, w.Write)
	if w.Target.Project.Path == "" {
		b.WriteString("\nA personal snippet: only you can see it.")
	}
	fmt.Fprintf(&b, "\nFiles: %s.", idents(w.Files))
	if !w.DryRun {
		fmt.Fprintf(&b, "\n%s; visibility %s.", Ident(w.WebURL), Ident(w.Visibility))
	}
	return b.String()
}

// SnippetUpdate renders update_snippet.
func SnippetUpdate(w model.SnippetUpdate, _ Boundary) string {
	var b strings.Builder
	writeHead(&b, outcomeLine(w.Outcome, fmt.Sprintf("snippet %d", w.ID), ""), w.Write)
	if w.Target.Project.Path == "" {
		b.WriteString("\nA personal snippet.")
	}
	fmt.Fprintf(&b, "\nFiles: %s.", idents(w.Files))
	if w.DryRun {
		return b.String()
	}
	fmt.Fprintf(&b, "\n%s; visibility %s, unchanged.", Ident(w.WebURL), Ident(w.Visibility))
	changedLine(&b, w.Outcome, w.Changed)
	removedLine(&b, "description", w.DescriptionRemoved)
	witnessLine(&b, w.UpdatedAt, "update_snippet or delete_snippet")
	return b.String()
}

// SnippetDelete renders delete_snippet.
func SnippetDelete(w model.SnippetDelete, _ Boundary) string {
	var b strings.Builder
	writeHead(&b, fmt.Sprintf("Deleted snippet %d.", w.ID), w.Write)
	goneLine(&b, w.Write, "snippet")
	return b.String()
}

// -------------------------------------------------------------- releases

// Releases renders list_releases.
func Releases(l model.Releases, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Releases of %s\n", projectLine(l.Project))
	b.WriteString(listingLine("releases", l.Listing))
	if len(l.Releases) > 0 {
		b.WriteString("\n" + bd.Notice())
	}
	for _, r := range l.Releases {
		fmt.Fprintf(&b, "\n- tag %s at %s, by @%s, released %s", Ident(r.TagName), shortSHA(r.CommitSHA), Ident(r.Author), whenPtr(r.ReleasedAt))
		if r.Upcoming {
			b.WriteString(" (upcoming)")
		}
		fmt.Fprintf(&b, ": %s", bd.Inline(r.UntrustedName))
	}
	return b.String()
}

// Release renders get_release.
func Release(r model.Release, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Release of tag %s in %s\n", Ident(r.TagName), projectLine(r.Project))
	fmt.Fprintf(&b, "Commit %s; by @%s; created %s; released %s", Ident(r.CommitSHA), Ident(r.Author), when(r.CreatedAt), whenPtr(r.ReleasedAt))
	if r.Upcoming {
		b.WriteString(" (upcoming)")
	}
	fmt.Fprintf(&b, "; milestones %s; %d asset(s).\n", idents(r.Milestones), r.Assets)
	b.WriteString(bd.Notice() + "\n")
	fmt.Fprintf(&b, "Name: %s\n", bd.Inline(r.UntrustedName))
	b.WriteString(bd.Block(Origin{Kind: "release_notes", Project: r.Project.Path, Item: r.TagName, Author: r.Author}, r.UntrustedDescription) + "\n")
	b.WriteString(budgetLine("Release notes", r.Budget))
	return b.String()
}

// ReleaseWrite renders create_release.
func ReleaseWrite(w model.ReleaseWrite, _ Boundary) string {
	var b strings.Builder
	writeHead(&b, fmt.Sprintf("Created the release of tag %s.", Ident(w.TagName)), w.Write)
	switch {
	case w.TagCreated && w.DryRun:
		b.WriteString("\nThe tag does not exist yet: GitLab would create it at ref, which starts the project's tag pipelines.")
	case w.TagCreated:
		b.WriteString("\nGitLab created the tag, which starts the project's tag pipelines.")
	}
	if !w.DryRun {
		fmt.Fprintf(&b, "\nCommit %s; released %s; milestones %s.", Ident(w.CommitSHA), whenPtr(w.ReleasedAt), idents(w.Milestones))
	}
	for _, l := range w.Links {
		fmt.Fprintf(&b, "\nAsset link %s (%s): %s", Ident(l.Name), Ident(orNone(l.LinkType)), Ident(l.URL))
	}
	return b.String()
}

// ----------------------------------------------------------- deployments

// Environments renders list_environments.
func Environments(l model.Environments, _ Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Environments of %s\n", projectLine(l.Project))
	b.WriteString(listingLine("environments", l.Listing))
	for _, e := range l.Environments {
		fmt.Fprintf(&b, "\n- environment %d %s (tier %s): %s, updated %s", e.ID, Ident(e.Name), Ident(orNone(e.Tier)), Ident(e.State), when(e.UpdatedAt))
		if e.ExternalURL != "" {
			fmt.Fprintf(&b, ", served at %s", Ident(e.ExternalURL))
		}
		if d := e.LastDeployment; d != nil {
			fmt.Fprintf(&b, "; last deployment %d (#%d) of %s at %s, %s", d.ID, d.IID, Ident(d.Ref), shortSHA(d.SHA), Ident(d.Status))
		}
	}
	return b.String()
}

// Deployments renders list_deployments.
func Deployments(l model.Deployments, _ Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Deployments of %s\n", projectLine(l.Project))
	b.WriteString(listingLine("deployments", l.Listing))
	for _, d := range l.Deployments {
		fmt.Fprintf(&b, "\n- deployment %d (#%d) to %s: %s, %s at %s, by @%s, created %s", d.ID, d.IID, Ident(d.Environment), Ident(d.Status),
			Ident(d.Ref), shortSHA(d.SHA), Ident(d.User), when(d.CreatedAt))
		if d.JobID != 0 {
			fmt.Fprintf(&b, ", job %d %s", d.JobID, Ident(d.JobName))
		}
	}
	return b.String()
}

// -------------------------------------------------------------- activity

// Events renders list_events.
func Events(l model.Events, bd Boundary) string {
	var b strings.Builder
	where := "your activity"
	if l.Project != nil {
		where = "activity in " + projectLine(*l.Project)
	}
	fmt.Fprintf(&b, "Events: %s\n", where)
	b.WriteString(listingLine("events", l.Listing))
	if len(l.Events) > 0 {
		b.WriteString("\n" + bd.Notice())
	}
	for _, e := range l.Events {
		fmt.Fprintf(&b, "\n- %s @%s %s", when(e.CreatedAt), Ident(e.Author), Ident(e.Action))
		switch {
		case e.PushCommits > 0 || e.PushRef != "":
			fmt.Fprintf(&b, " %s, %d commit(s)", Ident(e.PushRef), e.PushCommits)
			if e.UntrustedCommitTitle != "" {
				fmt.Fprintf(&b, ": %s", bd.Inline(e.UntrustedCommitTitle))
			}
		case e.TargetType != "":
			fmt.Fprintf(&b, " %s", Ident(e.TargetType))
			if e.TargetIID != nil {
				fmt.Fprintf(&b, " %d", *e.TargetIID)
			} else if e.TargetID != nil {
				fmt.Fprintf(&b, " id %d", *e.TargetID)
			}
			if e.UntrustedTargetTitle != "" {
				fmt.Fprintf(&b, ": %s", bd.Inline(e.UntrustedTargetTitle))
			}
		}
		if e.ProjectID != nil && l.Project == nil {
			fmt.Fprintf(&b, " (project %d)", *e.ProjectID)
		}
	}
	return b.String()
}
