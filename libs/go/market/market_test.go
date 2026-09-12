package market_test

import (
	"errors"
	"testing"

	"github.com/satta/platform/libs/go/market"
)

const tableJSON = `[
	{"jurisdiction":"mt","entity_id":"satta-mga-type1","state":"active"},
	{"jurisdiction":"se","entity_id":"satta-mga-type1","state":"active","verticals":["casino"]},
	{"jurisdiction":"in","entity_id":"satta-offshore","state":"staging"},
	{"jurisdiction":"us","entity_id":"satta-none","state":"inactive"}
]`

func table(t *testing.T) *market.Table {
	t.Helper()
	tt, err := market.ParseTable(tableJSON)
	if err != nil {
		t.Fatalf("ParseTable: %v", err)
	}
	return tt
}

func TestResolveActive(t *testing.T) {
	route, err := table(t).Resolve("mt", "")
	if err != nil {
		t.Fatalf("active jurisdiction: %v", err)
	}
	if route.EntityID != "satta-mga-type1" {
		t.Fatalf("entity = %q", route.EntityID)
	}
}

func TestResolveDeniesUnlisted(t *testing.T) {
	// The compliance-critical default: no route is NOT served.
	if _, err := table(t).Resolve("xx", ""); !errors.Is(err, market.ErrNoRoute) {
		t.Fatalf("unlisted jurisdiction: %v, want ErrNoRoute", err)
	}
}

func TestResolveNonActiveStates(t *testing.T) {
	if _, err := table(t).Resolve("us", ""); !errors.Is(err, market.ErrInactive) {
		t.Fatalf("inactive: %v, want ErrInactive", err)
	}
	if _, err := table(t).Resolve("in", ""); !errors.Is(err, market.ErrInactive) {
		t.Fatalf("staging must not serve real money: %v", err)
	}
	if !table(t).Staging("in") {
		t.Fatal("Staging() missed the staging route")
	}
}

func TestVerticalRestriction(t *testing.T) {
	tt := table(t)
	if _, err := tt.Resolve("se", "casino"); err != nil {
		t.Fatalf("licensed vertical: %v", err)
	}
	if _, err := tt.Resolve("se", "sportsbook"); !errors.Is(err, market.ErrNoRoute) {
		t.Fatalf("unlicensed vertical: %v, want ErrNoRoute", err)
	}
	// vertical "" checks the market as a whole (pre-money checks don't name one).
	if _, err := tt.Resolve("se", ""); err != nil {
		t.Fatalf("market-level check: %v", err)
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"missing entity": `[{"jurisdiction":"mt","state":"active"}]`,
		"duplicate jur":  `[{"jurisdiction":"mt","entity_id":"a","state":"active"},{"jurisdiction":"mt","entity_id":"b","state":"active"}]`,
		"unknown state":  `[{"jurisdiction":"mt","entity_id":"a","state":"vibes"}]`,
		"not a list":     `{"jurisdiction":"mt"}`,
	}
	for name, raw := range cases {
		if _, err := market.ParseTable(raw); err == nil {
			t.Errorf("%s: malformed route accepted", name)
		}
	}
}
