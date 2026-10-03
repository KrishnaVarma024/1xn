package ledger

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Payment is one tender on a bill.
type Payment struct {
	Method string // cash, upi, card, credit
	Amount int64
}

// BillSettled is everything the posting rule needs from a settled bill. The
// figures are the ones PRINTED on the invoice (the device's), because that is
// the tax the restaurant owes, even when our own recomputation disagrees.
type BillSettled struct {
	BillID        uuid.UUID
	TenantID      uuid.UUID
	LegalEntityID uuid.UUID
	OutletID      uuid.UUID
	Channel       string
	InvoiceNo     string
	IssuedAt      time.Time
	BusinessDate  time.Time
	GoodsValue    int64 // taxable value of food, after discounts
	ServiceCharge int64
	CGST          int64
	SGST          int64
	IGST          int64
	RoundOff      int64
	GrandTotal    int64
	Tip           int64
	Payments      []Payment
}

// BillSettledEntry is the posting rule for a settled bill. It is a pure
// function, so it can be tested without a database and could later run in an
// asynchronous consumer instead of inside the sales transaction.
//
//	Dr  cash / UPI / card / credit      what the guest paid
//	Cr  food and beverage sales         goods value
//	Cr  service charge income           service charge
//	Cr  output CGST / SGST / IGST       tax owed to the government
//	Cr  tips payable                    tip owed to staff
//	Cr/Dr round-off                     the paise the rounding added or removed
//	Cr/Dr suspense                      any difference between paid and due
//
// The suspense line means a device bug that records the wrong tender amount
// never blocks the books: the entry still balances, the difference is parked
// where an accountant will see it, and an alert is raised by the caller.
func BillSettledEntry(b BillSettled) (Entry, int64) {
	outlet := b.OutletID
	line := func(code string, amount int64) Line {
		return Line{AccountCode: code, Amount: amount, OutletID: &outlet, Channel: b.Channel}
	}

	byMethod := map[string]int64{}
	var paid int64
	for _, p := range b.Payments {
		byMethod[p.Method] += p.Amount
		paid += p.Amount
	}

	var lines []Line
	for _, m := range []struct{ method, code string }{
		{"cash", AcctCash}, {"upi", AcctUPI}, {"card", AcctCard}, {"credit", AcctReceivable},
	} {
		lines = append(lines, line(m.code, byMethod[m.method]))
	}
	lines = append(lines,
		line(AcctSales, -b.GoodsValue),
		line(AcctServiceCharge, -b.ServiceCharge),
		line(AcctOutputCGST, -b.CGST),
		line(AcctOutputSGST, -b.SGST),
		line(AcctOutputIGST, -b.IGST),
		line(AcctTipsPayable, -b.Tip),
		line(AcctRoundOff, -b.RoundOff),
	)
	diff := paid - (b.GrandTotal + b.Tip)
	lines = append(lines, line(AcctSuspense, -diff))

	issued := b.IssuedAt
	bd := b.BusinessDate
	return Entry{
		TenantID:      b.TenantID,
		LegalEntityID: b.LegalEntityID,
		SourceType:    "bill",
		SourceEventID: b.BillID,
		EntryDate:     issued,
		BusinessDate:  &bd,
		Memo:          fmt.Sprintf("Bill %s", b.InvoiceNo),
		Lines:         lines,
	}, diff
}

// PostBillSettled applies the rule and posts the entry. It returns the entry id
// and the payment difference (0 when the guest paid exactly what was due).
func PostBillSettled(ctx context.Context, tx *sql.Tx, b BillSettled) (uuid.UUID, int64, error) {
	e, diff := BillSettledEntry(b)
	id, err := Post(ctx, tx, e)
	return id, diff, err
}

// CashMoveEntry is the posting rule for cash that leaves or enters a till
// drawer other than through a sale. "Cash in hand" is the drawer.
//
//	expense     Dr petty cash expenses   Cr cash in hand
//	to_safe     Dr cash in safe          Cr cash in hand
//	from_safe   Dr cash in hand          Cr cash in safe
//	supplier    Dr till payouts (1150)   Cr cash in hand
func CashMoveEntry(tenantID, legalEntityID, outletID, moveID uuid.UUID, kind string, amount int64,
	at time.Time, businessDate time.Time, reason string) (Entry, error) {
	outlet := outletID
	line := func(code string, a int64) Line { return Line{AccountCode: code, Amount: a, OutletID: &outlet} }
	var lines []Line
	switch kind {
	case "expense":
		lines = []Line{line(AcctPettyExpenses, amount), line(AcctCash, -amount)}
	case "to_safe":
		lines = []Line{line(AcctCashSafe, amount), line(AcctCash, -amount)}
	case "from_safe":
		lines = []Line{line(AcctCash, amount), line(AcctCashSafe, -amount)}
	case "supplier":
		// A delivery paid from the drawer. The delivery itself (and its cost)
		// is booked when the owner receives it, against the same account.
		lines = []Line{line(AcctTillPayouts, amount), line(AcctCash, -amount)}
	default:
		return Entry{}, fmt.Errorf("ledger: unknown cash move kind %q", kind)
	}
	bd := businessDate
	return Entry{
		TenantID: tenantID, LegalEntityID: legalEntityID, SourceType: "cash_move", SourceEventID: moveID,
		EntryDate: at, BusinessDate: &bd, Memo: fmt.Sprintf("Cash %s: %s", kind, reason), Lines: lines,
	}, nil
}

// CashDifferenceEntry books what the counted drawer differs from what it
// should hold, when a shift closes. difference = counted - expected.
//
//	over  (+)   Dr cash in hand          Cr cash over and short
//	short (-)   Dr cash over and short   Cr cash in hand
//
// One formula covers both: the signs flip with the difference. The books then
// show the cash that is really there, and the loss (or gain) where an owner
// will see it.
func CashDifferenceEntry(tenantID, legalEntityID, outletID, shiftID uuid.UUID, difference int64,
	closedAt time.Time, businessDate time.Time) Entry {
	outlet := outletID
	bd := businessDate
	return Entry{
		TenantID: tenantID, LegalEntityID: legalEntityID, SourceType: "shift_close", SourceEventID: shiftID,
		EntryDate: closedAt, BusinessDate: &bd, Memo: "Cash counted at shift close",
		Lines: []Line{
			{AccountCode: AcctCash, Amount: difference, OutletID: &outlet},
			{AccountCode: AcctCashOverShort, Amount: -difference, OutletID: &outlet},
		},
	}
}

// ShiftAdjustEntry corrects a closed shift's cash difference when a cash bill
// of that shift reaches the server after the drawer was counted. The cash was
// in the drawer when it was counted, so the count stands; what the drawer
// should have held goes up by the bill, and the difference moves by
// `adjustment` (negative: less over, or more short). Same signs as
// CashDifferenceEntry; the bill's id names the event.
func ShiftAdjustEntry(tenantID, legalEntityID, outletID, billID uuid.UUID, adjustment int64,
	at time.Time, businessDate time.Time) Entry {
	e := CashDifferenceEntry(tenantID, legalEntityID, outletID, billID, adjustment, at, businessDate)
	e.SourceType = "shift_adjust"
	e.Memo = "Cash bill that reached the server after its shift was counted"
	return e
}

// Stock posting rules. Stock is perpetual: it is an asset (1300) while it sits
// in the store, and becomes a cost the moment it is used, lost or found
// missing. So the food cost of any day is in the books that day, not only
// after a month-end count.

// paidBy is how a purchase was paid: cash from the safe, cash from a till
// drawer (the till records the payout; account 1150 matches the two), from
// the bank, or on credit from the supplier (a payable to settle later).
var paidBy = map[string]string{"safe": AcctCashSafe, "till": AcctTillPayouts, "bank": AcctBank, "credit": AcctPayables}

// StockPurchaseEntry books goods received.
//
//	Dr stock (1300)               cost - trueUp
//	Dr cost of food sold (5100)   trueUp
//	Cr cash / bank / suppliers    cost
//
// trueUp corrects stock that was used before it was booked in (the outlet
// sold more than it held): it was costed at the last known price, and now
// the real price is known. Usually zero.
func StockPurchaseEntry(tenantID, legalEntityID, outletID, docID uuid.UUID, cost, trueUp int64, paid string,
	date, businessDate time.Time, memo string) (Entry, error) {
	acct, ok := paidBy[paid]
	if !ok {
		return Entry{}, fmt.Errorf("ledger: unknown payment %q for a purchase", paid)
	}
	outlet := outletID
	bd := businessDate
	return Entry{
		TenantID: tenantID, LegalEntityID: legalEntityID, SourceType: "stock_purchase", SourceEventID: docID,
		EntryDate: date, BusinessDate: &bd, Memo: memo,
		Lines: []Line{
			{AccountCode: AcctStock, Amount: cost - trueUp, OutletID: &outlet},
			{AccountCode: AcctFoodCost, Amount: trueUp, OutletID: &outlet},
			{AccountCode: acct, Amount: -cost, OutletID: &outlet},
		},
	}, nil
}

// StockOutEntry books stock that left the store at its value: used by bills
// (cost of food sold), thrown away (wastage), or missing at a count (count
// differences). A negative value is stock found, and flips the signs.
//
//	Dr expense   value
//	Cr stock     value
func StockOutEntry(tenantID, legalEntityID, outletID, docID uuid.UUID, kind string, value int64,
	date, businessDate time.Time, memo string) (Entry, error) {
	var acct, source string
	switch kind {
	case "sale":
		acct, source = AcctFoodCost, "stock_sale"
	case "wastage":
		acct, source = AcctWastage, "stock_wastage"
	case "count":
		acct, source = AcctCountDiff, "stock_count"
	default:
		return Entry{}, fmt.Errorf("ledger: unknown stock use %q", kind)
	}
	outlet := outletID
	bd := businessDate
	return Entry{
		TenantID: tenantID, LegalEntityID: legalEntityID, SourceType: source, SourceEventID: docID,
		EntryDate: date, BusinessDate: &bd, Memo: memo,
		Lines: []Line{
			{AccountCode: acct, Amount: value, OutletID: &outlet},
			{AccountCode: AcctStock, Amount: -value, OutletID: &outlet},
		},
	}, nil
}
