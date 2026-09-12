package synth_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/satta/platform/libs/go/synth"
)

var opts = synth.Options{
	Seed:          42,
	Prefix:        "t1",
	Jurisdictions: []string{"mt", "se", "ee"},
	Currencies:    []string{"EUR", "SEK"},
	Now:           time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC),
}

func TestPlayersAreReproducible(t *testing.T) {
	a, err := synth.Players(50, opts)
	if err != nil {
		t.Fatalf("players: %v", err)
	}
	b, err := synth.Players(50, opts)
	if err != nil {
		t.Fatalf("players2: %v", err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same seed did not reproduce the player set")
	}

	opts2 := opts
	opts2.Seed = 43
	c, err := synth.Players(50, opts2)
	if err != nil {
		t.Fatalf("players3: %v", err)
	}
	if reflect.DeepEqual(a, c) {
		t.Fatal("different seeds produced identical players")
	}
}

func TestEventStreamDeterminismAndKeys(t *testing.T) {
	players, err := synth.Players(20, opts)
	if err != nil {
		t.Fatalf("players: %v", err)
	}
	s1, err := synth.Stream(players, 5, opts)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	s2, err := synth.Stream(players, 5, opts)
	if err != nil {
		t.Fatalf("stream2: %v", err)
	}
	if !reflect.DeepEqual(s1, s2) {
		t.Fatal("replay diverged")
	}
	if len(s1) != 100 {
		t.Fatalf("events = %d, want 100", len(s1))
	}
	seen := map[string]bool{}
	for _, ev := range s1 {
		if ev.Key == "" || seen[ev.Key] {
			t.Fatalf("duplicate/empty key %q — replay would double-apply", ev.Key)
		}
		seen[ev.Key] = true
	}
}

func TestValidation(t *testing.T) {
	bad := []synth.Options{
		{}, // zero seed
		{Seed: 1, Currencies: []string{"EUR"}, Now: time.Now()},               // no jurisdictions
		{Seed: 1, Jurisdictions: []string{"mt"}, Currencies: []string{"EUR"}}, // no Now — harness must not read the clock itself
	}
	for i, o := range bad {
		if _, err := synth.Players(1, o); err == nil {
			t.Errorf("case %d: invalid options accepted", i)
		}
	}
}

func TestZeroSeedRejectedByGen(t *testing.T) {
	if _, err := synth.New(0); err == nil {
		t.Fatal("xorshift zero-state accepted — it outputs nothing forever")
	}
}
