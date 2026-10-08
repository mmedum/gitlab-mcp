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
//
// The sniff keeps out what is not an image: a key, a configuration
// file, a document. It does not prove the whole file is one. Bytes after
// a valid image header go too, and a file made that way needs someone
// who can write to this machine, which an instruction planted in GitLab
// content cannot do by itself.
package localimage

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// Kind is why an image was not read. The caller words each one and
// gives it a class: the names of the tool and the setting are its.
type Kind int

// The kinds.
const (
	// NotAbsolute: the path is relative.
	NotAbsolute Kind = iota + 1
	// DotDot: the path has a ".." element.
	DotDot
	// NoDirectories: no directory is allowed.
	NoDirectories
	// Outside: the path is in none of the allowed directories.
	Outside
	// NoDirectory: an allowed directory holding the path does not exist.
	NoDirectory
	// Missing: nothing is at the path.
	Missing
	// Escapes: a symbolic link along the path leads out of every allowed
	// directory holding it.
	Escapes
	// NotRegular: a symbolic link, a directory, a FIFO or a device;
	// Detail says which.
	NotRegular
	// Changed: the file was swapped between the check and the open.
	Changed
	// TooLarge: more than MaxBytes.
	TooLarge
	// NotImage: the bytes are not a PNG, JPEG, GIF or WebP.
	NotImage
	// Unreadable: the operating system refused; Detail is its reason,
	// without the path.
	Unreadable
)

// Error is a refusal to read an image.
type Error struct {
	Kind   Kind
	Detail string
}

func (e *Error) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("localimage: refused (kind %d): %s", e.Kind, e.Detail)
	}
	return fmt.Sprintf("localimage: refused (kind %d)", e.Kind)
}

func refuse(k Kind) *Error { return &Error{Kind: k} }

// Read reads the image at path, which must be absolute and inside one of
// dirs. Every refusal is an *Error, and none reads more than it has to.
func Read(path string, dirs []string) (Image, error) {
	switch {
	case !filepath.IsAbs(path):
		return Image{}, refuse(NotAbsolute)
	case slices.Contains(strings.Split(filepath.ToSlash(path), "/"), ".."):
		return Image{}, refuse(DotDot)
	case len(dirs) == 0:
		return Image{}, refuse(NoDirectories)
	}
	candidates := within(filepath.Clean(path), dirs)
	if len(candidates) == 0 {
		return Image{}, refuse(Outside)
	}
	// The widest directory first; a narrower one is tried when a link in
	// the path leads out of the wider, as an allowed directory that is
	// itself a link does.
	var f *os.File
	var size int64
	var err error
	for _, c := range candidates {
		if f, size, err = openIn(c.dir, c.rel); !is(err, Escapes) {
			break
		}
	}
	if err != nil {
		return Image{}, err
	}
	defer func() { _ = f.Close() }()
	if size > MaxBytes {
		return Image{}, refuse(TooLarge)
	}

	// The type is told from the first 512 bytes, as Workhorse tells it, so
	// a file that is no image is refused having read only those.
	head := make([]byte, 512)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return Image{}, failed(err)
	}
	typ := http.DetectContentType(head[:n])
	ext, ok := extensions[typ]
	if !ok {
		return Image{}, refuse(NotImage)
	}
	// The rest, into a buffer the size the file had when it was opened.
	// The limit still bounds it, since the file can grow while it is read.
	var b bytes.Buffer
	b.Grow(int(size) + bytes.MinRead)
	b.Write(head[:n])
	if _, err := b.ReadFrom(io.LimitReader(f, MaxBytes+1-int64(n))); err != nil {
		return Image{}, failed(err)
	}
	if b.Len() > MaxBytes {
		return Image{}, refuse(TooLarge)
	}
	return Image{Name: name(path, ext), Type: typ, Data: b.Bytes()}, nil
}

// is reports a refusal of kind k.
func is(err error, k Kind) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == k
}

// candidate is an allowed directory lexically holding the path, and the
// path relative to it.
type candidate struct{ dir, rel string }

// within lists the allowed directories lexically holding path, widest
// first: a symbolic link that stays inside a wider one stays in an
// allowed directory, where a narrower one would refuse it.
func within(path string, dirs []string) []candidate {
	var out []candidate
	for _, d := range dirs {
		if r, err := filepath.Rel(d, path); err == nil && filepath.IsLocal(r) {
			out = append(out, candidate{dir: d, rel: r})
		}
	}
	slices.SortStableFunc(out, func(a, b candidate) int { return len(a.dir) - len(b.dir) })
	return out
}

// openIn opens rel inside dir through os.Root, checked before and after
// it is opened, and gives its size when opened.
func openIn(dir, rel string) (*os.File, int64, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, 0, refuse(NoDirectory)
		}
		return nil, 0, failed(err)
	}
	defer func() { _ = root.Close() }()
	before, err := root.Lstat(rel)
	if err != nil {
		return nil, 0, failed(err)
	}
	if err := regular(before); err != nil {
		return nil, 0, err
	}
	f, err := root.OpenFile(rel, os.O_RDONLY|openFlags, 0)
	if err != nil {
		return nil, 0, failed(err)
	}
	after, err := f.Stat()
	if err != nil {
		err = failed(err)
	} else {
		err = unchanged(before, after)
	}
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, after.Size(), nil
}

// regular refuses anything but a regular file: a symbolic link, whose
// target the person may not have meant to share; a directory; a FIFO or
// a device, which a read could wait on forever.
func regular(fi fs.FileInfo) error {
	if !fi.Mode().IsRegular() {
		return &Error{Kind: NotRegular, Detail: kind(fi.Mode())}
	}
	return nil
}

// unchanged holds the open file to the one checked before it was opened,
// which was regular: a file swapped for a link or a FIFO in between is
// another file.
func unchanged(before, after fs.FileInfo) error {
	if !os.SameFile(before, after) {
		return refuse(Changed)
	}
	return nil
}

func kind(m fs.FileMode) string {
	switch {
	case m&fs.ModeSymlink != 0:
		return "a symbolic link"
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
// change there refuses the path all the same, as Unreadable.
const errEscapes = "path escapes from parent"

// failed turns the operating system's refusal into an *Error. Its
// message names the path, so only its cause is kept.
func failed(err error) *Error {
	var pe *fs.PathError
	cause := err
	if errors.As(err, &pe) {
		cause = pe.Err
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return refuse(Missing)
	case cause.Error() == errEscapes:
		return refuse(Escapes)
	}
	return &Error{Kind: Unreadable, Detail: cause.Error()}
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
