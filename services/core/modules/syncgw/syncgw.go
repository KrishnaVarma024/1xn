// Package syncgw is the sync gateway: devices push the ops they recorded
// offline, and the gateway applies each one exactly once.
//
// Exactly-once is built from two at-least-once halves:
//   - the device keeps every op in its local op log and re-sends it until it
//     gets an ack, so nothing is lost;
//   - the gateway remembers every (device, op_id) it has seen in sync_inbox,
//     so a re-sent op is answered from memory instead of applied twice.
//
// Cross-module API: Routes (appliers are registered by the app wiring).
package syncgw

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"1xn/core/internal/platform/alerts"
	"1xn/core/internal/platform/auth"
	"1xn/core/internal/platform/db"
	"1xn/core/internal/platform/httpx"
	"1xn/core/internal/platform/ids"
	"1xn/core/internal/platform/syncop"
	"1xn/core/modules/devices"
)

// Limits per push. A device that has been offline for 72 hours sends its
// backlog in pages of MaxOps.
const (
	MaxOps       = 200
	MaxBodyBytes = 4 << 20
)

// Op is one change recorded on a device.
type Op struct {
	OpID      string          `json:"op_id"`      // UUIDv7, made when the op was recorded
	DeviceSeq int64           `json:"device_seq"` // 1, 2, 3... per device, no gaps
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"` // re-sent byte for byte on retry
}

// PushRequest is a page of ops, oldest first.
type PushRequest struct {
	Ops []Op `json:"ops"`
}

// Ack answers one op.
type Ack struct {
	OpID      string       `json:"op_id"`
	Status    string       `json:"status"` // applied, duplicate, rejected
	ServerSeq int64        `json:"server_seq,omitempty"`
	Error     *httpx.Error `json:"error,omitempty"`
}

// PushResponse lists acks in request order. Complete is false when the
// gateway stopped early (a transient error); the device re-sends the rest.
type PushResponse struct {
	Acks     []Ack        `json:"acks"`
	Complete bool         `json:"complete"`
	Error    *httpx.Error `json:"error,omitempty"`
}

// Gateway applies ops with the registered appliers.
type Gateway struct {
	DB       *sql.DB
	Appliers map[string]syncop.Applier
}

// Push applies ops one by one, each in its own transaction, stopping at the
// first transient error. Per-op transactions mean one slow or failing op never
// holds back the ones already applied, and a retry only resends the tail.
func (g *Gateway) Push(ctx context.Context, p auth.Principal, req PushRequest) (PushResponse, error) {
	res := PushResponse{Acks: []Ack{}}
	if len(req.Ops) > MaxOps {
		return res, httpx.E(http.StatusRequestEntityTooLarge, "too_many_ops", fmt.Sprintf("send at most %d ops per push", MaxOps))
	}
	for _, op := range req.Ops {
		ack, err := g.applyOne(ctx, p, op)
		if err != nil {
			return res, err
		}
		res.Acks = append(res.Acks, ack)
	}
	res.Complete = true
	return res, nil
}

func (g *Gateway) applyOne(ctx context.Context, p auth.Principal, op Op) (Ack, error) {
	ack := Ack{OpID: op.OpID}
	opID, err := ids.ParseV7(op.OpID)
	if err != nil {
		// Without a valid id we cannot even remember the op; reject it outright.
		ack.Status, ack.Error = "rejected", httpx.E(http.StatusBadRequest, "bad_op_id", err.Error())
		return ack, nil
	}
	if op.DeviceSeq < 1 || len(op.Payload) == 0 {
		ack.Status, ack.Error = "rejected", httpx.E(http.StatusBadRequest, "bad_op", "device_seq must be >= 1 and payload is required")
		return ack, nil
	}
	sum := sha256.Sum256(op.Payload)
	hash := sum[:]

	err = db.InTenantTx(ctx, g.DB, p.TenantID, func(tx *sql.Tx) error {
		ack = Ack{OpID: op.OpID} // reset on retry

		// Lock the device row: ops from one device apply strictly in order,
		// even if the app fires two pushes at once.
		dev, err := devices.Lock(ctx, tx, p.SubjectID)
		if err != nil {
			return err
		}
		if dev.Revoked {
			return httpx.ErrForbidden
		}

		// 1. Seen this op before? Answer from memory.
		var status string
		var seq sql.NullInt64
		var code sql.NullString
		var seenHash []byte
		err = tx.QueryRowContext(ctx, `
			SELECT status, server_seq, error_code, payload_hash FROM sync_inbox
			WHERE device_id = $1 AND op_id = $2`, dev.ID, opID).Scan(&status, &seq, &code, &seenHash)
		if err == nil {
			switch {
			case string(seenHash) != string(hash):
				ack.Status = "rejected"
				ack.Error = httpx.E(http.StatusConflict, "op_id_reused", "this op_id was already used for a different payload")
			case status == "applied":
				ack.Status, ack.ServerSeq = "duplicate", seq.Int64
			default:
				ack.Status = "rejected"
				ack.Error = httpx.E(http.StatusUnprocessableEntity, code.String, "rejected earlier; see alerts")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		// 2. Sequence check: gaps mean the device lost ops before sending.
		if op.DeviceSeq != dev.LastDeviceSeq+1 {
			kind := "device_seq_gap"
			if op.DeviceSeq <= dev.LastDeviceSeq {
				kind = "device_seq_regression"
			}
			if err := alerts.Raise(ctx, tx, p.TenantID, kind, "warning", &dev.ID, map[string]any{
				"expected": dev.LastDeviceSeq + 1, "got": op.DeviceSeq, "op_id": op.OpID,
			}); err != nil {
				return err
			}
		}
		if err := devices.AdvanceSeq(ctx, tx, dev.ID, op.DeviceSeq); err != nil {
			return err
		}

		// 3. Apply, inside a savepoint so a rejection undoes only the op's work.
		applier, known := g.Appliers[op.Type]
		var applied syncop.Applied
		var applyErr error
		if !known {
			applyErr = syncop.Reject("unknown_op_type", "warning", fmt.Sprintf("no applier for %q", op.Type))
		} else {
			if _, err := tx.ExecContext(ctx, `SAVEPOINT apply_op`); err != nil {
				return err
			}
			applied, applyErr = applier(ctx, tx, syncop.ApplyContext{
				Device: syncop.Device{
					ID: dev.ID, TenantID: dev.TenantID, OutletID: dev.OutletID,
					LegalEntityID: dev.LegalEntityID, InvoicePrefix: dev.InvoicePrefix, Role: dev.Role,
				},
				OpType: op.Type, Payload: op.Payload, PayloadHash: hash,
				NextSeq: func() (int64, error) { return nextSeq(ctx, tx, p.TenantID) },
			})
			if applyErr == nil {
				// Fire the deferred checks (ledger balance) NOW, inside the
				// savepoint, so a violation can still be turned into a rejection
				// instead of failing the COMMIT on every retry.
				_, applyErr = tx.ExecContext(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
			}
			applyErr = permanentAsRejection(applyErr)
			if _, isRej := syncop.AsRejection(applyErr); isRej {
				if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT apply_op`); err != nil {
					return err
				}
			} else if applyErr != nil {
				return applyErr // transient: the whole transaction rolls back
			} else if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT apply_op`); err != nil {
				return err
			}
		}

		// 4. Remember the outcome.
		if rej, isRej := syncop.AsRejection(applyErr); isRej {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO sync_inbox (tenant_id, device_id, op_id, device_seq, op_type, payload_hash, status, error_code, payload)
				VALUES ($1, $2, $3, $4, $5, $6, 'rejected', $7, $8)`,
				p.TenantID, dev.ID, opID, op.DeviceSeq, op.Type, hash, rej.Code, []byte(op.Payload)); err != nil {
				return err
			}
			if err := alerts.Raise(ctx, tx, p.TenantID, "op_rejected", rej.Severity, &opID, map[string]any{
				"op_type": op.Type, "code": rej.Code, "message": rej.Message, "device_id": dev.ID,
			}); err != nil {
				return err
			}
			ack.Status = "rejected"
			ack.Error = httpx.E(http.StatusUnprocessableEntity, rej.Code, rej.Message)
			return nil
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sync_inbox (tenant_id, device_id, op_id, device_seq, op_type, payload_hash, status, server_seq)
			VALUES ($1, $2, $3, $4, $5, $6, 'applied', $7)`,
			p.TenantID, dev.ID, opID, op.DeviceSeq, op.Type, hash, applied.ServerSeq); err != nil {
			return err
		}
		ack.Status, ack.ServerSeq = "applied", applied.ServerSeq
		if applied.Duplicate {
			ack.Status = "duplicate"
		}
		return nil
	})
	return ack, err
}

// permanentAsRejection turns database errors that will fail identically on
// every retry into rejections. Without this, one malformed op (a value out of
// range, a NUL byte in a name, a broken constraint) would return 503 forever
// and block every op queued behind it on that device.
//
//	class 22 data_exception, class 23 integrity_constraint_violation,
//	42501 insufficient_privilege: permanent, reject and alert.
//	Everything else (40001 serialization, 40P01 deadlock, 08 connection,
//	53 resources, 57 operator intervention): transient, let the device retry.
func permanentAsRejection(err error) error {
	var pe *pq.Error
	if err == nil || !errors.As(err, &pe) {
		return err
	}
	if c := pe.Code.Class(); c == "22" || c == "23" || pe.Code == "42501" {
		return syncop.Reject("db_rejected", "critical", fmt.Sprintf("%s (SQLSTATE %s)", pe.Message, pe.Code))
	}
	return err
}

// nextSeq hands out the tenant's next server sequence number. The UPDATE
// row-locks sync_state until COMMIT, so numbers are assigned in commit order
// (see migrations/0005_sales_sync.sql for why that matters).
func nextSeq(ctx context.Context, tx *sql.Tx, tenantID uuid.UUID) (int64, error) {
	var seq int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO sync_state (tenant_id, last_seq) VALUES ($1, 1)
		ON CONFLICT (tenant_id) DO UPDATE SET last_seq = sync_state.last_seq + 1
		RETURNING last_seq`, tenantID).Scan(&seq)
	return seq, err
}

// PushHandler serves POST /v1/sync/push (device).
func (g *Gateway) PushHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req PushRequest
		if err := httpx.DecodeLenient(w, r, MaxBodyBytes, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		res, err := g.Push(r.Context(), auth.Must(r.Context()), req)
		if err != nil {
			var he *httpx.Error
			if errors.As(err, &he) {
				httpx.Fail(w, r, err)
				return
			}
			// Transient failure part-way: return the acks we have so the device
			// can drop those ops from its queue, and ask it to retry the rest.
			slog.ErrorContext(r.Context(), "sync push stopped early", "err", err, "acked", len(res.Acks))
			res.Complete = false
			res.Error = httpx.E(http.StatusServiceUnavailable, "retry_later", "temporary failure; resend the ops not acked")
			w.Header().Set("Retry-After", "5")
			httpx.JSON(w, http.StatusServiceUnavailable, res)
			return
		}
		httpx.JSON(w, http.StatusOK, res)
	}
}
