package e2e

// Adversarial review of slice 8: staff sign-in with a code, roles and
// permissions. Every test names a defect and failed against the code as
// reviewed; it passes once the defect is fixed.

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"1xn/core/internal/platform/ids"
)

// ---------------------------------------------------------------------------
// A1. A sign-in that races the owner switching the person off survives it.
//
// startSession checks that the person is still active (and still has the
// number the code went to) with a plain SELECT, then inserts the session.
// Switching someone off updates app_user and revokes their sessions in one
// transaction. Neither locks what the other checks: if the switch-off commits
// between the sign-in's check and its insert, the revoke has already run, so
// the new session is never revoked. It is useless while the person is off
// (auth_resolve checks active on every request), but when the owner switches
// them on again it works, although "switching off signs them out everywhere"
// and switching on is meant to need a fresh sign-in. The same race lets a
// session outlive a change of number.
//
// Scenario (made deterministic by a gate on the tenant row, which the
// session's INSERT must reference): Asha enters her code on her phone just as
// the owner switches her off for the night. Next week the owner switches her
// back on; the old phone in her locker is signed in again.
// ---------------------------------------------------------------------------
func TestReviewA1SignInRacingASwitchOffSurvivesIt(t *testing.T) {
	f := newRestaurant(t, "Review Racing Sign In", "", "29")
	phone := testPhone()
	asha := f.addStaff(t, "Asha", phone, "cashier")

	var ch challenge
	must(t, http.StatusAccepted, "POST", "/v1/auth/otp", "", map[string]any{"phone": phone}, &ch)
	var v verified
	must(t, http.StatusOK, "POST", "/v1/auth/otp/verify", "", map[string]any{"challenge_id": ch.ID, "code": codes.take(t, phone)}, &v)

	// The gate: hold the tenant row, so the session's INSERT (its foreign
	// key to tenant) waits after the active check.
	gate, err := ownerDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback()
	if _, err := gate.Exec(`SELECT 1 FROM tenant WHERE id = $1 FOR UPDATE`, f.TenantID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var started int
	var s session
	wg.Add(1)
	go func() {
		defer wg.Done()
		started, _ = call(t, "POST", "/v1/auth/sessions", "", map[string]any{"ticket": v.Ticket, "user_id": asha.ID}, &s)
	}()
	waitForLockWaiters(t, 1)

	// The owner switches her off: it either finishes, or (once fixed) waits.
	off := make(chan int, 1)
	go func() {
		status, _ := call(t, "PATCH", "/v1/staff/"+asha.ID.String(), f.OwnerToken, map[string]any{"active": false}, nil)
		off <- status
	}()
	var switched int
	select {
	case switched = <-off:
	case <-time.After(2 * time.Second): // waiting on the sign-in: the fix
	}
	if err := gate.Commit(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if switched == 0 {
		switched = <-off
	}
	if switched != http.StatusOK {
		t.Fatalf("switch-off: %d", switched)
	}

	must(t, http.StatusOK, "PATCH", "/v1/staff/"+asha.ID.String(), f.OwnerToken, map[string]any{"active": true}, nil)
	if started == http.StatusCreated {
		if status, _ := call(t, "GET", "/v1/me", s.Token, nil, nil); status != http.StatusUnauthorized {
			t.Fatalf("a session made while Asha was being switched off works again after she is switched on (%d); "+
				"switching off must end every session, including one being made at that moment", status)
		}
	}
}

// ---------------------------------------------------------------------------
// A2. A cashier can book a delivery as paid from the safe or the bank.
//
// Receiving goods is a cashier's job (stock.receive), and a delivery is paid
// one of four ways. Two of them, the safe (1105) and the bank (1140), move
// money a cashier does not hold: the entry credits the safe or the bank, and
// nothing a cashier counts ever checks it (a drawer is counted at shift close;
// a till payout is matched through 1150; credit is paid later by the owner).
//
// Scenario: a cashier books "₹4,000 of paneer, paid from the safe" that never
// arrived and takes ₹4,000 from the safe. The safe balances in the books; the
// missing paneer surfaces weeks later as a count difference, blamed on the
// kitchen. Separation of duties: whoever handles goods should not also be able
// to say money left the safe or the bank.
//
// Impact: the one permission a cashier has over stock lets them move money in
// the books. Paying from a till (counted at shift close) and on credit (paid
// later by the owner) are what a cashier needs at the back door.
// ---------------------------------------------------------------------------
func TestReviewA2CashierBooksADeliveryPaidFromTheSafe(t *testing.T) {
	f := newRestaurant(t, "Review Cashier Safe", "", "29")
	phone := testPhone()
	f.addStaff(t, "Asha", phone, "cashier")
	s := signInAs(t, phone)
	paneer := f.stockItem(t, "Paneer", "kg", 0)
	buy := func(paidBy string) (int, string) {
		var e errBody
		status, _ := call(t, "POST", "/v1/outlets/"+f.OutletID+"/stock/purchases", s.Token, map[string]any{
			"id": ids.New(), "supplier": "Bandra Dairy", "paid_by": paidBy,
			"lines": []any{map[string]any{"stock_item_id": paneer, "qty": 1_000, "cost": 40_000}},
		}, &e)
		return status, e.Error.Code
	}
	for _, paidBy := range []string{"credit", "till"} {
		if status, code := buy(paidBy); status != http.StatusCreated {
			t.Fatalf("a cashier receiving goods %s: %d %s", paidBy, status, code)
		}
	}
	for _, paidBy := range []string{"safe", "bank"} {
		if status, code := buy(paidBy); status != http.StatusForbidden || code != "forbidden" {
			t.Errorf("a cashier booking a delivery paid from the %s: %d %s, want 403 forbidden", paidBy, status, code)
		}
	}
}
