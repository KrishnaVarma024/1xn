// Package auth issues and checks bearer tokens.
//
// This is deliberately simple P0 auth: opaque random tokens, stored hashed.
// Owners get one at signup; devices get one when they pair. Real owner login
// (OTP on WhatsApp/SMS, sessions, staff PINs) comes with the identity module
// proper. See docs/adr/0009.
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

// Kind of principal behind a token.
type Kind string

const (
	Owner  Kind = "owner"
	Device Kind = "device"
)

// Principal is who is calling: which tenant, what kind, and their id.
type Principal struct {
	TenantID  uuid.UUID
	Kind      Kind
	SubjectID uuid.UUID // app_user.id for owners, device.id for devices
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
// ("onx_o_" or "onx_d_") makes leaked tokens easy to spot and lets secret
// scanners (GitHub's included) find them in code and logs.
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
			var p Principal
			var kind string
			err := d.QueryRowContext(r.Context(),
				`SELECT tenant_id, kind, subject_id FROM auth_resolve($1)`, Hash(token)).
				Scan(&p.TenantID, &kind, &p.SubjectID)
			if errors.Is(err, sql.ErrNoRows) {
				httpx.Fail(w, r, httpx.ErrUnauthorized)
				return
			}
			if err != nil {
				httpx.Fail(w, r, err)
				return
			}
			p.Kind = Kind(kind)
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
