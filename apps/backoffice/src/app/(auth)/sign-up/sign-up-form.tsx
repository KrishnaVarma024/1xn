"use client";

import Link from "next/link";
import { startTransition, useActionState, useRef, useState } from "react";
import { FormMessage } from "@/components/forms";
import { Button, Field, Input, Select, buttonClass } from "@/components/ui";
import { copyText, selectText } from "@/lib/clipboard";
import { STATES, stateName } from "@/lib/states";
import { signUp, type SignUpState } from "../actions";

export function SignUpForm() {
  const [state, action, pending] = useActionState<SignUpState, FormData>(signUp, undefined);
  const [copied, setCopied] = useState<"no" | "yes" | "select">("no");
  const tokenRef = useRef<HTMLElement>(null);
  const [stateCode, setStateCode] = useState("29");

  if (state?.token) {
    return (
      <div className="space-y-4">
        <h2 className="text-lg font-semibold">Your restaurant is ready</h2>
        <p className="text-sm text-ink-soft">
          This is your owner token. It is shown <strong>only once</strong>. Save it in your password manager: you need it to sign in
          on another computer.
        </p>
        <div className="rounded-lg border border-marigold-400/50 bg-marigold-50 p-3">
          {/* select-all: one click selects the whole token, for copying by hand. */}
          <code ref={tokenRef} className="block select-all break-all font-mono text-sm" data-testid="owner-token">
            {state.token}
          </code>
        </div>
        {copied === "select" && (
          <p role="status" className="text-sm text-ink-soft">
            This browser would not copy from this address, so the token is selected: press ⌘C (Ctrl+C on Windows) to copy it.
          </p>
        )}
        <div className="flex flex-wrap gap-2">
          <Button
            variant="secondary"
            type="button"
            onClick={async () => {
              if (await copyText(state.token!)) {
                setCopied("yes");
              } else {
                if (tokenRef.current) selectText(tokenRef.current);
                setCopied("select");
              }
            }}
          >
            {copied === "yes" ? "Copied" : "Copy token"}
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
        <Field label="GSTIN" hint="Leave empty if you are not registered. Its first two digits are its state, picked below for you.">
          <Input
            name="gstin"
            maxLength={15}
            placeholder="29ABCDE1234F1ZW"
            className="font-mono uppercase"
            onChange={(e) => {
              // "29..." is Karnataka: follow the GSTIN, so the two cannot disagree by accident.
              const code = e.currentTarget.value.trim().slice(0, 2);
              if (stateName(code)) setStateCode(code);
            }}
          />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Legal name" hint="As on the GST certificate.">
            <Input name="legal_name" maxLength={200} placeholder="Spice Route Foods Pvt Ltd" />
          </Field>
          <Field label="State">
            <Select name="state_code" value={stateCode} onChange={(e) => setStateCode(e.currentTarget.value)}>
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
