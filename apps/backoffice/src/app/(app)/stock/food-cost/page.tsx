import Link from "next/link";
import type { Metadata } from "next";
import { Badge, EmptyState, Input, PageHeader, Stat, Table, buttonClass, td, th } from "@/components/ui";
import { api } from "@/lib/api";
import { getContext } from "@/lib/context";
import { isRealDay, longDate, shiftDay } from "@/lib/dates";
import { tradingDayIST } from "@/lib/labels";
import { formatPaise, formatRate } from "@/lib/money";
import type { FoodCost } from "@/lib/types";
import { StockTabs } from "../tabs";
import { NoAccess } from "@/components/no-access";
import { can } from "@/lib/access";

export const metadata: Metadata = { title: "Food cost" };

export default async function FoodCostPage({ searchParams }: PageProps<"/stock/food-cost">) {
  if (!(await can("costs"))) return <NoAccess page="Food cost" />;
  const q = await searchParams;
  const today = tradingDayIST();
  const to = isRealDay(q.to) && q.to <= today ? q.to : today;
  const from = isRealDay(q.from) && q.from <= to && q.from >= shiftDay(to, -92) ? q.from : to;
  const { outlet } = await getContext();
  const r = await api<FoodCost>(`/v1/outlets/${outlet.id}/stock/food-cost?from=${from}&to=${to}`);

  return (
    <>
      <PageHeader
        title="Stock"
        subtitle={from === to ? longDate(from) : `${longDate(from)} to ${longDate(to)}`}
        actions={
          <form className="flex flex-wrap items-center gap-2 print:hidden" aria-label="Pick days">
            <Input type="date" name="from" defaultValue={from} max={today} aria-label="From" className="w-auto" />
            <span className="text-sm text-ink-soft">to</span>
            <Input type="date" name="to" defaultValue={to} max={today} aria-label="To" className="w-auto" />
            <button className={buttonClass("secondary")}>Show</button>
          </form>
        }
      />
      <StockTabs active="/stock/food-cost" />
      <div className="mb-3 flex flex-wrap gap-2 text-sm print:hidden">
        <Link className={buttonClass("ghost", "sm")} href={`/stock/food-cost?from=${today}&to=${today}`}>Today</Link>
        <Link className={buttonClass("ghost", "sm")} href={`/stock/food-cost?from=${shiftDay(today, -6)}&to=${today}`}>Last 7 days</Link>
        <Link className={buttonClass("ghost", "sm")} href={`/stock/food-cost?from=${shiftDay(today, -29)}&to=${today}`}>Last 30 days</Link>
      </div>
      <div className="mb-6 grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Stat label="Food sales" value={formatPaise(r.food_sales)} hint="after discounts, without GST" />
        <Stat
          label="Food cost"
          value={r.food_sales ? formatRate(r.food_cost_bp) : "—"}
          hint={`${formatPaise(r.food_cost)} of ingredients used by bills`}
        />
        <Stat
          label="All kitchen cost"
          value={r.food_sales ? formatRate(r.total_cost_bp) : "—"}
          hint={`with wastage ${formatPaise(r.wastage)} and count differences ${formatPaise(r.count_differences)}`}
        />
        <Stat label="Bought" value={formatPaise(r.purchases)} hint="deliveries booked in these days" />
      </div>
      {r.portions_without_recipe > 0 && (
        <p className="mb-4 rounded-xl border border-marigold-400/40 bg-marigold-50 px-4 py-3 text-sm">
          {r.portions_without_recipe} {r.portions_without_recipe === 1 ? "portion was" : "portions were"} sold without a recipe, so their ingredients
          are not in the food cost. <Link href="/recipes" className="font-medium text-neel-700 hover:underline">Add recipes</Link>.
        </p>
      )}
      {r.dishes.length === 0 ? (
        <EmptyState title="Nothing sold on these days" />
      ) : (
        <Table>
          <thead>
            <tr>
              <th className={th}>Dish</th>
              <th className={`${th} text-right`}>Sold</th>
              <th className={`${th} text-right`}>Ingredients, at today&apos;s recipe cost</th>
            </tr>
          </thead>
          <tbody>
            {r.dishes.map((d) => (
              <tr key={d.id}>
                <td className={td}>
                  {d.name} {!d.has_recipe && <Badge tone="warn">No recipe</Badge>}
                </td>
                <td className={`${td} text-right`}>{d.qty}</td>
                <td className={`${td} text-right`}>{d.has_recipe ? formatPaise(d.cost) : "—"}</td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
    </>
  );
}
