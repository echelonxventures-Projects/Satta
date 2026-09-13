// Package payments is the deposit pipeline (roadmap Task 10): the PSP
// abstraction port, the deposit state machine, and the durable saga that
// connects a confirmed payment to the ledger.
//
// The rules that stop money appearing or vanishing on network retries:
//
//   - A deposit is credited by the ledger, not by arithmetic. The processor
//     said "captured"; the only way a balance moves is a balanced entry, and
//     the saga's credit activity is that one call.
//   - Every transition is idempotent by the deposit's own ID: webhooks
//     re-deliver, clients re-click, workers re-run after crashes. The state
//     machine absorbs duplicates; it never double-credits and never
//     "re-captures" a different amount under a reused key.
//   - Amount is part of the state, compared at every external step. A
//     webhook that contradicts the stored intent is a security event, not a
//     state transition.
package payments

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/satta/platform/libs/go/money"
)

// Errors of the package.
var (
	// ErrBadTransition rejects illegal state moves (and is the only answer a
	// duplicate-credit attempt gets, alongside absorbing true replays).
	ErrBadTransition = errors.New("payments: illegal transition")
	// ErrAmountMismatch marks a provider signal that contradicts the intent.
	ErrAmountMismatch = errors.New("payments: amount mismatch")
	// ErrUnknownPayment for references no deposit owns.
	ErrUnknownPayment = errors.New("payments: unknown deposit")
)

// Status of a deposit through its lifecycle.
type Status string

const (
	StatusStarted        Status = "started"         // intent recorded, nothing external yet
	StatusActionRequired Status = "action_required" // 3DS / bank redirect pending
	StatusAuthorized     Status = "authorized"      // provider holds the funds
	StatusCaptured       Status = "captured"        // provider took the funds
	StatusCredited       Status = "credited"        // ledger entry posted — value exists for the player
	StatusFailed         Status = "failed"          // terminal failure (pre-credit)
	StatusReversed       Status = "reversed"        // credit undone by a ledger reversal (or chargeback later)
)

// Terminal reports whether no forward move exists from a status.
func (s Status) Terminal() bool {
	return s == StatusCredited || s == StatusFailed || s == StatusReversed
}

// Method is the payment rail. Values are the registry's (task 3 catalog),
// the type exists so a string is not a rail identity.
type Method string

const (
	MethodCard       Method = "card"
	MethodBank       Method = "bank_transfer"
	MethodEWallet    Method = "ewallet"
	MethodUPI        Method = "upi"
	MethodNetBanking Method = "netbanking"
	MethodCrypto     Method = "crypto"
)

// Deposit is the stored state of one deposit.
type Deposit struct {
	ID            string
	TenantID      string
	PlayerID      string
	Method        Method
	Amount        money.Amount
	Status        Status
	Provider      string // processor name for the attempt actually used
	ProviderRef   string // the provider's own id, once it exists
	FailureReason string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Advance validates a transition against the machine, applies it, and
// reports whether anything changed (false + nil on a duplicate event whose
// effect is already in place).
func (d *Deposit) Advance(to Status, when time.Time) (bool, error) {
	ok := map[Status][]Status{
		StatusActionRequired: {StatusStarted},
		StatusAuthorized:     {StatusStarted, StatusActionRequired},
		// Rails legitimately capture from authorized, from started (no 3DS, no
		// separate auth step), or straight from action_required when the bank
		// redirect completes in one hop.
		StatusCaptured: {StatusAuthorized, StatusStarted, StatusActionRequired},
		StatusFailed:   {StatusStarted, StatusActionRequired, StatusAuthorized},
		StatusReversed: {StatusCaptured, StatusCredited},
		StatusCredited: {StatusCaptured},
	}[to]
	if to == d.Status {
		return false, nil // replay of the current state is absorbed
	}
	if !containsStatus(ok, d.Status) {
		return false, fmt.Errorf("%w: %s -> %s on deposit %s", ErrBadTransition, d.Status, to, d.ID)
	}
	d.Status = to
	d.UpdatedAt = when.UTC()
	return true, nil
}

func containsStatus(hay []Status, needle Status) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// PaymentRequest is what a processor receives to take money IN.
type PaymentRequest struct {
	DepositID string // the idempotency key at the provider: the same deposit id, always
	Amount    money.Amount
	Method    Method
	PlayerRef string // the player as the merchant knows them (PSP-scoped)
	ReturnURL string // 3DS redirect target
}

// Validate rejects requests that could not be honoured.
func (p PaymentRequest) Validate() error {
	if p.DepositID == "" {
		return errors.New("payments: PaymentRequest needs the DepositID as its provider idempotency key")
	}
	if p.Amount.IsZero() || p.Amount.IsNegative() {
		return errors.New("payments: PaymentRequest needs a positive amount")
	}
	if p.Method == "" {
		return errors.New("payments: PaymentRequest needs a rail")
	}
	return nil
}

// ProcessorResult is the provider's answer to Create/Query. Status is
// mapped into the payment package's own vocabulary — the state machine never
// stores a provider's raw strings, because providers change them. Amount is
// reported by Query so the service can RECONCILE the captured sum against
// the intent it stored; a mismatch is a security event, not a transition.
type ProcessorResult struct {
	ProviderRef string
	Status      Status
	Amount      *money.Amount // set by Query on captured/authorized signals
	ActionURL   string        // when StatusActionRequired
	Err         string
}

// Processor is the PSP port (DoD gate 2): every rail (card acquirer, UPI
// handler, crypto custody, bank rails) implements it; tests implement the
// contract suite. The provider webhook signature/verification is the
// adapter's job inside its implementation — the port sees verified facts.
type Processor interface {
	// Name identifies the adapter (card-acquirer-vendor-x, upi-handler-y);
	// it is stamped on the deposit for routing, disputes and reconciliation.
	Name() string
	Create(ctx context.Context, req PaymentRequest) (ProcessorResult, error)
	Query(ctx context.Context, providerRef string) (ProcessorResult, error)
}

// Store persists deposits. Implementations: memory (tests/stage-0), a
// Postgres store lands with the payments service.
type Store interface {
	Save(ctx context.Context, d Deposit) error
	Load(ctx context.Context, id string) (Deposit, error)
}

// MemoryStore is the in-process Store for tests and the local substrate.
type MemoryStore struct {
	byID map[string]Deposit
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{byID: map[string]Deposit{}} }

// Save upserts by ID (the whole record, which is fine for the in-memory
// binding; durable stores update columns under a version check).
func (m *MemoryStore) Save(_ context.Context, d Deposit) error {
	m.byID[d.ID] = d
	return nil
}

// Load implements Store.
func (m *MemoryStore) Load(_ context.Context, id string) (Deposit, error) {
	d, ok := m.byID[id]
	if !ok {
		return Deposit{}, fmt.Errorf("%w: %s", ErrUnknownPayment, id)
	}
	return d, nil
}
