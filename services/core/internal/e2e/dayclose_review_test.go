package e2e

// Adversarial review of the day-close slice. Every test here names a defect
// and fails against the code as reviewed; it should pass once the defect is
// fixed (whichever reasonable fix is chosen).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"1xn/core/internal/platform/gst"
	"1xn/core/internal/platform/ids"
	"1xn/core/modules/dayclose"
)

// reviewBalances is the trial balance by account code.
func reviewBalances(t *testing.T, f *fixture) map[string]int64 {
	t.Helper()
	var tb struct {
		Accounts []struct {
			Code    string `json:"code"`
			Balance int64  `json:"balance"`
		} `json:"accounts"`
	}
	must(t, http.StatusOK, "GET", "/v1/ledger/trial-balance", f.OwnerToken, nil, &tb)
	out := map[string]int64{}
	for _, a := range tb.Accounts {
		out[a.Code] = a.Balance
	}
	return out
}

// reviewClose posts a close without failing the test, so the caller can
// judge the outcome.
func reviewClose(t *testing.T, f *fixture, date, digest string) (int, string) {
	t.Helper()
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	status, _ := call(t, "POST", "/v1/outlets/"+f.OutletID+"/days/"+date+"/close", f.OwnerToken,
		map[string]any{"digest": digest, "acknowledge_warnings": true}, &e)
	return status, e.Error.Code
}

// ---------------------------------------------------------------------------
// F1. Cash-move and shift-difference journal entries take their date from
// the device timestamp's own UTC offset, not from India's calendar (bills
// convert to IST first). A till that sends UTC times (JavaScript's
// toISOString does) books a 01:00 IST move on 1 Oct into 30 Sep: the wrong
// day, the wrong month, and refused when September is locked.
// ---------------------------------------------------------------------------
func TestReviewF1CashEntryDateDependsOnDeviceUTCOffset(t *testing.T) {
	ist := newRestaurant(t, "Review IST Till", "", "29")
	utc := ist.pair(t, "pos", "Counter Z") // same restaurant, a till that sends UTC
	// The accountant has locked September; October is open.
	if _, err := ownerDB.Exec(`INSERT INTO period (id, tenant_id, legal_entity_id, starts_on, ends_on, status)
		VALUES ($1, $2, $3, '2026-09-01', '2026-10-01', 'locked')`, ids.New(), ist.TenantID, ist.LegalEntityID); err != nil {
		t.Fatal(err)
	}
	opened := time.Date(2026, 9, 30, 22, 0, 0, 0, gst.IST) // night shift of 30 Sep
	moveAt := time.Date(2026, 10, 1, 1, 0, 0, 0, gst.IST)  // 01:00 on 1 Oct in India
	closeAt := time.Date(2026, 10, 1, 1, 30, 0, 0, gst.IST)

	type res struct{ moveStatus, moveDate, shiftStatus, shiftDate string }
	run := func(f *fixture, loc *time.Location) res {
		s, move := ids.New(), ids.New()
		f.apply(t,
			f.openShift(s, opened, "Asha", 100_000),
			f.op("cash.moved", map[string]any{"move_id": move, "shift_id": s, "kind": "expense",
				"amount_paise": 20_000, "reason": "Ice", "at": moveAt.In(loc)}),
			f.closeShift(s, closeAt.In(loc), 70_000, nil), // expected 80_000: Rs 100 short
		)
		var r res
		if err := ownerDB.QueryRow(`
			SELECT m.ledger_status, coalesce(e.entry_date::text, '-')
			FROM cash_move m LEFT JOIN journal_entry e ON e.id = m.journal_entry_id WHERE m.id = $1`, move).
			Scan(&r.moveStatus, &r.moveDate); err != nil {
			t.Fatal(err)
		}
		if err := ownerDB.QueryRow(`
			SELECT s.ledger_status, coalesce(e.entry_date::text, '-')
			FROM till_shift s LEFT JOIN journal_entry e ON e.id = s.journal_entry_id WHERE s.id = $1`, s).
			Scan(&r.shiftStatus, &r.shiftDate); err != nil {
			t.Fatal(err)
		}
		return r
	}
	a := run(ist, gst.IST)  // "2026-10-01T01:00:00+05:30"
	b := run(utc, time.UTC) // "2026-09-30T19:30:00Z": the same instant
	if a != b {
		t.Fatalf("the same instant must book the same way whatever offset the till used:\n  +05:30 till: %+v\n  UTC till:    %+v", a, b)
	}
}

// ---------------------------------------------------------------------------
// F2. A void can fall out of every Z report. Orders are picked by their own
// open/close window (opened_at < day end), then voids by the void's time. A
// void that does not sit inside its order's window is counted on no day:
// (a) the order is merged into an order opened after the void's trading day;
// (b) the voiding device's clock is a day behind (void `at` is only checked
// for zero, unlike bill and shift times). Voiding cooked food and pocketing
// the cash is exactly what the void section exists to catch.
// ---------------------------------------------------------------------------
func TestReviewF2VoidVanishesFromEveryZReport(t *testing.T) {
	voidsOn := func(f *fixture, days ...string) int {
		n := 0
		for _, d := range days {
			n += f.day(t, d).Report.Voids.Lines
		}
		return n
	}

	t.Run("merged into an order opened the next trading day", func(t *testing.T) {
		till := newRestaurant(t, "Review Merge Void", "", "29")
		k := setupFloor(t, till)
		a, b := ids.New(), ids.New()
		naan, paneer, lassi := newLine(k.Naan, "Butter naan", 1), newLine(k.Paneer, "Paneer tikka", 1), newLine(k.Lassi, "Sweet lassi", 1)
		late := time.Date(2026, 9, 28, 23, 0, 0, 0, gst.IST)
		next := time.Date(2026, 9, 29, 5, 0, 0, 0, gst.IST) // after the 04:00 cutoff
		till.apply(t,
			till.op("order.opened", map[string]any{"order_id": a, "channel": "dine_in", "table_id": k.T4, "covers": 2, "opened_at": late}),
			till.op("order.lines_added", map[string]any{"order_id": a, "lines": payloads(naan, paneer), "at": late}),
			till.op("kot.fired", map[string]any{"kot_id": ids.New(), "kot_no": "K1", "order_id": a, "section_id": k.Tandoor,
				"line_ids": lineIDs(naan), "fired_at": late}),
			till.op("order.line_voided", map[string]any{"order_id": a, "line_id": naan.ID, "reason": "burnt", "at": late.Add(30 * time.Minute)}),
		)
		if n := voidsOn(till, "2026-09-28"); n != 1 {
			t.Fatalf("before the merge the void is on 28 Sep: %d", n)
		}
		till.apply(t,
			till.op("order.opened", map[string]any{"order_id": b, "channel": "dine_in", "table_id": k.T5, "covers": 2, "opened_at": next}),
			till.op("order.lines_added", map[string]any{"order_id": b, "lines": payloads(lassi), "at": next}),
			till.op("order.merged", map[string]any{"target_order_id": b, "source_order_ids": []uuid.UUID{a}, "at": next.Add(30 * time.Minute)}),
		)
		if n := voidsOn(till, "2026-09-27", "2026-09-28", "2026-09-29", "2026-09-30"); n != 1 {
			t.Fatalf("a fired dish voided on 28 Sep must appear in exactly one Z report after a merge, found in %d", n)
		}
	})

	t.Run("voiding device clock a day behind", func(t *testing.T) {
		till := newRestaurant(t, "Review Clock Void", "", "29")
		k := setupFloor(t, till)
		o := ids.New()
		naan, lassi := newLine(k.Naan, "Butter naan", 1), newLine(k.Lassi, "Sweet lassi", 1)
		till.apply(t,
			till.op("order.opened", opened(o, k.T4, 2)),
			till.op("order.lines_added", added(o, naan, lassi)),
		)
		ack := till.push(t, till.op("order.line_voided", map[string]any{"order_id": o, "line_id": naan.ID,
			"reason": "guest changed mind", "at": dinner.Add(-24 * time.Hour)})).Acks[0]
		till.apply(t, till.billFor(t, o, lassi)) // the table pays for the rest and leaves
		if ack.Status == "rejected" {
			return // refusing an implausible void time is a fix too
		}
		if n := voidsOn(till, "2026-09-26", "2026-09-27", "2026-09-28", "2026-09-29"); n != 1 {
			t.Fatalf("an accepted void must appear in exactly one Z report, found in %d", n)
		}
	})
}

// ---------------------------------------------------------------------------
// F3. The digest covers figures that move on their own: an open shift's
// running cash is counted up to `now`, over any business date. While a
// cashier keeps billing the next day on a shift opened yesterday (or the
// till clock runs a few seconds ahead), yesterday's digest keeps changing,
// so the owner's close is refused with report_changed although nothing about
// that day changed. The amendments code already treats these figures as
// "moving on their own"; the digest does not.
// ---------------------------------------------------------------------------
func TestReviewF3DigestChangesWithoutTheDayChanging(t *testing.T) {
	t.Run("next day's cash sale on a shift still open", func(t *testing.T) {
		till := newRestaurant(t, "Review Open Shift", "", "29")
		s := ids.New()
		till.apply(t,
			till.openShift(s, at(18, 0), "Vikram", 100_000),
			till.bill(t, pavBhaji(1), billOpts{issuedAt: at(20, 0)}),
		)
		v := till.day(t, closeDay)
		// The next morning: same shift (nobody counted the drawer), one cash bill
		// for 29 Sep. 28 Sep's bills, voids, shifts opened and series are unchanged.
		till.apply(t, till.bill(t, pavBhaji(1), billOpts{issuedAt: time.Date(2026, 9, 29, 11, 0, 0, 0, gst.IST)}))
		if status, code := reviewClose(t, till, closeDay, v.Report.Digest); status != http.StatusCreated {
			t.Fatalf("closing 28 Sep after a 29 Sep sale: %d %s (want 201)", status, code)
		}
	})

	t.Run("next day's line on a table still open", func(t *testing.T) {
		till := newRestaurant(t, "Review Open Table", "", "29")
		k := setupFloor(t, till)
		o := ids.New()
		till.apply(t,
			till.op("order.opened", opened(o, k.T4, 2)),
			till.op("order.lines_added", added(o, newLine(k.Paneer, "Paneer tikka", 1))),
		)
		v := till.day(t, closeDay)
		till.apply(t, till.op("order.lines_added", map[string]any{"order_id": o,
			"lines": payloads(newLine(k.Lassi, "Sweet lassi", 1)), "at": time.Date(2026, 9, 29, 10, 0, 0, 0, gst.IST)}))
		if status, code := reviewClose(t, till, closeDay, v.Report.Digest); status != http.StatusCreated {
			t.Fatalf("closing 28 Sep after a 29 Sep order line: %d %s (want 201)", status, code)
		}
	})

	t.Run("till clock a few seconds ahead", func(t *testing.T) {
		till := newRestaurant(t, "Review Clock Ahead", "", "29")
		s := ids.New()
		day := "2026-09-27"
		till.apply(t, till.openShift(s, time.Date(2026, 9, 27, 18, 0, 0, 0, gst.IST), "Vikram", 100_000))
		issued := time.Now().Add(3 * time.Second).In(gst.IST) // printed "now" by a till 3 s fast
		till.apply(t, till.bill(t, pavBhaji(1), billOpts{issuedAt: issued}))
		v := till.day(t, day)
		time.Sleep(4 * time.Second) // nothing is pushed; only the clock moves
		if status, code := reviewClose(t, till, day, v.Report.Digest); status != http.StatusCreated {
			t.Fatalf("closing with an unchanged database: %d %s (want 201)", status, code)
		}
	})
}

// ---------------------------------------------------------------------------
// F4. The close does not serialise with sync. A bill that commits after the
// close took its snapshot but before the close commits is neither in the
// stored report nor flagged bill_after_day_close: its DayClosed check ran
// before the day_close row was visible. (ADR 0015: late records are kept AND
// flagged.) A trigger that sleeps inside the close makes the window wide.
// ---------------------------------------------------------------------------
func TestReviewF4BillDuringCloseIsNeitherInSnapshotNorFlagged(t *testing.T) {
	till := newRestaurant(t, "Review Close Race", "", "29")
	till.apply(t, till.bill(t, pavBhaji(1), billOpts{issuedAt: at(12, 0)}))
	v := till.day(t, closeDay)

	if _, err := ownerDB.Exec(fmt.Sprintf(`
		CREATE OR REPLACE FUNCTION review_slow_close() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_sleep(2); RETURN NEW; END $$;
		CREATE TRIGGER review_slow_close BEFORE INSERT ON day_close
		  FOR EACH ROW WHEN (NEW.tenant_id = '%s') EXECUTE FUNCTION review_slow_close();`, uuid.MustParse(till.TenantID))); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ownerDB.Exec(`DROP TRIGGER IF EXISTS review_slow_close ON day_close; DROP FUNCTION IF EXISTS review_slow_close()`)
	})

	type result struct {
		status int
		body   []byte
	}
	done := make(chan result, 1)
	go func() {
		b, _ := json.Marshal(map[string]any{"digest": v.Report.Digest, "acknowledge_warnings": true})
		req, _ := http.NewRequest("POST", api.URL+"/v1/outlets/"+till.OutletID+"/days/"+closeDay+"/close", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+till.OwnerToken)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- result{}
			return
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		done <- result{res.StatusCode, raw}
	}()
	time.Sleep(500 * time.Millisecond) // the close has its snapshot and is inside the INSERT
	till.apply(t, till.bill(t, pavBhaji(2), billOpts{issuedAt: at(21, 0)}))
	r := <-done

	if r.status == http.StatusConflict && bytes.Contains(r.body, []byte("report_changed")) {
		return // serialised with the sync: the owner is asked to look again
	}
	if r.status != http.StatusCreated {
		t.Fatalf("close: %d %s", r.status, r.body)
	}
	var view dayclose.DayView
	if err := json.Unmarshal(r.body, &view); err != nil {
		t.Fatal(err)
	}
	inSnapshot := view.Closed != nil && view.Closed.Report.Sales.Bills == 2
	flagged := till.alerts(t)["bill_after_day_close"] == 1
	if !inSnapshot && !flagged {
		t.Fatalf("a bill that committed during the close is in the snapshot: %v, flagged bill_after_day_close: %v (want one of them)",
			inSnapshot, flagged)
	}
}

// ---------------------------------------------------------------------------
// F5. Ledger posting is idempotent on (tenant, source_event_id) across ALL
// source types, and the new appliers hand it device-made ids (move_id,
// shift_id) without checking they are not already some other entry's
// source. A till that reuses an id (its shift id for the first cash move;
// a move id that is also a bill id) gets "posted" with another record's
// journal entry, and the money is silently never booked.
// ---------------------------------------------------------------------------
func TestReviewF5LedgerIdempotencyKeyCollides(t *testing.T) {
	t.Run("cash move id equal to the shift id hides the drawer difference", func(t *testing.T) {
		till := newRestaurant(t, "Review Id Shift", "", "29")
		s := ids.New()
		till.apply(t, till.openShift(s, at(11, 0), "Asha", 100_000))
		ack := till.push(t, till.op("cash.moved", map[string]any{"move_id": s, "shift_id": s, "kind": "expense",
			"amount_paise": 10_000, "reason": "Ice", "at": at(12, 0)})).Acks[0]
		till.apply(t, till.closeShift(s, at(16, 0), 80_000, nil))
		bal := reviewBalances(t, till)
		want := map[string]int64{"5200": 10_000, "5900": 10_000} // Rs 100 expense, Rs 100 short
		if ack.Status == "rejected" {
			want = map[string]int64{"5200": 0, "5900": 20_000}
		}
		if bal["5200"] != want["5200"] || bal["5900"] != want["5900"] {
			t.Fatalf("move %s: petty expenses %d (want %d), over/short %d (want %d)",
				ack.Status, bal["5200"], want["5200"], bal["5900"], want["5900"])
		}
	})

	t.Run("bill id equal to an earlier cash move id leaves the sale unbooked", func(t *testing.T) {
		till := newRestaurant(t, "Review Id Bill", "", "29")
		s, shared := ids.New(), ids.New()
		till.apply(t,
			till.openShift(s, at(11, 0), "Asha", 100_000),
			till.op("cash.moved", map[string]any{"move_id": shared, "shift_id": s, "kind": "expense",
				"amount_paise": 10_000, "reason": "Ice", "at": at(12, 0)}),
		)
		// Issued bills are never refused (ADR 0007): it must be stored AND booked.
		till.apply(t, till.bill(t, pavBhaji(1), billOpts{issuedAt: at(13, 0), billID: shared.String()}))
		var source string
		if err := ownerDB.QueryRow(`SELECT e.source_type FROM bill b JOIN journal_entry e ON e.id = b.journal_entry_id
			WHERE b.id = $1`, shared).Scan(&source); err != nil {
			t.Fatal(err)
		}
		if bal := reviewBalances(t, till); source != "bill" || bal["4100"] == 0 {
			t.Fatalf("the bill's journal entry is a %q entry; food sales booked: %d", source, bal["4100"])
		}
	})
}

// ---------------------------------------------------------------------------
// F6. A cash bill that is applied after its shift was counted is reconciled
// nowhere. The shift's cash sales are frozen at close, and "cash outside
// shifts" skips any bill whose time falls inside a shift window, closed or
// not. The gateway does not enforce op order (a device_seq regression is only
// an alert), and ADR 0007 replays rejected bills later, so this happens; the
// Z report then shows the drawer "over" by the bill and never mentions the
// cash anywhere else.
// ---------------------------------------------------------------------------
func TestReviewF6CashBillAfterShiftCountIsInNoDrawer(t *testing.T) {
	till := newRestaurant(t, "Review Late Cash", "", "29")
	s := ids.New()
	g := total(t, pavBhaji(1))
	open := till.openShift(s, at(11, 0), "Asha", 0)
	late := till.bill(t, pavBhaji(1), billOpts{issuedAt: at(12, 0)}) // device_seq 2
	shut := till.closeShift(s, at(16, 0), g, nil)                    // device_seq 3: the cash is in the drawer
	till.apply(t, open, shut)
	till.apply(t, late) // arrives last (alerted as a device_seq regression, applied)

	r := till.day(t, closeDay).Report
	cashTaken := int64(0)
	for _, p := range r.Payments {
		if p.Method == "cash" {
			cashTaken = p.Amount
		}
	}
	if cashTaken != g {
		t.Fatalf("setup: cash payments %d, want %d", cashTaken, g)
	}
	inDrawers := r.Cash.Sales + r.Cash.OutsideShifts
	if inDrawers != cashTaken {
		var codes []string
		for _, w := range r.Warnings {
			codes = append(codes, w.Code)
		}
		t.Fatalf("cash taken %d, but shifts' cash sales %d + outside shifts %d = %d; shift difference %+d; warnings %v",
			cashTaken, r.Cash.Sales, r.Cash.OutsideShifts, inDrawers, *r.Shifts[0].Difference, codes)
	}
}

// ---------------------------------------------------------------------------
// F7. shift.closed duplicate detection compares closed_at with Go's
// nanosecond time against Postgres's microsecond copy. A till that stamps
// times with sub-microsecond precision and re-queues its close under a new
// op id (the case the code means to treat as a duplicate) is rejected
// "shift_already_closed ... with a different count" although nothing differs.
// ---------------------------------------------------------------------------
func TestReviewF7ShiftCloseResentIsNotADuplicate(t *testing.T) {
	till := newRestaurant(t, "Review Close Resend", "", "29")
	s := ids.New()
	closedAt := at(16, 0).Add(123_456_789 * time.Nanosecond) // 16:00:00.123456789
	till.apply(t, till.openShift(s, at(11, 0), "Asha", 50_000), till.closeShift(s, closedAt, 50_000, nil))
	ack := till.push(t, till.closeShift(s, closedAt, 50_000, nil)).Acks[0] // same payload, new op id
	if ack.Status != "duplicate" {
		t.Fatalf("the same close re-queued under a new op id: %s %+v", ack.Status, ack.Error)
	}
}

// ---------------------------------------------------------------------------
// F8. A cash move's time is checked only against "after GST began, at most
// 48 h ahead", never against its shift. Its journal entry is dated by that
// time, so a till with a wrong clock books today's petty expense into any
// month since July 2017 (any month nobody has locked yet): years-old books
// change, while the Z report lists the move under today's shift.
// ---------------------------------------------------------------------------
func TestReviewF8CashMoveTimeIsNotTiedToItsShift(t *testing.T) {
	till := newRestaurant(t, "Review Move Time", "", "29")
	s, move := ids.New(), ids.New()
	till.apply(t, till.openShift(s, at(11, 0), "Asha", 100_000))
	ack := till.push(t, till.op("cash.moved", map[string]any{"move_id": move, "shift_id": s, "kind": "expense",
		"amount_paise": 30_000, "reason": "Vegetables", "at": time.Date(2018, 1, 15, 12, 0, 0, 0, gst.IST)})).Acks[0]
	if ack.Status == "rejected" {
		return // refusing a move outside its shift is a fix
	}
	var entryDate string
	if err := ownerDB.QueryRow(`SELECT e.entry_date::text FROM cash_move m JOIN journal_entry e ON e.id = m.journal_entry_id
		WHERE m.id = $1`, move).Scan(&entryDate); err != nil {
		t.Fatal(err)
	}
	if entryDate < "2026-09-28" {
		t.Fatalf("a move on a shift opened 28 Sep 2026 was booked on %s", entryDate)
	}
}

// ---------------------------------------------------------------------------
// F9. When the month is locked, a shift's cash difference (and cash moves)
// are kept unposted (ledger_status "blocked"), but the Z report's
// checks.ledger_blocked and its "not posted to the books" warning count
// bills only. The owner signs off a day whose drawer loss is not in the books
// and the report says nothing.
// ---------------------------------------------------------------------------
func TestReviewF9BlockedDrawerEntriesAreNotInTheReport(t *testing.T) {
	till := newRestaurant(t, "Review Blocked Drawer", "", "29")
	if _, err := ownerDB.Exec(`INSERT INTO period (id, tenant_id, legal_entity_id, starts_on, ends_on, status)
		VALUES ($1, $2, $3, '2026-09-01', '2026-10-01', 'locked')`, ids.New(), till.TenantID, till.LegalEntityID); err != nil {
		t.Fatal(err)
	}
	s := ids.New()
	till.apply(t,
		till.openShift(s, at(11, 0), "Asha", 100_000),
		till.moveCash(s, "expense", 30_000, "Vegetables", at(12, 0)),
		till.closeShift(s, at(16, 0), 60_000, nil), // Rs 100 short
	)
	r := till.day(t, closeDay).Report
	if r.Shifts[0].LedgerStatus != "blocked" {
		t.Fatalf("setup: shift ledger status %q", r.Shifts[0].LedgerStatus)
	}
	if r.Checks.LedgerBlocked == 0 && warningCodes(r)["ledger_blocked"] == "" {
		t.Fatalf("a drawer difference and a cash move are not in the books, but the Z report shows checks %+v, warnings %+v",
			r.Checks, r.Warnings)
	}
}

// ---------------------------------------------------------------------------
// F10. The change-log floor is per tenant, but a device's cursor only moves
// when its own outlet changes (an empty pull hands back `after`). In a
// two-outlet tenant, a till at a quiet outlet that pulls every few seconds
// keeps an old cursor; once the busy outlet's week-old changes are pruned,
// it is told "offline too long" (410) and must bootstrap, though it missed
// nothing. (Only one outlet can be created through the API today; the
// second is inserted directly.)
// ---------------------------------------------------------------------------
func TestReviewF10QuietOutletDeviceGetsCursorExpired(t *testing.T) {
	busy := newRestaurant(t, "Review Two Outlets", "", "29")
	k := setupFloor(t, busy)
	quietID := ids.New()
	if _, err := ownerDB.Exec(`INSERT INTO outlet (id, tenant_id, legal_entity_id, name, state_code)
		SELECT $1, tenant_id, legal_entity_id, 'Koramangala', state_code FROM outlet WHERE id = $2`, quietID, busy.OutletID); err != nil {
		t.Fatal(err)
	}
	q := *busy
	q.OutletID = quietID.String()
	quiet := q.pair(t, "pos", "Koramangala till")

	busy.apply(t, busy.op("order.opened", map[string]any{"order_id": ids.New(), "channel": "takeaway", "opened_at": dinner}))
	cursor := quiet.pull(t, 0, 0).Cursor // bootstrap: cursor 1, not 0 (0 means "bootstrap")
	busy.apply(t,
		busy.op("order.opened", opened(ids.New(), k.T4, 2)),
		busy.op("order.opened", opened(ids.New(), k.T5, 2)),
	)
	cursor = quiet.pull(t, cursor, 0).Cursor // online and up to date: nothing for this outlet
	// A week later the busy outlet's changes are old; its owner closes a day.
	if _, err := ownerDB.Exec(`UPDATE change_log SET created_at = now() - interval '8 days' WHERE tenant_id = $1`, busy.TenantID); err != nil {
		t.Fatal(err)
	}
	v := busy.day(t, "2026-09-01")
	busy.closeDay(t, http.StatusCreated, "2026-09-01", v.Report.Digest, false)

	if status, _ := call(t, "GET", fmt.Sprintf("/v1/sync/pull?after=%d", cursor), quiet.DeviceToken, nil, nil); status != http.StatusOK {
		t.Fatalf("a device that pulled a moment ago and missed nothing: %d (want 200)", status)
	}
}
