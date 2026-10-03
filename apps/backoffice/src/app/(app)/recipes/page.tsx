import Link from "next/link";
import type { Metadata } from "next";
import { ActionForm, SubmitButton } from "@/components/forms";
import { Badge, EmptyState, Input, PageHeader, Select, Stat } from "@/components/ui";
import { api } from "@/lib/api";
import { getContext } from "@/lib/context";
import { formatPaise, formatRate } from "@/lib/money";
import { formatQty, qtyToFriendlyInput } from "@/lib/quantity";
import type { Dish, StockList } from "@/lib/types";
import { saveRecipe } from "../stock/actions";

export const metadata: Metadata = { title: "Recipes" };

// A dish whose ingredients cost more than this share of its price is flagged.
const HIGH_BP = 3500;
const BLANK_ROWS = 3;
const MAX_ROWS = 50; // the API takes at most 50 ingredients

export default async function RecipesPage() {
  const { outlet } = await getContext();
  const [{ dishes }, { items }] = await Promise.all([
    api<{ dishes: Dish[] }>(`/v1/outlets/${outlet.id}/stock/recipes`),
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
                <ActionForm action={saveRecipe.bind(null, d.id)} label={`Recipe for ${d.name}`} resetOnSuccess={false}>
                  <div className="space-y-2">
                    {[...d.lines, ...Array.from({ length: Math.max(0, Math.min(BLANK_ROWS, MAX_ROWS - d.lines.length)) }, () => null)].map((l, i) => (
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
                    <span className="text-xs text-ink-faint">
                      Price without GST {formatPaise(d.net_price)} · margin {formatPaise(d.margin)}. Empty all rows to remove the recipe.
                    </span>
                  </div>
                </ActionForm>
              </div>
            </details>
          ))}
        </div>
      )}
    </>
  );
}
