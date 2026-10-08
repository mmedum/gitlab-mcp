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
// before anything is sent. These clients declare no elicitation, so
// nothing is asked; the question is ask_test.go's.

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

// uploadHarness is a harness whose upload_file may read from one
// directory, beside cfg, and that directory.
func uploadHarness(t *testing.T, cfg config.Config) (*harness, string) {
	t.Helper()
	cfg = withUploads(t, cfg)
	return newHarness(t, harnessOptions{cfg: cfg}), cfg.UploadDirs[0]
}

func TestUploadFile(t *testing.T) {
	h, dir := uploadHarness(t, config.Config{})
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
	h, dir := uploadHarness(t, config.Config{})
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
		{"public", alpha, ptr(true), "anyone_with_link", "In this public project anyone with an image's link can open it"},
		{"private, sign-in required", gitlabtest.ProjectBeta, ptr(true), "project_readers",
			"Only people signed in who can see this private project can open an image by its link"},
		{"private, sign-in not required", gitlabtest.ProjectBeta, ptr(false), "anyone_with_link",
			"In this private project anyone with an image's link can open it, signed in or not"},
		{"private, not said", gitlabtest.ProjectBeta, nil, "unknown",
			"unless it does, anyone with an image's link can open it"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, dir := uploadHarness(t, config.Config{})
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
	h, dir := uploadHarness(t, config.Config{})
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
	if text := h.fails("upload_file", map[string]any{"project": alpha, "path": key}, "invalid"); strings.Contains(text, "text/") {
		t.Errorf("the refusal says what the file holds: %s", text)
	}
	if n := writesSent(h); n != 0 {
		t.Errorf("a refused file still made %d write(s)", n)
	}
}

// upload_file reads only from the directories GITLAB_MCP_UPLOAD_DIRS
// names; unset, it refuses and says to set it.
func TestUploadFileReadsOnlyFromTheSettingsDirectories(t *testing.T) {
	h, dir := uploadHarness(t, config.Config{})
	path, _ := pngFile(t, dir, "shot.png")
	h.ok("upload_file", map[string]any{"project": alpha, "path": path})
	// Outside them, the refusal names the setting and not the directories.
	outside, _ := pngFile(t, t.TempDir(), "elsewhere.png")
	text := h.fails("upload_file", map[string]any{"project": alpha, "path": outside}, "blocked")
	if !strings.Contains(text, "outside every directory "+config.EnvUploadDirs+" names") || strings.Contains(text, dir) {
		t.Errorf("outside: %s", text)
	}

	h = newHarness(t, harnessOptions{})
	text = h.fails("upload_file", map[string]any{"project": alpha, "path": path}, "blocked")
	if !strings.Contains(text, config.EnvUploadDirs+" names, and it is not set") || writesSent(h) != 0 {
		t.Errorf("refusal: %s; %d write(s)", text, writesSent(h))
	}
}

// The allow-list is held before anything on this machine is read: a
// call aimed outside it learns nothing about the path, not even that
// nothing is there.
func TestUploadFileIsHeldToTheAllowList(t *testing.T) {
	h, dir := uploadHarness(t, config.Config{WriteNamespaces: []string{gitlabtest.GroupSub}})
	path, _ := pngFile(t, dir, "shot.png")
	for _, p := range []string{path, filepath.Join(dir, "missing.png"), "relative.png"} {
		text := h.fails("upload_file", map[string]any{"project": alpha, "path": p}, "blocked")
		if !strings.Contains(text, config.EnvWriteNamespaces) || strings.Contains(text, "path") {
			t.Errorf("%s: %s", p, text)
		}
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
	h, dir := uploadHarness(t, config.Config{})
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
	for _, c := range []struct {
		name           string
		landed, stored bool
	}{
		{"landed", true, true},
		{"never arrived", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, dir := uploadHarness(t, config.Config{})
			h.gl.Inject(gitlabtest.Fault{Method: http.MethodPost, Path: "/projects/2001/uploads", AfterApply: c.landed,
				Status: http.StatusBadGateway, Body: `{"message":"502 Bad Gateway"}`})
			path, _ := pngFile(t, dir, "shot.png")
			text := h.fails("upload_file", map[string]any{"project": alpha, "path": path}, "ambiguous_outcome")
			if !strings.Contains(text, "a repeat uploads the image a second time") {
				t.Errorf("%s", text)
			}
			if uploadPosts(h) != 1 || (len(h.gl.Uploads(alpha)) == 1) != c.stored {
				t.Errorf("%d request(s), %d upload(s) stored", uploadPosts(h), len(h.gl.Uploads(alpha)))
			}
		})
	}
}
