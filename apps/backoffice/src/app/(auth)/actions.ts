"use server";

import { redirect } from "next/navigation";
import { api, ApiError } from "@/lib/api";
import { text, type FormState } from "@/lib/actions";
import { mustUUID } from "@/lib/ids";
import { normalisePhone } from "@/lib/phone";
import {
  clearSession,
  clearSignIn,
  getPendingCode,
  getTicket,
  getToken,
  setPendingCode,
  setTicket,
  setToken,
} from "@/lib/session";
import { stateName } from "@/lib/states";
import type { Me, SignInAccount } from "@/lib/types";

/** The API's messages are sentence fragments ("that code is wrong; 4 tries left"). */
function sentence(msg: string): string {
  const s = msg.charAt(0).toUpperCase() + msg.slice(1);
  return /[.!?]$/.test(s) ? s : s + ".";
}

// Signing in with a mobile number: (1) send a code, (2) check it, (3) if the
// number works at several businesses, pick one. The challenge id and the
// ticket stay in short-lived httpOnly cookies between steps.

export async function requestCode(_: FormState, form: FormData): Promise<FormState> {
  const phone = normalisePhone(text(form, "phone"));
  if (!phone) return { error: "Enter your 10-digit mobile number, or the full number with + and the country code." };
  try {
    const ch = await api<{ challenge_id: string }>("/v1/auth/otp", { method: "POST", body: { phone }, token: null });
    await setPendingCode({ challenge: ch.challenge_id, phone });
  } catch (err) {
    if (err instanceof ApiError) return { error: sentence(err.message) };
    throw err;
  }
  redirect("/sign-in/code");
}

export type CodeState = (FormState & { accounts?: SignInAccount[] }) | undefined;

export async function verifyCode(_: CodeState, form: FormData): Promise<CodeState> {
  const pending = await getPendingCode();
  if (!pending) redirect("/sign-in");
  const code = text(form, "code").replace(/\s+/g, "");
  if (!/^\d{6}$/.test(code)) return { error: "Type the 6 digits from the message." };
  let v: { ticket: string; accounts: SignInAccount[] };
  try {
    v = await api("/v1/auth/otp/verify", { method: "POST", body: { challenge_id: pending.challenge, code }, token: null });
  } catch (err) {
    if (err instanceof ApiError) return { error: sentence(err.message) };
    throw err;
  }
  if (v.accounts.length === 1) return startSession(v.ticket, v.accounts[0].user_id);
  await setTicket(v.ticket);
  return { accounts: v.accounts };
}

/** The number works at several businesses: this one. */
export async function chooseBusiness(userId: string): Promise<CodeState> {
  mustUUID(userId);
  const ticket = await getTicket();
  if (!ticket) return { error: "Signing in took too long. Start again with your number." };
  return startSession(ticket, userId);
}

async function startSession(ticket: string, userId: string): Promise<CodeState> {
  let token: string;
  try {
    ({ token } = await api<{ token: string }>("/v1/auth/sessions", { method: "POST", body: { ticket, user_id: userId }, token: null }));
  } catch (err) {
    if (err instanceof ApiError) return { error: sentence(err.message) };
    throw err;
  }
  await clearSignIn();
  await clearSession(); // another business's outlet choice does not carry over
  await setToken(token);
  redirect("/floor");
}

export async function resendCode(): Promise<CodeState> {
  const pending = await getPendingCode();
  if (!pending) redirect("/sign-in");
  try {
    const ch = await api<{ challenge_id: string }>("/v1/auth/otp", { method: "POST", body: { phone: pending.phone }, token: null });
    await setPendingCode({ challenge: ch.challenge_id, phone: pending.phone });
  } catch (err) {
    if (err instanceof ApiError) return { error: sentence(err.message) };
    throw err;
  }
  return { ok: "A new code is on its way. The old one no longer works." };
}

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
  const gstin = text(form, "gstin").toUpperCase().replace(/\s+/g, "");
  const stateCode = text(form, "state_code");
  // A GSTIN's first two digits are its state. Say so when they disagree,
  // instead of quietly registering the restaurant in another state.
  if (gstin && gstin.slice(0, 2) !== stateCode) {
    const from = stateName(gstin.slice(0, 2));
    return {
      error: from
        ? `This GSTIN is registered in ${from} (${gstin.slice(0, 2)}), but you picked ${stateName(stateCode) ?? stateCode}. Fix the GSTIN or the state.`
        : `A GSTIN starts with its state's code, like 29 for Karnataka, and "${gstin.slice(0, 2)}" is not one in use. Copy it exactly from your GST certificate.`,
    };
  }
  const body = {
    business_name: text(form, "business_name"),
    owner: { name: text(form, "owner_name"), phone },
    legal_entity: {
      name: text(form, "legal_name") || text(form, "business_name"),
      gstin: gstin || undefined,
      state_code: stateCode,
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
    if (err instanceof ApiError && err.code === "bad_gstin") {
      return {
        error:
          "That GSTIN is not valid. Copy it exactly from your GST certificate: 15 characters, like 29ABCDE1234F1ZW. " +
          "The last one is a check digit, so a single typo makes the whole number invalid.",
      };
    }
    if (err instanceof ApiError) return { error: err.message };
    throw err;
  }
}

export async function signOut() {
  // End the session at the API too (an owner token is kept: it is the key
  // from sign-up). Passing the token means a refusal does not redirect.
  const token = await getToken();
  if (token) await api("/v1/auth/sign-out", { method: "POST", token }).catch(() => undefined);
  await clearSession();
  redirect("/sign-in");
}
