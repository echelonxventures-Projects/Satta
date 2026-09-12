package temporal

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	"github.com/satta/platform/libs/go/orchestration"
)

// Engine-level tests for the generic saga workflow: the port contract suite
// exercises Runner semantics against a client; these exercise the workflow's
// replay/compensation logic under the engine's own determinism rules (the
// in-process testsuite), which is where an engine binding actually breaks.

type recorder struct {
	mu   sync.Mutex
	undo []string
}

func (rec *recorder) note(name string) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.undo = append(rec.undo, name)
}

func stepMarker(rec *recorder, name string, fail bool) orchestration.Step {
	return orchestration.Step{
		Name: name,
		Run: func(_ context.Context, in []byte) ([]byte, error) {
			if fail {
				return nil, errors.New("boom: " + name)
			}
			return append([]byte(name+">"), in...), nil
		},
		Compensate: func(_ context.Context, _ []byte) error {
			rec.note(name)
			return nil
		},
	}
}

func stepFlaky(name string, okAfter int) orchestration.Step {
	var mu sync.Mutex
	attempts := 0
	return orchestration.Step{
		Name:    name,
		Retries: 2,
		Run: func(_ context.Context, in []byte) ([]byte, error) {
			mu.Lock()
			attempts++
			a := attempts
			mu.Unlock()
			if a < okAfter {
				return nil, errors.New("transient")
			}
			return []byte("settled"), nil
		},
	}
}

type suiteHarness struct {
	reg *Registry
	env *testsuite.TestWorkflowEnvironment
}

func newHarness(t *testing.T, def orchestration.Definition) *suiteHarness {
	t.Helper()
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	reg := NewRegistry()
	require.NoError(t, reg.Register(def))
	reg.Bind(env)
	return &suiteHarness{reg: reg, env: env}
}

func (h *suiteHarness) run(defName string, plan []stepPlan, input []byte) sagaResult {
	h.env.ExecuteWorkflow(saga, sagaInput{DefName: defName, Plan: plan, Input: input})
	if err := h.env.GetWorkflowError(); err != nil {
		// Compensation outcomes are returned, not failed (see package doc);
		// a hard error here means the workflow itself broke.
		panic(err)
	}
	var res sagaResult
	if err := h.env.GetWorkflowResult(&res); err != nil {
		panic(err)
	}
	return res
}

func TestSagaHappyPath(t *testing.T) {
	rec := &recorder{}
	def := orchestration.Definition{Name: "withdraw", Steps: []orchestration.Step{
		stepMarker(rec, "reserve", false),
		stepMarker(rec, "send", false),
	}}
	h := newHarness(t, def)
	res := h.run("withdraw", planFor(def), []byte("x"))
	require.Equal(t, string(orchestration.StatusCompleted), res.Status)
	require.Equal(t, "send>reserve>x", string(res.Output))
	require.Empty(t, rec.undo)
}

func TestSagaCompensatesInReverseOrder(t *testing.T) {
	rec := &recorder{}
	def := orchestration.Definition{Name: "deposit", Steps: []orchestration.Step{
		stepMarker(rec, "capture", false),
		stepMarker(rec, "post-ledger", false),
		stepMarker(rec, "boom", true),
	}}
	h := newHarness(t, def)
	res := h.run("deposit", planFor(def), []byte("x"))
	require.Equal(t, string(orchestration.StatusCompensated), res.Status)
	require.Equal(t, "boom", res.FailedStep)
	require.Equal(t, []string{"post-ledger", "capture"}, rec.undo,
		"compensation must run in reverse, the newest fact undone first")
}

func TestSagaCompensationBrokenStillUndoesOthers(t *testing.T) {
	rec := &recorder{}
	def := orchestration.Definition{Name: "mixed", Steps: []orchestration.Step{
		stepMarker(rec, "good", false),
		stepMarker(rec, "bad", false),
	}}
	// make "bad"'s compensation fail: the recorder returns errors for it.
	for i := range def.Steps {
		if def.Steps[i].Name == "bad" {
			def.Steps[i].Compensate = func(context.Context, []byte) error { return errors.New("cannot undo") }
		}
	}
	def.Steps = append(def.Steps, orchestration.Step{Name: "boom", Run: func(context.Context, []byte) ([]byte, error) {
		return nil, errors.New("forward failure")
	}})
	h := newHarness(t, def)
	res := h.run("mixed", planFor(def), []byte("x"))
	require.Equal(t, string(orchestration.StatusCompensationBroken), res.Status)
	// The undo that could happen did happen despite the one that could not.
	require.Contains(t, res.Compensated, "good")
}

func TestSagaRetryBudgetIsHonored(t *testing.T) {
	def := orchestration.Definition{Name: "flaky", Steps: []orchestration.Step{
		stepFlaky("third-time-lucky", 3),
	}}
	h := newHarness(t, def)
	res := h.run("flaky", planFor(def), nil)
	require.Equal(t, string(orchestration.StatusCompleted), res.Status)
	require.Equal(t, "settled", string(res.Output))
}

func TestRegistryRejectsConflictingRedefinition(t *testing.T) {
	reg := NewRegistry()
	require.NoError(t, reg.Register(orchestration.Definition{
		Name: "dup", Steps: []orchestration.Step{{Name: "a", Run: func(context.Context, []byte) ([]byte, error) { return nil, nil }}},
	}))
	err := reg.Register(orchestration.Definition{
		Name: "dup", Steps: []orchestration.Step{{Name: "b", Run: func(context.Context, []byte) ([]byte, error) { return nil, nil }}},
	})
	require.Error(t, err)
}
