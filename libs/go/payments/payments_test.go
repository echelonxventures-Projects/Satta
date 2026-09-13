package payments_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/ledger"
	"github.com/satta/platform/libs/go/money"
	"github.com/satta/platform/libs/go/orchestration"
	"github.com/satta/platform/libs/go/payments"
	"github.com/satta/platform/libs/go/tenancy"
)

var eur = money.Currency{Code: "EUR", Scale: 2, Kind: money.KindFiat}

func amt(v int64) money.Amount { return money.MustNew(v, eur) }

type mockProcessor struct {
	name    string
	create  payments.ProcessorResult
	query   func() payments.ProcessorResult
	created chan struct{}
}

func (m *mockProcessor) Name() string { return m.name }

func (m *mockProcessor) Create(context.Context, payments.PaymentRequest) (payments.ProcessorResult, error) {
	if m.created != nil {
		close(m.created)
	}
	return m.create, nil
}

func (m *mockProcessor) Query(context.Context, string) (payments.ProcessorResult, error) {
	return m.query(), nil
}

type announceMeter struct {
	events []payments.MeterEvent
	err    error
}

func (a *announceMeter) Emit(_ context.Context, ev payments.MeterEvent) error {
	if a.err != nil {
		return a.err
	}
	a.events = append(a.events, ev)
	return nil
}

func setup(t *testing.T, proc payments.Processor) (*payments.Service, *ledger.MemoryLedger, *payments.MemoryStore) {
	t.Helper()
	st := payments.NewMemoryStore()
	lgr := ledger.NewMemoryLedger(clock.NewFixed(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)))
	meter := &announceMeter{}
	svc, err := payments.NewServiceWithMeter(st, lgr, clock.NewFixed(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)), meter)
	if err != nil {
		t.Fatal(err)
	}
	svc.RegisterProcessor(payments.MethodCard, proc)
	return svc, lgr, st
}

func tenantCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, err := tenancy.WithScope(context.Background(), tenancy.Scope{TenantID: "op"})
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

// open the ledger accounts the flow needs on the house side.
func openAccounts(t *testing.T, lgr *ledger.MemoryLedger) {
	t.Helper()
	ctx := tenantCtx(t)
	for _, id := range []string{"psp:card:EUR"} {
		if err := lgr.OpenAccount(ctx, ledger.Account{ID: id, Kind: ledger.KindSettle, Currency: eur,
			Scope: tenancy.Scope{TenantID: "op"}}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDepositHappyPath(t *testing.T) {
	proc := &mockProcessor{
		name:   "card-vendor",
		create: payments.ProcessorResult{ProviderRef: "pi_123", Status: payments.StatusActionRequired},
		query: func() payments.ProcessorResult {
			return payments.ProcessorResult{ProviderRef: "pi_123", Status: payments.StatusCaptured, Amount: ptr(amt(5000))}
		},
	}
	svc, lgr, _ := setup(t, proc)
	openAccounts(t, lgr)
	ctx := tenantCtx(t)

	dep, err := svc.Start(ctx, "dep-1", "op", "alice", amt(5000), payments.MethodCard)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if dep.Status != payments.StatusActionRequired {
		t.Fatalf("status = %s, want action_required", dep.Status)
	}
	// 3DS completion webhook.
	dep, err = svc.Confirm(ctx, "dep-1", payments.ProcessorResult{ProviderRef: "pi_123", Status: payments.StatusCaptured})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if dep.Status != payments.StatusCaptured {
		t.Fatalf("status = %s, want captured", dep.Status)
	}
	dep, err = svc.Credit(ctx, "dep-1")
	if err != nil {
		t.Fatalf("credit: %v", err)
	}
	if dep.Status != payments.StatusCredited {
		t.Fatalf("status = %s, want credited", dep.Status)
	}
	bal, err := lgr.Balance(ctx, ledger.WalletAccount("alice", ledger.KindPlayerCash))
	if err != nil {
		t.Fatalf("balance (wallet opened by credit): %v", err)
	}
	if bal.Minor() != 5000 {
		t.Fatalf("balance = %d, want 5000", bal.Minor())
	}
	// Idempotent: re-credit is absorbed, ledger says once.
	if _, err := svc.Credit(ctx, "dep-1"); err != nil {
		t.Fatalf("re-credit: %v", err)
	}
	bal2, b2err := lgr.Balance(ctx, ledger.WalletAccount("alice", ledger.KindPlayerCash))
	if b2err != nil {
		t.Fatal(b2err)
	}
	if bal2.Minor() != 5000 {
		t.Fatalf("re-credit moved money: %d", bal.Minor())
	}
}

func TestAmountMismatchIsSecurityEvent(t *testing.T) {
	proc := &mockProcessor{
		name:   "card-vendor",
		create: payments.ProcessorResult{ProviderRef: "pi_x", Status: payments.StatusAuthorized},
		query: func() payments.ProcessorResult {
			// The provider captured a DIFFERENT sum than the intent.
			return payments.ProcessorResult{ProviderRef: "pi_x", Status: payments.StatusCaptured, Amount: ptr(amt(5001))}
		},
	}
	svc, lgr, _ := setup(t, proc)
	openAccounts(t, lgr)
	ctx := tenantCtx(t)
	if _, err := svc.Start(ctx, "dep-2", "op", "bob", amt(5000), payments.MethodCard); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Confirm(ctx, "dep-2", payments.ProcessorResult{ProviderRef: "pi_x", Status: payments.StatusCaptured})
	if !errors.Is(err, payments.ErrAmountMismatch) {
		t.Fatalf("mismatched capture: %v, want ErrAmountMismatch", err)
	}
	// The wallet never saw value.
	bal, err := lgr.Balance(ctx, ledger.WalletAccount("bob", ledger.KindPlayerCash))
	if err == nil && !bal.IsZero() {
		t.Fatalf("wallet credited %d despite the mismatch — the gate failed", bal.Minor())
	}
}

func TestIllegalTransitionsRejected(t *testing.T) {
	proc := &mockProcessor{
		name:   "card-vendor",
		create: payments.ProcessorResult{ProviderRef: "pi_9", Status: payments.StatusStarted},
	}
	svc, lgr, _ := setup(t, proc)
	openAccounts(t, lgr)
	ctx := tenantCtx(t)
	if _, err := svc.Start(ctx, "dep-3", "op", "eve", amt(100), payments.MethodCard); err != nil {
		t.Fatal(err)
	}
	// Credit straight from a non-captured state must be refused.
	if _, err := svc.Credit(ctx, "dep-3"); !errors.Is(err, payments.ErrBadTransition) {
		t.Fatalf("premature credit: %v, want ErrBadTransition", err)
	}
}

func TestSagaCreditsAndAnnounces(t *testing.T) {
	meter := &announceMeter{}
	st := payments.NewMemoryStore()
	clk := clock.NewFixed(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))
	lgr := ledger.NewMemoryLedger(clk)
	svc, serr := payments.NewServiceWithMeter(st, lgr, clk, meter)
	if serr != nil {
		t.Fatal(serr)
	}
	proc := &mockProcessor{
		name:   "card-vendor",
		create: payments.ProcessorResult{ProviderRef: "pi_s", Status: payments.StatusCaptured},
		query: func() payments.ProcessorResult {
			return payments.ProcessorResult{ProviderRef: "pi_s", Status: payments.StatusCaptured, Amount: ptr(amt(777))}
		},
	}
	svc.RegisterProcessor(payments.MethodCard, proc)
	ctx := tenantCtx(t)
	if err := lgr.OpenAccount(ctx, ledger.Account{ID: "psp:card:EUR", Kind: ledger.KindSettle, Currency: eur,
		Scope: tenancy.Scope{TenantID: "op"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(ctx, "dep-s", "op", "carol", amt(777), payments.MethodCard); err != nil {
		t.Fatal(err)
	}

	runner := orchestration.NewLocalRunner(clk)
	def := svc.SagaDefinition()
	run, created, err := runner.Start(ctx, def, "dep-s", []byte("dep-s"))
	if err != nil || !created {
		t.Fatalf("saga start: %v created=%v", err, created)
	}
	waitTerminal(t, runner, run.ID)
	final, err := runner.Status(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != orchestration.StatusCompleted {
		t.Fatalf("saga status = %s (%s), want completed", final.Status, final.Err)
	}
	bal, berr := lgr.Balance(ctx, ledger.WalletAccount("carol", ledger.KindPlayerCash))
	if berr != nil {
		t.Fatal(berr)
	}
	if bal.Minor() != 777 {
		t.Fatalf("saga credit = %d, want 777", bal.Minor())
	}
	if len(meter.events) != 1 || meter.events[0].SubjectID != "dep-s" {
		t.Fatalf("announce events = %+v, want exactly one for dep-s", meter.events)
	}
}

func TestSagaAnnounceFailureCompensatesTheCredit(t *testing.T) {
	meter := &announceMeter{err: errors.New("metering down")}
	st := payments.NewMemoryStore()
	clk := clock.NewFixed(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))
	lgr := ledger.NewMemoryLedger(clk)
	svc, serr := payments.NewServiceWithMeter(st, lgr, clk, meter)
	if serr != nil {
		t.Fatal(serr)
	}
	proc := &mockProcessor{
		name:   "card-vendor",
		create: payments.ProcessorResult{ProviderRef: "pi_c", Status: payments.StatusCaptured},
		query: func() payments.ProcessorResult {
			return payments.ProcessorResult{ProviderRef: "pi_c", Status: payments.StatusCaptured, Amount: ptr(amt(4242))}
		},
	}
	svc.RegisterProcessor(payments.MethodCard, proc)
	ctx := tenantCtx(t)
	if err := lgr.OpenAccount(ctx, ledger.Account{ID: "psp:card:EUR", Kind: ledger.KindSettle, Currency: eur,
		Scope: tenancy.Scope{TenantID: "op"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(ctx, "dep-c", "op", "dan", amt(4242), payments.MethodCard); err != nil {
		t.Fatal(err)
	}

	runner := orchestration.NewLocalRunner(clk)
	run, _, err := runner.Start(ctx, svc.SagaDefinition(), "dep-c", []byte("dep-c"))
	if err != nil {
		t.Fatal(err)
	}
	waitTerminal(t, runner, run.ID)
	final, ferr := runner.Status(ctx, run.ID)
	if ferr != nil {
		t.Fatal(ferr)
	}
	if final.Status != orchestration.StatusCompensated {
		t.Fatalf("saga status = %s (%s), want compensated", final.Status, final.Err)
	}
	bal, dberr := lgr.Balance(ctx, ledger.WalletAccount("dan", ledger.KindPlayerCash))
	if dberr != nil {
		t.Fatal(dberr)
	}
	if !bal.IsZero() {
		t.Fatalf("player holds %d after compensation — reversal failed", bal.Minor())
	}
}

func waitTerminal(t *testing.T, r orchestration.Runner, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		run, err := r.Status(context.Background(), id)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if run.Status.Terminal() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("saga %s stuck at %s", id, run.Status)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func ptr(a money.Amount) *money.Amount { return &a }
