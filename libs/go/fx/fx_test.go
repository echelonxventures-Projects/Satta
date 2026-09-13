package fx_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/fx"
	"github.com/satta/platform/libs/go/money"
)

var (
	eur = money.Currency{Code: "EUR", Scale: 2, Kind: money.KindFiat}
	usd = money.Currency{Code: "USD", Scale: 2, Kind: money.KindFiat}
)

func r(n, d int64) money.Ratio { return money.MustRatio(n, d) }

func quote(id string, now time.Time) fx.Quote {
	return fx.Quote{
		ID: id, Base: eur, Quote: usd,
		Bid: r(1178, 1000), Ask: r(1182, 1000), // EUR/USD ~1.18 with 2bp-symmetry-ish
		AsOf: now, Expires: now.Add(time.Minute), Source: "test-feed",
	}
}

func TestConvertDirection(t *testing.T) {
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	q := quote("q1", now)

	// Selling 100.00 EUR (base) earns the bid: 117.80 USD.
	got, err := q.Convert(now, money.MustNew(10000, eur), money.RoundingHalfEven)
	if err != nil {
		t.Fatalf("sell: %v", err)
	}
	if got.Minor() != 11780 {
		t.Fatalf("sell EUR -> got %d, want 11780 (bid leg)", got.Minor())
	}

	// Buying EUR with 118.00 USD divides by the ask: 11800 * 1000/1182 =
	// 99.83..., half-even to 9983.
	back, err := q.Convert(now, money.MustNew(11800, usd), money.RoundingHalfEven)
	if err != nil {
		t.Fatalf("buy: %v", err)
	}
	if back.Minor() != 9983 {
		t.Fatalf("buy USD -> got %d, want 9983 (ask leg, inverted)", back.Minor())
	}
}

func TestRoundingModeIsMandatory(t *testing.T) {
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	q := quote("q1", now)
	_, err := q.Convert(now, money.MustNew(10000, eur), money.RoundingUnspecified)
	if err == nil {
		t.Fatal("conversion without a policy rounding mode accepted — implicit defaults are house edges")
	}
}

func TestExpiredQuoteRefused(t *testing.T) {
	asOf := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	q := quote("q-exp", asOf)
	later := asOf.Add(2 * time.Minute)
	if _, err := q.Convert(later, money.MustNew(10000, eur), money.RoundingHalfEven); !errors.Is(err, fx.ErrExpired) {
		t.Fatalf("expired convert: %v, want ErrExpired", err)
	}
}

func TestInvertedSpreadIsPoison(t *testing.T) {
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	bad := quote("bad", now)
	bad.Bid, bad.Ask = bad.Ask, bad.Bid // customer buys below the sell price
	if err := bad.Validate(now); err == nil {
		t.Fatal("inverted spread accepted — feed bug would hand out a free edge")
	}
}

func TestStaticFeedLatestFreshness(t *testing.T) {
	fixed := clock.NewFixed(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))
	good := quote("fresh", fixed.Now())
	feed, err := fx.NewStaticFeed(fixed, good)
	if err != nil {
		t.Fatalf("build feed: %v", err)
	}
	q, err := feed.Latest(context.Background(), "EUR", "USD")
	if err != nil || q.ID != "fresh" {
		t.Fatalf("latest: %v %v", q, err)
	}
	// NewStaticFeed rejects an already-expired quote at construction — the
	// feed never starts with poison data.
	if _, nerr := fx.NewStaticFeed(fixed, fx.Quote{
		ID: "old", Base: eur, Quote: usd, Bid: r(1, 2), Ask: r(2, 3),
		AsOf: fixed.Now().Add(-2 * time.Minute), Expires: fixed.Now().Add(-time.Minute), Source: "s",
	}); nerr == nil {
		t.Fatal("expired quote accepted into a fresh feed")
	}

	// Reverse lookup via inversion.
	rev, err := feed.Latest(context.Background(), "USD", "EUR")
	if err != nil {
		t.Fatalf("mirrored: %v", err)
	}
	if rev.Base.Code != "USD" || rev.Quote.Code != "EUR" {
		t.Fatalf("mirrored pair orientation: %+v", rev)
	}
	// Selling USD against the mirrored bid (= 1/forward ask 1.182):
	// 118.20 USD * 1000/1182 = exactly 100.00 EUR.
	amt, err := rev.Convert(fixed.Now(), money.MustNew(11820, usd), money.RoundingHalfEven)
	if err != nil {
		t.Fatalf("mirrored convert: %v", err)
	}
	if amt.Minor() != 10000 {
		t.Fatalf("mirrored conversion = %d, want 10000", amt.Minor())
	}
}

func TestNoRateErr(t *testing.T) {
	fixed := clock.NewFixed(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))
	feed, err := fx.NewStaticFeed(fixed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := feed.Latest(context.Background(), "EUR", "JPY"); !errors.Is(err, fx.ErrNoRate) {
		t.Fatalf("empty feed: %v, want ErrNoRate", err)
	}
}

func TestApplySpreadBps(t *testing.T) {
	mid := r(118, 100) // 1.18
	bid, ask, err := fx.ApplySpreadBps(mid, 50)
	if err != nil {
		t.Fatalf("spread: %v", err)
	}
	if bid.Cmp(ask) >= 0 {
		t.Fatalf("spread must order bid < ask: %s %s", bid, ask)
	}
	// 50bp: ask = 1.18 * 1.0025 = 1.18295, bid = 1.18 * 0.9975 = 1.17705.
	// Compared by value: fractions are exact but unreduced (NewRatio does not
	// normalize, so the golden corpus String forms stay stable).
	if want := r(118295, 100000); ask.Cmp(want) != 0 {
		t.Fatalf("ask = %s, want value 118295/100000", ask)
	}
	if wantBid := r(117705, 100000); bid.Cmp(wantBid) != 0 {
		t.Fatalf("bid = %s, want value 117705/100000", bid)
	}
	if _, _, err := fx.ApplySpreadBps(mid, 50_000); err == nil {
		t.Fatal("absurd spread accepted")
	}
}
