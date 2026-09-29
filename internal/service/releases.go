package service

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/internal/gapi"
	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/v2/internal/model"
	"github.com/mmedum/gitlab-mcp/v2/internal/render"
)

// The releases, deployments and activity toolsets (§7.8). A release is
// Ship: it creates its tag when the tag does not exist, and a new tag
// starts the project's tag pipelines. Environments, deployments and
// events are reads.

// ListReleases lists a project's releases.
func (s *Service) ListReleases(ctx context.Context, raw string, q gapi.ReleaseQuery, opts gapi.ListOptions) (model.Releases, error) {
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.Releases{}, err
	}
	rows, page, err := s.client.ListReleases(ctx, p, q, opts)
	if err != nil {
		return model.Releases{}, err
	}
	out := model.Releases{Project: ref, Releases: make([]model.ReleaseRow, 0, len(rows)), Listing: listing(len(rows), page)}
	for _, r := range rows {
		out.Releases = append(out.Releases, releaseRow(&r))
	}
	return out, nil
}

// releaseRow is what a listing and a single read of a release share.
func releaseRow(r *gitlab.Release) model.ReleaseRow {
	row := model.ReleaseRow{TagName: r.TagName, CreatedAt: r.CreatedAt, ReleasedAt: r.ReleasedAt, Upcoming: r.UpcomingRelease}
	row.UntrustedName, _ = render.Line(r.Name, render.NoteBudget)
	if r.Author != nil {
		row.Author = r.Author.Username
	}
	if r.Commit != nil {
		row.CommitSHA = r.Commit.ID
	}
	return row
}

// GetRelease reads the release of a tag, its notes under the
// description budget.
func (s *Service) GetRelease(ctx context.Context, raw, tag string, offset int) (model.Release, error) {
	if strings.TrimSpace(tag) == "" {
		return model.Release{}, gapi.Errf(gapi.ClassInvalid, "tag_name is empty; list_releases lists them")
	}
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.Release{}, err
	}
	r, err := s.client.GetRelease(ctx, p, tag)
	if err != nil {
		return model.Release{}, err
	}
	row := releaseRow(r)
	out := model.Release{Project: ref, TagName: row.TagName, UntrustedName: row.UntrustedName, Author: row.Author, CommitSHA: row.CommitSHA,
		CreatedAt: row.CreatedAt, ReleasedAt: row.ReleasedAt, Upcoming: row.Upcoming, Milestones: releaseMilestones(r)}
	if r.Assets != nil {
		out.Assets = r.Assets.Count
	}
	description := ""
	if r.Description != nil {
		description = *r.Description
	}
	text, removed := render.Markdown(description, s.self())
	if out.UntrustedDescription, out.Budget, err = cut(text, removed, offset, render.DescriptionBudget, "the release notes", "offset"); err != nil {
		return model.Release{}, err
	}
	return out, nil
}

func releaseMilestones(r *gitlab.Release) []string {
	out := make([]string, 0, len(r.Milestones))
	for _, m := range r.Milestones {
		out = append(out, m.Title)
	}
	return out
}

// ReleaseCreate is create_release's request.
type ReleaseCreate struct {
	Project     string
	TagName     string
	Ref         string
	TagMessage  string
	Name        string
	Description string
	Milestones  []string
	ReleasedAt  *time.Time
	Links       []gapi.ReleaseLink
}

// maxReleaseLinks bounds the asset links of one release.
const maxReleaseLinks = 20

// releaseLinkTypes are the kinds of asset link GitLab knows.
var releaseLinkTypes = []string{"other", "runbook", "image", "package"}

// checkLinks refuses an asset link that points outside the release's own
// project: a link is published on the release page for everyone who
// reads it, so one a model was steered to write must lead only to what
// the project itself serves (§4.1). The URL must be the instance's
// origin, https on gitlab.com, with no credentials in it, under the
// project's web path or its API path, where its packages are.
func (s *Service) checkLinks(project *gitlab.Project, links []gapi.ReleaseLink) ([]gapi.ReleaseLink, error) {
	web := strings.TrimSuffix(s.inst.WebBase().EscapedPath(), "/") + "/" + project.PathWithNamespace + "/"
	api := strings.TrimSuffix(s.inst.APIRoot().EscapedPath(), "/") + "/projects/"
	roots := []string{web, api + strconv.FormatInt(project.ID, 10) + "/", api + url.PathEscape(project.PathWithNamespace) + "/"}
	if len(links) > maxReleaseLinks {
		return nil, gapi.Errf(gapi.ClassInvalid, "a release takes at most %d asset links", maxReleaseLinks)
	}
	out := make([]gapi.ReleaseLink, 0, len(links))
	for i, l := range links {
		name := strings.TrimSpace(l.Name)
		if name == "" {
			return nil, gapi.Errf(gapi.ClassInvalid, "asset link %d has no name", i+1)
		}
		u, err := url.Parse(l.URL)
		inProject := err == nil && slices.ContainsFunc(roots, func(r string) bool {
			return strings.HasPrefix(u.EscapedPath(), r) && !slices.Contains(strings.Split(u.Path, "/"), "..")
		})
		if err != nil || !u.IsAbs() || u.User != nil || u.Host == "" || !s.inst.SameOrigin(u) || !inProject {
			return nil, gapi.Errf(gapi.ClassBlocked, "asset link %q must be a URL in this project, under %s/%s or its API "+
				"path, with no credentials in it: a release page sends its readers wherever its links point", name,
				strings.TrimSuffix(s.inst.WebBase().String(), "/"), project.PathWithNamespace)
		}
		if l.LinkType != "" && !slices.Contains(releaseLinkTypes, l.LinkType) {
			return nil, gapi.Errf(gapi.ClassInvalid, "asset link %q has link_type %q; it is one of %s", name, l.LinkType,
				strings.Join(releaseLinkTypes, ", "))
		}
		if p := l.DirectAssetPath; p != "" && (!strings.HasPrefix(p, "/") || slices.Contains(strings.Split(p, "/"), "..")) {
			return nil, gapi.Errf(gapi.ClassInvalid, "asset link %q has direct_asset_path %q; it starts with / and has no .. segment", name, p)
		}
		out = append(out, gapi.ReleaseLink{Name: name, URL: u.String(), LinkType: l.LinkType, DirectAssetPath: l.DirectAssetPath})
	}
	return out, nil
}

// CreateRelease creates a release, and its tag at ref when the tag does
// not exist yet.
func (s *Service) CreateRelease(ctx context.Context, in ReleaseCreate) (model.ReleaseWrite, error) {
	if strings.TrimSpace(in.TagName) == "" {
		return model.ReleaseWrite{}, gapi.Errf(gapi.ClassInvalid, "tag_name is empty")
	}
	body := gapi.ReleaseCreate{TagName: in.TagName, Ref: in.Ref, TagMessage: in.TagMessage, Name: in.Name, Description: in.Description,
		Milestones: in.Milestones, ReleasedAt: in.ReleasedAt}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.ReleaseWrite{}, err
	}
	links, err := s.checkLinks(t.project, in.Links)
	if err != nil {
		return model.ReleaseWrite{}, err
	}
	if len(links) > 0 {
		body.Assets = &gapi.ReleaseLinks{Links: links}
	}
	exists, err := s.tagExists(ctx, t.p, in.TagName)
	if err != nil {
		return model.ReleaseWrite{}, err
	}
	switch {
	case !exists && in.Ref == "":
		return model.ReleaseWrite{}, gapi.Errf(gapi.ClassInvalid, "the tag %q does not exist: pass ref, the branch, tag or commit to create it at", in.TagName)
	case exists && in.Ref != "":
		return model.ReleaseWrite{}, gapi.Errf(gapi.ClassInvalid, "the tag %q exists, and ref only says where a new one goes: leave ref out", in.TagName)
	case exists && in.TagMessage != "":
		return model.ReleaseWrite{}, gapi.Errf(gapi.ClassInvalid, "the tag %q exists, and tag_message only annotates a new one: leave it out", in.TagName)
	}
	milestones := nonNil(in.Milestones)
	if gapi.IsDryRun(ctx) {
		return model.ReleaseWrite{Outcome: "dry_run", TagName: in.TagName, TagCreated: !exists, Milestones: milestones,
			Links: linkRows(links), Write: model.Write{
				DryRun: true, Target: t.ref, WouldSend: preview("POST", "create a release", fieldsOf(body))}}, nil
	}
	if err := ask(ctx, render.AskCreateRelease(t.ref.Project.Path, in.TagName, in.Name, in.Ref, exists, len(links))); err != nil {
		return model.ReleaseWrite{}, err
	}
	r, err := s.client.CreateRelease(ctx, t.p, body)
	if err != nil {
		return model.ReleaseWrite{}, settle(err, "release", func() (string, error) {
			got, err := s.client.GetRelease(ctx, t.p, in.TagName)
			if gapi.IsClass(err, gapi.ClassNotFound) {
				return "", nil
			}
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("the release of tag %s, created %s", got.TagName, got.CreatedAt.UTC().Format(time.RFC3339)), nil
		})
	}
	out := model.ReleaseWrite{Outcome: "created", Write: model.Write{Target: t.ref}, TagName: r.TagName, TagCreated: !exists,
		ReleasedAt: r.ReleasedAt, Milestones: releaseMilestones(r), Links: []model.LinkRow{}}
	if r.Assets != nil {
		for _, l := range r.Assets.Links {
			out.Links = append(out.Links, model.LinkRow{Name: l.Name, URL: l.URL, LinkType: l.LinkType})
		}
	}
	if r.Commit != nil {
		out.CommitSHA = r.Commit.ID
	}
	return out, nil
}

// linkRows are the links a dry run would send.
func linkRows(links []gapi.ReleaseLink) []model.LinkRow {
	out := make([]model.LinkRow, 0, len(links))
	for _, l := range links {
		out = append(out, model.LinkRow{Name: l.Name, URL: l.URL, LinkType: l.LinkType})
	}
	return out
}

// tagExists looks a tag up by its name.
func (s *Service) tagExists(ctx context.Context, p gapi.Project, name string) (bool, error) {
	_, err := s.client.GetTag(ctx, p, name)
	if gapi.IsClass(err, gapi.ClassNotFound) {
		return false, nil
	}
	return err == nil, err
}

// ----------------------------------------------------------- deployments

// ListEnvironments lists a project's environments.
func (s *Service) ListEnvironments(ctx context.Context, raw string, q gapi.EnvironmentQuery, opts gapi.ListOptions) (model.Environments, error) {
	if q.Name != "" && q.Search != "" {
		return model.Environments{}, gapi.Errf(gapi.ClassInvalid, "pass name or search, not both")
	}
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.Environments{}, err
	}
	rows, page, err := s.client.ListEnvironments(ctx, p, q, opts)
	if err != nil {
		return model.Environments{}, err
	}
	out := model.Environments{Project: ref, Environments: make([]model.EnvironmentRow, 0, len(rows)), Listing: listing(len(rows), page)}
	for _, e := range rows {
		row := model.EnvironmentRow{ID: e.ID, Name: e.Name, Slug: e.Slug, State: e.State, Tier: e.Tier, UpdatedAt: e.UpdatedAt,
			AutoStopAt: e.AutoStopAt}
		if e.ExternalURL != nil {
			row.ExternalURL = *e.ExternalURL
		}
		if d := e.LastDeployment; d != nil {
			row.LastDeployment = &model.DeploymentRef{ID: d.ID, IID: d.IID, Status: d.Status, Ref: d.Ref, SHA: d.SHA, CreatedAt: d.CreatedAt}
		}
		out.Environments = append(out.Environments, row)
	}
	return out, nil
}

// ListDeployments lists a project's deployments.
func (s *Service) ListDeployments(ctx context.Context, raw string, q gapi.DeploymentQuery, opts gapi.ListOptions) (model.Deployments, error) {
	if !q.UpdatedAfter.IsZero() || !q.UpdatedBefore.IsZero() {
		// GitLab filters by updated_at only when it sorts by it too
		// (phase 3 live run).
		switch q.OrderBy {
		case "":
			q.OrderBy = "updated_at"
		case "updated_at":
		default:
			return model.Deployments{}, gapi.Errf(gapi.ClassInvalid, "updated_after and updated_before need order_by updated_at, "+
				"which is the default with them")
		}
	}
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.Deployments{}, err
	}
	rows, page, err := s.client.ListDeployments(ctx, p, q, opts)
	if err != nil {
		return model.Deployments{}, err
	}
	out := model.Deployments{Project: ref, Deployments: make([]model.DeploymentRow, 0, len(rows)), Listing: listing(len(rows), page)}
	for _, d := range rows {
		row := model.DeploymentRow{ID: d.ID, IID: d.IID, Status: d.Status, Environment: d.Environment.Name, Ref: d.Ref, SHA: d.SHA,
			CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt}
		if d.User != nil {
			row.User = d.User.Username
		}
		if d.Deployable != nil {
			row.JobID, row.JobName = d.Deployable.ID, d.Deployable.Name
		}
		out.Deployments = append(out.Deployments, row)
	}
	return out, nil
}

// -------------------------------------------------------------- activity

// ListEvents lists a project's activity, or the signed-in account's own
// when project is empty.
func (s *Service) ListEvents(ctx context.Context, raw string, q gapi.EventQuery, opts gapi.ListOptions) (model.Events, error) {
	if err := checkDate("before", q.Before); err != nil {
		return model.Events{}, err
	}
	if err := checkDate("after", q.After); err != nil {
		return model.Events{}, err
	}
	c, err := s.api()
	if err != nil {
		return model.Events{}, err
	}
	out := model.Events{}
	var rows []gitlab.Event
	var page gapi.Page
	if raw == "" {
		rows, page, err = c.ListEvents(ctx, q, opts)
	} else {
		p, ref, perr := s.project(ctx, raw)
		if perr != nil {
			return model.Events{}, perr
		}
		out.Project = &ref
		rows, page, err = c.ListProjectEvents(ctx, p, q, opts)
	}
	if err != nil {
		return model.Events{}, err
	}
	out.Events, out.Listing = make([]model.EventRow, 0, len(rows)), listing(len(rows), page)
	for _, e := range rows {
		row := model.EventRow{ID: e.ID, Action: e.ActionName, TargetID: e.TargetID, TargetIID: e.TargetIID, Author: e.AuthorUsername,
			ProjectID: e.ProjectID, CreatedAt: e.CreatedAt}
		if e.TargetType != nil {
			row.TargetType = *e.TargetType
		}
		if e.TargetTitle != nil {
			row.UntrustedTargetTitle, _ = render.Line(*e.TargetTitle, render.NoteBudget)
		}
		if pd := e.PushData; pd != nil {
			row.PushCommits = pd.CommitCount
			if pd.Ref != nil {
				row.PushRef = *pd.Ref
			}
			if pd.CommitTitle != nil {
				row.UntrustedCommitTitle, _ = render.Line(*pd.CommitTitle, render.NoteBudget)
			}
		}
		out.Events = append(out.Events, row)
	}
	return out, nil
}
