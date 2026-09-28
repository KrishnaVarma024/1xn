package e2e

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"1xn/core/internal/platform/gst"
	"1xn/core/internal/platform/ids"
	"1xn/core/modules/sales"
	"1xn/core/modules/sales/pricing"
	"1xn/core/modules/syncgw"
)

// fixture is one restaurant: a tenant with a menu and one paired POS.
type fixture struct {
	OwnerToken    string
	TenantID      string
	OutletID      string
	LegalEntityID string
	DeviceID      string
	DeviceToken   string
	Prefix        string
	ItemID        string
	nextSeq       int64
	nextInvoice   int
}

// Friday 28 Sep 2026, 9:14 pm in Bengaluru.
var dinner = time.Date(2026, 9, 28, 21, 14, 3, 0, gst.IST)

func newRestaurant(t *testing.T, name, gstin, state string) *fixture {
	t.Helper()
	f := &fixture{nextSeq: 1, nextInvoice: 1}

	var signup struct {
		TenantID      string `json:"tenant_id"`
		LegalEntityID string `json:"legal_entity_id"`
		OutletID      string `json:"outlet_id"`
		OwnerToken    string `json:"owner_token"`
	}
	must(t, http.StatusCreated, "POST", "/v1/signup", "", map[string]any{
		"business_name": name,
		"owner":         map[string]any{"name": "Ravi", "phone": "+919876543210"},
		"legal_entity":  map[string]any{"name": name + " Pvt Ltd", "gstin": gstin, "state_code": state},
		"outlet":        map[string]any{"name": "Indiranagar"},
	}, &signup)
	f.TenantID, f.LegalEntityID, f.OutletID, f.OwnerToken =
		signup.TenantID, signup.LegalEntityID, signup.OutletID, signup.OwnerToken

	var tp, cat, item struct {
		ID string `json:"id"`
	}
	must(t, http.StatusCreated, "POST", "/v1/catalogue/tax-profiles", f.OwnerToken,
		map[string]any{"name": "Restaurant service 5%", "rate_bp": 500, "hsn_sac": "996331"}, &tp)
	must(t, http.StatusCreated, "POST", "/v1/catalogue/categories", f.OwnerToken,
		map[string]any{"name": "Mains", "sort": 1}, &cat)
	must(t, http.StatusCreated, "POST", "/v1/catalogue/items", f.OwnerToken, map[string]any{
		"category_id": cat.ID, "tax_profile_id": tp.ID, "name": "Pav bhaji", "price_paise": 9500,
		"channel_prices": map[string]int64{"swiggy": 11000},
	}, &item)
	f.ItemID = item.ID

	var code struct {
		Code string `json:"code"`
	}
	must(t, http.StatusCreated, "POST", "/v1/outlets/"+f.OutletID+"/pairing-codes", f.OwnerToken,
		map[string]any{"role": "pos"}, &code)
	var reg struct {
		DeviceID      string `json:"device_id"`
		DeviceToken   string `json:"device_token"`
		InvoicePrefix string `json:"invoice_prefix"`
	}
	must(t, http.StatusCreated, "POST", "/v1/devices/register", "",
		map[string]any{"pairing_code": code.Code, "name": "Counter 1"}, &reg)
	f.DeviceID, f.DeviceToken, f.Prefix = reg.DeviceID, reg.DeviceToken, reg.InvoicePrefix
	return f
}

// billOpts tweaks a bill before it is sent.
type billOpts struct {
	issuedAt  time.Time
	invoiceNo string
	payments  []sales.Payment
	tamper    func(in *pricing.Input) // change the input AFTER pricing, to fake a device bug
	billID    string
}

// bill prices lines exactly like the device will (same engine, same rules)
// and wraps the result in a bill.settled op.
func (f *fixture) bill(t *testing.T, in pricing.Input, o billOpts) syncgw.Op {
	t.Helper()
	totals, err := pricing.Compute(in)
	if err != nil {
		t.Fatal(err)
	}
	if o.tamper != nil {
		o.tamper(&in)
	}
	if o.issuedAt.IsZero() {
		o.issuedAt = dinner
	}
	if o.invoiceNo == "" {
		o.invoiceNo = gst.InvoiceNo(f.Prefix, gst.FY(o.issuedAt), f.nextInvoice)
		f.nextInvoice++
	}
	if o.payments == nil {
		o.payments = []sales.Payment{{Method: "cash", AmountPaise: totals.GrandTotal}}
	}
	if o.billID == "" {
		o.billID = ids.New().String()
	}
	inRaw, _ := json.Marshal(in)
	totRaw, _ := json.Marshal(totals)
	payload, _ := json.Marshal(sales.SettledBill{
		BillID: o.billID, InvoiceNo: o.invoiceNo, IssuedAt: o.issuedAt,
		BusinessDate: o.issuedAt.Format(time.DateOnly), Channel: "dine_in", TableLabel: "T4", Covers: 2,
		Input: inRaw, Totals: totRaw, Payments: o.payments,
	})
	op := syncgw.Op{OpID: ids.New().String(), DeviceSeq: f.nextSeq, Type: sales.OpBillSettled, Payload: payload}
	f.nextSeq++
	return op
}

func (f *fixture) push(t *testing.T, ops ...syncgw.Op) syncgw.PushResponse {
	t.Helper()
	var res syncgw.PushResponse
	must(t, http.StatusOK, "POST", "/v1/sync/push", f.DeviceToken, syncgw.PushRequest{Ops: ops}, &res)
	if len(res.Acks) != len(ops) || !res.Complete {
		t.Fatalf("push: %d acks for %d ops, complete=%v", len(res.Acks), len(ops), res.Complete)
	}
	return res
}

// pavBhaji is the Rs 95 dish from the golden cases: 95 + 2.38 + 2.38 = 99.76,
// rounded to Rs 100.
func pavBhaji(qty int64) pricing.Input {
	return pricing.Input{
		PriceMode: pricing.Exclusive, Supply: pricing.Intra, Rounding: pricing.NearestRupee,
		Lines: []pricing.Line{{Ref: "pav", Name: "Pav bhaji", Qty: qty, UnitPricePaise: 9500, TaxRateBP: 500}},
	}
}

type billView struct {
	InvoiceNo    string          `json:"invoice_no"`
	GrandTotal   int64           `json:"grand_total"`
	Verification string          `json:"verification"`
	LedgerStatus string          `json:"ledger_status"`
	ServerSeq    int64           `json:"server_seq"`
	ServerResult json.RawMessage `json:"server_result"`
	JournalLines []struct {
		AccountCode string `json:"account_code"`
		Amount      int64  `json:"amount"`
	} `json:"journal_lines"`
}

func (f *fixture) getBill(t *testing.T, op syncgw.Op) billView {
	t.Helper()
	var b sales.SettledBill
	_ = json.Unmarshal(op.Payload, &b)
	var v billView
	must(t, http.StatusOK, "GET", "/v1/bills/"+b.BillID, f.OwnerToken, nil, &v)
	return v
}

func (f *fixture) alerts(t *testing.T) map[string]int {
	t.Helper()
	var res struct {
		Alerts []struct {
			Kind string `json:"kind"`
		} `json:"alerts"`
	}
	must(t, http.StatusOK, "GET", "/v1/alerts", f.OwnerToken, nil, &res)
	kinds := map[string]int{}
	for _, a := range res.Alerts {
		kinds[a.Kind]++
	}
	return kinds
}
