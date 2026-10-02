package e2e

import (
	"encoding/json"
	"net/http"
	"testing"

	"1xn/core/internal/platform/ids"
	"1xn/core/modules/sales"
	"1xn/core/modules/syncgw"
)

// The owner endpoints the back office web app reads and writes.

func TestMeAndOutletMenu(t *testing.T) {
	f := newRestaurant(t, "Back Office Bistro", "", "29")
	k := setupFloor(t, f)

	var me struct {
		BusinessName string `json:"business_name"`
		User         struct {
			Name string `json:"name"`
			Role string `json:"role"`
		} `json:"user"`
		Outlets []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"outlets"`
	}
	must(t, http.StatusOK, "GET", "/v1/me", f.OwnerToken, nil, &me)
	if me.BusinessName != "Back Office Bistro" || me.User.Role != "owner" || len(me.Outlets) != 1 || me.Outlets[0].ID != f.OutletID {
		t.Fatalf("me: %+v", me)
	}
	// A device token is not an owner.
	must(t, http.StatusForbidden, "GET", "/v1/me", f.DeviceToken, nil, nil)

	var menu struct {
		Version  int64 `json:"version"`
		Sections []struct {
			Name string `json:"name"`
		} `json:"sections"`
		Floors []struct {
			Tables []struct {
				Label string `json:"label"`
			} `json:"tables"`
		} `json:"floors"`
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/menu", f.OwnerToken, nil, &menu)
	if len(menu.Sections) != 2 || len(menu.Floors) != 1 || len(menu.Floors[0].Tables) != 2 || len(menu.Items) != 4 {
		t.Fatalf("owner menu: %+v", menu)
	}
	_ = k

	// Another tenant's outlet does not exist for this owner.
	other := newRestaurant(t, "Someone Else", "", "29")
	must(t, http.StatusNotFound, "GET", "/v1/outlets/"+other.OutletID+"/menu", f.OwnerToken, nil, nil)
	must(t, http.StatusNotFound, "GET", "/v1/outlets/"+other.OutletID+"/devices", f.OwnerToken, nil, nil)
}

func TestUpdateItem(t *testing.T) {
	f := newRestaurant(t, "Price Change Cafe", "", "29")
	k := setupFloor(t, f)

	type item struct {
		ID               string           `json:"id"`
		Name             string           `json:"name"`
		PricePaise       int64            `json:"price_paise"`
		SectionID        *string          `json:"section_id"`
		ModifierGroupIDs []string         `json:"modifier_group_ids"`
		ChannelPrices    map[string]int64 `json:"channel_prices"`
		Available        bool             `json:"available"`
	}
	find := func() item {
		var menu struct {
			Version int64  `json:"version"`
			Items   []item `json:"items"`
		}
		must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/menu", f.OwnerToken, nil, &menu)
		for _, it := range menu.Items {
			if it.ID == k.Paneer.ID {
				return it
			}
		}
		t.Fatal("paneer is gone")
		return item{}
	}

	before := find()
	if before.SectionID == nil || len(before.ModifierGroupIDs) != 1 {
		t.Fatalf("setup: %+v", before)
	}
	// Absent fields stay as they are.
	must(t, http.StatusOK, "PATCH", "/v1/catalogue/items/"+k.Paneer.ID, f.OwnerToken,
		map[string]any{"price_paise": 34000, "name": "  Paneer tikka (8 pcs) "}, nil)
	got := find()
	if got.PricePaise != 34000 || got.Name != "Paneer tikka (8 pcs)" || got.SectionID == nil || len(got.ModifierGroupIDs) != 1 {
		t.Fatalf("price and name change only: %+v", got)
	}
	// null clears the section; lists are replaced whole.
	must(t, http.StatusOK, "PATCH", "/v1/catalogue/items/"+k.Paneer.ID, f.OwnerToken,
		map[string]any{"section_id": nil, "modifier_group_ids": []string{}, "channel_prices": map[string]int64{"zomato": 39000}}, nil)
	got = find()
	if got.SectionID != nil || len(got.ModifierGroupIDs) != 0 || got.ChannelPrices["zomato"] != 39000 || got.PricePaise != 34000 {
		t.Fatalf("clear section, replace lists: %+v", got)
	}

	for name, body := range map[string]map[string]any{
		"negative price":  {"price_paise": -1},
		"empty name":      {"name": "   "},
		"unknown channel": {"channel_prices": map[string]int64{"ubereats": 100}},
		"duplicate group": {"modifier_group_ids": []string{k.AddOns, k.AddOns}},
		"unknown field":   {"colour": "red"},
	} {
		t.Run(name, func(t *testing.T) {
			must(t, http.StatusBadRequest, "PATCH", "/v1/catalogue/items/"+k.Paneer.ID, f.OwnerToken, body, nil)
		})
	}
	// References must exist in this tenant.
	must(t, http.StatusBadRequest, "PATCH", "/v1/catalogue/items/"+k.Paneer.ID, f.OwnerToken,
		map[string]any{"section_id": ids.New()}, nil)
	must(t, http.StatusNotFound, "PATCH", "/v1/catalogue/items/"+ids.New().String(), f.OwnerToken,
		map[string]any{"price_paise": 100}, nil)

	other := newRestaurant(t, "Other Kitchen", "", "29")
	must(t, http.StatusNotFound, "PATCH", "/v1/catalogue/items/"+k.Paneer.ID, other.OwnerToken,
		map[string]any{"price_paise": 1}, nil)
	if got := find(); got.PricePaise != 34000 {
		t.Fatal("another tenant changed the price")
	}
}

func TestUpdateTable(t *testing.T) {
	f := newRestaurant(t, "Table Talk", "", "29")
	k := setupFloor(t, f)

	var tbl struct {
		Label  string `json:"label"`
		Seats  int    `json:"seats"`
		Active bool   `json:"active"`
	}
	must(t, http.StatusOK, "PATCH", "/v1/outlets/"+f.OutletID+"/tables/"+k.T5, f.OwnerToken,
		map[string]any{"seats": 6, "active": false}, &tbl)
	if tbl.Label != "T5" || tbl.Seats != 6 || tbl.Active {
		t.Fatalf("patched table: %+v", tbl)
	}
	must(t, http.StatusConflict, "PATCH", "/v1/outlets/"+f.OutletID+"/tables/"+k.T5, f.OwnerToken,
		map[string]any{"label": "T4"}, nil)
	must(t, http.StatusBadRequest, "PATCH", "/v1/outlets/"+f.OutletID+"/tables/"+k.T5, f.OwnerToken,
		map[string]any{"seats": 0}, nil)
	must(t, http.StatusNotFound, "PATCH", "/v1/outlets/"+ids.New().String()+"/tables/"+k.T5, f.OwnerToken,
		map[string]any{"seats": 2}, nil)

	// A captain who seated guests at T5 before hearing it was switched off.
	o := ids.New()
	f.apply(t, f.op("order.opened", opened(o, k.T5, 2)))
	if f.alerts(t)["inactive_table"] != 1 {
		t.Fatal("an order on a switched-off table is kept and flagged")
	}
}

func TestDevicesListAndRevoke(t *testing.T) {
	f := newRestaurant(t, "Lost Phone Dhaba", "", "29")
	captain := f.pair(t, "captain", "Captain Anil")

	var list struct {
		Devices []struct {
			ID            string  `json:"id"`
			Name          string  `json:"name"`
			Role          string  `json:"role"`
			InvoicePrefix string  `json:"invoice_prefix"`
			RevokedAt     *string `json:"revoked_at"`
		} `json:"devices"`
	}
	must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/devices", f.OwnerToken, nil, &list)
	if len(list.Devices) != 2 || list.Devices[0].Name != "Counter 1" || list.Devices[1].Role != "captain" {
		t.Fatalf("devices: %+v", list)
	}

	// The captain's phone is lost: revoke it. Its token stops working at once.
	must(t, http.StatusOK, "POST", "/v1/devices/"+captain.DeviceID+"/revoke", f.OwnerToken, nil, nil)
	must(t, http.StatusOK, "POST", "/v1/devices/"+captain.DeviceID+"/revoke", f.OwnerToken, nil, nil) // twice is fine
	must(t, http.StatusUnauthorized, "GET", "/v1/devices/me/menu", captain.DeviceToken, nil, nil)
	must(t, http.StatusOK, "GET", "/v1/devices/me/menu", f.DeviceToken, nil, nil)

	must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/devices", f.OwnerToken, nil, &list)
	if list.Devices[0].RevokedAt != nil || list.Devices[1].RevokedAt == nil {
		t.Fatalf("revoked devices sort last and show revoked_at: %+v", list)
	}

	// Owners cannot revoke other tenants' devices, and devices cannot revoke anything.
	other := newRestaurant(t, "Other Dhaba", "", "29")
	must(t, http.StatusNotFound, "POST", "/v1/devices/"+f.DeviceID+"/revoke", other.OwnerToken, nil, nil)
	must(t, http.StatusForbidden, "POST", "/v1/devices/"+other.DeviceID+"/revoke", f.DeviceToken, nil, nil)
	must(t, http.StatusOK, "GET", "/v1/devices/me/menu", f.DeviceToken, nil, nil)
}

func TestDayBillsList(t *testing.T) {
	f := newRestaurant(t, "Ledger Lounge", "", "29")
	upi := f.bill(t, pavBhaji(2), billOpts{payments: []sales.Payment{{Method: "upi", AmountPaise: 20000}}})
	cash := f.bill(t, pavBhaji(1), billOpts{})
	split := f.bill(t, pavBhaji(1), billOpts{payments: []sales.Payment{
		{Method: "cash", AmountPaise: 4000}, {Method: "card", AmountPaise: 6000, Reference: "AUTH123"}}})
	f.push(t, upi, cash, split)

	var day struct {
		BusinessDate string `json:"business_date"`
		Count        int    `json:"count"`
		GrandTotal   int64  `json:"grand_total"`
		Bills        []struct {
			ID        string `json:"id"`
			InvoiceNo string `json:"invoice_no"`
		} `json:"bills"`
		ByMethod map[string]int64 `json:"by_method"`
	}
	must(t, http.StatusOK, "GET", "/v1/bills?outlet_id="+f.OutletID+"&date=2026-09-28", f.OwnerToken, nil, &day)
	if day.Count != 3 || len(day.Bills) != 3 || day.GrandTotal != 40000 ||
		day.ByMethod["upi"] != 20000 || day.ByMethod["cash"] != 14000 || day.ByMethod["card"] != 6000 {
		t.Fatalf("day: %+v", day)
	}
	must(t, http.StatusOK, "GET", "/v1/bills?outlet_id="+f.OutletID+"&date=2026-09-27", f.OwnerToken, nil, &day)
	if day.Count != 0 || len(day.Bills) != 0 {
		t.Fatalf("another day is empty: %+v", day)
	}
	must(t, http.StatusBadRequest, "GET", "/v1/bills?outlet_id="+f.OutletID+"&date=28-09-2026", f.OwnerToken, nil, nil)
	must(t, http.StatusBadRequest, "GET", "/v1/bills", f.OwnerToken, nil, nil)

	// The bill view carries lines and payments for the bill page.
	var v struct {
		InvoiceNo  string `json:"invoice_no"`
		TableLabel string `json:"table_label"`
		Lines      []struct {
			Name string `json:"name"`
			Qty  int64  `json:"qty"`
		} `json:"lines"`
		Payments []struct {
			Method    string `json:"method"`
			Amount    int64  `json:"amount"`
			Reference string `json:"reference"`
		} `json:"payments"`
	}
	billID := billIDOf(t, split)
	must(t, http.StatusOK, "GET", "/v1/bills/"+billID, f.OwnerToken, nil, &v)
	if v.TableLabel != "T4" || len(v.Lines) != 1 || v.Lines[0].Name != "Pav bhaji" || len(v.Payments) != 2 ||
		v.Payments[1].Reference != "AUTH123" {
		t.Fatalf("bill view: %+v", v)
	}

	// Tenant isolation: another owner's outlet does not exist for this owner.
	other := newRestaurant(t, "Nosy Neighbour", "", "29")
	must(t, http.StatusNotFound, "GET", "/v1/bills?outlet_id="+f.OutletID+"&date=2026-09-28", other.OwnerToken, nil, nil)
	must(t, http.StatusBadRequest, "GET", "/v1/bills?outlet_id="+f.OutletID+"&date=0000-01-01", f.OwnerToken, nil, nil)
}

func billIDOf(t *testing.T, op syncgw.Op) string {
	t.Helper()
	var b sales.SettledBill
	if err := json.Unmarshal(op.Payload, &b); err != nil {
		t.Fatal(err)
	}
	return b.BillID
}
