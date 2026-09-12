package schedule

import (
	"encoding/json"
	"fmt"
	"time"
)

// Calendar is a UTC-day exclusion set, loaded from policy (Task 4's
// "calendar-aware windows": maintenance windows that must not fire on a
// market's public holiday, promotions that must). Days are date-only (Y-M-D
// at UTC midnight) — the same reason schedules store no local times: a
// holiday is a day, not an offset.
type Calendar struct {
	name    string
	exclude map[civil]struct{}
}

type civil struct{ y, m, d int }

// ParseCalendar decodes policy JSON (["2026-12-25", ...]) under a name.
func ParseCalendar(name, raw string) (*Calendar, error) {
	var days []string
	if err := json.Unmarshal([]byte(raw), &days); err != nil {
		return nil, fmt.Errorf("%w: calendar %q: %w", ErrInvalidSpec, name, err)
	}
	c := &Calendar{name: name, exclude: map[civil]struct{}{}}
	for _, s := range days {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return nil, fmt.Errorf("%w: calendar %q day %q", ErrInvalidSpec, name, s)
		}
		c.exclude[civil{t.Year(), int(t.Month()), t.Day()}] = struct{}{}
	}
	return c, nil
}

// Allows reports whether an instant falls on a non-excluded day.
func (c *Calendar) Allows(t time.Time) bool {
	tt := t.UTC()
	_, bad := c.exclude[civil{tt.Year(), int(tt.Month()), tt.Day()}]
	return !bad
}

// Name returns the calendar's label (for logs and audit details).
func (c *Calendar) Name() string { return c.name }
