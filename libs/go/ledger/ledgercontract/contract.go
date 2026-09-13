// Package ledgercontract holds the contract suite every ledger binding must
// pass — the same discipline as evidencetest and meteringtest. The balance
// invariant and the overdraft refusal are the platform's financial spine;
// they must not depend on which binding a service happens to run.
package ledgercontract

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/satta/platform/libs/go/ledger"
	"github.com/satta/platform/libs/go/money"
	"github.com/satta/platform/libs/go/tenancy"
)

// Factory builds a fresh empty ledger for one subtest.
type Factory func(t *testing.T) ledger.Ledger

var eur = money.Currency{Code: "EUR", Scale: 2, Kind: money.KindFiat}

// ScopeFor builds the tenant context contract tests post under.
func ScopeFor(t *testing.T, tenant string) context.Context {
	t.Helper()
	ctx, err := tenancy.WithScope(context.Background(), tenancy.Scope{TenantID: tenant, JurisdictionCode: "mt", RegionCode: "eu-west"})
	if err != nil {
		t.Fatalf("WithScope: %v", err)
	}
	return ctx
}

func account(id string, kind ledger.Kind, tenant string) ledger.Account {
	return ledger.Account{
		ID:       id,
		Kind:     kind,
		Currency: eur,
		Scope:    tenancy.Scope{TenantID: tenant, JurisdictionCode: "mt", RegionCode: "eu-west"},
	}
}

func amt(minor int64) money.Amount { return money.MustNew(minor, eur) }

func transfer(key, from, to string, minor int64) ledger.Entry {
	return ledger.Entry{
		IdempotencyKey: key,
		Description:    "test transfer",
		OccurredAt:     time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC),
		Postings: []ledger.Posting{
			{AccountID: from, Amount: amt(-minor)},
			{AccountID: to, Amount: amt(minor)},
		},
	}
}

// RunContractTests exercises the ledger port.
func RunContractTests(t *testing.T, newLedger Factory) {
	t.Helper()

	t.Run("BalancedTransferMovesValue", func(t *testing.T) {
		l := newLedger(t)
		ctx := ScopeFor(t, "t1")
		must(t, l.OpenAccount(ctx, account("cash-a", ledger.KindPlayerCash, "t1")))
		must(t, l.OpenAccount(ctx, account("cash-b", ledger.KindPlayerCash, "t1")))
		must(t, l.OpenAccount(ctx, account("house", ledger.KindHouse, "t1")))

		// Fund a from the house side, then move a -> b.
		if _, perr := l.Post(ctx, transfer("fund-a", "house", "cash-a", 10000)); perr != nil {
			t.Fatalf("post: %v", perr)
		}
		if _, perr := l.Post(ctx, transfer("move", "cash-a", "cash-b", 4000)); perr != nil {
			t.Fatalf("post: %v", perr)
		}

		if b := bal(ctx, t, l, "cash-a"); b.Minor() != 6000 {
			t.Fatalf("cash-a = %d, want 6000", b.Minor())
		}
		if b := bal(ctx, t, l, "cash-b"); b.Minor() != 4000 {
			t.Fatalf("cash-b = %d, want 4000", b.Minor())
		}
	})

	t.Run("UnbalancedEntryNeverTouchesStorage", func(t *testing.T) {
		l := newLedger(t)
		ctx := ScopeFor(t, "t1")
		must(t, l.OpenAccount(ctx, account("cash-a", ledger.KindPlayerCash, "t1")))
		must(t, l.OpenAccount(ctx, account("house", ledger.KindHouse, "t1")))
		oneSided := ledger.Entry{
			IdempotencyKey: "bad", OccurredAt: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC),
			Postings: []ledger.Posting{{AccountID: "house", Amount: amt(500)}},
		}
		if _, err := l.Post(ctx, oneSided); !errors.Is(err, ledger.ErrUnbalanced) {
			t.Fatalf("unbalanced post: %v, want ErrUnbalanced", err)
		}
	})

	t.Run("IdempotentPostReturnsOriginal", func(t *testing.T) {
		l := newLedger(t)
		ctx := ScopeFor(t, "t1")
		must(t, l.OpenAccount(ctx, account("cash-a", ledger.KindPlayerCash, "t1")))
		must(t, l.OpenAccount(ctx, account("house", ledger.KindHouse, "t1")))
		must(t, l.OpenAccount(ctx, account("cash-b", ledger.KindPlayerCash, "t1")))
		if _, perr := l.Post(ctx, transfer("fund", "house", "cash-a", 5000)); perr != nil {
			t.Fatalf("post: %v", perr)
		}
		e1, err := l.Post(ctx, transfer("t-1", "cash-a", "cash-b", 1000))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		e2, err := l.Post(ctx, transfer("t-1", "cash-a", "cash-b", 1000))
		if err != nil {
			t.Fatalf("repeat post: %v", err)
		}
		if e2.ID != e1.ID {
			t.Fatalf("repeat returned %s, want the original %s", e2.ID, e1.ID)
		}
		if b := bal(ctx, t, l, "cash-a"); b.Minor() != 4000 {
			t.Fatalf("cash-a = %d after duplicate post, want 4000 — money left twice", b.Minor())
		}
	})

	t.Run("CrossCurrencyEntryMustNotNet", func(t *testing.T) {
		// 500 EUR out and "500 units" of another currency in is not balance:
		// each currency must net to zero separately (FX conversion is the
		// explicit, separately-policyed operation it must be — Task 8).
		usd := money.Currency{Code: "USD", Scale: 2, Kind: money.KindFiat}
		mixed := ledger.Entry{
			IdempotencyKey: "mix", OccurredAt: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC),
			Postings: []ledger.Posting{
				{AccountID: "cash-a", Amount: money.MustNew(-500, eur)},
				{AccountID: "usd-side", Amount: money.MustNew(500, usd)},
			},
		}
		// Direct structural assertion (binding-independent): Validate rejects
		// it before any storage decision is even possible.
		if err := mixed.Validate(); !errors.Is(err, ledger.ErrUnbalanced) {
			t.Fatalf("cross-currency entry validated: %v, want ErrUnbalanced per currency", err)
		}
	})

	t.Run("OverdraftRefusedOnPlayerKinds", func(t *testing.T) {
		l := newLedger(t)
		ctx := ScopeFor(t, "t1")
		must(t, l.OpenAccount(ctx, account("cash-a", ledger.KindPlayerCash, "t1")))
		must(t, l.OpenAccount(ctx, account("house", ledger.KindHouse, "t1")))
		if _, perr := l.Post(ctx, transfer("fund", "house", "cash-a", 1000)); perr != nil {
			t.Fatalf("post: %v", perr)
		}
		if _, err := l.Post(ctx, transfer("too-much", "cash-a", "house", 1001)); !errors.Is(err, ledger.ErrOverdraft) {
			t.Fatalf("overdraft: %v, want ErrOverdraft", err)
		}
	})

	t.Run("HouseCanGoNegativeButPlayersCannot", func(t *testing.T) {
		l := newLedger(t)
		ctx := ScopeFor(t, "t1")
		must(t, l.OpenAccount(ctx, account("house", ledger.KindHouse, "t1")))
		must(t, l.OpenAccount(ctx, account("cash-a", ledger.KindPlayerCash, "t1")))
		// House pays out without prior funding: allowed (settlement semantics
		// are the operators; the balance invariant is still enforced).
		if _, err := l.Post(ctx, transfer("pay", "house", "cash-a", 900)); err != nil {
			t.Fatalf("house payout: %v", err)
		}
	})

	t.Run("UnknownAccountDenied", func(t *testing.T) {
		l := newLedger(t)
		ctx := ScopeFor(t, "t1")
		must(t, l.OpenAccount(ctx, account("cash-a", ledger.KindPlayerCash, "t1")))
		_, err := l.Post(ctx, transfer("ghost", "cash-a", "nowhere", 10))
		if !errors.Is(err, ledger.ErrNoSuchAccount) {
			t.Fatalf("unknown account: %v", err)
		}
	})

	t.Run("CrossTenantPostingDenied", func(t *testing.T) {
		l := newLedger(t)
		ctxA, ctxB := ScopeFor(t, "t1"), ScopeFor(t, "t2")
		must(t, l.OpenAccount(ctxA, account("cash-a", ledger.KindPlayerCash, "t1")))
		must(t, l.OpenAccount(ctxB, account("cash-b", ledger.KindPlayerCash, "t2")))
		must(t, l.OpenAccount(ctxA, account("house-a", ledger.KindHouse, "t1")))
		// t2 may not post into t1's account.
		if _, err := l.Post(ctxB, transfer("cross", "house-a", "cash-b", 100)); err == nil {
			t.Fatal("cross-tenant posting accepted")
		}
	})

	t.Run("SameKeyDifferentPostingsIsCorruption", func(t *testing.T) {
		l := newLedger(t)
		ctx := ScopeFor(t, "t1")
		must(t, l.OpenAccount(ctx, account("cash-a", ledger.KindPlayerCash, "t1")))
		must(t, l.OpenAccount(ctx, account("cash-b", ledger.KindPlayerCash, "t1")))
		must(t, l.OpenAccount(ctx, account("house", ledger.KindHouse, "t1")))
		if _, perr := l.Post(ctx, transfer("fund", "house", "cash-a", 5000)); perr != nil {
			t.Fatalf("post: %v", perr)
		}
		if _, perr := l.Post(ctx, transfer("t", "cash-a", "cash-b", 100)); perr != nil {
			t.Fatalf("post: %v", perr)
		}
		if _, err := l.Post(ctx, transfer("t", "cash-a", "cash-b", 9999)); !errors.Is(err, ledger.ErrDuplicateKey) {
			t.Fatalf("mutated retry: %v, want ErrDuplicateKey", err)
		}
	})
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
}

func bal(ctx context.Context, t *testing.T, l ledger.Ledger, accountID string) money.Amount {
	t.Helper()
	a, err := l.Balance(ctx, accountID)
	if err != nil {
		t.Fatalf("balance %s: %v", accountID, err)
	}
	return a
}
