import Link from "next/link";
import type { Metadata } from "next";
import { AutoRefresh } from "@/components/auto-refresh";
import { Badge, EmptyState, LinkButton, PageHeader, Stat } from "@/components/ui";
import { api } from "@/lib/api";
import { getContext } from "@/lib/context";
import { channelLabel, kotStatusLabel, since } from "@/lib/labels";
import { formatPaise } from "@/lib/money";
import { kotTone, openValue } from "@/lib/orders";
import type { Menu, Order, Table } from "@/lib/types";

export const metadata: Metadata = { title: "Live floor" };

export default async function FloorPage() {
  const { outlet } = await getContext();
  const [menu, { orders }] = await Promise.all([
    api<Menu>(`/v1/outlets/${outlet.id}/menu`),
    api<{ orders: Order[] }>(`/v1/outlets/${outlet.id}/orders`),
  ]);
  const sections = new Map(menu.sections.map((s) => [s.id, s.name]));
  const byTable = new Map<string, Order[]>();
  const noTable: Order[] = [];
  for (const o of orders) {
    if (o.table_id) byTable.set(o.table_id, [...(byTable.get(o.table_id) ?? []), o]);
    else noTable.push(o);
  }
  const tables = menu.floors.flatMap((f) => f.tables).filter((t) => t.active);
  const occupied = tables.filter((t) => byTable.has(t.id)).length;
  const covers = orders.reduce((s, o) => s + o.covers, 0);
  const value = orders.reduce((s, o) => s + openValue(o), 0); // split bills: paid lines are in Sales already
  const waiting = orders.flatMap((o) => o.kots).filter((k) => k.status === "new" || k.status === "preparing").length;

  return (
    <>
      <PageHeader title="Live floor" subtitle={<AutoRefresh />} />
      <div className="mb-8 grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Stat label="Tables in use" value={`${occupied} / ${tables.length}`} />
        <Stat label="Guests seated" value={covers} />
        <Stat label="On open orders" value={formatPaise(value)} hint="before tax" />
        <Stat label="Tickets cooking" value={waiting} hint="KOTs new or preparing" />
      </div>

      {menu.floors.length === 0 ? (
        <EmptyState title="No tables yet">
          Add a floor and its tables, and captains can start taking orders.
          <div className="mt-4">
            <LinkButton href="/tables" variant="primary">
              Set up tables
            </LinkButton>
          </div>
        </EmptyState>
      ) : (
        menu.floors.map((floor) => (
          <section key={floor.id} className="mb-8">
            <h2 className="mb-3 text-sm font-semibold text-ink-soft">{floor.name}</h2>
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5">
              {floor.tables.map((t) => (
                <TableCard key={t.id} table={t} orders={byTable.get(t.id) ?? []} sections={sections} />
              ))}
            </div>
          </section>
        ))
      )}

      {noTable.length > 0 && (
        <section className="mb-8">
          <h2 className="mb-3 text-sm font-semibold text-ink-soft">Without a table</h2>
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5">
            {noTable.map((o) => (
              <OrderCard key={o.id} title={channelLabel[o.channel] ?? o.channel} order={o} sections={sections} />
            ))}
          </div>
        </section>
      )}
    </>
  );
}

function TableCard({ table, orders, sections }: { table: Table; orders: Order[]; sections: Map<string, string> }) {
  if (orders.length === 0) {
    return (
      <div
        data-testid={`table-${table.label}`}
        className={`flex min-h-32 flex-col justify-between rounded-xl border bg-white p-3 ${
          table.active ? "border-line" : "border-dashed border-line opacity-60"
        }`}
      >
        <div className="flex items-baseline justify-between">
          <span className="text-lg font-semibold">{table.label}</span>
          <span className="text-xs text-ink-faint">{table.seats} seats</span>
        </div>
        <span className="text-xs text-ink-faint">{table.active ? "Free" : "Switched off"}</span>
      </div>
    );
  }
  return (
    <>
      {orders.map((o) => (
        <OrderCard key={o.id} title={table.label} order={o} sections={sections} testId={`table-${table.label}`} />
      ))}
    </>
  );
}

function OrderCard({ title, order, sections, testId }: { title: string; order: Order; sections: Map<string, string>; testId?: string }) {
  const live = order.lines.filter((l) => l.status === "held" || l.status === "fired").length;
  return (
    <Link
      href={`/orders/${order.id}`}
      data-testid={testId}
      className="flex min-h-32 flex-col justify-between rounded-xl border border-neel-200 bg-neel-50 p-3 transition-colors hover:border-neel-600"
    >
      <div className="flex items-baseline justify-between gap-2">
        <span className="text-lg font-semibold text-neel-800">{title}</span>
        <span className="text-xs text-ink-soft">{since(order.opened_at)}</span>
      </div>
      <div className="mt-2 flex flex-wrap gap-1">
        {order.kots.map((k) => (
          <Badge key={k.id} tone={kotTone[k.status]}>
            {sections.get(k.section_id ?? "") ?? "Kitchen"}: {kotStatusLabel[k.status]}
          </Badge>
        ))}
        {order.spillover_of && <Badge tone="warn">Late items</Badge>}
      </div>
      <div className="mt-2 flex items-end justify-between gap-2">
        <span className="text-xs text-ink-soft">
          {order.covers > 0 && <span className="block">{order.covers} guests</span>}
          {live} {live === 1 ? "dish" : "dishes"} open
        </span>
        <span className="whitespace-nowrap text-sm font-semibold tabular-nums">{formatPaise(openValue(order))}</span>
      </div>
    </Link>
  );
}
