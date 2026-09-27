package main

import (
	"strings"
	"testing"
)

// A surface with writes, as phase 2 will have it: a body routed through
// the guard, a title and a project listed plain, a bool that is no
// string, and a Ship tool.
const writeSurface = `{"server":"gitlab-mcp","tools":[
 {"name":"get_issue","description":"Reads one issue.","annotations":{"readOnlyHint":true},
  "inputSchema":{"type":"object","properties":{"project":{"type":"string","description":"The project"},"sha":{"type":"string","description":"An address, not a witness"}}}},
 {"name":"create_issue","description":"Creates an issue.","annotations":{"destructiveHint":false},
  "_meta":{"gitlab-mcp/quickaction":["description"]},
  "inputSchema":{"type":"object","properties":{
   "project":{"type":"string","description":"The project"},
   "title":{"type":"string","description":"The title"},
   "description":{"type":"string","description":"The body"},
   "dry_run":{"type":"boolean","description":"Say what would be sent"}}}},
 {"name":"merge_merge_request","description":"Merges.","annotations":{"destructiveHint":true},
  "_meta":{"anthropic/requiresUserInteraction":true},
  "inputSchema":{"type":"object","properties":{
   "project":{"type":"string","description":"The project"},
   "sha":{"type":"string","description":"The head you read. A refusal is NOT a retry signal."}}}}
],"resourceTemplates":[{"uriTemplate":"gitlab://projects/{id}/issues/{iid}","name":"issue","description":"The issue"}]}`

var writePlain = map[string]string{
	"project":                 "a numeric id or a path, resolved before anything is sent",
	"create_issue.title":      "GitLab evaluates quick actions in descriptions and notes, never in a title",
	"merge_merge_request.sha": "a commit SHA, checked against the shape of one before it is sent",
}

func TestTheFixtureBodiesHold(t *testing.T) {
	report, problems := checkBodies(mustDump(t, writeSurface), writePlain, 3, 2)
	if len(problems) > 0 {
		t.Fatalf("problems on a correct fixture:\n%s", strings.Join(problems, "\n"))
	}
	if want := "3 tools, 2 of them writes, with 5 string inputs: 1 through the quick-action guard, 4 plain"; !strings.Contains(report, want) {
		t.Errorf("report = %q, want %q", report, want)
	}
}

func TestTheBodiesGateFailsEveryWay(t *testing.T) {
	cases := []struct {
		name    string
		surface string
		plain   map[string]string
		floor   int
		want    string
	}{
		{name: "a body neither guarded nor listed",
			surface: strings.Replace(writeSurface, `["description"]`, `[]`, 1),
			want:    "create_issue.description is a string input of a Write tool, neither routed"},
		{name: "a Ship tool's string",
			plain: without(writePlain, "merge_merge_request.sha"),
			want:  "merge_merge_request.sha is a string input of a Ship or Destructive tool"},
		{name: "a tool with no annotations is a write",
			surface: strings.Replace(writeSurface, `"annotations":{"readOnlyHint":true},`, ``, 1),
			want:    "get_issue.sha is a string input of a Write tool"},
		{name: "a guard on an input that does not exist",
			surface: strings.Replace(writeSurface, `["description"]`, `["description","body"]`, 1),
			want:    `create_issue declares "body" guarded and has no such input`},
		{name: "a guard on a bool",
			surface: strings.Replace(writeSurface, `["description"]`, `["description","dry_run"]`, 1),
			want:    "create_issue.dry_run is declared guarded and is not a string"},
		{name: "a guard on a read tool",
			surface: strings.Replace(writeSurface, `"annotations":{"readOnlyHint":true},`, `"annotations":{"readOnlyHint":true},"_meta":{"gitlab-mcp/quickaction":["project"]},`, 1),
			want:    "get_issue is read-only and declares [project] guarded"},
		{name: "a guard list of the wrong shape",
			surface: strings.Replace(writeSurface, `["description"]`, `"description"`, 1),
			want:    `_meta["gitlab-mcp/quickaction"] is string`},
		{name: "a plain row that excuses nothing",
			plain: with(writePlain, "update_issue.title", "GitLab evaluates quick actions in descriptions only"),
			want:  `plainInputs["update_issue.title"] excuses no string input`},
		{name: "a plain row with no real reason",
			plain: with(writePlain, "create_issue.title", "plain"),
			want:  `plainInputs["create_issue.title"] gives the reason "plain"`},
		{name: "the floor on write tools",
			floor: 3,
			want:  "the dump carries 2 write tools and the floor is 3"},
		{name: "an empty dump",
			surface: `{"server":"gitlab-mcp","tools":[]}`,
			want:    "the dump carries 0 tools and the floor is 3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			surface, plain, floor := writeSurface, writePlain, 2
			if tc.surface != "" {
				surface = tc.surface
			}
			if tc.plain != nil {
				plain = tc.plain
			}
			if tc.floor != 0 {
				floor = tc.floor
			}
			_, problems := checkBodies(mustDump(t, surface), plain, 3, floor)
			if joined := strings.Join(problems, "\n"); !strings.Contains(joined, tc.want) {
				t.Errorf("want a problem containing %q, got:\n%s", tc.want, joined)
			}
		})
	}
}

func TestTheFixtureDescriptionsHold(t *testing.T) {
	if problems := checkDescriptions(mustDump(t, writeSurface), 3); len(problems) > 0 {
		t.Fatalf("problems on a correct fixture:\n%s", strings.Join(problems, "\n"))
	}
}

func TestTheDescriptionsGateFailsEveryWay(t *testing.T) {
	cases := []struct {
		name, from, to, want string
	}{
		{"a tool with no description", `"description":"Creates an issue."`, `"description":""`, "create_issue has no description"},
		{"an input with no description", `"description":"The title"`, `"description":" "`, "create_issue.title has no description"},
		{"a resource template with no description", `"description":"The issue"`, `"description":""`,
			"resource template gitlab://projects/{id}/issues/{iid} has no description"},
		{"two IMPORTANT:", `"description":"Merges."`, `"description":"IMPORTANT: one. IMPORTANT: two."`,
			"merge_merge_request says IMPORTANT: 2 times"},
		{"a witness without the sentence", "A refusal is NOT a retry signal.", "Pass it.",
			"merge_merge_request.sha is a witness and its description does not say"},
		{"an empty dump", `"tools":[`, `"tools":[],"x":[`, "the dump carries 0 tools"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(writeSurface, tc.from) {
				t.Fatalf("fixture lacks %s", tc.from)
			}
			problems := checkDescriptions(mustDump(t, strings.Replace(writeSurface, tc.from, tc.to, 1)), 3)
			if joined := strings.Join(problems, "\n"); !strings.Contains(joined, tc.want) {
				t.Errorf("want a problem containing %q, got:\n%s", tc.want, joined)
			}
		})
	}
	// sha on a read tool is an address: the same missing sentence is fine.
	if problems := checkDescriptions(mustDump(t, writeSurface), 3); strings.Contains(strings.Join(problems, "\n"), "get_issue.sha") {
		t.Error("a read tool's sha was held to the witness rule")
	}
}

func without(m map[string]string, key string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		if k != key {
			out[k] = v
		}
	}
	return out
}

func with(m map[string]string, key, value string) map[string]string {
	out := without(m, key)
	out[key] = value
	return out
}
