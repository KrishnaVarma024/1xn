import { Field, Input, Select } from "@/components/ui";
import { paiseToInput } from "@/lib/money";
import type { Item, Menu } from "@/lib/types";

// The fields shared by "add item" and "edit item".
export function ItemFields({ menu, item }: { menu: Menu; item?: Item }) {
  // The till asks for add-ons in the item's order, and the form sends the
  // ticked boxes in screen order: so list the item's own groups first, in
  // their order, then the rest. Saving a price then cannot reorder them.
  const attached = (item?.modifier_group_ids ?? []).map((id) => menu.modifier_groups.find((g) => g.id === id)).filter((g) => g !== undefined);
  const groups = [...attached, ...menu.modifier_groups.filter((g) => !attached.includes(g))];
  return (
    <div className="grid gap-3 sm:grid-cols-2">
      <Field label="Dish name" className="sm:col-span-2">
        <Input name="name" required maxLength={200} defaultValue={item?.name} placeholder="Paneer tikka" />
      </Field>
      <Field label="Price (₹)" hint={menu.outlet.price_mode === "inclusive" ? "Including GST" : "Before GST"}>
        <Input name="price" required inputMode="decimal" defaultValue={item ? paiseToInput(item.price_paise) : ""} placeholder="320" />
      </Field>
      <Field label="Category">
        <Select name="category_id" required defaultValue={item?.category_id}>
          {menu.categories.map((c) => (
            <option key={c.id} value={c.id}>
              {c.name}
            </option>
          ))}
        </Select>
      </Field>
      <Field label="GST">
        <Select name="tax_profile_id" required defaultValue={item?.tax_profile_id}>
          {menu.tax_profiles.map((t) => (
            <option key={t.id} value={t.id}>
              {t.name}
            </option>
          ))}
        </Select>
      </Field>
      <Field label="Cooked in" hint="Where its kitchen ticket prints.">
        <Select name="section_id" defaultValue={item?.section_id ?? ""}>
          <option value="">No ticket (e.g. packaged drinks)</option>
          {menu.sections.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
            </option>
          ))}
        </Select>
      </Field>
      {menu.modifier_groups.length > 0 && (
        <fieldset className="sm:col-span-2">
          <legend className="mb-1 text-xs font-medium text-ink-soft">Add-ons offered</legend>
          <div className="flex flex-wrap gap-x-4 gap-y-1">
            {groups.map((g) => (
              <label key={g.id} className="flex items-center gap-1.5 text-sm">
                <input type="checkbox" name="group" value={g.id} defaultChecked={item?.modifier_group_ids?.includes(g.id)} />
                {g.name}
              </label>
            ))}
          </div>
        </fieldset>
      )}
    </div>
  );
}
