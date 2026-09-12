package catalog_test

import (
	"errors"
	"testing"

	"github.com/satta/platform/libs/go/catalog"
)

func registry(t *testing.T) *catalog.Registry {
	t.Helper()
	r, err := catalog.NewRegistry([]catalog.Product{{
		Code: "sportsbook", Name: "Sportsbook",
		SKUs: []catalog.SKU{{
			Code: "core", Name: "Core Sportsbook",
			Capabilities: []string{"bet.place", "bet.cashout"},
			Meters: []catalog.Meter{
				{Code: "bet.placed", Unit: "count", Dimensions: []string{"channel"}},
				{Code: "handle.wagered", Unit: "minor-units"},
			},
		}},
	}})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return r
}

func TestRegistryLookups(t *testing.T) {
	r := registry(t)
	if _, err := r.Product("nope"); !errors.Is(err, catalog.ErrUnknownProduct) {
		t.Fatalf("unknown product: %v", err)
	}
	if _, err := r.SKU("sportsbook", "nope"); !errors.Is(err, catalog.ErrUnknownSKU) {
		t.Fatalf("unknown SKU: %v", err)
	}
	if err := r.ValidateUsage("sportsbook", "bet.placed"); err != nil {
		t.Fatalf("known meter rejected: %v", err)
	}
	if err := r.ValidateUsage("sportsbook", "ghost.meter"); err == nil {
		t.Fatal("unregistered meter accepted — it could never bill correctly")
	}
}

func TestRegistryRejectsMalformedData(t *testing.T) {
	cases := map[string][]catalog.Product{
		"duplicate product": {
			{Code: "a", Name: "A", SKUs: []catalog.SKU{{Code: "x", Capabilities: []string{"c"}}}},
			{Code: "a", Name: "A2", SKUs: []catalog.SKU{{Code: "x", Capabilities: []string{"c"}}}},
		},
		"SKU without capabilities": {
			{Code: "a", Name: "A", SKUs: []catalog.SKU{{Code: "x"}}},
		},
		"meter without unit": {
			{Code: "a", Name: "A", SKUs: []catalog.SKU{{Code: "x", Capabilities: []string{"c"},
				Meters: []catalog.Meter{{Code: "m"}}}}},
		},
	}
	for name, products := range cases {
		if _, err := catalog.NewRegistry(products); err == nil {
			t.Errorf("%s: malformed registry accepted", name)
		}
	}
}

func entitlements(t *testing.T, raw string) *catalog.Entitlements {
	t.Helper()
	e, err := catalog.ParseEntitlements(registry(t), raw)
	if err != nil {
		t.Fatalf("ParseEntitlements: %v", err)
	}
	return e
}

func TestEntitlementGate(t *testing.T) {
	e := entitlements(t, `[
		{"product_code":"sportsbook","sku_code":"core","capability":"bet.place","jurisdictions":["mt","se"]}
	]`)
	if err := e.Check("sportsbook", "bet.place", "mt"); err != nil {
		t.Fatalf("granted access denied: %v", err)
	}
	// Clear typed denial, not a crash (DoD gate 9).
	err := e.Check("sportsbook", "bet.place", "de")
	if !errors.Is(err, catalog.ErrNotEntitled) {
		t.Fatalf("ungranted jurisdiction: %v, want ErrNotEntitled", err)
	}
	if err := e.Check("sportsbook", "bet.cashout", "mt"); !errors.Is(err, catalog.ErrNotEntitled) {
		t.Fatalf("unganted capability: %v, want ErrNotEntitled", err)
	}
}

func TestParseEntitlementsCrossChecksRegistry(t *testing.T) {
	// An entitlement naming a capability the SKU does not have is a typo
	// that would deny real access forever — loud at load, not silent.
	if _, err := catalog.ParseEntitlements(registry(t),
		`[{"product_code":"sportsbook","sku_code":"core","capability":"bet.typo"}]`); err == nil {
		t.Fatal("entitlement to unknown capability accepted")
	}
}

func TestEmptyJurisdictionsMeansMarketWideGrant(t *testing.T) {
	e := entitlements(t, `[{"product_code":"sportsbook","sku_code":"core","capability":"bet.place"}]`)
	if err := e.Check("sportsbook", "bet.place", "any-where"); err != nil {
		t.Fatalf("market-wide grant: %v", err)
	}
}
