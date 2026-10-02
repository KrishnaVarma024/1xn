// Words people see for the codes the API uses.

export const channelLabel: Record<string, string> = {
  dine_in: "Dine-in",
  takeaway: "Takeaway",
  delivery: "Delivery",
  swiggy: "Swiggy",
  zomato: "Zomato",
};

export const roleLabel: Record<string, string> = {
  pos: "Till (POS)",
  captain: "Captain phone",
  kds: "Kitchen screen (KDS)",
  hub: "Outlet hub",
};

export const methodLabel: Record<string, string> = {
  cash: "Cash",
  upi: "UPI",
  card: "Card",
  credit: "On credit",
};

// What each alert means and what to do about it, in one line each.
export const alertText: Record<string, { title: string; help: string }> = {
  price_mismatch: {
    title: "A till priced a bill differently from the server",
    help: "The printed bill was kept. Check the device's menu version and its tax settings.",
  },
  payment_difference: {
    title: "Payments did not add up to the bill",
    help: "The difference sits in the suspense account until someone explains it.",
  },
  tax_rate_differs: {
    title: "A bill used a tax rate that is not on the current menu",
    help: "Often a till billing from an older menu. Make sure it syncs; if not, check for tampering.",
  },
  billing_settings_differ: {
    title: "A till used different billing settings from the outlet's",
    help: "Price mode or rounding on the device differs. Re-sync the till's menu.",
  },
  device_seq_gap: {
    title: "A device skipped operation numbers",
    help: "The device may have lost data before syncing. Check it.",
  },
  device_seq_regression: {
    title: "A device's operation numbers went backwards",
    help: "Usually a reinstall or a restored backup. Check the device.",
  },
  op_rejected: {
    title: "A device sent something that could not be applied",
    help: "The original was kept. See the details for the reason.",
  },
  period_locked: {
    title: "A bill arrived for a closed accounting period",
    help: "It is stored but not posted. An accountant needs to post it.",
  },
  late_items: {
    title: "Items were added after the table paid",
    help: "They went onto a new order on the same table so they can still be billed.",
  },
  void_after_kot_without_approval: {
    title: "Food already sent to the kitchen was removed without a manager",
    help: "Check with the staff member. This is the classic till fraud pattern.",
  },
  bill_order_mismatch: {
    title: "A bill does not match its table order",
    help: "The bill was kept as printed. Compare it with the order.",
  },
  inactive_table: {
    title: "Guests were seated at a table you switched off",
    help: "The order is kept. Switch the table back on, or tell the staff.",
  },
};

export const kotStatusLabel: Record<string, string> = {
  new: "New",
  preparing: "Preparing",
  ready: "Ready",
  served: "Served",
};

export function timeIST(iso: string): string {
  return new Date(iso).toLocaleTimeString("en-IN", { timeZone: "Asia/Kolkata", hour: "numeric", minute: "2-digit" });
}

export function dateTimeIST(iso: string): string {
  return new Date(iso).toLocaleString("en-IN", {
    timeZone: "Asia/Kolkata",
    day: "numeric",
    month: "short",
    hour: "numeric",
    minute: "2-digit",
  });
}

/** "12 min", "1 h 05 min" since an ISO time. */
export function since(iso: string, now = Date.now()): string {
  const min = Math.max(0, Math.floor((now - new Date(iso).getTime()) / 60000));
  if (min < 60) return `${min} min`;
  return `${Math.floor(min / 60)} h ${String(min % 60).padStart(2, "0")} min`;
}

export function todayIST(): string {
  return new Date().toLocaleDateString("en-CA", { timeZone: "Asia/Kolkata" }); // YYYY-MM-DD
}
