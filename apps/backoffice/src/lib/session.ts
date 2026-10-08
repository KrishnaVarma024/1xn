import "server-only";
import { cookies } from "next/headers";

// The signed-in person's token (a session from signing in with a code, or the
// owner token from sign-up) lives in an httpOnly cookie: the browser sends it
// to this Next.js server, but page JavaScript can never read it, so a
// cross-site script cannot steal it. Only the server forwards it to the API.
export const TOKEN_COOKIE = "onx_session";
export const OUTLET_COOKIE = "onx_outlet";
// Signing in, between steps: which code was sent to which number, and after
// the code, the ticket that picks a business. Short-lived, httpOnly.
const CODE_COOKIE = "onx_code";
const TICKET_COOKIE = "onx_ticket";

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

const shortLived = (maxAge: number) => ({
  httpOnly: true,
  sameSite: "lax" as const,
  secure: process.env.NODE_ENV === "production",
  path: "/sign-in",
  maxAge,
});

export type PendingCode = { challenge: string; phone: string };

export async function setPendingCode(p: PendingCode) {
  (await cookies()).set(CODE_COOKIE, JSON.stringify(p), shortLived(10 * 60));
}

export async function getPendingCode(): Promise<PendingCode | undefined> {
  const raw = (await cookies()).get(CODE_COOKIE)?.value;
  if (!raw) return undefined;
  try {
    const p = JSON.parse(raw);
    return typeof p?.challenge === "string" && typeof p?.phone === "string" ? p : undefined;
  } catch {
    return undefined;
  }
}

export async function setTicket(ticket: string) {
  (await cookies()).set(TICKET_COOKIE, ticket, shortLived(5 * 60));
}

export async function getTicket(): Promise<string | undefined> {
  return (await cookies()).get(TICKET_COOKIE)?.value;
}

export async function clearSignIn() {
  const jar = await cookies();
  jar.delete({ name: CODE_COOKIE, path: "/sign-in" });
  jar.delete({ name: TICKET_COOKIE, path: "/sign-in" });
}

export async function getOutletCookie(): Promise<string | undefined> {
  return (await cookies()).get(OUTLET_COOKIE)?.value;
}

export async function setOutletCookie(id: string) {
  (await cookies()).set(OUTLET_COOKIE, id, { httpOnly: true, sameSite: "lax", path: "/", maxAge: thirtyDays });
}
