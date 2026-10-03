// Trading days are YYYY-MM-DD strings, India's calendar.

/** isRealDay: "2026-02-30" and "0000-01-01" look like days but are not ones we keep. */
export function isRealDay(d: unknown): d is string {
  if (typeof d !== "string" || !/^\d{4}-\d{2}-\d{2}$/.test(d)) return false;
  const t = new Date(`${d}T00:00:00Z`);
  const year = Number(d.slice(0, 4));
  return !Number.isNaN(t.getTime()) && t.toISOString().slice(0, 10) === d && year >= 2017 && year <= 2100;
}

/** "Monday, 28 September 2026" for a trading day, whatever the server's time zone. */
export function longDate(day: string): string {
  return new Date(`${day}T12:00:00Z`).toLocaleDateString("en-IN", {
    timeZone: "UTC",
    weekday: "long",
    day: "numeric",
    month: "long",
    year: "numeric",
  });
}

/** The day before or after a trading day. */
export function shiftDay(day: string, by: number): string {
  const t = new Date(`${day}T12:00:00Z`);
  t.setUTCDate(t.getUTCDate() + by);
  return t.toISOString().slice(0, 10);
}
