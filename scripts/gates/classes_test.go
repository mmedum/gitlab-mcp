package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureErrors = `package gapi

type Class string

const (
	// ClassInvalid: bad arguments. The caller fixes them.
	ClassInvalid Class = "invalid"
	ClassAuth    Class = "auth" // not signed in; the caller runs login
	// ClassPlanned: arrives later.
	ClassPlanned Class = "someday"
)

var Classes = []Class{ClassInvalid, ClassAuth, ClassPlanned}
`

const fixtureEmitter = `package gapi

func refuse() error { return Errf(ClassInvalid, "no") }

func signedOut() *Error { return &Error{Class: gapi.ClassAuth} }

const note = "[invalid] is what a bad argument says"
`

var fixtureClassFloors = classesFloors{files: 2, classes: 3}

func writeClassFixture(t *testing.T, errorsSrc, emitter string) (string, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "gapi")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, src := range map[string]string{"errors.go": errorsSrc, "emit.go": emitter} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "errors.go"), filepath.Join(root, "internal")
}

func TestTheFixtureVocabularyHolds(t *testing.T) {
	source, root := writeClassFixture(t, fixtureErrors, fixtureEmitter)
	planned := map[string]string{"someday": "arrives with the write path in a later phase"}
	report, problems, err := checkClasses(source, []string{root}, planned, fixtureClassFloors)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) > 0 {
		t.Fatalf("problems on a correct fixture:\n%s", strings.Join(problems, "\n"))
	}
	if !strings.Contains(report, "3 classes") || !strings.Contains(report, "1 planned") || !strings.Contains(report, "2 files") {
		t.Errorf("report = %s", report)
	}
}

func TestTheClassesGateFailsEveryWay(t *testing.T) {
	planned := map[string]string{"someday": "arrives with the write path in a later phase"}
	cases := []struct {
		name            string
		errors, emitter string
		planned         map[string]string
		want            string
	}{
		{name: "a class with no comment",
			errors: strings.Replace(fixtureErrors, "\t// ClassPlanned: arrives later.\n", "", 1),
			want:   "declares ClassPlanned with no comment"},
		{name: "a class listed twice",
			errors: strings.Replace(fixtureErrors, "ClassAuth, ClassPlanned}", "ClassAuth, ClassPlanned, ClassInvalid}", 1),
			want:   "Classes lists ClassInvalid 2 times"},
		{name: "a class the list forgets",
			errors: strings.Replace(fixtureErrors, "ClassAuth, ClassPlanned}", "ClassPlanned}", 1),
			want:   "ClassAuth is declared and Classes does not list it"},
		{name: "a class nothing emits and nobody planned",
			planned: map[string]string{},
			want:    `"someday" is declared and nothing emits it`},
		{name: "a planned class that is emitted",
			emitter: fixtureEmitter + "\nfunc later() error { return Errf(ClassPlanned, \"now\") }\n",
			want:    `"someday" is emitted`},
		{name: "a planned class with no real reason",
			planned: map[string]string{"someday": "later"},
			want:    "which is not a sentence"},
		{name: "a planned class that is not declared",
			planned: map[string]string{"someday": "arrives with the write path in a later phase", "gone": "arrives never, but says so at length"},
			want:    `plannedClasses lists "gone"`},
		{name: "a class spelled into a string",
			emitter: fixtureEmitter + "\nconst oops = \"[conflicted] the branch exists\"\n",
			want:    "starts with [conflicted], which is not a class"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errorsSrc, emitter, pl := fixtureErrors, fixtureEmitter, planned
			if tc.errors != "" {
				errorsSrc = tc.errors
			}
			if tc.emitter != "" {
				emitter = tc.emitter
			}
			if tc.planned != nil {
				pl = tc.planned
			}
			source, root := writeClassFixture(t, errorsSrc, emitter)
			_, problems, err := checkClasses(source, []string{root}, pl, fixtureClassFloors)
			if err != nil {
				t.Fatal(err)
			}
			if joined := strings.Join(problems, "\n"); !strings.Contains(joined, tc.want) {
				t.Errorf("want a problem containing %q, got:\n%s", tc.want, joined)
			}
		})
	}
}

func TestTheClassesFloorsFire(t *testing.T) {
	source, root := writeClassFixture(t, fixtureErrors, fixtureEmitter)
	planned := map[string]string{"someday": "arrives with the write path in a later phase"}
	_, problems, err := checkClasses(source, []string{root}, planned, classesFloors{files: 40, classes: 13})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(problems, "\n")
	for _, want := range []string{"read 2 Go files", "declares 3 classes and the floor is 13"} {
		if !strings.Contains(joined, want) {
			t.Errorf("want %q in:\n%s", want, joined)
		}
	}
}

// The real vocabulary and the real code.
func TestTheRealVocabularyHolds(t *testing.T) {
	t.Chdir("../..")
	var out strings.Builder
	if err := classes(&out, nil); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
}
