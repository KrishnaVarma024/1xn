import Link from "next/link";
import { notFound } from "next/navigation";
import type { Metadata } from "next";
import { Badge, Card, PageHeader, Table, td, th } from "@/components/ui";
import { api, ApiError } from "@/lib/api";
import { isUUID } from "@/lib/ids";
import { channelLabel, dateTimeIST, methodLabel } from "@/lib/labels";
import { formatPaise, formatRate } from "@/lib/money";
import type { Bill } from "@/lib/types";
import { NoAccess } from "@/components/no-access";
import { can } from "@/lib/access";

export const metadata: Metadata = { title: "Bill" };

export default async function BillPage({ params }: PageProps<"/bills/[id]">) {
  if (!(await can("bills"))) return <NoAccess page="Bill" />;
  const { id } = await params;
  if (!isUUID(id)) notFound();
  let bill: Bill;
  try {
    bill = await api<Bill>(`/v1/bills/${id}`);
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) notFound();
    throw err;
  }
  const rows: [string, number][] = [
    ["Subtotal", bill.subtotal],
    ["Discount", -bill.discount_total],
    ["Service charge", bill.service_charge],
    ["Taxable value", bill.taxable_total],
    ["CGST", bill.cgst],
    ["SGST", bill.sgst],
    ["IGST", bill.igst],
    ["Round-off", bill.round_off],
  ];
  return (
    <>
      <PageHeader
        title={`Invoice ${bill.invoice_no}`}
        subtitle={
          <>
            {dateTimeIST(bill.issued_at)} · {channelLabel[bill.channel] ?? bill.channel}
            {bill.table_label && ` · table ${bill.table_label}`}
            {bill.covers ? ` · ${bill.covers} guests` : ""}
          </>
        }
        actions={
          <>
            {bill.verification === "match" ? <Badge tone="good">Server agrees with the till</Badge> : <Badge tone="bad">Price mismatch</Badge>}
            {bill.ledger_status === "posted" && <Badge tone="good">In the books</Badge>}
            {bill.ledger_status === "blocked" && <Badge tone="warn">Not posted: period closed</Badge>}
          </>
        }
      />
      <div className="grid gap-6 lg:grid-cols-3">
        <div className="space-y-6 lg:col-span-2">
          <Table>
            <thead>
              <tr>
                <th className={th}>Item</th>
                <th className={th}>Qty</th>
                <th className={th}>GST</th>
                <th className={`${th} text-right`}>Amount</th>
              </tr>
            </thead>
            <tbody>
              {bill.lines.map((l) => (
                <tr key={l.line_no}>
                  <td className={td}>{l.name}</td>
                  <td className={td}>{l.qty}</td>
                  <td className={td}>{formatRate(l.tax_rate_bp)}</td>
                  <td className={`${td} text-right`}>
                    {formatPaise(l.net)}
                    {l.discount > 0 && <div className="text-xs text-ink-faint">after {formatPaise(l.discount)} off</div>}
                  </td>
                </tr>
              ))}
            </tbody>
            <tfoot>
              {rows
                .filter(([, v]) => v !== 0)
                .map(([label, v]) => (
                  <tr key={label}>
                    <td className={`${td} text-ink-soft`} colSpan={3}>
                      {label}
                    </td>
                    <td className={`${td} text-right text-ink-soft`}>{formatPaise(v)}</td>
                  </tr>
                ))}
              <tr>
                <td className={`${td} font-semibold`} colSpan={3}>
                  Total
                </td>
                <td className={`${td} text-right text-lg font-semibold`}>{formatPaise(bill.grand_total)}</td>
              </tr>
            </tfoot>
          </Table>
          {bill.order_id && (
            <Link className="text-sm font-medium text-neel-700 hover:underline" href={`/orders/${bill.order_id}`}>
              See the table order →
            </Link>
          )}
        </div>
        <div className="space-y-6">
          <Card title="Paid with">
            <ul className="space-y-1 text-sm">
              {bill.payments.map((p, i) => (
                <li key={i} className="flex justify-between">
                  <span>
                    {methodLabel[p.method] ?? p.method}
                    {p.reference && <span className="ml-1 font-mono text-xs text-ink-faint">{p.reference}</span>}
                  </span>
                  <span>{formatPaise(p.amount)}</span>
                </li>
              ))}
              {bill.tip > 0 && (
                <li className="flex justify-between text-ink-soft">
                  <span>of which tip</span>
                  <span>{formatPaise(bill.tip)}</span>
                </li>
              )}
            </ul>
          </Card>
          <Card title="In the books">
            {bill.journal_lines.length === 0 ? (
              <p className="text-sm text-ink-soft">No journal entry (nothing to post, or the period was closed).</p>
            ) : (
              <ul className="space-y-1 font-mono text-xs">
                {bill.journal_lines.map((l) => (
                  <li key={l.line_no} className="flex justify-between gap-2">
                    <span>
                      {l.amount >= 0 ? "Dr" : "Cr"} {l.account_code} <span className="font-sans text-ink-soft">{l.account_name}</span>
                    </span>
                    <span>{formatPaise(Math.abs(l.amount))}</span>
                  </li>
                ))}
              </ul>
            )}
          </Card>
        </div>
      </div>
    </>
  );
}
