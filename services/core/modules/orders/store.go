package orders

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

var (
	// errNoOrder means the order id is not known in this tenant.
	errNoOrder = errors.New("no such order")
	// errMergeChain means following merge tombstones did not reach a live
	// order. Merges keep chains one hop long (see flatten), so this only
	// happens if data was edited by hand; it is permanent, never retried.
	errMergeChain = errors.New("merge chain too long")
	// errBadDoc means an order could not be encoded (a timestamp outside
	// years 0 to 9999, say). Also permanent.
	errBadDoc = errors.New("order cannot be encoded")
)

// maxHops bounds how far resolve follows tombstones.
const maxHops = 16

// lock loads an order and row-locks it until the transaction ends.
//
// Lock order matters to avoid deadlocks: every writer takes the device row,
// then the tenant's sync_state row (NextSeq), and only then order rows. With
// sync_state held, writes within a tenant are serialised, so two merges can
// never lock the same orders in opposite orders.
func lock(ctx context.Context, tx *sql.Tx, id uuid.UUID) (*Order, error) {
	var doc []byte
	err := tx.QueryRowContext(ctx, `SELECT doc FROM pos_order WHERE id = $1 FOR UPDATE`, id).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoOrder
	}
	if err != nil {
		return nil, err
	}
	var o Order
	if err := json.Unmarshal(doc, &o); err != nil {
		return nil, fmt.Errorf("order %s: corrupt doc: %w", id, err)
	}
	return &o, nil
}

// resolve follows merge tombstones to the order that now holds the lines, so
// an op aimed at a merged-away order lands where the lines went.
func resolve(ctx context.Context, tx *sql.Tx, id uuid.UUID) (*Order, error) {
	start := id
	for hop := 0; hop < maxHops; hop++ {
		o, err := lock(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if o.Status != Merged || o.MergedInto == nil {
			return o, nil
		}
		id = *o.MergedInto
	}
	return nil, fmt.Errorf("%w: order %s", errMergeChain, start)
}

// flatten re-points every tombstone that pointed at one of the sources to
// the merge target, so forwarding is always one hop, however many times a
// party is merged along. Without it, merging 1 into 2, 2 into 3, and so on
// would build a chain that grows with every merge.
func flatten(ctx context.Context, tx *sql.Tx, tenant uuid.UUID, sources []*Order, into uuid.UUID, seq int64) error {
	ids := make([]string, len(sources))
	for i, s := range sources {
		ids[i] = s.ID.String()
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM pos_order WHERE merged_into = ANY($1::uuid[]) ORDER BY id`, pq.Array(ids))
	if err != nil {
		return err
	}
	var old []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		old = append(old, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range old {
		o, err := lock(ctx, tx, id)
		if err != nil {
			return err
		}
		target := into
		o.MergedInto = &target
		if err := save(ctx, tx, tenant, o, seq); err != nil {
			return err
		}
	}
	return nil
}

// claimLines records line ids as used, and returns the ones that were not
// used before. A line id names one dish forever, so an id seen before means
// the op was re-sent, wherever that line has gone since.
func claimLines(ctx context.Context, tx *sql.Tx, tenant, orderID uuid.UUID, lineIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	ids := make([]string, len(lineIDs))
	for i, id := range lineIDs {
		ids[i] = id.String()
	}
	rows, err := tx.QueryContext(ctx, `
		INSERT INTO order_line_key (tenant_id, line_id, order_id)
		SELECT $1, l, $2 FROM unnest($3::uuid[]) AS l
		ON CONFLICT DO NOTHING
		RETURNING line_id`, tenant, orderID, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	fresh := map[uuid.UUID]bool{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		fresh[id] = true
	}
	return fresh, rows.Err()
}

// openSpillover finds the open spillover of a root order, if any.
func openSpillover(ctx context.Context, tx *sql.Tx, root uuid.UUID) (*Order, error) {
	var id uuid.UUID
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM pos_order WHERE spillover_of = $1 AND status = 'open'
		ORDER BY opened_at, id LIMIT 1`, root).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return lock(ctx, tx, id)
}

// target finds the order an op should act on: follow merges; if that order
// is already closed and its family has an open spillover holding the lines
// in question (or any open spillover when no lines are named), act on the
// spillover instead.
func target(ctx context.Context, tx *sql.Tx, id uuid.UUID, lineIDs []uuid.UUID) (*Order, error) {
	o, err := resolve(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if o.Status == Open {
		return o, nil
	}
	sp, err := openSpillover(ctx, tx, o.Root())
	if err != nil || sp == nil {
		return o, err
	}
	if len(lineIDs) == 0 {
		return sp, nil
	}
	for _, l := range lineIDs {
		if sp.line(l) != nil {
			return sp, nil
		}
	}
	return o, nil
}

func docOf(o *Order) (string, error) {
	b, err := json.Marshal(o)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %v", errBadDoc, o.ID, err)
	}
	return string(b), nil // text, because lib/pq sends []byte as bytea
}

// insert stores a new order. It returns false if the id already exists.
func insert(ctx context.Context, tx *sql.Tx, tenant uuid.UUID, o *Order, seq int64) (bool, error) {
	o.Version, o.LastSeq = 1, seq
	doc, err := docOf(o)
	if err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO pos_order (id, tenant_id, outlet_id, table_id, channel, status, merged_into, spillover_of,
		                       opened_at, closed_at, version, last_seq, doc)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (tenant_id, id) DO NOTHING`,
		o.ID, tenant, o.OutletID, o.TableID, o.Channel, o.Status, o.MergedInto, o.SpilloverOf,
		o.OpenedAt, o.ClosedAt, o.Version, o.LastSeq, doc)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// save writes a changed order back and records the change for pull.
func save(ctx context.Context, tx *sql.Tx, tenant uuid.UUID, o *Order, seq int64) error {
	o.Version++
	o.LastSeq = seq
	doc, err := docOf(o)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE pos_order SET table_id = $2, status = $3, merged_into = $4, closed_at = $5,
		       version = $6, last_seq = $7, doc = $8
		WHERE id = $1`,
		o.ID, o.TableID, o.Status, o.MergedInto, o.ClosedAt, o.Version, o.LastSeq, doc); err != nil {
		return err
	}
	return logChange(ctx, tx, tenant, seq, o)
}

func logChange(ctx context.Context, tx *sql.Tx, tenant uuid.UUID, seq int64, o *Order) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO change_log (tenant_id, seq, outlet_id, entity, entity_id) VALUES ($1, $2, $3, 'order', $4)
		ON CONFLICT DO NOTHING`, tenant, seq, o.OutletID, o.ID)
	return err
}
