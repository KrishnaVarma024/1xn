import Link from "next/link";
import type { Metadata } from "next";
import { Badge, EmptyState, Input, PageHeader, Stat, Table, buttonClass, td, th } from "@/components/ui";
import { api } from "@/lib/api";
import { getContext } from "@/lib/context";
import { isRealDay, longDate } from "@/lib/dates";
import { channelLabel, methodLabel, timeIST, todayIST } from "@/lib/labels";
import { formatPaise } from "@/lib/money";
import type { DayBills } from "@/lib/types";
import { NoAccess } from "@/components/no-access";
import { can } from "@/lib/access";

export const metadata: Metadata = { title: "Bills" };

export default async function BillsPage({ searchParams }: PageProps<"/bills">) {
  if (!(await can("bills"))) return <NoAccess page="Bills" />;
  const { date } = await searchParams;
  const day = isRealDay(date) ? date : todayIST();
  const { outlet } = await getContext();
  const res = await api<DayBills>(`/v1/bills?outlet_id=${outlet.id}&date=${day}`);
  const methods = Object.entries(res.by_method).sort((a, b) => b[1] - a[1]);

  return (
    <>
      <PageHeader
        title="Bills"
        subtitle={`Business day ${longDate(day)}`}
        actions={
          <form className="flex items-center gap-2" aria-label="Pick a day">
            <Input type="date" name="date" defaultValue={day} max={todayIST()} aria-label="Business day" />
            <button className={buttonClass("secondary")}>Show</button>
          </form>
        }
      />
      <div className="mb-6 grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Stat label="Bills" value={res.count} />
        <Stat label="Sales" value={formatPaise(res.grand_total)} hint="including GST" />
        <Stat label="GST collected" value={formatPaise(res.tax_total)} />
        <Stat
          label="Cash taken"
          value={formatPaise(res.by_method.cash ?? 0)}
          hint={
            methods
              .filter(([m]) => m !== "cash")
              .map(([m, a]) => `${methodLabel[m] ?? m} ${formatPaise(a)}`)
              .join(" · ") || "no other payments"
          }
        />
      </div>
      {res.count > res.bills.length && (
        <p className="mb-3 text-sm text-ink-soft">
          Showing the latest {res.bills.length} of {res.count} bills. The totals above cover all of them.
        </p>
      )}
      {res.bills.length === 0 ? (
        <EmptyState title="No bills on this day">Bills appear here as soon as a till syncs them.</EmptyState>
      ) : (
        <Table>
          <thead>
            <tr>
              <th className={th}>Invoice</th>
              <th className={th}>Time</th>
              <th className={th}>Type</th>
              <th className={th}>Checks</th>
              <th className={`${th} text-right`}>GST</th>
              <th className={`${th} text-right`}>Total</th>
            </tr>
          </thead>
          <tbody>
            {res.bills.map((b) => (
              <tr key={b.id} className="hover:bg-chalk">
                <td className={td}>
                  <Link href={`/bills/${b.id}`} className="font-mono text-neel-700 hover:underline">
                    {b.invoice_no}
                  </Link>
                </td>
                <td className={td}>{timeIST(b.issued_at)}</td>
                <td className={td}>
                  {channelLabel[b.channel] ?? b.channel}
                  {b.table_label && <span className="text-ink-soft"> · {b.table_label}</span>}
                </td>
                <td className={td}>
                  <div className="flex flex-wrap gap-1">
                    {b.verification === "mismatch" && <Badge tone="bad">Price mismatch</Badge>}
                    {b.ledger_status === "blocked" && <Badge tone="warn">Not posted</Badge>}
                    {b.verification === "match" && b.ledger_status === "posted" && <Badge tone="good">OK</Badge>}
                  </div>
                </td>
                <td className={`${td} text-right`}>{formatPaise(b.tax_total)}</td>
                <td className={`${td} text-right font-medium`}>{formatPaise(b.grand_total)}</td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </>
  );
}
