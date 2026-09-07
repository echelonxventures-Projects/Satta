package money

import (
	"fmt"
	"math/big"
)

// RoundingMode names how an inexact result is resolved. There is no default:
// every call site that can produce an inexact result must name a mode, which is
// itself resolved from policy rather than chosen by the developer.
type RoundingMode uint8

const (
	// RoundingUnspecified causes inexact operations to fail with
	// ErrRoundingRequired. It is the zero value so that forgetting to supply a
	// mode is an error rather than a silent choice.
	RoundingUnspecified RoundingMode = iota
	// RoundingDown truncates toward zero.
	RoundingDown
	// RoundingUp rounds away from zero.
	RoundingUp
	// RoundingFloor rounds toward negative infinity.
	RoundingFloor
	// RoundingCeil rounds toward positive infinity.
	RoundingCeil
	// RoundingHalfUp rounds halves away from zero.
	RoundingHalfUp
	// RoundingHalfDown rounds halves toward zero.
	RoundingHalfDown
	// RoundingHalfEven rounds halves to the nearest even value.
	RoundingHalfEven
)

// roundingNames drives String and ParseRoundingMode. Keeping one table avoids
// the two drifting apart.
var roundingNames = map[RoundingMode]string{
	RoundingUnspecified: "unspecified",
	RoundingDown:        "down",
	RoundingUp:          "up",
	RoundingFloor:       "floor",
	RoundingCeil:        "ceil",
	RoundingHalfUp:      "half_up",
	RoundingHalfDown:    "half_down",
	RoundingHalfEven:    "half_even",
}

func (m RoundingMode) String() string {
	if s, ok := roundingNames[m]; ok {
		return s
	}
	return fmt.Sprintf("RoundingMode(%d)", uint8(m))
}

// ParseRoundingMode resolves a configured mode name. Used when a rounding
// policy arrives from the config/rules engine as a string.
func ParseRoundingMode(s string) (RoundingMode, error) {
	for mode, name := range roundingNames {
		if name == s && mode != RoundingUnspecified {
			return mode, nil
		}
	}
	return RoundingUnspecified, fmt.Errorf("money: unknown rounding mode %q", s)
}

// Ratio is an exact proportion held as an integer fraction. Rake percentages,
// FX spreads, commission rates, tax rates and bonus contribution weights are all
// Ratios, never floats.
type Ratio struct {
	num int64
	den int64
}

// NewRatio returns num/den. The denominator must be non-zero. A negative
// denominator is normalised onto the numerator so that sign handling downstream
// has one shape to consider.
func NewRatio(num, den int64) (Ratio, error) {
	if den == 0 {
		return Ratio{}, fmt.Errorf("%w: zero denominator", ErrInvalidRatio)
	}
	if den < 0 {
		if den == -den { // math.MinInt64
			return Ratio{}, fmt.Errorf("%w: denominator %d cannot be negated", ErrInvalidRatio, den)
		}
		num, den = -num, -den
	}
	return Ratio{num: num, den: den}, nil
}

// MustRatio is NewRatio for known-valid literals in tests and fixtures.
func MustRatio(num, den int64) Ratio {
	r, err := NewRatio(num, den)
	if err != nil {
		panic(err)
	}
	return r
}

// Numerator returns the ratio's numerator.
func (r Ratio) Numerator() int64 { return r.num }

// Denominator returns the ratio's denominator.
func (r Ratio) Denominator() int64 { return r.den }

// IsZero reports whether the ratio is exactly zero.
func (r Ratio) IsZero() bool { return r.num == 0 }

func (r Ratio) String() string { return fmt.Sprintf("%d/%d", r.num, r.den) }

// ParseRatioPercent converts a percentage expressed as an exact decimal string
// (for example "2.5" for two and a half percent) into a Ratio. It exists so
// that configured percentages never pass through a float on their way into
// money arithmetic.
func ParseRatioPercent(s string) (Ratio, error) {
	num, den, err := parseDecimalFraction(s)
	if err != nil {
		return Ratio{}, err
	}
	// Divide by 100 by scaling the denominator.
	scaledDen := new(big.Int).Mul(den, big.NewInt(100))
	if !scaledDen.IsInt64() || !num.IsInt64() {
		return Ratio{}, fmt.Errorf("%w: percentage %q needs more precision than int64 allows", ErrInvalidRatio, s)
	}
	return NewRatio(num.Int64(), scaledDen.Int64())
}

// parseDecimalFraction converts an exact decimal string into num/den big
// integers without using floating point.
func parseDecimalFraction(s string) (num, den *big.Int, err error) {
	rat, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, nil, fmt.Errorf("%w: %q is not an exact decimal", ErrInvalidRatio, s)
	}
	return new(big.Int).Set(rat.Num()), new(big.Int).Set(rat.Denom()), nil
}

// ApplyRatio returns a*r, rounded according to mode.
//
// This is the single chokepoint for proportional money math in the platform.
// Rake, FX spread, commission, tax withholding and bonus contribution all route
// through it, so the rounding behaviour is auditable in one place.
func (a Amount) ApplyRatio(r Ratio, mode RoundingMode) (Amount, error) {
	if r.den == 0 {
		return Amount{}, fmt.Errorf("%w: zero denominator", ErrInvalidRatio)
	}
	product := new(big.Int).Mul(big.NewInt(a.minor), big.NewInt(r.num))
	quotient, err := divRound(product, big.NewInt(r.den), mode)
	if err != nil {
		return Amount{}, err
	}
	if !quotient.IsInt64() {
		return Amount{}, fmt.Errorf("%w: %d * %s", ErrOverflow, a.minor, r)
	}
	return Amount{minor: quotient.Int64(), currency: a.currency}, nil
}

// DivInt returns a/n rounded according to mode. Used for per-unit splits where
// an exact allocation is not required; when the parts must sum back to the
// original, use Allocate instead.
func (a Amount) DivInt(n int64, mode RoundingMode) (Amount, error) {
	if n == 0 {
		return Amount{}, fmt.Errorf("%w: division by zero", ErrInvalidRatio)
	}
	quotient, err := divRound(big.NewInt(a.minor), big.NewInt(n), mode)
	if err != nil {
		return Amount{}, err
	}
	if !quotient.IsInt64() {
		return Amount{}, fmt.Errorf("%w: %d / %d", ErrOverflow, a.minor, n)
	}
	return Amount{minor: quotient.Int64(), currency: a.currency}, nil
}

// divRound computes num/den as an integer using the given rounding mode. den
// must be non-zero. The sign of den is normalised first so that the mode logic
// only has to reason about the sign of the quotient.
func divRound(num, den *big.Int, mode RoundingMode) (*big.Int, error) {
	if den.Sign() == 0 {
		return nil, fmt.Errorf("%w: zero denominator", ErrInvalidRatio)
	}
	n, d := new(big.Int).Set(num), new(big.Int).Set(den)
	if d.Sign() < 0 {
		n.Neg(n)
		d.Neg(d)
	}

	// QuoRem gives truncated-toward-zero quotient and a remainder with the sign
	// of the numerator.
	quo, rem := new(big.Int).QuoRem(n, d, new(big.Int))
	if rem.Sign() == 0 {
		return quo, nil
	}

	negative := n.Sign() < 0
	one := big.NewInt(1)
	awayFromZero := func() *big.Int {
		if negative {
			return new(big.Int).Sub(quo, one)
		}
		return new(big.Int).Add(quo, one)
	}

	switch mode {
	case RoundingUnspecified:
		return nil, fmt.Errorf("%w: %s/%s", ErrRoundingRequired, num, den)
	case RoundingDown:
		return quo, nil
	case RoundingUp:
		return awayFromZero(), nil
	case RoundingFloor:
		if negative {
			return awayFromZero(), nil
		}
		return quo, nil
	case RoundingCeil:
		if negative {
			return quo, nil
		}
		return awayFromZero(), nil
	case RoundingHalfUp, RoundingHalfDown, RoundingHalfEven:
		// Compare |rem|*2 against d to classify the fraction.
		twiceRem := new(big.Int).Abs(rem)
		twiceRem.Lsh(twiceRem, 1)
		switch twiceRem.Cmp(d) {
		case -1: // below half
			return quo, nil
		case 1: // above half
			return awayFromZero(), nil
		default: // exactly half
			switch mode {
			case RoundingHalfUp:
				return awayFromZero(), nil
			case RoundingHalfDown:
				return quo, nil
			default: // RoundingHalfEven
				if quo.Bit(0) == 0 {
					return quo, nil
				}
				return awayFromZero(), nil
			}
		}
	default:
		return nil, fmt.Errorf("money: unsupported rounding mode %d", uint8(mode))
	}
}
