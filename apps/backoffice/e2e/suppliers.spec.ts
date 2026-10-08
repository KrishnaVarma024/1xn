// Stock, part two, in the browser: packs (rice by the bag), suppliers and
// what is owed to them, paying a supplier, and recipes for add-ons.
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

test("an owner buys rice by the bag on credit, then pays the supplier in two goes", async ({ page, request }) => {
  const o = await signUp(request, "Sack and Tin");
  await asOwner(request, o, "POST", "/v1/stock/items", { name: "Basmati rice", unit: "kg", reorder_level: 0 });
  await signIn(page, o.token);

  // A pack: rice comes in 25 kg bags.
  await page.goto("/stock");
  await page.getByRole("link", { name: "Basmati rice" }).click();
  const add = page.getByRole("form", { name: "Add a pack" });
  await add.getByLabel("Pack").fill("Bag");
  await add.getByLabel("Holds (kg)").fill("25");
  await add.getByRole("button", { name: "Add pack" }).click();
  await expect(add.getByRole("status")).toContainText("Bag of 25 kg added");
  await expect(page.getByTestId("pack-Bag")).toContainText("Bag of 25 kg");
  await add.getByLabel("Pack").fill("bag");
  await add.getByLabel("Holds (kg)").fill("10");
  await add.getByRole("button", { name: "Add pack" }).click();
  await expect(add.getByRole("alert")).toContainText("already has a pack with that name");

  // Two bags on credit from a supplier nobody has entered yet.
  await page.getByRole("link", { name: "Receive goods" }).click();
  const recv = page.getByRole("form", { name: "Receive goods" });
  await recv.getByLabel("Supplier", { exact: true }).fill("Grain House");
  await recv.getByLabel("Row 1 item").selectOption({ label: "Basmati rice: Bag of 25 kg" });
  await recv.getByLabel("Row 1 quantity").fill("2.5");
  await recv.getByLabel("Row 1 cost").fill("5,000");
  await recv.getByRole("button", { name: "Book the delivery" }).click();
  await expect(recv.getByRole("alert")).toContainText("say how many packs (Bag), as a whole number");
  await recv.getByLabel("Row 1 quantity").fill("2");
  await recv.getByRole("button", { name: "Book the delivery" }).click();
  await expect(recv.getByRole("status")).toContainText("Received from Grain House: 1 item");

  await page.getByRole("link", { name: "Items" }).click();
  await expect(page.getByTestId("stock-Basmati rice")).toContainText("50 kg");
  await expect(page.getByTestId("stock-Basmati rice")).toContainText("₹100.00/kg");
  await page.getByRole("link", { name: "Basmati rice" }).click();
  await expect(page.getByRole("row", { name: /Received/ })).toContainText("Grain House · 2 × Bag");

  // The supplier was added by the delivery, and is owed for it.
  await page.getByRole("link", { name: "Suppliers" }).click();
  await expect(page.getByTestId("supplier-Grain House")).toContainText("₹5,000.00");
  await page.getByRole("link", { name: "Grain House" }).click();
  await expect(page.getByRole("heading", { name: "Grain House", exact: true })).toBeVisible();
  const pay = page.getByRole("form", { name: "Pay Grain House" });
  await expect(pay.getByLabel("Amount, ₹")).toHaveValue("5000.00");

  await pay.getByLabel("Amount, ₹").fill("2,000");
  await pay.getByLabel("Paid by").selectOption("bank");
  await pay.getByLabel("Reference").fill("UTR 991");
  await pay.getByRole("button", { name: "Record the payment" }).click();
  await expect(page.getByRole("status")).toContainText("Paid ₹2,000.00 to Grain House (Bank");
  await expect(page.getByRole("status")).toContainText("Still owed ₹3,000.00.");
  await expect(page.getByTestId("line-payment")).toContainText("ref UTR 991");
  await expect(page.getByTestId("line-payment")).toContainText("−₹2,000.00");

  // More than is owed is refused, and says how much is.
  await pay.getByLabel("Amount, ₹").fill("3,500");
  await pay.getByRole("button", { name: "Record the payment" }).click();
  await expect(pay.getByRole("alert")).toContainText("Grain House is owed ₹3,000.00");

  // The rest from the safe: nothing is owed, and the page says so.
  await pay.getByLabel("Amount, ₹").fill("3000");
  await pay.getByLabel("Paid by").selectOption("safe");
  await pay.getByRole("button", { name: "Record the payment" }).click();
  await expect(page.getByRole("status")).toContainText("Paid ₹3,000.00 to Grain House (Cash from the safe");
  await expect(page.getByRole("status")).toContainText("Nothing is owed now.");
  await expect(page.getByText("Nothing is owed. Deliveries bought on credit")).toBeVisible();
  await expect(page.getByTestId("line-payment")).toHaveCount(2);

  // The books agree: payable to suppliers is back to zero.
  const tb = await asOwner<{ accounts: { code: string; balance: number }[] }>(request, o, "GET", "/v1/ledger/trial-balance");
  expect(tb.accounts.find((a) => a.code === "2100")?.balance ?? 0).toBe(0);

  // A pack no longer used drops out of the delivery form.
  await page.goto("/stock");
  await page.getByRole("link", { name: "Basmati rice" }).click();
  await page.getByRole("form", { name: "Stop using Bag" }).getByRole("button", { name: "Stop using" }).click();
  await expect(page.getByTestId("pack-Bag")).toContainText("Not in use");
  await page.getByRole("link", { name: "Receive goods" }).click();
  await expect(page.getByRole("form", { name: "Receive goods" }).getByLabel("Row 1 item").locator("option", { hasText: "Bag" })).toHaveCount(0);
});

test("suppliers are added and corrected by hand", async ({ page, request }) => {
  const o = await signUp(request, "Supplier Book");
  await signIn(page, o.token);
  await page.goto("/stock/suppliers");
  await expect(page.getByText("No suppliers yet")).toBeVisible();

  const add = page.getByRole("form", { name: "Add a supplier" });
  await add.getByLabel("Name").fill("Fresh Farms");
  await add.getByLabel("Phone").fill("12345");
  await add.getByRole("button", { name: "Add supplier" }).click();
  await expect(add.getByRole("alert")).toContainText("10-digit mobile number");
  await add.getByLabel("Phone").fill("98450 12345");
  await add.getByLabel("GSTIN").fill("29ABCDE1234F1Z5");
  await add.getByRole("button", { name: "Add supplier" }).click();
  await expect(add.getByRole("alert")).toContainText("GSTIN is not valid");
  await add.getByLabel("GSTIN").fill("29abcde1234f1zw");
  await add.getByRole("button", { name: "Add supplier" }).click();
  await expect(add.getByRole("status")).toContainText("Fresh Farms added");
  await expect(page.getByTestId("supplier-Fresh Farms")).toContainText("98450 12345");
  await expect(page.getByTestId("supplier-Fresh Farms")).toContainText("GSTIN 29ABCDE1234F1ZW");
  await expect(page.getByTestId("supplier-Fresh Farms")).toContainText("Paid up");

  await add.getByLabel("Name").fill("FRESH FARMS");
  await add.getByRole("button", { name: "Add supplier" }).click();
  await expect(add.getByRole("alert")).toContainText("already a supplier with that name");

  await page.getByRole("link", { name: "Fresh Farms" }).click();
  await expect(page.getByText("Nothing is owed. Deliveries bought on credit")).toBeVisible();
  const edit = page.getByRole("form", { name: "Edit supplier" });
  await expect(edit.getByLabel("Phone")).toHaveValue("98450 12345");
  await edit.getByLabel("Name").fill("Fresh Farms Pvt Ltd");
  await edit.getByLabel("Phone").fill("");
  await edit.getByRole("button", { name: "Save" }).click();
  await expect(edit.getByRole("status")).toContainText("Saved");
  await expect(page.getByRole("heading", { name: "Fresh Farms Pvt Ltd", exact: true })).toBeVisible();
  await page.getByRole("link", { name: "← All suppliers" }).click();
  await expect(page.getByTestId("supplier-Fresh Farms Pvt Ltd")).not.toContainText("98450");
});

test("an add-on's recipe is used up by the bills that carry it", async ({ page, request }) => {
  const o = await signUp(request, "Cheese Corner");
  const tax = await asOwner<{ id: string }>(request, o, "POST", "/v1/catalogue/tax-profiles", { name: "GST 5%", rate_bp: 500 });
  const cat = await asOwner<{ id: string }>(request, o, "POST", "/v1/catalogue/categories", { name: "Pizzas" });
  const group = await asOwner<{ id: string; options: { id: string; name: string }[] }>(request, o, "POST", "/v1/catalogue/modifier-groups", {
    name: "Toppings", min_select: 0, max_select: 2, options: [{ name: "Extra cheese", price_paise: 4000 }],
  });
  const dish = await asOwner<{ id: string }>(request, o, "POST", "/v1/catalogue/items", {
    name: "Margherita", price_paise: 30000, category_id: cat.id, tax_profile_id: tax.id, modifier_group_ids: [group.id],
  });
  const cheese = await asOwner<{ id: string }>(request, o, "POST", "/v1/stock/items", { name: "Mozzarella", unit: "kg", reorder_level: 0 });
  await asOwner(request, o, "POST", `/v1/outlets/${o.outletId}/stock/purchases`, {
    id: uuid7(), supplier: "Dairy Co", paid_by: "bank",
    lines: [{ stock_item_id: cheese.id, qty: 1000, cost: 50000 }],
  });

  await signIn(page, o.token);
  await page.goto("/recipes");
  const addon = page.getByTestId("addon-Extra cheese");
  await expect(addon.locator("summary")).toContainText("No recipe");
  await addon.locator("summary").click();
  const recipe = addon.getByRole("form", { name: "Recipe for Extra cheese" });
  await recipe.getByLabel("Ingredient 1").selectOption({ label: "Mozzarella (kg)" });
  await recipe.getByLabel("Quantity 1").fill("30 g");
  await recipe.getByRole("button", { name: "Save recipe" }).click();
  await expect(recipe.getByRole("status")).toContainText("Add-on recipe saved");
  // 30 g at Rs 500/kg is Rs 15 of a Rs 40 add-on (5% GST on top): 37.5%.
  await expect(addon.locator("summary")).toContainText("costs ₹15.00 of ₹40.00");
  await expect(addon.locator("summary")).toContainText("37.5%");

  // Two Margheritas with extra cheese, billed by a till: 60 g of cheese.
  const code = await asOwner<{ code: string }>(request, o, "POST", `/v1/outlets/${o.outletId}/pairing-codes`, { role: "pos" });
  const dev = await (await request.post(`${API}/v1/devices/register`, { data: { pairing_code: code.code, name: "Counter 1" } })).json();
  const at = new Date(Date.now() - 5 * 60e3);
  const ist = new Date(at.getTime() + 5.5 * 3600e3);
  const fy = ist.getUTCMonth() >= 3 ? ist.getUTCFullYear() : ist.getUTCFullYear() - 1;
  const fyCode = `${String(fy % 100).padStart(2, "0")}${String((fy + 1) % 100).padStart(2, "0")}`;
  const net = 2 * (30000 + 4000);
  const half = Math.round((net * 250) / 10000);
  const total = net + 2 * half;
  const rounded = Math.round(total / 100) * 100;
  const res = await request.post(`${API}/v1/sync/push`, {
    headers: { Authorization: `Bearer ${dev.device_token}` },
    data: {
      ops: [{
        op_id: uuid7(), device_seq: 1, type: "bill.settled",
        payload: {
          bill_id: uuid7(), invoice_no: `${dev.invoice_prefix}-${fyCode}-000001`,
          issued_at: at.toISOString(), business_date: tradingDay(at), channel: "takeaway",
          input: { price_mode: "exclusive", supply: "intra", rounding: "nearest_rupee",
            lines: [{ ref: dish.id, name: "Margherita", qty: 2, unit_price_paise: 30000, tax_rate_bp: 500,
              modifiers: [{ ref: group.options[0].id, name: "Extra cheese", price_paise: 4000 }] }] },
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
  const acks = (await res.json()).acks as { status: string }[];
  expect(acks[0].status, JSON.stringify(acks)).toBe("applied");

  await page.goto("/stock");
  await expect(page.getByTestId("stock-Mozzarella")).toContainText("940 g");
  await page.getByRole("link", { name: "Mozzarella" }).click();
  await expect(page.getByRole("row", { name: /Used by bills/ })).toContainText("−60 g");
});
