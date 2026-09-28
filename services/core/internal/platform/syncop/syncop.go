// Package syncop is the contract between the sync gateway and the modules
// that apply device ops. It lives in platform so modules can implement
// appliers without importing the gateway (which would create an import cycle).
package syncop

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
)

// Device is the pushing device, as appliers need it.
type Device struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	OutletID      uuid.UUID
	LegalEntityID uuid.UUID
	InvoicePrefix string
	Role          string // pos, captain, kds, hub
}

// ApplyContext is what an applier gets for one op.
type ApplyContext struct {
	Device      Device
	OpType      string
	Payload     []byte // exactly the bytes the device sent
	PayloadHash []byte // SHA-256 of Payload
	// NextSeq hands out the tenant's next server sequence number. Call it
	// once, only when the op will be applied.
	NextSeq func() (int64, error)
}

// Applied is a successful outcome.
type Applied struct {
	ServerSeq int64
	// Duplicate means the change already existed with identical content
	// (for example, the device re-queued a bill under a new op id).
	Duplicate bool
}

// Applier applies one op type inside the push transaction. Returning a
// *Rejection rolls its work back and records the op as rejected; any other
// error aborts the push so the device retries later.
type Applier func(ctx context.Context, tx *sql.Tx, a ApplyContext) (Applied, error)

// Rejection is a permanent failure: sending the same op again will fail the
// same way, so the device must not retry it forever.
type Rejection struct {
	Code     string
	Message  string
	Severity string // info, warning or critical: how loudly to alert a human
}

func (r *Rejection) Error() string { return r.Code + ": " + r.Message }

// Reject builds a *Rejection.
func Reject(code, severity, msg string) *Rejection {
	return &Rejection{Code: code, Message: msg, Severity: severity}
}

// AsRejection unwraps a *Rejection.
func AsRejection(err error) (*Rejection, bool) {
	var r *Rejection
	ok := errors.As(err, &r)
	return r, ok
}
