// Package fx is currency conversion (roadmap Task 8): rate feeds behind a
// port, spread application, and rate locking — entirely in exact fractions.
//
// The rules that make conversion auditable, in order of how often they get
// broken elsewhere:
//
//   - A rate is a money.Ratio (integer numerator/denominator), never a
//     float. 1.18 becomes 118/100, and the golden vectors pin which one a
//     calculator has to agree with.
//   - A conversion NEVER rounds by default: the mode comes from policy
//     (gate 1 + the config engine), passed explicitly. An implicit
//     half-up/half-even default is a house edge someone forgot to declare.
//   - A locked quote is a value: it carries pair, rates and expiry, and the
//     conversion recomputes from the stored quote — "we agreed 0.913" must
//     survive to the ledger entry that proves it.
//   - Direction decides the spread: the customer BUYING the quote currency
//     pays the ask (worse for them), SELLING receives the bid. A sign
//     mistake here is the classic silent value leak, so the API has no
//     boolean; it has two named functions.
package fx

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/money"
)

// Errors of the package.
var (
	// ErrNoRate: the feed cannot price this pair.
	ErrNoRate = errors.New("fx: no rate for pair")
	// ErrExpired: the quote is outside its freshness window.
	ErrExpired = errors.New("fx: quote expired")
	// ErrBadFeed marks provider data that cannot be used.
	ErrBadFeed = errors.New("fx: malformed feed data")
)

// Quote is one provider-priced pair at a point in time. Bid/Ask are ratios
// from the BASE currency to QUOTE currency (how much QUOTE per 1 scaled
// unit of BASE); mid is informational.
type Quote struct {
	ID      string
	Base    money.Currency
	Quote   money.Currency
	Bid     money.Ratio // customer sells base: gets bid
	Ask     money.Ratio // customer buys base: pays ask
	AsOf    time.Time
	Expires time.Time
	Source  string // feed identifier, for dispute ("whose 0.913 was it")
}

// Side names the customer's direction relative to the base currency.
type Side int

const (
	// SideBuy: the customer is buying base currency (pays ask).
	SideBuy Side = iota
	// SideSell: the customer is selling base currency (receives bid).
	SideSell
)

// Validate checks structural integrity and spread sanity: an inverted spread
// (bid > ask) means a feed bug that gives the customer a free edge — treat
// it as poison, not as a good day.
func (q Quote) Validate(now time.Time) error {
	if q.ID == "" || q.Base.Code == "" || q.Quote.Code == "" {
		return fmt.Errorf("%w: quote missing id or currency", ErrBadFeed)
	}
	if q.Base.Code == q.Quote.Code {
		return fmt.Errorf("%w: same-side pair %s", ErrBadFeed, q.Base.Code)
	}
	if q.Bid.IsZero() || q.Ask.IsZero() {
		return fmt.Errorf("%w: zero rate", ErrBadFeed)
	}
	if q.AsOf.IsZero() || q.Expires.IsZero() {
		return fmt.Errorf("%w: quote needs AsOf and Expires", ErrBadFeed)
	}
	if !q.Expires.After(q.AsOf) {
		return fmt.Errorf("%w: Expires must be after AsOf", ErrBadFeed)
	}
	if q.Bid.Cmp(q.Ask) > 0 {
		return fmt.Errorf("%w: bid exceeds ask on %s/%s", ErrBadFeed, q.Base.Code, q.Quote.Code)
	}
	if !now.UTC().Before(q.Expires.UTC()) {
		return fmt.Errorf("%w: quote %s expired at %s", ErrExpired, q.ID, q.Expires.UTC())
	}
	return nil
}

// RateFor returns the direction-correct rate: buying base pays the ask,
// selling base receives the bid. The asymmetry is the spread; the function
// names make a swap impossible to hide in a boolean.
func (q Quote) RateFor(side Side) money.Ratio {
	if side == SideBuy {
		return q.Ask
	}
	return q.Bid
}

// Convert applies the direction's locked rate to an amount expressed in the
// opposite side of the pair. from currency must match exactly one leg;
// the returned amount is in the other. Rounding is required by policy.
func (q Quote) Convert(now time.Time, from money.Amount, mode money.RoundingMode) (money.Amount, error) {
	if err := q.Validate(now); err != nil {
		return money.Amount{}, err
	}
	if mode == money.RoundingUnspecified {
		return money.Amount{}, errors.New("fx: rounding mode is policy — pass it explicitly (no default)")
	}
	switch from.Currency().Code {
	case q.Base.Code:
		rate := q.RateFor(SideSell) // giving away base: receive the bid
		return from.ApplyRatio(rate, mode)
	case q.Quote.Code:
		// Paying in the quote currency to obtain base divides by the ask —
		// the ask is expressed as quote-per-base, so the applied multiplier is
		// its reciprocal. Applying it un-inverted would overpay the customer
		// by the spread twice.
		rate, err := q.RateFor(SideBuy).Invert()
		if err != nil {
			return money.Amount{}, fmt.Errorf("fx: ask %s not invertible: %w", q.Ask.String(), err)
		}
		return from.ApplyRatio(rate, mode)
	default:
		return money.Amount{}, fmt.Errorf("fx: amount currency %s matches neither leg of %s/%s",
			from.Currency().Code, q.Base.Code, q.Quote.Code)
	}
}

// Feed is the port for external rate sources (roadmap Task 8's "FX rate
// feeds behind a port", DoD gate 2). Adapters: StaticFeed (tests/dev), and
// a vendor feed lands in a service with the same contract suite.
type Feed interface {
	// Latest returns the freshest valid quote for a pair or ErrNoRate.
	Latest(ctx context.Context, base, quote string) (Quote, error)
}

// StaticFeed is a deterministic Feed for tests and stage-0, and it is the
// second half of the port's contract: provider-shaped data in, validated
// quotes out.
type StaticFeed struct {
	now   func() time.Time // injected for expiry behaviour in tests
	byKey map[string][]Quote
}

// NewStaticFeed indexes quotes (each validated at construction against its
// own AsOf window) and stamps freshness from clk.
func NewStaticFeed(clk clock.Clock, quotes ...Quote) (*StaticFeed, error) {
	if clk == nil {
		return nil, errors.New("fx: StaticFeed requires a clock")
	}
	f := &StaticFeed{now: clk.Now, byKey: map[string][]Quote{}}
	for _, q := range quotes {
		if err := q.Validate(clk.Now()); err != nil {
			return nil, err
		}
		k := pairKey(q.Base.Code, q.Quote.Code)
		f.byKey[k] = append(f.byKey[k], q)
	}
	return f, nil
}

// Latest implements Feed: the freshest valid direct quote for the pair, or
// its mirror (bid/ask invert) when only the reverse was provided. The
// fallback is ONE step deep by construction — no recursion — or an
// unrated pair would loop between the two directions forever.
func (s *StaticFeed) Latest(_ context.Context, base, quote string) (Quote, error) {
	now := s.now()
	if q, ok := s.freshest(base, quote, now); ok {
		return q, nil
	}
	if fwd, ok := s.freshest(quote, base, now); ok {
		return invert(fwd, now)
	}
	return Quote{}, fmt.Errorf("%w: %s/%s", ErrNoRate, base, quote)
}

func (s *StaticFeed) freshest(base, quote string, now time.Time) (Quote, bool) {
	var best Quote
	for _, c := range s.byKey[pairKey(base, quote)] {
		if c.Validate(now) != nil {
			continue // expired or poisoned: skipped, never served stale
		}
		if best.ID == "" || c.AsOf.After(best.AsOf) {
			best = c
		}
	}
	return best, best.ID != ""
}

// invert derives a reverse quote from a forward one by flipping bid/ask.
// The provider gave one direction; the inverse is arithmetic, not a second
// opinion. A failed invert (zero rate) or re-validation surfaces as an
// error, which Latest reports as ErrNoRate.
func invert(q Quote, now time.Time) (Quote, error) {
	invBid, err := q.Ask.Invert()
	if err != nil {
		return Quote{}, err
	}
	invAsk, err := q.Bid.Invert()
	if err != nil {
		return Quote{}, err
	}
	inv := Quote{
		ID:      q.ID + ":inv",
		Base:    q.Quote,
		Quote:   q.Base,
		Bid:     invBid,
		Ask:     invAsk,
		AsOf:    q.AsOf,
		Expires: q.Expires,
		Source:  q.Source + "+inverse",
	}
	if err := inv.Validate(now); err != nil {
		return Quote{}, err
	}
	return inv, nil
}

func pairKey(base, quote string) string { return base + "/" + quote }

// ApplySpreadBps turns a mid rate into the provider's quoted bid/ask around
// it, in integer basis points (bps = 1/10000), never a percentage float.
func ApplySpreadBps(mid money.Ratio, spreadBps int64) (bid, ask money.Ratio, err error) {
	if spreadBps < 0 || spreadBps > 10_000 {
		return money.Ratio{}, money.Ratio{}, fmt.Errorf("%w: spread %d bps out of range", ErrBadFeed, spreadBps)
	}
	// ask = mid * (20000 + bps) / 20000, bid = mid * (20000 - bps) / 20000.
	askR, err := money.NewRatio(20000+spreadBps, 20000)
	if err != nil {
		return money.Ratio{}, money.Ratio{}, err
	}
	bidR, err := money.NewRatio(20000-spreadBps, 20000)
	if err != nil {
		return money.Ratio{}, money.Ratio{}, err
	}
	bid, err = mid.Mul(bidR)
	if err != nil {
		return money.Ratio{}, money.Ratio{}, err
	}
	ask, err = mid.Mul(askR)
	if err != nil {
		return money.Ratio{}, money.Ratio{}, err
	}
	return bid, ask, nil
}
