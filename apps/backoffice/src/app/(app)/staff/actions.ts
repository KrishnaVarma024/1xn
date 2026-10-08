"use server";

import { revalidatePath } from "next/cache";
import { api } from "@/lib/api";
import { attempt, fail, text, type FormState } from "@/lib/actions";
import { mustUUID } from "@/lib/ids";
import { formatPhone, normalisePhone } from "@/lib/phone";
import type { Person } from "@/lib/types";

function phoneOf(form: FormData): string {
  const phone = normalisePhone(text(form, "phone"));
  if (!phone) fail("Write a 10-digit mobile number, or the full number with + and the country code.");
  return phone;
}

function roleOf(form: FormData): string {
  const role = text(form, "role");
  if (role !== "manager" && role !== "cashier") fail("Pick manager or cashier.");
  return role;
}

export async function addPerson(_: FormState, form: FormData): Promise<FormState> {
  return attempt(async () => {
    const name = text(form, "name");
    if (!name) fail("Give their name.");
    const p = await api<Person>("/v1/staff", { method: "POST", body: { name, phone: phoneOf(form), role: roleOf(form) } });
    revalidatePath("/staff");
    return `${p.name} added. They sign in with ${formatPhone(p.phone)} and a code sent to it.`;
  });
}

/** Name, number and role of a manager or cashier; the owner's name only. */
export async function updatePerson(id: string, isOwner: boolean, _: FormState, form: FormData): Promise<FormState> {
  return attempt(async () => {
    mustUUID(id);
    const name = text(form, "name");
    if (!name) fail("Give their name.");
    const body = isOwner ? { name } : { name, phone: phoneOf(form), role: roleOf(form) };
    await api(`/v1/staff/${id}`, { method: "PATCH", body });
    revalidatePath("/staff");
    return "Saved.";
  });
}

/** Switched off, a person cannot sign in, and their sessions end at once. */
export async function setPersonActive(id: string, active: boolean): Promise<FormState> {
  return attempt(async () => {
    mustUUID(id);
    const p = await api<Person>(`/v1/staff/${id}`, { method: "PATCH", body: { active } });
    revalidatePath("/staff");
    return active ? `${p.name} can sign in again.` : `${p.name} is switched off and signed out everywhere.`;
  });
}
