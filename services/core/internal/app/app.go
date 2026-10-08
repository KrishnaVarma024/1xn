// Package app wires the modules into one HTTP handler. It is the only place
// that knows about every module; modules never import each other's internals.
package app

import (
	"database/sql"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"1xn/core/internal/platform/alerts"
	"1xn/core/internal/platform/auth"
	"1xn/core/internal/platform/httpx"
	"1xn/core/internal/platform/syncop"
	"1xn/core/modules/catalogue"
	"1xn/core/modules/dayclose"
	"1xn/core/modules/devices"
	"1xn/core/modules/identity"
	"1xn/core/modules/ledger"
	"1xn/core/modules/orders"
	"1xn/core/modules/sales"
	"1xn/core/modules/stock"
	"1xn/core/modules/syncgw"
)

// Option changes how New builds the API.
type Option func(*options)

type options struct {
	codes identity.CodeSender
}

// WithCodeSender sets how sign-in codes reach people's phones (tests capture
// them; development prints them in the log).
func WithCodeSender(s identity.CodeSender) Option { return func(o *options) { o.codes = s } }

// New returns the API handler.
//
//	Public:  POST /v1/signup, /v1/auth/otp, /v1/auth/otp/verify, /v1/auth/sessions,
//	         POST /v1/devices/register, GET /healthz, GET /readyz
//	People:  GET /v1/me, POST /v1/auth/sign-out, and every back office route below, each
//	         behind the permission it needs (auth/permissions.go: which roles have which)
//	Device:  GET /v1/devices/me/menu, POST /v1/sync/push, GET /v1/sync/pull
func New(d *sql.DB, opts ...Option) http.Handler {
	o := options{codes: identity.LogSender{}}
	for _, opt := range opts {
		opt(&o)
	}
	appliers := map[string]syncop.Applier{sales.OpBillSettled: sales.ApplySettledBill}
	for op, fn := range orders.Appliers() {
		appliers[op] = fn
	}
	for op, fn := range dayclose.Appliers() {
		appliers[op] = fn
	}
	gw := &syncgw.Gateway{DB: d, Appliers: appliers}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(logRequests)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := d.PingContext(r.Context()); err != nil {
			httpx.Fail(w, r, httpx.E(http.StatusServiceUnavailable, "db_unavailable", "database is not reachable"))
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	r.Route("/v1", func(r chi.Router) {
		// Public.
		r.Mount("/signup", identity.Routes(d))
		r.Mount("/auth", identity.LoginRoutes(d, o.codes))
		r.Post("/devices/register", devices.RegisterHandler(d))

		// People: the back office. Every route says which permission it needs.
		r.Group(func(r chi.Router) {
			r.Use(auth.Middleware(d, auth.People...))
			need := func(r chi.Router, p auth.Permission) chi.Router { return r.With(auth.Need(p)) }
			r.Get("/me", identity.MeHandler(d))
			r.Route("/outlets/{outletID}", func(r chi.Router) {
				need(r, auth.Floor).Get("/menu", catalogue.OutletMenuHandler(d))
				need(r, auth.Floor).Get("/orders", orders.OpenOrdersHandler(d))
				need(r, auth.Devices).Post("/pairing-codes", devices.PairingCodeHandler(d))
				need(r, auth.Devices).Get("/devices", devices.ListHandler(d))
				need(r, auth.Menu).Post("/floors", catalogue.CreateFloorHandler(d))
				need(r, auth.Menu).Post("/tables", catalogue.CreateTableHandler(d))
				need(r, auth.Menu).Patch("/tables/{tableID}", catalogue.UpdateTableHandler(d))
				need(r, auth.Day).Mount("/days", dayclose.Routes(d))
				need(r, auth.Day).Mount("/shifts", dayclose.ShiftRoutes(d))
				r.Mount("/stock", stock.OutletRoutes(d)) // permissions per route, inside
			})
			need(r, auth.Devices).Post("/devices/{deviceID}/revoke", devices.RevokeHandler(d))
			need(r, auth.Floor).Get("/orders/{orderID}", orders.GetOrderHandler(d))
			need(r, auth.Menu).Mount("/catalogue", catalogue.OwnerRoutes(d))
			need(r, auth.Bills).Mount("/bills", sales.Routes(d))
			need(r, auth.Books).Mount("/ledger", ledger.Routes(d))
			need(r, auth.StockManage).Mount("/stock", stock.OwnerRoutes(d))
			need(r, auth.Alerts).Mount("/alerts", alerts.Routes(d))
			need(r, auth.Staff).Mount("/staff", identity.StaffRoutes(d))
		})

		// Device.
		r.Group(func(r chi.Router) {
			r.Use(auth.Middleware(d, auth.Device))
			r.Mount("/devices/me", catalogue.DeviceRoutes(d))
			r.Post("/sync/push", gw.PushHandler())
			r.Get("/sync/pull", orders.PullHandler(d))
		})
	})
	return r
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		next.ServeHTTP(ww, r)
		slog.InfoContext(r.Context(), "http",
			"method", r.Method, "path", r.URL.Path, "status", ww.Status(),
			"ms", time.Since(start).Milliseconds(), "req_id", middleware.GetReqID(r.Context()))
	})
}
