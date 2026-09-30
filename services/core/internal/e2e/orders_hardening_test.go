package e2e

// Regression tests from the adversarial review of the orders slice (see
// docs/learn/05). Each test in the first group reproduced a real bug; its
// comment describes the bug as it was found, before the fix. The second
// group probes invariants the review checked and found sound, so they stay
// sound.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"1xn/core/internal/platform/ids"
	"1xn/core/modules/sales/pricing"
	"1xn/core/modules/syncgw"
)

// pushOpsRaw pushes several ops and returns the HTTP status without failing
// the test, so a 503 (transient) can be observed.
func pushOpsRaw(t *testing.T, token string, ops ...syncgw.Op) (int, syncgw.PushResponse) {
	t.Helper()
	var res syncgw.PushResponse
	b, _ := json.Marshal(syncgw.PushRequest{Ops: ops})
	req, _ := http.NewRequest("POST", api.URL+"/v1/sync/push", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_ = json.NewDecoder(resp.Body).Decode(&res)
	return resp.StatusCode, res
}

func openOrders(t *testing.T, f *fixture) []orderDoc {
	t.Helper()
	var open struct {
		Orders []orderDoc `json:"orders"`
	}
	must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/orders", f.OwnerToken, nil, &open)
	return open.Orders
}

// ---------------------------------------------------------------------------
// F1. Pull paging drops orders that share a seq with the page boundary.
//
// applyMerged saves the target and every source with ONE seq. changesSince
// sets cursor = max(seq) of the last row it returns and the next page asks for
// seq > cursor, so when limit cuts between two orders with the same seq, the
// second one is never delivered.
func TestPullNeverSplitsASeq(t *testing.T) {
	till := newRestaurant(t, "Tie Break", "", "29")
	k := setupFloor(t, till)
	a, b := ids.New(), ids.New()
	till.apply(t, till.op("order.opened", opened(a, k.T4, 2)), till.op("order.opened", opened(b, k.T5, 2)))
	start := till.pull(t, 0, 0).Cursor

	till.apply(t, till.op("order.merged", map[string]any{"target_order_id": a, "source_order_ids": []uuid.UUID{b}, "at": dinner}))

	if full := till.pull(t, start, 0); len(full.Orders) != 2 {
		t.Fatalf("an unpaged pull sees both orders: %d", len(full.Orders))
	}
	cursor := start
	seen := map[string]bool{}
	for pages := 0; pages < 10; pages++ {
		page := till.pull(t, cursor, 1)
		for _, o := range page.Orders {
			seen[o.ID] = true
		}
		cursor = page.Cursor
		if !page.HasMore {
			break
		}
	}
	if !seen[a.String()] || !seen[b.String()] {
		t.Fatalf("paging with limit=1 must deliver both the merge target and the tombstone; saw target=%v tombstone=%v",
			seen[a.String()], seen[b.String()])
	}
}

// ---------------------------------------------------------------------------
// F2. A re-queued order.lines_added (same line ids, new op id) opens a
// spillover and re-adds lines that were already billed / voided / spilled.
//
// target() returns the closed order; applyLinesAdded then builds a fresh
// spillover and calls AddLines on it, so the "skip lines whose id already
// exists" rule never sees the closed order's (or earlier spillover's) lines.
func TestRequeuedLinesAreNeverAddedTwice(t *testing.T) {
	t.Run("after the bill", func(t *testing.T) {
		till := newRestaurant(t, "Requeue Bill", "", "29")
		k := setupFloor(t, till)
		o := ids.New()
		x := newLine(k.Paneer, "Paneer tikka", 1)
		till.apply(t, till.op("order.opened", opened(o, k.T4, 2)), till.op("order.lines_added", added(o, x)))
		till.apply(t, till.billFor(t, o, x))

		ack := till.push(t, till.op("order.lines_added", added(o, x))).Acks[0]
		open := openOrders(t, till)
		late := till.alerts(t)["late_items"]
		if ack.Status != "duplicate" || len(open) != 0 || late != 0 {
			t.Fatalf("re-sent paneer (already billed) must change nothing; ack=%s, open orders=%d (lines: %+v), late_items=%d",
				ack.Status, len(open), linesOf(open), late)
		}
	})
	t.Run("after a cancel", func(t *testing.T) {
		till := newRestaurant(t, "Requeue Cancel", "", "29")
		k := setupFloor(t, till)
		o := ids.New()
		x := newLine(k.Naan, "Butter naan", 2)
		till.apply(t, till.op("order.opened", opened(o, k.T4, 2)), till.op("order.lines_added", added(o, x)),
			till.op("order.cancelled", map[string]any{"order_id": o, "reason": "guests left", "at": dinner}))

		ack := till.push(t, till.op("order.lines_added", added(o, x))).Acks[0]
		if open := openOrders(t, till); ack.Status != "duplicate" || len(open) != 0 {
			t.Fatalf("a voided line must not come back to life on a spillover; ack=%s, open orders=%d (lines: %+v)",
				ack.Status, len(open), linesOf(open))
		}
	})
	t.Run("after the spillover was billed", func(t *testing.T) {
		till := newRestaurant(t, "Requeue Spill", "", "29")
		k := setupFloor(t, till)
		captain := till.pair(t, "captain", "Captain")
		o := ids.New()
		x, dessert := newLine(k.Paneer, "Paneer tikka", 1), newLine(k.Lassi, "Sweet lassi", 1)
		till.apply(t, till.op("order.opened", opened(o, k.T4, 2)), till.op("order.lines_added", added(o, x)))
		till.apply(t, till.billFor(t, o, x))
		captain.apply(t, captain.op("order.lines_added", added(o, dessert))) // spillover #1
		till.apply(t, till.billFor(t, o, dessert))                           // spillover #1 closes

		ack := captain.push(t, captain.op("order.lines_added", added(o, dessert))).Acks[0] // re-queued
		open := openOrders(t, till)
		late := till.alerts(t)["late_items"]
		if ack.Status != "duplicate" || len(open) != 0 || late != 1 {
			t.Fatalf("the paid dessert must not open a second spillover; ack=%s, open orders=%d (lines: %+v), late_items=%d",
				ack.Status, len(open), linesOf(open), late)
		}
	})
}

// TestRequeuedLinesAfterMergeAndBill: a line that moved with a merge, then
// was paid, is still the same dish. Re-sending it to the old order id adds
// nothing and opens no spillover.
func TestRequeuedLinesAfterMergeAndBill(t *testing.T) {
	till := newRestaurant(t, "Requeue Merge Bill", "", "29")
	k := setupFloor(t, till)
	captain := till.pair(t, "captain", "Captain")
	a, b := ids.New(), ids.New()
	x, y := newLine(k.Paneer, "Paneer tikka", 1), newLine(k.Naan, "Butter naan", 2)
	till.apply(t, till.op("order.opened", opened(a, k.T4, 2)), till.op("order.lines_added", added(a, x)))
	captain.apply(t, captain.op("order.opened", opened(b, k.T5, 2)), captain.op("order.lines_added", added(b, y)))
	till.apply(t, till.op("order.merged", map[string]any{"target_order_id": a, "source_order_ids": []uuid.UUID{b}, "at": dinner}))
	till.apply(t, till.billFor(t, a, x, y))

	ack := captain.push(t, captain.op("order.lines_added", added(b, y))).Acks[0]
	if open := openOrders(t, till); ack.Status != "duplicate" || len(open) != 0 || till.alerts(t)["late_items"] != 0 {
		t.Fatalf("the paid naan must not come back; ack=%s, open orders=%d (lines: %+v)", ack.Status, len(open), linesOf(open))
	}
}

func linesOf(os []orderDoc) []string {
	var out []string
	for _, o := range os {
		for _, l := range o.Lines {
			out = append(out, fmt.Sprintf("%s:%s(spillover_of=%v)", l.Name, l.Status, o.SpilloverOf != nil))
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// F3. A merge chain of 16 tombstones turns every op (and every BILL) naming
// the oldest order into a poison op: resolve() returns a plain error, which
// the gateway treats as transient -> 503 forever, blocking the device queue.
func TestMergeChainsStayOneHop(t *testing.T) {
	till := newRestaurant(t, "Chain Gang", "", "29")
	k := setupFloor(t, till)
	var chain []uuid.UUID
	var ops []syncgw.Op
	for i := 0; i < 17; i++ {
		o := ids.New()
		chain = append(chain, o)
		ops = append(ops, till.op("order.opened", map[string]any{"order_id": o, "channel": "takeaway", "opened_at": dinner}))
	}
	x := newLine(k.Naan, "Butter naan", 1)
	ops = append(ops, till.op("order.lines_added", added(chain[0], x)))
	till.apply(t, ops...)
	for i := 0; i+1 < len(chain); i++ { // 0 -> 1, then 1 -> 2, ... 15 -> 16
		till.apply(t, till.op("order.merged", map[string]any{"target_order_id": chain[i+1],
			"source_order_ids": []uuid.UUID{chain[i]}, "at": dinner}))
	}
	last := chain[len(chain)-1].String()
	for _, id := range chain[:len(chain)-1] {
		if o := till.order(t, id.String()); o.MergedInto == nil || *o.MergedInto != last {
			t.Fatalf("every tombstone must point straight at the live order: %s -> %v", id, o.MergedInto)
		}
	}

	t.Run("order op", func(t *testing.T) {
		late := newLine(k.Lassi, "Sweet lassi", 1)
		good := till.op("order.opened", map[string]any{"order_id": ids.New(), "channel": "takeaway", "opened_at": dinner})
		status, res := pushOpsRaw(t, till.DeviceToken, till.op("order.lines_added", added(chain[0], late)), good)
		if status != http.StatusOK || len(res.Acks) != 2 || res.Acks[1].Status != "applied" {
			t.Fatalf("an op naming an old tombstone must be applied or rejected, never retried forever: HTTP %d, acks %+v, error %+v",
				status, res.Acks, res.Error)
		}
	})
	t.Run("bill", func(t *testing.T) {
		bill := till.billFor(t, chain[0], x)
		next := till.bill(t, pavBhaji(1), billOpts{})
		status, res := pushOpsRaw(t, till.DeviceToken, bill, next)
		if status != http.StatusOK || len(res.Acks) != 2 || res.Acks[0].Status != "applied" || res.Acks[1].Status != "applied" {
			t.Fatalf("a bill is never refused or retried forever because of its order link: HTTP %d, acks %+v, error %+v",
				status, res.Acks, res.Error)
		}
	})
}

// ---------------------------------------------------------------------------
// F4. order.cancelled follows merge tombstones, so cancelling the merged-away
// order cancels the TARGET (another table's live, fired food) with no alert.
func TestCancelDoesNotFollowAMerge(t *testing.T) {
	till := newRestaurant(t, "Cancel Merge", "", "29")
	k := setupFloor(t, till)
	captain := till.pair(t, "captain", "Captain")
	a, b := ids.New(), ids.New()
	x, y := newLine(k.Paneer, "Paneer tikka", 1), newLine(k.Naan, "Butter naan", 2)
	kot := ids.New()
	till.apply(t, till.op("order.opened", opened(a, k.T4, 2)), till.op("order.lines_added", added(a, x)),
		till.op("kot.fired", fired(a, kot, "001-K1", k.Tandoor, x)))
	captain.apply(t, captain.op("order.opened", opened(b, k.T5, 2)), captain.op("order.lines_added", added(b, y)))

	till.apply(t, till.op("order.merged", map[string]any{"target_order_id": a, "source_order_ids": []uuid.UUID{b}, "at": dinner}))
	// The captain, offline, cancels T5's order (he never learnt about the merge).
	ack := captain.push(t, captain.op("order.cancelled", map[string]any{"order_id": b, "reason": "guests left", "at": dinner})).Acks[0]
	if ack.Status != "rejected" || ack.Error.Code != "order_merged" {
		t.Fatalf("a cancel aimed at a merged-away order is refused and kept for a human: %+v", ack)
	}

	got := till.order(t, a.String())
	if got.Status != "open" || got.lineStatus(x.ID.String()) != "fired" {
		t.Fatalf("T4's order (the merge target) must survive a cancel aimed at the merged-away T5 order: status=%s, T4 paneer=%s, alerts=%v",
			got.Status, got.lineStatus(x.ID.String()), till.alerts(t))
	}
}

// ---------------------------------------------------------------------------
// F5. Cancel voids FIRED lines with no approval and raises no alert, while a
// line void of the same fired line raises void_after_kot_without_approval.
// A till can serve food, take cash and cancel the order with no trace.
func TestCancelAfterKOTIsFlagged(t *testing.T) {
	till := newRestaurant(t, "Silent Cancel", "", "29")
	k := setupFloor(t, till)
	o, kot := ids.New(), ids.New()
	x := newLine(k.Paneer, "Paneer tikka", 1)
	till.apply(t, till.op("order.opened", opened(o, k.T4, 2)), till.op("order.lines_added", added(o, x)),
		till.op("kot.fired", fired(o, kot, "001-K1", k.Tandoor, x)),
		till.op("order.cancelled", map[string]any{"order_id": o, "reason": "changed mind", "at": dinner}))
	if got := till.order(t, o.String()); got.Status != "cancelled" {
		t.Fatalf("setup: %s", got.Status)
	}
	if n := till.alerts(t)["void_after_kot_without_approval"]; n != 1 {
		t.Fatalf("cancelling an order whose food was already fired (no approval) must raise one alert, got %v", till.alerts(t))
	}

	// With a manager's approval, the same cancel is quiet.
	o2, kot2 := ids.New(), ids.New()
	y := newLine(k.Naan, "Butter naan", 1)
	till.apply(t, till.op("order.opened", opened(o2, k.T5, 2)), till.op("order.lines_added", added(o2, y)),
		till.op("kot.fired", fired(o2, kot2, "001-K2", k.Tandoor, y)),
		till.op("order.cancelled", map[string]any{"order_id": o2, "reason": "changed mind", "approved_by": "Manager Ravi", "at": dinner}))
	if n := till.alerts(t)["void_after_kot_without_approval"]; n != 1 {
		t.Fatalf("an approved cancel raises no new alert, got %v", till.alerts(t))
	}
}

// ---------------------------------------------------------------------------
// F6. Re-queued order.merged after the target was billed is REJECTED
// (order_not_open) and raises an op_rejected alert, instead of a duplicate.
func TestRequeuedMergeAfterBillIsDuplicate(t *testing.T) {
	till := newRestaurant(t, "Requeue Merge", "", "29")
	k := setupFloor(t, till)
	a, b := ids.New(), ids.New()
	x, y := newLine(k.Paneer, "Paneer tikka", 1), newLine(k.Naan, "Butter naan", 2)
	till.apply(t, till.op("order.opened", opened(a, k.T4, 2)), till.op("order.lines_added", added(a, x)),
		till.op("order.opened", opened(b, k.T5, 2)), till.op("order.lines_added", added(b, y)))
	merge := map[string]any{"target_order_id": a, "source_order_ids": []uuid.UUID{b}, "at": dinner}
	till.apply(t, till.op("order.merged", merge))
	till.apply(t, till.billFor(t, a, x, y))

	ack := till.push(t, till.op("order.merged", merge)).Acks[0]
	if ack.Status != "duplicate" {
		t.Fatalf("a re-queued merge that already happened must be a duplicate: %+v (alerts %v)", ack, till.alerts(t))
	}
}

// ---------------------------------------------------------------------------
// F7. MarkBilled never compares the bill line with the order line it claims
// to settle. A bill for 1 naan can settle a line of 4 naans (or a Rs 0 line
// can settle a Rs 320 paneer); the order closes and nobody is told.
func TestBillChargingLessThanTheOrderIsFlagged(t *testing.T) {
	till := newRestaurant(t, "Short Bill", "", "29")
	k := setupFloor(t, till)
	o := ids.New()
	naan := newLine(k.Naan, "Butter naan", 4)
	till.apply(t, till.op("order.opened", opened(o, k.T4, 2)), till.op("order.lines_added", added(o, naan)))

	short := naan
	short.Bill.Qty = 1 // the till charges one naan against the 4-naan line
	till.apply(t, till.billFor(t, o, short))
	got := till.order(t, o.String())
	if got.Status == "closed" && till.alerts(t)["bill_order_mismatch"] == 0 {
		t.Fatalf("a bill charging 1 of 4 naans closed the order with no alert (alerts %v)", till.alerts(t))
	}
}

// ---------------------------------------------------------------------------
// F8. A bill whose issued_at in IST falls in year 10000 passes every bill
// check, but when it closes its order MarkBilled stores closed_at =
// issued.In(IST); json.Marshal refuses years > 9999 and the (non-pq) error is
// treated as transient: 503 forever, and every bill behind it waits.
func TestBillWithABrokenClockIsRejected(t *testing.T) {
	till := newRestaurant(t, "Y10K", "", "29")
	k := setupFloor(t, till)
	o := ids.New()
	x := newLine(k.Naan, "Butter naan", 1)
	till.apply(t, till.op("order.opened", opened(o, k.T4, 2)), till.op("order.lines_added", added(o, x)))

	issued := time.Date(9999, 12, 31, 20, 0, 0, 0, time.UTC) // 1 Jan 10000, 01:30 IST
	in := pricing.Input{PriceMode: pricing.Exclusive, Supply: pricing.Intra, Rounding: pricing.NearestRupee,
		Lines: []pricing.Line{x.Bill}}
	bad := till.bill(t, in, billOpts{issuedAt: issued, invoiceNo: till.Prefix + "-9900-000001", orderID: &o,
		lineIDs: []uuid.UUID{x.ID}})
	// business_date is issued.Format(DateOnly) in the fixture = 9999-12-31 (UTC date), the day before the IST date: allowed.
	good := till.bill(t, pavBhaji(1), billOpts{})
	status, res := pushOpsRaw(t, till.DeviceToken, bad, good)
	if status != http.StatusOK || len(res.Acks) != 2 || res.Acks[1].Status != "applied" {
		t.Fatalf("a bill with a broken clock must be stored or rejected, not retried forever: HTTP %d, acks %+v, error %+v",
			status, res.Acks, res.Error)
	}
	if res.Acks[0].Status != "rejected" || res.Acks[0].Error.Code != "bad_issued_at" {
		t.Fatalf("a bill from year 10000 is rejected (and kept) as bad_issued_at: %+v", res.Acks[0])
	}
}

// TestBrokenMergeChainIsRejectedNotRetried: tombstones that loop (only
// possible if someone edits the data by hand) must not turn into a poison
// op. Order ops are rejected; a bill is stored and flagged.
func TestBrokenMergeChainIsRejectedNotRetried(t *testing.T) {
	till := newRestaurant(t, "Loop Lane", "", "29")
	k := setupFloor(t, till)
	a, b := ids.New(), ids.New()
	x := newLine(k.Naan, "Butter naan", 1)
	till.apply(t, till.op("order.opened", opened(a, k.T4, 2)), till.op("order.lines_added", added(a, x)),
		till.op("order.opened", opened(b, k.T5, 2)))
	for _, pair := range [][2]uuid.UUID{{a, b}, {b, a}} {
		if _, err := ownerDB.Exec(`UPDATE pos_order SET status = 'merged', merged_into = $2::uuid,
			doc = doc || jsonb_build_object('status', 'merged', 'merged_into', $2::uuid) WHERE id = $1`, pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	status, res := pushOpsRaw(t, till.DeviceToken, till.op("order.lines_added", added(a, newLine(k.Lassi, "Sweet lassi", 1))),
		till.billFor(t, a, x))
	if status != http.StatusOK || len(res.Acks) != 2 {
		t.Fatalf("HTTP %d, acks %+v, error %+v", status, res.Acks, res.Error)
	}
	if ack := res.Acks[0]; ack.Status != "rejected" || ack.Error.Code != "merge_chain_too_long" {
		t.Fatalf("an op into a tombstone loop is rejected: %+v", ack)
	}
	if ack := res.Acks[1]; ack.Status != "applied" || till.alerts(t)["bill_order_mismatch"] != 1 {
		t.Fatalf("a bill into a tombstone loop is stored and flagged: %+v, alerts %v", ack, till.alerts(t))
	}
}

// TestInactiveTableIsAcceptedWithAlert: the owner switches a table off while
// a captain, offline, seats guests at it. The guests are there; the order is
// kept and a human is told.
func TestInactiveTableIsAcceptedWithAlert(t *testing.T) {
	till := newRestaurant(t, "Closed Corner", "", "29")
	k := setupFloor(t, till)
	if _, err := ownerDB.Exec(`UPDATE dining_table SET active = false WHERE id = $1`, k.T5); err != nil {
		t.Fatal(err)
	}
	o := ids.New()
	till.apply(t, till.op("order.opened", opened(o, k.T5, 2)))
	if got := till.order(t, o.String()); got.TableID == nil || *got.TableID != k.T5 {
		t.Fatalf("order must be on T5: %+v", got)
	}
	if till.alerts(t)["inactive_table"] != 1 {
		t.Fatalf("an inactive table raises an alert: %v", till.alerts(t))
	}
}

// TestModifierOptionsMustExist: a modifier that names a catalogue option
// must name a real one, like items and sections.
func TestModifierOptionsMustExist(t *testing.T) {
	till := newRestaurant(t, "Add-on Alley", "", "29")
	k := setupFloor(t, till)
	var cheese string
	if err := ownerDB.QueryRow(`SELECT id FROM modifier_option WHERE tenant_id = $1 AND name = 'Extra cheese'`,
		till.TenantID).Scan(&cheese); err != nil {
		t.Fatal(err)
	}
	o := ids.New()
	till.apply(t, till.op("order.opened", opened(o, k.T4, 2)))
	good := newLine(k.Paneer, "Paneer tikka", 1)
	good.Payload["modifiers"] = []map[string]any{{"option_id": cheese, "name": "Extra cheese", "price_paise": 4000}}
	bad := newLine(k.Paneer, "Paneer tikka", 1)
	bad.Payload["modifiers"] = []map[string]any{{"option_id": ids.New(), "name": "Gold leaf", "price_paise": 1}}
	res := till.push(t, till.op("order.lines_added", added(o, good)), till.op("order.lines_added", added(o, bad)))
	if res.Acks[0].Status != "applied" || res.Acks[1].Status != "rejected" || res.Acks[1].Error.Code != "unknown_modifier" {
		t.Fatalf("real option applied, unknown option rejected: %+v", res.Acks)
	}
}

// ===========================================================================
// Areas probed and found sound (these are expected to pass).

// Lock order: opposite merges and bill-vs-add on the same orders, at once,
// many times. Every writer serialises on sync_state before touching orders,
// so none of these may deadlock or 503.
func TestConcurrentOppositeMergesDoNotDeadlock(t *testing.T) {
	till := newRestaurant(t, "Deadlock Den", "", "29")
	k := setupFloor(t, till)
	cap1 := till.pair(t, "captain", "C1")
	cap2 := till.pair(t, "captain", "C2")
	for i := 0; i < 15; i++ {
		a, b := ids.New(), ids.New()
		x, x2, y := newLine(k.Paneer, "P", 1), newLine(k.Paneer, "P2", 1), newLine(k.Naan, "N", 1)
		till.apply(t, till.op("order.opened", opened(a, k.T4, 1)), till.op("order.lines_added", added(a, x, x2)),
			till.op("order.opened", opened(b, k.T5, 1)), till.op("order.lines_added", added(b, y)))
		ops := []struct {
			tok string
			op  syncgw.Op
		}{
			{cap1.DeviceToken, cap1.op("order.merged", map[string]any{"target_order_id": a, "source_order_ids": []uuid.UUID{b}, "at": dinner})},
			{cap2.DeviceToken, cap2.op("order.merged", map[string]any{"target_order_id": b, "source_order_ids": []uuid.UUID{a}, "at": dinner})},
			{till.DeviceToken, till.billFor(t, a, x)},
			{cap1.DeviceToken, cap1.op("order.lines_added", added(b, newLine(k.Lassi, "L", 1)))},
		}
		var wg sync.WaitGroup
		errs := make(chan string, len(ops))
		for _, o := range ops {
			wg.Add(1)
			go func(tok string, op syncgw.Op) {
				defer wg.Done()
				status, res, err := pushRaw(tok, op)
				if err != nil || status != http.StatusOK || len(res.Acks) != 1 {
					errs <- fmt.Sprintf("%s: HTTP %d err %v %+v", op.Type, status, err, res)
				}
			}(o.tok, o.op)
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			t.Fatal(e)
		}
		ta, tb := till.order(t, a.String()), till.order(t, b.String())
		if (ta.Status == "merged") == (tb.Status == "merged") {
			t.Fatalf("exactly one of the two orders is a tombstone: %s / %s", ta.Status, tb.Status)
		}
	}
}

// RLS: the new tables hide other tenants' rows from the app role and refuse
// cross-tenant writes.
func TestOrdersRLS(t *testing.T) {
	a := newRestaurant(t, "RLS A", "", "29")
	b := newRestaurant(t, "RLS B", "", "29")
	o := ids.New()
	a.apply(t, a.op("order.opened", map[string]any{"order_id": o, "channel": "takeaway", "opened_at": dinner}))
	for _, table := range []string{"pos_order", "change_log", "kitchen_section", "floor", "dining_table", "modifier_group"} {
		var n int
		if err := asTenant(t, b.TenantID, func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n)
		}); err != nil || n != 0 {
			t.Errorf("tenant B sees %d rows of %s (err %v)", n, table, err)
		}
		if err := asTenant(t, "", func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n)
		}); err != nil || n != 0 {
			t.Errorf("no tenant sees %d rows of %s (err %v)", n, table, err)
		}
	}
	err := asTenant(t, b.TenantID, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE pos_order SET status = 'cancelled' WHERE id = $1`, o)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO change_log (tenant_id, seq, outlet_id, entity, entity_id) VALUES ($1, 999, $2, 'order', $3)`,
			a.TenantID, a.OutletID, o)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "row-level security") {
		t.Fatalf("cross-tenant change_log insert must violate RLS, got %v", err)
	}
	if got := a.order(t, o.String()); got.Status != "open" {
		t.Fatalf("tenant B changed tenant A's order: %s", got.Status)
	}
	must(t, http.StatusNotFound, "GET", "/v1/orders/"+o.String(), b.OwnerToken, nil, nil)
}

// Outlet isolation: a second outlet's devices see nothing of the first
// outlet's orders and cannot act on them; a bill naming them is kept and
// flagged without touching the order.
func TestOrdersOutletIsolation(t *testing.T) {
	r := newRestaurant(t, "Two Outlets", "", "29")
	k := setupFloor(t, r)
	outlet2 := ids.New()
	if _, err := ownerDB.Exec(`INSERT INTO outlet (id, tenant_id, legal_entity_id, name, state_code, price_mode, rounding)
		SELECT $1, tenant_id, legal_entity_id, 'Koramangala', state_code, price_mode, rounding FROM outlet WHERE id = $2`,
		outlet2, r.OutletID); err != nil {
		t.Fatal(err)
	}
	other := *r
	other.OutletID = outlet2.String()
	till2 := other.pair(t, "pos", "Till K")

	o := ids.New()
	x := newLine(k.Paneer, "Paneer tikka", 1)
	r.apply(t, r.op("order.opened", opened(o, k.T4, 2)), r.op("order.lines_added", added(o, x)))
	if p := till2.pull(t, 0, 0); len(p.Orders) != 0 {
		t.Fatalf("outlet 2 bootstrap sees outlet 1's orders: %d", len(p.Orders))
	}
	if p := till2.pull(t, 1, 0); len(p.Orders) != 0 {
		t.Fatalf("outlet 2 incremental pull sees outlet 1's orders: %d", len(p.Orders))
	}
	o2 := ids.New()
	till2.apply(t, till2.op("order.opened", map[string]any{"order_id": o2, "channel": "takeaway", "opened_at": dinner}))
	for name, op := range map[string]syncgw.Op{
		"add":    till2.op("order.lines_added", added(o, newLine(k.Naan, "N", 1))),
		"void":   till2.op("order.line_voided", map[string]any{"order_id": o, "line_id": x.ID, "reason": "x", "at": dinner}),
		"cancel": till2.op("order.cancelled", map[string]any{"order_id": o, "reason": "x", "at": dinner}),
		"merge":  till2.op("order.merged", map[string]any{"target_order_id": o2, "source_order_ids": []uuid.UUID{o}, "at": dinner}),
		"table":  till2.op("order.opened", map[string]any{"order_id": ids.New(), "channel": "dine_in", "table_id": k.T4, "opened_at": dinner}),
	} {
		ack := till2.push(t, op).Acks[0]
		if ack.Status != "rejected" || (ack.Error.Code != "wrong_outlet" && ack.Error.Code != "unknown_table") {
			t.Errorf("%s from another outlet: %+v", name, ack)
		}
	}
	till2.apply(t, till2.billFor(t, o, x))
	if got := r.order(t, o.String()); got.Status != "open" || got.lineStatus(x.ID.String()) != "held" {
		t.Fatalf("another outlet's bill must not touch the order: %+v", got)
	}
	if r.alerts(t)["bill_order_mismatch"] != 1 {
		t.Fatal("...but it is flagged")
	}
}

// Malformed order ops are rejected (kept), never 503, and the good op queued
// behind them applies.
func TestMalformedOrderOpsAreRejected(t *testing.T) {
	till := newRestaurant(t, "Poison Kitchen", "", "29")
	k := setupFloor(t, till)
	o := ids.New()
	x := newLine(k.Paneer, "Paneer tikka", 1)
	till.apply(t, till.op("order.opened", opened(o, k.T4, 2)), till.op("order.lines_added", added(o, x)))

	raw := func(typ, payload string) syncgw.Op {
		op := till.op(typ, nil)
		op.Payload = json.RawMessage(payload)
		return op
	}
	nulName := newLine(k.Naan, "Na\u0000an", 1)
	nulNote := newLine(k.Naan, "Naan", 1)
	nulNote.Payload["note"] = "no\u0000te"
	huge := newLine(k.Naan, "Naan", 1)
	huge.Payload["qty"] = int64(1) << 62
	manyMods := newLine(k.Naan, "Naan", 1)
	var mods []map[string]any
	for i := 0; i < 51; i++ {
		mods = append(mods, map[string]any{"name": "m", "price_paise": 1})
	}
	manyMods.Payload["modifiers"] = mods
	var hundredOne []orderLine
	for i := 0; i < 101; i++ {
		hundredOne = append(hundredOne, newLine(k.Naan, "Naan", 1))
	}
	bad := []syncgw.Op{
		till.op("order.lines_added", added(o, nulName)),
		till.op("order.lines_added", added(o, nulNote)),
		till.op("order.lines_added", added(o, huge)),
		till.op("order.lines_added", added(o, manyMods)),
		till.op("order.lines_added", added(o, hundredOne...)),
		till.op("order.lines_added", map[string]any{"order_id": o, "lines": payloads(newLine(k.Naan, "Naan", 1))}), // no "at"
		raw("order.lines_added", `null`),
		raw("order.lines_added", `{"order_id":"nope","lines":[],"at":"2026-09-28T21:14:03+05:30"}`),
		raw("order.merged", `{"target_order_id":"`+o.String()+`","source_order_ids":[null],"at":"2026-09-28T21:14:03+05:30"}`),
		raw("kot.fired", `{"kot_id":"`+ids.New().String()+`","kot_no":"K\u0000","order_id":"`+o.String()+`","line_ids":["`+x.ID.String()+`"],"fired_at":"2026-09-28T21:14:03+05:30"}`),
		till.op("order.line_voided", map[string]any{"order_id": o, "line_id": x.ID, "reason": "bu\u0000rnt", "at": dinner}),
		till.op("order.cancelled", map[string]any{"order_id": o, "reason": "", "at": dinner}),
		till.op("kot.status_changed", map[string]any{"order_id": o, "kot_id": ids.New(), "status": "eaten", "at": dinner}),
		till.op("order.opened", map[string]any{"order_id": ids.New(), "channel": "dine_in", "covers": 10_000, "opened_at": dinner}),
		till.op("order.moved", map[string]any{"order_id": o, "to_table_id": ids.New(), "at": dinner}),
	}
	good := till.op("order.opened", map[string]any{"order_id": ids.New(), "channel": "takeaway", "opened_at": dinner})
	status, res := pushOpsRaw(t, till.DeviceToken, append(bad, good)...)
	if status != http.StatusOK || !res.Complete {
		t.Fatalf("HTTP %d, acks %d/%d, error %+v", status, len(res.Acks), len(bad)+1, res.Error)
	}
	for i := range bad {
		if res.Acks[i].Status != "rejected" {
			t.Errorf("bad op %d (%s) was %s", i, bad[i].Type, res.Acks[i].Status)
		}
	}
	if res.Acks[len(bad)].Status != "applied" {
		t.Fatalf("good op behind the bad ones: %+v", res.Acks[len(bad)])
	}
}

// Merge edge cases that must be harmless: into itself, into its own
// tombstone, a cycle, a listed-twice source, a closed source.
func TestMergeEdges(t *testing.T) {
	till := newRestaurant(t, "Merge Edges", "", "29")
	k := setupFloor(t, till)
	a, b, c := ids.New(), ids.New(), ids.New()
	x, y, z := newLine(k.Paneer, "P", 1), newLine(k.Naan, "N", 1), newLine(k.Lassi, "L", 1)
	till.apply(t,
		till.op("order.opened", opened(a, k.T4, 1)), till.op("order.lines_added", added(a, x)),
		till.op("order.opened", opened(b, k.T5, 1)), till.op("order.lines_added", added(b, y)),
		till.op("order.opened", map[string]any{"order_id": c, "channel": "takeaway", "opened_at": dinner}),
		till.op("order.lines_added", added(c, z)))
	m := func(tgt uuid.UUID, src ...uuid.UUID) syncgw.Op {
		return till.op("order.merged", map[string]any{"target_order_id": tgt, "source_order_ids": src, "at": dinner})
	}
	till.apply(t, m(a, a))
	till.apply(t, m(a, b, b))
	till.apply(t, m(b, a)) // into its own tombstone: resolves to a, nothing to do
	if got := till.order(t, a.String()); got.Status != "open" || len(got.Lines) != 2 || got.Version != 3 {
		t.Fatalf("a: %+v", got)
	}
	till.apply(t, till.billFor(t, c, z))
	ack := till.push(t, m(a, c)).Acks[0]
	if ack.Status != "rejected" || ack.Error.Code != "order_not_open" {
		t.Fatalf("merging a paid order: %+v", ack)
	}
}

// Bootstrap vs an in-flight write: a transaction that has taken seq N but not
// committed is invisible to the bootstrap snapshot, whose cursor is N-1, so
// the next incremental pull picks the change up. (Simulates an applier by
// hand, as the app role, following the same lock order.)
func TestBootstrapCursorVsInFlightWrite(t *testing.T) {
	till := newRestaurant(t, "In Flight", "", "29")
	o := ids.New()
	till.apply(t, till.op("order.opened", map[string]any{"order_id": o, "channel": "takeaway", "covers": 1, "opened_at": dinner}))

	ctx := t.Context()
	tx, err := appDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var seq int64
	if _, err := tx.Exec(`SELECT set_config('app.tenant_id', $1, true)`, till.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(`UPDATE sync_state SET last_seq = last_seq + 1 RETURNING last_seq`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE pos_order SET last_seq = $2, version = version + 1,
		doc = doc || jsonb_build_object('covers', 7, 'last_seq', $2::bigint) WHERE id = $1`, o, seq); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO change_log (tenant_id, seq, outlet_id, entity, entity_id) VALUES ($1, $2, $3, 'order', $4)`,
		till.TenantID, seq, till.OutletID, o); err != nil {
		t.Fatal(err)
	}
	boot := till.pull(t, 0, 0) // must not block, must not see the uncommitted change
	if boot.Cursor != seq-1 {
		t.Fatalf("bootstrap cursor %d, want %d", boot.Cursor, seq-1)
	}
	if d, ok := boot.find(o.String()); !ok || d.Covers != 1 {
		t.Fatalf("bootstrap should see the committed order only: %+v", boot)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	inc := till.pull(t, boot.Cursor, 0)
	if d, ok := inc.find(o.String()); !ok || d.Covers != 7 || inc.Cursor != seq {
		t.Fatalf("the change that committed after the bootstrap must be pulled next: %+v", inc)
	}
}
