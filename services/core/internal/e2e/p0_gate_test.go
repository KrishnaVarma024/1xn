package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"1xn/core/modules/ledger"
	"1xn/core/modules/sales"
	"1xn/core/modules/sales/pricing"
	"1xn/core/modules/syncgw"
)

// TestP0Gate is the exit gate of phase P0 from the architecture doc:
// "A test tenant signs up, a device registers, one fake bill syncs and posts
// a balanced entry." Plus the menu publish path the POS needs.
func TestP0Gate(t *testing.T) {
	r := newRestaurant(t, "Spice Route", "27AAPFU0939F1ZV", "27")

	// The till downloads the menu and gets an ETag...
	var menu struct {
		Version int64 `json:"version"`
		Outlet  struct {
			PriceMode string `json:"price_mode"`
			GSTIN     string `json:"gstin"`
		} `json:"outlet"`
		Items []struct {
			Name          string           `json:"name"`
			TaxRateBP     int64            `json:"tax_rate_bp"`
			ChannelPrices map[string]int64 `json:"channel_prices"`
		} `json:"items"`
	}
	h := must(t, http.StatusOK, "GET", "/v1/devices/me/menu", r.DeviceToken, nil, &menu)
	if len(menu.Items) != 1 || menu.Items[0].TaxRateBP != 500 || menu.Items[0].ChannelPrices["swiggy"] != 11000 {
		t.Fatalf("menu: %+v", menu)
	}
	if menu.Outlet.GSTIN != "27AAPFU0939F1ZV" || menu.Outlet.PriceMode != "exclusive" {
		t.Fatalf("outlet settings missing from menu: %+v", menu.Outlet)
	}
	// ...and polling with it costs a 304 until the menu changes.
	must(t, http.StatusNotModified, "GET", "/v1/devices/me/menu", r.DeviceToken, nil, nil, "If-None-Match", h.Get("ETag"))
	must(t, http.StatusOK, "PATCH", "/v1/catalogue/items/"+r.ItemID+"/availability", r.OwnerToken,
		map[string]any{"available": false}, nil)
	must(t, http.StatusOK, "GET", "/v1/devices/me/menu", r.DeviceToken, nil, nil, "If-None-Match", h.Get("ETag"))

	// The device bills one pav bhaji offline and syncs it.
	op := r.bill(t, pavBhaji(1), billOpts{})
	res := r.push(t, op)
	if res.Acks[0].Status != "applied" || res.Acks[0].ServerSeq != 1 {
		t.Fatalf("first push: %+v", res.Acks[0])
	}

	// The network flickers; the device re-sends. Nothing is applied twice.
	res = r.push(t, op)
	if res.Acks[0].Status != "duplicate" || res.Acks[0].ServerSeq != 1 {
		t.Fatalf("re-push: %+v", res.Acks[0])
	}

	// The bill is stored, verified, and posted as a balanced entry.
	b := r.getBill(t, op)
	if b.Verification != "match" || b.LedgerStatus != "posted" || b.GrandTotal != 10000 {
		t.Fatalf("bill: %+v", b)
	}
	got := map[string]int64{}
	var sum int64
	for _, l := range b.JournalLines {
		got[l.AccountCode] += l.Amount
		sum += l.Amount
	}
	want := map[string]int64{
		ledger.AcctCash: 10000, ledger.AcctSales: -9500,
		ledger.AcctOutputCGST: -238, ledger.AcctOutputSGST: -238, ledger.AcctRoundOff: -24,
	}
	if sum != 0 || len(got) != len(want) {
		t.Fatalf("journal lines %v (sum %d)", got, sum)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("account %s = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}

	// And the trial balance says the books balance.
	var tb struct {
		Total    int64 `json:"total"`
		Balanced bool  `json:"balanced"`
	}
	must(t, http.StatusOK, "GET", "/v1/ledger/trial-balance", r.OwnerToken, nil, &tb)
	if !tb.Balanced || tb.Total != 0 {
		t.Fatalf("trial balance: %+v", tb)
	}

	// Exactly one bill, one journal entry, one outbox event.
	for q, n := range map[string]int{
		`SELECT count(*) FROM bill WHERE tenant_id = $1`:          1,
		`SELECT count(*) FROM journal_entry WHERE tenant_id = $1`: 1,
		`SELECT count(*) FROM outbox WHERE tenant_id = $1`:        1,
	} {
		var c int
		if err := ownerDB.QueryRow(q, r.TenantID).Scan(&c); err != nil || c != n {
			t.Fatalf("%s = %d (%v), want %d", q, c, err, n)
		}
	}
}

// TestConcurrentRetriesApplyOnce fires the same op ten times at once, as a
// flaky network plus an eager retry loop might. Exactly one wins.
func TestConcurrentRetriesApplyOnce(t *testing.T) {
	r := newRestaurant(t, "Chai Point", "", "29")
	op := r.bill(t, pavBhaji(2), billOpts{})

	var wg sync.WaitGroup
	statuses := make(chan string, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// No t.Fatal in here: only the test's own goroutine may call it.
			status, res, err := pushRaw(r.DeviceToken, op)
			if err != nil {
				statuses <- "error: " + err.Error()
				return
			}
			if status == http.StatusOK && len(res.Acks) == 1 {
				statuses <- res.Acks[0].Status
			} else {
				statuses <- "http_" + http.StatusText(status)
			}
		}()
	}
	wg.Wait()
	close(statuses)
	count := map[string]int{}
	for s := range statuses {
		count[s]++
	}
	if count["applied"] != 1 || count["duplicate"] != 9 {
		t.Fatalf("want 1 applied + 9 duplicate, got %v", count)
	}
	var bills, entries int
	ownerDB.QueryRow(`SELECT count(*) FROM bill WHERE tenant_id = $1`, r.TenantID).Scan(&bills)
	ownerDB.QueryRow(`SELECT count(*) FROM journal_entry WHERE tenant_id = $1`, r.TenantID).Scan(&entries)
	if bills != 1 || entries != 1 {
		t.Fatalf("bills %d, entries %d", bills, entries)
	}
}

// TestMismatchIsKeptAndFlagged: the device printed a total our engine does not
// agree with. The guest already has that invoice, so we keep it, post it,
// store our own answer next to it, and raise a critical alert.
func TestMismatchIsKeptAndFlagged(t *testing.T) {
	r := newRestaurant(t, "Tiffin Co", "", "29")
	in := pavBhaji(1)
	in.Rounding = pricing.None // device (buggy) prices without rounding: 99.76
	op := r.bill(t, in, billOpts{tamper: func(in *pricing.Input) { in.Rounding = pricing.NearestRupee }})
	if s := r.push(t, op).Acks[0].Status; s != "applied" {
		t.Fatalf("status %s", s)
	}
	b := r.getBill(t, op)
	if b.Verification != "mismatch" || b.GrandTotal != 9976 || len(b.ServerResult) == 0 || b.LedgerStatus != "posted" {
		t.Fatalf("bill: %+v", b)
	}
	if r.alerts(t)["price_mismatch"] != 1 {
		t.Fatalf("expected a price_mismatch alert, got %v", r.alerts(t))
	}
}

// TestInvoiceRules covers the ways an invoice number can be wrong.
func TestInvoiceRules(t *testing.T) {
	r := newRestaurant(t, "Dosa Hut", "", "29")

	good := r.bill(t, pavBhaji(1), billOpts{})
	r.push(t, good)
	var gb sales.SettledBill
	_ = jsonUnmarshal(good.Payload, &gb)

	cases := []struct {
		name string
		op   syncgw.Op
		want string
	}{
		{"another device's series", r.bill(t, pavBhaji(1), billOpts{invoiceNo: "ZZZ-2627-000001"}), "wrong_invoice_series"},
		{"wrong financial year", r.bill(t, pavBhaji(1), billOpts{invoiceNo: r.Prefix + "-2526-000009"}), "wrong_financial_year"},
		{"reused number, different bill", r.bill(t, pavBhaji(3), billOpts{invoiceNo: gb.InvoiceNo}), "invoice_conflict"},
		{"not a number at all", r.bill(t, pavBhaji(1), billOpts{invoiceNo: "INV/1"}), "bad_invoice_no"},
	}
	for _, c := range cases {
		ack := r.push(t, c.op).Acks[0]
		if ack.Status != "rejected" || ack.Error == nil || ack.Error.Code != c.want {
			t.Errorf("%s: got %+v, want rejected %s", c.name, ack, c.want)
		}
	}

	// A rejected op is remembered: re-sending it gets the same answer, and its
	// payload is kept for a human to inspect. Nothing is lost.
	ack := r.push(t, cases[0].op).Acks[0]
	if ack.Status != "rejected" || ack.Error.Code != "wrong_invoice_series" {
		t.Fatalf("re-sent rejected op: %+v", ack)
	}
	var kept int
	ownerDB.QueryRow(`SELECT count(*) FROM sync_inbox WHERE tenant_id = $1 AND status = 'rejected' AND payload IS NOT NULL`,
		r.TenantID).Scan(&kept)
	if kept != len(cases) {
		t.Fatalf("kept %d rejected payloads, want %d", kept, len(cases))
	}

	// The same bill re-queued under a NEW op id is recognised as a duplicate.
	again := good
	again.OpID = newOpID()
	again.DeviceSeq = r.nextSeq
	r.nextSeq++
	if s := r.push(t, again).Acks[0].Status; s != "duplicate" {
		t.Fatalf("same bill, new op id: %s", s)
	}

	// An op id reused for a different payload is refused.
	reused := r.bill(t, pavBhaji(5), billOpts{})
	reused.OpID = good.OpID
	if ack := r.push(t, reused).Acks[0]; ack.Status != "rejected" || ack.Error.Code != "op_id_reused" {
		t.Fatalf("reused op id: %+v", ack)
	}
}

// TestSequenceGapIsFlagged: a device that skips device_seq numbers lost ops
// before sending them. We still apply what arrived, and tell a human.
func TestSequenceGapIsFlagged(t *testing.T) {
	r := newRestaurant(t, "Momo Bar", "", "29")
	r.nextSeq = 5 // ops 1-4 never arrived
	if s := r.push(t, r.bill(t, pavBhaji(1), billOpts{})).Acks[0].Status; s != "applied" {
		t.Fatalf("status %s", s)
	}
	if r.alerts(t)["device_seq_gap"] != 1 {
		t.Fatalf("expected a device_seq_gap alert, got %v", r.alerts(t))
	}
}

// TestUnderpaymentParksInSuspense: the device recorded Rs 90 against a Rs 100
// bill. The books still balance; the Rs 10 waits in suspense for a human.
func TestUnderpaymentParksInSuspense(t *testing.T) {
	r := newRestaurant(t, "Kulfi Corner", "", "29")
	op := r.bill(t, pavBhaji(1), billOpts{payments: []sales.Payment{{Method: "upi", AmountPaise: 9000}}})
	r.push(t, op)
	b := r.getBill(t, op)
	var suspense, sum int64
	for _, l := range b.JournalLines {
		sum += l.Amount
		if l.AccountCode == ledger.AcctSuspense {
			suspense = l.Amount
		}
	}
	if sum != 0 || suspense != 1000 {
		t.Fatalf("sum %d, suspense %d", sum, suspense)
	}
	if r.alerts(t)["payment_difference"] != 1 {
		t.Fatalf("expected payment_difference alert")
	}
}

// TestPairingCodeIsSingleUseAndExpires.
func TestPairingCodeIsSingleUseAndExpires(t *testing.T) {
	r := newRestaurant(t, "Biryani Bros", "", "29")
	var code struct {
		Code string `json:"code"`
	}
	must(t, http.StatusCreated, "POST", "/v1/outlets/"+r.OutletID+"/pairing-codes", r.OwnerToken, map[string]any{"role": "captain"}, &code)
	var reg struct {
		InvoicePrefix string `json:"invoice_prefix"`
	}
	must(t, http.StatusCreated, "POST", "/v1/devices/register", "", map[string]any{"pairing_code": code.Code, "name": "Captain 1"}, &reg)
	if reg.InvoicePrefix == r.Prefix {
		t.Fatalf("second device got the same invoice series %s", reg.InvoicePrefix)
	}
	must(t, http.StatusBadRequest, "POST", "/v1/devices/register", "", map[string]any{"pairing_code": code.Code, "name": "Captain 2"}, nil)

	must(t, http.StatusCreated, "POST", "/v1/outlets/"+r.OutletID+"/pairing-codes", r.OwnerToken, map[string]any{"role": "pos"}, &code)
	if _, err := ownerDB.Exec(`UPDATE pairing_code SET expires_at = now() - interval '1 second' WHERE used_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	must(t, http.StatusBadRequest, "POST", "/v1/devices/register", "", map[string]any{"pairing_code": code.Code, "name": "Late"}, nil)
}

func TestSignupValidation(t *testing.T) {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	must(t, http.StatusBadRequest, "POST", "/v1/signup", "", map[string]any{
		"business_name": "Typo Foods",
		"owner":         map[string]any{"name": "A", "phone": "+919876543210"},
		"legal_entity":  map[string]any{"name": "Typo Foods", "gstin": "27AAPFU0939F1ZW"}, // last char wrong
		"outlet":        map[string]any{"name": "Main"},
	}, &e)
	if e.Error.Code != "bad_gstin" {
		t.Fatalf("code %q", e.Error.Code)
	}
	// Unknown fields are refused, so typos do not pass silently.
	must(t, http.StatusBadRequest, "POST", "/v1/signup", "", map[string]any{"busines_name": "x"}, nil)
	// No token, wrong kind of token.
	must(t, http.StatusUnauthorized, "GET", "/v1/alerts", "", nil, nil)
	r := newRestaurant(t, "Token Test", "", "29")
	must(t, http.StatusForbidden, "GET", "/v1/alerts", r.DeviceToken, nil, nil)
	must(t, http.StatusForbidden, "POST", "/v1/sync/push", r.OwnerToken, syncgw.PushRequest{}, nil)
}

// pushRaw is a goroutine-safe push that reports errors instead of failing the test.
func pushRaw(token string, op syncgw.Op) (int, syncgw.PushResponse, error) {
	var res syncgw.PushResponse
	b, err := json.Marshal(syncgw.PushRequest{Ops: []syncgw.Op{op}})
	if err != nil {
		return 0, res, err
	}
	req, err := http.NewRequest("POST", api.URL+"/v1/sync/push", bytes.NewReader(b))
	if err != nil {
		return 0, res, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, res, err
	}
	defer resp.Body.Close()
	err = json.NewDecoder(resp.Body).Decode(&res)
	return resp.StatusCode, res, err
}
