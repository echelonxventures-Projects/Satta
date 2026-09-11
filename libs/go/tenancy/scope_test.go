package tenancy_test

import (
	"context"
	"errors"
	"testing"

	"github.com/satta/platform/libs/go/tenancy"
)

// The tenancy package is the single isolation decision point for the whole
// platform (CheckAccess) plus the scope hierarchy the config resolver orders
// by. These tests pin both roles.

func TestValidateIdentifier(t *testing.T) {
	valid := []string{"tenant-a", "a", "brand_x", "v1-2"}
	for _, v := range valid {
		if err := tenancy.ValidateIdentifier("field", v); err != nil {
			t.Errorf("valid identifier %q rejected: %v", v, err)
		}
	}
	invalid := []string{"", "UPPER", "with space", "semi;colon", "-leading", "trailing-"}
	for _, v := range invalid {
		if err := tenancy.ValidateIdentifier("field", v); err == nil {
			t.Errorf("invalid identifier %q accepted", v)
		} else if !errors.Is(err, tenancy.ErrInvalidIdentifier) {
			t.Errorf("identifier %q error = %v, want ErrInvalidIdentifier", v, err)
		}
	}
}

func TestScopeValidateAndRequireTenant(t *testing.T) {
	// A brand without a tenant is meaningless: the hierarchy has no way to
	// express it and the resolver would have nothing to attach it to.
	if err := (tenancy.Scope{BrandID: "brand"}).Validate(); err == nil {
		t.Error("brand without tenant accepted")
	}
	// Empty scope is a valid global scope...
	if err := (tenancy.Scope{}).Validate(); err != nil {
		t.Errorf("global scope rejected: %v", err)
	}
	// ...but not a valid tenant-scoped operation.
	if err := (tenancy.Scope{}).RequireTenant(); !errors.Is(err, tenancy.ErrNoScope) {
		t.Errorf("RequireTenant on global = %v, want ErrNoScope", err)
	}
}

func TestCoversIsContainmentNotEquality(t *testing.T) {
	global := tenancy.Global()
	tenant := tenancy.Scope{TenantID: "t1"}
	brand := tenancy.Scope{TenantID: "t1", BrandID: "b1"}

	if !global.Covers(tenant) || !global.Covers(brand) {
		t.Error("global scope must cover everything (it is 'any' on every field)")
	}
	if !tenant.Covers(brand) {
		t.Error("tenant scope must cover its own brand")
	}
	if tenant.Covers(tenancy.Scope{TenantID: "t2"}) {
		t.Error("tenant scope covered a different tenant — isolation broken")
	}
	if brand.Covers(tenant) {
		t.Error("brand scope covered the wider tenant scope: containment is not symmetric")
	}
}

func TestSpecificityOrdersScopes(t *testing.T) {
	// The config resolver picks the most specific matching layer; the ordering
	// is load-bearing for global < jurisdiction < tenant < brand.
	global := tenancy.Scope{}
	jurisdiction := tenancy.Scope{JurisdictionCode: "mt"}
	tenant := tenancy.Scope{TenantID: "t1"}
	brand := tenancy.Scope{TenantID: "t1", BrandID: "b1"}
	if global.Specificity() >= jurisdiction.Specificity() ||
		jurisdiction.Specificity() >= tenant.Specificity() ||
		tenant.Specificity() >= brand.Specificity() {
		t.Fatalf("specificity order broken: global=%d jur=%d tenant=%d brand=%d",
			global.Specificity(), jurisdiction.Specificity(), tenant.Specificity(), brand.Specificity())
	}
}

func TestContextRoundTripAndDenyByDefault(t *testing.T) {
	want := tenancy.Scope{TenantID: "t1", JurisdictionCode: "mt"}
	ctx, err := tenancy.WithScope(context.Background(), want)
	if err != nil {
		t.Fatalf("WithScope: %v", err)
	}
	got, ok := tenancy.FromContext(ctx)
	if !ok || got != want {
		t.Fatalf("FromContext = %v, %v", got, ok)
	}

	if _, err := tenancy.RequireFromContext(context.Background()); !errors.Is(err, tenancy.ErrNoScope) {
		t.Fatalf("RequireFromContext on bare context = %v, want ErrNoScope (deny-by-default)", err)
	}
}

func TestCheckAccessIsTheIsolationDecisionPoint(t *testing.T) {
	ctxA, err := tenancy.WithScope(context.Background(), tenancy.Scope{TenantID: "tenant-a"})
	if err != nil {
		t.Fatalf("WithScope: %v", err)
	}

	// Same tenant: allowed.
	if aerr := tenancy.CheckAccess(ctxA, tenancy.Scope{TenantID: "tenant-a", BrandID: "brand-1"}); aerr != nil {
		t.Errorf("own-tenant access denied: %v", aerr)
	}
	// Different tenant: denied, with the scope-mismatch sentinel.
	err = tenancy.CheckAccess(ctxA, tenancy.Scope{TenantID: "tenant-b"})
	if !errors.Is(err, tenancy.ErrScopeMismatch) {
		t.Errorf("cross-tenant access = %v, want ErrScopeMismatch", err)
	}
	// No scope in context: denied, not treated as global.
	if err := tenancy.CheckAccess(context.Background(), tenancy.Scope{TenantID: "tenant-a"}); !errors.Is(err, tenancy.ErrNoScope) {
		t.Errorf("scopeless context = %v, want ErrNoScope (deny-by-default, never global)", err)
	}
}
