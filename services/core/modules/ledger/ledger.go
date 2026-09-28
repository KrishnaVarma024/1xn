// Package ledger is the double-entry money ledger.
//
// Money is recorded as journal entries made of lines. Each line moves an
// amount (paise) into or out of an account: debits are positive, credits are
// negative, and every entry sums to exactly zero. Entries are never edited or
// deleted; mistakes are fixed by posting a reversing entry. The database
// enforces all of this (migrations/0004_ledger.sql); the checks here only give
// earlier, clearer errors.
//
// Cross-module API: SeedAccounts, Post, PostBillSettled, TrialBalance.
package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"1xn/core/internal/platform/ids"
)

// Account codes that posting rules use. Four digits, first digit is the kind:
// 1 asset, 2 liability, 3 equity, 4 income, 5-9 expense.
const (
	AcctCash          = "1100"
	AcctUPI           = "1110"
	AcctCard          = "1120"
	AcctReceivable    = "1130"
	AcctSuspense      = "1190"
	AcctOutputCGST    = "2210"
	AcctOutputSGST    = "2220"
	AcctOutputIGST    = "2230"
	AcctTipsPayable   = "2300"
	AcctSales         = "4100"
	AcctServiceCharge = "4200"
	AcctRoundOff      = "4900"
)

var defaultChart = []struct{ code, name, kind string }{
	{AcctCash, "Cash in hand", "asset"},
	{AcctUPI, "UPI clearing", "asset"},
	{AcctCard, "Card clearing", "asset"},
	{AcctReceivable, "Customer credit (receivable)", "asset"},
	{AcctSuspense, "Payment differences (suspense)", "asset"},
	{AcctOutputCGST, "Output CGST", "liability"},
	{AcctOutputSGST, "Output SGST", "liability"},
	{AcctOutputIGST, "Output IGST", "liability"},
	{AcctTipsPayable, "Tips payable to staff", "liability"},
	{AcctSales, "Food and beverage sales", "income"},
	{AcctServiceCharge, "Service charge income", "income"},
	{AcctRoundOff, "Round-off", "income"},
}

// ErrPeriodLocked means the entry's month has been closed by the accountant.
var ErrPeriodLocked = errors.New("ledger: accounting period is locked")

// ErrNothingToPost means every line is zero (a fully comped bill): no money
// moved, so there is no entry to write.
var ErrNothingToPost = errors.New("ledger: nothing to post")

// SeedAccounts creates the default chart of accounts for a new legal entity.
func SeedAccounts(ctx context.Context, tx *sql.Tx, tenantID, legalEntityID uuid.UUID) error {
	for _, a := range defaultChart {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO account (id, tenant_id, legal_entity_id, code, name, kind) VALUES ($1, $2, $3, $4, $5, $6)`,
			ids.New(), tenantID, legalEntityID, a.code, a.name, a.kind); err != nil {
			return fmt.Errorf("seed account %s: %w", a.code, err)
		}
	}
	return nil
}

// Line is one side of an entry. Amount > 0 is a debit, < 0 a credit.
type Line struct {
	AccountCode string
	Amount      int64
	OutletID    *uuid.UUID
	Channel     string
}

// Entry is a journal entry before it is posted.
type Entry struct {
	TenantID      uuid.UUID
	LegalEntityID uuid.UUID
	SourceType    string
	SourceEventID uuid.UUID // the business event this records; posting is idempotent on it
	EntryDate     time.Time // only the date part is used
	BusinessDate  *time.Time
	Memo          string
	Lines         []Line
}

// Post writes e and returns its id. Posting the same SourceEventID again
// returns the id of the entry already posted and writes nothing.
func Post(ctx context.Context, tx *sql.Tx, e Entry) (uuid.UUID, error) {
	var existing uuid.UUID
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM journal_entry WHERE source_event_id = $1`, e.SourceEventID).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, err
	}

	// Drop zero lines (the database rejects them) and check the balance here
	// too, so a broken posting rule fails with a readable error.
	var lines []Line
	var sum int64
	for _, l := range e.Lines {
		if l.Amount != 0 {
			lines = append(lines, l)
			sum += l.Amount
		}
	}
	if len(lines) == 0 {
		return uuid.Nil, ErrNothingToPost
	}
	if len(lines) < 2 {
		return uuid.Nil, fmt.Errorf("ledger: entry for %s %s has fewer than two non-zero lines", e.SourceType, e.SourceEventID)
	}
	if sum != 0 {
		return uuid.Nil, fmt.Errorf("ledger: entry for %s %s is off by %d paise", e.SourceType, e.SourceEventID, sum)
	}

	periodID, err := openPeriod(ctx, tx, e.TenantID, e.LegalEntityID, e.EntryDate)
	if err != nil {
		return uuid.Nil, err
	}

	accounts, err := accountIDs(ctx, tx, e.LegalEntityID, lines)
	if err != nil {
		return uuid.Nil, err
	}

	id := ids.New()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO journal_entry (id, tenant_id, legal_entity_id, source_type, source_event_id,
		                           entry_date, business_date, period_id, memo)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		id, e.TenantID, e.LegalEntityID, e.SourceType, e.SourceEventID,
		e.EntryDate.Format(time.DateOnly), dateOrNil(e.BusinessDate), periodID, e.Memo); err != nil {
		return uuid.Nil, err
	}
	for i, l := range lines {
		var channel any
		if l.Channel != "" {
			channel = l.Channel
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO journal_line (entry_id, line_no, tenant_id, account_id, amount, outlet_id, channel)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			id, i+1, e.TenantID, accounts[l.AccountCode], l.Amount, l.OutletID, channel); err != nil {
			return uuid.Nil, err
		}
	}
	return id, nil
}

// openPeriod finds (or creates) the month that holds date and checks it is
// open.
func openPeriod(ctx context.Context, tx *sql.Tx, tenantID, legalEntityID uuid.UUID, date time.Time) (uuid.UUID, error) {
	start := time.Date(date.Year(), date.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO period (id, tenant_id, legal_entity_id, starts_on, ends_on)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (legal_entity_id, starts_on) DO NOTHING`,
		ids.New(), tenantID, legalEntityID, start.Format(time.DateOnly), end.Format(time.DateOnly)); err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	var status string
	if err := tx.QueryRowContext(ctx,
		// FOR SHARE: the month cannot be locked under us before we commit.
		`SELECT id, status FROM period WHERE legal_entity_id = $1 AND starts_on = $2 FOR SHARE`,
		legalEntityID, start.Format(time.DateOnly)).Scan(&id, &status); err != nil {
		return uuid.Nil, err
	}
	if status != "open" {
		return uuid.Nil, ErrPeriodLocked
	}
	return id, nil
}

func accountIDs(ctx context.Context, tx *sql.Tx, legalEntityID uuid.UUID, lines []Line) (map[string]uuid.UUID, error) {
	codes := make([]string, 0, len(lines))
	for _, l := range lines {
		codes = append(codes, l.AccountCode)
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT code, id FROM account WHERE legal_entity_id = $1 AND code = ANY($2)`,
		legalEntityID, pq.Array(codes))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]uuid.UUID{}
	for rows.Next() {
		var code string
		var id uuid.UUID
		if err := rows.Scan(&code, &id); err != nil {
			return nil, err
		}
		out[code] = id
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, c := range codes {
		if _, ok := out[c]; !ok {
			return nil, fmt.Errorf("ledger: account %s does not exist for legal entity %s", c, legalEntityID)
		}
	}
	return out, nil
}

func dateOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format(time.DateOnly)
}
