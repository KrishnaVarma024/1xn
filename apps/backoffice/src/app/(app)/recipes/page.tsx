import Link from "next/link";
import type { Metadata } from "next";
import type { ReactNode } from "react";
import { ActionForm, SubmitButton } from "@/components/forms";
import { Badge, EmptyState, Input, PageHeader, Select, Stat } from "@/components/ui";
import { api } from "@/lib/api";
import { getContext } from "@/lib/context";
import { formatPaise, formatRate } from "@/lib/money";
import { formatQty, qtyToFriendlyInput } from "@/lib/quantity";
import type { Addon, Dish, RecipeLine, StockItem, StockList } from "@/lib/types";
import { saveAddonRecipe, saveRecipe } from "../stock/actions";
import { NoAccess } from "@/components/no-access";
import { can } from "@/lib/access";

export const metadata: Metadata = { title: "Recipes" };

// A dish whose ingredients cost more than this share of its price is flagged.
const HIGH_BP = 3500;
const BLANK_ROWS = 3;
const MAX_ROWS = 50; // the API takes at most 50 ingredients

export default async function RecipesPage() {
  if (!(await can("costs"))) return <NoAccess page="Recipes" />;
  const { outlet } = await getContext();
  const [{ dishes, addons }, { items }] = await Promise.all([
    api<{ dishes: Dish[]; addons: Addon[] }>(`/v1/outlets/${outlet.id}/stock/recipes`),
    api<StockList>(`/v1/outlets/${outlet.id}/stock`),
  ]);
  const withRecipe = dishes.filter((d) => d.lines.length > 0);
  const usable = items.filter((i) => i.active);
  const high = withRecipe.filter((d) => d.food_cost_bp > HIGH_BP);

  return (
    <>
      <PageHeader
        title="Recipes"
        subtitle={`What one portion of each dish uses, and what it costs at ${outlet.name} today. Bills use up these ingredients as they sync.`}
      />
      <div className="mb-6 grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Stat label="Dishes with a recipe" value={`${withRecipe.length} of ${dishes.length}`} />
        <Stat label="Costly dishes" value={high.length} hint={`ingredients over ${formatRate(HIGH_BP)} of the price`} />
        <Stat
          label="Add-ons with a recipe"
          value={`${addons.filter((a) => a.lines.length > 0).length} of ${addons.length}`}
          hint="choices like a spice level rarely need one"
        />
      </div>
      {usable.length === 0 && (
        <p className="mb-4 text-sm text-ink-soft">
          <Link href="/stock" className="font-medium text-neel-700 hover:underline">Add stock items</Link> first: a recipe is made of them.
        </p>
      )}
      {dishes.length === 0 ? (
        <EmptyState title="No dishes yet">Build the menu first.</EmptyState>
      ) : (
        <div className="space-y-2">
          {dishes.map((d) => (
            <details key={d.id} className="group rounded-xl border border-line bg-white" data-testid={`dish-${d.name}`}>
              <summary className="flex cursor-pointer list-none flex-wrap items-center justify-between gap-3 px-4 py-3">
                <span>
                  <span className="font-medium">{d.name}</span> <span className="text-xs text-ink-faint">{d.category}</span>
                </span>
                <span className="flex flex-wrap items-center gap-3 text-sm tabular-nums">
                  {d.lines.length === 0 ? (
                    <Badge>No recipe</Badge>
                  ) : (
                    <>
                      <span className="text-ink-soft">
                        costs {formatPaise(d.cost)} of {formatPaise(d.net_price)}
                      </span>
                      <Badge tone={d.food_cost_bp > HIGH_BP ? "bad" : "good"}>{formatRate(d.food_cost_bp)}</Badge>
                      {d.uncosted > 0 && <Badge tone="warn">{d.uncosted} not costed yet</Badge>}
                    </>
                  )}
                </span>
              </summary>
              <div className="border-t border-line px-4 py-3">
                <RecipeForm action={saveRecipe.bind(null, d.id)} label={`Recipe for ${d.name}`} lines={d.lines} items={items}>
                  Price without GST {formatPaise(d.net_price)} · margin {formatPaise(d.margin)}. Empty all rows to remove the recipe.
                </RecipeForm>
              </div>
            </details>
          ))}
        </div>
      )}

      <h2 className="mt-10 mb-1 text-sm font-semibold">Add-ons</h2>
      <p className="mb-3 text-sm text-ink-soft">
        What one portion of an add-on uses, on top of the dish: &ldquo;Extra cheese&rdquo; on a pizza uses 30 g of cheese. A bill line of two
        pizzas with extra cheese uses it twice.
      </p>
      {addons.length === 0 ? (
        <EmptyState title="No add-ons on the menu">Add-ons are the options of a dish&apos;s choice groups, like Extra cheese.</EmptyState>
      ) : (
        <div className="space-y-2">
          {addons.map((a) => (
            <details key={a.id} className="group rounded-xl border border-line bg-white" data-testid={`addon-${a.name}`}>
              <summary className="flex cursor-pointer list-none flex-wrap items-center justify-between gap-3 px-4 py-3">
                <span>
                  <span className="font-medium">{a.name}</span> <span className="text-xs text-ink-faint">{a.group}</span>
                </span>
                <span className="flex flex-wrap items-center gap-3 text-sm tabular-nums">
                  {a.lines.length === 0 ? (
                    <Badge>No recipe</Badge>
                  ) : (
                    <>
                      <span className="text-ink-soft">
                        costs {formatPaise(a.cost)}
                        {a.net_price > 0 && ` of ${formatPaise(a.net_price)}`}
                      </span>
                      {a.net_price > 0 && <Badge tone={a.food_cost_bp > HIGH_BP ? "bad" : "good"}>{formatRate(a.food_cost_bp)}</Badge>}
                      {a.uncosted > 0 && <Badge tone="warn">{a.uncosted} not costed yet</Badge>}
                    </>
                  )}
                </span>
              </summary>
              <div className="border-t border-line px-4 py-3">
                <RecipeForm action={saveAddonRecipe.bind(null, a.id)} label={`Recipe for ${a.name}`} lines={a.lines} items={items}>
                  {a.price > 0
                    ? a.net_price > 0
                      ? `Adds ${formatPaise(a.price)} to the bill (${formatPaise(a.net_price)} without GST).`
                      : `Adds ${formatPaise(a.price)} to the bill; the dishes offering it have different GST rates.`
                    : "Free on the menu."}{" "}
                  Empty all rows to remove the recipe.
                </RecipeForm>
              </div>
            </details>
          ))}
        </div>
      )}
    </>
  );
}

/** The ingredient rows of a dish's or an add-on's recipe, and three blank ones. */
function RecipeForm({
  action,
  label,
  lines,
  items,
  children,
}: {
  action: Parameters<typeof ActionForm>[0]["action"];
  label: string;
  lines: RecipeLine[];
  items: StockItem[];
  children: ReactNode;
}) {
  const rows = [...lines, ...Array.from({ length: Math.max(0, Math.min(BLANK_ROWS, MAX_ROWS - lines.length)) }, () => null)];
  return (
    <ActionForm action={action} label={label} resetOnSuccess={false}>
      <div className="space-y-2">
        {rows.map((l, i) => (
          <div key={i} className="grid grid-cols-[2fr_1fr] gap-2 sm:grid-cols-[2fr_1fr_1fr]">
            <Select name={`ing${i}`} defaultValue={l?.stock_item_id ?? ""} aria-label={`Ingredient ${i + 1}`}>
              <option value="">—</option>
              {items.map((it) => (
                <option key={it.id} value={it.id}>
                  {it.name} ({it.unit === "pc" ? "pieces" : it.unit})
                </option>
              ))}
            </Select>
            <Input
              name={`qty${i}`}
              defaultValue={l ? qtyToFriendlyInput(l.qty, l.unit) : ""}
              aria-label={`Quantity ${i + 1}`}
              placeholder="per portion, like 150 g"
              inputMode="decimal"
            />
            <span className="hidden self-center text-sm text-ink-soft tabular-nums sm:block">
              {l && `${formatQty(l.qty, l.unit)} · ${l.unit_cost ? formatPaise(l.cost) : "no cost yet"}`}
            </span>
          </div>
        ))}
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-3">
        <SubmitButton size="sm">Save recipe</SubmitButton>
        <span className="text-xs text-ink-faint">{children}</span>
      </div>
    </ActionForm>
  );
}
