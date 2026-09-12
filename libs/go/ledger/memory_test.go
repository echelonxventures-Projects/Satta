package ledger_test

import (
	"testing"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/ledger"
	"github.com/satta/platform/libs/go/ledger/ledgercontract"
	"github.com/satta/platform/libs/go/money"
	"github.com/satta/platform/libs/go/tenancy"

	"context"
	"time"
)

// TestMemoryLedgerContract holds the in-process binding to the port's full
// contract; the durable binding must pass the identical suite.
func TestMemoryLedgerContract(t *testing.T) {
	ledgercontract.RunContractTests(t, func(t *testing.T) ledger.Ledger {
		t.Helper()
		return ledger.NewMemoryLedger(clock.NewFixed(time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)))
	})
}

func TestReversalPairsNetToZero(t *testing.T) {
	l := ledger.NewMemoryLedger(clock.NewFixed(time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)))
	ctx, err := tenancy.WithScope(context.Background(), tenancy.Scope{TenantID: "t1"})
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	eur := money.Currency{Code: "EUR", Scale: 2, Kind: money.KindFiat}
	for _, a := range []struct {
		id   string
		kind ledger.Kind
	}{
		{"cash", ledger.KindPlayerCash},
		{"house", ledger.KindHouse},
	} {
		if oerr := l.OpenAccount(ctx, ledger.Account{ID: a.id, Kind: a.kind, Currency: eur,
			Scope: tenancy.Scope{TenantID: "t1"}}); oerr != nil {
			t.Fatalf("open %s: %v", a.id, oerr)
		}
	}
	post := func(key string, from, to string, minor int64) {
		t.Helper()
		if _, perr := l.Post(ctx, ledger.Entry{
			IdempotencyKey: key, OccurredAt: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC),
			Postings: []ledger.Posting{
				{AccountID: from, Amount: money.MustNew(-minor, eur)},
				{AccountID: to, Amount: money.MustNew(minor, eur)},
			},
		}); perr != nil {
			t.Fatalf("post %s: %v", key, perr)
		}
	}
	post("fund", "house", "cash", 700)
	original, err := l.Entry(ctx, "e000001")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, rerr := ledger.Reverse(ctx, l, original, time.Date(2026, 9, 11, 1, 0, 0, 0, time.UTC)); rerr != nil {
		t.Fatalf("reverse: %v", rerr)
	}
	b, berr := l.Balance(ctx, "cash")
	if berr != nil {
		t.Fatalf("balance: %v", berr)
	}
	if b.Minor() != 0 {
		t.Fatalf("cash after reversal = %d, want 0", b.Minor())
	}
	// The mistake remains visible: the funding and its reversal both stand.
	if hist := l.History(); len(hist) != 2 {
		t.Fatalf("history = %d entries, want 2 (append-only corrections)", len(hist))
	}
}
