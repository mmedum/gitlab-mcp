package gitlabtest

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
)

// formBody is a multipart body with one part: a file when filename is
// set, a plain field otherwise.
func formBody(t *testing.T, field, filename string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	var err error
	if filename != "" {
		var part interface{ Write([]byte) (int, error) }
		if part, err = w.CreateFormFile(field, filename); err == nil {
			_, err = part.Write(data)
		}
	} else {
		err = w.WriteField(field, string(data))
	}
	if err != nil || w.Close() != nil {
		t.Fatal(err)
	}
	return &b, w.FormDataContentType()
}

// The uploads route answers as lib/api/markdown_uploads.rb and the
// uploader behind it do at v19.4.1-ee.
func TestUploadsAnswerAsGitLabDoes(t *testing.T) {
	s := New(t, Options{})
	tok := s.Token()
	url := s.URL + "/api/v4/projects/2001/uploads"
	for i, c := range []struct {
		filename, stored, alt, markdown string
	}{
		{"my shot.png", "my_shot.png", "my_shot", "![my_shot](/uploads/%s/my_shot.png)"},
		{"notes.txt", "notes.txt", "notes.txt", "[notes.txt](/uploads/%s/notes.txt)"},
		{"drawing.svg", "drawing.svg", "drawing.svg", "![drawing.svg](/uploads/%s/drawing.svg)"},
		{`dir\sub\Clip.GIF`, "Clip.GIF", "Clip", "![Clip](/uploads/%s/Clip.GIF)"},
	} {
		body, ct := formBody(t, "file", c.filename, []byte("bytes"))
		resp, got := do(t, http.MethodPost, url, tok, body, ct)
		wantStatus(t, c.filename, resp, got, http.StatusCreated)
		secret := []string{"00000000000000000000000000080001", "00000000000000000000000000080002",
			"00000000000000000000000000080003", "00000000000000000000000000080004"}[i]
		want := map[string]any{"id": float64(80001 + i), "alt": c.alt, "url": "/uploads/" + secret + "/" + c.stored,
			"full_path": "/-/project/2001/uploads/" + secret + "/" + c.stored, "markdown": strings.Replace(c.markdown, "%s", secret, 1)}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: %s = %v, want %v", c.filename, k, got[k], v)
			}
		}
	}
	if ups := s.Uploads(ProjectAlpha); len(ups) != 4 || ups[0].User != "alice" || string(ups[0].Data) != "bytes" {
		t.Errorf("stored: %+v", ups)
	}

	field, ct := formBody(t, "file", "", []byte("not a file"))
	resp, got := do(t, http.MethodPost, url, tok, field, ct)
	wantError(t, "a plain field", resp, got, http.StatusBadRequest, "error", "file is invalid")
	resp, got = send(t, s, http.MethodPost, "/projects/2001/uploads", tok, obj{"file": "x"})
	wantError(t, "no form", resp, got, http.StatusBadRequest, "error", "file is missing")
	body, ct := formBody(t, "file", "shot.png", []byte("bytes"))
	resp, got = do(t, http.MethodPost, s.URL+"/api/v4/projects/"+strings.ReplaceAll(ProjectSecret, "/", "%2F")+"/uploads", tok, body, ct)
	wantStatus(t, "a project alice cannot read", resp, got, http.StatusNotFound)
}
