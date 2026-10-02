"use server";

import { revalidatePath } from "next/cache";
import { api } from "@/lib/api";
import { attempt, text, type FormState } from "@/lib/actions";
import { getContext } from "@/lib/context";
import { mustUUID } from "@/lib/ids";

export type PairState = (FormState & { code?: string; expiresAt?: string; role?: string }) | undefined;

export async function createPairingCode(_: PairState, form: FormData): Promise<PairState> {
  const { outlet } = await getContext();
  const role = text(form, "role");
  let res: { code: string; expires_at: string } | undefined;
  const state = await attempt(async () => {
    res = await api(`/v1/outlets/${outlet.id}/pairing-codes`, { method: "POST", body: { role } });
  });
  if (state?.error || !res) return state;
  return { code: res.code, expiresAt: res.expires_at, role };
}

export async function revokeDevice(id: string) {
  await api(`/v1/devices/${mustUUID(id)}/revoke`, { method: "POST" });
  revalidatePath("/devices");
}
