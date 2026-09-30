// Package catalogue owns the menu: tax profiles, categories, items,
// per-channel prices, kitchen sections and modifiers, and publishes a
// versioned snapshot to devices.
//
// Cross-module API: ItemRates, KnownSections, KnownOptions, Tables. (Devices read the snapshot over
// HTTP.)
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

// KnownSections returns which of the given kitchen section ids exist in the
// caller's tenant.
func KnownSections(ctx context.Context, tx *sql.Tx, sectionIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	if len(sectionIDs) == 0 {
		return out, nil
	}
	strs := make([]string, len(sectionIDs))
	for i, id := range sectionIDs {
		strs[i] = id.String()
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM kitchen_section WHERE id = ANY($1::uuid[])`, pq.Array(strs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// TableRef is what the orders module needs to know about a table.
type TableRef struct {
	OutletID uuid.UUID
	Active   bool
}

// Tables returns each given table id that exists in the caller's tenant,
// active or not. An order opened offline on a table that was switched off in
// the meantime still happened, so the caller decides what inactive means.
func Tables(ctx context.Context, tx *sql.Tx, tableIDs []uuid.UUID) (map[uuid.UUID]TableRef, error) {
	out := map[uuid.UUID]TableRef{}
	if len(tableIDs) == 0 {
		return out, nil
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT id, outlet_id, active FROM dining_table WHERE id = ANY($1::uuid[])`, pq.Array(uuidStrings(tableIDs)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var t TableRef
		if err := rows.Scan(&id, &t.OutletID, &t.Active); err != nil {
			return nil, err
		}
		out[id] = t
	}
	return out, rows.Err()
}

// KnownOptions reports which of the given modifier option ids exist in the
// caller's tenant.
func KnownOptions(ctx context.Context, tx *sql.Tx, optionIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	if len(optionIDs) == 0 {
		return out, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM modifier_option WHERE id = ANY($1::uuid[])`, pq.Array(uuidStrings(optionIDs)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func uuidStrings(list []uuid.UUID) []string {
	out := make([]string, len(list))
	for i, id := range list {
		out[i] = id.String()
	}
	return out
}

// Table is a place to seat guests.
type Table struct {
	ID     uuid.UUID `json:"id"`
	Label  string    `json:"label"`
	Seats  int       `json:"seats"`
	Sort   int       `json:"sort"`
	Active bool      `json:"active"`
}

// Floor is a room or area of an outlet, with its tables.
type Floor struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Sort   int       `json:"sort"`
	Tables []Table   `json:"tables"`
}

// Section is a kitchen section: where an item is cooked.
type Section struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Sort int       `json:"sort"`
}

// ModifierOption is one choice in a modifier group ("Extra cheese", +Rs 40).
type ModifierOption struct {
	ID         uuid.UUID `json:"id"`
	Name       string    `json:"name"`
	PricePaise int64     `json:"price_paise"`
	Sort       int       `json:"sort"`
	Available  bool      `json:"available"`
}

// ModifierGroup is a question asked when an item is ordered.
type ModifierGroup struct {
	ID        uuid.UUID        `json:"id"`
	Name      string           `json:"name"`
	MinSelect int              `json:"min_select"`
	MaxSelect int              `json:"max_select"`
	Options   []ModifierOption `json:"options"`
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
	// SectionID is the kitchen section that cooks it; nil means no KOT.
	SectionID        *uuid.UUID  `json:"section_id,omitempty"`
	ModifierGroupIDs []uuid.UUID `json:"modifier_group_ids,omitempty"`
}

// Snapshot is the whole menu as a device needs it, plus the outlet's billing
// settings. The device stores it in SQLite and bills from it offline.
type Snapshot struct {
	Version        int64               `json:"version"`
	Outlet         identity.OutletInfo `json:"outlet"`
	TaxProfiles    []TaxProfile        `json:"tax_profiles"`
	Categories     []Category          `json:"categories"`
	Sections       []Section           `json:"sections"`
	Floors         []Floor             `json:"floors"` // the device's own outlet only
	ModifierGroups []ModifierGroup     `json:"modifier_groups"`
	Items          []Item              `json:"items"`
}

func loadSnapshot(ctx context.Context, tx *sql.Tx, tenantID, outletID uuid.UUID) (Snapshot, error) {
	s := Snapshot{TaxProfiles: []TaxProfile{}, Categories: []Category{}, Sections: []Section{},
		Floors: []Floor{}, ModifierGroups: []ModifierGroup{}, Items: []Item{}}
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

	rows, err = tx.QueryContext(ctx, `SELECT id, name, sort FROM kitchen_section ORDER BY sort, name`)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var sec Section
		if err := rows.Scan(&sec.ID, &sec.Name, &sec.Sort); err != nil {
			rows.Close()
			return s, err
		}
		s.Sections = append(s.Sections, sec)
	}
	rows.Close()

	if s.ModifierGroups, err = loadModifierGroups(ctx, tx); err != nil {
		return s, err
	}
	if s.Floors, err = loadFloors(ctx, tx, outletID); err != nil {
		return s, err
	}

	rows, err = tx.QueryContext(ctx, `
		SELECT i.id, i.category_id, i.tax_profile_id, t.rate_bp, i.name, i.price_paise, i.available, i.section_id,
		       coalesce((SELECT array_agg(p.channel ORDER BY p.channel)
		                 FROM menu_item_channel_price p WHERE p.item_id = i.id), '{}'),
		       coalesce((SELECT array_agg(p.price_paise ORDER BY p.channel)
		                 FROM menu_item_channel_price p WHERE p.item_id = i.id), '{}'),
		       coalesce((SELECT array_agg(g.group_id::text ORDER BY g.sort, g.group_id)
		                 FROM menu_item_modifier_group g WHERE g.item_id = i.id), '{}')
		FROM menu_item i
		JOIN tax_profile t ON t.id = i.tax_profile_id
		ORDER BY i.name, i.id`)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var it Item
		var section uuid.NullUUID
		var chans, groups []string
		var prices []int64
		if err := rows.Scan(&it.ID, &it.CategoryID, &it.TaxProfileID, &it.TaxRateBP, &it.Name, &it.PricePaise,
			&it.Available, &section, pq.Array(&chans), pq.Array(&prices), pq.Array(&groups)); err != nil {
			return s, err
		}
		if section.Valid {
			it.SectionID = &section.UUID
		}
		if len(chans) > 0 {
			it.ChannelPrices = map[string]int64{}
			for i, c := range chans {
				it.ChannelPrices[c] = prices[i]
			}
		}
		for _, g := range groups {
			it.ModifierGroupIDs = append(it.ModifierGroupIDs, uuid.MustParse(g))
		}
		s.Items = append(s.Items, it)
	}
	return s, rows.Err()
}

func loadFloors(ctx context.Context, tx *sql.Tx, outletID uuid.UUID) ([]Floor, error) {
	out := []Floor{}
	index := map[uuid.UUID]int{}
	rows, err := tx.QueryContext(ctx, `SELECT id, name, sort FROM floor WHERE outlet_id = $1 ORDER BY sort, name`, outletID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		f := Floor{Tables: []Table{}}
		if err := rows.Scan(&f.ID, &f.Name, &f.Sort); err != nil {
			rows.Close()
			return nil, err
		}
		index[f.ID] = len(out)
		out = append(out, f)
	}
	rows.Close()
	rows, err = tx.QueryContext(ctx, `
		SELECT id, floor_id, label, seats, sort, active FROM dining_table
		WHERE outlet_id = $1 ORDER BY sort, label`, outletID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t Table
		var floorID uuid.UUID
		if err := rows.Scan(&t.ID, &floorID, &t.Label, &t.Seats, &t.Sort, &t.Active); err != nil {
			return nil, err
		}
		if i, ok := index[floorID]; ok {
			out[i].Tables = append(out[i].Tables, t)
		}
	}
	return out, rows.Err()
}

func loadModifierGroups(ctx context.Context, tx *sql.Tx) ([]ModifierGroup, error) {
	out := []ModifierGroup{}
	index := map[uuid.UUID]int{}
	rows, err := tx.QueryContext(ctx, `SELECT id, name, min_select, max_select FROM modifier_group ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		g := ModifierGroup{Options: []ModifierOption{}}
		if err := rows.Scan(&g.ID, &g.Name, &g.MinSelect, &g.MaxSelect); err != nil {
			rows.Close()
			return nil, err
		}
		index[g.ID] = len(out)
		out = append(out, g)
	}
	rows.Close()
	rows, err = tx.QueryContext(ctx, `
		SELECT id, group_id, name, price_paise, sort, available FROM modifier_option ORDER BY group_id, sort, name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var o ModifierOption
		var group uuid.UUID
		if err := rows.Scan(&o.ID, &group, &o.Name, &o.PricePaise, &o.Sort, &o.Available); err != nil {
			return nil, err
		}
		if i, ok := index[group]; ok {
			out[i].Options = append(out[i].Options, o)
		}
	}
	return out, rows.Err()
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

	r.Post("/sections", func(w http.ResponseWriter, r *http.Request) {
		var sec Section
		if err := httpx.Decode(w, r, 2<<10, &sec); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		sec.Name = strings.TrimSpace(sec.Name)
		if sec.Name == "" || len(sec.Name) > 60 {
			httpx.Fail(w, r, bad("bad_section", "name is required (max 60 characters)"))
			return
		}
		sec.ID = ids.New()
		write(w, r, d, http.StatusCreated, sec, func(ctx context.Context, tx *sql.Tx, tenant uuid.UUID) error {
			_, err := tx.ExecContext(ctx,
				`INSERT INTO kitchen_section (id, tenant_id, name, sort) VALUES ($1, $2, $3, $4)`,
				sec.ID, tenant, sec.Name, sec.Sort)
			if db.IsUniqueViolation(err, "") {
				return httpx.E(http.StatusConflict, "duplicate_section", "a section with that name exists")
			}
			return err
		})
	})

	r.Post("/modifier-groups", func(w http.ResponseWriter, r *http.Request) {
		var g ModifierGroup
		if err := httpx.Decode(w, r, 16<<10, &g); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		g.Name = strings.TrimSpace(g.Name)
		if g.MaxSelect == 0 {
			g.MaxSelect = 1
		}
		switch {
		case g.Name == "" || len(g.Name) > 60:
			httpx.Fail(w, r, bad("bad_modifier_group", "name is required (max 60 characters)"))
			return
		case g.MinSelect < 0 || g.MaxSelect < 1 || g.MaxSelect > 20 || g.MinSelect > g.MaxSelect:
			httpx.Fail(w, r, bad("bad_modifier_group", "need 0 <= min_select <= max_select <= 20"))
			return
		case len(g.Options) == 0 || len(g.Options) > 50:
			httpx.Fail(w, r, bad("bad_modifier_group", "a group needs 1 to 50 options"))
			return
		}
		g.ID = ids.New()
		for i := range g.Options {
			o := &g.Options[i]
			o.Name = strings.TrimSpace(o.Name)
			if o.Name == "" || len(o.Name) > 60 || o.PricePaise < 0 || o.PricePaise > 1_000_000_000 {
				httpx.Fail(w, r, bad("bad_modifier_option", fmt.Sprintf("option %d: name required, price 0 to 1,000,000,000", i)))
				return
			}
			o.ID = ids.New()
			o.Available = true
			if o.Sort == 0 {
				o.Sort = i
			}
		}
		write(w, r, d, http.StatusCreated, g, func(ctx context.Context, tx *sql.Tx, tenant uuid.UUID) error {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO modifier_group (id, tenant_id, name, min_select, max_select) VALUES ($1, $2, $3, $4, $5)`,
				g.ID, tenant, g.Name, g.MinSelect, g.MaxSelect); err != nil {
				return err
			}
			for _, o := range g.Options {
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO modifier_option (id, tenant_id, group_id, name, price_paise, sort)
					VALUES ($1, $2, $3, $4, $5, $6)`, o.ID, tenant, g.ID, o.Name, o.PricePaise, o.Sort); err != nil {
					return err
				}
			}
			return nil
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
		if len(it.ModifierGroupIDs) > 20 {
			httpx.Fail(w, r, bad("bad_item", "at most 20 modifier groups per item"))
			return
		}
		seen := map[uuid.UUID]bool{}
		for _, g := range it.ModifierGroupIDs {
			if seen[g] {
				httpx.Fail(w, r, bad("bad_item", "modifier_group_ids has a duplicate"))
				return
			}
			seen[g] = true
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
				INSERT INTO menu_item (id, tenant_id, category_id, tax_profile_id, name, price_paise, section_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`,
				it.ID, tenant, it.CategoryID, it.TaxProfileID, it.Name, it.PricePaise, it.SectionID)
			if err != nil {
				return foreignKeyToBadRequest(err)
			}
			for i, g := range it.ModifierGroupIDs {
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO menu_item_modifier_group (tenant_id, item_id, group_id, sort) VALUES ($1, $2, $3, $4)`,
					tenant, it.ID, g, i); err != nil {
					return foreignKeyToBadRequest(err)
				}
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

// CreateFloorHandler serves POST /v1/outlets/{outletID}/floors (owner).
func CreateFloorHandler(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		outletID, err := uuid.Parse(chi.URLParam(r, "outletID"))
		if err != nil {
			httpx.Fail(w, r, httpx.ErrNotFound)
			return
		}
		var f Floor
		if err := httpx.Decode(w, r, 2<<10, &f); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		f.Name = strings.TrimSpace(f.Name)
		if f.Name == "" || len(f.Name) > 60 || len(f.Tables) > 0 {
			httpx.Fail(w, r, bad("bad_floor", "name is required (max 60 characters); add tables separately"))
			return
		}
		f.ID, f.Tables = ids.New(), []Table{}
		write(w, r, d, http.StatusCreated, f, func(ctx context.Context, tx *sql.Tx, tenant uuid.UUID) error {
			if _, err := identity.Outlet(ctx, tx, outletID); err != nil {
				if errors.Is(err, identity.ErrNoOutlet) {
					return httpx.ErrNotFound
				}
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO floor (id, tenant_id, outlet_id, name, sort) VALUES ($1, $2, $3, $4, $5)`,
				f.ID, tenant, outletID, f.Name, f.Sort)
			if db.IsUniqueViolation(err, "") {
				return httpx.E(http.StatusConflict, "duplicate_floor", "this outlet already has a floor with that name")
			}
			return err
		})
	}
}

// CreateTableHandler serves POST /v1/outlets/{outletID}/tables (owner).
func CreateTableHandler(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		outletID, err := uuid.Parse(chi.URLParam(r, "outletID"))
		if err != nil {
			httpx.Fail(w, r, httpx.ErrNotFound)
			return
		}
		var req struct {
			FloorID uuid.UUID `json:"floor_id"`
			Label   string    `json:"label"`
			Seats   int       `json:"seats"`
			Sort    int       `json:"sort"`
		}
		if err := httpx.Decode(w, r, 2<<10, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		req.Label = strings.TrimSpace(req.Label)
		if req.Seats == 0 {
			req.Seats = 4
		}
		if req.Label == "" || len(req.Label) > 20 || req.Seats < 1 || req.Seats > 50 {
			httpx.Fail(w, r, bad("bad_table", "label is 1 to 20 characters and seats 1 to 50"))
			return
		}
		t := Table{ID: ids.New(), Label: req.Label, Seats: req.Seats, Sort: req.Sort, Active: true}
		write(w, r, d, http.StatusCreated, t, func(ctx context.Context, tx *sql.Tx, tenant uuid.UUID) error {
			// The floor must belong to this outlet (RLS already limits it to this tenant).
			var floorOutlet uuid.UUID
			err := tx.QueryRowContext(ctx, `SELECT outlet_id FROM floor WHERE id = $1`, req.FloorID).Scan(&floorOutlet)
			if errors.Is(err, sql.ErrNoRows) || (err == nil && floorOutlet != outletID) {
				return bad("bad_floor", "floor_id is not a floor of this outlet")
			}
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `
				INSERT INTO dining_table (id, tenant_id, outlet_id, floor_id, label, seats, sort)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`, t.ID, tenant, outletID, req.FloorID, t.Label, t.Seats, t.Sort)
			if db.IsUniqueViolation(err, "") {
				return httpx.E(http.StatusConflict, "duplicate_table", "this outlet already has a table with that label")
			}
			return err
		})
	}
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
		return bad("bad_reference", "category_id, tax_profile_id, section_id or a modifier group does not exist")
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
