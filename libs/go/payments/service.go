package payments

import (
	"context"
	"errors"
	"fmt"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/ledger"
	"github.com/satta/platform/libs/go/money"
	"github.com/satta/platform/libs/go/orchestration"
	"github.com/satta/platform/libs/go/tenancy"
)

// Service wires store, processors and the ledger behind the deposit state
// machine: one path from "provider says captured" to "player sees balance",
// reconciled at the step that matters.
type Service struct {
	store      Store
	ledger     ledger.Ledger
	processors map[Method]Processor
	clk        clock.Clock
	meter      Meter
}

// NewService requires all collaborators — a half-wired payment service is
// how money goes missing silently, so construction is all-or-nothing.
func NewService(store Store, lgr ledger.Ledger, clk clock.Clock) (*Service, error) {
	if store == nil || lgr == nil || clk == nil {
		return nil, errors.New("payments: Service needs store, ledger and clock")
	}
	return &Service{store: store, ledger: lgr, processors: map[Method]Processor{}, clk: clk}, nil
}

// RegisterProcessor binds a rail adapter (boot-time).
func (s *Service) RegisterProcessor(m Method, p Processor) {
	s.processors[m] = p
}

// Start records the intent and asks the processor. Every persisted outcome
// matches the returned deposit; a processor error lands the deposit in
// failed, never stuck in started.
func (s *Service) Start(ctx context.Context, id, tenant, player string, amount money.Amount, method Method) (Deposit, error) {
	req := PaymentRequest{DepositID: id, Amount: amount, Method: method, PlayerRef: player}
	if err := req.Validate(); err != nil {
		return Deposit{}, err
	}
	proc, ok := s.processors[method]
	if !ok {
		return Deposit{}, fmt.Errorf("payments: no processor registered for rail %q", method)
	}
	now := s.clk.Now().UTC()
	dep := Deposit{
		ID: id, TenantID: tenant, PlayerID: player, Method: method, Amount: amount,
		Status: StatusStarted, Provider: proc.Name(),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.Save(ctx, dep); err != nil {
		return Deposit{}, err
	}
	res, err := proc.Create(ctx, req)
	if err != nil {
		if ferr := s.fail(ctx, id, "processor error: "+err.Error()); ferr != nil {
			return Deposit{}, ferr
		}
		return s.store.Load(ctx, id)
	}
	if err := s.applyResult(ctx, id, res); err != nil {
		return Deposit{}, err
	}
	return s.store.Load(ctx, id)
}

// Confirm applies a VERIFIED provider signal (the webhook/adapter verified
// signatures before this call). It first reconciles: any captured/authorized
// claim is checked against the provider's own Query for both reference and
// amount; a contradiction fails the deposit and reports ErrAmountMismatch
// rather than trusting a push.
func (s *Service) Confirm(ctx context.Context, id string, res ProcessorResult) (Deposit, error) {
	dep, err := s.store.Load(ctx, id)
	if err != nil {
		return Deposit{}, err
	}
	if dep.Status == StatusCredited && res.Status != StatusReversed {
		// Post-credit webhooks (reversals excluded): the ledger has spoken.
		return dep, nil
	}
	if (res.Status == StatusCaptured || res.Status == StatusAuthorized) && dep.Status != StatusCredited {
		proc, ok := s.processors[dep.Method]
		if !ok {
			return Deposit{}, fmt.Errorf("payments: rail %q unregistered after deposit %s", dep.Method, id)
		}
		if res.ProviderRef != "" && res.ProviderRef != dep.ProviderRef {
			if derr := s.fail(ctx, id, "provider reference contradiction"); derr != nil {
				return Deposit{}, derr
			}
			return Deposit{}, fmt.Errorf("%w: reference contradiction on deposit %s", ErrAmountMismatch, id)
		}
		q, qerr := proc.Query(ctx, res.ProviderRef)
		if qerr != nil {
			return Deposit{}, fmt.Errorf("payments: reconcile query: %w", qerr)
		}
		if q.Amount != nil && q.Status != StatusFailed && q.Amount.Minor() != dep.Amount.Minor() {
			if ferr := s.fail(ctx, id, fmt.Sprintf("amount mismatch: provider %d, intent %d", q.Amount.Minor(), dep.Amount.Minor())); ferr != nil {
				return Deposit{}, ferr
			}
			return Deposit{}, fmt.Errorf("%w: deposit %s: provider %d != intent %d",
				ErrAmountMismatch, id, q.Amount.Minor(), dep.Amount.Minor())
		}
	}
	if err := s.applyResult(ctx, id, res); err != nil {
		return Deposit{}, err
	}
	return s.store.Load(ctx, id)
}

func (s *Service) applyResult(ctx context.Context, id string, res ProcessorResult) error {
	dep, err := s.store.Load(ctx, id)
	if err != nil {
		return err
	}
	if res.ProviderRef != "" {
		dep.ProviderRef = res.ProviderRef
	}
	if err := s.store.Save(ctx, dep); err != nil {
		return err
	}
	return s.advance(ctx, id, res.Status, res.Err)
}

func (s *Service) fail(ctx context.Context, id, reason string) error {
	return s.advance(ctx, id, StatusFailed, reason)
}

func (s *Service) advance(ctx context.Context, id string, to Status, reason string) error {
	dep, err := s.store.Load(ctx, id)
	if err != nil {
		return err
	}
	if _, terr := dep.Advance(to, s.clk.Now()); terr != nil {
		return terr
	}
	if reason != "" {
		dep.FailureReason = reason
	}
	return s.store.Save(ctx, dep)
}

// Credit posts the ledger entry — the ONLY place deposit value becomes a
// player balance. Idempotent twice over: the ledger entry key is
// "deposit:<id>", and the state machine absorbs the already-credited case
// without touching the ledger again.
func (s *Service) Credit(ctx context.Context, id string) (Deposit, error) {
	dep, err := s.store.Load(ctx, id)
	if err != nil {
		return Deposit{}, err
	}
	if dep.Status == StatusCredited {
		return dep, nil
	}
	if dep.Status != StatusCaptured {
		return Deposit{}, fmt.Errorf("%w: %s not captured (status %s)", ErrBadTransition, id, dep.Status)
	}
	cur := dep.Amount.Currency()
	clearing := ClearingAccount(dep.Method, cur)
	to := ledger.WalletAccount(dep.PlayerID, ledger.KindPlayerCash)
	scope := tenancy.Scope{TenantID: dep.TenantID}
	if oerr := s.ledger.OpenAccount(ctx, ledger.Account{ID: clearing, Kind: ledger.KindSettle, Currency: cur,
		Scope: scope}); oerr != nil {
		return Deposit{}, oerr
	}
	// The player's cash wallet is opened here — the account a deposit
	// credits is created by the credit, not assumed to exist by it. A
	// player's first deposit funds their first wallet.
	if oerr := s.ledger.OpenAccount(ctx, ledger.Account{ID: to, Kind: ledger.KindPlayerCash, Currency: cur,
		Scope: scope}); oerr != nil {
		return Deposit{}, oerr
	}
	entry, terr := ledger.Transfer("deposit:"+id, clearing, to, dep.Amount, "deposit credit "+id, dep.UpdatedAt)
	if terr != nil {
		return Deposit{}, terr
	}
	if _, perr := s.ledger.Post(ctx, entry); perr != nil {
		return Deposit{}, perr
	}
	if aerr := s.advance(ctx, id, StatusCredited, ""); aerr != nil {
		return Deposit{}, aerr
	}
	return s.store.Load(ctx, id)
}

// ClearingAccount names the PSP-transit ledger account for a rail+currency
// (funds the provider confirmed it holds, before they are player balances).
func ClearingAccount(m Method, cur money.Currency) string {
	return fmt.Sprintf("psp:%s:%s", m, cur.Code)
}

// Meter is the metering port for the announce step (same shape as
// metering.Emitter; kept structural to avoid importing the metering type
// here).
type Meter interface {
	Emit(ctx context.Context, ev MeterEvent) error
}

// MeterEvent is the minimal shape the payments saga announces.
type MeterEvent struct {
	EventID     string
	TenantID    string
	ProductCode string
	MeterCode   string
	Quantity    int64
	Unit        string
	SubjectID   string
}

// NewServiceWithMeter wires the optional announce step; without it the
// definition has no meter leg.
func NewServiceWithMeter(store Store, lgr ledger.Ledger, clk clock.Clock, m Meter) (*Service, error) {
	s, err := NewService(store, lgr, clk)
	if err != nil {
		return nil, err
	}
	s.meter = m
	return s, nil
}

// SagaDefinition is the durable tail of the deposit flow (Task 2 x Task 10):
// reconcile-then-credit with a compensating reversal. The processor step
// needs a human (3DS) and stays interactive; everything after a captured
// signal is machine-owned, retryable, and compensated rather than abandoned.
//
// Run under the orchestration runner with the deposit ID as both the
// business key and the input.
func (s *Service) SagaDefinition() orchestration.Definition {
	def := orchestration.Definition{
		Name: "deposit-credit",
		Steps: []orchestration.Step{
			{
				Name:    "reconcile",
				Retries: 3,
				Run: func(ctx context.Context, id []byte) ([]byte, error) {
					dep, err := s.store.Load(ctx, string(id))
					if err != nil {
						return nil, err
					}
					if dep.Status == StatusCredited {
						return id, nil
					}
					proc, ok := s.processors[dep.Method]
					if !ok {
						return nil, fmt.Errorf("payments: rail %q unregistered", dep.Method)
					}
					q, qerr := proc.Query(ctx, dep.ProviderRef)
					if qerr != nil {
						return nil, qerr
					}
					if q.Status != StatusCaptured {
						// Not captured yet: a retryable wait, not a failure —
						// the runner's retry budget is the polling loop, and
						// ErrBadTransition keeps it out of "compensated".
						return nil, fmt.Errorf("%w: provider at %s, want captured", ErrBadTransition, q.Status)
					}
					if aerr := s.applyResult(ctx, dep.ID, q); aerr != nil {
						return nil, aerr
					}
					return id, nil
				},
			},
			{
				Name: "credit",
				Run: func(ctx context.Context, id []byte) ([]byte, error) {
					dep, err := s.Credit(ctx, string(id))
					if err != nil {
						return nil, err
					}
					return []byte(dep.ID), nil
				},
				Compensate: func(ctx context.Context, id []byte) error {
					dep, err := s.store.Load(ctx, string(id))
					if err != nil {
						return err
					}
					if dep.Status != StatusCredited {
						return nil // nothing was credited; nothing owed
					}
					cur := dep.Amount.Currency()
					from := ledger.WalletAccount(dep.PlayerID, ledger.KindPlayerCash)
					clearing := ClearingAccount(dep.Method, cur)
					neg, nerr := dep.Amount.Neg()
					if nerr != nil {
						return nerr
					}
					abs, aerr := neg.Abs()
					if aerr != nil {
						return aerr
					}
					rev, terr := ledger.Transfer("deposit:"+dep.ID+":reverse", from, clearing, abs,
						"compensated reversal of deposit "+dep.ID, s.clk.Now().UTC())
					if terr != nil {
						return terr
					}
					_, perr := s.ledger.Post(ctx, rev)
					if perr != nil {
						return perr
					}
					return s.advance(ctx, dep.ID, StatusReversed, "saga compensation")
				},
			},
		},
	}
	if s.meter == nil {
		return def
	}
	def.Steps = append(def.Steps, orchestration.Step{
		Name:    "announce",
		Retries: 3,
		Run: func(ctx context.Context, id []byte) ([]byte, error) {
			dep, err := s.store.Load(ctx, string(id))
			if err != nil {
				return nil, err
			}
			merr := s.meter.Emit(ctx, MeterEvent{
				EventID:     "usage:deposit:" + dep.ID,
				TenantID:    dep.TenantID,
				ProductCode: "payments",
				MeterCode:   "deposit.captured",
				Quantity:    1,
				Unit:        "count",
				SubjectID:   dep.ID,
			})
			if merr != nil {
				return nil, merr
			}
			return id, nil
		},
	})
	return def
}
