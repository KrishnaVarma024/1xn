import Link from "next/link";

const tabs = [
  { href: "/stock", label: "Items" },
  { href: "/stock/receive", label: "Receive goods" },
  { href: "/stock/waste", label: "Wastage" },
  { href: "/stock/count", label: "Count" },
  { href: "/stock/food-cost", label: "Food cost" },
];

/** The stock section's tabs. Each page says which one it is. */
export function StockTabs({ active }: { active: string }) {
  return (
    <nav aria-label="Stock" className="mb-6 flex gap-1 overflow-x-auto border-b border-line print:hidden">
      {tabs.map((t) => (
        <Link
          key={t.href}
          href={t.href}
          aria-current={t.href === active ? "page" : undefined}
          className={`-mb-px shrink-0 border-b-2 px-3 py-2 text-sm font-medium ${
            t.href === active ? "border-neel-700 text-neel-700" : "border-transparent text-ink-soft hover:text-neel-700"
          }`}
        >
          {t.label}
        </Link>
      ))}
    </nav>
  );
}
