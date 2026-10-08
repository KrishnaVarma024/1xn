import "server-only";
import { getContext } from "./context";
import type { Permission } from "./types";

// What the signed-in person may do. The API decides on every call (and says
// 403 otherwise); the back office asks first, to show only what works.

export async function can(p: Permission): Promise<boolean> {
  const { me } = await getContext();
  return me.permissions.includes(p);
}
