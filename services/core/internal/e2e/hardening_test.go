package e2e

// Regression tests for the issues found in the adversarial review of this
// slice. Each test names the failure it guards against.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"1xn/core/internal/platform/ids"
	"1xn/core/modules/sales"
	"1xn/core/modules/sales/pricing"
	"1xn/core/modules/syncgw"
)

// A fully comped bill used to fail ledger posting ("fewer than two lines") as
// a transient error, so the till retried it forever and every later bill was
// stuck behind it.
func TestFullCompBillDoesNotBlockTheQueue(t *testing.T) {
	r := newRestaurant(t, "Comp House", "", "29")
	comp := pavBhaji(2)
	comp.BillDiscount = &pricing.Discount{PercentBP: 10_000}
	compOp := r.bill(t, comp, billOpts{payments: []sales.Payment{}})
	next := r.bill(t, pavBhaji(1), billOpts{})

	res := r.push(t, compOp, next)
	if res.Acks[0].Status != "applied" || res.Acks[1].Status != "applied" {
		t.Fatalf("acks: %+v", res.Acks)
	}
	if b := r.getBill(t, compOp); b.LedgerStatus != "none" || b.GrandTotal != 0 || len(b.JournalLines) != 0 {
		t.Fatalf("comped bill: %+v", b)
	}
}

// Malformed ops must be rejected (and kept), never returned as 503 forever.
func TestMalformedOpsAreRejectedNotRetriedForever(t *testing.T) {
	r := newRestaurant(t, "Edge Cases", "", "29")

	mutate := func(op syncgw.Op, fn func(bill map[string]any)) syncgw.Op {
		var m map[string]any
		_ = json.Unmarshal(op.Payload, &m)
		fn(m)
		op.Payload, _ = json.Marshal(m)
		return op
	}

	// 1. Printed tax groups repeat a rate: would violate bill_tax_line's key.
	dupRate := mutate(r.bill(t, pavBhaji(1), billOpts{}), func(b map[string]any) {
		tot := b["totals"].(map[string]any)
		groups := tot["tax_groups"].([]any)
		tot["tax_groups"] = append(groups, groups[0])
	})
	// 2. A NUL character in an item name: valid JSON, but Postgres jsonb refuses it.
	in := pavBhaji(1)
	in.Lines[0].Name = "Pav\u0000bhaji"
	nul := r.bill(t, in, billOpts{})
	// 3. Absurd tax figures that would overflow int64 when added up.
	overflow := mutate(r.bill(t, pavBhaji(1), billOpts{}), func(b map[string]any) {
		tot := b["totals"].(map[string]any)
		tot["cgst"], tot["sgst"] = int64(1)<<62, int64(1)<<62
	})
	good := r.bill(t, pavBhaji(1), billOpts{})

	res := r.push(t, dupRate, nul, overflow, good)
	want := []string{"inconsistent_totals", "db_rejected", "inconsistent_totals"}
	for i, code := range want {
		if a := res.Acks[i]; a.Status != "rejected" || a.Error == nil || a.Error.Code != code {
			t.Errorf("op %d: %+v, want rejected %s", i, a, code)
		}
	}
	if res.Acks[3].Status != "applied" {
		t.Fatalf("the good op behind the bad ones must apply: %+v", res.Acks[3])
	}
	// The NUL payload is kept byte for byte (bytea), for a human to inspect.
	var kept []byte
	if err := ownerDB.QueryRow(`SELECT payload FROM sync_inbox WHERE op_id = $1`, nul.OpID).Scan(&kept); err != nil ||
		string(kept) != string(nul.Payload) {
		t.Fatalf("rejected payload not kept intact: %v", err)
	}
}

// Only a till (role pos) may issue invoices.
func TestOnlyTillsIssueBills(t *testing.T) {
	r := newRestaurant(t, "Captain Test", "", "29")
	var code struct {
		Code string `json:"code"`
	}
	must(t, http.StatusCreated, "POST", "/v1/outlets/"+r.OutletID+"/pairing-codes", r.OwnerToken, map[string]any{"role": "captain"}, &code)
	var cap struct {
		DeviceToken   string `json:"device_token"`
		InvoicePrefix string `json:"invoice_prefix"`
	}
	must(t, http.StatusCreated, "POST", "/v1/devices/register", "", map[string]any{"pairing_code": code.Code, "name": "Captain"}, &cap)
	captain := &fixture{DeviceToken: cap.DeviceToken, Prefix: cap.InvoicePrefix, nextSeq: 1, nextInvoice: 1}
	if a := captain.push(t, captain.bill(t, pavBhaji(1), billOpts{})).Acks[0]; a.Status != "rejected" || a.Error.Code != "role_not_allowed" {
		t.Fatalf("captain billing: %+v", a)
	}
}

// Re-pricing the device's own input cannot catch a wrong rate; comparing
// with the catalogue can.
func TestDriftFromCatalogueIsFlagged(t *testing.T) {
	r := newRestaurant(t, "Drift Diner", "", "29")
	in := pavBhaji(1)
	in.Lines[0].Ref = r.ItemID // a real menu item, taxed at 5% in the catalogue...
	in.Lines[0].TaxRateBP = 0  // ...billed at 0% by a stale or tampered till
	in.PriceMode = pricing.Inclusive
	op := r.bill(t, in, billOpts{})
	if s := r.push(t, op).Acks[0].Status; s != "applied" {
		t.Fatalf("status %s", s)
	}
	if b := r.getBill(t, op); b.Verification != "match" {
		t.Fatalf("server re-pricing the same input agrees, as expected: %+v", b)
	}
	got := r.alerts(t)
	if got["tax_rate_differs"] != 1 || got["billing_settings_differ"] != 1 {
		t.Fatalf("alerts: %v", got)
	}
}

// A revoked device's token stops working everywhere.
func TestRevokedDeviceIsLockedOut(t *testing.T) {
	r := newRestaurant(t, "Revoke Test", "", "29")
	must(t, http.StatusOK, "GET", "/v1/devices/me/menu", r.DeviceToken, nil, nil)
	if _, err := ownerDB.Exec(`UPDATE device SET revoked_at = now() WHERE id = $1`, r.DeviceID); err != nil {
		t.Fatal(err)
	}
	must(t, http.StatusUnauthorized, "GET", "/v1/devices/me/menu", r.DeviceToken, nil, nil)
	must(t, http.StatusUnauthorized, "POST", "/v1/sync/push", r.DeviceToken, syncgw.PushRequest{}, nil)
}

// Posted entries cannot grow later, locked months stay locked, and locking a
// month waits for entries being written into it.
func TestLedgerCannotBeChangedAfterTheFact(t *testing.T) {
	r := newRestaurant(t, "After The Fact", "", "29")
	op := r.bill(t, pavBhaji(1), billOpts{})
	r.push(t, op)

	var entry, period, cash, sales string
	ownerDB.QueryRow(`SELECT journal_entry_id, (SELECT period_id FROM journal_entry e WHERE e.id = b.journal_entry_id)
		FROM bill b WHERE tenant_id = $1`, r.TenantID).Scan(&entry, &period)
	ownerDB.QueryRow(`SELECT id FROM account WHERE legal_entity_id = $1 AND code = '1100'`, r.LegalEntityID).Scan(&cash)
	ownerDB.QueryRow(`SELECT id FROM account WHERE legal_entity_id = $1 AND code = '4100'`, r.LegalEntityID).Scan(&sales)

	// A balanced pair added to last week's entry: sum stays zero, still refused.
	err := asTenant(t, r.TenantID, func(tx *sql.Tx) error {
		for i, l := range []struct {
			acct string
			amt  int64
		}{{cash, 777777}, {sales, -777777}} {
			if _, err := tx.Exec(`INSERT INTO journal_line (entry_id, line_no, tenant_id, account_id, amount)
				VALUES ($1, $2, $3, $4, $5)`, entry, 100+i, r.TenantID, l.acct, l.amt); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "transaction that created") {
		t.Fatalf("appending to a posted entry must fail, got %v", err)
	}

	// Locking waits for an in-flight entry in that month (no race past the lock).
	ctx := context.Background()
	txA, _ := appDB.BeginTx(ctx, nil)
	defer txA.Rollback()
	txA.Exec(`SELECT set_config('app.tenant_id', $1, true)`, r.TenantID)
	if _, err := txA.Exec(`INSERT INTO journal_entry (id, tenant_id, legal_entity_id, source_type, source_event_id, entry_date, period_id)
		VALUES ($1, $2, $3, 'test', $4, '2026-09-28', $5)`, ids.New(), r.TenantID, r.LegalEntityID, ids.New(), period); err != nil {
		t.Fatal(err)
	}
	txB, _ := ownerDB.BeginTx(ctx, nil)
	txB.Exec(`SET LOCAL lock_timeout = '300ms'`)
	_, err = txB.Exec(`UPDATE period SET status = 'locked' WHERE id = $1`, period)
	txB.Rollback()
	if err == nil || !strings.Contains(err.Error(), "lock timeout") {
		t.Fatalf("locking the month must wait for the in-flight entry, got %v", err)
	}
	txA.Rollback()

	// Once locked, the app cannot re-open it.
	if _, err := ownerDB.Exec(`UPDATE period SET status = 'locked' WHERE id = $1`, period); err != nil {
		t.Fatal(err)
	}
	err = asTenant(t, r.TenantID, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE period SET status = 'open' WHERE id = $1`, period)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be re-opened") {
		t.Fatalf("re-opening a locked period must fail, got %v", err)
	}
}

// reverses_id cannot point into another tenant's books.
func TestReversalCannotCrossTenants(t *testing.T) {
	a := newRestaurant(t, "Rev A", "", "29")
	b := newRestaurant(t, "Rev B", "", "29")
	a.push(t, a.bill(t, pavBhaji(1), billOpts{}))
	var aEntry, bPeriod string
	ownerDB.QueryRow(`SELECT id FROM journal_entry WHERE tenant_id = $1`, a.TenantID).Scan(&aEntry)
	b.push(t, b.bill(t, pavBhaji(1), billOpts{}))
	ownerDB.QueryRow(`SELECT id FROM period WHERE tenant_id = $1`, b.TenantID).Scan(&bPeriod)

	err := asTenant(t, b.TenantID, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO journal_entry (id, tenant_id, legal_entity_id, source_type, source_event_id, entry_date, period_id, reverses_id)
			VALUES ($1, $2, $3, 'test', $4, '2026-09-28', $5, $6)`, ids.New(), b.TenantID, b.LegalEntityID, ids.New(), bPeriod, aEntry)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "foreign key") {
		t.Fatalf("cross-tenant reversal must fail, got %v", err)
	}
}
