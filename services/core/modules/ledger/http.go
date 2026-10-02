package ledger

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"1xn/core/internal/platform/auth"
	"1xn/core/internal/platform/db"
	"1xn/core/internal/platform/httpx"
)

// Balance is one account's total in a trial balance.
type Balance struct {
	LegalEntityID uuid.UUID `json:"legal_entity_id"`
	Code          string    `json:"code"`
	Name          string    `json:"name"`
	Kind          string    `json:"kind"`
	Debit         int64     `json:"debit"`  // sum of positive lines
	Credit        int64     `json:"credit"` // sum of negative lines, as a positive number
	Balance       int64     `json:"balance"`
}

// TrialBalance lists every account that has moved, with its totals. The sum
// of all balances is zero whenever the books are correct, which is exactly
// what an accountant checks first.
func TrialBalance(ctx context.Context, tx *sql.Tx) ([]Balance, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT a.legal_entity_id, a.code, a.name, a.kind,
		       coalesce(sum(l.amount) FILTER (WHERE l.amount > 0), 0),
		       coalesce(-sum(l.amount) FILTER (WHERE l.amount < 0), 0),
		       coalesce(sum(l.amount), 0)
		FROM account a
		JOIN journal_line l ON l.account_id = a.id
		GROUP BY a.legal_entity_id, a.code, a.name, a.kind
		ORDER BY a.legal_entity_id, a.code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Balance{} // [] not null: "no accounts moved yet" is an answer
	for rows.Next() {
		var b Balance
		if err := rows.Scan(&b.LegalEntityID, &b.Code, &b.Name, &b.Kind, &b.Debit, &b.Credit, &b.Balance); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// EntryLine is a posted line, for display.
type EntryLine struct {
	LineNo      int    `json:"line_no"`
	AccountCode string `json:"account_code"`
	AccountName string `json:"account_name"`
	Amount      int64  `json:"amount"`
}

// Lines returns the lines of one entry.
func Lines(ctx context.Context, tx *sql.Tx, entryID uuid.UUID) ([]EntryLine, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT l.line_no, a.code, a.name, l.amount
		FROM journal_line l JOIN account a ON a.id = l.account_id
		WHERE l.entry_id = $1 ORDER BY l.line_no`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EntryLine
	for rows.Next() {
		var l EntryLine
		if err := rows.Scan(&l.LineNo, &l.AccountCode, &l.AccountName, &l.Amount); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Routes mounts the owner-facing ledger endpoints.
func Routes(d *sql.DB) http.Handler {
	r := chi.NewRouter()
	r.Get("/trial-balance", func(w http.ResponseWriter, r *http.Request) {
		p := auth.Must(r.Context())
		var rows []Balance
		err := db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			var err error
			rows, err = TrialBalance(r.Context(), tx)
			return err
		})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		var total int64
		for _, b := range rows {
			total += b.Balance
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"accounts": rows, "total": total, "balanced": total == 0})
	})
	return r
}
