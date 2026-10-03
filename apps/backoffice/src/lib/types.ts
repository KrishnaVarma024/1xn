// Shapes of what the Go API returns. Money is always integer paise, rates are
// basis points (500 = 5%), times are ISO strings.

export type Outlet = {
  id: string;
  name: string;
  state_code: string;
  price_mode: "exclusive" | "inclusive";
  rounding: "nearest_rupee" | "none";
  legal_entity_id: string;
  legal_entity_name: string;
  gstin?: string;
};

export type Me = {
  tenant_id: string;
  business_name: string;
  user: { id: string; name: string; phone: string; role: string };
  outlets: Outlet[];
};

export type TaxProfile = { id: string; name: string; rate_bp: number; hsn_sac?: string };
export type Category = { id: string; name: string; sort: number };
export type Section = { id: string; name: string; sort: number };
export type ModifierOption = { id: string; name: string; price_paise: number; sort: number; available: boolean };
export type ModifierGroup = {
  id: string;
  name: string;
  min_select: number;
  max_select: number;
  options: ModifierOption[];
};
export type Table = { id: string; label: string; seats: number; sort: number; active: boolean };
export type Floor = { id: string; name: string; sort: number; tables: Table[] };
export type Item = {
  id: string;
  category_id: string;
  tax_profile_id: string;
  tax_rate_bp: number;
  name: string;
  price_paise: number;
  channel_prices?: Record<string, number>;
  available: boolean;
  section_id?: string;
  modifier_group_ids?: string[];
};

export type Menu = {
  version: number;
  outlet: Outlet;
  tax_profiles: TaxProfile[];
  categories: Category[];
  sections: Section[];
  floors: Floor[];
  modifier_groups: ModifierGroup[];
  items: Item[];
};

export type LineStatus = "held" | "fired" | "voided" | "billed";
export type KOTStatus = "new" | "preparing" | "ready" | "served";

export type OrderLine = {
  id: string;
  item_id?: string;
  name: string;
  qty: number;
  unit_price_paise: number;
  modifiers?: { option_id?: string; name: string; price_paise: number }[];
  tax_rate_bp: number;
  course: number;
  section_id?: string;
  note?: string;
  status: LineStatus;
  kot_id?: string;
  bill_id?: string;
  void?: { reason: string; approved_by?: string; at: string };
  added_at: string;
  from_order_id?: string;
};

export type KOT = {
  id: string;
  no: string;
  section_id?: string;
  line_ids: string[];
  status: KOTStatus;
  fired_at: string;
};

export type Order = {
  id: string;
  outlet_id: string;
  channel: string;
  table_id?: string;
  covers: number;
  status: "open" | "closed" | "merged" | "cancelled";
  merged_into?: string;
  spillover_of?: string;
  opened_at: string;
  closed_at?: string;
  lines: OrderLine[];
  kots: KOT[];
  version: number;
};

export type Device = {
  id: string;
  name: string;
  role: "pos" | "captain" | "kds" | "hub";
  invoice_prefix: string;
  last_device_seq: number;
  registered_at: string;
  revoked_at?: string;
};

export type BillRow = {
  id: string;
  invoice_no: string;
  issued_at: string;
  channel: string;
  table_label?: string;
  grand_total: number;
  tax_total: number;
  verification: "match" | "mismatch";
  ledger_status: "posted" | "blocked" | "none";
};

export type DayBills = {
  outlet_id: string;
  business_date: string;
  bills: BillRow[];
  count: number;
  grand_total: number;
  tax_total: number;
  by_method: Record<string, number>;
};

export type Bill = BillRow & {
  business_date: string;
  covers?: number;
  order_id?: string;
  subtotal: number;
  discount_total: number;
  service_charge: number;
  taxable_total: number;
  cgst: number;
  sgst: number;
  igst: number;
  round_off: number;
  tip: number;
  lines: { line_no: number; name: string; qty: number; gross: number; discount: number; net: number; tax_rate_bp: number }[];
  payments: { method: string; amount: number; reference?: string }[];
  journal_lines: { line_no: number; account_code: string; account_name: string; amount: number }[];
};

export type Alert = {
  id: string;
  kind: string;
  severity: "info" | "warning" | "critical";
  ref_id?: string;
  details: Record<string, unknown>;
  created_at: string;
};

export type Account = {
  legal_entity_id: string;
  code: string;
  name: string;
  kind: string;
  debit: number;
  credit: number;
  balance: number;
};

export type TrialBalance = { accounts: Account[]; total: number; balanced: boolean };

// Day close (Z report). See services/core/modules/dayclose.
export type Shift = {
  id: string;
  device_id: string;
  device_name: string;
  opened_by: string;
  opened_at: string;
  closed_by?: string;
  closed_at?: string;
  opening_float: number;
  cash_sales: number;
  expenses: number;
  suppliers?: number; // absent in reports closed before supplier payouts existed
  to_safe: number;
  from_safe: number;
  expected: number;
  counted?: number;
  difference?: number;
  ledger_status?: string;
};

export type ZReport = {
  outlet_id: string;
  outlet_name: string;
  business_date: string;
  from: string;
  to: string;
  generated_at: string;
  sales: {
    bills: number;
    covers: number;
    first_at?: string;
    last_at?: string;
    subtotal: number;
    discounts: number;
    service_charge: number;
    taxable: number;
    cgst: number;
    sgst: number;
    igst: number;
    tax: number;
    round_off: number;
    grand_total: number;
    tips: number;
    avg_bill: number;
    avg_cover: number;
  };
  tax_by_rate: { rate_bp: number; taxable: number; cgst: number; sgst: number; igst: number }[];
  payments: { method: string; bills: number; amount: number }[];
  channels: { channel: string; bills: number; grand_total: number }[];
  series: {
    device_id: string;
    device_name: string;
    fy: string;
    first: string;
    last: string;
    count: number;
    continues_from?: string;
    missing: string[];
    missing_count: number;
  }[];
  voids: {
    lines: number;
    value: number;
    fired: number;
    list: {
      order_id: string;
      table?: string;
      name: string;
      qty: number;
      value: number;
      reason: string;
      approved_by?: string;
      at: string;
      fired: boolean;
      cancelled: boolean;
    }[];
  };
  cancelled: { orders: number; value: number };
  open_orders: { orders: number; value: number; tables: string[] };
  shifts: Shift[];
  cash_moves: { shift_id: string; kind: "expense" | "supplier" | "to_safe" | "from_safe"; amount: number; reason: string; at: string }[];
  cash: {
    floats: number;
    sales: number;
    expenses: number;
    to_safe: number;
    from_safe: number;
    expected: number;
    counted: number;
    difference: number;
    open_shifts: number;
    outside_shifts: number;
  };
  top_items: { name: string; qty: number; net: number }[];
  checks: { price_mismatches: number; payment_differences: number; ledger_blocked: number };
  warnings: { code: string; level: "check" | "info"; text: string }[];
  digest: string;
};

export type Amendment = { what: string; before: number; after: number; money: boolean };

export type DayView = {
  business_date: string;
  today: string;
  report: ZReport;
  closed?: { closed_at: string; closed_by: string; report: ZReport };
  amended: boolean;
  amendments: Amendment[];
};

export type DayList = {
  today: string;
  closed: { business_date: string; closed_at: string; closed_by: string; bills: number; grand_total: number; shifts: number; cash_difference: number }[];
};

// Stock and recipes. Quantities are milli-units of the item's unit.
export type StockItem = {
  id: string;
  name: string;
  unit: "kg" | "l" | "pc";
  reorder_level: number;
  active: boolean;
  qty: number;
  value: number;
  unit_cost: number;
  last_cost: number;
  status: "ok" | "low" | "out" | "negative" | "new";
};

export type StockList = { items: StockItem[]; value: number };

export type StockMove = {
  doc_id: string;
  kind: "purchase" | "sale" | "wastage" | "count" | "trueup";
  qty: number;
  value: number;
  qty_after: number;
  business_date: string;
  at: string;
  note?: string;
  invoice_no?: string;
};

export type StockDoc = {
  id: string;
  kind: "purchase" | "sale" | "wastage" | "count";
  business_date: string;
  at: string;
  supplier?: string;
  invoice_no?: string;
  paid_by?: "safe" | "till" | "bank" | "credit";
  total: number;
  note?: string;
  ledger_status: "posted" | "blocked" | "none";
  lines: number;
};

export type RecipeLine = { stock_item_id: string; name: string; unit: "kg" | "l" | "pc"; qty: number; unit_cost: number; cost: number };

export type Dish = {
  id: string;
  name: string;
  category: string;
  price: number;
  net_price: number;
  lines: RecipeLine[];
  cost: number;
  food_cost_bp: number;
  margin: number;
  uncosted: number;
};

export type FoodCost = {
  from: string;
  to: string;
  food_sales: number;
  food_cost: number;
  food_cost_bp: number;
  wastage: number;
  count_differences: number;
  total_cost: number;
  total_cost_bp: number;
  purchases: number;
  portions_without_recipe: number;
  dishes: { id: string; name: string; qty: number; has_recipe: boolean; cost: number }[];
};
