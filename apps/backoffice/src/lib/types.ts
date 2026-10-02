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
