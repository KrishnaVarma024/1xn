import { redirect } from "next/navigation";
import type { Metadata } from "next";
import { Card } from "@/components/ui";
import { formatPhone } from "@/lib/phone";
import { getPendingCode } from "@/lib/session";
import { CodeForm } from "./code-form";

export const metadata: Metadata = { title: "Enter the code" };

export default async function CodePage() {
  const pending = await getPendingCode();
  if (!pending) redirect("/sign-in");
  // Until an SMS provider is set up, the API prints codes in its log.
  const devHint = !process.env.ONEXN_SMS_PROVIDER;
  return (
    <Card>
      <CodeForm phone={formatPhone(pending.phone)} devHint={devHint} />
    </Card>
  );
}
