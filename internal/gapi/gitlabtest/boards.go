package gitlabtest

import (
	"cmp"
	"slices"
)

// Issue boards. GET /projects/:id/boards answers each board with its
// lists whole, as GitLab does (lib/api/entities/board.rb at v19.4.1-ee):
// ordered by kind, then position, and without the Open and Closed lists.
// A list names no kind; the key present says it. A status list carries
// none of them. Alpha's namespace has the paid features, so its boards
// carry the scope keys and its lists every kind; beta has no board.

// Board ids a test can state literally.
const (
	// BoardDevelopment is alpha's unscoped board, with a list of every
	// kind and positions GitLab's order does not follow.
	BoardDevelopment = 98001
	// BoardSprint is scoped to a milestone, a label and a weight, and
	// hides its Closed list.
	BoardSprint = 98002
	// BoardUpcoming is scoped to GitLab's Upcoming filter, an assignee
	// and no weight.
	BoardUpcoming = 98003
	// IterationID is the iteration a list names.
	IterationID = 98100
)

// listType is GitLab's List#list_type, the order it lists them in.
const (
	listLabel = iota + 1
	_         // closed, never returned
	listAssignee
	listMilestone
	listIteration
	listStatus
)

type board struct {
	id          int64
	name        string
	hideBacklog bool
	hideClosed  bool
	milestone   map[string]any
	assignee    string
	labels      []string
	weight      *int
	lists       []boardList
}

type boardList struct {
	id       int64
	kind     int
	position int
	value    string // a label name, a username, a milestone or iteration title
}

// fillBoards gives alpha its boards.
func (s *Server) fillBoards(p *project) {
	three, none := 3, -2
	p.boards = []board{
		{id: BoardDevelopment, name: "Development", lists: []boardList{
			{id: 98011, kind: listLabel, position: 3, value: "feature"},
			{id: 98012, kind: listLabel, position: 0, value: "bug"},
			{id: 98013, kind: listAssignee, position: 1, value: "bob"},
			{id: 98014, kind: listMilestone, position: 4, value: "Sprint 2"},
			{id: 98015, kind: listIteration, position: 2, value: "Iteration 1"},
			{id: 98016, kind: listStatus, position: 5},
		}},
		{id: BoardSprint, name: "Sprint <<<2>>> board", hideClosed: true, milestone: s.milestoneJSON(p, MilestoneActive),
			labels: []string{"bug"}, weight: &three, lists: []boardList{{id: 98021, kind: listLabel, position: 0, value: "docs"}}},
		{id: BoardUpcoming, name: "Upcoming", hideBacklog: true, milestone: map[string]any{"title": "Upcoming"},
			assignee: "carol", weight: &none},
	}
}

func (s *Server) milestoneJSON(p *project, id int64) map[string]any {
	for _, m := range p.milestones {
		if m.ID == id {
			return map[string]any{"id": m.ID, "iid": m.IID, "project_id": p.ID, "title": m.Title, "description": "",
				"state": m.State, "created_at": m.UpdatedAt, "updated_at": m.UpdatedAt, "due_date": m.DueDate,
				"start_date": m.StartDate, "expired": m.Expired, "web_url": m.WebURL}
		}
	}
	return nil
}

// boardJSON is a board as GitLab answers it, with its paid scope keys.
func (s *Server) boardJSON(p *project, b board) map[string]any {
	lists := slices.Clone(b.lists)
	slices.SortFunc(lists, func(x, y boardList) int {
		return cmp.Or(cmp.Compare(x.kind, y.kind), cmp.Compare(x.position, y.position))
	})
	rows := make([]map[string]any, 0, len(lists))
	for _, l := range lists {
		row := map[string]any{"id": l.id, "label": nil, "position": l.position}
		switch l.kind {
		case listLabel:
			row["label"] = s.labelJSON(p, l.value)
		case listAssignee:
			u := s.user(l.value)
			row["assignee"] = map[string]any{"id": u.ID, "username": u.Username, "name": u.Name, "public_email": ""}
		case listMilestone:
			for _, m := range p.milestones {
				if m.Title == l.value {
					row["milestone"] = s.milestoneJSON(p, m.ID)
				}
			}
		case listIteration:
			row["iteration"] = map[string]any{"id": IterationID, "iid": 1, "sequence": 1, "group_id": firstGroupID,
				"title": l.value, "description": nil, "state": 2, "start_date": "2026-01-12", "due_date": "2026-01-25",
				"web_url": s.URL + "/groups/" + GroupTop + "/-/iterations/98100"}
		}
		rows = append(rows, row)
	}
	var assignee any
	if b.assignee != "" {
		u := s.user(b.assignee)
		assignee = map[string]any{"id": u.ID, "username": u.Username, "name": u.Name, "state": u.State, "web_url": u.WebURL}
	}
	labels := make([]map[string]any, 0, len(b.labels))
	for _, name := range b.labels {
		labels = append(labels, s.labelJSON(p, name))
	}
	var milestone any
	if b.milestone != nil {
		milestone = b.milestone
	}
	var weight any
	if b.weight != nil {
		weight = *b.weight
	}
	return map[string]any{"id": b.id, "name": b.name, "hide_backlog_list": b.hideBacklog, "hide_closed_list": b.hideClosed,
		"project": map[string]any{"id": p.ID, "name": p.Name, "path_with_namespace": p.PathWithNamespace, "web_url": p.WebURL},
		"group":   nil, "lists": rows, "milestone": milestone, "assignee": assignee, "labels": labels, "weight": weight}
}

// labelJSON is a label as a board or a list names it.
func (s *Server) labelJSON(p *project, name string) map[string]any {
	for _, l := range p.labels {
		if l.Name == name {
			return map[string]any{"id": l.ID, "name": l.Name, "description": l.Description, "description_html": "",
				"text_color": "#FFFFFF", "color": l.Color, "archived": l.Archived}
		}
	}
	return nil
}

// listBoards answers GET /projects/:id/boards, by id.
func (s *Server) listBoards(p *project) []map[string]any {
	out := make([]map[string]any, 0, len(p.boards))
	for _, b := range p.boards {
		out = append(out, s.boardJSON(p, b))
	}
	return out
}
