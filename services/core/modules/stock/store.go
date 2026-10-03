package stock

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"1xn/core/internal/platform/alerts"
	"1xn/core/internal/platform/gst"
	"1xn/core/internal/platform/ids"
	"1xn/core/modules/ledger"
)

// ErrUnknownItem: a document names a stock item the tenant does not have.
var ErrUnknownItem = errors.New("stock: unknown stock item")

// ErrTooLarge: a quantity or price so large that a value does not fit in
// 64 bits. Nothing is kept.
var ErrTooLarge = errors.New("stock: figures too large")

type itemInfo struct {
	Name    string
	Unit    string
	Reorder int64
	Active  bool
}

type move struct {
	item  uuid.UUID
	kind  string
	qty   int64
	value int64
	after int64
}

// book is one stock document being written: the balances it touches,
// locked, and the moves it makes. Everything is written at finish, in one
// go, inside the caller's transaction.
type book struct {
	ctx                          context.Context
	tx                           *sql.Tx
	tenant, outlet, legal, docID uuid.UUID
	businessDate, at, entryDate  time.Time
	bal                          map[uuid.UUID]*Balance
	before                       map[uuid.UUID]int64
	info                         map[uuid.UUID]itemInfo
	moves                        []move
}

// openBook locks the balances of these items at an outlet, creating the
// rows that do not exist yet.
//
// Locks are always taken in one order, so two transactions can never wait on
// each other in a circle: first the accounting period of the entry (every
// ledger posting locks it), then the balances, in item-id order.
//
// The entry is dated entryDate. An owner document dated in a locked month is
// refused (ledger.ErrPeriodLocked): the owner picks the date. A bill cannot
// be refused, so with lateOK its stock entry goes to the first open month.
func openBook(ctx context.Context, tx *sql.Tx, tenant, outlet, docID uuid.UUID, businessDate, at, entryDate time.Time,
	lateOK bool, items []uuid.UUID) (*book, error) {
	ids := make([]string, 0, len(items))
	seen := map[uuid.UUID]bool{}
	for _, id := range items {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id.String())
		}
	}
	sort.Strings(ids)
	b := &book{ctx: ctx, tx: tx, tenant: tenant, outlet: outlet, docID: docID, businessDate: businessDate, at: at,
		bal: map[uuid.UUID]*Balance{}, before: map[uuid.UUID]int64{}, info: map[uuid.UUID]itemInfo{}}
	if err := tx.QueryRowContext(ctx, `SELECT legal_entity_id FROM outlet WHERE id = $1`, outlet).Scan(&b.legal); err != nil {
		return nil, err
	}
	b.entryDate = entryDate
	if lateOK {
		d, err := ledger.FirstOpenDate(ctx, tx, tenant, b.legal, entryDate)
		if err != nil {
			return nil, err
		}
		b.entryDate = d
	} else if err := ledger.EnsureOpen(ctx, tx, tenant, b.legal, entryDate); err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id, name, unit, reorder_level, active FROM stock_item WHERE id = ANY($1::uuid[])`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uuid.UUID
		var in itemInfo
		if err := rows.Scan(&id, &in.Name, &in.Unit, &in.Reorder, &in.Active); err != nil {
			rows.Close()
			return nil, err
		}
		b.info[id] = in
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(b.info) != len(ids) {
		return nil, ErrUnknownItem
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO stock_balance (tenant_id, outlet_id, stock_item_id)
		SELECT $1, $2, unnest($3::uuid[]) ORDER BY 3
		ON CONFLICT DO NOTHING`, tenant, outlet, pq.Array(ids)); err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `
		SELECT stock_item_id, qty, value, last_cost FROM stock_balance
		WHERE outlet_id = $1 AND stock_item_id = ANY($2::uuid[])
		ORDER BY stock_item_id FOR UPDATE`, outlet, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var bal Balance
		if err := rows.Scan(&id, &bal.Qty, &bal.Value, &bal.LastCost); err != nil {
			return nil, err
		}
		b.bal[id] = &bal
		b.before[id] = bal.Qty
	}
	return b, rows.Err()
}

func (b *book) record(item uuid.UUID, kind string, qty, value int64) {
	b.moves = append(b.moves, move{item: item, kind: kind, qty: qty, value: value, after: b.bal[item].Qty})
}

// out takes stock out (used, wasted, missing) and returns its value.
func (b *book) out(item uuid.UUID, kind string, qty int64) int64 {
	v := b.bal[item].Out(qty)
	b.record(item, kind, -qty, -v)
	return v
}

// in books a purchase line and returns its true-up.
func (b *book) in(item uuid.UUID, qty, cost int64) int64 {
	up := b.bal[item].In(qty, cost)
	b.record(item, "purchase", qty, cost)
	if up != 0 {
		b.moves = append(b.moves, move{item: item, kind: "trueup", value: -up, after: b.bal[item].Qty})
	}
	return up
}

// found books stock a count found beyond the books and returns its value.
func (b *book) found(item uuid.UUID, qty int64) int64 {
	v := b.bal[item].Found(qty)
	b.record(item, "count", qty, v)
	return v
}

// header is the stock_doc row.
type header struct {
	Kind, Supplier, InvoiceNo, PaidBy, Note string
	BillID                                  *uuid.UUID
	Total                                   int64
	CreatedBy                               *uuid.UUID
	RequestHash                             []byte
}

// finish posts the ledger entry (if any), writes the document, its moves and
// the new balances, and raises low and negative stock alerts.
func (b *book) finish(h header, entry *ledger.Entry) error {
	if b.overflowed() {
		return ErrTooLarge
	}
	status, entryID := "none", (*uuid.UUID)(nil)
	if entry != nil {
		// The period was opened (and locked FOR SHARE) by openBook, so a
		// locked month cannot happen here: stock moves always come with
		// their entry, and the books always equal the stock.
		id, err := ledger.Post(b.ctx, b.tx, *entry)
		switch {
		case err == nil:
			status, entryID = "posted", &id
		case errors.Is(err, ledger.ErrNothingToPost):
		default:
			return err
		}
	}
	if _, err := b.tx.ExecContext(b.ctx, `
		INSERT INTO stock_doc (id, tenant_id, outlet_id, kind, bill_id, business_date, at, supplier, invoice_no, paid_by,
		                       total, note, ledger_status, journal_entry_id, created_by, request_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, nullif($8, ''), nullif($9, ''), nullif($10, ''), $11, nullif($12, ''), $13, $14, $15, $16)`,
		b.docID, b.tenant, b.outlet, h.Kind, h.BillID, b.businessDate.Format(time.DateOnly), b.at, h.Supplier, h.InvoiceNo,
		h.PaidBy, h.Total, h.Note, status, entryID, h.CreatedBy, h.RequestHash); err != nil {
		return err
	}
	for _, m := range b.moves {
		if _, err := b.tx.ExecContext(b.ctx, `
			INSERT INTO stock_move (tenant_id, doc_id, outlet_id, stock_item_id, kind, qty, value, qty_after, business_date, at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			b.tenant, b.docID, b.outlet, m.item, m.kind, m.qty, m.value, m.after, b.businessDate.Format(time.DateOnly), b.at); err != nil {
			return err
		}
	}
	items := make([]uuid.UUID, 0, len(b.bal))
	for id := range b.bal {
		items = append(items, id)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].String() < items[j].String() })
	for _, id := range items {
		bal := b.bal[id]
		if _, err := b.tx.ExecContext(b.ctx, `
			UPDATE stock_balance SET qty = $3, value = $4, last_cost = $5 WHERE outlet_id = $1 AND stock_item_id = $2`,
			b.outlet, id, bal.Qty, bal.Value, bal.LastCost); err != nil {
			return err
		}
		if err := b.alert(id); err != nil {
			return err
		}
	}
	return nil
}

func (b *book) overflowed() bool {
	for _, bal := range b.bal {
		if bal.Overflowed() {
			return true
		}
	}
	return false
}

// alert raises an alert when an item crosses below zero or below its reorder
// level: once per crossing, not on every bill while it stays low.
func (b *book) alert(id uuid.UUID) error {
	before, after, in := b.before[id], b.bal[id].Qty, b.info[id]
	details := map[string]any{"item": in.Name, "unit": in.Unit, "qty": after, "reorder_level": in.Reorder, "outlet_id": b.outlet}
	switch {
	case before >= 0 && after < 0:
		return alerts.Raise(b.ctx, b.tx, b.tenant, "negative_stock", "warning", &id, details)
	case in.Reorder > 0 && before >= in.Reorder && after < in.Reorder:
		return alerts.Raise(b.ctx, b.tx, b.tenant, "low_stock", "warning", &id, details)
	}
	return nil
}

// Sold is one bill line: the menu item (the line's ref) and how many.
type Sold struct {
	ItemRef string
	Qty     int64
}

// ConsumeForBill uses up the ingredients of a bill's dishes at its outlet,
// and books their cost as cost of food sold. It uses the recipes in force
// when the bill was ISSUED, so a till that syncs late still uses what the
// kitchen cooked by. Dishes without a recipe (and open items typed at the
// till) use nothing.
//
// It never refuses: a bill was printed and paid. Selling more than the books
// hold drives stock negative and raises an alert. A bill for a month the
// accountant has locked books its stock entry in the first open month, so
// the books still equal the stock. Figures too large to hold (impossible
// from a real kitchen) leave stock untouched and raise an alert.
func ConsumeForBill(ctx context.Context, tx *sql.Tx, tenant, outlet, billID uuid.UUID, businessDate, issuedAt time.Time,
	sold []Sold) error {
	portions := map[uuid.UUID]int64{}
	var dishes []string
	for _, s := range sold {
		id, err := uuid.Parse(s.ItemRef)
		if err != nil || s.Qty <= 0 {
			continue
		}
		if portions[id] == 0 {
			dishes = append(dishes, id.String())
		}
		portions[id] += s.Qty
	}
	if len(dishes) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT menu_item_id, stock_item_id, qty FROM recipe_line
		WHERE menu_item_id = ANY($1::uuid[]) AND valid_from <= $2 AND (valid_to IS NULL OR valid_to > $2)`,
		pq.Array(dishes), issuedAt)
	if err != nil {
		return err
	}
	need := map[uuid.UUID]int64{}
	var items []uuid.UUID
	tooLarge := false
	for rows.Next() {
		var dish, item uuid.UUID
		var qty int64
		if err := rows.Scan(&dish, &item, &qty); err != nil {
			rows.Close()
			return err
		}
		if need[item] == 0 {
			items = append(items, item)
		}
		n, ok := mulDivOK(qty, portions[dish], 1)
		if !ok || need[item] > math.MaxInt64-n {
			tooLarge = true
		}
		need[item] += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}

	at := issuedAt.In(gst.IST)
	b, err := openBook(ctx, tx, tenant, outlet, ids.New(), businessDate, issuedAt, at, true, items)
	if err != nil {
		return err
	}
	var value int64
	for _, item := range items {
		value += b.out(item, "sale", need[item])
	}
	if tooLarge || b.overflowed() {
		return alerts.Raise(ctx, tx, tenant, "stock_not_used", "critical", &billID, map[string]any{
			"reason": "the quantities or values are too large to hold; check the recipes and the bill",
		})
	}
	memo := "Ingredients used by a bill"
	if b.entryDate.Format(time.DateOnly) != at.Format(time.DateOnly) {
		memo += fmt.Sprintf(" of %s, booked here because that month is locked", at.Format(time.DateOnly))
	}
	e, err := ledger.StockOutEntry(tenant, b.legal, outlet, b.docID, "sale", value, b.entryDate, businessDate, memo)
	if err != nil {
		return err
	}
	id := billID
	return b.finish(header{Kind: "sale", BillID: &id, Total: value}, &e)
}
