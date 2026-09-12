package ledger

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/money"
	"github.com/satta/platform/libs/go/tenancy"
)

// MemoryLedger is the in-process implementation of the port — the reference
// for the contract suite and the stage-0/test binding. Its balances are
// derived (running totals maintained incrementally against the same postings
// the durable store re-derives; drift between the two is the contract
// suite's job to catch, not a cache-invalidation problem to ignore).
type MemoryLedger struct {
	clk clock.Clock

	mu       sync.Mutex
	accounts map[string]Account
	entries  []Entry
	byKey    map[string]int // scope|key -> entry index
	dirty    map[string]money.Amount
	dirtyOK  map[string]bool
}

// NewMemoryLedger returns an empty ledger stamping time from clk (or the
// system clock when nil).
func NewMemoryLedger(clk clock.Clock) *MemoryLedger {
	if clk == nil {
		clk = clock.NewSystem()
	}
	return &MemoryLedger{
		clk:      clk,
		accounts: map[string]Account{},
		byKey:    map[string]int{},
		dirty:    map[string]money.Amount{},
		dirtyOK:  map[string]bool{},
	}
}

var _ Ledger = (*MemoryLedger)(nil)

func memKey(scope tenancy.Scope, idem string) string {
	return scope.TenantID + "|" + idem
}

// OpenAccount implements Ledger.
func (m *MemoryLedger) OpenAccount(_ context.Context, acct Account) error {
	if acct.ID == "" || acct.Currency.IsZero() {
		return errors.New("ledger: account needs an ID and a currency")
	}
	if err := acct.Scope.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if prev, exists := m.accounts[acct.ID]; exists {
		if prev.Kind != acct.Kind || prev.Currency.Code != acct.Currency.Code || prev.Currency.Scale != acct.Currency.Scale {
			return fmt.Errorf("ledger: account %s already opened with a different definition", acct.ID)
		}
		return nil
	}
	m.accounts[acct.ID] = acct
	m.dirty[acct.ID] = money.Zero(acct.Currency)
	return nil
}

// Post implements Ledger. Overdraft policy for the player-side kinds is
// enforced here so every binding shares one rule: player_cash /
// player_bonus / escrow may not receive a resulting negative balance.
func (m *MemoryLedger) Post(ctx context.Context, entry Entry) (Entry, error) {
	scope, err := tenancy.RequireFromContext(ctx)
	if err != nil {
		return Entry{}, err
	}
	if err := entry.Validate(); err != nil {
		return Entry{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if i, dup := m.byKey[memKey(scope, entry.IdempotencyKey)]; dup {
		// Idempotent: the original stands, and its content must match —
		// same key, different postings is corruption, not a retry.
		prev := m.entries[i]
		if !samePostings(prev.Postings, entry.Postings) {
			return Entry{}, fmt.Errorf("%w: key %q re-posted with different postings", ErrDuplicateKey, entry.IdempotencyKey)
		}
		return prev, nil
	}

	// Two passes with deliberate order: structural validity of every posting
	// (account exists, currency and scope match) is settled before business
	// rules (overdraft) run — a malformed entry must fail as a defect even
	// when it would also have hit an overdraft, so failures are deterministic
	// and never mask a deployment bug behind a business NO.
	projections := map[string]money.Amount{}
	for _, p := range entry.Postings {
		acct, ok := m.accounts[p.AccountID]
		if !ok {
			return Entry{}, fmt.Errorf("%w: %s", ErrNoSuchAccount, p.AccountID)
		}
		if acct.Currency.Code != p.Amount.Currency().Code || acct.Currency.Scale != p.Amount.Currency().Scale {
			return Entry{}, fmt.Errorf("ledger: posting %s currency %s does not match account %s",
				p.AccountID, p.Amount.Currency().Code, acct.Currency.Code)
		}
		if acct.Scope.TenantID != scope.TenantID {
			return Entry{}, fmt.Errorf("%w: posting to account of tenant %q by tenant %q",
				tenancy.ErrScopeMismatch, acct.Scope.TenantID, scope.TenantID)
		}
	}
	for _, p := range entry.Postings {
		acct := m.accounts[p.AccountID]
		base, seen := projections[p.AccountID]
		if !seen {
			base = m.dirty[p.AccountID]
		}
		next, err := base.Add(p.Amount)
		if err != nil {
			return Entry{}, err
		}
		if next.IsNegative() && refusesNegative(acct.Kind) {
			return Entry{}, fmt.Errorf("%w: %s on %s would go negative", ErrOverdraft, p.AccountID, acct.Kind)
		}
		projections[p.AccountID] = next
	}

	stamped := entry
	stamped.ID = fmt.Sprintf("e%06d", len(m.entries)+1)
	stamped.RecordedAt = m.clk.Now().UTC()
	m.entries = append(m.entries, stamped)
	m.byKey[memKey(scope, stamped.IdempotencyKey)] = len(m.entries) - 1
	for id, amt := range projections {
		m.dirty[id] = amt
		m.dirtyOK[id] = true
	}
	return stamped, nil
}

func refusesNegative(k Kind) bool {
	switch k {
	case KindPlayerCash, KindPlayerBonus, KindEscrow:
		return true
	default:
		return false
	}
}

// Balance derives an account's balance from the (incrementally maintained)
// posting projections.
func (m *MemoryLedger) Balance(_ context.Context, accountID string) (money.Amount, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	amt, ok := m.dirty[accountID]
	if !ok {
		return money.Amount{}, fmt.Errorf("%w: account %s", ErrNoSuchAccount, accountID)
	}
	return amt, nil
}

// Entry implements Ledger.
func (m *MemoryLedger) Entry(_ context.Context, id string) (Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.entries {
		if e.ID == id {
			return e, nil
		}
	}
	return Entry{}, fmt.Errorf("%w: entry %s", ErrNotFound, id)
}

// History returns every posted entry in append order (reversal chains,
// audits and the contract suite's cross-checks read from here).
func (m *MemoryLedger) History() []Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Entry(nil), m.entries...)
}

func samePostings(a, b []Posting) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].AccountID != b[i].AccountID || a[i].Amount.Minor() != b[i].Amount.Minor() ||
			a[i].Amount.Currency().Code != b[i].Amount.Currency().Code {
			return false
		}
	}
	return true
}

// Reverse posts the negation of a prior entry citing it, the only way a
// mistake leaves the ledger: the original stays visible and the pair sums to
// zero, which is what an auditor reads. occurredAt is supplied by the caller
// (whose clock it trusts) because this package never reads the wall clock
// itself (SAFETY INVARIANT 12).
func Reverse(ctx context.Context, l Ledger, original Entry, occurredAt time.Time) (Entry, error) {
	postings := make([]Posting, 0, len(original.Postings))
	for _, p := range original.Postings {
		neg, err := p.Amount.Neg()
		if err != nil {
			return Entry{}, err
		}
		postings = append(postings, Posting{AccountID: p.AccountID, Amount: neg})
	}
	return l.Post(ctx, Entry{
		IdempotencyKey: original.IdempotencyKey + ":reverse",
		Description:    "reversal of " + original.IdempotencyKey,
		Reason:         "reverses:" + original.IdempotencyKey,
		OccurredAt:     occurredAt.UTC(),
		Postings:       postings,
	})
}
