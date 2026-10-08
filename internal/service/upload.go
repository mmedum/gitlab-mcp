package service

import (
	"context"

	"github.com/mmedum/gitlab-mcp/v2/internal/gapi"
	"github.com/mmedum/gitlab-mcp/v2/internal/localimage"
	"github.com/mmedum/gitlab-mcp/v2/internal/model"
	"github.com/mmedum/gitlab-mcp/v2/internal/render"
)

// Uploading an image for Markdown to embed (§7.10). The image is a local
// file, read by internal/localimage from a directory the person named in
// GITLAB_MCP_UPLOAD_DIRS. GitLab keeps an upload whether or not anything
// links to it, so it is sent once (§4.5), and listing uploads needs the
// Maintainer role and gives no link, so a lost answer stays unknown.

// Upload is upload_file's request.
type Upload struct {
	Project string
	// Path is the image's absolute path on this machine.
	Path string
}

// UploadFile uploads a local image to a project and returns the Markdown
// that embeds it. The project is read and held to the write allow-list
// first, so a call aimed elsewhere reads nothing on this machine; then
// the image is read and checked, a dry run included; then the person is
// asked, since an upload discloses the image (§4.12).
func (s *Service) UploadFile(ctx context.Context, in Upload) (model.UploadWrite, error) {
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.UploadWrite{}, err
	}
	img, err := localimage.Read(in.Path, s.cfg.UploadDirs)
	if err != nil {
		return model.UploadWrite{}, err
	}
	requires := t.project.EnforceAuthChecksOnUploads
	reach, note := linkReach(t.ref.Visibility, requires)
	notes := []string{note, maintainersNote, metadataNote(img.Type)}
	out := model.UploadWrite{Outcome: "uploaded", Write: model.Write{Target: t.ref, Notes: notes}, Filename: img.Name,
		ContentType: img.Type, Size: len(img.Data), MediaRequiresSignIn: requires, LinkOpensFor: reach}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun = "dry_run", true
		out.WouldSend = preview("POST", "upload the image to the project", []string{"file"})
		return out, nil
	}
	if err := ask(ctx, render.AskUploadFile(t.ref.Project.Path, img.Name, img.Type, len(img.Data), notes, img.Data)); err != nil {
		return model.UploadWrite{}, err
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

// maintainersNote is who reaches an upload without its link: listing
// and downloading uploads by id needs the Maintainer role
// (lib/api/markdown_uploads.rb at v19.4.1-ee, §18 row 116).
const maintainersNote = "The project's Maintainers can list every upload and download it by its id, without the link."

// metadataNote says what happens to the image's metadata: GitLab's
// Workhorse removes a JPEG's when it stores it, keeping its size,
// resolution and orientation, and leaves the other types' alone (§18 row
// 118).
func metadataNote(contentType string) string {
	if contentType == "image/jpeg" {
		return "GitLab removes a JPEG's metadata, such as EXIF location and camera details, when it stores it, keeping only " +
			"its size, resolution and orientation."
	}
	return "The image's metadata, such as EXIF, XMP or PNG text, goes as it is in the file: GitLab removes metadata only " +
		"from a JPEG."
}

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
