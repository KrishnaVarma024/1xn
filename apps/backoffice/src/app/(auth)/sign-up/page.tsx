import Link from "next/link";
import type { Metadata } from "next";
import { Card } from "@/components/ui";
import { SignUpForm } from "./sign-up-form";

export const metadata: Metadata = { title: "Create your restaurant" };

export default function SignUpPage() {
  return (
    <Card>
      <h1 className="mb-1 text-xl font-semibold">Create your restaurant</h1>
      <p className="mb-5 text-sm text-ink-soft">One account for your outlets, tills, kitchen screens and books.</p>
      <SignUpForm />
      <p className="mt-6 text-sm text-ink-soft">
        Already set up?{" "}
        <Link href="/sign-in" className="font-medium text-neel-700 hover:underline">
          Sign in
        </Link>
      </p>
    </Card>
  );
}
