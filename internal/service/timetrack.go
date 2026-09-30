package service

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
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

// maxEstimate is the largest estimate GitLab stores; it keeps a larger
// one as this, without refusing (TimeTrackable#time_estimate= at
// v19.4.1-ee, Gitlab::Database::MAX_INT_VALUE).
const maxEstimate = math.MaxInt32

// maxSpent is the most spent time GitLab lets an item total, four years:
// a timelog taking the total past it is refused 400
// (Timelog::MAX_TOTAL_TIME_SPENT and check_total_time_spent_is_within_range
// in app/models/timelog.rb at v19.4.1-ee).
const maxSpent = 126230400

// maxParsed bounds a parsed duration so it fits an int64; anything near
// it is past both limits above.
const maxParsed = 1 << 62

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
	seconds := int64(min(total, maxParsed))
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
	// TotalTimeSpent is the spent-time witness, the total in seconds the
	// caller read. GitLab does not move updated_at when spent time is
	// added or reset (§18 row 101), so only the total catches a repeat.
	TotalTimeSpent *int64
}

// timeStep is one time tracking request.
type timeStep struct {
	op       string // time_estimate, reset_time_estimate, add_spent_time or reset_spent_time
	duration string
	seconds  int64
	// capped marks an estimate past what GitLab stores, which it keeps
	// as maxEstimate.
	capped bool
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
	if (in.AddSpent != "" || in.ResetSpent) && in.TotalTimeSpent == nil {
		return nil, gapi.Errf(gapi.ClassInvalid, "add_spent and reset_spent need total_time_spent, the seconds spent as your latest read "+
			"gave them in time_stats: GitLab does not move updated_at when spent time is added or reset, so the total is what catches a change made "+
			"since, or this call made twice")
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
		steps = append(steps, timeStep{op: "time_estimate", duration: in.Estimate, seconds: min(n, maxEstimate), capped: n > maxEstimate})
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
				return nil, nil, gapi.Errf(gapi.ClassInvalid, "add_spent would take the time spent past four years, %d seconds, "+
					"which GitLab refuses", maxSpent)
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
		if st.capped {
			notes = append(notes, fmt.Sprintf("GitLab keeps an estimate of at most %d seconds, so it stores that.", maxEstimate))
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

// checkSpentWitness refuses spent time sent against a total that moved
// since the caller read it (§4.6). updated_at cannot: GitLab leaves it
// where it was when spent time is added or reset.
func checkSpentWitness(witness *int64, steps []timeStep, current gitlab.TimeStats, what string) error {
	spends := slices.ContainsFunc(steps, func(st timeStep) bool { return st.op == "add_spent_time" || st.op == "reset_spent_time" })
	if !spends || witness == nil || *witness == current.TotalTimeSpent {
		return nil
	}
	return gapi.Errf(gapi.ClassStale, "the time spent on the %s changed since it was read: total_time_spent is now %d (%s), not %d. "+
		"GitLab does not move updated_at when spent time is added or reset, so this may be this same call landing before. Read it again, "+
		"check the time still needs adding, and pass the new total_time_spent", what, current.TotalTimeSpent,
		human(current.HumanTotalTimeSpent), *witness)
}

// timeWrites are the client's time tracking writes for one kind of item.
type timeWrites struct {
	setEstimate, addSpent     func(context.Context, gapi.Project, int64, string) (*gitlab.TimeStats, error)
	resetEstimate, resetSpent func(context.Context, gapi.Project, int64) (*gitlab.TimeStats, error)
}

func (s *Service) timeWrites(mr bool) timeWrites {
	if mr {
		return timeWrites{setEstimate: s.client.SetMergeRequestTimeEstimate, addSpent: s.client.AddMergeRequestSpentTime,
			resetEstimate: s.client.ResetMergeRequestTimeEstimate, resetSpent: s.client.ResetMergeRequestSpentTime}
	}
	return timeWrites{setEstimate: s.client.SetIssueTimeEstimate, addSpent: s.client.AddIssueSpentTime,
		resetEstimate: s.client.ResetIssueTimeEstimate, resetSpent: s.client.ResetIssueSpentTime}
}

func (w timeWrites) send(ctx context.Context, p gapi.Project, iid int64, st timeStep) (*gitlab.TimeStats, error) {
	switch st.op {
	case "time_estimate":
		return w.setEstimate(ctx, p, iid, st.duration)
	case "reset_time_estimate":
		return w.resetEstimate(ctx, p, iid)
	case "add_spent_time":
		return w.addSpent(ctx, p, iid, st.duration)
	}
	return w.resetSpent(ctx, p, iid)
}

// timeItem is the item track_time works on.
type timeItem struct {
	p    gapi.Project
	mr   bool
	iid  int64
	what string // issue or merge request
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
	item := timeItem{p: t.p, mr: in.Type == "merge_request", iid: in.IID, what: "issue"}
	if item.mr {
		item.what = "merge request"
	}
	before, err := s.readTimed(ctx, item.p, item.mr, item.iid)
	if err != nil {
		return model.TimeWrite{}, err
	}
	steps, notes, refusal := needed(steps, before.stats)
	out := model.TimeWrite{Outcome: "unchanged", Write: model.Write{Target: t.ref, Notes: notes}, Type: in.Type, IID: in.IID,
		WebURL: before.webURL, Before: timeStats(before.stats), Sent: []string{}, Changed: []string{}, UpdatedAt: &before.updatedAt,
		TotalTimeSpent: before.stats.TotalTimeSpent}
	// Values that already hold change nothing, so a call repeated after
	// it landed reads as unchanged rather than stale, as an edit does.
	if refusal != nil || len(steps) > 0 {
		if err := checkWitness(witness, before.updatedAt, item.what); err != nil {
			return model.TimeWrite{}, err
		}
		if err := checkSpentWitness(in.TotalTimeSpent, steps, before.stats, item.what); err != nil {
			return model.TimeWrite{}, err
		}
	}
	if refusal != nil {
		return model.TimeWrite{}, refusal
	}
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
	writes := s.timeWrites(item.mr)
	var last *gitlab.TimeStats
	for i, st := range steps {
		if last, err = writes.send(ctx, item.p, item.iid, st); err != nil {
			return model.TimeWrite{}, s.timeFailed(ctx, item, out.Sent, st, steps[i+1:], before.stats, err)
		}
		out.Sent = append(out.Sent, st.label())
	}
	afterStats := *last
	if after, err := s.readTimed(ctx, item.p, item.mr, item.iid); err != nil {
		// GitLab confirmed every write, and its last answer carries the
		// item's time stats; only updated_at is unknown.
		out.UpdatedAt = nil
		out.Notes = append(out.Notes, "Reading it back failed, so after is GitLab's answer to the last request and updated_at "+
			"is unknown: read it again before the next change. Nothing needs to be sent again.")
	} else {
		afterStats, out.UpdatedAt = after.stats, &after.updatedAt
	}
	stats := timeStats(afterStats)
	out.After, out.TotalTimeSpent = &stats, afterStats.TotalTimeSpent
	out.Changed = names(field{"time_estimate", afterStats.TimeEstimate != before.stats.TimeEstimate},
		field{"total_time_spent", afterStats.TotalTimeSpent != before.stats.TotalTimeSpent})
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

func labels(steps []timeStep) string {
	out := make([]string, len(steps))
	for i, st := range steps {
		out[i] = st.label()
	}
	return strings.Join(out, ", then ")
}

// timeFailed says what a failed step means: which earlier steps landed
// and which later ones were not sent, what a refusal needs, whether the
// step itself may have landed, and, when anything may have, the
// updated_at a read now shows, so a next call is not refused [stale]
// for this one's own change. Nothing is sent again (§4.5).
func (s *Service) timeFailed(ctx context.Context, item timeItem, sent []string, st timeStep, rest []timeStep,
	before gitlab.TimeStats, err error,
) error {
	e := gapi.AsError(err)
	estimate := st.op == "time_estimate" || st.op == "reset_time_estimate"
	lost := e.Class == gapi.ClassAmbiguousOutcome
	// A repeatable estimate that GitLab never answered was retried and
	// may still have landed on any of its tries.
	maybe := estimate && e.Class == gapi.ClassUnavailable
	var b strings.Builder
	if len(sent) > 0 {
		fmt.Fprintf(&b, "GitLab took %s first. ", strings.Join(sent, ", then "))
	}
	switch {
	case e.Class == gapi.ClassForbidden:
		role := "the Planner role or higher"
		if item.mr {
			role = "the Developer role or higher"
		}
		fmt.Fprintf(&b, "GitLab refused %s: tracking time needs the rights to manage the %s, %s. GitLab said: %s",
			st.op, item.what, role, e.Message)
	case lost:
		fmt.Fprintf(&b, "GitLab did not confirm %s.", st.label())
	case maybe:
		fmt.Fprintf(&b, "%s failed and may have landed all the same: %s. Sending it again is safe, since the estimate lands "+
			"the same way twice.", st.label(), e.Message)
	case len(sent) == 0 && len(rest) == 0:
		return err
	default:
		fmt.Fprintf(&b, "%s failed: %s", st.op, e.Message)
	}
	if len(rest) > 0 {
		fmt.Fprintf(&b, " Not sent: %s.", labels(rest))
	}
	if len(sent) > 0 || lost || maybe {
		now, readErr := s.readTimed(ctx, item.p, item.mr, item.iid)
		switch {
		case readErr != nil && lost:
			b.WriteString(" Reading to find out failed too, so it is unknown: " + settledUnknown + ".")
		case readErr != nil:
			fmt.Fprintf(&b, " Read the %s again for its new updated_at and total_time_spent before calling again.", item.what)
		default:
			if lost || maybe {
				b.WriteString(" " + settleTime(st, before, now.stats) + ".")
			}
			fmt.Fprintf(&b, " updated_at is now %s and total_time_spent %d; pass them to the next call.",
				now.updatedAt.UTC().Format(time.RFC3339Nano), now.stats.TotalTimeSpent)
		}
	}
	return gapi.Wrap(e.Class, err, "%s", b.String())
}

// settleTime reads a lost time tracking write from the stats it left
// (§4.5). Spent time settles by the total: moved by exactly the amount,
// or to zero for a reset, it landed; not moved, it did not. The estimate
// settles by its value, and setting it again is safe either way.
func settleTime(st timeStep, before, now gitlab.TimeStats) string {
	was, is := human(before.HumanTotalTimeSpent), human(now.HumanTotalTimeSpent)
	switch st.op {
	case "time_estimate", "reset_time_estimate":
		want := st.seconds
		if now.TimeEstimate == want {
			return fmt.Sprintf("A read shows the estimate at %s, as asked", human(now.HumanTimeEstimate))
		}
		return fmt.Sprintf("A read shows the estimate at %s, so it did not land; setting it again is safe", human(now.HumanTimeEstimate))
	case "add_spent_time":
		if now.TotalTimeSpent == before.TotalTimeSpent+st.seconds {
			return fmt.Sprintf("A read shows the time spent went from %s to %s, so it landed. %s", was, is, settledLanded)
		}
	default:
		if now.TotalTimeSpent == 0 {
			return fmt.Sprintf("A read shows no time spent, so it is reset either way. %s", settledLanded)
		}
	}
	if now.TotalTimeSpent == before.TotalTimeSpent {
		return fmt.Sprintf("A read shows the time spent still at %s, so it did not land. %s", is, settledNotLanded)
	}
	return fmt.Sprintf("A read shows %s spent, which is neither the total before nor the one asked for, so it is unknown: %s",
		is, settledUnknown)
}
