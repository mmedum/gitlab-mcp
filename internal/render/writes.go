package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/internal/model"
)

// The readable half of the writes. Every one opens with what happened
// and where, from the shared model.Write: a dry run says nothing was
// written and what would have been sent, and the project's visibility is
// always named (§4.7, §4.11). Nothing the caller or GitLab wrote is
// shown here beyond names, so no boundary is needed.

// writeHead is a write result's first lines.
func writeHead(b *strings.Builder, done string, w model.Write) {
	where := projectLine(w.Target.Project)
	if w.Target.Project.Path == "" {
		where = ""
	}
	if w.DryRun {
		b.WriteString("Dry run: nothing was written.")
		if w.WouldSend != nil {
			fmt.Fprintf(b, " It would %s with %s", w.WouldSend.Operation, w.WouldSend.Method)
			if len(w.WouldSend.Fields) > 0 {
				fmt.Fprintf(b, ", sending %s", strings.Join(w.WouldSend.Fields, ", "))
			}
			b.WriteString(".")
		}
	} else {
		b.WriteString(done)
	}
	if where != "" {
		fmt.Fprintf(b, "\nProject: %s, %s.", where, visibility(w.Target.Visibility))
	}
	if len(w.EscapedCommands) > 0 {
		parts := make([]string, len(w.EscapedCommands))
		for i, e := range w.EscapedCommands {
			parts[i] = fmt.Sprintf("%s line %d /%s", e.Input, e.Line, Ident(e.Command))
		}
		verb := "were sent"
		if w.DryRun {
			verb = "would be sent"
		}
		fmt.Fprintf(b, "\nQuick-action lines %s as plain text, each with a leading backslash: %s.", verb, strings.Join(parts, ", "))
	}
	for _, n := range w.Notes {
		b.WriteString("\nNote: " + n)
	}
}

func visibility(v string) string {
	switch v {
	case "public":
		return "public: anyone can see what is written there"
	case "internal":
		return "internal: every signed-in user of the instance can see what is written there"
	case "private":
		return "private: only its members can see what is written there"
	}
	return "visibility unknown"
}

func changedLine(b *strings.Builder, outcome string, changed []string) {
	switch {
	case outcome == "unchanged":
		b.WriteString("\nNothing changed: GitLab reported every field as it was.")
	case len(changed) > 0:
		fmt.Fprintf(b, "\nChanged, as read back: %s.", strings.Join(changed, ", "))
	}
}

func removedLine(b *strings.Builder, what string, r *model.Removed) {
	if r != nil {
		fmt.Fprintf(b, "\nThe %s was replaced; the old one lost %d line(s), %d characters.", what, r.Lines, r.Chars)
	}
}

func witnessLine(b *strings.Builder, t *time.Time, tool string) {
	if t != nil {
		fmt.Fprintf(b, "\nupdated_at is %s; pass it to %s for the next change.", t.UTC().Format(time.RFC3339Nano), tool)
	}
}

// outcomeLine is a create's or an update's first line; a dry run's is
// written by writeHead.
func outcomeLine(outcome, what, created string) string {
	switch outcome {
	case "created":
		return created + " " + what + "."
	case "updated":
		return "Updated " + what + "."
	}
	return strings.ToUpper(what[:1]) + what[1:] + " was not changed."
}

// IssueWrite renders create_issue and update_issue.
func IssueWrite(w model.IssueWrite, _ Boundary) string {
	var b strings.Builder
	writeHead(&b, outcomeLine(w.Outcome, fmt.Sprintf("issue #%d", w.IID), "Created"), w.Write)
	if w.DryRun {
		return b.String()
	}
	fmt.Fprintf(&b, "\n%s; %s; confidential %s.", Ident(w.WebURL), Ident(w.State), yesNo(w.Confidential))
	if w.Outcome != "created" {
		fmt.Fprintf(&b, "\nLabels before: %s.", idents(w.LabelsBefore))
	}
	fmt.Fprintf(&b, "\nLabels: %s; assignees %s; milestone %s; due %s.", idents(w.Labels), atList(w.Assignees, "none"),
		milestone(w.Milestone), orNone(w.DueDate))
	changedLine(&b, w.Outcome, w.Changed)
	removedLine(&b, "description", w.DescriptionRemoved)
	witnessLine(&b, w.UpdatedAt, "update_issue")
	return b.String()
}

// MergeRequestWrite renders create_merge_request and
// update_merge_request.
func MergeRequestWrite(w model.MergeRequestWrite, _ Boundary) string {
	var b strings.Builder
	writeHead(&b, outcomeLine(w.Outcome, fmt.Sprintf("merge request !%d", w.IID), "Opened"), w.Write)
	if w.DryRun {
		return b.String()
	}
	fmt.Fprintf(&b, "\n%s; %s; draft %s; %s into %s.", Ident(w.WebURL), Ident(w.State), yesNo(w.Draft), Ident(w.SourceBranch), Ident(w.TargetBranch))
	if w.Outcome != "created" {
		fmt.Fprintf(&b, "\nLabels before: %s.", idents(w.LabelsBefore))
	}
	fmt.Fprintf(&b, "\nLabels: %s; assignees %s; reviewers %s; milestone %s.", idents(w.Labels), atList(w.Assignees, "none"),
		atList(w.Reviewers, "none"), milestone(w.Milestone))
	fmt.Fprintf(&b, "\nDelete the source branch when merged: %s; squash: %s.", yesNo(w.RemoveSourceBranch), yesNo(w.Squash))
	changedLine(&b, w.Outcome, w.Changed)
	removedLine(&b, "description", w.DescriptionRemoved)
	if w.Outcome != "created" {
		// A new merge request's updated_at moves within seconds (§18).
		witnessLine(&b, w.UpdatedAt, "update_merge_request")
	}
	return b.String()
}

// CommentWrite renders add_comment and add_review_comment.
func CommentWrite(w model.CommentWrite, _ Boundary) string {
	var b strings.Builder
	head := map[string]string{
		"comment": "Posted comment %d.", "thread": "Started a thread with comment %d.", "reply": "Replied with comment %d.",
		"draft": "Added draft review comment %d; only you see it until submit_review.",
	}[w.Kind]
	writeHead(&b, fmt.Sprintf(head, w.NoteID), w.Write)
	if w.DiscussionID != "" {
		fmt.Fprintf(&b, "\nThread: %s.", Ident(w.DiscussionID))
	}
	if p := w.Position; p != nil {
		verb := "Landed on"
		if w.DryRun {
			verb = "Would land on"
		}
		fmt.Fprintf(&b, "\n%s %s, %s, at head %s.", verb, fileName(p.OldPath, p.NewPath), lineOf(p), shortSHA(p.HeadSHA))
		if r := w.LineRange; r != nil {
			fmt.Fprintf(&b, " It covers %s lines %d to %d.", r.Side, r.Start, r.End)
		}
	}
	witnessLine(&b, w.UpdatedAt, "update_comment")
	return b.String()
}

// CommentUpdate renders update_comment.
func CommentUpdate(w model.CommentUpdate, _ Boundary) string {
	var b strings.Builder
	writeHead(&b, outcomeLine(w.Outcome, fmt.Sprintf("comment %d on %s", w.NoteID, noteableItem(w.Type, w.IID)), ""), w.Write)
	removedLine(&b, "text", w.BodyRemoved)
	witnessLine(&b, w.UpdatedAt, "update_comment")
	return b.String()
}

func lineOf(p *model.DiffPosition) string {
	switch {
	case p.OldLine != nil && p.NewLine != nil:
		return fmt.Sprintf("unchanged line, old %d and new %d", *p.OldLine, *p.NewLine)
	case p.NewLine != nil:
		return fmt.Sprintf("added line, new %d", *p.NewLine)
	case p.OldLine != nil:
		return fmt.Sprintf("removed line, old %d", *p.OldLine)
	}
	return "no line"
}

// DiscussionWrite renders resolve_discussion.
func DiscussionWrite(w model.DiscussionWrite, _ Boundary) string {
	var b strings.Builder
	head := "The thread was not changed."
	switch w.Outcome {
	case "resolved":
		head = "Resolved the thread."
	case "reopened":
		head = "Reopened the thread."
	}
	writeHead(&b, head, w.Write)
	state := "unresolved"
	if w.Resolved {
		state = "resolved"
	}
	fmt.Fprintf(&b, "\nThread %s is %s.", Ident(w.DiscussionID), state)
	return b.String()
}

// TimeWrite renders track_time.
func TimeWrite(w model.TimeWrite, _ Boundary) string {
	var b strings.Builder
	item := noteableItem(w.Type, w.IID)
	head := "Tracked time on " + item + "."
	if w.Outcome == "unchanged" {
		head = "The time tracking of " + item + " was not changed."
	}
	writeHead(&b, head, w.Write)
	if len(w.Sent) > 0 {
		fmt.Fprintf(&b, "\nSent: %s.", strings.Join(w.Sent, ", then "))
	}
	fmt.Fprintf(&b, "\nBefore: %s.", timeLine(w.Before))
	if w.After != nil {
		fmt.Fprintf(&b, "\nAfter, as read back: %s.", timeLine(*w.After))
	}
	if w.Outcome == "unchanged" && len(w.Sent) > 0 {
		b.WriteString("\nGitLab reported the estimate and the time spent as they were.")
	}
	witnessLine(&b, w.UpdatedAt, "track_time or an update")
	fmt.Fprintf(&b, "\ntotal_time_spent is %d; pass it to the next track_time that adds or resets spent time.", w.TotalTimeSpent)
	return b.String()
}

// DraftDelete renders delete_review_comment.
func DraftDelete(w model.DraftDelete, _ Boundary) string {
	var b strings.Builder
	writeHead(&b, fmt.Sprintf("Deleted draft %d.", w.DraftID), w.Write)
	fmt.Fprintf(&b, "\nYour drafts left on the merge request: %d.", w.Remaining)
	return b.String()
}

// ReviewSubmit renders submit_review.
func ReviewSubmit(w model.ReviewSubmit, _ Boundary) string {
	var b strings.Builder
	writeHead(&b, fmt.Sprintf("Published the review: %d draft(s).", w.Published), w.Write)
	if w.DryRun {
		fmt.Fprintf(&b, "\nIt would publish %d draft(s).", w.Published)
	}
	fmt.Fprintf(&b, "\nSummary comment: %s; reviewer state: %s; drafts still pending: %d.", yesNo(w.Summary), orNone(w.ReviewerState), w.Remaining)
	return b.String()
}

// BranchWrite renders create_branch.
func BranchWrite(w model.BranchWrite, _ Boundary) string {
	var b strings.Builder
	writeHead(&b, fmt.Sprintf("Created branch %s at %s.", Ident(w.Branch), shortSHA(w.CommitSHA)), w.Write)
	if !w.DryRun {
		fmt.Fprintf(&b, "\n%s; protected %s.", Ident(w.WebURL), yesNo(w.Protected))
	}
	return b.String()
}

// CommitWrite renders create_commit.
func CommitWrite(w model.CommitWrite, _ Boundary) string {
	var b strings.Builder
	writeHead(&b, fmt.Sprintf("Committed %s to %s.", Ident(w.ShortID), Ident(w.Branch)), w.Write)
	fmt.Fprintf(&b, "\nFiles: %s.", idents(w.Files))
	if w.DryRun {
		return b.String()
	}
	fmt.Fprintf(&b, "\n%s; +%d -%d; parents %s.", Ident(w.WebURL), w.Additions, w.Deletions, idents(w.ParentIDs))
	if w.BranchHead != "" {
		fmt.Fprintf(&b, "\nThe branch's head, read afterwards: %s.", shortSHA(w.BranchHead))
	}
	return b.String()
}

// TodosDone renders mark_todos_done.
func TodosDone(w model.TodosDone, _ Boundary) string {
	var b strings.Builder
	done := 0
	for _, it := range w.Items {
		if it.Outcome == "done" {
			done++
		}
	}
	writeHead(&b, fmt.Sprintf("Marked %d of %d to-do item(s) done.", done, len(w.Items)), w.Write)
	for _, it := range w.Items {
		fmt.Fprintf(&b, "\n- %d: %s", it.ID, strings.ReplaceAll(it.Outcome, "_", " "))
		if it.Error != "" {
			fmt.Fprintf(&b, " (%s)", it.Error)
		}
	}
	return b.String()
}

// SubscriptionWrite renders subscribe.
func SubscriptionWrite(w model.SubscriptionWrite, _ Boundary) string {
	var b strings.Builder
	item := noteableItem(w.Type, w.IID)
	head := "Your subscription to " + item + " was not changed."
	switch w.Outcome {
	case "subscribed":
		head = "Subscribed you to " + item + "."
	case "unsubscribed":
		head = "Unsubscribed you from " + item + "."
	}
	writeHead(&b, head, w.Write)
	state := "not subscribed"
	if w.Subscribed {
		state = "subscribed"
	}
	fmt.Fprintf(&b, "\nYou are %s to %s.", state, item)
	return b.String()
}

// ReactionWrite renders react.
func ReactionWrite(w model.ReactionWrite, _ Boundary) string {
	var b strings.Builder
	on := noteableItem(w.Type, w.IID)
	if w.NoteID != 0 {
		on = fmt.Sprintf("comment %d on %s", w.NoteID, on)
	}
	emoji := ":" + Ident(w.Emoji) + ":"
	head := "Your reactions on " + on + " were not changed."
	switch w.Outcome {
	case "added":
		head = "Reacted with " + emoji + " on " + on + "."
	case "removed":
		head = "Removed your " + emoji + " reaction from " + on + "."
	}
	writeHead(&b, head, w.Write)
	if w.Reacted {
		fmt.Fprintf(&b, "\nYour %s reaction is there.", emoji)
	} else {
		fmt.Fprintf(&b, "\nYou have no %s reaction there.", emoji)
	}
	return b.String()
}

// TodoWrite renders add_todo.
func TodoWrite(w model.TodoWrite, _ Boundary) string {
	var b strings.Builder
	item := noteableItem(w.Type, w.IID)
	head := "No to-do was added for " + item + "."
	if w.Outcome == "created" {
		head = "Added a to-do for " + item + "."
	}
	writeHead(&b, head, w.Write)
	if w.TodoID != 0 {
		fmt.Fprintf(&b, "\nTo-do item %d; mark_todos_done clears it.", w.TodoID)
	}
	return b.String()
}
