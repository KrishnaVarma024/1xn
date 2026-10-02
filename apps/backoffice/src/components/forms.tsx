"use client";

import { createContext, startTransition, useActionState, useContext, useEffect, useRef, type ReactNode } from "react";
import type { FormState } from "@/lib/actions";
import { Button, type ButtonVariant } from "./ui";

const Pending = createContext(false);

/**
 * ActionForm wires a Server Action to a form and shows what it answered: a
 * red line for an error, a green one for success.
 *
 * It submits through startTransition instead of the form's action prop on
 * purpose: React resets a form after every action-prop submission, which
 * would wipe what someone typed when the server says "that GSTIN is wrong".
 * Here the form is cleared only after a success.
 */
export function ActionForm({
  action,
  children,
  className = "",
  resetOnSuccess = true,
  label,
}: {
  action: (state: FormState, form: FormData) => Promise<FormState>;
  children: ReactNode;
  className?: string;
  resetOnSuccess?: boolean;
  label?: string;
}) {
  const [state, formAction, pending] = useActionState(action, undefined);
  const ref = useRef<HTMLFormElement>(null);
  useEffect(() => {
    if (state && !state.error && resetOnSuccess) ref.current?.reset();
  }, [state, resetOnSuccess]);
  return (
    <form
      ref={ref}
      aria-label={label}
      className={className}
      onSubmit={(e) => {
        e.preventDefault();
        const data = new FormData(e.currentTarget);
        startTransition(() => formAction(data));
      }}
    >
      <Pending.Provider value={pending}>{children}</Pending.Provider>
      <FormMessage state={state} />
    </form>
  );
}

export function FormMessage({ state }: { state: FormState }) {
  if (state?.error) {
    return (
      <p role="alert" className="mt-2 text-sm text-chilli-700">
        {state.error}
      </p>
    );
  }
  if (state?.ok) {
    return (
      <p role="status" className="mt-2 text-sm text-curry-700">
        {state.ok}
      </p>
    );
  }
  return null;
}

export function SubmitButton({
  children,
  variant = "primary",
  size = "md",
  pendingText = "Saving…",
}: {
  children: ReactNode;
  variant?: ButtonVariant;
  size?: "sm" | "md";
  pendingText?: string;
}) {
  const pending = useContext(Pending);
  return (
    <Button type="submit" variant={variant} size={size} disabled={pending} aria-disabled={pending}>
      {pending ? pendingText : children}
    </Button>
  );
}
