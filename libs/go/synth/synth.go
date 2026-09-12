// Package synth is the test-data & simulation harness (roadmap Task 5):
// deterministic synthetic players, wallets and event streams for tests,
// replay and load simulation.
//
// Determinism is the whole point — a seed must reproduce a scenario byte for
// byte, which is how a flaky settlement bug becomes a failing test that stays
// failing. Note the engine: math/rand is INVARIANT 2 (no math/rand in
// non-test source: its unseeded stream is unpredictable enough to hide races,
// and gambling code reaching for it as an RNG is the exact breach the
// invariant guards); a small, explicit xorshift lives here instead — it is
// NOT a gambling RNG, it never touches a bet outcome, and the name and
// comments say so out loud for auditors.
package synth

import (
	"fmt"
	"time"
)

// Gen is a deterministic xorshift64* stream seeded from one int64.
type Gen struct {
	state uint64
}

// New seeds a generator. Seed 0 is rejected: xorshift's all-zero state is a
// fixed point, and a "reproducible" stream that outputs nothing forever is
// worse than a crash.
func New(seed int64) (*Gen, error) {
	if seed == 0 {
		return nil, fmt.Errorf("synth: seed must be non-zero")
	}
	return &Gen{state: uint64(seed)}, nil
}

// next advances the stream. Not crypto, never for outcomes (see package doc):
// callers use crypto/rand or the platform RNG (Task 17) for anything a player
// can win on.
func (g *Gen) next() uint64 {
	x := g.state
	x ^= x >> 12
	x ^= x << 25
	x ^= x >> 27
	g.state = x
	return x * 2685821657736338717
}

// Uint64n returns a value in [0, n); n must be positive. Zero is a
// programming error at the call site (every in-package caller guards its
// pool), surfaced as a panic in the Must* sense rather than a silent 0.
func (g *Gen) Uint64n(n uint64) uint64 {
	if n == 0 {
		//nolint:forbidigo // programming-error guard, same contract as Must* constructors
		panic("synth: Uint64n(0)")
	}
	return g.next() % n
}

// deriveStreamSeed mixes a base seed through a Knuth LCG so event streams are
// a distinct deterministic sequence from player sequences (same base seed,
// reproducible, never identical streams).
func deriveStreamSeed(seed int64) int64 {
	out := int64(uint64(seed)*2862967187 + 3037002723)
	if out == 0 {
		return 1
	}
	return out
}

// Pick returns one of the options (fixture pools — jurisdictions, currency
// codes — passed by the caller; the harness ships no policy defaults on
// purpose).
func (g *Gen) Pick(options ...string) string {
	if len(options) == 0 {
		return ""
	}
	return options[g.Uint64n(uint64(len(options)))]
}

// Player is one synthetic account fixture.
type Player struct {
	ID           string
	Email        string
	Jurisdiction string
	Currency     string
	CreatedAt    time.Time
}

// Options parameterize a run; everything a scenario needs is passed in, and
// nothing defaults to a business value.
type Options struct {
	Seed          int64
	Prefix        string    // ID prefix, so parallel fixtures never collide
	Jurisdictions []string  // required: fixture pool
	Currencies    []string  // required: fixture pool
	Now           time.Time // creation times are derived, never read
	MinorPerLevel int64     // wallet funding granularity (in minor units)
}

// Validate fails fast on half-configured scenarios.
func (o Options) Validate() error {
	if o.Seed == 0 {
		return fmt.Errorf("synth: Options.Seed is required (non-zero)")
	}
	if len(o.Jurisdictions) == 0 || len(o.Currencies) == 0 {
		return fmt.Errorf("synth: Options requires jurisdiction and currency pools — fixtures must declare the world they model")
	}
	if o.Now.IsZero() {
		return fmt.Errorf("synth: Options.Now is required — the harness never reads the clock")
	}
	return nil
}

// Players generates a batch of deterministic synthetic players.
func Players(count int, opts Options) ([]Player, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if count < 0 {
		return nil, fmt.Errorf("synth: negative count")
	}
	g, err := New(opts.Seed)
	if err != nil {
		return nil, err
	}
	out := make([]Player, 0, count)
	prefix := opts.Prefix
	if prefix == "" {
		prefix = "p"
	}
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("%s-%d-%016x", prefix, i, g.next())
		out = append(out, Player{
			ID:           id,
			Email:        fmt.Sprintf("%s@example.invalid", id),
			Jurisdiction: g.Pick(opts.Jurisdictions...),
			Currency:     g.Pick(opts.Currencies...),
			// Creation times walk forward in bounded steps so ordering is
			// reproducible and never crosses an hour the scenario sorts by.
			CreatedAt: opts.Now.Add(time.Duration(i) * 97 * time.Second),
		})
	}
	return out, nil
}

// WalletMinor generates a bounded pseudo-random minor-unit balance using the
// generator's stream (wallet funding for deposit scenarios; the amounts are
// data shaped like money, not computed money).
func (g *Gen) WalletMinor(bound uint64) int64 {
	return int64(g.Uint64n(bound))
}

// Event is one replayable scenario op.
type Event struct {
	Type         string    `json:"type"` // "deposit", "bet", "withdraw"
	Player       string    `json:"player"`
	Minor        int64     `json:"minor"`
	Jurisdiction string    `json:"jurisdiction,omitempty"`
	At           time.Time `json:"at"`
	Key          string    `json:"key"` // idempotency key — replay-safe by construction
}

// Stream derives a deterministic event sequence from a player set: the same
// seed, players and options replay identically forever, which is what makes a
// recorded production-shaped incident reproducible in CI.
func Stream(players []Player, perPlayer int, opts Options) ([]Event, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	g, err := New(deriveStreamSeed(opts.Seed)) // a distinct, still-deterministic stream
	if err != nil {
		return nil, err
	}
	types := []string{"deposit", "bet", "bet", "withdraw"}
	out := make([]Event, 0, len(players)*perPlayer)
	for _, p := range players {
		for i := 0; i < perPlayer; i++ {
			t := types[g.Uint64n(uint64(len(types)))]
			minor := g.WalletMinor(50_000) + 100
			key := fmt.Sprintf("%s-%s-%d-%016x", p.ID, t, i, g.next())
			out = append(out, Event{
				Type: t, Player: p.ID, Minor: minor,
				Jurisdiction: p.Jurisdiction,
				At:           p.CreatedAt.Add(time.Duration(i) * time.Minute),
				Key:          key,
			})
		}
	}
	return out, nil
}
