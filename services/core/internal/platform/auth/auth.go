// Package auth issues and checks bearer tokens, and says who may do what.
//
// Tokens are opaque random strings, stored hashed (ADR 0009). Three kinds:
// the owner token made at sign-up (never expires; developers and the seed use
// it), a session made when a person signs in with a one-time code (expires;
// ADR 0019), and a device token made when a till pairs. A person's role
// (owner, manager, cashier) is read with the token on every request, so a
// change of role or a switched-off person takes effect at once.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"1xn/core/internal/platform/httpx"
)

// Kind of token.
type Kind string

const (
	OwnerToken Kind = "owner"   // made at sign-up for the owner; never expires
	Session    Kind = "session" // made when a person signs in with a code; expires
	Device     Kind = "device"  // made when a device pairs
)

// People are the token kinds a person holds (the back office's callers).
var People = []Kind{OwnerToken, Session}

// Principal is who is calling: which tenant, what kind, and their id.
type Principal struct {
	TenantID  uuid.UUID
	Kind      Kind
	SubjectID uuid.UUID // app_user.id for people, device.id for devices
	Role      Role      // a person's role; empty for devices
	TokenHash []byte    // the hash of the token used (to sign it out)
}

type ctxKey struct{}

// From returns the principal the middleware put on the context.
func From(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// WithPrincipal is for tests and internal callers.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// NewToken returns a new random token and the hash to store. The prefix
// ("onx_o_", "onx_s_" or "onx_d_") makes leaked tokens easy to spot and lets
// secret scanners (GitHub's included) find them in code and logs.
func NewToken(kind Kind) (token string, hash []byte) {
	b := make([]byte, 32) // 256 bits: unguessable
	if _, err := rand.Read(b); err != nil {
		panic(err) // the OS random source failing is not recoverable
	}
	token = "onx_" + string(kind[0]) + "_" + base64.RawURLEncoding.EncodeToString(b)
	return token, Hash(token)
}

// Hash is SHA-256. A fast hash is right here (unlike passwords): tokens are
// long and random, so there is nothing to brute-force.
func Hash(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

// Middleware resolves the bearer token and requires one of the given kinds.
func Middleware(d *sql.DB, kinds ...Kind) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || token == "" {
				httpx.Fail(w, r, httpx.ErrUnauthorized)
				return
			}
			p := Principal{TokenHash: Hash(token)}
			var kind, role string
			err := d.QueryRowContext(r.Context(),
				`SELECT tenant_id, kind, subject_id, role FROM auth_resolve($1)`, p.TokenHash).
				Scan(&p.TenantID, &kind, &p.SubjectID, &role)
			if errors.Is(err, sql.ErrNoRows) {
				httpx.Fail(w, r, httpx.ErrUnauthorized)
				return
			}
			if err != nil {
				httpx.Fail(w, r, err)
				return
			}
			p.Kind, p.Role = Kind(kind), Role(role)
			allowed := false
			for _, k := range kinds {
				allowed = allowed || k == p.Kind
			}
			if !allowed {
				httpx.Fail(w, r, httpx.ErrForbidden)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
		})
	}
}

// Must returns the principal or panics; use it only behind Middleware.
func Must(ctx context.Context) Principal {
	p, ok := From(ctx)
	if !ok {
		panic("auth: no principal on context; is the route behind auth.Middleware?")
	}
	return p
}
