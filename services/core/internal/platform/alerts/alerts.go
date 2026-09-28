// Package alerts records things a human must look at, and lists them.
package alerts

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"1xn/core/internal/platform/auth"
	"1xn/core/internal/platform/db"
	"1xn/core/internal/platform/httpx"
	"1xn/core/internal/platform/ids"
)

// Raise writes an alert in the caller's transaction, so it exists if and only
// if the change it is about was committed.
func Raise(ctx context.Context, tx *sql.Tx, tenantID uuid.UUID, kind, severity string, ref *uuid.UUID, details map[string]any) error {
	b, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO alert (id, tenant_id, kind, severity, ref_id, details) VALUES ($1, $2, $3, $4, $5, $6)`,
		ids.New(), tenantID, kind, severity, ref, string(b))
	return err
}

// Alert is one row, for display.
type Alert struct {
	ID        uuid.UUID       `json:"id"`
	Kind      string          `json:"kind"`
	Severity  string          `json:"severity"`
	RefID     *uuid.UUID      `json:"ref_id,omitempty"`
	Details   json.RawMessage `json:"details"`
	CreatedAt time.Time       `json:"created_at"`
}

// Routes is mounted at /v1/alerts (owner).
func Routes(d *sql.DB) http.Handler {
	r := chi.NewRouter()
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		p := auth.Must(r.Context())
		out := []Alert{}
		err := db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			rows, err := tx.QueryContext(r.Context(),
				`SELECT id, kind, severity, ref_id, details, created_at FROM alert ORDER BY created_at DESC LIMIT 100`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var a Alert
				var ref uuid.NullUUID
				if err := rows.Scan(&a.ID, &a.Kind, &a.Severity, &ref, &a.Details, &a.CreatedAt); err != nil {
					return err
				}
				if ref.Valid {
					a.RefID = &ref.UUID
				}
				out = append(out, a)
			}
			return rows.Err()
		})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"alerts": out})
	})
	return r
}
