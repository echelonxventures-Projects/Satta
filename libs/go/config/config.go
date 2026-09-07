// Package config resolves business policy values from the layered configuration
// hierarchy: global < jurisdiction < tenant < brand.
//
// DEFINITION OF DONE, GATE 1: no literal business-policy value may appear in
// code. Rake percentages, deposit limits, KYC thresholds, bonus wagering
// multipliers, rounding modes, retention periods and jurisdiction rules are all
// resolved through this package.
//
// Two deliberate design choices support that:
//
//   - There is no Resolve-with-default. A default supplied at the call site is a
//     hardcoded policy value wearing a disguise, and it is the mechanism by
//     which a jurisdiction ends up silently running another jurisdiction's
//     rules. A missing key is an error the operator must fix in configuration.
//   - Every resolved Value carries the scope that supplied it. When an auditor
//     asks why a particular rake was applied to a particular hand, the answer
//     has to be traceable to a configuration layer, not inferred.
//
// This package defines the port and ships a file-backed implementation for local
// development and tests. The served implementation, together with the rules
// engine that evaluates conditional policy, is a later piece of work; it plugs
// in behind the same interface.
package config

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/satta/platform/libs/go/money"
	"github.com/satta/platform/libs/go/tenancy"
)

// Errors returned by this package.
var (
	// ErrNotFound is returned when no layer defines the requested key for the
	// requested scope.
	ErrNotFound = errors.New("config: key not defined for scope")
	// ErrWrongType is returned when a value exists but cannot be interpreted as
	// the requested type.
	ErrWrongType = errors.New("config: value has wrong type")
)

// Resolver resolves configuration keys within a tenancy scope. It is the port
// that domain code depends on.
type Resolver interface {
	// Resolve returns the value of key for scope, choosing the most specific
	// matching layer. It returns ErrNotFound if no layer defines the key.
	Resolve(ctx context.Context, scope tenancy.Scope, key string) (Value, error)
	// Version identifies the configuration state that resolutions are being
	// served from. It is recorded alongside decisions so that a policy outcome
	// can be reproduced against the configuration that produced it.
	Version(ctx context.Context) (string, error)
}

// Value is a resolved configuration value together with its provenance.
type Value struct {
	key   string
	raw   string
	scope tenancy.Scope
	// version of the configuration state this value came from.
	version string
}

// NewValue constructs a Value. Implementations of Resolver use it; domain code
// does not.
func NewValue(key, raw string, scope tenancy.Scope, version string) Value {
	return Value{key: key, raw: raw, scope: scope, version: version}
}

// Key returns the key that was resolved.
func (v Value) Key() string { return v.key }

// Raw returns the underlying string form.
func (v Value) Raw() string { return v.raw }

// Scope returns the layer that supplied the value. This is the provenance an
// audit trail records.
func (v Value) Scope() tenancy.Scope { return v.scope }

// Version returns the configuration version the value came from.
func (v Value) Version() string { return v.version }

// LogAttrs returns provenance as structured-logging pairs.
func (v Value) LogAttrs() []any {
	return []any{
		"config_key", v.key,
		"config_source_scope", v.scope.String(),
		"config_version", v.version,
	}
}

func (v Value) typeErr(want string, err error) error {
	return fmt.Errorf("%w: key %q value %q is not a valid %s (from %s): %v",
		ErrWrongType, v.key, v.raw, want, v.scope.String(), err)
}

// String returns the value as a string.
func (v Value) String() (string, error) {
	if v.raw == "" {
		return "", fmt.Errorf("%w: key %q is empty", ErrWrongType, v.key)
	}
	return v.raw, nil
}

// Int returns the value as a signed 64-bit integer.
func (v Value) Int() (int64, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(v.raw), 10, 64)
	if err != nil {
		return 0, v.typeErr("integer", err)
	}
	return n, nil
}

// Bool returns the value as a boolean.
func (v Value) Bool() (bool, error) {
	b, err := strconv.ParseBool(strings.TrimSpace(v.raw))
	if err != nil {
		return false, v.typeErr("boolean", err)
	}
	return b, nil
}

// Duration returns the value as a duration, e.g. "24h" or "30m".
func (v Value) Duration() (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(v.raw))
	if err != nil {
		return 0, v.typeErr("duration", err)
	}
	return d, nil
}

// StringSlice returns a comma-separated value as a slice, trimming whitespace
// and dropping empty entries.
func (v Value) StringSlice() ([]string, error) {
	parts := strings.Split(v.raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: key %q has no list entries", ErrWrongType, v.key)
	}
	return out, nil
}

// Ratio returns the value as an exact money.Ratio. Accepted forms are "num/den"
// (for example "5/100") and a percentage suffixed with '%' (for example "2.5%").
// A bare decimal is rejected: "0.05" is ambiguous between five percent and five
// hundredths, and money math must not guess.
func (v Value) Ratio() (money.Ratio, error) {
	raw := strings.TrimSpace(v.raw)
	switch {
	case strings.HasSuffix(raw, "%"):
		r, err := money.ParseRatioPercent(strings.TrimSuffix(raw, "%"))
		if err != nil {
			return money.Ratio{}, v.typeErr("percentage", err)
		}
		return r, nil
	case strings.Contains(raw, "/"):
		num, den, ok := strings.Cut(raw, "/")
		if !ok {
			return money.Ratio{}, v.typeErr("fraction", errors.New("missing denominator"))
		}
		n, err := strconv.ParseInt(strings.TrimSpace(num), 10, 64)
		if err != nil {
			return money.Ratio{}, v.typeErr("fraction numerator", err)
		}
		d, err := strconv.ParseInt(strings.TrimSpace(den), 10, 64)
		if err != nil {
			return money.Ratio{}, v.typeErr("fraction denominator", err)
		}
		r, err := money.NewRatio(n, d)
		if err != nil {
			return money.Ratio{}, v.typeErr("fraction", err)
		}
		return r, nil
	default:
		return money.Ratio{}, v.typeErr("ratio",
			errors.New(`expected "num/den" or "N%"; a bare decimal is ambiguous`))
	}
}

// RoundingMode returns the value as a money rounding mode.
func (v Value) RoundingMode() (money.RoundingMode, error) {
	m, err := money.ParseRoundingMode(strings.TrimSpace(v.raw))
	if err != nil {
		return money.RoundingUnspecified, v.typeErr("rounding mode", err)
	}
	return m, nil
}

// Money returns the value as a monetary amount in cur. The configured form is an
// exact decimal string, e.g. "10.00".
func (v Value) Money(cur money.Currency) (money.Amount, error) {
	a, err := money.Parse(strings.TrimSpace(v.raw), cur)
	if err != nil {
		return money.Amount{}, v.typeErr("money amount", err)
	}
	return a, nil
}
