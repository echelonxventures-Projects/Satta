// Package ledger is the platform's double-entry core (roadmap Task 7):
// SAFETY INVARIANT 1's enforcement point — the one place money appears,
// moves and is proved, in integer minor units, always balanced.
//
// The conventions, deliberately stated because every wrong one is a
// reconciliation nightmare:
//
//   - An entry is a set of postings (signed amounts) whose sum is ZERO per
//     currency. That is double-entry in the form auditors actually verify:
//     "every value leaving one account arrived in another". Debit/credit
//     vocabulary is omitted on purpose — signed postings per account mean
//     one convention instead of two, and a bug cannot hide in the gap.
//   - Cross-currency balancing is NOT this package's job (FX conversion is
//     Task 8): an entry that mixes currencies must balance WITHIN each
//     currency, never net across them.
//   - Entries are append-only forever (same posture as the evidence store):
//     a wrong posting is answered by a reversing entry that cites the
//     original, so the mistake stays visible. A ledger you can edit is a
//     ledger no regulator can audit. Idempotency keys make crash retries of
//     the same transfer post once — the one deletion-adjacent operation
//     (de-duplicate) that safety requires is handled as refusal, not edit.
//   - Balances are DERIVED from postings. No balance column exists to drift
//     from the journal; the journal is the truth and caches sit above it.
package ledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/satta/platform/libs/go/money"
	"github.com/satta/platform/libs/go/tenancy"
)

// Errors of the package.
var (
	// ErrUnbalanced is the central invariant: postings did not sum to zero
	// (per currency). Returned on construction of the entry, before anything
	// touches storage.
	ErrUnbalanced = errors.New("ledger: entry does not balance")
	// ErrNoSuchAccount marks posting to an account the registry never
	// opened: there is no suspense account to hide value in.
	ErrNoSuchAccount = errors.New("ledger: unknown account")
	// ErrDuplicateKey is returned only by strict re-post attempts; the
	// normal idempotent Post returns the original entry instead.
	ErrDuplicateKey = errors.New("ledger: idempotency key already used")
	// ErrNotFound for unknown entry/account reads.
	ErrNotFound = errors.New("ledger: not found")
	// ErrOverdraft is a business denial: the account type refuses a
	// resulting negative balance. Distinct from system errors so a payer
	// sees NO and a broken deployment still breaks visibly.
	ErrOverdraft = errors.New("ledger: overdraft refused")
)

// Kind classifies accounts by what may legitimately hold a negative or the
// rules on movement. The values mirror money.Currency's kinds plus the
// house-side roles every operator ledger needs.
type Kind string

const (
	KindPlayerCash  Kind = "player_cash"  // refundable player balance
	KindPlayerBonus Kind = "player_bonus" // wagering-bound; cannot go negative
	KindHouse       Kind = "house"        // operator revenue side
	KindEscrow      Kind = "escrow"       // segregated funds (MGA player funds)
	KindSettle      Kind = "settlement"   // payout/batch clearing side
)

// Account is one immutable-ish ledger identity: it never changes kind or
// currency after opening (a new account replaces it; a correction never
// rewrites the history of one).
type Account struct {
	ID       string
	Kind     Kind
	Currency money.Currency
	Scope    tenancy.Scope
}

// Posting is one signed movement in an entry.
type Posting struct {
	AccountID string
	Amount    money.Amount // signed; currency must match the account's
}

// Entry is an immutable balanced group of postings with attribution.
type Entry struct {
	// ID assigned by the store on append (stable across reads).
	ID string
	// IdempotencyKey: required, unique per scope. The key is usually the
	// business identifier (withdrawal id + leg), so "post once" follows from
	// "the withdrawal is one thing".
	IdempotencyKey string
	Description    string
	OccurredAt     time.Time // UTC (INVARIANT 12)
	RecordedAt     time.Time // store-assigned
	Reason         string    // for reversals: "reverses:<original-key>" etc.
	Postings       []Posting
}

// Validate checks structure — including the invariant — before the store sees
// anything. It cannot fail "close enough": a cent missing is a bug, and a
// cent extra is a bug with compliance consequences.
func (e Entry) Validate() error {
	if e.IdempotencyKey == "" {
		return errors.New("ledger: entries require an idempotency key")
	}
	if e.OccurredAt.IsZero() {
		return errors.New("ledger: occurred_at is required")
	}
	if len(e.Postings) < 2 {
		return fmt.Errorf("%w: an entry with %d postings cannot balance two accounts", ErrUnbalanced, len(e.Postings))
	}
	seen := map[string]bool{}
	// Accumulation rides money.Add, so the overflow rule is the same one the
	// money package already enforces — not a second implementation to drift.
	balanceByCurrency := map[string]money.Amount{}
	for i, p := range e.Postings {
		if p.AccountID == "" {
			return fmt.Errorf("ledger: posting %d names no account", i)
		}
		if seen[p.AccountID] {
			return fmt.Errorf("%w: posting %d repeats account %s in one entry — merge the amounts", ErrUnbalanced, i, p.AccountID)
		}
		seen[p.AccountID] = true
		sum, ok := balanceByCurrency[p.Amount.Currency().Code]
		if !ok {
			sum = money.Zero(p.Amount.Currency())
			balanceByCurrency[p.Amount.Currency().Code] = sum
		}
		next, err := sum.Add(p.Amount)
		if err != nil {
			return fmt.Errorf("ledger: entry postings overflow int64 while netting %s: %w", p.Amount.Currency().Code, err)
		}
		balanceByCurrency[p.Amount.Currency().Code] = next
	}
	for code, sum := range balanceByCurrency {
		if !sum.IsZero() {
			return fmt.Errorf("%w: currency %s nets to %d minor units, want zero", ErrUnbalanced, code, sum.Minor())
		}
	}
	return nil
}

// Ledger is the append-only port.
type Ledger interface {
	// Post appends a balanced entry. Idempotent on (scope, IdempotencyKey):
	// re-issue returns the original entry (not an error) — never a second
	// posting. Implementations validate before writing anything.
	Post(ctx context.Context, entry Entry) (Entry, error)

	// OpenAccount registers an account before any posting may touch it.
	// Re-opening the same ID with an identical definition is a no-op; with a
	// different one it is an error (accounts never mutate).
	OpenAccount(ctx context.Context, acct Account) error

	// Balance derives the current balance of an account from its postings.
	Balance(ctx context.Context, accountID string) (money.Amount, error)

	// Entry reads one posted entry by id or ErrNotFound.
	Entry(ctx context.Context, id string) (Entry, error)
}
