// Ids from URLs and from bound Server Action arguments are user input: a
// browser can send anything. They are only ever pasted into API paths after
// this check, so "../ledger" can never steer a request to another endpoint.
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function isUUID(s: unknown): s is string {
  return typeof s === "string" && uuid.test(s);
}

export function mustUUID(s: unknown): string {
  if (!isUUID(s)) throw new Error("not an id");
  return s;
}

/**
 * newDocId makes a UUIDv7 for a document form when the page renders. The
 * form sends it back with the document, so a double click (or a retry after
 * a timeout) records the document once: the API answers the second request
 * with the first one's result.
 */
export function newDocId(now = Date.now()): string {
  const b = new Uint8Array(16);
  crypto.getRandomValues(b);
  let ms = now;
  for (let i = 5; i >= 0; i--) {
    b[i] = ms % 256;
    ms = Math.floor(ms / 256);
  }
  b[6] = (b[6] & 0x0f) | 0x70; // version 7
  b[8] = (b[8] & 0x3f) | 0x80; // variant 10
  const h = Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}
