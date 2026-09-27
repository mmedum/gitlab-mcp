package gitlabtest

import (
	"crypto/sha1" //nolint:gosec // GitLab's line code is a SHA-1 of the path
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/mmedum/gitlab-mcp/internal/gitlab"
)

// Threads, replies and reviews on a merge request. A diff position is
// checked as GitLab checks it: the three SHAs must be the merge request's
// current diff_refs, and the line must be in that file's diff, or the
// note has no line code and GitLab refuses it (§2.9).

// ------------------------------------------------------------ positions

// diffLine is one line of a unified diff: its kind (' ', '+' or '-') and
// its numbers on each side. An added line's old number is where it
// lands in the old file, as GitLab's line code counts it.
type diffLine struct {
	kind     byte
	old, new int
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// diffLines walks a unified diff. GitLab takes a hunk's start numbers
// literally, so "@@ -0,0 +1,4 @@" starts the new side at 1.
func diffLines(text string) []diffLine {
	var out []diffLine
	var oldN, newN int
	in := false
	for line := range strings.SplitSeq(text, "\n") {
		if m := hunkHeader.FindStringSubmatch(line); m != nil {
			oldN, _ = strconv.Atoi(m[1])
			newN, _ = strconv.Atoi(m[2])
			in = true
			continue
		}
		if !in || line == "" {
			continue
		}
		switch line[0] {
		case ' ':
			out = append(out, diffLine{' ', oldN, newN})
			oldN++
			newN++
		case '+':
			out = append(out, diffLine{'+', oldN, newN})
			newN++
		case '-':
			out = append(out, diffLine{'-', oldN, newN})
			oldN++
		}
	}
	return out
}

// lineCode is GitLab's: the SHA-1 of the path, then the old and new
// line numbers.
func lineCode(path string, l diffLine) string {
	sum := sha1.Sum([]byte(path)) //nolint:gosec // GitLab's format
	return fmt.Sprintf("%s_%d_%d", hex.EncodeToString(sum[:]), l.old, l.new)
}

// findLine finds the diff line a position names: a new line is an added
// or context line's new number, an old line a removed or context line's
// old number, and both together a context line.
func findLine(lines []diffLine, pos *gitlab.Position) (diffLine, bool) {
	for _, l := range lines {
		switch {
		case pos.OldLine != nil && pos.NewLine != nil:
			if l.kind == ' ' && l.old == *pos.OldLine && l.new == *pos.NewLine {
				return l, true
			}
		case pos.NewLine != nil:
			if l.kind != '-' && l.new == *pos.NewLine {
				return l, true
			}
		case pos.OldLine != nil:
			if l.kind != '+' && l.old == *pos.OldLine {
				return l, true
			}
		}
	}
	return diffLine{}, false
}

// checkPosition validates a position against the merge request and
// answers GitLab's 400 when it is not one. It returns the line code, ""
// for a file-level position.
func (s *Server) checkPosition(w http.ResponseWriter, p *project, mr *gitlab.MergeRequest, pos *gitlab.Position) (string, bool) {
	var missing []string
	for _, f := range []struct{ name, v string }{{"base_sha", pos.BaseSHA}, {"start_sha", pos.StartSHA},
		{"head_sha", pos.HeadSHA}, {"position_type", pos.PositionType}} {
		if f.v == "" {
			missing = append(missing, "position["+f.name+"] is missing")
		}
	}
	if len(missing) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": strings.Join(missing, ", ")})
		return "", false
	}
	refs := mr.DiffRefs
	if refs == nil || pos.BaseSHA != refs.BaseSHA || pos.StartSHA != refs.StartSHA || pos.HeadSHA != refs.HeadSHA {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": map[string]any{"base": []string{"The diff position is not valid"}}})
		return "", false
	}
	noLine := map[string]any{"message": map[string]any{"line_code": []string{"can't be blank", "must be a valid line code"}}}
	var diff *gitlab.Diff
	for i, d := range p.mrDiffs[mr.IID] {
		if (pos.NewPath != "" && d.NewPath == pos.NewPath) || (pos.OldPath != "" && d.OldPath == pos.OldPath) {
			diff = &p.mrDiffs[mr.IID][i]
			break
		}
	}
	switch pos.PositionType {
	case "file":
		if diff == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": map[string]any{"position": []string{"is invalid"}}})
			return "", false
		}
		return "", true
	case "text":
		if diff == nil {
			writeJSON(w, http.StatusBadRequest, noLine)
			return "", false
		}
		l, ok := findLine(diffLines(diff.Diff), pos)
		if !ok {
			writeJSON(w, http.StatusBadRequest, noLine)
			return "", false
		}
		return lineCode(diff.NewPath, l), true
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": map[string]any{"position": []string{"is invalid"}}})
		return "", false
	}
}

// readPosition decodes an optional position field.
func readPosition(w http.ResponseWriter, b body) (*gitlab.Position, bool) {
	if !b.has("position") || b.isNull("position") {
		return nil, true
	}
	var pos gitlab.Position
	if err := json.Unmarshal(b["position"], &pos); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "position is invalid"})
		return nil, false
	}
	return &pos, true
}

// ------------------------------------------------------------ threads

func strPtr(s string) *string { return &s }

// startThread stores a new thread on a merge request, a diff thread when
// pos names a path.
func (s *Server) startThread(p *project, t target, user, text string, pos *gitlab.Position) gitlab.Discussion {
	typ := "DiscussionNote"
	if pos != nil && (pos.NewPath != "" || pos.OldPath != "") {
		typ = "DiffNote"
	} else {
		pos = nil
	}
	n := s.newNote(t, user, text, strPtr(typ), true, pos)
	key := t.key()
	d := gitlab.Discussion{ID: fakeSHA(key, "thread", itoa(n.ID)), Notes: []gitlab.Note{n}}
	p.discussions[key] = append(p.discussions[key], d)
	return d
}

func (s *Server) createDiscussion(w http.ResponseWriter, r *http.Request, p *project, mr *gitlab.MergeRequest, user string) {
	b, ok := readBody(w, r)
	if !ok || !b.require(w, "body") {
		return
	}
	pos, ok := readPosition(w, b)
	if !ok {
		return
	}
	if pos != nil {
		if _, ok := s.checkPosition(w, p, mr, pos); !ok {
			return
		}
	}
	text, _ := b.str("body")
	t := mrTarget(mr)
	cmds, kept := extract(text)
	summary := s.runCommands(p, t, cmds, user)
	if kept == "" && len(cmds) > 0 {
		writeJSON(w, http.StatusAccepted, map[string]any{"commands_changes": map[string]any{}, "summary": summary})
		return
	}
	writeJSON(w, http.StatusCreated, s.startThread(p, t, user, kept, pos))
}

func findDiscussion(p *project, key, id string) int {
	return slices.IndexFunc(p.discussions[key], func(d gitlab.Discussion) bool { return d.ID == id })
}

// reply appends a note to a thread, typed and resolvable as the thread's
// first note is. An unknown thread takes nothing.
func (s *Server) reply(p *project, t target, user, id, text string) gitlab.Note {
	key := t.key()
	i := findDiscussion(p, key, id)
	if i < 0 {
		return gitlab.Note{}
	}
	first := p.discussions[key][i].Notes[0]
	n := s.newNote(t, user, text, first.Type, first.Resolvable, first.Position)
	p.discussions[key][i].Notes = append(p.discussions[key][i].Notes, n)
	return n
}

func (s *Server) replyToDiscussion(w http.ResponseWriter, r *http.Request, p *project, t target, user, id string) {
	if findDiscussion(p, t.key(), id) < 0 {
		message(w, http.StatusNotFound, "404 Discussion Not Found")
		return
	}
	b, ok := readBody(w, r)
	if !ok || !b.require(w, "body") {
		return
	}
	text, _ := b.str("body")
	cmds, kept := extract(text)
	summary := s.runCommands(p, t, cmds, user)
	if kept == "" && len(cmds) > 0 {
		writeJSON(w, http.StatusAccepted, map[string]any{"commands_changes": map[string]any{}, "summary": summary})
		return
	}
	writeJSON(w, http.StatusCreated, s.reply(p, t, user, id, kept))
}

// setResolved resolves or reopens every resolvable note of a thread and
// reports whether it had one.
func (s *Server) setResolved(d *gitlab.Discussion, user string, resolved bool) bool {
	now := s.opts.Now().UTC()
	found := false
	for i := range d.Notes {
		n := &d.Notes[i]
		if !n.Resolvable {
			continue
		}
		found = true
		n.Resolved = resolved
		if resolved {
			by := s.user(user)
			n.ResolvedBy, n.ResolvedAt = &by, &now
		} else {
			n.ResolvedBy, n.ResolvedAt = nil, nil
		}
	}
	return found
}

func (s *Server) resolveDiscussion(w http.ResponseWriter, r *http.Request, p *project, mr *gitlab.MergeRequest, user, id string) {
	key := mrTarget(mr).key()
	i := findDiscussion(p, key, id)
	if i < 0 {
		message(w, http.StatusNotFound, "404 Discussion Not Found")
		return
	}
	b, ok := readBody(w, r)
	if !ok || !b.require(w, "resolved") {
		return
	}
	resolved, ok := b.boolean("resolved")
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "resolved is invalid"})
		return
	}
	d := p.discussions[key][i]
	d.Notes = slices.Clone(d.Notes)
	if !s.setResolved(&d, user, resolved) {
		message(w, http.StatusBadRequest, "400 Bad request - Discussion is not resolvable")
		return
	}
	p.discussions[key][i] = d
	writeJSON(w, http.StatusOK, d)
}

// ------------------------------------------------------------ drafts

// nextDraft is a draft id past every one the instance holds or held.
func (s *Server) nextDraft() int64 {
	if s.nextDraftID == 0 {
		s.nextDraftID = firstDraftID
		for _, p := range s.projects {
			for _, ds := range p.drafts {
				for _, d := range ds {
					s.nextDraftID = max(s.nextDraftID, d.ID+1)
				}
			}
		}
		// Past the ids fillReview reserves for every merge request.
		s.nextDraftID = max(s.nextDraftID, firstDraftID+int64(s.opts.AlphaMergeRequests)*10)
	}
	id := s.nextDraftID
	s.nextDraftID++
	return id
}

func (s *Server) createDraft(w http.ResponseWriter, r *http.Request, p *project, mr *gitlab.MergeRequest, user string) {
	b, ok := readBody(w, r)
	if !ok || !b.require(w, "note") {
		return
	}
	pos, ok := readPosition(w, b)
	if !ok {
		return
	}
	var code any
	if pos != nil {
		c, ok := s.checkPosition(w, p, mr, pos)
		if !ok {
			return
		}
		if c != "" {
			code = c
		}
	}
	d := gitlab.DraftNote{AuthorID: s.user(user).ID}
	if id, ok := b.str("in_reply_to_discussion_id"); ok && id != "" {
		if findDiscussion(p, mrTarget(mr).key(), id) < 0 {
			message(w, http.StatusNotFound, "404 Discussion Not Found")
			return
		}
		d.DiscussionID = &id
	}
	if c, ok := b.str("commit_id"); ok && c != "" {
		d.CommitID = &c
	}
	d.Note, _ = b.str("note")
	d.ResolveDiscussion, _ = b.boolean("resolve_discussion")
	d.Position = pos
	d.ID = s.nextDraft()
	p.drafts[mr.IID] = append(p.drafts[mr.IID], d)
	writeJSON(w, http.StatusCreated, withExtra(d, map[string]any{"merge_request_id": mr.ID, "line_code": code}))
}

func (s *Server) deleteDraft(w http.ResponseWriter, p *project, mr *gitlab.MergeRequest, user, id string) {
	uid := s.user(user).ID
	i := slices.IndexFunc(p.drafts[mr.IID], func(d gitlab.DraftNote) bool {
		return strconv.FormatInt(d.ID, 10) == id && d.AuthorID == uid
	})
	if i < 0 {
		message(w, http.StatusNotFound, "404 Not Found")
		return
	}
	p.drafts[mr.IID] = slices.Delete(p.drafts[mr.IID], i, i+1)
	w.WriteHeader(http.StatusNoContent)
}

// publishDrafts is bulk_publish: every draft of the user on the merge
// request becomes a comment, a reply or a thread, its quick actions run
// then as GitLab runs them, and the summary and reviewer state land.
func (s *Server) publishDrafts(w http.ResponseWriter, r *http.Request, p *project, mr *gitlab.MergeRequest, user string) {
	b, ok := readBody(w, r)
	if !ok || !b.valid(w, "reviewer_state", "requested_changes", "reviewed", "approved") {
		return
	}
	t := mrTarget(mr)
	uid := s.user(user).ID
	var mine []gitlab.DraftNote
	p.drafts[mr.IID] = slices.DeleteFunc(p.drafts[mr.IID], func(d gitlab.DraftNote) bool {
		if d.AuthorID == uid {
			mine = append(mine, d)
			return true
		}
		return false
	})
	slices.SortFunc(mine, func(a, b gitlab.DraftNote) int { return int(a.ID - b.ID) })
	for _, d := range mine {
		cmds, kept := extract(d.Note)
		s.runCommands(p, t, cmds, user)
		switch {
		case d.DiscussionID != nil:
			if kept != "" {
				s.reply(p, t, user, *d.DiscussionID, kept)
			}
			if i := findDiscussion(p, t.key(), *d.DiscussionID); i >= 0 && d.ResolveDiscussion {
				s.setResolved(&p.discussions[t.key()][i], user, true)
			}
		case kept != "":
			s.startThread(p, t, user, kept, d.Position)
		}
	}
	if summary, _ := b.str("note"); strings.TrimSpace(summary) != "" {
		s.addNote(p, t, user, summary)
	}
	if state, ok := b.str("reviewer_state"); ok && state != "" {
		if s.reviewerStates == nil {
			s.reviewerStates = map[string]string{}
		}
		s.reviewerStates[reviewerKey(p.PathWithNamespace, mr.IID, user)] = state
		if a := p.approvals[mr.IID]; state == "approved" && a != nil &&
			!slices.ContainsFunc(a.ApprovedBy, func(x gitlab.Approver) bool { return x.User.Username == user }) {
			a.ApprovedBy = append(a.ApprovedBy, gitlab.Approver{User: s.user(user)})
			a.Approved = true
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
