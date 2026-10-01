package gitlabtest

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
)

// Emoji reactions on an issue, a merge request or a comment on one, as
// lib/api/award_emoji.rb serves them at v19.4.1-ee. The list is oldest
// first. Every refused POST is 404 with the reason folded into the
// message: an unknown name, a reaction already there, a system note, an
// item the user may not read. A name is normalized first, so +1 is kept
// as thumbsup. DELETE removes only the user's own reaction, 401 for
// another's, and honors If-Unmodified-Since. A reaction moves a
// comment's updated_at (Note#bump_updated_at) and leaves an issue's or a
// merge request's alone, whose upvotes and downvotes count thumbsup and
// thumbsdown. A reaction on the item, or on a comment outside a thread,
// marks the user's pending to-dos on the item done
// (TodoService#new_award_emoji). Custom emoji are not modeled.

// award is one reaction.
type award struct {
	id      int64
	project int64
	item    string // the target's key, "issue:3"
	noteID  int64  // 0 for the item itself
	name    string
	user    string
	created time.Time
}

// emojiNames are the names this instance knows; GitLab knows thousands.
var emojiNames = []string{"thumbsup", "thumbsdown", "tada", "heart", "eyes", "rocket", "smile", "confused"}

// emojiAliases maps an alias to the name GitLab stores.
var emojiAliases = map[string]string{"+1": "thumbsup", "-1": "thumbsdown", "thumbs_up": "thumbsup"}

// normalizeEmoji is AwardEmojis::BaseService#normalize_name: TanukiEmoji
// takes :name: and name alike, and an alias becomes its emoji's name.
func normalizeEmoji(name string) (string, bool) {
	if len(name) > 2 && strings.HasPrefix(name, ":") && strings.HasSuffix(name, ":") {
		name = name[1 : len(name)-1]
	}
	if c, ok := emojiAliases[name]; ok {
		return c, true
	}
	return name, slices.Contains(emojiNames, name)
}

// serveAwards routes the award_emoji routes under one item; it reports
// whether it answered. readable is whether the user may read the item.
func (s *Server) serveAwards(w http.ResponseWriter, r *http.Request, p *project, t target, readable bool, user string, rest []string) bool {
	var noteID int64
	switch {
	case len(rest) >= 3 && rest[0] == "notes" && rest[2] == "award_emoji":
		di, ni := findNote(p, t, rest[1])
		if di < 0 {
			message(w, http.StatusNotFound, "404 Not found")
			return true
		}
		noteID = p.discussions[t.key()][di].Notes[ni].ID
		rest = rest[2:]
	case len(rest) >= 1 && rest[0] == "award_emoji":
	default:
		return false
	}
	switch {
	case r.Method == http.MethodGet && len(rest) == 1:
		if !readable {
			message(w, http.StatusNotFound, "404 Award Emoji Not Found")
			return true
		}
		var rows []map[string]any
		for _, a := range s.awards {
			if a.project == p.ID && a.item == t.key() && a.noteID == noteID {
				rows = append(rows, s.awardJSON(a, t))
			}
		}
		writePage(s, w, r, rows)
	case r.Method == http.MethodPost && len(rest) == 1:
		s.addAward(w, r, p, t, noteID, readable, user)
	case r.Method == http.MethodDelete && len(rest) == 2:
		s.removeAward(w, r, p, t, noteID, user, rest[1])
	default:
		routeNotFound(w)
	}
	return true
}

func (s *Server) addAward(w http.ResponseWriter, r *http.Request, p *project, t target, noteID int64, readable bool, user string) {
	b, ok := readBody(w, r)
	if !ok || !b.require(w, "name") {
		return
	}
	if !readable {
		message(w, http.StatusNotFound, "404 Award Emoji Not Found")
		return
	}
	var note *gitlab.Note
	if noteID != 0 {
		di, ni := findNote(p, t, itoa(noteID))
		note = &p.discussions[t.key()][di].Notes[ni]
		if note.System {
			message(w, http.StatusNotFound, "404 Award Emoji Awardable cannot add emoji reactions Not Found")
			return
		}
	}
	raw, _ := b.str("name")
	name, known := normalizeEmoji(raw)
	if !known {
		message(w, http.StatusNotFound, "404 Award Emoji Name is not a valid emoji name Not Found")
		return
	}
	for _, a := range s.awards {
		if a.project == p.ID && a.item == t.key() && a.noteID == noteID && a.user == user && a.name == name {
			message(w, http.StatusNotFound, "404 Award Emoji Name has already been taken Not Found")
			return
		}
	}
	s.nextAwardID++
	a := award{id: firstAwardID + s.nextAwardID, project: p.ID, item: t.key(), noteID: noteID, name: name, user: user,
		created: s.opts.Now().UTC()}
	s.awards = append(s.awards, a)
	s.awardChanged(p, t, note, name, 1)
	if note == nil || s.individual(p, t, noteID) {
		s.resolveTodos(p, t.key(), user)
	}
	writeJSON(w, http.StatusCreated, s.awardJSON(a, t))
}

func (s *Server) removeAward(w http.ResponseWriter, r *http.Request, p *project, t target, noteID int64, user, id string) {
	i := slices.IndexFunc(s.awards, func(a award) bool {
		return a.project == p.ID && a.item == t.key() && a.noteID == noteID && itoa(a.id) == id
	})
	if i < 0 {
		message(w, http.StatusNotFound, "404 Not found")
		return
	}
	a := s.awards[i]
	if a.user != user {
		message(w, http.StatusUnauthorized, "401 Unauthorized")
		return
	}
	if since, err := time.Parse(time.RFC3339Nano, r.Header.Get("If-Unmodified-Since")); err == nil && a.created.After(since) {
		message(w, http.StatusPreconditionFailed, "412 Precondition Failed")
		return
	}
	s.awards = slices.Delete(s.awards, i, i+1)
	var note *gitlab.Note
	if noteID != 0 {
		di, ni := findNote(p, t, itoa(noteID))
		note = &p.discussions[t.key()][di].Notes[ni]
	}
	s.awardChanged(p, t, note, a.name, -1)
	w.WriteHeader(http.StatusNoContent)
}

// awardChanged moves what a reaction moves: a comment's updated_at, or
// an item's vote counts.
func (s *Server) awardChanged(p *project, t target, note *gitlab.Note, name string, by int) {
	if note != nil {
		note.UpdatedAt = s.opts.Now().UTC()
		return
	}
	up, down := s.votes(p, t)
	switch name {
	case "thumbsup":
		*up += by
	case "thumbsdown":
		*down += by
	}
}

// votes points at an item's upvotes and downvotes.
func (s *Server) votes(p *project, t target) (up, down *int) {
	if t.kind == "mr" {
		mr := findMR(p, itoa(t.iid))
		return &mr.Upvotes, &mr.Downvotes
	}
	iss := findIssue(p, itoa(t.iid))
	return &iss.Upvotes, &iss.Downvotes
}

// individual reports whether a comment stands outside a thread.
func (s *Server) individual(p *project, t target, noteID int64) bool {
	di, _ := findNote(p, t, itoa(noteID))
	return di >= 0 && p.discussions[t.key()][di].IndividualNote
}

// reacted reports whether user reacted on the item itself, which makes
// them a participant.
func (s *Server) reacted(p *project, t target, user string) bool {
	return slices.ContainsFunc(s.awards, func(a award) bool {
		return a.project == p.ID && a.item == t.key() && a.noteID == 0 && a.user == user
	})
}

// awardJSON is Entities::AwardEmoji.
func (s *Server) awardJSON(a award, t target) map[string]any {
	typ, id := t.typ, t.id
	if a.noteID != 0 {
		typ, id = "Note", a.noteID
	}
	return withExtra(gitlab.AwardEmoji{ID: a.id, Name: a.name, User: s.user(a.user), CreatedAt: a.created, UpdatedAt: a.created},
		map[string]any{"awardable_id": id, "awardable_type": typ, "url": nil})
}

// React adds user's reaction named name on an item, or on its comment
// noteID when that is not 0, as another user's call would.
func (s *Server) React(projectPath, kind string, iid, noteID int64, user, name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil {
		return false
	}
	t, ok := s.findTarget(p, kind, iid)
	if !ok {
		return false
	}
	s.nextAwardID++
	s.awards = append(s.awards, award{id: firstAwardID + s.nextAwardID, project: p.ID, item: t.key(), noteID: noteID, name: name,
		user: user, created: s.opts.Now().UTC()})
	var note *gitlab.Note
	if noteID != 0 {
		if di, ni := findNote(p, t, itoa(noteID)); di >= 0 {
			note = &p.discussions[t.key()][di].Notes[ni]
		}
	}
	s.awardChanged(p, t, note, name, 1)
	return true
}

// Reactions lists the reactions on an item, or on its comment noteID
// when that is not 0, as "user:name", oldest first.
func (s *Server) Reactions(projectPath, kind string, iid, noteID int64) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil {
		return nil
	}
	t, ok := s.findTarget(p, kind, iid)
	if !ok {
		return nil
	}
	var out []string
	for _, a := range s.awards {
		if a.project == p.ID && a.item == t.key() && a.noteID == noteID {
			out = append(out, a.user+":"+a.name)
		}
	}
	return out
}

// findTarget is an issue ("issue") or a merge request ("mr") by iid.
func (s *Server) findTarget(p *project, kind string, iid int64) (target, bool) {
	if kind == "mr" {
		if mr := findMR(p, itoa(iid)); mr != nil {
			return mrTarget(mr), true
		}
		return target{}, false
	}
	if iss := findIssue(p, itoa(iid)); iss != nil {
		return issueTarget(iss), true
	}
	return target{}, false
}
