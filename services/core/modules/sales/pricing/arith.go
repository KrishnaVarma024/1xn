package pricing

import (
	"math/bits"
	"sort"
)

// mulDiv returns floor(a*b/c) and the remainder (a*b) mod c, computed with a
// 128-bit intermediate so a*b can never overflow. a and b must be >= 0 and
// c > 0. The quotient must fit in 63 bits; every caller in this package
// guarantees that through the input limits in limits.go.
func mulDiv(a, b, c int64) (q int64, r uint64) {
	if a < 0 || b < 0 || c <= 0 {
		panic("pricing: mulDiv needs a, b >= 0 and c > 0")
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	if hi >= uint64(c) {
		panic("pricing: mulDiv quotient overflows 64 bits")
	}
	quo, rem := bits.Div64(hi, lo, uint64(c))
	if quo > 1<<63-1 {
		panic("pricing: mulDiv quotient overflows int64")
	}
	return int64(quo), rem
}

// mulDivRound returns a*b/c rounded half up: 0.5 paise and above goes up.
// All inputs are non-negative, so "half up" and "half away from zero" agree.
func mulDivRound(a, b, c int64) int64 {
	q, r := mulDiv(a, b, c)
	// r >= c/2, written without division so odd c rounds correctly.
	if 2*r >= uint64(c) {
		q++
	}
	return q
}

// allocate splits total across weights in proportion to each weight, so the
// parts always add up to total exactly. It uses the largest-remainder method
// (Hamilton's method, the one used to share parliament seats):
//
//  1. every weight gets floor(total * w / sum(w));
//  2. the paise left over go, one each, to the weights with the largest
//     remainders; ties go to the lower index, so the result is deterministic
//     and identical on the device, the server and the oracle.
//
// It requires 0 <= total <= sum(weights) and every weight >= 0.
func allocate(total int64, weights []int64) []int64 {
	out := make([]int64, len(weights))
	var sum int64
	for _, w := range weights {
		sum += w
	}
	if total == 0 || sum == 0 {
		return out
	}

	type share struct {
		idx int
		rem uint64
	}
	shares := make([]share, len(weights))
	var given int64
	for i, w := range weights {
		q, r := mulDiv(total, w, sum)
		out[i] = q
		given += q
		shares[i] = share{idx: i, rem: r}
	}

	left := total - given // always 0 <= left < len(weights)
	sort.SliceStable(shares, func(a, b int) bool {
		if shares[a].rem != shares[b].rem {
			return shares[a].rem > shares[b].rem
		}
		return shares[a].idx < shares[b].idx
	})
	for k := int64(0); k < left; k++ {
		out[shares[k].idx]++
	}
	return out
}
