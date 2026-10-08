import Link from "next/link";
import { notFound } from "next/navigation";
import type { Metadata } from "next";
import { ActionForm, SubmitButton } from "@/components/forms";
import { Badge, Card, EmptyState, Field, Input, PageHeader, Stat, Table, td, th } from "@/components/ui";
import { api, ApiError } from "@/lib/api";
import { getContext } from "@/lib/context";
import { isUUID } from "@/lib/ids";
import { dateTimeIST, stockKindLabel } from "@/lib/labels";
import { formatPaise } from "@/lib/money";
import { formatQty, perUnit, qtyToFriendlyInput } from "@/lib/quantity";
import type { StockItem, StockMove } from "@/lib/types";
import { addPack, setPackActive, updateStockItem } from "../actions";
import { StockStatus } from "../status";
import { StockTabs } from "../tabs";

export const metadata: Metadata = { title: "Stock item" };

export default async function StockItemPage({ params }: PageProps<"/stock/[id]">) {
  const { id } = await params;
  if (!isUUID(id)) notFound();
  const { outlet } = await getContext();
  let res: { item: StockItem; moves: StockMove[] };
  try {
    res = await api(`/v1/outlets/${outlet.id}/stock/items/${id}`);
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) notFound();
    throw err;
  }
  const { item, moves } = res;

  return (
    <>
      <PageHeader title={item.name} subtitle={<Link href="/stock" className="hover:underline">← All stock</Link>} />
      <StockTabs active="" />
      <div className="mb-6 grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Stat label="On hand" value={formatQty(item.qty, item.unit)} hint={<StockStatus status={item.status} />} />
        <Stat label="Value" value={formatPaise(item.value)} />
        <Stat label="Average cost" value={item.unit_cost ? `${formatPaise(item.unit_cost)}/${perUnit(item.unit)}` : "—"} />
        <Stat label="Last bought at" value={item.last_cost ? `${formatPaise(item.last_cost)}/${perUnit(item.unit)}` : "—"} />
      </div>

      <Card title="Details" className="mb-6">
        <ActionForm
          action={updateStockItem.bind(null, item.id, item.unit)}
          label="Edit stock item"
          resetOnSuccess={false}
          className="grid gap-3 sm:grid-cols-[2fr_1fr_auto_auto] sm:items-end"
        >
          <Field label="Name">
            <Input name="name" defaultValue={item.name} required maxLength={100} />
          </Field>
          <Field label={`Reorder at (${item.unit === "pc" ? "pieces" : item.unit})`} hint="like 2, or 500 g">
            <Input name="reorder" defaultValue={item.reorder_level ? qtyToFriendlyInput(item.reorder_level, item.unit) : ""} />
          </Field>
          <label className="flex items-center gap-2 pb-2 text-sm">
            <input type="checkbox" name="active" defaultChecked={item.active} className="h-4 w-4 accent-neel-700" />
            In use
          </label>
          <SubmitButton>Save</SubmitButton>
        </ActionForm>
      </Card>

      <Card title="Packs" className="mb-6">
        <p className="mb-3 text-sm text-ink-soft">
          How {item.name} is bought: a sack, a tin, a crate. Receive &ldquo;2 sacks&rdquo; and the store gets twice the sack&apos;s size.
          A pack never changes size; for a new size add a new pack and stop using the old one.
        </p>
        {item.packs.length > 0 && (
          <ul className="mb-4 divide-y divide-line rounded-lg border border-line">
            {item.packs.map((p) => (
              <li key={p.id} className="flex flex-wrap items-center justify-between gap-3 px-3 py-2 text-sm" data-testid={`pack-${p.name}`}>
                <span className={p.active ? "" : "text-ink-faint"}>
                  <span className="font-medium">{p.name}</span> of {formatQty(p.qty, item.unit)}
                  {!p.active && <span className="ml-2"><Badge>Not in use</Badge></span>}
                </span>
                <ActionForm action={setPackActive.bind(null, item.id, p.id, !p.active)} label={`${p.active ? "Stop using" : "Use again"} ${p.name}`} resetOnSuccess={false}>
                  <SubmitButton variant="secondary" size="sm">
                    {p.active ? "Stop using" : "Use again"}
                  </SubmitButton>
                </ActionForm>
              </li>
            ))}
          </ul>
        )}
        {item.active ? (
          <ActionForm
            action={addPack.bind(null, item.id, item.unit)}
            label="Add a pack"
            className="grid gap-3 sm:grid-cols-[2fr_1fr_auto] sm:items-end"
          >
            <Field label="Pack">
              <Input name="name" placeholder="Sack" required maxLength={40} />
            </Field>
            <Field label={`Holds (${item.unit === "pc" ? "pieces" : item.unit})`} hint="like 25, or 500 g">
              <Input name="size" required inputMode="decimal" />
            </Field>
            <SubmitButton>Add pack</SubmitButton>
          </ActionForm>
        ) : (
          <p className="text-sm text-ink-faint">Switch the item on to add packs.</p>
        )}
      </Card>

      <h2 className="mb-3 text-sm font-semibold">History</h2>
      {moves.length === 0 ? (
        <EmptyState title="Nothing has moved yet">Receive a delivery to start.</EmptyState>
      ) : (
        <Table>
          <thead>
            <tr>
              <th className={th}>When</th>
              <th className={th}>What</th>
              <th className={`${th} text-right`}>Quantity</th>
              <th className={`${th} text-right`}>Value</th>
              <th className={`${th} text-right`}>On hand after</th>
            </tr>
          </thead>
          <tbody>
            {moves.map((m, i) => (
              <tr key={i}>
                <td className={td}>
                  {dateTimeIST(m.at)}
                  <div className="text-xs text-ink-faint">day {m.business_date}</div>
                </td>
                <td className={td}>
                  <Badge tone={m.kind === "purchase" ? "good" : m.kind === "sale" ? "info" : "warn"}>{stockKindLabel[m.kind] ?? m.kind}</Badge>
                  {(m.note || m.invoice_no || m.pack) && (
                    <div className="mt-0.5 text-xs text-ink-soft">
                      {[m.note, m.invoice_no, m.pack && `${m.packs} × ${m.pack}`].filter(Boolean).join(" · ")}
                    </div>
                  )}
                </td>
                <td className={`${td} text-right`}>{m.kind === "trueup" ? "—" : (m.qty > 0 ? "+" : "") + formatQty(m.qty, item.unit)}</td>
                <td className={`${td} text-right`}>{formatPaise(m.value)}</td>
                <td className={`${td} text-right`}>{formatQty(m.qty_after, item.unit)}</td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </>
  );
}
