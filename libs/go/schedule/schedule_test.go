package schedule_test

import (
	"testing"
	"time"

	"github.com/satta/platform/libs/go/schedule"
)

func utc(y int, mo time.Month, d, hh, mm int) time.Time {
	return time.Date(y, mo, d, hh, mm, 0, 0, time.UTC)
}

func TestDailyAtResolvesInUTCOrder(t *testing.T) {
	r := schedule.Recurrence{DailyAt: []string{"00:30", "12:00"}}
	n := utc(2026, 9, 11, 9, 0)
	got, err := r.Next(n, nil)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if want := utc(2026, 9, 11, 12, 0); !got.Equal(want) {
		t.Fatalf("next = %v, want %v", got, want)
	}
	// After the 12:00 slot, next is tomorrow's 00:30 — the grid advances by
	// day, it does not re-emit today.
	got, err = r.Next(utc(2026, 9, 11, 12, 0), nil)
	if err != nil {
		t.Fatalf("next2: %v", err)
	}
	if want := utc(2026, 9, 12, 0, 30); !got.Equal(want) {
		t.Fatalf("next2 = %v, want %v", got, want)
	}
}

func TestWeeklyDaysRestrict(t *testing.T) {
	// Mondays only, at 02:00 UTC (a settlement window shape).
	r := schedule.Recurrence{DailyAt: []string{"02:00"}, WeeklyDays: []schedule.Weekday{1}}
	// Friday 2026-09-11 12:00 -> next Monday 02:00.
	got, err := r.Next(utc(2026, 9, 11, 12, 0), nil)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if want := utc(2026, 9, 14, 2, 0); !got.Equal(want) {
		t.Fatalf("next = %v, want %v", got, want)
	}
}

func TestEveryGridIsStableAcrossRestarts(t *testing.T) {
	anchor := utc(2026, 9, 1, 0, 0)
	r := schedule.Recurrence{Every: time.Hour, StartAfter: anchor}
	// The grid is anchored to StartAfter: whatever `now` is, the minute it
	// fires is the same. Restarts must not reslot settlement minutes.
	for _, now := range []time.Time{utc(2026, 9, 11, 9, 7), utc(2026, 9, 11, 10, 0), utc(2026, 9, 11, 9, 59)} {
		got, err := r.Next(now, nil)
		if err != nil {
			t.Fatalf("next(%v): %v", now, err)
		}
		if got.Minute() != 0 {
			t.Fatalf("next(%v) = %v, want minute :00 (anchor grid)", now, got)
		}
	}
}

func TestCalendarVetoesDays(t *testing.T) {
	// Daily 04:00 but never on 2026-12-25 (a maintenance-window calendar).
	r := schedule.Recurrence{DailyAt: []string{"04:00"}}
	cal, err := schedule.ParseCalendar("market-holidays", `["2026-12-25"]`)
	if err != nil {
		t.Fatalf("parse calendar: %v", err)
	}
	got, err := r.Next(utc(2026, 12, 24, 5, 0), cal)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if want := utc(2026, 12, 26, 4, 0); !got.Equal(want) {
		t.Fatalf("calendar veto failed: got %v, want %v", got, want)
	}
}

func TestUntilEndsTheSchedule(t *testing.T) {
	r := schedule.Recurrence{
		DailyAt:    []string{"00:00"},
		Until:      utc(2026, 9, 12, 23, 59),
		StartAfter: utc(2026, 9, 10, 0, 0),
	}
	if _, err := r.Next(utc(2026, 9, 13, 0, 0), nil); err == nil {
		t.Fatal("fired past Until")
	}
}

func TestValidationRejectsUnusableSpecs(t *testing.T) {
	cases := map[string]schedule.Recurrence{
		"empty":                {},
		"sub-second every":     {Every: 500 * time.Millisecond},
		"bad time-of-day":      {DailyAt: []string{"25:00"}},
		"weekday out of range": {WeeklyDays: []schedule.Weekday{9}},
		"until before start":   {DailyAt: []string{"01:00"}, StartAfter: utc(2026, 9, 10, 0, 0), Until: utc(2026, 9, 9, 0, 0)},
	}
	for name, r := range cases {
		if _, err := r.Next(time.Now().UTC(), nil); err == nil {
			t.Errorf("%s: Next accepted an invalid spec", name)
		}
	}
}

func TestWeekdayTimezoneAgnostic(t *testing.T) {
	// A caller passing a zoned "now" is coerced: the schedule sees UTC, the
	// invariant guarantees it, not the caller's discipline.
	zone := time.FixedZone("UTC+5:30", 5*3600+1800)
	r := schedule.Recurrence{DailyAt: []string{"12:00"}}
	got, err := r.Next(time.Date(2026, 9, 11, 17, 30, 0, 0, zone), nil)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if want := utc(2026, 9, 12, 12, 0); !got.Equal(want) {
		t.Fatalf("zoned now mishandled: got %v want %v", got, want)
	}
}
