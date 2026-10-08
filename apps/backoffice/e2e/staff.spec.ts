// Staff sign in with their mobile number and a code, and see what their role
// allows. The codes come from the API's log (E2E_API_LOG): no SMS here.
import { readFileSync } from "node:fs";
import { expect, test, type APIRequestContext, type Browser, type Page } from "@playwright/test";

const API = process.env.E2E_API_URL ?? "http://localhost:8080";
const LOG = process.env.E2E_API_LOG ?? "";

async function signUp(request: APIRequestContext, name: string): Promise<{ token: string; outletId: string }> {
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

async function signInWithToken(page: Page, token: string) {
  await page.goto("/sign-in/token");
  await page.getByLabel("Owner token").fill(token);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/floor$/);
}

/** A mobile number no other test uses: 10 digits starting with 7. */
function mobile(): string {
  return "7" + String(Math.floor(Math.random() * 1e9)).padStart(9, "0");
}

/** The latest code the API printed for a number. */
async function codeFor(local: string, after = ""): Promise<string> {
  expect(LOG, "run through scripts/test-web.sh, which sets E2E_API_LOG").not.toBe("");
  for (let i = 0; i < 50; i++) {
    const found = [...readFileSync(LOG, "utf8").matchAll(new RegExp(`SIGN-IN CODE (\\d{6}) for \\+91${local}`, "g"))];
    const code = found.at(-1)?.[1];
    if (code && code !== after) return code;
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`no code for ${local} in the API log`);
}

async function signInWithCode(browser: Browser, local: string) {
  const page = await (await browser.newContext()).newPage();
  await page.goto("/sign-in");
  await page.getByLabel("Mobile number").fill(local.replace(/(\d{5})(\d{5})/, "$1 $2"));
  await page.getByRole("button", { name: "Send me a code" }).click();
  await expect(page).toHaveURL(/\/sign-in\/code$/);
  await expect(page.getByText(`We sent a 6-digit code to ${local.slice(0, 5)} ${local.slice(5)}`)).toBeVisible();
  await page.getByLabel("Code", { exact: true }).fill(await codeFor(local));
  await page.getByRole("button", { name: "Sign in" }).click();
  return page;
}

test("the owner adds a cashier, who signs in with a code and sees only what a cashier may", async ({ page, request, browser }) => {
  const o = await signUp(request, "Staff Sign In");
  await signInWithToken(page, o.token);
  await page.getByRole("link", { name: "Staff" }).click();
  await expect(page.getByTestId("person-Meera")).toContainText("Owner");

  const cashier = mobile();
  const add = page.getByRole("form", { name: "Add someone" });
  await add.getByLabel("Name").fill("Asha");
  await add.getByLabel("Mobile number").fill("12345");
  await add.getByRole("button", { name: "Add" }).click();
  await expect(add.getByRole("alert")).toContainText("10-digit mobile number");
  await add.getByLabel("Mobile number").fill(cashier);
  await add.getByRole("button", { name: "Add" }).click();
  await expect(add.getByRole("status")).toContainText("Asha added");
  await expect(page.getByTestId("person-Asha")).toContainText("Cashier");

  // Asha signs in on her own phone.
  const asha = await signInWithCode(browser, cashier);
  await expect(asha).toHaveURL(/\/floor$/);
  await expect(asha.getByTestId("signed-in-as")).toContainText("Asha · Cashier");
  const nav = asha.getByRole("navigation", { name: "Main" });
  await expect(nav.getByRole("link")).toHaveText(["Live floor", "Stock", "Bills"]);
  await asha.goto("/books");
  await expect(asha.getByText("Cashiers can't open this page")).toBeVisible();
  await asha.goto("/stock");
  await expect(asha.getByRole("navigation", { name: "Stock" }).getByRole("link")).toHaveText(["Items", "Receive goods", "Wastage"]);
  await expect(asha.getByRole("form", { name: "Add a stock item" })).toHaveCount(0);
  await asha.goto("/stock/receive");
  await expect(asha.getByRole("form", { name: "Receive goods" })).toHaveCount(0); // no stock items yet: the empty state
  await expect(asha.getByText("Add stock items first")).toBeVisible();

  // The owner sees her signed in, makes her a manager: her next page shows more.
  await page.reload();
  await expect(page.getByTestId("person-Asha")).toContainText("Signed in");
  await page.getByTestId("person-Asha").locator("summary").click();
  const change = page.getByRole("form", { name: "Change Asha" });
  await change.getByLabel("Role").selectOption("manager");
  await change.getByRole("button", { name: "Save" }).click();
  await expect(change.getByRole("status")).toContainText("Saved");
  await asha.goto("/floor");
  await expect(asha.getByTestId("signed-in-as")).toContainText("Asha · Manager");
  await expect(asha.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Day close" })).toBeVisible();

  // Switched off: she is out at once.
  await page.getByRole("form", { name: "Switch off Asha" }).getByRole("button", { name: "Switch off" }).click();
  await expect(page.getByTestId("person-Asha")).toContainText("Switched off");
  await asha.goto("/stock");
  await expect(asha).toHaveURL(/\/sign-in\?expired=1$/);
});

test("a wrong code says how many tries are left; one number at two businesses picks one", async ({ request, browser }) => {
  const a = await signUp(request, "Morning Cafe");
  const b = await signUp(request, "Evening Bar");
  const both = mobile();
  for (const [o, role] of [[a, "manager"], [b, "cashier"]] as const) {
    const res = await request.post(`${API}/v1/staff`, {
      headers: { Authorization: `Bearer ${o.token}` },
      data: { name: "Vikram", phone: `+91${both}`, role },
    });
    expect(res.status()).toBe(201);
  }

  const page = await (await browser.newContext()).newPage();
  await page.goto("/sign-in");
  await page.getByLabel("Mobile number").fill(both);
  await page.getByRole("button", { name: "Send me a code" }).click();
  const code = await codeFor(both);
  const wrong = code === "000000" ? "111111" : "000000";
  const form = page.getByRole("form", { name: "Enter the code" });
  await form.getByLabel("Code").fill(wrong);
  await form.getByRole("button", { name: "Sign in" }).click();
  await expect(form.getByRole("alert")).toContainText("That code is wrong; 4 tries left.");

  // Asking again straight away is refused, politely.
  await page.getByRole("button", { name: "Send a new code" }).click();
  await expect(page.getByRole("alert").filter({ hasText: "wait" })).toContainText("A code was just sent; wait");

  await form.getByLabel("Code").fill(code.replace(/(\d{3})(\d{3})/, "$1 $2"));
  await form.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: "Which business?" })).toBeVisible();
  const businesses = page.getByRole("list", { name: "Businesses" });
  await expect(businesses.getByRole("button")).toHaveCount(2);
  await expect(businesses.getByRole("button").first()).toContainText("Evening Bar"); // newest first
  await businesses.getByRole("button", { name: /Morning Cafe/ }).click();
  await expect(page).toHaveURL(/\/floor$/);
  await expect(page.getByTestId("business-name")).toHaveText("Morning Cafe");
  await expect(page.getByTestId("signed-in-as")).toContainText("Vikram · Manager");

  // Signing out ends the session at the API, not just in this browser.
  const session = (await page.context().cookies()).find((c) => c.name === "onx_session")!.value;
  expect(session).toMatch(/^onx_s_/);
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page).toHaveURL(/\/sign-in$/);
  expect((await request.get(`${API}/v1/me`, { headers: { Authorization: `Bearer ${session}` } })).status()).toBe(401);
});

test("a number with no account gets the same answer, and the code never arrives", async ({ page }) => {
  const nobody = mobile();
  await page.goto("/sign-in");
  await page.getByLabel("Mobile number").fill(nobody);
  await page.getByRole("button", { name: "Send me a code" }).click();
  await expect(page).toHaveURL(/\/sign-in\/code$/);
  await page.getByLabel("Code", { exact: true }).fill("123456");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("form", { name: "Enter the code" }).getByRole("alert")).toContainText("That code is wrong");
  expect(readFileSync(LOG, "utf8")).not.toContain(`+91${nobody}`);
});
