package e2e

// Regression tests from the adversarial review of the back-office slice
// (GET /v1/me, outlet menu, item PATCH, table PATCH, device list/revoke, day
// bills list). The first five each reproduced a real bug; their comments
// describe it as found. The rest record attacks that were tried and held.

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"1xn/core/internal/platform/gst"
	"1xn/core/internal/platform/ids"
)

func menuVersion(t *testing.T, f *fixture) int64 {
	t.Helper()
	var m struct {
		Version int64 `json:"version"`
	}
	must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/menu", f.OwnerToken, nil, &m)
	return m.Version
}

// FINDING: itemPatch.validate counts BYTES (len) while the DB CHECK and POST
// /items count CHARACTERS. A dish created with a 100-character Hindi name
// (300 bytes) can never be edited from the back office: the edit form always
// re-sends the name, and the PATCH answers 400.
func TestPatchItemNameLimitCountsCharacters(t *testing.T) {
	f := newRestaurant(t, "Hindi Menu Bhojanalaya", "", "29")
	name := strings.Repeat("प", 100) // 100 characters, 300 bytes in UTF-8
	var it struct {
		ID string `json:"id"`
	}
	must(t, http.StatusCreated, "POST", "/v1/catalogue/items", f.OwnerToken, map[string]any{
		"category_id": f.CategoryID, "tax_profile_id": f.TaxProfileID, "name": name, "price_paise": 12000,
	}, &it)
	// Exactly the body the back office's "Save changes" sends for a price edit.
	must(t, http.StatusOK, "PATCH", "/v1/catalogue/items/"+it.ID, f.OwnerToken, map[string]any{
		"name": name, "price_paise": 13000, "category_id": f.CategoryID, "tax_profile_id": f.TaxProfileID,
		"section_id": nil, "modifier_group_ids": []string{},
	}, nil)
}

// FINDING: a NUL character in a PATCHed name/label reaches Postgres and comes
// back as a 500 ("null character not permitted"), not a 400.
func TestPatchWithNULIsBadRequest(t *testing.T) {
	f := newRestaurant(t, "Nul Naan", "", "29")
	k := setupFloor(t, f)
	t.Run("item name", func(t *testing.T) {
		must(t, http.StatusBadRequest, "PATCH", "/v1/catalogue/items/"+k.Paneer.ID, f.OwnerToken,
			map[string]any{"name": "Paneer\u0000tikka"}, nil)
	})
	t.Run("table label", func(t *testing.T) {
		must(t, http.StatusBadRequest, "PATCH", "/v1/outlets/"+f.OutletID+"/tables/"+k.T5, f.OwnerToken,
			map[string]any{"label": "T\u00005"}, nil)
	})
}

// FINDING: GET /v1/bills accepts any date Go's parser accepts. Year 0000 is a
// valid Go date but not a Postgres one, so the handler answers 500.
func TestBillsListRefusesYearZero(t *testing.T) {
	f := newRestaurant(t, "Year Zero Cafe", "", "29")
	must(t, http.StatusBadRequest, "GET", "/v1/bills?outlet_id="+f.OutletID+"&date=0000-01-01", f.OwnerToken, nil, nil)
}

// FINDING (seed + browser test): cmd/seed issued its evening's bills with
// each bill's own IST date as the business date, so a seed run between 00:00
// and 01:30 IST split the evening across two business days and the browser
// test found 0 of 3 bills on "today". The seed now gives the whole evening
// one business day, the IST date of two hours ago. This checks the API takes
// that: bills issued after midnight may carry the day before.
func TestOneBusinessDayAcrossMidnight(t *testing.T) {
	f := newRestaurant(t, "Midnight Dhaba", "", "29")
	now := time.Date(2026, 9, 29, 1, 10, 0, 0, gst.IST) // seed run at 01:10 IST
	at := func(minAgo int) time.Time { return now.Add(-time.Duration(minAgo) * time.Minute) }
	day := now.Add(-2 * time.Hour).Format(time.DateOnly) // 2026-09-28, as cmd/seed does
	f.push(t,
		f.bill(t, pavBhaji(2), billOpts{issuedAt: at(90), businessDate: day}), // 23:40 on the 28th
		f.bill(t, pavBhaji(1), billOpts{issuedAt: at(70), businessDate: day}), // 00:00 on the 29th
		f.bill(t, pavBhaji(1), billOpts{issuedAt: at(60), businessDate: day}), // 00:10 on the 29th
	)
	var got struct {
		Count int `json:"count"`
	}
	must(t, http.StatusOK, "GET", "/v1/bills?outlet_id="+f.OutletID+"&date="+day, f.OwnerToken, nil, &got)
	if got.Count != 3 {
		t.Fatalf("the evening shows %d of 3 bills on its business day", got.Count)
	}
}

// FINDING (low): a no-op PATCH (and every back-office "Save changes", which
// re-sends every field) bumps the catalogue version, so every till in the
// tenant re-downloads the whole menu although nothing changed.
func TestNoopPatchKeepsMenuVersion(t *testing.T) {
	f := newRestaurant(t, "Idle Edits Inn", "", "29")
	k := setupFloor(t, f)
	v0 := menuVersion(t, f)
	must(t, http.StatusOK, "PATCH", "/v1/catalogue/items/"+f.ItemID, f.OwnerToken, map[string]any{}, nil)
	must(t, http.StatusOK, "PATCH", "/v1/outlets/"+f.OutletID+"/tables/"+k.T4, f.OwnerToken, map[string]any{}, nil)
	if v1 := menuVersion(t, f); v1 != v0 {
		t.Fatalf("catalogue version went %d -> %d for a PATCH that changed nothing", v0, v1)
	}
}

// ---------------------------------------------------------------------------
// Attacks that held.

func TestBackOfficeCrossTenantAndCrossOutletRefs(t *testing.T) {
	a := newRestaurant(t, "Tenant A Tiffins", "", "29")
	ka := setupFloor(t, a)
	b := newRestaurant(t, "Tenant B Bhavan", "", "29")
	kb := setupFloor(t, b)

	// Item PATCH pointing at B's category / tax profile / section / modifier group.
	for name, body := range map[string]map[string]any{
		"category":       {"category_id": b.CategoryID},
		"tax profile":    {"tax_profile_id": b.TaxProfileID},
		"section":        {"section_id": kb.Tandoor},
		"modifier group": {"modifier_group_ids": []string{kb.AddOns}},
	} {
		t.Run("foreign "+name, func(t *testing.T) {
			must(t, http.StatusBadRequest, "PATCH", "/v1/catalogue/items/"+ka.Paneer.ID, a.OwnerToken, body, nil)
		})
	}
	// B's ids through A's token.
	must(t, http.StatusNotFound, "PATCH", "/v1/outlets/"+b.OutletID+"/tables/"+kb.T4, a.OwnerToken, map[string]any{"seats": 9}, nil)
	must(t, http.StatusNotFound, "PATCH", "/v1/outlets/"+a.OutletID+"/tables/"+kb.T4, a.OwnerToken, map[string]any{"seats": 9}, nil)
	must(t, http.StatusNotFound, "GET", "/v1/outlets/"+b.OutletID+"/menu", a.OwnerToken, nil, nil)
	must(t, http.StatusNotFound, "POST", "/v1/devices/"+b.DeviceID+"/revoke", a.OwnerToken, nil, nil)
	op := b.bill(t, pavBhaji(1), billOpts{})
	b.push(t, op)
	must(t, http.StatusNotFound, "GET", "/v1/bills/"+billIDOf(t, op), a.OwnerToken, nil, nil)

	// Device tokens (live and revoked) on every new owner route.
	captain := a.pair(t, "captain", "Captain X")
	must(t, http.StatusOK, "POST", "/v1/devices/"+captain.DeviceID+"/revoke", a.OwnerToken, nil, nil)
	for _, tok := range []struct {
		tok  string
		want int
	}{{a.DeviceToken, http.StatusForbidden}, {captain.DeviceToken, http.StatusUnauthorized}} {
		must(t, tok.want, "GET", "/v1/me", tok.tok, nil, nil)
		must(t, tok.want, "GET", "/v1/outlets/"+a.OutletID+"/menu", tok.tok, nil, nil)
		must(t, tok.want, "GET", "/v1/outlets/"+a.OutletID+"/devices", tok.tok, nil, nil)
		must(t, tok.want, "PATCH", "/v1/catalogue/items/"+ka.Paneer.ID, tok.tok, map[string]any{"price_paise": 1}, nil)
		must(t, tok.want, "PATCH", "/v1/outlets/"+a.OutletID+"/tables/"+ka.T4, tok.tok, map[string]any{"seats": 1}, nil)
		must(t, tok.want, "GET", "/v1/bills?outlet_id="+a.OutletID, tok.tok, nil, nil)
	}

	// Same tenant, second outlet: a table of outlet 1 cannot be patched via outlet 2's URL.
	second := ids.New()
	if _, err := ownerDB.Exec(`INSERT INTO outlet (id, tenant_id, legal_entity_id, name, state_code)
		SELECT $1, tenant_id, legal_entity_id, 'Second outlet', state_code FROM outlet WHERE id = $2`, second, a.OutletID); err != nil {
		t.Fatal(err)
	}
	must(t, http.StatusNotFound, "PATCH", "/v1/outlets/"+second.String()+"/tables/"+ka.T4, a.OwnerToken, map[string]any{"label": "Z1"}, nil)
	var me struct {
		Outlets []struct {
			ID string `json:"id"`
		} `json:"outlets"`
	}
	must(t, http.StatusOK, "GET", "/v1/me", a.OwnerToken, nil, &me)
	if len(me.Outlets) != 2 || me.Outlets[0].ID != a.OutletID {
		t.Fatalf("me lists both outlets, oldest first: %+v", me)
	}
}

func TestBackOfficeBadInputIs4xx(t *testing.T) {
	f := newRestaurant(t, "Fuzz Food", "", "29")
	k := setupFloor(t, f)
	item := "/v1/catalogue/items/" + k.Paneer.ID
	for name, body := range map[string]any{
		"section empty string":  map[string]any{"section_id": ""},
		"price float":           map[string]any{"price_paise": 1.5},
		"price too big":         map[string]any{"price_paise": 1_000_000_001},
		"channel price too big": map[string]any{"channel_prices": map[string]int64{"swiggy": 1_000_000_001}},
		"21 groups": map[string]any{"modifier_group_ids": func() []string {
			s := []string{}
			for i := 0; i < 21; i++ {
				s = append(s, ids.New().String())
			}
			return s
		}()},
		"name too long": map[string]any{"name": strings.Repeat("x", 201)},
		"not an object": []int{1},
	} {
		t.Run(name, func(t *testing.T) { must(t, http.StatusBadRequest, "PATCH", item, f.OwnerToken, body, nil) })
	}
	for name, body := range map[string]map[string]any{
		"seats 51":      {"seats": 51},
		"label 21":      {"label": strings.Repeat("x", 21)},
		"label blank":   {"label": "  "},
		"active string": {"active": "no"},
	} {
		t.Run("table "+name, func(t *testing.T) {
			must(t, http.StatusBadRequest, "PATCH", "/v1/outlets/"+f.OutletID+"/tables/"+k.T4, f.OwnerToken, body, nil)
		})
	}
	must(t, http.StatusBadRequest, "GET", "/v1/bills?outlet_id="+f.OutletID+"&date=2026-02-30", f.OwnerToken, nil, nil)
	must(t, http.StatusNotFound, "PATCH", "/v1/catalogue/items/not-a-uuid", f.OwnerToken, map[string]any{}, nil)
	must(t, http.StatusNotFound, "POST", "/v1/devices/not-a-uuid/revoke", f.OwnerToken, nil, nil)
	must(t, http.StatusNotFound, "POST", "/v1/devices/"+ids.New().String()+"/revoke", f.OwnerToken, nil, nil)
}

// Ten tables renamed to the same label at once: one wins, the rest get 409,
// nobody gets a 500. Item PATCHes racing menu reads never expose a half-written
// modifier list.
func TestBackOfficeConcurrentPatches(t *testing.T) {
	f := newRestaurant(t, "Race Condition Restobar", "", "29")
	k := setupFloor(t, f)
	var floorID string
	var m struct {
		Floors []struct {
			ID string `json:"id"`
		} `json:"floors"`
	}
	must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/menu", f.OwnerToken, nil, &m)
	floorID = m.Floors[0].ID
	tables := []string{k.T4, k.T5}
	for i := 0; i < 8; i++ {
		var id struct {
			ID string `json:"id"`
		}
		must(t, http.StatusCreated, "POST", "/v1/outlets/"+f.OutletID+"/tables", f.OwnerToken,
			map[string]any{"floor_id": floorID, "label": "X" + string(rune('A'+i))}, &id)
		tables = append(tables, id.ID)
	}
	codes := make(chan int, len(tables))
	var wg sync.WaitGroup
	for _, tbl := range tables {
		wg.Add(1)
		go func(tbl string) {
			defer wg.Done()
			s, _ := call(t, "PATCH", "/v1/outlets/"+f.OutletID+"/tables/"+tbl, f.OwnerToken, map[string]any{"label": "VIP"}, nil)
			codes <- s
		}(tbl)
	}
	wg.Wait()
	close(codes)
	got := map[int]int{}
	for c := range codes {
		got[c]++
	}
	if got[http.StatusOK] != 1 || got[http.StatusConflict] != len(tables)-1 {
		t.Fatalf("label race: %v", got)
	}

	// PATCH modifier lists while reading the menu.
	var g2 struct {
		ID string `json:"id"`
	}
	must(t, http.StatusCreated, "POST", "/v1/catalogue/modifier-groups", f.OwnerToken, map[string]any{
		"name": "Spice", "options": []map[string]any{{"name": "Mild"}, {"name": "Hot"}}}, &g2)
	stop := make(chan struct{})
	bad := make(chan string, 100)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			list := []string{k.AddOns, g2.ID}
			if i%2 == 1 {
				list = []string{g2.ID}
			}
			if s, _ := call(t, "PATCH", "/v1/catalogue/items/"+k.Paneer.ID, f.OwnerToken, map[string]any{"modifier_group_ids": list}, nil); s != 200 {
				bad <- "patch status " + http.StatusText(s)
			}
		}
		close(stop)
	}()
	for done := false; !done; {
		select {
		case <-stop:
			done = true
		default:
			var menu struct {
				Items []struct {
					ID     string   `json:"id"`
					Groups []string `json:"modifier_group_ids"`
				} `json:"items"`
			}
			if s, _ := call(t, "GET", "/v1/outlets/"+f.OutletID+"/menu", f.OwnerToken, nil, &menu); s != 200 {
				bad <- "menu status " + http.StatusText(s)
			}
			for _, it := range menu.Items {
				if it.ID == k.Paneer.ID && len(it.Groups) != 1 && len(it.Groups) != 2 {
					bad <- "torn modifier list"
				}
			}
		}
	}
	wg.Wait()
	close(bad)
	for b := range bad {
		t.Error(b)
	}
}
