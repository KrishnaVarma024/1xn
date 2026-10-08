package e2e

// Adversarial review of slice 7, stock part two: suppliers and what is owed
// to them, packs, and recipes for add-ons. Every test here names a defect and
// fails against the code as reviewed; it should pass once the defect is
// fixed (whichever reasonable fix is chosen).

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"1xn/core/internal/platform/gst"
	"1xn/core/internal/platform/ids"
	"1xn/core/modules/dayclose"
	"1xn/core/modules/stock"
)

// ---------------------------------------------------------------------------
// P1. A supplier can be paid for a delivery BEFORE the delivery happened.
//
// postSupplierPayment checks the amount against what is owed NOW (owed():
// every delivery on credit, whatever its date, less every payment), but books
// the payment on the date the owner picks (any day back to July 2017 in an
// open month). So a payment dated before the deliveries it pays is accepted.
//
// Scenario: Bandra Dairy delivers ₹1,120 of milk on credit today. The owner
// records a ₹1,120 bank payment and picks the 6th of LAST month (a slip on
// the date picker, or "paying" an old invoice that was never booked). It is
// accepted (201).
//
// Impact: on that date nothing was owed, so the payment is an advance, which
// the API says it does not support ("advance payments are not supported
// yet"). The supplier's statement runs to "owed after: -₹1,120" on that line,
// and account 2100 Payable to suppliers closes last month with a DEBIT
// balance (the payable is negative on last month's balance sheet). Once the
// accountant locks that month it can never be corrected. Every check in the
// slice compares the subledger with 2100 only as of today, so nothing flags
// it.
// ---------------------------------------------------------------------------
func TestReviewP1PaymentDatedBeforeTheDeliveryPaysAnAdvance(t *testing.T) {
	f := newRestaurant(t, "Review Advance", "", "29")
	milk := f.stockItem(t, "Milk", "l", 0)
	today := dayclose.TradingDay(time.Now())
	f.buy(t, http.StatusCreated, map[string]any{"supplier": "Bandra Dairy", "paid_by": "credit",
		"date": today.Format(time.DateOnly), "invoice_no": "BD-41",
		"lines": []any{map[string]any{"stock_item_id": milk, "qty": 20_000, "cost": 112_000}}})
	sup, _ := f.suppliers(t)
	bd := sup["Bandra Dairy"]

	// The 6th of last month: an open month, weeks before the delivery.
	now := time.Now().In(gst.IST)
	paidOn := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 5).Format(time.DateOnly)
	status, raw := reviewCall(t, f, "POST", "/v1/outlets/"+f.OutletID+"/stock/supplier-payments", map[string]any{
		"id": ids.New(), "supplier_id": bd.ID, "amount": 112_000, "paid_by": "bank", "reference": "UTR 7781", "date": paidOn,
	})

	// What the books said about suppliers at the end of the payment's day.
	var payable int64
	if err := ownerDB.QueryRow(`
		SELECT coalesce(sum(l.amount), 0)
		FROM journal_line l
		JOIN journal_entry e ON e.id = l.entry_id AND e.tenant_id = l.tenant_id
		JOIN account a ON a.id = l.account_id
		WHERE e.tenant_id = $1 AND a.code = '2100' AND e.entry_date <= $2`, f.TenantID, paidOn).Scan(&payable); err != nil {
		t.Fatal(err)
	}
	var st stock.Statement
	must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/stock/suppliers/"+bd.ID.String(), f.OwnerToken, nil, &st)
	lowest := int64(0)
	for _, l := range st.Lines {
		if l.Owed < lowest {
			lowest = l.Owed
		}
	}
	if status < 400 || status >= 500 || lowest < 0 || payable > 0 {
		t.Fatalf("a payment dated %s for a delivery dated %s: status %d %s (want it refused: nothing was owed on %s); "+
			"the statement runs down to owed %d paise, and account 2100 on %s is a %d paise DEBIT (a payable below zero)",
			paidOn, today.Format(time.DateOnly), status, raw, paidOn, lowest, paidOn, payable)
	}
}

// ---------------------------------------------------------------------------
// P3. An add-on recipe can end up using a switched-off stock item.
//
// The rule (TestAddonRecipes, review S5) is that an item in a current recipe
// cannot be switched off, and a recipe cannot name a switched-off item, because
// bills keep using it while Receive and Count hide it. putRecipe checks that
// its items are active and updateItem checks that no current recipe uses the
// item, but neither locks what the other checks: putRecipe locks the
// modifier option and its own recipe lines, updateItem only its stock_item
// row, and the FK check of the new addon_recipe_line (FOR KEY SHARE) does
// not conflict with updateItem's FOR NO KEY UPDATE. A classic write skew.
//
// Scenario: the owner saves "Extra cheese = 30 g Mozzarella" while a manager
// switches Mozzarella off. The save has passed its "active" check and is
// ending the old recipe lines; the switch-off sees no recipe using Mozzarella
// and commits; the save then inserts the Mozzarella line and commits. Both
// answer 204. (The gate below only makes the interleaving deterministic.)
//
// Impact: every bill with Extra cheese keeps taking Mozzarella out of stock,
// but Mozzarella is gone from Receive goods and Count, so its stock only
// ever goes down (negative, valued at a guess) and cannot be corrected.
// The dish recipe save shares the same putRecipe code and has the same race.
// ---------------------------------------------------------------------------
func TestReviewP3AddonRecipeRacesItemSwitchOff(t *testing.T) {
	f := newRestaurant(t, "Review Addon Race", "", "29")
	butter := f.stockItem(t, "Butter", "kg", 0)
	mozz := f.stockItem(t, "Mozzarella", "kg", 0)
	xCheese, _ := f.addonGroup(t)
	f.addonRecipe(t, http.StatusNoContent, xCheese.ID.String(), butter, 10)

	// Hold the add-on's current recipe line, so the save waits right after
	// its "is Mozzarella active?" check.
	gate, err := ownerDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback()
	if _, err := gate.Exec(`SELECT 1 FROM addon_recipe_line WHERE modifier_option_id = $1 AND valid_to IS NULL FOR UPDATE`, xCheese.ID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var saved, off int
	wg.Add(2)
	go func() {
		defer wg.Done()
		saved, _ = call(t, "PUT", "/v1/stock/addon-recipes/"+xCheese.ID.String(), f.OwnerToken,
			map[string]any{"lines": []map[string]any{{"stock_item_id": mozz, "qty": 30}}}, nil)
	}()
	waitForLockWaiters(t, 1)
	switched := make(chan struct{})
	go func() {
		defer wg.Done()
		defer close(switched)
		off, _ = call(t, "PATCH", "/v1/stock/items/"+mozz, f.OwnerToken, map[string]any{"active": false}, nil)
	}()
	// The switch-off either finishes, or (once fixed) waits for the save.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var waiting int
		if err := ownerDB.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database()`).
			Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		select {
		case <-switched:
			waiting = 2
		default:
		}
		if waiting >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the switch-off neither finished nor waited")
		}
	}
	if err := gate.Commit(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()

	var card struct {
		Addons []stock.Addon `json:"addons"`
	}
	must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/stock/recipes", f.OwnerToken, nil, &card)
	usesMozz := false
	for _, a := range card.Addons {
		if a.ID != xCheese.ID {
			continue
		}
		for _, l := range a.Lines {
			if l.StockItemID.String() == mozz {
				usesMozz = true
			}
		}
	}
	if active := f.stockLevels(t)["Mozzarella"].Active; !active && usesMozz {
		t.Fatalf("save %d and switch-off %d both succeeded: Extra cheese's current recipe uses Mozzarella, which is switched off "+
			"(bills use it; Receive and Count hide it)", saved, off)
	}
}

// ---------------------------------------------------------------------------
// P4. A supplier GSTIN for a state that does not exist is accepted.
//
// supplierInput.check only calls gst.ValidGSTIN (shape and check digit). Signup
// also requires the first two digits to be a GST state code in use
// (gst.ValidStateCode; "check GSTIN state codes", d3200af), but suppliers do
// not, so "00ABCDE1234F1ZG" (valid check digit, state 00) is stored, on create
// and on edit.
//
// Impact: a GSTIN no supplier can have is kept as if it were checked; it is
// what an input-tax-credit claim or a purchase register would be matched on
// later, and the back office shows it on the supplier list as genuine.
// ---------------------------------------------------------------------------
func TestReviewP4SupplierGSTINWithNoSuchStateIsAccepted(t *testing.T) {
	f := newRestaurant(t, "Review Supplier GSTIN", "", "29")
	const noState = "00ABCDE1234F1ZG" // check digit right, state code 00
	var e errBody
	status, _ := call(t, "POST", "/v1/stock/suppliers", f.OwnerToken, map[string]any{"name": "Ghost Traders", "gstin": noState}, &e)
	var s stock.Supplier
	must(t, http.StatusCreated, "POST", "/v1/stock/suppliers", f.OwnerToken, map[string]any{"name": "Real Traders"}, &s)
	patched, _ := call(t, "PATCH", "/v1/stock/suppliers/"+s.ID.String(), f.OwnerToken, map[string]any{"gstin": noState}, nil)
	if status != http.StatusBadRequest || patched != http.StatusBadRequest {
		t.Fatalf("GSTIN %s (no state 00): create %d %s, edit %d; want both refused with 400 bad_gstin", noState, status, e.Error.Code, patched)
	}
}

// ---------------------------------------------------------------------------
// P5. The API's payment limit disagrees with the table's.
//
// postSupplierPayment accepts amounts up to maxCost (₹1,000 crore), but
// supplier_payment.amount is CHECKed at 1..100000000000 (₹100 crore). A
// purchase line may cost up to ₹1,000 crore, so a supplier can be owed more
// than ₹100 crore; paying ₹200 crore of it passes every check, posts the
// journal entry, and then dies on the CHECK, answered by the generic
// "400 bad_input: a value is too long, out of range or not allowed". An amount
// above ₹1,000 crore gets "amount must be above zero".
//
// Impact: small (a chain-sized payable), but the owner gets an error that
// does not say what is wrong or what the limit is, from a request the API's
// own validation accepted.
// ---------------------------------------------------------------------------
func TestReviewP5PaymentLimitDisagreesWithTheDatabase(t *testing.T) {
	f := newRestaurant(t, "Review Payment Limit", "", "29")
	gold := f.stockItem(t, "Saffron", "kg", 0)
	f.buy(t, http.StatusCreated, map[string]any{"supplier": "Kashmir Saffron Co", "paid_by": "credit",
		"lines": []any{map[string]any{"stock_item_id": gold, "qty": 1_000_000, "cost": 500_000_000_000}}}) // ₹500 crore
	sup, _ := f.suppliers(t)
	ks := sup["Kashmir Saffron Co"]
	var e errBody
	status, _ := call(t, "POST", "/v1/outlets/"+f.OutletID+"/stock/supplier-payments", f.OwnerToken, map[string]any{
		"id": ids.New(), "supplier_id": ks.ID, "amount": 200_000_000_000, "paid_by": "bank", // ₹200 crore
	}, &e)
	if status != http.StatusCreated && e.Error.Code != "bad_amount" {
		t.Fatalf("paying ₹200 crore of ₹500 crore owed (within the API's ₹1,000 crore limit): %d %s %q; "+
			"want it paid, or refused up front as bad_amount with the real limit", status, e.Error.Code, e.Error.Message)
	}
	status, _ = call(t, "POST", "/v1/outlets/"+f.OutletID+"/stock/supplier-payments", f.OwnerToken, map[string]any{
		"id": ids.New(), "supplier_id": ks.ID, "amount": 2_000_000_000_000, "paid_by": "bank",
	}, &e)
	if status != http.StatusBadRequest || strings.Contains(e.Error.Message, "above zero") {
		b, _ := json.Marshal(e)
		t.Fatalf("₹2,000 crore is refused with %d %s: the message should say the limit, not that it must be above zero", status, b)
	}
}
