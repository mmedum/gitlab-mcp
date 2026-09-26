package gapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Defaults of the rate model (§11). gitlab.com allows 2,000
// authenticated API requests a minute per user and 60 notes a minute;
// self-managed instances ship the general throttles off, so the
// gitlab.com figures are a ceiling rather than a guess low.
const (
	DefaultRequestsPerMinute = 2000
	DefaultNotesPerMinute    = 60
	DefaultConcurrency       = 4
)

// Reading is the last rate-limit state GitLab reported. get_me shows it.
// The headers describe only the instance throttles, never an
// application limit, and are often absent (§2.13).
type Reading struct {
	// Known is false until a response carried RateLimit-Remaining.
	Known     bool
	Limit     int
	Remaining int
	Reset     time.Time
	Observed  time.Time
}

// rateModel is the per-instance budget: a token bucket, a concurrency
// cap, and a bucket per known application limit.
type rateModel struct {
	general *rate.Limiter
	notes   *rate.Limiter
	slots   chan struct{}
	now     func() time.Time
	sleep   func(ctx context.Context, d time.Duration) error

	mu        sync.Mutex
	base      rate.Limit
	baseBurst int
	restoreAt time.Time // when a lowered bucket returns to base
	pauseTill time.Time // nothing is sent before this
	last      Reading
}

func newRateModel(perMinute, notesPerMinute, concurrency int, now func() time.Time, sleep func(context.Context, time.Duration) error) *rateModel {
	if perMinute <= 0 {
		perMinute = DefaultRequestsPerMinute
	}
	if notesPerMinute <= 0 {
		notesPerMinute = DefaultNotesPerMinute
	}
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	base := rate.Limit(float64(perMinute) / 60)
	// A burst of a few seconds' worth lets one tool call's fan-out go at
	// once without letting a loop spend the minute in a second.
	burst := max(perMinute/60*5, 1)
	return &rateModel{
		general:   rate.NewLimiter(base, burst),
		notes:     rate.NewLimiter(rate.Limit(float64(notesPerMinute)/60), max(notesPerMinute/12, 1)),
		slots:     make(chan struct{}, concurrency),
		now:       now,
		sleep:     sleep,
		base:      base,
		baseBurst: burst,
	}
}

// acquire waits for the buckets and a concurrency slot. The returned
// release gives the slot back. A wait that would outlast the call's
// deadline is refused at once as rate_limited, never slept through.
func (m *rateModel) acquire(ctx context.Context, b Bucket) (func(), error) {
	m.mu.Lock()
	now := m.now()
	if !m.restoreAt.IsZero() && !now.Before(m.restoreAt) {
		m.general.SetLimit(m.base)
		m.general.SetBurst(m.baseBurst)
		m.restoreAt = time.Time{}
	}
	pause := m.pauseTill.Sub(now)
	m.mu.Unlock()

	if pause > 0 {
		if dl, ok := ctx.Deadline(); ok && now.Add(pause).After(dl) {
			return nil, Errf(ClassRateLimited,
				"GitLab asked this client to wait %s, longer than this call has left; retry after that", roundUp(pause))
		}
		if err := m.sleep(ctx, pause); err != nil {
			return nil, Wrap(ClassUnavailable, err, "the call was canceled while waiting for GitLab's rate limit")
		}
	}
	if err := waitLimiter(ctx, m.general, "the instance"); err != nil {
		return nil, err
	}
	if b == BucketNotes {
		if err := waitLimiter(ctx, m.notes, "note creation"); err != nil {
			return nil, err
		}
	}
	select {
	case m.slots <- struct{}{}:
		return func() { <-m.slots }, nil
	case <-ctx.Done():
		return nil, Wrap(ClassUnavailable, ctx.Err(), "the call was canceled while waiting for a free connection")
	}
}

func waitLimiter(ctx context.Context, l *rate.Limiter, what string) error {
	if err := l.Wait(ctx); err != nil {
		if ctx.Err() != nil {
			return Wrap(ClassUnavailable, err, "the call was canceled while waiting for the rate budget")
		}
		// Wait refuses up front when the wait would pass the deadline.
		return Wrap(ClassRateLimited, err,
			"the rate budget for %s is spent for now and would not refill before this call's deadline; retry in a minute", what)
	}
	return nil
}

// observe reads RateLimit-* from a response. They lower the bucket when
// present and are never trusted to be present (§11).
func (m *rateModel) observe(h http.Header) {
	remaining, err := strconv.Atoi(strings.TrimSpace(h.Get("RateLimit-Remaining")))
	if err != nil {
		return
	}
	now := m.now()
	reading := Reading{Known: true, Remaining: remaining, Observed: now}
	reading.Limit, _ = strconv.Atoi(strings.TrimSpace(h.Get("RateLimit-Limit")))
	if secs, err := strconv.ParseInt(strings.TrimSpace(h.Get("RateLimit-Reset")), 10, 64); err == nil && secs > 0 {
		reading.Reset = time.Unix(secs, 0)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.last = reading
	window := reading.Reset.Sub(now)
	if reading.Reset.IsZero() || window <= 0 {
		return
	}
	if remaining <= 0 {
		m.pauseTill = later(m.pauseTill, reading.Reset)
		return
	}
	lowered := rate.Limit(float64(remaining) / window.Seconds())
	if lowered < m.base {
		m.general.SetLimit(lowered)
		m.general.SetBurst(max(min(m.baseBurst, remaining), 1))
		m.restoreAt = reading.Reset
	}
}

// pause holds every call until d has passed, after a 429.
func (m *rateModel) pause(d time.Duration) {
	if d <= 0 {
		return
	}
	m.mu.Lock()
	m.pauseTill = later(m.pauseTill, m.now().Add(d))
	m.mu.Unlock()
}

func (m *rateModel) reading() Reading {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// roundUp rounds a wait up to the second, so "retry after 1s" is never
// said of a wait of 1.4 s.
func roundUp(d time.Duration) time.Duration {
	return (d + time.Second - 1).Truncate(time.Second)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
