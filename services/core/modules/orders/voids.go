package orders

import (
	"context"
	"database/sql"

	"github.com/google/uuid"

	"1xn/core/internal/platform/syncop"
)

// logVoids writes each of these lines that is now voided to order_void, once.
//
// The order document is the truth about a line, but a line can move to
// another order later (a merge). The void log keeps each void where it
// happened in time, so the Z report counts it on exactly one trading day.
func logVoids(ctx context.Context, tx *sql.Tx, a syncop.ApplyContext, o *Order, lineIDs []uuid.UUID, cancelled bool) error {
	for _, id := range lineIDs {
		l := o.line(id)
		if l == nil || l.Status != Voided || l.Void == nil {
			continue
		}
		var approvedBy any
		if l.Void.ApprovedBy != "" {
			approvedBy = l.Void.ApprovedBy
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO order_void (tenant_id, line_id, order_id, outlet_id, table_id, name, qty, value, fired, cancelled,
			                        reason, approved_by, device_id, at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			ON CONFLICT DO NOTHING`,
			a.Device.TenantID, l.ID, o.ID, o.OutletID, o.TableID, l.Name, l.Qty, l.Value(), l.KOTID != nil, cancelled,
			l.Void.Reason, approvedBy, a.Device.ID, l.Void.At); err != nil {
			return err
		}
	}
	return nil
}
