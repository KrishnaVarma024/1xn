"use client";

import Link from "next/link";
import { startTransition, useActionState, useState } from "react";
import { FormMessage } from "@/components/forms";
import { Button, Field, Input, Select, buttonClass } from "@/components/ui";
import { STATES } from "@/lib/states";
import { signUp, type SignUpState } from "../actions";

export function SignUpForm() {
  const [state, action, pending] = useActionState<SignUpState, FormData>(signUp, undefined);
  const [copied, setCopied] = useState(false);

  if (state?.token) {
    return (
      <div className="space-y-4">
        <h2 className="text-lg font-semibold">Your restaurant is ready</h2>
        <p className="text-sm text-ink-soft">
          This is your owner token. It is shown <strong>only once</strong>. Save it in your password manager: you need it to sign in
          on another computer.
        </p>
        <div className="rounded-lg border border-marigold-400/50 bg-marigold-50 p-3">
          <code className="block break-all font-mono text-sm" data-testid="owner-token">
            {state.token}
          </code>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button
            variant="secondary"
            type="button"
            onClick={async () => {
              await navigator.clipboard.writeText(state.token!);
              setCopied(true);
            }}
          >
            {copied ? "Copied" : "Copy token"}
          </Button>
          <Link href="/menu" className={buttonClass("primary")}>
            I saved it. Set up my menu
          </Link>
        </div>
      </div>
    );
  }

  return (
    <form
      aria-label="Create your restaurant"
      className="space-y-4"
      onSubmit={(e) => {
        e.preventDefault();
        const data = new FormData(e.currentTarget);
        startTransition(() => action(data));
      }}
    >
      <Field label="Restaurant name">
        <Input name="business_name" required maxLength={200} placeholder="Spice Route" />
      </Field>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Your name">
          <Input name="owner_name" required maxLength={200} placeholder="Ravi Kumar" />
        </Field>
        <Field label="Mobile number" hint="We will use it for login codes later.">
          <Input name="phone" required inputMode="tel" placeholder="98765 43210" />
        </Field>
      </div>
      <Field label="First outlet" hint="The branch or kitchen, e.g. Indiranagar.">
        <Input name="outlet_name" required maxLength={200} placeholder="Indiranagar" />
      </Field>
      <fieldset className="space-y-4 rounded-lg border border-line p-3">
        <legend className="px-1 text-xs font-medium text-ink-soft">GST registration</legend>
        <Field label="GSTIN" hint="Leave empty if you are not registered. The state then comes from the list below.">
          <Input name="gstin" maxLength={15} placeholder="29ABCDE1234F1Z5" className="font-mono uppercase" />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Legal name" hint="As on the GST certificate.">
            <Input name="legal_name" maxLength={200} placeholder="Spice Route Foods Pvt Ltd" />
          </Field>
          <Field label="State">
            <Select name="state_code" defaultValue="29">
              {STATES.map(([code, name]) => (
                <option key={code} value={code}>
                  {name} ({code})
                </option>
              ))}
            </Select>
          </Field>
        </div>
        <label className="flex items-start gap-2 text-sm">
          <input type="checkbox" name="inclusive" className="mt-0.5" />
          <span>
            My menu prices <strong>include</strong> GST
            <span className="block text-xs text-ink-faint">Most restaurants print prices before tax; leave this off if unsure.</span>
          </span>
        </label>
      </fieldset>
      <Button type="submit" disabled={pending}>
        {pending ? "Creating…" : "Create restaurant"}
      </Button>
      <FormMessage state={state} />
    </form>
  );
}
