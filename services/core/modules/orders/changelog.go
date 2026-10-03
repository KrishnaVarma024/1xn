package orders

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// KeepChanges is how long the change log keeps a change. A device offline
// longer than this cannot catch up by pulling changes; it gets 410 and
// bootstraps again, which costs one bigger download and loses nothing.
const KeepChanges = 7 * 24 * time.Hour

// pruneBatch bounds one prune, so it never holds locks for long.
const pruneBatch = 10_000

// PruneChangeLog deletes a tenant's changes older than `before` (at most
// pruneBatch of them) and raises the tenant's change-log floor to the highest
// seq deleted. Pulls from below the floor get 410.
//
// Seqs are handed out in commit order but created_at is the transaction's
// start, so the deleted rows are not always a prefix of the log. Taking the
// floor as the HIGHEST deleted seq keeps it safe: every cursor at or above
// the floor still finds every change after it.
func PruneChangeLog(ctx context.Context, tx *sql.Tx, tenantID uuid.UUID, before time.Time) (deleted int64, err error) {
	var top int64
	if err := tx.QueryRowContext(ctx, `
		WITH gone AS (
		  DELETE FROM change_log WHERE ctid IN (
		    SELECT ctid FROM change_log WHERE tenant_id = $1 AND created_at < $2 LIMIT $3)
		  RETURNING seq)
		SELECT count(*), coalesce(max(seq), 0) FROM gone`, tenantID, before, pruneBatch).Scan(&deleted, &top); err != nil {
		return 0, err
	}
	if deleted == 0 {
		return 0, nil
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE sync_state SET change_log_floor = greatest(change_log_floor, $2) WHERE tenant_id = $1`, tenantID, top)
	return deleted, err
}
