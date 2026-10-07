// Package dayclose runs the end of a trading day: till shifts and their cash
// drawers (opened, cash moved, counted and closed on the device, offline),
// and the day's Z report, which an owner closes from the back office.
//
// The Z report reads bills, orders and shifts across modules' tables. It is
// a read-only report; every write here goes to this module's own tables and
// to the ledger through its posting rules.
//
// Cross-module API: Appliers (sync ops), DayClosed, the HTTP handlers.
package dayclose

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"1xn/core/internal/platform/alerts"
	"1xn/core/internal/platform/gst"
	"1xn/core/internal/platform/syncop"
	"1xn/core/modules/ledger"
)

// Op types a till sends.
const (
	OpShiftOpened = "shift.opened"
	OpCashMoved   = "cash.moved"
	OpShiftClosed = "shift.closed"
)

// Appliers returns the sync appliers for shift and cash ops. Only tills
// (role pos) have a cash drawer.
func Appliers() map[string]syncop.Applier {
	return map[string]syncop.Applier{
		OpShiftOpened: tillOnly(applyShiftOpened),
		OpCashMoved:   tillOnly(applyCashMoved),
		OpShiftClosed: tillOnly(applyShiftClosed),
	}
}

func tillOnly(fn syncop.Applier) syncop.Applier {
	return func(ctx context.Context, tx *sql.Tx, a syncop.ApplyContext) (syncop.Applied, error) {
		if a.Device.Role != "pos" {
			return syncop.Applied{}, syncop.Reject("role_not_allowed", "critical",
				fmt.Sprintf("a %s device has no cash drawer; only tills send %s", a.Device.Role, a.OpType))
		}
		return fn(ctx, tx, a)
	}
}

func reject(code, msg string) error { return syncop.Reject(code, "warning", msg) }

const (
	maxCash       = 100_000_000_000 // Rs 100 crore: far beyond any drawer
	maxClockAhead = 48 * time.Hour
)

var gstStart = time.Date(2017, 7, 1, 0, 0, 0, 0, gst.IST)

// instant normalises a device time the way Postgres keeps it (to the
// microsecond: timestamptz rounds) and in India's time zone, so that a
// resent op compares equal to what was stored, and a journal entry's date is
// India's date whatever UTC offset the device wrote.
func instant(t time.Time) time.Time { return t.Round(time.Microsecond).In(gst.IST) }

// validTime refuses timestamps a broken device clock would produce.
func validTime(what string, t time.Time) error {
	if t.IsZero() || t.Before(gstStart) || t.After(time.Now().Add(maxClockAhead)) {
		return reject("bad_payload", fmt.Sprintf("%s is missing or not a plausible time; check the device clock", what))
	}
	return nil
}

// businessDay parses a trading day and checks it is the day of `at` (in
// India) or the day before: a night shift may run past midnight.
func businessDay(s string, at time.Time) (time.Time, error) {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return time.Time{}, reject("bad_business_date", "business_date must be YYYY-MM-DD")
	}
	local := at.In(gst.IST)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	if diff := day.Sub(d); diff < 0 || diff > 24*time.Hour {
		return time.Time{}, reject("bad_business_date", "business_date must be the day of the event or the day before")
	}
	return d, nil
}

func name(what, s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > 100 || strings.ContainsRune(s, 0) {
		return "", reject("bad_payload", what+" must be 1 to 100 characters")
	}
	return s, nil
}

func decode(a syncop.ApplyContext, v any) error {
	if err := json.Unmarshal(a.Payload, v); err != nil {
		return reject("bad_payload", err.Error())
	}
	return nil
}

func deviceMade(what string, id uuid.UUID) error {
	if id.Version() != 7 {
		return reject("bad_payload", fmt.Sprintf("%s must be a UUIDv7", what))
	}
	return nil
}

// DayClosed reports whether an outlet's trading day has been closed. Late
// arrivals for a closed day are kept and flagged, never refused.
func DayClosed(ctx context.Context, tx *sql.Tx, outletID uuid.UUID, day time.Time) (bool, error) {
	var closed bool
	err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM day_close WHERE outlet_id = $1 AND business_date = $2)`,
		outletID, day.Format(time.DateOnly)).Scan(&closed)
	return closed, err
}

func flagIfDayClosed(ctx context.Context, tx *sql.Tx, a syncop.ApplyContext, day time.Time, kind string, ref uuid.UUID) error {
	closed, err := DayClosed(ctx, tx, a.Device.OutletID, day)
	if err != nil || !closed {
		return err
	}
	return alerts.Raise(ctx, tx, a.Device.TenantID, kind, "warning", &ref, map[string]any{
		"business_date": day.Format(time.DateOnly), "device_id": a.Device.ID,
	})
}

// ---- shift.opened ----------------------------------------------------------

type shiftOpened struct {
	ShiftID      uuid.UUID `json:"shift_id"`
	BusinessDate string    `json:"business_date"`
	OpenedAt     time.Time `json:"opened_at"`
	OpenedBy     string    `json:"opened_by"`
	OpeningFloat int64     `json:"opening_float_paise"`
}

func applyShiftOpened(ctx context.Context, tx *sql.Tx, a syncop.ApplyContext) (syncop.Applied, error) {
	var p shiftOpened
	if err := decode(a, &p); err != nil {
		return syncop.Applied{}, err
	}
	p.OpenedAt = instant(p.OpenedAt)
	if err := deviceMade("shift_id", p.ShiftID); err != nil {
		return syncop.Applied{}, err
	}
	if err := validTime("opened_at", p.OpenedAt); err != nil {
		return syncop.Applied{}, err
	}
	day, err := businessDay(p.BusinessDate, p.OpenedAt)
	if err != nil {
		return syncop.Applied{}, err
	}
	by, err := name("opened_by", p.OpenedBy)
	if err != nil {
		return syncop.Applied{}, err
	}
	if p.OpeningFloat < 0 || p.OpeningFloat > maxCash {
		return syncop.Applied{}, reject("bad_payload", "opening_float_paise is out of range")
	}

	seq, err := a.NextSeq()
	if err != nil {
		return syncop.Applied{}, err
	}
	// The same shift sent again (a re-queued op) is a duplicate.
	var existingDevice uuid.UUID
	err = tx.QueryRowContext(ctx, `SELECT device_id FROM till_shift WHERE id = $1`, p.ShiftID).Scan(&existingDevice)
	if err == nil {
		if existingDevice != a.Device.ID {
			return syncop.Applied{}, syncop.Reject("wrong_device", "critical", "that shift belongs to another till")
		}
		return syncop.Applied{ServerSeq: seq, Duplicate: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return syncop.Applied{}, err
	}
	// Ops from one till arrive in the order it made them, so a second open
	// shift means the till lost its "closed" op. One drawer, one shift.
	var open uuid.UUID
	err = tx.QueryRowContext(ctx, `SELECT id FROM till_shift WHERE device_id = $1 AND closed_at IS NULL`, a.Device.ID).Scan(&open)
	if err == nil {
		return syncop.Applied{}, syncop.Reject("shift_already_open", "critical",
			fmt.Sprintf("this till's shift %s is still open; close it first", open))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return syncop.Applied{}, err
	}
	// Shifts of one till never overlap, so every bill belongs to at most one
	// shift and no cash is counted twice.
	var lastClose sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT max(closed_at) FROM till_shift WHERE device_id = $1`, a.Device.ID).Scan(&lastClose); err != nil {
		return syncop.Applied{}, err
	}
	if lastClose.Valid && !p.OpenedAt.After(lastClose.Time) {
		return syncop.Applied{}, reject("shift_overlaps",
			fmt.Sprintf("opened_at is not after this till's last shift closed (%s); check the device clock", lastClose.Time.Format(time.RFC3339)))
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO till_shift (id, tenant_id, outlet_id, device_id, business_date, opened_at, opened_by, opening_float, server_seq)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		p.ShiftID, a.Device.TenantID, a.Device.OutletID, a.Device.ID, p.BusinessDate, p.OpenedAt, by, p.OpeningFloat, seq); err != nil {
		return syncop.Applied{}, err
	}
	return syncop.Applied{ServerSeq: seq}, flagIfDayClosed(ctx, tx, a, day, "shift_after_day_close", p.ShiftID)
}

// shift is a till_shift row, locked for the rest of the transaction.
type shift struct {
	ID           uuid.UUID
	DeviceID     uuid.UUID
	BusinessDate time.Time
	OpenedAt     time.Time
	OpenedBy     string
	OpeningFloat int64
	ClosedAt     *time.Time
	Counted      *int64
}

func lockShift(ctx context.Context, tx *sql.Tx, id uuid.UUID) (*shift, error) {
	var s shift
	var closed sql.NullTime
	var counted sql.NullInt64
	err := tx.QueryRowContext(ctx, `
		SELECT id, device_id, business_date, opened_at, opened_by, opening_float, closed_at, counted_cash
		FROM till_shift WHERE id = $1 FOR UPDATE`, id).
		Scan(&s.ID, &s.DeviceID, &s.BusinessDate, &s.OpenedAt, &s.OpenedBy, &s.OpeningFloat, &closed, &counted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, reject("unknown_shift", fmt.Sprintf("shift %s is not known; was its shift.opened op sent?", id))
	}
	if err != nil {
		return nil, err
	}
	if closed.Valid {
		s.ClosedAt = &closed.Time
		s.Counted = &counted.Int64
	}
	return &s, nil
}

// ---- cash.moved ------------------------------------------------------------

type cashMoved struct {
	MoveID      uuid.UUID `json:"move_id"`
	ShiftID     uuid.UUID `json:"shift_id"`
	Kind        string    `json:"kind"` // expense, to_safe, from_safe, supplier
	AmountPaise int64     `json:"amount_paise"`
	Reason      string    `json:"reason"`
	At          time.Time `json:"at"`
}

// supplier: a delivery paid in cash from the drawer; the owner books the
// delivery itself as received "paid from a till".
var moveKinds = map[string]bool{"expense": true, "to_safe": true, "from_safe": true, "supplier": true}

func applyCashMoved(ctx context.Context, tx *sql.Tx, a syncop.ApplyContext) (syncop.Applied, error) {
	var p cashMoved
	if err := decode(a, &p); err != nil {
		return syncop.Applied{}, err
	}
	p.At = instant(p.At)
	if err := deviceMade("move_id", p.MoveID); err != nil {
		return syncop.Applied{}, err
	}
	if err := validTime("at", p.At); err != nil {
		return syncop.Applied{}, err
	}
	if !moveKinds[p.Kind] {
		return syncop.Applied{}, reject("bad_payload", "kind must be expense, supplier, to_safe or from_safe")
	}
	if p.AmountPaise < 1 || p.AmountPaise > maxCash {
		return syncop.Applied{}, reject("bad_payload", "amount_paise must be positive")
	}
	reason := strings.TrimSpace(p.Reason)
	if reason == "" || utf8.RuneCountInString(reason) > 200 || strings.ContainsRune(reason, 0) {
		return syncop.Applied{}, reject("bad_payload", "a cash move needs a reason (1 to 200 characters)")
	}

	seq, err := a.NextSeq()
	if err != nil {
		return syncop.Applied{}, err
	}
	s, err := lockShift(ctx, tx, p.ShiftID)
	if err != nil {
		return syncop.Applied{}, err
	}
	if s.DeviceID != a.Device.ID {
		return syncop.Applied{}, syncop.Reject("wrong_device", "critical", "cash can only move in the till's own drawer")
	}
	var dup bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM cash_move WHERE id = $1)`, p.MoveID).Scan(&dup); err != nil {
		return syncop.Applied{}, err
	}
	if dup {
		return syncop.Applied{ServerSeq: seq, Duplicate: true}, nil
	}
	if s.ClosedAt != nil {
		return syncop.Applied{}, reject("shift_closed", "the shift was already closed; cash moves after the count cannot be placed")
	}
	// The move is booked on its own date, so it must be inside its shift: a
	// till with a wrong clock must not change the books of another month.
	if p.At.Before(s.OpenedAt) {
		return syncop.Applied{}, reject("bad_payload", "at is before the shift opened; check the device clock")
	}

	entry, err := ledger.CashMoveEntry(a.Device.TenantID, a.Device.LegalEntityID, a.Device.OutletID, p.MoveID,
		p.Kind, p.AmountPaise, p.At, s.BusinessDate, reason)
	if err != nil {
		return syncop.Applied{}, err
	}
	status, entryID, err := post(ctx, tx, a, entry, p.MoveID)
	if err != nil {
		return syncop.Applied{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO cash_move (id, tenant_id, shift_id, kind, amount, reason, at, ledger_status, journal_entry_id, server_seq)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		p.MoveID, a.Device.TenantID, s.ID, p.Kind, p.AmountPaise, reason, p.At, status, entryID, seq); err != nil {
		return syncop.Applied{}, err
	}
	return syncop.Applied{ServerSeq: seq}, nil
}

// post books an entry; a locked accounting month keeps the record but
// leaves it unposted, with an alert, exactly as for bills.
func post(ctx context.Context, tx *sql.Tx, a syncop.ApplyContext, e ledger.Entry, ref uuid.UUID) (string, *uuid.UUID, error) {
	id, err := ledger.Post(ctx, tx, e)
	switch {
	case err == nil:
		return "posted", &id, nil
	case errors.Is(err, ledger.ErrNothingToPost):
		return "none", nil, nil
	case errors.Is(err, ledger.ErrPeriodLocked):
		return "blocked", nil, alerts.Raise(ctx, tx, a.Device.TenantID, "period_locked", "critical", &ref, map[string]any{
			"source": e.SourceType, "entry_date": e.EntryDate.Format(time.DateOnly),
		})
	}
	return "", nil, err
}

// ---- shift.closed ----------------------------------------------------------

type shiftClosed struct {
	ShiftID       uuid.UUID        `json:"shift_id"`
	ClosedAt      time.Time        `json:"closed_at"`
	ClosedBy      string           `json:"closed_by"`
	CountedPaise  int64            `json:"counted_paise"`
	Denominations map[string]int64 `json:"denominations,omitempty"` // rupee note or coin -> how many
}

// Indian notes and coins, in rupees.
var denominations = map[string]int64{"2000": 1, "500": 1, "200": 1, "100": 1, "50": 1, "20": 1, "10": 1, "5": 1, "2": 1, "1": 1}

// alertAbove: a difference above Rs 500 is critical; any other is a warning.
const alertAbove = 50_000

func applyShiftClosed(ctx context.Context, tx *sql.Tx, a syncop.ApplyContext) (syncop.Applied, error) {
	var p shiftClosed
	if err := decode(a, &p); err != nil {
		return syncop.Applied{}, err
	}
	p.ClosedAt = instant(p.ClosedAt)
	if err := validTime("closed_at", p.ClosedAt); err != nil {
		return syncop.Applied{}, err
	}
	by, err := name("closed_by", p.ClosedBy)
	if err != nil {
		return syncop.Applied{}, err
	}
	if p.CountedPaise < 0 || p.CountedPaise > maxCash {
		return syncop.Applied{}, reject("bad_payload", "counted_paise is out of range")
	}
	var denoms []byte
	if len(p.Denominations) > 0 {
		var problem string
		if denoms, problem = denominationsJSON(p.Denominations, p.CountedPaise); problem != "" {
			code := "denominations_mismatch"
			if strings.HasPrefix(problem, "denomination ") {
				code = "bad_payload"
			}
			return syncop.Applied{}, reject(code, problem)
		}
	}

	seq, err := a.NextSeq()
	if err != nil {
		return syncop.Applied{}, err
	}
	s, err := lockShift(ctx, tx, p.ShiftID)
	if err != nil {
		return syncop.Applied{}, err
	}
	if s.DeviceID != a.Device.ID {
		return syncop.Applied{}, syncop.Reject("wrong_device", "critical", "a shift is closed on its own till")
	}
	if s.ClosedAt != nil {
		if *s.Counted == p.CountedPaise && s.ClosedAt.Equal(p.ClosedAt) {
			return syncop.Applied{ServerSeq: seq, Duplicate: true}, nil
		}
		return syncop.Applied{}, reject("shift_already_closed", "this shift was already closed with a different count")
	}
	if p.ClosedAt.Before(s.OpenedAt) {
		return syncop.Applied{}, reject("bad_payload", "closed_at is before the shift opened")
	}

	c, err := countShift(ctx, tx, s, p.ClosedAt)
	if err != nil {
		return syncop.Applied{}, err
	}
	diff := p.CountedPaise - c.Expected

	status, entryID := "none", (*uuid.UUID)(nil)
	if diff != 0 {
		e := ledger.CashDifferenceEntry(a.Device.TenantID, a.Device.LegalEntityID, a.Device.OutletID, s.ID, diff, p.ClosedAt, s.BusinessDate)
		if status, entryID, err = post(ctx, tx, a, e, s.ID); err != nil {
			return syncop.Applied{}, err
		}
		severity := "warning"
		if diff > alertAbove || diff < -alertAbove {
			severity = "critical"
		}
		if err := alerts.Raise(ctx, tx, a.Device.TenantID, "cash_difference", severity, &s.ID, map[string]any{
			"cashier": s.OpenedBy, "closed_by": by, "expected": c.Expected, "counted": p.CountedPaise, "difference": diff,
		}); err != nil {
			return syncop.Applied{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE till_shift SET closed_at = $2, closed_by = $3, counted_cash = $4, cash_sales = $5, expected_cash = $6,
		       difference = $7, denominations = $8, ledger_status = $9, journal_entry_id = $10
		WHERE id = $1`,
		s.ID, p.ClosedAt, by, p.CountedPaise, c.CashSales, c.Expected, diff, nullJSON(denoms), status, entryID); err != nil {
		return syncop.Applied{}, err
	}
	return syncop.Applied{ServerSeq: seq}, flagIfDayClosed(ctx, tx, a, s.BusinessDate, "shift_after_day_close", s.ID)
}

// LateCash reconciles a bill that reaches the server after its till's shift
// was counted. A till's ops normally arrive in order, so its bills are in
// before the close; but a bill can come later (refused once and replayed,
// or ops re-queued out of order). The count stands, because the cash was in
// the drawer when it was counted. What the drawer should have held goes up,
// the difference moves, and the books follow with an adjusting entry. Sales
// calls this for every new bill; a bill outside any closed shift changes
// nothing.
func LateCash(ctx context.Context, tx *sql.Tx, a syncop.ApplyContext, billID uuid.UUID, issuedAt time.Time) error {
	var id uuid.UUID
	var expectedBefore, diffBefore int64
	err := tx.QueryRowContext(ctx, `
		SELECT id, expected_cash, difference FROM till_shift
		WHERE device_id = $1 AND closed_at IS NOT NULL AND opened_at <= $2 AND $2 <= closed_at
		FOR UPDATE`, a.Device.ID, issuedAt).Scan(&id, &expectedBefore, &diffBefore)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	s, err := lockShift(ctx, tx, id)
	if err != nil {
		return err
	}
	c, err := countShift(ctx, tx, s, *s.ClosedAt)
	if err != nil || c.Expected == expectedBefore {
		return err // no cash on this bill: nothing moved
	}
	diff := *s.Counted - c.Expected
	e := ledger.ShiftAdjustEntry(a.Device.TenantID, a.Device.LegalEntityID, a.Device.OutletID, billID,
		diff-diffBefore, s.ClosedAt.In(gst.IST), s.BusinessDate)
	status, _, err := post(ctx, tx, a, e, billID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE till_shift SET cash_sales = $2, expected_cash = $3, difference = $4,
		       ledger_status = CASE WHEN $5 = 'blocked' THEN 'blocked'
		                            WHEN $5 = 'posted' AND ledger_status = 'none' THEN 'posted'
		                            ELSE ledger_status END
		WHERE id = $1`, s.ID, c.CashSales, c.Expected, diff, status); err != nil {
		return err
	}
	return alerts.Raise(ctx, tx, a.Device.TenantID, "cash_after_shift_close", "warning", &s.ID, map[string]any{
		"bill_id": billID, "cashier": s.OpenedBy, "expected_before": expectedBefore, "expected": c.Expected,
		"difference_before": diffBefore, "difference": diff,
	})
}

func nullJSON(b []byte) any {
	if b == nil {
		return nil
	}
	return string(b)
}

// counted is what a drawer should hold.
type counted struct {
	CashSales, Expenses, Suppliers, ToSafe, FromSafe, Expected int64
}

// countShift works out what the drawer should hold at `until`: the opening
// float, plus cash taken on this till's bills during the shift, plus cash
// brought from the safe, minus expenses and suppliers paid out and cash taken
// to the safe.
//
// Bills are matched to the shift by till and time. One till's clock is
// consistent with itself, and its ops arrive in the order it made them, so
// every bill of the shift is in before the shift.closed op.
func countShift(ctx context.Context, tx *sql.Tx, s *shift, until time.Time) (counted, error) {
	var c counted
	if err := tx.QueryRowContext(ctx, `
		SELECT coalesce(sum(p.amount), 0)
		FROM bill b JOIN bill_payment p ON p.bill_id = b.id AND p.tenant_id = b.tenant_id
		WHERE b.device_id = $1 AND b.issued_at >= $2 AND b.issued_at <= $3 AND p.method = 'cash'`,
		s.DeviceID, s.OpenedAt, until).Scan(&c.CashSales); err != nil {
		return c, err
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT coalesce(sum(amount) FILTER (WHERE kind = 'expense'), 0),
		       coalesce(sum(amount) FILTER (WHERE kind = 'supplier'), 0),
		       coalesce(sum(amount) FILTER (WHERE kind = 'to_safe'), 0),
		       coalesce(sum(amount) FILTER (WHERE kind = 'from_safe'), 0)
		FROM cash_move WHERE shift_id = $1`, s.ID).Scan(&c.Expenses, &c.Suppliers, &c.ToSafe, &c.FromSafe); err != nil {
		return c, err
	}
	c.Expected = s.OpeningFloat + c.CashSales + c.FromSafe - c.Expenses - c.Suppliers - c.ToSafe
	return c, nil
}
