// Package devices pairs tills, captain phones and kitchen screens with an
// outlet, and gives each one its own invoice series.
//
// Cross-module API: Get, Lock, AdvanceSeq.
package devices

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"1xn/core/internal/platform/auth"
	"1xn/core/internal/platform/db"
	"1xn/core/internal/platform/gst"
	"1xn/core/internal/platform/httpx"
	"1xn/core/internal/platform/ids"
	"1xn/core/modules/identity"
)

// PairingTTL is how long a pairing code works.
const PairingTTL = 10 * time.Minute

// No 0/O, 1/I/L: codes are read aloud and typed on a till by busy people.
const codeAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

var roles = map[string]bool{"pos": true, "captain": true, "kds": true, "hub": true}

// newPairingCode returns 8 random characters shown as XXXX-XXXX.
// 31^8 is about 850 billion codes; with a 10-minute life and single use, a
// guess is hopeless even before rate limiting.
func newPairingCode() string {
	var b strings.Builder
	for i := 0; i < 8; i++ {
		// rand.Int draws uniformly; byte % 31 would favour the first letters.
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(codeAlphabet))))
		if err != nil {
			panic(err)
		}
		if i == 4 {
			b.WriteByte('-')
		}
		b.WriteByte(codeAlphabet[n.Int64()])
	}
	return b.String()
}

func normaliseCode(s string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(s))
}

// Info is a registered device.
type Info struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	OutletID      uuid.UUID
	LegalEntityID uuid.UUID
	Role          string
	InvoicePrefix string
	LastDeviceSeq int64
	Revoked       bool
}

// ErrNoDevice means the device does not exist for this tenant.
var ErrNoDevice = errors.New("devices: no such device")

// Get loads a device without locking it.
func Get(ctx context.Context, tx *sql.Tx, id uuid.UUID) (Info, error) {
	return load(ctx, tx, id, "")
}

// Lock loads a device and row-locks it for the rest of the transaction, so
// two pushes from the same device are applied one after the other.
func Lock(ctx context.Context, tx *sql.Tx, id uuid.UUID) (Info, error) {
	return load(ctx, tx, id, " FOR UPDATE")
}

func load(ctx context.Context, tx *sql.Tx, id uuid.UUID, lock string) (Info, error) {
	var d Info
	var revoked sql.NullTime
	err := tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, outlet_id, legal_entity_id, role, invoice_prefix, last_device_seq, revoked_at
		FROM device WHERE id = $1`+lock, id).
		Scan(&d.ID, &d.TenantID, &d.OutletID, &d.LegalEntityID, &d.Role, &d.InvoicePrefix, &d.LastDeviceSeq, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNoDevice
	}
	d.Revoked = revoked.Valid
	return d, err
}

// AdvanceSeq records the highest op sequence number seen from a device.
func AdvanceSeq(ctx context.Context, tx *sql.Tx, id uuid.UUID, seq int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE device SET last_device_seq = greatest(last_device_seq, $2) WHERE id = $1`, id, seq)
	return err
}

// RegisterRequest is sent by a new device.
type RegisterRequest struct {
	PairingCode string `json:"pairing_code"`
	Name        string `json:"name"`
}

// RegisterResponse is everything a fresh device needs to start billing.
type RegisterResponse struct {
	DeviceID      uuid.UUID           `json:"device_id"`
	DeviceToken   string              `json:"device_token"`
	Role          string              `json:"role"`
	InvoicePrefix string              `json:"invoice_prefix"`
	Outlet        identity.OutletInfo `json:"outlet"`
}

var errBadCode = httpx.E(http.StatusBadRequest, "invalid_pairing_code",
	"that pairing code is wrong, used or expired; ask for a new one")

// Register redeems a pairing code and creates the device.
//
// The code is redeemed INSIDE the same transaction that creates the device.
// If anything later fails, the redemption rolls back too and the code still
// works; otherwise a crash here would burn the code and leave no device.
func Register(ctx context.Context, d *sql.DB, req RegisterRequest) (RegisterResponse, error) {
	var res RegisterResponse
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 100 {
		return res, httpx.E(http.StatusBadRequest, "bad_name", "name is required (max 100 characters)")
	}
	code := normaliseCode(req.PairingCode)
	if len(code) != 8 {
		return res, errBadCode
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback() // no-op after Commit

	var tenantID, outletID uuid.UUID
	var role string
	err = tx.QueryRowContext(ctx, `SELECT tenant_id, outlet_id, role FROM pairing_redeem($1)`, auth.Hash(code)).
		Scan(&tenantID, &outletID, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return res, errBadCode
	}
	if err != nil {
		return res, err
	}
	// From here on we know the tenant, so RLS applies to everything below.
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID.String()); err != nil {
		return res, err
	}

	outlet, err := identity.Outlet(ctx, tx, outletID)
	if err != nil {
		return res, err
	}

	// Hand out the next series number for this GSTIN. The UPDATE row-locks the
	// legal entity, so two devices pairing at once get different numbers.
	var n int
	if err := tx.QueryRowContext(ctx, `
		UPDATE legal_entity SET next_series_no = next_series_no + 1
		WHERE id = $1 RETURNING next_series_no - 1`, outlet.LegalEntityID).Scan(&n); err != nil {
		return res, err
	}
	prefix, err := gst.SeriesPrefix(n)
	if err != nil {
		return res, httpx.E(http.StatusConflict, "series_exhausted", err.Error())
	}

	token, hash := auth.NewToken(auth.Device)
	res = RegisterResponse{DeviceID: ids.New(), DeviceToken: token, Role: role, InvoicePrefix: prefix, Outlet: outlet}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO device (id, tenant_id, outlet_id, legal_entity_id, name, role, invoice_prefix)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		res.DeviceID, tenantID, outletID, outlet.LegalEntityID, name, role, prefix); err != nil {
		return res, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO auth_token (token_hash, tenant_id, kind, subject_id) VALUES ($1, $2, 'device', $3)`,
		hash, tenantID, res.DeviceID); err != nil {
		return res, err
	}
	return res, tx.Commit()
}

// CreatePairingCode is called by an owner for one of their outlets.
func CreatePairingCode(ctx context.Context, d *sql.DB, p auth.Principal, outletID uuid.UUID, role string) (string, time.Time, error) {
	if !roles[role] {
		return "", time.Time{}, httpx.E(http.StatusBadRequest, "bad_role", "role must be pos, captain, kds or hub")
	}
	code := newPairingCode()
	expires := time.Now().Add(PairingTTL).UTC().Truncate(time.Second)
	err := db.InTenantTx(ctx, d, p.TenantID, func(tx *sql.Tx) error {
		if _, err := identity.Outlet(ctx, tx, outletID); err != nil {
			if errors.Is(err, identity.ErrNoOutlet) {
				return httpx.ErrNotFound
			}
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO pairing_code (code_hash, tenant_id, outlet_id, role, created_by, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			auth.Hash(normaliseCode(code)), p.TenantID, outletID, role, p.SubjectID, expires)
		return err
	})
	return code, expires, err
}

// PairingCodeHandler serves POST /v1/outlets/{outletID}/pairing-codes (owner).
func PairingCodeHandler(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		outletID, err := uuid.Parse(chi.URLParam(r, "outletID"))
		if err != nil {
			httpx.Fail(w, r, httpx.ErrNotFound)
			return
		}
		var req struct {
			Role string `json:"role"`
		}
		if err := httpx.Decode(w, r, 1<<10, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		code, exp, err := CreatePairingCode(r.Context(), d, auth.Must(r.Context()), outletID, req.Role)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{"code": code, "expires_at": exp})
	}
}

// RegisterHandler serves POST /v1/devices/register (public: the pairing code
// is the credential).
func RegisterHandler(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req RegisterRequest
		if err := httpx.Decode(w, r, 1<<10, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		res, err := Register(r.Context(), d, req)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, res)
	}
}
