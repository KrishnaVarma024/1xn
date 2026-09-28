package pricing

import (
	"math/rand"
	"testing"
)

// checkInvariants states what must be true of EVERY bill, whatever the input.
// Golden cases test specific answers; these test the shape of all answers.
func checkInvariants(t *testing.T, in Input, r Result) {
	t.Helper()
	var sumNet, sumDisc, sumGroupsNet, sumTaxable, sumSC int64
	for _, l := range r.Lines {
		if l.Gross < 0 || l.LineDiscount < 0 || l.BillDiscount < 0 || l.Net < 0 {
			t.Fatalf("negative line amount: %+v", l)
		}
		if l.Gross-l.LineDiscount-l.BillDiscount != l.Net {
			t.Fatalf("line does not add up: %+v", l)
		}
		sumNet += l.Net
		sumDisc += l.LineDiscount + l.BillDiscount
	}
	if r.Subtotal-r.DiscountTotal != sumNet || r.DiscountTotal != sumDisc {
		t.Fatalf("subtotal - discounts != sum of nets")
	}
	for i, g := range r.TaxGroups {
		if i > 0 && r.TaxGroups[i-1].RateBP >= g.RateBP {
			t.Fatalf("tax groups not strictly ordered by rate")
		}
		if g.GoodsValue+g.ServiceCharge != g.Taxable {
			t.Fatalf("group taxable != goods + service charge: %+v", g)
		}
		if in.Supply == Intra && (g.IGST != 0 || g.CGST != g.SGST) {
			t.Fatalf("intra-state group must have CGST == SGST and no IGST: %+v", g)
		}
		if in.Supply == Inter && (g.CGST != 0 || g.SGST != 0) {
			t.Fatalf("inter-state group must have only IGST: %+v", g)
		}
		if in.PriceMode == Inclusive && in.ServiceChargeBP == 0 &&
			g.GoodsValue+g.CGST+g.SGST+g.IGST != g.Net {
			t.Fatalf("inclusive group must keep the menu total exactly: %+v", g)
		}
		sumGroupsNet += g.Net
		sumTaxable += g.Taxable
		sumSC += g.ServiceCharge
	}
	if sumGroupsNet != sumNet || sumTaxable != r.TaxableTotal || sumSC != r.ServiceCharge {
		t.Fatalf("group totals do not match bill totals")
	}
	if r.TaxTotal != r.CGST+r.SGST+r.IGST {
		t.Fatalf("tax total != sum of heads")
	}
	if r.GrandTotal != r.TaxableTotal+r.TaxTotal+r.RoundOff {
		t.Fatalf("grand total does not add up")
	}
	if in.Rounding == NearestRupee && (r.GrandTotal%100 != 0 || r.RoundOff < -49 || r.RoundOff > 50) {
		t.Fatalf("round off out of range: %d, total %d", r.RoundOff, r.GrandTotal)
	}
	if in.Rounding == None && r.RoundOff != 0 {
		t.Fatalf("round off must be zero when rounding is none")
	}
}

func randomInput(rng *rand.Rand) Input {
	rates := []int64{0, 500, 500, 500, 1200, 1800, 2800, 4000}
	prices := []int64{0, 1, 99, 1000, 4999, 9900, 12500, 19900, 29999, 45000, 120000}
	in := Input{PriceMode: Exclusive, Supply: Intra, Rounding: NearestRupee}
	if rng.Intn(2) == 0 {
		in.PriceMode = Inclusive
	}
	if rng.Intn(5) == 0 {
		in.Supply = Inter
	}
	if rng.Intn(3) == 0 {
		in.Rounding = None
	}
	for i := 0; i < 1+rng.Intn(15); i++ {
		l := Line{
			Ref:            "L",
			Qty:            1 + rng.Int63n(30),
			UnitPricePaise: prices[rng.Intn(len(prices))] + rng.Int63n(1000),
			TaxRateBP:      rates[rng.Intn(len(rates))],
		}
		if rng.Intn(4) == 0 {
			l.Modifiers = []Modifier{{Name: "m", PricePaise: rng.Int63n(5000)}}
		}
		if rng.Intn(6) == 0 {
			l.Discount = &Discount{PercentBP: rng.Int63n(10001)}
		}
		in.Lines = append(in.Lines, l)
	}
	switch rng.Intn(5) {
	case 0:
		in.BillDiscount = &Discount{PercentBP: rng.Int63n(10001)}
	case 1:
		in.BillDiscount = &Discount{AmountPaise: rng.Int63n(500)}
	}
	if rng.Intn(4) == 0 {
		in.ServiceChargeBP = []int64{500, 750, 1000}[rng.Intn(3)]
	}
	return in
}

// TestInvariantsRandom checks 20,000 random bills. The seed is fixed so a
// failure is reproducible; change it locally to explore.
func TestInvariantsRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(20261005))
	for i := 0; i < 20_000; i++ {
		in := randomInput(rng)
		r, err := Compute(in)
		if err != nil {
			var pe *Error
			if e, ok := err.(*Error); ok {
				pe = e
			}
			// A fixed discount larger than the bill is a legitimate rejection.
			if pe != nil && pe.Code == "discount_exceeds_amount" {
				continue
			}
			t.Fatalf("case %d: unexpected error %v for %+v", i, err, in)
		}
		checkInvariants(t, in, r)
	}
}

// TestAllocateIsExact checks the largest-remainder split directly.
func TestAllocateIsExact(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 50_000; i++ {
		n := 1 + rng.Intn(20)
		w := make([]int64, n)
		var sum int64
		for j := range w {
			w[j] = rng.Int63n(1_000_000)
			sum += w[j]
		}
		total := int64(0)
		if sum > 0 {
			total = rng.Int63n(sum + 1)
		}
		out := allocate(total, w)
		var got int64
		for j, v := range out {
			if v < 0 || v > w[j] {
				t.Fatalf("share %d out of range: %d of weight %d", j, v, w[j])
			}
			got += v
		}
		if got != total {
			t.Fatalf("shares sum to %d, want %d", got, total)
		}
	}
}

// TestHugeValuesDoNotOverflow pushes the limits in limits.go.
func TestHugeValuesDoNotOverflow(t *testing.T) {
	in := Input{PriceMode: Inclusive, Supply: Intra, Rounding: NearestRupee,
		BillDiscount: &Discount{PercentBP: 3333}, ServiceChargeBP: 1000}
	for i := 0; i < 100; i++ {
		in.Lines = append(in.Lines, Line{Qty: 1000, UnitPricePaise: MaxUnitPricePaise, TaxRateBP: 4000})
	}
	r, err := Compute(in)
	if err != nil {
		t.Fatal(err)
	}
	checkInvariants(t, in, r)

	in.Lines[0].Qty = MaxQty // pushes the subtotal past the cap
	if _, err := Compute(in); err == nil {
		t.Fatal("expected amount_too_large")
	}
}

// FuzzCompute lets `go test -fuzz=FuzzCompute` hunt for inputs that break an
// invariant or panic.
func FuzzCompute(f *testing.F) {
	f.Add(int64(10000), int64(1), int64(500), int64(0), int64(0), true, true)
	f.Add(int64(9900), int64(3), int64(1800), int64(1000), int64(1000), false, false)
	f.Fuzz(func(t *testing.T, price, qty, rate, discBP, scBP int64, inclusive, intra bool) {
		in := Input{PriceMode: Exclusive, Supply: Inter, Rounding: NearestRupee,
			ServiceChargeBP: scBP,
			BillDiscount:    &Discount{PercentBP: discBP},
			Lines: []Line{
				{Qty: qty, UnitPricePaise: price, TaxRateBP: rate},
				{Qty: 1, UnitPricePaise: price / 3, TaxRateBP: 500},
			}}
		if inclusive {
			in.PriceMode = Inclusive
		}
		if intra {
			in.Supply = Intra
		}
		r, err := Compute(in)
		if err != nil {
			return // invalid inputs must be rejected, never panic
		}
		checkInvariants(t, in, r)
	})
}

// BenchmarkCompute prices a typical 8-line dine-in bill with a discount.
// Run: go test ./modules/sales/pricing -bench . -benchmem
func BenchmarkCompute(b *testing.B) {
	in := Input{PriceMode: Exclusive, Supply: Intra, Rounding: NearestRupee,
		BillDiscount: &Discount{PercentBP: 1000}}
	for i := 0; i < 8; i++ {
		in.Lines = append(in.Lines, Line{Ref: "L", Qty: int64(1 + i%3), UnitPricePaise: 18000 + int64(i)*1250, TaxRateBP: 500})
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Compute(in); err != nil {
			b.Fatal(err)
		}
	}
}
