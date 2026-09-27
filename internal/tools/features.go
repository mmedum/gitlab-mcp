package tools

import (
	"context"

	"github.com/mmedum/gitlab-mcp/internal/model"
	"github.com/mmedum/gitlab-mcp/internal/render"
	"github.com/mmedum/gitlab-mcp/internal/service"
)

// The phase 6 tools, with the kinds and toolsets §17.13 decided.

// ----------------------------------------------------------- issues

type moveIssueIn struct {
	Project   idOrPath `json:"project" jsonschema:"The project the issue is in: its numeric id, its full path such as group/sub/project, or its web URL"`
	IID       int64    `json:"iid" jsonschema:"The issue's number in its project: the number shown as #12 for issues and !12 for merge requests; not the global id"`
	ToProject idOrPath `json:"to_project" jsonschema:"The project to move it to: its numeric id, its full path or its web URL"`
	UpdatedAt string   `json:"updated_at" jsonschema:"The updated_at of your latest read of the issue, as get_issue returned it. The move is refused [stale] if it changed since. A [stale] refusal is NOT a retry signal: read it again and decide whether the move still makes sense"`
	DryRun    bool     `json:"dry_run,omitempty" jsonschema:"Check both projects and return what would be sent without moving anything"`
}

func moveIssue() definition {
	return tool[moveIssueIn, model.IssueMove]{
		sp: spec{Name: "move_issue", Kind: Ship,
			Description: "Move an issue to another project: GitLab copies it there with its comments and closes the original. " +
				"Both projects must be ones writes are allowed in, and a move to a project more people can see than the one " +
				"the issue is in is refused [blocked]. Never repeated after a lost answer." + shipNote + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in moveIssueIn) (model.IssueMove, error) {
			return svc.MoveIssue(ctx, service.IssueMoveRequest{Project: string(in.Project), IID: in.IID, ToProject: string(in.ToProject),
				UpdatedAt: in.UpdatedAt})
		},
		text: render.IssueMove,
	}
}

type issueLinkIn struct {
	Project       idOrPath `json:"project" jsonschema:"The project the first issue is in: its numeric id, its full path such as group/sub/project, or its web URL"`
	IID           int64    `json:"iid" jsonschema:"The first issue's number in its project: the number shown as #12 for issues and !12 for merge requests; not the global id"`
	TargetProject idOrPath `json:"target_project,omitempty" jsonschema:"The project the other issue is in; the first issue's project when omitted"`
	TargetIID     int64    `json:"target_iid" jsonschema:"The other issue's number in its project"`
	DryRun        bool     `json:"dry_run,omitempty" jsonschema:"Return what would be sent without writing anything"`
}

type linkIssuesIn struct {
	Project       idOrPath `json:"project" jsonschema:"The project the first issue is in: its numeric id, its full path such as group/sub/project, or its web URL"`
	IID           int64    `json:"iid" jsonschema:"The first issue's number in its project: the number shown as #12 for issues and !12 for merge requests; not the global id"`
	TargetProject idOrPath `json:"target_project,omitempty" jsonschema:"The project the other issue is in; the first issue's project when omitted"`
	TargetIID     int64    `json:"target_iid" jsonschema:"The other issue's number in its project"`
	LinkType      string   `json:"link_type,omitempty" jsonschema:"relates_to (default), blocks or is_blocked_by, from the first issue's side; the last two need Premium"`
	DryRun        bool     `json:"dry_run,omitempty" jsonschema:"Return what would be sent without writing anything"`
}

func linkIssues() definition {
	return tool[linkIssuesIn, model.IssueLinkWrite]{
		sp: spec{Name: "link_issues", Kind: Write, Enums: map[string][]string{"link_type": service.IssueLinkTypes},
			Description: "Link two issues, in one project or two; the link shows on both, so both projects must be ones writes " +
				"are allowed in. A link that exists is reported unchanged. Never repeated after a lost answer." + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in linkIssuesIn) (model.IssueLinkWrite, error) {
			return svc.LinkIssues(ctx, service.IssueLinkRequest{Project: string(in.Project), IID: in.IID,
				TargetProject: string(in.TargetProject), TargetIID: in.TargetIID, LinkType: in.LinkType})
		},
		text: render.IssueLinkWrite,
	}
}

func unlinkIssues() definition {
	return tool[issueLinkIn, model.IssueLinkWrite]{
		sp: spec{Name: "unlink_issues", Kind: Write, Idempotent: true,
			Description: "Remove the link between two issues. With no link between them, the result is unchanged." + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in issueLinkIn) (model.IssueLinkWrite, error) {
			return svc.UnlinkIssues(ctx, service.IssueLinkRequest{Project: string(in.Project), IID: in.IID,
				TargetProject: string(in.TargetProject), TargetIID: in.TargetIID})
		},
		text: render.IssueLinkWrite,
	}
}

// ----------------------------------------------------------- planning

type createLabelIn struct {
	Project     idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	Name        string   `json:"name" jsonschema:"The label's name; scoped labels such as priority::high are written this way"`
	Color       string   `json:"color" jsonschema:"#RRGGBB, or a CSS color name"`
	Description *string  `json:"description,omitempty" jsonschema:"What the label means"`
	Priority    *int     `json:"priority,omitempty" jsonschema:"A priority, 0 highest; lower numbers sort first"`
	DryRun      bool     `json:"dry_run,omitempty" jsonschema:"Return what would be sent without writing anything"`
}

func createLabel() definition {
	return tool[createLabelIn, model.LabelWrite]{
		sp: spec{Name: "create_label", Kind: Write, Toolset: "planning",
			Description: "Create a project label. Never repeated after a lost answer." + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in createLabelIn) (model.LabelWrite, error) {
			return svc.CreateLabel(ctx, service.LabelRequest{Project: string(in.Project), Name: in.Name, Color: in.Color,
				Description: in.Description, Priority: in.Priority})
		},
		text: render.LabelWrite,
	}
}

type updateLabelIn struct {
	Project     idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	LabelID     int64    `json:"label_id" jsonschema:"The label's id, as list_labels returned it"`
	Version     string   `json:"version" jsonschema:"The label's version as list_labels returned it. The write is refused [stale] if the label changed since. A [stale] refusal is NOT a retry signal: read it again and decide whether the change still makes sense"`
	Name        string   `json:"name,omitempty" jsonschema:"A new name; every issue and merge request carrying the label shows it"`
	Color       string   `json:"color,omitempty" jsonschema:"A new color: #RRGGBB, or a CSS color name"`
	Description *string  `json:"description,omitempty" jsonschema:"A new description; empty clears it"`
	Priority    *int     `json:"priority,omitempty" jsonschema:"A new priority, 0 highest"`
	DryRun      bool     `json:"dry_run,omitempty" jsonschema:"Return what would be sent without writing anything"`
}

func updateLabel() definition {
	return tool[updateLabelIn, model.LabelWrite]{
		sp: spec{Name: "update_label", Kind: Write, Toolset: "planning", Idempotent: true,
			Description: "Change a project label: only the fields given are sent. A group's label is refused [blocked]. version " +
				"from list_labels is required." + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in updateLabelIn) (model.LabelWrite, error) {
			return svc.UpdateLabel(ctx, service.LabelRequest{Project: string(in.Project), LabelID: in.LabelID, Version: in.Version,
				Name: in.Name, Color: in.Color, Description: in.Description, Priority: in.Priority})
		},
		text: render.LabelWrite,
	}
}

type deleteLabelIn struct {
	Project idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	LabelID int64    `json:"label_id" jsonschema:"The label's id, as list_labels returned it"`
	Version string   `json:"version" jsonschema:"The label's version as list_labels returned it. The delete is refused [stale] if the label changed since. A [stale] refusal is NOT a retry signal: read it again and decide whether to delete it"`
	Confirm bool     `json:"confirm,omitempty" jsonschema:"Must be true: the label comes off every issue and merge request, and this server cannot put it back"`
	DryRun  bool     `json:"dry_run,omitempty" jsonschema:"Check the label and return what would be sent without deleting it"`
}

func deleteLabel() definition {
	return tool[deleteLabelIn, model.LabelWrite]{
		sp: spec{Name: "delete_label", Kind: Destructive, Toolset: "planning", Idempotent: true,
			Description: "Delete a project label, which takes it off every issue and merge request. A group's label is refused " +
				"[blocked]. version from list_labels is required." + destructiveNote + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in deleteLabelIn) (model.LabelWrite, error) {
			return svc.DeleteLabel(ctx, string(in.Project), in.LabelID, in.Version)
		},
		text: render.LabelWrite,
	}
}

type createMilestoneIn struct {
	Project     idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	Title       string   `json:"title" jsonschema:"The milestone's title, by which issues and merge requests name it"`
	Description *string  `json:"description,omitempty" jsonschema:"What the milestone is for, in Markdown"`
	StartDate   *string  `json:"start_date,omitempty" jsonschema:"The day it starts, YYYY-MM-DD"`
	DueDate     *string  `json:"due_date,omitempty" jsonschema:"The day it is due, YYYY-MM-DD"`
	DryRun      bool     `json:"dry_run,omitempty" jsonschema:"Return what would be sent without writing anything"`
}

func createMilestone() definition {
	return tool[createMilestoneIn, model.MilestoneWrite]{
		sp: spec{Name: "create_milestone", Kind: Write, Toolset: "planning",
			Description: "Create a project milestone. Never repeated after a lost answer." + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in createMilestoneIn) (model.MilestoneWrite, error) {
			return svc.CreateMilestone(ctx, service.MilestoneRequest{Project: string(in.Project), Title: in.Title,
				Description: in.Description, StartDate: in.StartDate, DueDate: in.DueDate})
		},
		text: render.MilestoneWrite,
	}
}

type updateMilestoneIn struct {
	Project     idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	MilestoneID int64    `json:"milestone_id" jsonschema:"The milestone's id, as list_milestones returned it; not its iid"`
	UpdatedAt   string   `json:"updated_at" jsonschema:"The updated_at of your latest read of it, as list_milestones returned it. The write is refused [stale] if it changed since. A [stale] refusal is NOT a retry signal: read it again and decide whether the change still makes sense"`
	Title       string   `json:"title,omitempty" jsonschema:"A new title"`
	Description *string  `json:"description,omitempty" jsonschema:"What the milestone is for, in Markdown; empty clears it"`
	StartDate   *string  `json:"start_date,omitempty" jsonschema:"The day it starts, YYYY-MM-DD; empty clears it"`
	DueDate     *string  `json:"due_date,omitempty" jsonschema:"The day it is due, YYYY-MM-DD; empty clears it"`
	State       string   `json:"state,omitempty" jsonschema:"close to close it, activate to open it again"`
	DryRun      bool     `json:"dry_run,omitempty" jsonschema:"Return what would be sent without writing anything"`
}

func updateMilestone() definition {
	return tool[updateMilestoneIn, model.MilestoneWrite]{
		sp: spec{Name: "update_milestone", Kind: Write, Toolset: "planning", Idempotent: true,
			Enums: map[string][]string{"state": service.MilestoneStates},
			Description: "Change a project milestone, or close or reopen it: only the fields given are sent. updated_at from " +
				"list_milestones is required." + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in updateMilestoneIn) (model.MilestoneWrite, error) {
			return svc.UpdateMilestone(ctx, service.MilestoneRequest{Project: string(in.Project), ID: in.MilestoneID, UpdatedAt: in.UpdatedAt,
				Title: in.Title, Description: in.Description, StartDate: in.StartDate, DueDate: in.DueDate, StateEvent: in.State})
		},
		text: render.MilestoneWrite,
	}
}

type deleteMilestoneIn struct {
	Project     idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	MilestoneID int64    `json:"milestone_id" jsonschema:"The milestone's id, as list_milestones returned it; not its iid"`
	UpdatedAt   string   `json:"updated_at" jsonschema:"The updated_at of your latest read of it, as list_milestones returned it. The delete is refused [stale] if it changed since. A [stale] refusal is NOT a retry signal: read it again and decide whether to delete it"`
	Confirm     bool     `json:"confirm,omitempty" jsonschema:"Must be true: the milestone comes off every issue and merge request, and this server cannot put it back"`
	DryRun      bool     `json:"dry_run,omitempty" jsonschema:"Check the milestone and return what would be sent without deleting it"`
}

func deleteMilestone() definition {
	return tool[deleteMilestoneIn, model.MilestoneWrite]{
		sp: spec{Name: "delete_milestone", Kind: Destructive, Toolset: "planning", Idempotent: true,
			Description: "Delete a project milestone, which takes it off every issue and merge request. updated_at from " +
				"list_milestones is required." + destructiveNote + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in deleteMilestoneIn) (model.MilestoneWrite, error) {
			return svc.DeleteMilestone(ctx, string(in.Project), in.MilestoneID, in.UpdatedAt)
		},
		text: render.MilestoneWrite,
	}
}

// ----------------------------------------------------------- history

type rebaseIn struct {
	Project idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	IID     int64    `json:"iid" jsonschema:"The merge request's number in its project: the number shown as #12 for issues and !12 for merge requests; not the global id"`
	SHA     string   `json:"sha" jsonschema:"The merge request's head as get_merge_request returned it, whole or its first seven characters or more. The rebase is refused [stale] if the head moved since. A [stale] refusal is NOT a retry signal: look at what was pushed before deciding again"`
	SkipCI  bool     `json:"skip_ci,omitempty" jsonschema:"Start no pipeline for the rebased head"`
	DryRun  bool     `json:"dry_run,omitempty" jsonschema:"Check the merge request and return what would be sent without rebasing"`
}

func rebaseMergeRequest() definition {
	return tool[rebaseIn, model.RebaseWrite]{
		sp: spec{Name: "rebase_merge_request", Kind: Ship,
			Description: "Rebase a merge request's source branch onto its target branch. It rewrites the source branch, which " +
				"resets approvals, so it takes the head sha you reviewed; a protected source branch is refused [blocked]. GitLab " +
				"rebases in the background: the result says whether it is still running, and get_merge_request shows the new " +
				"head." + shipNote + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in rebaseIn) (model.RebaseWrite, error) {
			return svc.RebaseMergeRequest(ctx, string(in.Project), in.IID, in.SHA, in.SkipCI)
		},
		text: render.RebaseWrite,
	}
}

type pickIn struct {
	Project idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	Commit  string   `json:"commit" jsonschema:"The commit to apply: its SHA, whole or its first seven characters or more"`
	Branch  string   `json:"branch" jsonschema:"The branch to commit to; never the default branch or a protected one"`
	DryRun  bool     `json:"dry_run,omitempty" jsonschema:"Ask GitLab whether it applies cleanly, and commit nothing"`
}

type cherryPickIn struct {
	Project idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	Commit  string   `json:"commit" jsonschema:"The commit to apply: its SHA, whole or its first seven characters or more"`
	Branch  string   `json:"branch" jsonschema:"The branch to commit to; never the default branch or a protected one"`
	Message string   `json:"message,omitempty" jsonschema:"The new commit's message; GitLab's own, naming the commit picked, when omitted"`
	DryRun  bool     `json:"dry_run,omitempty" jsonschema:"Ask GitLab whether it applies cleanly, and commit nothing"`
}

func cherryPickCommit() definition {
	return tool[cherryPickIn, model.PickWrite]{
		sp: spec{Name: "cherry_pick_commit", Kind: Write,
			Description: "Apply a commit to a branch as a new commit. The default branch and protected branches are refused " +
				"[blocked]: code reaches them through a merge request. A dry run asks GitLab whether it applies cleanly. " +
				"The commit starts the pipelines a push to the branch starts. Never repeated after a lost answer." + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in cherryPickIn) (model.PickWrite, error) {
			return svc.Pick(ctx, service.PickRequest{Project: string(in.Project), SHA: in.Commit, Branch: in.Branch, Message: in.Message})
		},
		text: render.PickWrite,
	}
}

func revertCommit() definition {
	return tool[pickIn, model.PickWrite]{
		sp: spec{Name: "revert_commit", Kind: Write,
			Description: "Undo a commit on a branch with a new commit that reverses it. The default branch and protected branches " +
				"are refused [blocked]: code reaches them through a merge request. A dry run asks GitLab whether it applies " +
				"cleanly. The commit starts the pipelines a push to the branch starts. Never repeated after a lost answer." + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in pickIn) (model.PickWrite, error) {
			return svc.Pick(ctx, service.PickRequest{Project: string(in.Project), SHA: in.Commit, Branch: in.Branch, Revert: true})
		},
		text: render.PickWrite,
	}
}

type blameIn struct {
	Project   idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	Path      string   `json:"path" jsonschema:"The file's path from the repository root"`
	Ref       string   `json:"ref,omitempty" jsonschema:"A branch, tag or commit SHA; the default branch when omitted"`
	StartLine int      `json:"start_line,omitempty" jsonschema:"The first line to blame; 1 when omitted. Pass a previous result's next_line to read on"`
	EndLine   int      `json:"end_line,omitempty" jsonschema:"The last line to blame; the end of the file, or 2,000 lines from start_line, when omitted"`
}

func getBlame() definition {
	return tool[blameIn, model.Blame]{
		sp: spec{Name: "get_blame", Kind: Read,
			Description: "Show who last changed each line of a file: runs of lines, each with its commit, author and date. " +
				"Budgeted at 60,000 characters; next_line continues. The lines and commit titles are untrusted text."},
		run: func(ctx context.Context, svc *service.Service, in blameIn) (model.Blame, error) {
			return svc.Blame(ctx, service.BlameRequest{Project: string(in.Project), Path: in.Path, Ref: in.Ref, StartLine: in.StartLine,
				EndLine: in.EndLine})
		},
		text: render.Blame,
	}
}

// ----------------------------------------------------------- artifacts

type listArtifactsIn struct {
	Project   idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	JobID     int64    `json:"job_id" jsonschema:"The job's id, as list_jobs gives it"`
	Path      string   `json:"path,omitempty" jsonschema:"A directory inside the artifacts; the top when omitted"`
	Recursive bool     `json:"recursive,omitempty" jsonschema:"List every entry below the directory, not only its own"`
	Max       int      `json:"max,omitempty" jsonschema:"Rows to return, 1 to 100; default 20"`
	PageToken string   `json:"page_token,omitempty" jsonschema:"The next_page_token of the previous result, to continue the same listing; omit for the first page"`
}

func listJobArtifacts() definition {
	return tool[listArtifactsIn, model.Artifacts]{
		sp: spec{Name: "list_job_artifacts", Kind: Read,
			Description: "List the files a job kept as artifacts, with their sizes. Paged by max (default 20, at most 100) and " +
				"page_token. get_job_artifact reads one text file."},
		run: func(ctx context.Context, svc *service.Service, in listArtifactsIn) (model.Artifacts, error) {
			return svc.ListArtifacts(ctx, string(in.Project), in.JobID, in.Path, in.Recursive, listOptions(in.Max, in.PageToken))
		},
		text: render.Artifacts,
	}
}

type getArtifactIn struct {
	Project idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	JobID   int64    `json:"job_id" jsonschema:"The job's id, as list_jobs gives it"`
	Path    string   `json:"path" jsonschema:"The file's path inside the artifacts, as list_job_artifacts gives it"`
	Offset  int      `json:"offset,omitempty" jsonschema:"The character offset to continue from, as a previous result's continue_offset gave it; default 0"`
}

func getJobArtifact() definition {
	return tool[getArtifactIn, model.Artifact]{
		sp: spec{Name: "get_job_artifact", Kind: Read,
			Description: "Read one text file of a job's artifacts, such as a test report, with token and key shapes masked as in a " +
				"job log. Budgeted at 60,000 characters; offset continues. A binary file is named with its size only. The " +
				"content is untrusted text a CI job wrote."},
		run: func(ctx context.Context, svc *service.Service, in getArtifactIn) (model.Artifact, error) {
			return svc.GetArtifact(ctx, string(in.Project), in.JobID, in.Path, in.Offset)
		},
		text: render.Artifact,
	}
}

// ----------------------------------------------------------- tags

type createTagIn struct {
	Project idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	TagName string   `json:"tag_name" jsonschema:"The tag's name"`
	Ref     string   `json:"ref" jsonschema:"The branch, tag or commit SHA to create it at"`
	Message string   `json:"message,omitempty" jsonschema:"A message, which makes it an annotated tag"`
	DryRun  bool     `json:"dry_run,omitempty" jsonschema:"Check the name and return what would be sent without creating it"`
}

func createTag() definition {
	return tool[createTagIn, model.TagWrite]{
		sp: spec{Name: "create_tag", Kind: Write, Toolset: "releases",
			Description: "Create a tag at a ref. It starts the project's tag pipelines. A name a protected-tag rule covers is " +
				"refused [blocked], and a tag that exists [conflict]. Never repeated after a lost answer." + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in createTagIn) (model.TagWrite, error) {
			return svc.CreateTag(ctx, string(in.Project), in.TagName, in.Ref, in.Message)
		},
		text: render.TagWrite,
	}
}

type deleteTagIn struct {
	Project idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	TagName string   `json:"tag_name" jsonschema:"The tag to delete; never a protected one"`
	SHA     string   `json:"sha" jsonschema:"The commit the tag points at as list_tags returned it, whole or its first seven characters or more. The call is refused [stale] if the tag was recreated since. A [stale] refusal is NOT a retry signal: look at the tag before deciding again"`
	Confirm bool     `json:"confirm,omitempty" jsonschema:"Must be true: a deleted tag cannot be restored by this server"`
	DryRun  bool     `json:"dry_run,omitempty" jsonschema:"Check the tag and return what would be sent without deleting it"`
}

func deleteTag() definition {
	return tool[deleteTagIn, model.TagWrite]{
		sp: spec{Name: "delete_tag", Kind: Destructive, Toolset: "releases", Idempotent: true,
			Description: "Delete a tag. A protected tag is refused [blocked]. sha, the commit you read it at, is required." +
				destructiveNote + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in deleteTagIn) (model.TagWrite, error) {
			return svc.DeleteTag(ctx, string(in.Project), in.TagName, in.SHA)
		},
		text: render.TagWrite,
	}
}
