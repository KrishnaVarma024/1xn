"use server";

import { revalidatePath } from "next/cache";
import { api } from "@/lib/api";
import { attempt, fail, text, type FormState } from "@/lib/actions";
import { mustUUID } from "@/lib/ids";
import { parseRate, parseRupees } from "@/lib/money";

// Every action calls the Go API as the signed-in owner (api() adds the
// token). The API checks the token and the tenant on every call, so a forged
// POST to one of these actions gets nothing it could not get from the API.

function done(msg: string) {
  revalidatePath("/menu");
  return msg;
}

export async function addTaxProfile(_: FormState, form: FormData): Promise<FormState> {
  return attempt(async () => {
    const rate = parseRate(text(form, "rate"));
    if (rate === null) return fail("Rate must be a percentage from 0 to 40, like 5 or 18.");
    await api("/v1/catalogue/tax-profiles", {
      method: "POST",
      body: { name: text(form, "name"), rate_bp: rate, hsn_sac: text(form, "hsn_sac") || undefined },
    });
    return done("Tax rate added.");
  });
}

export async function addCategory(_: FormState, form: FormData): Promise<FormState> {
  return attempt(async () => {
    await api("/v1/catalogue/categories", { method: "POST", body: { name: text(form, "name") } });
    return done("Category added.");
  });
}

export async function addSection(_: FormState, form: FormData): Promise<FormState> {
  return attempt(async () => {
    await api("/v1/catalogue/sections", { method: "POST", body: { name: text(form, "name") } });
    return done("Kitchen section added.");
  });
}

export async function addModifierGroup(_: FormState, form: FormData): Promise<FormState> {
  return attempt(async () => {
    // One option per line: "Extra cheese, 40" (price optional, in rupees).
    const options = [];
    for (const raw of text(form, "options").split("\n")) {
      const line = raw.trim();
      if (!line) continue;
      const { name, price } = splitOption(line);
      if (!name || price === null) return fail(`Could not read the option "${line}". Write it as: Extra cheese, 40`);
      options.push({ name, price_paise: price });
    }
    if (options.length === 0) return fail("Add at least one option, one per line.");
    const min = Number(text(form, "min") || 0);
    const max = Number(text(form, "max") || 1);
    if (!Number.isInteger(min) || !Number.isInteger(max) || min < 0 || max < 1 || min > max) {
      fail("Pick at least / at most must be whole numbers, with at least ≤ at most.");
    }
    await api("/v1/catalogue/modifier-groups", {
      method: "POST",
      body: { name: text(form, "name"), min_select: min, max_select: max, options },
    });
    return done("Add-on group added.");
  });
}

/**
 * "Lobster, 1,200" is Lobster at ₹1,200, not "Lobster, 1" at ₹200: the price
 * is the number at the END after the first comma that starts one, and it may
 * use Indian digit grouping. A line with no price is free ("Mild").
 */
function splitOption(line: string): { name: string; price: number | null } {
  const m = /^(.*?)\s*,\s*(₹?\s*\d[\d,]*(?:\.\d{1,2})?)$/.exec(line);
  if (!m) return { name: line.trim(), price: 0 };
  return { name: m[1].trim(), price: parseRupees(m[2]) };
}

function itemBody(form: FormData) {
  const price = parseRupees(text(form, "price"));
  if (price === null) fail("Price must be in rupees, like 320 or 320.50.");
  const section = text(form, "section_id");
  return {
    name: text(form, "name"),
    price_paise: price,
    category_id: text(form, "category_id"),
    tax_profile_id: text(form, "tax_profile_id"),
    section_id: section || null,
    modifier_group_ids: form.getAll("group").map(String),
  };
}

export async function addItem(_: FormState, form: FormData): Promise<FormState> {
  return attempt(async () => {
    const body = itemBody(form);
    await api("/v1/catalogue/items", { method: "POST", body: { ...body, section_id: body.section_id ?? undefined } });
    return done(`${body.name} is on the menu.`);
  });
}

export async function updateItem(id: string, _: FormState, form: FormData): Promise<FormState> {
  return attempt(async () => {
    await api(`/v1/catalogue/items/${mustUUID(id)}`, { method: "PATCH", body: itemBody(form) });
    return done("Saved. Tills pick it up on their next menu check.");
  });
}

export async function setAvailable(id: string, available: boolean) {
  await api(`/v1/catalogue/items/${mustUUID(id)}/availability`, { method: "PATCH", body: { available: available === true } });
  revalidatePath("/menu");
}
