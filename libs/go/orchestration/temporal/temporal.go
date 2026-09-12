// Package temporal binds the orchestration port to Temporal — the durable
// engine (roadmap Task 2, ADR-0001: the port is the seam; this is one
// binding, not the only possible one).
//
// Two decisions inside it are worth stating because they are not obvious and
// they are load-bearing:
//
//  1. One generic workflow function, "saga", dispatched by definition name.
//     Step closures can never cross a durable boundary (they are not
//     serializable and must not be — a replayable workflow takes data, not
//     captured Go). A Definition is therefore registered in-process at
//     service boot (Register), and the workflow executes a deterministic
//     plan derived from it: names, retry budgets, compensation presence.
//     Activities look steps up in that registry.
//  2. A compensated saga COMPLETES rather than fails its workflow execution:
//     a failed workflow invites the engine's own retry loop, and an
//     automatically-retried withdrawal saga that already compensated is
//     exactly the double-application this package exists to prevent. Final
//     state travels in the workflow result and the memo.
package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	sdkactivity "go.temporal.io/sdk/activity"
	sdkclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/orchestration"
)

// TaskQueue is the single queue every saga runs on; per-tenant task queues
// are a scale-out concern (S49), not a correctness one.
const (
	TaskQueue        = "satta-orchestration"
	actRun           = "satta.runStep"
	actCompensate    = "satta.compensateStep"
	memoStatus       = "satta.status"
	memoFailedStep   = "satta.failed_step"
	memoErr          = "satta.error"
	workflowTimeout  = 7 * 24 * time.Hour // a money saga does not outlive its business week
	stepActivityTTL  = 10 * time.Minute
	stepActivityBeat = 30 * time.Second
)

// Registry maps definition names to their step implementations. It is
// worker-local data: what crosses to the engine is the plan, never a closure.
type Registry struct {
	defs map[string]orchestration.Definition
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{defs: map[string]orchestration.Definition{}} }

// Register stores a definition, or rejects a conflicting redefinition: two
// code revisions agreeing on a name but not on steps is a deploy bug that
// must surface at boot, not as a half-executed saga.
func (r *Registry) Register(def orchestration.Definition) error {
	if err := def.Validate(); err != nil {
		return err
	}
	if prev, exists := r.defs[def.Name]; exists && !sameShape(prev, def) {
		return fmt.Errorf("temporal: definition %q is already registered with a different step layout", def.Name)
	}
	r.defs[def.Name] = def
	return nil
}

func sameShape(a, b orchestration.Definition) bool {
	if len(a.Steps) != len(b.Steps) {
		return false
	}
	for i := range a.Steps {
		if a.Steps[i].Name != b.Steps[i].Name ||
			a.Steps[i].Retries != b.Steps[i].Retries ||
			(a.Steps[i].Compensate != nil) != (b.Steps[i].Compensate != nil) {
			return false
		}
	}
	return true
}

// stepPlan is the deterministic, serializable shape a workflow may see.
type stepPlan struct {
	Name          string
	Retries       int32
	HasCompensate bool
}

type sagaInput struct {
	DefName string
	Plan    []stepPlan
	Input   []byte
}

// sagaResult is what a finished execution reports; mirrored into the memo so
// a Describe-only caller can tell running from terminal immediately.
type sagaResult struct {
	Status      string
	FailedStep  string
	Compensated []string
	Err         string
	Output      []byte
}

// Runner implements orchestration.Runner over a Temporal client + registry.
type Runner struct {
	c        sdkclient.Client
	ns       string
	taskQ    string
	registry *Registry
	clk      clock.Clock
}

// NewRunner wires a Runner to a client; namespace/taskQueue default when
// empty. Timestamps come from the injected clock (SAFETY INVARIANT 12).
func NewRunner(c sdkclient.Client, namespace, taskQueue string, reg *Registry, clk clock.Clock) *Runner {
	if namespace == "" {
		namespace = "default"
	}
	if taskQueue == "" {
		taskQueue = TaskQueue
	}
	if clk == nil {
		clk = clock.NewSystem()
	}
	return &Runner{c: c, ns: namespace, taskQ: taskQueue, registry: reg, clk: clk}
}

var _ orchestration.Runner = (*Runner)(nil)

// Register adds a definition (to the shared registry; the worker reads the
// same pointer, so registration and execution cannot disagree in-process).
func (r *Runner) Register(def orchestration.Definition) error { return r.registry.Register(def) }

// Start implements orchestration.Runner. Exactly-once is delegated to the
// engine's WorkflowID identity: the business key IS the workflow ID, so a
// crash-recovery re-issue cannot mint a second run.
func (r *Runner) Start(ctx context.Context, def orchestration.Definition, key string, input []byte) (orchestration.Run, bool, error) {
	if err := def.Validate(); err != nil {
		return orchestration.Run{}, false, err
	}
	if key == "" {
		return orchestration.Run{}, false, errors.New("orchestration: Start requires a business key (exactly-once is contractual)")
	}
	if err := r.registry.Register(def); err != nil {
		return orchestration.Run{}, false, err
	}
	wfID := def.Name + "/" + key
	handle, err := r.c.ExecuteWorkflow(ctx, sdkclient.StartWorkflowOptions{
		ID:                       wfID,
		TaskQueue:                r.taskQ,
		WorkflowIDReusePolicy:    enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowExecutionTimeout: workflowTimeout,
	}, saga, sagaInput{DefName: def.Name, Plan: planFor(def), Input: input})
	if err != nil {
		var dup *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &dup) {
			run, derr := r.Status(ctx, wfID)
			if derr != nil {
				return orchestration.Run{}, false, derr
			}
			return run, false, nil
		}
		return orchestration.Run{}, false, fmt.Errorf("temporal: start %s: %w", wfID, err)
	}
	return orchestration.Run{
		ID: handle.GetID(), Definition: def.Name, Key: key,
		Status: orchestration.StatusRunning, StartedAt: r.clk.Now().UTC(),
	}, true, nil
}

// Status implements orchestration.Runner via Describe (memo state, live)
// plus Get (the completion result, once closed — the only view carrying
// compensation detail).
func (r *Runner) Status(ctx context.Context, runID string) (orchestration.Run, error) {
	desc, err := r.c.DescribeWorkflowExecution(ctx, runID, "")
	if err != nil {
		return orchestration.Run{}, fmt.Errorf("%w: %s: %w", orchestration.ErrNotFound, runID, err)
	}
	info := desc.GetWorkflowExecutionInfo()
	run := orchestration.Run{ID: runID, Definition: info.GetType().GetName()}
	for i := 0; i < len(runID); i++ {
		if runID[i] == '/' {
			run.Definition, run.Key = runID[:i], runID[i+1:]
			break
		}
	}

	status := orchestration.StatusRunning
	if f := info.GetMemo().GetFields()[memoStatus]; f != nil {
		var s string
		if json.Unmarshal(f.GetData(), &s) == nil && s != "" {
			status = orchestration.Status(s)
		}
	}
	if st := info.GetStatus(); st == enums.WORKFLOW_EXECUTION_STATUS_CANCELED && !status.Terminal() {
		status = orchestration.StatusCanceling
	}
	run.Status = status

	if status.Terminal() {
		var res sagaResult
		if gerr := r.c.GetWorkflow(ctx, runID, "").Get(ctx, &res); gerr == nil {
			run.Compensated = res.Compensated
			run.FailedStep = res.FailedStep
			run.Err = res.Err
			run.Output = res.Output
		}
	}
	return run, nil
}

// Cancel implements orchestration.Runner: the engine signals cancellation,
// the saga compensates on its disconnected context, and the terminal state
// read back is Canceled (or CompensationBroken — never "canceled" over a
// partially-applied ledger).
func (r *Runner) Cancel(ctx context.Context, runID, reason string) (orchestration.Run, error) {
	if err := r.c.CancelWorkflow(ctx, runID, ""); err != nil {
		var notFound *serviceerror.NotFound
		if !errors.As(err, &notFound) {
			return orchestration.Run{}, fmt.Errorf("temporal: cancel %s: %w", runID, err)
		}
	}
	_ = reason // recorded engine-side via the workflow's own cancel path
	return r.Status(ctx, runID)
}

// registrar is the registration surface shared by a production worker and
// the testsuite environment: engine bindings must be testable against the
// engine's own semantics without a live server.
type registrar interface {
	RegisterWorkflowWithOptions(any, workflow.RegisterOptions)
	RegisterActivityWithOptions(any, sdkactivity.RegisterOptions)
}

// Bind registers the workflow and the registry's activities on a worker or
// test environment. Plain functions (not a struct) because a named struct
// registration prefixes each method name; exact activity type names are part
// of the workflow's history and must never shift.
func (r *Registry) Bind(w registrar) {
	w.RegisterWorkflowWithOptions(saga, workflow.RegisterOptions{Name: "saga"})
	run, compensate := r.runStep, r.compensateStep
	w.RegisterActivityWithOptions(func(ctx context.Context, defName string, idx int32, input []byte) ([]byte, error) {
		return run(ctx, defName, idx, input)
	}, sdkactivity.RegisterOptions{Name: actRun})
	w.RegisterActivityWithOptions(func(ctx context.Context, defName string, idx int32, input []byte) error {
		return compensate(ctx, defName, idx, input)
	}, sdkactivity.RegisterOptions{Name: actCompensate})
}

// ---- workflow + activities -------------------------------------------------

func saga(ctx workflow.Context, in sagaInput) (sagaResult, error) {
	res := sagaResult{Status: string(orchestration.StatusRunning)}
	_ = workflow.UpsertMemo(ctx, map[string]any{memoStatus: res.Status}) //nolint:errcheck // memo mirrors state for Describe callers; the result is the truth

	var (
		cursor    = in.Input
		inputs    = make([][]byte, len(in.Plan))
		completed []int
		failErr   error
	)
	for i, sp := range in.Plan {
		inputs[i] = cursor
		ao := workflow.ActivityOptions{
			TaskQueue:           TaskQueue,
			StartToCloseTimeout: stepActivityTTL,
			HeartbeatTimeout:    stepActivityBeat,
			RetryPolicy:         retryFor(sp),
		}
		if err := workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, ao), actRun, in.DefName, i, cursor).Get(ctx, &cursor); err != nil {
			failErr, res.FailedStep = err, sp.Name
			break
		}
		completed = append(completed, i)
	}

	if failErr == nil {
		res.Status = string(orchestration.StatusCompleted)
		res.Output = cursor
		_ = workflow.UpsertMemo(ctx, map[string]any{memoStatus: res.Status}) //nolint:errcheck // memo mirrors state for Describe callers; the result is the truth
		return res, nil
	}

	// Compensation on a disconnected context: the canceled forward context
	// would abort every undo still owed — and an undo we cannot run is the
	// partial application this engine exists to prevent.
	cctx, cancelCompensation := workflow.NewDisconnectedContext(ctx)
	defer cancelCompensation()
	broken := false
	for i := len(completed) - 1; i >= 0; i-- {
		idx := completed[i]
		sp := in.Plan[idx]
		if !sp.HasCompensate {
			continue
		}
		ao := workflow.ActivityOptions{
			TaskQueue:           TaskQueue,
			StartToCloseTimeout: stepActivityTTL,
			RetryPolicy:         retryFor(sp),
		}
		if err := workflow.ExecuteActivity(workflow.WithActivityOptions(cctx, ao), actCompensate, in.DefName, idx, inputs[idx]).Get(cctx, nil); err == nil {
			res.Compensated = append(res.Compensated, sp.Name)
		} else {
			broken = true
		}
	}
	switch {
	case broken:
		res.Status = string(orchestration.StatusCompensationBroken)
	case cancelledErr(failErr):
		res.Status = string(orchestration.StatusCanceled)
	case len(res.Compensated) > 0:
		res.Status = string(orchestration.StatusCompensated)
	default:
		res.Status = string(orchestration.StatusFailed)
	}
	res.Err = failErr.Error()
	_ = workflow.UpsertMemo(ctx, map[string]any{ //nolint:errcheck // memo mirrors state for Describe callers; the result is the truth
		memoStatus: res.Status, memoFailedStep: res.FailedStep, memoErr: res.Err,
	})
	// Completed, not failed — see package comment (2).
	return res, nil
}

// cancelledErr distinguishes an engine-driven cancellation from a business
// failure: both end a forward step, only one must report as Canceled.
func cancelledErr(err error) bool {
	return temporal.IsCanceledError(err) || errors.Is(err, context.Canceled)
}

func retryFor(sp stepPlan) *temporal.RetryPolicy {
	return &temporal.RetryPolicy{InitialInterval: time.Second, MaximumAttempts: sp.Retries + 1}
}

func planFor(def orchestration.Definition) []stepPlan {
	plan := make([]stepPlan, 0, len(def.Steps))
	for _, s := range def.Steps {
		plan = append(plan, stepPlan{Name: s.Name, Retries: int32(s.Retries), HasCompensate: s.Compensate != nil})
	}
	return plan
}

func (r *Registry) runStep(ctx context.Context, defName string, idx int32, input []byte) ([]byte, error) {
	def, ok := r.defs[defName]
	if !ok {
		return nil, fmt.Errorf("temporal: definition %q not registered on this worker", defName)
	}
	if idx < 0 || int(idx) >= len(def.Steps) {
		return nil, fmt.Errorf("temporal: step index %d out of range for %q", idx, defName)
	}
	return def.Steps[idx].Run(ctx, input)
}

func (r *Registry) compensateStep(ctx context.Context, defName string, idx int32, input []byte) error {
	def, ok := r.defs[defName]
	if !ok {
		return fmt.Errorf("temporal: definition %q not registered on this worker", defName)
	}
	if idx < 0 || int(idx) >= len(def.Steps) || def.Steps[idx].Compensate == nil {
		return fmt.Errorf("temporal: no compensation at %q step %d", defName, idx)
	}
	return def.Steps[idx].Compensate(ctx, input)
}
