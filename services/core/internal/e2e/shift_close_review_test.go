package e2e

// Adversarial review of "close a till's shift from the back office". Every
// test here names a defect and fails against the code as reviewed; it should
// pass once the defect is fixed (whichever reasonable fix is chosen).

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"1xn/core/internal/platform/gst"
	"1xn/core/internal/platform/ids"
	"1xn/core/modules/dayclose"
)

// rcPost sends a JSON POST as the owner without touching t, so it can run in
// a goroutine.
func rcPost(path, token string, body any) (int, []byte) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", api.URL+path, bytes.NewReader(b))
	if err != nil {
		return 0, []byte(err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, []byte(err.Error())
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, raw
}

// rcWaitForSyncStateWaiters waits until n requests are blocked on the
// tenant's sync_state row lock and returns their backend pids and what each
// waits on ("transactionid": first in the queue, holding the tuple lock;
// "tuple": queued behind it).
func rcWaitForSyncStateWaiters(t *testing.T, n int) map[int]string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := ownerDB.Query(`
			SELECT pid, wait_event FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
			  AND query LIKE '%sync_state%'`)
		if err != nil {
			t.Fatal(err)
		}
		out := map[int]string{}
		for rows.Next() {
			var pid int
			var ev string
			if err := rows.Scan(&pid, &ev); err != nil {
				t.Fatal(err)
			}
			out[pid] = ev
		}
		rows.Close()
		if len(out) >= n {
			return out
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d requests to queue on sync_state", n)
	return nil
}

// ---------------------------------------------------------------------------
// C1. A cash move the till made BEFORE the owner's count, but that syncs
// after it, is refused (shift_closed). This is the normal case the feature is
// for: the till was offline, the cashier left, the owner counted. The drawer
// the owner counted was already missing the cash the till moved, so the shift
// stays short by exactly the move, 5900 Cash over and short books a loss that
// never happened, and the move itself (here: ₹1,000 into the safe) is never
// booked, so 1105 Cash in safe is ₹1,000 too low for ever. Late bills are
// reconciled (LateCash); late cash moves are not.
// ---------------------------------------------------------------------------
//
// Fixed by design: only a revoked till's shift can be closed in the back
// office. A till that still works closes its own shift, so its late ops
// (here a safe drop) arrive in order and nothing has to be fitted around a
// second count.
func TestReviewC1CashMoveBeforeOwnerCountIsRefused(t *testing.T) {
	till := newRestaurant(t, "Review Late Drop", "", "29")
	shift := ids.New()
	b := pavBhaji(2)
	cash := total(t, b)
	till.apply(t, till.openShift(shift, at(18, 0), "Vikram", 100_000), till.bill(t, b, billOpts{issuedAt: at(19, 0)}))

	// The till goes offline. At 20:00 the cashier takes ₹1,000 to the safe; the
	// op waits in the till's queue. The cashier then leaves without closing.
	drop := till.moveCash(shift, "to_safe", 100_000, "Evening drop", at(20, 0))

	// The owner cannot count it here while the till could still send ops.
	inDrawer := 100_000 + cash - 100_000
	status, _, code := till.closeShiftHere(t, shift, map[string]any{
		"id": ids.New(), "counted_paise": inDrawer, "note": "Till offline, cashier left; counted by Ravi"})
	if status != http.StatusConflict || code != "till_active" {
		t.Fatalf("closing a working till's shift here: %d %s, want 409 till_active", status, code)
	}

	// The till comes back, sends the drop, and the cashier closes on the till.
	till.apply(t, drop, till.closeShift(shift, at(23, 0), inDrawer, nil))
	var diff int64
	if err := ownerDB.QueryRow(`SELECT difference FROM till_shift WHERE id = $1`, shift).Scan(&diff); err != nil {
		t.Fatal(err)
	}
	bal := reviewBalances(t, till)
	if diff != 0 || bal["5900"] != 0 || bal["1105"] != 100_000 {
		t.Fatalf("shift difference %d (want 0); 5900 Cash over and short %d (want 0); 1105 Cash in safe %d (want 100000)",
			diff, bal["5900"], bal["1105"])
	}
}

// ---------------------------------------------------------------------------
// C3. The back-office close counts the till's bills up to the SERVER's clock
// (closed_at = server now), but bills carry the TILL's clock. A till whose
// clock runs a few minutes fast stamps its latest bills "in the future": the
// server already has them when the owner closes, yet countShift(until=now)
// leaves them out. The drawer the owner counted holds that cash, so the shift
// shows a false overage, posted to 5900, and nothing ever corrects it:
// LateCash only runs for bills that arrive later, and only up to closed_at.
// The bill's cash ends up in no drawer. (till_clock_ahead only catches a
// shift that OPENS after now, not bills after now.)
// ---------------------------------------------------------------------------
func TestReviewC3FastTillClockLeavesSyncedBillOutOfOwnerCount(t *testing.T) {
	till := newRestaurant(t, "Review Fast Clock", "", "29")
	now := time.Now().In(gst.IST)
	// The till's clock runs 10 minutes fast. It opened its shift two hours ago
	// and sold a cash bill five minutes ago; both are stamped 10 minutes late.
	shift := ids.New()
	b := pavBhaji(1)
	cash := total(t, b)
	till.apply(t,
		till.openShift(shift, now.Add(-2*time.Hour+10*time.Minute), "Vikram", 100_000),
		till.bill(t, b, billOpts{issuedAt: now.Add(5 * time.Minute)}),
	)

	// The bill is on the server. The till breaks; the owner revokes it,
	// counts float + that bill's cash and closes the shift here.
	must(t, http.StatusOK, "POST", "/v1/devices/"+till.DeviceID+"/revoke", till.OwnerToken, nil, nil)
	status, s, code := till.closeShiftHere(t, shift, map[string]any{
		"id": ids.New(), "counted_paise": 100_000 + cash, "note": "Till screen broke; counted by Ravi"})
	if status != http.StatusCreated {
		t.Fatalf("close: %d %s", status, code)
	}
	if s.Expected != 100_000+cash || s.Difference == nil || *s.Difference != 0 {
		t.Fatalf("a cash bill already on the server, from this till, during this shift, is in the owner's count: "+
			"expected %d (want %d), difference %v (want 0); the shift is booked %v over and the bill is in no drawer",
			s.Expected, 100_000+cash, deref(s.Difference), deref(s.Difference))
	}
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// ---------------------------------------------------------------------------
// C4. The back-office close only LOCKS sync_state; it never updates it. The
// day close relies on every writer updating that row: it takes its
// REPEATABLE READ snapshot first, then locks sync_state, and a writer that
// committed in between makes the lock fail with a serialization error (so
// the close is retried and sees the change). A row that was only locked does
// not. So a day close that queued behind a back-office shift close commits
// with its old snapshot: the stored Z report says the drawer is still open
// although it was counted and booked before the day was closed, the digest
// check passes, and nothing is flagged (the till path would raise
// shift_after_day_close). Same defect class as review F4 for bills.
// ---------------------------------------------------------------------------
func TestReviewC4DayCloseQueuedBehindShiftCloseStoresStaleReport(t *testing.T) {
	till := newRestaurant(t, "Review Shift Day Race", "", "29")
	shift := ids.New()
	till.apply(t, till.openShift(shift, at(18, 0), "Vikram", 100_000))
	must(t, http.StatusOK, "POST", "/v1/devices/"+till.DeviceID+"/revoke", till.OwnerToken, nil, nil)
	v := till.day(t, closeDay) // the owner looks at the day: one shift still open
	if warningCodes(v.Report)["open_shift"] == "" {
		t.Fatalf("setup: the day warns of the open shift: %+v", v.Report.Warnings)
	}

	// Hold the tenant's sync_state row so both requests queue in a known
	// order: the shift close first, then the day close.
	hold, err := ownerDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback()
	if _, err := hold.Exec(`SELECT 1 FROM sync_state WHERE tenant_id = $1 FOR UPDATE`, till.TenantID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		status int
		body   []byte
	}
	shiftDone, dayDone := make(chan result, 1), make(chan result, 1)
	go func() {
		s, b := rcPost("/v1/outlets/"+till.OutletID+"/shifts/"+shift.String()+"/close", till.OwnerToken,
			map[string]any{"id": ids.New(), "counted_paise": 95_000, "note": "Cashier left; counted by Ravi"})
		shiftDone <- result{s, b}
	}()
	var shiftPID int
	for pid := range rcWaitForSyncStateWaiters(t, 1) {
		shiftPID = pid
	}
	go func() {
		s, b := rcPost("/v1/outlets/"+till.OutletID+"/days/"+closeDay+"/close", till.OwnerToken,
			map[string]any{"digest": v.Report.Digest, "acknowledge_warnings": true})
		dayDone <- result{s, b}
	}()
	// The shift close is first in the queue (it holds the tuple lock and
	// waits for us); the day close, which already has its snapshot, is
	// queued behind it and can only lock the row after the shift close
	// commits.
	waiters := rcWaitForSyncStateWaiters(t, 2)
	for pid, ev := range waiters {
		if (pid == shiftPID) != (ev == "transactionid") {
			t.Fatalf("setup: queue order is not shift close then day close: %v (shift close pid %d)", waiters, shiftPID)
		}
	}
	if err := hold.Commit(); err != nil {
		t.Fatal(err)
	}
	sr, dr := <-shiftDone, <-dayDone

	if sr.status != http.StatusCreated {
		t.Fatalf("shift close: %d %s", sr.status, sr.body)
	}
	if dr.status == http.StatusConflict && bytes.Contains(dr.body, []byte("report_changed")) {
		return // serialised: the owner is asked to look at the day again
	}
	if dr.status != http.StatusCreated {
		t.Fatalf("day close: %d %s", dr.status, dr.body)
	}
	var view dayclose.DayView
	if err := json.Unmarshal(dr.body, &view); err != nil {
		t.Fatal(err)
	}
	inSnapshot := view.Closed != nil && len(view.Closed.Report.Shifts) == 1 && view.Closed.Report.Shifts[0].ClosedAt != nil
	flagged := till.alerts(t)["shift_after_day_close"] > 0
	if !inSnapshot && !flagged {
		t.Fatalf("a drawer counted (and ₹50 short booked) before the day close committed is in the stored Z report: %v; "+
			"flagged shift_after_day_close: %v (want one of them, or the close refused with report_changed)", inSnapshot, flagged)
	}
}

// ---------------------------------------------------------------------------
// C7. Closing in the back office a shift whose trading day is already closed
// changes that closed day (its drawer count, its cash difference, the books)
// but raises no shift_after_day_close, while the same close sent by the till
// does (flagIfDayClosed). docs/learn/07-day-close.md: late changes to a
// closed day are "kept, flagged, amended".
// ---------------------------------------------------------------------------
func TestReviewC7BackOfficeCloseOfClosedDayIsNotFlagged(t *testing.T) {
	till := newRestaurant(t, "Review Closed Day Drawer", "", "29")
	shift := ids.New()
	till.apply(t, till.openShift(shift, at(18, 0), "Vikram", 100_000))
	v := till.day(t, closeDay)
	till.closeDay(t, http.StatusCreated, closeDay, v.Report.Digest, true) // closed with the drawer still open
	must(t, http.StatusOK, "POST", "/v1/devices/"+till.DeviceID+"/revoke", till.OwnerToken, nil, nil)

	status, _, code := till.closeShiftHere(t, shift, map[string]any{
		"id": ids.New(), "counted_paise": 90_000, "note": "Till revoked; counted next morning"})
	if status != http.StatusCreated {
		t.Fatalf("close: %d %s", status, code)
	}
	if n := till.alerts(t)["shift_after_day_close"]; n != 1 {
		t.Fatalf("a drawer of a closed day counted afterwards (₹100 short booked into it) is flagged shift_after_day_close: got %d (alerts %v)",
			n, till.alerts(t))
	}
}

// ---------------------------------------------------------------------------
// C5. When the month the drawer difference is booked in is locked, the
// back-office close refuses (409 period_locked, "unlock it"), so the shift
// can never be closed: the app cannot re-open a locked period (period_guard),
// and the entry date is "now", so retrying does not help until the month
// ends. Every other drawer path (bills, cash moves, a till's own close,
// LateCash) keeps the record and marks it ledger_status 'blocked' with a
// period_locked alert, which the Z report counts in ledger_blocked.
// ---------------------------------------------------------------------------
func TestReviewC5LockedMonthMakesShiftUncloseable(t *testing.T) {
	till := newRestaurant(t, "Review Locked Month Drawer", "", "29")
	shift := ids.New()
	till.apply(t, till.openShift(shift, at(18, 0), "Vikram", 100_000))
	now := time.Now().In(gst.IST)
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	if _, err := ownerDB.Exec(`INSERT INTO period (id, tenant_id, legal_entity_id, starts_on, ends_on, status)
		VALUES ($1, $2, $3, $4, $5, 'locked')`, ids.New(), till.TenantID, till.LegalEntityID,
		start.Format(time.DateOnly), start.AddDate(0, 1, 0).Format(time.DateOnly)); err != nil {
		t.Fatal(err)
	}
	must(t, http.StatusOK, "POST", "/v1/devices/"+till.DeviceID+"/revoke", till.OwnerToken, nil, nil)
	status, s, code := till.closeShiftHere(t, shift, map[string]any{
		"id": ids.New(), "counted_paise": 95_000, "note": "Till revoked; counted by Ravi"})
	if status != http.StatusCreated || s.LedgerStatus != "blocked" || till.alerts(t)["period_locked"] != 1 {
		t.Fatalf("a drawer counted ₹50 short while the month is locked is closed and kept out of the books like a till close "+
			"(201, ledger_status blocked, alert period_locked): got %d %s ledger_status %q alerts %v",
			status, code, s.LedgerStatus, till.alerts(t))
	}
}

// ---------------------------------------------------------------------------
// C2. "The first count stands" is implemented as "the first count to REACH
// THE SERVER stands", not the first count made. The case the feature exists
// for: the till is offline but working. The cashier counts shift A on the
// till at 22:00 (exact) and hands over; the next cashier opens shift B with a
// ₹500 float and sells. The owner sees A "still open" in the back office,
// counts the drawer (which now holds B's float and sales, also exact) and
// closes A here. When the till syncs: its real count of A is discarded
// (shift_closed_twice), B is refused as overlapping (critical), and B's cash
// bill is charged to A by LateCash. Every count was exact, yet 5900 Cash over
// and short books a ₹700 loss (A's float and takings less B's float). (The existing test
// TestTillCloseAfterBackOfficeClose encodes the discarded earlier count and
// the refused shift as intended; that expectation is what is wrong.)
// ---------------------------------------------------------------------------
//
// Fixed by design, as C1: a working till's shift cannot be closed here, so
// there is only ever one count of a shift.
func TestReviewC2EarlierTillCountLosesToLaterOwnerCount(t *testing.T) {
	till := newRestaurant(t, "Review Two Counts", "", "29")
	a, b := ids.New(), ids.New()
	bill1, bill2 := pavBhaji(2), pavBhaji(1)
	cash1, cash2 := total(t, bill1), total(t, bill2)
	till.apply(t, till.openShift(a, at(18, 0), "Vikram", 100_000), till.bill(t, bill1, billOpts{issuedAt: at(19, 0)}))

	// Offline from 19:30. Queued on the till, in order:
	closeA := till.closeShift(a, at(22, 0), 100_000+cash1, nil) // exact count of A
	openB := till.openShift(b, at(22, 15), "Asha", 50_000)
	sale := till.bill(t, bill2, billOpts{issuedAt: at(22, 30)})

	status, _, code := till.closeShiftHere(t, a, map[string]any{
		"id": ids.New(), "counted_paise": 50_000 + cash2, "note": "Till not syncing; counted by Ravi"})
	if status != http.StatusConflict || code != "till_active" {
		t.Fatalf("closing a working till's shift here: %d %s, want 409 till_active", status, code)
	}

	till.apply(t, closeA, openB, sale)
	if bal := reviewBalances(t, till); bal["5900"] != 0 {
		t.Fatalf("both drawers were counted exactly, yet 5900 Cash over and short is %d (want 0)", bal["5900"])
	}
}
