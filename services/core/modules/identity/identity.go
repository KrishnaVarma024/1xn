// Package identity owns tenants, legal entities (GSTINs), outlets and users.
//
// Cross-module API (the only functions other modules may call):
// Outlet, OutletInfo, MeHandler.
package identity

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"1xn/core/internal/platform/auth"
	"1xn/core/internal/platform/db"
	"1xn/core/internal/platform/gst"
	"1xn/core/internal/platform/httpx"
	"1xn/core/internal/platform/ids"
	"1xn/core/modules/ledger"
)

// SignupRequest creates a tenant with its first legal entity, outlet and owner.
type SignupRequest struct {
	BusinessName string `json:"business_name"`
	Owner        struct {
		Name  string `json:"name"`
		Phone string `json:"phone"` // E.164, e.g. +919876543210
	} `json:"owner"`
	LegalEntity struct {
		Name      string `json:"name"`
		GSTIN     string `json:"gstin,omitempty"` // optional: small businesses may be unregistered
		StateCode string `json:"state_code"`      // 2 digits, e.g. "29" for Karnataka
	} `json:"legal_entity"`
	Outlet struct {
		Name      string `json:"name"`
		PriceMode string `json:"price_mode,omitempty"` // exclusive (default) or inclusive
		Rounding  string `json:"rounding,omitempty"`   // nearest_rupee (default) or none
	} `json:"outlet"`
}

// SignupResponse carries the owner token. It is shown exactly once; only its
// hash is stored.
type SignupResponse struct {
	TenantID      uuid.UUID `json:"tenant_id"`
	LegalEntityID uuid.UUID `json:"legal_entity_id"`
	OutletID      uuid.UUID `json:"outlet_id"`
	OwnerID       uuid.UUID `json:"owner_id"`
	OwnerToken    string    `json:"owner_token"`
}

var phoneRe = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

func bad(code, msg string) error { return httpx.E(http.StatusBadRequest, code, msg) }

func (req *SignupRequest) normalise() error {
	req.BusinessName = strings.TrimSpace(req.BusinessName)
	req.LegalEntity.GSTIN = strings.ToUpper(strings.TrimSpace(req.LegalEntity.GSTIN))
	if req.Outlet.PriceMode == "" {
		req.Outlet.PriceMode = "exclusive"
	}
	if req.Outlet.Rounding == "" {
		req.Outlet.Rounding = "nearest_rupee"
	}
	le := &req.LegalEntity
	if le.GSTIN != "" {
		if !gst.ValidGSTIN(le.GSTIN) {
			return bad("bad_gstin", "GSTIN is malformed or its check digit is wrong")
		}
		if le.StateCode == "" {
			le.StateCode = le.GSTIN[:2]
		}
		if le.StateCode != le.GSTIN[:2] {
			return bad("bad_state_code", "state_code must match the first two digits of the GSTIN")
		}
	}
	switch {
	case req.BusinessName == "" || len(req.BusinessName) > 200:
		return bad("bad_business_name", "business_name is required (max 200 characters)")
	case strings.TrimSpace(req.Owner.Name) == "":
		return bad("bad_owner", "owner.name is required")
	case !phoneRe.MatchString(req.Owner.Phone):
		return bad("bad_phone", "owner.phone must be E.164, like +919876543210")
	case strings.TrimSpace(le.Name) == "":
		return bad("bad_legal_entity", "legal_entity.name is required")
	case !gst.ValidStateCode(le.StateCode):
		return bad("bad_state_code", "legal_entity.state_code must be a GST state code in use, like 29 for Karnataka")
	case strings.TrimSpace(req.Outlet.Name) == "":
		return bad("bad_outlet", "outlet.name is required")
	case req.Outlet.PriceMode != "exclusive" && req.Outlet.PriceMode != "inclusive":
		return bad("bad_price_mode", "outlet.price_mode must be exclusive or inclusive")
	case req.Outlet.Rounding != "nearest_rupee" && req.Outlet.Rounding != "none":
		return bad("bad_rounding", "outlet.rounding must be nearest_rupee or none")
	}
	return nil
}

// Signup creates everything a new customer needs in ONE transaction: either
// all of it exists afterwards or none of it does.
func Signup(ctx context.Context, d *sql.DB, req SignupRequest) (SignupResponse, error) {
	if err := req.normalise(); err != nil {
		return SignupResponse{}, err
	}
	res := SignupResponse{
		TenantID: ids.New(), LegalEntityID: ids.New(), OutletID: ids.New(), OwnerID: ids.New(),
	}
	token, hash := auth.NewToken(auth.Owner)
	res.OwnerToken = token

	var gstin any
	if req.LegalEntity.GSTIN != "" {
		gstin = req.LegalEntity.GSTIN
	}

	// The tenant does not exist yet, but we already know its id, so we can set
	// the RLS context to it and every INSERT passes the policy's WITH CHECK.
	err := db.InTenantTx(ctx, d, res.TenantID, func(tx *sql.Tx) error {
		steps := []struct {
			q    string
			args []any
		}{
			{`INSERT INTO tenant (id, name) VALUES ($1, $2)`, []any{res.TenantID, req.BusinessName}},
			{`INSERT INTO legal_entity (id, tenant_id, name, gstin, state_code) VALUES ($1, $2, $3, $4, $5)`,
				[]any{res.LegalEntityID, res.TenantID, req.LegalEntity.Name, gstin, req.LegalEntity.StateCode}},
			{`INSERT INTO outlet (id, tenant_id, legal_entity_id, name, state_code, price_mode, rounding)
			  VALUES ($1, $2, $3, $4, $5, $6, $7)`,
				[]any{res.OutletID, res.TenantID, res.LegalEntityID, req.Outlet.Name, req.LegalEntity.StateCode,
					req.Outlet.PriceMode, req.Outlet.Rounding}},
			{`INSERT INTO app_user (id, tenant_id, name, phone, role) VALUES ($1, $2, $3, $4, 'owner')`,
				[]any{res.OwnerID, res.TenantID, req.Owner.Name, req.Owner.Phone}},
			{`INSERT INTO auth_token (token_hash, tenant_id, kind, subject_id) VALUES ($1, $2, 'owner', $3)`,
				[]any{hash, res.TenantID, res.OwnerID}},
		}
		for _, s := range steps {
			if _, err := tx.ExecContext(ctx, s.q, s.args...); err != nil {
				return err
			}
		}
		return ledger.SeedAccounts(ctx, tx, res.TenantID, res.LegalEntityID)
	})
	return res, err
}

// OutletInfo is what other modules need to know about an outlet.
type OutletInfo struct {
	ID              uuid.UUID `json:"id"`
	Name            string    `json:"name"`
	StateCode       string    `json:"state_code"`
	PriceMode       string    `json:"price_mode"`
	Rounding        string    `json:"rounding"`
	LegalEntityID   uuid.UUID `json:"legal_entity_id"`
	LegalEntityName string    `json:"legal_entity_name"`
	GSTIN           string    `json:"gstin,omitempty"`
}

// ErrNoOutlet means the outlet does not exist for this tenant (RLS hides
// other tenants' outlets, so "not yours" and "does not exist" look the same,
// which is exactly what we want an attacker to see).
var ErrNoOutlet = errors.New("identity: no such outlet")

// Outlet loads an outlet inside the caller's tenant transaction.
func Outlet(ctx context.Context, tx *sql.Tx, id uuid.UUID) (OutletInfo, error) {
	var o OutletInfo
	var gstin sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT o.id, o.name, o.state_code, o.price_mode, o.rounding, l.id, l.name, l.gstin
		FROM outlet o JOIN legal_entity l ON l.id = o.legal_entity_id
		WHERE o.id = $1`, id).
		Scan(&o.ID, &o.Name, &o.StateCode, &o.PriceMode, &o.Rounding, &o.LegalEntityID, &o.LegalEntityName, &gstin)
	if errors.Is(err, sql.ErrNoRows) {
		return o, ErrNoOutlet
	}
	o.GSTIN = gstin.String
	return o, err
}

// Routes mounts the public signup endpoint.
func Routes(d *sql.DB) http.Handler {
	r := chi.NewRouter()
	r.Post("/", func(w http.ResponseWriter, r *http.Request) {
		var req SignupRequest
		if err := httpx.Decode(w, r, 16<<10, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		res, err := Signup(r.Context(), d, req)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, res)
	})
	return r
}

// Me is who the owner is and what they own: the back office's first call.
type Me struct {
	TenantID     uuid.UUID    `json:"tenant_id"`
	BusinessName string       `json:"business_name"`
	User         MeUser       `json:"user"`
	Outlets      []OutletInfo `json:"outlets"`
}

// MeUser is the signed-in person.
type MeUser struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Phone string    `json:"phone"`
	Role  string    `json:"role"`
}

// MeHandler serves GET /v1/me (owner).
func MeHandler(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := auth.Must(r.Context())
		me := Me{TenantID: p.TenantID, Outlets: []OutletInfo{}}
		err := db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			if err := tx.QueryRowContext(r.Context(), `SELECT name FROM tenant WHERE id = $1`, p.TenantID).
				Scan(&me.BusinessName); err != nil {
				return err
			}
			if err := tx.QueryRowContext(r.Context(), `SELECT id, name, phone, role FROM app_user WHERE id = $1`, p.SubjectID).
				Scan(&me.User.ID, &me.User.Name, &me.User.Phone, &me.User.Role); err != nil {
				return err
			}
			rows, err := tx.QueryContext(r.Context(), `
				SELECT o.id, o.name, o.state_code, o.price_mode, o.rounding, l.id, l.name, coalesce(l.gstin, '')
				FROM outlet o JOIN legal_entity l ON l.id = o.legal_entity_id
				ORDER BY o.created_at, o.id`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var o OutletInfo
				if err := rows.Scan(&o.ID, &o.Name, &o.StateCode, &o.PriceMode, &o.Rounding,
					&o.LegalEntityID, &o.LegalEntityName, &o.GSTIN); err != nil {
					return err
				}
				me.Outlets = append(me.Outlets, o)
			}
			return rows.Err()
		})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, me)
	}
}
