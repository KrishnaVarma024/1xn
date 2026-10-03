package e2e

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"1xn/core/internal/platform/gst"
	"1xn/core/internal/platform/ids"
	"1xn/core/modules/dayclose"
	"1xn/core/modules/sales/pricing"
	"1xn/core/modules/stock"
)

// pav is pav bhaji as a real till bills it: the line's ref is the menu
// item's id, which is how a bill finds the dish's recipe.
func (f *fixture) pav(qty int64) pricing.Input {
	in := pavBhaji(qty)
	in.Lines[0].Ref = f.ItemID
	return in
}

func (f *fixture) stockItem(t *testing.T, name, unit string, reorder int64) string {
	t.Helper()
	var it stock.Item
	must(t, http.StatusCreated, "POST", "/v1/stock/items", f.OwnerToken,
		map[string]any{"name": name, "unit": unit, "reorder_level": reorder}, &it)
	return it.ID.String()
}

func (f *fixture) recipe(t *testing.T, dish string, lines ...any) {
	t.Helper()
	var ls []map[string]any
	for i := 0; i < len(lines); i += 2 {
		ls = append(ls, map[string]any{"stock_item_id": lines[i], "qty": lines[i+1]})
	}
	must(t, http.StatusNoContent, "PUT", "/v1/stock/recipes/"+dish, f.OwnerToken, map[string]any{"lines": ls}, nil)
}

type pl struct {
	item      string
	qty, cost int64
}

func (f *fixture) purchase(t *testing.T, want int, id uuid.UUID, date string, lines ...pl) stock.Doc {
	t.Helper()
	var ls []map[string]any
	for _, l := range lines {
		ls = append(ls, map[string]any{"stock_item_id": l.item, "qty": l.qty, "cost": l.cost})
	}
	var doc stock.Doc
	must(t, want, "POST", "/v1/outlets/"+f.OutletID+"/stock/purchases", f.OwnerToken, map[string]any{
		"id": id, "supplier": "Fresh Farms", "invoice_no": "FF-101", "date": date, "paid_by": "safe", "lines": ls,
	}, &doc)
	return doc
}

func (f *fixture) stockLevels(t *testing.T) map[string]stock.Item {
	t.Helper()
	var res struct {
		Items []stock.Item `json:"items"`
		Value int64        `json:"value"`
	}
	must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/stock", f.OwnerToken, nil, &res)
	out := map[string]stock.Item{}
	var sum int64
	for _, i := range res.Items {
		out[i.Name] = i
		sum += i.Value
	}
	if sum != res.Value {
		t.Fatalf("stock value %d is not the sum of the items %d", res.Value, sum)
	}
	return out
}

// checkStockBooks: for every item, the moves add up to the balance, and all
// balances together are exactly account 1300 Stock.
func checkStockBooks(t *testing.T, f *fixture) {
	t.Helper()
	var bad int
	if err := ownerDB.QueryRow(`
		SELECT count(*) FROM stock_balance b
		LEFT JOIN (SELECT outlet_id, stock_item_id, sum(qty) q, sum(value) v FROM stock_move GROUP BY 1, 2) m
		       USING (outlet_id, stock_item_id)
		WHERE b.tenant_id = $1 AND (b.qty <> coalesce(m.q, 0) OR b.value <> coalesce(m.v, 0))`, f.TenantID).Scan(&bad); err != nil {
		t.Fatal(err)
	}
	if bad != 0 {
		t.Fatalf("%d stock balances do not match their moves", bad)
	}
	var total int64
	if err := ownerDB.QueryRow(`SELECT coalesce(sum(value), 0) FROM stock_balance WHERE tenant_id = $1`, f.TenantID).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if bal := reviewBalances(t, f); bal["1300"] != total {
		t.Fatalf("account 1300 Stock is %d but the stock is worth %d", bal["1300"], total)
	}
}

// TestStockStory is a kitchen's day: buy, sell (recipes use up stock), go
// low, throw something away, count, sell more than the books hold, and buy
// again at a higher price. The books follow every step.
func TestStockStory(t *testing.T) {
	f := newRestaurant(t, "Stock Story", "", "29")
	potatoes := f.stockItem(t, "Potatoes", "kg", 2_000)
	pav := f.stockItem(t, "Pav buns", "pc", 10_000)
	butter := f.stockItem(t, "Butter", "kg", 0)
	f.recipe(t, f.ItemID, potatoes, 150, pav, 2_000, butter, 20)

	buy := ids.New()
	doc := f.purchase(t, http.StatusCreated, buy, "2026-09-28",
		pl{potatoes, 5_000, 20_000}, pl{pav, 20_000, 10_000}, pl{butter, 1_000, 50_000})
	if doc.Total != 80_000 || doc.Lines != 3 || doc.LedgerStatus != "posted" {
		t.Fatalf("purchase: %+v", doc)
	}
	// A double click: same id, same document, nothing booked twice.
	if again := f.purchase(t, http.StatusOK, buy, "2026-09-28",
		pl{potatoes, 5_000, 20_000}, pl{pav, 20_000, 10_000}, pl{butter, 1_000, 50_000}); again.Total != 80_000 {
		t.Fatalf("replay: %+v", again)
	}

	f.apply(t, f.bill(t, f.pav(2), billOpts{}))
	s := f.stockLevels(t)
	if p := s["Potatoes"]; p.Qty != 4_700 || p.Value != 18_800 || p.UnitCost != 4_000 || p.Status != "ok" {
		t.Fatalf("potatoes after 2 portions: %+v", p)
	}
	if s["Pav buns"].Qty != 16_000 || s["Butter"].Qty != 960 {
		t.Fatalf("pav %d, butter %d", s["Pav buns"].Qty, s["Butter"].Qty)
	}
	bal := reviewBalances(t, f)
	if bal["5100"] != 1_200+2_000+2_000 || bal["1300"] != 80_000-5_200 {
		t.Fatalf("books after the first bill: food cost %d, stock %d", bal["5100"], bal["1300"])
	}

	// Four more portions: 8 buns left, under the reorder level of 10.
	f.apply(t, f.bill(t, f.pav(4), billOpts{}))
	if s := f.stockLevels(t); s["Pav buns"].Status != "low" || f.alerts(t)["low_stock"] != 1 {
		t.Fatalf("pav after 6 portions: %+v, alerts %v", s["Pav buns"], f.alerts(t))
	}

	// 100 g of butter melted; the count finds 200 g of potatoes missing.
	must(t, http.StatusCreated, "POST", "/v1/outlets/"+f.OutletID+"/stock/wastage", f.OwnerToken, map[string]any{
		"id": ids.New(), "stock_item_id": butter, "qty": 100, "reason": "melted in the heat", "date": "2026-09-28",
	}, nil)
	var count struct {
		Lines []stock.CountLine `json:"lines"`
	}
	must(t, http.StatusCreated, "POST", "/v1/outlets/"+f.OutletID+"/stock/counts", f.OwnerToken, map[string]any{
		"id": ids.New(), "note": "night count", // a count is always today's
		"lines": []map[string]any{{"stock_item_id": potatoes, "counted": 3_900}},
	}, &count)
	if len(count.Lines) != 1 || count.Lines[0].Before != 4_100 || count.Lines[0].Value != 800 {
		t.Fatalf("count: %+v", count.Lines)
	}
	bal = reviewBalances(t, f)
	if bal["5110"] != 5_000 || bal["5120"] != 800 {
		t.Fatalf("wastage %d, count differences %d", bal["5110"], bal["5120"])
	}
	checkStockBooks(t, f)

	// Ten portions with 8 buns: 12 buns are sold that were never booked in.
	f.apply(t, f.bill(t, f.pav(10), billOpts{}))
	if p := f.stockLevels(t)["Pav buns"]; p.Qty != -12_000 || p.Value != -6_000 || p.Status != "negative" {
		t.Fatalf("pav sold beyond the books: %+v", p) // 12 at the last price, Rs 5 each
	}
	if f.alerts(t)["negative_stock"] != 1 {
		t.Fatalf("negative stock alert: %v", f.alerts(t))
	}
	// The supplier's buns are now Rs 6: the 12 already sold cost Rs 12 more.
	before := reviewBalances(t, f)["5100"]
	f.purchase(t, http.StatusCreated, ids.New(), "2026-09-28", pl{pav, 30_000, 18_000})
	if p := f.stockLevels(t)["Pav buns"]; p.Qty != 18_000 || p.Value != 10_800 || p.LastCost != 600 {
		t.Fatalf("pav after buying again: %+v", p)
	}
	if up := reviewBalances(t, f)["5100"] - before; up != 1_200 {
		t.Fatalf("true-up for the buns sold before they were booked in: %d, want 1200", up)
	}
	checkStockBooks(t, f)

	// The food cost report and the recipe card.
	var fc stock.FoodCost
	today := dayclose.TradingDay(time.Now()).Format(time.DateOnly)
	must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/stock/food-cost?from=2026-09-28&to="+today, f.OwnerToken, nil, &fc)
	if fc.FoodSales != 16*9_500 || fc.FoodCost != reviewBalances(t, f)["5100"] || fc.Wastage != 5_000 || fc.CountDiff != 800 ||
		fc.Purchases != 98_000 || len(fc.Dishes) != 1 || fc.Dishes[0].Qty != 16 || fc.PortionsNoRecipe != 0 {
		t.Fatalf("food cost: %+v", fc)
	}
	var rc struct {
		Dishes []stock.Dish `json:"dishes"`
	}
	must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/stock/recipes", f.OwnerToken, nil, &rc)
	if len(rc.Dishes) != 1 || len(rc.Dishes[0].Lines) != 3 || rc.Dishes[0].NetPrice != 9_500 || rc.Dishes[0].Uncosted != 0 {
		t.Fatalf("recipes: %+v", rc.Dishes)
	}
	d := rc.Dishes[0]
	if d.Cost != d.Lines[0].Cost+d.Lines[1].Cost+d.Lines[2].Cost || d.Margin != d.NetPrice-d.Cost || d.FoodCostBP <= 0 {
		t.Fatalf("dish costing: %+v", d)
	}

	// An item's history, newest first.
	var hist struct {
		Item  stock.Item   `json:"item"`
		Moves []stock.Move `json:"moves"`
	}
	must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/stock/items/"+pav, f.OwnerToken, nil, &hist)
	kinds := ""
	for _, m := range hist.Moves {
		kinds += m.Kind + " "
	}
	if kinds != "trueup purchase sale sale sale purchase " || hist.Moves[2].InvoiceNo == "" {
		t.Fatalf("pav history: %s %+v", kinds, hist.Moves)
	}
}

// TestRecipesFollowTheBill: open items and dishes without a recipe use no
// stock; a resent bill uses it once.
func TestRecipesFollowTheBill(t *testing.T) {
	f := newRestaurant(t, "Recipe Bills", "", "29")
	rice := f.stockItem(t, "Rice", "kg", 0)
	f.purchase(t, http.StatusCreated, ids.New(), "2026-09-28", pl{rice, 10_000, 60_000})
	open := pricing.Input{PriceMode: pricing.Exclusive, Supply: pricing.Intra, Rounding: pricing.NearestRupee,
		Lines: []pricing.Line{{Ref: "chef-special", Name: "Chef's special", Qty: 1, UnitPricePaise: 20000, TaxRateBP: 500}}}
	f.apply(t, f.bill(t, open, billOpts{}), f.bill(t, f.pav(1), billOpts{}))
	if q := f.stockLevels(t)["Rice"].Qty; q != 10_000 {
		t.Fatalf("no recipe, no stock used: %d", q)
	}
	f.recipe(t, f.ItemID, rice, 250)
	op := f.bill(t, f.pav(2), billOpts{})
	f.apply(t, op)
	f.push(t, op) // the till sends it again
	if q := f.stockLevels(t)["Rice"].Qty; q != 9_500 {
		t.Fatalf("2 portions x 250 g, once: %d", q)
	}
	var fc stock.FoodCost
	must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/stock/food-cost?from=2026-09-28&to=2026-09-28", f.OwnerToken, nil, &fc)
	// Recipes are today's: pav bhaji has one now, so only the open item is
	// "sold without a recipe".
	if fc.PortionsNoRecipe != 1 || fc.FoodCost != 3_000 {
		t.Fatalf("food cost: %+v", fc)
	}
	// Removing the recipe: bills issued from now on use nothing.
	f.recipe(t, f.ItemID)
	f.apply(t, f.bill(t, f.pav(1), billOpts{issuedAt: time.Now().In(gst.IST)}))
	if q := f.stockLevels(t)["Rice"].Qty; q != 9_500 {
		t.Fatalf("after removing the recipe: %d", q)
	}
	checkStockBooks(t, f)
}

// TestConcurrentBillsKeepStockExact: tills selling and the owner booking
// wastage at the same moment never lose an update to the same ingredient.
func TestConcurrentBillsKeepStockExact(t *testing.T) {
	f := newRestaurant(t, "Busy Kitchen", "", "29")
	oil := f.stockItem(t, "Oil", "l", 0)
	f.recipe(t, f.ItemID, oil, 30)
	f.purchase(t, http.StatusCreated, ids.New(), "2026-09-28", pl{oil, 15_000, 300_000})
	tills := []*fixture{f, f.pair(t, "pos", "Counter 2"), f.pair(t, "pos", "Counter 3"), f.pair(t, "pos", "Counter 4")}
	var wg sync.WaitGroup
	for _, till := range tills {
		wg.Add(1)
		go func(till *fixture) {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				till.apply(t, till.bill(t, till.pav(1), billOpts{}))
			}
		}(till)
	}
	// Meanwhile the owner books spills from the back office: owner documents
	// do not go through sync, so only the balance row lock keeps them apart.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			must(t, http.StatusCreated, "POST", "/v1/outlets/"+f.OutletID+"/stock/wastage", f.OwnerToken, map[string]any{
				"id": ids.New(), "stock_item_id": oil, "qty": 100, "reason": "spilt", "date": "2026-09-28",
			}, nil)
		}()
	}
	wg.Wait()
	if q := f.stockLevels(t)["Oil"].Qty; q != 15_000-20*30-10*100 {
		t.Fatalf("20 portions x 30 ml and 10 spills of 100 ml from 15 l: %d", q)
	}
	checkStockBooks(t, f)
}

// TestStockGuards: other tenants, bad input, replays.
func TestStockGuards(t *testing.T) {
	mine := newRestaurant(t, "Stock Mine", "", "29")
	theirs := newRestaurant(t, "Stock Theirs", "", "29")
	flour := mine.stockItem(t, "Flour", "kg", 0)
	alien := theirs.stockItem(t, "Their flour", "kg", 0)

	code := func(want int, method, path string, body any) string {
		t.Helper()
		var e struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		status, _ := call(t, method, path, mine.OwnerToken, body, &e)
		if status != want {
			t.Fatalf("%s %s: %d %s, want %d", method, path, status, e.Error.Code, want)
		}
		return e.Error.Code
	}
	out := "/v1/outlets/" + mine.OutletID + "/stock"
	purchase := func(id uuid.UUID, lines ...map[string]any) map[string]any {
		return map[string]any{"id": id, "supplier": "S", "date": "2026-09-28", "paid_by": "bank", "lines": lines}
	}
	line := func(item string, qty, cost int64) map[string]any {
		return map[string]any{"stock_item_id": item, "qty": qty, "cost": cost}
	}

	for _, c := range []struct {
		want         int
		method, path string
		body         any
		code         string
	}{
		{400, "POST", "/v1/stock/items", map[string]any{"name": "Salt", "unit": "gram"}, "bad_unit"},
		{409, "POST", "/v1/stock/items", map[string]any{"name": "FLOUR", "unit": "kg"}, "duplicate_item"},
		{400, "PATCH", "/v1/stock/items/" + flour, map[string]any{"unit": "pc"}, "unit_fixed"},
		{404, "PATCH", "/v1/stock/items/" + alien, map[string]any{"name": "Mine now"}, "not_found"},
		{400, "PUT", "/v1/stock/recipes/" + mine.ItemID, map[string]any{"lines": []map[string]any{{"stock_item_id": alien, "qty": 10}}}, "unknown_stock_item"},
		{404, "PUT", "/v1/stock/recipes/" + theirs.ItemID, map[string]any{"lines": []map[string]any{}}, "not_found"},
		{400, "PUT", "/v1/stock/recipes/" + mine.ItemID, map[string]any{"lines": []map[string]any{{"stock_item_id": flour, "qty": 0}}}, "bad_qty"},
		{400, "POST", out + "/purchases", purchase(ids.New(), line(alien, 1000, 100)), "unknown_stock_item"},
		{400, "POST", out + "/purchases", purchase(ids.New(), line(flour, 1000, 100), line(flour, 1, 1)), "duplicate_item"},
		{400, "POST", out + "/purchases", purchase(ids.New(), line(flour, 0, 100)), "bad_line"},
		{400, "POST", out + "/purchases", purchase(ids.New()), "bad_lines"},
		{400, "POST", out + "/purchases", purchase(uuid.New(), line(flour, 1000, 100)), "bad_id"},
		{400, "POST", out + "/purchases", map[string]any{"id": ids.New(), "supplier": "S", "date": "2099-01-01", "paid_by": "safe",
			"lines": []map[string]any{line(flour, 1, 1)}}, "bad_date"},
		{400, "POST", out + "/purchases", map[string]any{"id": ids.New(), "supplier": "S", "paid_by": "upi",
			"lines": []map[string]any{line(flour, 1, 1)}}, "bad_paid_by"},
		{400, "POST", out + "/wastage", map[string]any{"id": ids.New(), "stock_item_id": flour, "qty": 10, "reason": " "}, "bad_reason"},
		{400, "POST", out + "/counts", map[string]any{"id": ids.New(), "lines": []map[string]any{{"stock_item_id": flour, "counted": -1}}}, "bad_line"},
		{400, "GET", out + "/food-cost?from=2026-09-28&to=2026-01-01", nil, "bad_range"},
	} {
		if got := code(c.want, c.method, c.path, c.body); got != c.code {
			t.Fatalf("%s %s: code %s, want %s", c.method, c.path, got, c.code)
		}
	}

	// A document id is one document: reusing it for another kind is refused.
	id := ids.New()
	code(201, "POST", out+"/purchases", purchase(id, line(flour, 1000, 4000)))
	if got := code(409, "POST", out+"/wastage", map[string]any{"id": id, "stock_item_id": flour, "qty": 10, "reason": "x"}); got != "id_used" {
		t.Fatalf("id reuse: %s", got)
	}
	// Another tenant cannot see or use my outlet.
	for _, p := range []string{"", "/items/" + flour, "/recipes", "/food-cost", "/purchases"} {
		if status, _ := call(t, "GET", out+p, theirs.OwnerToken, nil, nil); status != http.StatusNotFound {
			t.Fatalf("GET %s by another tenant: %d", p, status)
		}
	}
	if status, _ := call(t, "POST", out+"/purchases", theirs.OwnerToken, purchase(ids.New(), line(alien, 1, 1)), nil); status != http.StatusNotFound {
		t.Fatalf("a purchase into another tenant's outlet: %d", status)
	}
	if status, _ := call(t, "GET", out, mine.DeviceToken, nil, nil); status != http.StatusUnauthorized && status != http.StatusForbidden {
		t.Fatalf("a device reading stock: %d", status)
	}
}

// TestTillPaysASupplier: a delivery paid in cash from a till drawer is
// recorded twice, once by each side, and booked once. The till's payout
// lowers the drawer's expected cash; the owner's delivery says "paid from a
// till"; account 1150 matches the two and comes back to zero.
func TestTillPaysASupplier(t *testing.T) {
	till := newRestaurant(t, "Till Pays", "", "29")
	tomatoes := till.stockItem(t, "Tomatoes", "kg", 0)
	s := ids.New()
	till.apply(t,
		till.openShift(s, at(11, 0), "Asha", 200_000),
		till.moveCash(s, "supplier", 50_000, "Sharma Vegetables, 12.5 kg tomatoes", at(12, 0)),
	)
	must(t, http.StatusCreated, "POST", "/v1/outlets/"+till.OutletID+"/stock/purchases", till.OwnerToken, map[string]any{
		"id": ids.New(), "supplier": "Sharma Vegetables", "date": "2026-09-28", "paid_by": "till",
		"lines": []map[string]any{{"stock_item_id": tomatoes, "qty": 12_500, "cost": 50_000}},
	}, nil)
	till.apply(t, till.closeShift(s, at(16, 0), 150_000, notes(150_000)))

	bal := reviewBalances(t, till)
	if bal["1150"] != 0 || bal["5900"] != 0 || bal["1100"] != -50_000 || bal["1300"] != 50_000 {
		t.Fatalf("till payouts %d (want 0), over/short %d (want 0), cash %d (want -50000), stock %d",
			bal["1150"], bal["5900"], bal["1100"], bal["1300"])
	}
	r := till.day(t, closeDay).Report
	if r.Shifts[0].Suppliers != 50_000 || *r.Shifts[0].Difference != 0 {
		t.Fatalf("the drawer knows about the payout: %+v", r.Shifts[0])
	}
	checkStockBooks(t, till)
}

// TestStockFiguresTooLargeAreRefused: values that cannot be held in 64 bits
// are refused, never wrapped.
func TestStockFiguresTooLargeAreRefused(t *testing.T) {
	f := newRestaurant(t, "Huge Figures", "", "29")
	gold := f.stockItem(t, "Gold leaf", "kg", 0)
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	status, _ := call(t, "POST", "/v1/outlets/"+f.OutletID+"/stock/counts", f.OwnerToken, map[string]any{
		"id": ids.New(), "lines": []map[string]any{{"stock_item_id": gold, "counted": 1_000_000_000_000, "unit_cost": 1_000_000_000_000}},
	}, &e)
	if status != http.StatusBadRequest || e.Error.Code != "too_large" {
		t.Fatalf("a count worth 10^21 paise: %d %s", status, e.Error.Code)
	}
	if q := f.stockLevels(t)["Gold leaf"]; q.Qty != 0 || q.Value != 0 {
		t.Fatalf("nothing kept: %+v", q)
	}
	checkStockBooks(t, f)
}
