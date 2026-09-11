package config_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/satta/platform/libs/go/config"
	"github.com/satta/platform/libs/go/money"
	"github.com/satta/platform/libs/go/tenancy"
)

// The config resolver is the platform's answer to "no hardcoded policy"
// (DoD gate 1): every policy value in every service resolves through here.
// Its two load-bearing properties are tested first — precedence by
// specificity, and the absence of any defaulting path.

var (
	global       = tenancy.Scope{}
	jurisdiction = tenancy.Scope{JurisdictionCode: "mt"}
	tenant       = tenancy.Scope{TenantID: "t1"}
	brand        = tenancy.Scope{TenantID: "t1", BrandID: "b1"}
)

func newTestResolver(t *testing.T, layers ...config.Layer) *config.FileResolver {
	t.Helper()
	r, err := config.NewStaticResolver("test-v1", layers...)
	if err != nil {
		t.Fatalf("NewStaticResolver: %v", err)
	}
	return r
}

func TestPrecedenceNarrowestLayerWins(t *testing.T) {
	r := newTestResolver(t,
		config.Layer{Scope: global, Values: map[string]string{"kyc.threshold_minor_units": "1000"}},
		config.Layer{Scope: jurisdiction, Values: map[string]string{"kyc.threshold_minor_units": "2000"}},
		config.Layer{Scope: tenant, Values: map[string]string{"kyc.threshold_minor_units": "3000"}},
		config.Layer{Scope: brand, Values: map[string]string{"kyc.threshold_minor_units": "4000"}},
	)
	ctx := context.Background()

	cases := []struct {
		scope tenancy.Scope
		want  string
	}{
		{brand, "4000"},  // exact brand layer
		{tenant, "3000"}, // tenant layer covers tenant-wide requests
		{jurisdiction, "2000"},
		{global, "1000"},
		// A scope the brand layer does not cover falls to the tenant layer,
		// not to the brand: layers cover or they do not match at all.
		{tenancy.Scope{TenantID: "t1", BrandID: "other"}, "3000"},
	}
	for _, tc := range cases {
		v, err := r.Resolve(ctx, tc.scope, "kyc.threshold_minor_units")
		if err != nil {
			t.Fatalf("resolve at %v: %v", tc.scope, err)
		}
		if v.Raw() != tc.want {
			t.Errorf("scope %v: raw = %q, want %q", tc.scope, v.Raw(), tc.want)
		}
	}
}

func TestMissingKeyIsAnErrorNeverADefault(t *testing.T) {
	// The design decision this platform is built on: an undefined key is a
	// deployment misconfiguration that must surface at boot or on access —
	// never a silent zero/empty value that production traffic then treats as
	// policy.
	r := newTestResolver(t, config.Layer{Scope: global, Values: map[string]string{}})
	_, err := r.Resolve(context.Background(), tenant, "rg.limit.daily_deposit_minor_units")
	if !errors.Is(err, config.ErrNotFound) {
		t.Fatalf("missing key = %v, want ErrNotFound", err)
	}
}

func TestLayerNotCoveringScopeIsIgnored(t *testing.T) {
	// A tenant-t1 layer must not answer for tenant-t2, even when t2 has no
	// layer of its own — isolation over availability.
	r := newTestResolver(t,
		config.Layer{Scope: tenancy.Scope{TenantID: "t1"}, Values: map[string]string{"bonus.rate": "5/100"}},
	)
	_, err := r.Resolve(context.Background(), tenancy.Scope{TenantID: "t2"}, "bonus.rate")
	if !errors.Is(err, config.ErrNotFound) {
		t.Fatalf("cross-tenant leak: %v", err)
	}
}

func TestTypedAccessors(t *testing.T) {
	r := newTestResolver(t, config.Layer{Scope: global, Values: map[string]string{
		"name":       "operator",
		"reel_count": "5",
		"enabled":    "true",
		"timeout":    "500ms",
		"countries":  "mt, ee, se",
		"rake":       "5/100",
		"rounding":   "half_even",
		"threshold":  "12.34",
	}})
	ctx := context.Background()
	must := func(key string) config.Value {
		t.Helper()
		v, err := r.Resolve(ctx, global, key)
		if err != nil {
			t.Fatalf("resolve %q: %v", key, err)
		}
		return v
	}

	nameStr, nameErr := must("name").String()
	if nameErr != nil {
		t.Fatalf("String: %v", nameErr)
	}
	if nameStr != "operator" {
		t.Errorf("String = %q", nameStr)
	}
	reels, reelsErr := must("reel_count").Int()
	if reelsErr != nil {
		t.Fatalf("Int: %v", reelsErr)
	}
	if reels != 5 {
		t.Errorf("Int = %d", reels)
	}
	enabled, enabledErr := must("enabled").Bool()
	if enabledErr != nil {
		t.Fatalf("Bool: %v", enabledErr)
	}
	if !enabled {
		t.Errorf("Bool = %v", enabled)
	}
	timeout, timeoutErr := must("timeout").Duration()
	if timeoutErr != nil {
		t.Fatalf("Duration: %v", timeoutErr)
	}
	if timeout != 500*time.Millisecond {
		t.Errorf("Duration = %v", timeout)
	}
	countries, countriesErr := must("countries").StringSlice()
	if countriesErr != nil {
		t.Fatalf("StringSlice: %v", countriesErr)
	}
	if len(countries) != 3 || countries[0] != "mt" {
		t.Errorf("StringSlice = %v", countries)
	}
	ratio, err := must("rake").Ratio()
	if err != nil || ratio.String() != "5/100" {
		t.Errorf("Ratio = %v, %v — policy rates must parse exactly", ratio, err)
	}
	mode, err := must("rounding").RoundingMode()
	if err != nil || mode != money.RoundingHalfEven {
		t.Errorf("RoundingMode = %v, %v", mode, err)
	}
	eur := money.Currency{Code: "EUR", Scale: 2, Kind: money.KindFiat}
	amt, err := must("threshold").Money(eur)
	if err != nil || amt.Minor() != 1234 {
		t.Errorf("Money = %v, %v — must parse to minor units exactly", amt, err)
	}

	// Wrong types are reported as wrong, never coerced.
	if _, err := must("name").Int(); err == nil {
		t.Error("Int on a non-integer coerced instead of failing")
	}
	if _, err := must("reel_count").Bool(); err == nil {
		t.Error("Bool on a non-boolean coerced instead of failing")
	}
}

func TestPreflightRequireReportsAllMissingKeys(t *testing.T) {
	// Boot-time preflight exists so a misconfigured deployment fails loudly
	// before traffic, with every missing key named in one report.
	r := newTestResolver(t, config.Layer{Scope: tenant, Values: map[string]string{
		"present.one": "1",
	}})
	err := config.Require(context.Background(), r, tenant, "present.one", "missing.two", "missing.three")
	if err == nil {
		t.Fatal("Require passed with missing keys")
	}
	if !contains(err.Error(), "missing.two") || !contains(err.Error(), "missing.three") {
		t.Fatalf("Require reported only %v; every missing key must be named", err)
	}
}

func TestResolveRejectsInvalidScope(t *testing.T) {
	r := newTestResolver(t, config.Layer{Scope: global, Values: map[string]string{"k": "v"}})
	// A brand without a tenant is not a position in the hierarchy.
	if _, err := r.Resolve(context.Background(), tenancy.Scope{BrandID: "orphan"}, "k"); err == nil {
		t.Fatal("invalid scope accepted")
	}
}

func TestVersionIsReported(t *testing.T) {
	r := newTestResolver(t, config.Layer{Scope: global, Values: map[string]string{}})
	v, err := r.Version(context.Background())
	if err != nil || v != "test-v1" {
		t.Fatalf("Version = %q, %v", v, err)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
