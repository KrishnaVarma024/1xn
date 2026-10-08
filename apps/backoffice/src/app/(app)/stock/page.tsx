import Link from "next/link";
import type { Metadata } from "next";
import { ActionForm, SubmitButton } from "@/components/forms";
import { Card, EmptyState, Field, Input, PageHeader, Select, Stat, Table, td, th } from "@/components/ui";
import { api } from "@/lib/api";
import { getContext } from "@/lib/context";
import { formatPaise } from "@/lib/money";
import { formatQty, perUnit } from "@/lib/quantity";
import type { StockList } from "@/lib/types";
import { createStockItem } from "./actions";
import { StockStatus } from "./status";
import { StockTabs } from "./tabs";
import { NoAccess } from "@/components/no-access";
import { can } from "@/lib/access";

export const metadata: Metadata = { title: "Stock" };

export default async function StockPage() {
  if (!(await can("stock"))) return <NoAccess page="Stock" />;
  const { outlet } = await getContext();
  const { items, value } = await api<StockList>(`/v1/outlets/${outlet.id}/stock`);
  const active = items.filter((i) => i.active);
  const low = active.filter((i) => i.status === "low" || i.status === "out");
  const negative = active.filter((i) => i.status === "negative");

  return (
    <>
      <PageHeader title="Stock" subtitle={`What ${outlet.name} holds, at average cost. Bills use up their dishes' recipes as they sync.`} />
      <StockTabs active="/stock" />
      <div className="mb-6 grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Stat label="Stock value" value={formatPaise(value)} hint={`${active.length} items`} />
        <Stat label="Running low" value={low.length} hint={low.length ? low.map((i) => i.name).join(", ") : "nothing to reorder"} />
        <Stat
          label="Below zero"
          value={negative.length}
          hint={negative.length ? "used more than was booked in: record the delivery" : "books and kitchen agree"}
        />
        <Stat label="Not stocked yet" value={active.filter((i) => i.status === "new").length} hint="no delivery booked here" />
      </div>

      {items.length === 0 ? (
        <EmptyState title="No stock items yet">Add the ingredients you buy, then give your dishes recipes.</EmptyState>
      ) : (
        <Table>
          <thead>
            <tr>
              <th className={`${th} min-w-40`}>Item</th>
              <th className={`${th} text-right`}>On hand</th>
              <th className={`${th} text-right`}>Average cost</th>
              <th className={`${th} text-right`}>Value</th>
              <th className={`${th} text-right`}>Reorder at</th>
              <th className={th}>Status</th>
            </tr>
          </thead>
          <tbody>
            {items.map((i) => (
              <tr key={i.id} className={`hover:bg-chalk ${i.active ? "" : "text-ink-faint"}`} data-testid={`stock-${i.name}`}>
                <td className={td}>
                  <Link href={`/stock/${i.id}`} className="font-medium text-neel-700 hover:underline">
                    {i.name}
                  </Link>
                  {!i.active && <span className="ml-1 text-xs">(not used)</span>}
                </td>
                <td className={`${td} text-right`}>{formatQty(i.qty, i.unit)}</td>
                <td className={`${td} text-right`}>{i.unit_cost ? `${formatPaise(i.unit_cost)}/${perUnit(i.unit)}` : "—"}</td>
                <td className={`${td} text-right font-medium`}>{formatPaise(i.value)}</td>
                <td className={`${td} text-right`}>{i.reorder_level ? formatQty(i.reorder_level, i.unit) : "—"}</td>
                <td className={td}>
                  <StockStatus status={i.status} />
                </td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}

      {(await can("stock.manage")) && (
        <Card title="Add an item" className="mt-6">
          <ActionForm action={createStockItem} label="Add a stock item" className="grid gap-3 sm:grid-cols-[2fr_1fr_1fr_auto] sm:items-end">
            <Field label="Name">
              <Input name="name" placeholder="Paneer" required maxLength={100} />
            </Field>
            <Field label="Counted in">
              <Select name="unit" defaultValue="kg">
                <option value="kg">kg (grams)</option>
                <option value="l">litres (ml)</option>
                <option value="pc">pieces</option>
              </Select>
            </Field>
            <Field label="Reorder at" hint="optional, like 2 or 500 g">
              <Input name="reorder" placeholder="2" />
            </Field>
            <SubmitButton>Add item</SubmitButton>
          </ActionForm>
        </Card>
      )}
    </>
  );
}
