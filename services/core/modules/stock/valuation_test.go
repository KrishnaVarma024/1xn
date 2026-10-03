package stock

import (
	"math/rand"
	"testing"
)

func TestAverageCostAndNoRoundingDust(t *testing.T) {
	var b Balance
	// 3 kg of paneer for Rs 1,000 (Rs 333.33/kg), then 2 kg for Rs 700.
	if up := b.In(3000, 100_000); up != 0 {
		t.Fatalf("true-up on a normal purchase: %d", up)
	}
	b.In(2000, 70_000)
	if b.Qty != 5000 || b.Value != 170_000 || b.UnitCost() != 34_000 || b.LastCost != 35_000 {
		t.Fatalf("after purchases: %+v, unit cost %d", b, b.UnitCost())
	}
	// Use it all, in awkward amounts: every paisa leaves, none is left over.
	var out int64
	for _, q := range []int64{333, 1001, 7, 2659, 1000} {
		out += b.Out(q)
	}
	if b.Qty != 0 || b.Value != 0 || out != 170_000 {
		t.Fatalf("after using everything: %+v, out %d", b, out)
	}
}

func TestSellingMoreThanBookedInIsTruedUpOnPurchase(t *testing.T) {
	b := Balance{LastCost: 4000} // flour was Rs 40/kg
	if v := b.Out(500); v != 2000 {
		t.Fatalf("500 g with none on hand leaves at the last price: %d", v)
	}
	// 2 kg arrives at Rs 50/kg: the 500 g sold really cost Rs 25.
	if up := b.In(2000, 10_000); up != 500 {
		t.Fatalf("true-up %d, want 500", up)
	}
	if b.Qty != 1500 || b.Value != 7500 || b.LastCost != 5000 {
		t.Fatalf("after the purchase: %+v", b)
	}
}

func TestPurchaseCoveringPartOfTheDeficit(t *testing.T) {
	b := Balance{LastCost: 4000}
	b.Out(3000) // Rs 120 at the estimate
	up := b.In(1000, 5000)
	// 1 kg of the 3 kg deficit is now real at Rs 50: true-up Rs 10. The other
	// 2 kg keep their estimate: Rs 80.
	if up != 1000 || b.Qty != -2000 || b.Value != -8000 {
		t.Fatalf("true-up %d, balance %+v", up, b)
	}
}

func TestFoundStockAtUnitCost(t *testing.T) {
	var b Balance
	b.In(1000, 30_000) // Rs 300/kg
	if v := b.Found(250); v != 7500 || b.Qty != 1250 || b.Value != 37_500 {
		t.Fatalf("found 250 g: %d, %+v", v, b)
	}
}

// The ledger promise: whatever happens, the value that went in minus the
// value that came out is exactly what the balance says.
func TestValueAlwaysReconciles(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	// Round quantities half the time, so balances often land exactly on zero.
	qty := func(max int64) int64 {
		if r.Intn(2) == 0 {
			return 250 * (1 + r.Int63n(8))
		}
		return 1 + r.Int63n(max)
	}
	for run := 0; run < 5000; run++ {
		var b Balance
		var in, out int64
		for step := 0; step < 30; step++ {
			switch r.Intn(3) {
			case 0:
				cost := r.Int63n(5_000_000)
				in += cost
				out += b.In(qty(50_000), cost) // the true-up leaves stock for food cost
			case 1:
				out += b.Out(qty(40_000))
			case 2:
				in += b.Found(qty(5_000))
			}
			if in-out != b.Value {
				t.Fatalf("run %d step %d: in %d - out %d != value %d (%+v)", run, step, in, out, b.Value, b)
			}
			if b.Qty == 0 && b.Value != 0 {
				t.Fatalf("run %d step %d: nothing on hand but worth %d", run, step, b.Value)
			}
		}
	}
}

func TestMulDivRoundsHalfAwayFromZero(t *testing.T) {
	for _, c := range []struct{ a, b, c, want int64 }{
		{5, 1, 2, 3}, {-5, 1, 2, -3}, {4, 1, 3, 1}, {5, 1, 3, 2}, {1 << 62, 4, 8, 1 << 61}, {7, 0, 3, 0}, {7, 3, 0, 0},
	} {
		if got := mulDiv(c.a, c.b, c.c); got != c.want {
			t.Errorf("mulDiv(%d, %d, %d) = %d, want %d", c.a, c.b, c.c, got, c.want)
		}
	}
}

func TestOverflowIsReportedNotWrapped(t *testing.T) {
	b := Balance{LastCost: 1_000_000_000_000_000} // Rs 10^13 a unit: nonsense, but representable
	v := b.Out(1_000_000_000_000)
	if !b.Overflowed() || v < 0 {
		t.Fatalf("10^12 milli-units at 10^15 paise each: overflowed %v, value %d", b.Overflowed(), v)
	}
	var ok Balance
	ok.In(1000, 5000)
	ok.Out(500)
	if ok.Overflowed() {
		t.Fatal("ordinary figures must not report overflow")
	}
}
