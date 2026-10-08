package auth

import (
	"net/http"

	"1xn/core/internal/platform/httpx"
)

// Role is what a person is in a restaurant.
type Role string

const (
	Owner   Role = "owner"   // everything, including staff, the books and paying suppliers
	Manager Role = "manager" // runs the restaurant: menu, devices, day close, stock
	Cashier Role = "cashier" // the floor, bills, and goods at the back door
)

// Permission is one thing a person may do in the back office. Every route a
// person can call names the permission it needs (Need); the table below is
// the only place that says which roles have it. Role-based access control,
// with the rules in one place to read and to test.
type Permission string

const (
	Floor        Permission = "floor"         // the live floor, orders, the menu as served
	Bills        Permission = "bills"         // the day's bills, a bill
	Menu         Permission = "menu"          // change the menu, floors and tables
	Devices      Permission = "devices"       // pair and revoke tills, phones, screens
	Day          Permission = "day"           // Z reports, closing a day, closing a revoked till's shift
	Stock        Permission = "stock"         // see stock and its history
	StockReceive Permission = "stock.receive" // receive goods (on credit or from a till), record wastage
	CashOut      Permission = "cash.out"      // book goods as paid from the safe or the bank
	StockManage  Permission = "stock.manage"  // items, packs, recipes, counts, suppliers' details
	Costs        Permission = "costs"         // recipe costing and food cost
	Suppliers    Permission = "suppliers"     // what suppliers are owed, their statements
	PaySuppliers Permission = "suppliers.pay" // pay a supplier
	Alerts       Permission = "alerts"        // the alert list
	Books        Permission = "books"         // the ledger
	Staff        Permission = "staff"         // add, change and switch off people
)

var everyone = []Role{Owner, Manager, Cashier}
var managers = []Role{Owner, Manager}
var owner = []Role{Owner}

var matrix = map[Permission][]Role{
	Floor:        everyone,
	Bills:        everyone,
	Stock:        everyone,
	StockReceive: everyone,
	Menu:         managers,
	CashOut:      managers,
	Devices:      managers,
	Day:          managers,
	StockManage:  managers,
	Costs:        managers,
	Suppliers:    managers,
	Alerts:       managers,
	PaySuppliers: owner,
	Books:        owner,
	Staff:        owner,
}

// AllPermissions lists every permission, in a stable order.
var AllPermissions = []Permission{Floor, Bills, Menu, Devices, Day, Stock, StockReceive, CashOut, StockManage,
	Costs, Suppliers, PaySuppliers, Alerts, Books, Staff}

// Can reports whether a role has a permission. Unknown roles have none.
func Can(r Role, p Permission) bool {
	for _, x := range matrix[p] {
		if x == r {
			return true
		}
	}
	return false
}

// PermissionsOf lists what a role may do (the back office shows only that).
func PermissionsOf(r Role) []Permission {
	out := []Permission{}
	for _, p := range AllPermissions {
		if Can(r, p) {
			out = append(out, p)
		}
	}
	return out
}

// ValidRole reports whether r is a role a person can have.
func ValidRole(r Role) bool { return r == Owner || r == Manager || r == Cashier }

// Need lets a request through only if the caller is a person whose role has
// the permission: 403 otherwise. Use it behind Middleware.
func Need(perm Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := From(r.Context())
			if !ok || p.Kind == Device || !Can(p.Role, perm) {
				httpx.Fail(w, r, httpx.E(http.StatusForbidden, "forbidden",
					"your role cannot do this ("+string(perm)+"); ask the owner"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
