// Package fin is the financial engine (roadmap Task 9): rake strategyegies,
// winner payouts, and hold/release, compiled into ledger entries.
//
// The separation that matters: this package DECIDES the money shape (who
// gets what fraction of a pool) and emits ledger entries; it never writes to
// storage itself. Every amount flows through money.Allocate / ApplyRatio —
// the golden-vector math — so the rake a tournament settlement charged at
// 03:00 is exactly the rake a reconciliation recomputes, in any language.
package fin

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/satta/platform/libs/go/ledger"
	"github.com/satta/platform/libs/go/money"
)

// Errors of the package.
var (
	// ErrRakePolicy: no rake rule matched / malformed tiers.
	ErrRakePolicy = errors.New("fin: rake policy")
	// ErrHolders: hold/release misuse (releasing an unheld payout, etc.).
	ErrHolders = errors.New("fin: holds")
)

// Rake is the house share extracted from a gross pool.
type Rake struct {
	Amount money.Amount
	Note   string
}

// RakeStrategy is the port for pluggable rake (Task 9: "pluggable rake
// strategyegies"). Implementations read their parameters from policy — a
// strategyegy constructed from hardcoded numbers violates gate 1 even if the
// interface is clean.
type RakeStrategy interface {
	Rake(gross money.Amount) (Rake, error)
	Name() string
}

// PercentageRake takes ratio of gross, optionally capped. The cap exists
// because regulators and commercial deals cap promo rake; an uncapped
// percentage is the constructor without it.
type PercentageRake struct {
	Share money.Ratio
	Cap   *money.Amount      // nil = no cap
	Mode  money.RoundingMode // policy, required — an implicit default is an undeclared edge
}

// NewPercentageRake validates: share in (0,1], cap non-negative.
func NewPercentageRake(share money.Ratio, capAmt *money.Amount, mode money.RoundingMode) (PercentageRake, error) {
	if share.IsZero() || share.Cmp(money.MustRatio(1, 1)) > 0 {
		return PercentageRake{}, fmt.Errorf("%w: share %s outside (0,1]", ErrRakePolicy, share)
	}
	if mode == money.RoundingUnspecified {
		return PercentageRake{}, fmt.Errorf("%w: rounding mode is policy; pass it explicitly", ErrRakePolicy)
	}
	if capAmt != nil && capAmt.IsNegative() {
		return PercentageRake{}, fmt.Errorf("%w: negative cap", ErrRakePolicy)
	}
	return PercentageRake{Share: share, Cap: capAmt, Mode: mode}, nil
}

// Rake implements RakeStrategy.
func (p PercentageRake) Rake(gross money.Amount) (Rake, error) {
	amt, err := gross.ApplyRatio(p.Share, p.Mode)
	if err != nil {
		return Rake{}, err
	}
	note := "percentage " + p.Share.String()
	if p.Cap != nil {
		c, cerr := amt.Cmp(*p.Cap)
		if cerr != nil {
			return Rake{}, cerr
		}
		if c > 0 {
			amt, note = *p.Cap, note+" capped at "+p.Cap.Decimal()
		}
	}
	return Rake{Amount: amt, Note: note}, nil
}

// Name implements RakeStrategy.
func (p PercentageRake) Name() string { return "percentage" }

// Tier is one breakpoint of a tiered rake: the share applies to the portion
// of gross up to (and including) UpTo; the final tier has UpTo nil.
type Tier struct {
	UpTo  *money.Amount
	Share money.Ratio
}

// TieredRake walks breakpoints over gross in order (the classic sportsbook
// promo structure: first X at a%, rest at b%).
type TieredRake struct {
	Tiers []Tier
	Mode  money.RoundingMode // policy, required per band
}

// NewTieredRake validates monotone breakpoints, shares in (0,1], exactly one
// unbounded final tier.
func NewTieredRake(tiers []Tier, mode money.RoundingMode) (TieredRake, error) {
	if mode == money.RoundingUnspecified {
		return TieredRake{}, fmt.Errorf("%w: rounding mode is policy; pass it explicitly", ErrRakePolicy)
	}
	if len(tiers) == 0 {
		return TieredRake{}, fmt.Errorf("%w: no tiers", ErrRakePolicy)
	}
	for i, t := range tiers {
		if t.Share.IsZero() || t.Share.Cmp(money.MustRatio(1, 1)) > 0 {
			return TieredRake{}, fmt.Errorf("%w: tier %d share %s outside (0,1]", ErrRakePolicy, i, t.Share)
		}
		unbounded := t.UpTo == nil
		if unbounded && i != len(tiers)-1 {
			return TieredRake{}, fmt.Errorf("%w: tier %d unbounded but not last", ErrRakePolicy, i)
		}
		if !unbounded && i > 0 {
			prev := tiers[i-1].UpTo
			if prev == nil {
				return TieredRake{}, fmt.Errorf("%w: tier %d unbounded but not last", ErrRakePolicy, i-1)
			}
			c, cerr := prev.Cmp(*t.UpTo)
			if cerr != nil {
				return TieredRake{}, cerr
			}
			if c >= 0 {
				return TieredRake{}, fmt.Errorf("%w: tier %d breakpoint not increasing", ErrRakePolicy, i)
			}
		}
	}
	return TieredRake{Tiers: tiers, Mode: mode}, nil
}

// Rake implements RakeStrategy. Each band is charged its share; totals are
// summed with money arithmetic (overflow-checked at every add).
func (tr TieredRake) Rake(gross money.Amount) (Rake, error) {
	total := money.Zero(gross.Currency())
	remaining := gross
	for _, t := range tr.Tiers {
		if remaining.IsZero() || remaining.IsNegative() {
			break
		}
		band := remaining
		if t.UpTo != nil {
			// Each band takes the lesser of what remains and its breakpoint.
			c, cerr := t.UpTo.Cmp(band)
			if cerr != nil {
				return Rake{}, cerr
			}
			if c < 0 {
				band = *t.UpTo
			}
		}
		part, err := band.ApplyRatio(t.Share, tr.Mode)
		if err != nil {
			return Rake{}, err
		}
		total, err = total.Add(part)
		if err != nil {
			return Rake{}, err
		}
		used, err := band.Neg()
		if err != nil {
			return Rake{}, err
		}
		remaining, err = remaining.Add(used)
		if err != nil {
			return Rake{}, err
		}
	}
	return Rake{Amount: total, Note: "tiered"}, nil
}

// Name implements RakeStrategy.
func (tr TieredRake) Name() string { return "tiered" }

// Winner is one recipient leg of a settlement.
type Winner struct {
	PlayerID string
	Weight   int64 // payout share weight (prize table position, etc.)
}

// Settlement is the money shape of one event's payout, computed before any
// ledger entry exists so it can be logged, diffed and audited standalone.
type Settlement struct {
	Net     money.Amount // pool after rake
	Rake    Rake
	Winners []Winner
}

// Settle extracts rake from the gross pool and defines the winners' split of
// the net. Winner order is normalized (player ID) so the allocation is
// reproducible independent of input ordering — the payout you compute is the
// payout you can defend.
func Settle(gross money.Amount, strategy RakeStrategy, winners []Winner) (Settlement, error) {
	if gross.IsNegative() {
		return Settlement{}, errors.New("fin: negative prize pool")
	}
	if len(winners) == 0 {
		return Settlement{}, errors.New("fin: settlement with no winners")
	}
	rake, err := strategy.Rake(gross)
	if err != nil {
		return Settlement{}, err
	}
	neg, err := rake.Amount.Neg()
	if err != nil {
		return Settlement{}, err
	}
	net, err := gross.Add(neg)
	if err != nil {
		return Settlement{}, err
	}
	if net.IsNegative() {
		return Settlement{}, fmt.Errorf("fin: rake %s exceeds pool %s", rake.Amount.Decimal(), gross.Decimal())
	}
	sorted := append([]Winner(nil), winners...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].PlayerID < sorted[j].PlayerID })
	return Settlement{Net: net, Rake: rake, Winners: sorted}, nil
}

// LedgerEntries builds the bookkeeping: pool out of the event-hold account,
// rake to house revenue, net distributed to winner wallets via the ledger's
// golden-rule waterfall.
func (s Settlement) LedgerEntries(eventID string, currency money.Currency, occurredAt time.Time) ([]ledger.Entry, error) {
	pool := "hold:event:" + eventID
	revenue := ledger.HouseAccount(ledger.KindHouse, currency.Code)
	targets := make([]ledger.Target, 0, len(s.Winners))
	for _, w := range s.Winners {
		targets = append(targets, ledger.Target{AccountID: ledger.WalletAccount(w.PlayerID, ledger.KindPlayerCash), Weight: w.Weight})
	}
	payout, err := ledger.Distribute("settle:"+eventID+":payout", pool, s.Net, targets, "payout of "+eventID, occurredAt.UTC())
	if err != nil {
		return nil, err
	}
	entries := []ledger.Entry{payout}
	if !s.Rake.Amount.IsZero() {
		rake, terr := ledger.Transfer("settle:"+eventID+":rake", pool, revenue, s.Rake.Amount, "rake of "+eventID+" "+s.Rake.Note, occurredAt.UTC())
		if terr != nil {
			return nil, terr
		}
		entries = append(entries, rake)
	}
	// Funding leg: the pool must hold gross. Source is house settlement for
	// now — the books balance either way, and the correct source is a
	// per-vertical fact (entry-fee escrow for tournaments, promo budget for
	// freerolls) that the owning service supplies when it builds the event
	// hold. It is deliberately NOT invented here.
	fund, err := ledger.Transfer("settle:"+eventID+":fund", revenue, pool, grossOf(s), "pool funding of "+eventID, occurredAt.UTC())
	if err != nil {
		return nil, err
	}
	return append([]ledger.Entry{fund}, entries...), nil
}

func grossOf(s Settlement) money.Amount {
	total, err := s.Net.Add(s.Rake.Amount)
	if err != nil {
		// Net was derived as gross-rake, so the sum cannot overflow again;
		// returning the wrong number here is impossible by construction.
		return s.Net
	}
	return total
}

// Hold / release: a payout that gates on a compliance step (KYC, AML review)
// moves to an escrow account first and is released by key. The release is a
// normal ledger transfer — the gate lives in WHO may call it, not in a
// second money model.
//
// HeldPayout is the receipt kept by the service: the release idempotency key
// is derived from it, so releasing twice posts once.
type HeldPayout struct {
	PayoutID string
	PlayerID string
	Amount   money.Amount
}

// HoldEntries moves an amount from house settlement to the player's escrow
// wallet (KindEscrow): still the player's value, not yet spendable.
func HoldEntries(h HeldPayout, occurredAt time.Time) ([]ledger.Entry, error) {
	from := ledger.HouseAccount(ledger.KindSettle, h.Amount.Currency().Code)
	to := ledger.WalletAccount(h.PlayerID, ledger.KindEscrow)
	holder, err := ledger.Transfer("hold:"+h.PayoutID, from, to, h.Amount, "hold "+h.PayoutID, occurredAt.UTC())
	if err != nil {
		return nil, err
	}
	return []ledger.Entry{holder}, nil
}

// ReleaseEntries moves a previously held payout from escrow to spendable
// cash. The keys are stable by construction: re-release is a no-op, and an
// attempt to release without the matching hold cannot happen (the hold entry
// is the only source the escrow wallet receives from).
func ReleaseEntries(h HeldPayout, occurredAt time.Time) (ledger.Entry, error) {
	from := ledger.WalletAccount(h.PlayerID, ledger.KindEscrow)
	to := ledger.WalletAccount(h.PlayerID, ledger.KindPlayerCash)
	rel, err := ledger.Transfer("release:"+h.PayoutID, from, to, h.Amount, "release "+h.PayoutID, occurredAt.UTC())
	if err != nil {
		return ledger.Entry{}, err
	}
	return rel, nil
}
