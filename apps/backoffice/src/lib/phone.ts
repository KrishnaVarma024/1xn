// Indian mobile numbers as people type them, to the E.164 form the API keeps.

/**
 * "98765 43210", "098765 43210" or "91 98765 43210" -> "+919876543210".
 * A number with + is kept as typed (the API checks it is E.164). Anything
 * else is refused rather than guessed: a guessed number gets someone else's
 * login codes.
 */
export function normalisePhone(p: string): string | null {
  const s = p.replace(/[\s()-]/g, "");
  if (s.startsWith("+")) return s;
  if (!/^\d+$/.test(s)) return null;
  const local = s.length === 12 && s.startsWith("91") ? s.slice(2) : s.replace(/^0/, "");
  return /^[6-9]\d{9}$/.test(local) ? `+91${local}` : null;
}

/**
 * "+919876543210" -> "98765 43210". Only Indian mobiles are shortened: they
 * are what normalisePhone reads back without the +91. Landlines ("+912223456789",
 * Mumbai) and other countries are shown as stored, so a form that shows a
 * phone and sends it back unchanged never turns it into something refused.
 */
export function formatPhone(e164: string): string {
  const m = /^\+91([6-9]\d{4})(\d{5})$/.exec(e164);
  return m ? `${m[1]} ${m[2]}` : e164;
}
