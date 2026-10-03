import type { Metadata } from "next";
import { ActionForm, SubmitButton } from "@/components/forms";
import { Card, EmptyState, Field, Input, PageHeader, Select } from "@/components/ui";
import { api } from "@/lib/api";
import { getContext } from "@/lib/context";
import { newDocId } from "@/lib/ids";
import { tradingDayIST } from "@/lib/labels";
import { formatQty } from "@/lib/quantity";
import type { StockList } from "@/lib/types";
import { recordWastage } from "../actions";
import { StockTabs } from "../tabs";

export const metadata: Metadata = { title: "Wastage" };

export default async function WastePage() {
  const { outlet } = await getContext();
  const { items } = await api<StockList>(`/v1/outlets/${outlet.id}/stock`);
  const usable = items.filter((i) => i.active);
  const today = tradingDayIST();

  return (
    <>
      <PageHeader title="Stock" subtitle="Food that was thrown away leaves stock at its cost and shows up as wastage, not as food cost." />
      <StockTabs active="/stock/waste" />
      {usable.length === 0 ? (
        <EmptyState title="Add stock items first" />
      ) : (
        <Card title="Record wastage">
          <ActionForm action={recordWastage.bind(null, newDocId())} label="Record wastage" className="grid gap-3 sm:grid-cols-2">
            <Field label="Item">
              <Select name="item" defaultValue="" required>
                <option value="">—</option>
                {usable.map((it) => (
                  <option key={it.id} value={it.id}>
                    {it.name} (on hand {formatQty(it.qty, it.unit)})
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="How much" hint="like 0.5, or 200 g, or 200 ml">
              <Input name="qty" required inputMode="decimal" />
            </Field>
            <Field label="Why">
              <Input name="reason" placeholder="Went sour in the walk-in" required maxLength={200} />
            </Field>
            <Field label="Date">
              <Input type="date" name="date" defaultValue={today} max={today} />
            </Field>
            <div className="sm:col-span-2">
              <SubmitButton pendingText="Booking…">Book wastage</SubmitButton>
            </div>
          </ActionForm>
        </Card>
      )}
    </>
  );
}
