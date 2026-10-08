import Link from "next/link";
import type { Metadata } from "next";
import { ActionForm, SubmitButton } from "@/components/forms";
import { Badge, Card, EmptyState, Field, Input, PageHeader, Stat, Table, td, th } from "@/components/ui";
import { api } from "@/lib/api";
import { getContext } from "@/lib/context";
import { longDate } from "@/lib/dates";
import { formatPaise } from "@/lib/money";
import { formatPhone } from "@/lib/phone";
import type { SupplierList } from "@/lib/types";
import { StockTabs } from "../tabs";
import { createSupplier } from "./actions";

export const metadata: Metadata = { title: "Suppliers" };

export default async function SuppliersPage() {
  const { outlet } = await getContext();
  const { suppliers, owed } = await api<SupplierList>(`/v1/outlets/${outlet.id}/stock/suppliers`);
  const due = suppliers.filter((s) => s.owed > 0);
  const recent = suppliers.reduce((n, s) => n + s.bought_30d, 0);

  return (
    <>
      <PageHeader
        title="Stock"
        subtitle={`Who ${outlet.legal_entity_name} buys from, and what it owes them for deliveries bought on credit.`}
      />
      <StockTabs active="/stock/suppliers" />
      <div className="mb-6 grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Stat label="Owed to suppliers" value={formatPaise(owed)} hint="the books' Payable to suppliers" />
        <Stat label="Suppliers to pay" value={due.length} hint={due.length ? due.map((s) => s.name).join(", ") : "all paid up"} />
        <Stat label="Bought in 30 days" value={formatPaise(recent)} hint="however it was paid" />
      </div>

      {suppliers.length === 0 ? (
        <EmptyState title="No suppliers yet">They are added as you receive goods, or add them below.</EmptyState>
      ) : (
        <Table>
          <thead>
            <tr>
              <th className={`${th} min-w-40`}>Supplier</th>
              <th className={th}>Phone</th>
              <th className={th}>Last delivery</th>
              <th className={`${th} text-right`}>Bought, 30 days</th>
              <th className={`${th} text-right`}>Owed</th>
            </tr>
          </thead>
          <tbody>
            {suppliers.map((s) => (
              <tr key={s.id} className="hover:bg-chalk" data-testid={`supplier-${s.name}`}>
                <td className={td}>
                  <Link href={`/stock/suppliers/${s.id}`} className="font-medium text-neel-700 hover:underline">
                    {s.name}
                  </Link>
                  {s.gstin && <div className="text-xs text-ink-faint">GSTIN {s.gstin}</div>}
                </td>
                <td className={td}>{s.phone ? formatPhone(s.phone) : "—"}</td>
                <td className={td}>{s.last_delivery ? longDate(s.last_delivery) : "—"}</td>
                <td className={`${td} text-right tabular-nums`}>{formatPaise(s.bought_30d)}</td>
                <td className={`${td} text-right tabular-nums`}>
                  {s.owed > 0 ? <span className="font-medium">{formatPaise(s.owed)}</span> : <Badge tone="good">Paid up</Badge>}
                </td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}

      <Card title="Add a supplier" className="mt-6">
        <ActionForm action={createSupplier} label="Add a supplier" className="grid gap-3 sm:grid-cols-[2fr_1fr_1fr_auto] sm:items-end">
          <Field label="Name">
            <Input name="name" placeholder="Grain House" required maxLength={100} />
          </Field>
          <Field label="Phone" hint="optional">
            <Input name="phone" inputMode="tel" placeholder="98765 43210" />
          </Field>
          <Field label="GSTIN" hint="optional">
            <Input name="gstin" maxLength={20} placeholder="29ABCDE1234F1ZW" />
          </Field>
          <SubmitButton>Add supplier</SubmitButton>
        </ActionForm>
      </Card>
    </>
  );
}
