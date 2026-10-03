package e2e

// Adversarial review of the stock and recipes slice. Every test here names a
// defect and fails against the code as reviewed; it should pass once the
// defect is fixed (whichever reasonable fix is chosen).

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"1xn/core/internal/platform/gst"
	"1xn/core/internal/platform/ids"
	"1xn/core/modules/stock"
)

// reviewCall posts without failing the test and returns the status and the
// raw body, so the caller can judge the outcome.
func reviewCall(t *testing.T, f *fixture, method, path string, body any) (int, json.RawMessage) {
	t.Helper()
	var raw json.RawMessage
	status, _ := call(t, method, path, f.OwnerToken, body, &raw)
	return status, raw
}

func reviewLockSeptember(t *testing.T, f *fixture) {
	t.Helper()
	if _, err := ownerDB.Exec(`INSERT INTO period (id, tenant_id, legal_entity_id, starts_on, ends_on, status)
		VALUES ($1, $2, $3, '2026-09-01', '2026-10-01', 'locked')`, ids.New(), f.TenantID, f.LegalEntityID); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// S1. An owner document (purchase, wastage, count) dated in a locked
// accounting month is accepted (201, ledger_status "blocked"): the stock and
// its value move, but nothing is posted, so the sum of stock_balance.value no
// longer equals account 1300, for good (there is no way to post it later).
// For a purchase on credit the supplier payable is lost from the books too.
// Unlike a bill, an owner document can simply be refused: the owner picks
// the date.
// ---------------------------------------------------------------------------
func TestReviewS1OwnerDocumentInLockedMonthMovesStockOutsideTheBooks(t *testing.T) {
	t.Run("purchase", func(t *testing.T) {
		f := newRestaurant(t, "Review Locked Purchase", "", "29")
		flour := f.stockItem(t, "Flour", "kg", 0)
		reviewLockSeptember(t, f)
		status, raw := reviewCall(t, f, "POST", "/v1/outlets/"+f.OutletID+"/stock/purchases", map[string]any{
			"id": ids.New(), "supplier": "Metro", "date": "2026-09-28", "paid_by": "credit",
			"lines": []map[string]any{{"stock_item_id": flour, "qty": 10_000, "cost": 45_000}},
		})
		if status >= 400 && status < 500 {
			return // refused: the owner re-dates it into an open month
		}
		if status != http.StatusCreated {
			t.Fatalf("purchase into a locked month: %d %s", status, raw)
		}
		bal := reviewBalances(t, f)
		if q := f.stockLevels(t)["Flour"]; q.Value != bal["1300"] || bal["2100"] != -q.Value {
			t.Fatalf("purchase accepted as %s: flour worth %d is in stock, but account 1300 is %d and payable to suppliers is %d",
				raw, q.Value, bal["1300"], bal["2100"])
		}
	})
	t.Run("wastage", func(t *testing.T) {
		f := newRestaurant(t, "Review Locked Wastage", "", "29")
		flour := f.stockItem(t, "Flour", "kg", 0)
		f.purchase(t, http.StatusCreated, ids.New(), "2026-10-01", pl{flour, 10_000, 45_000}) // October is open
		reviewLockSeptember(t, f)
		status, raw := reviewCall(t, f, "POST", "/v1/outlets/"+f.OutletID+"/stock/wastage", map[string]any{
			"id": ids.New(), "stock_item_id": flour, "qty": 2_000, "reason": "weevils", "date": "2026-09-30",
		})
		if status >= 400 && status < 500 {
			return
		}
		if status != http.StatusCreated {
			t.Fatalf("wastage into a locked month: %d %s", status, raw)
		}
		checkStockBooks(t, f) // fails: 1300 still holds the wasted flour
	})
}

// ---------------------------------------------------------------------------
// S2. A bill for a locked month (kept, ADR 0007) still uses up its recipes'
// stock, but the stock entry is "blocked" like the bill's own entry: stock
// value drops and account 1300 does not, so the books and the stock disagree
// from then on. Docs 08 and ADR 0016 promise they always agree. Nothing ever
// posts the blocked stock entry later (and the Z report's ledger_blocked
// check counts bills only).
// ---------------------------------------------------------------------------
func TestReviewS2BillInLockedMonthUsesStockButBooksNothing(t *testing.T) {
	f := newRestaurant(t, "Review Locked Bill", "", "29")
	potatoes := f.stockItem(t, "Potatoes", "kg", 0)
	f.recipe(t, f.ItemID, potatoes, 250)
	f.purchase(t, http.StatusCreated, ids.New(), "2026-10-01", pl{potatoes, 10_000, 40_000})
	reviewLockSeptember(t, f)
	f.apply(t, f.bill(t, f.pav(4), billOpts{})) // a till that was offline since 28 Sep: uses 1 kg
	checkStockBooks(t, f)
}

// ---------------------------------------------------------------------------
// S3. A count can be dated in the past (the count form has a date picker),
// but its difference is worked out against TODAY's books, not the books on
// that date. A count of 5 kg taken on the 27th, entered after the 28th's
// bills used 1 kg, "finds" 1 kg that is not there: stock is overstated and
// count differences understated, silently.
// ---------------------------------------------------------------------------
func TestReviewS3BackdatedCountIsComparedWithTodaysBooks(t *testing.T) {
	f := newRestaurant(t, "Review Backdated Count", "", "29")
	paneer := f.stockItem(t, "Paneer", "kg", 0)
	f.recipe(t, f.ItemID, paneer, 200)
	f.purchase(t, http.StatusCreated, ids.New(), "2026-09-27", pl{paneer, 5_000, 160_000})
	f.apply(t, f.bill(t, f.pav(5), billOpts{})) // 28 Sep: 1 kg used, 4 kg left
	// The night count of the 27th: 5 kg on the shelf, exactly what the books
	// said that night. Entered a day late.
	status, raw := reviewCall(t, f, "POST", "/v1/outlets/"+f.OutletID+"/stock/counts", map[string]any{
		"id": ids.New(), "date": "2026-09-27", "note": "night count, entered late",
		"lines": []map[string]any{{"stock_item_id": paneer, "counted": 5_000}},
	})
	if status >= 400 && status < 500 {
		return // refusing backdated counts is a fix too
	}
	if status != http.StatusCreated {
		t.Fatalf("count: %d %s", status, raw)
	}
	if p := f.stockLevels(t)["Paneer"]; p.Qty != 4_000 {
		t.Fatalf("a count that matched the books of its own day moved the stock from 4 kg to %d g (%s)", p.Qty, raw)
	}
}

// ---------------------------------------------------------------------------
// S4. "Paid by cash" credits 1100 Cash in hand, the till drawers' account,
// but no shift knows about it. A vegetable vendor paid from the drawer is
// booked twice: once by the purchase, once more as the drawer's shortage at
// shift close (or as a petty expense if the cashier records it). Rs 500 left
// the business; the books say Rs 1,000, and the drawer shows Rs 500 short.
// ---------------------------------------------------------------------------
func TestReviewS4CashPurchaseFromTheDrawerIsBookedTwice(t *testing.T) {
	till := newRestaurant(t, "Review Cash Purchase", "", "29")
	tomatoes := till.stockItem(t, "Tomatoes", "kg", 0)
	s := ids.New()
	till.apply(t, till.openShift(s, at(11, 0), "Asha", 200_000))
	// 12:00: the vendor delivers 12.5 kg and is paid Rs 500 from the drawer.
	// The owner books the delivery in the back office, paid by cash.
	status, raw := reviewCall(t, till, "POST", "/v1/outlets/"+till.OutletID+"/stock/purchases", map[string]any{
		"id": ids.New(), "supplier": "Sharma Vegetables", "date": "2026-09-28", "paid_by": "cash",
		"lines": []map[string]any{{"stock_item_id": tomatoes, "qty": 12_500, "cost": 50_000}},
	})
	if status >= 400 && status < 500 {
		return // refused: a cash payment must name the drawer it came from
	}
	if status != http.StatusCreated {
		t.Fatalf("purchase: %d %s", status, raw)
	}
	till.apply(t, till.closeShift(s, at(16, 0), 150_000, notes(150_000))) // the drawer is Rs 500 lighter
	bal := reviewBalances(t, till)
	if cash := bal["1100"] + bal["1105"]; cash != -50_000 {
		t.Fatalf("Rs 500 of cash paid once, but cash in hand and safe moved by %d (over/short %d, stock %d)",
			cash, bal["5900"], bal["1300"])
	}
}

// ---------------------------------------------------------------------------
// S5. Marking an ingredient "not in use" leaves it in every recipe: bills
// keep using it, drive it below zero and raise negative_stock alerts, while
// the back office hides inactive items from Receive and Count, so the owner
// can neither book it in nor count it. Deactivating should be refused while
// recipes use it (or bills should stop using it).
// ---------------------------------------------------------------------------
func TestReviewS5DeactivatedItemIsStillUsedByRecipes(t *testing.T) {
	f := newRestaurant(t, "Review Inactive Item", "", "29")
	cream := f.stockItem(t, "Cream", "l", 0)
	f.recipe(t, f.ItemID, cream, 50)
	status, raw := reviewCall(t, f, "PATCH", "/v1/stock/items/"+cream, map[string]any{"active": false})
	if status >= 400 && status < 500 {
		return // refused while a recipe uses it
	}
	if status != http.StatusNoContent {
		t.Fatalf("deactivate: %d %s", status, raw)
	}
	f.apply(t, f.bill(t, f.pav(2), billOpts{}))
	if c := f.stockLevels(t)["Cream"]; c.Qty != 0 || f.alerts(t)["negative_stock"] != 0 {
		t.Fatalf("an item marked not in use was used by a bill: %+v, alerts %v", c, f.alerts(t))
	}
}

// ---------------------------------------------------------------------------
// S6. Wastage of stock that was never booked in leaves at the last price,
// which for a new item is zero. When the delivery is booked, the true-up of
// that deficit goes to 5100 Cost of food sold, not 5110 Food wastage. The
// leak shows up as food cost and the wastage report says nothing.
// ---------------------------------------------------------------------------
func TestReviewS6WastageBeforeTheFirstPurchaseIsReportedAsFoodCost(t *testing.T) {
	f := newRestaurant(t, "Review Early Wastage", "", "29")
	tomatoes := f.stockItem(t, "Tomatoes", "kg", 0)
	// The morning's tomatoes rot before anyone books the delivery.
	status, raw := reviewCall(t, f, "POST", "/v1/outlets/"+f.OutletID+"/stock/wastage", map[string]any{
		"id": ids.New(), "stock_item_id": tomatoes, "qty": 1_000, "reason": "rotten", "date": "2026-09-28",
	})
	if status >= 400 && status < 500 {
		return // refused: wastage beyond what is booked in
	}
	if status != http.StatusCreated {
		t.Fatalf("wastage: %d %s", status, raw)
	}
	f.purchase(t, http.StatusCreated, ids.New(), "2026-09-28", pl{tomatoes, 5_000, 20_000}) // Rs 40/kg
	bal := reviewBalances(t, f)
	var fc stock.FoodCost
	must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/stock/food-cost?from=2026-09-28&to=2026-09-28", f.OwnerToken, nil, &fc)
	if bal["5110"] != 4_000 || bal["5100"] != 0 || fc.Wastage != 4_000 || fc.FoodCost != 0 {
		t.Fatalf("1 kg wasted at Rs 40/kg, nothing sold: food wastage %d, cost of food sold %d; report wastage %d, food cost %d",
			bal["5110"], bal["5100"], fc.Wastage, fc.FoodCost)
	}
}

// ---------------------------------------------------------------------------
// S7. An opening count (the natural first step for a restaurant starting on
// 1x.n: count what is on the shelves) values the stock at zero, because an
// item never bought has no cost. The stock is not in 1300, the dishes that
// use it cost nothing, and after the first delivery the average cost is
// diluted by the free opening stock: food cost is understated for weeks,
// silently.
// ---------------------------------------------------------------------------
func TestReviewS7OpeningCountIsValuedAtZero(t *testing.T) {
	f := newRestaurant(t, "Review Opening Count", "", "29")
	rice := f.stockItem(t, "Rice", "kg", 0)
	status, raw := reviewCall(t, f, "POST", "/v1/outlets/"+f.OutletID+"/stock/counts", map[string]any{
		"id": ids.New(), "note": "opening stock",
		"lines": []map[string]any{{"stock_item_id": rice, "counted": 20_000}},
	})
	if status >= 400 && status < 500 {
		// Refused: found stock with no known cost needs a price. With one,
		// the opening stock enters the books at that price.
		must(t, http.StatusCreated, "POST", "/v1/outlets/"+f.OutletID+"/stock/counts", f.OwnerToken, map[string]any{
			"id": ids.New(), "note": "opening stock",
			"lines": []map[string]any{{"stock_item_id": rice, "counted": 20_000, "unit_cost": 6_000}},
		}, nil)
		f.purchase(t, http.StatusCreated, ids.New(), "2026-09-28", pl{rice, 20_000, 120_000})
		if r := f.stockLevels(t)["Rice"]; r.UnitCost != 6_000 || r.Value != 240_000 {
			t.Fatalf("opening 20 kg at Rs 60/kg, then 20 kg bought at Rs 60/kg: %+v", r)
		}
		checkStockBooks(t, f)
		return
	}
	if status != http.StatusCreated {
		t.Fatalf("count: %d %s", status, raw)
	}
	f.purchase(t, http.StatusCreated, ids.New(), "2026-09-28", pl{rice, 20_000, 120_000}) // Rs 60/kg
	if r := f.stockLevels(t)["Rice"]; r.UnitCost != 6_000 {
		t.Fatalf("20 kg counted at no cost, then 20 kg bought at Rs 60/kg: average cost is %d paise/kg, want 6000 (%s)", r.UnitCost, raw)
	}
}

// ---------------------------------------------------------------------------
// S8. Replaying a document id with DIFFERENT content answers 200 with the
// first document. The back office ignores the answer and tells the owner
// "booked", so a corrected resubmission after a lost response (same form,
// same id) is silently dropped. An idempotency key reused with another
// payload should be refused (Stripe answers 400/409).
// ---------------------------------------------------------------------------
func TestReviewS8ReplayWithDifferentContentIsSilentlyDropped(t *testing.T) {
	f := newRestaurant(t, "Review Replay", "", "29")
	flour := f.stockItem(t, "Flour", "kg", 0)
	sugar := f.stockItem(t, "Sugar", "kg", 0)
	id := ids.New()
	f.purchase(t, http.StatusCreated, id, "2026-09-28", pl{flour, 1_000, 4_500})
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	status, _ := call(t, "POST", "/v1/outlets/"+f.OutletID+"/stock/purchases", f.OwnerToken, map[string]any{
		"id": id, "supplier": "Fresh Farms", "invoice_no": "FF-101", "date": "2026-09-28", "paid_by": "safe",
		"lines": []map[string]any{{"stock_item_id": sugar, "qty": 5_000, "cost": 22_500}},
	}, &e)
	if status != http.StatusConflict || e.Error.Code != "id_used" {
		t.Fatalf("same id, different delivery (5 kg sugar): %d %s, and the sugar was never booked (stock %d)",
			status, e.Error.Code, f.stockLevels(t)["Sugar"].Qty)
	}
}

// ---------------------------------------------------------------------------
// S9. Two saves of one dish's recipe at the same moment (owner and manager)
// fail with a 500: the second transaction's DELETE does not see the first's
// new lines, and its INSERT hits the primary key (23505, not mapped).
// ---------------------------------------------------------------------------
func TestReviewS9ConcurrentRecipeSavesFailWith500(t *testing.T) {
	f := newRestaurant(t, "Review Recipe Race", "", "29")
	oil := f.stockItem(t, "Oil", "l", 0)
	f.recipe(t, f.ItemID, oil, 20)

	// Hold the current recipe line so both saves queue on it, then let go.
	gate, err := ownerDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Exec(`SELECT 1 FROM recipe_line WHERE menu_item_id = $1 FOR UPDATE`, f.ItemID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], _ = call(t, "PUT", "/v1/stock/recipes/"+f.ItemID, f.OwnerToken,
				map[string]any{"lines": []map[string]any{{"stock_item_id": oil, "qty": 30 + i}}}, nil)
		}(i)
	}
	waitForLockWaiters(t, 2)
	if err := gate.Commit(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	for _, c := range codes {
		if c != http.StatusNoContent && c != http.StatusConflict {
			t.Fatalf("two saves of one recipe at once: statuses %v (want 204, or a 409 for the loser)", codes)
		}
	}
}

func waitForLockWaiters(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if err := ownerDB.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database()`).
			Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d transactions to queue on the lock", n)
}

// ---------------------------------------------------------------------------
// S10. Values that do not fit int64 wrap silently: mulDiv's big.Int result
// is truncated by Int64(). With inputs the API accepts (1 g bought for
// Rs 1,000 crore, then 123,456.789012 tonnes wasted, both within maxQty and
// maxCost), the wastage is valued at a NEGATIVE Rs 7.3e16: food wastage is
// credited and the stock's value jumps, and the API answers 201.
// ---------------------------------------------------------------------------
func TestReviewS10HugeQuantitiesWrapToNegativeValues(t *testing.T) {
	f := newRestaurant(t, "Review Overflow", "", "29")
	saffron := f.stockItem(t, "Saffron", "kg", 0)
	f.purchase(t, http.StatusCreated, ids.New(), "2026-09-28", pl{saffron, 1, 1_000_000_000_000})
	status, raw := reviewCall(t, f, "POST", "/v1/outlets/"+f.OutletID+"/stock/wastage", map[string]any{
		"id": ids.New(), "stock_item_id": saffron, "qty": 123_456_789_012, "reason": "typo", "date": "2026-09-28",
	})
	if status >= 400 && status < 500 {
		return // refused: the value cannot be represented
	}
	var doc stock.Doc
	_ = json.Unmarshal(raw, &doc)
	if doc.Total < 0 || reviewBalances(t, f)["5110"] < 0 {
		t.Fatalf("wastage accepted (%d) with a negative value %d: food wastage account %d",
			status, doc.Total, reviewBalances(t, f)["5110"])
	}
}

// ---------------------------------------------------------------------------
// S14. ADR 0016: "A recipe change affects future bills only." But a bill uses
// the recipe in force when it SYNCS, not when it was issued. Offline-first
// tills sync hours late, so a bill printed before the owner changed a recipe
// uses up the new recipe's quantities.
// ---------------------------------------------------------------------------
func TestReviewS14RecipeChangeRewritesBillsIssuedBeforeIt(t *testing.T) {
	f := newRestaurant(t, "Review Recipe Change", "", "29")
	rice := f.stockItem(t, "Rice", "kg", 0)
	f.purchase(t, http.StatusCreated, ids.New(), "", pl{rice, 10_000, 60_000})
	f.recipe(t, f.ItemID, rice, 100)
	op := f.bill(t, f.pav(2), billOpts{issuedAt: time.Now().In(gst.IST).Truncate(time.Second)}) // printed now, offline
	time.Sleep(20 * time.Millisecond)
	f.recipe(t, f.ItemID, rice, 300) // the owner changes the recipe afterwards
	f.apply(t, op)                   // the till comes back online
	if q := f.stockLevels(t)["Rice"].Qty; q != 10_000-2*100 {
		t.Fatalf("2 portions printed under the 100 g recipe used %d g", 10_000-q)
	}
}
