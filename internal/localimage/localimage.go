// Package localimage reads an image from a directory the person allowed
// in GITLAB_MCP_UPLOAD_DIRS, for upload_file to send (docs/architecture.md
// §7.10). It touches no network.
//
// An upload tool is an exfiltration channel by construction: a comment
// that persuades a model to "attach" a key file would send the key. So
// four checks stand between a path and the bytes sent:
//
//  1. The path is absolute, has no ".." element, and is lexically inside
//     a directory the person allowed.
//  2. It is opened through os.Root on that directory, so neither ".."
//     nor a symbolic link can lead outside it.
//  3. It names a regular file, not a symbolic link, a directory or a
//     FIFO. That is checked before opening and again on the open file, so
//     a file swapped in between is refused.
//  4. At most MaxBytes are read, and the bytes must sniff as PNG, JPEG,
//     GIF or WebP, the way GitLab's Workhorse sniffs the uploads it
//     serves. SVG is text that can carry script, and is refused.
package localimage

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mmedum/gitlab-mcp/v2/internal/config"
	"github.com/mmedum/gitlab-mcp/v2/internal/gapi"
)

// MaxBytes is the largest image read: 10 MiB, a tenth of gitlab.com's
// attachment limit. A screenshot or a mock-up is far smaller, and the
// whole image is held in memory and sent in one request that the HTTP
// timeout bounds (§7.10).
const MaxBytes = 10 << 20

// extensions are the image types accepted, by the media type
// http.DetectContentType gives them, each with the extension the name
// sent carries. GitLab decides from the extension whether Markdown
// embeds an upload as an image, and whether a private project serves it
// without sign-in, so it has to agree with the bytes.
var extensions = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// maxStem bounds the name sent, before its extension.
const maxStem = 100

// Image is an image read and checked, ready to send.
type Image struct {
	// Name is the name it is sent under: the file's base name with every
	// character but ASCII letters, digits, '.', '-' and '_' made '_', and
	// the extension of its type in place of its own. No directory goes
	// with it.
	Name string
	// Type is the media type its bytes sniff as.
	Type string
	Data []byte
}

// Read reads the image at path, which must be absolute and inside one of
// dirs. Every refusal is a classified error, and none reads more than it
// has to.
func Read(path string, dirs []string) (Image, error) {
	if !filepath.IsAbs(path) {
		return Image{}, gapi.Errf(gapi.ClassInvalid, "path must be the image's absolute path; a relative one would be read from "+
			"wherever the server was started")
	}
	if slices.Contains(strings.Split(filepath.ToSlash(path), "/"), "..") {
		return Image{}, gapi.Errf(gapi.ClassInvalid, "path may not contain a .. element; pass the image's path as it is")
	}
	if len(dirs) == 0 {
		return Image{}, gapi.Errf(gapi.ClassBlocked, "upload_file reads only from the directories %s names, and it is not set. "+
			"Nothing was read or sent. Set it to the directory holding the image and restart the server", config.EnvUploadDirs)
	}
	dir, rel, ok := within(filepath.Clean(path), dirs)
	if !ok {
		return Image{}, gapi.Errf(gapi.ClassBlocked, "path is outside every directory %s names (%s). Nothing was read or sent. "+
			"Save the image in one of them, or add its directory to the setting and restart the server",
			config.EnvUploadDirs, strings.Join(dirs, ", "))
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return Image{}, failed(err, "the allowed directory "+dir)
	}
	defer func() { _ = root.Close() }()

	before, err := root.Lstat(rel)
	if err != nil {
		return Image{}, failed(err, "path")
	}
	if err := regular(before); err != nil {
		return Image{}, err
	}
	f, err := root.OpenFile(rel, os.O_RDONLY|openFlags, 0)
	if err != nil {
		return Image{}, failed(err, "path")
	}
	defer func() { _ = f.Close() }()
	after, err := f.Stat()
	if err != nil {
		return Image{}, failed(err, "path")
	}
	if err := unchanged(before, after); err != nil {
		return Image{}, err
	}

	data, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return Image{}, failed(err, "path")
	}
	if len(data) > MaxBytes {
		return Image{}, gapi.Errf(gapi.ClassInvalid, "the image is larger than %d MiB, the most upload_file sends; make it smaller first",
			MaxBytes>>20)
	}
	typ := http.DetectContentType(data)
	ext, ok := extensions[typ]
	if !ok {
		return Image{}, gapi.Errf(gapi.ClassInvalid, "path is not a PNG, JPEG, GIF or WebP image: its bytes read as %s. upload_file "+
			"sends only those four, read from the bytes rather than the name; SVG is refused because it is text that can carry script", typ)
	}
	return Image{Name: name(path, ext), Type: typ, Data: data}, nil
}

// within finds the widest allowed directory lexically holding path, and
// path relative to it. The widest, because a symbolic link that stays in
// it stays in an allowed directory, where a narrower one would refuse it.
func within(path string, dirs []string) (string, string, bool) {
	dir, rel := "", ""
	for _, d := range dirs {
		r, err := filepath.Rel(d, path)
		if err != nil || !filepath.IsLocal(r) {
			continue
		}
		if dir == "" || len(d) < len(dir) {
			dir, rel = d, r
		}
	}
	return dir, rel, dir != ""
}

// regular refuses anything but a regular file: a symbolic link, whose
// target the person may not have meant to share; a directory; a FIFO or
// a device, which a read could wait on for ever.
func regular(fi fs.FileInfo) error {
	if !fi.Mode().IsRegular() {
		return gapi.Errf(gapi.ClassInvalid, "path is %s, not a regular file; pass the path of the image itself", kind(fi.Mode()))
	}
	return nil
}

// unchanged holds the open file to the one checked before it was opened,
// which was regular: a file swapped for a link or a FIFO in between is
// another file.
func unchanged(before, after fs.FileInfo) error {
	if !os.SameFile(before, after) {
		return gapi.Errf(gapi.ClassConflict, "the file at path changed while it was being opened, so what was checked is not what "+
			"would be read; nothing was read or sent. Call again once it is settled")
	}
	return nil
}

func kind(m fs.FileMode) string {
	switch {
	case m&fs.ModeSymlink != 0:
		return "a symbolic link, which upload_file does not follow"
	case m.IsDir():
		return "a directory"
	case m&fs.ModeNamedPipe != 0:
		return "a FIFO"
	case m&(fs.ModeDevice|fs.ModeCharDevice) != 0:
		return "a device"
	case m&fs.ModeSocket != 0:
		return "a socket"
	}
	return "something other than a file"
}

// errEscapes is the text of os.Root's refusal of a path that leads out
// of it. Go does not export the error, so it is matched by its text; a
// change there refuses the path all the same, as [invalid].
const errEscapes = "path escapes from parent"

// failed classifies a failure to open, read or stat what names. The
// operating system's message names the path, so only its cause is kept.
func failed(err error, what string) error {
	var pe *fs.PathError
	cause := err
	if errors.As(err, &pe) {
		cause = pe.Err
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return gapi.Errf(gapi.ClassNotFound, "there is nothing at %s", what)
	case cause.Error() == errEscapes:
		return gapi.Errf(gapi.ClassBlocked, "%s leads outside the directory it is in, through a symbolic link; upload_file reads only "+
			"inside the directories the person allowed. Nothing was read or sent", what)
	}
	return gapi.Errf(gapi.ClassInvalid, "%s could not be read: %s", what, cause)
}

// name is the name an image is sent under (Image.Name).
func name(path, ext string) string {
	base := filepath.Base(path)
	stem := strings.TrimLeft(strings.TrimSuffix(base, filepath.Ext(base)), ".")
	var b strings.Builder
	for _, r := range stem {
		if b.Len() == maxStem {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "image" + ext
	}
	return b.String() + ext
}
