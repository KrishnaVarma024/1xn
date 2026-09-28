// Package catalogue owns the menu: tax profiles, categories, items and
// per-channel prices, and publishes a versioned snapshot to devices.
//
// Cross-module API: ItemRates. (Devices read the snapshot over HTTP.)
package catalogue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lib/pq"

	"1xn/core/internal/platform/auth"
	"1xn/core/internal/platform/db"
	"1xn/core/internal/platform/httpx"
	"1xn/core/internal/platform/ids"
	"1xn/core/modules/devices"
	"1xn/core/modules/identity"
)

var channels = map[string]bool{"dine_in": true, "takeaway": true, "delivery": true, "swiggy": true, "zomato": true}

// bump increments the tenant's catalogue version. Every write calls it in the
// same transaction, so the version a device sees always matches the data.
func bump(ctx context.Context, tx *sql.Tx, tenantID uuid.UUID) (int64, error) {
	var v int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO catalogue_state (tenant_id, version) VALUES ($1, 1)
		ON CONFLICT (tenant_id) DO UPDATE SET version = catalogue_state.version + 1
		RETURNING version`, tenantID).Scan(&v)
	return v, err
}

func bad(code, msg string) error { return httpx.E(http.StatusBadRequest, code, msg) }

// ItemRates returns the current GST rate of each item id that exists in the
// caller's tenant. Unknown ids (open items typed at the till) are absent.
func ItemRates(ctx context.Context, tx *sql.Tx, itemIDs []uuid.UUID) (map[uuid.UUID]int64, error) {
	out := map[uuid.UUID]int64{}
	if len(itemIDs) == 0 {
		return out, nil
	}
	strs := make([]string, len(itemIDs))
	for i, id := range itemIDs {
		strs[i] = id.String()
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT i.id, t.rate_bp FROM menu_item i JOIN tax_profile t ON t.id = i.tax_profile_id
		WHERE i.id = ANY($1::uuid[])`, pq.Array(strs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var rate int64
		if err := rows.Scan(&id, &rate); err != nil {
			return nil, err
		}
		out[id] = rate
	}
	return out, rows.Err()
}

// TaxProfile is a named GST rate, e.g. "Restaurant service 5%".
type TaxProfile struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	RateBP int64     `json:"rate_bp"`
	HSNSAC string    `json:"hsn_sac,omitempty"`
}

// Category groups items on the menu.
type Category struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Sort int       `json:"sort"`
}

// Item is one sellable thing.
type Item struct {
	ID            uuid.UUID        `json:"id"`
	CategoryID    uuid.UUID        `json:"category_id"`
	TaxProfileID  uuid.UUID        `json:"tax_profile_id"`
	TaxRateBP     int64            `json:"tax_rate_bp"`
	Name          string           `json:"name"`
	PricePaise    int64            `json:"price_paise"`
	ChannelPrices map[string]int64 `json:"channel_prices,omitempty"`
	Available     bool             `json:"available"`
}

// Snapshot is the whole menu as a device needs it, plus the outlet's billing
// settings. The device stores it in SQLite and bills from it offline.
type Snapshot struct {
	Version     int64               `json:"version"`
	Outlet      identity.OutletInfo `json:"outlet"`
	TaxProfiles []TaxProfile        `json:"tax_profiles"`
	Categories  []Category          `json:"categories"`
	Items       []Item              `json:"items"`
}

func loadSnapshot(ctx context.Context, tx *sql.Tx, tenantID, outletID uuid.UUID) (Snapshot, error) {
	s := Snapshot{TaxProfiles: []TaxProfile{}, Categories: []Category{}, Items: []Item{}}
	var err error
	if s.Outlet, err = identity.Outlet(ctx, tx, outletID); err != nil {
		return s, err
	}
	err = tx.QueryRowContext(ctx, `SELECT coalesce((SELECT version FROM catalogue_state WHERE tenant_id = $1), 0)`,
		tenantID).Scan(&s.Version)
	if err != nil {
		return s, err
	}

	rows, err := tx.QueryContext(ctx, `SELECT id, name, rate_bp, coalesce(hsn_sac, '') FROM tax_profile ORDER BY name`)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var t TaxProfile
		if err := rows.Scan(&t.ID, &t.Name, &t.RateBP, &t.HSNSAC); err != nil {
			rows.Close()
			return s, err
		}
		s.TaxProfiles = append(s.TaxProfiles, t)
	}
	rows.Close()

	rows, err = tx.QueryContext(ctx, `SELECT id, name, sort FROM menu_category ORDER BY sort, name`)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name, &c.Sort); err != nil {
			rows.Close()
			return s, err
		}
		s.Categories = append(s.Categories, c)
	}
	rows.Close()

	rows, err = tx.QueryContext(ctx, `
		SELECT i.id, i.category_id, i.tax_profile_id, t.rate_bp, i.name, i.price_paise, i.available,
		       coalesce(array_agg(p.channel) FILTER (WHERE p.channel IS NOT NULL), '{}'),
		       coalesce(array_agg(p.price_paise) FILTER (WHERE p.channel IS NOT NULL), '{}')
		FROM menu_item i
		JOIN tax_profile t ON t.id = i.tax_profile_id
		LEFT JOIN menu_item_channel_price p ON p.item_id = i.id
		GROUP BY i.id, t.rate_bp
		ORDER BY i.name`)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var it Item
		var chans []string
		var prices []int64
		if err := rows.Scan(&it.ID, &it.CategoryID, &it.TaxProfileID, &it.TaxRateBP, &it.Name, &it.PricePaise,
			&it.Available, pq.Array(&chans), pq.Array(&prices)); err != nil {
			return s, err
		}
		if len(chans) > 0 {
			it.ChannelPrices = map[string]int64{}
			for i, c := range chans {
				it.ChannelPrices[c] = prices[i]
			}
		}
		s.Items = append(s.Items, it)
	}
	return s, rows.Err()
}

// OwnerRoutes is mounted at /v1/catalogue.
func OwnerRoutes(d *sql.DB) http.Handler {
	r := chi.NewRouter()

	r.Post("/tax-profiles", func(w http.ResponseWriter, r *http.Request) {
		var t TaxProfile
		if err := httpx.Decode(w, r, 4<<10, &t); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		t.Name = strings.TrimSpace(t.Name)
		if t.Name == "" || t.RateBP < 0 || t.RateBP > 4000 {
			httpx.Fail(w, r, bad("bad_tax_profile", "name is required and rate_bp must be 0 to 4000 (0% to 40%)"))
			return
		}
		t.ID = ids.New()
		write(w, r, d, http.StatusCreated, t, func(ctx context.Context, tx *sql.Tx, tenant uuid.UUID) error {
			var hsn any
			if t.HSNSAC != "" {
				hsn = t.HSNSAC
			}
			_, err := tx.ExecContext(ctx,
				`INSERT INTO tax_profile (id, tenant_id, name, rate_bp, hsn_sac) VALUES ($1, $2, $3, $4, $5)`,
				t.ID, tenant, t.Name, t.RateBP, hsn)
			return err
		})
	})

	r.Post("/categories", func(w http.ResponseWriter, r *http.Request) {
		var c Category
		if err := httpx.Decode(w, r, 4<<10, &c); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		c.Name = strings.TrimSpace(c.Name)
		if c.Name == "" {
			httpx.Fail(w, r, bad("bad_category", "name is required"))
			return
		}
		c.ID = ids.New()
		write(w, r, d, http.StatusCreated, c, func(ctx context.Context, tx *sql.Tx, tenant uuid.UUID) error {
			_, err := tx.ExecContext(ctx,
				`INSERT INTO menu_category (id, tenant_id, name, sort) VALUES ($1, $2, $3, $4)`,
				c.ID, tenant, c.Name, c.Sort)
			return err
		})
	})

	r.Post("/items", func(w http.ResponseWriter, r *http.Request) {
		var it Item
		if err := httpx.Decode(w, r, 8<<10, &it); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		it.Name = strings.TrimSpace(it.Name)
		if it.Name == "" || it.PricePaise < 0 || it.PricePaise > 1_000_000_000 {
			httpx.Fail(w, r, bad("bad_item", "name is required and price_paise must be 0 to 1,000,000,000"))
			return
		}
		for ch, p := range it.ChannelPrices {
			if !channels[ch] || p < 0 || p > 1_000_000_000 {
				httpx.Fail(w, r, bad("bad_channel_price", fmt.Sprintf("channel %q or its price is invalid", ch)))
				return
			}
		}
		it.ID = ids.New()
		it.Available = true
		write(w, r, d, http.StatusCreated, &it, func(ctx context.Context, tx *sql.Tx, tenant uuid.UUID) error {
			// Reading the rate through RLS also proves the tax profile is ours.
			err := tx.QueryRowContext(ctx, `SELECT rate_bp FROM tax_profile WHERE id = $1`, it.TaxProfileID).Scan(&it.TaxRateBP)
			if errors.Is(err, sql.ErrNoRows) {
				return bad("bad_tax_profile", "tax_profile_id does not exist")
			}
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `
				INSERT INTO menu_item (id, tenant_id, category_id, tax_profile_id, name, price_paise)
				VALUES ($1, $2, $3, $4, $5, $6)`,
				it.ID, tenant, it.CategoryID, it.TaxProfileID, it.Name, it.PricePaise)
			if err != nil {
				return foreignKeyToBadRequest(err)
			}
			for ch, p := range it.ChannelPrices {
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO menu_item_channel_price (tenant_id, item_id, channel, price_paise)
					VALUES ($1, $2, $3, $4)`, tenant, it.ID, ch, p); err != nil {
					return err
				}
			}
			return nil
		})
	})

	// The one-tap "sold out": flips availability and bumps the version, so
	// every till hides the item on its next menu check.
	r.Patch("/items/{itemID}/availability", func(w http.ResponseWriter, r *http.Request) {
		itemID, err := uuid.Parse(chi.URLParam(r, "itemID"))
		if err != nil {
			httpx.Fail(w, r, httpx.ErrNotFound)
			return
		}
		var req struct {
			Available *bool `json:"available"`
		}
		if err := httpx.Decode(w, r, 1<<10, &req); err != nil || req.Available == nil {
			httpx.Fail(w, r, bad("bad_request", `send {"available": true|false}`))
			return
		}
		write(w, r, d, http.StatusOK, map[string]any{"id": itemID, "available": *req.Available},
			func(ctx context.Context, tx *sql.Tx, _ uuid.UUID) error {
				res, err := tx.ExecContext(ctx,
					`UPDATE menu_item SET available = $2, updated_at = now() WHERE id = $1`, itemID, *req.Available)
				if err != nil {
					return err
				}
				if n, _ := res.RowsAffected(); n == 0 {
					return httpx.ErrNotFound
				}
				return nil
			})
	})
	return r
}

// write runs a catalogue change and the version bump in one tenant
// transaction, then answers with body.
func write(w http.ResponseWriter, r *http.Request, d *sql.DB, status int, body any,
	fn func(ctx context.Context, tx *sql.Tx, tenant uuid.UUID) error) {
	p := auth.Must(r.Context())
	err := db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
		if err := fn(r.Context(), tx, p.TenantID); err != nil {
			return err
		}
		_, err := bump(r.Context(), tx, p.TenantID)
		return err
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, status, body)
}

func foreignKeyToBadRequest(err error) error {
	var pe *pq.Error
	if errors.As(err, &pe) && pe.Code == "23503" {
		return bad("bad_reference", "category_id or tax_profile_id does not exist")
	}
	return err
}

// DeviceRoutes is mounted at /v1/devices/me.
//
// GET /menu answers with an ETag of the catalogue version. A device that
// sends If-None-Match with the version it already has gets 304 and no body,
// so polling every few seconds costs one indexed read.
func DeviceRoutes(d *sql.DB) http.Handler {
	r := chi.NewRouter()
	r.Get("/menu", func(w http.ResponseWriter, r *http.Request) {
		p := auth.Must(r.Context())
		var snap *Snapshot
		var etag string
		err := db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			dev, err := devices.Get(r.Context(), tx, p.SubjectID)
			if err != nil {
				return err
			}
			var version int64
			if err := tx.QueryRowContext(r.Context(),
				`SELECT coalesce((SELECT version FROM catalogue_state WHERE tenant_id = $1), 0)`,
				p.TenantID).Scan(&version); err != nil {
				return err
			}
			etag = fmt.Sprintf(`"v%d"`, version)
			if r.Header.Get("If-None-Match") == etag {
				return nil // unchanged: skip loading the menu at all
			}
			s, err := loadSnapshot(r.Context(), tx, p.TenantID, dev.OutletID)
			snap = &s
			return err
		})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "no-cache") // always revalidate; a 304 is cheap
		if snap == nil {
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		// The ETag is the version actually loaded (a write may have landed in
		// between; the device then simply fetches again next time).
		w.Header().Set("ETag", fmt.Sprintf(`"v%d"`, snap.Version))
		httpx.JSON(w, http.StatusOK, snap)
	})
	return r
}
