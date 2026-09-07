// Package tenancy carries the tenancy scope of a request through the call stack
// and provides the primitives that enforce isolation between tenants.
//
// Isolation is enforced in two independent layers, deliberately:
//
//  1. The application layer requires a Scope in context before any tenant data
//     is touched. A missing scope is an error, never an implicit "all tenants".
//  2. The database layer sets a per-transaction session variable that PostgreSQL
//     row-level security policies read. Even if application code forgets a
//     WHERE clause, the database will not return another tenant's rows.
//
// One layer alone is not enough: application filters get forgotten in new code
// paths, and RLS alone cannot express brand-level or jurisdiction-level rules.
package tenancy

import (
	"context"
	"errors"
	"fmt"
	"regexp"
)

// Errors returned by this package.
var (
	// ErrNoScope is returned when tenant-scoped work is attempted without a
	// scope in context. Deny-by-default: no scope means no access.
	ErrNoScope = errors.New("tenancy: no tenant scope in context")
	// ErrScopeMismatch is returned when a caller's scope does not permit access
	// to the requested resource's scope.
	ErrScopeMismatch = errors.New("tenancy: scope mismatch")
	// ErrInvalidIdentifier is returned for an identifier that fails validation.
	ErrInvalidIdentifier = errors.New("tenancy: invalid identifier")
)

// identifierPattern constrains tenancy identifiers. They are interpolated into
// log fields, metric labels, chain identifiers and database session variables,
// so the permitted character set is narrow on purpose.
var identifierPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9_-]{0,61}[a-z0-9])?$`)

// ValidateIdentifier checks a single tenancy identifier.
func ValidateIdentifier(field, value string) error {
	if !identifierPattern.MatchString(value) {
		return fmt.Errorf("%w: %s=%q must match %s", ErrInvalidIdentifier, field, value, identifierPattern)
	}
	return nil
}

// Scope is a position in the tenancy hierarchy.
//
// Configuration and policy precedence across this hierarchy is
// global < jurisdiction < tenant < brand; narrower scopes override wider ones.
type Scope struct {
	// TenantID is the operator or partner. Empty only for platform-global scope.
	TenantID string
	// BrandID is a brand within the tenant. Empty means tenant-wide.
	BrandID string
	// JurisdictionCode is the regulatory jurisdiction key. Empty means
	// jurisdiction-agnostic.
	JurisdictionCode string
	// RegionCode is the deployment region that owns the data, used for
	// residency enforcement.
	RegionCode string
}

// Global is the platform-wide scope. It is not a wildcard for reading tenant
// data; it identifies configuration and platform-owned records.
func Global() Scope { return Scope{} }

// IsGlobal reports whether the scope names no tenant.
func (s Scope) IsGlobal() bool { return s.TenantID == "" }

// Validate checks that every populated field is a well-formed identifier, and
// that the hierarchy is not inverted (a brand without a tenant is meaningless).
func (s Scope) Validate() error {
	if s.TenantID != "" {
		if err := ValidateIdentifier("tenant_id", s.TenantID); err != nil {
			return err
		}
	}
	if s.BrandID != "" {
		if s.TenantID == "" {
			return fmt.Errorf("%w: brand_id=%q given without tenant_id", ErrInvalidIdentifier, s.BrandID)
		}
		if err := ValidateIdentifier("brand_id", s.BrandID); err != nil {
			return err
		}
	}
	if s.JurisdictionCode != "" {
		if err := ValidateIdentifier("jurisdiction_code", s.JurisdictionCode); err != nil {
			return err
		}
	}
	if s.RegionCode != "" {
		if err := ValidateIdentifier("region_code", s.RegionCode); err != nil {
			return err
		}
	}
	return nil
}

// RequireTenant returns an error unless the scope names a tenant. Call this at
// the top of any operation that reads or writes tenant data.
func (s Scope) RequireTenant() error {
	if s.TenantID == "" {
		return fmt.Errorf("%w: operation requires a tenant", ErrNoScope)
	}
	return s.Validate()
}

// Specificity ranks how narrow the scope is. Used by the configuration resolver
// to order candidate layers: higher wins.
func (s Scope) Specificity() int {
	score := 0
	if s.JurisdictionCode != "" {
		score += 1
	}
	if s.RegionCode != "" {
		score += 2
	}
	if s.TenantID != "" {
		score += 4
	}
	if s.BrandID != "" {
		score += 8
	}
	return score
}

// Covers reports whether s is a scope that the more specific scope other falls
// under. An empty field in s means "any", so Global().Covers(anything) is true.
// This is the containment test used when matching configuration layers, and
// when checking that a caller may act on a resource.
func (s Scope) Covers(other Scope) bool {
	if s.TenantID != "" && s.TenantID != other.TenantID {
		return false
	}
	if s.BrandID != "" && s.BrandID != other.BrandID {
		return false
	}
	if s.JurisdictionCode != "" && s.JurisdictionCode != other.JurisdictionCode {
		return false
	}
	if s.RegionCode != "" && s.RegionCode != other.RegionCode {
		return false
	}
	return true
}

// String renders the scope compactly for logs and error messages.
func (s Scope) String() string {
	out := "global"
	if s.TenantID != "" {
		out = "tenant=" + s.TenantID
	}
	if s.BrandID != "" {
		out += " brand=" + s.BrandID
	}
	if s.JurisdictionCode != "" {
		out += " jurisdiction=" + s.JurisdictionCode
	}
	if s.RegionCode != "" {
		out += " region=" + s.RegionCode
	}
	return out
}

// LogAttrs returns the scope as alternating key/value pairs suitable for
// structured logging. Kept here so every service labels tenancy identically.
func (s Scope) LogAttrs() []any {
	attrs := make([]any, 0, 8)
	if s.TenantID != "" {
		attrs = append(attrs, "tenant_id", s.TenantID)
	}
	if s.BrandID != "" {
		attrs = append(attrs, "brand_id", s.BrandID)
	}
	if s.JurisdictionCode != "" {
		attrs = append(attrs, "jurisdiction_code", s.JurisdictionCode)
	}
	if s.RegionCode != "" {
		attrs = append(attrs, "region_code", s.RegionCode)
	}
	return attrs
}

type scopeContextKey struct{}

// WithScope returns a context carrying the given scope. The scope is validated
// here so that an invalid one cannot travel any further into the system.
func WithScope(ctx context.Context, s Scope) (context.Context, error) {
	if err := s.Validate(); err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, scopeContextKey{}, s), nil
}

// MustWithScope is WithScope for tests and fixtures where the scope is
// known-valid.
func MustWithScope(ctx context.Context, s Scope) context.Context {
	out, err := WithScope(ctx, s)
	if err != nil {
		panic(err)
	}
	return out
}

// FromContext returns the scope in ctx. The boolean reports whether one was
// present; callers that require a scope should use RequireFromContext.
func FromContext(ctx context.Context) (Scope, bool) {
	s, ok := ctx.Value(scopeContextKey{}).(Scope)
	return s, ok
}

// RequireFromContext returns the scope in ctx, or ErrNoScope. This is the
// function tenant-data code paths should call.
func RequireFromContext(ctx context.Context) (Scope, error) {
	s, ok := FromContext(ctx)
	if !ok {
		return Scope{}, ErrNoScope
	}
	if err := s.RequireTenant(); err != nil {
		return Scope{}, err
	}
	return s, nil
}

// CheckAccess verifies that the scope in ctx is permitted to act on resource.
// It is the single place cross-tenant access is decided, so that the rule is
// testable in isolation rather than restated at every call site.
func CheckAccess(ctx context.Context, resource Scope) error {
	caller, err := RequireFromContext(ctx)
	if err != nil {
		return err
	}
	if caller.TenantID != resource.TenantID {
		return fmt.Errorf("%w: caller %s may not access %s",
			ErrScopeMismatch, caller.String(), resource.String())
	}
	// A caller pinned to one brand may not reach another brand's data. A
	// tenant-wide caller (empty BrandID) may reach any of its brands.
	if caller.BrandID != "" && resource.BrandID != "" && caller.BrandID != resource.BrandID {
		return fmt.Errorf("%w: caller %s may not access %s",
			ErrScopeMismatch, caller.String(), resource.String())
	}
	return nil
}
