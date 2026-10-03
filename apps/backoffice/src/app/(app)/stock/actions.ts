"use server";

import { revalidatePath } from "next/cache";
import { api } from "@/lib/api";
import { attempt, fail, text, type FormState } from "@/lib/actions";
import { getContext } from "@/lib/context";
import { isRealDay } from "@/lib/dates";
import { mustUUID } from "@/lib/ids";
import { parseRupees } from "@/lib/money";
import { parseQty, type Unit } from "@/lib/quantity";
import type { StockList } from "@/lib/types";

const units: Unit[] = ["kg", "l", "pc"];

/** The recipe form shows at most this many rows; the API takes 50 ingredients. */
const RECIPE_MAX = 50;
const RECIPE_ROWS = RECIPE_MAX;

/** The tenant's stock items, to know each one's unit when reading quantities. */
async function itemUnits(): Promise<Map<string, Unit>> {
  const { outlet } = await getContext();
  const list = await api<StockList>(`/v1/outlets/${outlet.id}/stock`);
  return new Map(list.items.map((i) => [i.id, i.unit]));
}

function qty(form: FormData, name: string, unit: Unit, what: string): number {
  const q = parseQty(text(form, name), unit);
  if (q === null) fail(`${what}: write a quantity like 1.5 or 250 g`);
  return q;
}

export async function createStockItem(_: FormState, form: FormData): Promise<FormState> {
  return attempt(async () => {
    const name = text(form, "name");
    const unit = text(form, "unit") as Unit;
    if (!name) fail("Give the item a name.");
    if (!units.includes(unit)) fail("Pick a unit.");
    const reorder = text(form, "reorder") ? qty(form, "reorder", unit, "Reorder level") : 0;
    await api("/v1/stock/items", { method: "POST", body: { name, unit, reorder_level: reorder } });
    revalidatePath("/stock");
    return `${name} added.`;
  });
}

export async function updateStockItem(id: string, unit: Unit, _: FormState, form: FormData): Promise<FormState> {
  return attempt(async () => {
    mustUUID(id);
    if (!units.includes(unit)) fail("Reload the page and try again.");
    const name = text(form, "name");
    if (!name) fail("Give the item a name.");
    const reorder = text(form, "reorder") ? qty(form, "reorder", unit, "Reorder level") : 0;
    await api(`/v1/stock/items/${id}`, {
      method: "PATCH",
      body: { name, reorder_level: reorder, active: form.get("active") === "on" },
    });
    revalidatePath("/stock");
    revalidatePath(`/stock/${id}`);
    return "Saved.";
  });
}

function day(form: FormData): string | undefined {
  const d = text(form, "date");
  if (!d) return undefined;
  if (!isRealDay(d)) fail("Pick a real date.");
  return d;
}

/** Purchase rows are line0..lineN: item, quantity and cost; empty rows are skipped. */
export async function recordPurchase(docId: string, _: FormState, form: FormData): Promise<FormState> {
  return attempt(async () => {
    mustUUID(docId);
    const supplier = text(form, "supplier");
    if (!supplier) fail("Who delivered it? Add the supplier.");
    const paidBy = text(form, "paid_by");
    if (!["safe", "till", "bank", "credit"].includes(paidBy)) fail("How was it paid?");
    const unitOf = await itemUnits();
    const lines: { stock_item_id: string; qty: number; cost: number }[] = [];
    for (let i = 0; i < 20; i++) {
      const item = text(form, `item${i}`);
      const q = text(form, `qty${i}`);
      const c = text(form, `cost${i}`);
      if (!item && !q && !c) continue;
      const unit = unitOf.get(item);
      if (!unit) fail(`Row ${i + 1}: pick an item.`);
      const n = qty(form, `qty${i}`, unit, `Row ${i + 1}`);
      if (n <= 0) fail(`Row ${i + 1}: the quantity must be above zero.`);
      const cost = parseRupees(c);
      if (cost === null) fail(`Row ${i + 1}: write what it cost, like 450 or 1,200.50.`);
      lines.push({ stock_item_id: item, qty: n, cost });
    }
    if (lines.length === 0) fail("Add at least one item.");
    const { outlet } = await getContext();
    await api(`/v1/outlets/${outlet.id}/stock/purchases`, {
      method: "POST",
      body: { id: docId, supplier, invoice_no: text(form, "invoice_no"), date: day(form), paid_by: paidBy, lines },
    });
    revalidatePath("/stock", "layout");
    return `Received from ${supplier}: ${lines.length} ${lines.length === 1 ? "item" : "items"} booked into stock.`;
  });
}

export async function recordWastage(docId: string, _: FormState, form: FormData): Promise<FormState> {
  return attempt(async () => {
    mustUUID(docId);
    const item = text(form, "item");
    const unit = (await itemUnits()).get(item);
    if (!unit) fail("Pick what was wasted.");
    const n = qty(form, "qty", unit, "Quantity");
    if (n <= 0) fail("The quantity must be above zero.");
    const reason = text(form, "reason");
    if (!reason) fail("Say why, so the pattern shows over time.");
    const { outlet } = await getContext();
    await api(`/v1/outlets/${outlet.id}/stock/wastage`, {
      method: "POST",
      body: { id: docId, stock_item_id: item, qty: n, reason, date: day(form) },
    });
    revalidatePath("/stock", "layout");
    return "Wastage booked.";
  });
}

/**
 * A count has one input per item, "counted_<id>" (blank means not counted),
 * and for items with no cost yet "cost_<id>", the price of one unit. A count
 * is always today's: the API compares it with the books as they are now.
 */
export async function recordCount(docId: string, _: FormState, form: FormData): Promise<FormState> {
  return attempt(async () => {
    mustUUID(docId);
    const unitOf = await itemUnits();
    const lines: { stock_item_id: string; counted: number; unit_cost?: number }[] = [];
    for (const [key, value] of form.entries()) {
      if (!key.startsWith("counted_") || typeof value !== "string" || !value.trim()) continue;
      const id = key.slice("counted_".length);
      const unit = unitOf.get(id);
      if (!unit) fail("Reload the page: an item changed.");
      const line: (typeof lines)[number] = { stock_item_id: id, counted: qty(form, key, unit, "A count") };
      const cost = text(form, `cost_${id}`);
      if (cost) {
        const paise = parseRupees(cost);
        if (paise === null || paise <= 0) fail("Write the cost of one unit like 60 or 1,200.50.");
        line.unit_cost = paise;
      }
      lines.push(line);
    }
    if (lines.length === 0) fail("Enter what you counted for at least one item.");
    const { outlet } = await getContext();
    await api(`/v1/outlets/${outlet.id}/stock/counts`, {
      method: "POST",
      body: { id: docId, lines, note: text(form, "note") },
    });
    revalidatePath("/stock", "layout");
    return `Count booked for ${lines.length} ${lines.length === 1 ? "item" : "items"}.`;
  });
}

/** Recipe rows are ing0..ingN with qty0..qtyN; empty rows are dropped. */
export async function saveRecipe(dishId: string, _: FormState, form: FormData): Promise<FormState> {
  return attempt(async () => {
    mustUUID(dishId);
    const unitOf = await itemUnits();
    const lines: { stock_item_id: string; qty: number }[] = [];
    for (let i = 0; i < RECIPE_ROWS; i++) {
      const item = text(form, `ing${i}`);
      const q = text(form, `qty${i}`);
      if (!item && !q) continue;
      const unit = unitOf.get(item);
      if (!unit) fail(`Row ${i + 1}: pick an ingredient.`);
      const n = qty(form, `qty${i}`, unit, `Row ${i + 1}`);
      if (n <= 0) fail(`Row ${i + 1}: the quantity must be above zero.`);
      lines.push({ stock_item_id: item, qty: n });
    }
    if (form.has(`ing${RECIPE_ROWS}`)) fail(`A recipe has at most ${RECIPE_MAX} ingredients.`);
    if (lines.length > RECIPE_MAX) fail(`A recipe has at most ${RECIPE_MAX} ingredients.`);
    await api(`/v1/stock/recipes/${dishId}`, { method: "PUT", body: { lines } });
    revalidatePath("/recipes");
    return lines.length ? "Recipe saved." : "Recipe removed.";
  });
}
