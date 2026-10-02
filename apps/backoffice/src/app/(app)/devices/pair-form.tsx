"use client";

import { startTransition, useActionState } from "react";
import { FormMessage } from "@/components/forms";
import { Button, Field, Select } from "@/components/ui";
import { roleLabel } from "@/lib/labels";
import { createPairingCode, type PairState } from "./actions";

export function PairForm() {
  const [state, action, pending] = useActionState<PairState, FormData>(createPairingCode, undefined);
  return (
    <div>
      <form
        aria-label="Pair a device"
        className="flex flex-wrap items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          const data = new FormData(e.currentTarget);
          startTransition(() => action(data));
        }}
      >
        <Field label="What is it?">
          <Select name="role" defaultValue="captain">
            {Object.entries(roleLabel).map(([value, label]) => (
              <option key={value} value={value}>
                {label}
              </option>
            ))}
          </Select>
        </Field>
        <Button type="submit" disabled={pending}>
          {pending ? "Creating…" : "Get a pairing code"}
        </Button>
      </form>
      <FormMessage state={state} />
      {state?.code && (
        <div className="mt-4 rounded-xl border border-neel-200 bg-neel-50 p-4">
          <div className="text-xs font-medium uppercase tracking-wide text-neel-700">Pairing code · {roleLabel[state.role ?? ""]}</div>
          <div className="mt-1 font-mono text-3xl font-semibold tracking-widest" data-testid="pairing-code">
            {state.code}
          </div>
          <p className="mt-2 text-sm text-ink-soft">
            Type it into the new device within 10 minutes (until{" "}
            {new Date(state.expiresAt!).toLocaleTimeString("en-IN", { hour: "numeric", minute: "2-digit" })}). It works once.
          </p>
        </div>
      )}
    </div>
  );
}
