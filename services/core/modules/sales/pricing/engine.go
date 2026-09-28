package pricing

import (
	"fmt"
	"sort"
)

// Input limits. They exist so every multiplication in this package fits in
// 64 bits (see mulDiv) and so a typo like qty = 10000000 is caught at the till
// instead of on the GST return. MaxRateBP also keeps inclusive back-calculation
// safe: at absurd rates (100%) a one-paisa line would yield a negative goods
// value.
const (
	MaxLines          = 500
	MaxModifiers      = 50
	MaxQty            = 100_000
	MaxUnitPricePaise = 1_000_000_000       // ₹1 crore per unit
	MaxRateBP         = 4_000               // 40%, the top GST rate
	MaxSubtotalPaise  = 100_000_000_000_000 // ₹1,000 crore per bill
	bp                = 10_000              // basis points in 100%
)

// Compute prices a bill. It is a pure function: no I/O, no clock, no globals.
//
// The order of operations is fixed, because changing it changes paise:
//
//  1. gross    = qty * (unit price + modifiers), per line
//  2. line discount, per line (percent is rounded half up)
//  3. bill discount, split across lines by the largest-remainder method
//  4. group lines by GST rate
//  5. per group: back out GST if prices are inclusive, add service charge,
//     compute CGST+SGST (each half rounded separately, so they are always
//     equal) or IGST
//  6. round the grand total to the rupee if asked
func Compute(in Input) (Result, error) {
	if err := validate(in); err != nil {
		return Result{}, err
	}

	n := len(in.Lines)
	lines := make([]LineResult, n)
	afterLine := make([]int64, n)
	var subtotal int64

	// Steps 1 and 2: gross and line discounts.
	for i, l := range in.Lines {
		unit := l.UnitPricePaise
		for _, m := range l.Modifiers {
			unit += m.PricePaise
		}
		gross := l.Qty * unit
		ld, err := discountOn(gross, l.Discount, fmt.Sprintf("line %d", i))
		if err != nil {
			return Result{}, err
		}
		lines[i] = LineResult{Ref: l.Ref, Gross: gross, LineDiscount: ld}
		afterLine[i] = gross - ld
		subtotal += gross
	}
	if subtotal > MaxSubtotalPaise {
		return Result{}, fail("amount_too_large", "bill subtotal is above the supported maximum")
	}

	// Step 3: bill discount, shared in proportion to what each line is worth
	// after its own discount.
	var base int64
	for _, a := range afterLine {
		base += a
	}
	billDisc, err := discountOn(base, in.BillDiscount, "bill")
	if err != nil {
		return Result{}, err
	}
	shares := allocate(billDisc, afterLine)
	var discountTotal int64
	for i := range lines {
		lines[i].BillDiscount = shares[i]
		lines[i].Net = afterLine[i] - shares[i]
		discountTotal += lines[i].LineDiscount + shares[i]
	}

	// Step 4: group by rate, lowest rate first.
	byRate := map[int64]int64{}
	for i, l := range in.Lines {
		byRate[l.TaxRateBP] += lines[i].Net
	}
	rates := make([]int64, 0, len(byRate))
	for r := range byRate {
		rates = append(rates, r)
	}
	sort.Slice(rates, func(a, b int) bool { return rates[a] < rates[b] })

	// Step 5: tax per group.
	res := Result{Lines: lines, Subtotal: subtotal, DiscountTotal: discountTotal}
	for _, rate := range rates {
		g := taxGroup(rate, byRate[rate], in.PriceMode, in.Supply, in.ServiceChargeBP)
		res.TaxGroups = append(res.TaxGroups, g)
		res.ServiceCharge += g.ServiceCharge
		res.TaxableTotal += g.Taxable
		res.CGST += g.CGST
		res.SGST += g.SGST
		res.IGST += g.IGST
	}
	res.TaxTotal = res.CGST + res.SGST + res.IGST

	// Step 6: round off.
	total := res.TaxableTotal + res.TaxTotal
	if in.Rounding == NearestRupee {
		rem := total % 100
		if rem >= 50 {
			res.RoundOff = 100 - rem
		} else {
			res.RoundOff = -rem
		}
	}
	res.GrandTotal = total + res.RoundOff
	return res, nil
}

// taxGroup computes one rate group.
//
// Exclusive prices: the group's net is the goods value; service charge is
// added; GST is computed forward on (goods + service charge).
//
// Inclusive prices: GST is backed out of the net so that goods value + GST
// equals the net exactly (the guest pays the menu price, to the paisa).
// Service charge, if any, is then added on the goods value and taxed forward.
func taxGroup(rate, net int64, mode PriceMode, supply Supply, scBP int64) TaxGroup {
	g := TaxGroup{RateBP: rate, Net: net}

	var goodsCGST, goodsSGST, goodsIGST int64
	if mode == Inclusive {
		// Pre-tax estimate, then GST on that estimate, then goods value is
		// whatever is left. The last step is what makes the total exact.
		base0 := mulDivRound(net, bp, bp+rate)
		if supply == Intra {
			half := mulDivRound(base0, rate, 2*bp)
			goodsCGST, goodsSGST = half, half
		} else {
			goodsIGST = mulDivRound(base0, rate, bp)
		}
		g.GoodsValue = net - goodsCGST - goodsSGST - goodsIGST
	} else {
		g.GoodsValue = net
	}

	g.ServiceCharge = mulDivRound(g.GoodsValue, scBP, bp)
	g.Taxable = g.GoodsValue + g.ServiceCharge

	if mode == Inclusive {
		// Goods tax is already inside the price; only service charge is new.
		if supply == Intra {
			scHalf := mulDivRound(g.ServiceCharge, rate, 2*bp)
			g.CGST = goodsCGST + scHalf
			g.SGST = goodsSGST + scHalf
		} else {
			g.IGST = goodsIGST + mulDivRound(g.ServiceCharge, rate, bp)
		}
		return g
	}

	if supply == Intra {
		half := mulDivRound(g.Taxable, rate, 2*bp)
		g.CGST, g.SGST = half, half
	} else {
		g.IGST = mulDivRound(g.Taxable, rate, bp)
	}
	return g
}

// discountOn returns the discount in paise that d takes off amount.
func discountOn(amount int64, d *Discount, where string) (int64, error) {
	if d == nil {
		return 0, nil
	}
	switch {
	case d.PercentBP != 0 && d.AmountPaise != 0:
		return 0, fail("bad_discount", where+": set percent or amount, not both")
	case d.PercentBP < 0 || d.PercentBP > bp:
		return 0, fail("bad_discount", where+": percent must be between 0 and 100%")
	case d.AmountPaise < 0:
		return 0, fail("bad_discount", where+": amount cannot be negative")
	case d.AmountPaise > amount:
		return 0, fail("discount_exceeds_amount", where+": discount is larger than the amount")
	case d.PercentBP != 0:
		return mulDivRound(amount, d.PercentBP, bp), nil
	default:
		return d.AmountPaise, nil
	}
}

func validate(in Input) error {
	switch in.PriceMode {
	case Exclusive, Inclusive:
	default:
		return fail("bad_price_mode", fmt.Sprintf("price_mode %q is not exclusive or inclusive", in.PriceMode))
	}
	switch in.Supply {
	case Intra, Inter:
	default:
		return fail("bad_supply", fmt.Sprintf("supply %q is not intra or inter", in.Supply))
	}
	switch in.Rounding {
	case NearestRupee, None:
	default:
		return fail("bad_rounding", fmt.Sprintf("rounding %q is not nearest_rupee or none", in.Rounding))
	}
	if in.ServiceChargeBP < 0 || in.ServiceChargeBP > bp {
		return fail("bad_service_charge", "service charge must be between 0 and 100%")
	}
	if len(in.Lines) == 0 {
		return fail("no_lines", "a bill needs at least one line")
	}
	if len(in.Lines) > MaxLines {
		return fail("too_many_lines", fmt.Sprintf("a bill can have at most %d lines", MaxLines))
	}
	for i, l := range in.Lines {
		if l.Qty < 1 || l.Qty > MaxQty {
			return fail("bad_qty", fmt.Sprintf("line %d: qty must be between 1 and %d", i, MaxQty))
		}
		if l.UnitPricePaise < 0 || l.UnitPricePaise > MaxUnitPricePaise {
			return fail("bad_price", fmt.Sprintf("line %d: unit price out of range", i))
		}
		if l.TaxRateBP < 0 || l.TaxRateBP > MaxRateBP {
			return fail("bad_rate", fmt.Sprintf("line %d: tax rate out of range", i))
		}
		if len(l.Modifiers) > MaxModifiers {
			return fail("bad_price", fmt.Sprintf("line %d: at most %d modifiers", i, MaxModifiers))
		}
		for _, m := range l.Modifiers {
			if m.PricePaise < 0 || m.PricePaise > MaxUnitPricePaise {
				return fail("bad_price", fmt.Sprintf("line %d: modifier price out of range", i))
			}
		}
	}
	return nil
}
