// Package sales records what was sold: settled bills, their lines, taxes and
// payments. Bills are created on the device (offline-first) and arrive here
// through the sync gateway as "bill.settled" ops.
//
// Cross-module API: ApplySettledBill (a syncop.Applier), Routes.
package sales

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"

	"1xn/core/internal/platform/alerts"
	"1xn/core/internal/platform/gst"
	"1xn/core/internal/platform/ids"
	"1xn/core/internal/platform/outbox"
	"1xn/core/internal/platform/syncop"
	"1xn/core/modules/catalogue"
	"1xn/core/modules/dayclose"
	"1xn/core/modules/identity"
	"1xn/core/modules/ledger"
	"1xn/core/modules/orders"
	"1xn/core/modules/sales/pricing"
	"1xn/core/modules/stock"
)

// OpBillSettled is the op type devices send when a bill is paid and printed.
const OpBillSettled = "bill.settled"

// SettledBill is the payload of a bill.settled op.
type SettledBill struct {
	BillID       string          `json:"bill_id"`       // UUIDv7 made on the device
	InvoiceNo    string          `json:"invoice_no"`    // from the device's own series
	IssuedAt     time.Time       `json:"issued_at"`     // RFC 3339, when the bill was printed
	BusinessDate string          `json:"business_date"` // YYYY-MM-DD trading day
	Channel      string          `json:"channel"`
	TableLabel   string          `json:"table_label,omitempty"`
	Covers       int             `json:"covers,omitempty"`
	Input        json.RawMessage `json:"input"`  // what the device priced (pricing.Input)
	Totals       json.RawMessage `json:"totals"` // what the device printed (pricing.Result)
	Payments     []Payment       `json:"payments"`
	TipPaise     int64           `json:"tip_paise,omitempty"`
	// OrderID and LineIDs link the bill to a table order. LineIDs[i] is the
	// order line billed as Input.Lines[i]. A split bill lists only its lines.
	// Quick takeaway bills with no order leave both empty.
	OrderID *uuid.UUID  `json:"order_id,omitempty"`
	LineIDs []uuid.UUID `json:"line_ids,omitempty"`
}

// Payment is one tender.
type Payment struct {
	Method      string `json:"method"` // cash, upi, card, credit
	AmountPaise int64  `json:"amount_paise"`
	Reference   string `json:"reference,omitempty"` // UPI ref, card approval code
}

var (
	channels = map[string]bool{"dine_in": true, "takeaway": true, "delivery": true, "swiggy": true, "zomato": true}
	methods  = map[string]bool{"cash": true, "upi": true, "card": true, "credit": true}
	// gstStart is when GST began (1 July 2017); no GST invoice predates it.
	gstStart = time.Date(2017, 7, 1, 0, 0, 0, 0, gst.IST)
)

// maxClockAhead is how far in the future a device clock may run.
const maxClockAhead = 48 * time.Hour

func reject(code, msg string) error   { return syncop.Reject(code, "warning", msg) }
func critical(code, msg string) error { return syncop.Reject(code, "critical", msg) }

// ApplySettledBill stores a bill the device has already issued.
//
// The guiding rule: the printed invoice is a legal document the guest already
// holds, so we never silently drop or rewrite it. We recompute it with the
// same engine and, if our answer differs, we still store the device's version
// (it is what the restaurant owes tax on), keep ours next to it, and raise a
// critical alert. We only refuse a bill when storing it would corrupt data:
// a wrong invoice series, a reused invoice number, or totals that do not add
// up. Refused ops are kept in the sync inbox with their payload, so even then
// nothing is lost.
func ApplySettledBill(ctx context.Context, tx *sql.Tx, a syncop.ApplyContext) (syncop.Applied, error) {
	if a.Device.Role != "pos" {
		// Captain phones take orders and KDS screens show them; only a till
		// issues invoices from its own series.
		return syncop.Applied{}, critical("role_not_allowed", fmt.Sprintf("a %s device cannot issue bills", a.Device.Role))
	}
	var b SettledBill
	if err := json.Unmarshal(a.Payload, &b); err != nil {
		return syncop.Applied{}, reject("bad_payload", err.Error())
	}
	billID, err := ids.ParseV7(b.BillID)
	if err != nil {
		return syncop.Applied{}, reject("bad_bill_id", err.Error())
	}

	// The invoice number must come from THIS device's series and the right
	// financial year; otherwise two devices could issue the same number.
	prefix, fy, _, err := gst.ParseInvoiceNo(b.InvoiceNo)
	if err != nil {
		return syncop.Applied{}, reject("bad_invoice_no", err.Error())
	}
	if prefix != a.Device.InvoicePrefix {
		return syncop.Applied{}, critical("wrong_invoice_series",
			fmt.Sprintf("invoice %s uses series %s but this device owns %s", b.InvoiceNo, prefix, a.Device.InvoicePrefix))
	}
	if b.IssuedAt.IsZero() {
		return syncop.Applied{}, reject("bad_issued_at", "issued_at is required")
	}
	// A device clock that is years off would put the invoice in the wrong
	// financial year (and, far enough out, break date encoding). Bills are
	// issued in the past, never more than a clock's drift in the future.
	if b.IssuedAt.Before(gstStart) || b.IssuedAt.After(time.Now().Add(maxClockAhead)) {
		return syncop.Applied{}, reject("bad_issued_at",
			fmt.Sprintf("issued_at %s is before GST began or more than 48 hours ahead; check the device clock", b.IssuedAt.Format(time.RFC3339)))
	}
	issued := b.IssuedAt.In(gst.IST)
	if want := gst.FY(issued); fy != want {
		return syncop.Applied{}, critical("wrong_financial_year",
			fmt.Sprintf("invoice %s says FY %s but was issued in FY %s", b.InvoiceNo, fy, want))
	}
	bizDate, err := time.Parse(time.DateOnly, b.BusinessDate)
	if err != nil {
		return syncop.Applied{}, reject("bad_business_date", "business_date must be YYYY-MM-DD")
	}
	issuedDay := time.Date(issued.Year(), issued.Month(), issued.Day(), 0, 0, 0, 0, time.UTC)
	if d := issuedDay.Sub(bizDate); d < 0 || d > 24*time.Hour {
		// A night shift can run past midnight, so the business date may be the
		// issue date or the day before, never anything else.
		return syncop.Applied{}, reject("bad_business_date", "business_date must be the issue date or the day before")
	}
	if !channels[b.Channel] {
		return syncop.Applied{}, reject("bad_channel", fmt.Sprintf("unknown channel %q", b.Channel))
	}
	if b.TipPaise < 0 || b.TipPaise > maxAmount || b.Covers < 0 || b.Covers > 10_000 || len(b.TableLabel) > 50 {
		return syncop.Applied{}, reject("bad_payload", "tip_paise, covers or table_label out of range")
	}
	if len(b.Payments) > 20 {
		return syncop.Applied{}, reject("bad_payment", "at most 20 payments per bill")
	}
	for _, p := range b.Payments {
		if !methods[p.Method] || p.AmountPaise <= 0 || p.AmountPaise > maxAmount || len(p.Reference) > 100 {
			return syncop.Applied{}, reject("bad_payment", fmt.Sprintf("payment %q of %d is invalid", p.Method, p.AmountPaise))
		}
	}

	var in pricing.Input
	if err := json.Unmarshal(b.Input, &in); err != nil {
		return syncop.Applied{}, reject("bad_input", err.Error())
	}
	var printed pricing.Result
	if err := json.Unmarshal(b.Totals, &printed); err != nil {
		return syncop.Applied{}, reject("bad_totals", err.Error())
	}
	if err := consistent(in, printed); err != nil {
		return syncop.Applied{}, critical("inconsistent_totals", err.Error())
	}
	if b.OrderID == nil && len(b.LineIDs) > 0 {
		return syncop.Applied{}, reject("bad_payload", "line_ids need an order_id")
	}

	// Same bill id or same invoice number already stored?
	var existingID uuid.UUID
	var existingHash []byte
	var existingSeq int64
	err = tx.QueryRowContext(ctx, `
		SELECT id, payload_hash, server_seq FROM bill
		WHERE id = $1 OR (legal_entity_id = $2 AND invoice_no = $3)
		LIMIT 1`, billID, a.Device.LegalEntityID, b.InvoiceNo).Scan(&existingID, &existingHash, &existingSeq)
	switch {
	case err == nil && bytes.Equal(existingHash, a.PayloadHash):
		return syncop.Applied{ServerSeq: existingSeq, Duplicate: true}, nil
	case err == nil:
		return syncop.Applied{}, critical("invoice_conflict",
			fmt.Sprintf("bill %s / invoice %s already exists with different content", billID, b.InvoiceNo))
	case !errors.Is(err, sql.ErrNoRows):
		return syncop.Applied{}, err
	}

	// Recompute. A bill our engine cannot price at all means a device bug.
	ours, perr := pricing.Compute(in)
	if perr != nil {
		return syncop.Applied{}, critical("unpriceable", perr.Error())
	}
	verification := "match"
	var serverResult any
	if !reflect.DeepEqual(ours, printed) {
		verification = "mismatch"
		sr, _ := json.Marshal(ours)
		serverResult = string(sr) // lib/pq sends []byte as bytea; jsonb wants text
	}

	seq, err := a.NextSeq()
	if err != nil {
		return syncop.Applied{}, err
	}

	// Post to the ledger first: the bill row records the outcome.
	var paid int64
	var pays []ledger.Payment
	for _, p := range b.Payments {
		paid += p.AmountPaise
		pays = append(pays, ledger.Payment{Method: p.Method, Amount: p.AmountPaise})
	}
	entryID, diff, err := ledger.PostBillSettled(ctx, tx, ledger.BillSettled{
		BillID: billID, TenantID: a.Device.TenantID, LegalEntityID: a.Device.LegalEntityID,
		OutletID: a.Device.OutletID, Channel: b.Channel, InvoiceNo: b.InvoiceNo,
		IssuedAt: issued, BusinessDate: bizDate,
		GoodsValue: printed.TaxableTotal - printed.ServiceCharge, ServiceCharge: printed.ServiceCharge,
		CGST: printed.CGST, SGST: printed.SGST, IGST: printed.IGST,
		RoundOff: printed.RoundOff, GrandTotal: printed.GrandTotal, Tip: b.TipPaise, Payments: pays,
	})
	ledgerStatus := "posted"
	var journalID any = entryID
	if errors.Is(err, ledger.ErrNothingToPost) {
		// Fully comped bill: nothing was paid and nothing is owed. Still a
		// legal invoice (it uses a number from the series), so it is stored.
		ledgerStatus, journalID, err = "none", nil, nil
	}
	if errors.Is(err, ledger.ErrPeriodLocked) {
		// The accountant has closed that month. Keep the bill; flag it so the
		// accountant posts it into an open period on purpose.
		ledgerStatus, journalID = "blocked", nil
		diff = paid - printed.GrandTotal - b.TipPaise
		if err := alerts.Raise(ctx, tx, a.Device.TenantID, "period_locked", "critical", &billID,
			map[string]any{"invoice_no": b.InvoiceNo, "issued_at": issued}); err != nil {
			return syncop.Applied{}, err
		}
	} else if err != nil {
		return syncop.Applied{}, err
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO bill (id, tenant_id, legal_entity_id, outlet_id, device_id, invoice_no, fy, issued_at,
		  business_date, channel, table_label, covers, price_mode, supply,
		  subtotal, discount_total, service_charge, taxable_total, cgst, sgst, igst, tax_total, round_off,
		  grand_total, tip, input, device_result, verification, server_result, ledger_status,
		  journal_entry_id, payload_hash, server_seq, order_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, nullif($11, ''), $12, $13, $14,
		  $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30, $31, $32, $33, $34)`,
		billID, a.Device.TenantID, a.Device.LegalEntityID, a.Device.OutletID, a.Device.ID, b.InvoiceNo, fy, issued,
		b.BusinessDate, b.Channel, b.TableLabel, b.Covers, in.PriceMode, in.Supply,
		printed.Subtotal, printed.DiscountTotal, printed.ServiceCharge, printed.TaxableTotal,
		printed.CGST, printed.SGST, printed.IGST, printed.TaxTotal, printed.RoundOff, printed.GrandTotal,
		b.TipPaise, string(b.Input), string(b.Totals), verification, serverResult, ledgerStatus,
		journalID, a.PayloadHash, seq, b.OrderID); err != nil {
		return syncop.Applied{}, err
	}
	for i, l := range in.Lines {
		r := printed.Lines[i]
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO bill_line (bill_id, line_no, tenant_id, item_ref, name, qty, gross, discount, net, tax_rate_bp)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			billID, i+1, a.Device.TenantID, l.Ref, l.Name, l.Qty, r.Gross, r.LineDiscount+r.BillDiscount, r.Net, l.TaxRateBP); err != nil {
			return syncop.Applied{}, err
		}
	}
	for _, g := range printed.TaxGroups {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO bill_tax_line (bill_id, tenant_id, rate_bp, taxable, cgst, sgst, igst)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			billID, a.Device.TenantID, g.RateBP, g.Taxable, g.CGST, g.SGST, g.IGST); err != nil {
			return syncop.Applied{}, err
		}
	}
	for i, p := range b.Payments {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO bill_payment (bill_id, line_no, tenant_id, method, amount, reference)
			VALUES ($1, $2, $3, $4, $5, nullif($6, ''))`,
			billID, i+1, a.Device.TenantID, p.Method, p.AmountPaise, p.Reference); err != nil {
			return syncop.Applied{}, err
		}
	}

	if err := checkDrift(ctx, tx, a, billID, b.InvoiceNo, in); err != nil {
		return syncop.Applied{}, err
	}
	if b.OrderID != nil {
		// Link the bill to the order lines it charged. This never refuses the
		// bill; anything odd becomes an alert.
		var problems []string
		if len(b.LineIDs) != len(in.Lines) {
			problems = append(problems, fmt.Sprintf("bill has %d lines but names %d order lines", len(in.Lines), len(b.LineIDs)))
		} else {
			billed := make([]orders.BilledLine, len(b.LineIDs))
			for i, id := range b.LineIDs {
				l := in.Lines[i]
				var mods int64
				for _, m := range l.Modifiers {
					mods += m.PricePaise
				}
				billed[i] = orders.BilledLine{LineID: id, Qty: l.Qty, UnitPricePaise: l.UnitPricePaise, ModifierPaise: mods}
			}
			p, err := orders.MarkBilled(ctx, tx, a.Device, seq, *b.OrderID, billed, billID, issued)
			if err != nil {
				return syncop.Applied{}, err
			}
			problems = p
		}
		if len(problems) > 0 {
			if err := alerts.Raise(ctx, tx, a.Device.TenantID, "bill_order_mismatch", "warning", &billID, map[string]any{
				"invoice_no": b.InvoiceNo, "order_id": *b.OrderID, "problems": problems,
			}); err != nil {
				return syncop.Applied{}, err
			}
		}
	}
	if verification == "mismatch" {
		if err := alerts.Raise(ctx, tx, a.Device.TenantID, "price_mismatch", "critical", &billID, map[string]any{
			"invoice_no": b.InvoiceNo, "device_grand_total": printed.GrandTotal, "server_grand_total": ours.GrandTotal,
		}); err != nil {
			return syncop.Applied{}, err
		}
	}
	if diff != 0 {
		if err := alerts.Raise(ctx, tx, a.Device.TenantID, "payment_difference", "warning", &billID, map[string]any{
			"invoice_no": b.InvoiceNo, "paid": paid, "due": printed.GrandTotal + b.TipPaise, "difference": diff,
		}); err != nil {
			return syncop.Applied{}, err
		}
	}
	// Cash on a bill that arrives after its shift was counted: the shift's
	// expected cash and difference are corrected, in the books too.
	if err := dayclose.LateCash(ctx, tx, a, billID, b.IssuedAt); err != nil {
		return syncop.Applied{}, err
	}
	// The dishes' ingredients leave the store now, and their cost is booked.
	sold := make([]stock.Sold, len(in.Lines))
	for i, l := range in.Lines {
		sold[i] = stock.Sold{ItemRef: l.Ref, Qty: l.Qty}
		for _, m := range l.Modifiers {
			sold[i].AddonRefs = append(sold[i].AddonRefs, m.Ref)
		}
	}
	if err := stock.ConsumeForBill(ctx, tx, a.Device.TenantID, a.Device.OutletID, billID, bizDate, issued, sold); err != nil {
		return syncop.Applied{}, err
	}
	// A bill for a day the owner already closed is kept (the guest paid; the
	// invoice exists), and flagged: the day's Z report is now amended.
	closed, err := dayclose.DayClosed(ctx, tx, a.Device.OutletID, bizDate)
	if err != nil {
		return syncop.Applied{}, err
	}
	if closed {
		if err := alerts.Raise(ctx, tx, a.Device.TenantID, "bill_after_day_close", "warning", &billID, map[string]any{
			"invoice_no": b.InvoiceNo, "business_date": b.BusinessDate, "grand_total": printed.GrandTotal,
		}); err != nil {
			return syncop.Applied{}, err
		}
	}
	if err := outbox.Add(ctx, tx, a.Device.TenantID, "sales.bill.settled.v1", a.Device.OutletID.String(), map[string]any{
		"bill_id": billID, "invoice_no": b.InvoiceNo, "outlet_id": a.Device.OutletID, "channel": b.Channel,
		"business_date": b.BusinessDate, "grand_total": printed.GrandTotal, "server_seq": seq,
	}); err != nil {
		return syncop.Applied{}, err
	}
	return syncop.Applied{ServerSeq: seq}, nil
}

// maxAmount bounds every figure a device sends. Far above any real bill, and
// low enough that adding a few of them can never overflow int64.
const maxAmount = 1_000_000_000_000_000 // Rs 10 lakh crore

// consistent checks that the printed totals add up among themselves and match
// the input's shape. We post and report the printed figures, so if they did
// not add up, the journal entry could not balance and the rate-wise GST data
// (bill_tax_line) would contradict the bill.
func consistent(in pricing.Input, r pricing.Result) error {
	if len(r.Lines) != len(in.Lines) || len(in.Lines) == 0 || len(in.Lines) > pricing.MaxLines {
		return fmt.Errorf("totals have %d lines, input has %d", len(r.Lines), len(in.Lines))
	}
	// Range first, so the sums below cannot overflow.
	for _, v := range []int64{r.Subtotal, r.DiscountTotal, r.ServiceCharge, r.TaxableTotal,
		r.CGST, r.SGST, r.IGST, r.TaxTotal, r.GrandTotal} {
		if v < 0 || v > maxAmount {
			return fmt.Errorf("a total is out of range")
		}
	}
	if r.RoundOff < -49 || r.RoundOff > 50 {
		return fmt.Errorf("round_off %d out of range", r.RoundOff)
	}
	var lineNet int64
	for i, l := range r.Lines {
		for _, v := range []int64{l.Gross, l.LineDiscount, l.BillDiscount, l.Net} {
			if v < 0 || v > maxAmount {
				return fmt.Errorf("line %d out of range", i)
			}
		}
		if l.Gross-l.LineDiscount-l.BillDiscount != l.Net {
			return fmt.Errorf("line %d does not add up", i)
		}
		lineNet += l.Net
	}
	if r.Subtotal-r.DiscountTotal != lineNet {
		return fmt.Errorf("subtotal - discounts != sum of line nets")
	}

	var gNet, gTaxable, gSC, gCGST, gSGST, gIGST int64
	prevRate := int64(-1)
	for _, g := range r.TaxGroups {
		if g.RateBP <= prevRate || g.RateBP > pricing.MaxRateBP {
			return fmt.Errorf("tax groups must have distinct rates in ascending order, at most %d bp", pricing.MaxRateBP)
		}
		prevRate = g.RateBP
		for _, v := range []int64{g.Net, g.GoodsValue, g.ServiceCharge, g.Taxable, g.CGST, g.SGST, g.IGST} {
			if v < 0 || v > maxAmount {
				return fmt.Errorf("tax group %d bp out of range", g.RateBP)
			}
		}
		if g.GoodsValue+g.ServiceCharge != g.Taxable {
			return fmt.Errorf("tax group %d bp: taxable != goods + service charge", g.RateBP)
		}
		if in.Supply == pricing.Intra && (g.IGST != 0 || g.CGST != g.SGST) {
			return fmt.Errorf("tax group %d bp: intra-state needs CGST == SGST and no IGST", g.RateBP)
		}
		if in.Supply == pricing.Inter && (g.CGST != 0 || g.SGST != 0) {
			return fmt.Errorf("tax group %d bp: inter-state has IGST only", g.RateBP)
		}
		gNet += g.Net
		gTaxable += g.Taxable
		gSC += g.ServiceCharge
		gCGST += g.CGST
		gSGST += g.SGST
		gIGST += g.IGST
	}
	switch {
	case gNet != lineNet:
		return fmt.Errorf("tax groups hold %d of net, lines hold %d", gNet, lineNet)
	case gTaxable != r.TaxableTotal || gSC != r.ServiceCharge:
		return fmt.Errorf("tax groups do not add up to taxable_total / service_charge")
	case gCGST != r.CGST || gSGST != r.SGST || gIGST != r.IGST:
		return fmt.Errorf("tax groups do not add up to the CGST/SGST/IGST totals")
	case r.TaxTotal != r.CGST+r.SGST+r.IGST:
		return fmt.Errorf("tax_total %d != cgst+sgst+igst", r.TaxTotal)
	case r.GrandTotal != r.TaxableTotal+r.TaxTotal+r.RoundOff:
		return fmt.Errorf("grand_total %d != taxable_total + tax_total + round_off", r.GrandTotal)
	}
	return nil
}

// checkDrift compares what the device billed with what the back office says
// today: the outlet's price mode and rounding, and each menu item's GST rate.
// A difference is not proof of a bug (the till may have billed from a menu
// that changed since), so the bill is kept and a human is told. Without this
// check, a till billing a 5% item at 0% would pass verification, because the
// server re-prices the same wrong input.
func checkDrift(ctx context.Context, tx *sql.Tx, a syncop.ApplyContext, billID uuid.UUID, invoiceNo string, in pricing.Input) error {
	outlet, err := identity.Outlet(ctx, tx, a.Device.OutletID)
	if err != nil {
		return err
	}
	if string(in.PriceMode) != outlet.PriceMode || string(in.Rounding) != outlet.Rounding {
		if err := alerts.Raise(ctx, tx, a.Device.TenantID, "billing_settings_differ", "warning", &billID, map[string]any{
			"invoice_no": invoiceNo, "bill": map[string]any{"price_mode": in.PriceMode, "rounding": in.Rounding},
			"outlet": map[string]any{"price_mode": outlet.PriceMode, "rounding": outlet.Rounding},
		}); err != nil {
			return err
		}
	}

	var itemIDs []uuid.UUID
	for _, l := range in.Lines {
		if id, err := uuid.Parse(l.Ref); err == nil {
			itemIDs = append(itemIDs, id)
		}
	}
	rates, err := catalogue.ItemRates(ctx, tx, itemIDs)
	if err != nil {
		return err
	}
	var diffs []map[string]any
	for _, l := range in.Lines {
		id, err := uuid.Parse(l.Ref)
		if err != nil {
			continue // an open item typed at the till; nothing to compare with
		}
		if want, ok := rates[id]; ok && want != l.TaxRateBP {
			diffs = append(diffs, map[string]any{"item": l.Name, "billed_bp": l.TaxRateBP, "catalogue_bp": want})
		}
	}
	if len(diffs) > 0 {
		return alerts.Raise(ctx, tx, a.Device.TenantID, "tax_rate_differs", "critical", &billID,
			map[string]any{"invoice_no": invoiceNo, "lines": diffs})
	}
	return nil
}
