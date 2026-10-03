import { execFileSync } from "node:child_process";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";

const API = process.env.E2E_API_URL ?? "http://localhost:8080";
const core = path.resolve(__dirname, "../../../services/core");

async function signIn(page: Page, token: string) {
  await page.goto("/sign-in");
  await page.getByLabel("Owner token").fill(token);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/floor$/);
}

test("a new restaurant signs up and builds its menu, tables and devices", async ({ page, request }) => {
  // Not signed in: every page sends you to sign-in.
  await page.goto("/menu");
  await expect(page).toHaveURL(/\/sign-in$/);

  await page.getByRole("link", { name: "Create an account" }).click();
  await page.getByLabel("Restaurant name").fill("Playwright Paratha House");
  await page.getByLabel("Your name").fill("Asha");
  await page.getByLabel("Mobile number").fill("98765 43210");
  await page.getByLabel("First outlet").fill("Koramangala");
  await page.getByLabel("GSTIN").fill("29abcde1234f1z5"); // bad check digit
  await page.getByRole("button", { name: "Create restaurant" }).click();
  // (Scoped to the form: Next.js has its own role=alert route announcer.)
  const signUpForm = page.getByRole("form", { name: "Create your restaurant" });
  await expect(signUpForm.getByRole("alert")).toContainText("GSTIN");
  await expect(page.getByLabel("Restaurant name")).toHaveValue("Playwright Paratha House"); // kept on error

  await page.getByLabel("GSTIN").fill("");
  await page.getByRole("button", { name: "Create restaurant" }).click();
  const token = (await page.getByTestId("owner-token").textContent())!.trim();
  expect(token).toMatch(/^onx_o_/);
  // The session cookie holds the token, but page scripts cannot read it.
  const cookie = (await page.context().cookies()).find((c) => c.name === "onx_owner");
  expect(cookie?.httpOnly).toBe(true);
  expect(cookie?.sameSite).toBe("Lax");
  expect(await page.evaluate(() => document.cookie)).not.toContain("onx_o_");

  await page.getByRole("link", { name: /I saved it/ }).click();
  await expect(page).toHaveURL(/\/menu$/);
  await expect(page.getByTestId("business-name")).toHaveText("Playwright Paratha House");

  // Menu: GST rate, category, kitchen section, add-on group, then a dish.
  const gst = page.getByRole("form", { name: "Add a GST rate" });
  await gst.getByLabel("Name").fill("Restaurant service 5%");
  await gst.getByLabel("Rate (%)").fill("5");
  await gst.getByLabel("HSN/SAC").fill("996331");
  await gst.getByRole("button", { name: "Add GST rate" }).click();
  await expect(gst.getByRole("status")).toHaveText("Tax rate added.");

  const cat = page.getByRole("form", { name: "Add a category" });
  await cat.getByLabel("Category name").fill("Parathas");
  await cat.getByRole("button", { name: "Add" }).click();
  await expect(cat.getByRole("status")).toHaveText("Category added.");

  const sec = page.getByRole("form", { name: "Add a kitchen section" });
  await sec.getByLabel("Section name").fill("Tawa");
  await sec.getByRole("button", { name: "Add" }).click();
  await expect(sec.getByRole("status")).toHaveText("Kitchen section added.");

  const grp = page.getByRole("form", { name: "Add an add-on group" });
  await grp.getByLabel("Group name").fill("Toppings");
  await grp.getByLabel("Pick at most").fill("2");
  await grp.getByLabel("Options").fill("Butter, 20\nCurd, 25.50\nPickle");
  await grp.getByRole("button", { name: "Add group" }).click();
  await expect(grp.getByRole("status")).toHaveText("Add-on group added.");
  await expect(page.getByText("Butter +₹20 · Curd +₹25.50 · Pickle")).toBeVisible();

  const dish = page.getByRole("form", { name: "Add a dish" });
  await dish.getByLabel("Dish name").fill("Aloo paratha");
  await dish.getByLabel("Price (₹)").fill("12O"); // letter O: rejected before reaching the API
  await dish.getByLabel("Cooked in").selectOption({ label: "Tawa" });
  await dish.getByLabel("Toppings").check();
  await dish.getByRole("button", { name: "Add dish" }).click();
  await expect(dish.getByRole("alert")).toContainText("Price must be in rupees");
  await dish.getByLabel("Price (₹)").fill("120");
  await dish.getByRole("button", { name: "Add dish" }).click();
  await expect(dish.getByRole("status")).toHaveText("Aloo paratha is on the menu.");

  const row = page.getByTestId("item-Aloo paratha");
  await expect(row).toContainText("₹120");
  await expect(row).toContainText("Tawa");
  await expect(row).toContainText("Toppings");

  // Edit the price; mark it sold out and back.
  await row.getByText("Edit").click();
  await row.getByLabel("Price (₹)").fill("135.50");
  await row.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByTestId("item-Aloo paratha")).toContainText("₹135.50");
  await page.getByTestId("item-Aloo paratha").getByRole("button", { name: "Mark sold out" }).click();
  await expect(page.getByTestId("item-Aloo paratha")).toContainText("Sold out");
  await page.getByTestId("item-Aloo paratha").getByRole("button", { name: "Back on menu" }).click();
  await expect(page.getByTestId("item-Aloo paratha")).not.toContainText("Sold out");

  // Tables.
  await page.getByRole("link", { name: "Tables" }).click();
  const floor = page.getByRole("form", { name: "Add a floor" });
  await floor.getByLabel("Floor name").fill("Ground floor");
  await floor.getByRole("button", { name: "Add floor" }).click();
  const addTable = page.getByRole("form", { name: "Add a table to Ground floor" });
  await addTable.getByLabel("Table label").fill("G1");
  await addTable.getByRole("button", { name: "Add table" }).click();
  await expect(page.getByTestId("table-G1")).toContainText("4 seats");
  await addTable.getByLabel("Table label").fill("G1");
  await addTable.getByRole("button", { name: "Add table" }).click();
  await expect(addTable.getByRole("alert")).toContainText("already has a table");
  await addTable.getByLabel("Table label").fill("G2");
  await addTable.getByRole("button", { name: "Add table" }).click();
  await page.getByTestId("table-G2").getByRole("button", { name: "Switch off" }).click();
  await expect(page.getByTestId("table-G2")).toContainText("Off");

  // Devices: a pairing code, used by a "phone" through the API.
  await page.getByRole("link", { name: "Devices" }).click();
  await page.getByLabel("What is it?").selectOption("captain");
  await page.getByRole("button", { name: "Get a pairing code" }).click();
  const code = (await page.getByTestId("pairing-code").textContent())!.trim();
  expect(code).toMatch(/^[A-Z0-9]{4}-[A-Z0-9]{4}$/);
  const reg = await request.post(`${API}/v1/devices/register`, { data: { pairing_code: code, name: "Captain Asha" } });
  expect(reg.status()).toBe(201);
  const device = await reg.json();
  await page.reload();
  await expect(page.getByTestId("device-Captain Asha")).toContainText("Captain phone");

  // The captain seats G1 and sends an aloo paratha to the Tawa.
  const me = await (await request.get(`${API}/v1/me`, { headers: { Authorization: `Bearer ${token}` } })).json();
  const menu = await (
    await request.get(`${API}/v1/outlets/${me.outlets[0].id}/menu`, { headers: { Authorization: `Bearer ${token}` } })
  ).json();
  const g1 = menu.floors[0].tables.find((t: { label: string }) => t.label === "G1");
  const item = menu.items[0];
  const orderId = uuid7();
  const lineId = uuid7();
  const now = new Date().toISOString();
  const op = (seq: number, type: string, payload: unknown) => ({ op_id: uuid7(), device_seq: seq, type, payload });
  const push = await request.post(`${API}/v1/sync/push`, {
    headers: { Authorization: `Bearer ${device.device_token}` },
    data: {
      ops: [
        op(1, "order.opened", { order_id: orderId, channel: "dine_in", table_id: g1.id, covers: 2, opened_at: now }),
        op(2, "order.lines_added", {
          order_id: orderId,
          at: now,
          lines: [{ line_id: lineId, item_id: item.id, name: item.name, qty: 2, unit_price_paise: item.price_paise, tax_rate_bp: 500, section_id: item.section_id }],
        }),
        op(3, "kot.fired", { kot_id: uuid7(), kot_no: "K1", order_id: orderId, section_id: item.section_id, line_ids: [lineId], fired_at: now }),
      ],
    },
  });
  expect((await push.json()).acks.map((a: { status: string }) => a.status)).toEqual(["applied", "applied", "applied"]);

  await page.getByRole("link", { name: "Live floor" }).click();
  const card = page.getByTestId("table-G1");
  await expect(card).toContainText("Tawa: New");
  await expect(card).toContainText("₹271.00"); // 2 x 135.50, before tax
  await card.click();
  await expect(page.getByRole("heading", { name: "Table G1" })).toBeVisible();
  await expect(page.getByText("In kitchen")).toBeVisible();

  // Revoke the captain's phone: its token stops working.
  await page.getByRole("link", { name: "Devices" }).click();
  const deviceRow = page.getByTestId("device-Captain Asha");
  await deviceRow.getByText("Revoke…").click();
  await deviceRow.getByRole("button", { name: "Yes, revoke Captain Asha" }).click();
  await expect(page.getByTestId("device-Captain Asha")).toContainText("Revoked");
  const after = await request.get(`${API}/v1/devices/me/menu`, { headers: { Authorization: `Bearer ${device.device_token}` } });
  expect(after.status()).toBe(401);

  // Sign out, and a bad token is refused.
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page).toHaveURL(/\/sign-in$/);
  await page.getByLabel("Owner token").fill("onx_o_not-a-real-token");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("form", { name: "Sign in" }).getByRole("alert")).toContainText("did not work");
  await page.goto("/floor");
  await expect(page).toHaveURL(/\/sign-in$/);
});

test("a seeded evening: live floor, bills, a bill, day close, books and alerts", async ({ page }) => {
  const out = execFileSync("go", ["run", "./cmd/seed"], { cwd: core, env: { ...process.env, API_URL: API }, encoding: "utf8" });
  const token = /onx_o_\S+/.exec(out)![0];
  const day = /business day (\d{4}-\d{2}-\d{2})/.exec(out)![1]; // after midnight, the evening is yesterday
  await signIn(page, token);

  await expect(page.getByText("3 / 10")).toBeVisible(); // T1, T2, R1 seated
  await expect(page.getByTestId("table-T1")).toContainText("Tandoor: Ready");
  await expect(page.getByTestId("table-T3")).toContainText("Free"); // paid and gone

  await page.goto(`/bills?date=${day}`);
  await expect(page.getByRole("row")).toHaveCount(4); // header + 3 bills
  await expect(page.getByText("₹1,659.00")).toBeVisible();
  await page.getByRole("link", { name: /-000002$/ }).click();
  await expect(page.getByRole("heading", { name: /Invoice .*-000002/ })).toBeVisible();
  await expect(page.getByText("Server agrees with the till")).toBeVisible();
  await expect(page.getByText("UPI clearing")).toBeVisible(); // Dr 1110: the UPI money is on its way
  await page.getByRole("link", { name: "See the table order →" }).click();
  await expect(page.getByRole("heading", { name: "Table T3" })).toBeVisible();
  await expect(page.getByText("Paid", { exact: true })).toBeVisible();

  // The day so far: Asha's afternoon shift ended Rs 50 short, Vikram's is
  // open, three tables are still eating.
  await page.goto(`/day?date=${day}`);
  const drawers = page.locator("section", { hasText: "Cash drawers" });
  await expect(drawers.getByText("₹50.00 short")).toBeVisible();
  await expect(drawers.getByText("Paid out ₹300.00 · Ice and lemons")).toBeVisible();
  const warnings = page.getByRole("list", { name: "Warnings" });
  await expect(warnings).toContainText("Vikram's shift is still open");
  await expect(warnings).toContainText("3 orders are still open (R1, T1, T2)");
  await expect(page.locator("section", { hasText: "Invoice numbers" }).getByText("No gaps")).toBeVisible();

  await page.getByRole("link", { name: "Books" }).click();
  await expect(page.getByText("Balanced")).toBeVisible();
  await expect(page.getByText("Cash over and short")).toBeVisible();
  await page.getByRole("link", { name: "Alerts" }).click();
  await expect(page.getByText("A cash drawer did not match at the end of a shift")).toBeVisible();
});

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
