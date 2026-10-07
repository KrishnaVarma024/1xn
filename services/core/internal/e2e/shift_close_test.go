package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"1xn/core/internal/platform/ids"
	"1xn/core/modules/dayclose"
	"1xn/core/modules/syncgw"
)

type errBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (f *fixture) openShifts(t *testing.T) []dayclose.OpenShift {
	t.Helper()
	var out struct {
		Shifts []dayclose.OpenShift `json:"shifts"`
	}
	must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/shifts", f.OwnerToken, nil, &out)
	return out.Shifts
}

// closeShiftHere closes a shift from the back office and returns the status,
// the shift (on success) and the error code (on failure).
func (f *fixture) closeShiftHere(t *testing.T, shift uuid.UUID, body map[string]any) (int, dayclose.Shift, string) {
	t.Helper()
	var raw json.RawMessage
	status, _ := call(t, "POST", "/v1/outlets/"+f.OutletID+"/shifts/"+shift.String()+"/close", f.OwnerToken, body, &raw)
	var s dayclose.Shift
	var e errBody
	if status < 300 {
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
	} else {
		_ = json.Unmarshal(raw, &e)
	}
	return status, s, e.Error.Code
}

func journalEntries(t *testing.T, sourceType string, source uuid.UUID) int {
	t.Helper()
	var n int
	if err := ownerDB.QueryRow(`SELECT count(*) FROM journal_entry WHERE source_type = $1 AND source_event_id = $2`,
		sourceType, source).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestBackOfficeClosesAShift: a till is revoked with its shift open. The
// owner counts the drawer and closes the shift in the back office: same
// expected cash as a till close, the shortage booked once, the day clear.
func TestBackOfficeClosesAShift(t *testing.T) {
	till := newRestaurant(t, "Revoked Till Cafe", "", "29")
	shift := ids.New()
	b := pavBhaji(2)
	cash := total(t, b)
	till.apply(t,
		till.openShift(shift, at(18, 0), "Vikram", 100_000),
		till.bill(t, b, billOpts{issuedAt: at(19, 0)}),
		till.moveCash(shift, "expense", 20_000, "Ice", at(19, 30)),
	)
	expected := 100_000 + cash - 20_000

	open := till.openShifts(t)
	if len(open) != 1 || open[0].ID != shift || open[0].Expected != expected || open[0].DeviceRevoked ||
		open[0].BusinessDate != closeDay || open[0].Expenses != 20_000 {
		t.Fatalf("open shifts: %+v (want expected %d)", open, expected)
	}
	must(t, http.StatusOK, "POST", "/v1/devices/"+till.DeviceID+"/revoke", till.OwnerToken, nil, nil)
	if open = till.openShifts(t); len(open) != 1 || !open[0].DeviceRevoked {
		t.Fatalf("after revoking, the shift is still open and its till is marked revoked: %+v", open)
	}

	req := map[string]any{"id": ids.New(), "counted_paise": expected - 5_000, "note": "Till revoked; counted by Ravi"}
	status, s, code := till.closeShiftHere(t, shift, req)
	if status != http.StatusCreated {
		t.Fatalf("close: %d %s", status, code)
	}
	if s.Expected != expected || *s.Counted != expected-5_000 || *s.Difference != -5_000 || s.ClosedFrom != "back_office" ||
		s.CloseNote != "Till revoked; counted by Ravi" || s.ClosedBy != "Ravi" || s.LedgerStatus != "posted" || s.Expenses != 20_000 {
		t.Fatalf("closed shift: %+v", s)
	}

	// The same request again (a double click, a retry): the same answer, booked once.
	status, again, _ := till.closeShiftHere(t, shift, req)
	if status != http.StatusOK || *again.Difference != -5_000 || !again.ClosedAt.Equal(*s.ClosedAt) {
		t.Fatalf("replay: %d %+v", status, again)
	}
	if n := journalEntries(t, "shift_close", shift); n != 1 {
		t.Fatalf("the difference is booked once, got %d entries", n)
	}
	if n := till.alerts(t)["cash_difference"]; n != 1 {
		t.Fatalf("one cash_difference alert, got %v", till.alerts(t))
	}

	// The same id for another count, or another shift: refused, not replayed.
	if status, _, code := till.closeShiftHere(t, shift, map[string]any{"id": req["id"], "counted_paise": expected, "note": "x"}); status != http.StatusConflict || code != "id_used" {
		t.Fatalf("reused id: %d %s", status, code)
	}
	// A fresh close of a closed shift.
	if status, _, code := till.closeShiftHere(t, shift, map[string]any{"id": ids.New(), "counted_paise": expected, "note": "again"}); status != http.StatusConflict || code != "shift_closed" {
		t.Fatalf("closing twice: %d %s", status, code)
	}

	if open = till.openShifts(t); len(open) != 0 {
		t.Fatalf("no open shifts left: %+v", open)
	}
	v := till.day(t, closeDay)
	if w := warningCodes(v.Report); w["open_shift"] != "" {
		t.Fatalf("the day no longer warns of an open shift: %v", w)
	}
	if len(v.Report.Shifts) != 1 || v.Report.Shifts[0].ClosedFrom != "back_office" || *v.Report.Shifts[0].Difference != -5_000 {
		t.Fatalf("the Z report shows the back-office close: %+v", v.Report.Shifts)
	}
	var balance int64
	if err := ownerDB.QueryRow(`
		SELECT coalesce(sum(l.amount), 0) FROM journal_line l JOIN account a ON a.id = l.account_id
		WHERE a.code = '5900' AND l.tenant_id = $1`, till.TenantID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 5_000 {
		t.Fatalf("5900 Cash over and short holds the ₹50 shortage as a debit, got %d", balance)
	}
}

// TestBackOfficeCloseNeedsARevokedTill: a till that still works closes its
// own shift (it may be offline with ops not yet sent). Once it is revoked it
// can send nothing more, and the owner's count is the last word.
func TestBackOfficeCloseNeedsARevokedTill(t *testing.T) {
	till := newRestaurant(t, "Offline Till Cafe", "", "29")
	shift := ids.New()
	b := pavBhaji(1)
	till.apply(t, till.openShift(shift, at(18, 0), "Vikram", 100_000), till.bill(t, b, billOpts{issuedAt: at(19, 0)}))
	expected := 100_000 + total(t, b)

	status, _, code := till.closeShiftHere(t, shift, map[string]any{"id": ids.New(), "counted_paise": expected, "note": "Cashier left"})
	if status != http.StatusConflict || code != "till_active" {
		t.Fatalf("a working till's shift: %d %s, want 409 till_active", status, code)
	}

	// A bill still queued on the till when it is revoked never arrives: the
	// till's token stops working at once.
	late := till.bill(t, pavBhaji(1), billOpts{issuedAt: at(20, 0)})
	must(t, http.StatusOK, "POST", "/v1/devices/"+till.DeviceID+"/revoke", till.OwnerToken, nil, nil)
	must(t, http.StatusUnauthorized, "POST", "/v1/sync/push", till.DeviceToken, syncgw.PushRequest{Ops: []syncgw.Op{late}}, nil)

	status, s, code := till.closeShiftHere(t, shift, map[string]any{"id": ids.New(), "counted_paise": expected, "note": "Till lost"})
	if status != http.StatusCreated || *s.Difference != 0 || s.LedgerStatus != "none" {
		t.Fatalf("close after revoking: %d %s %+v", status, code, s)
	}
}

func TestBackOfficeShiftCloseGuards(t *testing.T) {
	a := newRestaurant(t, "Guarded Drawer A", "", "29")
	b := newRestaurant(t, "Guarded Drawer B", "", "29")
	shift := ids.New()
	a.apply(t, a.openShift(shift, at(18, 0), "Vikram", 100_000))
	ok := func() map[string]any {
		return map[string]any{"id": ids.New(), "counted_paise": 100_000, "note": "Till broke"}
	}

	// Another restaurant cannot see or close it, by its own outlet or ours.
	must(t, http.StatusNotFound, "POST", "/v1/outlets/"+b.OutletID+"/shifts/"+shift.String()+"/close", b.OwnerToken, ok(), nil)
	must(t, http.StatusNotFound, "POST", "/v1/outlets/"+a.OutletID+"/shifts/"+shift.String()+"/close", b.OwnerToken, ok(), nil)
	must(t, http.StatusNotFound, "GET", "/v1/outlets/"+a.OutletID+"/shifts", b.OwnerToken, nil, nil)
	// A device is not an owner.
	must(t, http.StatusForbidden, "POST", "/v1/outlets/"+a.OutletID+"/shifts/"+shift.String()+"/close", a.DeviceToken, ok(), nil)
	// Unknown shift, bad shift id.
	must(t, http.StatusNotFound, "POST", "/v1/outlets/"+a.OutletID+"/shifts/"+ids.New().String()+"/close", a.OwnerToken, ok(), nil)
	must(t, http.StatusNotFound, "POST", "/v1/outlets/"+a.OutletID+"/shifts/nope/close", a.OwnerToken, ok(), nil)

	for name, body := range map[string]map[string]any{
		"v4 id":           {"id": uuid.New(), "counted_paise": 100, "note": "x"},
		"negative count":  {"id": ids.New(), "counted_paise": -1, "note": "x"},
		"no note":         {"id": ids.New(), "counted_paise": 100, "note": "   "},
		"control in note": {"id": ids.New(), "counted_paise": 100, "note": "a\u0000b"},
		"long note":       {"id": ids.New(), "counted_paise": 100, "note": strings.Repeat("a", 201)},
		"unknown field":   {"id": ids.New(), "counted_paise": 100, "note": "x", "expected": 100},
	} {
		if status, _, _ := a.closeShiftHere(t, shift, body); status != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, status)
		}
	}
	if status, _, code := a.closeShiftHere(t, shift, map[string]any{"id": ids.New(), "counted_paise": 100_000, "note": "x",
		"denominations": map[string]int64{"500": 1}}); status != http.StatusBadRequest || code != "denominations_mismatch" {
		t.Fatalf("notes that do not add up: %d %s", status, code)
	}

	// A till whose clock runs ahead opened its shift and sold "in the
	// future": the close still takes in everything it sent.
	ahead := a.pair(t, "pos", "Counter 2")
	future := ids.New()
	b1 := pavBhaji(1)
	ahead.apply(t, ahead.openShift(future, time.Now().Add(2*time.Hour), "Asha", 0),
		ahead.bill(t, b1, billOpts{issuedAt: time.Now().Add(150 * time.Minute)}))
	must(t, http.StatusOK, "POST", "/v1/devices/"+ahead.DeviceID+"/revoke", a.OwnerToken, nil, nil)
	status, s, code := a.closeShiftHere(t, future, map[string]any{"id": ids.New(), "counted_paise": total(t, b1), "note": "Till broke"})
	if status != http.StatusCreated || s.Expected != total(t, b1) || *s.Difference != 0 || s.ClosedAt.Before(s.OpenedAt) {
		t.Fatalf("clock ahead: %d %s %+v", status, code, s)
	}
}

// Two people close the same shift at the same moment, with different ids:
// one close wins, the other is told the shift is closed. With the same id:
// one creates, the other replays it.
func TestBackOfficeShiftCloseRace(t *testing.T) {
	f := newRestaurant(t, "Racing Drawer", "", "29")
	for round, sameID := range []bool{false, true} {
		till := f.pair(t, "pos", fmt.Sprintf("Counter %d", round+2))
		shift := ids.New()
		till.apply(t, till.openShift(shift, time.Now().Add(-time.Hour), "Vikram", 100_000))
		must(t, http.StatusOK, "POST", "/v1/devices/"+till.DeviceID+"/revoke", f.OwnerToken, nil, nil)
		id := ids.New()
		var wg sync.WaitGroup
		statuses := make([]int, 2)
		codes := make([]string, 2)
		for i := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				body := map[string]any{"id": id, "counted_paise": 100_000, "note": "Race"}
				if !sameID {
					body["id"] = ids.New()
				}
				statuses[i], _, codes[i] = f.closeShiftHere(t, shift, body)
			}()
		}
		wg.Wait()
		created, other := 0, 0
		for i, s := range statuses {
			switch {
			case s == http.StatusCreated:
				created++
			case sameID && s == http.StatusOK:
				other++
			case !sameID && s == http.StatusConflict && codes[i] == "shift_closed":
				other++
			}
		}
		if created != 1 || other != 1 {
			t.Fatalf("same id %v: statuses %v codes %v", sameID, statuses, codes)
		}
		if n := journalEntries(t, "shift_close", shift); n != 0 {
			t.Fatalf("an exact count books nothing, got %d entries", n)
		}
	}
}
