// Regression tests from the adversarial review of the back office. The first
// group each reproduced a real bug; the second records attacks that held.
import { spawn, type ChildProcess } from "node:child_process";
import net from "node:net";
import path from "node:path";
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";

const API = process.env.E2E_API_URL ?? "http://localhost:8080";
const WEB_PORT = Number(process.env.E2E_WEB_PORT ?? 3100);
const app = path.resolve(__dirname, "..");

type Owner = { token: string; outletId: string; outletName: string };

async function signUpViaAPI(request: APIRequestContext, name: string): Promise<Owner> {
  const res = await request.post(`${API}/v1/signup`, {
    data: {
      business_name: name,
      owner: { name: "Reviewer", phone: "+919876543210" },
      legal_entity: { name, state_code: "29" },
      outlet: { name: `${name} outlet` },
    },
  });
  expect(res.status()).toBe(201);
  const body = await res.json();
  return { token: body.owner_token, outletId: body.outlet_id, outletName: `${name} outlet` };
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

// A tiny menu: one 5% GST rate, one category, optional extras.
async function basicMenu(request: APIRequestContext, o: Owner) {
  const tax = await asOwner<{ id: string }>(request, o, "POST", "/v1/catalogue/tax-profiles", { name: "GST 5%", rate_bp: 500 });
  const cat = await asOwner<{ id: string }>(request, o, "POST", "/v1/catalogue/categories", { name: "Mains" });
  return { tax: tax.id, cat: cat.id };
}

// ---------------------------------------------------------------------------
// Findings

test("with the API down, pages show the app's own error page (not Next's bare 500)", async ({ browser }) => {
  // A second production server whose API address points at nothing.
  const port = Number(process.env.REVIEW_DOWN_PORT ?? WEB_PORT + 101);
  const srv: ChildProcess = spawn("npx", ["next", "start", "--port", String(port)], {
    cwd: app,
    env: { ...process.env, ONEXN_API_URL: "http://127.0.0.1:9", NEXT_TELEMETRY_DISABLED: "1" },
    stdio: "ignore",
    detached: true,
  });
  try {
    for (let i = 0; i < 100; i++) {
      try {
        if ((await fetch(`http://localhost:${port}/sign-in`)).ok) break;
      } catch {}
      await new Promise((r) => setTimeout(r, 200));
    }
    const ctx = await browser.newContext({ baseURL: `http://localhost:${port}` });
    await ctx.addCookies([{ name: "onx_owner", value: "onx_o_whatever", url: `http://localhost:${port}` }]);
    const page = await ctx.newPage();
    await page.goto("/floor");
    // src/app/(app)/error.tsx says this; Next's built-in global error says
    // "This page couldn’t load. A server error occurred." and has no way to
    // sign out.
    await expect(page.getByText("Usually the API is not running")).toBeVisible({ timeout: 5000 });
    await ctx.close();
  } finally {
    try {
      process.kill(-srv.pid!, "SIGTERM");
    } catch {}
  }
});

// Starts `next start` on a port with the given API address; returns a stopper.
async function startWeb(port: number, apiURL: string): Promise<() => void> {
  const srv: ChildProcess = spawn("npx", ["next", "start", "--port", String(port)], {
    cwd: app,
    env: { ...process.env, ONEXN_API_URL: apiURL, NEXT_TELEMETRY_DISABLED: "1" },
    stdio: "ignore",
    detached: true,
  });
  for (let i = 0; i < 100; i++) {
    try {
      if ((await fetch(`http://localhost:${port}/sign-in`)).ok) break;
    } catch {}
    await new Promise((r) => setTimeout(r, 200));
  }
  return () => {
    try {
      process.kill(-srv.pid!, "SIGTERM");
    } catch {}
  };
}

test("the live floor survives a 7-second API blip (wall display keeps refreshing)", async ({ browser, request }) => {
  test.setTimeout(60_000);
  const o = await signUpViaAPI(request, "Blip Bistro");
  // A switchable TCP proxy in front of the API.
  const target = new URL(API);
  let up = true;
  const proxy = net.createServer((c) => {
    if (!up) return c.destroy();
    const u = net.connect(Number(target.port || 80), target.hostname);
    c.pipe(u).pipe(c);
    c.on("error", () => u.destroy());
    u.on("error", () => c.destroy());
  });
  await new Promise<void>((r) => proxy.listen(0, "127.0.0.1", r));
  const proxyPort = (proxy.address() as net.AddressInfo).port;
  const port = Number(process.env.REVIEW_BLIP_PORT ?? WEB_PORT + 102);
  const stop = await startWeb(port, `http://127.0.0.1:${proxyPort}`);
  try {
    const ctx = await browser.newContext({ baseURL: `http://localhost:${port}` });
    await ctx.addCookies([{ name: "onx_owner", value: o.token, url: `http://localhost:${port}` }]);
    const page = await ctx.newPage();
    await page.goto("/floor");
    await expect(page.getByRole("heading", { name: "Live floor" })).toBeVisible();
    up = false; // API restarts / deploys / Wi-Fi drops
    await page.waitForTimeout(7_000);
    up = true;
    // AutoRefresh polls every 5 s; the floor should come back on its own.
    await expect(page.getByRole("heading", { name: "Live floor" })).toBeVisible({ timeout: 15_000 });
    await ctx.close();
  } finally {
    stop();
    proxy.close();
  }
});

test("sign-up refuses a 9-digit mobile number instead of saving it as a foreign number", async ({ page, request }) => {
  await page.goto("/sign-up");
  await page.getByLabel("Restaurant name").fill("Typo Tiffin Centre");
  await page.getByLabel("Your name").fill("Meena");
  await page.getByLabel("Mobile number").fill("98765 4321"); // one digit short
  await page.getByLabel("First outlet").fill("Jayanagar");
  await page.getByRole("button", { name: "Create restaurant" }).click();
  const form = page.getByRole("form", { name: "Create your restaurant" });
  const token = page.getByTestId("owner-token");
  await expect(form.getByRole("alert").or(token)).toBeVisible();
  if (await token.isVisible()) {
    const t = (await token.textContent())!.trim();
    const me = await (await request.get(`${API}/v1/me`, { headers: { Authorization: `Bearer ${t}` } })).json();
    throw new Error(`a 9-digit Indian mobile was accepted and stored as ${me.user.phone}`);
  }
});

test("a pasted token with a stray non-ASCII character is 'wrong', not 'API unreachable'", async ({ page }) => {
  await page.goto("/sign-in");
  // The placeholder itself ends in "…"; chat apps also add zero-width spaces.
  await page.getByLabel("Owner token").fill("onx_o_abc\u200b");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("form", { name: "Sign in" }).getByRole("alert")).toContainText("did not work");
});

test("editing a dish's price keeps the order of its add-on groups", async ({ page, request }) => {
  const o = await signUpViaAPI(request, "Order Of Addons");
  const m = await basicMenu(request, o);
  const opts = [{ name: "One" }, { name: "Two" }];
  const zesty = await asOwner<{ id: string }>(request, o, "POST", "/v1/catalogue/modifier-groups", { name: "Zesty dips", options: opts });
  const amaze = await asOwner<{ id: string }>(request, o, "POST", "/v1/catalogue/modifier-groups", { name: "Amazing sides", options: opts });
  const item = await asOwner<{ id: string }>(request, o, "POST", "/v1/catalogue/items", {
    name: "Combo", price_paise: 20000, category_id: m.cat, tax_profile_id: m.tax, modifier_group_ids: [zesty.id, amaze.id],
  });

  await signIn(page, o.token);
  await page.goto("/menu");
  const row = page.getByTestId("item-Combo");
  await row.getByText("Edit").click();
  await row.getByLabel("Price (₹)").fill("210");
  await row.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByTestId("item-Combo")).toContainText("₹210");

  const menu = await asOwner<{ items: { id: string; modifier_group_ids: string[] }[] }>(request, o, "GET", `/v1/outlets/${o.outletId}/menu`);
  const after = menu.items.find((i) => i.id === item.id)!;
  // The till asks "Zesty dips" first; a price edit must not reorder that.
  expect(after.modifier_group_ids).toEqual([zesty.id, amaze.id]);
});

test("the live floor does not count lines already billed on a split table as 'on open orders'", async ({ page, request }) => {
  const o = await signUpViaAPI(request, "Split Bill Saloon");
  const m = await basicMenu(request, o);
  const thali = await asOwner<{ id: string }>(request, o, "POST", "/v1/catalogue/items", {
    name: "Thali", price_paise: 10000, category_id: m.cat, tax_profile_id: m.tax,
  });
  const floor = await asOwner<{ id: string }>(request, o, "POST", `/v1/outlets/${o.outletId}/floors`, { name: "Hall" });
  const table = await asOwner<{ id: string }>(request, o, "POST", `/v1/outlets/${o.outletId}/tables`, { floor_id: floor.id, label: "S1" });
  const code = await asOwner<{ code: string }>(request, o, "POST", `/v1/outlets/${o.outletId}/pairing-codes`, { role: "pos" });
  const dev = await (await request.post(`${API}/v1/devices/register`, { data: { pairing_code: code.code, name: "Till" } })).json();

  const order = uuid7();
  const [l1, l2] = [uuid7(), uuid7()];
  const now = new Date();
  const line = (id: string) => ({ line_id: id, item_id: thali.id, name: "Thali", qty: 1, unit_price_paise: 10000, tax_rate_bp: 500 });
  // Guest 1 pays for their thali (split bill): Rs 100 + 5% GST = Rs 105.
  const input = { price_mode: "exclusive", supply: "intra", rounding: "nearest_rupee", lines: [{ ref: thali.id, name: "Thali", qty: 1, unit_price_paise: 10000, tax_rate_bp: 500 }] };
  const totals = {
    lines: [{ ref: thali.id, gross: 10000, line_discount: 0, bill_discount: 0, net: 10000 }],
    subtotal: 10000, discount_total: 0, service_charge: 0, taxable_total: 10000,
    tax_groups: [{ rate_bp: 500, net: 10000, goods_value: 10000, service_charge: 0, taxable: 10000, cgst: 250, sgst: 250, igst: 0 }],
    cgst: 250, sgst: 250, igst: 0, tax_total: 500, round_off: 0, grand_total: 10500,
  };
  const ist = new Date(now.getTime() + 5.5 * 3600e3);
  const fy = ist.getUTCMonth() >= 3 ? ist.getUTCFullYear() : ist.getUTCFullYear() - 1;
  const fyCode = `${String(fy % 100).padStart(2, "0")}${String((fy + 1) % 100).padStart(2, "0")}`;
  const op = (seq: number, type: string, payload: unknown) => ({ op_id: uuid7(), device_seq: seq, type, payload });
  const push = await request.post(`${API}/v1/sync/push`, {
    headers: { Authorization: `Bearer ${dev.device_token}` },
    data: {
      ops: [
        op(1, "order.opened", { order_id: order, channel: "dine_in", table_id: table.id, covers: 2, opened_at: now.toISOString() }),
        op(2, "order.lines_added", { order_id: order, at: now.toISOString(), lines: [line(l1), line(l2)] }),
        op(3, "bill.settled", {
          bill_id: uuid7(), invoice_no: `${dev.invoice_prefix}-${fyCode}-000001`, issued_at: now.toISOString(),
          business_date: ist.toISOString().slice(0, 10), channel: "dine_in", table_label: "S1", covers: 1,
          input, totals, payments: [{ method: "cash", amount_paise: 10500 }], order_id: order, line_ids: [l1],
        }),
      ],
    },
  });
  expect((await push.json()).acks.map((a: { status: string }) => a.status)).toEqual(["applied", "applied", "applied"]);

  await signIn(page, o.token);
  // The order page itself is right: total Rs 200, of which Rs 100 already billed.
  // The floor card and the "On open orders" stat still show Rs 200 (the paid
  // thali is counted again on top of Rs 105 in today's Sales).
  await expect(page.getByTestId("table-S1")).toContainText("₹100.00");
});

test("an impossible ?date on the bills page does not blame the API", async ({ page, request }) => {
  const o = await signUpViaAPI(request, "Calendar Corner");
  await signIn(page, o.token);
  for (const d of ["2026-02-30", "0000-01-01"]) {
    await page.goto(`/bills?date=${d}`);
    await expect(page.getByRole("heading", { name: "Bills" })).toBeVisible({ timeout: 5000 });
  }
});

test("an add-on priced with Indian digit grouping ('Lobster, 1,200') keeps its name and price", async ({ page, request }) => {
  const o = await signUpViaAPI(request, "Grouping Grill");
  await signIn(page, o.token);
  await page.goto("/menu");
  const grp = page.getByRole("form", { name: "Add an add-on group" });
  await grp.getByLabel("Group name").fill("Premium");
  // parseRupees itself accepts "1,200"; the option splitter cuts at the LAST comma.
  await grp.getByLabel("Options").fill("Lobster, 1,200\nCaviar, 2,500.50");
  await grp.getByRole("button", { name: "Add group" }).click();
  await expect(grp.getByRole("status").or(grp.getByRole("alert"))).toBeVisible();
  const menu = await asOwner<{ modifier_groups: { name: string; options: { name: string; price_paise: number }[] }[] }>(
    request, o, "GET", `/v1/outlets/${o.outletId}/menu`);
  const g = menu.modifier_groups.find((x) => x.name === "Premium");
  // Either refuse the line or read it right; never store "Lobster, 1" at Rs 200.
  if (g) expect(g.options.map((x) => [x.name, x.price_paise])).toEqual([["Lobster", 120000], ["Caviar", 250050]]);
});

test("the price of a dish with a long Hindi name can be edited", async ({ page, request }) => {
  const o = await signUpViaAPI(request, "Hindi Thali Ghar");
  const m = await basicMenu(request, o);
  const name = "प".repeat(100); // 100 characters (the DB allows 200), 300 bytes
  await asOwner(request, o, "POST", "/v1/catalogue/items", { name, price_paise: 10000, category_id: m.cat, tax_profile_id: m.tax });
  await signIn(page, o.token);
  await page.goto("/menu");
  const row = page.getByTestId(`item-${name}`);
  await row.getByText("Edit").click();
  await row.getByLabel("Price (₹)").fill("110");
  await row.getByRole("button", { name: "Save changes" }).click();
  await expect(row.getByRole("status")).toContainText("Saved");
});

test("every page of a brand-new restaurant renders its empty state", async ({ page, request }) => {
  const o = await signUpViaAPI(request, "Fresh Start Kitchen");
  await signIn(page, o.token);
  const broken: string[] = [];
  for (const p of ["/floor", "/menu", "/tables", "/devices", "/bills", "/day", "/alerts", "/books"]) {
    await page.goto(p);
    await page.waitForLoadState("networkidle");
    if (await page.getByText("This page could not load").count()) broken.push(p);
  }
  // /books: GET /v1/ledger/trial-balance answers {"accounts": null} before the
  // first bill, and books/page.tsx calls tb.accounts.reduce(...).
  expect(broken).toEqual([]);
});

// ---------------------------------------------------------------------------
// Attacks that held

test("an outlet cookie naming someone else's outlet falls back to your own", async ({ page, request }) => {
  const mine = await signUpViaAPI(request, "Cookie Owner");
  const theirs = await signUpViaAPI(request, "Cookie Victim");
  await page.context().addCookies([{ name: "onx_outlet", value: theirs.outletId, url: `http://localhost:${WEB_PORT}` }]);
  await signIn(page, mine.token);
  await page.goto("/tables");
  await expect(page.getByText(`Floors and tables of ${mine.outletName}`)).toBeVisible();
  await expect(page.getByText(theirs.outletName)).toHaveCount(0);
});

test("a revoked/garbage token cookie lands on /sign-in?expired=1 once, and signing in again works", async ({ page, request }) => {
  const o = await signUpViaAPI(request, "Stale Cookie Cafe");
  await page.context().addCookies([{ name: "onx_owner", value: "onx_o_revoked", url: `http://localhost:${WEB_PORT}` }]);
  const seen: string[] = [];
  page.on("framenavigated", (f) => f === page.mainFrame() && seen.push(f.url()));
  await page.goto("/menu");
  await expect(page).toHaveURL(/\/sign-in\?expired=1$/);
  await expect(page.getByText("Your session ended")).toBeVisible();
  expect(seen.length).toBeLessThan(4);
  await page.getByLabel("Owner token").fill(o.token);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/floor$/);
});

test("server actions reject tampered bound ids and cross-site origins", async ({ page, request }) => {
  const o = await signUpViaAPI(request, "Tamper Proof Thali");
  const m = await basicMenu(request, o);
  const item = await asOwner<{ id: string }>(request, o, "POST", "/v1/catalogue/items", {
    name: "Dosa", price_paise: 8000, category_id: m.cat, tax_profile_id: m.tax,
  });
  await signIn(page, o.token);
  await page.goto("/menu");
  // Capture the real "Mark sold out" Server Action call.
  const reqP = page.waitForRequest((r) => r.method() === "POST" && !!r.headers()["next-action"]);
  await page.getByTestId("item-Dosa").getByRole("button", { name: "Mark sold out" }).click();
  const actionId = (await reqP).headers()["next-action"];
  await expect(page.getByTestId("item-Dosa")).toContainText("Sold out");

  const replay = (origin: string, body: string) =>
    page.request.post("/menu", {
      headers: { "Next-Action": actionId, Origin: origin, "Content-Type": "text/plain;charset=UTF-8" },
      data: body,
    });
  // Path traversal through the bound id: mustUUID throws before any API call.
  expect((await replay(`http://localhost:${WEB_PORT}`, JSON.stringify(["../../ledger/trial-balance", true]))).status()).toBe(500);
  // A forged cross-site POST: Next.js refuses (Origin != Host).
  expect((await replay("http://evil.example", JSON.stringify([item.id, true]))).status()).toBe(500);
  const menu = await asOwner<{ items: { id: string; available: boolean }[] }>(request, o, "GET", `/v1/outlets/${o.outletId}/menu`);
  expect(menu.items.find((i) => i.id === item.id)!.available).toBe(false); // neither replay changed it
  // The same id + same origin works, so the replays above failed for the right reason.
  expect((await replay(`http://localhost:${WEB_PORT}`, JSON.stringify([item.id, true]))).status()).toBe(200);
});

test("the owner token and API address never reach page HTML or scripts", async ({ page, request }) => {
  const o = await signUpViaAPI(request, "Leak Check Lounge");
  const bodies: string[] = [];
  page.on("response", async (r) => {
    const ct = r.headers()["content-type"] ?? "";
    if (/html|javascript|x-component|text\/plain/.test(ct)) bodies.push(await r.text().catch(() => ""));
  });
  await signIn(page, o.token);
  for (const p of ["/floor", "/menu", "/tables", "/devices", "/bills", "/day", "/alerts", "/books"]) {
    await page.goto(p);
    await page.waitForLoadState("networkidle");
  }
  const all = bodies.join("\n");
  expect(all).not.toContain(o.token);
  expect(all).not.toContain(new URL(API).host);
  expect(await page.evaluate(() => document.cookie)).not.toContain("onx_o_");
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
