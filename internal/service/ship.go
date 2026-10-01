package service

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
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

// ----------------------------------------------------------- auto-merge

// autoMergeSet reports a merge request waiting to merge when its
// pipeline succeeds: GitLab's merge_when_pipeline_succeeds is its
// auto_merge_enabled, for every strategy.
func autoMergeSet(mr *gitlab.MergeRequest) bool {
	return mr.State == "opened" && mr.MergeWhenPipelineSucceeds
}

// CancelAutoMerge stops a merge request from merging when its pipeline
// succeeds. GitLab answers 201 whether or not it canceled, with the
// outcome in the body (§18 row 109), so the body and a read afterwards
// say what happened. Nothing is sent when no auto-merge is set.
func (s *Service) CancelAutoMerge(ctx context.Context, raw string, iid int64) (model.AutoMergeCancel, error) {
	t, err := s.writeTarget(ctx, raw)
	if err != nil {
		return model.AutoMergeCancel{}, err
	}
	mr, err := s.client.GetMergeRequest(ctx, t.p, iid)
	if err != nil {
		return model.AutoMergeCancel{}, err
	}
	if out, done := mergingOrMerged(t, mr, "", false); done {
		return out, nil
	}
	if !autoMergeSet(mr) {
		out := autoMergeCancel(t, mr, "", "unchanged")
		out.Notes = []string{"It was not set to merge automatically; nothing was sent."}
		return out, nil
	}
	setBy := ""
	if mr.MergeUser != nil {
		setBy = mr.MergeUser.Username
	}
	if gapi.IsDryRun(ctx) {
		out := autoMergeCancel(t, mr, setBy, "dry_run")
		out.DryRun, out.WouldSend = true, preview("POST", "cancel the merge request's auto-merge", nil)
		return out, nil
	}
	res, err := s.client.CancelAutoMerge(ctx, t.p, iid)
	// The read settles every answer: GitLab's 201 says nothing by its
	// status, and a failed answer may follow a cancel that landed.
	after, readErr := s.client.GetMergeRequest(ctx, t.p, iid)
	if readErr != nil {
		return cancelUnread(t, mr, setBy, res, err)
	}
	return cancelRead(t, after, setBy, res, err)
}

// cancelUnread says what a cancel did when the merge request could not
// be read afterwards: only GitLab's answer tells.
func cancelUnread(t target, before *gitlab.MergeRequest, setBy string, res *gitlab.ServiceResult, err error) (model.AutoMergeCancel, error) {
	switch {
	case err != nil:
		return model.AutoMergeCancel{}, err
	case res.Status == "success":
		out := autoMergeCancel(t, before, setBy, "canceled")
		out.AutoMerge = false
		out.Notes = []string{"GitLab canceled the auto-merge; reading the merge request afterwards failed, so its state is as read before."}
		return out, nil
	case res.Status == "error":
		return model.AutoMergeCancel{}, gapi.Errf(gapi.ClassConflict, "GitLab did not cancel the auto-merge: %q", res.Message)
	}
	return model.AutoMergeCancel{}, gapi.Errf(gapi.ClassUnexpected, "GitLab answered the cancel with status %q, neither success nor "+
		"error, and reading the merge request afterwards failed, so whether it canceled is unknown: read it with get_merge_request",
		res.Status)
}

// cancelRead says what a cancel did from GitLab's answer and the merge
// request read afterwards.
func cancelRead(t target, after *gitlab.MergeRequest, setBy string, res *gitlab.ServiceResult, err error) (model.AutoMergeCancel, error) {
	gone := !autoMergeSet(after)
	var e *gapi.Error
	failed := errors.As(err, &e)
	if !failed || e.Status != 401 {
		// GitLab cancels an auto-merge whose merge has already begun, and
		// answers success, but the merge goes on (§18 row 109).
		if out, done := mergingOrMerged(t, after, setBy, true); done {
			return out, nil
		}
	}
	switch {
	case err != nil && gone:
		out := autoMergeCancel(t, after, setBy, "canceled")
		if after.State != "opened" {
			out.Outcome = "unchanged"
		}
		out.Notes = []string{fmt.Sprintf("GitLab answered with an error, but a read afterwards shows no auto-merge set and the merge "+
			"request %s: this call or another canceled it, or it merged.", after.State)}
		return out, nil
	case failed && e.Status == 401:
		// GitLab answers 401 to an account that may neither merge it nor
		// wrote it; the read just made shows the token is fine.
		return model.AutoMergeCancel{}, gapi.Wrap(gapi.ClassForbidden, err, "GitLab refused to cancel the auto-merge, and it is "+
			"still set: only someone who may merge the merge request, or its author, can cancel it")
	case failed && (e.Class == gapi.ClassAmbiguousOutcome || e.Class == gapi.ClassUnavailable):
		return model.AutoMergeCancel{}, gapi.Wrap(e.Class, err, "%s. A read afterwards shows the auto-merge still set; "+
			"canceling again is safe", e.Message)
	case err != nil:
		return model.AutoMergeCancel{}, err
	case res.Status == "success":
		out := autoMergeCancel(t, after, setBy, "canceled")
		if !gone {
			out.Notes = []string{"GitLab canceled the auto-merge, but a read afterwards shows one set again: someone set it since."}
		}
		return out, nil
	case res.Status != "error":
		return model.AutoMergeCancel{}, gapi.Errf(gapi.ClassUnexpected, "GitLab answered the cancel with status %q, neither success "+
			"nor error, so what it did is unknown; a read afterwards shows auto-merge set: %t", res.Status, !gone)
	case gone:
		out := autoMergeCancel(t, after, setBy, "unchanged")
		out.Notes = []string{fmt.Sprintf("GitLab answered %q, and a read shows no auto-merge set now: it merged or was "+
			"canceled in the meantime.", res.Message)}
		return out, nil
	}
	return model.AutoMergeCancel{}, gapi.Errf(gapi.ClassConflict, "GitLab did not cancel the auto-merge, and it is still set: %q",
		res.Message)
}

// mergingOrMerged is the result for a merge request GitLab is merging or
// has merged, which a cancel does not change, by whether one was sent;
// done is false otherwise.
func mergingOrMerged(t target, mr *gitlab.MergeRequest, setBy string, sent bool) (model.AutoMergeCancel, bool) {
	switch {
	case mr.State == "locked" && sent:
		out := autoMergeCancel(t, mr, setBy, "merging")
		out.Notes = []string{"GitLab answered the cancel, but the merge request is locked: GitLab was already merging it, and a " +
			"cancel does not stop a merge in progress. Read it again with get_merge_request."}
		return out, true
	case mr.State == "locked":
		out := autoMergeCancel(t, mr, setBy, "merging")
		out.Notes = []string{"GitLab is merging it now, and a cancel does not stop a merge in progress, so nothing was sent. " +
			"Read it again with get_merge_request."}
		return out, true
	case mr.State == "merged" && sent:
		out := autoMergeCancel(t, mr, setBy, "merged")
		out.Notes = []string{"It merged: the cancel came too late."}
		return out, true
	case mr.State == "merged":
		out := autoMergeCancel(t, mr, setBy, "merged")
		out.Notes = []string{"It has merged; nothing was sent."}
		return out, true
	}
	return model.AutoMergeCancel{}, false
}

func autoMergeCancel(t target, mr *gitlab.MergeRequest, setBy, outcome string) model.AutoMergeCancel {
	return model.AutoMergeCancel{Outcome: outcome, Write: model.Write{Target: t.ref}, IID: mr.IID, WebURL: mr.WebURL, State: mr.State,
		SetBy: setBy, AutoMerge: autoMergeSet(mr), UpdatedAt: mr.UpdatedAt}
}

// ---------------------------------------------------------- suggestions

// SuggestionsApplication is apply_suggestions' request.
type SuggestionsApplication struct {
	Project       string
	IID           int64
	IDs           []int64
	CommitMessage string
}

// MaxSuggestions caps the suggestions one call applies.
const MaxSuggestions = 100

func (in SuggestionsApplication) check() error {
	switch {
	case len(in.IDs) == 0:
		return gapi.Errf(gapi.ClassInvalid, "ids is empty: pass the ids of the suggestions to apply, as list_discussions names them")
	case len(in.IDs) > MaxSuggestions:
		return gapi.Errf(gapi.ClassInvalid, "one call applies at most %d suggestions", MaxSuggestions)
	}
	for i, id := range in.IDs {
		switch {
		case id <= 0:
			return gapi.Errf(gapi.ClassInvalid, "a suggestion id is a positive number")
		case slices.Contains(in.IDs[:i], id):
			// GitLab answers 404 for an id given twice.
			return gapi.Errf(gapi.ClassInvalid, "suggestion %d is given twice", id)
		}
	}
	return nil
}

// located is a suggestion and the comment it is in.
type located struct {
	sg   gitlab.Suggestion
	note int64
	path string
}

// ApplySuggestions commits suggestions from a merge request's diff
// comments to its source branch, as the account, in one commit. GitLab
// finds a suggestion by its id alone, so each is first found on this
// merge request. The source branch is held to create_commit's rule
// (§4.4): never the default branch or a protected one. The text is
// someone else's and is committed under the account's name, so a text
// with characters a reader would not see is refused, and the person is
// asked with the text before it is sent (§4.12). GitLab answers with the
// suggestions as they were before the commit and without it, so the
// result is read back.
func (s *Service) ApplySuggestions(ctx context.Context, in SuggestionsApplication) (model.SuggestionsApply, error) {
	if err := in.check(); err != nil {
		return model.SuggestionsApply{}, err
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.SuggestionsApply{}, err
	}
	mr, err := s.client.GetMergeRequest(ctx, t.p, in.IID)
	if err != nil {
		return model.SuggestionsApply{}, err
	}
	if mr.State != "opened" {
		return model.SuggestionsApply{}, gapi.Errf(gapi.ClassConflict, "the merge request is %s, and GitLab applies suggestions only "+
			"on an open one; nothing was sent", mr.State)
	}
	var src target
	var branch *gitlab.Branch
	var found map[int64]located
	if err := parallel(
		func() (err error) { src, branch, err = s.suggestionBranch(ctx, t, mr); return err },
		func() (err error) { found, err = s.suggestionsOn(ctx, t.p, in.IID, in.IDs); return err },
	); err != nil {
		return model.SuggestionsApply{}, err
	}
	out := model.SuggestionsApply{Outcome: "applied", Write: model.Write{Target: src.ref}, IID: in.IID, SourceProject: src.project.PathWithNamespace,
		SourceBranch: mr.SourceBranch, HeadBefore: branch.Commit.ID, Suggestions: appliedSuggestions(in.IDs, found)}
	var applied []int64
	for _, id := range in.IDs {
		if found[id].sg.Applied {
			applied = append(applied, id)
		}
	}
	switch {
	case len(applied) == len(in.IDs):
		out.Outcome, out.Head = "unchanged", branch.Commit.ID
		out.Notes = []string{"Every one was already applied; nothing was sent."}
		return out, nil
	case len(applied) > 0:
		return model.SuggestionsApply{}, gapi.Errf(gapi.ClassConflict, "already applied: %s. GitLab refuses to apply a suggestion "+
			"twice; pass only the others. Nothing was sent", joinIDs(applied))
	}
	asked := make([]render.AskedSuggestion, 0, len(in.IDs))
	for _, id := range in.IDs {
		l := found[id]
		if bad := render.Invisible(l.sg.ToContent); len(bad) > 0 {
			return model.SuggestionsApply{}, gapi.Errf(gapi.ClassBlocked, "suggestion %d would commit characters a reader does not "+
				"see (%s), which can make code read otherwise than it runs; nothing was sent. Its text, with them written out, is "+
				"in list_discussions", id, runeNames(bad))
		}
		asked = append(asked, render.AskedSuggestion{ID: id, Path: l.path, FromLine: l.sg.FromLine, ToLine: l.sg.ToLine,
			To: l.sg.ToContent})
	}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun = "dry_run", true
		out.WouldSend = preview("PUT", "commit the suggestions to the source branch", names(field{"ids", len(in.IDs) > 1},
			field{"commit_message", in.CommitMessage != ""}))
		return out, nil
	}
	if err := ask(ctx, render.AskApplySuggestions(t.ref.Project.Path, in.IID, mr.SourceBranch, branch.Commit.ID, asked)); err != nil {
		return model.SuggestionsApply{}, err
	}
	if len(in.IDs) == 1 {
		_, err = s.client.ApplySuggestion(ctx, in.IDs[0], in.CommitMessage)
	} else {
		_, err = s.client.ApplySuggestions(ctx, in.IDs, in.CommitMessage)
	}
	if err != nil {
		return model.SuggestionsApply{}, s.applyFailed(ctx, t, src, mr, in, branch.Commit.ID, err)
	}
	return s.readApplied(ctx, t, src, mr, in.IDs, out)
}

// runeNames names characters as U+202E, comma-separated.
func runeNames(rs []rune) string {
	parts := make([]string, len(rs))
	for i, r := range rs {
		parts[i] = fmt.Sprintf("U+%04X", r)
	}
	return strings.Join(parts, ", ")
}

// suggestionsOn finds each id among the merge request's diff comments,
// reading threads until every id is found.
func (s *Service) suggestionsOn(ctx context.Context, p gapi.Project, iid int64, ids []int64) (map[int64]located, error) {
	found := map[int64]located{}
	collect := func(rows []gitlab.Discussion) bool {
		for _, d := range rows {
			for _, n := range d.Notes {
				for _, sg := range n.Suggestions {
					l := located{sg: sg, note: n.ID}
					if n.Position != nil {
						l.path = n.Position.NewPath
					}
					found[sg.ID] = l
				}
			}
		}
		for _, id := range ids {
			if _, ok := found[id]; !ok {
				return false
			}
		}
		return true
	}
	seen := 0
	all, complete, err := readPagesUntil(maxDiscussionPages, func(opts gapi.ListOptions) ([]gitlab.Discussion, gapi.Page, error) {
		return s.client.ListMergeRequestDiscussions(ctx, p, iid, opts)
	}, func(rows []gitlab.Discussion) bool {
		done := collect(rows[seen:])
		seen = len(rows)
		return done
	})
	if err != nil {
		return nil, err
	}
	// The last page read is not offered to done.
	collect(all[seen:])
	for _, id := range ids {
		if _, ok := found[id]; ok {
			continue
		}
		if !complete {
			return nil, gapi.Errf(gapi.ClassInvalid, "suggestion %d is not in the merge request's first %d threads, which is as "+
				"many as one call reads; nothing was sent", id, maxDiscussionPages*gapi.MaxPerPage)
		}
		return nil, gapi.Errf(gapi.ClassNotFound, "suggestion %d is not on this merge request; nothing was sent. list_discussions "+
			"names each diff comment's suggestions, and editing a comment gives its suggestions new ids", id)
	}
	return found, nil
}

// suggestionBranch reads the branch the commit would go to: the source
// branch, in the fork it comes from when it does, which is held to the
// write allow-list too. The default branch and a protected one are
// refused, as create_commit refuses them (§4.4): GitLab would commit
// to either for an account that may push there.
func (s *Service) suggestionBranch(ctx context.Context, t target, mr *gitlab.MergeRequest) (target, *gitlab.Branch, error) {
	src := t
	if mr.SourceProjectID != t.project.ID {
		var err error
		if src, err = s.writeTarget(ctx, strconv.FormatInt(mr.SourceProjectID, 10)); err != nil {
			return target{}, nil, err
		}
	}
	branch, err := s.client.GetBranch(ctx, src.p, mr.SourceBranch)
	switch {
	case gapi.IsClass(err, gapi.ClassNotFound):
		return target{}, nil, gapi.Errf(gapi.ClassConflict, "the source branch is gone, and GitLab applies suggestions only to "+
			"it; nothing was sent")
	case err != nil:
		return target{}, nil, err
	}
	if err := s.guardBranch(ctx, src, mr.SourceBranch, branch); err != nil {
		return target{}, nil, err
	}
	return src, branch, nil
}

// fileChanged is GitLab refusing a suggestion whose comment sits at an
// older head than the source branch's: so is every one right after a
// push, until GitLab has moved the comments to the new head.
var fileChanged = regexp.MustCompile(`A file has been changed\.`)

// applyFailed says what a failed apply did, by the class the client
// gave the failure. A lost answer is settled by reading the suggestions
// and the branch, never by applying again (§4.5).
func (s *Service) applyFailed(ctx context.Context, t, src target, mr *gitlab.MergeRequest, in SuggestionsApplication, before string,
	err error) error {
	var e *gapi.Error
	if !errors.As(err, &e) {
		return err
	}
	switch {
	case e.Class == gapi.ClassAmbiguousOutcome:
		return s.settleApply(ctx, t, src, mr, in, before, err)
	case e.Class == gapi.ClassConflict && fileChanged.MatchString(e.Message):
		return gapi.Wrap(gapi.ClassConflict, err, "%s. GitLab refuses a suggestion whose comment was made on an older head "+
			"than the branch's. Right after a push it refuses every one until it has moved the comments to the new head, so "+
			"try again shortly; if it still refuses, read list_discussions again", e.Message)
	case e.Class == gapi.ClassForbidden:
		return gapi.Wrap(gapi.ClassForbidden, err, "%s. Nothing was committed: GitLab applies a suggestion only for an account "+
			"that may push to the source branch, which a protected branch or a fork that does not allow commits from the target "+
			"project's members prevents", e.Message)
	case e.Class == gapi.ClassNotFound:
		return gapi.Wrap(gapi.ClassNotFound, err, "%s. Nothing was committed: an edit to a comment gives its suggestions new "+
			"ids, so read list_discussions again", e.Message)
	}
	return err
}

// settleApply settles a lost apply. GitLab marks the suggestions applied
// in the request that commits them, so all applied means they landed,
// by this call or another. Anything else read right after the failure
// is no proof: GitLab may still be committing.
func (s *Service) settleApply(ctx context.Context, t, src target, mr *gitlab.MergeRequest, in SuggestionsApplication, before string,
	err error) error {
	found, readErr := s.suggestionsOn(ctx, t.p, mr.IID, in.IDs)
	var head *gitlab.Branch
	if readErr == nil {
		head, readErr = s.client.GetBranch(ctx, src.p, mr.SourceBranch)
	}
	if readErr != nil {
		return gapi.Wrap(gapi.ClassAmbiguousOutcome, err, "GitLab did not confirm the apply, and reading to find out failed too, so "+
			"it is unknown: %s", settledUnknown)
	}
	n := 0
	for _, id := range in.IDs {
		if found[id].sg.Applied {
			n++
		}
	}
	if n < len(in.IDs) {
		return gapi.Wrap(gapi.ClassAmbiguousOutcome, err, "GitLab did not confirm the apply, and a read right after shows %d of %d "+
			"applied with the branch's head at %s; GitLab may still be committing, so it is unknown: read list_discussions in a "+
			"moment, and apply none it shows applied", n, len(in.IDs), head.Commit.ID)
	}
	if s.madeByThisCall(ctx, head.Commit, before, in.CommitMessage) {
		return gapi.Wrap(gapi.ClassAmbiguousOutcome, err, "GitLab did not confirm the apply, but a read shows every suggestion "+
			"applied, and the source branch's head %s is one commit on from where it was, by this account. %s", head.Commit.ID,
			settledLanded)
	}
	return gapi.Wrap(gapi.ClassAmbiguousOutcome, err, "GitLab did not confirm the apply, but a read shows every suggestion applied, "+
		"by this call or another: a reviewer may have applied them in GitLab meanwhile. The source branch's head is %s. %s",
		head.Commit.ID, settledLanded)
}

// madeByThisCall reports whether head is the commit an apply makes: one
// commit on from before, by the account, with the message asked for when
// it has no placeholder GitLab fills.
func (s *Service) madeByThisCall(ctx context.Context, head gitlab.Commit, before, message string) bool {
	if len(head.ParentIDs) != 1 || head.ParentIDs[0] != before {
		return false
	}
	if message != "" && !strings.Contains(message, "%{") && !sameText(head.Message, message) {
		return false
	}
	me, err := s.me(ctx)
	return err == nil && head.AuthorName == me.Name
}

// readApplied reads back what an apply did: the suggestions' applied
// state and the branch's new head, which GitLab's answer gives neither
// of.
func (s *Service) readApplied(ctx context.Context, t, src target, mr *gitlab.MergeRequest, ids []int64,
	out model.SuggestionsApply) (model.SuggestionsApply, error) {
	var found map[int64]located
	var head *gitlab.Branch
	if err := parallel(
		func() (err error) { found, err = s.suggestionsOn(ctx, t.p, mr.IID, ids); return err },
		func() (err error) { head, err = s.client.GetBranch(ctx, src.p, mr.SourceBranch); return err },
	); err != nil {
		out.Notes = append(out.Notes, "GitLab applied the suggestions; reading them and the branch afterwards failed, so the new "+
			"head is not reported. Do not apply them again.")
		return out, nil //nolint:nilerr // the commit exists; a failed read afterwards is said, not a failed call
	}
	out.Head, out.Suggestions = head.Commit.ID, appliedSuggestions(ids, found)
	for _, sg := range out.Suggestions {
		if !sg.Applied {
			out.Notes = append(out.Notes, fmt.Sprintf("GitLab answered success, but a read afterwards does not show suggestion %d "+
				"applied.", sg.ID))
		}
	}
	if head.Commit.ID == out.HeadBefore {
		out.Notes = append(out.Notes, "GitLab answered success, but the source branch's head has not moved.")
	}
	return out, nil
}

func appliedSuggestions(ids []int64, found map[int64]located) []model.AppliedSuggestion {
	out := make([]model.AppliedSuggestion, 0, len(ids))
	for _, id := range ids {
		l := found[id]
		out = append(out, model.AppliedSuggestion{ID: id, NoteID: l.note, FilePath: l.path, FromLine: l.sg.FromLine,
			ToLine: l.sg.ToLine, Applied: l.sg.Applied, SuggestionText: suggestionText(l.sg)})
	}
	return out
}

func joinIDs(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return strings.Join(parts, ", ")
}
