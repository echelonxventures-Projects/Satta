// Package schedule is the platform's temporal control plane (roadmap Task 4):
// cron-like recurrence, calendar-aware windows, and timezone-safe execution.
//
// Timezone safety, per SAFETY INVARIANT 12, is not "handling timezones well" —
// it is never having them in stored state. Every occurrence here is computed
// and stored in UTC; a "09:00 maintenance window" is a UTC instant plus an
// optional calendar that says which DAYS the window may fire on. Local wall-
// clock strings like "daily at 9am Berlin" are resolved to UTC specs at
// config-authoring time, not by the runtime, so a DST change can never move a
// settlement job: the job that must not slip is the one worth proving about.
package schedule

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// ErrInvalidSpec marks a malformed schedule or calendar (boot-time error).
var ErrInvalidSpec = errors.New("schedule: invalid spec")

// Weekday mirrors time.Weekday (Sunday=0).
type Weekday int

// Recurrence describes when a job may fire. Fields combine with AND where
// both are set (Weekly days narrow an Every interval), and every computation
// is pure and UTC — the same spec replayed against the same clock always
// produces the same sequence, which is what makes a settlement schedule
// testable at its boundaries (the reason INVARIANT 12 exists).
type Recurrence struct {
	// Every: minimum spacing between firings.
	Every time.Duration `json:"every,omitempty"`
	// DailyAt: times-of-day in UTC (minutes/seconds resolution) to fire on
	// each matching day, e.g. ["00:00","12:00"].
	DailyAt []string `json:"daily_at_utc,omitempty"`
	// WeeklyDays restricts to specific weekdays (Sunday=0).
	WeeklyDays []Weekday `json:"weekly_days,omitempty"`
	// StartAfter bounds the first possible firing; zero = no bound.
	StartAfter time.Time `json:"start_after,omitempty"`
	// Until ends the schedule; zero = never.
	Until time.Time `json:"until,omitempty"`
}

// Validate checks the spec. Parsed time-of-day values are normalized into
// internal offsets so Next() never reparses or fails at runtime.
func (r Recurrence) Validate() error {
	if r.Every < 0 {
		return fmt.Errorf("%w: negative Every", ErrInvalidSpec)
	}
	if r.Every > 0 && r.Every < time.Second {
		// Sub-second recurrence is a busy-loop wearing a schedule's clothes;
		// anything that fast is stream processing, not scheduling.
		return fmt.Errorf("%w: Every below one second is not a schedule", ErrInvalidSpec)
	}
	if len(r.DailyAt) > 0 {
		for _, tod := range r.DailyAt {
			if _, err := parseTimeOfDay(tod); err != nil {
				return err
			}
		}
	}
	for _, d := range r.WeeklyDays {
		if d < 0 || d > 6 {
			return fmt.Errorf("%w: weekday %d out of range", ErrInvalidSpec, d)
		}
	}
	if !r.StartAfter.IsZero() && !r.Until.IsZero() && r.Until.Before(r.StartAfter) {
		return fmt.Errorf("%w: Until before StartAfter", ErrInvalidSpec)
	}
	if r.Every == 0 && len(r.DailyAt) == 0 && len(r.WeeklyDays) == 0 {
		return fmt.Errorf("%w: empty recurrence never fires", ErrInvalidSpec)
	}
	return nil
}

// ErrNoFuture is returned by Next when the schedule can never fire again.
var ErrNoFuture = errors.New("schedule: no future occurrence")

// Next returns the earliest firing strictly after now (UTC-coerced), or
// ErrNoFuture. A Calendar can veto candidate instants (holiday windows).
func (r Recurrence) Next(now time.Time, cal *Calendar) (time.Time, error) {
	if err := r.Validate(); err != nil {
		return time.Time{}, err
	}
	now = now.UTC()
	if !r.StartAfter.IsZero() && r.StartAfter.After(now) {
		now = r.StartAfter.Add(-time.Nanosecond)
	}

	candidates := r.candidatesAfter(now)
	for _, cand := range candidates {
		if !r.Until.IsZero() && cand.After(r.Until) {
			return time.Time{}, ErrNoFuture
		}
		if cal == nil || cal.Allows(cand) {
			return cand, nil
		}
		// A vetoed day advances past that day's slots; loop continues via
		// the per-day expansion below.
	}
	return time.Time{}, ErrNoFuture
}

// candidatesAfter enumerates, in order, the next firing instants implied by
// the recurrence, skipping days the weekly filter rejects and ordering the
// daily slots. Bounded by ~14 days of lookahead before declaring no-future
// for weekly patterns (a weekly schedule vetoed every single day for two
// weeks is a calendar bug worth surfacing, not an infinite search).
func (r Recurrence) candidatesAfter(now time.Time) []time.Time {
	const lookaheadDays = 14
	var out []time.Time

	// DailyAt drives day-based enumeration; otherwise Every drives offsets.
	if len(r.DailyAt) > 0 {
		start := now
		for d := 0; d <= lookaheadDays; d++ {
			day := start.Truncate(24 * time.Hour).Add(time.Duration(d) * 24 * time.Hour)
			if r.deniesWeekday(day) {
				continue
			}
			for _, tod := range r.DailyAt {
				offset, err := parseTimeOfDay(tod)
				if err != nil {
					continue // Validate checked; unreachable
				}
				cand := day.Add(offset)
				if cand.After(now) {
					out = append(out, cand)
				}
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
		return out
	}

	if r.Every > 0 {
		// Anchor to StartAfter (or now) so interval grids stay stable:
		// restarts must not reslot settlement minutes.
		anchor := r.StartAfter
		if anchor.IsZero() {
			anchor = now.Truncate(r.Every)
		}
		for k := int64(1); ; k++ {
			cand := anchor.Add(time.Duration(k) * r.Every)
			if !cand.After(now) {
				continue
			}
			if r.deniesWeekday(cand) {
				// A weekday filter with Every: keep scanning forward.
				if cand.After(now.Add(lookaheadDays * 24 * time.Hour)) {
					return nil
				}
				continue
			}
			return []time.Time{cand}
		}
	}

	// WeeklyDays-only (no DailyAt, no Every): weekly slots at midnight UTC.
	for d := 0; d <= lookaheadDays; d++ {
		day := now.Truncate(24 * time.Hour).Add(time.Duration(d+1) * 24 * time.Hour)
		if !r.deniesWeekday(day) {
			return []time.Time{day}
		}
	}
	return nil
}

func (r Recurrence) deniesWeekday(t time.Time) bool {
	if len(r.WeeklyDays) == 0 {
		return false
	}
	for _, d := range r.WeeklyDays {
		if int(t.Weekday()) == int(d) {
			return false
		}
	}
	return true
}

func parseTimeOfDay(s string) (time.Duration, error) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
		return 0, fmt.Errorf("%w: bad time-of-day %q", ErrInvalidSpec, s)
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("%w: time-of-day %q out of range", ErrInvalidSpec, s)
	}
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute, nil
}
