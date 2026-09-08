package money

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
)

// Errors returned by this package. Callers are expected to branch on these
// rather than on message text.
var (
	// ErrCurrencyMismatch is returned when an operation mixes currencies.
	// Mixing is never implicitly resolved; conversion is an explicit FX
	// operation with a recorded rate.
	ErrCurrencyMismatch = errors.New("money: currency mismatch")
	// ErrOverflow is returned when a result cannot be represented in int64
	// minor units.
	ErrOverflow = errors.New("money: arithmetic overflow")
	// ErrInvalidRatio is returned for a zero or unrepresentable denominator.
	ErrInvalidRatio = errors.New("money: invalid ratio")
	// ErrRoundingRequired is returned when an exact result is impossible and no
	// rounding mode was supplied.
	ErrRoundingRequired = errors.New("money: result is inexact and no rounding mode was given")
	// ErrInvalidCurrency is returned when an amount carries an unusable
	// currency.
	ErrInvalidCurrency = errors.New("money: invalid currency")
	// ErrNegativeWeight is returned by Allocate when given a negative weight.
	ErrNegativeWeight = errors.New("money: allocation weights must be non-negative")
)

// Amount is an exact monetary value: a signed integer count of the minor units
// of a Currency.
//
// Amount is a value type and is safe to copy. Its fields are unexported so that
// an Amount cannot be constructed with a currency-inconsistent scale, and so
// that no caller can reach past the arithmetic helpers.
type Amount struct {
	minor    int64
	currency Currency
}

// New returns an Amount of minor units in cur.
func New(minor int64, cur Currency) (Amount, error) {
	if err := cur.Validate(); err != nil {
		return Amount{}, fmt.Errorf("%w: %w", ErrInvalidCurrency, err)
	}
	return Amount{minor: minor, currency: cur}, nil
}

// MustNew is New for cases where the currency is known-valid, such as tests and
// package-level fixtures. It panics on an invalid currency.
func MustNew(minor int64, cur Currency) Amount {
	a, err := New(minor, cur)
	if err != nil {
		//nolint:forbidigo // Must* constructors panic by documented contract
		panic(err)
	}
	return a
}

// Zero returns the zero amount in cur.
func Zero(cur Currency) Amount { return Amount{minor: 0, currency: cur} }

// Minor returns the raw integer minor-unit count.
func (a Amount) Minor() int64 { return a.minor }

// Currency returns the amount's currency.
func (a Amount) Currency() Currency { return a.currency }

// IsZero reports whether the amount is exactly zero.
func (a Amount) IsZero() bool { return a.minor == 0 }

// IsNegative reports whether the amount is strictly less than zero.
func (a Amount) IsNegative() bool { return a.minor < 0 }

// IsPositive reports whether the amount is strictly greater than zero.
func (a Amount) IsPositive() bool { return a.minor > 0 }

// sameCurrency verifies that two amounts may be combined.
func (a Amount) sameCurrency(b Amount) error {
	if a.currency.Code != b.currency.Code || a.currency.Scale != b.currency.Scale {
		return fmt.Errorf("%w: %s(scale %d) vs %s(scale %d)",
			ErrCurrencyMismatch, a.currency.Code, a.currency.Scale, b.currency.Code, b.currency.Scale)
	}
	return nil
}

// Add returns a+b.
func (a Amount) Add(b Amount) (Amount, error) {
	if err := a.sameCurrency(b); err != nil {
		return Amount{}, err
	}
	sum := a.minor + b.minor
	// Signed overflow check: overflow occurred if both operands share a sign
	// that the result does not.
	if (a.minor > 0 && b.minor > 0 && sum < 0) || (a.minor < 0 && b.minor < 0 && sum >= 0) {
		return Amount{}, fmt.Errorf("%w: %d + %d", ErrOverflow, a.minor, b.minor)
	}
	return Amount{minor: sum, currency: a.currency}, nil
}

// Sub returns a-b.
func (a Amount) Sub(b Amount) (Amount, error) {
	neg, err := b.Neg()
	if err != nil {
		return Amount{}, err
	}
	return a.Add(neg)
}

// Neg returns -a.
func (a Amount) Neg() (Amount, error) {
	if a.minor == math.MinInt64 {
		return Amount{}, fmt.Errorf("%w: negating %d", ErrOverflow, a.minor)
	}
	return Amount{minor: -a.minor, currency: a.currency}, nil
}

// Abs returns |a|.
func (a Amount) Abs() (Amount, error) {
	if a.minor >= 0 {
		return a, nil
	}
	return a.Neg()
}

// Cmp returns -1 if a<b, 0 if a==b, +1 if a>b.
func (a Amount) Cmp(b Amount) (int, error) {
	if err := a.sameCurrency(b); err != nil {
		return 0, err
	}
	switch {
	case a.minor < b.minor:
		return -1, nil
	case a.minor > b.minor:
		return 1, nil
	default:
		return 0, nil
	}
}

// Equal reports whether a and b are the same currency and the same value.
// Unlike Cmp it does not error, so it is safe in test assertions and map keys.
func (a Amount) Equal(b Amount) bool {
	return a.currency.Code == b.currency.Code &&
		a.currency.Scale == b.currency.Scale &&
		a.minor == b.minor
}

// MulInt returns a*n. Multiplying money by a dimensionless integer (a stake by
// a line count, a unit price by a quantity) is always exact.
func (a Amount) MulInt(n int64) (Amount, error) {
	product := new(big.Int).Mul(big.NewInt(a.minor), big.NewInt(n))
	if !product.IsInt64() {
		return Amount{}, fmt.Errorf("%w: %d * %d", ErrOverflow, a.minor, n)
	}
	return Amount{minor: product.Int64(), currency: a.currency}, nil
}

// Sum adds amounts left to right. An empty input is an error because there is
// no currency to attribute a zero to; callers holding a possibly-empty slice
// should seed with Zero(currency).
func Sum(amounts ...Amount) (Amount, error) {
	if len(amounts) == 0 {
		return Amount{}, errors.New("money: Sum requires at least one amount")
	}
	total := amounts[0]
	for _, a := range amounts[1:] {
		var err error
		if total, err = total.Add(a); err != nil {
			return Amount{}, err
		}
	}
	return total, nil
}

// String renders the amount for logs and human display, e.g. "-12.34 EUR".
// It is not a serialisation format; use Decimal plus Currency for that.
func (a Amount) String() string {
	return a.Decimal() + " " + a.currency.Code
}

// Decimal renders the amount as an exact decimal string with the currency's
// scale, e.g. "12.34" or "-0.05". No rounding occurs and no float is involved.
func (a Amount) Decimal() string {
	scale := int(a.currency.Scale)
	neg := a.minor < 0

	// Take the magnitude via big.Int so that math.MinInt64 is handled.
	mag := new(big.Int).Abs(big.NewInt(a.minor)).String()
	if scale == 0 {
		if neg {
			return "-" + mag
		}
		return mag
	}
	if len(mag) <= scale {
		mag = strings.Repeat("0", scale-len(mag)+1) + mag
	}
	split := len(mag) - scale
	out := mag[:split] + "." + mag[split:]
	if neg {
		return "-" + out
	}
	return out
}

// Parse reads an exact decimal string as an Amount in cur. The input must have
// no more decimal places than the currency's scale: silently discarding
// precision from an input is how money goes missing, so it is rejected instead.
//
// Accepted forms: "12", "12.34", "-0.05", "+7.1", ".5".
func Parse(s string, cur Currency) (Amount, error) {
	if err := cur.Validate(); err != nil {
		return Amount{}, fmt.Errorf("%w: %w", ErrInvalidCurrency, err)
	}
	in := strings.TrimSpace(s)
	if in == "" {
		return Amount{}, fmt.Errorf("money: cannot parse empty string as %s", cur.Code)
	}

	neg := false
	switch in[0] {
	case '-':
		neg = true
		in = in[1:]
	case '+':
		in = in[1:]
	}

	intPart, fracPart := in, ""
	if dot := strings.IndexByte(in, '.'); dot >= 0 {
		intPart, fracPart = in[:dot], in[dot+1:]
	}
	if intPart == "" && fracPart == "" {
		return Amount{}, fmt.Errorf("money: %q has no digits", s)
	}
	if err := allDigits(intPart); err != nil {
		return Amount{}, fmt.Errorf("money: %q: %w", s, err)
	}
	if err := allDigits(fracPart); err != nil {
		return Amount{}, fmt.Errorf("money: %q: %w", s, err)
	}
	scale := int(cur.Scale)
	if len(fracPart) > scale {
		return Amount{}, fmt.Errorf(
			"money: %q has %d decimal places but %s has scale %d; rounding must be explicit",
			s, len(fracPart), cur.Code, scale)
	}

	digits := intPart + fracPart + strings.Repeat("0", scale-len(fracPart))
	if digits == "" {
		digits = "0"
	}
	val, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return Amount{}, fmt.Errorf("money: %q is not a valid decimal", s)
	}
	if neg {
		val.Neg(val)
	}
	if !val.IsInt64() {
		return Amount{}, fmt.Errorf("%w: %q exceeds int64 minor units", ErrOverflow, s)
	}
	return Amount{minor: val.Int64(), currency: cur}, nil
}

func allDigits(s string) error {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return fmt.Errorf("unexpected character %q", s[i])
		}
	}
	return nil
}
