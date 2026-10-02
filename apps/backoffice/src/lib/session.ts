import "server-only";
import { cookies } from "next/headers";

// The owner token lives in an httpOnly cookie: the browser sends it to this
// Next.js server, but page JavaScript can never read it, so a cross-site
// script cannot steal it. Only the server forwards it to the Go API.
export const TOKEN_COOKIE = "onx_owner";
export const OUTLET_COOKIE = "onx_outlet";

const thirtyDays = 60 * 60 * 24 * 30;

export async function getToken(): Promise<string | undefined> {
  return (await cookies()).get(TOKEN_COOKIE)?.value;
}

// Only callable from Server Actions and Route Handlers (Next.js rule).
export async function setToken(token: string) {
  (await cookies()).set(TOKEN_COOKIE, token, {
    httpOnly: true,
    sameSite: "lax",
    secure: process.env.NODE_ENV === "production",
    path: "/",
    maxAge: thirtyDays,
  });
}

export async function clearSession() {
  const jar = await cookies();
  jar.delete(TOKEN_COOKIE);
  jar.delete(OUTLET_COOKIE);
}

export async function getOutletCookie(): Promise<string | undefined> {
  return (await cookies()).get(OUTLET_COOKIE)?.value;
}

export async function setOutletCookie(id: string) {
  (await cookies()).set(OUTLET_COOKIE, id, { httpOnly: true, sameSite: "lax", path: "/", maxAge: thirtyDays });
}
