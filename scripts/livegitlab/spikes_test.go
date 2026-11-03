//go:build live

package main

import (
	"slices"
	"testing"
)

// Every spike has a name no step or other spike has, since a comment
// cites either as a live step, and each is asked by some run.
func TestEverySpikeHasANameOfItsOwn(t *testing.T) {
	s := sampleScratch()
	seen := map[string]bool{}
	for _, st := range slices.Concat(plan(s), shipDestructiveAndToolsets(s)) {
		seen[st.name] = true
	}
	for _, sp := range spikes {
		switch {
		case sp.name == "":
			t.Error("a spike has no name")
		case seen[sp.name]:
			t.Errorf("the spike %q shares its name with a step or another spike", sp.name)
		}
		seen[sp.name] = true
		if !sp.everyRun && sp.alone == "" {
			t.Errorf("the spike %q is asked by no run", sp.name)
		}
	}
}

// -only-spike names one spike by each value it takes, and none by a
// value no spike takes.
func TestOnlySpikePicksOneSpike(t *testing.T) {
	for _, sp := range spikes {
		if sp.alone == "" {
			continue
		}
		if got := spikesFor(sp.alone); len(got) != 1 || got[0].name != sp.name {
			t.Errorf("-only-spike %s asks %d spike(s), want %q alone", sp.alone, len(got), sp.name)
		}
	}
	if got := spikesFor("E"); len(got) != 0 {
		t.Errorf("-only-spike E asks %d spike(s), want none", len(got))
	}
	if aloneValues() != "job-log-size, merge-status, quick-actions, mcp-scope" {
		t.Errorf("-only-spike takes %q", aloneValues())
	}
}

// A full run asks the spikes marked for every run, in table order, and
// not the two asked only alone.
func TestAFullRunAsksTheEveryRunSpikes(t *testing.T) {
	var names []string
	for _, sp := range spikesFor("") {
		names = append(names, sp.name)
	}
	want := []string{
		"whether a conditional read spends the rate limit",
		"what byte_offset and byte_limit cut from a job log",
		"where a job log's size shows without reading it",
		"whether diffpos positions land on GitLab's lines",
		"whether a wildcard rule sets a branch's protected flag",
		"how soon a read finds what a create made",
		"what starts GitLab's merge status check",
		"how a comment delete holds If-Unmodified-Since",
	}
	if !slices.Equal(names, want) {
		t.Errorf("a full run asks %q, want %q", names, want)
	}
}
