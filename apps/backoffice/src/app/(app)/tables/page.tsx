import type { Metadata } from "next";
import { ActionForm, SubmitButton } from "@/components/forms";
import { Badge, Card, EmptyState, Input, PageHeader } from "@/components/ui";
import { api } from "@/lib/api";
import { getContext } from "@/lib/context";
import type { Menu } from "@/lib/types";
import { addFloor, addTable, setTableActive } from "./actions";

export const metadata: Metadata = { title: "Tables" };

export default async function TablesPage() {
  const { outlet } = await getContext();
  const menu = await api<Menu>(`/v1/outlets/${outlet.id}/menu`);
  return (
    <>
      <PageHeader title="Tables" subtitle={`Floors and tables of ${outlet.name}. Captains and tills see them on their next menu check.`} />
      {menu.floors.length === 0 && (
        <div className="mb-6">
          <EmptyState title="No floors yet">Add a floor (a room or area, like “Main hall” or “Terrace”), then its tables.</EmptyState>
        </div>
      )}
      <div className="space-y-6">
        {menu.floors.map((f) => (
          <Card key={f.id} title={f.name} actions={<span className="text-xs text-ink-faint">{switchedOn(f.tables)}</span>}>
            <div className="mb-4 grid grid-cols-2 gap-2 sm:grid-cols-4 lg:grid-cols-6">
              {f.tables.map((t) => (
                <div
                  key={t.id}
                  data-testid={`table-${t.label}`}
                  className={`rounded-lg border p-2 ${t.active ? "border-line bg-white" : "border-dashed border-line bg-chalk"}`}
                >
                  <div className="flex items-baseline justify-between">
                    <span className="font-semibold">{t.label}</span>
                    <span className="text-xs text-ink-faint">{t.seats} seats</span>
                  </div>
                  <form action={setTableActive.bind(null, t.id, !t.active)} className="mt-1">
                    <button className="text-xs font-medium text-neel-700 hover:underline">{t.active ? "Switch off" : "Switch on"}</button>
                  </form>
                  {!t.active && <Badge tone="warn">Off</Badge>}
                </div>
              ))}
            </div>
            <ActionForm action={addTable.bind(null, f.id)} className="flex flex-wrap items-end gap-2" label={`Add a table to ${f.name}`}>
              <div className="w-28">
                <Input name="label" required maxLength={20} placeholder="T7" aria-label="Table label" />
              </div>
              <div className="w-24">
                <Input name="seats" type="number" min={1} max={50} defaultValue={4} aria-label="Seats" />
              </div>
              <SubmitButton size="sm" variant="secondary">
                Add table
              </SubmitButton>
            </ActionForm>
          </Card>
        ))}
        <Card title="Add a floor">
          <ActionForm action={addFloor} className="flex gap-2" label="Add a floor">
            <Input name="name" required maxLength={60} placeholder="Terrace" aria-label="Floor name" />
            <SubmitButton size="sm">Add floor</SubmitButton>
          </ActionForm>
        </Card>
      </div>
    </>
  );
}

// "6 tables", or "5 of 6 switched on". Not "in use": on the live floor that
// means tables with guests at them.
function switchedOn(tables: { active: boolean }[]): string {
  const on = tables.filter((t) => t.active).length;
  if (on === tables.length) return `${on} ${on === 1 ? "table" : "tables"}`;
  return `${on} of ${tables.length} switched on`;
}
