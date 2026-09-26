package redact

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// A Masker replaces what identifies an instance, a project or a person
// with stable placeholders, {host 1}, {user 1}, {path 2}, for the
// reports people paste into a bug form. The same value always gets the
// same placeholder, so a report still reads as a story, and Footer says
// how many of each were hidden.
//
// Shapes are found as Text finds them. Values that have no shape — the
// username `doctor` read from /user, a project path the person typed —
// are registered with Known and matched as whole words, case
// insensitively.
type Masker struct {
	mu     sync.Mutex
	known  []knownValue
	seen   map[string]string
	counts map[string]int
}

type knownValue struct{ kind, value string }

// NewMasker returns a masker with nothing registered.
func NewMasker() *Masker {
	return &Masker{seen: map[string]string{}, counts: map[string]int{}}
}

// Known registers a value of a kind to mask wherever it appears. An
// empty value is ignored, as is gitlab.com as a host.
func (m *Masker) Known(kind, value string) {
	value = strings.TrimSpace(value)
	if value == "" || (kind == KindHost && publicHosts[strings.ToLower(value)]) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.known = append(m.known, knownValue{kind, value})
	// Longest first, so a path is replaced before a group inside it.
	sort.SliceStable(m.known, func(i, j int) bool { return len(m.known[i].value) > len(m.known[j].value) })
}

// placeholderRE matches a placeholder this type wrote, so a second pass
// leaves it alone.
var placeholderRE = regexp.MustCompile(`\{[a-z\-]+ [0-9]+\}`)

// Text masks s. Shapes first, as Text does, then registered values in
// the text between the placeholders already written, so a username that
// happens to be "host" cannot rewrite {host 1}.
func (m *Masker) Text(s string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	s = token.ReplaceAllStringFunc(s, func(v string) string { return m.placeholder(KindToken, v) })
	s = urlRE.ReplaceAllStringFunc(s, func(v string) string { return maskURL(v, m.placeholder) })
	s = address.ReplaceAllStringFunc(s, func(v string) string { return m.placeholder(KindEmail, strings.ToLower(v)) })
	s = clientID.ReplaceAllStringFunc(s, func(v string) string { return m.placeholder(KindClientID, v) })
	for _, k := range m.known {
		s = outsidePlaceholders(s, func(seg string) string {
			return replaceWord(seg, k.value, func() string { return m.placeholder(k.kind, asciiLower(k.value)) })
		})
	}
	return s
}

// outsidePlaceholders applies f to the text between the placeholders in
// s, leaving the placeholders as they are.
func outsidePlaceholders(s string, f func(string) string) string {
	var b strings.Builder
	last := 0
	for _, loc := range placeholderRE.FindAllStringIndex(s, -1) {
		b.WriteString(f(s[last:loc[0]]))
		b.WriteString(s[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(f(s[last:]))
	return b.String()
}

// replaceWord replaces each case-insensitive occurrence of word in s
// that is not part of a longer name. A name here runs over letters,
// digits, "_", "-" and ".", which is what GitLab usernames and paths are
// made of; a trailing "." is a sentence, not a name.
func replaceWord(s, word string, with func() string) string {
	lower, lw := asciiLower(s), asciiLower(word)
	var b strings.Builder
	i := 0
	for {
		j := strings.Index(lower[i:], lw)
		if j < 0 {
			b.WriteString(s[i:])
			return b.String()
		}
		start, end := i+j, i+j+len(lw)
		if (start == 0 || !nameByte(s[start-1])) && (end == len(s) || !nameByte(s[end]) || endsName(s, end)) {
			b.WriteString(s[i:start])
			b.WriteString(with())
		} else {
			b.WriteString(s[i:end])
		}
		i = end
	}
}

// asciiLower folds A-Z only, so byte offsets in the result are offsets
// in the input; strings.ToLower can change a string's length.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

func nameByte(c byte) bool {
	return c == '_' || c == '-' || c == '.' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// endsName reports whether the "." at s[i] ends a sentence rather than
// continuing a name.
func endsName(s string, i int) bool {
	return s[i] == '.' && (i+1 == len(s) || !nameByte(s[i+1]))
}

// placeholder returns the stable placeholder for value, numbering a new
// one. The caller holds m.mu.
func (m *Masker) placeholder(kind, value string) string {
	key := kind + "\x00" + value
	if p, ok := m.seen[key]; ok {
		return p
	}
	m.counts[kind]++
	p := "{" + kind + " " + strconv.Itoa(m.counts[kind]) + "}"
	m.seen[key] = p
	return p
}

// Count is how many distinct values have been masked.
func (m *Masker) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.seen)
}

// Footer says what was hidden, one count per kind in name order, or
// that nothing was.
func (m *Masker) Footer() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.seen) == 0 {
		return "redacted: nothing"
	}
	kinds := make([]string, 0, len(m.counts))
	for k := range m.counts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		parts = append(parts, fmt.Sprintf("%d %s", m.counts[k], k))
	}
	return "redacted: " + strings.Join(parts, ", ")
}
