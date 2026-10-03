package dayclose

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"1xn/core/internal/platform/auth"
	"1xn/core/internal/platform/db"
	"1xn/core/internal/platform/httpx"
	"1xn/core/modules/orders"
)

// DayView is one trading day as the back office shows it.
type DayView struct {
	BusinessDate string      `json:"business_date"`
	Today        string      `json:"today"`  // the current trading day
	Report       *Report     `json:"report"` // live: computed now from the stored records
	Closed       *Closed     `json:"closed,omitempty"`
	Amended      bool        `json:"amended"` // closed, and records arrived or changed since
	Amendments   []Amendment `json:"amendments"`
}

// Closed is a closed day: who closed it, and the report as it was then.
type Closed struct {
	ClosedAt time.Time `json:"closed_at"`
	ClosedBy string    `json:"closed_by"`
	Report   *Report   `json:"report"`
}

// Amendment is one figure that differs between the closing report and now.
type Amendment struct {
	What   string `json:"what"`
	Before int64  `json:"before"`
	After  int64  `json:"after"`
	Money  bool   `json:"money"`
}

// ClosedDay is one row of the list of closed days.
type ClosedDay struct {
	BusinessDate   string    `json:"business_date"`
	ClosedAt       time.Time `json:"closed_at"`
	ClosedBy       string    `json:"closed_by"`
	Bills          int       `json:"bills"`
	GrandTotal     int64     `json:"grand_total"`
	Shifts         int       `json:"shifts"`
	CashDifference int64     `json:"cash_difference"`
}

// DayList is the trading day now and the outlet's recently closed days.
type DayList struct {
	Today  string      `json:"today"`
	Closed []ClosedDay `json:"closed"`
}

type closeRequest struct {
	Digest              string `json:"digest"`
	AcknowledgeWarnings bool   `json:"acknowledge_warnings"`
}

// Routes is mounted at /v1/outlets/{outletID}/days (owner).
//
//	GET  /                    today's trading day and the last 60 closed days
//	GET  /{date}              the live Z report, and the closing copy if closed
//	POST /{date}/close        close the day: {digest, acknowledge_warnings}
func Routes(d *sql.DB) http.Handler {
	r := chi.NewRouter()
	r.Get("/", listHandler(d))
	r.Get("/{date}", dayHandler(d))
	r.Post("/{date}/close", closeHandler(d))
	return r
}

func outletParam(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "outletID"))
	if err != nil {
		return uuid.Nil, httpx.ErrNotFound
	}
	return id, nil
}

func dateParam(r *http.Request) (time.Time, error) {
	t, err := time.Parse(time.DateOnly, chi.URLParam(r, "date"))
	if err != nil || t.Year() < 2017 || t.Year() > 2100 {
		return time.Time{}, httpx.E(http.StatusBadRequest, "bad_date", "date must be a real day, YYYY-MM-DD, from 2017 on")
	}
	return t, nil
}

// outletExists: like every outlet endpoint, someone else's outlet does not exist.
func outletExists(ctx context.Context, tx *sql.Tx, id uuid.UUID) error {
	var ok bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM outlet WHERE id = $1)`, id).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return httpx.ErrNotFound
	}
	return nil
}

func listHandler(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		outletID, err := outletParam(r)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		p := auth.Must(r.Context())
		res := DayList{Today: TradingDay(time.Now()).Format(time.DateOnly), Closed: []ClosedDay{}}
		err = db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			if err := outletExists(r.Context(), tx, outletID); err != nil {
				return err
			}
			rows, err := tx.QueryContext(r.Context(), `
				SELECT c.business_date, c.closed_at, coalesce(u.name, ''),
				       coalesce((c.report->'sales'->>'bills')::int, 0),
				       coalesce((c.report->'sales'->>'grand_total')::bigint, 0),
				       coalesce(jsonb_array_length(c.report->'shifts'), 0),
				       coalesce((c.report->'cash'->>'difference')::bigint, 0)
				FROM day_close c LEFT JOIN app_user u ON u.id = c.closed_by
				WHERE c.outlet_id = $1
				ORDER BY c.business_date DESC LIMIT 60`, outletID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var c ClosedDay
				var day time.Time
				if err := rows.Scan(&day, &c.ClosedAt, &c.ClosedBy, &c.Bills, &c.GrandTotal, &c.Shifts, &c.CashDifference); err != nil {
					return err
				}
				c.BusinessDate = day.Format(time.DateOnly)
				res.Closed = append(res.Closed, c)
			}
			return rows.Err()
		})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, res)
	}
}

func dayHandler(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		outletID, err := outletParam(r)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		day, err := dateParam(r)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		p := auth.Must(r.Context())
		var view *DayView
		// One snapshot: the live report and the closing copy are compared as of
		// the same moment.
		err = db.InTenantReadTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			if err := outletExists(r.Context(), tx, outletID); err != nil {
				return err
			}
			view, err = load(r.Context(), tx, outletID, day, time.Now())
			return err
		})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, view)
	}
}

// load builds the live report and, for a closed day, compares it with the
// copy stored at closing.
func load(ctx context.Context, tx *sql.Tx, outletID uuid.UUID, day, now time.Time) (*DayView, error) {
	live, err := Build(ctx, tx, outletID, day, now)
	if err != nil {
		return nil, err
	}
	v := &DayView{
		BusinessDate: day.Format(time.DateOnly), Today: TradingDay(now).Format(time.DateOnly),
		Report: live, Amendments: []Amendment{},
	}
	var c Closed
	var raw []byte
	err = tx.QueryRowContext(ctx, `
		SELECT c.closed_at, coalesce(u.name, ''), c.report
		FROM day_close c LEFT JOIN app_user u ON u.id = c.closed_by
		WHERE c.outlet_id = $1 AND c.business_date = $2`, outletID, v.BusinessDate).Scan(&c.ClosedAt, &c.ClosedBy, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &c.Report); err != nil {
		return nil, err
	}
	v.Closed = &c
	v.Amendments = amendments(c.Report, live)
	v.Amended = len(v.Amendments) > 0
	return v, nil
}

// amendments lists the settled figures that changed since closing: bills
// that synced late, a shift counted after the day was closed, a void. Figures
// that move on their own (an open shift's running total, which tables are
// still open) are not amendments.
func amendments(before, after *Report) []Amendment {
	figures := []struct {
		what  string
		money bool
		of    func(*Report) int64
	}{
		{"bills", false, func(r *Report) int64 { return int64(r.Sales.Bills) }},
		{"sales", true, func(r *Report) int64 { return r.Sales.GrandTotal }},
		{"tax", true, func(r *Report) int64 { return r.Sales.Tax }},
		{"payments", true, func(r *Report) int64 {
			var n int64
			for _, p := range r.Payments {
				n += p.Amount
			}
			return n
		}},
		{"voided_dishes", false, func(r *Report) int64 { return int64(r.Voids.Lines) }},
		{"void_value", true, func(r *Report) int64 { return r.Voids.Value }},
		{"cancelled_orders", false, func(r *Report) int64 { return int64(r.Cancelled.Orders) }},
		{"shifts", false, func(r *Report) int64 { return int64(len(r.Shifts)) }},
		{"closed_shifts", false, func(r *Report) int64 { return int64(len(r.Shifts) - r.Cash.OpenShifts) }},
		{"counted_cash", true, func(r *Report) int64 { return r.Cash.Counted }},
		{"cash_difference", true, func(r *Report) int64 { return r.Cash.Difference }},
		{"cash_moves", false, func(r *Report) int64 { return int64(len(r.CashMoves)) }},
	}
	out := []Amendment{}
	for _, f := range figures {
		if b, a := f.of(before), f.of(after); b != a {
			out = append(out, Amendment{What: f.what, Before: b, After: a, Money: f.money})
		}
	}
	return out
}

func closeHandler(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		outletID, err := outletParam(r)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		day, err := dateParam(r)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		var req closeRequest
		if err := httpx.Decode(w, r, 4<<10, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if req.Digest == "" {
			httpx.Fail(w, r, httpx.E(http.StatusBadRequest, "digest_required",
				"send the digest of the report you reviewed, so the day you close is the day you saw"))
			return
		}
		now := time.Now()
		if day.After(TradingDay(now)) {
			httpx.Fail(w, r, httpx.E(http.StatusBadRequest, "day_not_started", "that trading day has not started yet"))
			return
		}
		p := auth.Must(r.Context())
		var view *DayView
		err = db.InTenantSnapshotTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			// Take turns with sync. Every applied op locks this row to get its
			// seq. Holding it, the close and a syncing bill cannot overlap: a
			// bill that committed after our snapshot makes this lock fail
			// (serialization error, so the close is retried and sees the bill),
			// and a bill that comes after waits, then finds the day closed and
			// is flagged bill_after_day_close.
			if _, err := tx.ExecContext(r.Context(),
				`SELECT 1 FROM sync_state WHERE tenant_id = $1 FOR UPDATE`, p.TenantID); err != nil {
				return err
			}
			if err := outletExists(r.Context(), tx, outletID); err != nil {
				return err
			}
			closed, err := DayClosed(r.Context(), tx, outletID, day)
			if err != nil {
				return err
			}
			if closed {
				return errDayClosed
			}
			rep, err := Build(r.Context(), tx, outletID, day, now)
			if err != nil {
				return err
			}
			if rep.Digest != req.Digest {
				return httpx.E(http.StatusConflict, "report_changed",
					"the day changed while you were looking at it; review the report again before closing")
			}
			if rep.NeedsAcknowledging() && !req.AcknowledgeWarnings {
				return httpx.E(http.StatusConflict, "warnings_not_acknowledged",
					"this day has warnings to check; acknowledge them to close it anyway")
			}
			b, err := json.Marshal(rep)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(r.Context(), `
				INSERT INTO day_close (tenant_id, outlet_id, business_date, closed_at, closed_by, report)
				VALUES ($1, $2, $3, $4, $5, $6)`,
				p.TenantID, outletID, rep.BusinessDate, now, p.SubjectID, string(b)); err != nil {
				if db.IsUniqueViolation(err, "") {
					return errDayClosed
				}
				return err
			}
			view, err = load(r.Context(), tx, outletID, day, now)
			return err
		})
		if db.Retryable(err) {
			// Tills kept syncing through every retry: the day is still moving.
			err = httpx.E(http.StatusConflict, "report_changed",
				"bills are still arriving for this day; review the report again before closing")
		}
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		// Housekeeping that rides on the daily close: prune the change log
		// devices pull from. A failure here does not undo the close.
		if err := db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			_, err := orders.PruneChangeLog(r.Context(), tx, p.TenantID, now.Add(-orders.KeepChanges))
			return err
		}); err != nil {
			slog.ErrorContext(r.Context(), "prune change log", "err", err, "tenant", p.TenantID)
		}
		httpx.JSON(w, http.StatusCreated, view)
	}
}

var errDayClosed = httpx.E(http.StatusConflict, "day_already_closed", "this day is already closed")
