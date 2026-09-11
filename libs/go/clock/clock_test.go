package clock_test

import (
	"testing"
	"time"

	"github.com/satta/platform/libs/go/clock"
)

// The clock port is small, but it is load-bearing: it is how SAFETY
// INVARIANT 12 (UTC-only, injectable time) is enforced. These tests pin the
// contract every service will depend on.

func TestSystemNowIsUTC(t *testing.T) {
	now := clock.NewSystem().Now()
	if now.Location() != time.UTC {
		t.Fatalf("System.Now() location = %v, want UTC", now.Location())
	}
}

func TestFixedClockIsInjectable(t *testing.T) {
	want := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	fixed := clock.NewFixed(want)
	if !fixed.Now().Equal(want) {
		t.Fatalf("Fixed.Now() = %v, want %v", fixed.Now(), want)
	}

	// Advance and Set let tests move time deliberately rather than sleeping.
	fixed.Advance(90 * time.Minute)
	if !fixed.Now().Equal(want.Add(90 * time.Minute)) {
		t.Fatalf("after Advance: %v, want %v", fixed.Now(), want.Add(90*time.Minute))
	}
	later := want.Add(24 * time.Hour)
	fixed.Set(later)
	if !fixed.Now().Equal(later) {
		t.Fatalf("after Set: %v, want %v", fixed.Now(), later)
	}
}

func TestFixedCoercesToUTC(t *testing.T) {
	zone := time.FixedZone("UTC+5:30", 5*3600+1800)
	zoned := time.Date(2026, 8, 1, 17, 30, 0, 0, zone)
	fixed := clock.NewFixed(zoned)
	if fixed.Now().Location() != time.UTC {
		t.Fatalf("Fixed.Now() location = %v, want UTC — a zoned injection must be coerced", fixed.Now().Location())
	}
	if !fixed.Now().Equal(zoned) {
		t.Fatal("coercion to UTC changed the instant")
	}
}
