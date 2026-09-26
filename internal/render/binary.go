package render

import (
	"bytes"
	"net/http"
	"unicode/utf8"
)

// sniffBytes is how much of a file decides whether it is text, as git
// decides it.
const sniffBytes = 8000

// IsBinary reports content that is not text: a NUL byte, or bytes that
// are not UTF-8, in the first 8,000. Binary content is described by its
// metadata, never shown as lossy text (§4.8).
func IsBinary(b []byte) bool {
	sample := b[:min(len(b), sniffBytes)]
	if bytes.IndexByte(sample, 0) >= 0 {
		return true
	}
	// A cut sample may end inside a character; drop up to three bytes of
	// an incomplete one before judging.
	for i := 0; i < 3 && len(sample) > 0 && len(sample) == sniffBytes && !utf8.Valid(sample); i++ {
		sample = sample[:len(sample)-1]
	}
	return !utf8.Valid(sample)
}

// ContentType guesses a media type from the first bytes.
func ContentType(b []byte) string { return http.DetectContentType(b) }
