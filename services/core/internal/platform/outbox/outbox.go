// Package outbox writes domain events in the same transaction as the change
// they describe (the transactional outbox pattern).
//
// A relay worker (next slice) reads unpublished rows in order, publishes them
// to NATS JetStream and stamps published_at. Delivery is therefore
// at-least-once, so every consumer must be idempotent (dedupe on the event
// id).
package outbox

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/google/uuid"

	"1xn/core/internal/platform/ids"
)

// Add queues an event. topic is versioned ("sales.bill.settled.v1") so the
// payload shape can evolve without breaking old consumers; key is the
// ordering key (events with the same key are delivered in order).
func Add(ctx context.Context, tx *sql.Tx, tenantID uuid.UUID, topic, key string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO outbox (id, tenant_id, topic, key, payload) VALUES ($1, $2, $3, $4, $5)`,
		ids.New(), tenantID, topic, key, string(b))
	return err
}
