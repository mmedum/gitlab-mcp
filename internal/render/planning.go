package render

import (
	"fmt"
	"strings"

	"github.com/mmedum/gitlab-mcp/v2/internal/model"
)

// The readable half of the planning and navigation reads.

// Labels renders list_labels.
func Labels(l model.Labels, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Labels usable in %s\n", projectLine(l.Project))
	b.WriteString(listingLine("labels", l.Listing))
	described := false
	for _, x := range l.Labels {
		described = described || x.UntrustedDescription != ""
	}
	if described {
		b.WriteString("\n" + bd.Notice())
	}
	for _, x := range l.Labels {
		where := "from a group"
		if x.ProjectOnly {
			where = "the project's"
		}
		fmt.Fprintf(&b, "\n- %s (id %d, %s, %s", Ident(x.Name), x.ID, where, Ident(x.Color))
		if x.Priority != nil {
			fmt.Fprintf(&b, ", priority %d", *x.Priority)
		}
		if x.OpenIssues != nil && x.ClosedIssues != nil && x.OpenMergeRequests != nil {
			fmt.Fprintf(&b, "; %d open issues, %d closed, %d open merge requests", *x.OpenIssues, *x.ClosedIssues, *x.OpenMergeRequests)
		}
		b.WriteString(")")
		if x.UntrustedDescription != "" {
			fmt.Fprintf(&b, ": %s", bd.Inline(x.UntrustedDescription))
		}
	}
	return b.String()
}

// Milestones renders list_milestones.
func Milestones(m model.Milestones, bd Boundary) string {
	var b strings.Builder
	switch {
	case m.Project != nil:
		fmt.Fprintf(&b, "Milestones of %s\n", projectLine(*m.Project))
	case m.Group != nil:
		fmt.Fprintf(&b, "Milestones of the group %s\n", Ident(*m.Group))
	}
	b.WriteString(listingLine("milestones", m.Listing))
	if len(m.Milestones) > 0 {
		b.WriteString("\n" + bd.Notice())
	}
	for _, x := range m.Milestones {
		fmt.Fprintf(&b, "\n- %s (id %d): %s", bd.Inline(x.UntrustedTitle), x.ID, Ident(x.State))
		if x.Expired {
			b.WriteString(", expired")
		}
		if x.StartDate != nil {
			fmt.Fprintf(&b, ", starts %s", Ident(*x.StartDate))
		}
		if x.DueDate != nil {
			fmt.Fprintf(&b, ", due %s", Ident(*x.DueDate))
		}
	}
	return b.String()
}

// Boards renders list_boards.
func Boards(l model.Boards, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Issue boards of %s\n", projectLine(l.Project))
	b.WriteString(listingLine("boards", l.Listing))
	if len(l.Boards) == 0 {
		if l.Listing.Total != nil && *l.Listing.Total == 0 {
			b.WriteString("\nThe project has no board yet: GitLab makes the first when someone opens its board page.")
		}
		return b.String()
	}
	fmt.Fprintf(&b, "\nA page holds about %d characters of boards and lists, and ends early rather than pass it. ", l.BudgetChars)
	b.WriteString("Lists are shown in board order, left to right, by their position; GitLab returns them by kind first. " +
		"GitLab does not return the Open and Closed lists, so they are described here. Each list's search_issues " +
		"arguments go with project.\n" + bd.Notice())
	for _, x := range l.Boards {
		fmt.Fprintf(&b, "\n\nBoard %d: %s", x.ID, bd.Inline(x.UntrustedName))
		if x.Scope != nil {
			b.WriteString("\n" + boardScope(*x.Scope, bd))
		}
		if x.OpenList {
			b.WriteString("\n- Open: open issues in none of the lists below. search_issues cannot leave issues out: " +
				"search state opened and drop those the lists below hold.")
		} else {
			b.WriteString("\n- Open: hidden on this board.")
		}
		for _, list := range x.Lists {
			b.WriteString("\n- " + boardList(list, bd))
		}
		if x.ListsNotShown > 0 {
			fmt.Fprintf(&b, "\n- %d more lists, not shown.", x.ListsNotShown)
		}
		if x.ClosedList {
			b.WriteString("\n- Closed: every closed issue; search_issues state closed.")
		} else {
			b.WriteString("\n- Closed: hidden on this board.")
		}
	}
	return b.String()
}

// boardScope names a board's scope and says what the lists' arguments
// carry of it.
func boardScope(s model.BoardScope, bd Boundary) string {
	var parts, notes []string
	switch {
	case s.Milestone != nil:
		parts = append(parts, fmt.Sprintf("milestone %s (id %d)", bd.Inline(s.Milestone.UntrustedTitle), s.Milestone.ID))
		notes = append(notes, "Label lists' arguments carry its milestone, as GitLab applies it to them; whether it "+
			"applies to the other lists is not verified.")
	case s.MilestoneFilter != nil:
		parts = append(parts, "milestone filter "+Ident(*s.MilestoneFilter))
		notes = append(notes, "Its milestone filter is in no list's arguments: GitLab does not apply it to label lists, "+
			"and where it applies it otherwise is not verified. search_issues takes it as milestone "+Ident(*s.MilestoneFilter)+".")
	}
	if s.Assignee != nil {
		parts = append(parts, "assignee @"+Ident(*s.Assignee))
	}
	if len(s.Labels) > 0 {
		parts = append(parts, "labels "+idents(s.Labels))
	}
	var rest []string
	if s.Assignee != nil {
		rest = append(rest, "assignee")
	}
	if len(s.Labels) > 0 {
		rest = append(rest, "labels")
	}
	if len(rest) > 0 {
		notes = append(notes, "Its "+strings.Join(rest, " and ")+" are in no list's arguments: where GitLab applies "+
			"them is not verified; add them to a search_issues call to match what the board shows.")
	}
	switch {
	case s.NoWeight:
		parts = append(parts, "no weight")
	case s.Weight != nil:
		parts = append(parts, fmt.Sprintf("weight %d", *s.Weight))
	}
	if s.NoWeight || s.Weight != nil {
		notes = append(notes, "search_issues has no weight filter, so its weight cannot be reproduced.")
	}
	return "Scope: " + strings.Join(parts, ", ") + ". " + strings.Join(notes, " ")
}

func boardList(l model.BoardList, bd Boundary) string {
	var s string
	ids := []string{fmt.Sprintf("list id %d", l.ID)}
	switch l.Kind {
	case "label":
		s = "label " + Ident(*l.Label)
	case "assignee":
		s = "assignee @" + Ident(*l.Assignee)
	case "milestone":
		s = "milestone " + bd.Inline(l.Milestone.UntrustedTitle)
		ids = append(ids, fmt.Sprintf("milestone id %d", l.Milestone.ID))
	case "iteration":
		s = "iteration " + bd.Inline(l.Iteration.UntrustedTitle)
		ids = append(ids, fmt.Sprintf("iteration id %d", l.Iteration.ID))
	default:
		s = "a list of unknown kind, perhaps a status list"
	}
	if l.Position != nil {
		ids = append(ids, fmt.Sprintf("position %d", *l.Position))
	}
	s += " (" + strings.Join(ids, ", ") + "): "
	q := l.SearchIssues
	if q == nil {
		return s + "search_issues cannot return it: " + l.SearchNote + "."
	}
	args := []string{"state " + Ident(q.State)}
	if len(q.Labels) > 0 {
		args = append(args, "labels "+idents(q.Labels))
	}
	if q.Assignee != nil {
		args = append(args, "assignee "+Ident(*q.Assignee))
	}
	if q.UntrustedMilestone != nil {
		// The argument is the exact title; the text shows it cleaned.
		title, _ := Line(*q.UntrustedMilestone, TitleChars)
		arg := "milestone " + bd.Inline(title)
		if title != *q.UntrustedMilestone {
			arg += " (shown cleaned; pass the exact title from search_issues.untrusted_milestone)"
		}
		args = append(args, arg)
	}
	s += "search_issues " + strings.Join(args, ", ") + "."
	if l.SearchNote != "" {
		s += " Note: " + l.SearchNote + "."
	}
	return s
}

// Members renders list_members.
func Members(l model.Members, _ Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Members of %s, through its groups included\n", projectLine(l.Project))
	b.WriteString(listingLine("members", l.Listing))
	for _, m := range l.Members {
		fmt.Fprintf(&b, "\n- @%s (%s): %s", Ident(m.Username), person(m.Name), m.Role)
		if m.State != "active" {
			fmt.Fprintf(&b, ", %s", Ident(m.State))
		}
		if m.ExpiresAt != nil {
			fmt.Fprintf(&b, ", until %s", Ident(*m.ExpiresAt))
		}
	}
	return b.String()
}

// Users renders find_users.
func Users(l model.Users, _ Boundary) string {
	var b strings.Builder
	b.WriteString(listingLine("users", l.Listing))
	for _, u := range l.Users {
		fmt.Fprintf(&b, "\n- @%s (%s), user id %d", Ident(u.Username), person(u.Name), u.ID)
		if u.State != "active" {
			fmt.Fprintf(&b, ", %s", Ident(u.State))
		}
	}
	return b.String()
}

// Todos renders list_todos.
func Todos(l model.Todos, bd Boundary) string {
	var b strings.Builder
	b.WriteString(listingLine("to-do items", l.Listing))
	if len(l.Todos) > 0 {
		b.WriteString("\n" + bd.Notice())
	}
	for _, t := range l.Todos {
		fmt.Fprintf(&b, "\n- %d %s, %s", t.ID, Ident(t.Action), Ident(t.TargetType))
		if t.TargetIID != nil {
			fmt.Fprintf(&b, " iid %d", *t.TargetIID)
		}
		if t.Project != nil {
			fmt.Fprintf(&b, " in %s (project id %d)", Ident(t.Project.Path), t.Project.ID)
		}
		fmt.Fprintf(&b, ", by @%s, %s, %s: %s", Ident(t.Author), Ident(t.State), when(t.CreatedAt), bd.Inline(t.UntrustedTitle))
		if t.UntrustedBody != "" && t.UntrustedBody != t.UntrustedTitle {
			fmt.Fprintf(&b, "; %s", bd.Inline(t.UntrustedBody))
		}
	}
	return b.String()
}

// Search renders search.
func Search(s model.Search, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Search of %s in the %s\n", Ident(s.Scope), s.Where)
	b.WriteString(listingLine("results", s.Listing))
	if len(s.Rows) > 0 {
		b.WriteString("\n" + bd.Notice())
	}
	for _, r := range s.Rows {
		b.WriteString("\n- " + r.Kind)
		switch {
		case r.Kind == "note" && r.ID != nil:
			fmt.Fprintf(&b, " %d", *r.ID)
			if r.IID != nil {
				fmt.Fprintf(&b, " on the item with iid %d", *r.IID)
			}
		case r.SHA != "":
			fmt.Fprintf(&b, " %s", Ident(r.SHA))
		case r.IID != nil:
			fmt.Fprintf(&b, " iid %d", *r.IID)
		case r.ID != nil:
			fmt.Fprintf(&b, " id %d", *r.ID)
		}
		if r.ProjectID != nil && r.Kind != "project" {
			fmt.Fprintf(&b, " in project %d", *r.ProjectID)
		}
		if r.Path != "" {
			fmt.Fprintf(&b, ", %s", Ident(r.Path))
		}
		if r.Ref != "" {
			fmt.Fprintf(&b, " at %s", Ident(r.Ref))
		}
		if r.StartLine != nil {
			fmt.Fprintf(&b, " from line %d", *r.StartLine)
		}
		if r.State != "" {
			fmt.Fprintf(&b, ", %s", Ident(r.State))
		}
		if r.Author != "" {
			fmt.Fprintf(&b, ", by %s", Ident(r.Author))
		}
		if r.UntrustedTitle != "" {
			fmt.Fprintf(&b, ": %s", bd.Inline(r.UntrustedTitle))
		}
		if r.UntrustedExcerpt != "" {
			b.WriteString("\n" + bd.Block(Origin{Kind: "search_" + r.Kind, Item: r.Path}, r.UntrustedExcerpt))
			if r.ExcerptCut {
				b.WriteString("\nThe excerpt was cut; get_file or list_discussions reads it whole.")
			}
		}
	}
	return b.String()
}
