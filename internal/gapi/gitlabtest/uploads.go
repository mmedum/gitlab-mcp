package gitlabtest

import (
	"fmt"
	"io"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
)

// Markdown uploads, as lib/api/markdown_uploads.rb serves POST
// /projects/:id/uploads at v19.4.1-ee: any signed-in account that can
// read the project may upload, with no role checked; the file comes in
// the multipart field "file"; GitLab stores it under a random secret
// with CarrierWave's sanitized base name, and answers with Markdown that
// embeds it when its extension names an image, a video or an audio file
// (Gitlab::FileTypeDetection, Gitlab::FileMarkdownLinkBuilder).

// Upload is a file an account uploaded to a project.
type Upload struct {
	ID      int64
	Project string // the project's full path
	User    string
	// Filename is the name GitLab stored, after sanitizing it.
	Filename string
	// ContentType is what the file part said it was.
	ContentType string
	Secret      string
	Data        []byte
}

// firstUploadID is the first upload's id.
const firstUploadID = 80001

// embeddableExtensions are the extensions GitLab's Markdown embeds:
// SAFE_IMAGE_EXT, SAFE_VIDEO_EXT and SAFE_AUDIO_EXT.
var embeddableExtensions = []string{"png", "jpg", "jpeg", "gif", "bmp", "tiff", "ico", "webp",
	"mp4", "m4v", "mov", "webm", "ogv", "mp3", "oga", "ogg", "spx", "wav"}

func (s *Server) createUpload(w http.ResponseWriter, r *http.Request, p *project, user string) {
	f, hdr, err := r.FormFile("file")
	if err != nil {
		// Grape's answer for a parameter that is absent, or present and
		// not a file.
		msg := "file is missing"
		if r.MultipartForm != nil && len(r.MultipartForm.Value["file"]) > 0 {
			msg = "file is invalid"
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": msg})
		return
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "file is invalid"})
		return
	}
	id := firstUploadID + int64(len(s.uploads))
	up := Upload{ID: id, Project: p.PathWithNamespace, User: user, Filename: sanitizedName(hdr.Filename),
		ContentType: hdr.Header.Get("Content-Type"), Secret: fmt.Sprintf("%032d", id), Data: data}
	s.uploads = append(s.uploads, up)
	url := "/uploads/" + up.Secret + "/" + up.Filename
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(up.Filename), "."))
	embeddable := slices.Contains(embeddableExtensions, ext)
	alt := up.Filename
	if embeddable {
		alt = strings.TrimSuffix(up.Filename, path.Ext(up.Filename))
	}
	markdown := "[" + strings.ReplaceAll(alt, "]", `\]`) + "](" + url + ")"
	if embeddable || ext == "svg" {
		markdown = "!" + markdown
	}
	writeJSON(w, http.StatusCreated, gitlab.ProjectUpload{ID: id, Alt: alt, URL: url, Markdown: markdown,
		FullPath: fmt.Sprintf("/-/project/%d/uploads/%s/%s", p.ID, up.Secret, up.Filename)})
}

// sanitizedName is CarrierWave's: the base name, with every character
// but word characters, '.', '-' and '+' made '_'. Ruby's word characters
// include letters beyond ASCII, which no test sends.
func sanitizedName(name string) string {
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-', r == '+':
			return r
		}
		return '_'
	}, name)
}

// Uploads returns the uploads made to a project, oldest first.
func (s *Server) Uploads(projectPath string) []Upload {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil {
		return nil
	}
	var out []Upload
	for _, u := range s.uploads {
		if u.Project == p.PathWithNamespace {
			out = append(out, u)
		}
	}
	return out
}

// SetMediaAuth sets a project's "Require authentication to view media
// files"; nil leaves it out of the project's answer. It reports whether
// the project exists.
func (s *Server) SetMediaAuth(projectPath string, on *bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.projectByPath(projectPath)
	if p == nil {
		return false
	}
	p.EnforceAuthChecksOnUploads = on
	return true
}
