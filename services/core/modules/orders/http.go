package orders

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lib/pq"

	"1xn/core/internal/platform/auth"
	"1xn/core/internal/platform/db"
	"1xn/core/internal/platform/httpx"
	"1xn/core/modules/devices"
)

// PullResponse is what a device gets from GET /v1/sync/pull.
//
// It is state-based: for every order that changed after the cursor, the
// device gets the order's CURRENT state once, not the list of ops. The device
// replaces its copy and re-applies its own unacked ops on top. That is simpler
// and more robust than replaying other devices' ops in order.
type PullResponse struct {
	Orders  []json.RawMessage `json:"orders"`
	Cursor  int64             `json:"cursor"`   // send this as ?after= next time
	HasMore bool              `json:"has_more"` // pull again right away
}

const maxPull = 200

// PullHandler serves GET /v1/sync/pull?after=<cursor>&limit=<n> for a device.
//
//	after = 0: bootstrap. All OPEN orders of the device's outlet, and the
//	           tenant's current sequence as the cursor.
//	after > 0: every order of the outlet changed after the cursor, oldest
//	           change first, each once, at most limit per page. A cursor
//	           older than the pruned part of the change log gets 410
//	           cursor_expired: bootstrap again.
//
// The read runs at REPEATABLE READ, so the orders and the cursor come from
// the same snapshot. Sequence numbers are handed out in commit order, so a
// cursor never skips a change that commits late.
func PullHandler(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := auth.Must(r.Context())
		after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		if r.URL.Query().Get("after") == "" {
			after, err = 0, nil
		}
		if err != nil || after < 0 {
			httpx.Fail(w, r, httpx.E(http.StatusBadRequest, "bad_cursor", "after must be a non-negative integer"))
			return
		}
		limit := maxPull
		if s := r.URL.Query().Get("limit"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n > 0 && n < maxPull {
				limit = n
			}
		}
		res := PullResponse{Orders: []json.RawMessage{}, Cursor: after}
		err = db.InTenantReadTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			dev, err := devices.Get(r.Context(), tx, p.SubjectID)
			if err != nil {
				return err
			}
			if after == 0 {
				return bootstrap(r.Context(), tx, p.TenantID, dev.OutletID, &res)
			}
			// Changes at or below the floor may have been pruned. A device that
			// far behind would silently miss them, so it must start afresh.
			var floor int64
			if err := tx.QueryRowContext(r.Context(),
				`SELECT coalesce((SELECT change_log_floor FROM sync_state WHERE tenant_id = $1), 0)`, p.TenantID).Scan(&floor); err != nil {
				return err
			}
			if after < floor {
				return httpx.E(http.StatusGone, "cursor_expired",
					"this device was offline too long to catch up; pull again with after=0 to start afresh")
			}
			if err := changesSince(r.Context(), tx, p.TenantID, dev.OutletID, after, limit, &res); err != nil || res.HasMore {
				return err
			}
			// Caught up: move the cursor to the tenant's latest seq, even if this
			// outlet had no changes. Seqs are handed out in commit order, so every
			// change at or below last_seq is in this snapshot and was sent. A
			// device at a quiet outlet then stays above the prune floor, which is
			// per tenant, instead of being told to start afresh.
			var last int64
			if err := tx.QueryRowContext(r.Context(),
				`SELECT coalesce((SELECT last_seq FROM sync_state WHERE tenant_id = $1), 0)`, p.TenantID).Scan(&last); err != nil {
				return err
			}
			res.Cursor = max(res.Cursor, last)
			return nil
		})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, res)
	}
}

func bootstrap(ctx context.Context, tx *sql.Tx, tenant, outlet uuid.UUID, res *PullResponse) error {
	if err := tx.QueryRowContext(ctx,
		`SELECT coalesce((SELECT last_seq FROM sync_state WHERE tenant_id = $1), 0)`, tenant).Scan(&res.Cursor); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT doc FROM pos_order WHERE outlet_id = $1 AND status = 'open' ORDER BY opened_at, id`, outlet)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var doc []byte
		if err := rows.Scan(&doc); err != nil {
			return err
		}
		res.Orders = append(res.Orders, doc)
	}
	return rows.Err()
}

// pullBudget is the most document bytes one pull page carries (at least one
// seq group is always sent, whatever its size).
const pullBudget = 4 << 20

type change struct {
	id   uuid.UUID
	last int64
}

// changesSince fills one page of an incremental pull.
//
// A page never ends in the middle of a seq. One op can change several orders
// under one seq (a merge saves the target and every source; a bill can close
// an order and its spillover), and the next page asks for seq > cursor, so
// splitting such a group would lose the rest of it for good. A page may
// therefore run a little over `limit` (at most a merge's 21 orders).
func changesSince(ctx context.Context, tx *sql.Tx, tenant, outlet uuid.UUID, after int64, limit int, res *PullResponse) error {
	// Each changed order once, at the position of its latest change.
	changes, err := queryChanges(ctx, tx, changedSQL+` ORDER BY last, entity_id LIMIT $4`, tenant, outlet, after, limit+1)
	if err != nil {
		return err
	}
	if len(changes) > limit {
		boundary := changes[limit-1].last
		spill := changes[limit].last == boundary
		changes = changes[:limit]
		if spill {
			group, err := queryChanges(ctx, tx, changedSQL+` HAVING max(seq) = $4 ORDER BY entity_id`, tenant, outlet, after, boundary)
			if err != nil {
				return err
			}
			have := map[uuid.UUID]bool{}
			for _, c := range changes {
				have[c.id] = true
			}
			for _, c := range group {
				if !have[c.id] {
					changes = append(changes, c)
				}
			}
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (SELECT 1 FROM change_log
			               WHERE tenant_id = $1 AND outlet_id = $2 AND seq > $3 AND entity = 'order')`,
			tenant, outlet, boundary).Scan(&res.HasMore); err != nil {
			return err
		}
	}
	if len(changes) == 0 {
		return nil
	}

	ids := make([]string, len(changes))
	for i, c := range changes {
		ids[i] = c.id.String()
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, doc FROM pos_order WHERE id = ANY($1::uuid[])`, pq.Array(ids))
	if err != nil {
		return err
	}
	docs := map[uuid.UUID][]byte{}
	for rows.Next() {
		var id uuid.UUID
		var doc []byte
		if err := rows.Scan(&id, &doc); err != nil {
			rows.Close()
			return err
		}
		docs[id] = doc
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Oldest change first. Stop early if the page gets too heavy, but only
	// between two seqs, for the reason above.
	size := 0
	for i, c := range changes {
		doc, ok := docs[c.id]
		if !ok {
			res.Cursor = c.last // cannot happen (orders are never deleted); never stall on it
			continue
		}
		if i > 0 && size+len(doc) > pullBudget && c.last != changes[i-1].last {
			res.HasMore = true
			break
		}
		size += len(doc)
		res.Orders = append(res.Orders, doc)
		res.Cursor = c.last
	}
	return nil
}

// changedSQL lists orders of an outlet changed after a cursor, each once, at
// the seq of its latest change.
const changedSQL = `
	SELECT entity_id, max(seq) AS last
	FROM change_log
	WHERE tenant_id = $1 AND outlet_id = $2 AND seq > $3 AND entity = 'order'
	GROUP BY entity_id`

func queryChanges(ctx context.Context, tx *sql.Tx, q string, args ...any) ([]change, error) {
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []change
	for rows.Next() {
		var c change
		if err := rows.Scan(&c.id, &c.last); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetOrderHandler serves GET /v1/orders/{orderID} (owner).
func GetOrderHandler(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "orderID"))
		if err != nil {
			httpx.Fail(w, r, httpx.ErrNotFound)
			return
		}
		p := auth.Must(r.Context())
		var doc []byte
		err = db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			return tx.QueryRowContext(r.Context(), `SELECT doc FROM pos_order WHERE id = $1`, id).Scan(&doc)
		})
		if errors.Is(err, sql.ErrNoRows) {
			httpx.Fail(w, r, httpx.ErrNotFound)
			return
		}
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write(doc)
	}
}

// OpenOrdersHandler serves GET /v1/outlets/{outletID}/orders (owner): the
// live floor, for the back office.
func OpenOrdersHandler(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		outlet, err := uuid.Parse(chi.URLParam(r, "outletID"))
		if err != nil {
			httpx.Fail(w, r, httpx.ErrNotFound)
			return
		}
		p := auth.Must(r.Context())
		out := []json.RawMessage{}
		err = db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			rows, err := tx.QueryContext(r.Context(),
				`SELECT doc FROM pos_order WHERE outlet_id = $1 AND status = 'open' ORDER BY opened_at, id`, outlet)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var doc []byte
				if err := rows.Scan(&doc); err != nil {
					return err
				}
				out = append(out, doc)
			}
			return rows.Err()
		})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"orders": out})
	}
}
