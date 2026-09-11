package metering_test

import (
	"context"
	"testing"
	"time"

	"github.com/satta/platform/libs/go/metering"
	"github.com/satta/platform/libs/go/metering/meteringtest"
	"github.com/satta/platform/libs/go/tenancy"
)

func meteringScopeCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, err := tenancy.WithScope(context.Background(), tenancy.Scope{TenantID: "tenant-a"})
	if err != nil {
		t.Fatalf("WithScope: %v", err)
	}
	return ctx
}

// TestMemoryEmitterContract holds the in-memory double to the same standard
// as the durable emitter.
func TestMemoryEmitterContract(t *testing.T) {
	meteringtest.RunContractTests(t, func(t *testing.T) metering.Emitter {
		return metering.NewMemoryEmitter()
	})
}

// TestMarkRatedEnforcesImmutability pins SAFETY INVARIANT 7 on the in-memory
// double: once rated, an event cannot be re-rated, and the rating engine sees
// the same refusal the durable store would give.
func TestMarkRatedEnforcesImmutability(t *testing.T) {
	m := metering.NewMemoryEmitter()
	if err := m.MarkRated("missing", meteringtest.FixedClock().Now()); err == nil {
		t.Fatal("rating an unknown event accepted")
	}

	// Emit first, then rate: rating is a transition on a recorded event.
	ctx := meteringScopeCtx(t)
	if _, err := m.Emit(ctx, metering.UsageEvent{
		EventID:       "e1",
		Scope:         tenancy.Scope{TenantID: "tenant-a"},
		ProductCode:   "sportsbook",
		MeterCode:     "bet.placed",
		Quantity:      1,
		Unit:          "count",
		OccurredAt:    time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		SourceService: "sportsbook-service",
		SubjectType:   "bet",
		SubjectID:     "bet-e1",
	}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := m.MarkRated("e1", meteringtest.FixedClock().Now()); err != nil {
		t.Fatalf("first rating: %v", err)
	}
	if err := m.MarkRated("e1", meteringtest.FixedClock().Now()); err == nil {
		t.Fatal("re-rating an already-rated event accepted — usage records are immutable once rated")
	}
	if !m.IsRated("e1") {
		t.Fatal("IsRated false after successful rating")
	}
}
