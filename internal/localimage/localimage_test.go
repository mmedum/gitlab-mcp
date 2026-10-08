package localimage

import (
	"bytes"
	"errors"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The images are generated here, never copied from anywhere (§9.1).

func encoded(t *testing.T, enc func(*bytes.Buffer, image.Image) error) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := enc(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func pngBytes(t *testing.T) []byte {
	return encoded(t, func(b *bytes.Buffer, m image.Image) error { return png.Encode(b, m) })
}

func jpegBytes(t *testing.T) []byte {
	return encoded(t, func(b *bytes.Buffer, m image.Image) error { return jpeg.Encode(b, m, nil) })
}

func gifBytes(t *testing.T) []byte {
	return encoded(t, func(b *bytes.Buffer, m image.Image) error { return gif.Encode(b, m, nil) })
}

// webpBytes is a lossless 1×1 WebP, written out: the standard library
// has no WebP encoder. RIFF, the size of what follows, WEBP, then one
// VP8L chunk.
var webpBytes = []byte("RIFF\x1a\x00\x00\x00WEBPVP8L\x0d\x00\x00\x00\x2f\x00\x00\x00\x10\x07\x10\x11\x11\x88\x88\xfe\x07\x00")

func write(t *testing.T, path string, data []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// symlink makes a link, or skips the test where the system will not let
// it, as Windows does without the right privilege.
func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
}

// kindOf is a refusal's kind, 0 for anything else.
func kindOf(err error) Kind {
	var e *Error
	if !errors.As(err, &e) {
		return 0
	}
	return e.Kind
}

func TestReadsAnImageAndNamesItByItsType(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		file, wantName, wantType string
		data                     []byte
	}{
		{"shot.png", "shot.png", "image/png", pngBytes(t)},
		{"photo.jpeg", "photo.jpg", "image/jpeg", jpegBytes(t)},
		{"anim.GIF", "anim.gif", "image/gif", gifBytes(t)},
		{"pic", "pic.webp", "image/webp", webpBytes},
		{"notes.txt", "notes.png", "image/png", pngBytes(t)},
		{"photo.backup.jpg", "photo.backup.jpg", "image/jpeg", jpegBytes(t)},
		{"my shot (1).png", "my_shot__1_.png", "image/png", pngBytes(t)},
		{"skærm.png", "sk_rm.png", "image/png", pngBytes(t)},
		{".png", "image.png", "image/png", pngBytes(t)},
		{"..hidden.gif", "hidden.gif", "image/gif", gifBytes(t)},
		{strings.Repeat("a", 120) + ".png", strings.Repeat("a", 100) + ".png", "image/png", pngBytes(t)},
	}
	for _, c := range cases {
		path := write(t, filepath.Join(dir, c.file), c.data)
		img, err := Read(path, []string{dir})
		if err != nil {
			t.Errorf("%s: %v", c.file, err)
			continue
		}
		if img.Name != c.wantName || img.Type != c.wantType || !bytes.Equal(img.Data, c.data) {
			t.Errorf("%s: name %q type %q, %d bytes; want %q %q, %d bytes", c.file, img.Name, img.Type, len(img.Data),
				c.wantName, c.wantType, len(c.data))
		}
	}
}

// MaxBytes is read whole, and a byte more is refused, by the size the
// file has when it is opened: a file of zeros a byte too large is
// refused as too large, not as no image, which reading it would find.
func TestTheSizeCap(t *testing.T) {
	dir := t.TempDir()
	head := pngBytes(t)
	atCap := write(t, filepath.Join(dir, "cap.png"), append(head, make([]byte, MaxBytes-len(head))...))
	img, err := Read(atCap, []string{dir})
	if err != nil || len(img.Data) != MaxBytes {
		t.Errorf("an image of exactly %d bytes: %d bytes, %v", MaxBytes, len(img.Data), err)
	}
	over := write(t, filepath.Join(dir, "over.png"), append(head, make([]byte, MaxBytes+1-len(head))...))
	if _, err := Read(over, []string{dir}); kindOf(err) != TooLarge {
		t.Errorf("an image a byte over: %v", err)
	}
	zeros := filepath.Join(dir, "zeros.png")
	f, err := os.Create(zeros)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxBytes + 1); err != nil || f.Close() != nil {
		t.Fatal(err)
	}
	if _, err := Read(zeros, []string{dir}); kindOf(err) != TooLarge {
		t.Errorf("zeros a byte over: %v", err)
	}
}

// Each row breaks one thing a readable image needs, and says the class
// the refusal carries.
func TestRefusals(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	png := pngBytes(t)
	good := write(t, filepath.Join(dir, "good.png"), png)
	cases := []struct {
		name string
		path func(t *testing.T) string
		dirs []string
		kind Kind
	}{
		{"a relative path", func(*testing.T) string { return "good.png" }, []string{dir}, NotAbsolute},
		{"a .. element, even one that leads back in", func(*testing.T) string {
			return filepath.Join(dir, "sub") + string(filepath.Separator) + ".." + string(filepath.Separator) + "good.png"
		}, []string{dir}, DotDot},
		{"no allowed directory", func(*testing.T) string { return good }, nil, NoDirectories},
		{"outside every allowed directory", func(t *testing.T) string {
			return write(t, filepath.Join(other, "elsewhere.png"), png)
		}, []string{dir}, Outside},
		{"nothing there", func(*testing.T) string { return filepath.Join(dir, "missing.png") }, []string{dir}, Missing},
		{"an allowed directory that is gone", func(*testing.T) string { return filepath.Join(dir, "gone", "shot.png") },
			[]string{filepath.Join(dir, "gone")}, NoDirectory},
		{"a file the account may not read", func(t *testing.T) string {
			if runtime.GOOS == "windows" || os.Getuid() == 0 {
				t.Skip("file modes do not refuse a read here")
			}
			p := write(t, filepath.Join(dir, "locked.png"), png)
			if err := os.Chmod(p, 0); err != nil {
				t.Fatal(err)
			}
			return p
		}, []string{dir}, Unreadable},
		{"a directory", func(t *testing.T) string {
			p := filepath.Join(dir, "folder.png")
			if err := os.Mkdir(p, 0o700); err != nil {
				t.Fatal(err)
			}
			return p
		}, []string{dir}, NotRegular},
		{"a FIFO", func(t *testing.T) string {
			p := filepath.Join(dir, "pipe.png")
			if err := mkfifo(p); err != nil {
				t.Skipf("no FIFOs here: %v", err)
			}
			return p
		}, []string{dir}, NotRegular},
		{"a symbolic link to an image beside it", func(t *testing.T) string {
			p := filepath.Join(dir, "link.png")
			symlink(t, good, p)
			return p
		}, []string{dir}, NotRegular},
		{"a directory link that leads outside", func(t *testing.T) string {
			write(t, filepath.Join(other, "inside", "secret.png"), png)
			p := filepath.Join(dir, "out")
			target, err := filepath.Rel(dir, filepath.Join(other, "inside"))
			if err != nil {
				t.Fatal(err)
			}
			symlink(t, target, p)
			return filepath.Join(p, "secret.png")
		}, []string{dir}, Escapes},
		{"an absolute directory link, even to inside", func(t *testing.T) string {
			write(t, filepath.Join(dir, "real", "shot.png"), png)
			p := filepath.Join(dir, "absolute")
			symlink(t, filepath.Join(dir, "real"), p)
			return filepath.Join(p, "shot.png")
		}, []string{dir}, Escapes},
		{"text", func(t *testing.T) string {
			return write(t, filepath.Join(dir, "key.png"), []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n"))
		}, []string{dir}, NotImage},
		// With its XML declaration, as drawing tools write one, an SVG
		// sniffs as XML rather than plain text.
		{"an SVG", func(t *testing.T) string {
			return write(t, filepath.Join(dir, "drawing.svg"),
				[]byte(`<?xml version="1.0" encoding="UTF-8"?><svg><script>alert(1)</script></svg>`))
		}, []string{dir}, NotImage},
		{"an empty file", func(t *testing.T) string { return write(t, filepath.Join(dir, "empty.png"), nil) }, []string{dir}, NotImage},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Read(c.path(t), c.dirs)
			if err == nil {
				t.Fatalf("read, want kind %d", c.kind)
			}
			if got := kindOf(err); got != c.kind {
				t.Errorf("kind %d, %v; want kind %d", got, err, c.kind)
			}
		})
	}
	// A file that is not a regular file says what it is.
	var e *Error
	if _, err := Read(dir, []string{filepath.Dir(dir)}); !errors.As(err, &e) || e.Detail != "a directory" {
		t.Errorf("a directory: %v", err)
	}
}

// A relative link between directories is followed while it stays inside
// an allowed directory, the widest one that holds the path: a link from
// a narrower allowed directory into a wider one is still inside.
func TestALinkThatStaysInsideIsFollowed(t *testing.T) {
	wide := t.TempDir()
	narrow := filepath.Join(wide, "narrow")
	png := pngBytes(t)
	write(t, filepath.Join(wide, "real", "shot.png"), png)
	if err := os.MkdirAll(narrow, 0o700); err != nil {
		t.Fatal(err)
	}
	symlink(t, filepath.Join("..", "real"), filepath.Join(narrow, "link"))
	path := filepath.Join(narrow, "link", "shot.png")

	if img, err := Read(path, []string{narrow, wide}); err != nil || !bytes.Equal(img.Data, png) {
		t.Errorf("inside the wider directory: %v", err)
	}
	if _, err := Read(path, []string{narrow}); kindOf(err) != Escapes {
		t.Errorf("with only the narrower directory allowed: %v", err)
	}
}

// An allowed directory that is itself a link to elsewhere works: the
// wider allowed directory holding it refuses the link, and the path is
// opened through the narrower one instead.
func TestAnAllowedDirectoryThatIsALinkWorks(t *testing.T) {
	wide, elsewhere := t.TempDir(), t.TempDir()
	png := pngBytes(t)
	write(t, filepath.Join(elsewhere, "shot.png"), png)
	shots := filepath.Join(wide, "shots")
	symlink(t, elsewhere, shots)
	path := filepath.Join(shots, "shot.png")

	if img, err := Read(path, []string{wide, shots}); err != nil || !bytes.Equal(img.Data, png) {
		t.Errorf("both allowed: %v", err)
	}
	if _, err := Read(path, []string{wide}); kindOf(err) != Escapes {
		t.Errorf("only the wider allowed: %v", err)
	}
}

// What was opened must be what was checked: the same regular file.
func TestTheOpenFileIsTheOneChecked(t *testing.T) {
	dir := t.TempDir()
	a := write(t, filepath.Join(dir, "a.png"), pngBytes(t))
	b := write(t, filepath.Join(dir, "b.png"), pngBytes(t))
	stat := func(p string) fs.FileInfo {
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		return fi
	}
	if err := unchanged(stat(a), stat(a)); err != nil {
		t.Errorf("the same file: %v", err)
	}
	if err := unchanged(stat(a), stat(b)); kindOf(err) != Changed {
		t.Errorf("another file: %v", err)
	}
}
