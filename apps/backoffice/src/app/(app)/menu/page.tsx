import type { Metadata } from "next";
import { ActionForm, SubmitButton } from "@/components/forms";
import { Badge, Card, EmptyState, Field, Input, PageHeader, Textarea } from "@/components/ui";
import { api } from "@/lib/api";
import { getContext } from "@/lib/context";
import { formatPrice, formatRate } from "@/lib/money";
import type { Item, Menu } from "@/lib/types";
import { addCategory, addItem, addModifierGroup, addSection, addTaxProfile, setAvailable, updateItem } from "./actions";
import { ItemFields } from "./item-fields";

export const metadata: Metadata = { title: "Menu" };

export default async function MenuPage() {
  const { outlet } = await getContext();
  const menu = await api<Menu>(`/v1/outlets/${outlet.id}/menu`);
  const ready = menu.categories.length > 0 && menu.tax_profiles.length > 0;
  const soldOut = menu.items.filter((i) => !i.available).length;

  return (
    <>
      <PageHeader
        title="Menu"
        subtitle={`${menu.items.length} dishes${soldOut ? ` · ${soldOut} sold out` : ""} · version ${menu.version} (tills check it every few seconds)`}
      />
      <div className="grid gap-6 xl:grid-cols-3">
        <div className="space-y-6 xl:col-span-2">
          {menu.items.length === 0 && (
            <EmptyState title="No dishes yet">
              {ready ? "Add your first dish below." : "Start on the right: add a GST rate and a category, then your dishes."}
            </EmptyState>
          )}
          {menu.categories.map((c) => {
            const items = menu.items.filter((i) => i.category_id === c.id);
            if (items.length === 0) return null;
            return (
              <Card key={c.id} title={c.name} actions={<span className="text-xs text-ink-faint">{items.length}</span>}>
                <ul className="-my-2 divide-y divide-line/70">
                  {items.map((item) => (
                    <ItemRow key={item.id} item={item} menu={menu} />
                  ))}
                </ul>
              </Card>
            );
          })}
          {ready && (
            <Card title="Add a dish">
              <ActionForm action={addItem} className="space-y-3" label="Add a dish">
                <ItemFields menu={menu} />
                <SubmitButton>Add dish</SubmitButton>
              </ActionForm>
            </Card>
          )}
        </div>

        <div className="space-y-6">
          <Card title="GST rates">
            <ul className="mb-3 space-y-1 text-sm">
              {menu.tax_profiles.map((t) => (
                <li key={t.id} className="flex justify-between">
                  <span>{t.name}</span>
                  <span className="text-ink-soft">
                    {formatRate(t.rate_bp)}
                    {t.hsn_sac && ` · ${t.hsn_sac}`}
                  </span>
                </li>
              ))}
            </ul>
            <ActionForm action={addTaxProfile} className="space-y-2" label="Add a GST rate">
              <Input name="name" required placeholder="Restaurant service 5%" aria-label="Name" />
              <div className="flex gap-2">
                <Input name="rate" required placeholder="5" aria-label="Rate (%)" inputMode="decimal" />
                <Input name="hsn_sac" placeholder="996331" aria-label="HSN/SAC" />
              </div>
              <SubmitButton size="sm" variant="secondary">
                Add GST rate
              </SubmitButton>
            </ActionForm>
            <p className="mt-2 text-xs text-ink-faint">Most restaurants: 5% with SAC 996331.</p>
          </Card>

          <Card title="Categories">
            <ul className="mb-3 flex flex-wrap gap-1.5">
              {menu.categories.map((c) => (
                <li key={c.id}>
                  <Badge>{c.name}</Badge>
                </li>
              ))}
            </ul>
            <ActionForm action={addCategory} className="flex gap-2" label="Add a category">
              <Input name="name" required placeholder="Starters" aria-label="Category name" />
              <SubmitButton size="sm" variant="secondary">
                Add
              </SubmitButton>
            </ActionForm>
          </Card>

          <Card title="Kitchen sections">
            <ul className="mb-3 flex flex-wrap gap-1.5">
              {menu.sections.map((s) => (
                <li key={s.id}>
                  <Badge tone="info">{s.name}</Badge>
                </li>
              ))}
            </ul>
            <ActionForm action={addSection} className="flex gap-2" label="Add a kitchen section">
              <Input name="name" required placeholder="Tandoor" aria-label="Section name" />
              <SubmitButton size="sm" variant="secondary">
                Add
              </SubmitButton>
            </ActionForm>
            <p className="mt-2 text-xs text-ink-faint">Each section gets its own kitchen ticket (KOT) and screen.</p>
          </Card>

          <Card title="Add-on groups">
            <ul className="mb-3 space-y-2 text-sm">
              {menu.modifier_groups.map((g) => (
                <li key={g.id}>
                  <div className="font-medium">
                    {g.name}{" "}
                    <span className="text-xs font-normal text-ink-faint">
                      pick {g.min_select === g.max_select ? g.min_select : `${g.min_select}–${g.max_select}`}
                    </span>
                  </div>
                  <div className="text-xs text-ink-soft">
                    {g.options.map((o) => `${o.name}${o.price_paise ? ` +${formatPrice(o.price_paise)}` : ""}`).join(" · ")}
                  </div>
                </li>
              ))}
            </ul>
            <ActionForm action={addModifierGroup} className="space-y-2" label="Add an add-on group">
              <Input name="name" required placeholder="Spice level" aria-label="Group name" />
              <div className="grid grid-cols-2 gap-2">
                <Field label="Pick at least">
                  <Input name="min" type="number" min={0} max={20} defaultValue={0} />
                </Field>
                <Field label="Pick at most">
                  <Input name="max" type="number" min={1} max={20} defaultValue={1} />
                </Field>
              </div>
              <Field label="Options" hint="One per line: name, price in ₹ (price optional).">
                <Textarea name="options" rows={3} required placeholder={"Mild\nMedium\nExtra cheese, 40"} />
              </Field>
              <SubmitButton size="sm" variant="secondary">
                Add group
              </SubmitButton>
            </ActionForm>
          </Card>
        </div>
      </div>
    </>
  );
}

function ItemRow({ item, menu }: { item: Item; menu: Menu }) {
  const section = menu.sections.find((s) => s.id === item.section_id);
  const groups = menu.modifier_groups.filter((g) => item.modifier_group_ids?.includes(g.id));
  const toggle = setAvailable.bind(null, item.id, !item.available);
  return (
    <li className="py-3" data-testid={`item-${item.name}`}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <div className={`font-medium ${item.available ? "" : "text-ink-faint line-through"}`}>{item.name}</div>
          <div className="mt-1 flex flex-wrap gap-1">
            {section && <Badge tone="info">{section.name}</Badge>}
            {groups.map((g) => (
              <Badge key={g.id}>{g.name}</Badge>
            ))}
            <Badge>GST {formatRate(item.tax_rate_bp)}</Badge>
            {!item.available && <Badge tone="bad">Sold out</Badge>}
          </div>
        </div>
        <div className="flex items-center gap-3">
          <span className="font-semibold">{formatPrice(item.price_paise)}</span>
          <form action={toggle}>
            <button
              className={`rounded-lg px-2.5 py-1 text-xs font-medium ring-1 ring-inset ${
                item.available ? "text-ink-soft ring-line hover:bg-chilli-50 hover:text-chilli-700" : "bg-curry-50 text-curry-700 ring-curry-700/20"
              }`}
            >
              {item.available ? "Mark sold out" : "Back on menu"}
            </button>
          </form>
        </div>
      </div>
      <details className="mt-2">
        <summary className="cursor-pointer text-xs font-medium text-neel-700">Edit</summary>
        <ActionForm action={updateItem.bind(null, item.id)} className="mt-3 space-y-3 rounded-lg bg-chalk p-3" resetOnSuccess={false} label={`Edit ${item.name}`}>
          <ItemFields menu={menu} item={item} />
          <SubmitButton size="sm">Save changes</SubmitButton>
        </ActionForm>
      </details>
    </li>
  );
}
