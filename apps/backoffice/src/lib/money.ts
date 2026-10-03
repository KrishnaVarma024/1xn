// Money in the back office is what it is everywhere in 1x.n: an integer
// count of paise. These helpers only convert at the edges (showing a number,
// reading what someone typed). No floating point ever touches a price.

const grouping = new Intl.NumberFormat("en-IN"); // 1,23,45,678: Indian grouping

/** formatPaise(1234567) === "₹12,345.67" */
export function formatPaise(paise: number): string {
  if (!Number.isSafeInteger(paise)) return "₹—";
  const sign = paise < 0 ? "−" : "";
  const abs = Math.abs(paise);
  const rupees = Math.trunc(abs / 100);
  const rest = abs % 100;
  return `${sign}₹${grouping.format(rupees)}.${String(rest).padStart(2, "0")}`;
}

/** Like formatPaise, but drops ".00" for whole rupees: menus read better. */
export function formatPrice(paise: number): string {
  const s = formatPaise(paise);
  return s.endsWith(".00") ? s.slice(0, -3) : s;
}

/**
 * parseRupees("320.5") === 32050. Accepts "320", "320.50", "1,200", "₹99".
 * Returns null for anything else: negatives, more than two decimals, junk.
 */
export function parseRupees(input: string): number | null {
  const s = input.trim().replace(/^₹\s*/, "");
  const m = new RegExp(`^(${WHOLE})(?:\\.(\\d{1,2}))?$`).exec(s);
  if (!m) return null;
  const whole = m[1].replaceAll(",", "");
  if (whole.length > 9) return null;
  const paise = Number(whole) * 100 + Number((m[2] ?? "").padEnd(2, "0"));
  return Number.isSafeInteger(paise) ? paise : null;
}

/**
 * A whole number as people write it: plain digits, or grouped with commas
 * the Indian way (1,20,000) or the international way (120,000). The last
 * group always has three digits, so "1,50" (a decimal comma) is not a
 * number here: it is refused, never read as 150.
 */
export const WHOLE = String.raw`\d+|\d{1,3}(?:,\d{3})+|\d{1,2}(?:,\d{2})*,\d{3}`;

/** paise to the plain rupee string an input box shows: 32050 -> "320.50" */
export function paiseToInput(paise: number): string {
  return `${Math.trunc(paise / 100)}.${String(paise % 100).padStart(2, "0")}`;
}

/** Basis points as a percentage: 500 -> "5%", 1250 -> "12.5%". */
export function formatRate(bp: number): string {
  const whole = Math.trunc(bp / 100);
  const frac = bp % 100;
  if (frac === 0) return `${whole}%`;
  return `${whole}.${String(frac).padStart(2, "0").replace(/0$/, "")}%`;
}

/** parseRate("12.5") === 1250 basis points; null if not 0 to 40. */
export function parseRate(input: string): number | null {
  const m = /^(\d{1,2})(?:\.(\d{1,2}))?$/.exec(input.replace(/\s*%\s*$/, "").trim());
  if (!m) return null;
  const bp = Number(m[1]) * 100 + Number((m[2] ?? "").padEnd(2, "0"));
  return bp <= 4000 ? bp : null;
}
