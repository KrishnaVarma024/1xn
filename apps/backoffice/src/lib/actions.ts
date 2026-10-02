import "server-only";
import { unstable_rethrow } from "next/navigation";
import { ApiError } from "./api";

// What a form's Server Action hands back to the form.
export type FormState = { error?: string; ok?: string } | undefined;

/** A problem with what someone typed, found before calling the API. */
export class FormError extends Error {}

export function fail(message: string): never {
  throw new FormError(message);
}

/**
 * attempt runs a mutation and turns a refusal (ours or the API's) into a
 * message the form can show. Framework signals (redirect, notFound) are
 * rethrown untouched; anything unexpected goes to the error page.
 */
export async function attempt(fn: () => Promise<string | void>): Promise<FormState> {
  try {
    const ok = await fn();
    return ok ? { ok } : {};
  } catch (err) {
    unstable_rethrow(err);
    if (err instanceof FormError || err instanceof ApiError) return { error: err.message };
    throw err;
  }
}

export function text(form: FormData, name: string): string {
  const v = form.get(name);
  return typeof v === "string" ? v.trim() : "";
}
