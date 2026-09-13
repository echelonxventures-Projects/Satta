package fin_test

import (
	"context"
	"testing"
	"time"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/fin"
	"github.com/satta/platform/libs/go/ledger"
	"github.com/satta/platform/libs/go/money"
	"github.com/satta/platform/libs/go/tenancy"
)

var eur = money.Currency{Code: "EUR", Scale: 2, Kind: money.KindFiat}

func r(n, d int64) money.Ratio { return money.MustRatio(n, d) }
func a(v int64) money.Amount   { return money.MustNew(v, eur) }

func bal(ctx context.Context, t *testing.T, l *ledger.MemoryLedger, id string) money.Amount {
	t.Helper()
	amt, err := l.Balance(ctx, id)
	if err != nil {
		t.Fatalf("balance %s: %v", id, err)
	}
	return amt
}

func TestPercentageRakeAndCap(t *testing.T) {
	p, err := fin.NewPercentageRake(r(5, 100), nil, money.RoundingDown)
	if err != nil {
		t.Fatal(err)
	}
	rake, err := p.Rake(a(100000))
	if err != nil {
		t.Fatal(err)
	}
	if rake.Amount.Minor() != 5000 {
		t.Fatalf("5%% of 1000.00 = %d, want 5000", rake.Amount.Minor())
	}
	capAmt := a(3000)
	p2, err := fin.NewPercentageRake(r(5, 100), &capAmt, money.RoundingDown)
	if err != nil {
		t.Fatal(err)
	}
	rake2, err := p2.Rake(a(100000))
	if err != nil {
		t.Fatal(err)
	}
	if rake2.Amount.Minor() != 3000 {
		t.Fatalf("capped rake = %d, want 3000", rake2.Amount.Minor())
	}
}

func TestTieredRakeBands(t *testing.T) {
	t1 := a(100000) // first 1000.00 at 5%
	tr, err := fin.NewTieredRake([]fin.Tier{
		{UpTo: &t1, Share: r(5, 100)},
		{Share: r(2, 100)},
	}, money.RoundingDown)
	if err != nil {
		t.Fatal(err)
	}
	// gross 2000.00: 5% of 1000.00 = 50.00, 2% of 1000.00 = 20.00 → 70.00
	rake, err := tr.Rake(a(200000))
	if err != nil {
		t.Fatal(err)
	}
	if rake.Amount.Minor() != 7000 {
		t.Fatalf("tiered rake = %d, want 7000", rake.Amount.Minor())
	}
	if _, err := fin.NewTieredRake([]fin.Tier{{Share: r(1, 2)}, {UpTo: &t1, Share: r(1, 4)}}, money.RoundingDown); err == nil {
		t.Fatal("unbounded tier not last accepted")
	}
}

func TestSettlementBooksBalance(t *testing.T) {
	ctx, err := tenancy.WithScope(context.Background(), tenancy.Scope{TenantID: "op"})
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFixed(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))
	l := ledger.NewMemoryLedger(clk)
	revenue := ledger.HouseAccount(ledger.KindHouse, "EUR")
	settle := ledger.HouseAccount(ledger.KindSettle, "EUR")
	pool := "hold:event:world-final"
	w1 := ledger.WalletAccount("alice", ledger.KindPlayerCash)
	w2 := ledger.WalletAccount("bob", ledger.KindPlayerCash)
	for _, ac := range []struct {
		id   string
		kind ledger.Kind
	}{
		{revenue, ledger.KindHouse}, {settle, ledger.KindSettle},
		{pool, ledger.KindEscrow}, {w1, ledger.KindPlayerCash}, {w2, ledger.KindPlayerCash},
	} {
		if oerr := l.OpenAccount(ctx, ledger.Account{ID: ac.id, Kind: ac.kind, Currency: eur,
			Scope: tenancy.Scope{TenantID: "op"}}); oerr != nil {
			t.Fatalf("open %s: %v", ac.id, oerr)
		}
	}

	strategy, err := fin.NewPercentageRake(r(5, 100), nil, money.RoundingDown)
	if err != nil {
		t.Fatal(err)
	}
	settlement, err := fin.Settle(a(100000), strategy, []fin.Winner{
		{PlayerID: "bob", Weight: 1}, {PlayerID: "alice", Weight: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if settlement.Net.Minor() != 95000 || settlement.Rake.Amount.Minor() != 5000 {
		t.Fatalf("net/rake = %d/%d, want 95000/5000", settlement.Net.Minor(), settlement.Rake.Amount.Minor())
	}

	entries, err := settlement.LedgerEntries("world-final", eur, clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if _, perr := l.Post(ctx, e); perr != nil {
			t.Fatalf("post %s: %v", e.IdempotencyKey, perr)
		}
	}

	// Winner ordering was normalized: alice (weight 3) first, remainder rule
	// gives the odd cent to her too. 95000*3/4=71250, *1/4=23750.
	alice := bal(ctx, t, l, w1)
	bob := bal(ctx, t, l, w2)
	if alice.Minor() != 71250 || bob.Minor() != 23750 {
		t.Fatalf("payouts = alice %d bob %d, want 71250/23750", alice.Minor(), bob.Minor())
	}
	revBal := bal(ctx, t, l, revenue)
	if revBal.Minor() != -95000 {
		t.Fatalf("house revenue netted %d, want -95000 (funded pool, recovered via rake)", revBal.Minor())
	}
	poolBal := bal(ctx, t, l, pool)
	if !poolBal.IsZero() {
		t.Fatalf("event pool left at %d, want 0 — funding must drain through payout+rake", poolBal.Minor())
	}
}

func TestHoldAndReleaseGatedPayout(t *testing.T) {
	ctx, err := tenancy.WithScope(context.Background(), tenancy.Scope{TenantID: "op"})
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFixed(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))
	l := ledger.NewMemoryLedger(clk)
	held := fin.HeldPayout{PayoutID: "wd-1", PlayerID: "carol", Amount: a(50000)}
	settle := ledger.HouseAccount(ledger.KindSettle, "EUR")
	escrow := ledger.WalletAccount("carol", ledger.KindEscrow)
	cash := ledger.WalletAccount("carol", ledger.KindPlayerCash)
	for _, ac := range []struct {
		id   string
		kind ledger.Kind
	}{{settle, ledger.KindSettle}, {escrow, ledger.KindEscrow}, {cash, ledger.KindPlayerCash}} {
		if oerr := l.OpenAccount(ctx, ledger.Account{ID: ac.id, Kind: ac.kind, Currency: eur,
			Scope: tenancy.Scope{TenantID: "op"}}); oerr != nil {
			t.Fatal(oerr)
		}
	}

	// Releasing without the hold must fail — the escrow wallet has nothing
	// (escrow rejects negative results, so the gate is structural).
	if rel, rerr := fin.ReleaseEntries(held, clk.Now()); rerr == nil {
		if _, perr := l.Post(ctx, rel); perr == nil {
			t.Fatal("release without hold posted")
		}
	}

	holdEntries, err := fin.HoldEntries(held, clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range holdEntries {
		if _, perr := l.Post(ctx, e); perr != nil {
			t.Fatalf("hold: %v", perr)
		}
	}
	ebal := bal(ctx, t, l, escrow)
	if ebal.Minor() != 50000 {
		t.Fatalf("escrow = %d, want 50000", ebal.Minor())
	}

	rel, err := fin.ReleaseEntries(held, clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, perr := l.Post(ctx, rel); perr != nil {
		t.Fatalf("release: %v", perr)
	}
	cbal := bal(ctx, t, l, cash)
	if cbal.Minor() != 50000 {
		t.Fatalf("cash = %d, want 50000", cbal.Minor())
	}
	ebal = bal(ctx, t, l, escrow)
	if !ebal.IsZero() {
		t.Fatalf("escrow drained to %d, want 0", ebal.Minor())
	}

	// Re-release: idempotent (stable key), not a double payment.
	if _, perr := l.Post(ctx, rel); perr != nil {
		t.Fatalf("re-release: %v", perr)
	}
	if cbal = bal(ctx, t, l, cash); cbal.Minor() != 50000 {
		t.Fatalf("re-release moved money: %d", cbal.Minor())
	}
}
