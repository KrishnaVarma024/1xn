// Day close in the browser: a till's shift and bills arrive by sync, the
// owner reads the Z report and closes the day, and a bill that syncs late
// marks the day as amended.
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";

const API = process.env.E2E_API_URL ?? "http://localhost:8080";

type Owner = { token: string; outletId: string };

async function signUp(request: APIRequestContext, name: string): Promise<Owner> {
  const res = await request.post(`${API}/v1/signup`, {
    data: {
      business_name: name,
      owner: { name: "Meera", phone: "+919876543210" },
      legal_entity: { name, state_code: "29" },
      outlet: { name: `${name} outlet` },
    },
  });
  expect(res.status()).toBe(201);
  const b = await res.json();
  return { token: b.owner_token, outletId: b.outlet_id };
}

async function asOwner<T>(request: APIRequestContext, o: Owner, method: string, p: string, data?: unknown): Promise<T> {
  const res = await request.fetch(`${API}${p}`, { method, data, headers: { Authorization: `Bearer ${o.token}` } });
  expect(res.ok(), `${method} ${p}: ${res.status()} ${await res.text()}`).toBe(true);
  return (await res.json()) as T;
}

async function signIn(page: Page, token: string) {
  await page.goto("/sign-in/token");
  await page.getByLabel("Owner token").fill(token);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/floor$/);
}

// UUIDv7: 48-bit millisecond timestamp, version 7, variant 10, random rest.
function uuid7(): string {
  const b = new Uint8Array(16);
  crypto.getRandomValues(b);
  let ms = Date.now();
  for (let i = 5; i >= 0; i--) {
    b[i] = ms & 0xff;
    ms = Math.floor(ms / 256);
  }
  b[6] = (b[6] & 0x0f) | 0x70;
  b[8] = (b[8] & 0x3f) | 0x80;
  const h = [...b].map((x) => x.toString(16).padStart(2, "0")).join("");
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}

/** A paired till that can push ops: shifts and Rs 105 thali bills. */
async function till(request: APIRequestContext, o: Owner) {
  const tax = await asOwner<{ id: string }>(request, o, "POST", "/v1/catalogue/tax-profiles", { name: "GST 5%", rate_bp: 500 });
  const cat = await asOwner<{ id: string }>(request, o, "POST", "/v1/catalogue/categories", { name: "Mains" });
  const thali = await asOwner<{ id: string }>(request, o, "POST", "/v1/catalogue/items", {
    name: "Thali", price_paise: 10000, category_id: cat.id, tax_profile_id: tax.id,
  });
  const code = await asOwner<{ code: string }>(request, o, "POST", `/v1/outlets/${o.outletId}/pairing-codes`, { role: "pos" });
  const dev = await (await request.post(`${API}/v1/devices/register`, { data: { pairing_code: code.code, name: "Counter 1" } })).json();
  let seq = 0;
  let invoice = 0;
  const push = async (...ops: { type: string; payload: unknown }[]) => {
    const res = await request.post(`${API}/v1/sync/push`, {
      headers: { Authorization: `Bearer ${dev.device_token}` },
      data: { ops: ops.map((op) => ({ op_id: uuid7(), device_seq: ++seq, ...op })) },
    });
    const acks = (await res.json()).acks as { status: string; error?: { code: string } }[];
    expect(acks.map((a) => a.status), JSON.stringify(acks)).toEqual(ops.map(() => "applied"));
  };
  const bill = (at: Date, businessDate: string) => {
    const ist = new Date(at.getTime() + 5.5 * 3600e3);
    const fy = ist.getUTCMonth() >= 3 ? ist.getUTCFullYear() : ist.getUTCFullYear() - 1;
    const fyCode = `${String(fy % 100).padStart(2, "0")}${String((fy + 1) % 100).padStart(2, "0")}`;
    return {
      type: "bill.settled",
      payload: {
        bill_id: uuid7(), invoice_no: `${dev.invoice_prefix}-${fyCode}-${String(++invoice).padStart(6, "0")}`,
        issued_at: at.toISOString(), business_date: businessDate, channel: "takeaway",
        input: { price_mode: "exclusive", supply: "intra", rounding: "nearest_rupee", lines: [{ ref: thali.id, name: "Thali", qty: 1, unit_price_paise: 10000, tax_rate_bp: 500 }] },
        totals: {
          lines: [{ ref: thali.id, gross: 10000, line_discount: 0, bill_discount: 0, net: 10000 }],
          subtotal: 10000, discount_total: 0, service_charge: 0, taxable_total: 10000,
          tax_groups: [{ rate_bp: 500, net: 10000, goods_value: 10000, service_charge: 0, taxable: 10000, cgst: 250, sgst: 250, igst: 0 }],
          cgst: 250, sgst: 250, igst: 0, tax_total: 500, round_off: 0, grand_total: 10500,
        },
        payments: [{ method: "cash", amount_paise: 10500 }],
      },
    };
  };
  return { push, bill, deviceId: dev.device_id as string };
}

/** The trading day now: it starts at 04:00 in India. */
function tradingDay(now = new Date()): string {
  return new Date(now.getTime() + 5.5 * 3600e3 - 4 * 3600e3).toISOString().slice(0, 10);
}

test("an owner checks the Z report, closes the day, and a late bill marks it amended", async ({ page, request }) => {
  const o = await signUp(request, "Closing Time Cafe");
  const t = await till(request, o);
  const day = tradingDay();
  const ago = (min: number) => new Date(Date.now() - min * 60e3);
  const shift = uuid7();
  await t.push(
    { type: "shift.opened", payload: { shift_id: shift, business_date: day, opened_at: ago(90).toISOString(), opened_by: "Asha", opening_float_paise: 100000 } },
    t.bill(ago(60), day),
    { type: "cash.moved", payload: { move_id: uuid7(), shift_id: shift, kind: "expense", amount_paise: 5000, reason: "Ice", at: ago(50).toISOString() } },
    // Float 1,000 + 105 - 50 = 1,055 expected; Asha counts 1,045: Rs 10 short.
    { type: "shift.closed", payload: { shift_id: shift, closed_at: ago(30).toISOString(), closed_by: "Asha", counted_paise: 104500, denominations: { "500": 2, "20": 2, "5": 1 } } },
  );

  await signIn(page, o.token);
  await page.getByRole("link", { name: "Day close" }).click();
  await expect(page.getByRole("heading", { name: "Day close" })).toBeVisible();
  await expect(page.getByText("₹105.00").first()).toBeVisible();
  const drawers = page.locator("section", { hasText: "Cash drawers" });
  await expect(drawers.getByText("₹10.00 short")).toBeVisible();
  await expect(drawers.getByText("Paid out ₹50.00 · Ice")).toBeVisible();
  const close = page.getByRole("form", { name: "Close the day" });
  await expect(page.getByRole("list", { name: "Warnings" })).toContainText("Asha's drawer was ₹10.00 short");
  await expect(close.getByLabel("I have checked the warnings above")).toHaveCount(0); // only notes: nothing to acknowledge

  await close.getByRole("button", { name: "Close the day" }).click();
  await expect(page.getByText(/by Meera on/)).toBeVisible();
  await expect(page.getByText("Showing the report as it was closed.")).toBeVisible();
  await expect(page.getByRole("form", { name: "Close the day" })).toHaveCount(0);

  // A bill that was stuck on the till syncs now.
  await t.push(t.bill(ago(20), day));
  await page.reload();
  const note = page.getByRole("note", { name: "Amended" });
  await expect(note).toContainText("Amended after closing");
  await expect(note).toContainText("Bills: 1 → 2");
  await expect(note).toContainText("Sales: ₹105.00 → ₹210.00");
  await page.getByRole("link", { name: "Show today's figures" }).click();
  await expect(page.getByText("₹210.00").first()).toBeVisible();

  await page.getByRole("link", { name: "Alerts" }).click();
  await expect(page.getByText("A bill arrived for a day you already closed")).toBeVisible();
  await expect(page.getByText("A cash drawer did not match at the end of a shift")).toBeVisible();
});

test("open shifts and missing invoice numbers must be acknowledged before closing", async ({ page, request }) => {
  const o = await signUp(request, "Busy Bistro");
  const t = await till(request, o);
  const day = tradingDay();
  const ago = (min: number) => new Date(Date.now() - min * 60e3);
  const first = t.bill(ago(40), day);
  t.bill(ago(30), day); // number 2 never reaches the server
  await t.push(
    { type: "shift.opened", payload: { shift_id: uuid7(), business_date: day, opened_at: ago(50).toISOString(), opened_by: "Vikram", opening_float_paise: 0 } },
    first,
    t.bill(ago(20), day),
  );

  await signIn(page, o.token);
  await page.goto("/day");
  const warnings = page.getByRole("list", { name: "Warnings" });
  await expect(warnings).toContainText("Vikram's shift is still open");
  await expect(warnings).toContainText("missing 1 invoice number");
  const close = page.getByRole("form", { name: "Close the day" });
  await close.getByRole("button", { name: "Close the day" }).click();
  await expect(close.getByRole("alert")).toContainText("Tick the box");
  await close.getByLabel("I have checked the warnings above").check();
  await close.getByRole("button", { name: "Close the day" }).click();
  await expect(page.getByText(/by Meera on/)).toBeVisible();
});

test("a stale Z report cannot be closed: new figures are shown first", async ({ page, request }) => {
  const o = await signUp(request, "Stale Page Diner");
  const t = await till(request, o);
  const day = tradingDay();
  await t.push(t.bill(new Date(Date.now() - 30 * 60e3), day));
  await signIn(page, o.token);
  await page.goto("/day");
  await expect(page.getByText("₹105.00").first()).toBeVisible();

  await t.push(t.bill(new Date(Date.now() - 10 * 60e3), day)); // arrives while the owner reads
  const close = page.getByRole("form", { name: "Close the day" });
  await close.getByRole("button", { name: "Close the day" }).click();
  await expect(close.getByRole("alert")).toContainText("New figures arrived");
  await expect(page.getByText("₹210.00").first()).toBeVisible(); // the page now shows them
  await close.getByRole("button", { name: "Close the day" }).click();
  await expect(page.getByText(/by Meera on/)).toBeVisible();
});

test("an impossible ?date on the day close page falls back to today", async ({ page, request }) => {
  const o = await signUp(request, "Odd Dates");
  await signIn(page, o.token);
  for (const d of ["0000-01-01", "2026-02-30", "../ledger", "2101-01-01"]) {
    await page.goto(`/day?date=${encodeURIComponent(d)}`);
    await expect(page.getByRole("heading", { name: "Day close" })).toBeVisible();
    await expect(page.getByText("This page could not load")).toHaveCount(0);
  }
});

test("an owner counts a revoked till's drawer and closes its shift from the back office", async ({ page, request }) => {
  const o = await signUp(request, "Lost Till Cafe");
  const t = await till(request, o);
  const day = tradingDay();
  const ago = (min: number) => new Date(Date.now() - min * 60e3);
  // Float 1,000 + two thalis in cash (210) = 1,210 in the drawer.
  await t.push(
    { type: "shift.opened", payload: { shift_id: uuid7(), business_date: day, opened_at: ago(120).toISOString(), opened_by: "Vikram", opening_float_paise: 100000 } },
    t.bill(ago(90), day),
    t.bill(ago(60), day),
  );
  await asOwner(request, o, "POST", `/v1/devices/${t.deviceId}/revoke`);

  await signIn(page, o.token);
  await page.goto("/day");
  const open = page.getByTestId("open-shift-Counter 1");
  await expect(page.getByText("A shift is still open")).toBeVisible();
  await expect(open).toContainText("Till revoked");
  await expect(open).toContainText("₹1,210.00");
  await expect(page.getByRole("list", { name: "Warnings" })).toContainText("revoke it and close the shift below");

  await open.getByText("Count the drawer and close it here").click();
  const form = page.getByRole("form", { name: "Close Counter 1's shift" });
  await form.getByLabel("Cash counted (₹)").fill("1,160");
  await form.getByLabel("Why here, not on the till").fill("   "); // blank, past the browser's own check
  await form.getByRole("button", { name: "Close the shift" }).click();
  await expect(form.getByRole("alert")).toContainText("Say why"); // a reason is required
  await form.getByLabel("Why here, not on the till").fill("Till stolen; counted by Meera");
  await form.getByRole("button", { name: "Close the shift" }).click();
  await expect(page.getByRole("status").filter({ hasText: "Vikram's shift is closed" })).toContainText("₹50.00 short");
  await expect(page.getByText("A shift is still open")).toHaveCount(0);
  const drawers = page.locator("section", { hasText: "Cash drawers" });
  await expect(drawers.getByText("₹50.00 short")).toBeVisible();
  await expect(drawers.getByText("Counted in the back office by Meera: Till stolen; counted by Meera")).toBeVisible();
  await page.getByRole("link", { name: "Books" }).click();
  await expect(page.getByText("Balanced")).toBeVisible();
  await expect(page.getByText("Cash over and short")).toBeVisible();
});
