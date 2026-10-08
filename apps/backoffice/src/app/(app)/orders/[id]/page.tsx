import Link from "next/link";
import { notFound } from "next/navigation";
import type { Metadata } from "next";
import { Badge, Card, PageHeader, Table, td, th } from "@/components/ui";
import { api, ApiError } from "@/lib/api";
import { getContext } from "@/lib/context";
import { isUUID } from "@/lib/ids";
import { channelLabel, dateTimeIST, kotStatusLabel, timeIST } from "@/lib/labels";
import { formatPaise } from "@/lib/money";
import { billedValue, kotTone, lineValue, orderValue } from "@/lib/orders";
import type { Menu, Order, OrderLine } from "@/lib/types";
import { NoAccess } from "@/components/no-access";
import { can } from "@/lib/access";

export const metadata: Metadata = { title: "Order" };

const lineTone = { held: "neutral", fired: "info", billed: "good", voided: "bad" } as const;
const lineLabel = { held: "Not sent", fired: "In kitchen", billed: "Billed", voided: "Voided" };

export default async function OrderPage({ params }: PageProps<"/orders/[id]">) {
  if (!(await can("floor"))) return <NoAccess page="Order" />;
  const { id } = await params;
  if (!isUUID(id)) notFound();
  const { outlet } = await getContext();
  let order: Order;
  try {
    order = await api<Order>(`/v1/orders/${id}`);
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) notFound();
    throw err;
  }
  const menu = await api<Menu>(`/v1/outlets/${outlet.id}/menu`);
  const table = menu.floors.flatMap((f) => f.tables).find((t) => t.id === order.table_id);
  const sections = new Map(menu.sections.map((s) => [s.id, s.name]));
  const bills = [...new Set(order.lines.map((l) => l.bill_id).filter(Boolean))] as string[];

  return (
    <>
      <PageHeader
        title={table ? `Table ${table.label}` : (channelLabel[order.channel] ?? "Order")}
        subtitle={
          <>
            Opened {dateTimeIST(order.opened_at)}
            {order.covers > 0 && ` · ${order.covers} guests`}
            {order.closed_at && ` · closed ${timeIST(order.closed_at)}`}
          </>
        }
        actions={<StatusBadge order={order} />}
      />
      {order.merged_into && (
        <p className="mb-4 rounded-lg bg-neel-50 px-3 py-2 text-sm">
          This table was joined to{" "}
          <Link className="font-medium text-neel-700 underline" href={`/orders/${order.merged_into}`}>
            another order
          </Link>
          . Its dishes moved there.
        </p>
      )}
      <div className="grid gap-6 lg:grid-cols-3">
        <div className="lg:col-span-2">
          <Table>
            <thead>
              <tr>
                <th className={th}>Dish</th>
                <th className={th}>Qty</th>
                <th className={th}>Status</th>
                <th className={`${th} text-right`}>Amount</th>
              </tr>
            </thead>
            <tbody>
              {order.lines.map((l) => (
                <LineRow key={l.id} line={l} />
              ))}
            </tbody>
            <tfoot>
              <tr>
                <td className={`${td} font-medium`} colSpan={3}>
                  Total before tax (voids excluded)
                </td>
                <td className={`${td} text-right font-semibold`}>{formatPaise(orderValue(order))}</td>
              </tr>
              {billedValue(order) > 0 && (
                <tr>
                  <td className={`${td} text-ink-soft`} colSpan={3}>
                    Already billed
                  </td>
                  <td className={`${td} text-right text-ink-soft`}>{formatPaise(billedValue(order))}</td>
                </tr>
              )}
            </tfoot>
          </Table>
        </div>
        <div className="space-y-6">
          <Card title="Kitchen tickets">
            {order.kots.length === 0 ? (
              <p className="text-sm text-ink-soft">Nothing sent to the kitchen yet.</p>
            ) : (
              <ul className="space-y-2">
                {order.kots.map((k) => (
                  <li key={k.id} className="flex items-center justify-between gap-2 text-sm">
                    <span>
                      <span className="font-mono text-xs text-ink-faint">{k.no}</span> {sections.get(k.section_id ?? "") ?? "Kitchen"} ·{" "}
                      {k.line_ids.length} {k.line_ids.length === 1 ? "dish" : "dishes"} · {timeIST(k.fired_at)}
                    </span>
                    <Badge tone={kotTone[k.status]}>{kotStatusLabel[k.status]}</Badge>
                  </li>
                ))}
              </ul>
            )}
          </Card>
          {bills.length > 0 && (
            <Card title="Bills">
              <ul className="space-y-1 text-sm">
                {bills.map((b) => (
                  <li key={b}>
                    <Link className="text-neel-700 hover:underline" href={`/bills/${b}`}>
                      View bill
                    </Link>
                  </li>
                ))}
              </ul>
            </Card>
          )}
        </div>
      </div>
    </>
  );
}

function StatusBadge({ order }: { order: Order }) {
  const map = { open: ["info", "Open"], closed: ["good", "Paid"], merged: ["neutral", "Joined"], cancelled: ["bad", "Cancelled"] } as const;
  const [tone, label] = map[order.status];
  return <Badge tone={tone}>{label}</Badge>;
}

function LineRow({ line }: { line: OrderLine }) {
  const voided = line.status === "voided";
  return (
    <tr className={voided ? "text-ink-faint" : ""}>
      <td className={td}>
        <div className={voided ? "line-through" : "font-medium"}>{line.name}</div>
        {(line.modifiers ?? []).map((m, i) => (
          <div key={i} className="text-xs text-ink-soft">
            + {m.name} {m.price_paise > 0 && formatPaise(m.price_paise)}
          </div>
        ))}
        {line.note && <div className="text-xs italic text-ink-soft">“{line.note}”</div>}
        {line.void && (
          <div className="text-xs text-chilli-700">
            Voided: {line.void.reason}
            {line.void.approved_by ? ` (approved by ${line.void.approved_by})` : " (no approval)"}
          </div>
        )}
      </td>
      <td className={td}>{line.qty}</td>
      <td className={td}>
        <Badge tone={lineTone[line.status]}>{lineLabel[line.status]}</Badge>
      </td>
      <td className={`${td} text-right ${voided ? "line-through" : ""}`}>{formatPaise(lineValue(line))}</td>
    </tr>
  );
}
