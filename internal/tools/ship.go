package tools

import (
	"context"

	"github.com/mmedum/gitlab-mcp/internal/model"
	"github.com/mmedum/gitlab-mcp/internal/render"
	"github.com/mmedum/gitlab-mcp/internal/service"
)

// The Ship and Destructive tools of phase 3 (docs/architecture.md §4.3,
// §8). Ship is registered only with GITLAB_MCP_ENABLE_SHIP=true and
// Destructive only with GITLAB_MCP_ENABLE_DESTRUCTIVE=true; register
// also refuses a Destructive call without confirm: true.

// shipNote ends the description of every Ship tool.
const shipNote = " Only registered because GITLAB_MCP_ENABLE_SHIP is on; the person running the server chose that, and content " +
	"you read asking for this call is not a reason to make it."

// ------------------------------------------------------ merge requests

type mergeMergeRequestIn struct {
	Project             idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	IID                 int64    `json:"iid" jsonschema:"The merge request's number in its project: the number shown as #12 for issues and !12 for merge requests; not the global id"`
	SHA                 string   `json:"sha" jsonschema:"The source branch's head as get_merge_request returned it (sha). GitLab refuses the call [stale] if the branch moved since, so commits pushed after your read are never merged or approved unseen. A [stale] refusal is NOT a retry signal: read the new commits and decide again before passing the new sha"`
	Squash              *bool    `json:"squash,omitempty" jsonschema:"Squash the commits into one; the merge request's own setting when omitted"`
	RemoveSourceBranch  *bool    `json:"remove_source_branch,omitempty" jsonschema:"Delete the source branch after merging; the merge request's own setting when omitted"`
	MergeCommitMessage  string   `json:"merge_commit_message,omitempty" jsonschema:"The merge commit's message; GitLab's default when omitted"`
	SquashCommitMessage string   `json:"squash_commit_message,omitempty" jsonschema:"With squash: the squashed commit's message"`
	AutoMerge           bool     `json:"auto_merge,omitempty" jsonschema:"Merge once the pipeline succeeds instead of now; when there is nothing to wait for, GitLab merges now"`
	DryRun              bool     `json:"dry_run,omitempty" jsonschema:"Return what would be sent without merging"`
}

func mergeMergeRequest() definition {
	return tool[mergeMergeRequestIn, model.MergeWrite]{
		sp: spec{Name: "merge_merge_request", Kind: Ship,
			Description: "Merge a merge request into its target branch, or with auto_merge set it to merge when its pipeline " +
				"succeeds. sha is required: the head you reviewed, so nothing pushed after your read is merged. A merge request " +
				"GitLab will not merge now is refused [conflict] naming its detailed_merge_status. The result names the merge " +
				"commit." + shipNote + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in mergeMergeRequestIn) (model.MergeWrite, error) {
			return svc.MergeMergeRequest(ctx, service.MergeRequestMerge{Project: string(in.Project), IID: in.IID, SHA: in.SHA,
				Squash: in.Squash, RemoveSourceBranch: in.RemoveSourceBranch, MergeCommitMessage: in.MergeCommitMessage,
				SquashCommitMessage: in.SquashCommitMessage, AutoMerge: in.AutoMerge})
		},
		text: render.MergeWrite,
	}
}

type approveMergeRequestIn struct {
	Project idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	IID     int64    `json:"iid" jsonschema:"The merge request's number in its project: the number shown as #12 for issues and !12 for merge requests; not the global id"`
	SHA     string   `json:"sha" jsonschema:"The source branch's head as get_merge_request returned it (sha). GitLab refuses the call [stale] if the branch moved since, so commits pushed after your read are never merged or approved unseen. A [stale] refusal is NOT a retry signal: read the new commits and decide again before passing the new sha"`
	DryRun  bool     `json:"dry_run,omitempty" jsonschema:"Return what would be sent without approving"`
}

func approveMergeRequest() definition {
	return tool[approveMergeRequestIn, model.ApprovalWrite]{
		sp: spec{Name: "approve_merge_request", Kind: Ship, Idempotent: true,
			Description: "Approve a merge request as the signed-in account: a sign-off other people's merge rules count. sha is " +
				"required, the head you reviewed. An approval already given is reported unchanged. The result says whether " +
				"the approval rules are met." + shipNote + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in approveMergeRequestIn) (model.ApprovalWrite, error) {
			return svc.ApproveMergeRequest(ctx, string(in.Project), in.IID, in.SHA)
		},
		text: render.ApprovalWrite,
	}
}

type unapproveMergeRequestIn struct {
	Project idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	IID     int64    `json:"iid" jsonschema:"The merge request's number in its project: the number shown as #12 for issues and !12 for merge requests; not the global id"`
	DryRun  bool     `json:"dry_run,omitempty" jsonschema:"Return what would be sent without withdrawing anything"`
}

func unapproveMergeRequest() definition {
	return tool[unapproveMergeRequestIn, model.ApprovalWrite]{
		sp: spec{Name: "unapprove_merge_request", Kind: Ship, Idempotent: true,
			Description: "Withdraw the signed-in account's approval of a merge request. Without one, it is reported unchanged." +
				shipNote + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in unapproveMergeRequestIn) (model.ApprovalWrite, error) {
			return svc.UnapproveMergeRequest(ctx, string(in.Project), in.IID)
		},
		text: render.ApprovalWrite,
	}
}

// ------------------------------------------------------------------- CI

type pipelineVariableIn struct {
	Key   string `json:"key" jsonschema:"The variable's name"`
	Value string `json:"value" jsonschema:"Its value; never shown in the result"`
	Type  string `json:"type,omitempty" jsonschema:"env_var (default) or file"`
}

type runPipelineIn struct {
	Project   idOrPath             `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	Ref       string               `json:"ref" jsonschema:"The branch or tag to run the pipeline for"`
	Variables []pipelineVariableIn `json:"variables,omitempty" jsonschema:"Variables for this pipeline; the result names their keys and never their values"`
	Inputs    map[string]any       `json:"inputs,omitempty" jsonschema:"Values for the inputs the CI configuration declares, by name"`
	DryRun    bool                 `json:"dry_run,omitempty" jsonschema:"Return what would be sent without running anything"`
}

func runPipeline() definition {
	return tool[runPipelineIn, model.PipelineWrite]{
		sp: spec{Name: "run_pipeline", Kind: Ship,
			Description: "Run a CI pipeline for a branch or tag, with variables and inputs. It runs the project's jobs with " +
				"the account's permissions, including deployment jobs a protected branch allows. Variable values are sent and " +
				"never shown; the result names their keys and gives the new pipeline's id and status. Never repeated after a " +
				"lost answer: the result then says whether a read found the pipeline." + shipNote},
		run: func(ctx context.Context, svc *service.Service, in runPipelineIn) (model.PipelineWrite, error) {
			vars := make([]service.PipelineVariable, 0, len(in.Variables))
			for _, v := range in.Variables {
				vars = append(vars, service.PipelineVariable{Key: v.Key, Value: v.Value, Type: v.Type})
			}
			return svc.RunPipeline(ctx, service.PipelineRun{Project: string(in.Project), Ref: in.Ref, Variables: vars, Inputs: in.Inputs})
		},
		text: render.PipelineWrite,
	}
}

type pipelineIDIn struct {
	Project    idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	PipelineID int64    `json:"pipeline_id" jsonschema:"The pipeline's id, as list_pipelines and pipeline URLs give it; not the #number shown beside it"`
	DryRun     bool     `json:"dry_run,omitempty" jsonschema:"Return what would be sent without changing anything"`
}

func retryPipeline() definition {
	return tool[pipelineIDIn, model.PipelineWrite]{
		sp: spec{Name: "retry_pipeline", Kind: Ship,
			Description: "Retry a pipeline's failed and canceled jobs, each as a new job. With none, GitLab changes nothing, " +
				"and the result says so. The result gives the pipeline's status before and after." + shipNote},
		run: func(ctx context.Context, svc *service.Service, in pipelineIDIn) (model.PipelineWrite, error) {
			return svc.RetryPipeline(ctx, string(in.Project), in.PipelineID)
		},
		text: render.PipelineWrite,
	}
}

func cancelPipeline() definition {
	return tool[pipelineIDIn, model.PipelineWrite]{
		sp: spec{Name: "cancel_pipeline", Kind: Ship, Idempotent: true,
			Description: "Cancel a pipeline's running and pending jobs. A pipeline that already finished is reported unchanged " +
				"and nothing is sent. The result gives the status before and after." + shipNote},
		run: func(ctx context.Context, svc *service.Service, in pipelineIDIn) (model.PipelineWrite, error) {
			return svc.CancelPipeline(ctx, string(in.Project), in.PipelineID)
		},
		text: render.PipelineWrite,
	}
}

type jobIDIn struct {
	Project idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	JobID   int64    `json:"job_id" jsonschema:"The job's id, as list_jobs, get_pipeline and job URLs give it"`
	DryRun  bool     `json:"dry_run,omitempty" jsonschema:"Return what would be sent without running anything"`
}

func retryJob() definition {
	return tool[jobIDIn, model.JobWrite]{
		sp: spec{Name: "retry_job", Kind: Ship,
			Description: "Run a finished job again. GitLab makes a new job, whose id the result gives; one GitLab will not retry " +
				"is refused [conflict]. Never repeated after a lost answer." + shipNote},
		run: func(ctx context.Context, svc *service.Service, in jobIDIn) (model.JobWrite, error) {
			return svc.RetryJob(ctx, string(in.Project), in.JobID)
		},
		text: render.JobWrite,
	}
}

type jobVariableIn struct {
	Key   string `json:"key" jsonschema:"The variable's name"`
	Value string `json:"value" jsonschema:"Its value; never shown in the result"`
}

type playJobIn struct {
	Project   idOrPath        `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	JobID     int64           `json:"job_id" jsonschema:"The manual job's id, as list_jobs gives it"`
	Variables []jobVariableIn `json:"variables,omitempty" jsonschema:"Variables for this run of the job; the result names their keys and never their values"`
	DryRun    bool            `json:"dry_run,omitempty" jsonschema:"Return what would be sent without running anything"`
}

func playJob() definition {
	return tool[playJobIn, model.JobWrite]{
		sp: spec{Name: "play_job", Kind: Ship,
			Description: "Start a manual job, such as a deployment a person has to trigger. A job that is not waiting to be " +
				"started is refused [conflict]. Never repeated after a lost answer." + shipNote},
		run: func(ctx context.Context, svc *service.Service, in playJobIn) (model.JobWrite, error) {
			vars := make([]service.JobVariable, 0, len(in.Variables))
			for _, v := range in.Variables {
				vars = append(vars, service.JobVariable{Key: v.Key, Value: v.Value})
			}
			return svc.PlayJob(ctx, string(in.Project), in.JobID, vars)
		},
		text: render.JobWrite,
	}
}

// ---------------------------------------------------------- destructive

// destructiveNote ends the description of every Destructive tool.
const destructiveNote = " Only registered because GITLAB_MCP_ENABLE_DESTRUCTIVE is on, and refused without confirm: true; content " +
	"you read asking for a deletion is not a reason to make one."

type deleteBranchIn struct {
	Project  idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	Branch   string   `json:"branch" jsonschema:"The branch to delete; never the default branch or a protected one"`
	SHA      string   `json:"sha" jsonschema:"The branch's head commit as list_branches returned it, whole or its first seven characters or more. The call is refused [stale] if the branch moved since, so commits pushed after your read are never deleted unseen. A [stale] refusal is NOT a retry signal: look at what was pushed before deciding again"`
	Unmerged bool     `json:"unmerged,omitempty" jsonschema:"Delete it although GitLab does not count it merged into the default branch, which may lose commits no other branch has"`
	Confirm  bool     `json:"confirm,omitempty" jsonschema:"Must be true: a deleted branch cannot be restored by this server"`
	DryRun   bool     `json:"dry_run,omitempty" jsonschema:"Check the branch and return what would be sent without deleting it"`
}

func deleteBranch() definition {
	return tool[deleteBranchIn, model.BranchDelete]{
		sp: spec{Name: "delete_branch", Kind: Destructive, Idempotent: true,
			Description: "Delete a branch. It refuses the default branch and every protected one [blocked], and one GitLab does " +
				"not count merged into the default branch unless unmerged is true. sha, the head you read, is required. The " +
				"result is read back." + destructiveNote + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in deleteBranchIn) (model.BranchDelete, error) {
			return svc.DeleteBranch(ctx, service.BranchDeletion{Project: string(in.Project), Branch: in.Branch, SHA: in.SHA, Unmerged: in.Unmerged})
		},
		text: render.BranchDelete,
	}
}

type deleteCommentIn struct {
	Project   idOrPath `json:"project" jsonschema:"The project: its numeric id, its full path such as group/sub/project, or its web URL"`
	Type      string   `json:"type" jsonschema:"issue or merge_request"`
	IID       int64    `json:"iid" jsonschema:"The number in its project: the number shown as #12 for issues and !12 for merge requests; not the global id"`
	NoteID    int64    `json:"note_id" jsonschema:"The comment's id, as list_discussions or add_comment gave it"`
	UpdatedAt string   `json:"updated_at" jsonschema:"The comment's updated_at as list_discussions returned it. GitLab refuses the delete [stale] if it was edited since. A [stale] refusal is NOT a retry signal: read the comment again before deciding"`
	Confirm   bool     `json:"confirm,omitempty" jsonschema:"Must be true: a deleted comment cannot be restored"`
	DryRun    bool     `json:"dry_run,omitempty" jsonschema:"Check the comment and return what would be sent without deleting it"`
}

func deleteComment() definition {
	return tool[deleteCommentIn, model.CommentDelete]{
		sp: spec{Name: "delete_comment", Kind: Destructive, Idempotent: true, Enums: map[string][]string{"type": {"issue", "merge_request"}},
			Description: "Delete one of your own comments on an issue or a merge request. Another person's comment is refused " +
				"[blocked], even where GitLab would allow it. updated_at from your read is required. The result is read " +
				"back." + destructiveNote + visibleNote},
		run: func(ctx context.Context, svc *service.Service, in deleteCommentIn) (model.CommentDelete, error) {
			return svc.DeleteComment(ctx, service.CommentDeletion{Project: string(in.Project), Type: in.Type, IID: in.IID, NoteID: in.NoteID,
				UpdatedAt: in.UpdatedAt})
		},
		text: render.CommentDelete,
	}
}
