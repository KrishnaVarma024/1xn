// Adversarial review of slice 8 in the browser. Each test names a defect and
// failed against the code as reviewed; it passes once fixed.
import { readFileSync } from "node:fs";
import { expect, test, type APIRequestContext, type Browser } from "@playwright/test";

const API = process.env.E2E_API_URL ?? "http://localhost:8080";
const LOG = process.env.E2E_API_LOG ?? "";

async function signUp(request: APIRequestContext, name: string): Promise<string> {
  const res = await request.post(`${API}/v1/signup`, {
    data: { business_name: name, owner: { name: "Meera", phone: "+919876543210" }, legal_entity: { name, state_code: "29" }, outlet: { name: "Main" } },
  });
  expect(res.status()).toBe(201);
  return (await res.json()).owner_token;
}

async function cashierPage(request: APIRequestContext, browser: Browser, owner: string) {
  const local = "7" + String(Math.floor(Math.random() * 1e9)).padStart(9, "0");
  const res = await request.post(`${API}/v1/staff`, {
    headers: { Authorization: `Bearer ${owner}` },
    data: { name: "Asha", phone: `+91${local}`, role: "cashier" },
  });
  expect(res.status()).toBe(201);
  const page = await (await browser.newContext()).newPage();
  await page.goto("/sign-in");
  await page.getByLabel("Mobile number").fill(local);
  await page.getByRole("button", { name: "Send me a code" }).click();
  await expect(page).toHaveURL(/\/sign-in\/code$/);
  let code = "";
  for (let i = 0; i < 50 && !code; i++) {
    code = [...readFileSync(LOG, "utf8").matchAll(new RegExp(`SIGN-IN CODE (\\d{6}) for \\+91${local}`, "g"))].at(-1)?.[1] ?? "";
    if (!code) await new Promise((r) => setTimeout(r, 100));
  }
  await page.getByLabel("Code", { exact: true }).fill(code);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/floor$/);
  return page;
}

// ---------------------------------------------------------------------------
// W1. Pages point a cashier at things a cashier cannot do.
//
// The live floor of a restaurant with no tables yet offers "Set up tables",
// which opens a page cashiers cannot use; the delivery form tells them to
// "add the pack on the item's page", where cashiers have no pack form. Each
// is a dead end the role-aware navigation was meant to remove.
// ---------------------------------------------------------------------------
test("W1: a cashier is not sent to pages and forms a cashier cannot use", async ({ request, browser }) => {
  const owner = await signUp(request, "Review Dead Ends");
  const item = await request.post(`${API}/v1/stock/items`, {
    headers: { Authorization: `Bearer ${owner}` },
    data: { name: "Rice", unit: "kg", reorder_level: 0 },
  });
  expect(item.status()).toBe(201);
  const page = await cashierPage(request, browser, owner);
  await expect(page.getByText("No tables yet")).toBeVisible();
  await expect(page.getByRole("link", { name: "Set up tables" })).toHaveCount(0);
  await page.goto("/stock/receive");
  await expect(page.getByRole("form", { name: "Receive goods" })).toBeVisible();
  await expect(page.getByText("Add the pack on the item")).toHaveCount(0);
  // The delivery form offers only what a cashier may book.
  await expect(page.getByLabel("Paid by").locator("option")).toHaveText(["On credit (pay later)", "Cash from a till drawer"]);
});
