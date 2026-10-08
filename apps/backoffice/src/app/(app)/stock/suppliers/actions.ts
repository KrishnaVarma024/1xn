"use server";

import { revalidatePath } from "next/cache";
import { redirect } from "next/navigation";
import { api } from "@/lib/api";
import { attempt, fail, text, type FormState } from "@/lib/actions";
import { getContext } from "@/lib/context";
import { isRealDay } from "@/lib/dates";
import { mustUUID } from "@/lib/ids";
import { parseRupees } from "@/lib/money";
import { normalisePhone } from "@/lib/phone";
import type { Supplier, SupplierPayment } from "@/lib/types";

/** Phone and GSTIN as the API keeps them; blank stays blank (it clears on an edit). */
function contact(form: FormData): { phone: string; gstin: string } {
  const typed = text(form, "phone");
  const phone = typed ? normalisePhone(typed) : "";
  if (phone === null) fail("Write a 10-digit mobile number, or the full number with + and the country code.");
  const gstin = text(form, "gstin").toUpperCase().replace(/\s+/g, "");
  if (gstin && gstin.length !== 15) fail("A GSTIN has 15 characters, like 29ABCDE1234F1ZW. Leave it blank if you do not have it.");
  return { phone, gstin };
}

export async function createSupplier(_: FormState, form: FormData): Promise<FormState> {
  return attempt(async () => {
    const name = text(form, "name");
    if (!name) fail("Give the supplier a name.");
    const { phone, gstin } = contact(form);
    await api<Supplier>("/v1/stock/suppliers", { method: "POST", body: { name, phone, gstin } });
    revalidatePath("/stock", "layout");
    return `${name} added.`;
  });
}

export async function updateSupplier(id: string, _: FormState, form: FormData): Promise<FormState> {
  return attempt(async () => {
    mustUUID(id);
    const name = text(form, "name");
    if (!name) fail("Give the supplier a name.");
    const { phone, gstin } = contact(form);
    await api(`/v1/stock/suppliers/${id}`, { method: "PATCH", body: { name, phone, gstin } });
    revalidatePath("/stock", "layout");
    return "Saved.";
  });
}

/**
 * Paying a supplier for deliveries bought on credit. paymentId is made when
 * the page renders, so a double click pays once. On success the page is
 * loaded afresh with ?paid=<id>, and says what was paid: the form itself is
 * gone once nothing is owed, and its message with it.
 */
export async function paySupplier(supplierId: string, paymentId: string, _: FormState, form: FormData): Promise<FormState> {
  return attempt(async () => {
    mustUUID(supplierId);
    mustUUID(paymentId);
    const amount = parseRupees(text(form, "amount"));
    if (amount === null || amount <= 0) fail("Write the amount paid, like 4,500 or 1,200.50.");
    const paidBy = text(form, "paid_by");
    if (!["safe", "bank", "till"].includes(paidBy)) fail("How was it paid?");
    const date = text(form, "date");
    if (date && !isRealDay(date)) fail("Pick a real date.");
    const { outlet } = await getContext();
    await api<SupplierPayment>(`/v1/outlets/${outlet.id}/stock/supplier-payments`, {
      method: "POST",
      body: {
        id: paymentId,
        supplier_id: supplierId,
        amount,
        paid_by: paidBy,
        date: date || undefined,
        reference: text(form, "reference"),
      },
    });
    revalidatePath("/stock", "layout");
    redirect(`/stock/suppliers/${supplierId}?paid=${paymentId}`);
  });
}
