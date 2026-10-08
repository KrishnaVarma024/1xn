import Link from "next/link";
import { notFound } from "next/navigation";
import type { Metadata } from "next";
import { ActionForm, SubmitButton } from "@/components/forms";
import { Badge, Card, EmptyState, Field, Input, PageHeader, Select, Stat, Table, td, th } from "@/components/ui";
import { api, ApiError } from "@/lib/api";
import { getContext } from "@/lib/context";
import { longDate } from "@/lib/dates";
import { isUUID, newDocId } from "@/lib/ids";
import { paidByLabel, tradingDayIST } from "@/lib/labels";
import { formatPaise, paiseToInput } from "@/lib/money";
import { formatPhone } from "@/lib/phone";
import type { Statement } from "@/lib/types";
import { StockTabs } from "../../tabs";
import { paySupplier, updateSupplier } from "../actions";
import { NoAccess } from "@/components/no-access";
import { can } from "@/lib/access";

export const metadata: Metadata = { title: "Supplier" };

export default async function SupplierPage({ params, searchParams }: PageProps<"/stock/suppliers/[id]">) {
  if (!(await can("suppliers"))) return <NoAccess page="Supplier" />;
  const { id } = await params;
  const { paid } = await searchParams;
  if (!isUUID(id)) notFound();
  const { me, outlet } = await getContext();
  let st: Statement;
  try {
    st = await api<Statement>(`/v1/outlets/${outlet.id}/stock/suppliers/${id}`);
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) notFound();
    throw err;
  }
  const { supplier, owed, lines } = st;
  const today = tradingDayIST();
  const outletName = new Map(me.outlets.map((o) => [o.id, o.name]));
  const lastPayment = lines.find((l) => l.kind === "payment");
  const justPaid = typeof paid === "string" ? lines.find((l) => l.kind === "payment" && l.id === paid) : undefined;
  const [pay, edit] = [await can("suppliers.pay"), await can("stock.manage")];

  return (
    <>
      <PageHeader title={supplier.name} subtitle={<Link href="/stock/suppliers" className="hover:underline">← All suppliers</Link>} />
      <StockTabs active="/stock/suppliers" />
      {justPaid && (
        <p role="status" className="mb-6 rounded-lg border border-curry-700/30 bg-curry-50 px-4 py-3 text-sm text-curry-700">
          Paid {formatPaise(justPaid.amount)} to {supplier.name} ({paidByLabel[justPaid.paid_by] ?? justPaid.paid_by}, {longDate(justPaid.date)}).{" "}
          {owed > 0 ? `Still owed ${formatPaise(owed)}.` : "Nothing is owed now."}
        </p>
      )}
      <div className="mb-6 grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Stat label="Owed now" value={formatPaise(owed)} hint={`by ${outlet.legal_entity_name}`} />
        <Stat
          label="Last paid"
          value={lastPayment ? formatPaise(lastPayment.amount) : "—"}
          hint={lastPayment ? longDate(lastPayment.date) : "no payments yet"}
        />
        <Stat label="Deliveries" value={lines.filter((l) => l.kind === "delivery").length} hint={lines.length >= 200 ? "the latest 200 lines" : undefined} />
      </div>

      <div className="mb-8 grid gap-6 lg:grid-cols-2">
        <Card title={`Pay ${supplier.name}`}>
          {owed <= 0 ? (
            <p className="text-sm text-ink-soft">Nothing is owed. Deliveries bought on credit show up here to be paid.</p>
          ) : !pay ? (
            <p className="text-sm text-ink-soft">{formatPaise(owed)} is owed. Only the owner records payments to suppliers.</p>
          ) : (
            // A new id each time the page renders: a double click pays once.
            <ActionForm action={paySupplier.bind(null, supplier.id, newDocId())} label={`Pay ${supplier.name}`} className="grid gap-3 sm:grid-cols-2">
              <Field label="Amount, ₹" hint={`owed ${formatPaise(owed)}`}>
                <Input name="amount" defaultValue={paiseToInput(owed)} required inputMode="decimal" />
              </Field>
              <Field label="Paid by" hint="from a till: the cashier also records the payout on the till">
                <Select name="paid_by" defaultValue="bank">
                  <option value="bank">Bank or UPI</option>
                  <option value="safe">Cash from the safe</option>
                  <option value="till">Cash from a till drawer</option>
                </Select>
              </Field>
              <Field label="Date">
                <Input type="date" name="date" defaultValue={today} max={today} />
              </Field>
              <Field label="Reference" hint="optional: cheque no., UTR or UPI ref">
                <Input name="reference" maxLength={100} />
              </Field>
              <div className="sm:col-span-2">
                <SubmitButton pendingText="Paying…">Record the payment</SubmitButton>
              </div>
            </ActionForm>
          )}
        </Card>

        {edit && (
          <Card title="Details">
            <ActionForm action={updateSupplier.bind(null, supplier.id)} label="Edit supplier" resetOnSuccess={false} className="grid gap-3">
              <Field label="Name">
                <Input name="name" defaultValue={supplier.name} required maxLength={100} />
              </Field>
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="Phone" hint="blank to remove">
                  <Input name="phone" defaultValue={supplier.phone ? formatPhone(supplier.phone) : ""} inputMode="tel" />
                </Field>
                <Field label="GSTIN" hint="blank to remove">
                  <Input name="gstin" defaultValue={supplier.gstin ?? ""} maxLength={20} />
                </Field>
              </div>
              <div>
                <SubmitButton variant="secondary">Save</SubmitButton>
              </div>
            </ActionForm>
          </Card>
        )}
      </div>

      <h2 className="mb-3 text-sm font-semibold">Statement</h2>
      {lines.length === 0 ? (
        <EmptyState title="Nothing yet">Deliveries from {supplier.name} and payments to them show up here.</EmptyState>
      ) : (
        <Table>
          <thead>
            <tr>
              <th className={th}>Day</th>
              <th className={th}>What</th>
              <th className={th}>Paid by</th>
              <th className={`${th} text-right`}>Amount</th>
              <th className={`${th} text-right`}>Owed after</th>
            </tr>
          </thead>
          <tbody>
            {lines.map((l) => (
              <tr key={l.id} data-testid={`line-${l.kind}`}>
                <td className={td}>{longDate(l.date)}</td>
                <td className={td}>
                  <Badge tone={l.kind === "payment" ? "info" : "good"}>{l.kind === "payment" ? "Payment" : "Delivery"}</Badge>
                  {(l.reference || l.outlet_id !== outlet.id) && (
                    <div className="mt-0.5 text-xs text-ink-soft">
                      {[
                        l.reference && (l.kind === "delivery" ? `invoice ${l.reference}` : `ref ${l.reference}`),
                        l.outlet_id !== outlet.id && `at ${outletName.get(l.outlet_id) ?? "another outlet"}`,
                      ]
                        .filter(Boolean)
                        .join(" · ")}
                    </div>
                  )}
                </td>
                <td className={td}>{paidByLabel[l.paid_by] ?? l.paid_by}</td>
                <td className={`${td} text-right tabular-nums ${l.kind === "delivery" && l.paid_by !== "credit" ? "text-ink-faint" : ""}`}>
                  {l.kind === "payment" ? "−" : ""}
                  {formatPaise(l.amount)}
                </td>
                <td className={`${td} text-right font-medium tabular-nums`}>{formatPaise(l.owed)}</td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      <p className="mt-3 text-xs text-ink-faint">
        Only deliveries on credit add to what is owed; ones paid on the spot are listed for the record.
      </p>
    </>
  );
}
