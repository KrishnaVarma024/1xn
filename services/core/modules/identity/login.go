package identity

// Signing in with a mobile number and a one-time code (ADR 0019).
//
//	POST /v1/auth/otp          {phone}               202 {challenge_id, expires_in, resend_in}
//	POST /v1/auth/otp/verify   {challenge_id, code}  200 {ticket, accounts: [...]}
//	POST /v1/auth/sessions     {ticket, user_id}     201 {token, expires_at, user, business_name}
//	POST /v1/auth/sign-out     (a signed-in person)  204
//
// One number can belong to people in several businesses (a manager who also
// helps at a friend's café), so the code proves the number, and then the
// person picks the business. The answer to "send a code" is the same whether
// or not the number has an account, so the endpoint cannot be used to find
// out who works where.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"1xn/core/internal/platform/auth"
	"1xn/core/internal/platform/db"
	"1xn/core/internal/platform/httpx"
	"1xn/core/internal/platform/ids"
)

// How long and how often. A six-digit code has a million values; five tries
// a code, three codes in fifteen minutes and ten a day give a guesser about
// one chance in twenty thousand a day per number.
const (
	codeTTL     = 10 * time.Minute
	maxAttempts = 5
	resendGap   = 30 * time.Second
	burstWindow = 15 * time.Minute
	burstCodes  = 3
	dayCodes    = 10
	ticketTTL   = 5 * time.Minute
	sessionTTL  = 30 * 24 * time.Hour
)

// CodeSender delivers a one-time code to a phone (SMS or WhatsApp in
// production).
type CodeSender interface {
	SendCode(ctx context.Context, phone, code string) error
}

// LogSender prints codes in the API's log: development only, until an SMS
// provider is wired in.
type LogSender struct{}

func (LogSender) SendCode(ctx context.Context, phone, code string) error {
	slog.WarnContext(ctx, fmt.Sprintf("SIGN-IN CODE %s for %s (development: shown here instead of an SMS)", code, phone))
	return nil
}

// Account is one business a number can sign in to.
type Account struct {
	UserID       uuid.UUID `json:"user_id"`
	BusinessName string    `json:"business_name"`
	Name         string    `json:"name"`
	Role         string    `json:"role"`
	tenantID     uuid.UUID
}

var codeRe = regexp.MustCompile(`^[0-9]{6}$`)

// codeHash binds a code to its challenge, so one leaked hash says nothing
// about another challenge's code.
func codeHash(challenge uuid.UUID, code string) []byte {
	h := sha256.Sum256([]byte(challenge.String() + ":" + code))
	return h[:]
}

func newCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		panic(err) // the OS random source failing is not recoverable
	}
	return fmt.Sprintf("%06d", n.Int64())
}

func newTicket() (string, []byte) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	t := "onx_t_" + base64.RawURLEncoding.EncodeToString(b)
	return t, auth.Hash(t)
}

func accounts(ctx context.Context, tx *sql.Tx, phone string) ([]Account, error) {
	rows, err := tx.QueryContext(ctx, `SELECT user_id, tenant_id, business_name, user_name, role FROM login_accounts($1)`, phone)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.UserID, &a.tenantID, &a.BusinessName, &a.Name, &a.Role); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// LoginRoutes is mounted at /v1/auth: public, except sign-out.
func LoginRoutes(d *sql.DB, send CodeSender) http.Handler {
	r := chi.NewRouter()
	r.Post("/otp", startOTP(d, send))
	r.Post("/otp/verify", verifyOTP(d))
	r.Post("/sessions", startSession(d))
	r.With(auth.Middleware(d, auth.People...)).Post("/sign-out", signOut(d))
	return r
}

func startOTP(d *sql.DB, send CodeSender) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Phone string `json:"phone"`
		}
		if err := httpx.Decode(w, r, 1<<10, &in); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if !phoneRe.MatchString(in.Phone) {
			httpx.Fail(w, r, bad("bad_phone", "phone must be the full number with + and the country code, like +919876543210"))
			return
		}
		id := ids.New()
		var code string
		// No tenant yet: uuid.Nil hides every tenant's rows (RLS), and
		// otp_challenge has none.
		err := db.InTenantTx(r.Context(), d, uuid.Nil, func(tx *sql.Tx) error {
			code = ""
			// One request per number at a time, so the limits count right.
			if _, err := tx.ExecContext(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('otp:' || $1, 0))`, in.Phone); err != nil {
				return err
			}
			if _, err := tx.ExecContext(r.Context(),
				`DELETE FROM otp_challenge WHERE phone = $1 AND created_at < now() - interval '2 days'`, in.Phone); err != nil {
				return err
			}
			var burst, day int
			var last sql.NullTime
			var now time.Time
			if err := tx.QueryRowContext(r.Context(), `
				SELECT count(*) FILTER (WHERE created_at > now() - make_interval(secs => $2)),
				       count(*) FILTER (WHERE created_at > now() - interval '1 day'),
				       max(created_at), now()
				FROM otp_challenge WHERE phone = $1`, in.Phone, burstWindow.Seconds()).Scan(&burst, &day, &last, &now); err != nil {
				return err
			}
			if last.Valid && now.Sub(last.Time) < resendGap {
				wait := int((resendGap - now.Sub(last.Time)).Seconds()) + 1
				w.Header().Set("Retry-After", strconv.Itoa(wait))
				return httpx.E(http.StatusTooManyRequests, "too_soon", fmt.Sprintf("a code was just sent; wait %d seconds before asking for another", wait))
			}
			if burst >= burstCodes {
				w.Header().Set("Retry-After", strconv.Itoa(int(burstWindow.Seconds())))
				return httpx.E(http.StatusTooManyRequests, "too_many_codes", "too many codes for this number; try again in 15 minutes")
			}
			if day >= dayCodes {
				w.Header().Set("Retry-After", "86400")
				return httpx.E(http.StatusTooManyRequests, "too_many_codes", "too many codes for this number today; try again tomorrow")
			}
			var has bool
			if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS (SELECT 1 FROM login_accounts($1))`, in.Phone).Scan(&has); err != nil {
				return err
			}
			var hash any // NULL: no account, nothing sent, no code can match
			if has {
				code = newCode()
				hash = codeHash(id, code)
			}
			_, err := tx.ExecContext(r.Context(), `
				INSERT INTO otp_challenge (id, phone, code_hash, expires_at) VALUES ($1, $2, $3, now() + make_interval(secs => $4))`,
				id, in.Phone, hash, codeTTL.Seconds())
			return err
		})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if code != "" {
			// A failed send is logged, not reported: reporting it would tell
			// a stranger the number has an account. The person asks again.
			if err := send.SendCode(r.Context(), in.Phone, code); err != nil {
				slog.ErrorContext(r.Context(), "sending a sign-in code failed", "err", err)
			}
		}
		httpx.JSON(w, http.StatusAccepted, map[string]any{
			"challenge_id": id, "expires_in": int(codeTTL.Seconds()), "resend_in": int(resendGap.Seconds()),
		})
	}
}

func verifyOTP(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ChallengeID uuid.UUID `json:"challenge_id"`
			Code        string    `json:"code"`
		}
		if err := httpx.Decode(w, r, 1<<10, &in); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if !codeRe.MatchString(in.Code) {
			httpx.Fail(w, r, bad("bad_code", "the code is the 6 digits in the message"))
			return
		}
		var wrong *httpx.Error
		var ticket string
		var found []Account
		err := db.InTenantTx(r.Context(), d, uuid.Nil, func(tx *sql.Tx) error {
			wrong, ticket, found = nil, "", nil
			var phone string
			var hash []byte
			var attempts int
			var live bool
			err := tx.QueryRowContext(r.Context(), `
				SELECT phone, code_hash, attempts, verified_at IS NULL AND expires_at > now()
				FROM otp_challenge WHERE id = $1 FOR UPDATE`, in.ChallengeID).Scan(&phone, &hash, &attempts, &live)
			if errors.Is(err, sql.ErrNoRows) {
				wrong = httpx.E(http.StatusUnauthorized, "wrong_code", "that code is wrong; check the message, or ask for a new code")
				return nil
			}
			if err != nil {
				return err
			}
			if !live || attempts >= maxAttempts {
				wrong = httpx.E(http.StatusUnauthorized, "code_expired", "that code has expired or was tried too often; ask for a new one")
				return nil
			}
			// The attempt counts whatever happens next, and is committed even
			// when the code is wrong (this function then returns nil).
			if _, err := tx.ExecContext(r.Context(), `UPDATE otp_challenge SET attempts = attempts + 1 WHERE id = $1`, in.ChallengeID); err != nil {
				return err
			}
			if hash == nil || subtle.ConstantTimeCompare(hash, codeHash(in.ChallengeID, in.Code)) != 1 {
				left := maxAttempts - attempts - 1
				msg := fmt.Sprintf("that code is wrong; %d %s left", left, plural(left, "try", "tries"))
				if left == 0 {
					msg = "that code is wrong, and that was the last try; ask for a new code"
				}
				wrong = httpx.E(http.StatusUnauthorized, "wrong_code", msg)
				return nil
			}
			var tHash []byte
			ticket, tHash = newTicket()
			if _, err := tx.ExecContext(r.Context(), `
				UPDATE otp_challenge SET verified_at = now(), ticket_hash = $2, ticket_expires_at = now() + make_interval(secs => $3)
				WHERE id = $1`, in.ChallengeID, tHash, ticketTTL.Seconds()); err != nil {
				return err
			}
			found, err = accounts(r.Context(), tx, phone)
			return err
		})
		if err == nil && wrong != nil {
			err = wrong
		}
		if err == nil && len(found) == 0 {
			err = httpx.E(http.StatusForbidden, "no_account", "this number is not on any business's staff list; ask the owner to add you")
		}
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"ticket": ticket, "accounts": found})
	}
}

// Session is what signing in returns. The token is shown once.
type Session struct {
	Token        string    `json:"token"`
	ExpiresAt    time.Time `json:"expires_at"`
	BusinessName string    `json:"business_name"`
	User         MeUser    `json:"user"`
}

func startSession(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Ticket string    `json:"ticket"`
			UserID uuid.UUID `json:"user_id"`
		}
		if err := httpx.Decode(w, r, 1<<10, &in); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		expired := httpx.E(http.StatusUnauthorized, "bad_ticket", "signing in took too long; start again")
		tHash := auth.Hash(in.Ticket)
		// Which business: read without a tenant, then do the rest inside it.
		var chosen *Account
		err := db.InTenantTx(r.Context(), d, uuid.Nil, func(tx *sql.Tx) error {
			chosen = nil
			var phone string
			err := tx.QueryRowContext(r.Context(), `
				SELECT phone FROM otp_challenge WHERE ticket_hash = $1 AND ticket_used_at IS NULL AND ticket_expires_at > now()`,
				tHash).Scan(&phone)
			if errors.Is(err, sql.ErrNoRows) {
				return expired
			}
			if err != nil {
				return err
			}
			list, err := accounts(r.Context(), tx, phone)
			for i := range list {
				if list[i].UserID == in.UserID {
					chosen = &list[i]
				}
			}
			if err == nil && chosen == nil {
				return httpx.E(http.StatusForbidden, "not_your_account", "this number cannot sign in to that business")
			}
			return err
		})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		token, hash := auth.NewToken(auth.Session)
		out := Session{Token: token, BusinessName: chosen.BusinessName,
			User: MeUser{ID: chosen.UserID, Name: chosen.Name, Role: chosen.Role}}
		err = db.InTenantTx(r.Context(), d, chosen.tenantID, func(tx *sql.Tx) error {
			// The ticket is used once: of two requests with it, one wins here.
			var phone string
			err := tx.QueryRowContext(r.Context(), `
				UPDATE otp_challenge SET ticket_used_at = now()
				WHERE ticket_hash = $1 AND ticket_used_at IS NULL AND ticket_expires_at > now()
				RETURNING phone`, tHash).Scan(&phone)
			if errors.Is(err, sql.ErrNoRows) {
				return expired
			}
			if err != nil {
				return err
			}
			// Still on the staff list, with that number (RLS: this tenant
			// only). FOR SHARE: switching them off or changing their number
			// locks this row first, so it either waits for this session (and
			// then revokes it with the rest) or has already happened (and
			// this finds no row). Without the lock a session made at that
			// moment escaped the revoke.
			err = tx.QueryRowContext(r.Context(), `
				SELECT phone FROM app_user WHERE id = $1 AND phone = $2 AND active FOR SHARE`, chosen.UserID, phone).Scan(&out.User.Phone)
			if errors.Is(err, sql.ErrNoRows) {
				return httpx.E(http.StatusForbidden, "not_your_account", "this number cannot sign in to that business")
			}
			if err != nil {
				return err
			}
			return tx.QueryRowContext(r.Context(), `
				INSERT INTO auth_token (token_hash, tenant_id, kind, subject_id, expires_at)
				VALUES ($1, $2, 'session', $3, now() + make_interval(secs => $4))
				RETURNING expires_at`, hash, chosen.tenantID, chosen.UserID, sessionTTL.Seconds()).Scan(&out.ExpiresAt)
		})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, out)
	}
}

// signOut ends the session that made the request. The owner token from
// sign-up is not revoked by signing out of a browser: it is the key the owner
// keeps (and the seed prints), like a master key.
func signOut(d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := auth.Must(r.Context())
		if p.Kind == auth.Session {
			err := db.InTenantTx(r.Context(), d, p.TenantID, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(r.Context(), `
					UPDATE auth_token SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`, p.TokenHash)
				return err
			})
			if err != nil {
				httpx.Fail(w, r, err)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
