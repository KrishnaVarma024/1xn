package e2e

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

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
	TaxProfileID  string
	CategoryID    string
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
	f.ItemID, f.TaxProfileID, f.CategoryID = item.ID, tp.ID, cat.ID

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
	orderID   *uuid.UUID  // link the bill to a table order
	lineIDs   []uuid.UUID // the order lines billed, aligned with the input lines
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
		OrderID: o.orderID, LineIDs: o.lineIDs,
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

// pair registers another device of the given role for the same outlet. The
// copy shares the owner and tenant fields, with its own token, series and
// op sequence.
func (f *fixture) pair(t *testing.T, role, name string) *fixture {
	t.Helper()
	var code struct {
		Code string `json:"code"`
	}
	must(t, http.StatusCreated, "POST", "/v1/outlets/"+f.OutletID+"/pairing-codes", f.OwnerToken,
		map[string]any{"role": role}, &code)
	var reg struct {
		DeviceID      string `json:"device_id"`
		DeviceToken   string `json:"device_token"`
		InvoicePrefix string `json:"invoice_prefix"`
	}
	must(t, http.StatusCreated, "POST", "/v1/devices/register", "",
		map[string]any{"pairing_code": code.Code, "name": name}, &reg)
	d := *f
	d.DeviceID, d.DeviceToken, d.Prefix, d.nextSeq, d.nextInvoice = reg.DeviceID, reg.DeviceToken, reg.InvoicePrefix, 1, 1
	return &d
}

// op wraps a payload as the device's next op.
func (f *fixture) op(typ string, payload any) syncgw.Op {
	b, _ := json.Marshal(payload)
	o := syncgw.Op{OpID: ids.New().String(), DeviceSeq: f.nextSeq, Type: typ, Payload: b}
	f.nextSeq++
	return o
}

// apply pushes ops and insists each one is applied (or a harmless duplicate).
func (f *fixture) apply(t *testing.T, ops ...syncgw.Op) syncgw.PushResponse {
	t.Helper()
	res := f.push(t, ops...)
	for i, a := range res.Acks {
		if a.Status != "applied" && a.Status != "duplicate" {
			t.Fatalf("op %d (%s) not applied: %+v", i, ops[i].Type, a)
		}
	}
	return res
}

// orderDoc is the order JSON devices pull.
type orderDoc struct {
	ID          string  `json:"id"`
	Status      string  `json:"status"`
	TableID     *string `json:"table_id"`
	Covers      int     `json:"covers"`
	MergedInto  *string `json:"merged_into"`
	SpilloverOf *string `json:"spillover_of"`
	Version     int64   `json:"version"`
	LastSeq     int64   `json:"last_seq"`
	Lines       []struct {
		ID          string  `json:"id"`
		Name        string  `json:"name"`
		Status      string  `json:"status"`
		KOTID       *string `json:"kot_id"`
		BillID      *string `json:"bill_id"`
		FromOrderID *string `json:"from_order_id"`
		Void        *struct {
			Reason     string `json:"reason"`
			ApprovedBy string `json:"approved_by"`
		} `json:"void"`
	} `json:"lines"`
	KOTs []struct {
		ID      string   `json:"id"`
		No      string   `json:"no"`
		Status  string   `json:"status"`
		LineIDs []string `json:"line_ids"`
	} `json:"kots"`
}

func (o orderDoc) lineStatus(id string) string {
	for _, l := range o.Lines {
		if l.ID == id {
			return l.Status
		}
	}
	return "missing"
}

type pullRes struct {
	Orders  []orderDoc `json:"orders"`
	Cursor  int64      `json:"cursor"`
	HasMore bool       `json:"has_more"`
}

func (f *fixture) pull(t *testing.T, after int64, limit int) pullRes {
	t.Helper()
	var res pullRes
	path := "/v1/sync/pull?after=" + strconv.FormatInt(after, 10)
	if limit > 0 {
		path += "&limit=" + strconv.Itoa(limit)
	}
	must(t, http.StatusOK, "GET", path, f.DeviceToken, nil, &res)
	return res
}

func (f *fixture) order(t *testing.T, id string) orderDoc {
	t.Helper()
	var o orderDoc
	must(t, http.StatusOK, "GET", "/v1/orders/"+id, f.OwnerToken, nil, &o)
	return o
}

func (r pullRes) find(id string) (orderDoc, bool) {
	for _, o := range r.Orders {
		if o.ID == id {
			return o, true
		}
	}
	return orderDoc{}, false
}
