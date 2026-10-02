"use server";

import { revalidatePath } from "next/cache";
import { api } from "@/lib/api";
import { attempt, fail, text, type FormState } from "@/lib/actions";
import { getContext } from "@/lib/context";
import { mustUUID } from "@/lib/ids";

async function outletPath() {
  const { outlet } = await getContext();
  return `/v1/outlets/${outlet.id}`;
}

export async function addFloor(_: FormState, form: FormData): Promise<FormState> {
  return attempt(async () => {
    await api(`${await outletPath()}/floors`, { method: "POST", body: { name: text(form, "name") } });
    revalidatePath("/tables");
    return "Floor added.";
  });
}

export async function addTable(floorID: string, _: FormState, form: FormData): Promise<FormState> {
  return attempt(async () => {
    const seats = Number(text(form, "seats") || 4);
    if (!Number.isInteger(seats) || seats < 1 || seats > 50) fail("Seats must be a whole number from 1 to 50.");
    await api(`${await outletPath()}/tables`, {
      method: "POST",
      body: { floor_id: mustUUID(floorID), label: text(form, "label"), seats },
    });
    revalidatePath("/tables");
    return "Table added.";
  });
}

export async function setTableActive(tableID: string, active: boolean) {
  await api(`${await outletPath()}/tables/${mustUUID(tableID)}`, { method: "PATCH", body: { active: active === true } });
  revalidatePath("/tables");
}
