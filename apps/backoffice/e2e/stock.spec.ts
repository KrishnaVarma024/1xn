// Stock in the browser: add ingredients, receive a delivery, give a dish a
// recipe, sell it from a till, and see stock, food cost, wastage and a count.
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

function tradingDay(now = new Date()): string {
  return new Date(now.getTime() + 5.5 * 3600e3 - 4 * 3600e3).toISOString().slice(0, 10);
}

/** A menu with one Rs 300 paneer dish (5% GST), and a till that bills it. */
async function kitchen(request: APIRequestContext, o: Owner) {
  const tax = await asOwner<{ id: string }>(request, o, "POST", "/v1/catalogue/tax-profiles", { name: "GST 5%", rate_bp: 500 });
  const cat = await asOwner<{ id: string }>(request, o, "POST", "/v1/catalogue/categories", { name: "Mains" });
  const dish = await asOwner<{ id: string }>(request, o, "POST", "/v1/catalogue/items", {
    name: "Paneer tikka", price_paise: 30000, category_id: cat.id, tax_profile_id: tax.id,
  });
  const code = await asOwner<{ code: string }>(request, o, "POST", `/v1/outlets/${o.outletId}/pairing-codes`, { role: "pos" });
  const dev = await (await request.post(`${API}/v1/devices/register`, { data: { pairing_code: code.code, name: "Counter 1" } })).json();
  let seq = 0;
  let invoice = 0;
  const sell = async (qty: number) => {
    const at = new Date(Date.now() - 5 * 60e3);
    const ist = new Date(at.getTime() + 5.5 * 3600e3);
    const fy = ist.getUTCMonth() >= 3 ? ist.getUTCFullYear() : ist.getUTCFullYear() - 1;
    const fyCode = `${String(fy % 100).padStart(2, "0")}${String((fy + 1) % 100).padStart(2, "0")}`;
    const net = 30000 * qty;
    const half = Math.round((net * 250) / 10000);
    const total = net + 2 * half;
    const rounded = Math.round(total / 100) * 100;
    const res = await request.post(`${API}/v1/sync/push`, {
      headers: { Authorization: `Bearer ${dev.device_token}` },
      data: {
        ops: [{
          op_id: uuid7(), device_seq: ++seq, type: "bill.settled",
          payload: {
            bill_id: uuid7(), invoice_no: `${dev.invoice_prefix}-${fyCode}-${String(++invoice).padStart(6, "0")}`,
            issued_at: at.toISOString(), business_date: tradingDay(at), channel: "takeaway",
            input: { price_mode: "exclusive", supply: "intra", rounding: "nearest_rupee",
              lines: [{ ref: dish.id, name: "Paneer tikka", qty, unit_price_paise: 30000, tax_rate_bp: 500 }] },
            totals: {
              lines: [{ ref: dish.id, gross: net, line_discount: 0, bill_discount: 0, net }],
              subtotal: net, discount_total: 0, service_charge: 0, taxable_total: net,
              tax_groups: [{ rate_bp: 500, net, goods_value: net, service_charge: 0, taxable: net, cgst: half, sgst: half, igst: 0 }],
              cgst: half, sgst: half, igst: 0, tax_total: 2 * half, round_off: rounded - total, grand_total: rounded,
            },
            payments: [{ method: "cash", amount_paise: rounded }],
          },
        }],
      },
    });
    const acks = (await res.json()).acks as { status: string; error?: unknown }[];
    expect(acks[0].status, JSON.stringify(acks)).toBe("applied");
  };
  return { sell };
}

test("an owner stocks the kitchen, gives a dish a recipe, and sees food cost as bills sync", async ({ page, request }) => {
  const o = await signUp(request, "Stockroom Kitchen");
  const k = await kitchen(request, o);
  await signIn(page, o.token);

  await page.getByRole("link", { name: "Stock", exact: true }).click();
  await expect(page.getByText("No stock items yet")).toBeVisible();
  const add = page.getByRole("form", { name: "Add a stock item" });
  await add.getByLabel("Name").fill("Paneer");
  await add.getByLabel("Reorder at").fill("1");
  await add.getByRole("button", { name: "Add item" }).click();
  await expect(add.getByRole("status")).toContainText("Paneer added");
  await expect(page.getByTestId("stock-Paneer")).toContainText("Not stocked yet");

  // A delivery: 2 kg for Rs 640, on credit.
  await page.getByRole("link", { name: "Receive goods" }).click();
  const recv = page.getByRole("form", { name: "Receive goods" });
  await recv.getByLabel("Supplier", { exact: true }).fill("Bandra Dairy");
  await recv.getByLabel("Row 1 item").selectOption({ label: "Paneer (kg)" });
  await recv.getByLabel("Row 1 quantity").fill("2");
  await recv.getByLabel("Row 1 cost").fill("640");
  await recv.getByRole("button", { name: "Book the delivery" }).click();
  await expect(recv.getByRole("status")).toContainText("Received from Bandra Dairy: 1 item");
  await expect(page.getByRole("row", { name: /Bandra Dairy/ })).toContainText("₹640.00");
  await expect(recv.getByLabel("Row 1 quantity")).toHaveValue(""); // a fresh form for the next delivery

  await page.getByRole("link", { name: "Items" }).click();
  await expect(page.getByTestId("stock-Paneer")).toContainText("2 kg");
  await expect(page.getByTestId("stock-Paneer")).toContainText("₹320.00/kg");

  // A recipe: 200 g of paneer a portion. Rs 64 of a Rs 300 dish: 21.33%.
  await page.getByRole("link", { name: "Recipes" }).click();
  const dish = page.getByTestId("dish-Paneer tikka");
  await dish.getByText("No recipe").click();
  const recipe = dish.getByRole("form", { name: "Recipe for Paneer tikka" });
  await recipe.getByLabel("Ingredient 1").selectOption({ label: "Paneer (kg)" });
  await recipe.getByLabel("Quantity 1").fill("200 g");
  await recipe.getByRole("button", { name: "Save recipe" }).click();
  await expect(recipe.getByRole("status")).toContainText("Recipe saved");
  await expect(dish.locator("summary")).toContainText("costs ₹64.00 of ₹300.00");
  await expect(dish.locator("summary")).toContainText("21.33%");

  // Three portions sell at the till: 600 g leaves the store.
  await k.sell(3);
  await page.goto("/stock");
  await expect(page.getByTestId("stock-Paneer")).toContainText("1.4 kg");
  await page.goto("/stock/food-cost");
  await expect(page.getByText("₹900.00")).toBeVisible(); // food sales
  await expect(page.getByText("21.33%").first()).toBeVisible(); // food cost: 192 of 900

  // 100 g goes bad; the night count finds 1.2 kg (100 g missing).
  await page.getByRole("link", { name: "Wastage" }).click();
  const waste = page.getByRole("form", { name: "Record wastage" });
  await waste.getByLabel("Item").selectOption({ index: 1 });
  await waste.getByLabel("How much").fill("100 g");
  await waste.getByLabel("Why").fill("Left out overnight");
  await waste.getByRole("button", { name: "Book wastage" }).click();
  await expect(waste.getByRole("status")).toContainText("Wastage booked");

  await page.getByRole("link", { name: "Count" }).click();
  const count = page.getByRole("form", { name: "Stock count" });
  await expect(count.getByRole("row", { name: /Paneer/ })).toContainText("1.3 kg");
  await count.getByLabel("Counted Paneer").fill("1.2");
  await count.getByRole("button", { name: "Book the count" }).click();
  await expect(count.getByRole("status")).toContainText("Count booked for 1 item");

  await page.goto("/stock");
  await page.getByRole("link", { name: "Paneer" }).click();
  await expect(page.getByRole("heading", { name: "Paneer" })).toBeVisible();
  for (const kind of ["Received", "Used by bills", "Wasted", "Count"]) {
    await expect(page.getByRole("table").getByText(kind, { exact: true })).toBeVisible();
  }
  await page.getByRole("link", { name: "Books" }).click();
  await expect(page.getByText("Balanced")).toBeVisible();
  await expect(page.getByText("Cost of food sold")).toBeVisible();
  await expect(page.getByText("Payable to suppliers")).toBeVisible();
});

test("selling more than the books hold raises an alert, and quantities must be readable", async ({ page, request }) => {
  const o = await signUp(request, "Overdraft Dhaba");
  const k = await kitchen(request, o);
  const paneer = await asOwner<{ id: string }>(request, o, "POST", "/v1/stock/items", { name: "Paneer", unit: "kg" });
  const menu = await asOwner<{ items: { id: string }[] }>(request, o, "GET", `/v1/outlets/${o.outletId}/menu`);
  await request.fetch(`${API}/v1/stock/recipes/${menu.items[0].id}`, {
    method: "PUT", headers: { Authorization: `Bearer ${o.token}` }, data: { lines: [{ stock_item_id: paneer.id, qty: 200 }] },
  });
  await k.sell(1); // nothing was ever booked in

  await signIn(page, o.token);
  await page.goto("/stock");
  await expect(page.getByTestId("stock-Paneer")).toContainText("Below zero");
  await page.getByRole("link", { name: "Alerts" }).click();
  await expect(page.getByText("More of an ingredient was used than was ever booked in")).toBeVisible();

  await page.goto("/stock/receive");
  const recv = page.getByRole("form", { name: "Receive goods" });
  await recv.getByLabel("Supplier", { exact: true }).fill("Anyone");
  await recv.getByLabel("Row 1 item").selectOption({ label: "Paneer (kg)" });
  await recv.getByLabel("Row 1 quantity").fill("2.5 g"); // finer than a gram
  await recv.getByLabel("Row 1 cost").fill("10");
  await recv.getByRole("button", { name: "Book the delivery" }).click();
  await expect(recv.getByRole("alert")).toContainText("Row 1: write a quantity");
  await expect(recv.getByLabel("Supplier", { exact: true })).toHaveValue("Anyone"); // what was typed is kept
});
