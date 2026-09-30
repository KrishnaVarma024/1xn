package e2e

import (
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"

	"1xn/core/internal/platform/ids"
	"1xn/core/modules/sales/pricing"
	"1xn/core/modules/syncgw"
)

type menuItem struct {
	ID      string
	Price   int64
	Section string
}

type floorKit struct {
	T4, T5              string
	Tandoor, Bar        string
	Paneer, Naan, Lassi menuItem
	AddOns              string
}

// setupFloor gives a restaurant two kitchen sections, an add-ons group, three
// items, one floor and tables T4 and T5.
func setupFloor(t *testing.T, r *fixture) floorKit {
	t.Helper()
	var k floorKit
	var id struct {
		ID string `json:"id"`
	}
	must(t, http.StatusCreated, "POST", "/v1/catalogue/sections", r.OwnerToken, map[string]any{"name": "Tandoor"}, &id)
	k.Tandoor = id.ID
	must(t, http.StatusCreated, "POST", "/v1/catalogue/sections", r.OwnerToken, map[string]any{"name": "Bar", "sort": 2}, &id)
	k.Bar = id.ID
	must(t, http.StatusCreated, "POST", "/v1/catalogue/modifier-groups", r.OwnerToken, map[string]any{
		"name": "Add-ons", "min_select": 0, "max_select": 3,
		"options": []map[string]any{{"name": "Extra cheese", "price_paise": 4000}, {"name": "Extra butter", "price_paise": 2000}},
	}, &id)
	k.AddOns = id.ID

	item := func(name string, price int64, section string, groups ...string) menuItem {
		body := map[string]any{"category_id": r.CategoryID, "tax_profile_id": r.TaxProfileID, "name": name,
			"price_paise": price, "section_id": section}
		if len(groups) > 0 {
			body["modifier_group_ids"] = groups
		}
		must(t, http.StatusCreated, "POST", "/v1/catalogue/items", r.OwnerToken, body, &id)
		return menuItem{ID: id.ID, Price: price, Section: section}
	}
	k.Paneer = item("Paneer tikka", 32000, k.Tandoor, k.AddOns)
	k.Naan = item("Butter naan", 6000, k.Tandoor)
	k.Lassi = item("Sweet lassi", 9000, k.Bar)

	must(t, http.StatusCreated, "POST", "/v1/outlets/"+r.OutletID+"/floors", r.OwnerToken, map[string]any{"name": "Main hall"}, &id)
	floor := id.ID
	must(t, http.StatusCreated, "POST", "/v1/outlets/"+r.OutletID+"/tables", r.OwnerToken,
		map[string]any{"floor_id": floor, "label": "T4", "seats": 4}, &id)
	k.T4 = id.ID
	must(t, http.StatusCreated, "POST", "/v1/outlets/"+r.OutletID+"/tables", r.OwnerToken,
		map[string]any{"floor_id": floor, "label": "T5", "seats": 2}, &id)
	k.T5 = id.ID
	return k
}

// orderLine is one line as a device builds it: the op payload and what the
// bill will need later.
type orderLine struct {
	ID      uuid.UUID
	Payload map[string]any
	Bill    pricing.Line
}

func newLine(it menuItem, name string, qty int64, mods ...pricing.Modifier) orderLine {
	id := ids.New()
	var opMods []map[string]any
	for _, m := range mods {
		opMods = append(opMods, map[string]any{"name": m.Name, "price_paise": m.PricePaise})
	}
	return orderLine{
		ID: id,
		Payload: map[string]any{"line_id": id, "item_id": it.ID, "name": name, "qty": qty,
			"unit_price_paise": it.Price, "tax_rate_bp": 500, "section_id": it.Section, "course": 1, "modifiers": opMods},
		Bill: pricing.Line{Ref: it.ID, Name: name, Qty: qty, UnitPricePaise: it.Price, TaxRateBP: 500, Modifiers: mods},
	}
}

func payloads(ls ...orderLine) []map[string]any {
	out := make([]map[string]any, len(ls))
	for i, l := range ls {
		out[i] = l.Payload
	}
	return out
}

func lineIDs(ls ...orderLine) []uuid.UUID {
	out := make([]uuid.UUID, len(ls))
	for i, l := range ls {
		out[i] = l.ID
	}
	return out
}

// billFor prices exactly these order lines, like the till does at settle.
func (f *fixture) billFor(t *testing.T, order uuid.UUID, ls ...orderLine) syncgw.Op {
	t.Helper()
	in := pricing.Input{PriceMode: pricing.Exclusive, Supply: pricing.Intra, Rounding: pricing.NearestRupee}
	for _, l := range ls {
		in.Lines = append(in.Lines, l.Bill)
	}
	return f.bill(t, in, billOpts{orderID: &order, lineIDs: lineIDs(ls...)})
}

func opened(order uuid.UUID, table string, covers int) map[string]any {
	return map[string]any{"order_id": order, "channel": "dine_in", "table_id": table, "covers": covers, "opened_at": dinner}
}

func added(order uuid.UUID, ls ...orderLine) map[string]any {
	return map[string]any{"order_id": order, "lines": payloads(ls...), "at": dinner}
}

func fired(order uuid.UUID, kot uuid.UUID, no, section string, ls ...orderLine) map[string]any {
	return map[string]any{"kot_id": kot, "kot_no": no, "order_id": order, "section_id": section,
		"line_ids": lineIDs(ls...), "fired_at": dinner}
}

// TestDineInFlow is a table's evening: a captain takes the order, the kitchen
// cooks it, the till voids one item, and the table pays in two split bills.
func TestDineInFlow(t *testing.T) {
	till := newRestaurant(t, "Dhaba Deluxe", "", "29")
	k := setupFloor(t, till)
	captain := till.pair(t, "captain", "Captain Anil")
	kds := till.pair(t, "kds", "Tandoor screen")

	// The menu now carries the room and the kitchen.
	var menu struct {
		Sections []struct{ ID string } `json:"sections"`
		Floors   []struct {
			Tables []struct{ Label string } `json:"tables"`
		} `json:"floors"`
		ModifierGroups []struct {
			Options []struct{ Name string } `json:"options"`
		} `json:"modifier_groups"`
		Items []struct {
			Name             string   `json:"name"`
			SectionID        *string  `json:"section_id"`
			ModifierGroupIDs []string `json:"modifier_group_ids"`
		} `json:"items"`
	}
	must(t, http.StatusOK, "GET", "/v1/devices/me/menu", captain.DeviceToken, nil, &menu)
	if len(menu.Sections) != 2 || len(menu.Floors) != 1 || len(menu.Floors[0].Tables) != 2 ||
		len(menu.ModifierGroups) != 1 || len(menu.ModifierGroups[0].Options) != 2 {
		t.Fatalf("menu layout: %+v", menu)
	}
	for _, it := range menu.Items {
		if it.Name == "Paneer tikka" && (it.SectionID == nil || len(it.ModifierGroupIDs) != 1) {
			t.Fatalf("paneer should carry its section and add-ons: %+v", it)
		}
	}

	order := ids.New()
	paneer := newLine(k.Paneer, "Paneer tikka", 1, pricing.Modifier{Name: "Extra cheese", PricePaise: 4000})
	naan := newLine(k.Naan, "Butter naan", 4)
	lassi := newLine(k.Lassi, "Sweet lassi", 2)
	kot1, kot2 := ids.New(), ids.New()
	captain.apply(t,
		captain.op("order.opened", opened(order, k.T4, 2)),
		captain.op("order.lines_added", added(order, paneer, naan, lassi)),
		captain.op("kot.fired", fired(order, kot1, "002-K1", k.Tandoor, paneer, naan)),
		captain.op("kot.fired", fired(order, kot2, "002-K2", k.Bar, lassi)),
	)

	// The tandoor screen marks its ticket ready; a stale "preparing" arrives late.
	res := kds.apply(t,
		kds.op("kot.status_changed", map[string]any{"order_id": order, "kot_id": kot1, "status": "ready", "at": dinner}),
		kds.op("kot.status_changed", map[string]any{"order_id": order, "kot_id": kot1, "status": "preparing", "at": dinner}),
	)
	if res.Acks[1].Status != "duplicate" {
		t.Fatalf("a backwards KOT status must change nothing: %+v", res.Acks[1])
	}

	// The till bootstraps and sees exactly the captain's table.
	boot := till.pull(t, 0, 0)
	o, ok := boot.find(order.String())
	if !ok || o.Status != "open" || len(o.Lines) != 3 || len(o.KOTs) != 2 || boot.Cursor == 0 {
		t.Fatalf("bootstrap: %+v", boot)
	}
	for _, kt := range o.KOTs {
		if (kt.ID == kot1.String() && kt.Status != "ready") || (kt.ID == kot2.String() && kt.Status != "new") {
			t.Fatalf("KOT statuses: %+v", o.KOTs)
		}
	}

	// A burnt naan is voided with the manager's approval.
	till.apply(t, till.op("order.line_voided", map[string]any{"order_id": order, "line_id": naan.ID,
		"reason": "burnt", "approved_by": "Manager Ravi", "at": dinner}))
	inc := captain.pull(t, boot.Cursor, 0)
	if o, ok := inc.find(order.String()); !ok || o.lineStatus(naan.ID.String()) != "voided" || inc.Cursor <= boot.Cursor {
		t.Fatalf("incremental pull should carry the void: %+v", inc)
	}

	// Split bill: the paneer first, then the lassi.
	till.apply(t, till.billFor(t, order, paneer))
	if o := till.order(t, order.String()); o.Status != "open" || o.lineStatus(paneer.ID.String()) != "billed" {
		t.Fatalf("after the first split the order stays open: %+v", o)
	}
	b2 := till.billFor(t, order, lassi)
	till.apply(t, b2)
	final := till.order(t, order.String())
	if final.Status != "closed" || final.lineStatus(lassi.ID.String()) != "billed" {
		t.Fatalf("after the last live line is billed the order closes: %+v", final)
	}
	if v := till.getBill(t, b2); v.Verification != "match" || v.LedgerStatus != "posted" {
		t.Fatalf("split bill: %+v", v)
	}
	if got := till.alerts(t); got["bill_order_mismatch"]+got["void_after_kot_without_approval"] != 0 {
		t.Fatalf("a clean evening raises no alerts, got %v", got)
	}
	var tb struct {
		Balanced bool `json:"balanced"`
	}
	must(t, http.StatusOK, "GET", "/v1/ledger/trial-balance", till.OwnerToken, nil, &tb)
	if !tb.Balanced {
		t.Fatal("books must balance")
	}
}

// TestMergeForwardsLateOps: the till merges T5 into T4 while a captain, who
// has not pulled yet, keeps adding to T5. Nothing is lost; it lands on T4.
func TestMergeForwardsLateOps(t *testing.T) {
	till := newRestaurant(t, "Merge House", "", "29")
	k := setupFloor(t, till)
	captain := till.pair(t, "captain", "Captain Sita")

	a, b := ids.New(), ids.New()
	x, y, z := newLine(k.Paneer, "Paneer tikka", 1), newLine(k.Naan, "Butter naan", 2), newLine(k.Lassi, "Sweet lassi", 1)
	till.apply(t, till.op("order.opened", opened(a, k.T4, 2)), till.op("order.lines_added", added(a, x)))
	captain.apply(t, captain.op("order.opened", opened(b, k.T5, 2)), captain.op("order.lines_added", added(b, y)))
	cursor := captain.pull(t, 0, 0).Cursor

	till.apply(t, till.op("order.merged", map[string]any{"target_order_id": a, "source_order_ids": []uuid.UUID{b}, "at": dinner}))
	kot := ids.New()
	captain.apply(t,
		captain.op("order.lines_added", added(b, z)),               // aimed at the merged-away order
		captain.op("kot.fired", fired(b, kot, "003-K1", k.Bar, z)), // same
	)

	merged := till.order(t, a.String())
	if len(merged.Lines) != 3 || merged.Covers != 4 || merged.lineStatus(z.ID.String()) != "fired" || len(merged.KOTs) != 1 {
		t.Fatalf("T4 should hold all three lines and the late KOT: %+v", merged)
	}
	if src := till.order(t, b.String()); src.Status != "merged" || src.MergedInto == nil || *src.MergedInto != a.String() {
		t.Fatalf("T5 order should be a tombstone pointing at T4: %+v", src)
	}
	inc := captain.pull(t, cursor, 0)
	if _, ok := inc.find(b.String()); !ok {
		t.Fatal("the captain must learn about the tombstone")
	}
	if o, ok := inc.find(a.String()); !ok || len(o.Lines) != 3 {
		t.Fatal("...and about the order that now holds its lines")
	}

	// The till bills everything against the stale id; it still lands on T4.
	till.apply(t, till.billFor(t, b, x, y, z))
	if o := till.order(t, a.String()); o.Status != "closed" {
		t.Fatalf("billing through the tombstone must close T4: %s", o.Status)
	}
}

// TestLateItemsGoToSpillover: a dessert is added to a table that already
// paid. The guest ate it, so it must be billed: a new order opens on the same
// table and a human is told.
func TestLateItemsGoToSpillover(t *testing.T) {
	till := newRestaurant(t, "Late Night", "", "29")
	k := setupFloor(t, till)
	captain := till.pair(t, "captain", "Captain Ravi")

	o := ids.New()
	x := newLine(k.Paneer, "Paneer tikka", 1)
	till.apply(t, till.op("order.opened", opened(o, k.T4, 2)), till.op("order.lines_added", added(o, x)))
	till.apply(t, till.billFor(t, o, x))

	dessert := newLine(k.Lassi, "Sweet lassi", 1)
	kot := ids.New()
	captain.apply(t,
		captain.op("order.lines_added", added(o, dessert)),
		captain.op("kot.fired", fired(o, kot, "004-K9", k.Bar, dessert)),
	)
	if got := till.order(t, o.String()); got.Status != "closed" || len(got.Lines) != 1 {
		t.Fatalf("the paid order stays closed and untouched: %+v", got)
	}
	var open struct {
		Orders []orderDoc `json:"orders"`
	}
	must(t, http.StatusOK, "GET", "/v1/outlets/"+till.OutletID+"/orders", till.OwnerToken, nil, &open)
	if len(open.Orders) != 1 {
		t.Fatalf("exactly one open order (the spillover), got %d", len(open.Orders))
	}
	sp := open.Orders[0]
	if sp.SpilloverOf == nil || *sp.SpilloverOf != o.String() || sp.TableID == nil || *sp.TableID != k.T4 ||
		sp.lineStatus(dessert.ID.String()) != "fired" {
		t.Fatalf("spillover should be on T4 with the fired dessert: %+v", sp)
	}
	if till.alerts(t)["late_items"] != 1 {
		t.Fatal("late items must raise an alert")
	}
	// The till bills the dessert against the original order id; it finds the spillover.
	till.apply(t, till.billFor(t, o, dessert))
	if got := till.order(t, sp.ID); got.Status != "closed" {
		t.Fatalf("spillover should close once billed: %s", got.Status)
	}
	if till.alerts(t)["bill_order_mismatch"] != 0 {
		t.Fatal("billing the spillover through the original id is not a mismatch")
	}
}

// TestOrderOpsAreIdempotent: every op can be re-sent, as-is or re-queued
// under a new op id, without doubling anything.
func TestOrderOpsAreIdempotent(t *testing.T) {
	till := newRestaurant(t, "Retry Cafe", "", "29")
	k := setupFloor(t, till)
	o, kot := ids.New(), ids.New()
	a, b := newLine(k.Paneer, "Paneer tikka", 1), newLine(k.Naan, "Butter naan", 2)
	ops := []syncgw.Op{
		till.op("order.opened", opened(o, k.T4, 2)),
		till.op("order.lines_added", added(o, a, b)),
		till.op("kot.fired", fired(o, kot, "001-K1", k.Tandoor, a, b)),
		till.op("order.line_voided", map[string]any{"order_id": o, "line_id": b.ID, "reason": "wrong item", "approved_by": "Mgr", "at": dinner}),
	}
	till.apply(t, ops...)
	for i, ack := range till.push(t, ops...).Acks {
		if ack.Status != "duplicate" {
			t.Fatalf("re-sent op %d: %+v", i, ack)
		}
	}
	// Same content, new op ids (the device re-queued them).
	requeued := till.apply(t,
		till.op("order.opened", opened(o, k.T4, 2)),
		till.op("order.lines_added", added(o, a, b)),
		till.op("kot.fired", fired(o, kot, "001-K1", k.Tandoor, a, b)),
		till.op("order.line_voided", map[string]any{"order_id": o, "line_id": b.ID, "reason": "wrong item", "at": dinner}),
	)
	for i, ack := range requeued.Acks {
		if ack.Status != "duplicate" {
			t.Fatalf("re-queued op %d should change nothing: %+v", i, ack)
		}
	}
	got := till.order(t, o.String())
	if len(got.Lines) != 2 || len(got.KOTs) != 1 || got.Version != 4 {
		t.Fatalf("exactly 2 lines, 1 KOT and 4 versions (open, add, fire, void): %+v", got)
	}
}

// TestConcurrentCaptainsAddLines: five captains add to one table at the same
// moment. Every line lands once.
func TestConcurrentCaptainsAddLines(t *testing.T) {
	till := newRestaurant(t, "Busy Bistro", "", "29")
	k := setupFloor(t, till)
	o := ids.New()
	till.apply(t, till.op("order.opened", opened(o, k.T4, 10)))

	var captains []*fixture
	for i := 0; i < 5; i++ {
		captains = append(captains, till.pair(t, "captain", "Captain"))
	}
	var wg sync.WaitGroup
	errs := make(chan string, len(captains))
	for _, c := range captains {
		op := c.op("order.lines_added", added(o, newLine(k.Naan, "Naan", 1), newLine(k.Lassi, "Lassi", 1), newLine(k.Paneer, "Paneer", 1)))
		wg.Add(1)
		go func(token string, op syncgw.Op) {
			defer wg.Done()
			status, res, err := pushRaw(token, op)
			if err != nil || status != http.StatusOK || len(res.Acks) != 1 || res.Acks[0].Status != "applied" {
				errs <- "push failed"
			}
		}(c.DeviceToken, op)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	got := till.order(t, o.String())
	if len(got.Lines) != 15 || got.Version != 6 {
		t.Fatalf("15 lines, version 6 (open + 5 adds), got %d lines, version %d", len(got.Lines), got.Version)
	}
}

// TestPullPagingAndIsolation: paging returns every changed order exactly
// once, cursors only move forward, and nothing leaks across tenants.
func TestPullPagingAndIsolation(t *testing.T) {
	till := newRestaurant(t, "Pager Inn", "", "29")
	k := setupFloor(t, till)
	first := ids.New()
	till.apply(t, till.op("order.opened", opened(first, k.T4, 1)))
	cursor := till.pull(t, 0, 0).Cursor

	var created []uuid.UUID
	for i := 0; i < 5; i++ {
		o := ids.New()
		created = append(created, o)
		till.apply(t, till.op("order.opened", map[string]any{"order_id": o, "channel": "takeaway", "opened_at": dinner}))
	}
	seen := map[string]int{}
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("paging does not terminate")
		}
		page := till.pull(t, cursor, 2)
		if page.Cursor < cursor || len(page.Orders) > 2 {
			t.Fatalf("bad page: cursor %d -> %d, %d orders", cursor, page.Cursor, len(page.Orders))
		}
		for _, o := range page.Orders {
			seen[o.ID]++
		}
		cursor = page.Cursor
		if !page.HasMore {
			break
		}
	}
	for _, o := range created {
		if seen[o.String()] != 1 {
			t.Fatalf("order %s seen %d times across pages", o, seen[o.String()])
		}
	}
	if len(seen) != 5 {
		t.Fatalf("only the 5 new orders changed after the cursor, saw %d", len(seen))
	}
	if again := till.pull(t, cursor, 0); len(again.Orders) != 0 || again.Cursor != cursor {
		t.Fatalf("nothing changed since: %+v", again)
	}

	other := newRestaurant(t, "Other Tenant", "", "29")
	if leak := other.pull(t, 0, 0); len(leak.Orders) != 0 {
		t.Fatal("another tenant's device must see none of these orders")
	}
	if leak := other.pull(t, 1, 0); len(leak.Orders) != 0 {
		t.Fatal("...on incremental pulls either")
	}
}

// TestOrderOpValidation: the ways an order op can be wrong.
func TestOrderOpValidation(t *testing.T) {
	till := newRestaurant(t, "Strict Kitchen", "", "29")
	k := setupFloor(t, till)
	kds := till.pair(t, "kds", "Screen")
	o := ids.New()
	x := newLine(k.Paneer, "Paneer tikka", 1)
	till.apply(t, till.op("order.opened", opened(o, k.T4, 2)), till.op("order.lines_added", added(o, x)))

	bogusItem := newLine(menuItem{ID: ids.New().String(), Price: 100, Section: k.Tandoor}, "Ghost", 1)
	v4Line := newLine(k.Naan, "Naan", 1)
	v4Line.Payload["line_id"] = uuid.New() // version 4, not 7

	cases := []struct {
		name string
		dev  *fixture
		op   syncgw.Op
		want string
	}{
		{"kitchen screens cannot open orders", kds, kds.op("order.opened", opened(ids.New(), k.T4, 1)), "role_not_allowed"},
		{"unknown table", till, till.op("order.opened", opened(ids.New(), ids.New().String(), 1)), "unknown_table"},
		{"unknown item", till, till.op("order.lines_added", added(o, bogusItem)), "unknown_item"},
		{"ids must be UUIDv7", till, till.op("order.lines_added", added(o, v4Line)), "bad_payload"},
		{"unknown order", till, till.op("order.lines_added", added(ids.New(), newLine(k.Naan, "Naan", 1))), "unknown_order"},
		{"firing a line not on the order", till, till.op("kot.fired", fired(o, ids.New(), "K1", k.Tandoor, newLine(k.Naan, "Naan", 1))), "unknown_line"},
	}
	for _, c := range cases {
		ack := c.dev.push(t, c.op).Acks[0]
		if ack.Status != "rejected" || ack.Error == nil || ack.Error.Code != c.want {
			t.Errorf("%s: got %+v, want rejected %s", c.name, ack, c.want)
		}
	}

	// Voiding a billed line is refused; a bill naming the wrong number of lines is kept but flagged.
	till.apply(t, till.billFor(t, o, x))
	ack := till.push(t, till.op("order.line_voided", map[string]any{"order_id": o, "line_id": x.ID, "reason": "late", "at": dinner})).Acks[0]
	if ack.Status != "rejected" || ack.Error.Code != "line_already_billed" {
		t.Fatalf("void after billing: %+v", ack)
	}
	o2 := ids.New()
	y := newLine(k.Lassi, "Lassi", 1)
	till.apply(t, till.op("order.opened", opened(o2, k.T5, 1)), till.op("order.lines_added", added(o2, y)))
	in := pricing.Input{PriceMode: pricing.Exclusive, Supply: pricing.Intra, Rounding: pricing.NearestRupee,
		Lines: []pricing.Line{y.Bill}}
	till.apply(t, till.bill(t, in, billOpts{orderID: &o2, lineIDs: []uuid.UUID{y.ID, ids.New()}}))
	if till.alerts(t)["bill_order_mismatch"] != 1 {
		t.Fatal("a bill whose line list does not match must be flagged")
	}
}
