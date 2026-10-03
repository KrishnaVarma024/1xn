// Adversarial review of the stock slice in the browser. Each test names a
// defect and fails against the code as reviewed.
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";

const API = process.env.E2E_API_URL ?? "http://localhost:8080";

type Owner = { token: string; outletId: string };

async function signUp(request: APIRequestContext, name: string): Promise<Owner> {
  const res = await request.post(`${API}/v1/signup`, {
    data: {
      business_name: name,
      owner: { name: "Reviewer", phone: "+919876543210" },
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
  return (res.status() === 204 ? undefined : await res.json()) as T;
}

async function signIn(page: Page, token: string) {
  await page.goto("/sign-in");
  await page.getByLabel("Owner token").fill(token);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/floor$/);
}

async function dish(request: APIRequestContext, o: Owner, name: string) {
  const tax = await asOwner<{ id: string }>(request, o, "POST", "/v1/catalogue/tax-profiles", { name: "GST 5%", rate_bp: 500 });
  const cat = await asOwner<{ id: string }>(request, o, "POST", "/v1/catalogue/categories", { name: "Mains" });
  return asOwner<{ id: string }>(request, o, "POST", "/v1/catalogue/items", {
    name, price_paise: 45000, category_id: cat.id, tax_profile_id: tax.id,
  });
}

// S12. The recipe form shows every saved line plus three blank rows, but the
// Server Action reads only rows 0 to 29 (the API allows 50 ingredients). On a
// dish with 30 ingredients, the 31st the owner adds is dropped and the page
// says "Recipe saved."; on a dish with more than 30 (made through the API or
// an import), ANY save from the page deletes ingredients 31 and up.
test("S12 a recipe's 31st ingredient is not silently dropped", async ({ page, request }) => {
  const o = await signUp(request, "Review Long Recipe");
  const biryani = await dish(request, o, "Hyderabadi biryani");
  const items: { id: string }[] = [];
  for (let i = 1; i <= 31; i++) {
    items.push(await asOwner(request, o, "POST", "/v1/stock/items", { name: `Spice ${String(i).padStart(2, "0")}`, unit: "kg" }));
  }
  await asOwner(request, o, "PUT", `/v1/stock/recipes/${biryani.id}`, {
    lines: items.slice(0, 30).map((it) => ({ stock_item_id: it.id, qty: 5 })),
  });

  await signIn(page, o.token);
  await page.goto("/recipes");
  const card = page.getByTestId("dish-Hyderabadi biryani");
  await card.locator("summary").click();
  const form = card.getByRole("form", { name: "Recipe for Hyderabadi biryani" });
  await form.getByLabel("Ingredient 31", { exact: true }).selectOption({ label: "Spice 31 (kg)" });
  await form.getByLabel("Quantity 31", { exact: true }).fill("5 g");
  await form.getByRole("button", { name: "Save recipe" }).click();
  await expect(form.getByRole("status").or(form.getByRole("alert"))).toBeVisible();

  const { dishes } = await asOwner<{ dishes: { id: string; lines: unknown[] }[] }>(
    request, o, "GET", `/v1/outlets/${o.outletId}/stock/recipes`);
  const saved = dishes.find((d) => d.id === biryani.id)!;
  if ((await form.getByRole("status").count()) > 0) {
    // The page said "Recipe saved.": then all 31 ingredients must be saved.
    expect(saved.lines.length, "the page said the recipe was saved").toBe(31);
  }
});

// S13. On a phone the food cost page scrolls sideways: the page header's
// date-range form (two date inputs, "to" and a button) is a flex row that
// does not wrap, wider than the screen (+4 px at 390 px, more at 360 px, a
// common Android width). Every other page fits, including the rest of stock.
test("S13 stock pages fit a phone screen without sideways scrolling", async ({ page, request }) => {
  const o = await signUp(request, "Review Phone Stock");
  await dish(request, o, "Paneer tikka");
  const paneer = await asOwner<{ id: string }>(request, o, "POST", "/v1/stock/items", { name: "Paneer", unit: "kg" });
  await signIn(page, o.token);
  const wide: string[] = [];
  for (const width of [360, 390]) {
    await page.setViewportSize({ width, height: 800 });
    for (const p of ["/stock", "/stock/receive", "/stock/waste", "/stock/count", "/stock/food-cost", `/stock/${paneer.id}`, "/recipes"]) {
      await page.goto(p);
      await page.waitForLoadState("networkidle");
      const extra = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      if (extra > 0) wide.push(`${p} at ${width}px (+${extra}px)`);
    }
  }
  expect(wide, "pages wider than the phone").toEqual([]);
});

// Seen in Safari (3 Oct): a grey scrollbar beside the stock tabs. Each tab was
// pulled down a pixel over the row's border, so the scrolling row overflowed
// by one pixel and Safari, which shows scrollbars when a mouse is attached,
// drew one. No scrolling box should overflow when nothing needs to scroll.
test("the stock tabs and the sidebar do not overflow their scroll boxes", async ({ page, request }) => {
  const o = await signUp(request, "Review Tab Overflow");
  await signIn(page, o.token);
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto("/stock");
  const overflow = await page.evaluate(() =>
    [...document.querySelectorAll("nav")].map((n) => ({
      nav: n.getAttribute("aria-label"),
      down: n.scrollHeight - n.clientHeight,
      across: n.scrollWidth - n.clientWidth,
    })),
  );
  expect(overflow.filter((n) => n.down > 0 || n.across > 0)).toEqual([]);
});
