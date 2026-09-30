package gitlabtest

import (
	"net/http"
	"slices"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
)

// The resource events of issues and merge requests. GitLab records one
// when a label is added or removed, when the state changes, when the
// milestone is set or removed, and when an issue's weight changes, and
// lists each kind oldest first. The writes here record them as GitLab
// does, and fillItemEvents seeds a history on alpha's first issue and
// merge request.
//
// What each list leaves out is GitLab's (lib/api/resource_*_events.rb at
// v19.4.1-ee): a label event whose label the user cannot read is dropped
// after the page is cut, so a page can be short and X-Total counts it; a
// milestone event whose milestone was deleted or cannot be read is
// dropped before; a deleted label's event is kept with label null.

// Ids a test can state literally: event ids, and the label and milestone
// a seeded event names that no longer exist.
const (
	firstItemEventID = 110001
	// DeletedLabelID is a label alpha's first issue carried and that was
	// deleted since.
	DeletedLabelID = 96999
	// deletedMilestoneID is a milestone deleted since.
	deletedMilestoneID = 97999
	// SecretLabel is ProjectSecret's label, which alpha's first issue
	// once carried and alice cannot read.
	SecretLabel = "secret-label"
)

// itemEventKinds maps each list's route to the kind it lists. Weight is
// an issue's only.
var itemEventKinds = map[string]string{"resource_label_events": "label", "resource_state_events": "state",
	"resource_milestone_events": "milestone", "resource_weight_events": "weight"}

// itemEvent is one resource event.
type itemEvent struct {
	kind        string // label, state, milestone or weight
	id          int64
	user        string
	at          time.Time
	action      string // add or remove
	labelID     int64
	milestoneID int64
	state       string
	commit      *string
	sourceMR    *int64
	weight      *int
	// itemState is the item's state when a milestone event was made.
	itemState string
}

// itemState is what an event records a change of.
type itemState struct {
	labels    []string
	milestone *gitlab.Milestone
	state     string
}

func issueState(iss *gitlab.Issue) itemState {
	return itemState{labels: slices.Clone(iss.Labels), milestone: iss.Milestone, state: iss.State}
}

func mrState(mr *gitlab.MergeRequest) itemState {
	return itemState{labels: slices.Clone(mr.Labels), milestone: mr.Milestone, state: mr.State}
}

// addEvent records one event on key, "issue:12" or "mr:3".
func (s *Server) addEvent(p *project, key string, e itemEvent) {
	e.id = s.nextEventID
	s.nextEventID++
	p.itemEvents[key] = append(p.itemEvents[key], e)
}

// recordChanges records the events a write made: labels added and
// removed, the milestone set or removed, and the state it moved to.
func (s *Server) recordChanges(p *project, key string, before, after itemState, user string) {
	now := s.opts.Now().UTC()
	for _, name := range after.labels {
		if !slices.Contains(before.labels, name) {
			s.addEvent(p, key, itemEvent{kind: "label", user: user, at: now, action: "add", labelID: s.labelID(p, name)})
		}
	}
	for _, name := range before.labels {
		if !slices.Contains(after.labels, name) {
			s.addEvent(p, key, itemEvent{kind: "label", user: user, at: now, action: "remove", labelID: s.labelID(p, name)})
		}
	}
	switch {
	case after.milestone != nil && (before.milestone == nil || before.milestone.ID != after.milestone.ID):
		s.addEvent(p, key, itemEvent{kind: "milestone", user: user, at: now, action: "add", milestoneID: after.milestone.ID,
			itemState: after.state})
	case after.milestone == nil && before.milestone != nil:
		s.addEvent(p, key, itemEvent{kind: "milestone", user: user, at: now, action: "remove", milestoneID: before.milestone.ID,
			itemState: after.state})
	}
	if before.state != "" && after.state != before.state {
		state := after.state
		if before.state == "closed" && state == "opened" {
			state = "reopened"
		}
		s.addEvent(p, key, itemEvent{kind: "state", user: user, at: now, state: state})
		if state == "closed" || state == "merged" {
			s.resolveTodos(p, key, user)
		}
	}
}

// labelID is the id of the label named name the project can use.
func (s *Server) labelID(p *project, name string) int64 {
	rows := slices.Clone(p.labels)
	for g := p.groupID; g != 0; g = s.parentOf(g) {
		rows = append(rows, s.groupLabels[g]...)
	}
	for _, l := range rows {
		if l.Name == name {
			return l.ID
		}
	}
	return 0
}

// groupReadable is GitLab's read_group: a public group is read by
// anyone; a private one by its members, its ancestors' members, and the
// members of a project in it or below it.
func (s *Server) groupReadable(id int64, user string) bool {
	var g *group
	for _, x := range s.groups {
		if x.id == id {
			g = x
		}
	}
	if g == nil || !g.private {
		return g != nil
	}
	for a := id; a != 0; a = s.parentOf(a) {
		if _, ok := s.groupLevels[a][user]; ok {
			return true
		}
	}
	for _, p := range s.projects {
		for a := p.groupID; a != 0; a = s.parentOf(a) {
			if a == id && p.members[user] {
				return true
			}
		}
	}
	return false
}

// SetGroupPrivate makes a group private.
func (s *Server) SetGroupPrivate(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.groups {
		if g.path == path {
			g.private = true
			return true
		}
	}
	return false
}

// eventLabel finds a label by id anywhere on the instance, and whether
// user may read it; nil when it was deleted.
func (s *Server) eventLabel(id int64, user string) (*gitlab.Label, bool) {
	for g, ls := range s.groupLabels {
		for _, l := range ls {
			if l.ID == id {
				return &l, s.groupReadable(g, user)
			}
		}
	}
	for _, q := range s.projects {
		for _, l := range q.labels {
			if l.ID == id {
				return &l, s.visible(q, user)
			}
		}
	}
	return nil, true
}

// eventMilestone finds a milestone by id anywhere on the instance, nil
// when it was deleted or user may not read it.
func (s *Server) eventMilestone(id int64, user string) *gitlab.ProjectMilestone {
	for g, ms := range s.groupMilestones {
		for _, m := range ms {
			if m.ID == id && s.groupReadable(g, user) {
				return &m
			}
		}
	}
	for _, q := range s.projects {
		for _, m := range q.milestones {
			if m.ID == id && s.visible(q, user) {
				return &m
			}
		}
	}
	return nil
}

// serveItemEvents answers GET …/resource_{label,state,milestone,weight}_events
// for the item key names; resourceType and resourceID are the item's.
func (s *Server) serveItemEvents(w http.ResponseWriter, r *http.Request, p *project, key, kind, resourceType string,
	resourceID int64, user string) {
	base := func(e itemEvent) map[string]any {
		row := map[string]any{"id": e.id, "user": s.user(e.user), "created_at": e.at}
		if kind == "weight" {
			row["issue_id"] = resourceID
		} else {
			row["resource_type"], row["resource_id"] = resourceType, resourceID
		}
		return row
	}
	var rows []map[string]any
	var hidden []bool
	for _, e := range p.itemEvents[key] {
		if e.kind != kind {
			continue
		}
		row := base(e)
		readable := true
		switch kind {
		case "label":
			l, ok := s.eventLabel(e.labelID, user)
			readable = ok
			row["action"], row["label"] = e.action, nil
			if l != nil {
				row["label"] = map[string]any{"id": l.ID, "name": l.Name, "color": l.Color, "description": l.Description,
					"text_color": "#FFFFFF", "archived": false}
			}
		case "state":
			row["state"], row["source_commit"], row["source_merge_request_id"] = e.state, e.commit, e.sourceMR
		case "milestone":
			// Dropped before paging, as the finder does.
			m := s.eventMilestone(e.milestoneID, user)
			if m == nil {
				continue
			}
			row["action"], row["milestone"], row["state"] = e.action, m, e.itemState
		case "weight":
			row["weight"] = e.weight
		}
		rows = append(rows, row)
		hidden = append(hidden, !readable)
	}
	start, end, ok := s.offsetPage(w, r, len(rows), false)
	if !ok {
		return
	}
	// Label events are filtered after the page is cut.
	out := []map[string]any{}
	for i := start; i < end; i++ {
		if !hidden[i] {
			out = append(out, rows[i])
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// fillItemEvents seeds the history of alpha's first issue and merge
// request, and a close on each closed issue. ProjectSecret gets a label
// for alpha's first issue to have carried.
func (s *Server) fillItemEvents(alpha, secret *project) {
	secretLabel := gitlab.Label{ID: 96300, Name: SecretLabel, Color: "#000000", IsProjectLabel: true}
	secret.labels = append(secret.labels, secretLabel)

	iss := alpha.issues[0]
	key := "issue:" + itoa(iss.IID)
	at := func(h int) time.Time { return iss.CreatedAt.Add(time.Duration(h) * time.Hour) }
	three := 3
	mrID := alpha.mrs[0].ID
	for _, e := range []itemEvent{
		{kind: "label", user: "bob", at: at(1), action: "add", labelID: s.labelID(alpha, "bug")},
		{kind: "label", user: "bob", at: at(2), action: "add", labelID: DeletedLabelID},
		{kind: "label", user: "bob", at: at(3), action: "add", labelID: secretLabel.ID},
		{kind: "milestone", user: "alice", at: at(4), action: "add", milestoneID: MilestoneActive, itemState: "opened"},
		{kind: "milestone", user: "alice", at: at(5), action: "add", milestoneID: deletedMilestoneID, itemState: "opened"},
		{kind: "weight", user: "carol", at: at(6), weight: &three},
		{kind: "state", user: "alice", at: at(7), state: "closed", sourceMR: &mrID},
		{kind: "state", user: "bob", at: at(8), state: "reopened"},
		{kind: "weight", user: "carol", at: at(9)},
		{kind: "label", user: "bob", at: at(10), action: "remove", labelID: DeletedLabelID},
	} {
		s.addEvent(alpha, key, e)
	}
	mr := alpha.mrs[0]
	key = "mr:" + itoa(mr.IID)
	s.addEvent(alpha, key, itemEvent{kind: "label", user: mr.Author.Username, at: mr.CreatedAt, action: "add",
		labelID: s.labelID(alpha, "feature")})
	s.addEvent(alpha, key, itemEvent{kind: "milestone", user: "alice", at: mr.CreatedAt.Add(time.Hour), action: "add",
		milestoneID: MilestoneGroup, itemState: "opened"})

	head := alpha.commits["main"][0].ID
	for _, i := range alpha.issues {
		if i.State == "closed" && i.ClosedAt != nil {
			s.addEvent(alpha, "issue:"+itoa(i.IID), itemEvent{kind: "state", user: "alice", at: *i.ClosedAt, state: "closed",
				commit: &head})
		}
	}
}

// AddLabelEvents adds n label events to an issue, or a merge request
// when mr is set, an hour apart from the hour after its newest event,
// adding and removing the label docs in turn.
func (s *Server) AddLabelEvents(projectPath string, mr bool, iid int64, n int) bool {
	return s.AddLabelEventsFor(projectPath, mr, iid, n, "docs")
}

// AddStateEvent adds a state event to an issue, or a merge request when
// mr is set, at a given time.
func (s *Server) AddStateEvent(projectPath string, mr bool, iid int64, state string, at time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil {
		return false
	}
	key := "issue:" + itoa(iid)
	if mr {
		key = "mr:" + itoa(iid)
	}
	s.addEvent(p, key, itemEvent{kind: "state", user: "dave", at: at.UTC(), state: state})
	return true
}

// AddLabelEventsFor is AddLabelEvents with a label named by the
// instance's first label of that name, in any project.
func (s *Server) AddLabelEventsFor(projectPath string, mr bool, iid int64, n int, label string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil {
		return false
	}
	key := "issue:" + itoa(iid)
	if mr {
		key = "mr:" + itoa(iid)
	}
	id := s.labelID(p, label)
	for _, q := range s.projects {
		for _, l := range q.labels {
			if id == 0 && l.Name == label {
				id = l.ID
			}
		}
	}
	at := p.CreatedAt
	for _, e := range p.itemEvents[key] {
		if e.at.After(at) {
			at = e.at
		}
	}
	for i := range n {
		action := "add"
		if i%2 == 1 {
			action = "remove"
		}
		s.addEvent(p, key, itemEvent{kind: "label", user: "dave", at: at.Add(time.Duration(i+1) * time.Hour), action: action,
			labelID: id})
	}
	return true
}
