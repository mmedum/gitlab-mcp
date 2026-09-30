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
		if l.Listing.Complete {
			b.WriteString("\nThe project has no board yet: GitLab makes the first when someone opens its board page.")
		}
		return b.String()
	}
	b.WriteString("\nLists are shown in board order, left to right, by their position; GitLab returns them by kind first. " +
		"GitLab does not return the Open and Closed lists, so they are described here. Each list's search_issues " +
		"arguments go with project.\n" + bd.Notice())
	for _, x := range l.Boards {
		fmt.Fprintf(&b, "\n\nBoard %d: %s", x.ID, bd.Inline(x.UntrustedName))
		if x.Scope != nil {
			b.WriteString("\nScope: " + boardScope(*x.Scope, bd) + ". A label list's arguments carry a scope's milestone, " +
				"as GitLab applies it; where GitLab applies the rest of a scope is not verified, so add it to a search to " +
				"match what the board shows.")
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
		if x.ClosedList {
			b.WriteString("\n- Closed: every closed issue; search_issues state closed.")
		} else {
			b.WriteString("\n- Closed: hidden on this board.")
		}
	}
	return b.String()
}

func boardScope(s model.BoardScope, bd Boundary) string {
	var parts []string
	if s.UntrustedMilestone != nil {
		parts = append(parts, "milestone "+bd.Inline(*s.UntrustedMilestone))
	}
	if s.Assignee != nil {
		parts = append(parts, "assignee @"+Ident(*s.Assignee))
	}
	if len(s.Labels) > 0 {
		parts = append(parts, "labels "+identList(s.Labels))
	}
	switch {
	case s.NoWeight:
		parts = append(parts, "no weight")
	case s.Weight != nil:
		parts = append(parts, fmt.Sprintf("weight %d", *s.Weight))
	}
	return strings.Join(parts, ", ")
}

func identList(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = Ident(n)
	}
	return strings.Join(out, ", ")
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
		return s + "search_issues has no filter for it."
	}
	args := []string{"state " + Ident(q.State)}
	if len(q.Labels) > 0 {
		args = append(args, "labels "+identList(q.Labels))
	}
	if q.Assignee != nil {
		args = append(args, "assignee "+Ident(*q.Assignee))
	}
	if q.UntrustedMilestone != nil {
		args = append(args, "milestone "+bd.Inline(*q.UntrustedMilestone))
	}
	return s + "search_issues " + strings.Join(args, ", ") + "."
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
