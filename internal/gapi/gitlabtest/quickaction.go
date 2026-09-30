package gitlabtest

import (
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
)

// Quick actions run from a note, a description or a published draft sent
// through the API (§2.8). The model is small on purpose: /close and
// /reopen change the issue or merge request, /label and /unlabel change
// its labels, other known commands are consumed without effect, fenced
// code is skipped, and a note that is only commands answers 202 and saves
// nothing. That is enough for a test to fail when the server's guard is
// missing.

var commandLine = regexp.MustCompile(`^/([a-z_]+)(?:\s+(.*))?$`)

var knownCommands = map[string]bool{
	"close": true, "reopen": true, "label": true, "unlabel": true, "assign": true, "unassign": true,
	"merge": true, "approve": true, "milestone": true, "due": true, "title": true, "lock": true, "move": true,
}

// labelRef is one label in a /label line: ~name, or ~"name with spaces".
var labelRef = regexp.MustCompile(`~"([^"]+)"|~([^\s~"]+)`)

// command is one quick-action line: its name and the rest of the line.
type command struct{ name, args string }

// extract splits a body into the commands it runs and the text it keeps.
func extract(body string) ([]command, string) {
	var cmds []command
	var kept []string
	fenced := false
	for line := range strings.SplitSeq(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
		}
		if m := commandLine.FindStringSubmatch(line); !fenced && m != nil && knownCommands[m[1]] {
			cmds = append(cmds, command{name: m[1], args: strings.TrimSpace(m[2])})
			continue
		}
		kept = append(kept, line)
	}
	return cmds, strings.TrimSpace(strings.Join(kept, "\n"))
}

func labelArgs(args string) []string {
	matches := labelRef.FindAllStringSubmatch(args, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1]+m[2])
	}
	return out
}

// target is the issue or merge request a note or a quick action lands on:
// pointers into the stored record, so one model serves both.
type target struct {
	kind      string // "issue" or "mr", the discussions key's prefix
	noun      string // "issue" or "merge request", for the command summary
	typ       string // "Issue" or "MergeRequest"
	id, iid   int64
	state     *string
	closedAt  **time.Time
	closedBy  **gitlab.UserBasic // nil for a merge request
	labels    *[]string
	updatedAt *time.Time
	notes     *int
}

func issueTarget(iss *gitlab.Issue) target {
	return target{kind: "issue", noun: "issue", typ: "Issue", id: iss.ID, iid: iss.IID, state: &iss.State,
		closedAt: &iss.ClosedAt, closedBy: &iss.ClosedBy, labels: &iss.Labels, updatedAt: &iss.UpdatedAt,
		notes: &iss.UserNotesCount}
}

func mrTarget(mr *gitlab.MergeRequest) target {
	return target{kind: "mr", noun: "merge request", typ: "MergeRequest", id: mr.ID, iid: mr.IID, state: &mr.State,
		closedAt: &mr.ClosedAt, labels: &mr.Labels, updatedAt: &mr.UpdatedAt, notes: &mr.UserNotesCount}
}

func (t target) key() string { return t.kind + ":" + itoa(t.iid) }

// runCommands applies commands to t and returns GitLab's summary lines.
func (s *Server) runCommands(p *project, t target, cmds []command, user string) []string {
	now := s.opts.Now().UTC()
	before := itemState{labels: slices.Clone(*t.labels), state: *t.state}
	defer func() { s.recordChanges(p, t.key(), before, itemState{labels: *t.labels, state: *t.state}, user) }()
	var summary []string
	for _, c := range cmds {
		switch c.name {
		case "close":
			if *t.state != "merged" {
				*t.state, *t.closedAt = "closed", &now
				if t.closedBy != nil {
					by := s.user(user)
					*t.closedBy = &by
				}
				summary = append(summary, "Closed this "+t.noun+".")
			}
		case "reopen":
			if *t.state == "closed" {
				*t.state, *t.closedAt = "opened", nil
				if t.closedBy != nil {
					*t.closedBy = nil
				}
				summary = append(summary, "Reopened this "+t.noun+".")
			}
		case "label":
			for _, name := range labelArgs(c.args) {
				s.ensureLabel(p, name)
				if !slices.Contains(*t.labels, name) {
					*t.labels = append(*t.labels, name)
				}
				summary = append(summary, "Added ~"+name+" label.")
			}
		case "unlabel":
			names := labelArgs(c.args)
			*t.labels = slices.DeleteFunc(*t.labels, func(l string) bool { return len(names) == 0 || slices.Contains(names, l) })
			summary = append(summary, "Removed labels.")
		}
		*t.updatedAt = now
	}
	return summary
}

// ensureLabel creates a label the project cannot see yet, as GitLab does
// when one is assigned by name.
func (s *Server) ensureLabel(p *project, name string) {
	if slices.ContainsFunc(p.labels, func(l gitlab.Label) bool { return l.Name == name }) {
		return
	}
	for g := p.groupID; g != 0; g = s.parentOf(g) {
		if slices.ContainsFunc(s.groupLabels[g], func(l gitlab.Label) bool { return l.Name == name }) {
			return
		}
	}
	// Label ids are instance-wide, so the next one is past every project's.
	next := int64(96200)
	for _, q := range s.projects {
		for _, l := range q.labels {
			next = max(next, l.ID+1)
		}
	}
	zero, zero2, zero3 := 0, 0, 0
	p.labels = append(p.labels, gitlab.Label{ID: next, Name: name, Color: "#6699cc", IsProjectLabel: true,
		OpenIssuesCount: &zero, ClosedIssuesCount: &zero2, OpenMergeRequestsCount: &zero3})
}

// bump moves an updated_at to now, strictly later than it was, so a
// test can tell a change happened even on a frozen clock.
func bump(at *time.Time, now time.Time) {
	if now.After(*at) {
		*at = now
		return
	}
	*at = at.Add(time.Nanosecond)
}

// addNote runs a body's quick actions and, unless it was only commands,
// saves the rest as a new individual note on t. onlyCommands reports the
// 202 case, where nothing was saved.
func (s *Server) addNote(p *project, t target, user, body string) (note gitlab.Note, summary []string, onlyCommands bool) {
	cmds, kept := extract(body)
	summary = s.runCommands(p, t, cmds, user)
	if kept == "" && len(cmds) > 0 {
		return gitlab.Note{}, summary, true
	}
	return s.storeNote(p, t, user, kept), summary, false
}

// storeNote saves body as a new individual note on t, as its own
// discussion.
func (s *Server) storeNote(p *project, t target, user, body string) gitlab.Note {
	note := s.newNote(t, user, body, nil, false, nil)
	p.discussions[t.key()] = append(p.discussions[t.key()], gitlab.Discussion{ID: fakeSHA(t.key(), itoa(note.ID)),
		IndividualNote: true, Notes: []gitlab.Note{note}})
	s.resolveTodos(p, t.key(), user)
	return note
}

// newNote makes a note on t and counts it; it does not store it.
func (s *Server) newNote(t target, user, body string, typ *string, resolvable bool, pos *gitlab.Position) gitlab.Note {
	now := s.opts.Now().UTC()
	iid := t.iid
	n := gitlab.Note{ID: s.nextNoteID, Type: typ, Body: body, Author: s.user(user), CreatedAt: now, UpdatedAt: now,
		NoteableID: t.id, NoteableType: t.typ, NoteableIID: &iid, Resolvable: resolvable, Position: pos}
	s.nextNoteID++
	*t.notes++
	bump(t.updatedAt, now)
	return n
}

// createNote is POST …/notes on an issue or a merge request.
func (s *Server) createNote(w http.ResponseWriter, r *http.Request, p *project, t target, user string) {
	b, ok := readBody(w, r)
	if !ok || !b.require(w, "body") {
		return
	}
	body, _ := b.str("body")
	note, summary, onlyCommands := s.addNote(p, t, user, body)
	if onlyCommands {
		writeJSON(w, http.StatusAccepted, map[string]any{"commands_changes": map[string]any{}, "summary": summary})
		return
	}
	writeJSON(w, http.StatusCreated, note)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
