package sales

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"1xn/core/internal/platform/auth"
	"1xn/core/internal/platform/db"
	"1xn/core/internal/platform/httpx"
	"1xn/core/modules/ledger"
)

// BillView is a stored bill as the back office shows it.
type BillView struct {
	ID           uuid.UUID          `json:"id"`
	InvoiceNo    string             `json:"invoice_no"`
	IssuedAt     time.Time          `json:"issued_at"`
	BusinessDate string             `json:"business_date"`
	Channel      string             `json:"channel"`
	GrandTotal   int64              `json:"grand_total"`
	TaxTotal     int64              `json:"tax_total"`
	Verification string             `json:"verification"`
	LedgerStatus string             `json:"ledger_status"`
	ServerSeq    int64              `json:"server_seq"`
	DeviceResult json.RawMessage    `json:"device_result"`
	ServerResult json.RawMessage    `json:"server_result,omitempty"`
	JournalLines []ledger.EntryLine `json:"journal_lines"`
	JournalEntry *uuid.UUID         `json:"journal_entry_id,omitempty"`
}

// Routes is mounted at /v1/bills (owner).
func Routes(d *sql.DB) http.Handler {
	r := chi.NewRouter()
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
			var entry uuid.NullUUID
			var bizDate time.Time
			err := tx.QueryRowContext(r.Context(), `
				SELECT id, invoice_no, issued_at, business_date, channel, grand_total, tax_total,
				       verification, ledger_status, server_seq, device_result, server_result, journal_entry_id
				FROM bill WHERE id = $1`, id).
				Scan(&v.ID, &v.InvoiceNo, &v.IssuedAt, &bizDate, &v.Channel, &v.GrandTotal, &v.TaxTotal,
					&v.Verification, &v.LedgerStatus, &v.ServerSeq, &v.DeviceResult, &server, &entry)
			if errors.Is(err, sql.ErrNoRows) {
				return httpx.ErrNotFound // other tenants' bills are invisible, so this is also "not yours"
			}
			if err != nil {
				return err
			}
			v.BusinessDate = bizDate.Format(time.DateOnly)
			if server != nil {
				v.ServerResult = server
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
