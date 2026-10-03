// Stock quantities are integers in thousandths of the item's unit
// ("milli-units"), as in the API: grams for kg items, millilitres for litre
// items, thousandths of a piece. Like money, they are converted only at the
// edges, with string arithmetic, never floating point.

import { WHOLE } from "./money.ts";

export type Unit = "kg" | "l" | "pc";

const grouping = new Intl.NumberFormat("en-IN", { maximumFractionDigits: 3 });

function decimal(milli: number): string {
  const sign = milli < 0 ? "−" : "";
  const abs = Math.abs(milli);
  const whole = grouping.format(Math.trunc(abs / 1000));
  const frac = String(abs % 1000).padStart(3, "0").replace(/0+$/, "");
  return sign + whole + (frac ? "." + frac : "");
}

/** formatQty(850, "kg") === "850 g"; formatQty(1250, "kg") === "1.25 kg"; formatQty(2000, "pc") === "2 pc". */
export function formatQty(milli: number, unit: Unit): string {
  if (!Number.isSafeInteger(milli)) return "—";
  const small = Math.abs(milli) < 1000 && milli !== 0;
  switch (unit) {
    case "kg":
      return small ? `${decimal(milli * 1000)} g` : `${decimal(milli)} kg`;
    case "l":
      return small ? `${decimal(milli * 1000)} ml` : `${decimal(milli)} l`;
    default:
      return `${decimal(milli)} pc`;
  }
}

/** milli-units to what an input box shows, in the item's unit: 1250 -> "1.25". */
export function qtyToInput(milli: number): string {
  return decimal(milli).replaceAll(",", "").replace("−", "-");
}

const suffixes: Record<Unit, Record<string, number>> = {
  kg: { "": 1000, kg: 1000, kgs: 1000, g: 1, gm: 1, gms: 1, grams: 1 },
  l: { "": 1000, l: 1000, ltr: 1000, litre: 1000, liter: 1000, ml: 1 },
  pc: { "": 1000, pc: 1000, pcs: 1000, piece: 1000, pieces: 1000, nos: 1000 },
};

/**
 * parseQty reads what someone typed, in the item's unit or a smaller one:
 * parseQty("1.5", "kg") === 1500, parseQty("250 g", "kg") === 250,
 * parseQty("½", ...) === null. Returns null for anything it cannot read
 * exactly (negatives, finer than a gram, junk).
 */
export function parseQty(input: string, unit: Unit): number | null {
  const m = new RegExp(String.raw`^(${WHOLE})(?:\.(\d+))?\s*([a-z]*)$`, "i").exec(input.trim());
  if (!m) return null;
  const scale = suffixes[unit][m[3].toLowerCase()];
  if (scale === undefined) return null;
  const whole = Number(m[1].replaceAll(",", ""));
  const frac = m[2] ?? "";
  const digits = scale === 1000 ? 3 : 0;
  if (frac.length > digits) return null;
  const milli = whole * scale + (digits ? Number(frac.padEnd(3, "0")) : 0);
  return Number.isSafeInteger(milli) && milli <= 1e12 ? milli : null;
}

/** The cost of one whole unit: "₹320.00/kg". */
export function perUnit(unit: Unit): string {
  return unit === "pc" ? "piece" : unit;
}

/**
 * What an input box shows for a quantity, the way people say it: "15 g"
 * rather than "0.015" kg, "1.5" for one and a half kilos. parseQty reads both.
 */
export function qtyToFriendlyInput(milli: number, unit: Unit): string {
  if (unit !== "pc" && milli > 0 && milli < 1000) return `${milli} ${unit === "kg" ? "g" : "ml"}`;
  return qtyToInput(milli);
}
