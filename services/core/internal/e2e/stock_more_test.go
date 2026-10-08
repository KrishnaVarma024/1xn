package e2e

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"1xn/core/internal/platform/gst"
	"1xn/core/internal/platform/ids"
	"1xn/core/modules/catalogue"
	"1xn/core/modules/dayclose"
	"1xn/core/modules/sales/pricing"
	"1xn/core/modules/stock"
)

// addonGroup makes an "Add-ons" group with Extra cheese (₹40) and Extra
// butter (₹20), offered on the fixture's dish, and returns the options.
func (f *fixture) addonGroup(t *testing.T) (cheese, butter catalogue.ModifierOption) {
	t.Helper()
	var g catalogue.ModifierGroup
	must(t, http.StatusCreated, "POST", "/v1/catalogue/modifier-groups", f.OwnerToken, map[string]any{
		"name": "Add-ons", "min_select": 0, "max_select": 3,
		"options": []map[string]any{{"name": "Extra cheese", "price_paise": 4000}, {"name": "Extra butter", "price_paise": 2000}},
	}, &g)
	must(t, http.StatusOK, "PATCH", "/v1/catalogue/items/"+f.ItemID, f.OwnerToken,
		map[string]any{"modifier_group_ids": []string{g.ID.String()}}, nil)
	return g.Options[0], g.Options[1]
}

func (f *fixture) addonRecipe(t *testing.T, want int, option string, lines ...any) {
	t.Helper()
	ls := []map[string]any{}
	for i := 0; i < len(lines); i += 2 {
		ls = append(ls, map[string]any{"stock_item_id": lines[i], "qty": lines[i+1]})
	}
	must(t, want, "PUT", "/v1/stock/addon-recipes/"+option, f.OwnerToken, map[string]any{"lines": ls}, nil)
}

// pavWith is pav bhaji with add-ons, as a till bills it: each modifier's ref
// is the add-on option's id.
func (f *fixture) pavWith(qty int64, opts ...catalogue.ModifierOption) pricing.Input {
	in := f.pav(qty)
	for _, o := range opts {
		in.Lines[0].Modifiers = append(in.Lines[0].Modifiers, pricing.Modifier{Ref: o.ID.String(), Name: o.Name, PricePaise: o.PricePaise})
	}
	return in
}

// TestAddonRecipes: "Extra cheese" uses cheese, once per portion of the
// line it is on, by the recipe in force when the bill was issued.
func TestAddonRecipes(t *testing.T) {
	f := newRestaurant(t, "Addon Kitchen", "", "29")
	potatoes := f.stockItem(t, "Potatoes", "kg", 0)
	cheese := f.stockItem(t, "Cheese", "kg", 0)
	butter := f.stockItem(t, "Butter", "kg", 0)
	f.recipe(t, f.ItemID, potatoes, 150)
	xCheese, xButter := f.addonGroup(t)
	f.addonRecipe(t, http.StatusNoContent, xCheese.ID.String(), cheese, 30)
	f.addonRecipe(t, http.StatusNoContent, xButter.ID.String(), butter, 15)
	f.purchase(t, http.StatusCreated, ids.New(), "", pl{potatoes, 10_000, 40_000}, pl{cheese, 1_000, 60_000}, pl{butter, 1_000, 50_000})

	// Two portions with extra cheese, one with both, one plain from a till
	// that does not send add-on refs yet.
	f.apply(t, f.bill(t, f.pavWith(2, xCheese), billOpts{issuedAt: time.Now().Add(-time.Hour)}))
	f.apply(t, f.bill(t, f.pavWith(1, xCheese, xButter), billOpts{issuedAt: time.Now().Add(-time.Hour)}))
	old := f.pav(1)
	old.Lines[0].Modifiers = []pricing.Modifier{{Name: "Extra cheese", PricePaise: 4000}}
	f.apply(t, f.bill(t, old, billOpts{issuedAt: time.Now().Add(-time.Hour)}))
	s := f.stockLevels(t)
	if s["Potatoes"].Qty != 10_000-4*150 || s["Cheese"].Qty != 1_000-3*30 || s["Butter"].Qty != 1_000-15 {
		t.Fatalf("potatoes %d, cheese %d, butter %d", s["Potatoes"].Qty, s["Cheese"].Qty, s["Butter"].Qty)
	}
	checkStockBooks(t, f)

	// The recipe card costs the add-on: 30 g of ₹600/kg cheese is ₹18.
	var card struct {
		Addons []stock.Addon `json:"addons"`
	}
	must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/stock/recipes", f.OwnerToken, nil, &card)
	if len(card.Addons) != 2 || card.Addons[0].Name != "Extra cheese" || card.Addons[0].Cost != 1_800 ||
		card.Addons[0].Group != "Add-ons" || card.Addons[0].FoodCostBP != 4_500 {
		t.Fatalf("add-on card: %+v", card.Addons)
	}

	// Cheese cannot be switched off while an add-on uses it.
	var e errBody
	must(t, http.StatusConflict, "PATCH", "/v1/stock/items/"+cheese, f.OwnerToken, map[string]any{"active": false}, &e)
	if e.Error.Message != "1 add-on still use this item; take it out of their recipes first" {
		t.Fatalf("in_recipes: %q", e.Error.Message)
	}

	// The recipe changes to 50 g. A bill issued before the change, synced
	// after it, uses the old 30 g; a new bill uses 50 g.
	before := f.bill(t, f.pavWith(1, xCheese), billOpts{issuedAt: time.Now().Add(-time.Minute)})
	time.Sleep(10 * time.Millisecond)
	f.addonRecipe(t, http.StatusNoContent, xCheese.ID.String(), cheese, 50)
	f.apply(t, before, f.bill(t, f.pavWith(1, xCheese), billOpts{issuedAt: time.Now().Add(time.Second)}))
	if q := f.stockLevels(t)["Cheese"].Qty; q != 1_000-3*30-30-50 {
		t.Fatalf("cheese after the recipe change: %d", q)
	}

	// Removing the recipe: later bills use no cheese, and cheese can go.
	f.addonRecipe(t, http.StatusNoContent, xCheese.ID.String())
	f.apply(t, f.bill(t, f.pavWith(1, xCheese), billOpts{issuedAt: time.Now().Add(2 * time.Second)}))
	if q := f.stockLevels(t)["Cheese"].Qty; q != 1_000-3*30-30-50 {
		t.Fatalf("cheese after removing the recipe: %d", q)
	}
	must(t, http.StatusNoContent, "PATCH", "/v1/stock/items/"+cheese, f.OwnerToken, map[string]any{"active": false}, nil)

	// Another tenant's option, a dish id, and nonsense: not found.
	other := newRestaurant(t, "Addon Other", "", "29")
	must(t, http.StatusNotFound, "PUT", "/v1/stock/addon-recipes/"+xButter.ID.String(), other.OwnerToken,
		map[string]any{"lines": []any{}}, nil)
	must(t, http.StatusNotFound, "PUT", "/v1/stock/addon-recipes/"+f.ItemID, f.OwnerToken, map[string]any{"lines": []any{}}, nil)
	must(t, http.StatusNotFound, "PUT", "/v1/stock/addon-recipes/nope", f.OwnerToken, map[string]any{"lines": []any{}}, nil)
	checkStockBooks(t, f)
}

func (f *fixture) pack(t *testing.T, want int, item, name string, qty int64) stock.Pack {
	t.Helper()
	var p stock.Pack
	must(t, want, "POST", "/v1/stock/items/"+item+"/packs", f.OwnerToken, map[string]any{"name": name, "qty": qty}, &p)
	return p
}

func (f *fixture) buy(t *testing.T, want int, body map[string]any) (stock.Doc, string) {
	t.Helper()
	if body["id"] == nil {
		body["id"] = ids.New()
	}
	var doc stock.Doc
	var e errBody
	status, _ := call(t, "POST", "/v1/outlets/"+f.OutletID+"/stock/purchases", f.OwnerToken, body, &struct {
		*stock.Doc
		*errBody
	}{&doc, &e})
	if status != want {
		t.Fatalf("purchase: status %d, want %d (%s %s)", status, want, e.Error.Code, e.Error.Message)
	}
	return doc, e.Error.Code
}

// TestPurchasePacks: flour comes in 25 kg sacks. Two sacks put 50 kg in the
// store, and the history remembers "2 × Sack".
func TestPurchasePacks(t *testing.T) {
	f := newRestaurant(t, "Pack Kitchen", "", "29")
	flour := f.stockItem(t, "Maida", "kg", 0)
	oil := f.stockItem(t, "Oil", "l", 0)
	sack := f.pack(t, http.StatusCreated, flour, "Sack", 25_000)
	tin := f.pack(t, http.StatusCreated, oil, "Tin", 15_000)
	f.pack(t, http.StatusConflict, flour, "sack", 50_000) // one name per item

	line := func(item string, pack stock.Pack, packs, cost int64) map[string]any {
		return map[string]any{"stock_item_id": item, "pack_id": pack.ID, "packs": packs, "cost": cost}
	}
	f.buy(t, http.StatusCreated, map[string]any{"supplier": "Grain House", "paid_by": "safe",
		"lines": []any{line(flour, sack, 2, 200_000)}})
	s := f.stockLevels(t)
	if s["Maida"].Qty != 50_000 || s["Maida"].UnitCost != 4_000 || len(s["Maida"].Packs) != 1 || s["Maida"].Packs[0].Name != "Sack" {
		t.Fatalf("maida: %+v", s["Maida"])
	}
	var hist struct {
		Moves []stock.Move `json:"moves"`
	}
	must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/stock/items/"+flour, f.OwnerToken, nil, &hist)
	if len(hist.Moves) != 1 || hist.Moves[0].Pack != "Sack" || hist.Moves[0].Packs != 2 || hist.Moves[0].Qty != 50_000 {
		t.Fatalf("history: %+v", hist.Moves)
	}

	for name, l := range map[string]map[string]any{
		"another item's pack": line(flour, tin, 1, 100),
		"unknown pack":        line(flour, stock.Pack{ID: ids.New()}, 1, 100),
	} {
		if _, code := f.buy(t, http.StatusBadRequest, map[string]any{"supplier": "X", "paid_by": "safe", "lines": []any{l}}); code != "unknown_pack" {
			t.Errorf("%s: %s", name, code)
		}
	}
	for name, l := range map[string]map[string]any{
		"pack and qty":  {"stock_item_id": flour, "pack_id": sack.ID, "packs": 1, "qty": 25_000, "cost": 100},
		"no packs":      {"stock_item_id": flour, "pack_id": sack.ID, "cost": 100},
		"too many":      line(flour, sack, 100_001, 100),
		"packs, no id":  {"stock_item_id": flour, "packs": 2, "cost": 100},
		"negative pack": line(flour, sack, -1, 100),
	} {
		if _, code := f.buy(t, http.StatusBadRequest, map[string]any{"supplier": "X", "paid_by": "safe", "lines": []any{l}}); code != "bad_line" {
			t.Errorf("%s: %s", name, code)
		}
	}
	// A pack so big that packs × size does not fit.
	huge := f.pack(t, http.StatusCreated, oil, "Tanker", 1_000_000_000)
	if _, code := f.buy(t, http.StatusBadRequest, map[string]any{"supplier": "X", "paid_by": "safe",
		"lines": []any{line(oil, huge, 100_000, 100)}}); code != "too_large" {
		t.Fatalf("huge: %s", code)
	}

	// A switched-off pack cannot be received; its name is free again.
	must(t, http.StatusNoContent, "PATCH", "/v1/stock/packs/"+sack.ID.String(), f.OwnerToken, map[string]any{"active": false}, nil)
	if _, code := f.buy(t, http.StatusBadRequest, map[string]any{"supplier": "X", "paid_by": "safe",
		"lines": []any{line(flour, sack, 1, 100)}}); code != "pack_inactive" {
		t.Fatalf("inactive pack: %s", code)
	}
	f.pack(t, http.StatusCreated, flour, "Sack", 50_000)
	must(t, http.StatusConflict, "PATCH", "/v1/stock/packs/"+sack.ID.String(), f.OwnerToken, map[string]any{"active": true}, nil)
	must(t, http.StatusBadRequest, "PATCH", "/v1/stock/packs/"+sack.ID.String(), f.OwnerToken, map[string]any{"name": "Bag"}, nil)
	must(t, http.StatusBadRequest, "POST", "/v1/stock/items/"+flour+"/packs", f.OwnerToken, map[string]any{"name": "Bag", "qty": 0}, nil)
	other := newRestaurant(t, "Pack Other", "", "29")
	must(t, http.StatusNotFound, "POST", "/v1/stock/items/"+flour+"/packs", other.OwnerToken, map[string]any{"name": "Bag", "qty": 1}, nil)
	must(t, http.StatusNotFound, "PATCH", "/v1/stock/packs/"+tin.ID.String(), other.OwnerToken, map[string]any{"active": false}, nil)
	checkStockBooks(t, f)
}

type supplierList struct {
	Suppliers []stock.SupplierBalance `json:"suppliers"`
	Owed      int64                   `json:"owed"`
}

func (f *fixture) suppliers(t *testing.T) (map[string]stock.SupplierBalance, int64) {
	t.Helper()
	var l supplierList
	must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/stock/suppliers", f.OwnerToken, nil, &l)
	out := map[string]stock.SupplierBalance{}
	for _, s := range l.Suppliers {
		out[s.Name] = s
	}
	return out, l.Owed
}

func (f *fixture) pay(t *testing.T, want int, body map[string]any) (stock.Payment, string) {
	t.Helper()
	if body["id"] == nil {
		body["id"] = ids.New()
	}
	var p stock.Payment
	var e errBody
	status, _ := call(t, "POST", "/v1/outlets/"+f.OutletID+"/stock/supplier-payments", f.OwnerToken, body, &struct {
		*stock.Payment
		*errBody
	}{&p, &e})
	if status != want {
		t.Fatalf("payment: status %d, want %d (%s %s)", status, want, e.Error.Code, e.Error.Message)
	}
	return p, e.Error.Code
}

// TestSuppliersAndPayables: deliveries on credit build up what is owed to a
// supplier; payments bring it down. Who is owed what always adds up to
// account 2100 Payable to suppliers.
func TestSuppliersAndPayables(t *testing.T) {
	f := newRestaurant(t, "Payables Kitchen", "", "29")
	milk := f.stockItem(t, "Milk", "l", 0)
	paneer := f.stockItem(t, "Paneer", "kg", 0)
	checkPayables := func() {
		t.Helper()
		_, owed := f.suppliers(t)
		if bal := reviewBalances(t, f); -bal["2100"] != owed {
			t.Fatalf("suppliers are owed %d in all, but account 2100 holds %d", owed, -bal["2100"])
		}
	}

	// Deliveries name suppliers in free text; one name, whatever its case,
	// is one supplier.
	f.buy(t, http.StatusCreated, map[string]any{"supplier": "Bandra Dairy", "paid_by": "credit", "date": "2026-09-28",
		"lines": []any{map[string]any{"stock_item_id": milk, "qty": 20_000, "cost": 112_000}}})
	f.buy(t, http.StatusCreated, map[string]any{"supplier": "  bandra DAIRY ", "paid_by": "credit", "invoice_no": "BD-7",
		"lines": []any{map[string]any{"stock_item_id": paneer, "qty": 2_000, "cost": 64_000}}})
	f.buy(t, http.StatusCreated, map[string]any{"supplier": "Bandra Dairy", "paid_by": "bank",
		"lines": []any{map[string]any{"stock_item_id": milk, "qty": 5_000, "cost": 28_000}}})
	sup, total := f.suppliers(t)
	bd := sup["Bandra Dairy"]
	if len(sup) != 1 || bd.Owed != 176_000 || total != 176_000 || bd.Bought30d != 204_000 {
		t.Fatalf("suppliers: %+v total %d", sup, total)
	}
	checkPayables()

	// By id works too; an id and a name together, or an unknown id, do not.
	f.buy(t, http.StatusCreated, map[string]any{"supplier_id": bd.ID, "paid_by": "credit",
		"lines": []any{map[string]any{"stock_item_id": milk, "qty": 1_000, "cost": 5_600}}})
	if _, code := f.buy(t, http.StatusBadRequest, map[string]any{"supplier_id": bd.ID, "supplier": "x", "paid_by": "credit",
		"lines": []any{map[string]any{"stock_item_id": milk, "qty": 1_000, "cost": 1}}}); code != "bad_supplier" {
		t.Fatalf("both: %s", code)
	}
	if _, code := f.buy(t, http.StatusBadRequest, map[string]any{"supplier_id": ids.New(), "paid_by": "credit",
		"lines": []any{map[string]any{"stock_item_id": milk, "qty": 1_000, "cost": 1}}}); code != "unknown_supplier" {
		t.Fatalf("unknown supplier: %s", code)
	}

	// A bank transfer pays part of it.
	pay := map[string]any{"supplier_id": bd.ID, "amount": 100_000, "paid_by": "bank", "reference": "UTR 4411", "date": "2026-09-30"}
	pay["id"] = ids.New()
	p, _ := f.pay(t, http.StatusCreated, pay)
	if p.OwedAfter != 81_600 || p.Supplier != "Bandra Dairy" || p.Reference != "UTR 4411" || p.Date != "2026-09-30" {
		t.Fatalf("payment: %+v", p)
	}
	if again, _ := f.pay(t, http.StatusOK, pay); again.ID != p.ID || again.OwedAfter != 81_600 {
		t.Fatalf("replay: %+v", again)
	}
	reused := map[string]any{"id": pay["id"], "supplier_id": bd.ID, "amount": 1, "paid_by": "bank"}
	if _, code := f.pay(t, http.StatusConflict, reused); code != "id_used" {
		t.Fatalf("reused id: %s", code)
	}
	bal := reviewBalances(t, f)
	if bal["1140"] != -28_000-100_000 {
		t.Fatalf("bank paid the cash delivery and the transfer: %d", bal["1140"])
	}
	checkPayables()

	// Never more than is owed.
	if _, code := f.pay(t, http.StatusConflict, map[string]any{"supplier_id": bd.ID, "amount": 81_601, "paid_by": "safe"}); code != "more_than_owed" {
		t.Fatalf("overpay: %s", code)
	}
	// Two payments at once that together are more than owed: one is refused.
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], _ = call(t, "POST", "/v1/outlets/"+f.OutletID+"/stock/supplier-payments", f.OwnerToken,
				map[string]any{"id": ids.New(), "supplier_id": bd.ID, "amount": 50_000, "paid_by": "safe"}, nil)
		}()
	}
	wg.Wait()
	if !((codes[0] == 201 && codes[1] == 409) || (codes[0] == 409 && codes[1] == 201)) {
		t.Fatalf("racing payments: %v", codes)
	}
	// From a till: through clearing account 1150, which the till's payout matches.
	f.pay(t, http.StatusCreated, map[string]any{"supplier_id": bd.ID, "amount": 31_600, "paid_by": "till"})
	if sup, _ := f.suppliers(t); sup["Bandra Dairy"].Owed != 0 {
		t.Fatalf("paid in full: %+v", sup["Bandra Dairy"])
	}
	if bal := reviewBalances(t, f); bal["1150"] != -31_600 || bal["1105"] != -50_000 {
		t.Fatalf("till payment %d, safe payment %d", bal["1150"], bal["1105"])
	}
	checkPayables()

	// The statement: every delivery and payment, newest first, with what was
	// owed after each. A delivery paid by bank changes nothing owed.
	var st stock.Statement
	must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/stock/suppliers/"+bd.ID.String(), f.OwnerToken, nil, &st)
	if st.Owed != 0 || len(st.Lines) != 7 || st.Lines[0].Owed != 0 {
		t.Fatalf("statement: owed %d, %d lines: %+v", st.Owed, len(st.Lines), st.Lines)
	}
	last := st.Lines[len(st.Lines)-1]
	if last.Kind != "delivery" || last.Date != "2026-09-28" || last.Owed != 112_000 {
		t.Fatalf("first line: %+v", last)
	}

	// Bad payments.
	for name, body := range map[string]map[string]any{
		"v4 id":        {"id": uuid.New(), "supplier_id": bd.ID, "amount": 1, "paid_by": "bank"},
		"zero":         {"supplier_id": bd.ID, "amount": 0, "paid_by": "bank"},
		"credit":       {"supplier_id": bd.ID, "amount": 1, "paid_by": "credit"},
		"future":       {"supplier_id": bd.ID, "amount": 1, "paid_by": "bank", "date": time.Now().AddDate(0, 0, 3).Format(time.DateOnly)},
		"long ref":     {"supplier_id": bd.ID, "amount": 1, "paid_by": "bank", "reference": fmt.Sprintf("%0101d", 0)},
		"no supplier":  {"supplier_id": ids.New(), "amount": 1, "paid_by": "bank"},
		"unknown key":  {"supplier_id": bd.ID, "amount": 1, "paid_by": "bank", "tip": 1},
		"control char": {"supplier_id": bd.ID, "amount": 1, "paid_by": "bank", "reference": "a\u0007b"},
	} {
		want := http.StatusBadRequest
		if name == "no supplier" {
			want = http.StatusBadRequest
		}
		if body["id"] == nil {
			body["id"] = ids.New()
		}
		if status, _ := call(t, "POST", "/v1/outlets/"+f.OutletID+"/stock/supplier-payments", f.OwnerToken, body, nil); status != want {
			t.Errorf("%s: status %d", name, status)
		}
	}

	// A locked month refuses payments dated in it.
	now := time.Now().In(gst.IST)
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
	if _, err := ownerDB.Exec(`INSERT INTO period (id, tenant_id, legal_entity_id, starts_on, ends_on, status)
		VALUES ($1, $2, $3, $4, $5, 'locked')
		ON CONFLICT (legal_entity_id, starts_on) DO UPDATE SET status = 'locked'`, ids.New(), f.TenantID, f.LegalEntityID,
		start.Format(time.DateOnly), start.AddDate(0, 1, 0).Format(time.DateOnly)); err != nil {
		t.Fatal(err)
	}
	f.buy(t, http.StatusCreated, map[string]any{"supplier": "Bandra Dairy", "paid_by": "credit",
		"lines": []any{map[string]any{"stock_item_id": milk, "qty": 1_000, "cost": 5_600}}})
	if _, code := f.pay(t, http.StatusConflict, map[string]any{"supplier_id": bd.ID, "amount": 100, "paid_by": "bank",
		"date": start.AddDate(0, 0, 10).Format(time.DateOnly)}); code != "period_locked" {
		t.Fatalf("locked month: %s", code)
	}

	// Suppliers can be added and corrected by hand.
	var s stock.Supplier
	must(t, http.StatusCreated, "POST", "/v1/stock/suppliers", f.OwnerToken,
		map[string]any{"name": "Grain House", "phone": "+919812345678", "gstin": "29ABCDE1234F1ZW"}, &s)
	must(t, http.StatusConflict, "POST", "/v1/stock/suppliers", f.OwnerToken, map[string]any{"name": "grain house"}, nil)
	must(t, http.StatusBadRequest, "POST", "/v1/stock/suppliers", f.OwnerToken, map[string]any{"name": "X", "gstin": "29ABCDE1234F1Z5"}, nil)
	must(t, http.StatusBadRequest, "POST", "/v1/stock/suppliers", f.OwnerToken, map[string]any{"name": "X", "phone": "98123"}, nil)
	must(t, http.StatusNoContent, "PATCH", "/v1/stock/suppliers/"+s.ID.String(), f.OwnerToken, map[string]any{"phone": "", "name": "Grain House Pvt"}, nil)
	must(t, http.StatusConflict, "PATCH", "/v1/stock/suppliers/"+s.ID.String(), f.OwnerToken, map[string]any{"name": "BANDRA dairy"}, nil)
	sup, _ = f.suppliers(t)
	if g := sup["Grain House Pvt"]; g.Phone != "" || g.GSTIN != "29ABCDE1234F1ZW" || g.Owed != 0 {
		t.Fatalf("edited supplier: %+v", g)
	}

	// Another restaurant sees none of it.
	other := newRestaurant(t, "Payables Other", "", "29")
	if sup, _ := other.suppliers(t); len(sup) != 0 {
		t.Fatalf("other tenant sees %v", sup)
	}
	must(t, http.StatusNotFound, "GET", "/v1/outlets/"+other.OutletID+"/stock/suppliers/"+bd.ID.String(), other.OwnerToken, nil, nil)
	if _, code := other.pay(t, http.StatusBadRequest, map[string]any{"supplier_id": bd.ID, "amount": 1, "paid_by": "bank"}); code != "unknown_supplier" {
		t.Fatalf("other tenant pays our supplier: %s", code)
	}
	must(t, http.StatusNotFound, "PATCH", "/v1/stock/suppliers/"+bd.ID.String(), other.OwnerToken, map[string]any{"name": "Mine"}, nil)
	checkPayables()
	checkStockBooks(t, f)
}

// A payment is booked on its own date, so it can pay at most the least the
// supplier was owed on any day from then on: never an advance hidden in the
// past. Delivered ₹1,000 six days ago, paid ₹800 two days ago, delivered
// ₹500 yesterday: ₹700 is owed now, but four days ago only ₹200 of it can be
// paid (₹500 would take the account to -₹300 between the payment and the
// last delivery).
func TestSupplierPaymentsFollowTheirDates(t *testing.T) {
	f := newRestaurant(t, "Payment Dates", "", "29")
	rice := f.stockItem(t, "Rice", "kg", 0)
	today := dayclose.TradingDay(time.Now())
	ago := func(n int) string { return today.AddDate(0, 0, -n).Format(time.DateOnly) }
	deliver := func(daysAgo int, cost int64) {
		f.buy(t, http.StatusCreated, map[string]any{"supplier": "Grain House", "paid_by": "credit", "date": ago(daysAgo),
			"lines": []any{map[string]any{"stock_item_id": rice, "qty": 1_000, "cost": cost}}})
	}
	deliver(6, 100_000)
	sup, _ := f.suppliers(t)
	gh := sup["Grain House"].ID
	f.pay(t, http.StatusCreated, map[string]any{"supplier_id": gh, "amount": 80_000, "paid_by": "bank", "date": ago(2)})
	deliver(1, 50_000)

	if _, code := f.pay(t, http.StatusConflict, map[string]any{"supplier_id": gh, "amount": 50_000, "paid_by": "bank", "date": ago(4)}); code != "more_than_owed" {
		t.Fatalf("₹500 dated four days ago: %s", code)
	}
	if _, code := f.pay(t, http.StatusConflict, map[string]any{"supplier_id": gh, "amount": 10_000, "paid_by": "bank", "date": ago(7)}); code != "more_than_owed" {
		t.Fatalf("a payment dated before the first delivery: %s", code)
	}
	f.pay(t, http.StatusCreated, map[string]any{"supplier_id": gh, "amount": 20_000, "paid_by": "bank", "date": ago(4)})
	p, _ := f.pay(t, http.StatusCreated, map[string]any{"supplier_id": gh, "amount": 50_000, "paid_by": "safe"})
	if p.OwedAfter != 0 {
		t.Fatalf("owed after paying the rest: %d", p.OwedAfter)
	}

	var st stock.Statement
	must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/stock/suppliers/"+gh.String(), f.OwnerToken, nil, &st)
	for _, l := range st.Lines {
		if l.Owed < 0 {
			t.Fatalf("the statement goes below zero on %s: %+v", l.Date, st.Lines)
		}
	}
	reviewBalances(t, f)
}
