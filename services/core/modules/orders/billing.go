package orders

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"1xn/core/internal/platform/syncop"
)

// BilledLine is one line of a settled bill, as charged, and the order line it
// settles.
type BilledLine struct {
	LineID         uuid.UUID
	Qty            int64
	UnitPricePaise int64
	ModifierPaise  int64 // sum of the line's modifier prices, per unit
}

// MarkBilled links a settled bill to the order lines it charged for. It is
// called by the sales module inside the bill's transaction, after NextSeq, so
// it follows the same lock order as every other writer.
//
// It never fails the bill: problems (unknown order, lines on another bill,
// voided lines, a line charged differently from the order) come back as
// strings for an alert, because the printed invoice is what the guest paid
// and it must be stored regardless. Only a database failure is an error.
func MarkBilled(ctx context.Context, tx *sql.Tx, dev syncop.Device, seq int64,
	orderID uuid.UUID, lines []BilledLine, billID uuid.UUID, at time.Time) ([]string, error) {

	o, err := resolve(ctx, tx, orderID)
	switch {
	case errors.Is(err, errNoOrder):
		return []string{fmt.Sprintf("order %s is unknown", orderID)}, nil
	case errors.Is(err, errMergeChain):
		return []string{err.Error()}, nil
	case err != nil:
		return nil, err
	}
	if o.OutletID != dev.OutletID {
		return []string{fmt.Sprintf("order %s belongs to another outlet", orderID)}, nil
	}

	// Lines billed from a closed order's spillover (late items) live on the
	// spillover; split the list between the two.
	var sp *Order
	if o.Status != Open {
		if sp, err = openSpillover(ctx, tx, o.Root()); err != nil {
			return nil, err
		}
		if sp != nil && sp.ID == o.ID {
			sp = nil
		}
	}
	var problems []string
	var onOrder, onSpill []uuid.UUID
	for _, b := range lines {
		holder := o
		if sp != nil && o.line(b.LineID) == nil && sp.line(b.LineID) != nil {
			holder = sp
			onSpill = append(onSpill, b.LineID)
		} else {
			onOrder = append(onOrder, b.LineID)
		}
		if l := holder.line(b.LineID); l != nil {
			problems = append(problems, charged(l, b)...)
		}
	}

	for _, part := range []struct {
		o     *Order
		lines []uuid.UUID
	}{{o, onOrder}, {sp, onSpill}} {
		if part.o == nil || len(part.lines) == 0 {
			continue
		}
		changed, p := part.o.MarkBilled(part.lines, billID, at)
		problems = append(problems, p...)
		if !changed {
			continue
		}
		err := save(ctx, tx, dev.TenantID, part.o, seq)
		if errors.Is(err, errBadDoc) {
			// The bill is stored; the order cannot record it. A human looks.
			problems = append(problems, err.Error())
			continue
		}
		if err != nil {
			return nil, err
		}
	}
	return problems, nil
}

// charged compares what the bill charged for a line with what the order
// says was served. A bill that charges less than the order (1 naan against a
// line of 4, or a paneer at zero) is how food walks out unpaid; charging more
// is a complaint waiting to happen. Either way a human should look.
func charged(l *Line, b BilledLine) []string {
	var mods int64
	for _, m := range l.Modifiers {
		mods += m.PricePaise
	}
	var out []string
	if b.Qty != l.Qty {
		out = append(out, fmt.Sprintf("line %s: order has qty %d, bill charged %d", l.ID, l.Qty, b.Qty))
	}
	if b.UnitPricePaise != l.UnitPricePaise || b.ModifierPaise != mods {
		out = append(out, fmt.Sprintf("line %s: order has unit price %d + modifiers %d paise, bill charged %d + %d",
			l.ID, l.UnitPricePaise, mods, b.UnitPricePaise, b.ModifierPaise))
	}
	return out
}
