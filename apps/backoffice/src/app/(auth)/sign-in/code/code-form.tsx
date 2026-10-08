"use client";

import Link from "next/link";
import { startTransition, useActionState } from "react";
import { FormMessage } from "@/components/forms";
import { Button, Field, Input } from "@/components/ui";
import { personRoleLabel } from "@/lib/labels";
import { chooseBusiness, resendCode, verifyCode, type CodeState } from "../../actions";

/**
 * The code, then (when the number works at several businesses) which one.
 * Both steps live in one component, because the list of businesses comes back
 * from checking the code.
 */
export function CodeForm({ phone, devHint }: { phone: string; devHint: boolean }) {
  const [state, verify, checking] = useActionState(verifyCode, undefined);
  const [resent, resend, resending] = useActionState(resendCode, undefined);
  const [picked, choose, opening] = useActionState(async (_: CodeState, userId: string) => chooseBusiness(userId), undefined);

  if (state?.accounts) {
    return (
      <>
        <h1 className="text-xl font-semibold">Which business?</h1>
        <p className="mt-1 text-sm text-ink-soft">{phone} works at more than one. Pick where to sign in.</p>
        <ul className="mt-5 space-y-2" aria-label="Businesses">
          {state.accounts.map((a) => (
            <li key={a.user_id}>
              <button
                type="button"
                disabled={opening}
                onClick={() => startTransition(() => choose(a.user_id))}
                className="flex w-full items-center justify-between gap-3 rounded-lg border border-line bg-white px-4 py-3 text-left hover:border-neel-600 hover:bg-neel-50 disabled:opacity-60"
              >
                <span className="font-medium">{a.business_name}</span>
                <span className="text-sm text-ink-soft">
                  {a.name} · {personRoleLabel[a.role] ?? a.role}
                </span>
              </button>
            </li>
          ))}
        </ul>
        <FormMessage state={picked} />
      </>
    );
  }

  return (
    <>
      <h1 className="text-xl font-semibold">Enter the code</h1>
      <p className="mt-1 text-sm text-ink-soft">We sent a 6-digit code to {phone}. It works for 10 minutes.</p>
      {devHint && (
        <p className="mt-3 rounded-lg bg-chalk px-3 py-2 text-xs text-ink-soft">
          No SMS is set up yet: the code is printed in the terminal running <code>make run</code> (look for SIGN-IN CODE).
        </p>
      )}
      <form
        aria-label="Enter the code"
        className="mt-5 space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          const data = new FormData(e.currentTarget);
          startTransition(() => verify(data));
        }}
      >
        <Field label="Code">
          <Input
            name="code"
            required
            inputMode="numeric"
            autoComplete="one-time-code"
            pattern="\s*\d{3}\s*\d{3}\s*"
            maxLength={9}
            placeholder="6 digits"
            className="tracking-widest"
          />
        </Field>
        <Button type="submit" disabled={checking}>
          {checking ? "Checking…" : "Sign in"}
        </Button>
        <FormMessage state={state} />
      </form>
      <div className="mt-6 flex flex-wrap items-center gap-x-4 gap-y-2 text-sm">
        <button
          type="button"
          disabled={resending}
          onClick={() => startTransition(() => resend())}
          className="font-medium text-neel-700 hover:underline disabled:opacity-60"
        >
          {resending ? "Sending…" : "Send a new code"}
        </button>
        <Link href="/sign-in" className="text-ink-soft hover:underline">
          Use another number
        </Link>
      </div>
      <FormMessage state={resent} />
    </>
  );
}
