// Adversarial review of "close a till's shift from the back office", in the
// browser. Every test here names a defect and fails against the code as
// reviewed; it should pass once the defect is fixed.
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
  await page.goto("/sign-in");
  await page.getByLabel("Owner token").fill(token);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/floor$/);
}

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

/** A paired till that can push ops: shifts and Rs 105 thali bills paid in cash. */
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

// C6. closeShiftHere answered 409 shift_closed with revalidatePath("/day")
// and then a form error. The revalidated page no longer lists the shift, so
// the OpenShifts card (and the form inside it) unmounted in the same
// response: the message was never shown, and the owner who had just counted
// a drawer saw the form vanish with no word that their count was not used.
// (Only a revoked till's shift can be closed here, so "meanwhile" means
// another person in the back office.)
test("C6: the owner is told their count was not used when someone else closed the shift meanwhile", async ({ page, request }) => {
  const o = await signUp(request, "Review Meanwhile Cafe");
  const t = await till(request, o);
  const day = tradingDay();
  const ago = (min: number) => new Date(Date.now() - min * 60e3);
  const shift = uuid7();
  await t.push(
    { type: "shift.opened", payload: { shift_id: shift, business_date: day, opened_at: ago(120).toISOString(), opened_by: "Vikram", opening_float_paise: 100000 } },
    t.bill(ago(60), day),
  );
  await asOwner(request, o, "POST", `/v1/devices/${t.deviceId}/revoke`);
  await signIn(page, o.token);
  await page.goto("/day");
  await page.getByTestId("open-shift-Counter 1").getByText("Count the drawer and close it here").click();
  const form = page.getByRole("form", { name: "Close Counter 1's shift" });
  await form.getByLabel("Cash counted (₹)").fill("1,105");
  await form.getByLabel("Why here, not on the till").fill("Till lost");
  // While the owner was counting, someone else closed it in another tab.
  await asOwner(request, o, "POST", `/v1/outlets/${o.outletId}/shifts/${shift}/close`, { id: uuid7(), counted_paise: 110000, note: "Counted by the manager" });
  await form.getByRole("button", { name: "Close the shift" }).click();
  await expect(page.getByText("This shift was closed meanwhile by Meera, so your count was not used")).toBeVisible();
  await expect(page.getByText("The drawer was ₹5.00 short")).toBeVisible();
});

test("a working till's open shift can only be closed on the till: the page says how", async ({ page, request }) => {
  const o = await signUp(request, "Review Working Till Cafe");
  const t = await till(request, o);
  const day = tradingDay();
  await t.push({ type: "shift.opened", payload: { shift_id: uuid7(), business_date: day, opened_at: new Date(Date.now() - 3600e3).toISOString(), opened_by: "Vikram", opening_float_paise: 100000 } });
  await signIn(page, o.token);
  await page.goto("/day");
  const open = page.getByTestId("open-shift-Counter 1");
  await expect(open).toContainText("Close it on the till");
  await expect(open.getByText("Count the drawer and close it here")).toHaveCount(0);
  await open.getByRole("link", { name: "revoke it" }).click();
  await expect(page).toHaveURL(/\/devices$/);
});
