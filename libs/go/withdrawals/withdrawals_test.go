package withdrawals_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/satta/platform/libs/go/access"
	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/ledger"
	"github.com/satta/platform/libs/go/money"
	"github.com/satta/platform/libs/go/payments"
	"github.com/satta/platform/libs/go/rules"
	"github.com/satta/platform/libs/go/tenancy"
	"github.com/satta/platform/libs/go/withdrawals"
)

var eur = money.Currency{Code: "EUR", Scale: 2, Kind: money.KindFiat}

func amt(v int64) money.Amount { return money.MustNew(v, eur) }

type mockPayout struct {
	name       string
	payRes     withdrawals.PayoutResult
	payErr     error
	reconRes   *withdrawals.PayoutResult
	reconErr   error
	queryRes   withdrawals.PayoutResult
	payCalls   int
	reconCalls int
}

func (m *mockPayout) Name() string { return m.name }

func (m *mockPayout) Pay(context.Context, withdrawals.PayoutRequest) (withdrawals.PayoutResult, error) {
	m.payCalls++
	return m.payRes, m.payErr
}

func (m *mockPayout) Query(context.Context, string) (withdrawals.PayoutResult, error) {
	return m.queryRes, nil
}

func (m *mockPayout) Reconcile(context.Context, withdrawals.PayoutRequest) (withdrawals.PayoutResult, error) {
	m.reconCalls++
	if m.reconErr != nil {
		return withdrawals.PayoutResult{}, m.reconErr
	}
	if m.reconRes != nil {
		return *m.reconRes, nil
	}
	return withdrawals.PayoutResult{Status: withdrawals.StatusFailed}, nil
}

func gate(t *testing.T) withdrawals.Gate {
	t.Helper()
	g, err := withdrawals.NewPolicyGate(
		&rules.Node{Fact: "kyc_level", LtInt: int64p(2)},
		&rules.Node{Fact: "amount_minor", GteInt: int64p(100000)},
	)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func int64p(v int64) *int64 { return &v }

func approverPolicy(t *testing.T) *access.Policy {
	t.Helper()
	p, err := access.ParsePolicy(`[{"role":"finance-approver","permission":"withdrawal.approve"}]`)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type harness struct {
	svc  *withdrawals.Service
	lgr  *ledger.MemoryLedger
	st   *withdrawals.MemoryStore
	proc *mockPayout
	ctx  context.Context
	w    string // player wallet id
}

func newHarness(t *testing.T, fund int64) *harness {
	t.Helper()
	st := withdrawals.NewMemoryStore()
	clk := clock.NewFixed(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))
	lgr := ledger.NewMemoryLedger(clk)
	proc := &mockPayout{name: "payout-vendor", payRes: withdrawals.PayoutResult{ProviderRef: "po_1", Status: withdrawals.StatusPaid}}
	svc, err := withdrawals.NewService(st, lgr, gate(t), approverPolicy(t), clk)
	if err != nil {
		t.Fatal(err)
	}
	svc.RegisterPayoutProcessor(payments.MethodBank, proc)
	ctx, err := tenancy.WithScope(context.Background(), tenancy.Scope{TenantID: "op"})
	if err != nil {
		t.Fatal(err)
	}
	if err := lgr.OpenAccount(ctx, ledger.Account{ID: "bank:EUR", Kind: ledger.KindSettle, Currency: eur,
		Scope: tenancy.Scope{TenantID: "op"}}); err != nil {
		t.Fatal(err)
	}
	wallet := ledger.WalletAccount("alice", ledger.KindPlayerCash)
	if err := lgr.OpenAccount(ctx, ledger.Account{ID: wallet, Kind: ledger.KindPlayerCash, Currency: eur,
		Scope: tenancy.Scope{TenantID: "op"}}); err != nil {
		t.Fatal(err)
	}
	if fund > 0 {
		e, err := ledger.Transfer("fund-alice", "bank:EUR", wallet, amt(fund), "deposit", clk.Now())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := lgr.Post(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	ctx = context.WithValue(ctx, withdrawals.FactsKey{}, map[string]rules.Fact{
		"kyc_level": rules.IntFact(3),
	})
	return &harness{svc: svc, lgr: lgr, st: st, proc: proc, ctx: ctx, w: wallet}
}

func (h *harness) balance(t *testing.T) int64 {
	t.Helper()
	b, err := h.lgr.Balance(h.ctx, h.w)
	if err != nil {
		t.Fatal(err)
	}
	return b.Minor()
}

func TestWithdrawHappyPath(t *testing.T) {
	h := newHarness(t, 50000)
	wd, err := h.svc.Request(h.ctx, "wd-1", "alice", amt(20000), payments.MethodBank)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if wd.Status != withdrawals.StatusApproved {
		t.Fatalf("status = %s, want approved (under review cap)", wd.Status)
	}
	// The hold already moved the money out of spendable balance.
	if bal := h.balance(t); bal != 30000 {
		t.Fatalf("after hold balance = %d, want 30000", bal)
	}
	if _, perr := h.svc.Pay(h.ctx, "wd-1"); perr != nil {
		t.Fatalf("pay: %v", perr)
	}
	h.proc.queryRes = withdrawals.PayoutResult{Status: withdrawals.StatusConfirmed, Amount: ptrAmt(20000)}
	conf, err := h.svc.Confirm(h.ctx, "wd-1")
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if conf.Status != withdrawals.StatusConfirmed {
		t.Fatalf("status = %s, want confirmed", conf.Status)
	}
	if h.balance(t) != 30000 {
		t.Fatalf("balance drifted: %d", h.balance(t))
	}
	// Idempotent pay/confirm: no double movement.
	if _, err := h.svc.Pay(h.ctx, "wd-1"); err != nil {
		t.Fatalf("re-pay: %v", err)
	}
	if h.proc.payCalls != 1 {
		t.Fatalf("pay called %d times, want 1", h.proc.payCalls)
	}
}

func TestReviewThenApprove(t *testing.T) {
	h := newHarness(t, 500000)
	wd, err := h.svc.Request(h.ctx, "wd-2", "alice", amt(100000), payments.MethodBank)
	if err != nil {
		t.Fatal(err)
	}
	if wd.Status != withdrawals.StatusReview {
		t.Fatalf("status = %s, want review", wd.Status)
	}
	// A role not in the matrix: approval refused with a typed denial. An unknown
	// role (never bound in the matrix) is ErrUnknownRole — a different,
	// equally typed denial; "unknown" must not be laundered into "allowed".
	_, aerr := h.svc.Approve(h.ctx, "wd-2", "intern", "someone", nil)
	if aerr == nil {
		t.Fatal("unpermitted approve succeeded")
	}
	if !errors.Is(aerr, access.ErrDenied) && !errors.Is(aerr, access.ErrUnknownRole) {
		t.Fatalf("unpermitted approve: %v, want a typed denial", aerr)
	}
	if _, err := h.svc.Approve(h.ctx, "wd-2", "finance-approver", "ops-1", nil); err != nil {
		t.Fatalf("approve: %v", err)
	}
	after, lerr := h.st.Load(h.ctx, "wd-2")
	if lerr != nil {
		t.Fatal(lerr)
	}
	if after.Status != withdrawals.StatusApproved || after.Approver != "ops-1" {
		t.Fatalf("after approve: %+v", after)
	}
}

func TestGateDenyKeepsBalance(t *testing.T) {
	st := withdrawals.NewMemoryStore()
	clk := clock.NewFixed(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))
	lgr := ledger.NewMemoryLedger(clk)
	svc, err := withdrawals.NewService(st, lgr, gate(t), approverPolicy(t), clk)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cErr := tenancy.WithScope(context.Background(), tenancy.Scope{TenantID: "op"})
	if cErr != nil {
		t.Fatal(cErr)
	}
	ctx = context.WithValue(ctx, withdrawals.FactsKey{}, map[string]rules.Fact{
		"kyc_level": rules.IntFact(1), // below threshold: denied
	})
	wd, err := svc.Request(ctx, "wd-3", "bob", amt(5000), payments.MethodBank)
	if err != nil {
		t.Fatal(err)
	}
	if wd.Status != withdrawals.StatusRejected {
		t.Fatalf("status = %s, want rejected", wd.Status)
	}
}

func TestOverdraftRefusedAtHold(t *testing.T) {
	h := newHarness(t, 100)
	if _, err := h.svc.Request(h.ctx, "wd-4", "alice", amt(500), payments.MethodBank); !errors.Is(err, ledger.ErrOverdraft) {
		t.Fatalf("insufficient funds: %v, want ErrOverdraft", err)
	}
	if h.balance(t) != 100 {
		t.Fatalf("balance moved despite refusal: %d", h.balance(t))
	}
}

func TestRejectionReturnsFunds(t *testing.T) {
	h := newHarness(t, 500000)
	if _, err := h.svc.Request(h.ctx, "wd-5", "alice", amt(100000), payments.MethodBank); err != nil {
		t.Fatal(err)
	}
	if h.balance(t) != 400000 {
		t.Fatalf("hold failed: %d", h.balance(t))
	}
	if _, err := h.svc.Reject(h.ctx, "wd-5", "AML review"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if h.balance(t) != 500000 {
		t.Fatalf("rejection did not release: %d", h.balance(t))
	}
}

func TestPayErrorReconciledNotPaidReleases(t *testing.T) {
	h := newHarness(t, 50000)
	if _, err := h.svc.Request(h.ctx, "wd-6", "alice", amt(20000), payments.MethodBank); err != nil {
		t.Fatal(err)
	}
	h.proc.payErr = errors.New("network timeout")
	h.proc.reconRes = &withdrawals.PayoutResult{Status: withdrawals.StatusFailed}
	if _, err := h.svc.Pay(h.ctx, "wd-6"); err != nil {
		t.Fatalf("pay with clean reconcile: %v", err)
	}
	wd, werr := h.st.Load(h.ctx, "wd-6")
	if werr != nil {
		t.Fatal(werr)
	}
	if wd.Status != withdrawals.StatusFailed {
		t.Fatalf("status = %s, want failed", wd.Status)
	}
	if h.balance(t) != 50000 {
		t.Fatalf("failed payout did not release the hold: %d", h.balance(t))
	}
}

func TestPayErrorReconciledPaidMarksPaid(t *testing.T) {
	h := newHarness(t, 50000)
	if _, err := h.svc.Request(h.ctx, "wd-7", "alice", amt(20000), payments.MethodBank); err != nil {
		t.Fatal(err)
	}
	// The dangerous one: Pay errored (maybe after the provider executed).
	h.proc.payErr = errors.New("connection reset")
	h.proc.reconRes = &withdrawals.PayoutResult{Status: withdrawals.StatusPaid, ProviderRef: "po_late"}
	if _, err := h.svc.Pay(h.ctx, "wd-7"); err != nil {
		t.Fatalf("pay reconciled to paid: %v", err)
	}
	wd, werr := h.st.Load(h.ctx, "wd-7")
	if werr != nil {
		t.Fatal(werr)
	}
	if wd.Status != withdrawals.StatusPaid {
		t.Fatalf("status = %s, want paid", wd.Status)
	}
	if h.proc.payCalls != 1 || h.proc.reconCalls != 1 {
		t.Fatalf("calls pay=%d recon=%d, want 1/1 — retried blindly would double-pay", h.proc.payCalls, h.proc.reconCalls)
	}
}

func TestPayErrorUnreconcilableStaysHeld(t *testing.T) {
	h := newHarness(t, 50000)
	if _, err := h.svc.Request(h.ctx, "wd-8", "alice", amt(20000), payments.MethodBank); err != nil {
		t.Fatal(err)
	}
	h.proc.payErr = errors.New("timeout")
	h.proc.reconErr = errors.New("provider unreachable")
	if _, err := h.svc.Pay(h.ctx, "wd-8"); err == nil {
		t.Fatal("unreconcilable pay returned no error")
	}
	wd, werr := h.st.Load(h.ctx, "wd-8")
	if werr != nil {
		t.Fatal(werr)
	}
	if wd.Status != withdrawals.StatusApproved {
		t.Fatalf("status = %s, want approved-still-held", wd.Status)
	}
	// Funds remain held: neither spendable nor paid.
	if h.balance(t) != 30000 {
		t.Fatalf("unverifiable payout changed balance: %d", h.balance(t))
	}
}

func TestBatchGroupsByRailAndCurrency(t *testing.T) {
	h := newHarness(t, 900000)
	for _, id := range []string{"b-1", "b-2"} {
		if _, err := h.svc.Request(h.ctx, id, "alice", amt(30000), payments.MethodBank); err != nil {
			t.Fatal(err)
		}
	}
	b, err := h.svc.BuildBatch(h.ctx, payments.MethodBank, eur, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Withdrawals) != 2 || b.Total.Minor() != 60000 {
		t.Fatalf("batch = %d items total %d, want 2/60000", len(b.Withdrawals), b.Total.Minor())
	}
	// Deterministic order by ID.
	if b.Withdrawals[0].ID != "b-1" || b.Withdrawals[1].ID != "b-2" {
		t.Fatal("batch order is not deterministic")
	}
}

func ptrAmt(v int64) *money.Amount { a := amt(v); return &a }
