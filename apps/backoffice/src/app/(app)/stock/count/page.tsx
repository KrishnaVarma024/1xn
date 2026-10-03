import type { Metadata } from "next";
import { ActionForm, SubmitButton } from "@/components/forms";
import { EmptyState, Field, Input, PageHeader, Table, td, th } from "@/components/ui";
import { api } from "@/lib/api";
import { getContext } from "@/lib/context";
import { newDocId } from "@/lib/ids";
import { formatQty, perUnit } from "@/lib/quantity";
import type { StockList } from "@/lib/types";
import { recordCount } from "../actions";
import { StockStatus } from "../status";
import { StockTabs } from "../tabs";

export const metadata: Metadata = { title: "Stock count" };

export default async function CountPage() {
  const { outlet } = await getContext();
  const { items } = await api<StockList>(`/v1/outlets/${outlet.id}/stock`);
  const usable = items.filter((i) => i.active);
  const anyUncosted = usable.some((i) => i.unit_cost <= 0 && i.qty >= 0);

  return (
    <>
      <PageHeader
        title="Stock"
        subtitle="Count what is on the shelves. The books move to match, and the difference shows as count differences."
      />
      <StockTabs active="/stock/count" />
      {usable.length === 0 ? (
        <EmptyState title="Add stock items first" />
      ) : (
        <ActionForm action={recordCount.bind(null, newDocId())} label="Stock count">
          <Table>
            <thead>
              <tr>
                <th className={`${th} min-w-40`}>Item</th>
                <th className={`${th} text-right`}>The books say</th>
                <th className={th}>Counted</th>
                {anyUncosted && <th className={th}>Cost of one unit, ₹</th>}
                <th className={th}>Status</th>
              </tr>
            </thead>
            <tbody>
              {usable.map((it) => (
                <tr key={it.id}>
                  <td className={td}>{it.name}</td>
                  <td className={`${td} text-right`}>{formatQty(it.qty, it.unit)}</td>
                  <td className={td}>
                    <Input
                      name={`counted_${it.id}`}
                      aria-label={`Counted ${it.name}`}
                      inputMode="decimal"
                      placeholder={it.unit === "pc" ? "pieces" : it.unit}
                      className="max-w-36"
                    />
                  </td>
                  {anyUncosted && (
                    <td className={td}>
                      {it.unit_cost <= 0 && it.qty >= 0 ? (
                        <Input
                          name={`cost_${it.id}`}
                          aria-label={`Cost of ${it.name}`}
                          inputMode="decimal"
                          placeholder={`per ${perUnit(it.unit)}`}
                          className="max-w-36"
                        />
                      ) : (
                        <span className="text-xs text-ink-faint">known</span>
                      )}
                    </td>
                  )}
                  <td className={td}>
                    <StockStatus status={it.status} />
                  </td>
                </tr>
              ))}
            </tbody>
          </Table>
          <div className="mt-4 grid gap-3 sm:grid-cols-[2fr_auto] sm:items-end">
            <Field label="Note" hint="optional">
              <Input name="note" placeholder="Monthly count" maxLength={200} />
            </Field>
            <SubmitButton pendingText="Booking…">Book the count</SubmitButton>
          </div>
          <p className="mt-2 text-xs text-ink-faint">
            A count is today&apos;s: it is compared with the books as they are now. Leave an item blank to skip it.
            {anyUncosted && " Items never bought here need the cost of one unit, so the stock you found enters the books at its price."}
          </p>
        </ActionForm>
      )}
    </>
  );
}
