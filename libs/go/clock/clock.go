// Package clock is the platform's single time authority.
//
// SAFETY INVARIANT 12: all stored and computed time is UTC, display timezone is
// an explicit presentation-layer parameter, and no local-time arithmetic occurs
// anywhere in the system.
//
// Two things follow from that and are why this package exists rather than
// services calling time.Now directly:
//
//   - Now always returns a UTC instant. A value obtained here cannot carry a
//     local zone into a database write or a settlement calculation.
//   - Time is injected, so scheduled settlement, betting cut-offs, bonus expiry,
//     self-exclusion windows and statement periods are testable at their
//     boundaries. Those boundaries are exactly where money and compliance
//     mistakes happen, and they are untestable if the code reads the wall clock.
//
// Timezone-aware presentation belongs in the display layer: format a UTC instant
// into a named zone at the edge, and never store the result.
package clock

import (
	"sync"
	"time"
)

// Clock supplies the current instant.
type Clock interface {
	// Now returns the current time in UTC.
	Now() time.Time
}

// System is the production clock, reading the host clock and coercing to UTC.
type System struct{}

// Now implements Clock.
func (System) Now() time.Time { return time.Now().UTC() }

// NewSystem returns the system clock.
func NewSystem() Clock { return System{} }

// Fixed is a controllable clock for tests. It is safe for concurrent use.
type Fixed struct {
	mu  sync.Mutex
	now time.Time
}

// NewFixed returns a clock pinned to t, coerced to UTC.
func NewFixed(t time.Time) *Fixed {
	return &Fixed{now: t.UTC()}
}

// Now implements Clock.
func (f *Fixed) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Set moves the clock to t.
func (f *Fixed) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t.UTC()
}

// Advance moves the clock forward by d.
func (f *Fixed) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}
