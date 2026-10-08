package stock

// Packs: how a supplier sells an item. Rice comes in a 25 kg bag, oil in a
// 15 l tin; the owner receives "2 bags" and the store gets 50 kg. Stock is
// always kept in the item's own unit (milli-units); a pack only converts on
// the way in, and the purchase move remembers it for the history.
//
// A pack never changes size. A new size is a new pack and the old one is
// switched off, so a move that says "2 × Bag" keeps meaning what it meant.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"1xn/core/internal/platform/auth"
	"1xn/core/internal/platform/db"
	"1xn/core/internal/platform/httpx"
	"1xn/core/internal/platform/ids"
)

// Pack is one way an item is bought.
type Pack struct {
	ID     uuid.UUID `json:"id"`
	ItemID uuid.UUID `json:"stock_item_id"`
	Name   string    `json:"name"`
	Qty    int64     `json:"qty"` // milli-units of the item in one pack
	Active bool      `json:"active"`
}

const maxPacks = 100_000 // packs on one purchase line

func createPack(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		item, err := pathID(r, "itemID")
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		var in struct {
			Name string `json:"name"`
			Qty  int64  `json:"qty"`
		}
		if err := httpx.Decode(w, r, 4<<10, &in); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		name, err := text("name", in.Name, 1, 40)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if in.Qty < 1 || in.Qty > 1_000_000_000 {
			httpx.Fail(w, r, bad("bad_qty", "a pack holds more than nothing and at most a million kg, litres or pieces"))
			return
		}
		p := auth.Must(r.Context())
		pack := Pack{ID: ids.New(), ItemID: item, Name: name, Qty: in.Qty, Active: true}
		err = db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			var active bool
			err := tx.QueryRowContext(r.Context(), `SELECT active FROM stock_item WHERE id = $1`, item).Scan(&active)
			if errors.Is(err, sql.ErrNoRows) {
				return httpx.ErrNotFound
			}
			if err != nil {
				return err
			}
			if !active {
				return httpx.E(http.StatusConflict, "item_inactive", "that item is switched off; switch it on to add packs")
			}
			_, err = tx.ExecContext(r.Context(), `
				INSERT INTO stock_pack (id, tenant_id, stock_item_id, name, qty) VALUES ($1, $2, $3, $4, $5)`,
				pack.ID, p.TenantID, item, pack.Name, pack.Qty)
			if db.IsUniqueViolation(err, "stock_pack_name") {
				return httpx.E(http.StatusConflict, "duplicate_pack", "this item already has a pack with that name")
			}
			return err
		})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, pack)
	}
}

// updatePack only switches a pack off (or on again): its name and size are
// part of the history of every delivery that used it.
func updatePack(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r, "packID")
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		var in struct {
			Active *bool `json:"active"`
		}
		if err := httpx.Decode(w, r, 1<<10, &in); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if in.Active == nil {
			httpx.Fail(w, r, bad("bad_request", "send active: true or false; a pack's name and size never change"))
			return
		}
		p := auth.Must(r.Context())
		err = db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(r.Context(), `UPDATE stock_pack SET active = $2 WHERE id = $1`, id, *in.Active)
			if db.IsUniqueViolation(err, "stock_pack_name") {
				return httpx.E(http.StatusConflict, "duplicate_pack", "another pack of this item in use has that name")
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

// allPacks returns every item's packs: active ones first, by name; with
// inactive true, the switched-off ones too (for an item's own page).
func allPacks(ctx context.Context, tx *sql.Tx, inactive bool) (map[uuid.UUID][]Pack, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, stock_item_id, name, qty, active FROM stock_pack
		WHERE active OR $1
		ORDER BY stock_item_id, NOT active, lower(name), id`, inactive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID][]Pack{}
	for rows.Next() {
		var p Pack
		if err := rows.Scan(&p.ID, &p.ItemID, &p.Name, &p.Qty, &p.Active); err != nil {
			return nil, err
		}
		out[p.ItemID] = append(out[p.ItemID], p)
	}
	return out, rows.Err()
}
