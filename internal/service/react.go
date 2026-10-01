package service

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/mmedum/gitlab-mcp/v2/internal/gapi"
	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/v2/internal/model"
)

// The signed-in account's own emoji reactions on an issue, a merge
// request or a comment on either (§7.2, §18 row 106). GitLab answers
// every refused reaction with 404, one already there included, so the
// reactions are read first and an add or a remove that would change
// nothing is reported unchanged without a write. An add is never
// repeated; a lost answer is settled by reading. A remove takes the
// reaction's id from that read. The item's project is held to the write
// allow-list. register has already held type to its enum and iid and
// note_id to at least 1.

// maxAwardPages bounds the read of an item's reactions: GitLab lists
// them oldest first and cannot filter by account.
const maxAwardPages = 10

// emojiShape is a name GitLab could know: a TanukiEmoji alpha code
// ([_+\-a-z0-9]+, tanuki_emoji Character::ALPHA_CODE_REGEXP) or a
// custom emoji's ([a-z0-9_-]+, CustomEmoji::NAME_REGEXP).
var emojiShape = regexp.MustCompile(`^[a-z0-9_+-]{1,100}$`)

// emojiAliases are the aliases GitLab stores under another name that
// are common enough to look up by: TanukiEmoji's gemojione data gives
// thumbsup the alias +1 and thumbsdown -1. An alias not here still adds,
// and GitLab's answer names it.
var emojiAliases = map[string]string{"+1": "thumbsup", "-1": "thumbsdown"}

// reactionRefused is what GitLab's 404 to a reaction may mean.
const reactionRefused = "GitLab answers not found when it does not know the emoji, when the comment is one GitLab wrote " +
	"itself, and when you may not react there. Custom emoji exist only in projects in a group, defined on the group or " +
	"a parent group"

// emojiName reads an emoji's name as a caller may write it, :tada: or
// tada, and maps a known alias to GitLab's name.
func emojiName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if len(name) > 2 && strings.HasPrefix(name, ":") && strings.HasSuffix(name, ":") {
		name = name[1 : len(name)-1]
	}
	if !emojiShape.MatchString(name) {
		return "", gapi.Errf(gapi.ClassInvalid, "%q is not an emoji name: pass the name GitLab uses, lowercase letters, digits, "+
			"_, + and -, such as thumbsup or tada, not the emoji itself", raw)
	}
	if canonical, ok := emojiAliases[name]; ok {
		return canonical, nil
	}
	return name, nil
}

// Reaction is react's request.
type Reaction struct {
	Project string
	Type    string // issue or merge_request
	IID     int64
	NoteID  int64 // 0 for the item itself
	Emoji   string
	Remove  bool
}

// React adds the signed-in account's reaction to an issue, a merge
// request or a comment on one, or removes it.
func (s *Service) React(ctx context.Context, in Reaction) (model.ReactionWrite, error) {
	name, err := emojiName(in.Emoji)
	if err != nil {
		return model.ReactionWrite{}, err
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.ReactionWrite{}, err
	}
	a := gapi.Awardable{MergeRequest: in.Type == "merge_request", IID: in.IID, NoteID: in.NoteID}
	out := model.ReactionWrite{Outcome: "unchanged", Write: model.Write{Target: t.ref}, Type: in.Type, IID: in.IID,
		NoteID: in.NoteID, Emoji: name}
	mine, err := s.myAwards(ctx, t.p, a, name)
	if err != nil {
		return model.ReactionWrite{}, err
	}
	out.Reacted = mine.find(name) != nil
	if in.Remove {
		return s.unreact(ctx, t, a, mine, out)
	}
	if out.Reacted {
		out.Notes = []string{"You already reacted with " + name + " there, so nothing was sent."}
		return out, nil
	}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun, out.WouldSend = "dry_run", true, preview("POST", "react with "+name, []string{"name"})
		return out, nil
	}
	award, err := s.client.CreateAward(ctx, t.p, a, name)
	switch {
	case err == nil:
		out.Outcome, out.Emoji, out.Reacted = "added", award.Name, true
		return out, nil
	case gapi.IsClass(err, gapi.ClassConflict):
		// Added since the read, or under an alias of a name the read had.
		out.Reacted = true
		out.Notes = []string{"GitLab reports you already reacted with " + name + " there; it made no change."}
		return out, nil
	case gapi.IsClass(err, gapi.ClassAmbiguousOutcome):
		now, readErr := s.myAwards(ctx, t.p, a, name)
		if readErr == nil && now.find(name) != nil {
			out.Outcome, out.Reacted = "added", true
			out.Notes = []string{"GitLab's answer was lost, and a read shows your " + name + " reaction there."}
			return out, nil
		}
		return model.ReactionWrite{}, settle(err, "reaction", func() (string, error) { return "", readErr })
	case gapi.IsClass(err, gapi.ClassNotFound):
		return model.ReactionWrite{}, s.reactionRefused(ctx, t.p, a, err)
	}
	return model.ReactionWrite{}, err
}

// unreact removes the account's reaction found by the read, by its id.
func (s *Service) unreact(ctx context.Context, t target, a gapi.Awardable, mine awards, out model.ReactionWrite) (model.ReactionWrite, error) {
	award := mine.find(out.Emoji)
	if award == nil {
		if !mine.complete {
			return model.ReactionWrite{}, gapi.Errf(gapi.ClassUnsupported, "there are more than %d reactions there and yours with %s "+
				"was not among them; GitLab cannot list one account's reactions alone, so nothing was sent", maxAwardPages*gapi.MaxPerPage, out.Emoji)
		}
		note := "You have no " + out.Emoji + " reaction there, so nothing was sent."
		if len(mine.names) > 0 {
			note += " Yours there: " + strings.Join(mine.names, ", ") + ". GitLab keeps a reaction under its own name, " +
				"which may differ from an alias you gave."
		}
		out.Notes = []string{note}
		return out, nil
	}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun, out.WouldSend = "dry_run", true, preview("DELETE", "remove your "+out.Emoji+" reaction", nil)
		return out, nil
	}
	err := s.client.DeleteAward(ctx, t.p, a, award.ID)
	if err != nil && !gapi.IsClass(err, gapi.ClassNotFound) {
		return model.ReactionWrite{}, err
	}
	now, readErr := s.myAwards(ctx, t.p, a, out.Emoji)
	switch {
	case readErr != nil && err != nil:
		return model.ReactionWrite{}, err
	case readErr != nil:
		out.Outcome, out.Reacted = "removed", false
		out.Notes = []string{"GitLab accepted the removal; reading the reactions afterwards failed, so it is not confirmed."}
		return out, nil //nolint:nilerr // the removal was accepted; a failed read afterwards is said, not a failed call
	case now.find(out.Emoji) != nil:
		// GitLab answers 204 even when its service refuses the removal.
		return model.ReactionWrite{}, gapi.Errf(gapi.ClassUnexpected, "GitLab accepted the removal, but your %s reaction is still there", out.Emoji)
	}
	out.Outcome, out.Reacted = "removed", false
	if err != nil {
		out.Notes = []string{"GitLab answered the removal with not found, and a read afterwards finds no " + out.Emoji +
			" reaction of yours: it is gone, whether this call or another removed it."}
	}
	return out, nil
}

// reactionRefused explains GitLab's 404 to a reaction. A comment GitLab
// wrote itself is named when a read shows it is one.
func (s *Service) reactionRefused(ctx context.Context, p gapi.Project, a gapi.Awardable, err error) error {
	msg := gapi.AsError(err).Message
	if a.NoteID != 0 {
		get := s.client.GetIssueNote
		if a.MergeRequest {
			get = s.client.GetMergeRequestNote
		}
		if note, readErr := get(ctx, p, a.IID, a.NoteID); readErr == nil && note.System {
			return gapi.Wrap(gapi.ClassInvalid, err, "%s. That comment is a note GitLab wrote to record an event, which takes no reactions", msg)
		}
	}
	return gapi.Wrap(gapi.ClassNotFound, err, "%s. %s", msg, reactionRefused)
}

// awards is the account's own reactions on one item, as read.
type awards struct {
	rows     []gitlab.AwardEmoji
	names    []string
	complete bool // the read reached the end of the list
}

func (m awards) find(name string) *gitlab.AwardEmoji {
	if i := slices.IndexFunc(m.rows, func(a gitlab.AwardEmoji) bool { return a.Name == name }); i >= 0 {
		return &m.rows[i]
	}
	return nil
}

// myAwards reads the account's reactions on a, stopping early once the
// one named is found.
func (s *Service) myAwards(ctx context.Context, p gapi.Project, a gapi.Awardable, name string) (awards, error) {
	me, err := s.me(ctx)
	if err != nil {
		return awards{}, err
	}
	mine := func(row gitlab.AwardEmoji) bool { return row.User.ID == me.ID }
	rows, complete, err := readPagesUntil(maxAwardPages, func(o gapi.ListOptions) ([]gitlab.AwardEmoji, gapi.Page, error) {
		return s.client.ListAwards(ctx, p, a, o)
	}, func(rows []gitlab.AwardEmoji) bool {
		return slices.ContainsFunc(rows, func(row gitlab.AwardEmoji) bool { return mine(row) && row.Name == name })
	})
	if err != nil {
		return awards{}, err
	}
	out := awards{complete: complete}
	for _, row := range rows {
		if mine(row) {
			out.rows = append(out.rows, row)
			out.names = append(out.names, row.Name)
		}
	}
	return out, nil
}
