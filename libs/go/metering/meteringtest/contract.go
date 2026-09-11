// Package meteringtest holds the contract test suite for metering.Emitter
// implementations — the same one-standard discipline evidencetest applies to
// the evidence store (DoD gate 2: every port with a local implementation gets
// contract tests).
package meteringtest

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/metering"
	"github.com/satta/platform/libs/go/tenancy"
)

// Factory builds a fresh, empty emitter for one test.
type Factory func(t *testing.T) metering.Emitter

// RunContractTests exercises the Emitter contract.
func RunContractTests(t *testing.T, newEmitter Factory) {
	t.Helper()

	t.Run("EmitIsIdempotentOnEventID", func(t *testing.T) {
		e := newEmitter(t)
		ctx := ctxFor(t)
		event := validEvent("emit-1")

		first, err := e.Emit(ctx, event)
		if err != nil {
			t.Fatalf("emit: %v", err)
		}
		if first.Deduplicated() {
			t.Fatal("first emit reported as duplicate")
		}
		// A lost-response retry must not double-bill.
		retry, err := e.Emit(ctx, event)
		if err != nil {
			t.Fatalf("retry: %v", err)
		}
		if !retry.Deduplicated() {
			t.Fatal("retry was not reported as duplicate — a crash-recovery retry would double-bill")
		}
	})

	t.Run("EmitBatchIsAtomic", func(t *testing.T) {
		e := newEmitter(t)
		ctx := ctxFor(t)

		good := []metering.UsageEvent{validEvent("b-1"), validEvent("b-2")}
		results, err := e.EmitBatch(ctx, good)
		if err != nil {
			t.Fatalf("batch: %v", err)
		}
		if len(results) != 2 {
			t.Fatalf("batch results = %d, want 2", len(results))
		}

		// A batch containing an invalid event commits nothing: half-recorded
		// usage is as bad as half-recorded ledger entries.
		mixed := []metering.UsageEvent{validEvent("c-1"), invalidEvent()}
		if _, err := e.EmitBatch(ctx, mixed); err == nil {
			t.Fatal("batch with invalid event accepted")
		}
		if _, err := e.Emit(ctx, validEvent("probe-after-rejected-batch")); err != nil {
			t.Fatalf("emitter unusable after rejected batch: %v", err)
		}
	})

	t.Run("RejectsInvalidEvents", func(t *testing.T) {
		e := newEmitter(t)
		ctx := ctxFor(t)

		mutations := map[string]func(*metering.UsageEvent){
			"no event id":       func(ev *metering.UsageEvent) { ev.EventID = "" },
			"no product":        func(ev *metering.UsageEvent) { ev.ProductCode = "" },
			"no meter":          func(ev *metering.UsageEvent) { ev.MeterCode = "" },
			"no unit":           func(ev *metering.UsageEvent) { ev.Unit = "" },
			"no occurred_at":    func(ev *metering.UsageEvent) { ev.OccurredAt = time.Time{} },
			"no source service": func(ev *metering.UsageEvent) { ev.SourceService = "" },
			"negative quantity": func(ev *metering.UsageEvent) { ev.Quantity = -1 },
		}
		for name, mutate := range mutations {
			t.Run(name, func(t *testing.T) {
				event := validEvent("invalid-" + strings.ReplaceAll(name, " ", "-"))
				mutate(&event)
				if _, err := e.Emit(ctx, event); err == nil {
					t.Fatalf("expected %s to be rejected", name)
				}
			})
		}
	})

	t.Run("DuplicateKeyWithinOneBatchIsRejected", func(t *testing.T) {
		e := newEmitter(t)
		ctx := ctxFor(t)
		same := []metering.UsageEvent{validEvent("dup"), validEvent("dup")}
		if _, err := e.EmitBatch(ctx, same); err == nil {
			t.Fatal("duplicate event_id within one batch accepted — ambiguous intent")
		}
	})

	t.Run("UsageEventsCarryNoMoney", func(t *testing.T) {
		// SAFETY INVARIANT 7: usage carries quantity and dimensions only.
		// The type is the enforcement; this asserts the shape so a monetary
		// field cannot be added silently.
		event := validEvent("shape")
		eventType := reflect.TypeOf(event)
		for i := 0; i < eventType.NumField(); i++ {
			name := eventType.Field(i).Name
			for _, banned := range []string{"Amount", "Money", "Price", "Cost", "Ledger", "Balance"} {
				if strings.Contains(name, banned) {
					t.Fatalf("UsageEvent has a field %q; usage events must not carry monetary fields (SAFETY INVARIANT 7)", name)
				}
			}
		}
	})

	t.Run("EmitterSurfaceHasNoMutationMethods", func(t *testing.T) {
		// The structural check, as with the evidence port: no Update, Delete
		// or Void may ever appear on the port.
		emitterType := reflect.TypeOf((*metering.Emitter)(nil)).Elem()
		for i := 0; i < emitterType.NumMethod(); i++ {
			name := emitterType.Method(i).Name
			for _, banned := range []string{"Update", "Delete", "Remove", "Void", "Purge", "Correct"} {
				if strings.Contains(name, banned) {
					t.Fatalf("metering.Emitter exposes %q; usage history is immutable (SAFETY INVARIANT 7)", name)
				}
			}
		}
	})
}

// validEvent builds one structurally valid usage event.
func validEvent(id string) metering.UsageEvent {
	return metering.UsageEvent{
		EventID:       id,
		Scope:         tenancy.Scope{TenantID: "tenant-a"},
		ProductCode:   "sportsbook",
		MeterCode:     "bet.placed",
		Quantity:      1,
		Unit:          "count",
		OccurredAt:    time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		Dimensions:    map[string]string{"channel": "web"},
		SourceService: "sportsbook-service",
		SourceVersion: "v1.0.0",
		SubjectType:   "bet",
		SubjectID:     "bet-" + id,
	}
}

func invalidEvent() metering.UsageEvent {
	event := validEvent("invalid")
	event.ProductCode = ""
	return event
}

func ctxFor(t *testing.T) context.Context {
	t.Helper()
	ctx, err := tenancy.WithScope(context.Background(), tenancy.Scope{TenantID: "tenant-a"})
	if err != nil {
		t.Fatalf("WithScope: %v", err)
	}
	return ctx
}

// FixedClock is the standard test clock for emitter suites.
func FixedClock() clock.Clock {
	return clock.NewFixed(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
}
