package money_test

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/satta/platform/libs/go/money"
)

var (
	eur = money.Currency{Code: "EUR", Scale: 2, Kind: money.KindFiat}
	jpy = money.Currency{Code: "JPY", Scale: 0, Kind: money.KindFiat}
	btc = money.Currency{Code: "BTC", Scale: 8, Kind: money.KindCrypto}
	usd = money.Currency{Code: "USD", Scale: 2, Kind: money.KindFiat}
)

// TestNoFloatingPointInMoneyPackage enforces SAFETY INVARIANT 1 structurally
// rather than by convention. If someone introduces a float into money
// arithmetic, this fails before the code can reach the ledger.
func TestNoFloatingPointInMoneyPackage(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse money package: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatal("no packages parsed; test is not exercising anything")
	}

	banned := map[string]bool{"float32": true, "float64": true, "complex64": true, "complex128": true}
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.Ident:
					if banned[node.Name] {
						t.Errorf("%s: forbidden type %q in money package (SAFETY INVARIANT 1)",
							filepath.Base(name), node.Name)
					}
				case *ast.BasicLit:
					if node.Kind == token.FLOAT || node.Kind == token.IMAG {
						t.Errorf("%s: forbidden floating-point literal %q in money package (SAFETY INVARIANT 1)",
							filepath.Base(name), node.Value)
					}
				}
				return true
			})
		}
	}
}

func TestCurrencyValidate(t *testing.T) {
	tests := []struct {
		name    string
		cur     money.Currency
		wantErr bool
	}{
		{"valid fiat", eur, false},
		{"valid zero scale", jpy, false},
		{"empty code", money.Currency{Scale: 2, Kind: money.KindFiat}, true},
		{"lowercase code", money.Currency{Code: "eur", Scale: 2, Kind: money.KindFiat}, true},
		{"scale too large", money.Currency{Code: "XXX", Scale: 19, Kind: money.KindFiat}, true},
		{"no kind", money.Currency{Code: "XXX", Scale: 2}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cur.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestRegistryRejectsDuplicatesAndInvalidEntries(t *testing.T) {
	if _, err := money.NewMapRegistry([]money.Currency{eur, eur}); err == nil {
		t.Fatal("expected duplicate currency to be rejected")
	}
	if _, err := money.NewMapRegistry([]money.Currency{{Code: "BAD"}}); err == nil {
		t.Fatal("expected currency with no kind to be rejected")
	}
	reg, err := money.NewMapRegistry([]money.Currency{eur, jpy, btc})
	if err != nil {
		t.Fatalf("NewMapRegistry: %v", err)
	}
	got, err := reg.Lookup("BTC")
	if err != nil {
		t.Fatalf("Lookup(BTC): %v", err)
	}
	if got.Scale != 8 {
		t.Fatalf("BTC scale = %d, want 8", got.Scale)
	}
	if _, err := reg.Lookup("ZZZ"); err == nil {
		t.Fatal("expected unknown currency lookup to fail")
	}
	if len(reg.All()) != 3 {
		t.Fatalf("All() returned %d currencies, want 3", len(reg.All()))
	}
}

func TestAddSubRejectsCurrencyMismatch(t *testing.T) {
	a := money.MustNew(1000, eur)
	b := money.MustNew(1000, usd)

	if _, err := a.Add(b); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Fatalf("Add across currencies: got %v, want ErrCurrencyMismatch", err)
	}
	if _, err := a.Sub(b); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Fatalf("Sub across currencies: got %v, want ErrCurrencyMismatch", err)
	}
	if _, err := a.Cmp(b); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Fatalf("Cmp across currencies: got %v, want ErrCurrencyMismatch", err)
	}
}

func TestOverflowIsDetectedNotWrapped(t *testing.T) {
	max := money.MustNew(math.MaxInt64, eur)
	one := money.MustNew(1, eur)
	if _, err := max.Add(one); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("MaxInt64 + 1: got %v, want ErrOverflow", err)
	}

	min := money.MustNew(math.MinInt64, eur)
	if _, err := min.Sub(one); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("MinInt64 - 1: got %v, want ErrOverflow", err)
	}
	if _, err := min.Neg(); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("-MinInt64: got %v, want ErrOverflow", err)
	}
	if _, err := max.MulInt(2); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("MaxInt64 * 2: got %v, want ErrOverflow", err)
	}
}

func TestDecimalAndParseRoundTrip(t *testing.T) {
	tests := []struct {
		cur     money.Currency
		minor   int64
		decimal string
	}{
		{eur, 1234, "12.34"},
		{eur, -1234, "-12.34"},
		{eur, 5, "0.05"},
		{eur, -5, "-0.05"},
		{eur, 0, "0.00"},
		{eur, 100, "1.00"},
		{jpy, 1234, "1234"},
		{jpy, -7, "-7"},
		{btc, 1, "0.00000001"},
		{btc, 123456789, "1.23456789"},
	}
	for _, tt := range tests {
		amount := money.MustNew(tt.minor, tt.cur)
		if got := amount.Decimal(); got != tt.decimal {
			t.Errorf("Amount(%d, %s).Decimal() = %q, want %q", tt.minor, tt.cur.Code, got, tt.decimal)
			continue
		}
		parsed, err := money.Parse(tt.decimal, tt.cur)
		if err != nil {
			t.Errorf("Parse(%q, %s): %v", tt.decimal, tt.cur.Code, err)
			continue
		}
		if !parsed.Equal(amount) {
			t.Errorf("Parse(%q) = %s, want %s", tt.decimal, parsed, amount)
		}
	}
}

func TestParseRejectsExcessPrecision(t *testing.T) {
	// Truncating an input silently is how money disappears. Reject instead.
	if _, err := money.Parse("12.345", eur); err == nil {
		t.Fatal("expected 12.345 to be rejected for a scale-2 currency")
	}
	if _, err := money.Parse("1.5", jpy); err == nil {
		t.Fatal("expected 1.5 to be rejected for a scale-0 currency")
	}
	for _, bad := range []string{"", " ", "abc", "1.2.3", "1,50", "--1", "1e5", "0x10"} {
		if _, err := money.Parse(bad, eur); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", bad)
		}
	}
	// Leading-dot and sign forms are accepted.
	for _, good := range []string{".5", "+7.10", "-0.01", "0", "12"} {
		if _, err := money.Parse(good, eur); err != nil {
			t.Errorf("Parse(%q): unexpected error %v", good, err)
		}
	}
}

func TestApplyRatioRequiresExplicitRounding(t *testing.T) {
	stake := money.MustNew(1000, eur) // 10.00
	rake := money.MustRatio(1, 3)     // inexact

	if _, err := stake.ApplyRatio(rake, money.RoundingUnspecified); !errors.Is(err, money.ErrRoundingRequired) {
		t.Fatalf("inexact ratio without a mode: got %v, want ErrRoundingRequired", err)
	}

	// An exact result needs no mode.
	half := money.MustRatio(1, 2)
	got, err := stake.ApplyRatio(half, money.RoundingUnspecified)
	if err != nil {
		t.Fatalf("exact ratio with no mode should succeed: %v", err)
	}
	if got.Minor() != 500 {
		t.Fatalf("10.00 * 1/2 = %s, want 5.00", got)
	}
}

func TestRoundingModes(t *testing.T) {
	// 25/10 = 2.5 exactly, and -25/10 = -2.5, to exercise the tie cases.
	tests := []struct {
		mode      money.RoundingMode
		positive  int64
		negative  int64
		modeLabel string
	}{
		{money.RoundingDown, 2, -2, "down"},
		{money.RoundingUp, 3, -3, "up"},
		{money.RoundingFloor, 2, -3, "floor"},
		{money.RoundingCeil, 3, -2, "ceil"},
		{money.RoundingHalfUp, 3, -3, "half_up"},
		{money.RoundingHalfDown, 2, -2, "half_down"},
		{money.RoundingHalfEven, 2, -2, "half_even"}, // 2 is even
	}
	tenth := money.MustRatio(1, 10)
	for _, tt := range tests {
		t.Run(tt.modeLabel, func(t *testing.T) {
			if got := tt.mode.String(); got != tt.modeLabel {
				t.Fatalf("String() = %q, want %q", got, tt.modeLabel)
			}
			parsed, err := money.ParseRoundingMode(tt.modeLabel)
			if err != nil || parsed != tt.mode {
				t.Fatalf("ParseRoundingMode(%q) = %v, %v", tt.modeLabel, parsed, err)
			}

			pos, err := money.MustNew(25, jpy).ApplyRatio(tenth, tt.mode)
			if err != nil {
				t.Fatalf("positive: %v", err)
			}
			if pos.Minor() != tt.positive {
				t.Errorf("25 * 1/10 = %d, want %d", pos.Minor(), tt.positive)
			}

			neg, err := money.MustNew(-25, jpy).ApplyRatio(tenth, tt.mode)
			if err != nil {
				t.Fatalf("negative: %v", err)
			}
			if neg.Minor() != tt.negative {
				t.Errorf("-25 * 1/10 = %d, want %d", neg.Minor(), tt.negative)
			}
		})
	}

	// Half-even must round the odd-quotient tie away from zero.
	odd, err := money.MustNew(35, jpy).ApplyRatio(tenth, money.RoundingHalfEven)
	if err != nil {
		t.Fatalf("half_even odd tie: %v", err)
	}
	if odd.Minor() != 4 {
		t.Fatalf("35 * 1/10 half_even = %d, want 4", odd.Minor())
	}

	if _, err := money.ParseRoundingMode("nonsense"); err == nil {
		t.Fatal("expected unknown rounding mode to be rejected")
	}
	if _, err := money.ParseRoundingMode("unspecified"); err == nil {
		t.Fatal("unspecified must not be selectable by name")
	}
}

func TestParseRatioPercent(t *testing.T) {
	r, err := money.ParseRatioPercent("2.5")
	if err != nil {
		t.Fatalf("ParseRatioPercent: %v", err)
	}
	// 2.5% of 200.00 EUR is 5.00 EUR, exactly.
	got, err := money.MustNew(20000, eur).ApplyRatio(r, money.RoundingUnspecified)
	if err != nil {
		t.Fatalf("ApplyRatio: %v", err)
	}
	if got.Minor() != 500 {
		t.Fatalf("2.5%% of 200.00 = %s, want 5.00", got)
	}
	if _, err := money.ParseRatioPercent("not-a-number"); err == nil {
		t.Fatal("expected invalid percentage to be rejected")
	}
}

func TestNewRatioRejectsZeroDenominator(t *testing.T) {
	if _, err := money.NewRatio(1, 0); !errors.Is(err, money.ErrInvalidRatio) {
		t.Fatalf("NewRatio(1,0): got %v, want ErrInvalidRatio", err)
	}
	// A negative denominator is normalised onto the numerator.
	r, err := money.NewRatio(1, -2)
	if err != nil {
		t.Fatalf("NewRatio(1,-2): %v", err)
	}
	if r.Numerator() != -1 || r.Denominator() != 2 {
		t.Fatalf("NewRatio(1,-2) = %s, want -1/2", r)
	}
}

func TestAllocateIsExactAndDeterministic(t *testing.T) {
	tests := []struct {
		name    string
		minor   int64
		weights []int64
		want    []int64
	}{
		{"exact thirds of 0.10", 10, []int64{1, 1, 1}, []int64{4, 3, 3}},
		{"exact halves", 10, []int64{1, 1}, []int64{5, 5}},
		{"weighted waterfall", 100, []int64{70, 20, 10}, []int64{70, 20, 10}},
		{"uneven weights", 101, []int64{1, 1, 1}, []int64{34, 34, 33}},
		{"zero weight bucket", 100, []int64{1, 0, 1}, []int64{50, 0, 50}},
		{"negative amount", -10, []int64{1, 1, 1}, []int64{-4, -3, -3}},
		{"single bucket", 999, []int64{5}, []int64{999}},
		{"zero amount", 0, []int64{3, 1}, []int64{0, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parts, err := money.MustNew(tt.minor, eur).Allocate(tt.weights)
			if err != nil {
				t.Fatalf("Allocate: %v", err)
			}
			if len(parts) != len(tt.want) {
				t.Fatalf("got %d parts, want %d", len(parts), len(tt.want))
			}
			var total int64
			for i, p := range parts {
				if p.Minor() != tt.want[i] {
					t.Errorf("part[%d] = %d, want %d", i, p.Minor(), tt.want[i])
				}
				total += p.Minor()
			}
			// The invariant that actually matters: nothing created, nothing lost.
			if total != tt.minor {
				t.Fatalf("parts sum to %d, want %d — allocation lost or created value", total, tt.minor)
			}
		})
	}
}

func TestAllocateRejectsInvalidInput(t *testing.T) {
	a := money.MustNew(100, eur)
	if _, err := a.Allocate(nil); err == nil {
		t.Fatal("expected empty weights to be rejected")
	}
	if _, err := a.Allocate([]int64{1, -1}); !errors.Is(err, money.ErrNegativeWeight) {
		t.Fatal("expected negative weight to be rejected")
	}
	if _, err := a.Allocate([]int64{0, 0}); err == nil {
		t.Fatal("expected non-zero amount over zero total weight to be rejected")
	}
	zeros, err := money.Zero(eur).Allocate([]int64{0, 0})
	if err != nil {
		t.Fatalf("zero amount over zero weight should succeed: %v", err)
	}
	for i, z := range zeros {
		if !z.IsZero() {
			t.Errorf("zeros[%d] = %s, want zero", i, z)
		}
	}
}

func TestSplitEvenly(t *testing.T) {
	parts, err := money.MustNew(1000, eur).SplitEvenly(3)
	if err != nil {
		t.Fatalf("SplitEvenly: %v", err)
	}
	want := []int64{334, 333, 333}
	var total int64
	for i, p := range parts {
		if p.Minor() != want[i] {
			t.Errorf("part[%d] = %d, want %d", i, p.Minor(), want[i])
		}
		total += p.Minor()
	}
	if total != 1000 {
		t.Fatalf("sum = %d, want 1000", total)
	}
	if _, err := money.MustNew(1, eur).SplitEvenly(0); err == nil {
		t.Fatal("expected SplitEvenly(0) to be rejected")
	}
}

func TestDivInt(t *testing.T) {
	a := money.MustNew(1000, eur)
	got, err := a.DivInt(3, money.RoundingDown)
	if err != nil {
		t.Fatalf("DivInt: %v", err)
	}
	if got.Minor() != 333 {
		t.Fatalf("1000/3 down = %d, want 333", got.Minor())
	}
	if _, err := a.DivInt(0, money.RoundingDown); !errors.Is(err, money.ErrInvalidRatio) {
		t.Fatalf("DivInt(0): got %v, want ErrInvalidRatio", err)
	}
}

func TestSumAndHelpers(t *testing.T) {
	total, err := money.Sum(
		money.MustNew(100, eur),
		money.MustNew(-40, eur),
		money.MustNew(5, eur),
	)
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	if total.Minor() != 65 {
		t.Fatalf("Sum = %s, want 0.65", total)
	}
	if _, err := money.Sum(); err == nil {
		t.Fatal("expected Sum() with no arguments to be rejected")
	}
	if _, err := money.Sum(money.MustNew(1, eur), money.MustNew(1, usd)); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Fatal("expected mixed-currency Sum to be rejected")
	}

	neg := money.MustNew(-500, eur)
	abs, err := neg.Abs()
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	if abs.Minor() != 500 {
		t.Fatalf("Abs = %s, want 5.00", abs)
	}
	if !neg.IsNegative() || neg.IsPositive() || neg.IsZero() {
		t.Fatal("sign predicates disagree for a negative amount")
	}
	if got := neg.String(); got != "-5.00 EUR" {
		t.Fatalf("String() = %q, want %q", got, "-5.00 EUR")
	}
	if _, err := money.New(1, money.Currency{}); !errors.Is(err, money.ErrInvalidCurrency) {
		t.Fatal("expected New with invalid currency to fail")
	}
}
