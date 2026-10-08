package tools

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mmedum/gitlab-mcp/v2/internal/config"
	"github.com/mmedum/gitlab-mcp/v2/internal/gapi/gitlabtest"
)

// upload_file against the in-memory instance: what it sends, who the
// result says can open the image, where it may read from, and that a
// lost answer is not sent again (§7.10). Which files it refuses is
// internal/localimage's test; here one refusal shows the check runs
// before anything is sent.

// firstSecret is the secret the in-memory instance gives its first
// upload.
const firstSecret = "00000000000000000000000000080001"

// pngFile writes a generated PNG and returns its path and bytes.
func pngFile(t *testing.T, dir, name string) (string, []byte) {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, b.Bytes()
}

// uploadPosts counts the uploads sent.
func uploadPosts(h *harness) int {
	n := 0
	for _, r := range h.gl.Requests() {
		if r.Method == http.MethodPost && strings.HasSuffix(r.EscapedPath, "/uploads") {
			n++
		}
	}
	return n
}

func boolp(b bool) *bool { return &b }

func TestUploadFile(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, harnessOptions{cfg: config.Config{UploadDirs: []string{dir}}})
	path, data := pngFile(t, dir, "mock-up.png")
	text, out := h.ok("upload_file", map[string]any{"project": alpha, "path": path})
	want := map[string]any{
		"outcome": "uploaded", "filename": "mock-up.png", "content_type": "image/png", "size": float64(len(data)),
		"markdown":  "![mock-up](/uploads/" + firstSecret + "/mock-up.png)",
		"url":       "/uploads/" + firstSecret + "/mock-up.png",
		"full_path": "/-/project/2001/uploads/" + firstSecret + "/mock-up.png",
		"alt":       "mock-up", "upload_id": float64(80001), "media_requires_sign_in": true, "link_opens_for": "anyone_with_link",
		"dry_run": false, "would_send": nil,
	}
	for k, v := range want {
		if got := get(out, k); got != v {
			t.Errorf("%s = %v, want %v", k, got, v)
		}
	}
	if get(out, "target", "visibility") != "public" || get(out, "target", "project", "path") != alpha {
		t.Errorf("target = %v", get(out, "target"))
	}
	ups := h.gl.Uploads(alpha)
	if len(ups) != 1 || !bytes.Equal(ups[0].Data, data) || ups[0].Filename != "mock-up.png" || ups[0].ContentType != "image/png" ||
		ups[0].User != "alice" || uploadPosts(h) != 1 {
		t.Errorf("GitLab received %d upload(s) in %d request(s): %+v", len(ups), uploadPosts(h), ups)
	}
	for _, line := range []string{
		"Uploaded mock-up.png.",
		"Image: mock-up.png, image/png, ",
		"Markdown, for this project's issues, merge requests and comments: `![mock-up](/uploads/" + firstSecret + "/mock-up.png)`",
		"From another project, link it as /-/project/2001/uploads/" + firstSecret + "/mock-up.png on gitlab.com.",
		"Note: In this public project anyone with an image's link can open it, signed in or not.",
	} {
		if !strings.Contains(text, line) {
			t.Errorf("the text lacks %q:\n%s", line, text)
		}
	}
}

// An image is sent under the extension its bytes show, which is what
// makes GitLab's Markdown embed it rather than link it.
func TestAnUploadIsNamedForItsBytes(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, harnessOptions{cfg: config.Config{UploadDirs: []string{dir}}})
	path, _ := pngFile(t, dir, "diagram.txt")
	_, out := h.ok("upload_file", map[string]any{"project": alpha, "path": path})
	if get(out, "filename") != "diagram.png" || get(out, "markdown") != "![diagram](/uploads/"+firstSecret+"/diagram.png)" {
		t.Errorf("result = %v", out)
	}
}

// Who can open an uploaded image by its link follows the project's
// visibility and its "Require authentication to view media files".
func TestWhoCanOpenAnUpload(t *testing.T) {
	cases := []struct {
		name     string
		project  string
		setting  *bool
		reach    string
		sentence string
	}{
		{"public", alpha, boolp(true), "anyone_with_link", "In this public project anyone with an image's link can open it"},
		{"private, sign-in required", gitlabtest.ProjectBeta, boolp(true), "project_readers",
			"Only people signed in who can see this private project can open an image by its link"},
		{"private, sign-in not required", gitlabtest.ProjectBeta, boolp(false), "anyone_with_link",
			"In this private project anyone with an image's link can open it, signed in or not"},
		{"private, not said", gitlabtest.ProjectBeta, nil, "unknown",
			"unless it does, anyone with an image's link can open it"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			h := newHarness(t, harnessOptions{cfg: config.Config{UploadDirs: []string{dir}}})
			h.gl.SetMediaAuth(c.project, c.setting)
			path, _ := pngFile(t, dir, "shot.png")
			text, out := h.ok("upload_file", map[string]any{"project": c.project, "path": path})
			var setting any
			if c.setting != nil {
				setting = *c.setting
			}
			if get(out, "link_opens_for") != c.reach || get(out, "media_requires_sign_in") != setting {
				t.Errorf("link_opens_for %v, media_requires_sign_in %v; want %s, %v", get(out, "link_opens_for"),
					get(out, "media_requires_sign_in"), c.reach, setting)
			}
			if !strings.Contains(text, c.sentence) {
				t.Errorf("the text lacks %q:\n%s", c.sentence, text)
			}
		})
	}
}

// A dry run reads and checks the image and the project, and sends
// nothing; a file that is no image is refused before any request at all.
func TestUploadFileSendsNothingUntilItMay(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, harnessOptions{cfg: config.Config{UploadDirs: []string{dir}}})
	path, data := pngFile(t, dir, "shot.png")
	text, out := h.ok("upload_file", map[string]any{"project": alpha, "path": path, "dry_run": true})
	if get(out, "outcome") != "dry_run" || get(out, "would_send", "operation") != "upload the image to the project" ||
		get(out, "markdown") != "" || get(out, "size") != float64(len(data)) || !strings.HasPrefix(text, "Dry run: nothing was written.") {
		t.Errorf("dry run: %v\n%s", out, text)
	}
	if uploadPosts(h) != 0 || len(h.gl.Uploads(alpha)) != 0 {
		t.Errorf("a dry run uploaded: %d request(s)", uploadPosts(h))
	}

	key := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(key, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.gl.ResetRequests()
	h.fails("upload_file", map[string]any{"project": alpha, "path": key}, "invalid")
	if n := len(h.gl.Requests()); n != 0 {
		t.Errorf("a refused file still made %d request(s)", n)
	}
}

// upload_file reads only from the directories GITLAB_MCP_UPLOAD_DIRS
// names; unset, it refuses and says to set it.
func TestUploadFileReadsOnlyFromTheSettingsDirectories(t *testing.T) {
	dir := t.TempDir()
	path, _ := pngFile(t, dir, "shot.png")
	h := newHarness(t, harnessOptions{cfg: config.Config{UploadDirs: []string{dir}}})
	h.ok("upload_file", map[string]any{"project": alpha, "path": path})

	h = newHarness(t, harnessOptions{})
	text := h.fails("upload_file", map[string]any{"project": alpha, "path": path}, "blocked")
	if !strings.Contains(text, config.EnvUploadDirs+" names, and it is not set") || len(h.gl.Requests()) != 0 {
		t.Errorf("refusal: %s; %d request(s)", text, len(h.gl.Requests()))
	}
}

func TestUploadFileIsHeldToTheAllowList(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, harnessOptions{cfg: config.Config{UploadDirs: []string{dir}, WriteNamespaces: []string{gitlabtest.GroupSub}}})
	path, _ := pngFile(t, dir, "shot.png")
	if text := h.fails("upload_file", map[string]any{"project": alpha, "path": path}, "blocked"); !strings.Contains(text, config.EnvWriteNamespaces) {
		t.Errorf("refusal: %s", text)
	}
	if uploadPosts(h) != 0 {
		t.Fatal("an upload outside the allow-list was sent")
	}
	h.ok("upload_file", map[string]any{"project": gitlabtest.ProjectBeta, "path": path})
	if len(h.gl.Uploads(gitlabtest.ProjectBeta)) != 1 {
		t.Error("the allowed project got no upload")
	}
}

// GitLab's refusal of an upload is passed on as it is, not taken for a
// lost answer.
func TestARefusedUploadIsReportedAsGitLabSaid(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, harnessOptions{cfg: config.Config{UploadDirs: []string{dir}}})
	h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: "/projects/2001/uploads", Status: http.StatusBadRequest,
		Body: `{"error":"file is invalid"}`})
	path, _ := pngFile(t, dir, "shot.png")
	if text := h.fails("upload_file", map[string]any{"project": alpha, "path": path}, "invalid"); !strings.Contains(text, "file is invalid") {
		t.Errorf("refusal: %s", text)
	}
	if uploadPosts(h) != 1 {
		t.Errorf("%d upload request(s), want 1", uploadPosts(h))
	}
}

// An upload is a create: a lost answer is never sent again, and no read
// can settle it, so the result says what a repeat would leave (§4.5).
func TestALostUploadIsNotSentAgain(t *testing.T) {
	for _, landed := range []bool{true, false} {
		dir := t.TempDir()
		h := newHarness(t, harnessOptions{cfg: config.Config{UploadDirs: []string{dir}}})
		h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: "/projects/2001/uploads", AfterApply: landed,
			Status: http.StatusBadGateway, Body: `{"message":"502 Bad Gateway"}`})
		path, _ := pngFile(t, dir, "shot.png")
		text := h.fails("upload_file", map[string]any{"project": alpha, "path": path}, "ambiguous_outcome")
		if !strings.Contains(text, "a repeat uploads the image a second time") {
			t.Errorf("landed %v: %s", landed, text)
		}
		want := 0
		if landed {
			want = 1
		}
		if uploadPosts(h) != 1 || len(h.gl.Uploads(alpha)) != want {
			t.Errorf("landed %v: %d request(s), %d upload(s) stored", landed, uploadPosts(h), len(h.gl.Uploads(alpha)))
		}
	}
}
