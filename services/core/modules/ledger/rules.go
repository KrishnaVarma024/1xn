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
