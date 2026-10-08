package stock

// Suppliers and what is owed to them.
//
// A delivery bought on credit credits 2100 Payable to suppliers; paying the
// supplier later debits it. The ledger knows the total owed to all suppliers;
// who is owed what is worked out from the documents themselves: a
// supplier's deliveries on credit, less what was paid to them. This is a
// subledger: the sum over suppliers always equals account 2100 (a test
// checks it), the way stock items add up to account 1300.
//
// The payable sits in one legal entity's books (the one whose outlet received
// the goods), so balances are per legal entity: an outlet pays from its own
// books what its own legal entity owes.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"1xn/core/internal/platform/auth"
	"1xn/core/internal/platform/db"
	"1xn/core/internal/platform/gst"
	"1xn/core/internal/platform/httpx"
	"1xn/core/internal/platform/ids"
	"1xn/core/modules/ledger"
)

// Supplier is someone the restaurant buys from.
type Supplier struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Phone string    `json:"phone,omitempty"`
	GSTIN string    `json:"gstin,omitempty"`
}

// SupplierBalance is a supplier as one outlet's books see them.
type SupplierBalance struct {
	Supplier
	Owed         int64  `json:"owed"`                    // paise: deliveries on credit less payments
	LastDelivery string `json:"last_delivery,omitempty"` // YYYY-MM-DD
	Bought30d    int64  `json:"bought_30d"`              // deliveries in the last 30 days, however paid
}

var phoneRe = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

type supplierInput struct {
	Name  *string `json:"name"`
	Phone *string `json:"phone"`
	GSTIN *string `json:"gstin"`
}

// check normalises the fields that were sent; "" clears phone and GSTIN.
func (in *supplierInput) check() error {
	if in.Name != nil {
		n, err := text("name", *in.Name, 1, 100)
		if err != nil {
			return err
		}
		in.Name = &n
	}
	if in.Phone != nil {
		v := strings.TrimSpace(*in.Phone)
		if v != "" && !phoneRe.MatchString(v) {
			return bad("bad_phone", "phone must be the full number with + and the country code, like +919876543210")
		}
		in.Phone = &v
	}
	if in.GSTIN != nil {
		v := strings.ToUpper(strings.TrimSpace(*in.GSTIN))
		if v != "" && (!gst.ValidGSTIN(v) || !gst.ValidStateCode(v[:2])) {
			return bad("bad_gstin", "that GSTIN is not valid: 15 characters, and the last one is a check digit")
		}
		in.GSTIN = &v
	}
	return nil
}

func createSupplier(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in supplierInput
		if err := httpx.Decode(w, r, 4<<10, &in); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if in.Name == nil {
			httpx.Fail(w, r, bad("bad_name", "name is required"))
			return
		}
		if err := in.check(); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		s := Supplier{ID: ids.New(), Name: *in.Name}
		if in.Phone != nil {
			s.Phone = *in.Phone
		}
		if in.GSTIN != nil {
			s.GSTIN = *in.GSTIN
		}
		p := auth.Must(r.Context())
		err := db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(r.Context(), `
				INSERT INTO supplier (id, tenant_id, name, phone, gstin) VALUES ($1, $2, $3, nullif($4, ''), nullif($5, ''))`,
				s.ID, p.TenantID, s.Name, s.Phone, s.GSTIN)
			if db.IsUniqueViolation(err, "supplier_name") {
				return httpx.E(http.StatusConflict, "duplicate_supplier", "there is already a supplier with that name")
			}
			return err
		})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, s)
	}
}

func updateSupplier(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r, "supplierID")
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		var in supplierInput
		if err := httpx.Decode(w, r, 4<<10, &in); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if err := in.check(); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		p := auth.Must(r.Context())
		err = db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(r.Context(), `
				UPDATE supplier SET name = coalesce($2, name),
				       phone = CASE WHEN $3::text IS NULL THEN phone ELSE nullif($3, '') END,
				       gstin = CASE WHEN $4::text IS NULL THEN gstin ELSE nullif($4, '') END
				WHERE id = $1`, id, in.Name, in.Phone, in.GSTIN)
			if db.IsUniqueViolation(err, "supplier_name") {
				return httpx.E(http.StatusConflict, "duplicate_supplier", "there is already a supplier with that name")
			}
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return httpx.ErrNotFound
			}
			return nil
		})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// resolveSupplier finds the supplier a delivery names, by id or by name
// (ignoring case), and creates it from the name if it is new: the list of
// suppliers builds itself from the deliveries.
func resolveSupplier(ctx context.Context, tx *sql.Tx, tenant uuid.UUID, id *uuid.UUID, name string) (Supplier, error) {
	var s Supplier
	if id != nil {
		err := tx.QueryRowContext(ctx, `SELECT id, name FROM supplier WHERE id = $1`, *id).Scan(&s.ID, &s.Name)
		if errors.Is(err, sql.ErrNoRows) {
			return s, bad("unknown_supplier", "that supplier does not exist")
		}
		return s, err
	}
	// Two deliveries naming a new supplier at once: one inserts, the other
	// finds it. ON CONFLICT never fails on the unique name.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO supplier (id, tenant_id, name) VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id, lower(name)) DO NOTHING`, ids.New(), tenant, name); err != nil {
		return s, err
	}
	err := tx.QueryRowContext(ctx, `SELECT id, name FROM supplier WHERE lower(name) = lower($1)`, name).Scan(&s.ID, &s.Name)
	return s, err
}

// owed is what a legal entity owes a supplier: deliveries on credit to its
// outlets, less what it paid them.
func owed(ctx context.Context, tx *sql.Tx, legal, supplier uuid.UUID) (int64, error) {
	var n int64
	err := tx.QueryRowContext(ctx, `
		SELECT coalesce((SELECT sum(d.total) FROM stock_doc d JOIN outlet o ON o.id = d.outlet_id
		                 WHERE d.supplier_id = $2 AND d.kind = 'purchase' AND d.paid_by = 'credit' AND o.legal_entity_id = $1), 0)
		     - coalesce((SELECT sum(amount) FROM supplier_payment WHERE supplier_id = $2 AND legal_entity_id = $1), 0)`,
		legal, supplier).Scan(&n)
	return n, err
}

// owedFrom is the least a legal entity owed a supplier on any day from the
// given one to today, by the documents' own dates: the most a payment dated
// that day can pay without the account going below zero on some day.
func owedFrom(ctx context.Context, tx *sql.Tx, legal, supplier uuid.UUID, from time.Time) (int64, error) {
	var n int64
	err := tx.QueryRowContext(ctx, `
		WITH ev AS (
		  SELECT d.business_date AS day, d.total AS delta
		  FROM stock_doc d JOIN outlet o ON o.id = d.outlet_id
		  WHERE d.supplier_id = $2 AND d.kind = 'purchase' AND d.paid_by = 'credit' AND o.legal_entity_id = $1
		  UNION ALL
		  SELECT paid_on, -amount FROM supplier_payment WHERE supplier_id = $2 AND legal_entity_id = $1),
		running AS (
		  SELECT day, sum(sum(delta)) OVER (ORDER BY day) AS owed FROM ev GROUP BY day)
		SELECT least(
		  coalesce((SELECT owed FROM running WHERE day <= $3 ORDER BY day DESC LIMIT 1), 0),
		  coalesce((SELECT min(owed) FROM running WHERE day > $3), 9223372036854775807))`,
		legal, supplier, from.Format(time.DateOnly)).Scan(&n)
	return n, err
}

func legalEntityOf(ctx context.Context, tx *sql.Tx, outlet uuid.UUID) (uuid.UUID, error) {
	var le uuid.UUID
	err := tx.QueryRowContext(ctx, `SELECT legal_entity_id FROM outlet WHERE id = $1`, outlet).Scan(&le)
	return le, err
}

func listSuppliers(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		readTx(w, r, d, func(tx *sql.Tx, outlet uuid.UUID) (any, error) {
			le, err := legalEntityOf(r.Context(), tx, outlet)
			if err != nil {
				return nil, err
			}
			since := dayAgo(30)
			rows, err := tx.QueryContext(r.Context(), `
				WITH bought AS (
				  SELECT d.supplier_id,
				         sum(d.total) FILTER (WHERE d.paid_by = 'credit') AS on_credit,
				         max(d.business_date) AS last_delivery,
				         coalesce(sum(d.total) FILTER (WHERE d.business_date >= $2), 0) AS recent
				  FROM stock_doc d JOIN outlet o ON o.id = d.outlet_id
				  WHERE d.kind = 'purchase' AND o.legal_entity_id = $1
				  GROUP BY d.supplier_id),
				paid AS (
				  SELECT supplier_id, sum(amount) AS amount FROM supplier_payment WHERE legal_entity_id = $1 GROUP BY supplier_id)
				SELECT s.id, s.name, coalesce(s.phone, ''), coalesce(s.gstin, ''),
				       coalesce(b.on_credit, 0) - coalesce(p.amount, 0), b.last_delivery, coalesce(b.recent, 0)
				FROM supplier s
				LEFT JOIN bought b ON b.supplier_id = s.id
				LEFT JOIN paid p ON p.supplier_id = s.id
				ORDER BY coalesce(b.on_credit, 0) - coalesce(p.amount, 0) DESC, lower(s.name), s.id`, le, since)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			out := []SupplierBalance{}
			var total int64
			for rows.Next() {
				var s SupplierBalance
				var last sql.NullTime
				if err := rows.Scan(&s.ID, &s.Name, &s.Phone, &s.GSTIN, &s.Owed, &last, &s.Bought30d); err != nil {
					return nil, err
				}
				if last.Valid {
					s.LastDelivery = last.Time.Format(time.DateOnly)
				}
				total += s.Owed
				out = append(out, s)
			}
			return map[string]any{"suppliers": out, "owed": total}, rows.Err()
		})
	}
}

func dayAgo(n int) string {
	return time.Now().In(gst.IST).AddDate(0, 0, -n).Format(time.DateOnly)
}

// StatementLine is one delivery or payment, with what was owed after it.
type StatementLine struct {
	Date      string    `json:"date"`
	Kind      string    `json:"kind"` // delivery, payment
	ID        uuid.UUID `json:"id"`
	OutletID  uuid.UUID `json:"outlet_id"`
	Reference string    `json:"reference,omitempty"` // invoice number, or the payment's reference
	PaidBy    string    `json:"paid_by"`             // credit, safe, till, bank
	Amount    int64     `json:"amount"`
	Owed      int64     `json:"owed"` // after this line
}

// Statement is a supplier's account in one legal entity's books.
type Statement struct {
	Supplier Supplier        `json:"supplier"`
	Owed     int64           `json:"owed"`
	Lines    []StatementLine `json:"lines"` // newest first, the latest 200
}

func supplierStatement(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r, "supplierID")
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		readTx(w, r, d, func(tx *sql.Tx, outlet uuid.UUID) (any, error) {
			var st Statement
			err := tx.QueryRowContext(r.Context(), `SELECT id, name, coalesce(phone, ''), coalesce(gstin, '') FROM supplier WHERE id = $1`, id).
				Scan(&st.Supplier.ID, &st.Supplier.Name, &st.Supplier.Phone, &st.Supplier.GSTIN)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, httpx.ErrNotFound
			}
			if err != nil {
				return nil, err
			}
			le, err := legalEntityOf(r.Context(), tx, outlet)
			if err != nil {
				return nil, err
			}
			rows, err := tx.QueryContext(r.Context(), `
				SELECT d.business_date, 'delivery', d.id, d.outlet_id, coalesce(d.invoice_no, ''), d.paid_by, d.total, d.created_at
				FROM stock_doc d JOIN outlet o ON o.id = d.outlet_id
				WHERE d.supplier_id = $1 AND d.kind = 'purchase' AND o.legal_entity_id = $2
				UNION ALL
				SELECT paid_on, 'payment', id, outlet_id, coalesce(reference, ''), paid_by, amount, created_at
				FROM supplier_payment WHERE supplier_id = $1 AND legal_entity_id = $2
				ORDER BY 1, 8, 3`, id, le)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			var all []StatementLine
			for rows.Next() {
				var l StatementLine
				var date, created time.Time
				if err := rows.Scan(&date, &l.Kind, &l.ID, &l.OutletID, &l.Reference, &l.PaidBy, &l.Amount, &created); err != nil {
					return nil, err
				}
				l.Date = date.Format(time.DateOnly)
				switch {
				case l.Kind == "payment":
					st.Owed -= l.Amount
				case l.PaidBy == "credit":
					st.Owed += l.Amount
				}
				l.Owed = st.Owed
				all = append(all, l)
			}
			if err := rows.Err(); err != nil {
				return nil, err
			}
			st.Lines = []StatementLine{}
			for i := len(all) - 1; i >= 0 && len(st.Lines) < 200; i-- {
				st.Lines = append(st.Lines, all[i])
			}
			return st, nil
		})
	}
}

// Payment is money paid to a supplier.
type Payment struct {
	ID         uuid.UUID `json:"id"`
	SupplierID uuid.UUID `json:"supplier_id"`
	Supplier   string    `json:"supplier"`
	Date       string    `json:"date"`
	Amount     int64     `json:"amount"`
	PaidBy     string    `json:"paid_by"`
	Reference  string    `json:"reference,omitempty"`
	OwedAfter  int64     `json:"owed_after"` // what is still owed to them now
}

var paymentPaidBy = map[string]bool{"safe": true, "bank": true, "till": true}

func postSupplierPayment(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		outlet, err := pathID(r, "outletID")
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		var in struct {
			ID         uuid.UUID `json:"id"`
			SupplierID uuid.UUID `json:"supplier_id"`
			Amount     int64     `json:"amount"`
			PaidBy     string    `json:"paid_by"`
			Date       string    `json:"date"`
			Reference  string    `json:"reference"`
		}
		if err := httpx.Decode(w, r, 4<<10, &in); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if in.ID.Version() != 7 {
			httpx.Fail(w, r, bad("bad_id", "id must be a UUIDv7 made when the form was opened"))
			return
		}
		if in.Amount < 1 || in.Amount > maxPayment {
			httpx.Fail(w, r, bad("bad_amount", "amount must be from 1 paisa to Rs 100 crore; pay a larger sum in parts"))
			return
		}
		if !paymentPaidBy[in.PaidBy] {
			httpx.Fail(w, r, bad("bad_paid_by",
				"paid_by must be safe, bank or till (cash from a till drawer: the till records the payout)"))
			return
		}
		ref, err := text("reference", in.Reference, 0, 100)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		in.Reference = ref
		date, err := day(in.Date, time.Now())
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		raw, _ := json.Marshal(in)
		sum := sha256.Sum256(raw)
		hash := sum[:]
		p := auth.Must(r.Context())
		status := http.StatusCreated
		var out Payment
		err = db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			if err := outletExists(r.Context(), tx, outlet); err != nil {
				return err
			}
			if _, err := tx.ExecContext(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, in.ID.String()); err != nil {
				return err
			}
			var stored []byte
			err := tx.QueryRowContext(r.Context(), `SELECT request_hash FROM supplier_payment WHERE id = $1`, in.ID).Scan(&stored)
			if err == nil {
				if !bytes.Equal(stored, hash) {
					return httpx.E(http.StatusConflict, "id_used",
						"that id was already used for a different payment; reload the form and enter it again")
				}
				status = http.StatusOK
				out, err = paymentView(r.Context(), tx, in.ID)
				return err
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			le, err := legalEntityOf(r.Context(), tx, outlet)
			if err != nil {
				return err
			}
			// Locks in the usual order: the accounting period, then the
			// supplier, so two payments to one supplier take turns and the
			// second sees what the first paid.
			if err := ledger.EnsureOpen(r.Context(), tx, p.TenantID, le, entryDate(date)); errors.Is(err, ledger.ErrPeriodLocked) {
				return httpx.E(http.StatusConflict, "period_locked",
					"the accounting month of that date is locked; date the payment in an open month")
			} else if err != nil {
				return err
			}
			var name string
			err = tx.QueryRowContext(r.Context(), `SELECT name FROM supplier WHERE id = $1 FOR UPDATE`, in.SupplierID).Scan(&name)
			if errors.Is(err, sql.ErrNoRows) {
				return bad("unknown_supplier", "that supplier does not exist")
			}
			if err != nil {
				return err
			}
			due, err := owed(r.Context(), tx, le, in.SupplierID)
			if err != nil {
				return err
			}
			if in.Amount > due {
				return httpx.E(http.StatusConflict, "more_than_owed",
					fmt.Sprintf("%s is owed %s; pay at most that (advance payments are not supported yet)", name, rupees(due)))
			}
			// A payment is booked on its date, so it must not pay more than
			// was owed then, or on any day since: a payment dated before the
			// delivery it pays would be an advance, and the payable would be
			// below zero in that month's books.
			floor, err := owedFrom(r.Context(), tx, le, in.SupplierID, date)
			if err != nil {
				return err
			}
			if in.Amount > floor {
				return httpx.E(http.StatusConflict, "more_than_owed",
					fmt.Sprintf("dated %s, a payment to %s can be at most %s: that is the least they were owed on any day since; "+
						"date it on or after the deliveries it pays", date.Format("2 Jan 2006"), name, rupees(floor)))
			}
			memo := "Paid to " + name
			if in.Reference != "" {
				memo += ", ref " + in.Reference
			}
			e, err := ledger.SupplierPaymentEntry(p.TenantID, le, outlet, in.ID, in.Amount, in.PaidBy, entryDate(date), memo)
			if err != nil {
				return err
			}
			entryID, err := ledger.Post(r.Context(), tx, e)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(r.Context(), `
				INSERT INTO supplier_payment (id, tenant_id, legal_entity_id, outlet_id, supplier_id, paid_on, amount, paid_by,
				                              reference, journal_entry_id, created_by, request_hash)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, nullif($9, ''), $10, $11, $12)`,
				in.ID, p.TenantID, le, outlet, in.SupplierID, date.Format(time.DateOnly), in.Amount, in.PaidBy,
				in.Reference, entryID, p.SubjectID, hash); err != nil {
				return err
			}
			out, err = paymentView(r.Context(), tx, in.ID)
			return err
		})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, status, out)
	}
}

func paymentView(ctx context.Context, tx *sql.Tx, id uuid.UUID) (Payment, error) {
	var p Payment
	var date time.Time
	var le uuid.UUID
	err := tx.QueryRowContext(ctx, `
		SELECT sp.id, sp.supplier_id, s.name, sp.paid_on, sp.amount, sp.paid_by, coalesce(sp.reference, ''), sp.legal_entity_id
		FROM supplier_payment sp JOIN supplier s ON s.id = sp.supplier_id
		WHERE sp.id = $1`, id).Scan(&p.ID, &p.SupplierID, &p.Supplier, &date, &p.Amount, &p.PaidBy, &p.Reference, &le)
	if err != nil {
		return p, err
	}
	p.Date = date.Format(time.DateOnly)
	p.OwedAfter, err = owed(ctx, tx, le, p.SupplierID)
	return p, err
}

// rupees formats paise for a message: ₹1,23,456.78 in Indian grouping.
func rupees(p int64) string {
	sign := ""
	if p < 0 {
		sign, p = "-", -p
	}
	whole := fmt.Sprint(p / 100)
	if len(whole) > 3 {
		head, tail := whole[:len(whole)-3], whole[len(whole)-3:]
		var parts []string
		for len(head) > 2 {
			parts = append([]string{head[len(head)-2:]}, parts...)
			head = head[:len(head)-2]
		}
		parts = append([]string{head}, parts...)
		whole = strings.Join(parts, ",") + "," + tail
	}
	return fmt.Sprintf("%s₹%s.%02d", sign, whole, p%100)
}
