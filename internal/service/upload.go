package service

import (
	"context"
	"slices"

	"github.com/mmedum/gitlab-mcp/v2/internal/gapi"
	"github.com/mmedum/gitlab-mcp/v2/internal/localimage"
	"github.com/mmedum/gitlab-mcp/v2/internal/model"
)

// Uploading an image for Markdown to embed (§7.10). The image is a local
// file, read by internal/localimage from a directory the person allowed:
// one of the client's roots, or one GITLAB_MCP_UPLOAD_DIRS names. GitLab
// keeps an upload whether or not anything links to it, so it is sent
// once (§4.5), and listing uploads needs the Maintainer role and gives no
// link, so a lost answer stays unknown.

// Roots lists the directories the client shares as its roots. The tools
// layer installs one per call; a call without one has none.
type Roots interface {
	Roots(ctx context.Context) []string
}

type rootsKey struct{}

// WithRoots returns a context whose client roots are r's.
func WithRoots(ctx context.Context, r Roots) context.Context {
	return context.WithValue(ctx, rootsKey{}, r)
}

// uploadDirs is every directory an upload may read from: the setting's
// and the client's roots, sorted, each once.
func (s *Service) uploadDirs(ctx context.Context) []string {
	dirs := slices.Clone(s.cfg.UploadDirs)
	if r, ok := ctx.Value(rootsKey{}).(Roots); ok {
		dirs = append(dirs, r.Roots(ctx)...)
	}
	slices.Sort(dirs)
	return slices.Compact(dirs)
}

// Upload is upload_file's request.
type Upload struct {
	Project string
	// Path is the image's absolute path on this machine.
	Path string
}

// UploadFile uploads a local image to a project and returns the Markdown
// that embeds it. The image is read and checked before anything is
// sent, a dry run included.
func (s *Service) UploadFile(ctx context.Context, in Upload) (model.UploadWrite, error) {
	img, err := localimage.Read(in.Path, s.uploadDirs(ctx))
	if err != nil {
		return model.UploadWrite{}, err
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.UploadWrite{}, err
	}
	requires := t.project.EnforceAuthChecksOnUploads
	reach, note := linkReach(t.ref.Visibility, requires)
	out := model.UploadWrite{Outcome: "uploaded", Write: model.Write{Target: t.ref, Notes: []string{note}}, Filename: img.Name,
		ContentType: img.Type, Size: len(img.Data), MediaRequiresSignIn: requires, LinkOpensFor: reach}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun = "dry_run", true
		out.WouldSend = preview("POST", "upload the image to the project", []string{"file"})
		return out, nil
	}
	up, err := s.client.UploadFile(ctx, t.p, img.Name, img.Type, img.Data)
	if gapi.IsClass(err, gapi.ClassAmbiguousOutcome) {
		return model.UploadWrite{}, gapi.Wrap(gapi.ClassAmbiguousOutcome, err, "GitLab did not confirm whether the image was "+
			"uploaded, and no read can tell: listing a project's uploads needs the Maintainer role and gives no link. It was not "+
			"sent again, so the outcome is unknown. Ask the person before calling again: a repeat uploads the image a second "+
			"time, and if this attempt landed, that upload stays in the project with nothing linking to it")
	}
	if err != nil {
		return model.UploadWrite{}, err
	}
	out.Markdown, out.URL, out.FullPath, out.Alt, out.UploadID = up.Markdown, up.URL, up.FullPath, up.Alt, up.ID
	return out, nil
}

// Who can open an uploaded image by its link (§18 row 112): GitLab
// serves it to anyone in a public project, and in a private or internal
// one too unless the project requires sign-in to view media files
// (UploadsActions#bypass_auth_checks_on_uploads?, which decides an image
// by its extension).
const (
	reachAnyone  = "anyone_with_link"
	reachReaders = "project_readers"
	reachUnknown = "unknown"
)

// linkReach says who can open an uploaded image by its link, from the
// project's visibility and its media setting, and says it in a sentence.
func linkReach(visibility string, requiresSignIn *bool) (string, string) {
	switch {
	case visibility == "public":
		return reachAnyone, "In this public project anyone with an image's link can open it, signed in or not."
	case requiresSignIn == nil:
		return reachUnknown, "GitLab did not say whether this project requires sign-in to view media files; unless it does, " +
			"anyone with an image's link can open it, signed in or not."
	case !*requiresSignIn:
		return reachAnyone, "In this " + visibility + " project anyone with an image's link can open it, signed in or not: the " +
			"project does not require sign-in to view media files, so the link's random part is all that keeps it private."
	}
	return reachReaders, "Only people signed in who can see this " + visibility + " project can open an image by its link: " +
		"the project requires sign-in to view media files."
}
