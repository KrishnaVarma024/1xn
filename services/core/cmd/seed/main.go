// Command seed creates a demo restaurant with a real-looking evening in it,
// through the public API (exactly what real devices do), and prints the owner
// token so you can sign in to the back office.
//
//	make run    # in one terminal
//	make seed   # in another; API_URL defaults to http://localhost:8080
//
// Every run creates a NEW restaurant; nothing is deleted.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"

	"1xn/core/internal/platform/gst"
	"1xn/core/internal/platform/ids"
	"1xn/core/modules/sales"
	"1xn/core/modules/sales/pricing"
	"1xn/core/modules/syncgw"
)

var base = "http://localhost:8080"

func main() {
	if u := os.Getenv("API_URL"); u != "" {
		base = u
	}
	token, day, err := seed()
	if err != nil {
		fmt.Fprintln(os.Stderr, "\nseed failed:", err)
		os.Exit(1)
	}
	fmt.Printf(`
Demo restaurant ready. Its bills are on business day %s.

  Owner token (treat it like a password; it is shown only here):

    %s

  Sign in to the back office: http://localhost:3000/sign-in
`, day, token)
}

type ref struct {
	ID string `json:"id"`
}

type menuItem struct {
	ID      string
	Name    string
	Price   int64
	Section string
}

// device is a paired device and its own op sequence and invoice counter.
type device struct {
	ID, Token, Prefix string
	seq               int64
	invoice           int
}

func (d *device) op(typ string, payload any) syncgw.Op {
	b, _ := json.Marshal(payload)
	d.seq++
	return syncgw.Op{OpID: ids.New().String(), DeviceSeq: d.seq, Type: typ, Payload: b}
}

func (d *device) push(ops ...syncgw.Op) error {
	var res syncgw.PushResponse
	if err := call("POST", "/v1/sync/push", d.Token, syncgw.PushRequest{Ops: ops}, &res); err != nil {
		return err
	}
	for i, a := range res.Acks {
		if a.Status == "rejected" {
			return fmt.Errorf("op %d (%s) rejected: %+v", i, ops[i].Type, a.Error)
		}
	}
	return nil
}

// line is one dish on an order: what the captain sends and what the bill
// will charge.
type line struct {
	ID   uuid.UUID
	Item menuItem
	Qty  int64
	Mods []pricing.Modifier
}

func (l line) payload() map[string]any {
	mods := []map[string]any{}
	for _, m := range l.Mods {
		mods = append(mods, map[string]any{"name": m.Name, "price_paise": m.PricePaise})
	}
	return map[string]any{"line_id": l.ID, "item_id": l.Item.ID, "name": l.Item.Name, "qty": l.Qty,
		"unit_price_paise": l.Item.Price, "tax_rate_bp": 500, "section_id": l.Item.Section, "course": 1, "modifiers": mods}
}

func seed() (token, businessDay string, err error) {
	now := time.Now().In(gst.IST)
	// One business day for the whole evening, even when the seed runs just
	// after midnight: a bill may carry its issue date or the day before.
	businessDay = now.Add(-2 * time.Hour).Format(time.DateOnly)
	fmt.Println("Creating a demo restaurant through the API...")

	var signup struct {
		OutletID   string `json:"outlet_id"`
		OwnerToken string `json:"owner_token"`
	}
	if err := call("POST", "/v1/signup", "", map[string]any{
		"business_name": "Demo Dhaba " + now.Format("0102-1504"),
		"owner":         map[string]any{"name": "Ravi Kumar", "phone": "+919876543210"},
		"legal_entity":  map[string]any{"name": "Demo Dhaba Foods Pvt Ltd", "gstin": "27AAPFU0939F1ZV"},
		"outlet":        map[string]any{"name": "Bandra"},
	}, &signup); err != nil {
		return "", "", err
	}
	owner, outlet := signup.OwnerToken, signup.OutletID
	post := func(path string, body any, out any) error { return call("POST", path, owner, body, out) }

	// Menu.
	var tax, starters, mains, breads, drinks, tandoor, kitchen, bar, addOns, spice ref
	steps := []struct {
		path string
		body any
		out  *ref
	}{
		{"/v1/catalogue/tax-profiles", map[string]any{"name": "Restaurant service 5%", "rate_bp": 500, "hsn_sac": "996331"}, &tax},
		{"/v1/catalogue/categories", map[string]any{"name": "Starters", "sort": 1}, &starters},
		{"/v1/catalogue/categories", map[string]any{"name": "Mains", "sort": 2}, &mains},
		{"/v1/catalogue/categories", map[string]any{"name": "Breads", "sort": 3}, &breads},
		{"/v1/catalogue/categories", map[string]any{"name": "Drinks", "sort": 4}, &drinks},
		{"/v1/catalogue/sections", map[string]any{"name": "Tandoor", "sort": 1}, &tandoor},
		{"/v1/catalogue/sections", map[string]any{"name": "Kitchen", "sort": 2}, &kitchen},
		{"/v1/catalogue/sections", map[string]any{"name": "Bar", "sort": 3}, &bar},
		{"/v1/catalogue/modifier-groups", map[string]any{"name": "Add-ons", "min_select": 0, "max_select": 3,
			"options": []map[string]any{{"name": "Extra cheese", "price_paise": 4000}, {"name": "Extra butter", "price_paise": 2000}}}, &addOns},
		{"/v1/catalogue/modifier-groups", map[string]any{"name": "Spice level", "min_select": 1, "max_select": 1,
			"options": []map[string]any{{"name": "Mild", "price_paise": 0}, {"name": "Medium", "price_paise": 0}, {"name": "Hot", "price_paise": 0}}}, &spice},
	}
	for _, s := range steps {
		if err := post(s.path, s.body, s.out); err != nil {
			return "", "", err
		}
	}
	item := func(name string, price int64, cat, section ref, groups ...string) (menuItem, error) {
		var r ref
		body := map[string]any{"category_id": cat.ID, "tax_profile_id": tax.ID, "name": name, "price_paise": price,
			"section_id": section.ID, "modifier_group_ids": groups}
		if err := post("/v1/catalogue/items", body, &r); err != nil {
			return menuItem{}, err
		}
		return menuItem{ID: r.ID, Name: name, Price: price, Section: section.ID}, nil
	}
	var items = map[string]menuItem{}
	for _, d := range []struct {
		name    string
		price   int64
		cat     ref
		section ref
		groups  []string
	}{
		{"Paneer tikka", 32000, starters, tandoor, []string{addOns.ID, spice.ID}},
		{"Chicken 65", 34000, starters, kitchen, []string{spice.ID}},
		{"Dal makhani", 28000, mains, kitchen, []string{addOns.ID}},
		{"Butter chicken", 42000, mains, kitchen, []string{addOns.ID, spice.ID}},
		{"Veg biryani", 30000, mains, kitchen, []string{spice.ID}},
		{"Butter naan", 6000, breads, tandoor, nil},
		{"Garlic naan", 7000, breads, tandoor, nil},
		{"Sweet lassi", 9000, drinks, bar, nil},
		{"Masala chaas", 6000, drinks, bar, nil},
		{"Fresh lime soda", 8000, drinks, bar, nil},
	} {
		it, err := item(d.name, d.price, d.cat, d.section, d.groups...)
		if err != nil {
			return "", "", err
		}
		items[d.name] = it
	}
	fmt.Println("  menu: 10 dishes, 3 kitchen sections, 2 add-on groups")

	// Dining room.
	tables := map[string]string{}
	for _, fl := range []struct {
		name   string
		labels []string
		seats  int
	}{{"Main hall", []string{"T1", "T2", "T3", "T4", "T5", "T6"}, 4}, {"Terrace", []string{"R1", "R2", "R3", "R4"}, 2}} {
		var f ref
		if err := post("/v1/outlets/"+outlet+"/floors", map[string]any{"name": fl.name}, &f); err != nil {
			return "", "", err
		}
		for i, l := range fl.labels {
			var t ref
			if err := post("/v1/outlets/"+outlet+"/tables", map[string]any{"floor_id": f.ID, "label": l, "seats": fl.seats, "sort": i}, &t); err != nil {
				return "", "", err
			}
			tables[l] = t.ID
		}
	}
	fmt.Println("  floors: Main hall (T1-T6), Terrace (R1-R4)")

	// Devices.
	pair := func(role, name string) (*device, error) {
		var c struct {
			Code string `json:"code"`
		}
		if err := post("/v1/outlets/"+outlet+"/pairing-codes", map[string]any{"role": role}, &c); err != nil {
			return nil, err
		}
		var r struct {
			DeviceID      string `json:"device_id"`
			DeviceToken   string `json:"device_token"`
			InvoicePrefix string `json:"invoice_prefix"`
		}
		if err := call("POST", "/v1/devices/register", "", map[string]any{"pairing_code": c.Code, "name": name}, &r); err != nil {
			return nil, err
		}
		return &device{ID: r.DeviceID, Token: r.DeviceToken, Prefix: r.InvoicePrefix, invoice: 1}, nil
	}
	till, err := pair("pos", "Counter 1")
	if err != nil {
		return "", "", err
	}
	captain, err := pair("captain", "Captain Anil")
	if err != nil {
		return "", "", err
	}
	kds, err := pair("kds", "Tandoor screen")
	if err != nil {
		return "", "", err
	}
	fmt.Println("  devices: Counter 1 (till), Captain Anil (phone), Tandoor screen (KDS)")

	at := func(minAgo int) time.Time { return now.Add(-time.Duration(minAgo) * time.Minute) }
	newLine := func(name string, qty int64, mods ...pricing.Modifier) line {
		return line{ID: ids.New(), Item: items[name], Qty: qty, Mods: mods}
	}
	// open seats a table and sends its first round to the kitchen.
	open := func(table string, covers, minAgo int, lines ...line) (uuid.UUID, error) {
		o := ids.New()
		var payloads []map[string]any
		var order []string // sections in the order dishes were added, so tickets are stable run to run
		bySection := map[string][]uuid.UUID{}
		for _, l := range lines {
			payloads = append(payloads, l.payload())
			if _, ok := bySection[l.Item.Section]; !ok {
				order = append(order, l.Item.Section)
			}
			bySection[l.Item.Section] = append(bySection[l.Item.Section], l.ID)
		}
		ops := []syncgw.Op{
			captain.op("order.opened", map[string]any{"order_id": o, "channel": "dine_in", "table_id": tables[table],
				"covers": covers, "opened_at": at(minAgo)}),
			captain.op("order.lines_added", map[string]any{"order_id": o, "lines": payloads, "at": at(minAgo - 1)}),
		}
		n := 1
		for _, section := range order {
			ops = append(ops, captain.op("kot.fired", map[string]any{"kot_id": ids.New(), "kot_no": fmt.Sprintf("%s-K%d", captain.Prefix, n),
				"order_id": o, "section_id": section, "line_ids": bySection[section], "fired_at": at(minAgo - 2)}))
			n++
		}
		return o, captain.push(ops...)
	}

	t1, err := open("T1", 4, 50, newLine("Paneer tikka", 1, pricing.Modifier{Name: "Extra cheese", PricePaise: 4000}),
		newLine("Butter chicken", 2), newLine("Butter naan", 4), newLine("Sweet lassi", 2))
	if err != nil {
		return "", "", err
	}
	if _, err := open("T2", 2, 25, newLine("Chicken 65", 1), newLine("Fresh lime soda", 2)); err != nil {
		return "", "", err
	}
	if _, err := open("R1", 2, 10, newLine("Veg biryani", 1), newLine("Masala chaas", 2)); err != nil {
		return "", "", err
	}
	// The kitchen is on top of T1's ticket.
	var floor struct {
		Orders []struct {
			ID   string `json:"id"`
			KOTs []struct {
				ID string `json:"id"`
			} `json:"kots"`
		} `json:"orders"`
	}
	if err := call("GET", "/v1/outlets/"+outlet+"/orders", owner, nil, &floor); err != nil {
		return "", "", err
	}
	for _, o := range floor.Orders {
		if o.ID == t1.String() && len(o.KOTs) > 0 {
			if err := kds.push(kds.op("kot.status_changed", map[string]any{"order_id": t1, "kot_id": o.KOTs[0].ID,
				"status": "ready", "at": at(30)})); err != nil {
				return "", "", err
			}
		}
	}

	// T3 ate early and paid: a closed order and its bill.
	t3Lines := []line{newLine("Dal makhani", 1), newLine("Garlic naan", 3), newLine("Sweet lassi", 1)}
	t3, err := open("T3", 3, 120, t3Lines...)
	if err != nil {
		return "", "", err
	}
	// Bills in the order they were printed, so invoice numbers follow time:
	// a takeaway, then T3, then another takeaway.
	if err := till.push(
		till.bill(uuid.Nil, "", 0, at(90), businessDay, "cash", newLine("Veg biryani", 2)),
		till.bill(t3, "T3", 3, at(70), businessDay, "upi", t3Lines...),
		till.bill(uuid.Nil, "", 0, at(60), businessDay, "card", newLine("Chicken 65", 1), newLine("Masala chaas", 1)),
	); err != nil {
		return "", "", err
	}
	fmt.Println("  floor: T1, T2 and R1 eating; T3 paid (UPI); 2 takeaways paid")
	return owner, businessDay, nil
}

// bill prices lines with the shared engine and wraps them in a bill.settled
// op, linked to the order when one is given.
func (d *device) bill(order uuid.UUID, table string, covers int, issued time.Time, businessDay, method string, lines ...line) syncgw.Op {
	in := pricing.Input{PriceMode: pricing.Exclusive, Supply: pricing.Intra, Rounding: pricing.NearestRupee}
	var lineIDs []uuid.UUID
	for _, l := range lines {
		in.Lines = append(in.Lines, pricing.Line{Ref: l.Item.ID, Name: l.Item.Name, Qty: l.Qty,
			UnitPricePaise: l.Item.Price, TaxRateBP: 500, Modifiers: l.Mods})
		lineIDs = append(lineIDs, l.ID)
	}
	totals, err := pricing.Compute(in)
	if err != nil {
		panic(err) // the seed's own menu always prices
	}
	inRaw, _ := json.Marshal(in)
	totRaw, _ := json.Marshal(totals)
	sb := sales.SettledBill{
		BillID: ids.New().String(), InvoiceNo: gst.InvoiceNo(d.Prefix, gst.FY(issued), d.invoice),
		IssuedAt: issued, BusinessDate: businessDay, Channel: "takeaway",
		Input: inRaw, Totals: totRaw, Payments: []sales.Payment{{Method: method, AmountPaise: totals.GrandTotal}},
	}
	d.invoice++
	if order != uuid.Nil {
		sb.Channel, sb.TableLabel, sb.Covers, sb.OrderID, sb.LineIDs = "dine_in", table, covers, &order, lineIDs
	}
	return d.op(sales.OpBillSettled, sb)
}

func call(method, path, token string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, base+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w (is the API running? try: make run)", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %d %s", method, path, res.StatusCode, raw)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}
