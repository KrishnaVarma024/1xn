// Command demo walks through the P0 exit gate against a running API and
// explains each step: a restaurant signs up, a till pairs, one bill is priced
// offline, synced twice (the second time is a harmless retry) and lands in the
// books as a balanced journal entry.
//
//	make run    # in one terminal
//	make demo   # in another (API_URL defaults to http://localhost:8080)
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"1xn/core/internal/platform/gst"
	"1xn/core/internal/platform/ids"
	"1xn/core/modules/sales"
	"1xn/core/modules/sales/pricing"
	"1xn/core/modules/syncgw"
)

var base = "http://localhost:8080"

func main() {
	if u := os.Getenv("API_URL"); u != "" {
		base = u
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "\ndemo failed:", err)
		os.Exit(1)
	}
}

func rupees(p int64) string {
	sign := ""
	if p < 0 {
		sign, p = "-", -p
	}
	return fmt.Sprintf("%sRs %d.%02d", sign, p/100, p%100)
}

func step(n int, title string) { fmt.Printf("\n%d. %s\n", n, title) }

func run() error {
	step(1, "A restaurant signs up (one transaction: tenant, GSTIN, outlet, owner, chart of accounts)")
	var signup struct {
		TenantID   string `json:"tenant_id"`
		OutletID   string `json:"outlet_id"`
		OwnerToken string `json:"owner_token"`
	}
	if err := call("POST", "/v1/signup", "", map[string]any{
		"business_name": "Spice Route " + time.Now().Format("150405"),
		"owner":         map[string]any{"name": "Ravi", "phone": "+919876543210"},
		"legal_entity":  map[string]any{"name": "Spice Route Foods Pvt Ltd", "gstin": "27AAPFU0939F1ZV"},
		"outlet":        map[string]any{"name": "Bandra"},
	}, &signup); err != nil {
		return err
	}
	fmt.Printf("   tenant %s, owner token %s... (shown once, stored only as a hash)\n", signup.TenantID, signup.OwnerToken[:12])
	owner := signup.OwnerToken

	step(2, "The owner builds a one-item menu")
	var tp, cat, item struct {
		ID string `json:"id"`
	}
	if err := call("POST", "/v1/catalogue/tax-profiles", owner, map[string]any{"name": "Restaurant 5%", "rate_bp": 500, "hsn_sac": "996331"}, &tp); err != nil {
		return err
	}
	if err := call("POST", "/v1/catalogue/categories", owner, map[string]any{"name": "Street food"}, &cat); err != nil {
		return err
	}
	if err := call("POST", "/v1/catalogue/items", owner, map[string]any{
		"category_id": cat.ID, "tax_profile_id": tp.ID, "name": "Pav bhaji", "price_paise": 9500,
	}, &item); err != nil {
		return err
	}
	fmt.Println("   Pav bhaji, Rs 95.00, GST 5% (SAC 996331), prices exclusive of tax")

	step(3, "The owner asks for a pairing code; the till types it in")
	var code struct {
		Code string `json:"code"`
	}
	if err := call("POST", "/v1/outlets/"+signup.OutletID+"/pairing-codes", owner, map[string]any{"role": "pos"}, &code); err != nil {
		return err
	}
	var dev struct {
		DeviceToken   string `json:"device_token"`
		InvoicePrefix string `json:"invoice_prefix"`
	}
	if err := call("POST", "/v1/devices/register", "", map[string]any{"pairing_code": code.Code, "name": "Counter 1"}, &dev); err != nil {
		return err
	}
	fmt.Printf("   code %s -> device paired, its own invoice series is %s-\n", code.Code, dev.InvoicePrefix)

	step(4, "The till downloads the menu (and would get 304 Not Modified next time)")
	var menu struct {
		Version int64 `json:"version"`
		Items   []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			PricePaise int64  `json:"price_paise"`
			TaxRateBP  int64  `json:"tax_rate_bp"`
		} `json:"items"`
	}
	if err := call("GET", "/v1/devices/me/menu", dev.DeviceToken, nil, &menu); err != nil {
		return err
	}
	fmt.Printf("   menu version %d, %d item(s)\n", menu.Version, len(menu.Items))

	step(5, "Offline, the till prices a bill with the same engine the server uses")
	it := menu.Items[0]
	in := pricing.Input{PriceMode: pricing.Exclusive, Supply: pricing.Intra, Rounding: pricing.NearestRupee,
		Lines: []pricing.Line{{Ref: it.ID, Name: it.Name, Qty: 1, UnitPricePaise: it.PricePaise, TaxRateBP: it.TaxRateBP}}}
	totals, err := pricing.Compute(in)
	if err != nil {
		return err
	}
	issued := time.Now().In(gst.IST)
	invoice := gst.InvoiceNo(dev.InvoicePrefix, gst.FY(issued), 1)
	fmt.Printf("   invoice %s\n   taxable %s + CGST %s + SGST %s + round-off %s = %s\n", invoice,
		rupees(totals.TaxableTotal), rupees(totals.CGST), rupees(totals.SGST), rupees(totals.RoundOff), rupees(totals.GrandTotal))

	inRaw, _ := json.Marshal(in)
	totRaw, _ := json.Marshal(totals)
	payload, _ := json.Marshal(sales.SettledBill{
		BillID: ids.New().String(), InvoiceNo: invoice, IssuedAt: issued,
		BusinessDate: issued.Format(time.DateOnly), Channel: "dine_in", TableLabel: "T4", Covers: 2,
		Input: inRaw, Totals: totRaw, Payments: []sales.Payment{{Method: "upi", AmountPaise: totals.GrandTotal}},
	})
	op := syncgw.Op{OpID: ids.New().String(), DeviceSeq: 1, Type: sales.OpBillSettled, Payload: payload}

	step(6, "The internet comes back: the till pushes the bill, then (network flicker) pushes it again")
	for i := 1; i <= 2; i++ {
		var res syncgw.PushResponse
		if err := call("POST", "/v1/sync/push", dev.DeviceToken, syncgw.PushRequest{Ops: []syncgw.Op{op}}, &res); err != nil {
			return err
		}
		fmt.Printf("   push %d -> %s (server seq %d)\n", i, res.Acks[0].Status, res.Acks[0].ServerSeq)
	}

	step(7, "The server re-priced the bill, stored it and posted it to the ledger")
	var bill struct {
		Verification string `json:"verification"`
		JournalLines []struct {
			AccountCode string `json:"account_code"`
			AccountName string `json:"account_name"`
			Amount      int64  `json:"amount"`
		} `json:"journal_lines"`
	}
	var b sales.SettledBill
	_ = json.Unmarshal(payload, &b)
	if err := call("GET", "/v1/bills/"+b.BillID, owner, nil, &bill); err != nil {
		return err
	}
	fmt.Printf("   verification: %s\n", bill.Verification)
	var sum int64
	for _, l := range bill.JournalLines {
		side, amt := "Dr", l.Amount
		if amt < 0 {
			side, amt = "Cr", -amt // stored as negative paise; shown the accountant's way
		}
		fmt.Printf("   %s %s %-32s %12s\n", side, l.AccountCode, l.AccountName, rupees(amt))
		sum += l.Amount
	}
	fmt.Printf("   lines sum to %s\n", rupees(sum))

	step(8, "Trial balance")
	var tb struct {
		Balanced bool `json:"balanced"`
	}
	if err := call("GET", "/v1/ledger/trial-balance", owner, nil, &tb); err != nil {
		return err
	}
	fmt.Printf("   balanced: %v\n\nP0 exit gate passed: tenant signed up, device registered, bill synced, balanced entry posted.\n", tb.Balanced)
	return nil
}

func call(method, path, token string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, base+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w (is the API running? try: make run)", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %d %s", method, path, res.StatusCode, raw)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}
