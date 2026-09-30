package gitlabtest

import (
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
)

// Time tracking on an issue or a merge request, as lib/api/
// time_tracking_endpoints.rb serves it at v19.4.1-ee: each write needs
// admin_issue (Planner and up) or admin_merge_request (Developer and up)
// and answers 403 without it; setting and resetting answer 200, adding
// spent time 201. A changed estimate moves updated_at; adding or
// resetting spent time does not, as the live runs saw on gitlab.com (§18
// row 101).
// GitLab's system notes for these changes are not modeled.

const (
	// maxEstimate and maxTotalSpent are MAX_INT_VALUE and
	// Timelog::MAX_TOTAL_TIME_SPENT.
	maxEstimate   = math.MaxInt32
	maxTotalSpent = 126230400
)

// serveTime routes time_stats and the four time tracking writes on one
// item; it reports whether it answered.
func (s *Server) serveTime(w http.ResponseWriter, r *http.Request, p *project, ts *gitlab.TimeStats, updated *time.Time,
	mr bool, user string, rest []string,
) bool {
	if r.Method == http.MethodGet && match(rest, "time_stats") {
		writeJSON(w, http.StatusOK, ts)
		return true
	}
	if r.Method != http.MethodPost || len(rest) != 1 {
		return false
	}
	switch rest[0] {
	case "time_estimate", "reset_time_estimate", "add_spent_time", "reset_spent_time":
	default:
		return false
	}
	need := plannerAccess
	if mr {
		need = developerAccess
	}
	if s.accessLevel(p, user) < need {
		message(w, http.StatusForbidden, "403 Forbidden")
		return true
	}
	b, ok := readBody(w, r)
	if !ok {
		return true
	}
	switch rest[0] {
	case "time_estimate":
		s.setEstimate(w, b, ts, updated)
	case "reset_time_estimate":
		if ts.TimeEstimate != 0 {
			ts.TimeEstimate = 0
			bump(updated, s.opts.Now().UTC())
		}
		humanize(ts)
		writeJSON(w, http.StatusOK, ts)
	case "add_spent_time":
		s.addSpent(w, b, ts)
	default:
		ts.TotalTimeSpent = 0
		humanize(ts)
		writeJSON(w, http.StatusOK, ts)
	}
	return true
}

func (s *Server) setEstimate(w http.ResponseWriter, b body, ts *gitlab.TimeStats, updated *time.Time) {
	if !b.require(w, "duration") {
		return
	}
	raw, _ := b.str("duration")
	n, ok := parseTimeTracking(raw, true)
	if !ok || n < 0 {
		message(w, http.StatusBadRequest, "400 Bad request - Time estimate must have a valid format and be greater than or equal to zero.")
		return
	}
	n = min(n, maxEstimate)
	if n != ts.TimeEstimate {
		ts.TimeEstimate = n
		bump(updated, s.opts.Now().UTC())
	}
	humanize(ts)
	writeJSON(w, http.StatusOK, ts)
}

func (s *Server) addSpent(w http.ResponseWriter, b body, ts *gitlab.TimeStats) {
	if !b.require(w, "duration") {
		return
	}
	raw, _ := b.str("duration")
	n, ok := parseTimeTracking(raw, false)
	switch {
	case !ok:
		// A duration GitLab cannot read is nil, and the timelog made of it
		// fails its presence validation.
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": map[string]any{"timelogs.time_spent": []string{"can't be blank"}}})
		return
	case n < 0 && -n > ts.TotalTimeSpent:
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": map[string]any{"base": []string{"Time to subtract exceeds the total time spent"}}})
		return
	case ts.TotalTimeSpent+n > maxTotalSpent:
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": map[string]any{"base": []string{"Total time spent cannot exceed 4 years."}}})
		return
	}
	ts.TotalTimeSpent += n
	humanize(ts)
	writeJSON(w, http.StatusCreated, ts)
}

// accessLevel is a user's access to a project, groups above included.
func (s *Server) accessLevel(p *project, user string) int {
	for _, m := range s.members(p, "") {
		if m.Username == user {
			return m.AccessLevel
		}
	}
	return 0
}

// ------------------------------------------------------------ durations

// GitLab's time tracking day and month (Gitlab::TimeTrackingFormatter):
// 8 hours a day, 20 days a month, and a week of a quarter month.
const (
	ttHour  = 3600
	ttDay   = 8 * ttHour
	ttWeek  = 5 * ttDay
	ttMonth = 20 * ttDay
	ttYear  = 31557600
)

var (
	ttChrono = regexp.MustCompile(`^[0-9]+:[0-9]+(:[0-9]+){0,4}(\.[0-9]*)?$`)
	ttNumber = regexp.MustCompile(`[0-9]*\.?[0-9]+`)
	ttUnits  = map[string]string{
		"seconds": "seconds", "second": "seconds", "secs": "seconds", "sec": "seconds", "s": "seconds",
		"minutes": "minutes", "minute": "minutes", "mins": "minutes", "min": "minutes", "m": "minutes",
		"hours": "hours", "hour": "hours", "hrs": "hours", "hr": "hours", "h": "hours",
		"days": "days", "day": "days", "dy": "days", "d": "days",
		"weeks": "weeks", "week": "weeks", "wks": "weeks", "wk": "weeks", "w": "weeks",
		"months": "months", "mo": "months", "mos": "months", "month": "months",
		"years": "years", "year": "years", "yrs": "years", "yr": "years", "y": "years",
	}
	ttSeconds = map[string]float64{"years": ttYear, "months": ttMonth, "weeks": ttWeek, "days": ttDay, "hours": ttHour,
		"minutes": 60, "seconds": 1}
)

// parseTimeTracking is Gitlab::TimeTrackingFormatter.parse over
// ChronicDuration.parse, less the numerizing of words: a leading minus
// negates, unknown words are dropped, a number with no unit is hours,
// and 0 is no duration unless keepZero. Fractions of a second are
// dropped, as the integer column does.
func parseTimeTracking(raw string, keepZero bool) (int64, bool) {
	negative := strings.HasPrefix(raw, "-")
	s := strings.ToLower(strings.TrimPrefix(raw, "-"))
	if compact := strings.ReplaceAll(s, " ", ""); ttChrono.MatchString(compact) {
		units := []string{"seconds", "minutes", "hours", "days", "months", "years"}
		parts := strings.Split(compact, ":")
		var words []string
		for i := range parts {
			words = append([]string{parts[len(parts)-1-i] + " " + units[i]}, words...)
		}
		s = strings.Join(words, " ")
	}
	s = ttNumber.ReplaceAllStringFunc(s, func(n string) string { return " " + n + " " })
	var words []string
	for _, word := range strings.Fields(s) {
		if ttNumber.MatchString(word) {
			words = append(words, word)
		} else if u, ok := ttUnits[strings.Trim(word, ",")]; ok {
			words = append(words, u)
		}
	}
	if len(words) > 0 && ttUnits[words[0]] != "" {
		words = append([]string{"1"}, words...)
	}
	var total float64
	for i, word := range words {
		if !ttNumber.MatchString(word) {
			continue
		}
		n, err := strconv.ParseFloat(word, 64)
		if err != nil {
			continue
		}
		unit := "hours"
		if i+1 < len(words) {
			unit = words[i+1]
		}
		total += n * ttSeconds[unit]
	}
	if total == 0 && !keepZero {
		return 0, false
	}
	n := int64(total)
	if negative {
		n = -n
	}
	return n, true
}

// humanize sets the human forms, ChronicDuration.output in :short with
// weeks and limit_to_hours, as gitlab.com formats them (a 1d 2h estimate
// reads back 10h, seen live): null at zero.
func humanize(ts *gitlab.TimeStats) {
	ts.HumanTimeEstimate = humanDuration(ts.TimeEstimate)
	ts.HumanTotalTimeSpent = humanDuration(ts.TotalTimeSpent)
}

func humanDuration(seconds int64) *string {
	if seconds < 0 {
		h := humanDuration(-seconds)
		if h == nil {
			return nil
		}
		neg := "-" + *h
		return &neg
	}
	var years, months, days, hours, minutes int64
	switch {
	case seconds >= ttYear && seconds%ttYear < seconds%ttMonth:
		years, seconds = seconds/ttYear, seconds%ttYear
		months, seconds = seconds/ttMonth, seconds%ttMonth
		days, seconds = seconds/ttDay, seconds%ttDay
		hours, seconds = seconds/ttHour, seconds%ttHour
		minutes, seconds = seconds/60, seconds%60
	case seconds >= 60:
		minutes, seconds = seconds/60, seconds%60
		hours, minutes = minutes/60, minutes%60
	}
	var parts []string
	for _, u := range []struct {
		n    int64
		unit string
	}{{years, "y"}, {months, "mo"}, {days, "d"}, {hours, "h"}, {minutes, "m"}, {seconds, "s"}} {
		if u.n != 0 {
			parts = append(parts, strconv.FormatInt(u.n, 10)+u.unit)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	out := strings.Join(parts, " ")
	return &out
}
