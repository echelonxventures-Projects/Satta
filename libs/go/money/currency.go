// Package money provides exact, integer-only monetary arithmetic.
//
// SAFETY INVARIANT 1: no floating-point type appears anywhere in this package's
// API or implementation. There is no conversion to or from float32/float64, by
// design. If you need a proportion, use Ratio. If you need rounding, name the
// RoundingMode explicitly — this package never picks one for you.
package money

import (
	"fmt"
	"strings"
)

// Currency is a unit of account. Currencies are reference data resolved from
// the currency registry; domain code must never construct one from a literal
// scale it invented.
type Currency struct {
	// Code is the registry key: an ISO 4217 alphabetic code for fiat, or a
	// platform key for non-fiat units (crypto assets, bonus funds, sweeps
	// coins, gold coins, loyalty points).
	Code string
	// Scale is the number of decimal places in which amounts of this currency
	// are represented as integers. EUR has scale 2; BTC has scale 8; JPY has
	// scale 0; a loyalty point may have scale 0.
	Scale uint8
	// Kind distinguishes redeemable value from promotional or social units.
	// Compliance, tax and RG rules differ per kind, so the distinction is
	// carried on the currency itself rather than inferred from the code.
	Kind Kind
}

// Kind classifies what a currency represents.
type Kind string

const (
	// KindUnspecified is the zero value and is never valid for arithmetic.
	KindUnspecified Kind = ""
	// KindFiat is government-issued currency.
	KindFiat Kind = "fiat"
	// KindCrypto is a digital asset.
	KindCrypto Kind = "crypto"
	// KindBonus is non-withdrawable promotional balance subject to wagering
	// requirements.
	KindBonus Kind = "bonus"
	// KindSweeps is a sweepstakes entry unit redeemable for cash.
	KindSweeps Kind = "sweeps"
	// KindSocial is a play-only unit with no redemption path.
	KindSocial Kind = "social"
	// KindLoyalty is a points unit.
	KindLoyalty Kind = "loyalty"
)

// IsZero reports whether c is the zero Currency.
func (c Currency) IsZero() bool { return c.Code == "" }

// Validate reports whether c is usable for arithmetic.
func (c Currency) Validate() error {
	if c.Code == "" {
		return fmt.Errorf("money: currency code is empty")
	}
	if c.Code != strings.ToUpper(c.Code) {
		return fmt.Errorf("money: currency code %q must be upper case", c.Code)
	}
	if c.Scale > MaxScale {
		return fmt.Errorf("money: currency %s scale %d exceeds maximum %d", c.Code, c.Scale, MaxScale)
	}
	if c.Kind == KindUnspecified {
		return fmt.Errorf("money: currency %s has no kind", c.Code)
	}
	return nil
}

func (c Currency) String() string { return c.Code }

// MaxScale bounds the decimal places any currency may declare. It exists to
// keep integer conversions provably in range, not as a business rule.
const MaxScale = 18

// Registry resolves currency codes to their definitions. It is a port: the
// local implementation is backed by a seeded file, and the production
// implementation is backed by the configuration service. Domain code depends on
// this interface, never on a hardcoded table.
type Registry interface {
	// Lookup returns the currency for code, or an error if the code is not
	// registered. Lookup must be safe for concurrent use.
	Lookup(code string) (Currency, error)
	// All returns every registered currency.
	All() []Currency
}

// MapRegistry is an in-memory Registry, used by the local seed loader and by
// tests.
type MapRegistry struct {
	byCode map[string]Currency
}

// NewMapRegistry builds a MapRegistry, validating every entry. It returns an
// error rather than silently accepting a malformed definition, because an
// incorrect scale is a money-correctness defect.
func NewMapRegistry(currencies []Currency) (*MapRegistry, error) {
	byCode := make(map[string]Currency, len(currencies))
	for _, c := range currencies {
		if err := c.Validate(); err != nil {
			return nil, err
		}
		if _, dup := byCode[c.Code]; dup {
			return nil, fmt.Errorf("money: duplicate currency %s in registry", c.Code)
		}
		byCode[c.Code] = c
	}
	return &MapRegistry{byCode: byCode}, nil
}

// Lookup implements Registry.
func (r *MapRegistry) Lookup(code string) (Currency, error) {
	c, ok := r.byCode[code]
	if !ok {
		return Currency{}, fmt.Errorf("money: currency %q is not registered", code)
	}
	return c, nil
}

// All implements Registry.
func (r *MapRegistry) All() []Currency {
	out := make([]Currency, 0, len(r.byCode))
	for _, c := range r.byCode {
		out = append(out, c)
	}
	return out
}
