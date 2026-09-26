package main

import (
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/mmedum/gitlab-mcp/scripts/internal/gatekit"
	"github.com/mmedum/gitlab-mcp/scripts/internal/tsv"
)

// The API-coverage gate (docs/architecture.md §8a, CLAUDE.md rule 18).
//
// GitLab publishes far more than this server calls, and the gap is meant
// to be a set of decisions. testdata/api-coverage.tsv is those decisions,
// the snapshot is what GitLab publishes, and internal/gapi's Call
// literals are what the client sends. Three directions fail:
//
//  1. a published operation with no verdict — GitLab added it and
//     nobody judged it;
//  2. a client call with no used or gated row — the client grew a
//     request nobody recorded;
//  3. a row or rule matching nothing — a decision about an API that no
//     longer exists reads exactly like one about the current API.
//
// Plus the rules that keep the record honest: a rule's count is the
// gate's own count, two rules on one operation fail rather than
// resolve by precedence, and a reason has to be a sentence.

const coverageFile = "testdata/api-coverage.tsv"

const (
	// minReasonLen is the shortest reason accepted: "not needed" is not
	// a decision anybody can revisit.
	minReasonLen = 20
)

// coverageFloors are the least the gate must have read, so a truncated
// input cannot pass as a clean one.
type coverageFloors struct{ rows, calls, used int }

var realCoverageFloors = coverageFloors{rows: 300, calls: 20, used: 20}

// offSnapshotCalls are client calls outside the REST API the OpenAPI
// file describes, each with the reason. A call in this list that the
// client no longer makes fails, like any stale row.
var offSnapshotCalls = map[string]string{
	"GET oauth/token/info": "Doorkeeper's token introspection, served under the web root; the REST OpenAPI file does not describe OAuth",
}

var verdicts = []string{"used", "gated", "deferred", "written-off"}

// coverageRow is one line of the record.
type coverageRow struct {
	verb, path string // path without the trailing * of a rule
	rule       bool
	verdict    string
	count      int
	reason     string
	line       int
}

func (r coverageRow) key() string {
	if r.rule {
		return r.verb + " " + r.path + "*"
	}
	return r.verb + " " + r.path
}

func (r coverageRow) matches(op apiOperation) bool {
	if r.verb != "*" && r.verb != op.Verb {
		return false
	}
	if r.rule {
		return strings.HasPrefix(op.Path, r.path)
	}
	return op.Path == r.path
}

func apiCoverage(out io.Writer, _ []string) error {
	snap, err := readSnapshot(snapshotFile)
	if err != nil {
		return err
	}
	rows, problems := readCoverage(coverageFile)
	calls, callProblems, err := clientCalls(clientDir)
	if err != nil {
		return err
	}
	problems = append(problems, callProblems...)
	report, more := checkCoverage(snap, rows, calls, realCoverageFloors)
	problems = append(problems, more...)
	if err := gatekit.Problems(out, "API coverage", problems); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, report)
	return nil
}

func readCoverage(path string) ([]coverageRow, []string) {
	raw, problems := tsv.Read(path, 4)
	var rows []coverageRow
	for _, r := range raw {
		where := fmt.Sprintf("%s:%d", path, r.Line)
		key, verdict, countText, reason := r.Fields[0], r.Fields[1], r.Fields[2], strings.TrimSpace(r.Fields[3])
		verb, p, ok := strings.Cut(key, " ")
		if !ok || !strings.HasPrefix(p, "/") {
			problems = append(problems, fmt.Sprintf("%s: %q is not \"VERB /path\" or \"VERB /path*\"", where, key))
			continue
		}
		row := coverageRow{verb: verb, path: p, verdict: verdict, reason: reason, line: r.Line}
		if strings.HasSuffix(p, "*") {
			row.rule, row.path = true, strings.TrimSuffix(p, "*")
		}
		if verb == "*" && !row.rule {
			problems = append(problems, fmt.Sprintf("%s: %s names every verb of one path; spell each operation, or make it a rule with *", where, key))
			continue
		}
		if !slices.Contains(verdicts, verdict) {
			problems = append(problems, fmt.Sprintf("%s: %s has verdict %q; want one of %s", where, key, verdict, strings.Join(verdicts, ", ")))
			continue
		}
		n, err := strconv.Atoi(countText)
		if err != nil || n < 1 {
			problems = append(problems, fmt.Sprintf("%s: %s has count %q; it is how many operations the row covers", where, key, countText))
			continue
		}
		row.count = n
		if !row.rule && n != 1 {
			problems = append(problems, fmt.Sprintf("%s: %s names one operation and counts %d", where, key, n))
			continue
		}
		if row.rule && (verdict == "used" || verdict == "gated") {
			problems = append(problems, fmt.Sprintf("%s: %s is a rule marked %s; a used operation is bound to its client call one row at a time", where, key, verdict))
			continue
		}
		if len(reason) < minReasonLen {
			problems = append(problems, fmt.Sprintf("%s: %s has the reason %q; say why in a sentence of at least %d characters",
				where, key, reason, minReasonLen))
			continue
		}
		rows = append(rows, row)
	}
	return rows, problems
}

var clientMention = regexp.MustCompile(`\bClient\.[A-Za-z0-9_]+`)

// checkCoverage is the rule over three inputs, so it can be tested on
// fixtures. It returns the success line and the problems.
func checkCoverage(snap apiSnapshot, rows []coverageRow, calls []clientCall, fl coverageFloors) (string, []string) {
	var problems []string
	if len(rows) < fl.rows {
		problems = append(problems, fmt.Sprintf("%s has %d rows and the floor is %d; this is not the whole record",
			coverageFile, len(rows), fl.rows))
	}
	if len(calls) < fl.calls {
		problems = append(problems, fmt.Sprintf("%s has %d Call literals and the floor is %d; the gate is not reading the client",
			clientDir, len(calls), fl.calls))
	}
	a := assignVerdicts(snap, rows)
	problems = append(problems, a.problems...)
	problems = append(problems, a.stale()...)
	bound, more := bindCalls(snap, calls, a.verdictOf)
	problems = append(problems, more...)
	used, more := checkUsedRows(rows, bound)
	problems = append(problems, more...)
	if used < fl.used {
		problems = append(problems, fmt.Sprintf("%s has %d used or gated rows and the floor is %d", coverageFile, used, fl.used))
	}
	slices.Sort(problems)
	problems = slices.Compact(problems)

	tally := map[string]int{}
	for _, op := range snap.Operations {
		if r := a.verdictOf[op.key()]; r != nil {
			tally[r.verdict]++
		}
	}
	return fmt.Sprintf("api coverage ok: %d operations at %s (%d used, %d gated, %d deferred, %d written off) from %d rows and %d rules; %d client calls bound",
		len(snap.Operations), snap.Tag, tally["used"], tally["gated"], tally["deferred"], tally["written-off"],
		len(a.exact), len(a.rules), len(calls)), problems
}

// assignment is every operation's verdict, and what that showed about
// the record.
type assignment struct {
	exact     map[string]*coverageRow
	rules     []*coverageRow
	verdictOf map[string]*coverageRow // operation key -> its row or rule
	counted   map[*coverageRow]int
	seen      map[*coverageRow]bool
	problems  []string
}

// assignVerdicts is direction 1: every operation has exactly one verdict.
// An exact row decides for its operation; otherwise exactly one rule must
// match, because two rules do not take precedence over each other.
func assignVerdicts(snap apiSnapshot, rows []coverageRow) assignment {
	a := assignment{exact: map[string]*coverageRow{}, verdictOf: map[string]*coverageRow{},
		counted: map[*coverageRow]int{}, seen: map[*coverageRow]bool{}}
	for i := range rows {
		if rows[i].rule {
			a.rules = append(a.rules, &rows[i])
		} else {
			a.exact[rows[i].key()] = &rows[i]
		}
	}
	for _, op := range snap.Operations {
		if r, ok := a.exact[op.key()]; ok {
			a.seen[r] = true
			a.verdictOf[op.key()] = r
			continue
		}
		var hit []*coverageRow
		for _, r := range a.rules {
			if r.matches(op) {
				hit = append(hit, r)
			}
		}
		switch len(hit) {
		case 0:
			a.problems = append(a.problems, fmt.Sprintf("%s has no verdict in %s. GitLab publishes it; add a row, or a rule that covers it",
				op.key(), coverageFile))
		case 1:
			a.counted[hit[0]]++
			a.verdictOf[op.key()] = hit[0]
		default:
			names := make([]string, 0, len(hit))
			for _, h := range hit {
				names = append(names, fmt.Sprintf("%s (line %d)", h.key(), h.line))
			}
			a.problems = append(a.problems, fmt.Sprintf("%s is matched by %d rules: %s. Rules do not take precedence over each other; narrow one",
				op.key(), len(hit), strings.Join(names, ", ")))
		}
	}
	return a
}

// stale is direction 3: nothing in the record is about an operation that
// is not published, and every rule's count is what it covers now.
func (a assignment) stale() []string {
	var problems []string
	for _, r := range a.exact {
		if !a.seen[r] {
			problems = append(problems, fmt.Sprintf("%s:%d: %s is not in the snapshot. The API dropped it or the row is misspelled",
				coverageFile, r.line, r.key()))
		}
	}
	for _, r := range a.rules {
		switch n := a.counted[r]; {
		case n == 0:
			problems = append(problems, fmt.Sprintf("%s:%d: the rule %s covers no operation; drop it", coverageFile, r.line, r.key()))
		case n != r.count:
			problems = append(problems, fmt.Sprintf("%s:%d: the rule %s covers %d operations and says %d; read what it covers now and correct the count",
				coverageFile, r.line, r.key(), n, r.count))
		}
	}
	return problems
}

// bindCalls is direction 2: every client call is to a published
// operation whose row says used or gated. It returns, per operation, the
// functions that call it.
func bindCalls(snap apiSnapshot, calls []clientCall, verdictOf map[string]*coverageRow) (map[string]map[string]bool, []string) {
	var problems []string
	idx := indexOperations(snap)
	bound := map[string]map[string]bool{}
	webSeen := map[string]bool{}
	for _, c := range sortedCalls(calls) {
		if c.Web {
			if _, ok := offSnapshotCalls[c.op()]; !ok {
				problems = append(problems, fmt.Sprintf("%s: %s %s is under the web root, outside the OpenAPI file; list it in offSnapshotCalls with the reason",
					c.Pos, c.Func, c.op()))
			}
			webSeen[c.op()] = true
			continue
		}
		op := idx.find(c)
		if op == nil {
			problems = append(problems, fmt.Sprintf("%s: %s calls %s, which is no operation in the snapshot", c.Pos, c.Func, c.op()))
			continue
		}
		r := verdictOf[op.key()]
		if r == nil || r.rule || (r.verdict != "used" && r.verdict != "gated") {
			v := "none"
			if r != nil {
				v = r.verdict
			}
			problems = append(problems, fmt.Sprintf("%s: %s calls %s and %s gives it %s; the row must say used or gated and name %s",
				c.Pos, c.Func, op.key(), coverageFile, v, c.Func))
			continue
		}
		if op.Lifecycle != "" || op.Deprecated {
			problems = append(problems, fmt.Sprintf("%s: %s calls %s, which GitLab marks %s; build on the stable operation",
				c.Pos, c.Func, op.key(), strings.TrimSpace(op.Lifecycle+map[bool]string{true: " deprecated"}[op.Deprecated])))
		}
		if bound[op.key()] == nil {
			bound[op.key()] = map[string]bool{}
		}
		bound[op.key()][c.Func] = true
	}
	for _, k := range slices.Sorted(maps.Keys(offSnapshotCalls)) {
		if !webSeen[k] {
			problems = append(problems, fmt.Sprintf("offSnapshotCalls lists %s and the client makes no such call; drop it", k))
		}
	}
	return bound, problems
}

// checkUsedRows holds each used or gated row to the calls: made, and
// by exactly the functions it names.
func checkUsedRows(rows []coverageRow, bound map[string]map[string]bool) (int, []string) {
	var problems []string
	used := 0
	for _, r := range rows {
		if r.verdict != "used" && r.verdict != "gated" {
			continue
		}
		used++
		named := map[string]bool{}
		for _, m := range clientMention.FindAllString(r.reason, -1) {
			named[m] = true
		}
		calling := bound[r.key()]
		if len(calling) == 0 {
			problems = append(problems, fmt.Sprintf("%s:%d: %s is %s and no Call literal in %s makes it; defer it, or write the call",
				coverageFile, r.line, r.key(), r.verdict, clientDir))
			continue
		}
		for f := range calling {
			if !named[f] {
				problems = append(problems, fmt.Sprintf("%s:%d: %s is made by %s, which the row does not name", coverageFile, r.line, r.key(), f))
			}
		}
		for f := range named {
			if !calling[f] {
				problems = append(problems, fmt.Sprintf("%s:%d: %s names %s, which does not make this call", coverageFile, r.line, r.key(), f))
			}
		}
	}
	return used, problems
}
