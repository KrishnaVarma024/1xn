import "server-only";
import { redirect } from "next/navigation";
import { getToken } from "./session";

// The one door to the Go API. Server Components and Server Actions call it;
// it adds the owner's token, sends JSON, and turns API errors into ApiError.
// This is the Backend-for-Frontend pattern: the browser only ever talks to
// Next.js, and Next.js talks to the API.

const API_URL = process.env.ONEXN_API_URL ?? "http://localhost:8080";

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message);
  }
}

type Options = { method?: "GET" | "POST" | "PATCH"; body?: unknown; token?: string | null };

/**
 * api<T>("/v1/me") calls the API as the signed-in owner.
 * Pass token: null for public endpoints (signup); pass a token to try one.
 * A missing or rejected owner token sends the browser to /sign-in.
 */
export async function api<T>(path: string, opts: Options = {}): Promise<T> {
  const token = opts.token === undefined ? await getToken() : opts.token;
  if (opts.token === undefined && !token) redirect("/sign-in");

  let res: Response;
  try {
    res = await fetch(API_URL + path, {
      method: opts.method ?? "GET",
      headers: {
        "Content-Type": "application/json",
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
      cache: "no-store", // owner data is live; never serve a cached copy
    });
  } catch (err) {
    // Only a failed connection means "unreachable". Anything else (say, a
    // header fetch refuses) is a bug to see in the logs, not to disguise.
    if (err instanceof TypeError && err.cause) {
      throw new ApiError(503, "api_unreachable", "Can't reach the 1x.n API. Is it running? (make run)");
    }
    throw err;
  }

  if (res.status === 401 && opts.token === undefined) redirect("/sign-in?expired=1");
  const text = await res.text();
  let data: any;
  try {
    data = text ? JSON.parse(text) : undefined;
  } catch {
    data = undefined; // a proxy or router error page, not our JSON
  }
  if (!res.ok) {
    const e = data?.error ?? {};
    throw new ApiError(res.status, e.code ?? "error", e.message ?? `Request failed (${res.status})`);
  }
  return data as T;
}
