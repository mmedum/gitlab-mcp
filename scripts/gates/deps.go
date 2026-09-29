package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/scripts/internal/gatekit"
)

// depsStaleAfter is how far behind a direct dependency may fall before
// it has to be upgraded or pinned with a reason. Six months gives
// dependabot time to surface an upgrade without forcing emergency bumps.
const depsStaleAfter = 183 * 24 * time.Hour

// depsMinDirect is the floor on direct dependencies read: the SDK, the
// schema library, the keyring, oauth2, and x/sys, x/term and x/time.
const depsMinDirect = 5

// depsModule is what `go list -m -u -json` prints, narrowed to what this
// reads.
type depsModule struct {
	Path     string
	Version  string
	Main     bool
	Indirect bool
	Update   *struct {
		Version string
		Time    *time.Time
	}
}

// deps fails when a direct dependency has had an update available for
// more than six months, unless go.mod says `// pinned: <reason>` on its
// require line. It needs the module proxy, so it is manual and in the
// release checklist rather than in `make check`.
func deps(out io.Writer, _ []string) error {
	gomod, err := os.ReadFile("go.mod")
	if err != nil {
		return err
	}
	listing, err := exec.Command("go", "list", "-m", "-u", "-json", "all").Output()
	if err != nil {
		return fmt.Errorf("go list -m -u: %w", err)
	}
	return depsCheck(out, string(gomod), listing, time.Now())
}

// depsCheck is the judgment over a go.mod and a `go list` listing, at a
// given time, so a test can state all three.
func depsCheck(out io.Writer, gomod string, listing []byte, now time.Time) error {
	dec := json.NewDecoder(strings.NewReader(string(listing)))
	var problems []string
	direct := 0
	for dec.More() {
		var m depsModule
		if err := dec.Decode(&m); err != nil {
			return fmt.Errorf("decode go list output: %w", err)
		}
		if m.Main || m.Indirect {
			continue
		}
		direct++
		reason, pinned := depsPinned(gomod, m.Path)
		switch {
		case pinned && len(reason) < 10:
			problems = append(problems, fmt.Sprintf("%s is pinned with %q, which is not a reason", m.Path, reason))
		case pinned:
			_, _ = fmt.Fprintf(out, "pinned  %s %s: %s\n", m.Path, m.Version, reason)
		case m.Update == nil:
			_, _ = fmt.Fprintf(out, "ok      %s %s is current\n", m.Path, m.Version)
		case m.Update.Time == nil:
			problems = append(problems, fmt.Sprintf("%s: %s is available and the proxy gives no release date", m.Path, m.Update.Version))
		case now.Sub(*m.Update.Time) > depsStaleAfter:
			problems = append(problems, fmt.Sprintf("%s: %s, and %s has been out since %s (%.0f days); upgrade, "+
				"or add `// pinned: <reason>` to its require line", m.Path, m.Version, m.Update.Version,
				m.Update.Time.Format(time.DateOnly), now.Sub(*m.Update.Time).Hours()/24))
		default:
			_, _ = fmt.Fprintf(out, "ok      %s %s (%s out since %s)\n", m.Path, m.Version, m.Update.Version,
				m.Update.Time.Format(time.DateOnly))
		}
	}
	problems = append(problems, gatekit.Floor("direct dependencies", direct, depsMinDirect)...)
	if err := gatekit.Problems(out, "the direct dependencies", problems); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "deps ok: %d direct dependencies, none more than six months behind\n", direct)
	return nil
}

// depsPinned reports the reason go.mod gives for holding a module back:
// `// pinned: <reason>` on its require line.
func depsPinned(gomod, path string) (string, bool) {
	re := regexp.MustCompile(`(?m)^(?:require\s+|\s+)` + regexp.QuoteMeta(path) + `\s+\S+.*//\s*pinned:(.*)$`)
	m := re.FindStringSubmatch(gomod)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}
