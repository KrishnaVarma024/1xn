package dayclose

// Closing a till's shift from the back office.
//
// A till closes its own shift: the cashier counts the drawer and the till
// sends shift.closed. When the till never can (it was lost, stolen or broke),
// the owner revokes it, counts the drawer and closes the shift here.
// Everything after the count is the same as a till close: the server works
// out what the drawer should hold, the difference is booked to 5900 Cash over
// and short, and a difference raises cash_difference.
//
// Only a revoked till's shift can be closed here. A till that still works may
// be offline with ops it has not sent: a bill, a cash move, or its own count
// and the next shift. Each would have to be fitted around the owner's count
// after the fact, and when two people count one drawer there is no right
// answer to fit. A revoked till sends nothing more (the sync gateway checks
// revoked_at under the device row lock, which the revoke also takes), so
// once it is revoked every bill and cash move of the shift is already here,
// and the owner's count is the last word.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"1xn/core/internal/platform/alerts"
	"1xn/core/internal/platform/auth"
	"1xn/core/internal/platform/db"
	"1xn/core/internal/platform/gst"
	"1xn/core/internal/platform/httpx"
	"1xn/core/modules/ledger"
)

// ShiftRoutes is mounted at /v1/outlets/{outletID}/shifts (owner).
//
//	GET  /                   the outlet's open shifts, whatever their day
//	POST /{shiftID}/close    count the drawer and close the shift here
func ShiftRoutes(d *sql.DB) http.Handler {
	r := chi.NewRouter()
	r.Get("/", openShiftsHandler(d))
	r.Post("/{shiftID}/close", closeShiftHandler(d))
	return r
}

// OpenShift is a shift that has not been counted yet.
type OpenShift struct {
	Shift
	BusinessDate  string `json:"business_date"`
	DeviceRevoked bool   `json:"device_revoked"` // the till can never close it
}

func openShiftsHandler(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		outletID, err := outletParam(r)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		p := auth.Must(r.Context())
		out := []OpenShift{}
		err = db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			if err := outletExists(r.Context(), tx, outletID); err != nil {
				return err
			}
			rows, err := tx.QueryContext(r.Context(), `
				SELECT s.id FROM till_shift s
				WHERE s.outlet_id = $1 AND s.closed_at IS NULL
				ORDER BY s.opened_at, s.id`, outletID)
			if err != nil {
				return err
			}
			var ids []uuid.UUID
			for rows.Next() {
				var id uuid.UUID
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				ids = append(ids, id)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			now := instant(time.Now())
			for _, id := range ids {
				s, err := openShiftView(r.Context(), tx, id, now)
				if err != nil {
					return err
				}
				out = append(out, s)
			}
			return nil
		})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"shifts": out})
	}
}

// openShiftView is an open shift with what its drawer should hold so far.
func openShiftView(ctx context.Context, tx *sql.Tx, id uuid.UUID, now time.Time) (OpenShift, error) {
	var o OpenShift
	var day time.Time
	var revoked sql.NullTime
	var s shift
	err := tx.QueryRowContext(ctx, `
		SELECT s.id, s.device_id, coalesce(dv.name, ''), dv.revoked_at, s.business_date, s.opened_by, s.opened_at, s.opening_float
		FROM till_shift s LEFT JOIN device dv ON dv.id = s.device_id
		WHERE s.id = $1`, id).
		Scan(&s.ID, &s.DeviceID, &o.DeviceName, &revoked, &day, &s.OpenedBy, &s.OpenedAt, &s.OpeningFloat)
	if err != nil {
		return o, err
	}
	until, err := closeWindow(ctx, tx, &s, now)
	if err != nil {
		return o, err
	}
	c, err := countShift(ctx, tx, &s, until)
	if err != nil {
		return o, err
	}
	o.ID, o.DeviceID, o.OpenedBy, o.OpenedAt, o.OpeningFloat = s.ID, s.DeviceID, s.OpenedBy, s.OpenedAt, s.OpeningFloat
	o.CashSales, o.Expenses, o.Suppliers, o.ToSafe, o.FromSafe, o.Expected = c.CashSales, c.Expenses, c.Suppliers, c.ToSafe, c.FromSafe, c.Expected
	o.BusinessDate = day.Format(time.DateOnly)
	o.DeviceRevoked = revoked.Valid
	return o, nil
}

// closeWindow is when a shift counted now ends: at the last bill or cash
// move its till recorded, or now, whichever is later. A till's clock can run
// ahead of ours, and a revoked till has sent everything it ever will: none of
// it may fall after the count and land in no drawer.
func closeWindow(ctx context.Context, tx *sql.Tx, s *shift, now time.Time) (time.Time, error) {
	var last time.Time
	if err := tx.QueryRowContext(ctx, `
		SELECT greatest(
		  (SELECT max(issued_at) FROM bill WHERE device_id = $1 AND issued_at >= $2),
		  (SELECT max(at) FROM cash_move WHERE shift_id = $3),
		  $2)`, s.DeviceID, s.OpenedAt, s.ID).Scan(&last); err != nil {
		return time.Time{}, err
	}
	if last.After(now) {
		return instant(last), nil
	}
	return now, nil
}

// closeShiftRequest is the owner's count. ID is made when the form opens:
// a double click or a retry sends the same id and gets the same answer.
type closeShiftRequest struct {
	ID            uuid.UUID        `json:"id"`
	CountedPaise  int64            `json:"counted_paise"`
	Denominations map[string]int64 `json:"denominations,omitempty"`
	Note          string           `json:"note"` // why the till did not close it
}

func closeShiftHandler(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		outletID, err := outletParam(r)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		shiftID, err := uuid.Parse(chi.URLParam(r, "shiftID"))
		if err != nil {
			httpx.Fail(w, r, httpx.ErrNotFound)
			return
		}
		var req closeShiftRequest
		if err := httpx.Decode(w, r, 4<<10, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		note, denoms, err := req.check()
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		req.Note = note
		raw, _ := json.Marshal(req)
		sum := sha256.Sum256(raw)
		hash := sum[:]

		p := auth.Must(r.Context())
		status := http.StatusCreated
		var out Shift
		err = db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			// Take turns with the day close, which holds this row while it
			// builds and stores its report. Writing the row, not only locking
			// it: a day close that took its snapshot before this commits then
			// fails its own lock (a serialization error), is retried, and sees
			// the counted drawer instead of storing the shift as still open.
			if _, err := tx.ExecContext(r.Context(),
				`UPDATE sync_state SET last_seq = last_seq WHERE tenant_id = $1`, p.TenantID); err != nil {
				return err
			}
			if err := outletExists(r.Context(), tx, outletID); err != nil {
				return err
			}
			var outlet uuid.UUID
			if err := tx.QueryRowContext(r.Context(),
				`SELECT outlet_id FROM till_shift WHERE id = $1`, shiftID).Scan(&outlet); errors.Is(err, sql.ErrNoRows) {
				return httpx.ErrNotFound
			} else if err != nil {
				return err
			}
			if outlet != outletID {
				return httpx.ErrNotFound
			}
			// The same request again: answer as the first time. The same id
			// for anything else: refused, never mistaken for the first.
			var doneShift uuid.UUID
			var doneHash []byte
			err := tx.QueryRowContext(r.Context(),
				`SELECT id, close_request_hash FROM till_shift WHERE close_request = $1`, req.ID).Scan(&doneShift, &doneHash)
			if err == nil {
				if doneShift != shiftID || !bytes.Equal(doneHash, hash) {
					return httpx.E(http.StatusConflict, "id_used",
						"that id was already used for a different close; reload the page and count again")
				}
				status = http.StatusOK
				out, err = closedShiftView(r.Context(), tx, shiftID)
				return err
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}

			s, err := lockShift(r.Context(), tx, shiftID)
			if err != nil {
				return err
			}
			if s.ClosedAt != nil {
				return httpx.E(http.StatusConflict, "shift_closed",
					fmt.Sprintf("this shift was already closed at %s; reload the page", s.ClosedAt.In(gst.IST).Format("2 Jan, 3:04 pm")))
			}
			var revoked bool
			if err := tx.QueryRowContext(r.Context(),
				`SELECT revoked_at IS NOT NULL FROM device WHERE id = $1`, s.DeviceID).Scan(&revoked); err != nil {
				return err
			}
			if !revoked {
				return httpx.E(http.StatusConflict, "till_active",
					"this till can still close the shift itself: count the drawer on the till. If the till is lost or broken, revoke it first, then close the shift here")
			}
			now := instant(time.Now())
			closedAt, err := closeWindow(r.Context(), tx, s, now)
			if err != nil {
				return err
			}

			var userName string
			if err := tx.QueryRowContext(r.Context(), `SELECT name FROM app_user WHERE id = $1`, p.SubjectID).Scan(&userName); err != nil {
				return err
			}
			by := clip(userName, 100)

			c, err := countShift(r.Context(), tx, s, closedAt)
			if err != nil {
				return err
			}
			diff := req.CountedPaise - c.Expected
			ledgerStatus, entryID := "none", (*uuid.UUID)(nil)
			if diff != 0 {
				var le, ol uuid.UUID
				if err := tx.QueryRowContext(r.Context(),
					`SELECT o.legal_entity_id, o.id FROM outlet o WHERE o.id = $1`, outletID).Scan(&le, &ol); err != nil {
					return err
				}
				e := ledger.CashDifferenceEntry(p.TenantID, le, ol, s.ID, diff, now, s.BusinessDate)
				e.Memo = "Cash counted in the back office at shift close"
				id, err := ledger.Post(r.Context(), tx, e)
				switch {
				case err == nil:
					ledgerStatus, entryID = "posted", &id
				case errors.Is(err, ledger.ErrPeriodLocked):
					// As for a till's close: the count is kept, the entry
					// waits for the accountant, and the owner is told.
					ledgerStatus = "blocked"
					if err := alerts.Raise(r.Context(), tx, p.TenantID, "period_locked", "critical", &s.ID, map[string]any{
						"source": e.SourceType, "entry_date": e.EntryDate.Format(time.DateOnly),
					}); err != nil {
						return err
					}
				default:
					return err
				}
				severity := "warning"
				if diff > alertAbove || diff < -alertAbove {
					severity = "critical"
				}
				if err := alerts.Raise(r.Context(), tx, p.TenantID, "cash_difference", severity, &s.ID, map[string]any{
					"cashier": s.OpenedBy, "closed_by": by, "closed_from": "back_office",
					"expected": c.Expected, "counted": req.CountedPaise, "difference": diff,
				}); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(r.Context(), `
				UPDATE till_shift SET closed_at = $2, closed_by = $3, counted_cash = $4, cash_sales = $5, expected_cash = $6,
				       difference = $7, denominations = $8, ledger_status = $9, journal_entry_id = $10,
				       closed_by_user = $11, close_note = $12, close_request = $13, close_request_hash = $14
				WHERE id = $1`,
				s.ID, closedAt, by, req.CountedPaise, c.CashSales, c.Expected, diff, nullJSON(denoms), ledgerStatus, entryID,
				p.SubjectID, req.Note, req.ID, hash); err != nil {
				return err
			}
			// A closed day whose drawer is counted now: its figures change,
			// and it is flagged like any record that arrives after closing.
			if closed, err := DayClosed(r.Context(), tx, outletID, s.BusinessDate); err != nil {
				return err
			} else if closed {
				if err := alerts.Raise(r.Context(), tx, p.TenantID, "shift_after_day_close", "warning", &s.ID, map[string]any{
					"business_date": s.BusinessDate.Format(time.DateOnly), "closed_from": "back_office",
				}); err != nil {
					return err
				}
			}
			out, err = closedShiftView(r.Context(), tx, shiftID)
			return err
		})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, status, out)
	}
}

func (q *closeShiftRequest) check() (note string, denoms []byte, err error) {
	bad := func(msg string) error { return httpx.E(http.StatusBadRequest, "bad_request", msg) }
	if q.ID.Version() != 7 {
		return "", nil, bad("id must be a UUIDv7 made when the form was opened")
	}
	if q.CountedPaise < 0 || q.CountedPaise > maxCash {
		return "", nil, bad("counted_paise is out of range")
	}
	note = strings.TrimSpace(q.Note)
	if note == "" || utf8.RuneCountInString(note) > 200 || strings.IndexFunc(note, unicode.IsControl) >= 0 {
		return "", nil, bad("say why the shift is closed here and not on the till (1 to 200 characters)")
	}
	if len(q.Denominations) > 0 {
		var problem string
		if denoms, problem = denominationsJSON(q.Denominations, q.CountedPaise); problem != "" {
			return "", nil, httpx.E(http.StatusBadRequest, "denominations_mismatch", problem)
		}
	}
	return note, denoms, nil
}

// denominationsJSON checks a count of notes and coins against the counted
// amount and returns it as stored; problem says what is wrong, if anything.
func denominationsJSON(m map[string]int64, counted int64) (b []byte, problem string) {
	var sum int64
	for k, n := range m {
		if denominations[k] == 0 || n < 0 || n > 1_000_000 {
			return nil, fmt.Sprintf("denomination %q or its count is not valid", k)
		}
		v, _ := strconv.ParseInt(k, 10, 64)
		sum += v * 100 * n
	}
	if sum != counted {
		return nil, fmt.Sprintf("the notes and coins add up to %d paise but counted_paise is %d", sum, counted)
	}
	b, _ = json.Marshal(m)
	return b, ""
}

// closedShiftView is one closed shift as the day report shows it.
func closedShiftView(ctx context.Context, tx *sql.Tx, id uuid.UUID) (Shift, error) {
	var s Shift
	var closed sql.NullTime
	var counted, diff sql.NullInt64
	var fromBackOffice bool
	var note sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT s.id, s.device_id, coalesce(dv.name, ''), s.opened_by, s.opened_at, s.opening_float,
		       coalesce(s.closed_by, ''), s.closed_at, s.counted_cash, coalesce(s.cash_sales, 0),
		       coalesce(s.expected_cash, 0), s.difference, coalesce(s.ledger_status, ''),
		       s.closed_by_user IS NOT NULL, s.close_note
		FROM till_shift s LEFT JOIN device dv ON dv.id = s.device_id
		WHERE s.id = $1`, id).
		Scan(&s.ID, &s.DeviceID, &s.DeviceName, &s.OpenedBy, &s.OpenedAt, &s.OpeningFloat,
			&s.ClosedBy, &closed, &counted, &s.CashSales, &s.Expected, &diff, &s.LedgerStatus, &fromBackOffice, &note)
	if err != nil {
		return s, err
	}
	if closed.Valid {
		s.ClosedAt, s.Counted, s.Difference = &closed.Time, &counted.Int64, &diff.Int64
	}
	if fromBackOffice {
		s.ClosedFrom, s.CloseNote = "back_office", note.String
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT coalesce(sum(amount) FILTER (WHERE kind = 'expense'), 0),
		       coalesce(sum(amount) FILTER (WHERE kind = 'supplier'), 0),
		       coalesce(sum(amount) FILTER (WHERE kind = 'to_safe'), 0),
		       coalesce(sum(amount) FILTER (WHERE kind = 'from_safe'), 0)
		FROM cash_move WHERE shift_id = $1`, id).Scan(&s.Expenses, &s.Suppliers, &s.ToSafe, &s.FromSafe); err != nil {
		return s, err
	}
	return s, nil
}

// clip shortens s to at most n characters (runes, not bytes).
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
