package e2e

// Every route says who may call it. This test lists the API's routes and
// fails when one is missing from the table below, so a new route cannot be
// added without deciding its permission; then it calls every back office
// route as an owner, a manager and a cashier and checks that exactly the
// roles without the permission are refused.

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"1xn/core/internal/app"
	"1xn/core/internal/platform/auth"
)

// "public" needs no token, "device" a device token, "person" any signed-in
// person; anything else is the permission a person's role needs.
var routeAccess = map[string]string{
	"GET /healthz":                       "public",
	"GET /readyz":                        "public",
	"POST /v1/signup/":                   "public",
	"POST /v1/auth/otp":                  "public",
	"POST /v1/auth/otp/verify":           "public",
	"POST /v1/auth/sessions":             "public",
	"POST /v1/devices/register":          "public",
	"POST /v1/auth/sign-out":             "person",
	"GET /v1/me":                         "person",
	"GET /v1/devices/me/menu":            "device",
	"POST /v1/sync/push":                 "device",
	"GET /v1/sync/pull":                  "device",
	"GET /v1/alerts/":                    "alerts",
	"GET /v1/bills/":                     "bills",
	"GET /v1/bills/{billID}":             "bills",
	"GET /v1/ledger/trial-balance":       "books",
	"GET /v1/orders/{orderID}":           "floor",
	"POST /v1/devices/{deviceID}/revoke": "devices",

	"POST /v1/catalogue/categories":                   "menu",
	"POST /v1/catalogue/items":                        "menu",
	"PATCH /v1/catalogue/items/{itemID}":              "menu",
	"PATCH /v1/catalogue/items/{itemID}/availability": "menu",
	"POST /v1/catalogue/modifier-groups":              "menu",
	"POST /v1/catalogue/sections":                     "menu",
	"POST /v1/catalogue/tax-profiles":                 "menu",

	"GET /v1/outlets/{outletID}/menu":                    "floor",
	"GET /v1/outlets/{outletID}/orders":                  "floor",
	"POST /v1/outlets/{outletID}/pairing-codes":          "devices",
	"GET /v1/outlets/{outletID}/devices":                 "devices",
	"POST /v1/outlets/{outletID}/floors":                 "menu",
	"POST /v1/outlets/{outletID}/tables":                 "menu",
	"PATCH /v1/outlets/{outletID}/tables/{tableID}":      "menu",
	"GET /v1/outlets/{outletID}/days/":                   "day",
	"GET /v1/outlets/{outletID}/days/{date}":             "day",
	"POST /v1/outlets/{outletID}/days/{date}/close":      "day",
	"GET /v1/outlets/{outletID}/shifts/":                 "day",
	"POST /v1/outlets/{outletID}/shifts/{shiftID}/close": "day",

	"GET /v1/outlets/{outletID}/stock/":                       "stock",
	"GET /v1/outlets/{outletID}/stock/items/{itemID}":         "stock",
	"GET /v1/outlets/{outletID}/stock/purchases":              "stock",
	"POST /v1/outlets/{outletID}/stock/purchases":             "stock.receive",
	"POST /v1/outlets/{outletID}/stock/wastage":               "stock.receive",
	"POST /v1/outlets/{outletID}/stock/counts":                "stock.manage",
	"GET /v1/outlets/{outletID}/stock/recipes":                "costs",
	"GET /v1/outlets/{outletID}/stock/food-cost":              "costs",
	"GET /v1/outlets/{outletID}/stock/suppliers":              "suppliers",
	"GET /v1/outlets/{outletID}/stock/suppliers/{supplierID}": "suppliers",
	"POST /v1/outlets/{outletID}/stock/supplier-payments":     "suppliers.pay",

	"POST /v1/stock/items":                     "stock.manage",
	"PATCH /v1/stock/items/{itemID}":           "stock.manage",
	"POST /v1/stock/items/{itemID}/packs":      "stock.manage",
	"PATCH /v1/stock/packs/{packID}":           "stock.manage",
	"PUT /v1/stock/recipes/{menuItemID}":       "stock.manage",
	"PUT /v1/stock/addon-recipes/{menuItemID}": "stock.manage",
	"POST /v1/stock/suppliers":                 "stock.manage",
	"PATCH /v1/stock/suppliers/{supplierID}":   "stock.manage",

	"GET /v1/staff/":           "staff",
	"POST /v1/staff/":          "staff",
	"PATCH /v1/staff/{userID}": "staff",
}

func TestEveryRouteNamesWhoMayCallIt(t *testing.T) {
	f := newRestaurant(t, "Permission Inventory", "", "29")
	manager, cashier := testPhone(), testPhone()
	f.addStaff(t, "Priya", manager, "manager")
	f.addStaff(t, "Asha", cashier, "cashier")
	tokens := map[auth.Role]string{
		auth.Owner:   f.OwnerToken,
		auth.Manager: signInAs(t, manager).Token,
		auth.Cashier: signInAs(t, cashier).Token,
	}

	// The inventory: every route is in the table, and the table has no
	// route the API no longer has.
	seen := map[string]bool{}
	var routes []string
	if err := chi.Walk(app.New(appDB).(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		key := method + " " + route
		seen[key] = true
		routes = append(routes, key)
		if _, ok := routeAccess[key]; !ok {
			t.Errorf("%s is not in routeAccess: decide who may call it", key)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for key := range routeAccess {
		if !seen[key] {
			t.Errorf("routeAccess lists %s, which the API does not have", key)
		}
	}
	sort.Strings(routes)

	// Then each role, on each route.
	for _, key := range routes {
		access := routeAccess[key]
		method, route, _ := strings.Cut(key, " ")
		if access == "public" || key == "POST /v1/auth/sign-out" {
			continue
		}
		path := strings.NewReplacer("{outletID}", f.OutletID, "{date}", "2026-10-01").Replace(route)
		for strings.Contains(path, "{") {
			i, j := strings.Index(path, "{"), strings.Index(path, "}")
			path = path[:i] + uuid.NewString() + path[j+1:]
		}
		var body any
		if method != "GET" {
			body = map[string]any{}
		}
		for _, role := range []auth.Role{auth.Owner, auth.Manager, auth.Cashier} {
			var e errBody
			status, _ := call(t, method, path, tokens[role], body, &e)
			allowed := access == "person" || access != "device" && auth.Can(role, auth.Permission(access))
			switch {
			case status == http.StatusUnauthorized:
				t.Errorf("%s as %s: 401 (the session should be good)", key, role)
			case allowed && status == http.StatusForbidden:
				t.Errorf("%s as %s: refused, but %s may %s", key, role, role, access)
			case !allowed && (status != http.StatusForbidden || e.Error.Code != "forbidden"):
				t.Errorf("%s as %s: %d %s, want 403 forbidden (%s)", key, role, status, e.Error.Code, access)
			}
		}
	}
}

// Every outlet route answers 404 for another business's outlet, as if it
// did not exist (RLS hides it): never an empty page that looks like an
// outlet with nothing in it.
func TestAnotherBusinessesOutletIsNotFound(t *testing.T) {
	mine := newRestaurant(t, "Outlet Owner", "", "29")
	theirs := newRestaurant(t, "Next Door Outlet", "", "29")
	if err := chi.Walk(app.New(appDB).(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method != "GET" || !strings.Contains(route, "{outletID}") {
			return nil
		}
		path := strings.NewReplacer("{outletID}", theirs.OutletID, "{date}", "2026-10-01").Replace(route)
		for strings.Contains(path, "{") {
			i, j := strings.Index(path, "{"), strings.Index(path, "}")
			path = path[:i] + uuid.NewString() + path[j+1:]
		}
		if status, _ := call(t, "GET", path, mine.OwnerToken, nil, nil); status != http.StatusNotFound {
			t.Errorf("GET %s with another business's outlet: %d, want 404", route, status)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
