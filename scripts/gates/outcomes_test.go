package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const outcomeClient = `package gapi

type Call struct{ Method, Path string }
type Client struct{}
type TreeQuery struct{ Recursive bool }
func (c *Client) Do(_ any, _ Call, _ any) error { return nil }
func (c *Client) GetIssue() error { return c.Do(nil, Call{Method: "GET", Path: "issues/{}"}, nil) }
func (c *Client) CreateIssue() error { return c.Do(nil, Call{Method: "POST", Path: "issues"}, nil) }
`

const outcomeModel = `package model

type Issue struct{ Title string; Notes []string }
type Created struct {
	Outcome string
	IID     int64
	Notes   []string
	DryRun  bool
}
`

const outcomeService = `package service

type Service struct{ c *gapi.Client }
type IssueInput struct {
	Title       string
	Confidential bool
	Lock        bool
}

func (s *Service) GetIssue(in IssueInput) (model.Issue, error) {
	out := model.Issue{}
	if in.Confidential {
		out.Notes = append(out.Notes, "the issue is confidential now")
	}
	return out, s.c.GetIssue()
}

func (s *Service) CreateIssue(in IssueInput) (model.Created, error) {
	if in.Lock {
		return model.Created{}, errLocked
	}
	if err := s.c.CreateIssue(); err != nil {
		return model.Created{Outcome: "not_created"}, err
	}
	return model.Created{Outcome: "created"}, nil
}
`

func outcomePackages(t *testing.T, client, model, service string) (parsedPackage, parsedPackage, parsedPackage, []clientCall) {
	t.Helper()
	root := t.TempDir()
	dirs := map[string]string{"gapi": client, "model": model, "service": service}
	for dir, src := range dirs {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "x.go"), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	parse := func(dir string) parsedPackage {
		p, err := parsePackageDir(filepath.Join(root, dir))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	calls, problems, err := clientCalls(filepath.Join(root, "gapi"))
	if err != nil || len(problems) > 0 {
		t.Fatal(err, problems)
	}
	return parse("service"), parse("model"), parse("gapi"), calls
}

var outcomeFixtureFloors = outcomesFloors{files: 1, boolFields: 2, writes: 1}

// The fixture's one read states an outcome from the request; that is
// the finding every case but the exempt one expects to see.
func TestTheOutcomesGateFindsAnOutcomeFromTheRequest(t *testing.T) {
	service, model, client, calls := outcomePackages(t, outcomeClient, outcomeModel, outcomeService)
	_, problems := checkOutcomes(service, model, client, calls, nil, outcomeFixtureFloors)
	joined := strings.Join(problems, "\n")
	if len(problems) != 1 || !strings.Contains(joined, `tests the request's Confidential and then tells the caller "the issue is confidential now"`) {
		t.Fatalf("problems:\n%s", joined)
	}
}

func TestAnExemptBranchPasses(t *testing.T) {
	service, model, client, calls := outcomePackages(t, outcomeClient, outcomeModel, outcomeService)
	key := strings.ReplaceAll(service.paths[0], `\`, "/") + ":Confidential"
	report, problems := checkOutcomes(service, model, client, calls,
		map[string]string{key: "the sentence restates the filter the caller chose, not a state"}, outcomeFixtureFloors)
	if len(problems) > 0 {
		t.Fatalf("problems:\n%s", strings.Join(problems, "\n"))
	}
	if !strings.Contains(report, "1 writing functions each stating an outcome, 2 boolean request fields, 1 branch(es) exempt") {
		t.Errorf("report = %s", report)
	}
}

func TestTheOutcomesGateFailsEveryWay(t *testing.T) {
	cases := []struct {
		name           string
		model, service string
		exempt         map[string]string
		fl             outcomesFloors
		want           string
	}{
		{name: "a write result without its Outcome",
			service: strings.Replace(outcomeService, `model.Created{Outcome: "created"}`, `model.Created{IID: 1}`, 1),
			want:    "a model.Created is returned without its Outcome"},
		{name: "a write model with no Outcome field",
			model:   strings.Replace(outcomeModel, "Outcome string\n", "", 1),
			service: strings.NewReplacer(`{Outcome: "not_created"}`, `{}`, `{Outcome: "created"}`, `{}`).Replace(outcomeService),
			want:    "model.Created has no Outcome field"},
		{name: "a write that returns no model",
			service: strings.Replace(outcomeService, "func (s *Service) CreateIssue(in IssueInput) (model.Created, error) {",
				"func (s *Service) CreateIssue(in IssueInput) (int, error) {", 1),
			want: "writes to GitLab and returns no model type"},
		{name: "a stale exemption",
			exempt: map[string]string{"elsewhere.go:Lock": "a reason long enough to be read as one"},
			want:   `outcomeExemptions["elsewhere.go:Lock"] excuses no branch`},
		{name: "a branch that consults GitLab is fine, and one that does not after the call is not",
			service: strings.Replace(outcomeService, `out.Notes = append(out.Notes, "the issue is confidential now")`,
				`_ = s.c.GetIssue()`, 1) + "\nfunc (s *Service) Lock(in IssueInput) model.Issue {\n\tvar out model.Issue\n\tif in.Lock {\n\t\tout.Notes = []string{\"locked for everyone\"}\n\t}\n\treturn out\n}\n",
			want: `tests the request's Lock and then tells the caller "locked for everyone"`},
		{name: "the floor on boolean request fields",
			fl:   outcomesFloors{files: 1, boolFields: 9, writes: 1},
			want: "found 2 boolean request fields"},
		{name: "the floor on writes",
			fl:   outcomesFloors{files: 1, boolFields: 1, writes: 2},
			want: "found 1 service functions that write and the floor is 2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model, service := outcomeModel, outcomeService
			if tc.model != "" {
				model = tc.model
			}
			if tc.service != "" {
				service = tc.service
			}
			fl := outcomeFixtureFloors
			if tc.fl != (outcomesFloors{}) {
				fl = tc.fl
			}
			s, m, c, calls := outcomePackages(t, outcomeClient, model, service)
			_, problems := checkOutcomes(s, m, c, calls, tc.exempt, fl)
			if joined := strings.Join(problems, "\n"); !strings.Contains(joined, tc.want) {
				t.Errorf("want a problem containing %q, got:\n%s", tc.want, joined)
			}
		})
	}
}

// A dry-run branch may say what would happen, and a refusal may say
// what it refused.
func TestDryRunAndRefusalBranchesMayState(t *testing.T) {
	service := strings.Replace(outcomeService, `out.Notes = append(out.Notes, "the issue is confidential now")`,
		`return out, errConfidential`, 1) +
		"\nfunc (s *Service) Preview(in IssueInput) model.Created {\n\tif in.Lock {\n\t\treturn model.Created{Outcome: \"dry_run\", DryRun: true, Notes: []string{\"would lock it\"}}\n\t}\n\treturn model.Created{Outcome: \"none\"}\n}\n"
	s, m, c, calls := outcomePackages(t, outcomeClient, outcomeModel, service)
	if _, problems := checkOutcomes(s, m, c, calls, nil, outcomeFixtureFloors); len(problems) > 0 {
		t.Fatalf("problems:\n%s", strings.Join(problems, "\n"))
	}
}

func TestTheRealServiceStatesNoOutcomeFromTheRequest(t *testing.T) {
	t.Chdir("../..")
	var out strings.Builder
	if err := outcomes(&out, nil); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
}
