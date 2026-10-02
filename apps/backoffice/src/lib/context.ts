import "server-only";
import { cache } from "react";
import { api } from "./api";
import { getOutletCookie } from "./session";
import type { Me, Outlet } from "./types";

// Who is signed in and which outlet they are looking at. cache() makes this
// one API call per request, however many components ask.
export const getContext = cache(async (): Promise<{ me: Me; outlet: Outlet }> => {
  const me = await api<Me>("/v1/me");
  const wanted = await getOutletCookie();
  const outlet = me.outlets.find((o) => o.id === wanted) ?? me.outlets[0];
  return { me, outlet };
});
