package dayclose

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"1xn/core/internal/platform/gst"
	"1xn/core/modules/orders"
)

// A trading day runs from 04:00 to 04:00 in India: the bar that closes at
// 01:30 is still selling "yesterday". Bills carry the business date the till
// gave them; voids and cancellations, which have only a time, are placed in a
// day by this cutoff.
const dayStartsAt = 4 * time.Hour

// window is the stretch of time that counts as one trading day.
func window(day time.Time) (from, to time.Time) {
	from = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, gst.IST).Add(dayStartsAt)
	return from, from.Add(24 * time.Hour)
}

// TradingDay is the trading day at t: before 04:00 it is still yesterday.
func TradingDay(t time.Time) time.Time {
	l := t.In(gst.IST).Add(-dayStartsAt)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
}

// Report is the end-of-day (Z) report of one outlet's trading day. Every
// figure is integer paise. It is computed from stored records only, so it can
// be recomputed at any time and compared with the copy kept at closing.
type Report struct {
	OutletID     uuid.UUID  `json:"outlet_id"`
	OutletName   string     `json:"outlet_name"`
	BusinessDate string     `json:"business_date"`
	From         time.Time  `json:"from"` // the window voids and cancellations are counted in
	To           time.Time  `json:"to"`
	GeneratedAt  time.Time  `json:"generated_at"`
	Sales        Sales      `json:"sales"`
	TaxByRate    []TaxRate  `json:"tax_by_rate"`
	Payments     []Payment  `json:"payments"`
	Channels     []Channel  `json:"channels"`
	Series       []Series   `json:"series"`
	Voids        Voids      `json:"voids"`
	Cancelled    Cancelled  `json:"cancelled"`
	OpenOrders   OpenOrders `json:"open_orders"`
	Shifts       []Shift    `json:"shifts"`
	CashMoves    []CashMove `json:"cash_moves"`
	Cash         Cash       `json:"cash"`
	TopItems     []Item     `json:"top_items"`
	Checks       Checks     `json:"checks"`
	Warnings     []Warning  `json:"warnings"`
	// Digest identifies exactly what the report says. Closing a day sends the
	// digest the owner looked at; if the day changed since, closing is refused.
	Digest string `json:"digest"`
}

// Sales are the day's bills by business date.
type Sales struct {
	Bills         int        `json:"bills"`
	Covers        int        `json:"covers"`
	FirstAt       *time.Time `json:"first_at,omitempty"`
	LastAt        *time.Time `json:"last_at,omitempty"`
	Subtotal      int64      `json:"subtotal"`
	Discounts     int64      `json:"discounts"`
	ServiceCharge int64      `json:"service_charge"`
	Taxable       int64      `json:"taxable"`
	CGST          int64      `json:"cgst"`
	SGST          int64      `json:"sgst"`
	IGST          int64      `json:"igst"`
	Tax           int64      `json:"tax"`
	RoundOff      int64      `json:"round_off"`
	GrandTotal    int64      `json:"grand_total"`
	Tips          int64      `json:"tips"`
	AvgBill       int64      `json:"avg_bill"`
	AvgCover      int64      `json:"avg_cover"` // over bills that recorded covers
}

// TaxRate is one GST slab: what GSTR-1 asks for.
type TaxRate struct {
	RateBP  int64 `json:"rate_bp"`
	Taxable int64 `json:"taxable"`
	CGST    int64 `json:"cgst"`
	SGST    int64 `json:"sgst"`
	IGST    int64 `json:"igst"`
}

// Payment is money taken by one method.
type Payment struct {
	Method string `json:"method"`
	Bills  int    `json:"bills"`
	Amount int64  `json:"amount"`
}

// Channel is sales through one channel.
type Channel struct {
	Channel    string `json:"channel"`
	Bills      int    `json:"bills"`
	GrandTotal int64  `json:"grand_total"`
}

// Series is one till's invoice numbers for the day. A missing number is a
// bill that was issued but has not reached the server (or was never issued:
// GST wants every number accounted for).
type Series struct {
	DeviceID      uuid.UUID `json:"device_id"`
	DeviceName    string    `json:"device_name"`
	FY            string    `json:"fy"`
	First         string    `json:"first"`
	Last          string    `json:"last"`
	Count         int       `json:"count"`
	ContinuesFrom string    `json:"continues_from,omitempty"` // the till's last number before the day
	Missing       []string  `json:"missing"`                  // at most maxMissing
	MissingCount  int       `json:"missing_count"`
}

const maxMissing = 50

// VoidLine is one dish taken off an order.
type VoidLine struct {
	OrderID    uuid.UUID `json:"order_id"`
	Table      string    `json:"table,omitempty"`
	Name       string    `json:"name"`
	Qty        int64     `json:"qty"`
	Value      int64     `json:"value"`
	Reason     string    `json:"reason"`
	ApprovedBy string    `json:"approved_by,omitempty"`
	At         time.Time `json:"at"`
	Fired      bool      `json:"fired"`     // the kitchen had it: food was made
	Cancelled  bool      `json:"cancelled"` // part of a whole order cancelled
}

// Voids are dishes taken off orders during the day.
type Voids struct {
	Lines int        `json:"lines"`
	Value int64      `json:"value"`
	Fired int        `json:"fired"` // of which the kitchen had already been told to cook
	List  []VoidLine `json:"list"`  // at most maxVoids, newest first
}

const maxVoids = 100

// Cancelled counts orders cancelled whole during the day.
type Cancelled struct {
	Orders int   `json:"orders"`
	Value  int64 `json:"value"`
}

// OpenOrders are orders opened by the end of the day and not yet settled.
type OpenOrders struct {
	Orders int      `json:"orders"`
	Value  int64    `json:"value"`
	Tables []string `json:"tables"`
}

// Shift is one till shift of the day.
type Shift struct {
	ID           uuid.UUID  `json:"id"`
	DeviceID     uuid.UUID  `json:"device_id"`
	DeviceName   string     `json:"device_name"`
	OpenedBy     string     `json:"opened_by"`
	OpenedAt     time.Time  `json:"opened_at"`
	ClosedBy     string     `json:"closed_by,omitempty"`
	ClosedAt     *time.Time `json:"closed_at,omitempty"`
	OpeningFloat int64      `json:"opening_float"`
	CashSales    int64      `json:"cash_sales"`
	Expenses     int64      `json:"expenses"`
	Suppliers    int64      `json:"suppliers"` // deliveries paid from the drawer
	ToSafe       int64      `json:"to_safe"`
	FromSafe     int64      `json:"from_safe"`
	Expected     int64      `json:"expected"`             // for an open shift: so far
	Counted      *int64     `json:"counted,omitempty"`    // closed shifts only
	Difference   *int64     `json:"difference,omitempty"` // counted - expected: + over, - short
	LedgerStatus string     `json:"ledger_status,omitempty"`
}

// CashMove is cash paid out of or into a drawer other than sales.
type CashMove struct {
	ShiftID uuid.UUID `json:"shift_id"`
	Kind    string    `json:"kind"`
	Amount  int64     `json:"amount"`
	Reason  string    `json:"reason"`
	At      time.Time `json:"at"`
}

// Cash sums the day's drawers.
type Cash struct {
	Floats        int64 `json:"floats"`
	Sales         int64 `json:"sales"`
	Expenses      int64 `json:"expenses"`
	Suppliers     int64 `json:"suppliers"`
	ToSafe        int64 `json:"to_safe"`
	FromSafe      int64 `json:"from_safe"`
	Expected      int64 `json:"expected"`
	Counted       int64 `json:"counted"`    // closed shifts
	Difference    int64 `json:"difference"` // closed shifts
	OpenShifts    int   `json:"open_shifts"`
	OutsideShifts int64 `json:"outside_shifts"` // cash on the day's bills taken while no shift was open on that till
}

// Item is a best seller.
type Item struct {
	Name string `json:"name"`
	Qty  int64  `json:"qty"`
	Net  int64  `json:"net"`
}

// Checks count records that need a look.
type Checks struct {
	PriceMismatches    int `json:"price_mismatches"`
	PaymentDifferences int `json:"payment_differences"`
	LedgerBlocked      int `json:"ledger_blocked"`
}

// Warning is something the owner should see before closing. Level "check"
// warnings must be acknowledged to close the day; "info" ones need not.
type Warning struct {
	Code  string `json:"code"`
	Level string `json:"level"`
	Text  string `json:"text"`
}

// Build computes the report from the database, as the transaction sees it.
func Build(ctx context.Context, tx *sql.Tx, outletID uuid.UUID, day time.Time, now time.Time) (*Report, error) {
	from, to := window(day)
	r := &Report{
		OutletID: outletID, BusinessDate: day.Format(time.DateOnly), From: from, To: to, GeneratedAt: now,
		TaxByRate: []TaxRate{}, Payments: []Payment{}, Channels: []Channel{}, Series: []Series{},
		Voids: Voids{List: []VoidLine{}}, OpenOrders: OpenOrders{Tables: []string{}}, Shifts: []Shift{},
		CashMoves: []CashMove{}, TopItems: []Item{}, Warnings: []Warning{},
	}
	if err := tx.QueryRowContext(ctx, `SELECT name FROM outlet WHERE id = $1`, outletID).Scan(&r.OutletName); err != nil {
		return nil, err
	}
	d := r.BusinessDate
	steps := []func(context.Context, *sql.Tx, *Report, string) error{
		salesTotals, taxByRate, payments, channels, series, topItems, checks,
	}
	for _, step := range steps {
		if err := step(ctx, tx, r, d); err != nil {
			return nil, err
		}
	}
	if err := ordersOfDay(ctx, tx, r, from, to); err != nil {
		return nil, err
	}
	if err := shiftsOfDay(ctx, tx, r, d, now); err != nil {
		return nil, err
	}
	r.warn()
	r.Digest = r.digest()
	return r, nil
}

func salesTotals(ctx context.Context, tx *sql.Tx, r *Report, d string) error {
	s := &r.Sales
	var first, last sql.NullTime
	var coverBills int64
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*), coalesce(sum(covers), 0), min(issued_at), max(issued_at),
		       coalesce(sum(subtotal), 0), coalesce(sum(discount_total), 0), coalesce(sum(service_charge), 0),
		       coalesce(sum(taxable_total), 0), coalesce(sum(cgst), 0), coalesce(sum(sgst), 0), coalesce(sum(igst), 0),
		       coalesce(sum(tax_total), 0), coalesce(sum(round_off), 0), coalesce(sum(grand_total), 0), coalesce(sum(tip), 0),
		       coalesce(sum(grand_total) FILTER (WHERE covers > 0), 0)
		FROM bill WHERE outlet_id = $1 AND business_date = $2`, r.OutletID, d).
		Scan(&s.Bills, &s.Covers, &first, &last, &s.Subtotal, &s.Discounts, &s.ServiceCharge, &s.Taxable,
			&s.CGST, &s.SGST, &s.IGST, &s.Tax, &s.RoundOff, &s.GrandTotal, &s.Tips, &coverBills); err != nil {
		return err
	}
	if first.Valid {
		s.FirstAt, s.LastAt = &first.Time, &last.Time
	}
	if s.Bills > 0 {
		s.AvgBill = s.GrandTotal / int64(s.Bills)
	}
	if s.Covers > 0 {
		s.AvgCover = coverBills / int64(s.Covers)
	}
	return nil
}

func taxByRate(ctx context.Context, tx *sql.Tx, r *Report, d string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT t.rate_bp, sum(t.taxable), sum(t.cgst), sum(t.sgst), sum(t.igst)
		FROM bill_tax_line t JOIN bill b ON b.id = t.bill_id AND b.tenant_id = t.tenant_id
		WHERE b.outlet_id = $1 AND b.business_date = $2
		GROUP BY t.rate_bp ORDER BY t.rate_bp`, r.OutletID, d)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var t TaxRate
		if err := rows.Scan(&t.RateBP, &t.Taxable, &t.CGST, &t.SGST, &t.IGST); err != nil {
			return err
		}
		r.TaxByRate = append(r.TaxByRate, t)
	}
	return rows.Err()
}

func payments(ctx context.Context, tx *sql.Tx, r *Report, d string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT p.method, count(DISTINCT p.bill_id), sum(p.amount)
		FROM bill_payment p JOIN bill b ON b.id = p.bill_id AND b.tenant_id = p.tenant_id
		WHERE b.outlet_id = $1 AND b.business_date = $2
		GROUP BY p.method ORDER BY sum(p.amount) DESC, p.method`, r.OutletID, d)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var p Payment
		if err := rows.Scan(&p.Method, &p.Bills, &p.Amount); err != nil {
			return err
		}
		r.Payments = append(r.Payments, p)
	}
	return rows.Err()
}

func channels(ctx context.Context, tx *sql.Tx, r *Report, d string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT channel, count(*), sum(grand_total)
		FROM bill WHERE outlet_id = $1 AND business_date = $2
		GROUP BY channel ORDER BY sum(grand_total) DESC, channel`, r.OutletID, d)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.Channel, &c.Bills, &c.GrandTotal); err != nil {
			return err
		}
		r.Channels = append(r.Channels, c)
	}
	return rows.Err()
}

// series checks each till's invoice numbers for the day. A till numbers its
// bills 1, 2, 3 ... afresh each financial year, so the day's numbers must
// carry on from the till's last bill before the day (or from 1). Numbers in
// between are looked up a week either side, so a bill filed under the
// neighbouring business day is not reported missing.
func series(ctx context.Context, tx *sql.Tx, r *Report, d string) error {
	type key struct {
		device uuid.UUID
		fy     string
	}
	type group struct {
		name    string
		prefix  string
		nums    []int
		firstAt time.Time
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT b.device_id, coalesce(dv.name, ''), b.fy, b.invoice_no, b.issued_at
		FROM bill b LEFT JOIN device dv ON dv.id = b.device_id
		WHERE b.outlet_id = $1 AND b.business_date = $2`, r.OutletID, d)
	if err != nil {
		return err
	}
	groups := map[key]*group{}
	var keys []key
	for rows.Next() {
		var k key
		var name, no string
		var at time.Time
		if err := rows.Scan(&k.device, &name, &k.fy, &no, &at); err != nil {
			rows.Close()
			return err
		}
		prefix, _, n, err := gst.ParseInvoiceNo(no)
		if err != nil {
			continue // stored bills were checked on the way in
		}
		g := groups[k]
		if g == nil {
			g = &group{name: name, prefix: prefix, firstAt: at}
			groups[k] = g
			keys = append(keys, k)
		}
		g.nums = append(g.nums, n)
		if at.Before(g.firstAt) {
			g.firstAt = at
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	sort.Slice(keys, func(i, j int) bool {
		gi, gj := groups[keys[i]], groups[keys[j]]
		if gi.name != gj.name {
			return gi.name < gj.name
		}
		if keys[i].device != keys[j].device { // two tills may share a name; the order must not depend on row order
			return keys[i].device.String() < keys[j].device.String()
		}
		return keys[i].fy < keys[j].fy
	})
	day, _ := time.Parse(time.DateOnly, d)
	for _, k := range keys {
		g := groups[k]
		sort.Ints(g.nums)
		first, last := g.nums[0], g.nums[len(g.nums)-1]
		s := Series{
			DeviceID: k.device, DeviceName: g.name, FY: k.fy, Count: len(g.nums), Missing: []string{},
			First: gst.InvoiceNo(g.prefix, k.fy, first), Last: gst.InvoiceNo(g.prefix, k.fy, last),
		}
		// The till's last bill of this financial year before the day's first.
		start := 1
		var prevNo string
		err := tx.QueryRowContext(ctx, `
			SELECT invoice_no FROM bill
			WHERE device_id = $1 AND fy = $2 AND issued_at < $3
			ORDER BY issued_at DESC LIMIT 1`, k.device, k.fy, g.firstAt).Scan(&prevNo)
		switch {
		case err == nil:
			if _, _, n, perr := gst.ParseInvoiceNo(prevNo); perr == nil && n < first {
				start, s.ContinuesFrom = n+1, prevNo
			} else {
				start = first
			}
		case errors.Is(err, sql.ErrNoRows):
		default:
			return err
		}
		if start > first {
			start = first
		}
		near := map[int]bool{}
		nrows, err := tx.QueryContext(ctx, `
			SELECT invoice_no FROM bill
			WHERE outlet_id = $1 AND device_id = $2 AND fy = $3 AND business_date BETWEEN $4 AND $5`,
			r.OutletID, k.device, k.fy, day.AddDate(0, 0, -7).Format(time.DateOnly), day.AddDate(0, 0, 7).Format(time.DateOnly))
		if err != nil {
			return err
		}
		for nrows.Next() {
			var no string
			if err := nrows.Scan(&no); err != nil {
				nrows.Close()
				return err
			}
			if _, _, n, err := gst.ParseInvoiceNo(no); err == nil {
				near[n] = true
			}
		}
		nrows.Close()
		if err := nrows.Err(); err != nil {
			return err
		}
		for n := start; n <= last; n++ {
			if !near[n] {
				s.MissingCount++
				if len(s.Missing) < maxMissing {
					s.Missing = append(s.Missing, gst.InvoiceNo(g.prefix, k.fy, n))
				}
			}
		}
		r.Series = append(r.Series, s)
	}
	return nil
}

func topItems(ctx context.Context, tx *sql.Tx, r *Report, d string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT l.name, sum(l.qty), sum(l.net)
		FROM bill_line l JOIN bill b ON b.id = l.bill_id AND b.tenant_id = l.tenant_id
		WHERE b.outlet_id = $1 AND b.business_date = $2
		GROUP BY l.name ORDER BY sum(l.net) DESC, l.name LIMIT 10`, r.OutletID, d)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.Name, &it.Qty, &it.Net); err != nil {
			return err
		}
		r.TopItems = append(r.TopItems, it)
	}
	return rows.Err()
}

func checks(ctx context.Context, tx *sql.Tx, r *Report, d string) error {
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE b.verification = 'mismatch'),
		       count(*) FILTER (WHERE b.ledger_status = 'blocked'),
		       count(*) FILTER (WHERE coalesce(p.paid, 0) <> b.grand_total + b.tip)
		FROM bill b
		LEFT JOIN (SELECT bill_id, tenant_id, sum(amount) AS paid FROM bill_payment GROUP BY bill_id, tenant_id) p
		       ON p.bill_id = b.id AND p.tenant_id = b.tenant_id
		WHERE b.outlet_id = $1 AND b.business_date = $2`, r.OutletID, d).
		Scan(&r.Checks.PriceMismatches, &r.Checks.LedgerBlocked, &r.Checks.PaymentDifferences); err != nil {
		return err
	}
	// Drawer differences and cash moves of the day's shifts that a locked
	// month kept out of the books are just as missing from them as bills.
	var drawers int
	if err := tx.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM till_shift WHERE outlet_id = $1 AND business_date = $2 AND ledger_status = 'blocked')
		     + (SELECT count(*) FROM cash_move m JOIN till_shift s ON s.id = m.shift_id AND s.tenant_id = m.tenant_id
		        WHERE s.outlet_id = $1 AND s.business_date = $2 AND m.ledger_status = 'blocked')`, r.OutletID, d).
		Scan(&drawers); err != nil {
		return err
	}
	r.Checks.LedgerBlocked += drawers
	return nil
}

// ordersOfDay finds the day's voids and cancellations (by the 04:00 window),
// and the orders opened by the day's end that are still open.
//
// Voids come from the void log, by the time they happened. A voided line can
// move to another order later (a merge), but its void stays on its own day,
// so every void is counted on exactly one day.
func ordersOfDay(ctx context.Context, tx *sql.Tx, r *Report, from, to time.Time) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT v.order_id, coalesce(t.label, ''), v.name, v.qty, v.value, v.reason, coalesce(v.approved_by, ''),
		       v.at, v.fired, v.cancelled
		FROM order_void v LEFT JOIN dining_table t ON t.id = v.table_id AND t.tenant_id = v.tenant_id
		WHERE v.outlet_id = $1 AND v.at >= $2 AND v.at < $3
		ORDER BY v.at DESC, v.line_id`, r.OutletID, from, to)
	if err != nil {
		return err
	}
	for rows.Next() {
		var l VoidLine
		if err := rows.Scan(&l.OrderID, &l.Table, &l.Name, &l.Qty, &l.Value, &l.Reason, &l.ApprovedBy,
			&l.At, &l.Fired, &l.Cancelled); err != nil {
			rows.Close()
			return err
		}
		r.Voids.Lines++
		r.Voids.Value += l.Value
		if l.Fired {
			r.Voids.Fired++
		}
		if l.Cancelled {
			r.Cancelled.Value += l.Value
		}
		if len(r.Voids.List) < maxVoids {
			r.Voids.List = append(r.Voids.List, l)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM pos_order
		WHERE outlet_id = $1 AND status = 'cancelled' AND closed_at >= $2 AND closed_at < $3`, r.OutletID, from, to).
		Scan(&r.Cancelled.Orders); err != nil {
		return err
	}

	orows, err := tx.QueryContext(ctx, `
		SELECT o.doc, coalesce(t.label, '')
		FROM pos_order o LEFT JOIN dining_table t ON t.id = o.table_id AND t.tenant_id = o.tenant_id
		WHERE o.outlet_id = $1 AND o.status = 'open' AND o.opened_at < $2
		ORDER BY o.opened_at, o.id`, r.OutletID, to)
	if err != nil {
		return err
	}
	defer orows.Close()
	for orows.Next() {
		var doc []byte
		var table string
		if err := orows.Scan(&doc, &table); err != nil {
			return err
		}
		var o orders.Order
		if err := json.Unmarshal(doc, &o); err != nil {
			return fmt.Errorf("order doc: %w", err)
		}
		r.OpenOrders.Orders++
		for i := range o.Lines {
			if o.Lines[i].Live() {
				r.OpenOrders.Value += o.Lines[i].Value()
			}
		}
		if table != "" {
			r.OpenOrders.Tables = append(r.OpenOrders.Tables, table)
		}
	}
	if err := orows.Err(); err != nil {
		return err
	}
	sort.Strings(r.OpenOrders.Tables)
	return nil
}

func shiftsOfDay(ctx context.Context, tx *sql.Tx, r *Report, d string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT s.id, s.device_id, coalesce(dv.name, ''), s.business_date, s.opened_by, s.opened_at, s.opening_float,
		       coalesce(s.closed_by, ''), s.closed_at, s.counted_cash, coalesce(s.cash_sales, 0),
		       coalesce(s.expected_cash, 0), s.difference, coalesce(s.ledger_status, '')
		FROM till_shift s LEFT JOIN device dv ON dv.id = s.device_id
		WHERE s.outlet_id = $1 AND s.business_date = $2
		ORDER BY s.opened_at, s.id`, r.OutletID, d)
	if err != nil {
		return err
	}
	var open []int
	for rows.Next() {
		var s Shift
		var bizDate time.Time
		var closed sql.NullTime
		var counted, diff sql.NullInt64
		if err := rows.Scan(&s.ID, &s.DeviceID, &s.DeviceName, &bizDate, &s.OpenedBy, &s.OpenedAt, &s.OpeningFloat,
			&s.ClosedBy, &closed, &counted, &s.CashSales, &s.Expected, &diff, &s.LedgerStatus); err != nil {
			rows.Close()
			return err
		}
		if closed.Valid {
			s.ClosedAt, s.Counted, s.Difference = &closed.Time, &counted.Int64, &diff.Int64
		} else {
			open = append(open, len(r.Shifts))
		}
		r.Shifts = append(r.Shifts, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// Cash moves of the day's shifts, and their totals per shift.
	mrows, err := tx.QueryContext(ctx, `
		SELECT m.shift_id, m.kind, m.amount, m.reason, m.at
		FROM cash_move m JOIN till_shift s ON s.id = m.shift_id AND s.tenant_id = m.tenant_id
		WHERE s.outlet_id = $1 AND s.business_date = $2
		ORDER BY m.at, m.id`, r.OutletID, d)
	if err != nil {
		return err
	}
	moved := map[uuid.UUID]*Shift{}
	for i := range r.Shifts {
		moved[r.Shifts[i].ID] = &r.Shifts[i]
	}
	for mrows.Next() {
		var m CashMove
		if err := mrows.Scan(&m.ShiftID, &m.Kind, &m.Amount, &m.Reason, &m.At); err != nil {
			mrows.Close()
			return err
		}
		r.CashMoves = append(r.CashMoves, m)
		s := moved[m.ShiftID]
		switch m.Kind {
		case "expense":
			s.Expenses += m.Amount
		case "supplier":
			s.Suppliers += m.Amount
		case "to_safe":
			s.ToSafe += m.Amount
		case "from_safe":
			s.FromSafe += m.Amount
		}
	}
	mrows.Close()
	if err := mrows.Err(); err != nil {
		return err
	}
	// An open shift's drawer so far: the same sum as at closing, up to now.
	for _, i := range open {
		s := &r.Shifts[i]
		c, err := countShift(ctx, tx, &shift{ID: s.ID, DeviceID: s.DeviceID, OpenedAt: s.OpenedAt, OpeningFloat: s.OpeningFloat}, now)
		if err != nil {
			return err
		}
		s.CashSales, s.Expected = c.CashSales, c.Expected
	}
	for _, s := range r.Shifts {
		c := &r.Cash
		c.Floats += s.OpeningFloat
		c.Sales += s.CashSales
		c.Expenses += s.Expenses
		c.Suppliers += s.Suppliers
		c.ToSafe += s.ToSafe
		c.FromSafe += s.FromSafe
		c.Expected += s.Expected
		if s.Counted != nil {
			c.Counted += *s.Counted
			c.Difference += *s.Difference
		} else {
			c.OpenShifts++
		}
	}
	if len(r.Shifts) == 0 {
		return nil // an outlet that does not run shifts: nothing to compare with
	}
	return tx.QueryRowContext(ctx, `
		SELECT coalesce(sum(p.amount), 0)
		FROM bill b JOIN bill_payment p ON p.bill_id = b.id AND p.tenant_id = b.tenant_id
		WHERE b.outlet_id = $1 AND b.business_date = $2 AND p.method = 'cash'
		  AND NOT EXISTS (SELECT 1 FROM till_shift s
		                  WHERE s.device_id = b.device_id AND s.opened_at <= b.issued_at
		                    AND (s.closed_at IS NULL OR b.issued_at <= s.closed_at))`, r.OutletID, d).
		Scan(&r.Cash.OutsideShifts)
}

// warn lists what the owner should look at before closing.
func (r *Report) warn() {
	add := func(code, level, format string, args ...any) {
		r.Warnings = append(r.Warnings, Warning{Code: code, Level: level, Text: fmt.Sprintf(format, args...)})
	}
	for _, s := range r.Shifts {
		if s.ClosedAt == nil {
			add("open_shift", "check", "%s: %s's shift is still open. Count the drawer and close it on the till.", s.DeviceName, s.OpenedBy)
		}
	}
	if n := r.OpenOrders.Orders; n > 0 {
		tables := ""
		if len(r.OpenOrders.Tables) > 0 {
			tables = " (" + strings.Join(r.OpenOrders.Tables, ", ") + ")"
		}
		add("open_orders", "check", "%d %s still open%s worth %s.", n, plural(n, "order is", "orders are"), tables, rupees(r.OpenOrders.Value))
	}
	for _, s := range r.Series {
		if s.MissingCount > 0 {
			add("missing_invoices", "check", "%s is missing %d invoice %s (%s). Those bills may not have synced yet.",
				s.DeviceName, s.MissingCount, plural(s.MissingCount, "number", "numbers"), firstFew(s.Missing, 3))
		}
	}
	for _, s := range r.Shifts {
		if s.Difference != nil && *s.Difference != 0 {
			word := "over"
			if *s.Difference < 0 {
				word = "short"
			}
			add("cash_difference", "info", "%s: %s's drawer was %s %s.", s.DeviceName, s.OpenedBy, rupees(abs(*s.Difference)), word)
		}
	}
	if r.Cash.OutsideShifts > 0 {
		add("cash_outside_shifts", "info", "%s in cash was taken on bills while no shift was open on that till.", rupees(r.Cash.OutsideShifts))
	}
	if n := r.Checks.PriceMismatches; n > 0 {
		add("price_mismatch", "info", "%d %s priced differently on the till than on the server.", n, plural(n, "bill was", "bills were"))
	}
	if n := r.Checks.PaymentDifferences; n > 0 {
		add("payment_difference", "info", "%d %s paid more or less than the bill total.", n, plural(n, "bill was", "bills were"))
	}
	if n := r.Checks.LedgerBlocked; n > 0 {
		add("ledger_blocked", "info", "%d %s not posted to the books (bills, drawer differences or cash moves): the accounting month is locked.",
			n, plural(n, "record is", "records are"))
	}
}

// NeedsAcknowledging reports whether closing must be confirmed.
func (r *Report) NeedsAcknowledging() bool {
	for _, w := range r.Warnings {
		if w.Level == "check" {
			return true
		}
	}
	return false
}

// digest identifies the day's settled figures: bills, taxes, payments,
// invoice numbers, voids, shifts and cash moves. It leaves out what moves on
// its own without the day changing: when the report was made, an open shift's
// running cash (it counts up to now, across days), and the tables still
// eating. Otherwise a till billing tomorrow on a shift opened today would
// keep the owner from ever closing today.
func (r *Report) digest() string {
	shifts := make([]Shift, len(r.Shifts))
	for i, s := range r.Shifts {
		if s.ClosedAt == nil {
			s.CashSales, s.Expected = 0, 0
		}
		shifts[i] = s
	}
	b, _ := json.Marshal(struct {
		OutletID      uuid.UUID
		BusinessDate  string
		Sales         Sales
		TaxByRate     []TaxRate
		Payments      []Payment
		Channels      []Channel
		Series        []Series
		Voids         Voids
		Cancelled     Cancelled
		Shifts        []Shift
		CashMoves     []CashMove
		OutsideShifts int64
		TopItems      []Item
		Checks        Checks
	}{r.OutletID, r.BusinessDate, r.Sales, r.TaxByRate, r.Payments, r.Channels, r.Series, r.Voids, r.Cancelled,
		shifts, r.CashMoves, r.Cash.OutsideShifts, r.TopItems, r.Checks})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

func firstFew(s []string, n int) string {
	if len(s) <= n {
		return strings.Join(s, ", ")
	}
	return strings.Join(s[:n], ", ") + ", …"
}

// rupees formats paise for a warning: Rs 1,23,456.78 in Indian grouping.
func rupees(p int64) string {
	sign := ""
	if p < 0 {
		sign, p = "-", -p
	}
	whole := fmt.Sprint(p / 100)
	if len(whole) > 3 {
		head, tail := whole[:len(whole)-3], whole[len(whole)-3:]
		var parts []string
		for len(head) > 2 {
			parts = append([]string{head[len(head)-2:]}, parts...)
			head = head[:len(head)-2]
		}
		parts = append([]string{head}, parts...)
		whole = strings.Join(parts, ",") + "," + tail
	}
	return fmt.Sprintf("%s₹%s.%02d", sign, whole, p%100)
}
