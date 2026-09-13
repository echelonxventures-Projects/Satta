package ledger

import (
	"fmt"
	"time"

	"github.com/satta/platform/libs/go/money"
)

// Wallet account naming: a player's multi-wallet is one account per
// (player, kind) — cash/bonus/sweeps/social are separate accounts because
// they carry different rules (wagering requirements, refundability, and
// MGA-segregated cash vs bonus must never share a balance). The convention
// lives here, in one function, so a rule (e.g. "bonus cannot fund
// withdrawals") is always asked of the same account identity.
const walletPrefix = "wallet"

// WalletAccount returns the account ID for a player's wallet of a kind.
func WalletAccount(playerID string, kind Kind) string {
	return fmt.Sprintf("%s:%s:%s", walletPrefix, playerID, kind)
}

// HouseAccount returns the operator-side account for a currency's house leg
// (revenue, prizes payable, ...): keyed by kind+currency because those
// balances are per-currency by nature.
func HouseAccount(kind Kind, currency string) string {
	return fmt.Sprintf("house:%s:%s", kind, currency)
}

// Transfer builds the two-leg entry moving amount from one account to
// another. The amount is positive minor-unit value; direction lives in the
// signs. Callers supply the idempotency key — the business identifier that
// makes "exactly once" meaningful (a withdrawal id, a transfer id), never a
// timestamp.
func Transfer(idempotencyKey, fromAccount, toAccount string, amount money.Amount, description string, occurredAt time.Time) (Entry, error) {
	if amount.IsNegative() {
		return Entry{}, fmt.Errorf("ledger: Transfer takes a positive amount, got %d minor units", amount.Minor())
	}
	if amount.IsZero() {
		return Entry{}, fmt.Errorf("ledger: Transfer of zero moves nothing; omit the entry")
	}
	neg, err := amount.Neg()
	if err != nil {
		return Entry{}, err
	}
	e := Entry{
		IdempotencyKey: idempotencyKey,
		Description:    description,
		OccurredAt:     occurredAt.UTC(),
		Postings: []Posting{
			{AccountID: fromAccount, Amount: neg},
			{AccountID: toAccount, Amount: amount},
		},
	}
	return e, e.Validate()
}

// Target is one recipient leg of a distribution with its allocation weight.
// Zero-weight targets receive nothing and get no posting (a zero posting is
// rejected structurally, and inventing one would be noise in derived
// balances and on statements).
type Target struct {
	AccountID string
	Weight    int64
}

// Distribute builds the waterfall entry: value out of source, split across
// targets by weight, using money.Allocate — the same big.Int waterfall the
// golden vectors pin, so the split a payout service computes at 03:00 is
// byte-identical to the one the reconciliation job recomputes at 09:00.
func Distribute(idempotencyKey, sourceAccount string, amount money.Amount, targets []Target, description string, occurredAt time.Time) (Entry, error) {
	if amount.IsNegative() || amount.IsZero() {
		return Entry{}, fmt.Errorf("ledger: Distribute requires a positive amount")
	}
	weights := make([]int64, 0, len(targets))
	for _, tg := range targets {
		weights = append(weights, tg.Weight)
	}
	parts, err := amount.Allocate(weights)
	if err != nil {
		return Entry{}, fmt.Errorf("ledger: distribute %s: %w", idempotencyKey, err)
	}
	neg, err := amount.Neg()
	if err != nil {
		return Entry{}, err
	}
	e := Entry{
		IdempotencyKey: idempotencyKey,
		Description:    description,
		OccurredAt:     occurredAt.UTC(),
		Postings:       []Posting{{AccountID: sourceAccount, Amount: neg}},
	}
	for i, tg := range targets {
		if parts[i].IsZero() {
			continue
		}
		e.Postings = append(e.Postings, Posting{AccountID: tg.AccountID, Amount: parts[i]})
	}
	return e, e.Validate()
}
