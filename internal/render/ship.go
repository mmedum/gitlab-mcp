package render

import (
	"fmt"
	"strings"

	"github.com/mmedum/gitlab-mcp/internal/model"
)

// The readable half of the Ship and Destructive results. Like the other
// writes they open with what happened and where; the names they show
// (branches, jobs, usernames) are made visible, and no body is shown.

// MergeWrite renders merge_merge_request.
func MergeWrite(w model.MergeWrite, _ Boundary) string {
	var b strings.Builder
	head := fmt.Sprintf("Merge request !%d was not changed.", w.IID)
	switch w.Outcome {
	case "merged":
		head = fmt.Sprintf("Merged merge request !%d.", w.IID)
	case "auto_merge_set":
		head = fmt.Sprintf("Set merge request !%d to merge when its pipeline succeeds; it is not merged yet.", w.IID)
	}
	writeHead(&b, head, w.Write)
	fmt.Fprintf(&b, "\n%s; %s; %s into %s at %s; merge status %s.", Ident(w.WebURL), Ident(w.State), Ident(w.SourceBranch),
		Ident(w.TargetBranch), shortSHA(w.SHA), Ident(w.DetailedMergeStatus))
	if w.MergedAt != nil {
		fmt.Fprintf(&b, "\nMerged %s by @%s", when(*w.MergedAt), Ident(w.MergedBy))
		if w.MergeCommitSHA != "" {
			fmt.Fprintf(&b, "; merge commit %s", shortSHA(w.MergeCommitSHA))
		}
		if w.SquashCommitSHA != "" {
			fmt.Fprintf(&b, "; squash commit %s", shortSHA(w.SquashCommitSHA))
		}
		b.WriteString(".")
	}
	return b.String()
}

// ApprovalWrite renders approve_merge_request and
// unapprove_merge_request.
func ApprovalWrite(w model.ApprovalWrite, _ Boundary) string {
	var b strings.Builder
	head := fmt.Sprintf("Your approval of merge request !%d was not changed.", w.IID)
	switch w.Outcome {
	case "approved":
		head = fmt.Sprintf("Approved merge request !%d.", w.IID)
	case "unapproved":
		head = fmt.Sprintf("Withdrew your approval of merge request !%d.", w.IID)
	}
	writeHead(&b, head, w.Write)
	if w.SHA != "" {
		fmt.Fprintf(&b, "\nAt head %s.", shortSHA(w.SHA))
	}
	fmt.Fprintf(&b, "\nYour approval stands: %s. Approved by %s; rules met: %s", yesNo(w.YouApproved), atList(w.ApprovedBy, "nobody"),
		yesNo(w.Approved))
	if w.ApprovalsLeft != nil {
		fmt.Fprintf(&b, "; approvals still required: %d", *w.ApprovalsLeft)
	}
	b.WriteString(".")
	return b.String()
}

// PipelineWrite renders run_pipeline, retry_pipeline and cancel_pipeline.
func PipelineWrite(w model.PipelineWrite, _ Boundary) string {
	var b strings.Builder
	var head string
	switch w.Outcome {
	case "created":
		head = fmt.Sprintf("Started pipeline %d.", w.PipelineID)
	case "retried":
		head = fmt.Sprintf("Retried the failed and canceled jobs of pipeline %d.", w.PipelineID)
	case "canceled":
		head = fmt.Sprintf("Canceled pipeline %d.", w.PipelineID)
	default:
		head = fmt.Sprintf("Pipeline %d was not changed.", w.PipelineID)
	}
	writeHead(&b, head, w.Write)
	if w.PipelineID != 0 {
		fmt.Fprintf(&b, "\n%s; #%d on %s at %s, from %s.", Ident(w.WebURL), w.IID, Ident(w.Ref), shortSHA(w.SHA), Ident(w.Source))
		if w.StatusBefore != "" && !w.DryRun {
			fmt.Fprintf(&b, "\nStatus before: %s; after: %s.", Ident(w.StatusBefore), Ident(w.Status))
		} else {
			fmt.Fprintf(&b, "\nStatus: %s.", Ident(w.Status))
		}
	}
	if len(w.Variables) > 0 {
		fmt.Fprintf(&b, "\nVariables sent, values not shown: %s.", idents(w.Variables))
	}
	if len(w.Inputs) > 0 {
		fmt.Fprintf(&b, "\nInputs sent, values not shown: %s.", idents(w.Inputs))
	}
	return b.String()
}

// JobWrite renders retry_job and play_job.
func JobWrite(w model.JobWrite, _ Boundary) string {
	var b strings.Builder
	head := fmt.Sprintf("Started manual job %d.", w.JobID)
	if w.Outcome == "retried" {
		head = fmt.Sprintf("Retried job %d as job %d.", w.FromJobID, w.JobID)
	}
	writeHead(&b, head, w.Write)
	fmt.Fprintf(&b, "\n%s; %s (stage %s) in pipeline %d: %s.", Ident(w.WebURL), Ident(w.Name), Ident(w.Stage), w.PipelineID, Ident(w.Status))
	if len(w.Variables) > 0 {
		fmt.Fprintf(&b, "\nVariables sent, values not shown: %s.", idents(w.Variables))
	}
	return b.String()
}

// BranchDelete renders delete_branch.
func BranchDelete(w model.BranchDelete, _ Boundary) string {
	var b strings.Builder
	writeHead(&b, fmt.Sprintf("Deleted branch %s.", Ident(w.Branch)), w.Write)
	fmt.Fprintf(&b, "\nIts head was %s; merged into the default branch: %s.", Ident(w.SHA), yesNo(w.Merged))
	goneLine(&b, w.Write, "branch")
	return b.String()
}

// CommentDelete renders delete_comment.
func CommentDelete(w model.CommentDelete, _ Boundary) string {
	var b strings.Builder
	item := fmt.Sprintf("#%d", w.IID)
	if w.Type == "merge_request" {
		item = mrItem(w.IID)
	}
	writeHead(&b, fmt.Sprintf("Deleted comment %d on %s.", w.NoteID, item), w.Write)
	goneLine(&b, w.Write, "comment")
	return b.String()
}

// goneLine says a delete was read back, unless it was a dry run or the
// read failed, which the notes then say.
func goneLine(b *strings.Builder, w model.Write, what string) {
	if !w.DryRun && len(w.Notes) == 0 {
		b.WriteString("\nA read afterwards finds no such " + what + ".")
	}
}
