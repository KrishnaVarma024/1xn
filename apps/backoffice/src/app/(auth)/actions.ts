"use server";

import { redirect } from "next/navigation";
import { api, ApiError } from "@/lib/api";
import { text, type FormState } from "@/lib/actions";
import { clearSession, setToken } from "@/lib/session";
import type { Me } from "@/lib/types";

export async function signIn(_: FormState, form: FormData): Promise<FormState> {
  const token = text(form, "token");
  if (!token.startsWith("onx_o_")) {
    return { error: "That is not an owner token. It starts with onx_o_ (device tokens start with onx_d_)." };
  }
  if (!/^onx_o_[A-Za-z0-9_-]{20,}$/.test(token)) {
    return { error: "That token did not work. Copy it again: it has no spaces or other characters." };
  }
  try {
    await api<Me>("/v1/me", { token }); // the API decides whether it is real
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) return { error: "That token did not work. It may be mistyped or revoked." };
    if (err instanceof ApiError) return { error: err.message };
    throw err;
  }
  await setToken(token);
  redirect("/floor");
}

export type SignUpState = (FormState & { token?: string }) | undefined;

export async function signUp(_: SignUpState, form: FormData): Promise<SignUpState> {
  const phone = normalisePhone(text(form, "phone"));
  if (!phone) return { error: "Enter a 10-digit mobile number, or the full number with + and the country code." };
  const gstin = text(form, "gstin").toUpperCase();
  const body = {
    business_name: text(form, "business_name"),
    owner: { name: text(form, "owner_name"), phone },
    legal_entity: {
      name: text(form, "legal_name") || text(form, "business_name"),
      gstin: gstin || undefined,
      state_code: gstin ? gstin.slice(0, 2) : text(form, "state_code"),
    },
    outlet: {
      name: text(form, "outlet_name"),
      price_mode: form.get("inclusive") ? "inclusive" : "exclusive",
    },
  };
  try {
    const res = await api<{ owner_token: string }>("/v1/signup", { method: "POST", body, token: null });
    await setToken(res.owner_token);
    return { token: res.owner_token };
  } catch (err) {
    if (err instanceof ApiError) return { error: err.message };
    throw err;
  }
}

export async function signOut() {
  await clearSession();
  redirect("/sign-in");
}

// "98765 43210", "098765 43210" or "91 98765 43210" -> "+919876543210".
// A number with + is kept as typed (the API checks it is E.164). Anything
// else is refused rather than guessed: a guessed number gets someone else's
// login codes.
function normalisePhone(p: string): string | null {
  const s = p.replace(/[\s()-]/g, "");
  if (s.startsWith("+")) return s;
  if (!/^\d+$/.test(s)) return null;
  const local = s.length === 12 && s.startsWith("91") ? s.slice(2) : s.replace(/^0/, "");
  return /^[6-9]\d{9}$/.test(local) ? `+91${local}` : null;
}
