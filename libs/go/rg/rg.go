// Package rg is responsible gambling (roadmap Task 13): deposit/loss/wager
// limits, reality checks, cooling-off, self-exclusion, and harm-signal
// detection.
//
// SAFETY INVARIANT 5 says self-exclusion enforcement is a safety invariant,
// which shapes this package's contract:
//
//   - Enforcement is here, once: every gate that can move a player's money
//     asks this package, and the answer is an error or all-clear, never a
//     hint. A second "soft" enforcement path is how exclusions leak.
//   - Exclusion cannot be lifted by the excluded player. Only a named
//     human holding a permission, checked through the access policy, ends
//     one early — and the act itself is an audit event the service records.
//   - Limits are policy (numbers arrive from the config engine); the
//     buckets are UTC (SAFETY INVARIANT 12), and a limit that is set in
//     code cannot be changed without a deploy, which is exactly what the
//     regulator forbids.
package rg

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/satta/platform/libs/go/access"
	"github.com/satta/platform/libs/go/clock"
	"github.com/satta/platform/libs/go/rules"
)

// Errors.
var (
	// ErrExcluded is INVARIANT 5's typed NO.
	ErrExcluded = errors.New("rg: self-excluded")
	// ErrCoolingOff is the temporary break's NO.
	ErrCoolingOff = errors.New("rg: cooling off")
	// ErrLimitExceeded reports the bucket and cap so the UI can say
	// "daily deposit limit of X reached" from facts, not guesses.
	ErrLimitExceeded = errors.New("rg: limit exceeded")
	// ErrNotExcluded guards lifts of nothing.
	ErrNotExcluded = errors.New("rg: not excluded")
)

// Kind is a metered activity class.
type Kind string

const (
	KindDeposit Kind = "deposit"
	KindLoss    Kind = "loss"    // net staked minus won, reported by callers
	KindWager   Kind = "wager"   // turnover
	KindTime    Kind = "session" // played minutes
)

// Period names the bucket a cap applies to.
type Period string

const (
	PeriodDaily   Period = "daily"
	PeriodWeekly  Period = "weekly"
	PeriodMonthly Period = "monthly"
)

// Caps maps kind -> period -> max value in that period's units (money in
// minor units; KindTime in seconds). Values resolved from policy; NewLimits
// rejects any missing kind/period combination it was built to enforce.
type Caps map[Kind]map[Period]int64

// Limits enforces the caps and tracks spend per player.
type Limits struct {
	caps       Caps
	realitySec int64 // session minutes between reality-check prompts
	clk        clock.Clock

	mu      sync.Mutex
	spend   map[spendKey]int64 // player|kind|bucket
	excl    map[string]Exclusion
	alerted map[string]bool // harm alerts per player|UTC-day
}

type spendKey struct {
	player string
	kind   Kind
	period Period
	start  time.Time
}

// Exclusion is one self-exclusion or cooling-off record.
type Exclusion struct {
	PlayerID string
	Until    time.Time // zero = permanent
	Reason   string
	Actor    string // who set it (the player themselves, usually)
	SetAt    time.Time
}

// NewLimits validates caps fully present and builds an enforcer.
func NewLimits(caps Caps, realityCheckSeconds int64, clk clock.Clock) (*Limits, error) {
	if clk == nil {
		return nil, errors.New("rg: clock required")
	}
	if realityCheckSeconds <= 0 {
		return nil, errors.New("rg: reality-check interval must be positive")
	}
	for _, kind := range []Kind{KindDeposit, KindLoss, KindWager} {
		byPeriod, ok := caps[kind]
		if !ok {
			return nil, fmt.Errorf("rg: caps missing for %s", kind)
		}
		for _, p := range []Period{PeriodDaily, PeriodWeekly, PeriodMonthly} {
			if v, has := byPeriod[p]; !has || v < 0 {
				return nil, fmt.Errorf("rg: caps[%s][%s] missing or negative", kind, p)
			}
		}
	}
	return &Limits{
		caps: caps, realitySec: realityCheckSeconds, clk: clk,
		spend:   map[spendKey]int64{},
		excl:    map[string]Exclusion{},
		alerted: map[string]bool{},
	}, nil
}

// bucketStart computes the UTC bucket (daily: midnight; weekly: Monday
// 00:00; monthly: 1st 00:00) — no local times in the math (INVARIANT 12).
func bucketStart(t time.Time, p Period) time.Time {
	t = t.UTC()
	switch p {
	case PeriodDaily:
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	case PeriodWeekly:
		day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		offset := (int(day.Weekday()) + 6) % 7 // Monday=0
		return day.AddDate(0, 0, -offset)
	case PeriodMonthly:
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return t
}

// Check asks "would amount fit?" without committing.
func (l *Limits) Check(player string, kind Kind, amount int64, when time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.blockedLocked(player, when); err != nil {
		return err
	}
	return l.checkLocked(player, kind, amount, when)
}

func (l *Limits) checkLocked(player string, kind Kind, amount int64, when time.Time) error {
	byPeriod, ok := l.caps[kind]
	if !ok {
		return fmt.Errorf("rg: no caps configured for %s", kind)
	}
	for period, cap := range byPeriod {
		k := spendKey{player: player, kind: kind, period: period, start: bucketStart(when, period)}
		if l.spend[k]+amount > cap {
			return fmt.Errorf("%w: %s over %s (spent %d + %d > cap %d)",
				ErrLimitExceeded, kind, period, l.spend[k], amount, cap)
		}
	}
	return nil
}

// Commit records actual activity after the caller has allowed it. A commit
// that would now exceed (concurrent activity raced) is refused; the caller
// must decide, because silently accepting over-cap activity is the bug.
func (l *Limits) Commit(player string, kind Kind, amount int64, when time.Time) error {
	if amount < 0 {
		return errors.New("rg: Commit takes non-negative amounts")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.blockedLocked(player, when); err != nil {
		return err
	}
	if err := l.checkLocked(player, kind, amount, when); err != nil {
		return err
	}
	for period := range l.caps[kind] {
		k := spendKey{player: player, kind: kind, period: period, start: bucketStart(when, period)}
		l.spend[k] += amount
	}
	return nil
}

// Spent reports usage in the current bucket.
func (l *Limits) Spent(player string, kind Kind, period Period, when time.Time) int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.spend[spendKey{player: player, kind: kind, period: period, start: bucketStart(when, period)}]
}

// Exclude starts a self-exclusion (INVARIANT 5). An existing active
// exclusion is never shortened by a re-set.
func (l *Limits) Exclude(player string, until time.Time, reason, actor string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clk.Now().UTC()
	if cur, active := l.excl[player]; active && !l.expiredLocked(cur, now) {
		if cur.Until.IsZero() || (!until.IsZero() && until.After(cur.Until)) {
			l.excl[player] = Exclusion{PlayerID: player, Until: cur.Until, Reason: cur.Reason, Actor: cur.Actor, SetAt: cur.SetAt}
			return
		}
	}
	l.excl[player] = Exclusion{PlayerID: player, Until: until.UTC(), Reason: reason, Actor: actor, SetAt: now}
}

// CoolingOff is a short break sharing the same enforcement point.
func (l *Limits) CoolingOff(player string, until time.Time, actor string) {
	l.Exclude(player, until, "cooling-off", actor)
}

// Excluded reports active exclusion status and kind.
func (l *Limits) Excluded(player string, when time.Time) (Exclusion, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.excl[player]
	if !ok || l.expiredLocked(e, when.UTC()) {
		return Exclusion{}, false
	}
	return e, true
}

func (l *Limits) expiredLocked(e Exclusion, when time.Time) bool {
	return !e.Until.IsZero() && !when.Before(e.Until)
}

func (l *Limits) blockedLocked(player string, when time.Time) error {
	if e, ok := l.excl[player]; ok && !l.expiredLocked(e, when.UTC()) {
		if !e.Until.IsZero() && e.Reason == "cooling-off" {
			return fmt.Errorf("%w until %s", ErrCoolingOff, e.Until.Format(time.RFC3339))
		}
		return fmt.Errorf("%w since %s", ErrExcluded, e.SetAt.Format(time.RFC3339))
	}
	return nil
}

// Lift is the ONLY way an exclusion ends early: a named holder of
// rg.unexclude (access policy, so jurisdiction-specific authority is
// configuration) may lift; the player themselves cannot.
func (l *Limits) Lift(ctx context.Context, policy *access.Policy, role, approver, player string, facts map[string]rules.Fact) error {
	if policy == nil {
		return errors.New("rg: lift requires an approval policy")
	}
	if err := policy.Check(role, "rg.unexclude", facts); err != nil {
		return fmt.Errorf("rg: approver %s not permitted: %w", approver, err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.excl[player]
	if !ok || l.expiredLocked(e, l.clk.Now().UTC()) {
		return fmt.Errorf("%w: %s", ErrNotExcluded, player)
	}
	delete(l.excl, player)
	return nil
}

// RealityCheckDue tells a session service when to interrupt play: at least
// realitySec of played time since the last prompt.
func (l *Limits) RealityCheckDue(startedAt, lastPrompt time.Time) bool {
	now := l.clk.Now().UTC()
	since := lastPrompt
	if since.IsZero() {
		since = startedAt.UTC()
	}
	return now.Sub(since) >= time.Duration(l.realitySec)*time.Second
}

// HarmPolicy is the detection rule tree (policy-configured): facts like
// deposits_24h, night_sessions_week, chasing_flag evaluate to an alert.
type HarmPolicy struct{ node *rules.Node }

// NewHarmPolicy validates the tree at construction.
func NewHarmPolicy(node *rules.Node) (*HarmPolicy, error) {
	if node == nil {
		return nil, errors.New("rg: harm policy requires a rule tree")
	}
	if err := node.Validate(); err != nil {
		return nil, err
	}
	return &HarmPolicy{node: node}, nil
}

// EvaluateHarm raises at most ONE alert per player per UTC day (an
// escalating signal, not a siren on every bet).
func (l *Limits) EvaluateHarm(ctx context.Context, hp *HarmPolicy, player string, facts map[string]rules.Fact) (bool, error) {
	hit, err := rules.Evaluate(hp.node, facts)
	if err != nil {
		return false, err
	}
	if !hit {
		return false, nil
	}
	day := bucketStart(l.clk.Now(), PeriodDaily)
	key := player + "|" + day.Format("2006-01-02")
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.alerted[key] {
		return false, nil
	}
	l.alerted[key] = true
	return true, nil
}
