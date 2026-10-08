package stock

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lib/pq"

	"1xn/core/internal/platform/auth"
	"1xn/core/internal/platform/db"
	"1xn/core/internal/platform/gst"
	"1xn/core/internal/platform/httpx"
	"1xn/core/internal/platform/ids"
	"1xn/core/modules/dayclose"
	"1xn/core/modules/ledger"
)

// Limits on what an owner can send. Far beyond any real kitchen.
const (
	maxQty     = 1_000_000_000_000 // milli-units: a million tonnes
	maxCost    = 1_000_000_000_000 // paise: Rs 1,000 crore
	maxPayment = 100_000_000_000   // paise: Rs 100 crore, as supplier_payment.amount allows
	maxLines   = 200
)

var units = map[string]bool{"kg": true, "l": true, "pc": true}

// recipesBegin is when a dish's first recipe starts: GST's first day, before
// any bill the server accepts.
var recipesBegin = time.Date(2017, 7, 1, 0, 0, 0, 0, gst.IST)

// How a delivery was paid: cash from the safe, cash from a till drawer (the
// till records the payout, so the drawer's expected cash knows), the bank,
// or on credit from the supplier.
var paidBy = map[string]bool{"safe": true, "till": true, "bank": true, "credit": true}

func unitWord(unit string) string {
	switch unit {
	case "l":
		return "litre"
	case "pc":
		return "piece"
	}
	return "kg"
}

// qtyText shows milli-units for a message: "1.5 kg", "250 g".
func qtyText(milli int64, unit string) string {
	small := map[string]string{"kg": "g", "l": "ml", "pc": "thousandths of a piece"}[unit]
	if milli > -1000 && milli < 1000 && unit != "pc" {
		return fmt.Sprintf("%d %s", milli, small)
	}
	whole, frac := milli/1000, milli%1000
	if frac < 0 {
		frac = -frac
	}
	s := fmt.Sprintf("%d", whole)
	if frac != 0 {
		s += strings.TrimRight(fmt.Sprintf(".%03d", frac), "0")
	}
	return s + " " + unit
}

func bad(code, msg string) error { return httpx.E(http.StatusBadRequest, code, msg) }

func text(what, s string, min, max int) (string, error) {
	s = strings.TrimSpace(s)
	if strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", bad("bad_"+what, what+" cannot contain control characters")
	}
	if n := utf8.RuneCountInString(s); n < min || n > max {
		if min == 0 {
			return "", bad("bad_"+what, fmt.Sprintf("%s must be at most %d characters", what, max))
		}
		return "", bad("bad_"+what, fmt.Sprintf("%s must be %d to %d characters", what, min, max))
	}
	return s, nil
}

func pathID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, httpx.ErrNotFound
	}
	return id, nil
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

// ---- items ---------------------------------------------------------------

// Item is a stock item as the owner sees it at one outlet.
type Item struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	Unit         string    `json:"unit"`
	ReorderLevel int64     `json:"reorder_level"` // milli-units
	Active       bool      `json:"active"`
	Qty          int64     `json:"qty"`       // milli-units on hand
	Value        int64     `json:"value"`     // paise
	UnitCost     int64     `json:"unit_cost"` // paise per kg, litre or piece
	LastCost     int64     `json:"last_cost"`
	Status       string    `json:"status"` // ok, low, out (none left), negative, new (never stocked)
	Packs        []Pack    `json:"packs"`  // how it is bought: active packs first
}

func status(i Item, stocked bool) string {
	switch {
	case !stocked:
		return "new"
	case i.Qty < 0:
		return "negative"
	case i.Qty == 0:
		return "out"
	case i.ReorderLevel > 0 && i.Qty < i.ReorderLevel:
		return "low"
	}
	return "ok"
}

type itemInput struct {
	Name         *string `json:"name"`
	Unit         string  `json:"unit"`
	ReorderLevel *int64  `json:"reorder_level"`
	Active       *bool   `json:"active"`
}

// OwnerRoutes is mounted at /v1/stock (owner): the tenant's stock items and
// recipes, shared by its outlets.
//
//	POST  /items                    {name, unit: kg|l|pc, reorder_level}
//	PATCH /items/{id}               {name?, reorder_level?, active?}
//	POST  /items/{id}/packs         {name, qty}: how a supplier sells it ("Sack", 25000 g)
//	PATCH /packs/{id}               {active: false}
//	PUT   /recipes/{menuItem}       {lines: [{stock_item_id, qty}]}; no lines removes the recipe
//	PUT   /addon-recipes/{option}   the same, for an add-on option ("Extra cheese")
//	POST  /suppliers                {name, phone?, gstin?}
//	PATCH /suppliers/{id}           {name?, phone?, gstin?}
func OwnerRoutes(d *sql.DB) http.Handler {
	r := chi.NewRouter()
	r.Post("/items", createItem(d))
	r.Patch("/items/{itemID}", updateItem(d))
	r.Post("/items/{itemID}/packs", createPack(d))
	r.Patch("/packs/{packID}", updatePack(d))
	r.Put("/recipes/{menuItemID}", putRecipe(d, dishRecipes))
	r.Put("/addon-recipes/{menuItemID}", putRecipe(d, addonRecipes))
	r.Post("/suppliers", createSupplier(d))
	r.Patch("/suppliers/{supplierID}", updateSupplier(d))
	return r
}

func createItem(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in itemInput
		if err := httpx.Decode(w, r, 4<<10, &in); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if in.Name == nil {
			httpx.Fail(w, r, bad("bad_name", "name is required"))
			return
		}
		name, err := text("name", *in.Name, 1, 100)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if !units[in.Unit] {
			httpx.Fail(w, r, bad("bad_unit", "unit must be kg, l or pc"))
			return
		}
		item := Item{ID: ids.New(), Name: name, Unit: in.Unit, Active: true, Status: "new"}
		if in.ReorderLevel != nil {
			item.ReorderLevel = *in.ReorderLevel
		}
		if item.ReorderLevel < 0 || item.ReorderLevel > maxQty {
			httpx.Fail(w, r, bad("bad_reorder_level", "reorder_level is out of range"))
			return
		}
		p := auth.Must(r.Context())
		err = db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(r.Context(), `
				INSERT INTO stock_item (id, tenant_id, name, unit, reorder_level) VALUES ($1, $2, $3, $4, $5)`,
				item.ID, p.TenantID, item.Name, item.Unit, item.ReorderLevel)
			if db.IsUniqueViolation(err, "stock_item_name") {
				return httpx.E(http.StatusConflict, "duplicate_item", "there is already a stock item with that name")
			}
			return err
		})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, item)
	}
}

func updateItem(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r, "itemID")
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		var in itemInput
		if err := httpx.Decode(w, r, 4<<10, &in); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if in.Unit != "" {
			// Changing kg to pc would reinterpret every quantity ever booked.
			httpx.Fail(w, r, bad("unit_fixed", "a stock item's unit cannot change; make a new item"))
			return
		}
		var name *string
		if in.Name != nil {
			n, err := text("name", *in.Name, 1, 100)
			if err != nil {
				httpx.Fail(w, r, err)
				return
			}
			name = &n
		}
		if in.ReorderLevel != nil && (*in.ReorderLevel < 0 || *in.ReorderLevel > maxQty) {
			httpx.Fail(w, r, bad("bad_reorder_level", "reorder_level is out of range"))
			return
		}
		p := auth.Must(r.Context())
		err = db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			if in.Active != nil && !*in.Active {
				// Bills would keep using it, while Receive and Count hide it.
				// Lock the item first: a recipe being saved with it holds it
				// FOR SHARE, so one of the two waits and then sees the other.
				var locked uuid.UUID
				err := tx.QueryRowContext(r.Context(), `SELECT id FROM stock_item WHERE id = $1 FOR NO KEY UPDATE`, id).Scan(&locked)
				if errors.Is(err, sql.ErrNoRows) {
					return httpx.ErrNotFound
				}
				if err != nil {
					return err
				}
				var dishes, addons int
				if err := tx.QueryRowContext(r.Context(), `
					SELECT (SELECT count(DISTINCT menu_item_id) FROM recipe_line WHERE stock_item_id = $1 AND valid_to IS NULL),
					       (SELECT count(DISTINCT modifier_option_id) FROM addon_recipe_line WHERE stock_item_id = $1 AND valid_to IS NULL)`,
					id).Scan(&dishes, &addons); err != nil {
					return err
				}
				if dishes+addons > 0 {
					what := fmt.Sprintf("%d %s", dishes, plural(dishes, "dish", "dishes"))
					switch {
					case dishes == 0:
						what = fmt.Sprintf("%d %s", addons, plural(addons, "add-on", "add-ons"))
					case addons > 0:
						what += fmt.Sprintf(" and %d %s", addons, plural(addons, "add-on", "add-ons"))
					}
					return httpx.E(http.StatusConflict, "in_recipes",
						fmt.Sprintf("%s still use this item; take it out of their recipes first", what))
				}
			}
			res, err := tx.ExecContext(r.Context(), `
				UPDATE stock_item SET name = coalesce($2, name), reorder_level = coalesce($3, reorder_level),
				       active = coalesce($4, active)
				WHERE id = $1`, id, name, in.ReorderLevel, in.Active)
			if db.IsUniqueViolation(err, "stock_item_name") {
				return httpx.E(http.StatusConflict, "duplicate_item", "there is already a stock item with that name")
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

// ---- recipes ---------------------------------------------------------------

type recipeLineInput struct {
	StockItemID uuid.UUID `json:"stock_item_id"`
	Qty         int64     `json:"qty"` // milli-units per portion
}

// recipeKind is what a recipe belongs to: a dish, or an add-on option. Both
// are versioned the same way; only the tables differ.
type recipeKind struct {
	table, key, owner string // recipe table, its owner column, the owner's table
}

var (
	dishRecipes  = recipeKind{table: "recipe_line", key: "menu_item_id", owner: "menu_item"}
	addonRecipes = recipeKind{table: "addon_recipe_line", key: "modifier_option_id", owner: "modifier_option"}
)

func putRecipe(d *sql.DB, k recipeKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dish, err := pathID(r, "menuItemID")
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		var in struct {
			Lines []recipeLineInput `json:"lines"`
		}
		if err := httpx.Decode(w, r, 32<<10, &in); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if len(in.Lines) > 50 {
			httpx.Fail(w, r, bad("bad_recipe", "a recipe has at most 50 ingredients"))
			return
		}
		seen := map[uuid.UUID]bool{}
		for _, l := range in.Lines {
			if l.Qty < 1 || l.Qty > 1_000_000_000 {
				httpx.Fail(w, r, bad("bad_qty", "each ingredient needs a quantity above zero"))
				return
			}
			if seen[l.StockItemID] {
				httpx.Fail(w, r, bad("duplicate_ingredient", "an ingredient appears twice; add the quantities together"))
				return
			}
			seen[l.StockItemID] = true
		}
		p := auth.Must(r.Context())
		err = db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			// Lock the dish: two saves of one recipe at once take turns, and
			// the second sees the first's lines.
			var found uuid.UUID
			err := tx.QueryRowContext(r.Context(), `SELECT id FROM `+k.owner+` WHERE id = $1 FOR UPDATE`, dish).Scan(&found)
			if errors.Is(err, sql.ErrNoRows) {
				return httpx.ErrNotFound
			}
			if err != nil {
				return err
			}
			if len(in.Lines) > 0 {
				items := make([]string, len(in.Lines))
				for i, l := range in.Lines {
					items[i] = l.StockItemID.String()
				}
				// FOR SHARE: an item cannot be switched off while this recipe
				// is being saved with it (updateItem locks the item before it
				// looks for recipes that use it), or the recipe would end up
				// using an item that Receive and Count no longer show.
				rows, err := tx.QueryContext(r.Context(), `
					SELECT id FROM stock_item WHERE id = ANY($1::uuid[]) AND active ORDER BY id FOR SHARE`, pq.Array(items))
				if err != nil {
					return err
				}
				n := 0
				for rows.Next() {
					n++
				}
				rows.Close()
				if err := rows.Err(); err != nil {
					return err
				}
				if n != len(items) {
					return bad("unknown_stock_item", "a recipe names a stock item that does not exist or is not in use")
				}
			}
			// Recipes are versioned: the current lines end now and the new ones
			// start now, so a bill uses the recipe of the moment it was issued.
			// A dish's FIRST recipe describes how it has always been made, so
			// it counts from the start (bills issued earlier and synced later
			// use it too).
			var now time.Time
			var first bool
			if err := tx.QueryRowContext(r.Context(), `
				SELECT clock_timestamp(), NOT EXISTS (SELECT 1 FROM `+k.table+` WHERE `+k.key+` = $1)`, dish).Scan(&now, &first); err != nil {
				return err
			}
			from := now
			if first {
				from = recipesBegin
			}
			if _, err := tx.ExecContext(r.Context(), `
				UPDATE `+k.table+` SET valid_to = $2 WHERE `+k.key+` = $1 AND valid_to IS NULL`, dish, now); err != nil {
				return err
			}
			for _, l := range in.Lines {
				if _, err := tx.ExecContext(r.Context(), `
					INSERT INTO `+k.table+` (tenant_id, `+k.key+`, stock_item_id, qty, valid_from)
					VALUES ($1, $2, $3, $4, $5)`, p.TenantID, dish, l.StockItemID, l.Qty, from); err != nil {
					return err
				}
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

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// ---- outlet stock --------------------------------------------------------

// OutletRoutes is mounted at /v1/outlets/{outletID}/stock (owner).
//
//	GET  /                 every stock item with what this outlet holds
//	GET  /items/{id}       one item and its latest moves
//	POST /purchases        goods received: {id, supplier | supplier_id, invoice_no, date, paid_by,
//	                       lines: [{stock_item_id, qty | pack_id + packs, cost}]}
//	GET  /purchases        the latest purchases
//	POST /wastage          {id, stock_item_id, qty, reason, date}
//	POST /counts           a physical count: {id, lines: [{stock_item_id, counted}], note, date}
//	GET  /recipes          every dish with its recipe and what a portion costs here
//	GET  /food-cost        ?from=&to= sales, food cost, wastage, count differences
//	GET  /suppliers        suppliers with what this outlet's books owe each
//	GET  /suppliers/{id}   a supplier's statement: deliveries and payments, with the running balance
//	POST /supplier-payments {id, supplier_id, amount, paid_by: safe|bank|till, date, reference}
//
// Documents carry an id the back office made before submitting (a UUIDv7):
// sending the same one again returns the first result and books nothing.
func OutletRoutes(d *sql.DB) http.Handler {
	r := chi.NewRouter()
	r.Get("/", listItems(d))
	r.Get("/items/{itemID}", itemHistory(d))
	r.Post("/purchases", postPurchase(d))
	r.Get("/purchases", listPurchases(d))
	r.Post("/wastage", postWastage(d))
	r.Post("/counts", postCount(d))
	r.Get("/recipes", recipes(d))
	r.Get("/food-cost", foodCost(d))
	r.Get("/suppliers", listSuppliers(d))
	r.Get("/suppliers/{supplierID}", supplierStatement(d))
	r.Post("/supplier-payments", postSupplierPayment(d))
	return r
}

// readTx runs fn for an existing outlet in one consistent snapshot.
func readTx(w http.ResponseWriter, r *http.Request, d *sql.DB, fn func(tx *sql.Tx, outlet uuid.UUID) (any, error)) {
	outlet, err := pathID(r, "outletID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	p := auth.Must(r.Context())
	var out any
	err = db.InTenantReadTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
		if err := outletExists(r.Context(), tx, outlet); err != nil {
			return err
		}
		var err error
		out, err = fn(tx, outlet)
		return err
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func listItems(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		readTx(w, r, d, func(tx *sql.Tx, outlet uuid.UUID) (any, error) {
			items, err := loadItems(r.Context(), tx, outlet, nil)
			if err != nil {
				return nil, err
			}
			var total int64
			for _, i := range items {
				total += i.Value
			}
			return map[string]any{"items": items, "value": total}, nil
		})
	}
}

func loadItems(ctx context.Context, tx *sql.Tx, outlet uuid.UUID, only *uuid.UUID) ([]Item, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT i.id, i.name, i.unit, i.reorder_level, i.active, b.stock_item_id IS NOT NULL,
		       coalesce(b.qty, 0), coalesce(b.value, 0), coalesce(b.last_cost, 0)
		FROM stock_item i
		LEFT JOIN stock_balance b ON b.stock_item_id = i.id AND b.outlet_id = $1
		WHERE $2::uuid IS NULL OR i.id = $2
		ORDER BY i.active DESC, lower(i.name), i.id`, outlet, only)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Item{}
	for rows.Next() {
		var i Item
		var stocked bool
		var b Balance
		if err := rows.Scan(&i.ID, &i.Name, &i.Unit, &i.ReorderLevel, &i.Active, &stocked, &b.Qty, &b.Value, &b.LastCost); err != nil {
			return nil, err
		}
		i.Qty, i.Value, i.LastCost, i.UnitCost = b.Qty, b.Value, b.LastCost, b.UnitCost()
		i.Status = status(i, stocked)
		i.Packs = []Pack{}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	packs, err := allPacks(ctx, tx, only != nil)
	if err != nil {
		return nil, err
	}
	for k := range items {
		if ps := packs[items[k].ID]; ps != nil {
			items[k].Packs = ps
		}
	}
	return items, nil
}

// Move is one line of an item's history.
type Move struct {
	DocID        uuid.UUID `json:"doc_id"`
	Kind         string    `json:"kind"`
	Qty          int64     `json:"qty"`
	Value        int64     `json:"value"`
	QtyAfter     int64     `json:"qty_after"`
	BusinessDate string    `json:"business_date"`
	At           time.Time `json:"at"`
	Note         string    `json:"note,omitempty"`       // the supplier, the wastage reason, the count note
	InvoiceNo    string    `json:"invoice_no,omitempty"` // the bill (for sales) or the supplier's invoice
	Pack         string    `json:"pack,omitempty"`       // received in packs: the pack's name
	Packs        int64     `json:"packs,omitempty"`      // and how many
}

func itemHistory(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r, "itemID")
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		readTx(w, r, d, func(tx *sql.Tx, outlet uuid.UUID) (any, error) {
			items, err := loadItems(r.Context(), tx, outlet, &id)
			if err != nil {
				return nil, err
			}
			if len(items) == 0 {
				return nil, httpx.ErrNotFound
			}
			rows, err := tx.QueryContext(r.Context(), `
				SELECT m.doc_id, m.kind, m.qty, m.value, m.qty_after, m.business_date, m.at,
				       coalesce(d.supplier, d.note, ''), coalesce(b.invoice_no, d.invoice_no, ''),
				       coalesce(pk.name, ''), coalesce(m.packs, 0)
				FROM stock_move m
				JOIN stock_doc d ON d.id = m.doc_id AND d.tenant_id = m.tenant_id
				LEFT JOIN bill b ON b.id = d.bill_id AND b.tenant_id = d.tenant_id
				LEFT JOIN stock_pack pk ON pk.id = m.pack_id AND pk.tenant_id = m.tenant_id
				WHERE m.outlet_id = $1 AND m.stock_item_id = $2
				ORDER BY m.n DESC LIMIT 200`, outlet, id)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			moves := []Move{}
			for rows.Next() {
				var m Move
				var day time.Time
				if err := rows.Scan(&m.DocID, &m.Kind, &m.Qty, &m.Value, &m.QtyAfter, &day, &m.At, &m.Note, &m.InvoiceNo,
					&m.Pack, &m.Packs); err != nil {
					return nil, err
				}
				m.BusinessDate = day.Format(time.DateOnly)
				moves = append(moves, m)
			}
			return map[string]any{"item": items[0], "moves": moves}, rows.Err()
		})
	}
}

// ---- documents -------------------------------------------------------------

// docTx runs an owner document: it serialises on the document's id (two
// clicks of "Save" arrive as two requests with one id), answers a replay
// with the stored document, and otherwise runs fn.
func docTx(w http.ResponseWriter, r *http.Request, d *sql.DB, id uuid.UUID, kind string, request any,
	fn func(tx *sql.Tx, outlet uuid.UUID, now time.Time, hash []byte) (any, error)) {
	outlet, err := pathID(r, "outletID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if id.Version() != 7 {
		httpx.Fail(w, r, bad("bad_id", "id must be a UUIDv7 made when the form was opened"))
		return
	}
	// The request's fingerprint tells a replay (same id, same document) from
	// an id reused for something else, which is refused rather than ignored.
	raw, err := json.Marshal(request)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	sum := sha256.Sum256(raw)
	hash := sum[:]
	p := auth.Must(r.Context())
	status := http.StatusCreated
	var out any
	err = db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
		if err := outletExists(r.Context(), tx, outlet); err != nil {
			return err
		}
		// An advisory lock on the id: the second click waits for the first
		// to commit, then finds its document.
		if _, err := tx.ExecContext(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, id.String()); err != nil {
			return err
		}
		var existing string
		var stored []byte
		err := tx.QueryRowContext(r.Context(), `SELECT kind, request_hash FROM stock_doc WHERE id = $1`, id).Scan(&existing, &stored)
		if err == nil {
			if existing != kind || !bytes.Equal(stored, hash) {
				return httpx.E(http.StatusConflict, "id_used",
					"that id was already used for a different document; reload the form and enter it again")
			}
			status = http.StatusOK
			out, err = replay(r.Context(), tx, id, kind)
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		out, err = fn(tx, outlet, time.Now(), hash)
		switch {
		case errors.Is(err, ErrUnknownItem):
			return bad("unknown_stock_item", "a line names a stock item that does not exist")
		case errors.Is(err, ErrTooLarge):
			return bad("too_large", "a quantity or price is far too large; check what was typed")
		case errors.Is(err, ledger.ErrPeriodLocked):
			return httpx.E(http.StatusConflict, "period_locked",
				"the accounting month of that date is locked; date the document in an open month")
		}
		return err
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, status, out)
}

// Doc is a stored stock document, briefly.
type Doc struct {
	ID           uuid.UUID `json:"id"`
	Kind         string    `json:"kind"`
	BusinessDate string    `json:"business_date"`
	At           time.Time `json:"at"`
	Supplier     string    `json:"supplier,omitempty"`
	InvoiceNo    string    `json:"invoice_no,omitempty"`
	PaidBy       string    `json:"paid_by,omitempty"`
	Total        int64     `json:"total"`
	Note         string    `json:"note,omitempty"`
	LedgerStatus string    `json:"ledger_status"`
	Lines        int       `json:"lines"`
}

func docSummary(ctx context.Context, tx *sql.Tx, id uuid.UUID) (*Doc, error) {
	var doc Doc
	var day time.Time
	err := tx.QueryRowContext(ctx, `
		SELECT d.id, d.kind, d.business_date, d.at, coalesce(d.supplier, ''), coalesce(d.invoice_no, ''),
		       coalesce(d.paid_by, ''), d.total, coalesce(d.note, ''), d.ledger_status,
		       (SELECT count(*) FROM stock_move m WHERE m.doc_id = d.id AND m.tenant_id = d.tenant_id AND m.kind <> 'trueup')
		FROM stock_doc d WHERE d.id = $1`, id).
		Scan(&doc.ID, &doc.Kind, &day, &doc.At, &doc.Supplier, &doc.InvoiceNo, &doc.PaidBy, &doc.Total, &doc.Note,
			&doc.LedgerStatus, &doc.Lines)
	doc.BusinessDate = day.Format(time.DateOnly)
	return &doc, err
}

// replay answers a repeated request with what the first one answered.
func replay(ctx context.Context, tx *sql.Tx, id uuid.UUID, kind string) (any, error) {
	doc, err := docSummary(ctx, tx, id)
	if err != nil || kind != "count" {
		return doc, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT m.stock_item_id, i.name, m.qty_after - m.qty, m.qty_after, -m.value
		FROM stock_move m JOIN stock_item i ON i.id = m.stock_item_id AND i.tenant_id = m.tenant_id
		WHERE m.doc_id = $1 ORDER BY m.n`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	lines := []CountLine{}
	for rows.Next() {
		var l CountLine
		if err := rows.Scan(&l.StockItemID, &l.Name, &l.Before, &l.Counted, &l.Value); err != nil {
			return nil, err
		}
		lines = append(lines, l)
	}
	return map[string]any{"document": doc, "lines": lines}, rows.Err()
}

// day reads an optional YYYY-MM-DD: from GST's start to today (India's
// trading day). Stock documents are never dated in the future.
func day(s string, now time.Time) (time.Time, error) {
	today := dayclose.TradingDay(now)
	if s == "" {
		return today, nil
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil || t.Before(time.Date(2017, 7, 1, 0, 0, 0, 0, time.UTC)) || t.After(today) {
		return time.Time{}, bad("bad_date", "date must be a real day, YYYY-MM-DD, not in the future")
	}
	return t, nil
}

// entryDate is the date a document's journal entry carries: its day, in India.
func entryDate(d time.Time) time.Time {
	return time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, gst.IST)
}

type purchaseLine struct {
	StockItemID uuid.UUID  `json:"stock_item_id"`
	Qty         int64      `json:"qty,omitempty"`     // milli-units received; or, received in packs:
	PackID      *uuid.UUID `json:"pack_id,omitempty"` // the pack
	Packs       int64      `json:"packs,omitempty"`   // and how many (the quantity is packs x the pack's size)
	Cost        int64      `json:"cost"`              // paise for the whole line, taxes included (no input credit for restaurants at 5%)
}

func postPurchase(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID         uuid.UUID      `json:"id"`
			Supplier   string         `json:"supplier,omitempty"`
			SupplierID *uuid.UUID     `json:"supplier_id,omitempty"`
			InvoiceNo  string         `json:"invoice_no"`
			Date       string         `json:"date"`
			PaidBy     string         `json:"paid_by"`
			Lines      []purchaseLine `json:"lines"`
		}
		if err := httpx.Decode(w, r, 64<<10, &in); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		var supplierName string
		var err error
		if in.SupplierID == nil {
			supplierName, err = text("supplier", in.Supplier, 1, 100)
		} else if in.Supplier != "" {
			err = bad("bad_supplier", "send supplier_id or a supplier name, not both")
		}
		if err == nil {
			in.InvoiceNo, err = text("invoice_no", in.InvoiceNo, 0, 40)
		}
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if !paidBy[in.PaidBy] {
			httpx.Fail(w, r, bad("bad_paid_by",
				"paid_by must be safe (cash from the safe), till (cash from a till drawer: the till records the payout), bank or credit"))
			return
		}
		if err := checkLines(len(in.Lines), func(i int) (uuid.UUID, error) {
			l := in.Lines[i]
			if l.PackID != nil {
				if l.Qty != 0 || l.Packs < 1 || l.Packs > maxPacks {
					return l.StockItemID, bad("bad_line", fmt.Sprintf("line %d: with a pack, send how many packs (1 to %d) and no quantity", i+1, maxPacks))
				}
			} else if l.Packs != 0 || l.Qty < 1 || l.Qty > maxQty {
				return l.StockItemID, bad("bad_line", fmt.Sprintf("line %d: quantity must be above zero", i+1))
			}
			if l.Cost < 0 || l.Cost > maxCost {
				return l.StockItemID, bad("bad_line", fmt.Sprintf("line %d: cost must not be negative", i+1))
			}
			return l.StockItemID, nil
		}); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		date, err := day(in.Date, time.Now())
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		p := auth.Must(r.Context())
		docTx(w, r, d, in.ID, "purchase", in, func(tx *sql.Tx, outlet uuid.UUID, now time.Time, hash []byte) (any, error) {
			supplier, err := resolveSupplier(r.Context(), tx, p.TenantID, in.SupplierID, supplierName)
			if err != nil {
				return nil, err
			}
			// Packs become quantities: packs x the pack's size, of its own item.
			qty := make([]int64, len(in.Lines))
			for i, l := range in.Lines {
				qty[i] = l.Qty
				if l.PackID == nil {
					continue
				}
				var item uuid.UUID
				var size int64
				var active bool
				err := tx.QueryRowContext(r.Context(), `SELECT stock_item_id, qty, active FROM stock_pack WHERE id = $1`, *l.PackID).
					Scan(&item, &size, &active)
				if errors.Is(err, sql.ErrNoRows) || (err == nil && item != l.StockItemID) {
					return nil, bad("unknown_pack", fmt.Sprintf("line %d: that pack is not one of this item's", i+1))
				}
				if err != nil {
					return nil, err
				}
				if !active {
					return nil, bad("pack_inactive", fmt.Sprintf("line %d: that pack is switched off; pick another or receive loose", i+1))
				}
				n, ok := mulDivOK(size, l.Packs, 1)
				if !ok || n > maxQty {
					return nil, ErrTooLarge
				}
				qty[i] = n
			}
			items := make([]uuid.UUID, len(in.Lines))
			for i, l := range in.Lines {
				items[i] = l.StockItemID
			}
			b, err := openBook(r.Context(), tx, p.TenantID, outlet, in.ID, date, now, entryDate(date), false, items)
			if err != nil {
				return nil, err
			}
			var total, trueUp int64
			for i, l := range in.Lines {
				total += l.Cost
				trueUp += b.inPacks(l.StockItemID, qty[i], l.Cost, l.PackID, l.Packs)
			}
			memo := "Goods received from " + supplier.Name
			if in.InvoiceNo != "" {
				memo += ", invoice " + in.InvoiceNo
			}
			e, err := ledger.StockPurchaseEntry(p.TenantID, b.legal, outlet, in.ID, total, trueUp, in.PaidBy,
				b.entryDate, date, memo)
			if err != nil {
				return nil, err
			}
			by := p.SubjectID
			if err := b.finish(header{Kind: "purchase", Supplier: supplier.Name, SupplierID: &supplier.ID, InvoiceNo: in.InvoiceNo,
				PaidBy: in.PaidBy, Total: total, CreatedBy: &by, RequestHash: hash}, &e); err != nil {
				return nil, err
			}
			return docSummary(r.Context(), tx, in.ID)
		})
	}
}

// checkLines checks a document's lines: 1 to maxLines, each valid, no item twice.
func checkLines(n int, line func(i int) (uuid.UUID, error)) error {
	if n < 1 || n > maxLines {
		return bad("bad_lines", fmt.Sprintf("a document has 1 to %d lines", maxLines))
	}
	seen := map[uuid.UUID]bool{}
	for i := 0; i < n; i++ {
		id, err := line(i)
		if err != nil {
			return err
		}
		if seen[id] {
			return bad("duplicate_item", fmt.Sprintf("line %d: that item is already on the document; add the quantities together", i+1))
		}
		seen[id] = true
	}
	return nil
}

func listPurchases(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		readTx(w, r, d, func(tx *sql.Tx, outlet uuid.UUID) (any, error) {
			rows, err := tx.QueryContext(r.Context(), `
				SELECT id FROM stock_doc WHERE outlet_id = $1 AND kind IN ('purchase', 'wastage', 'count')
				ORDER BY business_date DESC, at DESC, id DESC LIMIT 100`, outlet)
			if err != nil {
				return nil, err
			}
			var docIDs []uuid.UUID
			for rows.Next() {
				var id uuid.UUID
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return nil, err
				}
				docIDs = append(docIDs, id)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
			docs := []*Doc{}
			for _, id := range docIDs {
				doc, err := docSummary(r.Context(), tx, id)
				if err != nil {
					return nil, err
				}
				docs = append(docs, doc)
			}
			return map[string]any{"documents": docs}, nil
		})
	}
}

func postWastage(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID          uuid.UUID `json:"id"`
			StockItemID uuid.UUID `json:"stock_item_id"`
			Qty         int64     `json:"qty"`
			Reason      string    `json:"reason"`
			Date        string    `json:"date"`
		}
		if err := httpx.Decode(w, r, 4<<10, &in); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		reason, err := text("reason", in.Reason, 1, 200)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if in.Qty < 1 || in.Qty > maxQty {
			httpx.Fail(w, r, bad("bad_qty", "qty must be above zero"))
			return
		}
		date, err := day(in.Date, time.Now())
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		p := auth.Must(r.Context())
		docTx(w, r, d, in.ID, "wastage", in, func(tx *sql.Tx, outlet uuid.UUID, now time.Time, hash []byte) (any, error) {
			b, err := openBook(r.Context(), tx, p.TenantID, outlet, in.ID, date, now, entryDate(date), false, []uuid.UUID{in.StockItemID})
			if err != nil {
				return nil, err
			}
			// Only stock the books hold can be thrown away. Wastage of stock
			// never booked in would be valued at a guess and, when the
			// delivery is booked, its true-up would land in food cost.
			if have := b.bal[in.StockItemID].Qty; in.Qty > have {
				return nil, httpx.E(http.StatusConflict, "not_enough_stock", fmt.Sprintf(
					"the books hold only %s of %s; receive the delivery first, or count the stock", qtyText(have, b.info[in.StockItemID].Unit),
					b.info[in.StockItemID].Name))
			}
			v := b.out(in.StockItemID, "wastage", in.Qty)
			e, err := ledger.StockOutEntry(p.TenantID, b.legal, outlet, in.ID, "wastage", v, b.entryDate, date,
				"Wastage: "+b.info[in.StockItemID].Name+": "+reason)
			if err != nil {
				return nil, err
			}
			by := p.SubjectID
			if err := b.finish(header{Kind: "wastage", Total: v, Note: reason, CreatedBy: &by, RequestHash: hash}, &e); err != nil {
				return nil, err
			}
			return docSummary(r.Context(), tx, in.ID)
		})
	}
}

// CountLine is one item of a stock count, as booked.
type CountLine struct {
	StockItemID uuid.UUID `json:"stock_item_id"`
	Name        string    `json:"name"`
	Before      int64     `json:"before"`  // what the books said
	Counted     int64     `json:"counted"` // what was there
	Value       int64     `json:"value"`   // paise: + missing (a cost), - found
}

func postCount(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID    uuid.UUID `json:"id"`
			Note  string    `json:"note"`
			Date  string    `json:"date"`
			Lines []struct {
				StockItemID uuid.UUID `json:"stock_item_id"`
				Counted     int64     `json:"counted"`
				UnitCost    *int64    `json:"unit_cost,omitempty"` // paise per kg, litre or piece: only for an item with no cost yet
			} `json:"lines"`
		}
		if err := httpx.Decode(w, r, 64<<10, &in); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		note, err := text("note", in.Note, 0, 200)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if err := checkLines(len(in.Lines), func(i int) (uuid.UUID, error) {
			if c := in.Lines[i].Counted; c < 0 || c > maxQty {
				return uuid.Nil, bad("bad_line", fmt.Sprintf("line %d: counted must be zero or more", i+1))
			}
			if uc := in.Lines[i].UnitCost; uc != nil && (*uc < 0 || *uc > maxCost) {
				return uuid.Nil, bad("bad_line", fmt.Sprintf("line %d: unit_cost is out of range", i+1))
			}
			return in.Lines[i].StockItemID, nil
		}); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		// A count compares the shelves with the books as they are NOW, so it
		// is always today's. (A count entered a day late would compare that
		// night's shelves with books that later bills have already changed.)
		now := time.Now()
		date := dayclose.TradingDay(now)
		if in.Date != "" && in.Date != date.Format(time.DateOnly) {
			httpx.Fail(w, r, bad("count_is_today",
				"a count is compared with the books as they are now, so it is dated today; count again and enter it now"))
			return
		}
		p := auth.Must(r.Context())
		docTx(w, r, d, in.ID, "count", in, func(tx *sql.Tx, outlet uuid.UUID, now time.Time, hash []byte) (any, error) {
			items := make([]uuid.UUID, len(in.Lines))
			for i, l := range in.Lines {
				items[i] = l.StockItemID
			}
			b, err := openBook(r.Context(), tx, p.TenantID, outlet, in.ID, date, now, entryDate(date), false, items)
			if err != nil {
				return nil, err
			}
			var net int64
			lines := make([]CountLine, len(in.Lines))
			for i, l := range in.Lines {
				before := b.bal[l.StockItemID].Qty
				cl := CountLine{StockItemID: l.StockItemID, Name: b.info[l.StockItemID].Name, Before: before, Counted: l.Counted}
				switch diff := l.Counted - before; {
				case diff < 0:
					cl.Value = b.out(l.StockItemID, "count", -diff)
				case diff > 0:
					// Stock found with no known cost (an opening count) needs a
					// price, or it would enter the books at zero and dilute
					// every average after it.
					bal := b.bal[l.StockItemID]
					if bal.UnitCost() <= 0 && bal.Qty >= 0 {
						if l.UnitCost == nil || *l.UnitCost <= 0 {
							return nil, bad("needs_cost", fmt.Sprintf(
								"%s has no cost yet: give its cost per %s for the stock you found", cl.Name, unitWord(b.info[l.StockItemID].Unit)))
						}
						bal.LastCost = *l.UnitCost
					}
					cl.Value = -b.found(l.StockItemID, diff)
				}
				net += cl.Value
				lines[i] = cl
			}
			memo := "Stock count"
			if note != "" {
				memo += ": " + note
			}
			e, err := ledger.StockOutEntry(p.TenantID, b.legal, outlet, in.ID, "count", net, b.entryDate, date, memo)
			if err != nil {
				return nil, err
			}
			by := p.SubjectID
			if err := b.finish(header{Kind: "count", Total: net, Note: note, CreatedBy: &by, RequestHash: hash}, &e); err != nil {
				return nil, err
			}
			doc, err := docSummary(r.Context(), tx, in.ID)
			return map[string]any{"document": doc, "lines": lines}, err
		})
	}
}

// ---- recipes and food cost ---------------------------------------------------

// RecipeLine is one ingredient of a dish, costed at this outlet.
type RecipeLine struct {
	StockItemID uuid.UUID `json:"stock_item_id"`
	Name        string    `json:"name"`
	Unit        string    `json:"unit"`
	Qty         int64     `json:"qty"`       // milli-units per portion
	UnitCost    int64     `json:"unit_cost"` // paise per kg, litre or piece here
	Cost        int64     `json:"cost"`      // paise per portion
}

// Dish is a menu item with its recipe and what it earns per portion.
type Dish struct {
	ID         uuid.UUID    `json:"id"`
	Name       string       `json:"name"`
	Category   string       `json:"category"`
	Price      int64        `json:"price"`     // as on the menu
	NetPrice   int64        `json:"net_price"` // without GST (prices that include GST are taken apart)
	Lines      []RecipeLine `json:"lines"`
	Cost       int64        `json:"cost"`         // paise per portion
	FoodCostBP int64        `json:"food_cost_bp"` // cost / net price, in basis points
	Margin     int64        `json:"margin"`       // net price - cost
	Uncosted   int          `json:"uncosted"`     // ingredients with no cost yet (never bought here)
}

func recipes(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		readTx(w, r, d, func(tx *sql.Tx, outlet uuid.UUID) (any, error) {
			dishes, err := loadDishes(r.Context(), tx, outlet)
			if err != nil {
				return nil, err
			}
			out := make([]Dish, 0, len(dishes))
			for _, id := range dishOrder(dishes) {
				out = append(out, *dishes[id])
			}
			addons, err := loadAddons(r.Context(), tx, outlet)
			if err != nil {
				return nil, err
			}
			return map[string]any{"dishes": out, "addons": addons}, nil
		})
	}
}

func dishOrder(m map[uuid.UUID]*Dish) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := m[ids[i]], m[ids[j]]
		if a.Category != b.Category {
			return a.Category < b.Category
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return ids[i].String() < ids[j].String()
	})
	return ids
}

func loadDishes(ctx context.Context, tx *sql.Tx, outlet uuid.UUID) (map[uuid.UUID]*Dish, error) {
	var priceMode string
	if err := tx.QueryRowContext(ctx, `SELECT price_mode FROM outlet WHERE id = $1`, outlet).Scan(&priceMode); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT m.id, m.name, c.name, m.price_paise, t.rate_bp
		FROM menu_item m
		JOIN menu_category c ON c.id = m.category_id AND c.tenant_id = m.tenant_id
		JOIN tax_profile t ON t.id = m.tax_profile_id AND t.tenant_id = m.tenant_id`)
	if err != nil {
		return nil, err
	}
	dishes := map[uuid.UUID]*Dish{}
	for rows.Next() {
		var dish Dish
		var rate int64
		if err := rows.Scan(&dish.ID, &dish.Name, &dish.Category, &dish.Price, &rate); err != nil {
			rows.Close()
			return nil, err
		}
		dish.NetPrice = dish.Price
		if priceMode == "inclusive" {
			dish.NetPrice = mulDiv(dish.Price, 10_000, 10_000+rate)
		}
		dish.Lines = []RecipeLine{}
		dishes[dish.ID] = &dish
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `
		SELECT r.menu_item_id, r.stock_item_id, i.name, i.unit, r.qty,
		       coalesce(b.qty, 0), coalesce(b.value, 0), coalesce(b.last_cost, 0)
		FROM recipe_line r
		JOIN stock_item i ON i.id = r.stock_item_id AND i.tenant_id = r.tenant_id
		LEFT JOIN stock_balance b ON b.stock_item_id = r.stock_item_id AND b.outlet_id = $1
		WHERE r.valid_to IS NULL
		ORDER BY lower(i.name), r.stock_item_id`, outlet)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var dishID uuid.UUID
		var l RecipeLine
		var b Balance
		if err := rows.Scan(&dishID, &l.StockItemID, &l.Name, &l.Unit, &l.Qty, &b.Qty, &b.Value, &b.LastCost); err != nil {
			return nil, err
		}
		dish := dishes[dishID]
		if dish == nil {
			continue
		}
		l.UnitCost = b.UnitCost()
		l.Cost = mulDiv(l.UnitCost, l.Qty, 1000)
		if l.UnitCost <= 0 {
			dish.Uncosted++
		}
		dish.Lines = append(dish.Lines, l)
		dish.Cost += l.Cost
	}
	for _, dish := range dishes {
		dish.Margin = dish.NetPrice - dish.Cost
		if dish.NetPrice > 0 {
			dish.FoodCostBP = mulDiv(dish.Cost, 10_000, dish.NetPrice)
		}
	}
	return dishes, rows.Err()
}

// Addon is an add-on option ("Extra cheese") with its recipe and what it
// costs per portion here. Its price is added to the dish's.
type Addon struct {
	ID         uuid.UUID    `json:"id"`
	Name       string       `json:"name"`
	Group      string       `json:"group"`
	Price      int64        `json:"price"`        // as on the menu, added to the dish's price
	NetPrice   int64        `json:"net_price"`    // without GST, when the dishes offering it share one rate
	Lines      []RecipeLine `json:"lines"`        // current recipe
	Cost       int64        `json:"cost"`         // paise per portion
	FoodCostBP int64        `json:"food_cost_bp"` // cost / net price; 0 when it has no price
	Uncosted   int          `json:"uncosted"`
}

// loadAddons lists every add-on option with its recipe, costed here.
func loadAddons(ctx context.Context, tx *sql.Tx, outlet uuid.UUID) ([]Addon, error) {
	var priceMode string
	if err := tx.QueryRowContext(ctx, `SELECT price_mode FROM outlet WHERE id = $1`, outlet).Scan(&priceMode); err != nil {
		return nil, err
	}
	// An add-on is taxed with the dish it is on. Prices that include GST are
	// taken apart only when every dish offering the add-on has one rate.
	rows, err := tx.QueryContext(ctx, `
		SELECT o.id, o.name, g.name, o.price_paise,
		       (SELECT CASE WHEN count(DISTINCT t.rate_bp) = 1 THEN min(t.rate_bp) END
		        FROM menu_item_modifier_group mg
		        JOIN menu_item m ON m.id = mg.item_id AND m.tenant_id = mg.tenant_id
		        JOIN tax_profile t ON t.id = m.tax_profile_id AND t.tenant_id = m.tenant_id
		        WHERE mg.group_id = g.id)
		FROM modifier_option o JOIN modifier_group g ON g.id = o.group_id AND g.tenant_id = o.tenant_id
		ORDER BY lower(g.name), o.sort, lower(o.name), o.id`)
	if err != nil {
		return nil, err
	}
	out := []Addon{}
	at := map[uuid.UUID]int{}
	for rows.Next() {
		var a Addon
		var rate sql.NullInt64
		if err := rows.Scan(&a.ID, &a.Name, &a.Group, &a.Price, &rate); err != nil {
			rows.Close()
			return nil, err
		}
		a.NetPrice = a.Price
		if priceMode == "inclusive" {
			a.NetPrice = 0
			if rate.Valid {
				a.NetPrice = mulDiv(a.Price, 10_000, 10_000+rate.Int64)
			}
		}
		a.Lines = []RecipeLine{}
		at[a.ID] = len(out)
		out = append(out, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `
		SELECT r.modifier_option_id, r.stock_item_id, i.name, i.unit, r.qty,
		       coalesce(b.qty, 0), coalesce(b.value, 0), coalesce(b.last_cost, 0)
		FROM addon_recipe_line r
		JOIN stock_item i ON i.id = r.stock_item_id AND i.tenant_id = r.tenant_id
		LEFT JOIN stock_balance b ON b.stock_item_id = r.stock_item_id AND b.outlet_id = $1
		WHERE r.valid_to IS NULL
		ORDER BY lower(i.name), r.stock_item_id`, outlet)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var option uuid.UUID
		var l RecipeLine
		var b Balance
		if err := rows.Scan(&option, &l.StockItemID, &l.Name, &l.Unit, &l.Qty, &b.Qty, &b.Value, &b.LastCost); err != nil {
			return nil, err
		}
		k, ok := at[option]
		if !ok {
			continue
		}
		a := &out[k]
		l.UnitCost = b.UnitCost()
		l.Cost = mulDiv(l.UnitCost, l.Qty, 1000)
		if l.UnitCost <= 0 {
			a.Uncosted++
		}
		a.Lines = append(a.Lines, l)
		a.Cost += l.Cost
	}
	for k := range out {
		if out[k].NetPrice > 0 {
			out[k].FoodCostBP = mulDiv(out[k].Cost, 10_000, out[k].NetPrice)
		}
	}
	return out, rows.Err()
}

// FoodCost is what the kitchen cost over some trading days, against sales.
type FoodCost struct {
	From             string     `json:"from"`
	To               string     `json:"to"`
	FoodSales        int64      `json:"food_sales"`        // goods sold, after discounts, without GST or service charge
	FoodCost         int64      `json:"food_cost"`         // ingredients the bills used (and true-ups)
	FoodCostBP       int64      `json:"food_cost_bp"`      // food cost / food sales
	Wastage          int64      `json:"wastage"`           // thrown away
	CountDiff        int64      `json:"count_differences"` // missing at counts (negative: found)
	TotalCost        int64      `json:"total_cost"`        // all three
	TotalCostBP      int64      `json:"total_cost_bp"`
	Purchases        int64      `json:"purchases"`
	PortionsNoRecipe int64      `json:"portions_without_recipe"` // sold dishes that use no stock: their cost is not counted
	Dishes           []DishSold `json:"dishes"`
}

// DishSold is one dish sold over the period.
type DishSold struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Qty       int64     `json:"qty"`
	HasRecipe bool      `json:"has_recipe"`
	Cost      int64     `json:"cost"` // at today's recipe cost: qty x cost per portion
}

const maxReportDays = 93

func foodCost(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		to, err := day(r.URL.Query().Get("to"), now)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		from := to
		if s := r.URL.Query().Get("from"); s != "" {
			if from, err = day(s, now); err != nil {
				httpx.Fail(w, r, err)
				return
			}
		}
		if from.After(to) || to.Sub(from) > maxReportDays*24*time.Hour {
			httpx.Fail(w, r, bad("bad_range", fmt.Sprintf("from must be on or before to, at most %d days apart", maxReportDays)))
			return
		}
		f, t := from.Format(time.DateOnly), to.Format(time.DateOnly)
		readTx(w, r, d, func(tx *sql.Tx, outlet uuid.UUID) (any, error) {
			res := FoodCost{From: f, To: t, Dishes: []DishSold{}}
			if err := tx.QueryRowContext(r.Context(), `
				SELECT coalesce(sum(taxable_total - service_charge), 0)
				FROM bill WHERE outlet_id = $1 AND business_date BETWEEN $2 AND $3`, outlet, f, t).Scan(&res.FoodSales); err != nil {
				return nil, err
			}
			if err := tx.QueryRowContext(r.Context(), `
				SELECT coalesce(-sum(value) FILTER (WHERE kind IN ('sale', 'trueup')), 0),
				       coalesce(-sum(value) FILTER (WHERE kind = 'wastage'), 0),
				       coalesce(-sum(value) FILTER (WHERE kind = 'count'), 0),
				       coalesce(sum(value) FILTER (WHERE kind = 'purchase'), 0)
				FROM stock_move WHERE outlet_id = $1 AND business_date BETWEEN $2 AND $3`, outlet, f, t).
				Scan(&res.FoodCost, &res.Wastage, &res.CountDiff, &res.Purchases); err != nil {
				return nil, err
			}
			res.TotalCost = res.FoodCost + res.Wastage + res.CountDiff
			if res.FoodSales > 0 {
				res.FoodCostBP = mulDiv(res.FoodCost, 10_000, res.FoodSales)
				res.TotalCostBP = mulDiv(res.TotalCost, 10_000, res.FoodSales)
			}

			dishes, err := loadDishes(r.Context(), tx, outlet)
			if err != nil {
				return nil, err
			}
			rows, err := tx.QueryContext(r.Context(), `
				SELECT l.item_ref, sum(l.qty)
				FROM bill_line l JOIN bill b ON b.id = l.bill_id AND b.tenant_id = l.tenant_id
				WHERE b.outlet_id = $1 AND b.business_date BETWEEN $2 AND $3
				GROUP BY l.item_ref`, outlet, f, t)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			sold := map[uuid.UUID]int64{}
			for rows.Next() {
				var ref sql.NullString
				var qty int64
				if err := rows.Scan(&ref, &qty); err != nil {
					return nil, err
				}
				id, err := uuid.Parse(ref.String)
				if err != nil || dishes[id] == nil {
					res.PortionsNoRecipe += qty // open items typed at the till
					continue
				}
				sold[id] += qty
			}
			if err := rows.Err(); err != nil {
				return nil, err
			}
			for _, id := range dishOrder(dishes) {
				qty := sold[id]
				if qty == 0 {
					continue
				}
				dish := dishes[id]
				ds := DishSold{ID: id, Name: dish.Name, Qty: qty, HasRecipe: len(dish.Lines) > 0, Cost: qty * dish.Cost}
				if !ds.HasRecipe {
					res.PortionsNoRecipe += qty
				}
				res.Dishes = append(res.Dishes, ds)
			}
			sort.SliceStable(res.Dishes, func(i, j int) bool { return res.Dishes[i].Cost > res.Dishes[j].Cost })
			return res, nil
		})
	}
}
