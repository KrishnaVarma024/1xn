"use server";

import { revalidatePath } from "next/cache";
import { api, ApiError } from "@/lib/api";
import { attempt, fail, text, type FormState } from "@/lib/actions";
import { getContext } from "@/lib/context";
import { isRealDay } from "@/lib/dates";

/**
 * closeDay closes a trading day. It sends the digest of the report the owner
 * was looking at: if a bill or a shift arrived since, the API refuses, and the
 * page re-renders with the new figures to check.
 */
export async function closeDay(day: string, _: FormState, form: FormData): Promise<FormState> {
  return attempt(async () => {
    if (!isRealDay(day)) fail("That is not a day we can close.");
    const digest = text(form, "digest");
    if (!/^[0-9a-f]{24}$/.test(digest)) fail("Reload the page and try again.");
    const { outlet } = await getContext();
    try {
      await api(`/v1/outlets/${outlet.id}/days/${day}/close`, {
        method: "POST",
        body: { digest, acknowledge_warnings: form.get("acknowledge") === "on" },
      });
    } catch (err) {
      if (err instanceof ApiError && err.code === "report_changed") {
        revalidatePath("/day");
        fail("New figures arrived while you were looking. The report is now up to date: check it and close again.");
      }
      if (err instanceof ApiError && err.code === "warnings_not_acknowledged") {
        fail("Tick the box to confirm you have checked the warnings.");
      }
      throw err;
    }
    revalidatePath("/day");
    return "Day closed. The Z report is saved as it is now.";
  });
}
