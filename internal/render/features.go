package render

import (
	"fmt"
	"strings"

	"github.com/mmedum/gitlab-mcp/v2/internal/model"
)

// The phase 6 renderers (§17.13).

// IssueMove renders move_issue.
func IssueMove(w model.IssueMove, _ Boundary) string {
	var b strings.Builder
	writeHead(&b, fmt.Sprintf("Moved issue #%d to %s as #%d.", w.FromIID, projectLine(w.To), w.IID), w.Write)
	if w.DryRun {
		fmt.Fprintf(&b, "\nIt would go to %s, which is %s.", projectLine(w.To), Ident(w.ToVisibility))
	} else {
		fmt.Fprintf(&b, "\n%s, %s. GitLab closed the original.", Ident(w.WebURL), visibility(w.ToVisibility))
	}
	return b.String()
}

// IssueLinkWrite renders link_issues and unlink_issues.
func IssueLinkWrite(w model.IssueLinkWrite, _ Boundary) string {
	var b strings.Builder
	other := fmt.Sprintf("%s#%d", Ident(w.TargetProject.Path), w.TargetIID)
	head := fmt.Sprintf("Linked issue #%d to %s (%s).", w.IID, other, Ident(w.LinkType))
	switch w.Outcome {
	case "unlinked":
		head = fmt.Sprintf("Unlinked issue #%d from %s.", w.IID, other)
	case "unchanged":
		head = "Nothing changed."
	}
	writeHead(&b, head, w.Write)
	if w.LinkID != 0 {
		fmt.Fprintf(&b, "\nLink %d: #%d %s %s.", w.LinkID, w.IID, Ident(w.LinkType), other)
	}
	return b.String()
}

// LabelWrite renders create_label, update_label and delete_label.
func LabelWrite(w model.LabelWrite, bd Boundary) string {
	var b strings.Builder
	head := map[string]string{"created": "Created the label.", "updated": "Updated the label.", "deleted": "Deleted the label."}[w.Outcome]
	writeHead(&b, head, w.Write)
	l := w.Label
	fmt.Fprintf(&b, "\nLabel %s, id %d, color %s", Ident(l.Name), l.ID, Ident(l.Color))
	if l.Priority != nil {
		fmt.Fprintf(&b, ", priority %d", *l.Priority)
	}
	if l.Version != "" && w.Outcome != "deleted" {
		fmt.Fprintf(&b, "; version %s", l.Version)
	}
	b.WriteString(".")
	changedLine(&b, w.Outcome, w.Changed)
	if l.UntrustedDescription != "" {
		fmt.Fprintf(&b, "\n%s\nDescription: %s", bd.Notice(), bd.Inline(l.UntrustedDescription))
	}
	if w.Outcome == "deleted" {
		goneLine(&b, w.Write, "label")
	}
	return b.String()
}

// MilestoneWrite renders create_milestone, update_milestone and
// delete_milestone.
func MilestoneWrite(w model.MilestoneWrite, bd Boundary) string {
	var b strings.Builder
	head := map[string]string{"created": "Created the milestone.", "updated": "Updated the milestone.",
		"deleted": "Deleted the milestone."}[w.Outcome]
	writeHead(&b, head, w.Write)
	m := w.Milestone
	fmt.Fprintf(&b, "\nMilestone id %d (%%%d), %s; start %s, due %s", m.ID, m.IID, Ident(m.State), orNone(deref(m.StartDate)),
		orNone(deref(m.DueDate)))
	if !m.UpdatedAt.IsZero() {
		fmt.Fprintf(&b, "; updated_at %s", when(m.UpdatedAt))
	}
	b.WriteString(".")
	changedLine(&b, w.Outcome, w.Changed)
	b.WriteString("\n" + bd.Notice())
	fmt.Fprintf(&b, "\nTitle: %s", bd.Inline(m.UntrustedTitle))
	if w.UntrustedDescription != "" {
		b.WriteString("\n" + bd.Block(Origin{Kind: "milestone_description", Project: w.Target.Project.Path}, w.UntrustedDescription))
	}
	if w.Outcome == "deleted" {
		goneLine(&b, w.Write, "milestone")
	}
	return b.String()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// RebaseWrite renders rebase_merge_request.
func RebaseWrite(w model.RebaseWrite, bd Boundary) string {
	var b strings.Builder
	writeHead(&b, fmt.Sprintf("Started a rebase of !%d from %s.", w.IID, Ident(w.SHA)), w.Write)
	switch {
	case w.DryRun:
	case w.RebaseInProgress:
		b.WriteString("\nGitLab is rebasing in the background; get_merge_request shows the new head when it is done.")
	case w.UntrustedMergeError != "":
		fmt.Fprintf(&b, "\n%s\nGitLab reported: %s", bd.Notice(), bd.Inline(w.UntrustedMergeError))
	default:
		b.WriteString("\nThe rebase is no longer running; get_merge_request shows the head it left.")
	}
	if w.SkipCI {
		b.WriteString("\nNo pipeline runs for the rebased head.")
	}
	return b.String()
}

// PickWrite renders cherry_pick_commit and revert_commit.
func PickWrite(w model.PickWrite, bd Boundary) string {
	var b strings.Builder
	head := fmt.Sprintf("Cherry-picked %s onto %s as %s.", Ident(w.FromSHA), Ident(w.Branch), Ident(w.SHA))
	if w.Action == "revert" {
		head = fmt.Sprintf("Reverted %s on %s with %s.", Ident(w.FromSHA), Ident(w.Branch), Ident(w.SHA))
	}
	writeHead(&b, head, w.Write)
	if w.DryRun {
		if w.Applies {
			fmt.Fprintf(&b, "\nGitLab's dry run says it applies to %s cleanly.", Ident(w.Branch))
		}
		return b.String()
	}
	fmt.Fprintf(&b, "\n%s\n%s\nTitle: %s", Ident(w.WebURL), bd.Notice(), bd.Inline(w.UntrustedTitle))
	return b.String()
}

// Blame renders get_blame.
func Blame(bl model.Blame, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Blame of %s at %s in %s\n", Ident(bl.Path), Ident(bl.Ref), projectLine(bl.Project))
	b.WriteString(bd.Notice())
	for _, r := range bl.Ranges {
		fmt.Fprintf(&b, "\n\nLines %d to %d: %s by %s, %s: %s\n", r.StartLine, r.EndLine, Ident(shortSHA(r.CommitSHA)), person(r.Author),
			when(r.AuthoredAt), bd.Inline(r.UntrustedSummary))
		b.WriteString(bd.Block(Origin{Kind: "file", Project: bl.Project.Path, Item: bl.Path + "@" + bl.Ref}, r.UntrustedLines))
	}
	if bl.NextLine != nil {
		fmt.Fprintf(&b, "\n\nMore lines follow: continue with start_line=%d.", *bl.NextLine)
	}
	if bl.HiddenRemoved > 0 {
		fmt.Fprintf(&b, "\n%d hidden characters were made visible.", bl.HiddenRemoved)
	}
	return b.String()
}

// Artifacts renders list_job_artifacts.
func Artifacts(l model.Artifacts, _ Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Artifacts of job %d in %s, under %s\n", l.JobID, projectLine(l.Project), orNone(Ident(l.Path)))
	b.WriteString(listingLine("entries", l.Listing))
	for _, e := range l.Entries {
		fmt.Fprintf(&b, "\n- %s %s", Ident(e.Type), Ident(e.Path))
		if e.Size != nil {
			fmt.Fprintf(&b, ", %d bytes", *e.Size)
		}
	}
	return b.String()
}

// Artifact renders get_job_artifact.
func Artifact(a model.Artifact, bd Boundary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Artifact %s of job %d in %s, %d bytes\n", Ident(a.Path), a.JobID, projectLine(a.Project), a.Size)
	if a.Binary {
		b.WriteString("It is not text; only its size is shown.")
		return b.String()
	}
	if a.SecretsMasked > 0 {
		fmt.Fprintf(&b, "%d secret shapes were replaced with [MASKED kind].\n", a.SecretsMasked)
	}
	b.WriteString(bd.Notice() + "\n")
	b.WriteString(bd.Block(Origin{Kind: "job_artifact", Project: a.Project.Path, Item: fmt.Sprintf("job %d %s", a.JobID, a.Path)},
		a.UntrustedContent) + "\n")
	b.WriteString(budgetLine("Content", a.Budget))
	return b.String()
}

// TagWrite renders create_tag and delete_tag.
func TagWrite(w model.TagWrite, _ Boundary) string {
	var b strings.Builder
	head := fmt.Sprintf("Created the tag %s at %s.", Ident(w.Tag), Ident(w.CommitSHA))
	if w.Outcome == "deleted" || (w.DryRun && w.CommitSHA != "") {
		head = fmt.Sprintf("Deleted the tag %s, which pointed at %s.", Ident(w.Tag), Ident(w.CommitSHA))
	}
	writeHead(&b, head, w.Write)
	if w.Outcome == "created" {
		b.WriteString("\nCreating it starts the project's tag pipelines.")
	}
	if w.Outcome == "deleted" {
		goneLine(&b, w.Write, "tag")
	}
	return b.String()
}
