package gitlabtest

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/v4/internal/gitlab"
)

// The Premium planning objects: epics and iterations.

// Fixture epics and iterations tests address. EpicLaunch is GroupTop's
// &1, the parent of EpicRetire (&2, closed) and of EpicSub, GroupSub's &1.
const (
	EpicLaunch = 1
	EpicRetire = 2
	EpicSub    = 1
	// EpicLaunchText is EpicLaunch's description.
	EpicLaunchText = "Ship the new login.\n\nIssues #1 and #2 track it."
	// IterationClosed, IterationCurrent and IterationUpcoming are
	// GroupTop's; IterationSub is GroupSub's.
	IterationClosed   = 97001
	IterationCurrent  = 97002
	IterationUpcoming = 97003
	IterationSub      = 97004
)

// fillEpics gives GroupTop and GroupSub epics and iterations. EpicLaunch
// holds ProjectAlpha's first two issues.
func (s *Server) fillEpics(p *project) {
	at := p.CreatedAt.Add(48 * time.Hour)
	top, sub := int64(firstGroupID), int64(firstGroupID+1)
	one := int64(EpicLaunch)
	epic := func(g int64, path string, iid int64, title, desc, state, author string, parent *int64, labels ...string) gitlab.Epic {
		return gitlab.Epic{ID: 98000 + g*10 + iid, IID: iid, GroupID: g, ParentIID: parent, Title: title, Description: desc,
			State: state, Labels: labels, Author: s.user(author), StartDate: "2026-01-05", DueDate: "2026-03-31",
			Reference: "&" + strconv.FormatInt(iid, 10), WebURL: s.URL + "/groups/" + path + "/-/epics/" + strconv.FormatInt(iid, 10),
			CreatedAt: at, UpdatedAt: at}
	}
	s.epics = map[int64][]gitlab.Epic{
		top: {
			epic(top, GroupTop, EpicLaunch, "Launch the login", EpicLaunchText, "opened", "bob", nil, "feature"),
			epic(top, GroupTop, EpicRetire, "Retire the old login", "Remove it once the new one ships.", "closed", "carol", &one),
		},
		sub: {epic(sub, GroupSub, EpicSub, "Sub-group login polish", "Small fixes.", "opened", "alice", &one, "docs")},
	}
	s.epicIssues = map[int64][]*gitlab.Issue{98000 + top*10 + EpicLaunch: {p.issues[0], p.issues[1]}}
	// A parent is a group's epic: its iid alone names it only within that
	// group, so each child records the parent's group.
	s.epicParentGroup = map[int64]int64{98000 + top*10 + EpicRetire: top, 98000 + sub*10 + EpicSub: top}

	it := func(g int64, path string, id int64, iid int64, seq int, title string, state int, start, due string) gitlab.Iteration {
		return gitlab.Iteration{ID: id, IID: iid, GroupID: g, Sequence: seq, Title: title, State: state, StartDate: start,
			DueDate: due, WebURL: s.URL + "/groups/" + path + "/-/iterations/" + strconv.FormatInt(id, 10), UpdatedAt: at}
	}
	s.iterations = map[int64][]gitlab.Iteration{
		top: {
			it(top, GroupTop, IterationClosed, 1, 1, "Sprint A", 3, "2026-01-05", "2026-01-16"),
			it(top, GroupTop, IterationCurrent, 2, 2, "", 2, "2026-01-19", "2026-01-30"),
			it(top, GroupTop, IterationUpcoming, 3, 3, "Sprint C", 1, "2026-02-02", "2026-02-13"),
		},
		sub: {it(sub, GroupSub, IterationSub, 1, 1, "Sub sprint", 2, "2026-01-19", "2026-01-30")},
	}
}

// premium answers 403 on a Free instance, as GitLab does for a Premium
// feature, and reports whether it did.
func (s *Server) premium(w http.ResponseWriter) bool {
	if s.opts.Free {
		message(w, http.StatusForbidden, "403 Forbidden")
		return true
	}
	return false
}

// serveEpics serves GET /groups/:id/epics and an epic, its children and
// its issues. rest is the path after epics.
func (s *Server) serveEpics(w http.ResponseWriter, r *http.Request, id string, rest []string) {
	g := s.findGroup(id)
	if g == nil {
		message(w, http.StatusNotFound, "404 Group Not Found")
		return
	}
	if s.premium(w) {
		return
	}
	if len(rest) == 0 {
		writePage(s, w, r, s.filterEpics(g.id, r))
		return
	}
	iid, err := strconv.ParseInt(rest[0], 10, 64)
	i := slices.IndexFunc(s.epics[g.id], func(e gitlab.Epic) bool { return e.IID == iid })
	if err != nil || i < 0 {
		message(w, http.StatusNotFound, "404 Not found")
		return
	}
	e := s.epics[g.id][i]
	switch {
	case len(rest) == 1:
		writeJSON(w, http.StatusOK, e)
	case len(rest) == 2 && rest[1] == "epics":
		// GitLab answers an epic's children unpaged.
		children := []gitlab.Epic{}
		for _, es := range s.epics {
			for _, c := range es {
				if c.ParentIID != nil && *c.ParentIID == e.IID && s.epicParentGroup[c.ID] == g.id {
					children = append(children, c)
				}
			}
		}
		slices.SortFunc(children, func(a, b gitlab.Epic) int { return int(a.ID - b.ID) })
		writeJSON(w, http.StatusOK, children)
	case len(rest) == 2 && rest[1] == "issues":
		rows := []map[string]any{}
		for _, is := range s.epicIssues[e.ID] {
			rows = append(rows, map[string]any{"id": is.ID, "iid": is.IID, "project_id": is.ProjectID, "title": is.Title,
				"state": is.State, "web_url": is.WebURL})
		}
		writePage(s, w, r, rows)
	default:
		routeNotFound(w)
	}
}

// filterEpics is a group's epics, and its subgroups' unless
// include_descendant_groups is false, newest first, filtered as GitLab
// filters them.
func (s *Server) filterEpics(group int64, r *http.Request) []gitlab.Epic {
	q := r.URL.Query()
	rows := slices.Clone(s.epics[group])
	if q.Get("include_descendant_groups") != "false" {
		for gid, es := range s.epics {
			for a := s.parentOf(gid); a != 0; a = s.parentOf(a) {
				if a == group {
					rows = append(rows, es...)
					break
				}
			}
		}
	}
	labels := strings.Split(q.Get("labels"), ",")
	term := strings.ToLower(q.Get("search"))
	rows = slices.DeleteFunc(rows, func(e gitlab.Epic) bool {
		switch {
		case q.Get("state") != "" && q.Get("state") != "all" && e.State != q.Get("state"):
			return true
		case term != "" && !strings.Contains(strings.ToLower(e.Title+" "+e.Description), term):
			return true
		case q.Get("author_username") != "" && e.Author.Username != q.Get("author_username"):
			return true
		}
		for _, l := range labels {
			if l != "" && !slices.Contains(e.Labels, l) {
				return true
			}
		}
		return false
	})
	slices.SortStableFunc(rows, func(a, b gitlab.Epic) int { return int(b.ID - a.ID) })
	return rows
}

// iterationStates are what each state filter keeps, by GitLab's numbers:
// 1 upcoming, 2 current, 3 closed; opened is upcoming and current.
var iterationStates = map[string][]int{"opened": {1, 2}, "upcoming": {1}, "current": {2}, "closed": {3}}

// serveIterations serves a group's iterations, and a project's, which
// are its group's: with the groups above unless include_ancestors is
// false, GitLab's default being true.
func (s *Server) serveIterations(w http.ResponseWriter, r *http.Request, group int64) {
	if s.premium(w) {
		return
	}
	q := r.URL.Query()
	rows := slices.Clone(s.iterations[group])
	if q.Get("include_ancestors") != "false" {
		for g := s.parentOf(group); g != 0; g = s.parentOf(g) {
			rows = append(rows, s.iterations[g]...)
		}
	}
	keep, filtered := iterationStates[q.Get("state")]
	term := strings.ToLower(q.Get("search"))
	rows = slices.DeleteFunc(rows, func(it gitlab.Iteration) bool {
		return filtered && !slices.Contains(keep, it.State) || term != "" && !strings.Contains(strings.ToLower(it.Title), term)
	})
	writePage(s, w, r, rows)
}
