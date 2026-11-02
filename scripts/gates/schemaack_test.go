package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
)

// plain is a released surface: two tools and a resource template.
const plain = `{"server":"gitlab-mcp","version":"v0.0.0","sdk":"v1.8.0","tools":[
	{"name":"update_issue","annotations":{"destructiveHint":false},
	 "inputSchema":{"type":"object","properties":{"iid":{"type":"integer"}}}},
	{"name":"get_issue","annotations":{"readOnlyHint":true},
	 "inputSchema":{"type":"object","properties":{"iid":{"type":"integer"}}}}],
	"resourceTemplates":[{"uriTemplate":"gitlab://projects/{id}/issues/{iid}","name":"issue"}]}`

// flipped is plain with update_issue made destructive, and renamed is
// plain with update_issue's iid input renamed, which breaks it.
var (
	flipped = strings.Replace(plain, `"destructiveHint":false`, `"destructiveHint":true`, 1)
	renamed = strings.Replace(plain, `"properties":{"iid"`, `"properties":{"issue_iid"`, 1)
)

// renameBreak is the break renamed makes of plain.
var renameBreak = []change{{"update_issue", "update_issue: input removed: iid", "input iid"}}

// schema-ack fails a pull request unless its commits name every changed
// item in a footer, and every break of a release in a BREAKING CHANGE:
// footer.
func TestSchemaAckNeedsEveryItemNamed(t *testing.T) {
	relabeled := strings.Replace(plain, `"name":"issue"`, `"name":"an issue"`, 1)
	withoutTemplate := strings.Replace(plain, `{"uriTemplate":"gitlab://projects/{id}/issues/{iid}","name":"issue"}`, ``, 1)
	reworded := strings.Replace(plain, `"sdk":"v1.8.0",`, `"sdk":"v1.8.0","instructions":{"default":"new"},`, 1)
	const (
		tmpl             = "gitlab://projects/{id}/issues/{iid}"
		wantUnnamed      = "no footer names update_issue"
		wantUnnamedBreak = "no BREAKING CHANGE: footer names update_issue"
	)
	for _, tc := range []struct {
		name, built, message, wantErr string
	}{
		{"an unchanged surface needs nothing", plain, "Tidy the README", ""},
		{"a change needs a footer", flipped, "Mark update_issue destructive", wantUnnamed},
		{"a footer naming the tool acknowledges it", flipped, "Mark it\n\nSCHEMA-CHANGE: update_issue: destructiveHint true", ""},
		{"a footer naming another tool does not", flipped, "Mark it\n\nSCHEMA-CHANGE: get_issue: destructiveHint true", wantUnnamed},
		{"a name in the description names nothing", flipped, "Mark it\n\nSCHEMA-CHANGE: get_issue: as update_issue is now", wantUnnamed},
		{"a footer with no description names its list", flipped, "Mark it\n\nSCHEMA-CHANGE: update_issue", ""},
		{"a name is a whole token", flipped, "Mark it\n\nSCHEMA-CHANGE: update_issues: destructiveHint true", wantUnnamed},
		{"a continuation line can name it", flipped, "Mark them\n\nSCHEMA-CHANGE: get_issue,\n  update_issue: destructiveHint true", ""},
		{"an empty footer says nothing", flipped, "Mark it\n\nSCHEMA-CHANGE:", wantUnnamed},
		{"the next line is not the footer's text", flipped, "Mark it\n\nSCHEMA-CHANGE:\nCo-Authored-By: update_issue", wantUnnamed},
		{"a footer must open its line", flipped, "Mark it, see SCHEMA-CHANGE: update_issue", wantUnnamed},
		{"a line of prose is not a footer", flipped, "Mark it\n\nThe gate wants a\nSCHEMA-CHANGE: update_issue footer, which this is not.", wantUnnamed},
		{"a subject is not a footer", flipped, "SCHEMA-CHANGE: update_issue", wantUnnamed},
		{"an indented block is not a footer block", flipped, "Mark it\n\nSCHEMA-CHANGE: update_issue: hints\n\n    make schema-ack", wantUnnamed},
		{"a footer above the co-author trailer", flipped, "Mark it\n\nSCHEMA-CHANGE: update_issue: hints\n\nCo-Authored-By: someone <x@example.com>", ""},
		{"a footer above three blank lines", flipped, "Mark it\n\nSCHEMA-CHANGE: update_issue: hints\n\n\n\nCo-Authored-By: someone <x@example.com>", ""},
		{"a footer beside the co-author trailer", flipped, "Mark it\n\nCo-Authored-By: someone <x@example.com>\nSCHEMA-CHANGE: update_issue: hints", ""},
		{"a resource template by its URI", relabeled, "Rename it\n\nSCHEMA-CHANGE: " + tmpl + ": renamed", ""},
		{"a resource template unnamed", relabeled, "Rename it\n\nSCHEMA-CHANGE: update_issue: renamed", "no footer names " + tmpl},
		{"an instructions change needs a footer", reworded, "Reword them", "no footer names instructions"},
		{"the instructions by their key", reworded, "Reword them\n\nSCHEMA-CHANGE: instructions: reworded", ""},
		{"a break named only as a change", renamed, "Rename the input\n\nSCHEMA-CHANGE: update_issue: iid is issue_iid", wantUnnamedBreak},
		{"BREAKING CHANGE: acknowledges a break", renamed, "Rename the input\n\nBREAKING CHANGE: update_issue: iid is issue_iid", ""},
		{"BREAKING-CHANGE: is its synonym", renamed, "Rename the input\n\nBREAKING-CHANGE: update_issue: iid is issue_iid", ""},
		{"SCHEMA-CHANGE: in any case", flipped, "Mark it\n\nSchema-Change: update_issue: destructiveHint true", ""},
		{"BREAKING CHANGE: only in uppercase", renamed, "Rename the input\n\nBreaking-Change: update_issue: iid is issue_iid", wantUnnamedBreak},
		{"a removed template needs BREAKING CHANGE:", withoutTemplate, "Drop it\n\nSCHEMA-CHANGE: " + tmpl + ": gone",
			"no BREAKING CHANGE: footer names " + tmpl},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := compareSurfaces(mustDump(t, plain), mustDump(t, tc.built))
			err := checkAck(io.Discard, c, []string{"Tidy the docs", tc.message}, "base")
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("checkAck = %v, want it to pass", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("checkAck = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

// The fix a refusal names is the footer that fixes it: BREAKING CHANGE:
// for an unnamed break, SCHEMA-CHANGE: for the rest, each naming its
// items, and a message that ends with it passes.
func TestARefusalNamesTheFooterThatFixesIt(t *testing.T) {
	both := strings.Replace(renamed, `"name":"issue"`, `"name":"an issue"`, 1)
	for _, tc := range []struct{ name, built, wantFix string }{
		{"a change", flipped, "SCHEMA-CHANGE: update_issue: what changed"},
		{"a break", renamed, "BREAKING CHANGE: update_issue: what broke"},
		{"a break and a change", both, "BREAKING CHANGE: update_issue: what broke\nSCHEMA-CHANGE: gitlab://projects/{id}/issues/{iid}: what changed"},
		{
			"a break and a change to one tool", strings.Replace(renamed, `"destructiveHint":false`, `"destructiveHint":true`, 1),
			"BREAKING CHANGE: update_issue: what broke",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := compareSurfaces(mustDump(t, plain), mustDump(t, tc.built))
			err := checkAck(io.Discard, c, []string{"Change it"}, "base")
			if err == nil {
				t.Fatal("checkAck passed with no footer")
			}
			_, fix, _ := strings.Cut(err.Error(), "\n\n")
			if fix != tc.wantFix {
				t.Errorf("the refusal's fix is %q, want %q", fix, tc.wantFix)
			}
			if err := checkAck(io.Discard, c, []string{"Change it", "Name it\n\n" + fix}, "base"); err != nil {
				t.Errorf("checkAck = %v after the fix it named", err)
			}
		})
	}
}

// A name in backticks is taken whole, so a URI with a comma in it can be
// named; split on its comma, it names nothing.
func TestAFooterCanNameAURIWithACommaInBackticks(t *testing.T) {
	const tmpl = "gitlab://projects/{id}/issues{?state,labels}"
	was := strings.Replace(plain, `"gitlab://projects/{id}/issues/{iid}"`, `"`+tmpl+`"`, 1)
	now := strings.Replace(was, `"name":"issue"`, `"name":"an issue"`, 1)
	c := compareSurfaces(mustDump(t, was), mustDump(t, now))
	if err := checkAck(io.Discard, c, []string{"Rename\n\nSCHEMA-CHANGE: `" + tmpl + "`: renamed"}, "base"); err != nil {
		t.Errorf("checkAck = %v, want the backticked URI to name the template", err)
	}
	if err := checkAck(io.Discard, c, []string{"Rename\n\nSCHEMA-CHANGE: " + tmpl + ": renamed"}, "base"); err == nil {
		t.Error("checkAck passed with the URI split on its comma, which names nothing")
	}
}

// The fix a refusal prints puts a name the footer would split or trim in
// backticks, so the footer it prints is one that passes.
func TestARefusalQuotesANameTheFooterWouldSplit(t *testing.T) {
	for _, uri := range []string{"gitlab://projects/{id}/issues{?state,labels}", "gitlab://projects/{id}."} {
		t.Run(uri, func(t *testing.T) {
			was := strings.Replace(plain, `"gitlab://projects/{id}/issues/{iid}"`, `"`+uri+`"`, 1)
			c := compareSurfaces(mustDump(t, was), mustDump(t, strings.Replace(was, `"name":"issue"`, `"name":"an issue"`, 1)))
			err := checkAck(io.Discard, c, []string{"Rename it"}, "base")
			_, fix, _ := strings.Cut(fmt.Sprint(err), "\n\n")
			if want := "SCHEMA-CHANGE: `" + uri + "`: what changed"; fix != want {
				t.Fatalf("the refusal's fix is %q, want %q", fix, want)
			}
			if err := checkAck(io.Discard, c, []string{"Rename it", "Name it\n\n" + fix}, "base"); err != nil {
				t.Errorf("checkAck = %v after the fix it named", err)
			}
		})
	}
}

// A break of the released surface needs BREAKING CHANGE:; removing a tool
// added since the release breaks no caller, so it is a plain change, and
// the report lists it as one.
func TestABreakIsABreakOfARelease(t *testing.T) {
	release := mustDump(t, plain)
	base := mustDump(t, strings.Replace(plain, `"tools":[`, `"tools":[{"name":"draft_tool"},`, 1))
	head := mustDump(t, renamed)
	c := releaseBreaksOnly(compareSurfaces(base, head), compareSurfaces(release, head).breaking)
	if !slices.Equal(c.breaking, renameBreak) {
		t.Errorf("breaking = %v, want %v", c.breaking, renameBreak)
	}
	var report strings.Builder
	footer := "Drop and rename\n\nSCHEMA-CHANGE: draft_tool: never released\nBREAKING CHANGE: update_issue: iid is issue_iid"
	if err := checkAck(&report, c, []string{footer}, "base"); err != nil {
		t.Errorf("checkAck = %v, want both acknowledged", err)
	}
	want := "BREAKING  update_issue: input removed: iid\n" +
		"added     update_issue: input added: issue_iid\n" +
		"changed   update_issue: inputSchema.properties\n" +
		"changed   tool removed: draft_tool\n" +
		"schema ack ok: the change since base is acknowledged\n"
	if report.String() != want {
		t.Errorf("the report is\n%s\nwant\n%s", report.String(), want)
	}
}

// A tool the base branch already broke since the release, in one field,
// does not make a pull request's break of another field, one no release
// shipped, a break of the release.
func TestABreakOfTheReleaseIsMatchedByItsField(t *testing.T) {
	withInputs := func(props string) schemaDump {
		return mustDump(t, strings.Replace(plain, `"properties":{"iid":{"type":"integer"}}`, `"properties":{`+props+`}`, 1))
	}
	release := withInputs(`"iid":{"type":"integer"},"note":{"type":"string"}`)
	base := withInputs(`"iid":{"type":"integer"},"draft":{"type":"string"}`)
	head := withInputs(`"iid":{"type":"integer"}`)
	c := releaseBreaksOnly(compareSurfaces(base, head), compareSurfaces(release, head).breaking)
	if len(c.breaking) != 0 {
		t.Errorf("breaking = %v, want draft, which no release shipped, as a change", c.breaking)
	}
}

// The release is the build of the newest tag the base branch reaches: not
// the working tree, not a later commit, and not an older tag a branch cut
// before the last release reaches. Before the first tag there is none,
// and nothing breaks one.
func TestReleasedBreaksReadTheBaseBranchTag(t *testing.T) {
	r := newFakeRepo(t)
	r.git("add", "-A")
	r.git("commit", "--quiet", "-m", "Start")
	r.git("branch", "before-any-release")
	head := mustDump(t, renamed)

	if breaks, err := releasedBreaks(r.root, "HEAD", head); err != nil || len(breaks) != 0 {
		t.Fatalf("releasedBreaks with no tag = %v, %v; want none", breaks, err)
	}
	r.commit(plain, "Release 1")
	r.git("tag", "v1.0.0")
	r.git("branch", "feature")
	// The next release renamed the input already, so it breaks nothing.
	r.commit(renamed, "Release 2")
	r.git("tag", "v2.0.0")
	r.git("switch", "--quiet", "feature")
	// A later commit and the working tree are not the release.
	r.commit(`{"tools":[{"name":"other"}]}`, "After")
	r.write(`{"tools":[{"name":"another"}]}`)

	if breaks, err := releasedBreaks(r.root, "main", head); err != nil || len(breaks) != 0 {
		t.Errorf("releasedBreaks against main = %v, %v; want none, as v2.0.0 shipped the rename", breaks, err)
	}
	if breaks, err := releasedBreaks(r.root, "feature", head); err != nil || !slices.Equal(breaks, renameBreak) {
		t.Errorf("releasedBreaks against feature = %v, %v; want %v, from v1.0.0", breaks, err, renameBreak)
	}
	// A branch whose history reaches no tag has no release, although the
	// repository has tags.
	if breaks, err := releasedBreaks(r.root, "before-any-release", head); err != nil || len(breaks) != 0 {
		t.Errorf("releasedBreaks against a branch that reaches no tag = %v, %v; want none", breaks, err)
	}
}

// Both sides are built from commits: the merge-base and the head. The
// checkout is neither, in CI the merge commit and here main with a change
// nobody committed, so what it would serve is not what is judged.
func TestSchemaAckBuildsBothSidesFromCommits(t *testing.T) {
	r := newFakeRepo(t)
	r.commit(dumpJSON(map[string][]string{"get_issue": {"project"}}), "Base")
	r.git("switch", "--quiet", "-c", "feature")
	r.commit(dumpJSON(map[string][]string{"get_issue": {"project"}, "get_file": {"project"}}), "Add get_file")
	r.git("switch", "--quiet", "main")
	r.write(dumpJSON(map[string][]string{"get_issue": {"project"}, "list_wiki_pages": {"project"}}))
	t.Chdir(r.root)

	var out bytes.Buffer
	err := schemaAck(&out, []string{"main", "feature"})
	if err == nil || !strings.HasSuffix(err.Error(), "\n\nSCHEMA-CHANGE: get_file: what changed") {
		t.Fatalf("schemaAck = %v, want a refusal naming get_file alone", err)
	}
	if want := "added     tool added: get_file\n"; out.String() != want {
		t.Errorf("the report is\n%s\nwant\n%s", out.String(), want)
	}
	r.git("checkout", "--", ".")
	r.git("switch", "--quiet", "feature")
	r.git("commit", "--quiet", "--allow-empty", "-m", "Acknowledge it\n\nSCHEMA-CHANGE: get_file: added")
	out.Reset()
	if err := schemaAck(&out, []string{"main", "HEAD"}); err != nil {
		t.Fatalf("acknowledged: %v\n%s", err, out.String())
	}
	if n := r.worktrees(); n != 1 {
		t.Errorf("%d worktrees left, want the checkout alone", n)
	}
}

// Through the whole gate: a tool no release shipped, removed, is a plain
// change, and an input the last tag shipped, renamed, is a break.
func TestSchemaAckAsksForBreakingChangeOnlyForAReleasedBreak(t *testing.T) {
	r := newFakeRepo(t)
	r.commit(plain, "Release")
	r.git("tag", "v1.0.0")
	r.commit(strings.Replace(plain, `"tools":[`, `"tools":[{"name":"draft_tool"},`, 1), "Add draft_tool")
	r.git("switch", "--quiet", "-c", "feature")
	r.commit(renamed, "Drop draft_tool and rename iid")
	t.Chdir(r.root)
	err := schemaAck(io.Discard, []string{"main", "HEAD"})
	_, fix, _ := strings.Cut(fmt.Sprint(err), "\n\n")
	if want := "BREAKING CHANGE: update_issue: what broke\nSCHEMA-CHANGE: draft_tool: what changed"; fix != want {
		t.Fatalf("schemaAck = %v, want the fix %q", err, want)
	}
	r.git("commit", "--quiet", "--allow-empty", "-m", "Acknowledge them\n\n"+fix)
	if err := schemaAck(io.Discard, []string{"main", "HEAD"}); err != nil {
		t.Fatalf("acknowledged: %v", err)
	}
}

// A pull request from before the server existed compares with an empty
// surface: every tool is added, and the fix the refusal prints names
// them all.
func TestSchemaAckFromBeforeTheServer(t *testing.T) {
	r := newFakeRepo(t)
	r.git("add", "-A")
	r.git("commit", "--quiet", "-m", "Initial")
	base := r.git("rev-parse", "HEAD")
	r.commit(dumpJSON(map[string][]string{"get_issue": {"project"}, "list_tree": {"project"}}), "The server")
	t.Chdir(r.root)
	err := schemaAck(io.Discard, []string{base, "HEAD"})
	_, fix, _ := strings.Cut(fmt.Sprint(err), "\n\n")
	if want := "SCHEMA-CHANGE: get_issue, list_tree: what changed"; fix != want {
		t.Fatalf("schemaAck = %v, want the fix %q", err, want)
	}
	r.git("commit", "--quiet", "--allow-empty", "-m", "Acknowledge them\n\n"+fix)
	if err := schemaAck(io.Discard, []string{base, "HEAD"}); err != nil {
		t.Fatalf("acknowledged: %v", err)
	}
}

// A gate that could not run says so and exits 2; a refusal exits 1.
func TestSchemaAckTellsARefusalFromAFailure(t *testing.T) {
	refused := checkAck(io.Discard, compareSurfaces(mustDump(t, plain), mustDump(t, flipped)), nil, "base")
	if refused == nil || exitCode(refused) != 1 || errors.As(refused, new(couldNotRun)) {
		t.Errorf("a refusal (%v) exits %d, want 1", refused, exitCode(refused))
	}
	r := newFakeRepo(t)
	r.git("commit", "--quiet", "--allow-empty", "-m", "Start")
	t.Chdir(r.root)
	// git's own words, which a locale would translate.
	t.Setenv("LC_ALL", "C")
	t.Setenv("LANGUAGE", "")
	var stdout, stderr bytes.Buffer
	code := run([]string{"schema-ack", "no-such-ref", "HEAD"}, &stdout, &stderr)
	const want = "gates schema-ack: could not run: git merge-base no-such-ref HEAD: exit status 128: " +
		"fatal: Not a valid object name no-such-ref\n"
	if code != 2 || stderr.String() != want {
		t.Errorf("schema-ack with no base exits %d with %q; want 2 and %q", code, stderr.String(), want)
	}
}
