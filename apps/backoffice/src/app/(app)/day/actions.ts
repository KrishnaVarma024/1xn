"use server";

import { revalidatePath } from "next/cache";
import { redirect } from "next/navigation";
import { api, ApiError } from "@/lib/api";
import { attempt, fail, text, type FormState } from "@/lib/actions";
import { getContext } from "@/lib/context";
import { isRealDay } from "@/lib/dates";
import { mustUUID } from "@/lib/ids";
import { parseRupees } from "@/lib/money";

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

/**
 * closeShiftHere closes a till's shift from the back office, with the cash
 * the owner counted in the drawer. requestId is made when the page renders:
 * a double click sends the same close twice, and the API answers it once.
 * Then it shows the shift's day, where the closed drawer now is.
 */
export async function closeShiftHere(
  shiftId: string,
  requestId: string,
  businessDate: string,
  _: FormState,
  form: FormData,
): Promise<FormState> {
  return attempt(async () => {
    mustUUID(shiftId);
    mustUUID(requestId);
    if (!isRealDay(businessDate)) fail("Reload the page and try again.");
    const counted = parseRupees(text(form, "counted"));
    if (counted === null) fail("Write the cash you counted in rupees, like 2,450 or 2450.50.");
    const note = text(form, "note");
    if (!note) fail("Say why it is closed here and not on the till, so the next person reading the report knows.");
    const { outlet } = await getContext();
    try {
      await api(`/v1/outlets/${outlet.id}/shifts/${shiftId}/close`, {
        method: "POST",
        body: { id: requestId, counted_paise: counted, note },
      });
    } catch (err) {
      // Someone else closed it meanwhile: show the shift's day, where the
      // count that was used is, and say that this one was not.
      if (err instanceof ApiError && err.code === "shift_closed") {
        revalidatePath("/day");
        redirect(`/day?date=${businessDate}&closed=${shiftId}&meanwhile=1`);
      }
      if (err instanceof ApiError && err.code === "till_active") {
        revalidatePath("/day");
        fail("This till still works, so it closes its own shift. If it is lost or broken, revoke it on the Devices page first.");
      }
      if (err instanceof ApiError && err.code === "id_used") fail("Reload the page and count again.");
      throw err;
    }
    revalidatePath("/day");
    redirect(`/day?date=${businessDate}&closed=${shiftId}`);
  });
}
