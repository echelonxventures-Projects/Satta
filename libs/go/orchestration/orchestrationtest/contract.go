// Package orchestrationtest holds the contract test suite every orchestration
// Runner must pass — the same one-standard discipline evidencetest applies to
// the evidence store (DoD gate 2). The saga semantics that protect the ledger
// (compensation order, exactly-once start, the refusal to end "partially
// applied") must hold identically in the in-process binding used by tests and
// in the durable engine used by production, or production inherits semantics
// no test has ever exercised.
package orchestrationtest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/satta/platform/libs/go/orchestration"
)

// Factory builds a fresh, empty runner for one test.
type Factory func(t *testing.T) orchestration.Runner

// RunContractTests exercises the Runner contract.
func RunContractTests(t *testing.T, newRunner Factory) {
	t.Helper()

	t.Run("HappyPathRecordsOutput", func(t *testing.T) {
		testHappyPath(t, newRunner(t))
	})
	t.Run("FailureCompensatesInReverseOrder", func(t *testing.T) {
		testReverseCompensation(t, newRunner(t))
	})
	t.Run("FailureWithoutCompensationIsFailedNotCompensated", func(t *testing.T) {
		testFailedNoCompensation(t, newRunner(t))
	})
	t.Run("CompensationFailureSurfacesAsBroken", func(t *testing.T) {
		testCompensationBroken(t, newRunner(t))
	})
	t.Run("StartIsIdempotentByBusinessKey", func(t *testing.T) {
		testIdempotentStart(t, newRunner(t))
	})
	t.Run("StartAfterTerminalStillReturnsOriginalRun", func(t *testing.T) {
		testIdempotentAfterTerminal(t, newRunner(t))
	})
	t.Run("StartRequiresBusinessKey", func(t *testing.T) {
		testKeyRequired(t, newRunner(t))
	})
	t.Run("StatusUnknownRunIsNotFound", func(t *testing.T) {
		testStatusNotFound(t, newRunner(t))
	})
	t.Run("CancelCompensatesAndReachesTerminal", func(t *testing.T) {
		testCancelCompensates(t, newRunner(t))
	})
	t.Run("TransientStepSucceedsWithinRetryBudget", func(t *testing.T) {
		testRetries(t, newRunner(t))
	})
	t.Run("DefinitionValidationIsEager", func(t *testing.T) {
		testDefinitionValidation(t, newRunner)
	})
}

// helpers

func waitForTerminal(t *testing.T, r orchestration.Runner, runID string) orchestration.Run {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		run, err := r.Status(context.Background(), runID)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if run.Status.Terminal() {
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s never reached a terminal state (last: %s)", runID, run.Status)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func rec(id string) orchestration.Step {
	return orchestration.Step{Name: id, Run: func(_ context.Context, in []byte) ([]byte, error) {
		return append([]byte(id+">"), in...), nil
	}}
}

func testHappyPath(t *testing.T, r orchestration.Runner) {
	def := orchestration.Definition{Name: "happy", Steps: []orchestration.Step{rec("a"), rec("b")}}
	run, created, err := r.Start(context.Background(), def, "k-1", []byte("x"))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !created {
		t.Fatal("first Start reported not-created")
	}
	done := waitForTerminal(t, r, run.ID)
	if done.Status != orchestration.StatusCompleted {
		t.Fatalf("status = %s (%s), want completed", done.Status, done.Err)
	}
	if string(done.Output) != "b>a>x" {
		t.Fatalf("output = %q, want b>a>x (steps chain outputs in order)", done.Output)
	}
}

func testReverseCompensation(t *testing.T, r orchestration.Runner) {
	var mu sync.Mutex
	var undo []string
	mark := func(name string) orchestration.Step {
		return orchestration.Step{
			Name: name,
			Run: func(_ context.Context, in []byte) ([]byte, error) {
				return append([]byte(name+">"), in...), nil
			},
			Compensate: func(_ context.Context, _ []byte) error {
				mu.Lock()
				defer mu.Unlock()
				undo = append(undo, name)
				return nil
			},
		}
	}
	def := orchestration.Definition{Name: "reverse", Steps: []orchestration.Step{
		mark("one"), mark("two"),
		{Name: "boom", Run: func(context.Context, []byte) ([]byte, error) {
			return nil, errors.New("transient ledger reject")
		}},
	}}
	run, _, err := r.Start(context.Background(), def, "k-2", []byte("x"))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	done := waitForTerminal(t, r, run.ID)
	if done.Status != orchestration.StatusCompensated {
		t.Fatalf("status = %s (%s), want compensated", done.Status, done.Err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(undo) != 2 || undo[0] != "two" || undo[1] != "one" {
		t.Fatalf("compensation order = %v, want [two one] (reverse)", undo)
	}
	if done.FailedStep != "boom" {
		t.Errorf("failed step = %q, want boom", done.FailedStep)
	}
}

func testFailedNoCompensation(t *testing.T, r orchestration.Runner) {
	def := orchestration.Definition{Name: "nocmp", Steps: []orchestration.Step{
		rec("lookup"), // no compensation: read-only step
		{Name: "boom", Run: func(context.Context, []byte) ([]byte, error) {
			return nil, errors.New("nope")
		}},
	}}
	run, _, err := r.Start(context.Background(), def, "k-3", nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	done := waitForTerminal(t, r, run.ID)
	// Nothing was undone because nothing needed undoing; the distinction
	// between Failed and Compensated is the distinction between "no damage
	// possible" and "damage repaired".
	if done.Status != orchestration.StatusFailed {
		t.Fatalf("status = %s (%s), want failed", done.Status, done.Err)
	}
}

func testCompensationBroken(t *testing.T, r orchestration.Runner) {
	def := orchestration.Definition{Name: "broken", Steps: []orchestration.Step{
		{Name: "hold", Run: rec("hold").Run, Compensate: func(context.Context, []byte) error { return nil }},
		{Name: "sticky", Run: rec("sticky").Run, Compensate: func(context.Context, []byte) error {
			return errors.New("undo itself failed")
		}},
		{Name: "boom", Run: func(context.Context, []byte) ([]byte, error) { return nil, errors.New("step 3 fails") }},
	}}
	run, _, err := r.Start(context.Background(), def, "k-4", nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	done := waitForTerminal(t, r, run.ID)
	if done.Status != orchestration.StatusCompensationBroken {
		t.Fatalf("status = %s (%s), want compensation_broken", done.Status, done.Err)
	}
	// The good compensation must still have happened despite the bad one.
	if len(done.Compensated) != 1 || done.Compensated[0] != "hold" {
		t.Errorf("compensated = %v, want [hold]", done.Compensated)
	}
}

func testIdempotentStart(t *testing.T, r orchestration.Runner) {
	gate := make(chan struct{})
	def := orchestration.Definition{Name: "idem", Steps: []orchestration.Step{
		{Name: "block", Run: func(ctx context.Context, _ []byte) ([]byte, error) {
			select {
			case <-gate:
				return []byte("ok"), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}},
	}}
	first, created, err := r.Start(context.Background(), def, "same-key", nil)
	if err != nil || !created {
		t.Fatalf("first start: %v created=%v", err, created)
	}
	second, created2, err := r.Start(context.Background(), def, "same-key", nil)
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	if created2 {
		t.Fatal("second Start with the same key reported created — exactly-once broken")
	}
	if second.ID != first.ID {
		t.Fatalf("second run %q != first %q", second.ID, first.ID)
	}
	close(gate)
	waitForTerminal(t, r, first.ID)
}

func testIdempotentAfterTerminal(t *testing.T, r orchestration.Runner) {
	def := orchestration.Definition{Name: "idem-done", Steps: []orchestration.Step{rec("a")}}
	run, _, err := r.Start(context.Background(), def, "k-5", nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForTerminal(t, r, run.ID)
	again, created, err := r.Start(context.Background(), def, "k-5", nil)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if created {
		t.Fatal("re-issue after terminal created a second run")
	}
	if again.Status != orchestration.StatusCompleted || again.ID != run.ID {
		t.Fatalf("re-issue returned %+v, want the original completed run", again)
	}
}

func testKeyRequired(t *testing.T, r orchestration.Runner) {
	def := orchestration.Definition{Name: "nokey", Steps: []orchestration.Step{rec("a")}}
	if _, _, err := r.Start(context.Background(), def, "", nil); err == nil {
		t.Fatal("Start without a business key accepted — exactly-once cannot be guaranteed")
	}
}

func testStatusNotFound(t *testing.T, r orchestration.Runner) {
	if _, err := r.Status(context.Background(), "nope/nope"); !errors.Is(err, orchestration.ErrNotFound) {
		t.Fatalf("unknown run: %v, want ErrNotFound", err)
	}
}

func testCancelCompensates(t *testing.T, r orchestration.Runner) {
	var mu sync.Mutex
	compensated := false
	applied := make(chan struct{})
	def := orchestration.Definition{Name: "cancel", Steps: []orchestration.Step{
		{Name: "apply", Run: func(_ context.Context, _ []byte) ([]byte, error) {
			close(applied)
			return nil, nil
		}, Compensate: func(context.Context, []byte) error {
			mu.Lock()
			defer mu.Unlock()
			compensated = true
			return nil
		}},
		{Name: "wait", Run: func(ctx context.Context, _ []byte) ([]byte, error) {
			<-ctx.Done() // until canceled, then the saga compensates
			return nil, ctx.Err()
		}},
	}}
	run, _, err := r.Start(context.Background(), def, "k-6", nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	// Deterministic: cancel only once the first step has completed, so the
	// point under test — mid-flight cancel compensates what already ran —
	// cannot pass by timing accident.
	<-applied
	final, err := r.Cancel(context.Background(), run.ID, "operator stopped it")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if final.Status != orchestration.StatusCanceled {
		t.Fatalf("after cancel: %s (%s), want canceled", final.Status, final.Err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !compensated {
		t.Fatal("cancel without compensation is abandonment, not cancellation")
	}
}

func testRetries(t *testing.T, r orchestration.Runner) {
	var attempts int
	def := orchestration.Definition{Name: "retry", Steps: []orchestration.Step{
		{Name: "flaky", Retries: 2, Run: func(context.Context, []byte) ([]byte, error) {
			attempts++
			if attempts < 3 {
				return nil, fmt.Errorf("attempt %d transient", attempts)
			}
			return []byte("settled"), nil
		}},
	}}
	run, _, err := r.Start(context.Background(), def, "k-7", nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	done := waitForTerminal(t, r, run.ID)
	if done.Status != orchestration.StatusCompleted {
		t.Fatalf("status = %s (%s), want completed", done.Status, done.Err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3 (first + 2 retries)", attempts)
	}
}

func testDefinitionValidation(t *testing.T, newRunner Factory) {
	// Definition.Validate is pure and binding-independent, but every Start
	// must invoke it eagerly: a saga scheduled from an invalid definition is
	// a saga whose steps may already be running before the flaw is caught.
	r := newRunner(t)
	cases := map[string]orchestration.Definition{
		"no name":          {Steps: []orchestration.Step{{Name: "a", Run: rec("a").Run}}},
		"no steps":         {Name: "x"},
		"step without Run": {Name: "x", Steps: []orchestration.Step{{Name: "a"}}},
	}
	for name, def := range cases {
		if _, _, err := r.Start(context.Background(), def, "k", nil); err == nil {
			t.Errorf("%s: Start accepted an invalid definition", name)
		}
	}
}
