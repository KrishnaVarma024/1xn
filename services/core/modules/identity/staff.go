package identity

// The owner's staff list (permission auth.Staff).
//
//	GET   /v1/staff         everyone in the business
//	POST  /v1/staff         {name, phone, role: manager | cashier}
//	PATCH /v1/staff/{id}    {name?, phone?, role?, active?}
//
// A business has one owner, made at sign-up; the owner's role, number and
// access are not changed here. A role change applies on the person's next
// request (roles are read with the token). Switching someone off, or changing
// their number, also ends their sessions.

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"1xn/core/internal/platform/auth"
	"1xn/core/internal/platform/db"
	"1xn/core/internal/platform/httpx"
	"1xn/core/internal/platform/ids"
)

// Person is one member of staff.
type Person struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Phone     string    `json:"phone"`
	Role      string    `json:"role"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	SignedIn  bool      `json:"signed_in"` // has a session that has not ended
}

// StaffRoutes is mounted at /v1/staff.
func StaffRoutes(d *sql.DB) http.Handler {
	r := chi.NewRouter()
	r.Get("/", listStaff(d))
	r.Post("/", addStaff(d))
	r.Patch("/{userID}", updateStaff(d))
	return r
}

type staffInput struct {
	Name   *string `json:"name"`
	Phone  *string `json:"phone"`
	Role   *string `json:"role"`
	Active *bool   `json:"active"`
}

func (in *staffInput) check() error {
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || utf8.RuneCountInString(n) > 200 || strings.ContainsFunc(n, func(r rune) bool { return r < ' ' || r == 0x7f }) {
			return bad("bad_name", "name is required: up to 200 characters, on one line")
		}
		in.Name = &n
	}
	if in.Phone != nil && !phoneRe.MatchString(*in.Phone) {
		return bad("bad_phone", "phone must be the full number with + and the country code, like +919876543210")
	}
	if in.Role != nil && *in.Role != string(auth.Manager) && *in.Role != string(auth.Cashier) {
		return bad("bad_role", "role must be manager or cashier (a business has one owner)")
	}
	return nil
}

const personCols = `u.id, u.name, u.phone, u.role, u.active, u.created_at,
	EXISTS (SELECT 1 FROM auth_token t WHERE t.subject_id = u.id AND t.kind = 'session'
	        AND t.revoked_at IS NULL AND t.expires_at > now())`

func scanPerson(row interface{ Scan(...any) error }) (Person, error) {
	var p Person
	err := row.Scan(&p.ID, &p.Name, &p.Phone, &p.Role, &p.Active, &p.CreatedAt, &p.SignedIn)
	return p, err
}

func listStaff(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := auth.Must(r.Context())
		out := []Person{}
		err := db.InTenantReadTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			out = out[:0]
			rows, err := tx.QueryContext(r.Context(), `
				SELECT `+personCols+` FROM app_user u
				ORDER BY u.role <> 'owner', NOT u.active, u.role, lower(u.name), u.id`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				person, err := scanPerson(rows)
				if err != nil {
					return err
				}
				out = append(out, person)
			}
			return rows.Err()
		})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"staff": out})
	}
}

var duplicatePhone = httpx.E(http.StatusConflict, "duplicate_phone", "someone on the staff list already has that number")

func addStaff(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in staffInput
		if err := httpx.Decode(w, r, 2<<10, &in); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if in.Name == nil || in.Phone == nil || in.Role == nil {
			httpx.Fail(w, r, bad("bad_request", "send name, phone and role"))
			return
		}
		if in.Active != nil {
			httpx.Fail(w, r, bad("bad_request", "a new person is always active"))
			return
		}
		if err := in.check(); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		p := auth.Must(r.Context())
		var out Person
		err := db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			id := ids.New()
			_, err := tx.ExecContext(r.Context(), `
				INSERT INTO app_user (id, tenant_id, name, phone, role) VALUES ($1, $2, $3, $4, $5)`,
				id, p.TenantID, *in.Name, *in.Phone, *in.Role)
			if db.IsUniqueViolation(err, "app_user_phone") {
				return duplicatePhone
			}
			if err != nil {
				return err
			}
			out, err = scanPerson(tx.QueryRowContext(r.Context(), `SELECT `+personCols+` FROM app_user u WHERE u.id = $1`, id))
			return err
		})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, out)
	}
}

func updateStaff(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "userID"))
		if err != nil {
			httpx.Fail(w, r, httpx.ErrNotFound)
			return
		}
		var in staffInput
		if err := httpx.Decode(w, r, 2<<10, &in); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if in.Name == nil && in.Phone == nil && in.Role == nil && in.Active == nil {
			httpx.Fail(w, r, bad("bad_request", "send name, phone, role or active"))
			return
		}
		if err := in.check(); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		p := auth.Must(r.Context())
		var out Person
		err = db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
			var role, phone string
			var active bool
			err := tx.QueryRowContext(r.Context(), `SELECT role, phone, active FROM app_user WHERE id = $1 FOR UPDATE`, id).
				Scan(&role, &phone, &active)
			if errors.Is(err, sql.ErrNoRows) {
				return httpx.ErrNotFound
			}
			if err != nil {
				return err
			}
			if role == string(auth.Owner) && (in.Phone != nil && *in.Phone != phone || in.Role != nil || in.Active != nil && !*in.Active) {
				return httpx.E(http.StatusConflict, "owner_fixed", "the owner's role, number and access cannot be changed here; only the name")
			}
			_, err = tx.ExecContext(r.Context(), `
				UPDATE app_user SET name = coalesce($2, name), phone = coalesce($3, phone),
				       role = coalesce($4, role), active = coalesce($5, active)
				WHERE id = $1`, id, in.Name, in.Phone, in.Role, in.Active)
			if db.IsUniqueViolation(err, "app_user_phone") {
				return duplicatePhone
			}
			if err != nil {
				return err
			}
			// Switched off, or a new number: their sessions end now.
			if in.Active != nil && !*in.Active || in.Phone != nil && *in.Phone != phone {
				if _, err := tx.ExecContext(r.Context(), `
					UPDATE auth_token SET revoked_at = now()
					WHERE subject_id = $1 AND kind = 'session' AND revoked_at IS NULL`, id); err != nil {
					return err
				}
			}
			out, err = scanPerson(tx.QueryRowContext(r.Context(), `SELECT `+personCols+` FROM app_user u WHERE u.id = $1`, id))
			return err
		})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}
