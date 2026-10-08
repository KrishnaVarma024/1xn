import Link from "next/link";
import { getContext } from "@/lib/context";
import type { Permission } from "@/lib/types";

const tabs: { href: string; label: string; perm: Permission }[] = [
  { href: "/stock", label: "Items", perm: "stock" },
  { href: "/stock/receive", label: "Receive goods", perm: "stock.receive" },
  { href: "/stock/suppliers", label: "Suppliers", perm: "suppliers" },
  { href: "/stock/waste", label: "Wastage", perm: "stock.receive" },
  { href: "/stock/count", label: "Count", perm: "stock.manage" },
  { href: "/stock/food-cost", label: "Food cost", perm: "costs" },
];

/** The stock section's tabs a person's role can use. Each page says which one it is. */
export async function StockTabs({ active }: { active: string }) {
  const { me } = await getContext();
  return (
    // The grey baseline is an inset shadow, not a border: the active tab's
    // underline covers it without poking out of the box. A tab pulled down
    // over a border (-mb-px) overflowed by one pixel, and Safari drew a
    // scrollbar for it.
    <nav aria-label="Stock" className="mb-6 flex gap-1 overflow-x-auto shadow-[inset_0_-1px_0_var(--color-line)] print:hidden">
      {tabs.filter((t) => me.permissions.includes(t.perm)).map((t) => (
        <Link
          key={t.href}
          href={t.href}
          aria-current={t.href === active ? "page" : undefined}
          className={`shrink-0 border-b-2 px-3 py-2 text-sm font-medium ${
            t.href === active ? "border-neel-700 text-neel-700" : "border-transparent text-ink-soft hover:text-neel-700"
          }`}
        >
          {t.label}
        </Link>
      ))}
    </nav>
  );
}
