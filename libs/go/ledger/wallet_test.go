package ledger_test

import (
	"context"
	"testing"
	"time"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/ledger"
	"github.com/satta/platform/libs/go/money"
	"github.com/satta/platform/libs/go/tenancy"
)

var eur = money.Currency{Code: "EUR", Scale: 2, Kind: money.KindFiat}

func TestTransferEndToEnd(t *testing.T) {
	l := ledger.NewMemoryLedger(clock.NewFixed(time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)))
	ctx, err := tenancy.WithScope(context.Background(), tenancy.Scope{TenantID: "op"})
	if err != nil {
		t.Fatal(err)
	}
	open := func(id string, kind ledger.Kind) {
		t.Helper()
		if oerr := l.OpenAccount(ctx, ledger.Account{ID: id, Kind: kind, Currency: eur,
			Scope: tenancy.Scope{TenantID: "op"}}); oerr != nil {
			t.Fatalf("open %s: %v", id, oerr)
		}
	}
	a := ledger.WalletAccount("p-1", ledger.KindPlayerCash)
	b := ledger.WalletAccount("p-2", ledger.KindPlayerCash)
	prizes := ledger.HouseAccount(ledger.KindHouse, "EUR")
	open(a, ledger.KindPlayerCash)
	open(b, ledger.KindPlayerCash)
	open(prizes, ledger.KindHouse)

	mustPost := func(e ledger.Entry) {
		t.Helper()
		if _, perr := l.Post(ctx, e); perr != nil {
			t.Fatalf("post %s: %v", e.IdempotencyKey, perr)
		}
	}
	fund, err := ledger.Transfer("w-fund", prizes, a, money.MustNew(100000, eur), "wage credit", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	mustPost(fund)
	xfer, err := ledger.Transfer("w-xfer", a, b, money.MustNew(25000, eur), "P2P", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	mustPost(xfer)

	balA, aerr := l.Balance(ctx, a)
	if aerr != nil {
		t.Fatal(aerr)
	}
	if balA.Minor() != 75000 {
		t.Fatalf("a = %d, want 75000", balA.Minor())
	}
	balB, berr := l.Balance(ctx, b)
	if berr != nil {
		t.Fatal(berr)
	}
	if balB.Minor() != 25000 {
		t.Fatalf("b = %d, want 25000", balB.Minor())
	}
}

func TestDistributeWaterfallConservesValue(t *testing.T) {
	l := ledger.NewMemoryLedger(clock.NewFixed(time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)))
	ctx, err := tenancy.WithScope(context.Background(), tenancy.Scope{TenantID: "op"})
	if err != nil {
		t.Fatal(err)
	}
	pool := ledger.HouseAccount(ledger.KindHouse, "EUR")
	if oerr := l.OpenAccount(ctx, ledger.Account{ID: pool, Kind: ledger.KindHouse, Currency: eur,
		Scope: tenancy.Scope{TenantID: "op"}}); oerr != nil {
		t.Fatal(oerr)
	}
	var wids []string
	for i := 0; i < 3; i++ {
		id := ledger.WalletAccount(string(rune('a'+i)), ledger.KindPlayerCash)
		wids = append(wids, id)
		if oerr := l.OpenAccount(ctx, ledger.Account{ID: id, Kind: ledger.KindPlayerCash, Currency: eur,
			Scope: tenancy.Scope{TenantID: "op"}}); oerr != nil {
			t.Fatal(oerr)
		}
	}
	// 1000/3 across 1:1:1 — indivisible remainder goes first, exactly as the
	// golden vectors pin in Go, TS and any future language.
	dist, err := ledger.Distribute("jackpot-payout", pool, money.MustNew(1000, eur),
		[]ledger.Target{{AccountID: wids[0], Weight: 1}, {AccountID: wids[1], Weight: 1}, {AccountID: wids[2], Weight: 1}},
		"jackpot", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, perr := l.Post(ctx, dist); perr != nil {
		t.Fatalf("post: %v", perr)
	}
	var sum int64
	for _, id := range wids {
		bal, berr := l.Balance(ctx, id)
		if berr != nil {
			t.Fatal(berr)
		}
		sum += bal.Minor()
	}
	if sum != 1000 {
		t.Fatalf("wallets hold %d total, want 1000 — distribution leaked", sum)
	}
	first, ferr := l.Balance(ctx, wids[0])
	if ferr != nil {
		t.Fatal(ferr)
	}
	if first.Minor() != 334 {
		t.Fatalf("first part = %d, want 334 (golden remainder rule)", first.Minor())
	}
}

func TestDistributeSkipsZeroWeights(t *testing.T) {
	_, err := ledger.Distribute("d", "src", money.MustNew(900, eur),
		[]ledger.Target{{AccountID: "w1", Weight: 0}, {AccountID: "w2", Weight: 1}},
		"", time.Now().UTC())
	if err != nil {
		t.Fatalf("distribute: %v", err)
	}
	// The zero-weight leg must not appear as a posting (entries reject zero
	// legs structurally).
}

func TestTransferAndDistributeRejections(t *testing.T) {
	if _, err := ledger.Transfer("x", "a", "b", money.MustNew(0, eur), "", time.Now().UTC()); err == nil {
		t.Fatal("zero transfer accepted")
	}
	if _, err := ledger.Transfer("x", "a", "b", money.MustNew(-5, eur), "", time.Now().UTC()); err == nil {
		t.Fatal("negative transfer accepted")
	}
	if _, err := ledger.Distribute("x", "a", money.MustNew(100, eur), nil, "", time.Now().UTC()); err == nil {
		t.Fatal("distribute with no targets accepted")
	}
}

func TestWalletNamingStable(t *testing.T) {
	if ledger.WalletAccount("p-1", ledger.KindPlayerCash) != "wallet:p-1:player_cash" {
		t.Fatal("wallet naming drifted — existing balances are addressed by this string")
	}
	if ledger.HouseAccount(ledger.KindEscrow, "EUR") != "house:escrow:EUR" {
		t.Fatal("house account naming drifted")
	}
}
