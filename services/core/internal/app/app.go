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
	"1xn/core/modules/syncgw"
)

// New returns the API handler.
//
//	Public:  POST /v1/signup, POST /v1/devices/register, GET /healthz, GET /readyz
//	Owner:   /v1/outlets/{id}/pairing-codes|floors|tables|orders|days, /v1/catalogue/*,
//	         /v1/orders/{id}, /v1/bills/{id}, /v1/ledger/trial-balance, /v1/alerts
//	Device:  GET /v1/devices/me/menu, POST /v1/sync/push, GET /v1/sync/pull
func New(d *sql.DB) http.Handler {
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
		r.Post("/devices/register", devices.RegisterHandler(d))

		// Owner.
		r.Group(func(r chi.Router) {
			r.Use(auth.Middleware(d, auth.Owner))
			r.Get("/me", identity.MeHandler(d))
			r.Route("/outlets/{outletID}", func(r chi.Router) {
				r.Get("/menu", catalogue.OutletMenuHandler(d))
				r.Post("/pairing-codes", devices.PairingCodeHandler(d))
				r.Get("/devices", devices.ListHandler(d))
				r.Post("/floors", catalogue.CreateFloorHandler(d))
				r.Post("/tables", catalogue.CreateTableHandler(d))
				r.Patch("/tables/{tableID}", catalogue.UpdateTableHandler(d))
				r.Get("/orders", orders.OpenOrdersHandler(d))
				r.Mount("/days", dayclose.Routes(d))
			})
			r.Post("/devices/{deviceID}/revoke", devices.RevokeHandler(d))
			r.Get("/orders/{orderID}", orders.GetOrderHandler(d))
			r.Mount("/catalogue", catalogue.OwnerRoutes(d))
			r.Mount("/bills", sales.Routes(d))
			r.Mount("/ledger", ledger.Routes(d))
			r.Mount("/alerts", alerts.Routes(d))
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
