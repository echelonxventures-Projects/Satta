package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/satta/platform/libs/go/clock"
)

// LocalRunner executes sagas in-process on a goroutine per run. It is the
// stage-0 and test binding of the port: same contract as the durable engine,
// no crash recovery (a restart loses running state — the durable adapter
// exists precisely to survive that, and the contract suite encodes which
// properties each binding must satisfy).
//
// Cancellation, idempotent Start, retry accounting and reverse-order
// compensation behave exactly as the Temporal adapter, which is what makes a
// saga test written against this runner meaningful for the one that ships.
type LocalRunner struct {
	clk clock.Clock

	mu   sync.Mutex
	runs map[string]*runState
	keys map[string]string // sha256(def|key) -> runID
}

type runState struct {
	// run is guarded by LocalRunner.mu for reads/writes of the snapshot.
	run Run

	// per-step bookkeeping, guarded by LocalRunner.mu
	inputs    [][]byte // inputs[i] is what completed steps[i] received
	completed []Step

	cancel    context.CancelFunc
	cancelled bool
	done      chan struct{} // closed when execute returns
}

// NewLocalRunner returns an empty runner. Run timestamps come from the
// injected clock (SAFETY INVARIANT 12).
func NewLocalRunner(clk clock.Clock) *LocalRunner {
	if clk == nil {
		clk = clock.NewSystem()
	}
	return &LocalRunner{
		clk:  clk,
		runs: map[string]*runState{},
		keys: map[string]string{},
	}
}

var _ Runner = (*LocalRunner)(nil)

func keyOf(defName, businessKey string) string {
	sum := sha256.Sum256([]byte(defName + "\x00" + businessKey))
	return hex.EncodeToString(sum[:])
}

// Start implements Runner. Same definition + same business key returns the
// original run (created=false), while running or after terminal.
func (l *LocalRunner) Start(ctx context.Context, def Definition, key string, input []byte) (Run, bool, error) {
	if err := def.Validate(); err != nil {
		return Run{}, false, err
	}
	if key == "" {
		return Run{}, false, fmt.Errorf("orchestration: Start requires a business key (exactly-once is contractual)")
	}
	// Copy input: a saga must not observe a mutated payload mid-flight.
	in := append([]byte(nil), input...)
	// Copy steps: definitions are shared values; a caller mutating one
	// mid-run must not change a running saga.
	steps := append([]Step(nil), def.Steps...)
	def.Steps = steps

	l.mu.Lock()
	if existing, ok := l.keys[keyOf(def.Name, key)]; ok {
		run := l.runs[existing].run
		l.mu.Unlock()
		return run, false, nil
	}
	// RunID derived from definition+key, not random: the identity a caller
	// can recompute after a crash is the one worth having.
	runID := def.Name + "/" + key
	state := &runState{
		run: Run{
			ID: runID, Definition: def.Name, Key: key,
			Status: StatusRunning, StartedAt: l.clk.Now().UTC(),
		},
		done: make(chan struct{}),
	}
	l.runs[runID] = state
	l.keys[keyOf(def.Name, key)] = runID
	l.mu.Unlock()

	// The saga's life is its own: walking away from the Start call must not
	// cancel a money process mid-flight. WithoutCancel preserves the caller's
	// values (correlation, tenancy for logs) while detaching cancellation.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	l.mu.Lock()
	state.cancel = cancel
	l.mu.Unlock()

	go l.execute(runCtx, state, def, in)

	l.mu.Lock()
	defer l.mu.Unlock()
	return state.snapshot(), true, nil
}

// snapshot copies the run under the shared lock so callers never race the
// executing goroutine.
func (r *runState) snapshot() Run { return r.run }

func (l *LocalRunner) execute(ctx context.Context, state *runState, def Definition, input []byte) {
	defer close(state.done)

	var (
		cursor     = input
		failErr    error
		failedName string
	)
	for _, step := range def.Steps {
		l.mu.Lock()
		cancelled := state.cancelled
		l.mu.Unlock()
		if ctx.Err() != nil || cancelled {
			failErr, failedName = context.Canceled, step.Name
			break
		}

		out, err := l.attempt(ctx, state, step, cursor)
		if err != nil {
			failErr, failedName = err, step.Name
			break
		}

		l.mu.Lock()
		state.completed = append(state.completed, step)
		state.inputs = append(state.inputs, cursor)
		l.mu.Unlock()
		cursor = out
	}
	l.finish(ctx, state, failErr, failedName, cursor)
}

// attempt runs one step with its retry budget. Retries are immediate here —
// a test binding must be deterministic; the durable engine owns real backoff.
func (l *LocalRunner) attempt(ctx context.Context, state *runState, step Step, input []byte) ([]byte, error) {
	var last error
	for tries := 0; tries <= step.Retries+1; tries++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		l.mu.Lock()
		cancelled := state.cancelled
		l.mu.Unlock()
		if cancelled {
			return nil, context.Canceled
		}
		out, err := step.Run(ctx, input)
		if err == nil {
			return out, nil
		}
		last = err
	}
	return nil, fmt.Errorf("%w: step %q: %w", ErrStepFailed, step.Name, last)
}

// finish compensates completed steps in reverse order and records the
// terminal status. Compensation runs on a fresh context: a canceled run must
// still undo what it did — that is the entire reason compensation exists, and
// inheriting the canceled context would defeat it.
func (l *LocalRunner) finish(ctx context.Context, state *runState, failErr error, failedName string, output []byte) {
	var compensated []string
	compensationErr := false

	if failErr != nil {
		// Fresh, cancellation-free context, same rule as the durable engine:
		// a canceled run must still owe and pay its undos.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		l.mu.Lock()
		steps, inputs := state.completed, state.inputs
		cancelled := state.cancelled
		l.mu.Unlock()

		for i := len(steps) - 1; i >= 0; i-- {
			step := steps[i]
			if step.Compensate == nil {
				continue
			}
			if err := step.Compensate(cctx, inputs[i]); err == nil {
				compensated = append(compensated, step.Name)
			} else {
				// A failing compensation does not stop the others: giving up
				// halfway is worse than finishing the undo and flagging loud.
				compensationErr = true
				failErr = fmt.Errorf("%w (compensation of %q also failed: %w)", failErr, step.Name, err)
			}
		}

		l.mu.Lock()
		defer l.mu.Unlock()
		state.run.Compensated = compensated
		state.run.FailedStep = failedName
		state.run.Err = failErr.Error()
		switch {
		case compensationErr:
			state.run.Status = StatusCompensationBroken
		case cancelled:
			state.run.Status = StatusCanceled
		case len(compensated) > 0:
			state.run.Status = StatusCompensated
		default:
			state.run.Status = StatusFailed
		}
		state.run.EndedAt = l.clk.Now().UTC()
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	state.run.Compensated = nil
	state.run.Status = StatusCompleted
	state.run.Output = output
	state.run.EndedAt = l.clk.Now().UTC()
}

// Status implements Runner.
func (l *LocalRunner) Status(_ context.Context, runID string) (Run, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	state, ok := l.runs[runID]
	if !ok {
		return Run{}, fmt.Errorf("%w: %s", ErrNotFound, runID)
	}
	return state.snapshot(), nil
}

// Cancel implements Runner. Waiting for the compensation to finish before
// returning is deliberate: a caller who asked to cancel is the right party
// to learn the final state, and "Cancel() returned while the saga was still
// half-applied" would push that question onto everyone downstream.
func (l *LocalRunner) Cancel(ctx context.Context, runID, reason string) (Run, error) {
	l.mu.Lock()
	state, ok := l.runs[runID]
	if !ok {
		l.mu.Unlock()
		return Run{}, fmt.Errorf("%w: %s", ErrNotFound, runID)
	}
	if state.run.Status.Terminal() {
		run := state.snapshot()
		l.mu.Unlock()
		return run, nil
	}
	state.cancelled = true
	if state.run.Err == "" {
		state.run.Err = reason
	}
	state.run.Status = StatusCanceling
	cancel := state.cancel
	done := state.done
	l.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	<-done
	return l.Status(ctx, runID)
}
