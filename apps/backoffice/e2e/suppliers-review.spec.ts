// Adversarial review of slice 7 (stock part two) in the browser. Each test
// names a defect and fails against the code as reviewed; it should pass once
// the defect is fixed.
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";

const API = process.env.E2E_API_URL ?? "http://localhost:8080";

async function signUp(request: APIRequestContext, name: string): Promise<{ token: string }> {
  const res = await request.post(`${API}/v1/signup`, {
    data: {
      business_name: name,
      owner: { name: "Meera", phone: "+919876543210" },
      legal_entity: { name, state_code: "27" },
      outlet: { name: `${name} outlet` },
    },
  });
  expect(res.status()).toBe(201);
  return { token: (await res.json()).owner_token };
}

async function signIn(page: Page, token: string) {
  await page.goto("/sign-in");
  await page.getByLabel("Owner token").fill(token);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/floor$/);
}

// ---------------------------------------------------------------------------
// P2. A supplier whose phone is a city landline can never be edited again.
//
// The supplier page fills the Phone field with formatPhone(stored), which
// shows any "+91" number with ten digits as "XXXXX XXXXX". Saving the form
// sends that text back through normalisePhone, which only accepts a mobile
// number (first digit 6 to 9) unless the text starts with "+". A Mumbai
// landline, +91 22 2345 6789 (stored "+912223456789"), is shown as
// "22234 56789" and refused: "Write a 10-digit mobile number, or the full
// number with + and the country code." The same for Delhi (011), Chennai
// (044), Kolkata (033), Hyderabad (040), Pune (020) and 1800 numbers.
//
// Scenario: the owner adds "Crawford Market Traders" with its landline,
// typed with +91 as the form asks (accepted). Later they correct only the
// name and press Save.
//
// Impact: every later edit of that supplier (name, GSTIN) fails with a
// message about a field the owner did not touch, until they retype the
// phone with +91; wholesalers' phones are often landlines.
// ---------------------------------------------------------------------------
test("P2: a supplier with a city landline can still be renamed", async ({ page, request }) => {
  const o = await signUp(request, "Landline Kitchen");
  await signIn(page, o.token);
  await page.goto("/stock/suppliers");

  const add = page.getByRole("form", { name: "Add a supplier" });
  await add.getByLabel("Name").fill("Crawford Market Traders");
  await add.getByLabel("Phone").fill("+91 22 2345 6789");
  await add.getByRole("button", { name: "Add supplier" }).click();
  await expect(add.getByRole("status")).toContainText("Crawford Market Traders added");

  await page.getByRole("link", { name: "Crawford Market Traders" }).click();
  const edit = page.getByRole("form", { name: "Edit supplier" });
  // Only the name changes; the phone stays as the page shows it.
  await edit.getByLabel("Name").fill("Crawford Market Traders LLP");
  await edit.getByRole("button", { name: "Save" }).click();
  const answer = edit.getByRole("alert").or(edit.getByRole("status"));
  await expect(answer).toBeVisible();
  expect(await answer.textContent()).toContain("Saved");
  await expect(page.getByRole("heading", { name: "Crawford Market Traders LLP", exact: true })).toBeVisible();
});
