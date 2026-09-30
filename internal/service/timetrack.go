package service

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/internal/gapi"
	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/v2/internal/model"
)

// Time tracking on an issue or a merge request (§7.2, §18 row 101).
// GitLab holds no witness for it, so the item is read first and the
// caller's updated_at compared (§4.6). Adding spent time is a POST GitLab
// does not deduplicate, so it is never repeated, and a lost answer is
// settled by reading the total (§4.5).

// Durations are GitLab's human form (Gitlab::TimeTrackingFormatter): 8
// hours a day, 5 days a week, 4 weeks a month, and a bare number is
// hours. GitLab's parser also takes words and ignores what it does not
// know, so "5 foo" is five hours; this server takes only numbers and the
// short units, and refuses the rest.
const (
	secondsPerMinute = 60
	secondsPerHour   = 60 * secondsPerMinute
	secondsPerDay    = 8 * secondsPerHour
	secondsPerWeek   = 5 * secondsPerDay
	secondsPerMonth  = 4 * secondsPerWeek
)

var (
	bareHours    = regexp.MustCompile(`^(\d+(?:\.\d+)?|\.\d+)$`)
	durationPart = regexp.MustCompile(`^(\d+(?:\.\d+)?|\.\d+)\s*(mo|w|d|h|m)\s*`)
	unitSeconds  = map[string]float64{"mo": secondsPerMonth, "w": secondsPerWeek, "d": secondsPerDay, "h": secondsPerHour, "m": secondsPerMinute}
)

// maxEstimate is the largest estimate GitLab stores; it clamps a larger
// one silently (TimeTrackable#time_estimate=, Gitlab::Database
// MAX_INT_VALUE).
const maxEstimate = math.MaxInt32

// maxSpent is the most spent time GitLab lets an item total, four years
// (Timelog::MAX_TOTAL_TIME_SPENT).
const maxSpent = 126230400

const durationHelp = "a duration such as 3h30m, 1w 2d or 1.5 (hours): numbers with the units mo, w, d, h or m, " +
	"where 1mo is 4w, 1w is 5d and 1d is 8h"

// parseDuration reads a duration as GitLab would, in seconds. GitLab
// stores whole seconds and drops the fraction.
func parseDuration(name, raw string) (int64, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	negative := strings.HasPrefix(s, "-")
	s = strings.TrimSpace(strings.TrimPrefix(s, "-"))
	var total float64
	if bareHours.MatchString(s) {
		n, _ := strconv.ParseFloat(s, 64)
		total = n * secondsPerHour
	} else {
		if s == "" {
			return 0, gapi.Errf(gapi.ClassInvalid, "%s is empty; pass %s", name, durationHelp)
		}
		for s != "" {
			m := durationPart.FindStringSubmatch(s)
			if m == nil {
				return 0, gapi.Errf(gapi.ClassInvalid, "%s %q is not %s", name, raw, durationHelp)
			}
			n, _ := strconv.ParseFloat(m[1], 64)
			total += n * unitSeconds[m[2]]
			s = s[len(m[0]):]
		}
	}
	if total > maxEstimate {
		return 0, gapi.Errf(gapi.ClassInvalid, "%s %q is more than GitLab stores", name, raw)
	}
	seconds := int64(total)
	if negative {
		seconds = -seconds
	}
	return seconds, nil
}

// TimeTracking is track_time's request.
type TimeTracking struct {
	Project       string
	Type          string
	IID           int64
	UpdatedAt     string
	Estimate      string
	ResetEstimate bool
	AddSpent      string
	ResetSpent    bool
}

// timeStep is one time tracking request.
type timeStep struct {
	op       string // time_estimate, reset_time_estimate, add_spent_time or reset_spent_time
	duration string
	seconds  int64
}

func (st timeStep) label() string {
	if st.duration == "" {
		return st.op
	}
	return st.op + " " + st.duration
}

// what says what the step does, for a dry run's preview.
func (st timeStep) what() string {
	switch st.op {
	case "time_estimate":
		return "set the estimate to " + st.duration
	case "reset_time_estimate":
		return "reset the estimate"
	case "add_spent_time":
		if st.seconds < 0 {
			return "subtract " + strings.TrimSpace(strings.TrimPrefix(st.duration, "-")) + " of spent time"
		}
		return "add " + st.duration + " of spent time"
	}
	return "reset the spent time"
}

// steps checks the request and lists what it asks, estimate first.
func (in TimeTracking) steps() ([]timeStep, error) {
	in.Estimate, in.AddSpent = strings.TrimSpace(in.Estimate), strings.TrimSpace(in.AddSpent)
	if in.Estimate != "" && in.ResetEstimate {
		return nil, gapi.Errf(gapi.ClassInvalid, "pass estimate or reset_estimate, not both")
	}
	if in.AddSpent != "" && in.ResetSpent {
		return nil, gapi.Errf(gapi.ClassInvalid, "pass add_spent or reset_spent, not both; to start over, reset first and add in a second call")
	}
	var steps []timeStep
	switch {
	case in.Estimate != "":
		n, err := parseDuration("estimate", in.Estimate)
		if err != nil {
			return nil, err
		}
		if n < 0 {
			return nil, gapi.Errf(gapi.ClassInvalid, "estimate must not be negative; GitLab refuses one")
		}
		steps = append(steps, timeStep{op: "time_estimate", duration: in.Estimate, seconds: n})
	case in.ResetEstimate:
		steps = append(steps, timeStep{op: "reset_time_estimate"})
	}
	switch {
	case in.AddSpent != "":
		n, err := parseDuration("add_spent", in.AddSpent)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, gapi.Errf(gapi.ClassInvalid, "add_spent is zero; GitLab refuses a duration of no time")
		}
		steps = append(steps, timeStep{op: "add_spent_time", duration: in.AddSpent, seconds: n})
	case in.ResetSpent:
		steps = append(steps, timeStep{op: "reset_spent_time"})
	}
	if len(steps) == 0 {
		return nil, gapi.Errf(gapi.ClassInvalid, "nothing to change: pass estimate, reset_estimate, add_spent or reset_spent")
	}
	return steps, nil
}

// needed drops the steps that would change nothing, saying so, and
// refuses spent time GitLab would refuse. It judges from the read.
func needed(steps []timeStep, before gitlab.TimeStats) ([]timeStep, []string, error) {
	var out []timeStep
	var notes []string
	for _, st := range steps {
		skip := ""
		switch st.op {
		case "time_estimate":
			if st.seconds == before.TimeEstimate {
				skip = "The estimate is already " + human(before.HumanTimeEstimate) + ", so setting it is left out."
			}
		case "reset_time_estimate":
			if before.TimeEstimate == 0 {
				skip = "There is no estimate, so resetting it is left out."
			}
		case "add_spent_time":
			total := before.TotalTimeSpent + st.seconds
			if total < 0 {
				return nil, nil, gapi.Errf(gapi.ClassInvalid, "add_spent would take the time spent below zero: %s is spent so far; "+
					"GitLab refuses that", human(before.HumanTotalTimeSpent))
			}
			if total > maxSpent {
				return nil, nil, gapi.Errf(gapi.ClassInvalid, "add_spent would take the time spent past four years, which GitLab refuses")
			}
		case "reset_spent_time":
			if before.TotalTimeSpent == 0 {
				skip = "No time is spent, so resetting it is left out."
			}
		}
		if skip != "" {
			notes = append(notes, skip)
			continue
		}
		out = append(out, st)
	}
	return out, notes, nil
}

// human is one of GitLab's human durations, which it leaves null at zero.
func human(h *string) string {
	if h == nil || *h == "" {
		return "none"
	}
	return *h
}

func timeStats(t gitlab.TimeStats) model.TimeStats {
	out := model.TimeStats{TimeEstimate: t.TimeEstimate, TotalTimeSpent: t.TotalTimeSpent}
	if t.HumanTimeEstimate != nil {
		out.HumanTimeEstimate = *t.HumanTimeEstimate
	}
	if t.HumanTotalTimeSpent != nil {
		out.HumanTotalTimeSpent = *t.HumanTotalTimeSpent
	}
	return out
}

// timed is what track_time reads of an item.
type timed struct {
	stats     gitlab.TimeStats
	updatedAt time.Time
	webURL    string
}

func (s *Service) readTimed(ctx context.Context, p gapi.Project, mr bool, iid int64) (timed, error) {
	if mr {
		m, err := s.client.GetMergeRequest(ctx, p, iid)
		if err != nil {
			return timed{}, err
		}
		return timed{stats: m.TimeStats, updatedAt: m.UpdatedAt, webURL: m.WebURL}, nil
	}
	is, err := s.client.GetIssue(ctx, p, iid)
	if err != nil {
		return timed{}, err
	}
	return timed{stats: is.TimeStats, updatedAt: is.UpdatedAt, webURL: is.WebURL}, nil
}

// TrackTime sets or resets an estimate and adds or resets spent time,
// after checking the witness, and reads the result back.
func (s *Service) TrackTime(ctx context.Context, in TimeTracking) (model.TimeWrite, error) {
	steps, err := in.steps()
	if err != nil {
		return model.TimeWrite{}, err
	}
	witness, err := parseWitness(in.UpdatedAt)
	if err != nil {
		return model.TimeWrite{}, err
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.TimeWrite{}, err
	}
	mr, what := in.Type == "merge_request", "issue"
	if mr {
		what = "merge request"
	}
	before, err := s.readTimed(ctx, t.p, mr, in.IID)
	if err != nil {
		return model.TimeWrite{}, err
	}
	if err := checkWitness(witness, before.updatedAt, what); err != nil {
		return model.TimeWrite{}, err
	}
	steps, notes, err := needed(steps, before.stats)
	if err != nil {
		return model.TimeWrite{}, err
	}
	out := model.TimeWrite{Outcome: "unchanged", Write: model.Write{Target: t.ref, Notes: notes}, Type: in.Type, IID: in.IID,
		WebURL: before.webURL, Before: timeStats(before.stats), Sent: []string{}, Changed: []string{}, UpdatedAt: &before.updatedAt}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun = "dry_run", true
		if len(steps) > 0 {
			out.WouldSend = timePreview(steps)
		}
		return out, nil
	}
	if len(steps) == 0 {
		return out, nil
	}
	for _, st := range steps {
		switch st.op {
		case "time_estimate":
			_, err = s.client.SetTimeEstimate(ctx, t.p, mr, in.IID, st.duration)
		case "reset_time_estimate":
			_, err = s.client.ResetTimeEstimate(ctx, t.p, mr, in.IID)
		case "add_spent_time":
			_, err = s.client.AddSpentTime(ctx, t.p, mr, in.IID, st.duration)
		default:
			_, err = s.client.ResetSpentTime(ctx, t.p, mr, in.IID)
		}
		if err != nil {
			return model.TimeWrite{}, s.timeFailed(ctx, t.p, mr, in.IID, what, out.Sent, st, before.stats, err)
		}
		out.Sent = append(out.Sent, st.label())
	}
	after, err := s.readTimed(ctx, t.p, mr, in.IID)
	if err != nil {
		c, _ := gapi.ClassOf(err)
		return model.TimeWrite{}, gapi.Wrap(c, err, "GitLab took %s, but reading the %s back failed: read it before changing it again",
			strings.Join(out.Sent, ", then "), what)
	}
	stats := timeStats(after.stats)
	out.After, out.UpdatedAt = &stats, &after.updatedAt
	out.Changed = names(field{"time_estimate", after.stats.TimeEstimate != before.stats.TimeEstimate},
		field{"total_time_spent", after.stats.TotalTimeSpent != before.stats.TotalTimeSpent})
	if len(out.Changed) > 0 {
		out.Outcome = "updated"
	}
	return out, nil
}

func timePreview(steps []timeStep) *model.Preview {
	parts := make([]string, len(steps))
	var fields []string
	for i, st := range steps {
		parts[i] = st.what()
		if st.duration != "" && len(fields) == 0 {
			fields = []string{"duration"}
		}
	}
	return preview("POST", strings.Join(parts, ", then "), fields)
}

// timeFailed says what a failed step means: which earlier steps landed,
// what a refusal needs, and for a lost answer what a read shows. Nothing
// is sent again (§4.5).
func (s *Service) timeFailed(ctx context.Context, p gapi.Project, mr bool, iid int64, what string, sent []string, st timeStep,
	before gitlab.TimeStats, err error,
) error {
	e := gapi.AsError(err)
	landed := ""
	if len(sent) > 0 {
		landed = fmt.Sprintf("GitLab took %s first. ", strings.Join(sent, ", then "))
	}
	switch e.Class {
	case gapi.ClassForbidden:
		return gapi.Wrap(e.Class, err, "%sGitLab refused %s: tracking time needs the rights to manage the %s, the Planner role or "+
			"higher for an issue and Developer or higher for a merge request. GitLab said: %s", landed, st.op, what, e.Message)
	case gapi.ClassAmbiguousOutcome:
		now, readErr := s.readTimed(ctx, p, mr, iid)
		if readErr != nil {
			return gapi.Wrap(e.Class, err, "%sGitLab did not confirm %s, and reading to find out failed too, so it is unknown: "+
				"read the %s before doing anything, and do not repeat the call", landed, st.label(), what)
		}
		return gapi.Wrap(e.Class, err, "%sGitLab did not confirm %s, and it was not repeated. %s", landed, st.label(),
			settleTime(st, before, now.stats))
	}
	if landed == "" {
		return err
	}
	return gapi.Wrap(e.Class, err, "%sThen %s failed: %s", landed, st.op, e.Message)
}

// settleTime reads a lost spent-time write from the total it left. A
// total that moved by exactly the amount most likely took it; one that
// did not move most likely did not, though a slow request may still land.
func settleTime(st timeStep, before, now gitlab.TimeStats) string {
	switch {
	case st.op == "reset_spent_time" && now.TotalTimeSpent == 0:
		return "A read shows no time spent now, so it is reset either way. Do not repeat the call"
	case st.op == "add_spent_time" && now.TotalTimeSpent == before.TotalTimeSpent+st.seconds:
		return fmt.Sprintf("A read shows the time spent went from %s to %s, so it most likely landed. Do not repeat the call",
			human(before.HumanTotalTimeSpent), human(now.HumanTotalTimeSpent))
	case now.TotalTimeSpent == before.TotalTimeSpent:
		return fmt.Sprintf("A read shows the time spent still at %s, so it most likely did not land. Read it again before "+
			"calling again, in case the request is still on its way", human(now.HumanTotalTimeSpent))
	}
	return fmt.Sprintf("A read shows %s spent, which is neither the total before nor the total asked for, so someone else "+
		"changed it too and whether this landed is unknown: read the time stats before doing anything", human(now.HumanTotalTimeSpent))
}
