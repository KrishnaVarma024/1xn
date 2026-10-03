// Package stock keeps the ingredients a restaurant holds: what each outlet
// has, what it is worth, and every movement in and out. Dishes have recipes;
// every bill uses up its dishes' ingredients the moment it syncs, so food
// cost is known per day, not only after a month-end count.
//
// Quantities are integer milli-units (grams for kg items, millilitres for
// litre items, thousandths of a piece). Money is integer paise. Stock is
// valued at weighted average cost.
//
// Cross-module API: ConsumeForBill (sales calls it), the HTTP handlers.
package stock

import (
	"math"
	"math/big"
)

// Balance is what one outlet holds of one item and what it is worth.
//
// The rule that keeps the books right: every operation moves Value by exactly
// the amount it returns (or is given), and that same amount goes to the
// ledger. So the sum of all balances always equals account 1300 Stock.
type Balance struct {
	Qty      int64 // milli-units; negative when more was used than was booked in
	Value    int64 // paise
	LastCost int64 // paise per whole unit (per kg, litre or piece) at the last purchase

	overflow bool // a figure did not fit in 64 bits: the operation must not be kept
}

// Overflowed reports that an operation produced a figure too large for
// int64 (absurd quantities or prices). The caller must throw the result away.
func (b *Balance) Overflowed() bool { return b.overflow }

func (b *Balance) mul(x, y, z int64) int64 {
	v, ok := mulDivOK(x, y, z)
	if !ok {
		b.overflow = true
	}
	return v
}

func (b *Balance) add(x, y int64) int64 {
	if (y > 0 && x > math.MaxInt64-y) || (y < 0 && x < math.MinInt64-y) {
		b.overflow = true
		return x
	}
	return x + y
}

// UnitCost is the average cost of one whole unit, in paise. With nothing on
// hand (or less than nothing) there is no average, so it is the last
// purchase price.
func (b Balance) UnitCost() int64 {
	if b.Qty > 0 {
		return mulDiv(b.Value, 1000, b.Qty)
	}
	return b.LastCost
}

// Out takes q milli-units out and returns the value that leaves (what goes
// to food cost, wastage or count differences).
//
// Stock on hand leaves at its average cost: its share of Value. When the
// last unit leaves, Value is exactly zero, with no rounding dust left behind.
// Stock that is not there (the outlet sells more than it booked in, which
// happens: purchases are recorded late) leaves at the last purchase price.
func (b *Balance) Out(q int64) int64 {
	if q <= 0 {
		return 0
	}
	var v int64
	switch {
	case b.Qty >= q:
		v = b.mul(b.Value, q, b.Qty)
	case b.Qty > 0:
		v = b.add(b.Value, b.mul(b.LastCost, q-b.Qty, 1000))
	default:
		v = b.mul(b.LastCost, q, 1000)
	}
	b.Qty = b.add(b.Qty, -q)
	b.Value = b.add(b.Value, -v)
	return v
}

// In receives q milli-units that cost `cost` paise in all (a purchase), and
// returns the true-up: the extra cost of food sold for stock that had been
// used before it was booked in.
//
// Say 500 g of flour was sold with none booked in. It left at the last price,
// say Rs 40/kg: Rs 20. Now 2 kg arrives for Rs 100 (Rs 50/kg). Those 500 g
// really cost Rs 25, so food cost goes up by Rs 5 (the true-up), and the
// 1.5 kg left is worth exactly Rs 75 at the new price.
func (b *Balance) In(q, cost int64) (trueUp int64) {
	if q <= 0 {
		return 0
	}
	before := b.add(b.Value, cost) // what the books would say with no true-up
	switch {
	case b.Qty >= 0:
		b.Value = before
	case b.Qty+q >= 0:
		// The purchase covers the whole deficit: what is left is the rest of
		// the purchase, at its own price.
		b.Value = cost - b.mul(cost, -b.Qty, q)
	default:
		// The purchase covers part of the deficit: the rest of the deficit
		// keeps its estimate.
		b.Value = b.mul(b.Value, b.Qty+q, b.Qty)
	}
	b.Qty = b.add(b.Qty, q)
	b.LastCost = b.mul(cost, 1000, q)
	return before - b.Value
}

// Found adds q milli-units that a count found beyond the books, and returns
// their value. On hand, they are valued at the average cost. Against a
// deficit (stock used but never booked in), they cancel its estimate: the
// count shows the stock was there after all.
func (b *Balance) Found(q int64) int64 {
	if q <= 0 {
		return 0
	}
	var after int64
	switch {
	case b.Qty >= 0:
		after = b.add(b.Value, b.mul(b.UnitCost(), q, 1000))
	case b.Qty+q <= 0:
		after = b.mul(b.Value, b.Qty+q, b.Qty) // the rest of the deficit keeps its estimate
	default:
		after = b.mul(b.LastCost, b.Qty+q, 1000) // the deficit is gone; the rest at the last price
	}
	v := after - b.Value
	b.Qty = b.add(b.Qty, q)
	b.Value = after
	return v
}

// mulDiv is a*b/c rounded half away from zero, saturating at the int64
// limits: for figures that are only shown (a dish's cost), never booked.
func mulDiv(a, b, c int64) int64 {
	v, _ := mulDivOK(a, b, c)
	return v
}

// mulDivOK is a*b/c rounded half away from zero, computed without overflow
// (a stock value times a quantity can pass int64), and whether the result
// fits in int64. If it does not, the value is clamped and must not be used.
func mulDivOK(a, b, c int64) (int64, bool) {
	if c == 0 {
		return 0, true
	}
	n := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	d := big.NewInt(c)
	neg := (n.Sign() < 0) != (d.Sign() < 0)
	n.Abs(n)
	d.Abs(d)
	q, r := new(big.Int).QuoRem(n, d, new(big.Int))
	if r.Mul(r, big.NewInt(2)).Cmp(d) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if neg {
		q.Neg(q)
	}
	if !q.IsInt64() {
		if neg {
			return math.MinInt64, false
		}
		return math.MaxInt64, false
	}
	return q.Int64(), true
}
