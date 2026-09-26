package gitlabtest

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/gitlab"
)

// Quick actions run from a note sent through the API (§2.8). The model
// is small on purpose: /close and /reopen change the issue, other known
// commands are consumed without effect, fenced code is skipped, and a
// note that is only commands answers 202 and saves nothing. That is
// enough for a test to fail when the server's guard is missing.

var commandLine = regexp.MustCompile(`^/([a-z_]+)(?:\s.*)?$`)

var knownCommands = map[string]bool{
	"close": true, "reopen": true, "label": true, "unlabel": true, "assign": true, "unassign": true,
	"merge": true, "approve": true, "milestone": true, "due": true, "title": true, "lock": true, "move": true,
}

// extract splits a body into the commands it runs and the text it keeps.
func extract(body string) ([]string, string) {
	var cmds, kept []string
	fenced := false
	for line := range strings.SplitSeq(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
		}
		if m := commandLine.FindStringSubmatch(line); !fenced && m != nil && knownCommands[m[1]] {
			cmds = append(cmds, m[1])
			continue
		}
		kept = append(kept, line)
	}
	return cmds, strings.TrimSpace(strings.Join(kept, "\n"))
}

func (s *Server) createIssueNote(w http.ResponseWriter, r *http.Request, p *project, iss *gitlab.Issue, user string) {
	var in struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Body) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "body is missing"})
		return
	}
	cmds, kept := extract(in.Body)
	now := s.opts.Now().UTC()
	var summary []string
	for _, c := range cmds {
		switch c {
		case "close":
			iss.State, iss.ClosedAt = "closed", &now
			by := s.user(user)
			iss.ClosedBy = &by
			summary = append(summary, "Closed this issue.")
		case "reopen":
			iss.State, iss.ClosedAt, iss.ClosedBy = "opened", nil, nil
			summary = append(summary, "Reopened this issue.")
		}
		iss.UpdatedAt = now
	}
	if kept == "" && len(cmds) > 0 {
		writeJSON(w, http.StatusAccepted, map[string]any{"commands_changes": map[string]any{}, "summary": summary})
		return
	}
	iid := iss.IID
	note := gitlab.Note{ID: s.nextNoteID, Body: kept, Author: s.user(user), CreatedAt: now, UpdatedAt: now,
		NoteableID: iss.ID, NoteableType: "Issue", NoteableIID: &iid}
	s.nextNoteID++
	key := "issue:" + itoa(iid)
	p.discussions[key] = append(p.discussions[key], gitlab.Discussion{ID: fakeSHA(key, itoa(note.ID)),
		IndividualNote: true, Notes: []gitlab.Note{note}})
	iss.UserNotesCount++
	iss.UpdatedAt = now.Add(time.Nanosecond)
	writeJSON(w, http.StatusCreated, note)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
