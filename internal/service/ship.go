package service

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/internal/gapi"
	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/v2/internal/model"
	"github.com/mmedum/gitlab-mcp/v2/internal/render"
)

// The Ship kind (§4.3): merging, approving, and running CI. The tools
// that reach here exist only when GITLAB_MCP_ENABLE_SHIP is on. Merge
// and approve carry the head sha the caller read, which GitLab holds
// (§4.6); the rest read the pipeline or job first, so the result can say
// what the call changed (§4.11).

// ------------------------------------------------------------- merging

// MergeRequestMerge is merge_merge_request's request.
type MergeRequestMerge struct {
	Project             string
	IID                 int64
	SHA                 string
	Squash              *bool
	RemoveSourceBranch  *bool
	MergeCommitMessage  string
	SquashCommitMessage string
	AutoMerge           bool
}

// MergeMergeRequest merges a merge request at the head the caller read,
// or sets it to merge when its pipeline succeeds.
func (s *Service) MergeMergeRequest(ctx context.Context, in MergeRequestMerge) (model.MergeWrite, error) {
	sha := strings.TrimSpace(in.SHA)
	if sha == "" {
		return model.MergeWrite{}, errNoSHA("merged")
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.MergeWrite{}, err
	}
	mr, err := s.client.GetMergeRequest(ctx, t.p, in.IID)
	if err != nil {
		return model.MergeWrite{}, err
	}
	if mr.State == "merged" {
		out := mergeWrite(t, mr, "unchanged")
		out.Notes = []string{"It was already merged."}
		return out, nil
	}
	if err := mergeable(mr, sha); err != nil {
		return model.MergeWrite{}, err
	}
	body := gapi.MergeBody{SHA: sha, Squash: in.Squash, ShouldRemoveSourceBranch: in.RemoveSourceBranch,
		MergeCommitMessage: in.MergeCommitMessage, SquashCommitMessage: in.SquashCommitMessage, AutoMerge: in.AutoMerge}
	if gapi.IsDryRun(ctx) {
		out := mergeWrite(t, mr, "dry_run")
		verb := "merge the merge request"
		if in.AutoMerge {
			verb = "set the merge request to merge when its pipeline succeeds"
		}
		out.DryRun, out.WouldSend = true, preview("PUT", verb, fieldsOf(body))
		return out, nil
	}
	if err := ask(ctx, render.AskMerge(t.ref.Project.Path, in.IID, mr.Title, mr.SourceBranch, mr.TargetBranch, sha, in.AutoMerge,
		in.Squash, in.RemoveSourceBranch)); err != nil {
		return model.MergeWrite{}, err
	}
	res, err := s.client.MergeMergeRequest(ctx, t.p, in.IID, body)
	if err != nil {
		return s.mergeFailed(ctx, t, in.IID, sha, in.AutoMerge, err)
	}
	switch {
	case res.State == "merged":
		return mergeWrite(t, res, "merged"), nil
	case in.AutoMerge && res.MergeWhenPipelineSucceeds:
		return mergeWrite(t, res, "auto_merge_set"), nil
	}
	return model.MergeWrite{}, gapi.Errf(gapi.ClassUnexpected, "GitLab accepted the merge but reported the merge request %s and not set to merge", res.State)
}

// mergeable refuses what GitLab would refuse, with the reason and the
// next step, before anything is sent.
func mergeable(mr *gitlab.MergeRequest, sha string) error {
	switch {
	case mr.State != "opened":
		return gapi.Errf(gapi.ClassConflict, "the merge request is %s; reopen it with update_merge_request before merging", mr.State)
	case mr.SHA != sha:
		return gapi.Errf(gapi.ClassStale, "the source branch moved since it was read: its head is now %s, not %s. Read the merge request "+
			"and its diff again, then pass the new sha", mr.SHA, sha)
	case mr.Draft:
		return gapi.Errf(gapi.ClassConflict, "a draft merge request cannot be merged: mark it ready with update_merge_request draft false first")
	}
	return nil
}

// mergeFailed reads the merge request after a failed merge: a merge
// whose answer was lost shows as merged at the head sent, and a refusal
// of its state is named by GitLab's own merge status.
func (s *Service) mergeFailed(ctx context.Context, t target, iid int64, sha string, autoMerge bool, err error) (model.MergeWrite, error) {
	if gapi.IsClass(err, gapi.ClassStale) {
		return model.MergeWrite{}, err
	}
	after, readErr := s.client.GetMergeRequest(ctx, t.p, iid)
	if readErr != nil {
		return model.MergeWrite{}, err
	}
	switch {
	case after.State == "merged" && after.SHA == sha:
		out := mergeWrite(t, after, "merged")
		out.Notes = []string{"GitLab answered the merge with an error, but a read afterwards shows it merged at this head."}
		return out, nil
	case autoMerge && after.State == "opened" && after.MergeWhenPipelineSucceeds && after.SHA == sha:
		out := mergeWrite(t, after, "auto_merge_set")
		out.Notes = []string{"GitLab answered with an error, but a read afterwards shows it set to merge when its pipeline succeeds."}
		return out, nil
	}
	var e *gapi.Error
	if errors.As(err, &e) && (e.Status == 405 || e.Status == 406 || e.Status == 422) {
		// GitLab's 405 "Method Not Allowed" and 422 "Branch cannot be
		// merged" say only no (lib/api/merge_requests.rb).
		return model.MergeWrite{}, gapi.Errf(gapi.ClassConflict, "GitLab would not merge it now, and nothing was merged: its "+
			"detailed_merge_status is %q. get_merge_request shows the checks; auto_merge merges once they pass", after.DetailedMergeStatus)
	}
	return model.MergeWrite{}, err
}

func mergeWrite(t target, mr *gitlab.MergeRequest, outcome string) model.MergeWrite {
	out := model.MergeWrite{Outcome: outcome, Write: model.Write{Target: t.ref}, IID: mr.IID, WebURL: mr.WebURL, State: mr.State,
		SourceBranch: mr.SourceBranch, TargetBranch: mr.TargetBranch, SHA: mr.SHA, MergedAt: mr.MergedAt,
		DetailedMergeStatus: mr.DetailedMergeStatus}
	if mr.MergeCommitSHA != nil {
		out.MergeCommitSHA = *mr.MergeCommitSHA
	}
	if mr.SquashCommitSHA != nil {
		out.SquashCommitSHA = *mr.SquashCommitSHA
	}
	if mr.MergeUser != nil {
		out.MergedBy = mr.MergeUser.Username
	}
	return out
}

func errNoSHA(what string) error {
	return gapi.Errf(gapi.ClassInvalid, "sha is required: the head sha get_merge_request returned, so a push made after "+
		"your read is never %s unseen", what)
}

// ----------------------------------------------------------- approving

// ApproveMergeRequest approves a merge request at the head the caller
// read. An approval is someone's sign-off another person's merge rule
// counts, which is why it is Ship (§4.3, §17.2).
func (s *Service) ApproveMergeRequest(ctx context.Context, raw string, iid int64, sha string) (model.ApprovalWrite, error) {
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return model.ApprovalWrite{}, errNoSHA("approved")
	}
	t, err := s.writeTarget(ctx, raw)
	if err != nil {
		return model.ApprovalWrite{}, err
	}
	var mr *gitlab.MergeRequest
	var before *gitlab.Approvals
	if err := parallel(
		func() (err error) { mr, err = s.client.GetMergeRequest(ctx, t.p, iid); return err },
		func() (err error) { before, err = s.client.GetMergeRequestApprovals(ctx, t.p, iid); return err },
	); err != nil {
		return model.ApprovalWrite{}, err
	}
	switch {
	case mr.State != "opened":
		return model.ApprovalWrite{}, gapi.Errf(gapi.ClassConflict, "the merge request is %s, and only an open one is approved", mr.State)
	case mr.SHA != sha:
		return model.ApprovalWrite{}, gapi.Errf(gapi.ClassStale, "the source branch moved since it was read: its head is now %s, not %s. "+
			"Review the new commits, then pass the new sha", mr.SHA, sha)
	case before.UserHasApproved:
		out := approvalWrite(t, iid, sha, before, "unchanged")
		out.Notes = []string{"You had already approved it."}
		return out, nil
	}
	if gapi.IsDryRun(ctx) {
		out := approvalWrite(t, iid, sha, before, "dry_run")
		out.DryRun, out.WouldSend = true, preview("POST", "approve the merge request", []string{"sha"})
		return out, nil
	}
	if err := ask(ctx, render.AskApprove(t.ref.Project.Path, iid, mr.Title, sha)); err != nil {
		return model.ApprovalWrite{}, err
	}
	res, err := s.client.ApproveMergeRequest(ctx, t.p, iid, sha)
	if err != nil {
		return s.approvalFailed(ctx, t, iid, sha, err, true)
	}
	return approvalWrite(t, iid, sha, res, "approved"), nil
}

// UnapproveMergeRequest withdraws the signed-in account's approval.
func (s *Service) UnapproveMergeRequest(ctx context.Context, raw string, iid int64) (model.ApprovalWrite, error) {
	t, err := s.writeTarget(ctx, raw)
	if err != nil {
		return model.ApprovalWrite{}, err
	}
	before, err := s.client.GetMergeRequestApprovals(ctx, t.p, iid)
	if err != nil {
		return model.ApprovalWrite{}, err
	}
	if !before.UserHasApproved {
		out := approvalWrite(t, iid, "", before, "unchanged")
		out.Notes = []string{"You had not approved it."}
		return out, nil
	}
	if gapi.IsDryRun(ctx) {
		out := approvalWrite(t, iid, "", before, "dry_run")
		out.DryRun, out.WouldSend = true, preview("POST", "withdraw your approval", nil)
		return out, nil
	}
	res, err := s.client.UnapproveMergeRequest(ctx, t.p, iid)
	if err != nil {
		return s.approvalFailed(ctx, t, iid, "", err, false)
	}
	return approvalWrite(t, iid, "", res, "unapproved"), nil
}

// approvalFailed reads the approvals after a failed approve or
// unapprove: the answer may have been lost after it landed, and GitLab's
// 401 for an approval it will not take is not a sign-in problem when the
// same token can still read.
func (s *Service) approvalFailed(ctx context.Context, t target, iid int64, sha string, err error, approve bool) (model.ApprovalWrite, error) {
	if gapi.IsClass(err, gapi.ClassStale) {
		return model.ApprovalWrite{}, err
	}
	after, readErr := s.client.GetMergeRequestApprovals(ctx, t.p, iid)
	if readErr != nil {
		return model.ApprovalWrite{}, err
	}
	outcome := "unapproved"
	if approve {
		outcome = "approved"
	}
	if after.UserHasApproved == approve {
		out := approvalWrite(t, iid, sha, after, outcome)
		out.Notes = []string{"GitLab answered with an error, but a read afterwards shows the change was made."}
		return out, nil
	}
	var e *gapi.Error
	if approve && errors.As(err, &e) && e.Status == 401 {
		return model.ApprovalWrite{}, gapi.Errf(gapi.ClassForbidden, "GitLab did not take the approval, and nothing changed: the "+
			"account may not be an eligible approver here, or the project may forbid approving your own merge request")
	}
	if gapi.IsClass(err, gapi.ClassAmbiguousOutcome) {
		return model.ApprovalWrite{}, gapi.Wrap(gapi.ClassAmbiguousOutcome, err, "GitLab did not confirm the change, and a read shows it "+
			"was not made. Nothing was repeated; calling again is safe")
	}
	return model.ApprovalWrite{}, err
}

func approvalWrite(t target, iid int64, sha string, a *gitlab.Approvals, outcome string) model.ApprovalWrite {
	by := make([]string, 0, len(a.ApprovedBy))
	for _, ap := range a.ApprovedBy {
		by = append(by, ap.User.Username)
	}
	return model.ApprovalWrite{Outcome: outcome, Write: model.Write{Target: t.ref}, IID: iid, SHA: sha, YouApproved: a.UserHasApproved,
		Approved: a.Approved, ApprovedBy: by, ApprovalsLeft: a.ApprovalsLeft}
}

// ------------------------------------------------------------------- CI

// PipelineVariable is one variable run_pipeline sets.
type PipelineVariable struct {
	Key   string
	Value string
	Type  string // env_var (default) or file
}

// PipelineRun is run_pipeline's request.
type PipelineRun struct {
	Project   string
	Ref       string
	Variables []PipelineVariable
	Inputs    map[string]any
}

func (in PipelineRun) check() error {
	if strings.TrimSpace(in.Ref) == "" {
		return gapi.Errf(gapi.ClassInvalid, "ref is empty: name the branch or tag to run the pipeline for")
	}
	keys := make([]string, 0, len(in.Variables))
	for _, v := range in.Variables {
		if v.Type != "" && v.Type != "env_var" && v.Type != "file" {
			return gapi.Errf(gapi.ClassInvalid, "variable %q: type must be env_var or file", v.Key)
		}
		keys = append(keys, v.Key)
	}
	return checkKeys(keys)
}

// checkKeys refuses a variable with no key and a key given twice.
func checkKeys(keys []string) error {
	for i, k := range keys {
		switch {
		case strings.TrimSpace(k) == "":
			return gapi.Errf(gapi.ClassInvalid, "variable %d has no key", i+1)
		case slices.Contains(keys[:i], k):
			return gapi.Errf(gapi.ClassInvalid, "the variable %q is given twice", k)
		}
	}
	return nil
}

// RunPipeline runs a pipeline for a ref. Variable values are sent and
// never shown: a result names their keys (§7.6).
func (s *Service) RunPipeline(ctx context.Context, in PipelineRun) (model.PipelineWrite, error) {
	if err := in.check(); err != nil {
		return model.PipelineWrite{}, err
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.PipelineWrite{}, err
	}
	body := gapi.PipelineCreate{Ref: in.Ref, Inputs: in.Inputs}
	keys := make([]string, 0, len(in.Variables))
	for _, v := range in.Variables {
		body.Variables = append(body.Variables, gapi.PipelineVariable{Key: v.Key, Value: v.Value, VariableType: v.Type})
		keys = append(keys, v.Key)
	}
	inputs := nonNil(slices.Sorted(maps.Keys(in.Inputs)))
	if gapi.IsDryRun(ctx) {
		return model.PipelineWrite{Outcome: "dry_run", Ref: in.Ref, Variables: keys, Inputs: inputs, Write: model.Write{DryRun: true,
			Target: t.ref, WouldSend: preview("POST", "run a pipeline", fieldsOf(body))}}, nil
	}
	if err := s.askRun(ctx, t, in.Ref, keys, inputs); err != nil {
		return model.PipelineWrite{}, err
	}
	start := time.Now()
	pl, err := s.client.CreatePipeline(ctx, t.p, body)
	if err != nil {
		return model.PipelineWrite{}, settle(err, "pipeline", func() (string, error) {
			return s.findPipeline(ctx, t.p, in.Ref, start)
		})
	}
	out := pipelineWrite(t, pl, "", "created")
	out.Variables, out.Inputs = keys, inputs
	return out, nil
}

// askRun asks before a pipeline runs on the default branch or a
// protected branch or tag, whose jobs see protected variables and may
// deploy (§4.12). GitLab's own flags on the ref decide. A fully
// qualified ref, refs/heads/ or refs/tags/, is read as the branch or tag
// it names, as GitLab reads it; otherwise a branch comes before a tag of
// the same name, as a pipeline takes it. A ref that is neither is asked
// about too, since whether it is protected cannot be told. The ref is
// read only when a question could go out.
func (s *Service) askRun(ctx context.Context, t target, ref string, variables, inputs []string) error {
	if !asks(ctx) {
		return nil
	}
	kind, err := s.refKind(ctx, t, ref)
	if err != nil || kind == 0 {
		return err
	}
	return ask(ctx, render.AskRunPipeline(t.ref.Project.Path, ref, kind, variables, inputs))
}

// refKind is why a pipeline on ref asks, or 0 when it does not.
func (s *Service) refKind(ctx context.Context, t target, ref string) (render.RefKind, error) {
	name, onlyTag := strings.CutPrefix(ref, "refs/tags/")
	name, onlyBranch := strings.CutPrefix(name, "refs/heads/")
	if !onlyTag {
		b, err := s.client.GetBranch(ctx, t.p, name)
		switch {
		case err == nil && b.Default:
			return render.DefaultBranch, nil
		case err == nil && b.Protected:
			return render.ProtectedBranch, nil
		case err == nil:
			return 0, nil
		case !gapi.IsClass(err, gapi.ClassNotFound):
			return 0, err
		}
	}
	if !onlyBranch {
		tag, err := s.client.GetTag(ctx, t.p, name)
		switch {
		case err == nil && tag.Protected:
			return render.ProtectedTag, nil
		case err == nil:
			return 0, nil
		case !gapi.IsClass(err, gapi.ClassNotFound):
			return 0, err
		}
	}
	return render.UnknownRef, nil
}

// findPipeline settles a lost run_pipeline: a pipeline on the ref that
// this account started through the API since the call began (§4.5).
func (s *Service) findPipeline(ctx context.Context, p gapi.Project, ref string, start time.Time) (string, error) {
	me, err := s.me(ctx)
	if err != nil {
		return "", err
	}
	rows, _, err := s.client.ListPipelines(ctx, p, gapi.PipelineQuery{Ref: ref, Source: "api", Username: me.Username,
		CreatedAfter: start.Add(-settleSkew), OrderBy: "id", Sort: "desc"}, gapi.ListOptions{PerPage: 5})
	if err != nil || len(rows) == 0 {
		return "", err
	}
	return fmt.Sprintf("pipeline %d, %s", rows[0].ID, rows[0].Status), nil
}

// RunMergeRequestPipeline runs a merge request's pipeline: merged
// results where the project has them on, detached otherwise, as GitLab
// decides (MergeRequests::CreatePipelineService).
func (s *Service) RunMergeRequestPipeline(ctx context.Context, raw string, iid int64) (model.MergeRequestPipelineWrite, error) {
	t, err := s.writeTarget(ctx, raw)
	if err != nil {
		return model.MergeRequestPipelineWrite{}, err
	}
	mr, err := s.client.GetMergeRequest(ctx, t.p, iid)
	if err != nil {
		return model.MergeRequestPipelineWrite{}, err
	}
	if mr.SourceProjectID != t.project.ID {
		// GitLab runs a fork's pipeline in this project, with its
		// unprotected variables and its runners, when the project allows
		// it, and in the fork otherwise. The setting is shown only to
		// maintainers, so where it would run cannot be told (§17.14).
		return model.MergeRequestPipelineWrite{}, gapi.Errf(gapi.ClassBlocked, "the merge request comes from a fork, and its pipeline "+
			"would run the fork's code, in this project with its variables and runners or in the fork; nothing was sent. Review the "+
			"fork's code, then run the pipeline from the merge request's Pipelines tab in GitLab")
	}
	out := model.MergeRequestPipelineWrite{Outcome: "created", Write: model.Write{Target: t.ref}, IID: iid,
		SourceBranch: mr.SourceBranch, TargetBranch: mr.TargetBranch}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun, out.WouldSend = "dry_run", true, preview("POST", "run a merge request pipeline", nil)
		return out, nil
	}
	// The question binds the head read above, and a head that moved
	// before the answer is refused by that (§4.12). The create cannot
	// carry a head, so one that moves after it is named in the result.
	if err := s.askMRRun(ctx, t, mr); err != nil {
		return model.MergeRequestPipelineWrite{}, err
	}
	floor, err := s.newestPipeline(ctx, t.p)
	if err != nil {
		return model.MergeRequestPipelineWrite{}, err
	}
	start := time.Now()
	pl, err := s.client.CreateMergeRequestPipeline(ctx, t.p, iid)
	if err != nil {
		return model.MergeRequestPipelineWrite{}, mrPipelineFailed(err, func() (string, error) {
			return s.findMRPipeline(ctx, t, mr, floor, start)
		})
	}
	out.PipelineID, out.PipelineIID, out.Kind, out.Status = pl.ID, pl.IID, mrPipelineKind(pl.Ref, mr.IID), pl.Status
	out.Ref, out.SHA, out.Source, out.WebURL = pl.Ref, pl.SHA, pl.Source, pl.WebURL
	if out.Kind == "detached" && pl.SHA != mr.SHA {
		out.Notes = []string{fmt.Sprintf("The source branch moved after it was read: the pipeline runs %s, not the head %s that was "+
			"read. Look at what was pushed", pl.SHA, mr.SHA)}
	}
	return out, nil
}

// mrPipelineKind names a merge request pipeline by its ref. A
// same-project account that may not push to the source branch is
// refused, not given a pipeline on the branch (§18 row 104), so these
// two refs are the ones a create answers with.
func mrPipelineKind(ref string, iid int64) string {
	switch ref {
	case fmt.Sprintf("refs/merge-requests/%d/merge", iid):
		return "merged_results"
	case fmt.Sprintf("refs/merge-requests/%d/head", iid):
		return "detached"
	}
	return ""
}

// permissionRefused is GitLab's 400 for an account that may not create
// the pipeline or run one for the branch
// (Gitlab::Ci::Pipeline::Chain::Validate::Abilities).
var permissionRefused = regexp.MustCompile(`(?i)insufficient permissions to create a new pipeline|do not have sufficient permission to run a pipeline`)

// mrPipelineFailed names GitLab's refusals of a merge request pipeline
// and settles a lost answer by reading (§4.5).
func mrPipelineFailed(err error, find func() (string, error)) error {
	var e *gapi.Error
	if !errors.As(err, &e) {
		return err
	}
	switch {
	case e.Status == 405:
		return gapi.Wrap(gapi.ClassConflict, err, "GitLab ran no pipeline: the merge request has no commits yet. Push a commit to "+
			"its source branch first")
	case e.Status == 400 && permissionRefused.MatchString(e.Message):
		return gapi.Wrap(gapi.ClassForbidden, err, "GitLab refused to run the pipeline for this account, and nothing ran "+
			"(it answers 400 for this, not 403). %s", e.Message)
	case e.Status == 400:
		return gapi.Wrap(gapi.ClassConflict, err, "GitLab could not create the pipeline, and nothing ran. Often the CI "+
			"configuration has no jobs for merge request pipelines; lint_ci checks it. %s", e.Message)
	}
	return settle(err, "merge request pipeline", find)
}

// newestPipeline is the id of the project's newest pipeline, 0 for none.
// A pipeline the create makes has a higher one, which tells it from the
// pipelines GitLab started before, under this account's name too, for a
// push or for opening the merge request.
func (s *Service) newestPipeline(ctx context.Context, p gapi.Project) (int64, error) {
	rows, _, err := s.client.ListPipelines(ctx, p, gapi.PipelineQuery{OrderBy: "id", Sort: "desc"}, gapi.ListOptions{PerPage: 1})
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	return rows[0].ID, nil
}

// findMRPipeline settles a lost run_merge_request_pipeline: a merge
// request pipeline this account started on one of the merge request's
// refs, newer than the newest pipeline before the call. GitLab filters
// the listing and orders it by id, newest first, so its first row
// answers: any match is newer than it or no newer than the floor.
func (s *Service) findMRPipeline(ctx context.Context, t target, mr *gitlab.MergeRequest, floor int64, start time.Time) (string, error) {
	me, err := s.me(ctx)
	if err != nil {
		return "", err
	}
	for _, ref := range []string{fmt.Sprintf("refs/merge-requests/%d/head", mr.IID), fmt.Sprintf("refs/merge-requests/%d/merge", mr.IID)} {
		rows, _, err := s.client.ListPipelines(ctx, t.p, gapi.PipelineQuery{Ref: ref, Source: "merge_request_event", Username: me.Username,
			CreatedAfter: start.Add(-settleSkew), OrderBy: "id", Sort: "desc"}, gapi.ListOptions{PerPage: 1})
		if err != nil {
			return "", err
		}
		if len(rows) > 0 && rows[0].ID > floor {
			return fmt.Sprintf("pipeline %d, %s, on %s", rows[0].ID, rows[0].Status, rows[0].Ref), nil
		}
	}
	return "", nil
}

// askMRRun asks before a merge request pipeline whose jobs may see
// protected variables: GitLab gives them one only when its source and
// target branches are both protected, in the same project
// (Ci::Pipeline#protected_for_merge_request?). The project's setting
// and the account's push rights, which it also checks, are not read, so
// it asks in a few more cases than that. A branch that cannot be read
// may be protected by name, so it counts as protected. The branches are
// read only when a question could go out.
func (s *Service) askMRRun(ctx context.Context, t target, mr *gitlab.MergeRequest) error {
	if !asks(ctx) {
		return nil
	}
	var source, tgt render.RefKind
	if err := parallel(
		func() (err error) { source, err = s.refKind(ctx, t, "refs/heads/"+mr.SourceBranch); return err },
		func() (err error) { tgt, err = s.refKind(ctx, t, "refs/heads/"+mr.TargetBranch); return err },
	); err != nil || source == 0 || tgt == 0 {
		return err
	}
	return ask(ctx, render.AskRunMergeRequestPipeline(t.ref.Project.Path, mr.IID, mr.Title, mr.SourceBranch, mr.TargetBranch,
		mr.SHA, source, tgt))
}

// RetryPipeline retries a pipeline's failed and canceled jobs.
func (s *Service) RetryPipeline(ctx context.Context, raw string, id int64) (model.PipelineWrite, error) {
	t, err := s.writeTarget(ctx, raw)
	if err != nil {
		return model.PipelineWrite{}, err
	}
	before, err := s.client.GetPipeline(ctx, t.p, id)
	if err != nil {
		return model.PipelineWrite{}, err
	}
	if gapi.IsDryRun(ctx) {
		out := pipelineWrite(t, before, before.Status, "dry_run")
		out.DryRun, out.WouldSend = true, preview("POST", "retry the pipeline's failed and canceled jobs", nil)
		return out, nil
	}
	pl, err := s.client.RetryPipeline(ctx, t.p, id)
	if err != nil {
		return model.PipelineWrite{}, settle(err, "pipeline retry", func() (string, error) {
			after, err := s.client.GetPipeline(ctx, t.p, id)
			if err != nil || after.Status == before.Status {
				return "", err
			}
			return fmt.Sprintf("the pipeline went from %s to %s", before.Status, after.Status), nil
		})
	}
	out := pipelineWrite(t, pl, before.Status, "retried")
	if pl.Status == before.Status {
		out.Outcome = "unchanged"
		out.Notes = []string{fmt.Sprintf("GitLab reported the pipeline %s before and after. It retries failed and canceled jobs only, "+
			"and a pipeline still running keeps its status while retried jobs run; list_jobs shows the jobs", pl.Status)}
	}
	return out, nil
}

// finished are the pipeline statuses nothing can cancel.
var finished = []string{"success", "failed", "canceled", "skipped"}

// cancelable are the pipeline statuses GitLab's cancel acts on, for any
// pipeline but an external one (Ci::HasStatus::CANCELABLE_STATUSES,
// §18 row 91).
var cancelable = []string{"created", "waiting_for_resource", "preparing", "waiting_for_callback", "pending", "running",
	"manual", "scheduled"}

// CancelPipeline cancels a pipeline's running and pending jobs.
func (s *Service) CancelPipeline(ctx context.Context, raw string, id int64) (model.PipelineWrite, error) {
	t, err := s.writeTarget(ctx, raw)
	if err != nil {
		return model.PipelineWrite{}, err
	}
	before, err := s.client.GetPipeline(ctx, t.p, id)
	if err != nil {
		return model.PipelineWrite{}, err
	}
	if slices.Contains(finished, before.Status) {
		out := pipelineWrite(t, before, before.Status, "unchanged")
		out.Notes = []string{"It had already finished as " + before.Status + "; nothing was sent."}
		return out, nil
	}
	if gapi.IsDryRun(ctx) {
		out := pipelineWrite(t, before, before.Status, "dry_run")
		out.DryRun, out.WouldSend = true, preview("POST", "cancel the pipeline", nil)
		return out, nil
	}
	pl, err := s.client.CancelPipeline(ctx, t.p, id)
	if err != nil {
		return model.PipelineWrite{}, err
	}
	outcome, note := cancelOutcome(before, pl)
	out := pipelineWrite(t, pl, before.Status, outcome)
	if note != "" {
		out.Notes = []string{note}
	}
	return out, nil
}

// cancelOutcome says what a cancel did from the pipeline read first and
// GitLab's answer. GitLab cancels the jobs before it answers and
// recomputes the pipeline's status after, so an answer that still reads
// running is a cancel that took effect.
func cancelOutcome(before, answer *gitlab.PipelineDetail) (outcome, note string) {
	switch {
	case answer.Status == "canceled" || answer.Status == "canceling":
		return "canceled", ""
	case slices.Contains(finished, answer.Status), before.Source == "external", !slices.Contains(cancelable, before.Status):
		return "unchanged", "GitLab answered with the pipeline " + answer.Status + ": nothing in it could be canceled."
	default:
		return "canceled", "GitLab canceled the pipeline's jobs; the pipeline still reads " + answer.Status +
			" until GitLab recomputes its status, which get_pipeline shows."
	}
}

// pipelineWrite is a pipeline write's result; before is the status read
// first, "" for a new pipeline.
func pipelineWrite(t target, pl *gitlab.PipelineDetail, before, outcome string) model.PipelineWrite {
	return model.PipelineWrite{Outcome: outcome, Write: model.Write{Target: t.ref}, PipelineID: pl.ID, IID: pl.IID, StatusBefore: before, Status: pl.Status,
		Ref: pl.Ref, SHA: pl.SHA, Source: pl.Source, WebURL: pl.WebURL, Variables: []string{}, Inputs: []string{}}
}

// RetryJob runs a job again as a new job.
func (s *Service) RetryJob(ctx context.Context, raw string, id int64, inputs map[string]any) (model.JobWrite, error) {
	inputNames, err := inputNamesOf(inputs)
	if err != nil {
		return model.JobWrite{}, err
	}
	t, err := s.writeTarget(ctx, raw)
	if err != nil {
		return model.JobWrite{}, err
	}
	job, err := s.client.GetJob(ctx, t.p, id)
	if err != nil {
		return model.JobWrite{}, err
	}
	if gapi.IsDryRun(ctx) {
		out := jobWrite(t, id, job, "dry_run")
		out.Inputs = inputNames
		out.DryRun, out.WouldSend = true, preview("POST", "retry the job as a new job", names(field{"inputs", len(inputs) > 0}))
		return out, nil
	}
	res, err := s.client.RetryJob(ctx, t.p, id, inputs)
	if err != nil {
		return model.JobWrite{}, settle(err, "job retry", func() (string, error) {
			return s.findRetry(ctx, t.p, job)
		})
	}
	out := jobWrite(t, id, res, "retried")
	out.Inputs = inputNames
	return out, nil
}

// inputNamesOf names the inputs sent, in order, refusing one without a
// name; GitLab checks the rest against the specification that declares
// them.
func inputNamesOf(inputs map[string]any) ([]string, error) {
	names := nonNil(slices.Sorted(maps.Keys(inputs)))
	if slices.Contains(names, "") {
		return nil, gapi.Errf(gapi.ClassInvalid, "an input has no name")
	}
	return names, nil
}

// findRetry settles a lost retry_job: a newer job of the same name in
// the same pipeline.
func (s *Service) findRetry(ctx context.Context, p gapi.Project, job *gitlab.Job) (string, error) {
	rows, _, err := readPages(maxJobPages, func(o gapi.ListOptions) ([]gitlab.Job, gapi.Page, error) {
		return s.client.ListPipelineJobs(ctx, p, job.Pipeline.ID, gapi.JobQuery{IncludeRetried: true}, o)
	})
	if err != nil {
		return "", err
	}
	for _, j := range rows {
		if j.Name == job.Name && j.ID > job.ID {
			return fmt.Sprintf("job %d, %s", j.ID, j.Status), nil
		}
	}
	return "", nil
}

// maxJobPages bounds the jobs read to settle a lost retry.
const maxJobPages = 5

// JobVariable is one variable play_job sets.
type JobVariable struct {
	Key   string
	Value string
}

// PlayJob starts a manual job, with variables for this run. Their
// values are sent and never shown.
func (s *Service) PlayJob(ctx context.Context, raw string, id int64, vars []JobVariable, inputs map[string]any) (model.JobWrite, error) {
	inputNames, err := inputNamesOf(inputs)
	if err != nil {
		return model.JobWrite{}, err
	}
	keys := make([]string, 0, len(vars))
	body := make([]gapi.JobVariable, 0, len(vars))
	for _, v := range vars {
		keys = append(keys, v.Key)
		body = append(body, gapi.JobVariable(v))
	}
	if err := checkKeys(keys); err != nil {
		return model.JobWrite{}, err
	}
	t, err := s.writeTarget(ctx, raw)
	if err != nil {
		return model.JobWrite{}, err
	}
	job, err := s.client.GetJob(ctx, t.p, id)
	if err != nil {
		return model.JobWrite{}, err
	}
	if gapi.IsDryRun(ctx) {
		out := jobWrite(t, id, job, "dry_run")
		out.Variables, out.Inputs = keys, inputNames
		out.DryRun, out.WouldSend = true, preview("POST", "run the manual job", names(field{"job_variables_attributes", len(body) > 0},
			field{"job_inputs", len(inputs) > 0}))
		return out, nil
	}
	if job.Status != "manual" && job.Status != "scheduled" {
		// Refused before the person is asked: the question would describe
		// a run GitLab will not start.
		return model.JobWrite{}, gapi.Errf(gapi.ClassConflict, "the job is %s, and only a manual or scheduled job is played; "+
			"retry_job runs a finished one again", job.Status)
	}
	if err := ask(ctx, render.AskPlayJob(t.ref.Project.Path, id, job.Name, job.Stage, job.Pipeline.ID, keys, inputNames)); err != nil {
		return model.JobWrite{}, err
	}
	res, err := s.client.PlayJob(ctx, t.p, id, body, inputs)
	var e *gapi.Error
	if errors.As(err, &e) && e.Class == gapi.ClassForbidden && e.Status == 403 && len(body) > 0 && strings.HasSuffix(e.Message, ": 403 Forbidden") {
		// GitLab answers a bare 403, with no reason, when the project's
		// minimum role for pipeline variables is above the account's
		// (phase 3 live run), and for other refusals too, so the cause is
		// offered, not stated.
		return model.JobWrite{}, gapi.Wrap(gapi.ClassForbidden, err, "GitLab refused to run the job with variables, saying only "+
			"403 Forbidden. The cause may be that %s", gapi.PipelineVariablesRefused)
	}
	if err != nil {
		return model.JobWrite{}, settle(err, "job run", func() (string, error) {
			after, err := s.client.GetJob(ctx, t.p, id)
			if err != nil || after.Status == job.Status {
				return "", err
			}
			return fmt.Sprintf("the job went from %s to %s", job.Status, after.Status), nil
		})
	}
	out := jobWrite(t, id, res, "played")
	out.Variables, out.Inputs = keys, inputNames
	return out, nil
}

func jobWrite(t target, from int64, j *gitlab.Job, outcome string) model.JobWrite {
	return model.JobWrite{Outcome: outcome, Write: model.Write{Target: t.ref}, JobID: j.ID, FromJobID: from, Name: j.Name, Stage: j.Stage,
		Status: j.Status, PipelineID: j.Pipeline.ID, WebURL: j.WebURL, Variables: []string{}, Inputs: []string{}}
}
