import Link from "next/link";
import type { Metadata } from "next";
import { ActionForm, SubmitButton } from "@/components/forms";
import { Card, Field, Textarea } from "@/components/ui";
import { signIn } from "../../actions";

export const metadata: Metadata = { title: "Sign in with a token" };

export default function TokenSignInPage() {
  return (
    <Card>
      <h1 className="text-xl font-semibold">Sign in with the owner token</h1>
      <p className="mt-1 text-sm text-ink-soft">
        Paste the owner token you got when you signed up (or from <code className="rounded bg-chalk px-1">make seed</code>). It
        never expires, so keep it like a password.
      </p>
      <ActionForm action={signIn} className="mt-5 space-y-4" label="Sign in">
        <Field label="Owner token">
          <Textarea name="token" rows={2} required autoComplete="off" spellCheck={false} placeholder="onx_o_…" className="font-mono" />
        </Field>
        <SubmitButton pendingText="Checking…">Sign in</SubmitButton>
      </ActionForm>
      <p className="mt-6 text-sm text-ink-soft">
        <Link href="/sign-in" className="font-medium text-neel-700 hover:underline">
          Sign in with your mobile number
        </Link>
      </p>
    </Card>
  );
}
