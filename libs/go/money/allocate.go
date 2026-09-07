package money

import (
	"fmt"
	"math/big"
	"sort"
)

// Allocate distributes the amount across len(weights) buckets in proportion to
// the integer weights, guaranteeing that the parts sum back to exactly the
// original amount. No minor unit is created and none is lost.
//
// This is the primitive underneath every waterfall in the platform: multi-wallet
// stake distribution, pot splits between winners, rake shares, pari-mutuel
// dividends, and multi-level commission splits.
//
// The remainder is distributed one minor unit at a time to the buckets with the
// largest fractional entitlement, breaking ties by the lowest index. That makes
// the result deterministic and therefore reproducible during a dispute or an
// audit replay — which matters more than any notion of fairness between
// equally-entitled buckets, and is why the tie-break is defined rather than
// left to map ordering.
func (a Amount) Allocate(weights []int64) ([]Amount, error) {
	if len(weights) == 0 {
		return nil, fmt.Errorf("money: Allocate requires at least one weight")
	}

	total := new(big.Int)
	for i, w := range weights {
		if w < 0 {
			return nil, fmt.Errorf("%w: weight[%d] = %d", ErrNegativeWeight, i, w)
		}
		total.Add(total, big.NewInt(w))
	}

	out := make([]Amount, len(weights))
	if total.Sign() == 0 {
		// With no weight anywhere there is no defensible distribution. Zero can
		// be split into zeros; anything else must be an error rather than a
		// quietly dropped amount.
		if a.minor != 0 {
			return nil, fmt.Errorf("money: cannot allocate %s across zero total weight", a)
		}
		for i := range out {
			out[i] = Zero(a.currency)
		}
		return out, nil
	}

	negative := a.minor < 0
	magnitude := new(big.Int).Abs(big.NewInt(a.minor))

	type share struct {
		index     int
		base      *big.Int
		remainder *big.Int
	}
	shares := make([]share, len(weights))
	allocated := new(big.Int)

	for i, w := range weights {
		product := new(big.Int).Mul(magnitude, big.NewInt(w))
		base, remainder := new(big.Int).QuoRem(product, total, new(big.Int))
		shares[i] = share{index: i, base: base, remainder: remainder}
		allocated.Add(allocated, base)
	}

	leftover := new(big.Int).Sub(magnitude, allocated)

	// Order by descending remainder, then ascending index.
	order := make([]int, len(shares))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(x, y int) bool {
		cmp := shares[order[x]].remainder.Cmp(shares[order[y]].remainder)
		if cmp != 0 {
			return cmp > 0
		}
		return shares[order[x]].index < shares[order[y]].index
	})

	one := big.NewInt(1)
	for _, idx := range order {
		if leftover.Sign() <= 0 {
			break
		}
		shares[idx].base.Add(shares[idx].base, one)
		leftover.Sub(leftover, one)
	}

	for i, s := range shares {
		if !s.base.IsInt64() {
			return nil, fmt.Errorf("%w: allocation share %d", ErrOverflow, i)
		}
		minor := s.base.Int64()
		if negative {
			minor = -minor
		}
		out[i] = Amount{minor: minor, currency: a.currency}
	}
	return out, nil
}

// SplitEvenly divides the amount into n parts that sum to the original, with any
// remainder going to the earliest parts.
func (a Amount) SplitEvenly(n int) ([]Amount, error) {
	if n <= 0 {
		return nil, fmt.Errorf("money: SplitEvenly requires n > 0, got %d", n)
	}
	weights := make([]int64, n)
	for i := range weights {
		weights[i] = 1
	}
	return a.Allocate(weights)
}
