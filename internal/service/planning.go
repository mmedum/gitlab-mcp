package service

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/mmedum/gitlab-mcp/v2/internal/gapi"
	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/v2/internal/model"
	"github.com/mmedum/gitlab-mcp/v2/internal/render"
)

// ListLabels lists the labels a project's issues and merge requests can
// carry (§7.7).
func (s *Service) ListLabels(ctx context.Context, raw string, q gapi.LabelQuery, opts gapi.ListOptions) (model.Labels, error) {
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.Labels{}, err
	}
	rows, page, err := s.client.ListLabels(ctx, p, q, opts)
	if err != nil {
		return model.Labels{}, err
	}
	out := model.Labels{Project: ref, Labels: make([]model.Label, 0, len(rows)), Listing: listing(len(rows), page)}
	for _, l := range rows {
		row := labelRow(l)
		if q.WithCounts {
			row.OpenIssues, row.ClosedIssues, row.OpenMergeRequests = l.OpenIssuesCount, l.ClosedIssuesCount, l.OpenMergeRequestsCount
		}
		out.Labels = append(out.Labels, row)
	}
	return out, nil
}

// MilestoneSearch is list_milestones' query: a project or a group.
type MilestoneSearch struct {
	Project          string
	Group            string
	State            string
	Search           string
	Title            string
	IncludeAncestors bool
	List             gapi.ListOptions
}

// ListMilestones lists a project's or a group's milestones (§7.7).
func (s *Service) ListMilestones(ctx context.Context, q MilestoneSearch) (model.Milestones, error) {
	c, err := s.api()
	if err != nil {
		return model.Milestones{}, err
	}
	if (q.Project == "") == (q.Group == "") {
		return model.Milestones{}, gapi.Errf(gapi.ClassInvalid, "pass a project or a group, one of them")
	}
	mq := gapi.MilestoneQuery{State: q.State, Search: q.Search, Title: q.Title, IncludeAncestors: q.IncludeAncestors}
	out := model.Milestones{Milestones: []model.MilestoneRow{}}
	if q.Project != "" {
		p, ref, err := s.project(ctx, q.Project)
		if err != nil {
			return model.Milestones{}, err
		}
		mq.Project, out.Project = p, &ref
	} else {
		if mq.Group, err = gapi.ParseGroup(q.Group); err != nil {
			return model.Milestones{}, err
		}
		g := mq.Group.String()
		out.Group = &g
	}
	rows, page, err := c.ListMilestones(ctx, mq, q.List)
	if err != nil {
		return model.Milestones{}, err
	}
	out.Listing = listing(len(rows), page)
	for _, m := range rows {
		row := milestoneRow(m)
		out.Milestones = append(out.Milestones, row)
	}
	return out, nil
}

// boardPages bounds how many pages of boards list_boards reads.
const boardPages = render.BoardsRead / gapi.MaxPerPage

// boardListCap bounds the lists shown of one board.
const boardListCap = 100

// milestoneFilters are GitLab's milestone filters a board scope can
// carry in place of a milestone, by their title, spelled as
// search_issues' milestone takes them (app/finders/issuable_finder/
// params.rb at v19.4.1-ee).
var milestoneFilters = map[string]string{"No Milestone": "None", "Any Milestone": "Any", "Upcoming": "#upcoming",
	"Started": "#started"}

// titleIsFilter reports whether search_issues' milestone would read a
// milestone's title as one of GitLab's filters rather than the title.
func titleIsFilter(title string) bool {
	switch strings.ToLower(title) {
	case "none", "any":
		return true
	}
	return title == "No Milestone" || title == "Any Milestone" || title == "#upcoming" || title == "#started"
}

// ListBoards lists a project's issue boards with their lists in board
// order, and for each list the search_issues arguments that return its
// issues (§7.7). GitLab returns every list of a board with it, so a
// page is cut by max and by the boards budget; the token names the
// next board by id.
func (s *Service) ListBoards(ctx context.Context, raw string, opts gapi.ListOptions) (model.Boards, error) {
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.Boards{}, err
	}
	start := int64(0)
	if opts.PageToken != "" {
		if err := gapi.DecodeToken(opts.PageToken, boardBinding(p.ID()), &start); err != nil {
			return model.Boards{}, err
		}
	}
	var all []gitlab.Board
	read := gapi.ListOptions{PerPage: gapi.MaxPerPage}
	complete := false
	for range boardPages {
		rows, page, err := s.client.ListBoards(ctx, p, read)
		if err != nil {
			return model.Boards{}, err
		}
		all = append(all, rows...)
		if complete = page.Complete(); complete {
			break
		}
		read.PageToken = page.NextToken
	}
	slices.SortFunc(all, func(a, b gitlab.Board) int { return cmp.Compare(a.ID, b.ID) })
	from, _ := slices.BinarySearchFunc(all, start, func(b gitlab.Board, id int64) int { return cmp.Compare(b.ID, id) })
	limit := opts.PerPage
	if limit <= 0 {
		limit = gapi.DefaultPerPage
	}
	limit = min(limit, gapi.MaxPerPage)
	out := model.Boards{Project: ref, Boards: []model.Board{}, BudgetChars: render.BoardsBudget, BoardsNotRead: !complete}
	used, next := 0, len(all)
	for i, b := range all[from:] {
		row := boardRow(b)
		size := boardChars(row)
		if len(out.Boards) == limit || (len(out.Boards) > 0 && used+size > render.BoardsBudget) {
			next = from + i
			break
		}
		used += size
		out.Boards = append(out.Boards, row)
	}
	out.Listing = model.Listing{Returned: len(out.Boards), Complete: complete && next >= len(all)}
	if complete {
		total := len(all)
		out.Listing.Total = &total
	}
	if next < len(all) {
		tok := gapi.EncodeToken(boardBinding(p.ID()), all[next].ID)
		out.Listing.NextPageToken = &tok
	}
	return out, nil
}

// boardBinding names the query a list_boards page token belongs to.
func boardBinding(projectID int64) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "boards/%d", projectID))
	return hex.EncodeToString(sum[:12])
}

// boardChars estimates the characters a board takes in a result.
func boardChars(b model.Board) int {
	n := 400 + len(b.UntrustedName)
	for _, l := range b.Lists {
		n += 200
		for _, v := range []*string{l.Label, l.Assignee} {
			if v != nil {
				n += 2 * len(*v)
			}
		}
		for _, t := range []*model.BoardTimebox{l.Milestone, l.Iteration} {
			if t != nil {
				n += 3 * len(t.UntrustedTitle)
			}
		}
	}
	return n
}

func boardRow(b gitlab.Board) model.Board {
	name, _ := render.Line(b.Name, render.TitleChars)
	out := model.Board{ID: b.ID, UntrustedName: name, OpenList: !b.HideBacklogList, ClosedList: !b.HideClosedList,
		Scope: boardScope(b), Lists: []model.BoardList{}}
	// A label list on a board scoped to a milestone holds only the
	// issues in it; a filter in its place is not applied there (§18 row
	// 100).
	var milestone *gitlab.BoardTimebox
	if b.Milestone != nil && b.Milestone.ID > 0 {
		milestone = b.Milestone
	}
	// GitLab orders lists by kind, then position. Every kind shares the
	// positions, so position is the board's order.
	position := func(l gitlab.BoardList) int {
		if l.Position == nil {
			return math.MaxInt
		}
		return *l.Position
	}
	lists := slices.Clone(b.Lists)
	slices.SortStableFunc(lists, func(x, y gitlab.BoardList) int { return cmp.Compare(position(x), position(y)) })
	if len(lists) > boardListCap {
		out.ListsNotShown = len(lists) - boardListCap
		lists = lists[:boardListCap]
	}
	for _, l := range lists {
		out.Lists = append(out.Lists, boardList(l, milestone))
	}
	return out
}

// boardScope reads a board's own filter; nil when it has none.
func boardScope(b gitlab.Board) *model.BoardScope {
	sc := model.BoardScope{Labels: []string{}}
	scoped := false
	if m := b.Milestone; m != nil {
		filter, isFilter := milestoneFilters[m.Title]
		if m.ID == 0 && isFilter {
			sc.MilestoneFilter = &filter
		} else {
			sc.Milestone = timebox(m)
		}
		scoped = true
	}
	if b.Assignee != nil {
		sc.Assignee, scoped = &b.Assignee.Username, true
	}
	for _, l := range b.Labels {
		sc.Labels, scoped = append(sc.Labels, l.Name), true
	}
	if w := b.Weight; w != nil && *w != -1 {
		scoped = true
		if *w == -2 {
			sc.NoWeight = true
		} else {
			sc.Weight = w
		}
	}
	if !scoped {
		return nil
	}
	return &sc
}

// timebox is a milestone or an iteration with its title cleaned for
// display.
func timebox(t *gitlab.BoardTimebox) *model.BoardTimebox {
	title, _ := render.Line(t.Title, render.TitleChars)
	return &model.BoardTimebox{ID: t.ID, UntrustedTitle: title}
}

// boardList reads one list: its kind from the key present, and the
// search_issues arguments that return its issues. A milestone argument
// is the exact title GitLab holds, never the cleaned one.
func boardList(l gitlab.BoardList, scopeMilestone *gitlab.BoardTimebox) model.BoardList {
	out := model.BoardList{ID: l.ID, Position: l.Position, Kind: "unknown"}
	search := &model.ListSearch{State: "opened", Labels: []string{}}
	switch {
	case l.Label != nil:
		out.Kind, out.Label = "label", &l.Label.Name
		search.Labels = []string{l.Label.Name}
		if scopeMilestone != nil {
			if titleIsFilter(scopeMilestone.Title) {
				out.SearchNote = "the board's milestone is left out: search_issues would read its title as one of GitLab's milestone filters"
			} else {
				search.UntrustedMilestone = &scopeMilestone.Title
			}
		}
	case l.Assignee != nil:
		out.Kind, out.Assignee = "assignee", &l.Assignee.Username
		search.Assignee = &l.Assignee.Username
	case l.Milestone != nil:
		out.Kind, out.Milestone = "milestone", timebox(l.Milestone)
		if titleIsFilter(l.Milestone.Title) {
			search = nil
			out.SearchNote = "search_issues would read this milestone's title as one of GitLab's milestone filters, and takes no milestone id"
		} else {
			search.UntrustedMilestone = &l.Milestone.Title
		}
	case l.Iteration != nil:
		out.Kind, out.Iteration, search = "iteration", timebox(l.Iteration), nil
		out.SearchNote = "search_issues has no iteration filter"
	default:
		search = nil
		out.SearchNote = "its kind is unknown"
	}
	out.SearchIssues = search
	return out
}

// roles names GitLab's access levels.
var roles = map[int]string{0: "no access", 5: "minimal access", 10: "guest", 15: "planner", 20: "reporter",
	30: "developer", 40: "maintainer", 50: "owner", 60: "admin"}

// ListMembers lists everyone with access to a project, through a group
// included.
func (s *Service) ListMembers(ctx context.Context, raw, query string, opts gapi.ListOptions) (model.Members, error) {
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.Members{}, err
	}
	rows, page, err := s.client.ListProjectMembers(ctx, p, query, opts)
	if err != nil {
		return model.Members{}, err
	}
	out := model.Members{Project: ref, Members: make([]model.Member, 0, len(rows)), Listing: listing(len(rows), page)}
	for _, m := range rows {
		role, ok := roles[m.AccessLevel]
		if !ok {
			role = "unknown"
		}
		name, _ := render.Line(m.Name, render.TitleChars)
		out.Members = append(out.Members, model.Member{ID: m.ID, Username: m.Username, Name: name, State: m.State,
			AccessLevel: m.AccessLevel, Role: role, ExpiresAt: m.ExpiresAt})
	}
	return out, nil
}

// FindUsers finds accounts by exact username or by a search of names
// and usernames (§6.3).
func (s *Service) FindUsers(ctx context.Context, q gapi.UserQuery, opts gapi.ListOptions) (model.Users, error) {
	c, err := s.api()
	if err != nil {
		return model.Users{}, err
	}
	if (q.Search == "") == (q.Username == "") {
		return model.Users{}, gapi.Errf(gapi.ClassInvalid, "pass username for an exact match or search for a partial one, one of them")
	}
	rows, page, err := c.ListUsers(ctx, q, opts)
	if err != nil {
		return model.Users{}, err
	}
	out := model.Users{Users: make([]model.UserRow, 0, len(rows)), Listing: listing(len(rows), page)}
	for _, u := range rows {
		name, _ := render.Line(u.Name, render.TitleChars)
		out.Users = append(out.Users, model.UserRow{ID: u.ID, Username: u.Username, Name: name, State: u.State, WebURL: u.WebURL})
	}
	return out, nil
}

// TodoSearch is list_todos' query.
type TodoSearch struct {
	Project string
	State   string
	Action  string
	Type    string
	List    gapi.ListOptions
}

// ListTodos lists the signed-in account's to-do items (§7.7).
func (s *Service) ListTodos(ctx context.Context, q TodoSearch) (model.Todos, error) {
	c, err := s.api()
	if err != nil {
		return model.Todos{}, err
	}
	tq := gapi.TodoQuery{State: q.State, Action: q.Action, Type: q.Type}
	if q.Project != "" {
		// GitLab filters to-do items by numeric project id only.
		p, _, err := s.project(ctx, q.Project)
		if err != nil {
			return model.Todos{}, err
		}
		tq.ProjectID = p.ID()
	}
	rows, page, err := c.ListTodos(ctx, tq, q.List)
	if err != nil {
		return model.Todos{}, err
	}
	out := model.Todos{Todos: make([]model.Todo, 0, len(rows)), Listing: listing(len(rows), page)}
	for _, t := range rows {
		title, _ := render.Line(t.Target.Title, render.TitleChars)
		body, _ := render.Line(t.Body, render.TitleChars)
		row := model.Todo{ID: t.ID, Action: t.ActionName, TargetType: t.TargetType, State: t.State,
			Author: t.Author.Username, CreatedAt: t.CreatedAt, TargetURL: t.TargetURL, UntrustedTitle: title, UntrustedBody: body}
		if t.Project != nil {
			row.Project = &model.ProjectRef{ID: t.Project.ID, Path: t.Project.PathWithNamespace}
		}
		if t.Target.IID > 0 {
			iid := t.Target.IID
			row.TargetIID = &iid
		}
		out.Todos = append(out.Todos, row)
	}
	return out, nil
}

// SearchExcerptChars bounds one search row's excerpt: a blob's matching
// lines or a note's body.
const SearchExcerptChars = 1000

// SearchScopes are the scopes search takes. Snippet titles are left to
// the snippets toolset.
var SearchScopes = []string{"blobs", "commits", "issues", "merge_requests", "milestones", "notes", "projects", "users", "wiki_blobs"}

// SearchQuery is search's query.
type SearchQuery struct {
	Project string
	Group   string
	Scope   string
	Search  string
	State   string
	Ref     string
	List    gapi.ListOptions
}

// Search runs GitLab's search in one project, one group or the instance
// (§7.1). A scope the instance or group can only search with advanced
// search, which it lacks, is [unsupported] and says to search a project.
func (s *Service) Search(ctx context.Context, q SearchQuery) (model.Search, error) {
	c, err := s.api()
	if err != nil {
		return model.Search{}, err
	}
	if strings.TrimSpace(q.Search) == "" {
		return model.Search{}, gapi.Errf(gapi.ClassInvalid, "search is empty: pass the text to look for")
	}
	if q.Project != "" && q.Group != "" {
		return model.Search{}, gapi.Errf(gapi.ClassInvalid, "pass a project or a group, not both")
	}
	if q.Ref != "" && q.Project == "" {
		return model.Search{}, gapi.Errf(gapi.ClassInvalid, "ref searches one project's code or commits and needs project")
	}
	sq := gapi.SearchQuery{Scope: q.Scope, Search: q.Search, State: q.State, Ref: q.Ref}
	out := model.Search{Scope: q.Scope, Where: "instance", Rows: []model.SearchRow{}}
	switch {
	case q.Project != "":
		if sq.Project, _, err = s.project(ctx, q.Project); err != nil {
			return model.Search{}, err
		}
		out.Where = "project"
	case q.Group != "":
		if sq.Group, err = gapi.ParseGroup(q.Group); err != nil {
			return model.Search{}, err
		}
		out.Where = "group"
	}
	var page gapi.Page
	if q.Scope == "commits" {
		rows, pg, err := c.SearchCommits(ctx, sq, q.List)
		if err != nil {
			return model.Search{}, searchError(err, out.Where)
		}
		page = pg
		for _, cm := range rows {
			title, _ := render.Line(cm.Title, render.TitleChars)
			author, _ := render.Line(cm.AuthorName, render.TitleChars)
			at := cm.CommittedDate
			out.Rows = append(out.Rows, model.SearchRow{Kind: "commit", ProjectID: positive(cm.ProjectID), SHA: cm.ID,
				Author: author, UpdatedAt: &at, WebURL: cm.WebURL, UntrustedTitle: title})
		}
	} else {
		rows, pg, err := c.Search(ctx, sq, q.List)
		if err != nil {
			return model.Search{}, searchError(err, out.Where)
		}
		page = pg
		for _, h := range rows {
			out.Rows = append(out.Rows, s.searchRow(q.Scope, h))
		}
	}
	out.Listing = listing(len(out.Rows), page)
	return out, nil
}

// searchRow reads one hit of a scope other than commits.
func (s *Service) searchRow(scope string, h gitlab.SearchHit) model.SearchRow {
	row := model.SearchRow{ProjectID: positive(h.ProjectID), State: h.State, UpdatedAt: h.UpdatedAt, WebURL: h.WebURL}
	if h.Author != nil {
		row.Author = h.Author.Username
	}
	title := h.Title
	switch scope {
	case "issues", "merge_requests":
		row.Kind = strings.TrimSuffix(scope, "s")
		row.IID = positive(h.IID)
	case "milestones":
		row.Kind, row.ID, row.IID = "milestone", positive(h.ID), positive(h.IID)
	case "projects":
		row.Kind, row.ID, row.Path, title = "project", positive(h.ID), h.PathWithNamespace, h.Name
		row.ProjectID = positive(h.ID)
	case "users":
		row.Kind, row.ID, row.Author, title = "user", positive(h.ID), h.Username, h.Name
	case "blobs", "wiki_blobs":
		row.Kind, row.Path, row.Ref = strings.TrimSuffix(scope, "s"), h.Path, h.Ref
		if h.Startline > 0 {
			line := h.Startline
			row.StartLine = &line
		}
		text, _ := render.Code(h.Data)
		row.UntrustedExcerpt, row.ExcerptCut = excerpt(text)
	case "notes":
		row.Kind, row.ID, row.IID = "note", positive(h.ID), h.NoteableIID
		clean, _ := render.Markdown(h.Body, s.self())
		row.UntrustedExcerpt, row.ExcerptCut = excerpt(clean)
	}
	row.UntrustedTitle, _ = render.Line(title, render.TitleChars)
	return row
}

// excerpt cuts a search row's text at SearchExcerptChars.
func excerpt(text string) (string, bool) {
	shown, b := render.Cut(text, 0, SearchExcerptChars)
	return shown, b.ContinueOffset != nil
}

// searchError says what to do about a scope GitLab searches here only
// with advanced search.
func searchError(err error, where string) error {
	if c, _ := gapi.ClassOf(err); c == gapi.ClassUnsupported {
		return gapi.Errf(gapi.ClassUnsupported,
			"this scope needs advanced search across a %s, which it does not have here; search inside one project instead", where)
	}
	return err
}

func positive(n int64) *int64 {
	if n <= 0 {
		return nil
	}
	return &n
}
