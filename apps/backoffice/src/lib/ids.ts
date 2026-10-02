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
