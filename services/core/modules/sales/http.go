package sales

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"1xn/core/internal/platform/auth"
	"1xn/core/internal/platform/db"
	"1xn/core/internal/platform/gst"
	"1xn/core/internal/platform/httpx"
	"1xn/core/modules/ledger"
)

// BillLine is one printed line of a bill.
type BillLine struct {
	LineNo    int    `json:"line_no"`
	Name      string `json:"name"`
	Qty       int64  `json:"qty"`
	Gross     int64  `json:"gross"`
	Discount  int64  `json:"discount"`
	Net       int64  `json:"net"`
	TaxRateBP int64  `json:"tax_rate_bp"`
}

// PaymentView is one way the guest paid.
type PaymentView struct {
	Method    string `json:"method"`
	Amount    int64  `json:"amount"`
	Reference string `json:"reference,omitempty"`
}

// BillView is a stored bill as the back office shows it.
type BillView struct {
	ID            uuid.UUID          `json:"id"`
	InvoiceNo     string             `json:"invoice_no"`
	IssuedAt      time.Time          `json:"issued_at"`
	BusinessDate  string             `json:"business_date"`
	Channel       string             `json:"channel"`
	TableLabel    string             `json:"table_label,omitempty"`
	Covers        int                `json:"covers,omitempty"`
	OrderID       *uuid.UUID         `json:"order_id,omitempty"`
	Subtotal      int64              `json:"subtotal"`
	DiscountTotal int64              `json:"discount_total"`
	ServiceCharge int64              `json:"service_charge"`
	TaxableTotal  int64              `json:"taxable_total"`
	CGST          int64              `json:"cgst"`
	SGST          int64              `json:"sgst"`
	IGST          int64              `json:"igst"`
	RoundOff      int64              `json:"round_off"`
	Tip           int64              `json:"tip"`
	Lines         []BillLine         `json:"lines"`
	Payments      []PaymentView      `json:"payments"`
	GrandTotal    int64              `json:"grand_total"`
	TaxTotal      int64              `json:"tax_total"`
	Verification  string             `json:"verification"`
	LedgerStatus  string             `json:"ledger_status"`
	ServerSeq     int64              `json:"server_seq"`
	DeviceResult  json.RawMessage    `json:"device_result"`
	ServerResult  json.RawMessage    `json:"server_result,omitempty"`
	JournalLines  []ledger.EntryLine `json:"journal_lines"`
	JournalEntry  *uuid.UUID         `json:"journal_entry_id,omitempty"`
}

func billLines(ctx context.Context, tx *sql.Tx, id uuid.UUID) ([]BillLine, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT line_no, name, qty, gross, discount, net, tax_rate_bp FROM bill_line WHERE bill_id = $1 ORDER BY line_no`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BillLine{}
	for rows.Next() {
		var l BillLine
		if err := rows.Scan(&l.LineNo, &l.Name, &l.Qty, &l.Gross, &l.Discount, &l.Net, &l.TaxRateBP); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func billPayments(ctx context.Context, tx *sql.Tx, id uuid.UUID) ([]PaymentView, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT method, amount, coalesce(reference, '') FROM bill_payment WHERE bill_id = $1 ORDER BY line_no`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PaymentView{}
	for rows.Next() {
		var p PaymentView
		if err := rows.Scan(&p.Method, &p.Amount, &p.Reference); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// BillRow is one bill in a day's list.
type BillRow struct {
	ID           uuid.UUID `json:"id"`
	InvoiceNo    string    `json:"invoice_no"`
	IssuedAt     time.Time `json:"issued_at"`
	Channel      string    `json:"channel"`
	TableLabel   string    `json:"table_label,omitempty"`
	GrandTotal   int64     `json:"grand_total"`
	TaxTotal     int64     `json:"tax_total"`
	Verification string    `json:"verification"`
	LedgerStatus string    `json:"ledger_status"`
}

// DayBills is the answer of GET /v1/bills.
type DayBills struct {
	OutletID     uuid.UUID        `json:"outlet_id"`
	BusinessDate string           `json:"business_date"`
	Bills        []BillRow        `json:"bills"`
	Count        int              `json:"count"`
	GrandTotal   int64            `json:"grand_total"`
	TaxTotal     int64            `json:"tax_total"`
	ByMethod     map[string]int64 `json:"by_method"`
}

// maxDayBills caps one day's list. A busy outlet does a few hundred bills;
// the Z report (day close) carries totals for any size.
const maxDayBills = 2000

// Routes is mounted at /v1/bills (owner).
func Routes(d *sql.DB) http.Handler {
	r := chi.NewRouter()

	// GET /v1/bills?outlet_id=...&date=YYYY-MM-DD (business date; default:
	// today in India).
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		outletID, err := uuid.Parse(r.URL.Query().Get("outlet_id"))
		if err != nil {
			httpx.Fail(w, r, httpx.E(http.StatusBadRequest, "bad_outlet", "outlet_id is required"))
			return
		}
		day := time.Now().In(gst.IST).Format(time.DateOnly)
		if s := r.URL.Query().Get("date"); s != "" {
			t, err := time.Parse(time.DateOnly, s)
			if err != nil || t.Year() < 2017 || t.Year() > 2100 { // GST began in 2017
				httpx.Fail(w, r, httpx.E(http.StatusBadRequest, "bad_date", "date must be a real day, YYYY-MM-DD, from 2017 on"))
				return
			}
			day = s
		}
		p := auth.Must(r.Context())
		res := DayBills{OutletID: outletID, BusinessDate: day, Bills: []BillRow{}, ByMethod: map[string]int64{}}
		err = db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			// Like every other outlet endpoint: someone else's outlet does not exist.
			var exists bool
			if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS (SELECT 1 FROM outlet WHERE id = $1)`, outletID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return httpx.ErrNotFound
			}
			rows, err := tx.QueryContext(r.Context(), `
				SELECT id, invoice_no, issued_at, channel, coalesce(table_label, ''), grand_total, tax_total,
				       verification, ledger_status
				FROM bill WHERE outlet_id = $1 AND business_date = $2
				ORDER BY issued_at DESC, invoice_no DESC LIMIT $3`, outletID, day, maxDayBills)
			if err != nil {
				return err
			}
			for rows.Next() {
				var b BillRow
				if err := rows.Scan(&b.ID, &b.InvoiceNo, &b.IssuedAt, &b.Channel, &b.TableLabel, &b.GrandTotal,
					&b.TaxTotal, &b.Verification, &b.LedgerStatus); err != nil {
					rows.Close()
					return err
				}
				res.Bills = append(res.Bills, b)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			if err := tx.QueryRowContext(r.Context(), `
				SELECT count(*), coalesce(sum(grand_total), 0), coalesce(sum(tax_total), 0)
				FROM bill WHERE outlet_id = $1 AND business_date = $2`, outletID, day).
				Scan(&res.Count, &res.GrandTotal, &res.TaxTotal); err != nil {
				return err
			}
			mrows, err := tx.QueryContext(r.Context(), `
				SELECT p.method, sum(p.amount)
				FROM bill_payment p JOIN bill b ON b.id = p.bill_id AND b.tenant_id = p.tenant_id
				WHERE b.outlet_id = $1 AND b.business_date = $2
				GROUP BY p.method`, outletID, day)
			if err != nil {
				return err
			}
			defer mrows.Close()
			for mrows.Next() {
				var m string
				var amt int64
				if err := mrows.Scan(&m, &amt); err != nil {
					return err
				}
				res.ByMethod[m] = amt
			}
			return mrows.Err()
		})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, res)
	})

	r.Get("/{billID}", func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "billID"))
		if err != nil {
			httpx.Fail(w, r, httpx.ErrNotFound)
			return
		}
		p := auth.Must(r.Context())
		var v BillView
		err = db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			var server []byte
			var entry, order uuid.NullUUID
			var bizDate time.Time
			var table sql.NullString
			var covers sql.NullInt64
			err := tx.QueryRowContext(r.Context(), `
				SELECT id, invoice_no, issued_at, business_date, channel, table_label, covers, order_id,
				       subtotal, discount_total, service_charge, taxable_total, cgst, sgst, igst, round_off, tip,
				       grand_total, tax_total,
				       verification, ledger_status, server_seq, device_result, server_result, journal_entry_id
				FROM bill WHERE id = $1`, id).
				Scan(&v.ID, &v.InvoiceNo, &v.IssuedAt, &bizDate, &v.Channel, &table, &covers, &order,
					&v.Subtotal, &v.DiscountTotal, &v.ServiceCharge, &v.TaxableTotal, &v.CGST, &v.SGST, &v.IGST,
					&v.RoundOff, &v.Tip, &v.GrandTotal, &v.TaxTotal,
					&v.Verification, &v.LedgerStatus, &v.ServerSeq, &v.DeviceResult, &server, &entry)
			if errors.Is(err, sql.ErrNoRows) {
				return httpx.ErrNotFound // other tenants' bills are invisible, so this is also "not yours"
			}
			if err != nil {
				return err
			}
			v.BusinessDate = bizDate.Format(time.DateOnly)
			v.TableLabel, v.Covers = table.String, int(covers.Int64)
			if order.Valid {
				v.OrderID = &order.UUID
			}
			if server != nil {
				v.ServerResult = server
			}
			if v.Lines, err = billLines(r.Context(), tx, v.ID); err != nil {
				return err
			}
			if v.Payments, err = billPayments(r.Context(), tx, v.ID); err != nil {
				return err
			}
			v.JournalLines = []ledger.EntryLine{}
			if entry.Valid {
				v.JournalEntry = &entry.UUID
				v.JournalLines, err = ledger.Lines(r.Context(), tx, entry.UUID)
			}
			return err
		})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, v)
	})
	return r
}
