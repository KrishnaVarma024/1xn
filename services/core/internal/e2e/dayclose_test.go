package e2e

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"1xn/core/internal/platform/gst"
	"1xn/core/internal/platform/ids"
	"1xn/core/modules/dayclose"
	"1xn/core/modules/sales"
	"1xn/core/modules/sales/pricing"
	"1xn/core/modules/syncgw"
)

// at is a time on Monday 28 Sep 2026 in India.
func at(hour, min int) time.Time { return time.Date(2026, 9, 28, hour, min, 0, 0, gst.IST) }

const closeDay = "2026-09-28"

func (f *fixture) openShift(id uuid.UUID, when time.Time, by string, float int64) syncgw.Op {
	return f.op("shift.opened", map[string]any{
		"shift_id": id, "business_date": when.Format(time.DateOnly), "opened_at": when, "opened_by": by, "opening_float_paise": float,
	})
}

func (f *fixture) moveCash(shift uuid.UUID, kind string, amount int64, reason string, when time.Time) syncgw.Op {
	return f.op("cash.moved", map[string]any{
		"move_id": ids.New(), "shift_id": shift, "kind": kind, "amount_paise": amount, "reason": reason, "at": when,
	})
}

func (f *fixture) closeShift(id uuid.UUID, when time.Time, counted int64, denoms map[string]int64) syncgw.Op {
	p := map[string]any{"shift_id": id, "closed_at": when, "closed_by": "Asha", "counted_paise": counted}
	if denoms != nil {
		p["denominations"] = denoms
	}
	return f.op("shift.closed", p)
}

// notes counts an amount in Indian notes and coins, largest first.
func notes(paise int64) map[string]int64 {
	out := map[string]int64{}
	rupees := paise / 100
	for _, n := range []int64{500, 200, 100, 50, 20, 10, 5, 2, 1} {
		if c := rupees / n; c > 0 {
			out[strconv.FormatInt(n, 10)] = c
			rupees -= c * n
		}
	}
	return out
}

func total(t *testing.T, in pricing.Input) int64 {
	t.Helper()
	r, err := pricing.Compute(in)
	if err != nil {
		t.Fatal(err)
	}
	return r.GrandTotal
}

func (f *fixture) day(t *testing.T, date string) dayclose.DayView {
	t.Helper()
	var v dayclose.DayView
	must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/days/"+date, f.OwnerToken, nil, &v)
	return v
}

func (f *fixture) closeDay(t *testing.T, want int, date, digest string, ack bool) (dayclose.DayView, string) {
	t.Helper()
	var v dayclose.DayView
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	var out any = &v
	if want != http.StatusCreated {
		out = &e
	}
	status, _ := call(t, "POST", "/v1/outlets/"+f.OutletID+"/days/"+date+"/close", f.OwnerToken,
		map[string]any{"digest": digest, "acknowledge_warnings": ack}, out)
	if status != want {
		t.Fatalf("close %s: status %d, want %d (%s)", date, status, want, e.Error.Code)
	}
	return v, e.Error.Code
}

func warningCodes(r *dayclose.Report) map[string]string {
	out := map[string]string{}
	for _, w := range r.Warnings {
		out[w.Code] = w.Level
	}
	return out
}

// TestDayClose is a whole trading day: a lunch shift that ends ₹50 short
// after an expense and a safe drop, an evening shift still open, a void, a
// cancelled order, a table still eating, the owner closing the day, and a
// bill that syncs after the day was closed.
func TestDayClose(t *testing.T) {
	till := newRestaurant(t, "Day Close Dhaba", "", "29")
	k := setupFloor(t, till)

	lunch, evening := ids.New(), ids.New()
	b1, b2, b3 := pavBhaji(2), pavBhaji(1), pavBhaji(1)
	g1, g2, g3 := total(t, b1), total(t, b2), total(t, b3)
	expected := int64(200_000) + g1 - 30_000 - 100_000
	counted := expected - 5_000
	till.apply(t,
		till.openShift(lunch, at(11, 0), "Asha", 200_000),
		till.bill(t, b1, billOpts{issuedAt: at(12, 30)}),
		till.bill(t, b2, billOpts{issuedAt: at(13, 0), payments: []sales.Payment{{Method: "upi", AmountPaise: g2}}}),
		till.moveCash(lunch, "expense", 30_000, "Vegetables from the market", at(14, 0)),
		till.moveCash(lunch, "to_safe", 100_000, "Mid-day drop", at(15, 0)),
		till.closeShift(lunch, at(16, 0), counted, notes(counted)),
		till.openShift(evening, at(18, 0), "Vikram", 100_000),
		till.bill(t, b3, billOpts{issuedAt: at(21, 14)}),
	)

	// The drawer: expected, counted, ₹50 short, posted and alerted.
	var exp, diff int64
	var status string
	if err := ownerDB.QueryRow(`SELECT expected_cash, difference, ledger_status FROM till_shift WHERE id = $1`, lunch).
		Scan(&exp, &diff, &status); err != nil {
		t.Fatal(err)
	}
	if exp != expected || diff != -5_000 || status != "posted" {
		t.Fatalf("lunch shift: expected %d (want %d), difference %d, ledger %s", exp, expected, diff, status)
	}
	if n := till.alerts(t)["cash_difference"]; n != 1 {
		t.Fatalf("a short drawer raises one cash_difference alert, got %v", till.alerts(t))
	}
	var tb struct {
		Accounts []struct {
			Code    string `json:"code"`
			Balance int64  `json:"balance"`
		} `json:"accounts"`
		Balanced bool `json:"balanced"`
	}
	must(t, http.StatusOK, "GET", "/v1/ledger/trial-balance", till.OwnerToken, nil, &tb)
	bal := map[string]int64{}
	for _, a := range tb.Accounts {
		bal[a.Code] = a.Balance
	}
	if !tb.Balanced || bal["5200"] != 30_000 || bal["1105"] != 100_000 || bal["5900"] != 5_000 {
		t.Fatalf("books: petty expenses, safe and over/short = %d, %d, %d (balanced %v)", bal["5200"], bal["1105"], bal["5900"], tb.Balanced)
	}

	// The floor: T4 eats on with one dish voided; a takeaway is cancelled.
	t4, takeaway := ids.New(), ids.New()
	paneer, naan, lassi := newLine(k.Paneer, "Paneer tikka", 1), newLine(k.Naan, "Butter naan", 2), newLine(k.Lassi, "Sweet lassi", 1)
	till.apply(t,
		till.op("order.opened", opened(t4, k.T4, 2)),
		till.op("order.lines_added", added(t4, paneer, naan)),
		till.op("order.line_voided", map[string]any{"order_id": t4, "line_id": naan.ID, "reason": "guest changed mind", "at": dinner}),
		till.op("order.opened", map[string]any{"order_id": takeaway, "channel": "takeaway", "opened_at": dinner}),
		till.op("order.lines_added", added(takeaway, lassi)),
		till.op("order.cancelled", map[string]any{"order_id": takeaway, "reason": "guest left", "at": dinner}),
	)

	v := till.day(t, closeDay)
	r := v.Report
	if v.Closed != nil || r.Sales.Bills != 3 || r.Sales.GrandTotal != g1+g2+g3 {
		t.Fatalf("sales: %d bills, %d total (closed %v)", r.Sales.Bills, r.Sales.GrandTotal, v.Closed != nil)
	}
	pay := map[string]int64{}
	for _, p := range r.Payments {
		pay[p.Method] = p.Amount
	}
	if pay["cash"] != g1+g3 || pay["upi"] != g2 {
		t.Fatalf("payments: %v", pay)
	}
	if len(r.Shifts) != 2 || *r.Shifts[0].Difference != -5_000 || r.Shifts[1].ClosedAt != nil ||
		r.Shifts[1].Expected != 100_000+g3 {
		t.Fatalf("shifts: %+v", r.Shifts)
	}
	if c := r.Cash; c.Expenses != 30_000 || c.ToSafe != 100_000 || c.Counted != counted || c.OpenShifts != 1 || c.OutsideShifts != 0 {
		t.Fatalf("cash: %+v", c)
	}
	if r.Voids.Lines != 2 || r.Voids.Value != 2*6000+9000 || r.Cancelled.Orders != 1 || r.Cancelled.Value != 9000 {
		t.Fatalf("voids %+v, cancelled %+v", r.Voids, r.Cancelled)
	}
	if r.OpenOrders.Orders != 1 || r.OpenOrders.Value != 32000 || len(r.OpenOrders.Tables) != 1 || r.OpenOrders.Tables[0] != "T4" {
		t.Fatalf("open orders: %+v", r.OpenOrders)
	}
	if len(r.Series) != 1 || r.Series[0].Count != 3 || r.Series[0].MissingCount != 0 || r.Series[0].DeviceName != "Counter 1" {
		t.Fatalf("series: %+v", r.Series)
	}
	w := warningCodes(r)
	if w["open_shift"] != "check" || w["open_orders"] != "check" || w["cash_difference"] != "info" || len(w) != 3 {
		t.Fatalf("warnings: %+v", r.Warnings)
	}
	if again := till.day(t, closeDay); again.Report.Digest != r.Digest {
		t.Fatal("the digest must not change while nothing changes")
	}

	// Closing: the digest you saw, and the warnings acknowledged.
	if status, _ := call(t, "POST", "/v1/outlets/"+till.OutletID+"/days/"+closeDay+"/close", till.OwnerToken,
		map[string]any{"acknowledge_warnings": true}, nil); status != http.StatusBadRequest {
		t.Fatalf("closing without a digest: %d", status)
	}
	if _, code := till.closeDay(t, http.StatusConflict, closeDay, "0000", true); code != "report_changed" {
		t.Fatalf("stale digest: %s", code)
	}
	if _, code := till.closeDay(t, http.StatusConflict, closeDay, r.Digest, false); code != "warnings_not_acknowledged" {
		t.Fatalf("unacknowledged warnings: %s", code)
	}
	closed, _ := till.closeDay(t, http.StatusCreated, closeDay, r.Digest, true)
	if closed.Closed == nil || closed.Closed.ClosedBy != "Ravi" || closed.Amended || closed.Closed.Report.Digest != r.Digest {
		t.Fatalf("closed: %+v", closed.Closed)
	}
	if _, code := till.closeDay(t, http.StatusConflict, closeDay, r.Digest, true); code != "day_already_closed" {
		t.Fatalf("closing twice: %s", code)
	}

	// A bill that synced late: kept, flagged, and the day shows as amended.
	g4 := total(t, pavBhaji(3))
	till.apply(t, till.bill(t, pavBhaji(3), billOpts{issuedAt: at(22, 0)}))
	if n := till.alerts(t)["bill_after_day_close"]; n != 1 {
		t.Fatalf("a late bill raises bill_after_day_close, got %v", till.alerts(t))
	}
	v = till.day(t, closeDay)
	if !v.Amended || v.Closed.Report.Sales.Bills != 3 || v.Report.Sales.Bills != 4 {
		t.Fatalf("amended %v: closed copy %d bills, live %d", v.Amended, v.Closed.Report.Sales.Bills, v.Report.Sales.Bills)
	}
	got := map[string][2]int64{}
	for _, a := range v.Amendments {
		got[a.What] = [2]int64{a.Before, a.After}
	}
	if got["bills"] != [2]int64{3, 4} || got["sales"] != [2]int64{g1 + g2 + g3, g1 + g2 + g3 + g4} {
		t.Fatalf("amendments: %+v", v.Amendments)
	}
	if _, ok := got["closed_shifts"]; ok {
		t.Fatalf("the evening shift's running cash is not an amendment: %+v", v.Amendments)
	}

	var list dayclose.DayList
	must(t, http.StatusOK, "GET", "/v1/outlets/"+till.OutletID+"/days", till.OwnerToken, nil, &list)
	if len(list.Closed) != 1 || list.Closed[0].BusinessDate != closeDay || list.Closed[0].Bills != 3 ||
		list.Closed[0].CashDifference != -5_000 || list.Today != dayclose.TradingDay(time.Now()).Format(time.DateOnly) {
		t.Fatalf("day list: %+v", list)
	}
}

// TestInvoiceGaps: a number skipped inside a day, and one skipped across
// midnight, are listed until the bill turns up.
func TestInvoiceGaps(t *testing.T) {
	till := newRestaurant(t, "Gap Cafe", "", "29")
	day1 := time.Date(2026, 9, 29, 12, 0, 0, 0, gst.IST)
	day2 := day1.AddDate(0, 0, 1)
	no := func(n int) string { return gst.InvoiceNo(till.Prefix, gst.FY(day1), n) }

	till.apply(t,
		till.bill(t, pavBhaji(1), billOpts{issuedAt: day1, invoiceNo: no(1)}),
		till.bill(t, pavBhaji(1), billOpts{issuedAt: day1.Add(time.Hour), invoiceNo: no(2)}),
		till.bill(t, pavBhaji(1), billOpts{issuedAt: day1.Add(3 * time.Hour), invoiceNo: no(4)}),
		till.bill(t, pavBhaji(1), billOpts{issuedAt: day2, invoiceNo: no(6)}),
	)
	s := till.day(t, "2026-09-29").Report.Series
	if len(s) != 1 || s[0].MissingCount != 1 || s[0].Missing[0] != no(3) {
		t.Fatalf("day 1: %+v", s)
	}
	r := till.day(t, "2026-09-30").Report
	if s := r.Series; len(s) != 1 || s[0].ContinuesFrom != no(4) || s[0].MissingCount != 1 || s[0].Missing[0] != no(5) {
		t.Fatalf("day 2 carries on from day 1: %+v", s)
	}
	if warningCodes(r)["missing_invoices"] != "check" {
		t.Fatalf("missing numbers must be checked before closing: %+v", r.Warnings)
	}

	// Bill 3 syncs; bill 5 was filed under day 1 (a night bill). Both clear.
	till.apply(t,
		till.bill(t, pavBhaji(1), billOpts{issuedAt: day1.Add(2 * time.Hour), invoiceNo: no(3)}),
		till.bill(t, pavBhaji(1), billOpts{issuedAt: day2.Add(-12*time.Hour + 30*time.Minute), businessDate: "2026-09-29", invoiceNo: no(5)}),
	)
	for _, d := range []string{"2026-09-29", "2026-09-30"} {
		if s := till.day(t, d).Report.Series; s[0].MissingCount != 0 {
			t.Fatalf("%s after the late bills: %+v", d, s)
		}
	}

	// A new till whose first bill of the year is number 3: 1 and 2 are missing.
	second := till.pair(t, "pos", "Counter 2")
	second.apply(t, second.bill(t, pavBhaji(1), billOpts{issuedAt: day1, invoiceNo: gst.InvoiceNo(second.Prefix, gst.FY(day1), 3)}))
	for _, s := range till.day(t, "2026-09-29").Report.Series {
		if s.DeviceName == "Counter 2" && s.MissingCount != 2 {
			t.Fatalf("a series starts at 1: %+v", s)
		}
	}
}

// TestShiftRules: what a till may and may not send about its drawer.
func TestShiftRules(t *testing.T) {
	till := newRestaurant(t, "Shift Rules", "", "29")
	other := till.pair(t, "pos", "Counter 2")
	captain := till.pair(t, "captain", "Captain Anil")
	s := ids.New()

	reject := func(f *fixture, op syncgw.Op, code string) {
		t.Helper()
		ack := f.push(t, op).Acks[0]
		if ack.Status != "rejected" || ack.Error == nil || ack.Error.Code != code {
			t.Fatalf("%s: want rejected %s, got %+v", op.Type, code, ack)
		}
	}
	reject(captain, captain.openShift(ids.New(), at(11, 0), "Anil", 0), "role_not_allowed")
	reject(till, till.op("shift.opened", map[string]any{
		"shift_id": ids.New(), "business_date": "2026-09-26", "opened_at": at(11, 0), "opened_by": "Asha",
	}), "bad_business_date")
	reject(till, till.openShift(uuid.New(), at(11, 0), "Asha", 0), "bad_payload") // not a UUIDv7
	reject(till, till.moveCash(ids.New(), "expense", 100, "Ice", at(11, 5)), "unknown_shift")

	till.apply(t, till.openShift(s, at(11, 0), "Asha", 50_000))
	if ack := till.push(t, till.openShift(s, at(11, 0), "Asha", 50_000)).Acks[0]; ack.Status != "duplicate" {
		t.Fatalf("the same shift sent again is a duplicate: %+v", ack)
	}
	reject(till, till.openShift(ids.New(), at(12, 0), "Asha", 0), "shift_already_open")
	reject(other, other.moveCash(s, "expense", 100, "Ice", at(11, 5)), "wrong_device")
	reject(till, till.moveCash(s, "bonus", 100, "Ice", at(11, 5)), "bad_payload")
	reject(till, till.moveCash(s, "expense", 100, "  ", at(11, 5)), "bad_payload")
	reject(till, till.closeShift(s, at(16, 0), 50_000, map[string]int64{"500": 2}), "denominations_mismatch")
	reject(till, till.closeShift(s, at(16, 0), 50_000, map[string]int64{"1000": 5}), "bad_payload")
	reject(till, till.closeShift(s, at(10, 0), 50_000, nil), "bad_payload") // before it opened

	till.apply(t, till.closeShift(s, at(16, 0), 50_000, map[string]int64{"500": 1}))
	if ack := till.push(t, till.closeShift(s, at(16, 0), 50_000, nil)).Acks[0]; ack.Status != "duplicate" {
		t.Fatalf("the same close sent again is a duplicate: %+v", ack)
	}
	reject(till, till.closeShift(s, at(16, 0), 40_000, nil), "shift_already_closed")
	reject(till, till.moveCash(s, "expense", 100, "Ice", at(16, 30)), "shift_closed")
	reject(till, till.openShift(ids.New(), at(15, 0), "Vikram", 0), "shift_overlaps")
	if n := till.alerts(t)["cash_difference"]; n != 0 {
		t.Fatalf("an exact drawer raises no alert, got %v", till.alerts(t))
	}

	// Late arrivals for a closed day are kept and flagged.
	v := till.day(t, closeDay)
	till.closeDay(t, http.StatusCreated, closeDay, v.Report.Digest, false)
	till.apply(t, till.openShift(ids.New(), at(18, 0), "Vikram", 0))
	if n := till.alerts(t)["shift_after_day_close"]; n != 1 {
		t.Fatalf("a shift for a closed day is flagged, got %v", till.alerts(t))
	}
}

// TestLateCashCorrectsTheDrawer: a cash bill that reaches the server after its
// shift was counted moves the shift's expected cash and difference, and the
// books follow: the drawer that looked ₹g over is exact after all.
func TestLateCashCorrectsTheDrawer(t *testing.T) {
	till := newRestaurant(t, "Late Cash", "", "29")
	s := ids.New()
	g := total(t, pavBhaji(1))
	open := till.openShift(s, at(11, 0), "Asha", 50_000)
	late := till.bill(t, pavBhaji(1), billOpts{issuedAt: at(12, 0)})
	shut := till.closeShift(s, at(16, 0), 50_000+g, nil) // the bill's cash is in the drawer
	till.apply(t, open, shut)
	if bal := reviewBalances(t, till); bal["5900"] != -g {
		t.Fatalf("before the bill: over/short %d, want %d (looks over)", bal["5900"], -g)
	}
	till.apply(t, late)

	var cashSales, expected, diff int64
	if err := ownerDB.QueryRow(`SELECT cash_sales, expected_cash, difference FROM till_shift WHERE id = $1`, s).
		Scan(&cashSales, &expected, &diff); err != nil {
		t.Fatal(err)
	}
	if cashSales != g || expected != 50_000+g || diff != 0 {
		t.Fatalf("after the bill: cash sales %d, expected %d, difference %d", cashSales, expected, diff)
	}
	if bal := reviewBalances(t, till); bal["5900"] != 0 {
		t.Fatalf("after the bill the books show no over/short: %d", bal["5900"])
	}
	if n := till.alerts(t)["cash_after_shift_close"]; n != 1 {
		t.Fatalf("the correction is flagged once: %v", till.alerts(t))
	}
	// The same bill sent again changes nothing.
	if ack := till.push(t, late).Acks[0]; ack.Status != "duplicate" {
		t.Fatalf("resend: %+v", ack)
	}
	if bal := reviewBalances(t, till); bal["5900"] != 0 {
		t.Fatalf("a resent bill must not adjust twice: %d", bal["5900"])
	}
}

// TestCashOutsideShifts: an outlet that runs shifts is told about cash taken
// while no shift was open on that till.
func TestCashOutsideShifts(t *testing.T) {
	till := newRestaurant(t, "Outside Shift", "", "29")
	s := ids.New()
	g := total(t, pavBhaji(1))
	till.apply(t,
		till.bill(t, pavBhaji(1), billOpts{issuedAt: at(10, 0)}), // before the shift
		till.openShift(s, at(11, 0), "Asha", 0),
		till.bill(t, pavBhaji(1), billOpts{issuedAt: at(12, 0)}),
		till.closeShift(s, at(16, 0), g, nil),
	)
	r := till.day(t, closeDay).Report
	if r.Cash.OutsideShifts != g || r.Shifts[0].Difference == nil || *r.Shifts[0].Difference != 0 {
		t.Fatalf("cash outside shifts %d (want %d), shifts %+v", r.Cash.OutsideShifts, g, r.Shifts)
	}
	if warningCodes(r)["cash_outside_shifts"] != "info" {
		t.Fatalf("warnings: %+v", r.Warnings)
	}
}

// TestDayCloseGuards: other tenants, future days, bad dates.
func TestDayCloseGuards(t *testing.T) {
	mine := newRestaurant(t, "Guard Mine", "", "29")
	theirs := newRestaurant(t, "Guard Theirs", "", "29")
	path := "/v1/outlets/" + mine.OutletID + "/days"
	for _, c := range []struct{ method, path string }{
		{"GET", path},
		{"GET", path + "/" + closeDay},
		{"POST", path + "/" + closeDay + "/close"},
	} {
		if status, _ := call(t, c.method, c.path, theirs.OwnerToken, map[string]any{"digest": "x"}, nil); status != http.StatusNotFound {
			t.Fatalf("%s %s by another tenant: %d", c.method, c.path, status)
		}
	}
	if status, _ := call(t, "GET", path+"/0000-01-01", mine.OwnerToken, nil, nil); status != http.StatusBadRequest {
		t.Fatalf("year 0: %d", status)
	}
	if status, _ := call(t, "GET", path+"/not-a-day", mine.OwnerToken, nil, nil); status != http.StatusBadRequest {
		t.Fatalf("bad date: %d", status)
	}
	tomorrow := dayclose.TradingDay(time.Now()).AddDate(0, 0, 1).Format(time.DateOnly)
	v := mine.day(t, tomorrow)
	if _, code := mine.closeDay(t, http.StatusBadRequest, tomorrow, v.Report.Digest, true); code != "day_not_started" {
		t.Fatalf("closing a day that has not started: %s", code)
	}
	if status, _ := call(t, "GET", path+"/"+closeDay, mine.DeviceToken, nil, nil); status != http.StatusUnauthorized && status != http.StatusForbidden {
		t.Fatalf("a device token cannot read the Z report: %d", status)
	}
}

// TestPullCursorExpired: closing a day prunes week-old changes, and a device
// whose cursor is below the pruned part must bootstrap again.
func TestPullCursorExpired(t *testing.T) {
	till := newRestaurant(t, "Prune Cafe", "", "29")
	k := setupFloor(t, till)
	// Two changes before the cursor, so a cursor below the floor is not 0
	// (which means "bootstrap").
	a, b := ids.New(), ids.New()
	till.apply(t,
		till.op("order.opened", map[string]any{"order_id": ids.New(), "channel": "takeaway", "opened_at": dinner}),
		till.op("order.opened", opened(a, k.T4, 2)))
	first := till.pull(t, 0, 0).Cursor
	till.apply(t, till.op("order.opened", opened(b, k.T5, 2)))
	last := till.pull(t, first, 0).Cursor

	// Age the first change past a week, then close a day.
	if _, err := ownerDB.Exec(`UPDATE change_log SET created_at = now() - interval '8 days' WHERE tenant_id = $1 AND seq <= $2`,
		till.TenantID, first); err != nil {
		t.Fatal(err)
	}
	v := till.day(t, "2026-09-01")
	till.closeDay(t, http.StatusCreated, "2026-09-01", v.Report.Digest, false)

	var floor int64
	if err := ownerDB.QueryRow(`SELECT change_log_floor FROM sync_state WHERE tenant_id = $1`, till.TenantID).Scan(&floor); err != nil {
		t.Fatal(err)
	}
	if floor != first {
		t.Fatalf("floor %d, want %d", floor, first)
	}
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if status, _ := call(t, "GET", "/v1/sync/pull?after="+strconv.FormatInt(first-1, 10), till.DeviceToken, nil, &e); status != http.StatusGone || e.Error.Code != "cursor_expired" {
		t.Fatalf("a cursor below the floor: %d %s", status, e.Error.Code)
	}
	if res := till.pull(t, first, 0); len(res.Orders) != 1 || res.Cursor != last {
		t.Fatalf("a cursor at the floor still catches up: %+v", res)
	}
	if res := till.pull(t, 0, 0); len(res.Orders) != 3 {
		t.Fatalf("bootstrap still sends every open order: %d", len(res.Orders))
	}
}
