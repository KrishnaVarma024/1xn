// Package httpx has the small helpers every HTTP handler uses: JSON in, JSON
// out, and one error shape for the whole API.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
)

// Error is the one error body the API ever returns:
//
//	{"error": {"code": "invoice_conflict", "message": "..."}}
//
// Code is stable and machine-readable (clients switch on it); Message is for
// humans and may change.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message) }

// E builds an *Error.
func E(status int, code, msg string) *Error { return &Error{Status: status, Code: code, Message: msg} }

// Common errors.
var (
	ErrUnauthorized = E(http.StatusUnauthorized, "unauthorized", "missing or invalid token")
	ErrForbidden    = E(http.StatusForbidden, "forbidden", "this token cannot do that")
	ErrNotFound     = E(http.StatusNotFound, "not_found", "no such resource")
)

// JSON writes v with the given status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Fail writes err as an API error. Anything that is not an *Error is logged
// and hidden behind a generic 500, so internal details never reach clients.
func Fail(w http.ResponseWriter, r *http.Request, err error) {
	var e *Error
	if !errors.As(err, &e) {
		slog.ErrorContext(r.Context(), "internal error", "err", err, "path", r.URL.Path)
		e = E(http.StatusInternalServerError, "internal", "something went wrong; it has been logged")
	}
	JSON(w, e.Status, map[string]*Error{"error": e})
}

// Decode reads a JSON body of at most maxBytes into v. Unknown fields are
// rejected, which turns a client typo ("prce_paise") into a clear 400 instead
// of a silently ignored field.
func Decode(w http.ResponseWriter, r *http.Request, maxBytes int64, v any) error {
	return decode(w, r, maxBytes, v, true)
}

// DecodeLenient is Decode but tolerates unknown fields. Device sync uses it:
// an older server must accept payloads from a newer app version.
func DecodeLenient(w http.ResponseWriter, r *http.Request, maxBytes int64, v any) error {
	return decode(w, r, maxBytes, v, false)
}

func decode(w http.ResponseWriter, r *http.Request, maxBytes int64, v any, strict bool) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return E(http.StatusRequestEntityTooLarge, "body_too_large",
				fmt.Sprintf("body is larger than %d bytes", maxBytes))
		}
		return E(http.StatusBadRequest, "bad_json", err.Error())
	}
	if dec.More() {
		return E(http.StatusBadRequest, "bad_json", "body has trailing data")
	}
	return nil
}
