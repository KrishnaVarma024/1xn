import Link from "next/link";
import type { Metadata } from "next";
import { ActionForm, SubmitButton } from "@/components/forms";
import { Card, Field, Input } from "@/components/ui";
import { requestCode } from "../actions";

export const metadata: Metadata = { title: "Sign in" };

export default async function SignInPage({ searchParams }: PageProps<"/sign-in">) {
  const { expired } = await searchParams;
  return (
    <Card>
      <h1 className="text-xl font-semibold">Sign in</h1>
      <p className="mt-1 text-sm text-ink-soft">Owner, manager or cashier: we send a 6-digit code to your mobile.</p>
      {expired && (
        <p className="mt-3 rounded-lg bg-marigold-50 px-3 py-2 text-sm text-marigold-700">Your session ended. Sign in again.</p>
      )}
      <ActionForm action={requestCode} className="mt-5 space-y-4" label="Sign in">
        <Field label="Mobile number">
          <Input name="phone" required inputMode="tel" autoComplete="tel" placeholder="98765 43210" />
        </Field>
        <SubmitButton pendingText="Sending…">Send me a code</SubmitButton>
      </ActionForm>
      <p className="mt-6 text-sm text-ink-soft">
        New restaurant?{" "}
        <Link href="/sign-up" className="font-medium text-neel-700 hover:underline">
          Create an account
        </Link>
      </p>
      <p className="mt-2 text-sm text-ink-soft">
        Have the owner token from sign-up?{" "}
        <Link href="/sign-in/token" className="font-medium text-neel-700 hover:underline">
          Use the token instead
        </Link>
      </p>
    </Card>
  );
}
