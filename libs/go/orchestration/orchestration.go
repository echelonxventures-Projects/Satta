// Package orchestration is the saga/workflow port (roadmap Task 2): every
// multi-step, compensating business process — withdrawal approval, deposit
// reconciliation, KYC review, tenant provisioning — is expressed once against
// this port and runs either durably (the Temporal adapter) or locally
// (in-process, for tests and stage-0).
//
// The port's shape is deliberately narrow, and what it refuses to expose is
// the point: there is no way to express "delete this run" or "skip this step"
// because an audit-relevant process cannot silently disappear mid-flight, and
// a failed process must end either Completed or Compensated — "partially
// applied" is the state a ledger cannot tolerate.
//
// Exactly-once start semantics are part of the contract, not a feature: a
// withdrawal retried after a crash must not pay out twice, so Start is keyed
// on a business key and re-issue returns the existing run.
package orchestration

import (
	"context"
	"errors"
	"time"
)

// Errors returned by this package and required of every adapter.
var (
	// ErrNotFound is returned when no run matches the requested ID.
	ErrNotFound = errors.New("orchestration: run not found")
	// ErrStepFailed wraps the first step error in a run's result.
	ErrStepFailed = errors.New("orchestration: step failed")
)

// StepFunc executes one step of a saga. Input and output are opaque bytes so
// the port never presumes an encoding the durable engine must share; an
// adapter that needs typed payloads passes a codec through the Definition's
// Name convention and keeps bytes at the port.
type StepFunc func(ctx context.Context, input []byte) ([]byte, error)

// CompFunc undoes one step. It receives the same input the step received —
// compensation data must exist before the forward step ran, never be
// recovered from half-written state afterward.
type CompFunc func(ctx context.Context, input []byte) error

// Step is one unit of a saga with its compensation and retry policy.
//
// Retries counts retries for transient failures on top of the first attempt.
// Compensate may be nil for genuinely read-only steps (a lookup, an
// entitlement check); a failed run that reaches a non-compensable step
// compensates the prior steps that can be undone and records the rest.
type Step struct {
	Name       string
	Run        StepFunc
	Compensate CompFunc
	Retries    int
}

// Definition is a named saga. Runners register nothing up front: a run carries
// its definition through the Start call, and the durable adapter maps Name to
// the engine's workflow type. Name must be stable across deploys — it is part
// of a run's identity in any persisted view.
type Definition struct {
	Name  string
	Steps []Step
}

// Validate checks the definition before anything is scheduled.
func (d Definition) Validate() error {
	if d.Name == "" {
		return errors.New("orchestration: definition name is required")
	}
	if len(d.Steps) == 0 {
		return errors.New("orchestration: definition requires at least one step")
	}
	for i, s := range d.Steps {
		if s.Name == "" {
			return errors.New("orchestration: every step requires a name")
		}
		if s.Run == nil {
			return errors.New("orchestration: every step requires a Run function")
		}
		if s.Retries < 0 {
			return errors.New("orchestration: step retries must be non-negative")
		}
		_ = i
	}
	return nil
}

// Status is the terminal-or-progress state of a run.
type Status string

const (
	StatusRunning     Status = "running"
	StatusCompleted   Status = "completed"
	StatusCompensated Status = "compensated" // failed and undone; not "canceled by admin"
	StatusFailed      Status = "failed"      // failed, no compensation was needed/possible
	StatusCanceling   Status = "canceling"   // cancel requested, compensation in flight
	StatusCanceled    Status = "canceled"    // cancel requested, compensation finished
	// StatusCompensationBroken is the state a human must page about: a
	// forward step failed AND at least one compensation failed too, so the
	// saga is PARTIALLY APPLIED. The abstraction only earns its existence
	// by making that state explicit rather than reporting "canceled" and
	// letting the ledger disagree.
	StatusCompensationBroken Status = "compensation_broken"
)

// Terminal reports whether a run can still move.
func (s Status) Terminal() bool {
	switch s {
	case StatusCompleted, StatusCompensated, StatusFailed, StatusCanceled, StatusCompensationBroken:
		return true
	default:
		return false
	}
}

// Run is a snapshot of one saga execution.
type Run struct {
	ID          string
	Definition  string
	Key         string // the business key Start was called with
	Status      Status
	Output      []byte // the last step's output, when Completed
	FailedStep  string
	Compensated []string // step names compensated, in compensation order
	Err         string
	StartedAt   time.Time
	EndedAt     time.Time
}

// Runner schedules and tracks sagas. Implementations: local.go (in-process),
// the Temporal adapter in orchestration/temporal.
type Runner interface {
	// Start launches a run. A second Start with the same definition name and
	// business key while (or after) the first is executing returns the
	// original run with no error and a zero-value Created=false — idempotency
	// is the contract, and it must survive the caller being killed mid-call.
	Start(ctx context.Context, def Definition, key string, input []byte) (run Run, created bool, err error)

	// Status returns the current snapshot of a run, or ErrNotFound.
	Status(ctx context.Context, runID string) (Run, error)

	// Cancel requests cancellation. A run that reaches the end of its
	// compensations reports Canceled; one already terminal is returned
	// unchanged. Cancellation is never "abandon in place" — the saga
	// abstraction only exists because partial application is unacceptable.
	Cancel(ctx context.Context, runID, reason string) (Run, error)
}
