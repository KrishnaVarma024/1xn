import Link from "next/link";
import type { Metadata } from "next";
import { ActionForm, SubmitButton } from "@/components/forms";
import { Card, Field, Textarea } from "@/components/ui";
import { signIn } from "../actions";

export const metadata: Metadata = { title: "Sign in" };

export default async function SignInPage({ searchParams }: PageProps<"/sign-in">) {
  const { expired } = await searchParams;
  return (
    <Card>
      <h1 className="text-xl font-semibold">Sign in</h1>
      <p className="mt-1 text-sm text-ink-soft">
        Paste your owner token. You got it when you signed up (or from <code className="rounded bg-chalk px-1">make seed</code>).
      </p>
      {expired && (
        <p className="mt-3 rounded-lg bg-marigold-50 px-3 py-2 text-sm text-marigold-700">
          Your session ended. Sign in again.
        </p>
      )}
      <ActionForm action={signIn} className="mt-5 space-y-4" label="Sign in">
        <Field label="Owner token">
          <Textarea name="token" rows={2} required autoComplete="off" spellCheck={false} placeholder="onx_o_…" className="font-mono" />
        </Field>
        <SubmitButton pendingText="Checking…">Sign in</SubmitButton>
      </ActionForm>
      <p className="mt-6 text-sm text-ink-soft">
        New restaurant?{" "}
        <Link href="/sign-up" className="font-medium text-neel-700 hover:underline">
          Create an account
        </Link>
      </p>
      <p className="mt-4 text-xs text-ink-faint">
        Phone-number login (OTP) replaces tokens in a later release.
      </p>
    </Card>
  );
}
