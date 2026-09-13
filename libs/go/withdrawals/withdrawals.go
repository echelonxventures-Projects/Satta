// Package withdrawals is the payout pipeline (roadmap Task 11): the request
// state machine, approval gates (KYC/limits via the access policy), the
// payout processor port, batching, and reversal handling — all money moves
// through the ledger, and the durable flow through the orchestration port.
//
// The posture, stated once: a withdrawal is the inverse of a deposit in the
// direction that matters — value leaves. The state machine therefore holds
// the player's cash FIRST (the hold is the ledger debit; if the balance
// cannot fund it, the withdrawal is refused before a processor is ever
// asked), and every later step can be COMPENSATED by reversing that hold.
package withdrawals

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/satta/platform/libs/go/access"
	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/ledger"
	"github.com/satta/platform/libs/go/money"
	"github.com/satta/platform/libs/go/payments"
	"github.com/satta/platform/libs/go/rules"
	"github.com/satta/platform/libs/go/tenancy"
)

// Errors.
var (
	ErrBadTransition = errors.New("withdrawals: illegal transition")
	ErrApproval      = errors.New("withdrawals: approval required")
	ErrNotFound      = errors.New("withdrawals: unknown withdrawal")
	// ErrAmountChanged guards confirmations against provider drift.
	ErrAmountChanged = errors.New("withdrawals: provider amount differs")
)

// Status lifecycle.
type Status string

const (
	StatusRequested  Status = "requested"  // recorded, not held
	StatusHeld       Status = "held"       // player cash debited into payout transit
	StatusReview     Status = "review"     // over the auto-approve threshold
	StatusApproved   Status = "approved"   // gates passed, human approval if required
	StatusPaid       Status = "paid"       // processor accepted the payout
	StatusConfirmed  Status = "confirmed"  // provider reports settled
	StatusRejected   Status = "rejected"   // declined by a gate; hold reversed
	StatusFailed     Status = "failed"     // processor failed post-approval; hold reversed
	StatusReinstated Status = "reinstated" // provider reversed the payout; funds returned
)

// Terminal reports closed states.
func (s Status) Terminal() bool {
	return s == StatusConfirmed || s == StatusRejected || s == StatusFailed || s == StatusReinstated
}

// Withdrawal state.
type Withdrawal struct {
	ID          string
	TenantID    string
	PlayerID    string
	Method      payments.Method
	Amount      money.Amount
	Status      Status
	ProviderRef string
	Approver    string
	Reason      string
	SubmittedAt time.Time
	UpdatedAt   time.Time
}

// Advance validates a transition against the machine (like payments).
func (w *Withdrawal) Advance(to Status, when time.Time) (bool, error) {
	edges := map[Status][]Status{
		StatusHeld:       {StatusRequested},
		StatusReview:     {StatusHeld},
		StatusApproved:   {StatusHeld, StatusReview},
		StatusPaid:       {StatusApproved},
		StatusConfirmed:  {StatusPaid},
		StatusRejected:   {StatusRequested, StatusHeld, StatusReview},
		StatusFailed:     {StatusHeld, StatusApproved, StatusReview, StatusPaid},
		StatusReinstated: {StatusConfirmed},
	}
	if to == w.Status {
		return false, nil
	}
	if !contains(edges[to], w.Status) {
		return false, fmt.Errorf("%w: %s -> %s on %s", ErrBadTransition, w.Status, to, w.ID)
	}
	w.Status = to
	w.UpdatedAt = when.UTC()
	return true, nil
}

func contains(hay []Status, needle Status) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// PayoutRequest carries the payout instruction; ID is the provider-side
// idempotency key, so a retried pay cannot send money twice.
type PayoutRequest struct {
	WithdrawalID string
	Amount       money.Amount
	Method       payments.Method
	PlayerRef    string
}

// PayoutResult is the processor's answer.
type PayoutResult struct {
	ProviderRef string
	Status      Status // paid or confirmed, else failed
	Amount      *money.Amount
	Err         string
}

// PayoutProcessor is the payout rail port (PSP payouts, UPI payout, crypto
// transfer). Same verification posture as deposits: Confirm reconciles
// against Query; provider amounts that drift from the hold are an error,
// never a top-up.
type PayoutProcessor interface {
	Name() string
	Pay(ctx context.Context, req PayoutRequest) (PayoutResult, error)
	Query(ctx context.Context, providerRef string) (PayoutResult, error)
	// Reconcile answers "did you execute this payout id?" — the ONLY
	// authority on whether a failed Pay attempt moved money.
	Reconcile(ctx context.Context, req PayoutRequest) (PayoutResult, error)
}

// Store persists withdrawals.
type Store interface {
	Save(ctx context.Context, w Withdrawal) error
	Load(ctx context.Context, id string) (Withdrawal, error)
	ListApprovedForBatch(ctx context.Context, method payments.Method, currency string) ([]Withdrawal, error)
}

// MemoryStore implements Store in-process.
type MemoryStore struct {
	byID map[string]Withdrawal
}

// NewMemoryStore returns empty.
func NewMemoryStore() *MemoryStore { return &MemoryStore{byID: map[string]Withdrawal{}} }

// Save upserts.
func (m *MemoryStore) Save(_ context.Context, w Withdrawal) error {
	m.byID[w.ID] = w
	return nil
}

// Load implements Store.
func (m *MemoryStore) Load(_ context.Context, id string) (Withdrawal, error) {
	w, ok := m.byID[id]
	if !ok {
		return Withdrawal{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return w, nil
}

// ListApprovedForBatch implements Store (scan; durable stores index).
func (m *MemoryStore) ListApprovedForBatch(_ context.Context, method payments.Method, currency string) ([]Withdrawal, error) {
	var out []Withdrawal
	for _, w := range m.byID {
		if w.Status == StatusApproved && w.Method == method && w.Amount.Currency().Code == currency {
			out = append(out, w)
		}
	}
	// Deterministic batch order (map iteration is not): by ID.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ID < out[j-1].ID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// Gate decides whether a withdrawal may be auto-approved, must be reviewed,
// or must be rejected — evaluated against policy facts, so it is data.
// Facts include "amount_minor", "kyc_level", "withdrawals_24h".
type Gate interface {
	Evaluate(ctx context.Context, w Withdrawal) (decision Decision, err error)
}

// Decision from a Gate.
type Decision int

const (
	// DecisionPass: proceed (auto-approved within limits).
	DecisionPass Decision = iota
	// DecisionReview: require a human approval step.
	DecisionReview
	// DecisionDeny: refuse; funds never held or immediately released.
	DecisionDeny
)

// PolicyGate composes rules.Node conditions over withdrawal facts (the same
// engine that gates authorisation — one rule language platform-wide).
type PolicyGate struct {
	deny   *rules.Node
	review *rules.Node
}

// NewPolicyGate validates both trees at construction.
func NewPolicyGate(deny, review *rules.Node) (*PolicyGate, error) {
	if deny != nil {
		if err := deny.Validate(); err != nil {
			return nil, err
		}
	}
	if review != nil {
		if err := review.Validate(); err != nil {
			return nil, err
		}
	}
	return &PolicyGate{deny: deny, review: review}, nil
}

// Evaluate implements Gate: deny wins, then review, else pass.
func (p *PolicyGate) Evaluate(ctx context.Context, w Withdrawal) (Decision, error) {
	facts := map[string]rules.Fact{
		"amount_minor": rules.IntFact(w.Amount.Minor()),
		"method":       rules.StringFact(string(w.Method)),
	}
	if v, ok := ctx.Value(FactsKey{}).(map[string]rules.Fact); ok {
		for k, f := range v {
			facts[k] = f
		}
	}
	if p.deny != nil {
		hit, err := rules.Evaluate(p.deny, facts)
		if err != nil {
			return DecisionDeny, err
		}
		if hit {
			return DecisionDeny, nil
		}
	}
	if p.review != nil {
		hit, err := rules.Evaluate(p.review, facts)
		if err != nil {
			return DecisionReview, err
		}
		if hit {
			return DecisionReview, nil
		}
	}
	return DecisionPass, nil
}

// FactsKey carries extra evaluation facts (kyc_level etc.) in context; the
// service that knows the player injects them at request time.
type FactsKey struct{}

// Service is the withdrawal pipeline.
type Service struct {
	store      Store
	ledger     ledger.Ledger
	processors map[payments.Method]PayoutProcessor
	gate       Gate
	approver   *access.Policy
	clk        clock.Clock
}

// NewService requires everything; review thresholds are policy (the Gate),
// not constructor numbers.
func NewService(store Store, lgr ledger.Ledger, gate Gate, approverPolicy *access.Policy, clk clock.Clock) (*Service, error) {
	if store == nil || lgr == nil || gate == nil || clk == nil {
		return nil, errors.New("withdrawals: Service needs store, ledger, gate, clock")
	}
	return &Service{
		store: store, ledger: lgr, gate: gate, approver: approverPolicy,
		processors: map[payments.Method]PayoutProcessor{}, clk: clk,
	}, nil
}

// RegisterPayoutProcessor binds a rail (boot).
func (s *Service) RegisterPayoutProcessor(m payments.Method, p PayoutProcessor) {
	s.processors[m] = p
}

// Request evaluates the gate and, if allowed, posts the hold debit
// (player cash -> payout transit). Insufficient balance fails at the ledger
// (ErrOverdraft), which is the honest answer: there is no money to send.
func (s *Service) Request(ctx context.Context, id, player string, amount money.Amount, method payments.Method) (Withdrawal, error) {
	if id == "" || amount.IsZero() || amount.IsNegative() {
		return Withdrawal{}, errors.New("withdrawals: id and positive amount required")
	}
	scope, serr := tenancy.RequireFromContext(ctx)
	if serr != nil {
		return Withdrawal{}, serr
	}
	w := Withdrawal{
		ID: id, TenantID: scope.TenantID, PlayerID: player, Method: method, Amount: amount,
		Status: StatusRequested, SubmittedAt: s.clk.Now().UTC(), UpdatedAt: s.clk.Now().UTC(),
	}
	decision, err := s.gate.Evaluate(ctx, w)
	if err != nil {
		return Withdrawal{}, err
	}
	switch decision {
	case DecisionPass:
		// fall through to hold + auto-approve below
	case DecisionDeny:
		w.Status = StatusRejected
		w.Reason = "gate denied"
		if serr := s.store.Save(ctx, w); serr != nil {
			return Withdrawal{}, serr
		}
		return w, nil
	case DecisionReview:
		// Held first, then reviewed: a review that takes hours must not be
		// racing the player's spendable balance for the funds it might
		// release.
	}
	if err := s.hold(ctx, &w); err != nil {
		return Withdrawal{}, err
	}
	if decision == DecisionReview {
		if aerr := s.advance(ctx, w.ID, StatusReview, ""); aerr != nil {
			return Withdrawal{}, aerr
		}
		return s.mustLoad(ctx, w.ID)
	}
	if aerr := s.advance(ctx, w.ID, StatusApproved, "auto-approved within policy"); aerr != nil {
		return Withdrawal{}, aerr
	}
	return s.mustLoad(ctx, w.ID)
}

// Approve is the human step. role is the approver's resolved role (IAM
// session did that upstream — this function never trusts a caller-claimed
// role string without checking it): it must hold withdrawal.approve in the
// access policy, and only a review-status withdrawal moves.
func (s *Service) Approve(ctx context.Context, id, role, approverID string, facts map[string]rules.Fact) (Withdrawal, error) {
	if s.approver == nil {
		return Withdrawal{}, errors.New("withdrawals: no approval policy configured")
	}
	if err := s.approver.Check(role, "withdrawal.approve", facts); err != nil {
		return Withdrawal{}, fmt.Errorf("withdrawals: approver %s not permitted: %w", approverID, err)
	}
	w, err := s.store.Load(ctx, id)
	if err != nil {
		return Withdrawal{}, err
	}
	if w.Status != StatusReview {
		return Withdrawal{}, fmt.Errorf("%w: approve from %s", ErrBadTransition, w.Status)
	}
	w.Approver = approverID
	if serr := s.store.Save(ctx, w); serr != nil {
		return Withdrawal{}, serr
	}
	if aerr := s.advance(ctx, id, StatusApproved, "approved by "+approverID); aerr != nil {
		return Withdrawal{}, aerr
	}
	return s.mustLoad(ctx, id)
}

// Pay sends the money: processor Pay, then paid status. Idempotent by
// provider-side key (the withdrawal ID).
func (s *Service) Pay(ctx context.Context, id string) (Withdrawal, error) {
	w, err := s.store.Load(ctx, id)
	if err != nil {
		return Withdrawal{}, err
	}
	if w.Status == StatusPaid || w.Status == StatusConfirmed {
		return w, nil
	}
	if w.Status != StatusApproved {
		return Withdrawal{}, fmt.Errorf("%w: pay from %s", ErrBadTransition, w.Status)
	}
	proc, ok := s.processors[w.Method]
	if !ok {
		return Withdrawal{}, fmt.Errorf("withdrawals: no payout processor for %q", w.Method)
	}
	req := PayoutRequest{WithdrawalID: id, Amount: w.Amount, Method: w.Method, PlayerRef: w.PlayerID}
	res, perr := proc.Pay(ctx, req)
	if perr != nil {
		// The classic double-payout trap: a network error does NOT mean the
		// provider didn't pay. Reconcile the idempotency key before touching
		// the hold — release only for a payout the provider denies.
		rec, rerr := proc.Reconcile(ctx, req)
		if rerr != nil {
			// Cannot verify: leave HELD (not released, not paid) — money
			// neither moves nor frees on unverifiable state; a human
			// reconciles. Failing open here would pay twice on retry.
			return w, fmt.Errorf("withdrawals: pay unverified for %s: pay=%w reconcile=%w", id, perr, rerr)
		}
		if rec.Status == StatusPaid || rec.Status == StatusConfirmed {
			res = rec
		} else {
			if ferr := s.failAndRelease(ctx, id, "processor error: "+perr.Error()); ferr != nil {
				return Withdrawal{}, ferr
			}
			return s.mustLoad(ctx, id)
		}
	}
	if res.Status != StatusPaid && res.Status != StatusConfirmed {
		if ferr := s.failAndRelease(ctx, id, res.Err); ferr != nil {
			return Withdrawal{}, ferr
		}
		return s.mustLoad(ctx, id)
	}
	w.ProviderRef = res.ProviderRef
	if serr := s.store.Save(ctx, w); serr != nil {
		return Withdrawal{}, serr
	}
	if aerr := s.advance(ctx, id, StatusPaid, ""); aerr != nil {
		return Withdrawal{}, aerr
	}
	if res.Status == StatusConfirmed {
		if cerr := s.advance(ctx, id, StatusConfirmed, "provider reported settled inline"); cerr != nil {
			return Withdrawal{}, cerr
		}
	}
	return s.mustLoad(ctx, id)
}

// Confirm verifies the provider's view (reconcile-then-advance), mirroring
// deposits: the stored intent is the source of truth.
func (s *Service) Confirm(ctx context.Context, id string) (Withdrawal, error) {
	w, err := s.store.Load(ctx, id)
	if err != nil {
		return Withdrawal{}, err
	}
	if w.Status == StatusConfirmed {
		return w, nil
	}
	if w.Status != StatusPaid {
		return Withdrawal{}, fmt.Errorf("%w: confirm from %s", ErrBadTransition, w.Status)
	}
	proc, ok := s.processors[w.Method]
	if !ok {
		return Withdrawal{}, fmt.Errorf("withdrawals: rail unregistered after pay")
	}
	q, qerr := proc.Query(ctx, w.ProviderRef)
	if qerr != nil {
		return Withdrawal{}, fmt.Errorf("withdrawals: confirm query: %w", qerr)
	}
	if q.Amount != nil && q.Status == StatusConfirmed && q.Amount.Minor() != w.Amount.Minor() {
		if ferr := s.failAndRelease(ctx, id, fmt.Sprintf("provider amount drift: %d vs %d", q.Amount.Minor(), w.Amount.Minor())); ferr != nil {
			return Withdrawal{}, ferr
		}
		return Withdrawal{}, fmt.Errorf("%w: %d vs intent %d", ErrAmountChanged, q.Amount.Minor(), w.Amount.Minor())
	}
	if aerr := s.advance(ctx, id, StatusConfirmed, ""); aerr != nil {
		return Withdrawal{}, aerr
	}
	return s.mustLoad(ctx, id)
}

// Reject releases a not-yet-paid withdrawal back to the player (the hold
// reversal). After Pay, the rail has the money: that path is Reinstated by
// provider signal, not local rejection.
func (s *Service) Reject(ctx context.Context, id, reason string) (Withdrawal, error) {
	w, err := s.store.Load(ctx, id)
	if err != nil {
		return Withdrawal{}, err
	}
	if w.Status != StatusReview && w.Status != StatusApproved {
		return Withdrawal{}, fmt.Errorf("%w: reject from %s", ErrBadTransition, w.Status)
	}
	if err := s.release(ctx, id, "rejected: "+reason); err != nil {
		return Withdrawal{}, err
	}
	return s.mustLoad(ctx, id)
}

// hold posts the debit from cash into transit and marks held.
func (s *Service) hold(ctx context.Context, w *Withdrawal) error {
	cur := w.Amount.Currency()
	transit := TransitAccount(w.Method, cur)
	scope := scopeOf(*w)
	if oerr := s.ledger.OpenAccount(ctx, ledger.Account{ID: transit, Kind: ledger.KindEscrow, Currency: cur, Scope: scope}); oerr != nil {
		return oerr
	}
	from := ledger.WalletAccount(w.PlayerID, ledger.KindPlayerCash)
	e, terr := ledger.Transfer("withdraw-hold:"+w.ID, from, transit, w.Amount, "withdrawal hold "+w.ID, w.SubmittedAt)
	if terr != nil {
		return terr
	}
	if _, perr := s.ledger.Post(ctx, e); perr != nil {
		return perr
	}
	if serr := s.store.Save(ctx, *w); serr != nil {
		return serr
	}
	return s.advance(ctx, w.ID, StatusHeld, "")
}

// release reverses the hold (transit -> cash) and marks rejected.
func (s *Service) release(ctx context.Context, id, reason string) error {
	w, err := s.store.Load(ctx, id)
	if err != nil {
		return err
	}
	cur := w.Amount.Currency()
	transit := TransitAccount(w.Method, cur)
	to := ledger.WalletAccount(w.PlayerID, ledger.KindPlayerCash)
	e, terr := ledger.Transfer("withdraw-release:"+id, transit, to, w.Amount, "release "+reason, s.clk.Now())
	if terr != nil {
		return terr
	}
	if _, perr := s.ledger.Post(ctx, e); perr != nil {
		return perr
	}
	if aerr := s.advance(ctx, id, StatusRejected, reason); aerr != nil {
		return aerr
	}
	return nil
}

func (s *Service) failAndRelease(ctx context.Context, id, reason string) error {
	w, err := s.store.Load(ctx, id)
	if err != nil {
		return err
	}
	cur := w.Amount.Currency()
	transit := TransitAccount(w.Method, cur)
	to := ledger.WalletAccount(w.PlayerID, ledger.KindPlayerCash)
	e, terr := ledger.Transfer("withdraw-fail:"+id, transit, to, w.Amount, "failed: "+reason, s.clk.Now())
	if terr != nil {
		return terr
	}
	if _, perr := s.ledger.Post(ctx, e); perr != nil {
		return perr
	}
	return s.advance(ctx, id, StatusFailed, reason)
}

// TransitAccount names the payout-transit ledger account (funds the player
// no longer controls, the provider has not yet paid).
func TransitAccount(m payments.Method, cur money.Currency) string {
	return fmt.Sprintf("psp-out:%s:%s", m, cur.Code)
}

// Batch is a payout grouping for the processor (Task 11's "payout
// batching"): same rail, same currency, processed together; ledger entries
// remain per-withdrawal, the batch is an operational fact.
type Batch struct {
	ID          string
	Method      payments.Method
	Currency    money.Currency
	Withdrawals []Withdrawal
	Total       money.Amount
}

// BuildBatch collects approved withdrawals for a rail/currency, capped by
// maxCount (0 = all), and sums the total with checked money arithmetic.
func (s *Service) BuildBatch(ctx context.Context, method payments.Method, cur money.Currency, maxCount int) (Batch, error) {
	list, err := s.store.ListApprovedForBatch(ctx, method, cur.Code)
	if err != nil {
		return Batch{}, err
	}
	if maxCount > 0 && len(list) > maxCount {
		list = list[:maxCount]
	}
	b := Batch{Method: method, Currency: cur, Withdrawals: list, Total: money.Zero(cur)}
	for _, w := range list {
		next, terr := b.Total.Add(w.Amount)
		if terr != nil {
			return Batch{}, terr
		}
		b.Total = next
	}
	return b, nil
}

func (s *Service) advance(ctx context.Context, id string, to Status, reason string) error {
	w, err := s.store.Load(ctx, id)
	if err != nil {
		return err
	}
	if _, aerr := w.Advance(to, s.clk.Now()); aerr != nil {
		return aerr
	}
	if reason != "" {
		w.Reason = reason
	}
	return s.store.Save(ctx, w)
}

func (s *Service) mustLoad(ctx context.Context, id string) (Withdrawal, error) {
	return s.store.Load(ctx, id)
}

func scopeOf(w Withdrawal) tenancy.Scope {
	return tenancy.Scope{TenantID: w.TenantID}
}
