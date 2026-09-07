// Package metering is the SDK every billable action uses to emit usage.
//
// DEFINITION OF DONE, GATE 10: every billable action emits a metered usage event
// with an idempotency key. No exceptions.
//
// SAFETY INVARIANT 7: once rated, a usage record is immutable; entitlements are
// not tenant-bypassable; and billing may not mutate the gaming ledger. The third
// clause is why UsageEvent carries a quantity and no monetary amount, and holds no
// reference to a ledger account. Pricing happens downstream in the rating engine,
// which posts only to billing accounts. A usage event that could carry money would
// eventually be used to carry it.
package metering

import (
	"errors"
	"fmt"
	"time"

	"github.com/satta/platform/libs/go/tenancy"
)

// Errors returned by this package.
var (
	// ErrInvalidEvent is returned when an event is missing required fields.
	ErrInvalidEvent = errors.New("metering: invalid usage event")
	// ErrImmutable is returned when a rated usage record would be modified.
	ErrImmutable = errors.New("metering: usage record is immutable once rated")
	// ErrNotFound is returned when no usage record matches.
	ErrNotFound = errors.New("metering: usage record not found")
)

// UsageEvent is the record of one billable action.
type UsageEvent struct {
	// EventID is globally unique and doubles as the idempotency key. Retrying an
	// emission with the same EventID is a no-op.
	//
	// It must be derived deterministically from the action being metered — for
	// example from the bet identifier and the meter code — rather than randomly.
	// A random identifier makes the retry of a lost emission indistinguishable
	// from a second occurrence, and the tenant is billed twice.
	EventID string
	// Scope attributes the usage for billing.
	Scope tenancy.Scope
	// ProductCode names the product from the product/SKU registry.
	ProductCode string
	// MeterCode names the meter within the product.
	MeterCode string
	// Quantity is an integer count in Unit. Fractional usage is expressed by
	// choosing a finer unit.
	Quantity int64
	// Unit is the unit-of-measure key, e.g. "count", "byte", "millisecond".
	Unit string
	// OccurredAt is when the metered action happened, in UTC. It determines the
	// billing period the usage falls into, so it is the action's time and not the
	// emission's time; those differ whenever an emission is retried or replayed.
	OccurredAt time.Time
	// Dimensions are billing dimensions used for rating and usage breakdowns.
	Dimensions map[string]string
	// SourceService and SourceVersion record provenance, which is what a billing
	// dispute is resolved with.
	SourceService string
	SourceVersion string
	// CorrelationID links the usage back to the originating request.
	CorrelationID string
	// SubjectType and SubjectID identify the domain object that caused the usage,
	// so that an invoice line can be reconciled against operations.
	SubjectType string
	SubjectID   string
}

// Validate checks the event.
func (e UsageEvent) Validate() error {
	if e.EventID == "" {
		return fmt.Errorf("%w: event_id is required and is the idempotency key", ErrInvalidEvent)
	}
	if err := e.Scope.RequireTenant(); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidEvent, err)
	}
	if e.ProductCode == "" {
		return fmt.Errorf("%w: product_code is required; usage must attach to a registered product", ErrInvalidEvent)
	}
	if e.MeterCode == "" {
		return fmt.Errorf("%w: meter_code is required", ErrInvalidEvent)
	}
	if e.Unit == "" {
		return fmt.Errorf("%w: unit is required", ErrInvalidEvent)
	}
	if e.Quantity < 0 {
		// A negative quantity is how a credit would be smuggled through the usage
		// pipeline. Corrections are issued as billing adjustments against the
		// rated total, which leaves both the original usage and the correction
		// visible; a negative usage event would erase the original.
		return fmt.Errorf("%w: quantity must not be negative (got %d); issue a billing adjustment instead of negative usage",
			ErrInvalidEvent, e.Quantity)
	}
	if e.OccurredAt.IsZero() {
		return fmt.Errorf("%w: occurred_at is required", ErrInvalidEvent)
	}
	if e.SourceService == "" {
		return fmt.Errorf("%w: source_service is required for billing provenance", ErrInvalidEvent)
	}
	for key, value := range e.Dimensions {
		if key == "" {
			return fmt.Errorf("%w: dimension keys must not be empty", ErrInvalidEvent)
		}
		if len(value) > 256 {
			return fmt.Errorf("%w: dimension %q value exceeds 256 characters", ErrInvalidEvent, key)
		}
	}
	return nil
}

// Normalize returns a copy with OccurredAt coerced to UTC at microsecond
// precision, matching what the store can represent.
func (e UsageEvent) Normalize() UsageEvent {
	out := e
	if !e.OccurredAt.IsZero() {
		out.OccurredAt = e.OccurredAt.UTC().Truncate(time.Microsecond)
	}
	if e.Dimensions != nil {
		out.Dimensions = make(map[string]string, len(e.Dimensions))
		for k, v := range e.Dimensions {
			out.Dimensions[k] = v
		}
	}
	return out
}

// Outcome reports what happened to an emitted event.
type Outcome string

const (
	// OutcomeAccepted means the event was durably recorded.
	OutcomeAccepted Outcome = "accepted"
	// OutcomeDuplicate means the event ID was already present and the stored
	// event stands.
	OutcomeDuplicate Outcome = "duplicate"
)

// EmitResult reports the outcome of an emission.
type EmitResult struct {
	EventID string
	Outcome Outcome
}

// Deduplicated reports whether the event had already been recorded.
func (r EmitResult) Deduplicated() bool { return r.Outcome == OutcomeDuplicate }
