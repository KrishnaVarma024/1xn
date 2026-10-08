"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import type { Permission } from "@/lib/types";

// Each link shows only to people whose role has its permission (the API
// refuses the rest anyway).
const links: { href: string; label: string; icon: string; perm: Permission }[] = [
  { href: "/floor", label: "Live floor", perm: "floor", icon: "M4 5h16v14H4zM4 10h16M10 5v14" },
  { href: "/menu", label: "Menu", perm: "menu", icon: "M5 4h14v16H5zM8 8h8M8 12h8M8 16h5" },
  { href: "/tables", label: "Tables", perm: "menu", icon: "M4 9h16M6 9v9M18 9v9M8 5h8" },
  { href: "/devices", label: "Devices", perm: "devices", icon: "M7 3h10v18H7zM11 18h2" },
  { href: "/stock", label: "Stock", perm: "stock", icon: "M4 7l8-4 8 4v10l-8 4-8-4zM4 7l8 4 8-4M12 11v10" },
  { href: "/recipes", label: "Recipes", perm: "costs", icon: "M7 3v8a3 3 0 006 0V3M10 3v18M17 3c-1.7 0-3 2-3 5s1.3 4 3 4v9" },
  { href: "/day", label: "Day close", perm: "day", icon: "M12 3v2M12 19v2M5 12H3M21 12h-2M7 7l-1.5-1.5M18.5 18.5 17 17M7 17l-1.5 1.5M18.5 5.5 17 7M12 8a4 4 0 100 8 4 4 0 000-8z" },
  { href: "/bills", label: "Bills", perm: "bills", icon: "M6 3h12v18l-3-2-3 2-3-2-3 2zM9 8h6M9 12h6" },
  { href: "/alerts", label: "Alerts", perm: "alerts", icon: "M12 4l9 16H3zM12 10v4M12 17h.01" },
  { href: "/books", label: "Books", perm: "books", icon: "M5 4h6a2 2 0 012 2v14a2 2 0 00-2-2H5zM19 4h-6a2 2 0 00-2 2v14a2 2 0 012-2h6z" },
  { href: "/staff", label: "Staff", perm: "staff", icon: "M16 8a4 4 0 11-8 0 4 4 0 018 0zM4 21a8 8 0 0116 0" },
];

export function Nav({ perms }: { perms: Permission[] }) {
  const path = usePathname();
  return (
    // A sideways-scrolling row on phones; on wider screens a plain column, with
    // no scroll box (Safari left slivers of the old highlight in it).
    <nav aria-label="Main" className="flex gap-1 overflow-x-auto md:flex-col md:overflow-visible">
      {links.filter((l) => perms.includes(l.perm)).map((l) => {
        const active = path === l.href || path.startsWith(l.href + "/") || (l.href === "/floor" && path.startsWith("/orders"));
        return (
          <Link
            key={l.href}
            href={l.href}
            aria-current={active ? "page" : undefined}
            className={`flex shrink-0 items-center gap-2.5 rounded-lg px-3 py-2 text-sm font-medium transition-colors ${
              active ? "bg-neel-700 text-white" : "text-ink-soft hover:bg-neel-50 hover:text-neel-700"
            }`}
          >
            <svg aria-hidden viewBox="0 0 24 24" className="h-4 w-4 fill-none stroke-current" strokeWidth={1.8} strokeLinecap="round" strokeLinejoin="round">
              <path d={l.icon} />
            </svg>
            {l.label}
          </Link>
        );
      })}
    </nav>
  );
}
