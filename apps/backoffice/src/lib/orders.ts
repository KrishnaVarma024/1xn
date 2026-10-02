import type { KOTStatus, Order, OrderLine } from "./types";

// Money on an order is before tax: what the guest will be charged for each
// dish, as the till will price it (unit price plus add-ons, times quantity).
export function lineValue(l: OrderLine): number {
  const mods = (l.modifiers ?? []).reduce((s, m) => s + m.price_paise, 0);
  return l.qty * (l.unit_price_paise + mods);
}

export function orderValue(o: Order): number {
  return o.lines.filter((l) => l.status !== "voided").reduce((s, l) => s + lineValue(l), 0);
}

/** What is still to be billed: everything not voided and not yet paid. */
export function openValue(o: Order): number {
  return orderValue(o) - billedValue(o);
}

export function billedValue(o: Order): number {
  return o.lines.filter((l) => l.status === "billed").reduce((s, l) => s + lineValue(l), 0);
}

export const kotTone: Record<KOTStatus, "warn" | "info" | "good" | "neutral"> = {
  new: "warn",
  preparing: "info",
  ready: "good",
  served: "neutral",
};
