// Package scopes is the one source of truth for the OAuth scopes this
// server requests in each mode, the scope each kind of tool needs, and
// the text people paste when they register their OAuth application.
//
// GitLab offers nothing between read_api and api for the REST surface
// (§2.6): read_api is enforced by HTTP method and covers every GET, and
// api covers everything else, merging and deleting included. So the
// scope follows read-only mode alone. The Ship and Destructive flags
// change what is registered, not what is requested, which is why
// registration is the control (§4.3, §9.4).
//
// docs/setup.md's application block is generated from SetupBlock and
// compared exactly by the staleness gate.
package scopes

import (
	"fmt"
	"slices"
	"strings"
)

// GitLab scopes.
const (
	// API is read and write access to the whole REST API.
	API = "api"
	// ReadAPI is GET and HEAD on the REST API.
	ReadAPI = "read_api"
)

// RedirectURI is the redirect URI people register with their OAuth
// application, and the one login sends. GitLab ignores the port of a
// loopback IP literal when it compares redirect URIs (§18 row 1), so the
// registered URI carries none and login listens on 127.0.0.1:0.
// `localhost` would need an exact port (§18 row 2).
const RedirectURI = "http://127.0.0.1/callback"

// implies maps a scope to the narrower scopes it covers. A token holding
// api satisfies a read_api requirement, so a person who granted more
// than asked is not told to log in again.
var implies = map[string][]string{
	API: {ReadAPI},
}

// Mode is a configuration with a distinct meaning for scopes.
type Mode string

// Modes.
const (
	ModeReadOnly Mode = "read-only"
	ModeDefault  Mode = "default"
)

// ModeFor is the mode a configuration is in.
func ModeFor(readOnly bool) Mode {
	if readOnly {
		return ModeReadOnly
	}
	return ModeDefault
}

// ForMode is the set login requests in a mode.
func ForMode(m Mode) []string {
	if m == ModeReadOnly {
		return []string{ReadAPI}
	}
	return []string{API}
}

// Kind is what a tool does, as far as scopes are concerned. It mirrors
// the kinds of §4.3; the tool registry maps its own kind onto this one.
type Kind string

// Kinds.
const (
	KindRead        Kind = "read"
	KindWrite       Kind = "write"
	KindShip        Kind = "ship"
	KindDestructive Kind = "destructive"
)

// Kinds is every kind, in the order §4.3 tabulates them.
func Kinds() []Kind { return []Kind{KindRead, KindWrite, KindShip, KindDestructive} }

// Required is the narrowest scope that lets a tool of kind k work. Read
// needs read_api, which api also covers; everything else needs api. An
// unknown kind needs api, the answer that never registers a write under
// a read-only token.
func Required(k Kind) string {
	if k == KindRead {
		return ReadAPI
	}
	return API
}

// Satisfied reports whether granted covers needed, directly or through
// a wider scope.
func Satisfied(granted []string, needed string) bool {
	for _, g := range granted {
		if g == needed || slices.Contains(implies[g], needed) {
			return true
		}
	}
	return false
}

// Missing returns the scopes of needed that granted does not cover, in
// needed's order.
func Missing(granted, needed []string) []string {
	var out []string
	for _, n := range needed {
		if !Satisfied(granted, n) {
			out = append(out, n)
		}
	}
	return out
}

// Parse splits the scope string of a token response. OAuth separates
// scopes with spaces (RFC 6749 §3.3); extra whitespace is ignored.
func Parse(s string) []string { return strings.Fields(s) }

// ModeRow is one row of the scope table: a mode, the settings that
// select it, and what login requests in it.
type ModeRow struct {
	Mode   Mode
	Flags  []string
	Scopes []string
}

// Modes is every mode with the settings that select it, in the order
// the documentation tabulates them. Built from ForMode, so the table
// cannot say one thing while login does another.
func Modes() []ModeRow {
	return []ModeRow{
		{Mode: ModeDefault, Scopes: ForMode(ModeDefault)},
		{Mode: ModeReadOnly, Flags: []string{"GITLAB_MCP_READ_ONLY=true"}, Scopes: ForMode(ModeReadOnly)},
	}
}

// SetupBlock is the exact text people follow when they register the
// OAuth application for mode m: GitLab's form fields and the value each
// takes. docs/setup.md carries one block per mode, generated from this.
func SetupBlock(m Mode) string {
	return fmt.Sprintf(`Name:          gitlab-mcp
Redirect URI:  %s
Confidential:  unchecked
Scopes:        %s
`, RedirectURI, strings.Join(ForMode(m), " "))
}
