import type { Metadata } from "next";
import { ActionForm, SubmitButton } from "@/components/forms";
import { Badge, Card, EmptyState, Field, Input, PageHeader, Select, Table, td, th } from "@/components/ui";
import { api } from "@/lib/api";
import { getContext } from "@/lib/context";
import { longDate } from "@/lib/dates";
import { newDocId } from "@/lib/ids";
import { paidByLabel, stockKindLabel, tradingDayIST } from "@/lib/labels";
import { formatPaise } from "@/lib/money";
import { formatQty } from "@/lib/quantity";
import type { StockDoc, StockList, SupplierList } from "@/lib/types";
import { recordPurchase } from "../actions";
import { StockTabs } from "../tabs";
import { NoAccess } from "@/components/no-access";
import { can } from "@/lib/access";

export const metadata: Metadata = { title: "Receive goods" };

const ROWS = 8;

export default async function ReceivePage() {
  if (!(await can("stock.receive"))) return <NoAccess page="Receive goods" />;
  const { outlet } = await getContext();
  const [{ items }, { documents }, { suppliers }] = await Promise.all([
    api<StockList>(`/v1/outlets/${outlet.id}/stock`),
    api<{ documents: StockDoc[] }>(`/v1/outlets/${outlet.id}/stock/purchases`),
    // Suppliers' names for the list; what they are owed is not a cashier's business.
    (await can("suppliers")) ? api<SupplierList>(`/v1/outlets/${outlet.id}/stock/suppliers`) : { suppliers: [], owed: 0 },
  ]);
  const usable = items.filter((i) => i.active);
  const [cashOut, packs] = [await can("cash.out"), await can("stock.manage")];
  const today = tradingDayIST();

  return (
    <>
      <PageHeader title="Stock" subtitle="Book a delivery: the stock goes up, and the books show what it cost and how it was paid." />
      <StockTabs active="/stock/receive" />
      {usable.length === 0 ? (
        <EmptyState title="Add stock items first">Each line of a delivery is one of your stock items.</EmptyState>
      ) : (
        <Card title="Receive goods" className="mb-8">
          {/* A new id each time the page renders: a double click books the delivery once. */}
          <ActionForm action={recordPurchase.bind(null, newDocId())} label="Receive goods">
            <div className="grid gap-3 sm:grid-cols-4">
              <Field label="Supplier">
                <Input name="supplier" list="supplier-names" placeholder="Bandra Meat & Dairy" required maxLength={100} autoComplete="off" />
                <datalist id="supplier-names">
                  {suppliers.map((s) => (
                    <option key={s.id} value={s.name} />
                  ))}
                </datalist>
              </Field>
              <Field label="Their invoice no." hint="optional">
                <Input name="invoice_no" maxLength={40} />
              </Field>
              <Field label="Date">
                <Input type="date" name="date" defaultValue={today} max={today} />
              </Field>
              <Field label="Paid by" hint="from a till: the cashier also records it on the till as a supplier payout">
                <Select name="paid_by" defaultValue="credit">
                  <option value="credit">On credit (pay later)</option>
                  {cashOut && <option value="bank">Bank or UPI</option>}
                  {cashOut && <option value="safe">Cash from the safe</option>}
                  <option value="till">Cash from a till drawer</option>
                </Select>
              </Field>
            </div>
            <div className="mt-4 space-y-2">
              <div className="hidden grid-cols-[2fr_1fr_1fr] gap-2 text-xs font-medium text-ink-soft sm:grid">
                <span>Item</span>
                <span>Quantity (like 5, or 500 g; for a pack, how many)</span>
                <span>Cost for the line, ₹ (with taxes)</span>
              </div>
              {Array.from({ length: ROWS }, (_, i) => (
                <div key={i} className="grid grid-cols-1 gap-2 sm:grid-cols-[2fr_1fr_1fr]">
                  <Select name={`item${i}`} defaultValue="" aria-label={`Row ${i + 1} item`}>
                    <option value="">—</option>
                    {usable.map((it) => [
                      <option key={it.id} value={it.id}>
                        {it.name} ({it.unit === "pc" ? "pieces" : it.unit})
                      </option>,
                      // A pack is "<item id>|<pack id>": the quantity is then how many packs.
                      ...it.packs
                        .filter((p) => p.active)
                        .map((p) => (
                          <option key={p.id} value={`${it.id}|${p.id}`}>
                            {it.name}: {p.name} of {formatQty(p.qty, it.unit)}
                          </option>
                        )),
                    ])}
                  </Select>
                  <Input name={`qty${i}`} aria-label={`Row ${i + 1} quantity`} inputMode="decimal" />
                  <Input name={`cost${i}`} aria-label={`Row ${i + 1} cost`} inputMode="decimal" />
                </div>
              ))}
            </div>
            <div className="mt-4 flex flex-wrap items-center gap-3">
              <SubmitButton pendingText="Booking…">Book the delivery</SubmitButton>
              {packs && (
                <span className="text-xs text-ink-faint">
                  Bought by the sack or tin? Add the pack on the item&apos;s page and it shows up in the list.
                </span>
              )}
            </div>
          </ActionForm>
        </Card>
      )}

      <h2 className="mb-3 text-sm font-semibold">Recent stock documents</h2>
      {documents.length === 0 ? (
        <EmptyState title="No deliveries, wastage or counts yet" />
      ) : (
        <Table>
          <thead>
            <tr>
              <th className={th}>Day</th>
              <th className={th}>What</th>
              <th className={th}>Details</th>
              <th className={`${th} text-right`}>Value</th>
            </tr>
          </thead>
          <tbody>
            {documents.map((d) => (
              <tr key={d.id}>
                <td className={td}>{longDate(d.business_date)}</td>
                <td className={td}>
                  <Badge tone={d.kind === "purchase" ? "good" : "warn"}>{stockKindLabel[d.kind]}</Badge>
                  {d.ledger_status === "blocked" && <Badge tone="bad">Not in the books</Badge>}
                </td>
                <td className={td}>
                  {d.kind === "purchase"
                    ? `${d.supplier}${d.invoice_no ? `, invoice ${d.invoice_no}` : ""} · ${paidByLabel[d.paid_by ?? ""] ?? ""} · ${d.lines} ${d.lines === 1 ? "item" : "items"}`
                    : `${d.note ?? ""}${d.kind === "count" ? ` · ${d.lines} ${d.lines === 1 ? "item" : "items"} adjusted` : ""}`}
                </td>
                <td className={`${td} text-right`}>{formatPaise(d.total)}</td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </>
  );
}
