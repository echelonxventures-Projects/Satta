package rg_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/satta/platform/libs/go/access"
	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/rg"
	"github.com/satta/platform/libs/go/rules"
)

func int64p(v int64) *int64 { return &v }

var fixed = time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC) // a Monday

func caps() rg.Caps {
	return rg.Caps{
		rg.KindDeposit: {rg.PeriodDaily: 10000, rg.PeriodWeekly: 25000, rg.PeriodMonthly: 60000},
		rg.KindLoss:    {rg.PeriodDaily: 5000, rg.PeriodWeekly: 12000, rg.PeriodMonthly: 30000},
		rg.KindWager:   {rg.PeriodDaily: 40000, rg.PeriodWeekly: 100000, rg.PeriodMonthly: 300000},
	}
}

func limits(t *testing.T) *rg.Limits {
	t.Helper()
	l, err := rg.NewLimits(caps(), 3600, clock.NewFixed(fixed))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestDailyCapBlocksAndCommitsAccumulate(t *testing.T) {
	l := limits(t)
	if err := l.Commit("alice", rg.KindDeposit, 6000, fixed); err != nil {
		t.Fatalf("first deposit: %v", err)
	}
	if err := l.Commit("alice", rg.KindDeposit, 4000, fixed); err != nil {
		t.Fatalf("second deposit: %v", err)
	}
	err := l.Commit("alice", rg.KindDeposit, 1, fixed)
	if !errors.Is(err, rg.ErrLimitExceeded) {
		t.Fatalf("over daily cap: %v, want ErrLimitExceeded", err)
	}
	if spent := l.Spent("alice", rg.KindDeposit, rg.PeriodDaily, fixed); spent != 10000 {
		t.Fatalf("spent = %d, want 10000", spent)
	}
}

func TestWeeklyBucketSpansDays(t *testing.T) {
	l := limits(t)
	mon := fixed
	tue := fixed.Add(24 * time.Hour)
	wed := fixed.Add(48 * time.Hour)
	// Three daily-sized deposits each fit their day, but together they cross
	// the weekly cap (25000): the weekly bucket is enforced from Monday's
	// spend even on Wednesday.
	for _, day := range []time.Time{mon, tue} {
		if err := l.Commit("bob", rg.KindDeposit, 9000, day); err != nil {
			t.Fatalf("day %v: %v", day, err)
		}
	}
	if err := l.Commit("bob", rg.KindDeposit, 8000, wed); !errors.Is(err, rg.ErrLimitExceeded) {
		t.Fatalf("weekly cap not enforced: %v", err)
	}
	if err := l.Commit("bob", rg.KindDeposit, 7000, wed); err != nil {
		t.Fatalf("7000 on wed should fit weekly 25000 exactly: %v", err)
	}
	// And the day boundary is independent: 10000 on Thursday fails on the
	// daily bucket already used.
	if err := l.Commit("bob", rg.KindDeposit, 10000, wed.Add(24*time.Hour)); !errors.Is(err, rg.ErrLimitExceeded) {
		t.Fatalf("Thursday daily cap not enforced: %v", err)
	}
}

func TestCheckDoesNotCommit(t *testing.T) {
	l := limits(t)
	if err := l.Check("carol", rg.KindDeposit, 9000, fixed); err != nil {
		t.Fatalf("check under cap: %v", err)
	}
	if spent := l.Spent("carol", rg.KindDeposit, rg.PeriodDaily, fixed); spent != 0 {
		t.Fatalf("Check changed state: %d", spent)
	}
}

func TestSelfExclusionBlocksEverythingUntilExpiry(t *testing.T) {
	l := limits(t)
	l.Exclude("dave", fixed.Add(24*time.Hour), "player request", "dave")
	err := l.Commit("dave", rg.KindDeposit, 100, fixed)
	if !errors.Is(err, rg.ErrExcluded) {
		t.Fatalf("excluded deposit: %v, want ErrExcluded", err)
	}
	if err := l.Check("dave", rg.KindDeposit, 100, fixed.Add(23*time.Hour)); !errors.Is(err, rg.ErrExcluded) {
		t.Fatalf("mid-exclusion: %v", err)
	}
	// After expiry the player may act again (exclusion honoured, not a ban).
	if err := l.Check("dave", rg.KindDeposit, 100, fixed.Add(25*time.Hour)); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
}

func TestPermanentExclusion(t *testing.T) {
	l := limits(t)
	l.Exclude("erin", time.Time{}, "permanent", "erin")
	if _, ok := l.Excluded("erin", fixed.AddDate(10, 0, 0)); !ok {
		t.Fatal("permanent exclusion lapsed — INVARIANT 5 breach")
	}
}

func TestLiftRequiresAuthority(t *testing.T) {
	l := limits(t)
	pol, err := access.ParsePolicy(`[{"role":"compliance-officer","permission":"rg.unexclude"}]`)
	if err != nil {
		t.Fatal(err)
	}
	l.Exclude("frank", time.Time{}, "self", "frank")
	// The player cannot lift their own exclusion through any role they hold.
	if err := l.Lift(context.Background(), pol, "player", "frank", "frank", nil); !errors.Is(err, access.ErrUnknownRole) {
		t.Fatalf("self-lift: %v, want a typed denial", err)
	}
	if err := l.Lift(context.Background(), pol, "compliance-officer", "officer-2", "frank", nil); err != nil {
		t.Fatalf("officer lift: %v", err)
	}
	if _, ok := l.Excluded("frank", fixed); ok {
		t.Fatal("exclusion survived the lift")
	}
}

func TestCoolingOffDistinctError(t *testing.T) {
	l := limits(t)
	l.CoolingOff("gina", fixed.Add(time.Hour), "gina")
	err := l.Check("gina", rg.KindDeposit, 1, fixed)
	if !errors.Is(err, rg.ErrCoolingOff) {
		t.Fatalf("cooling-off: %v, want ErrCoolingOff", err)
	}
}

func TestRealityCheckTiming(t *testing.T) {
	l := limits(t)
	start := fixed.Add(-2 * time.Hour)
	if !l.RealityCheckDue(start, time.Time{}) {
		t.Fatal("2h without a prompt must be due")
	}
	recent := fixed.Add(-30 * time.Minute)
	if l.RealityCheckDue(start, recent) {
		t.Fatal("30m since last prompt must not be due")
	}
}

func harmPolicy(t *testing.T) *rg.HarmPolicy {
	t.Helper()
	p, err := rg.NewHarmPolicy(&rules.Node{All: []*rules.Node{
		{Fact: "deposits_24h", GteInt: int64p(3)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHarmAlertsDedupPerDay(t *testing.T) {
	l := limits(t)
	hp := harmPolicy(t)
	facts := map[string]rules.Fact{"deposits_24h": rules.IntFact(4)}
	ctx := context.Background()
	hit, err := l.EvaluateHarm(ctx, hp, "ivy", facts)
	if err != nil || !hit {
		t.Fatalf("first alert: %v %v", hit, err)
	}
	// Same player, same UTC day: no second siren.
	hit, err = l.EvaluateHarm(ctx, hp, "ivy", facts)
	if err != nil || hit {
		t.Fatalf("duplicate alert: %v %v", hit, err)
	}
	// Next day: can alert again.
	nextDay := fixed.Add(24 * time.Hour)
	l2, err := rg.NewLimits(caps(), 3600, clock.NewFixed(nextDay))
	if err != nil {
		t.Fatal(err)
	}
	hit, err = l2.EvaluateHarm(ctx, hp, "ivy", facts)
	if err != nil || !hit {
		t.Fatalf("next-day alert: %v %v", hit, err)
	}
}

func TestLimitsConstructionValidation(t *testing.T) {
	bad := caps()
	delete(bad, rg.KindLoss)
	if _, err := rg.NewLimits(bad, 60, clock.NewFixed(fixed)); err == nil {
		t.Fatal("missing kind accepted")
	}
	if _, err := rg.NewLimits(caps(), 0, clock.NewFixed(fixed)); err == nil {
		t.Fatal("zero reality-check interval accepted")
	}
}
