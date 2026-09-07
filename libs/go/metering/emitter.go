package metering

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/tenancy"
)

// Emitter records usage events. It is the port that every billable capability
// depends on.
//
// There is no Update, Delete or Void method. Corrections are billing adjustments
// applied to rated totals, not edits to usage history (SAFETY INVARIANT 7).
type Emitter interface {
	// Emit records one usage event. Idempotent on EventID.
	Emit(ctx context.Context, event UsageEvent) (EmitResult, error)
	// EmitBatch records several events atomically.
	EmitBatch(ctx context.Context, events []UsageEvent) ([]EmitResult, error)
}

// TxEmitter records usage inside a caller-supplied transaction.
//
// This is the interface that services doing billable work should reach for. A
// billable action and its usage event must commit or fail together: if the event
// is emitted over the network after the action commits, a crash in between means
// unbilled usage, and emitting before means billing for work that never happened.
// Neither is recoverable by retry, because neither leaves a record of what was
// missed.
type TxEmitter interface {
	EmitTx(ctx context.Context, tx pgx.Tx, event UsageEvent) (EmitResult, error)
}

// Meter is a convenience wrapper that stamps provenance onto every event, so that
// call sites read as a single line and cannot forget the source fields.
type Meter struct {
	emitter Emitter
	service string
	version string
	clock   clock.Clock
}

// NewMeter returns a Meter bound to a service identity.
func NewMeter(emitter Emitter, service, version string, clk clock.Clock) *Meter {
	if clk == nil {
		clk = clock.NewSystem()
	}
	return &Meter{emitter: emitter, service: service, version: version, clock: clk}
}

// Record builds and emits a usage event.
//
// eventID must be derived from the metered action so that a retry produces the
// same identifier; see the note on UsageEvent.EventID.
func (m *Meter) Record(
	ctx context.Context,
	eventID, productCode, meterCode string,
	quantity int64,
	unit string,
	subjectType, subjectID string,
	dimensions map[string]string,
) (EmitResult, error) {
	scope, err := tenancy.RequireFromContext(ctx)
	if err != nil {
		return EmitResult{}, err
	}
	event := UsageEvent{
		EventID:       eventID,
		Scope:         scope,
		ProductCode:   productCode,
		MeterCode:     meterCode,
		Quantity:      quantity,
		Unit:          unit,
		OccurredAt:    m.clock.Now(),
		Dimensions:    dimensions,
		SourceService: m.service,
		SourceVersion: m.version,
		SubjectType:   subjectType,
		SubjectID:     subjectID,
	}
	return m.emitter.Emit(ctx, event)
}

// MemoryEmitter is an in-memory Emitter for tests and for local runs without a
// database. It mirrors the durable implementation's tenant checks and idempotency
// so that the two behave identically under test.
type MemoryEmitter struct {
	mu     sync.Mutex
	byID   map[string]UsageEvent
	order  []string
	rated  map[string]time.Time
	closed bool
}

// NewMemoryEmitter returns an empty in-memory emitter.
func NewMemoryEmitter() *MemoryEmitter {
	return &MemoryEmitter{byID: map[string]UsageEvent{}, rated: map[string]time.Time{}}
}

var _ Emitter = (*MemoryEmitter)(nil)

// Emit implements Emitter.
func (m *MemoryEmitter) Emit(ctx context.Context, event UsageEvent) (EmitResult, error) {
	results, err := m.EmitBatch(ctx, []UsageEvent{event})
	if err != nil {
		return EmitResult{}, err
	}
	return results[0], nil
}

// EmitBatch implements Emitter.
func (m *MemoryEmitter) EmitBatch(ctx context.Context, events []UsageEvent) ([]EmitResult, error) {
	if len(events) == 0 {
		return nil, fmt.Errorf("metering: EmitBatch requires at least one event")
	}
	if _, err := tenancy.RequireFromContext(ctx); err != nil {
		return nil, err
	}
	for i := range events {
		if err := events[i].Validate(); err != nil {
			return nil, fmt.Errorf("metering: event %d: %w", i, err)
		}
		if err := tenancy.CheckAccess(ctx, events[i].Scope); err != nil {
			return nil, err
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	staged := make([]UsageEvent, 0, len(events))
	results := make([]EmitResult, 0, len(events))
	for _, event := range events {
		if _, exists := m.byID[event.EventID]; exists {
			results = append(results, EmitResult{EventID: event.EventID, Outcome: OutcomeDuplicate})
			continue
		}
		for _, s := range staged {
			if s.EventID == event.EventID {
				return nil, fmt.Errorf("%w: event_id %q appears twice in one batch", ErrInvalidEvent, event.EventID)
			}
		}
		staged = append(staged, event.Normalize())
		results = append(results, EmitResult{EventID: event.EventID, Outcome: OutcomeAccepted})
	}
	for _, event := range staged {
		m.byID[event.EventID] = event
		m.order = append(m.order, event.EventID)
	}
	return results, nil
}

// Events returns the recorded events in emission order. Test helper.
func (m *MemoryEmitter) Events() []UsageEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]UsageEvent, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, m.byID[id])
	}
	return out
}

// Get returns a recorded event by ID. Test helper.
func (m *MemoryEmitter) Get(eventID string) (UsageEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	event, ok := m.byID[eventID]
	if !ok {
		return UsageEvent{}, fmt.Errorf("%w: event_id %q", ErrNotFound, eventID)
	}
	return event, nil
}

// MarkRated records that an event has been rated, after which it is immutable.
// The rating engine owns this transition; it is here so that the in-memory double
// can enforce SAFETY INVARIANT 7 the same way the database does.
func (m *MemoryEmitter) MarkRated(eventID string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byID[eventID]; !ok {
		return fmt.Errorf("%w: event_id %q", ErrNotFound, eventID)
	}
	if _, already := m.rated[eventID]; already {
		return fmt.Errorf("%w: event_id %q is already rated", ErrImmutable, eventID)
	}
	m.rated[eventID] = at.UTC()
	return nil
}

// IsRated reports whether an event has been rated.
func (m *MemoryEmitter) IsRated(eventID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.rated[eventID]
	return ok
}
