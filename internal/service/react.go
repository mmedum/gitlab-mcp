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
// every refused reaction with 404, one already there included, and words
// the reason in the account's language, so the reactions are read first
// and again after a refusal: an add or a remove that would change nothing
// is reported unchanged without a write, and no answer's text is read.
// An add is never repeated; a lost answer is settled by the account's
// reactions before and after. A remove takes the reaction's id from the
// read and reads that id back. The item's project is held to the write
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
const reactionRefused = "GitLab answers not found when it does not know the emoji, when you already reacted with it under " +
	"another of its names, when the comment is one GitLab wrote itself, and when you may not react there, such as on a " +
	"locked discussion of a project you are not a member of. Custom emoji exist only in projects in a group, defined on " +
	"the group or a parent group"

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
	var t target
	var me *gitlab.User
	if err := parallel(
		func() (err error) { t, err = s.writeTarget(ctx, in.Project); return err },
		func() (err error) { me, err = s.me(ctx); return err },
	); err != nil {
		return model.ReactionWrite{}, err
	}
	r := reacting{s: s, t: t, a: gapi.Awardable{MergeRequest: in.Type == "merge_request", IID: in.IID, NoteID: in.NoteID}, me: me.ID}
	out := model.ReactionWrite{Outcome: "unchanged", Write: model.Write{Target: t.ref}, Type: in.Type, IID: in.IID,
		NoteID: in.NoteID, Emoji: name}
	before, err := r.read(ctx)
	if err != nil {
		return model.ReactionWrite{}, err
	}
	found := before.find(name)
	out.Reacted = before.has(found)
	if found != nil {
		out.Emoji = found.Name
	}
	if in.Remove {
		return r.remove(ctx, before, found, out)
	}
	if found != nil {
		out.Notes = []string{"You already reacted with " + name + " there, so nothing was sent."}
		return out, nil
	}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun, out.WouldSend = "dry_run", true, preview("POST", "react with "+name, []string{"name"})
		return out, nil
	}
	award, err := s.client.CreateAward(ctx, t.p, r.a, name)
	switch {
	case err == nil:
		out.Outcome, out.Emoji, out.Reacted = "added", award.Name, boolp(true)
		return out, nil
	case gapi.IsClass(err, gapi.ClassAmbiguousOutcome):
		return r.settleAdd(ctx, before, out, err)
	case gapi.IsClass(err, gapi.ClassNotFound):
		return r.refused(ctx, out, err)
	}
	return model.ReactionWrite{}, err
}

// reacting is one react call: where it goes and whose reactions are
// the caller's.
type reacting struct {
	s  *Service
	t  target
	a  gapi.Awardable
	me int64
}

// settleAdd settles an add whose answer was lost by the account's
// reactions before and after: a new one is the add's. Either read
// falling short of the whole list leaves it unknown.
func (r reacting) settleAdd(ctx context.Context, before awards, out model.ReactionWrite, err error) (model.ReactionWrite, error) {
	after, readErr := r.read(ctx)
	if readErr == nil && (!before.complete || !after.complete) {
		readErr = gapi.Errf(gapi.ClassUnsupported, "there are more than %d reactions there", maxAwardPages*gapi.MaxPerPage)
	}
	if readErr == nil {
		if added := after.newSince(before); added != nil {
			out.Outcome, out.Emoji, out.Reacted = "added", added.Name, boolp(true)
			out.Notes = []string{"GitLab's answer was lost, and a read shows your new " + added.Name + " reaction there."}
			return out, nil
		}
	}
	return model.ReactionWrite{}, settle(err, "reaction", func() (string, error) { return "", readErr })
}

// refused reads the reactions again after GitLab's 404 to an add: the
// reaction there is unchanged, whatever the answer said. Otherwise
// GitLab's reason stands, with what it may mean, and a comment GitLab
// wrote itself is named when a read shows it is one.
func (r reacting) refused(ctx context.Context, out model.ReactionWrite, err error) (model.ReactionWrite, error) {
	now, readErr := r.read(ctx)
	if readErr == nil {
		if found := now.find(out.Emoji); found != nil {
			out.Emoji, out.Reacted = found.Name, boolp(true)
			out.Notes = []string{"GitLab refused the add, and a read shows your " + found.Name + " reaction there; it made no change."}
			return out, nil
		}
	}
	msg := gapi.AsError(err).Message
	if r.a.NoteID != 0 {
		get := r.s.client.GetIssueNote
		if r.a.MergeRequest {
			get = r.s.client.GetMergeRequestNote
		}
		if note, noteErr := get(ctx, r.t.p, r.a.IID, r.a.NoteID); noteErr == nil && note.System {
			return model.ReactionWrite{}, gapi.Wrap(gapi.ClassInvalid, err,
				"%s. That comment is a note GitLab wrote to record an event, which takes no reactions", msg)
		}
	}
	yours := ""
	if readErr == nil && len(now.rows) > 0 {
		yours = " Your reactions there: " + strings.Join(now.names(), ", ") + "."
	}
	return model.ReactionWrite{}, gapi.Wrap(gapi.ClassNotFound, err, "%s. %s.%s", msg, reactionRefused, yours)
}

// remove removes the account's reaction the read found, by its id, and
// reads that id back.
func (r reacting) remove(ctx context.Context, before awards, award *gitlab.AwardEmoji, out model.ReactionWrite) (model.ReactionWrite, error) {
	if award == nil {
		if !before.complete {
			return model.ReactionWrite{}, gapi.Errf(gapi.ClassUnsupported, "there are more than %d reactions there and yours with %s "+
				"was not among them; GitLab cannot list one account's reactions alone, so nothing was sent", maxAwardPages*gapi.MaxPerPage, out.Emoji)
		}
		note := "You have no " + out.Emoji + " reaction there, so nothing was sent."
		if len(before.rows) > 0 {
			note += " Yours there: " + strings.Join(before.names(), ", ") + ". GitLab keeps a reaction under its own name, " +
				"which may differ from an alias you gave."
		}
		out.Notes = []string{note}
		return out, nil
	}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun, out.WouldSend = "dry_run", true, preview("DELETE", "remove your "+award.Name+" reaction", nil)
		return out, nil
	}
	err := r.s.client.DeleteAward(ctx, r.t.p, r.a, award.ID)
	_, readErr := r.s.client.GetAward(ctx, r.t.p, r.a, award.ID)
	switch {
	case gapi.IsClass(readErr, gapi.ClassNotFound):
		out.Outcome, out.Reacted = "removed", boolp(false)
		if err != nil {
			out.Notes = []string{"GitLab did not confirm the removal, and a read shows the reaction gone: this call, or its " +
				"repeat, removed it."}
		}
		return out, nil
	case readErr == nil && err != nil:
		return model.ReactionWrite{}, err
	case readErr == nil:
		// GitLab answers 204 even when its service refuses the removal.
		return model.ReactionWrite{}, gapi.Errf(gapi.ClassUnexpected, "GitLab accepted the removal, but your %s reaction is still there", award.Name)
	case err != nil:
		return model.ReactionWrite{}, gapi.Wrap(gapi.ClassAmbiguousOutcome, err, "GitLab did not confirm the removal, and reading "+
			"the reaction back failed too, so whether it is gone is unknown: %s", settledUnknown)
	}
	out.Outcome, out.Reacted = "removed", boolp(false)
	out.Notes = []string{"GitLab accepted the removal; reading the reaction back failed, so it is not confirmed."}
	return out, nil
}

// awards is the account's own reactions on one item, as read.
type awards struct {
	rows     []gitlab.AwardEmoji
	complete bool // the read reached the end of the list
}

func (m awards) find(name string) *gitlab.AwardEmoji {
	if i := slices.IndexFunc(m.rows, func(a gitlab.AwardEmoji) bool { return a.Name == name }); i >= 0 {
		return &m.rows[i]
	}
	return nil
}

// has is whether the reaction found is there: unknown when it was not
// found in a read that fell short of the whole list.
func (m awards) has(found *gitlab.AwardEmoji) *bool {
	if found == nil && !m.complete {
		return nil
	}
	return boolp(found != nil)
}

func (m awards) names() []string {
	out := make([]string, len(m.rows))
	for i, row := range m.rows {
		out[i] = row.Name
	}
	return out
}

// newSince is a reaction in m that before did not have.
func (m awards) newSince(before awards) *gitlab.AwardEmoji {
	for i, row := range m.rows {
		if !slices.ContainsFunc(before.rows, func(b gitlab.AwardEmoji) bool { return b.ID == row.ID }) {
			return &m.rows[i]
		}
	}
	return nil
}

// read reads the account's reactions, up to maxAwardPages of the list.
func (r reacting) read(ctx context.Context) (awards, error) {
	rows, complete, err := readPages(maxAwardPages, func(o gapi.ListOptions) ([]gitlab.AwardEmoji, gapi.Page, error) {
		return r.s.client.ListAwards(ctx, r.t.p, r.a, o)
	})
	if err != nil {
		return awards{}, err
	}
	out := awards{complete: complete}
	for _, row := range rows {
		if row.User.ID == r.me {
			out.rows = append(out.rows, row)
		}
	}
	return out, nil
}

func boolp(v bool) *bool { return &v }
