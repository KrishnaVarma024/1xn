// Package pricing turns an order into a GST-correct bill.
//
// Every amount is an int64 count of paise (₹1 = 100 paise). There are no
// floats anywhere in this package: in floating point 0.1 + 0.2 != 0.3, and a
// tax invoice has to be exact to the paisa.
//
// The same algorithm runs in three places: here (the server), in Dart on the
// POS device, and in tools/golden/oracle.py (an independent reference). All
// three must agree on every case in packages/golden/tax. The rules are written
// down in docs/learn/01-money-and-gst-engine.md; change them there first.
package pricing

// PriceMode says whether menu prices already include GST.
type PriceMode string

const (
	// Exclusive: the menu says ₹100 and GST is added on top ("+ taxes").
	// Usual for dine-in restaurants.
	Exclusive PriceMode = "exclusive"
	// Inclusive: the menu says ₹99 and the guest pays exactly ₹99; GST is
	// backed out of the price. Usual for QSRs and cafes.
	Inclusive PriceMode = "inclusive"
)

// Supply says whether the sale is inside the outlet's state (CGST + SGST) or
// across states (IGST). A restaurant meal is almost always intra-state.
type Supply string

const (
	Intra Supply = "intra"
	Inter Supply = "inter"
)

// Rounding says how the final bill total is rounded.
type Rounding string

const (
	// NearestRupee rounds the grand total to whole rupees; 50 paise and
	// above round up. The difference is shown as a "round off" line.
	NearestRupee Rounding = "nearest_rupee"
	// None keeps paise on the bill.
	None Rounding = "none"
)

// Discount is either a percentage in basis points (1000 = 10%) or a fixed
// amount in paise. Setting both is an error; setting neither means no
// discount.
type Discount struct {
	PercentBP   int64 `json:"percent_bp,omitempty"`
	AmountPaise int64 `json:"amount_paise,omitempty"`
}

// Modifier is an add-on priced on top of its line's unit price, taxed at the
// line's rate ("extra cheese +₹40").
type Modifier struct {
	Name       string `json:"name"`
	PricePaise int64  `json:"price_paise"`
}

// Line is one row of the order.
type Line struct {
	Ref            string     `json:"ref"` // item id or line id; echoed back
	Name           string     `json:"name"`
	Qty            int64      `json:"qty"`
	UnitPricePaise int64      `json:"unit_price_paise"`
	Modifiers      []Modifier `json:"modifiers,omitempty"`
	TaxRateBP      int64      `json:"tax_rate_bp"` // 500 = 5%
	Discount       *Discount  `json:"discount,omitempty"`
}

// Input is everything the engine needs. It is deliberately free of database
// ids and clocks, so the same input always gives the same bill.
type Input struct {
	PriceMode       PriceMode `json:"price_mode"`
	Supply          Supply    `json:"supply"`
	Rounding        Rounding  `json:"rounding"`
	Lines           []Line    `json:"lines"`
	BillDiscount    *Discount `json:"bill_discount,omitempty"`
	ServiceChargeBP int64     `json:"service_charge_bp,omitempty"` // off unless the guest agrees
}

// LineResult shows how each line's money moved.
type LineResult struct {
	Ref          string `json:"ref"`
	Gross        int64  `json:"gross"`         // qty * (unit price + modifiers)
	LineDiscount int64  `json:"line_discount"` // discount on this line alone
	BillDiscount int64  `json:"bill_discount"` // this line's share of the bill discount
	Net          int64  `json:"net"`           // gross - both discounts, in the bill's price mode
}

// TaxGroup totals every line that shares one GST rate. Tax is computed once
// per group, not per line, which keeps rounding error to at most half a paisa
// per tax head per rate.
type TaxGroup struct {
	RateBP        int64 `json:"rate_bp"`
	Net           int64 `json:"net"`            // sum of line nets at this rate
	GoodsValue    int64 `json:"goods_value"`    // net without GST
	ServiceCharge int64 `json:"service_charge"` // service charge on this group's goods
	Taxable       int64 `json:"taxable"`        // goods_value + service_charge
	CGST          int64 `json:"cgst"`
	SGST          int64 `json:"sgst"`
	IGST          int64 `json:"igst"`
}

// Result is the computed bill. Every figure printed on the invoice comes from
// here.
type Result struct {
	Lines         []LineResult `json:"lines"`
	Subtotal      int64        `json:"subtotal"`       // sum of gross
	DiscountTotal int64        `json:"discount_total"` // line + bill discounts
	ServiceCharge int64        `json:"service_charge"`
	TaxableTotal  int64        `json:"taxable_total"`
	TaxGroups     []TaxGroup   `json:"tax_groups"` // ordered by rate, lowest first
	CGST          int64        `json:"cgst"`
	SGST          int64        `json:"sgst"`
	IGST          int64        `json:"igst"`
	TaxTotal      int64        `json:"tax_total"`
	RoundOff      int64        `json:"round_off"` // between -49 and +50 paise
	GrandTotal    int64        `json:"grand_total"`
}

// Error is a validation failure. Code is stable and machine-readable; the
// device shows a translated message for it.
type Error struct {
	Code string `json:"code"`
	Msg  string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

func fail(code, msg string) error { return &Error{Code: code, Msg: msg} }
