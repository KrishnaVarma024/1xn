package e2e

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"1xn/core/internal/platform/gst"
	"1xn/core/internal/platform/ids"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
func newOpID() string                     { return ids.New().String() }

// asTenant runs fn as the APP role with the given tenant context, exactly like
// the service does.
func asTenant(t *testing.T, tenantID string, fn func(tx *sql.Tx) error) error {
	t.Helper()
	tx, err := appDB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if tenantID != "" {
		if _, err := tx.Exec(`SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
			t.Fatal(err)
		}
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// TestTenantIsolation proves Row-Level Security holds even when the
// application code is wrong: we query WITHOUT a tenant filter on purpose.
func TestTenantIsolation(t *testing.T) {
	a := newRestaurant(t, "Tenant A", "", "29")
	b := newRestaurant(t, "Tenant B", "", "29")
	opA := a.bill(t, pavBhaji(1), billOpts{})
	a.push(t, opA)

	// Through the API: B's owner asking for A's bill gets "not found".
	var billA struct {
		BillID string `json:"bill_id"`
	}
	_ = json.Unmarshal(opA.Payload, &billA)
	must(t, http.StatusNotFound, "GET", "/v1/bills/"+billA.BillID, b.OwnerToken, nil, nil)
	must(t, http.StatusNotFound, "POST", "/v1/outlets/"+a.OutletID+"/pairing-codes", b.OwnerToken, map[string]any{"role": "pos"}, nil)

	count := func(tenant, table string) int {
		var n int
		if err := asTenant(t, tenant, func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n) // no WHERE on purpose
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, table := range []string{"bill", "journal_line", "outlet", "menu_item", "device", "auth_token"} {
		if n := count(b.TenantID, table); table == "bill" && n != 0 || table == "journal_line" && n != 0 {
			t.Errorf("tenant B sees %d rows of %s", n, table)
		}
		if n := count("", table); n != 0 {
			t.Errorf("no tenant context must see nothing, saw %d rows of %s", n, table)
		}
	}
	if count(a.TenantID, "bill") != 1 {
		t.Fatal("tenant A should see its own bill")
	}

	// Writing a row for another tenant is refused by the policy's WITH CHECK.
	err := asTenant(t, b.TenantID, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO menu_category (id, tenant_id, name) VALUES ($1, $2, 'Sneaky')`, ids.New(), a.TenantID)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "row-level security") {
		t.Fatalf("cross-tenant insert should violate RLS, got %v", err)
	}
}

// TestLedgerInvariants attacks the journal directly, as a buggy or malicious
// piece of code might, and checks the database refuses.
func TestLedgerInvariants(t *testing.T) {
	r := newRestaurant(t, "Ledger Test", "", "29")
	op := r.bill(t, pavBhaji(1), billOpts{})
	r.push(t, op)

	var cash, sales string
	asTenant(t, r.TenantID, func(tx *sql.Tx) error {
		tx.QueryRow(`SELECT id FROM account WHERE legal_entity_id = $1 AND code = '1100'`, r.LegalEntityID).Scan(&cash)
		tx.QueryRow(`SELECT id FROM account WHERE legal_entity_id = $1 AND code = '4100'`, r.LegalEntityID).Scan(&sales)
		return nil
	})
	var period string
	ownerDB.QueryRow(`SELECT id FROM period WHERE legal_entity_id = $1 LIMIT 1`, r.LegalEntityID).Scan(&period)

	post := func(amounts ...int64) error {
		return asTenant(t, r.TenantID, func(tx *sql.Tx) error {
			id := ids.New()
			if _, err := tx.Exec(`INSERT INTO journal_entry (id, tenant_id, legal_entity_id, source_type, source_event_id, entry_date, period_id)
				VALUES ($1, $2, $3, 'test', $4, '2026-09-28', $5)`, id, r.TenantID, r.LegalEntityID, ids.New(), period); err != nil {
				return err
			}
			for i, a := range amounts {
				acct := cash
				if a < 0 {
					acct = sales
				}
				if _, err := tx.Exec(`INSERT INTO journal_line (entry_id, line_no, tenant_id, account_id, amount) VALUES ($1, $2, $3, $4, $5)`,
					id, i+1, r.TenantID, acct, a); err != nil {
					return err
				}
			}
			return nil
		})
	}
	if err := post(500, -500); err != nil {
		t.Fatalf("a balanced entry must post: %v", err)
	}
	if err := post(500, -499); err == nil || !strings.Contains(err.Error(), "does not balance") {
		t.Fatalf("unbalanced entry must fail at commit, got %v", err)
	}
	if err := post(500); err == nil || !strings.Contains(err.Error(), "at least 2") {
		t.Fatalf("one-line entry must fail, got %v", err)
	}
	if err := post(); err == nil {
		t.Fatal("entry with no lines must fail")
	}

	// The app role cannot edit history...
	for _, q := range []string{`UPDATE journal_line SET amount = amount * 2`, `DELETE FROM journal_line`, `UPDATE bill SET grand_total = 1`} {
		err := asTenant(t, r.TenantID, func(tx *sql.Tx) error { _, err := tx.Exec(q); return err })
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s: want permission denied, got %v", q, err)
		}
	}
	// ...and neither can the owner role: the trigger stops it.
	if _, err := ownerDB.Exec(`UPDATE journal_line SET amount = amount WHERE tenant_id = $1`, r.TenantID); err == nil ||
		!strings.Contains(err.Error(), "append-only") {
		t.Fatalf("owner update must hit the append-only trigger, got %v", err)
	}

	// A bill dated in a locked month is kept, flagged, and not posted.
	aug := time.Date(2026, 8, 30, 20, 0, 0, 0, gst.IST)
	if _, err := ownerDB.Exec(`INSERT INTO period (id, tenant_id, legal_entity_id, starts_on, ends_on, status)
		VALUES ($1, $2, $3, '2026-08-01', '2026-09-01', 'locked')`, ids.New(), r.TenantID, r.LegalEntityID); err != nil {
		t.Fatal(err)
	}
	late := r.bill(t, pavBhaji(1), billOpts{issuedAt: aug})
	if s := r.push(t, late).Acks[0].Status; s != "applied" {
		t.Fatalf("late bill: %s", s)
	}
	if b := r.getBill(t, late); b.LedgerStatus != "blocked" || len(b.JournalLines) != 0 {
		t.Fatalf("late bill should be blocked from the ledger: %+v", b)
	}
	if r.alerts(t)["period_locked"] != 1 {
		t.Fatal("expected a period_locked alert")
	}
}
