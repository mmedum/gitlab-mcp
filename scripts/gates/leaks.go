package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mmedum/gitlab-mcp/v2/internal/gapi/gitlabtest"
	core "github.com/mmedum/gitlab-mcp/v2/internal/redact"
	"github.com/mmedum/gitlab-mcp/v2/scripts/internal/gitx"
)

// leaks looks for anything from a real instance or account in the files
// git would commit (docs/architecture.md §9.1).
//
// Every rule is anchored on a shape the server's generated fields cannot
// take: an @ with a dotted domain, an https:// host, a GitLab token
// prefix, gitlab.com/ followed by a namespace, a 64-hex id beside the
// word that says what it is. Each is an allow-list: what is allowed is
// named, with the reason, and everything else of that shape is a
// finding. A deny-list naming what to look for would itself be the
// disclosure. Titles, bodies and file contents are ordinary words and no
// pattern finds them; that half is structural — fixtures are generated
// and the live driver reads only the scratch project it created.
func leaks(out io.Writer, _ []string) error {
	return leaksTree(out, ".", leaksMinFiles)
}

// leaksHistory runs the same rules over every blob, commit message and
// tag message reachable from any ref. A leak deleted from the tip is
// still in the log.
func leaksHistory(out io.Writer, _ []string) error {
	return leaksScanHistory(out, ".")
}

// leaksMinFiles is the floor on text files read from the working tree.
const leaksMinFiles = 50

// leaksAllow is one exemption from a rule, with the reason it is safe.
type leaksAllow struct {
	re     *regexp.Regexp
	reason string
}

// leaksRule is one shape of leak.
type leaksRule struct {
	name string
	re   *regexp.Regexp
	// group is the submatch the allow-list is held against; 0 is the
	// whole match.
	group int
	// value, when set, turns the match into the value the allow-list is
	// held against, such as a URL into its host.
	value func(string) string
	allow []leaksAllow
}

// leaksReserved matches the names RFC 2606 and RFC 6761 reserve for
// documentation and tests, and their subdomains, as a whole host.
const leaksReserved = `(?i)^(?:[A-Za-z0-9\-]+\.)*(?:example\.(?:com|org|net)|example|test|invalid|localhost)$`

// leaksFixtureNamespaces are the namespaces the in-memory instance
// generates under, and its users, who are namespaces too. Derived from
// gitlabtest, so a fixture user added there is allowed here without a
// second list.
func leaksFixtureNamespaces() string {
	names := append([]string{`example(?:-[A-Za-z0-9_.\-]+)?`}, gitlabtest.Users...)
	return `^(?:` + strings.Join(names, "|") + `)$`
}

// leaksPublicHosts are the hosts outside the reserved set that a
// document or workflow may name, each with the reason it names nobody.
var leaksPublicHosts = map[string]string{
	"gitlab.com":                          "the public instance; the namespace after it is its own rule",
	"docs.gitlab.com":                     "GitLab's public documentation",
	"github.com":                          "this repository's home and its actions; the maintainer's other repositories are their own rule",
	"raw.githubusercontent.com":           "the pinned upstream schema files the bundle manifest names",
	"token.actions.githubusercontent.com": "the OIDC issuer every signature verification names",
	"modelcontextprotocol.io":             "the protocol specification",
	"static.modelcontextprotocol.io":      "the registry's published schema",
	"registry.modelcontextprotocol.io":    "the public MCP registry",
	"www.contributor-covenant.org":        "the code of conduct's source",
	"creativecommons.org":                 "the code of conduct's license",
	"www.apache.org":                      "the license text",
	"semver.org":                          "the versioning scheme the CHANGELOG follows",
	"keepachangelog.com":                  "the CHANGELOG format",
	"img.shields.io":                      "the README's release badge",
	"pkg.go.dev":                          "public package documentation",
	"go.dev":                              "the Go project",
	"proxy.golang.org":                    "the public module proxy the deps gate reads",
	"json-schema.org":                     "the JSON Schema dialects the vendored schemas declare",
	"www.rfc-editor.org":                  "published RFCs",
	"datatracker.ietf.org":                "published RFCs",
	"claude.ai":                           "the client the bundle installs into",
	"docs.github.com":                     "GitHub's public documentation",
	"127.0.0.1":                           "loopback: the sign-in redirect and test servers",
	"[::1]":                               "loopback over IPv6: test servers",
}

// leaksGitLabPaths are gitlab.com paths that name GitLab's own pages or
// public source rather than somebody's namespace.
var leaksGitLabPaths = map[string]string{
	"gitlab-org": "GitLab Inc.'s public source, which the evidence log cites",
	"-":          "GitLab's own routes, such as /-/profile",
	"api":        "the REST API root",
	"oauth":      "the OAuth endpoints",
	"help":       "GitLab's help pages",
	"users":      "sign-in pages",
}

var leaksRules = []leaksRule{
	{
		name: "an address at a domain somebody could own",
		re:   regexp.MustCompile(`[A-Za-z0-9._%+\-=]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)*\.[A-Za-z]{2,}`),
		value: func(m string) string {
			_, domain, _ := strings.Cut(m, "@")
			return domain
		},
		allow: []leaksAllow{
			{regexp.MustCompile(leaksReserved), "RFC 2606 and RFC 6761 reserve these domains; they can never deliver"},
			{regexp.MustCompile(`^(?:anthropic\.com|users\.noreply\.github\.com)$`), "held by the no-reply rule below, address by address"},
		},
	},
	{
		name: "an address at a vendor's no-reply domain",
		re:   regexp.MustCompile(`[A-Za-z0-9._%+\-=]+@(?:anthropic\.com|users\.noreply\.github\.com)\b`),
		allow: []leaksAllow{
			{regexp.MustCompile(`^noreply@anthropic\.com$`),
				"the Co-Authored-By trailer on commits; a vendor's no-reply address that identifies nobody"},
			{regexp.MustCompile(`^(?:[0-9]+\+)?[A-Za-z0-9\-]+@users\.noreply\.github\.com$`),
				"GitHub's no-reply addresses, which exist so a commit carries no real address"},
		},
	},
	{
		name:  "a URL to a host outside the reserved and public set",
		re:    regexp.MustCompile(`https?://[^\s"'` + "`" + `<>(){},]+`),
		value: leaksURLHost,
		allow: []leaksAllow{
			{regexp.MustCompile(leaksReserved), "RFC 2606 and RFC 6761 reserve these names; they never resolve"},
			{leaksHostSet(), "a public host, listed in leaksPublicHosts with its reason"},
			{regexp.MustCompile(`^$`), "not a host: a template or a pattern, such as https://{host}"},
		},
	},
	{
		name:  "gitlab.com/ followed by a namespace outside the fixture set",
		re:    regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9.\-])gitlab\.com/([A-Za-z0-9_.\-]+)`),
		group: 1,
		allow: []leaksAllow{
			{regexp.MustCompile(leaksFixtureNamespaces()), "a namespace or user the in-memory instance generates"},
			{leaksPathSet(), "one of GitLab's own paths, listed in leaksGitLabPaths with its reason"},
		},
	},
	{
		name:  "another repository of this repository's owner",
		re:    regexp.MustCompile(`\bgithub\.com/mmedum/([A-Za-z0-9_.\-]+)`),
		group: 1,
		allow: []leaksAllow{
			{regexp.MustCompile(`^gitlab-mcp(?:\.svg|\.git)?$`), "this repository, its badge image or its clone URL"},
		},
	},
	{
		name: "a GitLab token",
		re:   regexp.MustCompile(core.TokenPattern),
		allow: []leaksAllow{
			{regexp.MustCompile(`^(?:gl[a-z]+-|GR1348941|_gitlab_session=)(?:EXAMPLE|FAKE|TEST)`),
				"a synthetic token: the prefix followed by EXAMPLE, FAKE or TEST, as .gitleaks.toml allows"},
		},
	},
	{
		// The keyword is the anchor: a bare 64-hex run is a SHA-256 as
		// often as an application id.
		name:  "an OAuth application id, secret or token beside its name",
		re:    regexp.MustCompile(`(?i)(?:client[_-]?id|application[_ -]?id|app[_-]?id|client[_-]?secret|secret|access[_-]?token|refresh[_-]?token)["'` + "`" + `]?\s*[:=]?\s*["'` + "`" + `]?([0-9a-f]{64})\b`),
		group: 1,
		allow: []leaksAllow{
			{regexp.MustCompile(`^(?:0{64}|1{64}|a{64}|f{64}|(?:0123456789abcdef){4})$`),
				"a synthetic value from the set .gitleaks.toml allows"},
		},
	},
}

func leaksHostSet() *regexp.Regexp {
	hosts := make([]string, 0, len(leaksPublicHosts))
	for h := range leaksPublicHosts {
		hosts = append(hosts, regexp.QuoteMeta(h))
	}
	slices.Sort(hosts)
	return regexp.MustCompile(`^(?:` + strings.Join(hosts, "|") + `)$`)
}

func leaksPathSet() *regexp.Regexp {
	paths := make([]string, 0, len(leaksGitLabPaths))
	for p := range leaksGitLabPaths {
		paths = append(paths, regexp.QuoteMeta(p))
	}
	slices.Sort(paths)
	return regexp.MustCompile(`(?i)^(?:` + strings.Join(paths, "|") + `)$`)
}

// leaksURLHost is the host a URL is for, decided by parsing rather than
// by matching text: userinfo before an @, a lookalike suffix and a case
// difference all fool a pattern. A URL that is a template or a pattern
// has no host, and gives "".
func leaksURLHost(raw string) string {
	// A URL written inside a regular expression escapes its dots.
	raw = strings.ReplaceAll(raw, `\.`, ".")
	raw = strings.TrimRight(raw, ".:;\\")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if strings.ContainsAny(host, "{}<>$%*") || host == "" {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() {
			if ip.To4() == nil {
				return "[::1]"
			}
			return "127.0.0.1"
		}
		return host
	}
	return host
}

// leaksSkip are files that are not ours to police, by repository path,
// with the reason.
var leaksSkip = map[string]string{
	"go.sum": "module hashes; nothing in it was written by a person",
	"testdata/openapi-v3.snapshot.json": "written only by `gates api-diff` from GitLab's public OpenAPI file at a pinned tag; " +
		"its examples are GitLab's own",
	"scripts/gates/schemas/mcpb-manifest-v0.3.schema.json": "upstream's published schema, vendored byte for byte; " +
		"the mcpb gate holds it to a recorded SHA-256",
	"scripts/gates/schemas/server-2025-12-11.schema.json": "upstream's published schema, vendored byte for byte; " +
		"the server-json gate holds it to a recorded SHA-256, and its examples are the registry's own",
}

// leaksExecutableMagic starts a linked executable.
var leaksExecutableMagic = [][]byte{
	[]byte("\x7fELF"),                                  // Linux, BSD
	[]byte("MZ"),                                       // Windows PE
	{0xfe, 0xed, 0xfa, 0xce}, {0xce, 0xfa, 0xed, 0xfe}, // Mach-O, 32-bit
	{0xfe, 0xed, 0xfa, 0xcf}, {0xcf, 0xfa, 0xed, 0xfe}, // Mach-O, 64-bit
	{0xca, 0xfe, 0xba, 0xbe}, // Mach-O universal
}

// leaksBinaryAllowed are the binary files the tree may carry, by path,
// with the reason. A binary is otherwise the finding: nothing here can
// read it, and a built executable's symbol table buries the line that
// matters.
var leaksBinaryAllowed = map[string]string{}

// leaksBase64 is a run that could be base64 or base64url: GitLab's
// files API returns content that way, so a fixture pasted from a real
// response hides its text from every rule above unless it is decoded.
var leaksBase64 = regexp.MustCompile(`[A-Za-z0-9+/_\-]{24,}={0,2}`)

// leaksFind reports every leak in text, each redacted. Text that
// decodes from base64 is scanned too, once.
func leaksFind(text string) []string {
	out := leaksFindPlain(text)
	for _, run := range leaksBase64.FindAllString(text, -1) {
		decoded, ok := leaksDecode(run)
		if !ok {
			continue
		}
		for _, f := range leaksFindPlain(decoded) {
			out = append(out, "inside base64 "+leaksRedact(run)+": "+f)
		}
	}
	return out
}

func leaksFindPlain(text string) []string {
	var out []string
	for _, rule := range leaksRules {
		for _, m := range rule.re.FindAllStringSubmatch(text, -1) {
			value := m[rule.group]
			if rule.value != nil {
				value = rule.value(value)
			}
			if slices.ContainsFunc(rule.allow, func(a leaksAllow) bool { return a.re.MatchString(value) }) {
				continue
			}
			out = append(out, rule.name+": "+leaksRedact(m[0]))
		}
	}
	return out
}

// leaksDecode decodes a base64 or base64url run and keeps it only when
// it is text: valid UTF-8 and mostly printable. A hash or a random id
// decodes to noise and is dropped.
func leaksDecode(run string) (string, bool) {
	trimmed := strings.TrimRight(run, "=")
	var data []byte
	var err error
	if strings.ContainsAny(trimmed, "-_") {
		data, err = base64.RawURLEncoding.DecodeString(trimmed)
	} else {
		data, err = base64.RawStdEncoding.DecodeString(trimmed)
	}
	if err != nil || len(data) < 12 || !utf8.Valid(data) {
		return "", false
	}
	printable := 0
	for _, r := range string(data) {
		if r == '\n' || r == '\t' || r == '\r' || (r >= 0x20 && r != 0x7f) {
			printable++
		}
	}
	if printable*10 < utf8.RuneCount(data)*9 {
		return "", false
	}
	return string(data), true
}

// leaksRedact shortens a finding so the report does not reprint it:
// enough to find it with a search, not enough to read it.
func leaksRedact(s string) string {
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + "…" + s[len(s)-2:] + " (" + strconv.Itoa(len(s)) + " chars)"
}

// leaksBinary reports whether data is not text: a NUL in the first 8000
// bytes, which is git's own rule.
func leaksBinary(data []byte) bool {
	return bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0
}

// leaksArtifact says why a binary file is a finding.
func leaksArtifact(data []byte) string {
	for _, magic := range leaksExecutableMagic {
		if bytes.HasPrefix(data, magic) {
			return fmt.Sprintf("a compiled executable (%d bytes); nothing built belongs in the tree", len(data))
		}
	}
	return fmt.Sprintf("%d bytes of binary that no rule can read; add it to leaksBinaryAllowed with the reason "+
		"it is safe, or leave it out", len(data))
}

// leaksTree scans what git would commit from root: tracked files and
// untracked files that are not ignored. The untracked half is where a
// phase's new files are, before anything else has read them.
func leaksTree(out io.Writer, root string, minFiles int) error {
	files, err := gitx.Files(root)
	if err != nil {
		return err
	}
	var findings []string
	scanned, binaries := 0, 0
	for _, name := range files {
		if _, skip := leaksSkip[name]; skip {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name))) //nolint:gosec // a file git lists
		if err != nil {
			continue
		}
		if leaksBinary(data) {
			binaries++
			if _, ok := leaksBinaryAllowed[name]; !ok {
				findings = append(findings, name+": "+leaksArtifact(data))
			}
			continue
		}
		scanned++
		for _, f := range leaksFind(string(data)) {
			findings = append(findings, name+": "+f)
		}
	}
	if len(findings) > 0 {
		slices.Sort(findings)
		for _, f := range findings {
			_, _ = fmt.Fprintln(out, "  "+f)
		}
		return fmt.Errorf("%d finding(s) that look like they came from a real instance or account", len(findings))
	}
	if scanned < minFiles {
		return fmt.Errorf("read %d text file(s), want at least %d; the scan is not seeing the repository", scanned, minFiles)
	}
	_, _ = fmt.Fprintf(out, "leaks ok: %d text files and %d allowed binary files, %d rules\n", scanned, binaries, len(leaksRules))
	return nil
}

// leaksObject is one git object read from `git cat-file --batch`.
type leaksObject struct {
	sha, kind string
	body      []byte
}

// leaksScanHistory scans every blob and every commit and tag message.
//
// A commit or tag is scanned from its message down: the header lines
// carry the author, committer and tagger that git writes itself, which
// are public in every repository by construction.
func leaksScanHistory(out io.Writer, root string) error {
	shas, paths, err := leaksHistoryObjects(root)
	if err != nil {
		return err
	}
	var h leaksHistoryScan
	if err := leaksCatFile(root, shas, func(o leaksObject) { h.scan(o, paths) }); err != nil {
		return err
	}

	// The floor, derived from git rather than guessed: every commit
	// reachable from a ref must have been read.
	count, err := gitx.Output(root, "rev-list", "--all", "--count")
	if err != nil {
		return err
	}
	want, err := strconv.Atoi(strings.TrimSpace(count))
	if err != nil {
		return fmt.Errorf("rev-list --count: %w", err)
	}
	if len(h.findings) > 0 {
		slices.Sort(h.findings)
		for _, f := range h.findings {
			_, _ = fmt.Fprintln(out, "  "+f)
		}
		return fmt.Errorf("%d finding(s) in the history", len(h.findings))
	}
	if want == 0 || h.commits < want || h.blobs == 0 {
		return fmt.Errorf("read %d commit(s) of %d and %d blob(s); the scan is not seeing the history",
			h.commits, want, h.blobs)
	}
	_, _ = fmt.Fprintf(out, "history leaks ok: %d commits, %d tag messages, %d blobs\n", h.commits, h.tagMsgs, h.blobs)
	return nil
}

// leaksHistoryObjects lists every object reachable from a ref, and every
// annotated tag, which is an object of its own. paths maps a blob to the
// path it was seen at.
func leaksHistoryObjects(root string) (shas []string, paths map[string]string, err error) {
	listing, err := gitx.Output(root, "rev-list", "--all", "--objects")
	if err != nil {
		return nil, nil, err
	}
	paths = map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(listing), "\n") {
		sha, path, _ := strings.Cut(line, " ")
		if sha == "" {
			continue
		}
		if _, seen := paths[sha]; !seen {
			shas = append(shas, sha)
		}
		paths[sha] = path
	}
	tags, err := gitx.Output(root, "for-each-ref", "--format=%(objectname) %(objecttype)", "refs/tags")
	if err != nil {
		return nil, nil, err
	}
	for line := range strings.SplitSeq(strings.TrimSpace(tags), "\n") {
		sha, kind, _ := strings.Cut(line, " ")
		if _, seen := paths[sha]; kind == "tag" && !seen {
			shas = append(shas, sha)
			paths[sha] = ""
		}
	}
	return shas, paths, nil
}

// leaksHistoryScan accumulates what a history scan read and found.
type leaksHistoryScan struct {
	findings                []string
	blobs, commits, tagMsgs int
}

// scan reads one object: a blob's content, or a commit's or tag's
// message.
func (h *leaksHistoryScan) scan(o leaksObject, paths map[string]string) {
	where := o.kind + " " + o.sha[:min(8, len(o.sha))]
	switch o.kind {
	case "blob":
		path := paths[o.sha]
		if _, skip := leaksSkip[path]; skip {
			return
		}
		h.blobs++
		where = path + "@" + o.sha[:min(8, len(o.sha))]
		if leaksBinary(o.body) {
			if _, ok := leaksBinaryAllowed[path]; !ok {
				h.findings = append(h.findings, where+": "+leaksArtifact(o.body))
			}
			return
		}
		for _, f := range leaksFind(string(o.body)) {
			h.findings = append(h.findings, where+": "+f)
		}
	case "commit", "tag":
		if o.kind == "commit" {
			h.commits++
		} else {
			h.tagMsgs++
		}
		_, message, _ := strings.Cut(string(o.body), "\n\n")
		for _, f := range leaksFind(message) {
			h.findings = append(h.findings, where+" message: "+f)
		}
	}
}

// leaksCatFile reads objects through one `git cat-file --batch` and
// hands each to fn as it arrives, so the history is never held whole.
func leaksCatFile(root string, shas []string, fn func(leaksObject)) error {
	cmd := exec.Command("git", "cat-file", "--batch")
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(strings.Join(shas, "\n") + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("git cat-file: %w", err)
	}
	readErr := leaksReadBatch(bufio.NewReader(stdout), len(shas), fn)
	if readErr != nil {
		// Drain what is left so git can exit rather than block on a
		// full pipe.
		_, _ = io.Copy(io.Discard, stdout)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("git cat-file: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return readErr
}

// leaksReadBatch reads n objects in `git cat-file --batch` form.
func leaksReadBatch(r *bufio.Reader, n int, fn func(leaksObject)) error {
	for range n {
		header, err := r.ReadString('\n')
		if err != nil {
			return fmt.Errorf("git cat-file: short output: %w", err)
		}
		fields := strings.Fields(header)
		if len(fields) != 3 {
			return fmt.Errorf("git cat-file: %q", strings.TrimSpace(header))
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil {
			return fmt.Errorf("git cat-file: %q", strings.TrimSpace(header))
		}
		body := make([]byte, size+1) // the object and its trailing newline
		if _, err := io.ReadFull(r, body); err != nil {
			return fmt.Errorf("git cat-file: %w", err)
		}
		fn(leaksObject{sha: fields[0], kind: fields[1], body: body[:size]})
	}
	return nil
}
