import Link from "next/link";
import type { Metadata } from "next";
import type { ReactNode } from "react";
import { ActionForm, SubmitButton } from "@/components/forms";
import { PrintButton } from "@/components/print-button";
import { Badge, Card, EmptyState, Input, LinkButton, PageHeader, Stat, Table, buttonClass, td, th } from "@/components/ui";
import { api } from "@/lib/api";
import { getContext } from "@/lib/context";
import { isRealDay, longDate, shiftDay } from "@/lib/dates";
import { amendmentLabel, cashMoveLabel, channelLabel, dateTimeIST, methodLabel, timeIST } from "@/lib/labels";
import { formatPaise, formatRate } from "@/lib/money";
import type { DayList, DayView, Shift, ZReport } from "@/lib/types";
import { closeDay } from "./actions";

export const metadata: Metadata = { title: "Day close" };

export default async function DayPage({ searchParams }: PageProps<"/day">) {
  const { date, view } = await searchParams;
  const { outlet } = await getContext();
  const list = await api<DayList>(`/v1/outlets/${outlet.id}/days`);
  const day = isRealDay(date) ? date : list.today;
  const v = await api<DayView>(`/v1/outlets/${outlet.id}/days/${day}`);
  // A closed day shows the report as it was closed, unless the owner asks
  // for today's figures.
  const live = view === "live" || !v.closed;
  const r = live ? v.report : v.closed!.report;

  return (
    <>
      <PageHeader
        title="Day close"
        subtitle={
          <>
            {longDate(day)} · {outlet.name}
            {day === list.today && " · today"}
          </>
        }
        actions={
          <div className="flex flex-wrap items-center gap-2 print:hidden">
            <LinkButton href={`/day?date=${shiftDay(day, -1)}`} aria-label="Previous day">
              ←
            </LinkButton>
            <form className="flex items-center gap-2" aria-label="Pick a day">
              <Input type="date" name="date" defaultValue={day} max={list.today} aria-label="Trading day" />
              <button className={buttonClass("secondary")}>Show</button>
            </form>
            {day < list.today && (
              <LinkButton href={`/day?date=${shiftDay(day, 1)}`} aria-label="Next day">
                →
              </LinkButton>
            )}
            <PrintButton />
          </div>
        }
      />

      <Status v={v} live={live} day={day} />

      <div className="mb-6 grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Stat label="Sales" value={formatPaise(r.sales.grand_total)} hint={`${r.sales.bills} ${r.sales.bills === 1 ? "bill" : "bills"} · including GST`} />
        <Stat label="GST" value={formatPaise(r.sales.tax)} hint={`on ${formatPaise(r.sales.taxable)} taxable`} />
        <Stat
          label="Average bill"
          value={formatPaise(r.sales.avg_bill)}
          hint={r.sales.covers > 0 ? `${formatPaise(r.sales.avg_cover)} a cover · ${r.sales.covers} covers` : "no covers recorded"}
        />
        <Stat
          label="Cash in drawers"
          value={r.shifts.length === 0 ? "No shifts" : formatPaise(inDrawers(r))}
          hint={r.shifts.length === 0 ? "tills did not open shifts" : drawerHint(r)}
        />
      </div>

      {/* grid-cols-1, not a bare grid: its track may shrink, so wide tables scroll inside their card on a phone. */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Card title="Sales">
          <dl className="space-y-1.5 text-sm tabular-nums">
            <Row label="Items sold" value={formatPaise(r.sales.subtotal)} />
            {r.sales.discounts > 0 && <Row label="Discounts" value={`− ${formatPaise(r.sales.discounts)}`} />}
            {r.sales.service_charge > 0 && <Row label="Service charge" value={formatPaise(r.sales.service_charge)} />}
            <Row label="Taxable value" value={formatPaise(r.sales.taxable)} strong />
            <Row label="CGST" value={formatPaise(r.sales.cgst)} />
            <Row label="SGST" value={formatPaise(r.sales.sgst)} />
            {r.sales.igst > 0 && <Row label="IGST" value={formatPaise(r.sales.igst)} />}
            {r.sales.round_off !== 0 && <Row label="Round off" value={formatPaise(r.sales.round_off)} />}
            <Row label="Total billed" value={formatPaise(r.sales.grand_total)} strong />
            {r.sales.tips > 0 && <Row label="Tips (not sales)" value={formatPaise(r.sales.tips)} />}
            {r.sales.first_at && (
              <p className="pt-1 text-xs text-ink-faint">
                First bill {timeIST(r.sales.first_at)}, last {timeIST(r.sales.last_at!)}
              </p>
            )}
          </dl>
        </Card>

        <Card title="Payments and channels">
          {r.payments.length === 0 ? (
            <p className="text-sm text-ink-soft">No payments on this day.</p>
          ) : (
            <dl className="space-y-1.5 text-sm tabular-nums">
              {r.payments.map((p) => (
                <Row key={p.method} label={`${methodLabel[p.method] ?? p.method} · ${p.bills} ${p.bills === 1 ? "bill" : "bills"}`} value={formatPaise(p.amount)} />
              ))}
            </dl>
          )}
          {r.channels.length > 0 && (
            <dl className="mt-4 space-y-1.5 border-t border-line pt-3 text-sm tabular-nums">
              {r.channels.map((c) => (
                <Row key={c.channel} label={`${channelLabel[c.channel] ?? c.channel} · ${c.bills}`} value={formatPaise(c.grand_total)} />
              ))}
            </dl>
          )}
        </Card>

        <Card title="GST by rate" className="lg:col-span-2">
          {r.tax_by_rate.length === 0 ? (
            <p className="text-sm text-ink-soft">No taxable sales.</p>
          ) : (
            <Table>
              <thead>
                <tr>
                  <th className={th}>Rate</th>
                  <th className={`${th} text-right`}>Taxable</th>
                  <th className={`${th} text-right`}>CGST</th>
                  <th className={`${th} text-right`}>SGST</th>
                  <th className={`${th} text-right`}>IGST</th>
                </tr>
              </thead>
              <tbody>
                {r.tax_by_rate.map((t) => (
                  <tr key={t.rate_bp}>
                    <td className={td}>{formatRate(t.rate_bp)}</td>
                    <td className={`${td} text-right`}>{formatPaise(t.taxable)}</td>
                    <td className={`${td} text-right`}>{formatPaise(t.cgst)}</td>
                    <td className={`${td} text-right`}>{formatPaise(t.sgst)}</td>
                    <td className={`${td} text-right`}>{formatPaise(t.igst)}</td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )}
        </Card>

        <Card title="Cash drawers" className="lg:col-span-2">
          <Drawers r={r} />
        </Card>

        <Card title="Invoice numbers">
          {r.series.length === 0 ? (
            <p className="text-sm text-ink-soft">No invoices on this day.</p>
          ) : (
            <ul className="space-y-3 text-sm">
              {r.series.map((s) => (
                <li key={s.device_id + s.fy}>
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <span className="font-medium">{s.device_name}</span>
                    {s.missing_count === 0 ? <Badge tone="good">No gaps</Badge> : <Badge tone="bad">{s.missing_count} missing</Badge>}
                  </div>
                  <div className="mt-0.5 font-mono text-xs text-ink-soft">
                    {s.first} → {s.last} · {s.count} {s.count === 1 ? "bill" : "bills"}
                    {s.continues_from && <> · after {s.continues_from}</>}
                  </div>
                  {s.missing.length > 0 && (
                    <p className="mt-1 font-mono text-xs text-chilli-700">
                      Missing: {s.missing.join(", ")}
                      {s.missing_count > s.missing.length && ` and ${s.missing_count - s.missing.length} more`}
                    </p>
                  )}
                </li>
              ))}
            </ul>
          )}
        </Card>

        <Card title="Best sellers">
          {r.top_items.length === 0 ? (
            <p className="text-sm text-ink-soft">Nothing sold.</p>
          ) : (
            <ol className="space-y-1.5 text-sm tabular-nums">
              {r.top_items.map((it, i) => (
                <li key={it.name} className="flex justify-between gap-3">
                  <span className="min-w-0 truncate">
                    <span className="text-ink-faint">{i + 1}.</span> {it.name} <span className="text-ink-soft">× {it.qty}</span>
                  </span>
                  <span>{formatPaise(it.net)}</span>
                </li>
              ))}
            </ol>
          )}
        </Card>

        <Card title="Voids and cancellations" className="lg:col-span-2">
          <p className="mb-3 text-sm text-ink-soft">
            {r.voids.lines === 0
              ? "Nothing was voided."
              : `${r.voids.lines} ${r.voids.lines === 1 ? "dish" : "dishes"} worth ${formatPaise(r.voids.value)} voided` +
                (r.voids.fired > 0 ? `, ${r.voids.fired} after the kitchen had the order` : "") +
                "."}
            {r.cancelled.orders > 0 && ` ${r.cancelled.orders} ${r.cancelled.orders === 1 ? "order" : "orders"} cancelled whole (${formatPaise(r.cancelled.value)}).`}
            {" "}Counted from {dateTimeIST(r.from)} to {dateTimeIST(r.to)}.
          </p>
          {r.voids.list.length > 0 && (
            <Table>
              <thead>
                <tr>
                  <th className={th}>Time</th>
                  <th className={`${th} min-w-40`}>Dish</th>
                  <th className={th}>Reason</th>
                  <th className={th}>Approved by</th>
                  <th className={`${th} text-right`}>Value</th>
                </tr>
              </thead>
              <tbody>
                {r.voids.list.map((l, i) => (
                  <tr key={i}>
                    <td className={td}>{timeIST(l.at)}</td>
                    <td className={td}>
                      <Link href={`/orders/${l.order_id}`} className="text-neel-700 hover:underline">
                        {l.name} × {l.qty}
                      </Link>
                      {l.table && <span className="text-ink-soft"> · {l.table}</span>}
                      <div className="mt-0.5 flex gap-1">
                        {l.fired && <Badge tone="warn">Kitchen had it</Badge>}
                        {l.cancelled && <Badge>Order cancelled</Badge>}
                      </div>
                    </td>
                    <td className={td}>{l.reason}</td>
                    <td className={td}>{l.approved_by || (l.fired ? <span className="text-chilli-700">nobody</span> : "—")}</td>
                    <td className={`${td} text-right`}>{formatPaise(l.value)}</td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )}
        </Card>
      </div>

      <RecentDays list={list} day={day} />
      <p className="mt-6 hidden text-xs text-ink-faint print:block">
        Printed from 1x.n. Report made {dateTimeIST(r.generated_at)}. Digest {r.digest}.
      </p>
    </>
  );
}

/** Counted cash of closed shifts plus what open drawers should hold now. */
function inDrawers(r: ZReport): number {
  return r.shifts.reduce((sum, s) => sum + (s.counted ?? s.expected), 0);
}

function drawerHint(r: ZReport): string {
  const parts: string[] = [];
  if (r.cash.difference !== 0) parts.push(`${formatPaise(Math.abs(r.cash.difference))} ${r.cash.difference < 0 ? "short" : "over"}`);
  if (r.cash.open_shifts > 0) parts.push(`${r.cash.open_shifts} ${r.cash.open_shifts === 1 ? "shift" : "shifts"} still open`);
  else if (r.cash.difference === 0) parts.push("all counted, no difference");
  return parts.join(" · ");
}

function Row({ label, value, strong }: { label: ReactNode; value: ReactNode; strong?: boolean }) {
  return (
    <div className={`flex justify-between gap-3 ${strong ? "font-semibold" : ""}`}>
      <dt className={strong ? "" : "text-ink-soft"}>{label}</dt>
      <dd>{value}</dd>
    </div>
  );
}

/** Where the day stands: open (with the close form) or closed (and amended?). */
function Status({ v, live, day }: { v: DayView; live: boolean; day: string }) {
  const warnings = v.report.warnings;
  if (v.closed) {
    return (
      <div className="mb-6 space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-curry-700/20 bg-curry-50 px-4 py-3 text-sm">
          <span>
            <Badge tone="good">Closed</Badge>{" "}
            <span className="ml-1">
              by {v.closed.closed_by} on {dateTimeIST(v.closed.closed_at)}. {live ? "Showing today's figures." : "Showing the report as it was closed."}
            </span>
          </span>
          <Link href={live ? `/day?date=${day}` : `/day?date=${day}&view=live`} className="font-medium text-neel-700 hover:underline print:hidden">
            {live ? "Show the closing report" : "Show today's figures"}
          </Link>
        </div>
        {v.amended && (
          <div role="note" aria-label="Amended" className="rounded-xl border border-marigold-400/40 bg-marigold-50 px-4 py-3 text-sm">
            <p className="font-medium text-marigold-700">Amended after closing</p>
            <p className="mt-0.5 text-ink-soft">Records for this day arrived or changed after it was closed. They are counted; the closing report is kept as it was.</p>
            <ul className="mt-2 space-y-0.5 tabular-nums">
              {v.amendments.map((a) => (
                <li key={a.what}>
                  {amendmentLabel[a.what] ?? a.what}: {a.money ? formatPaise(a.before) : a.before} → <strong>{a.money ? formatPaise(a.after) : a.after}</strong>
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>
    );
  }
  const mustCheck = warnings.some((w) => w.level === "check");
  return (
    <Card title="Close this day" className="mb-6 print:hidden">
      {warnings.length > 0 ? (
        <ul aria-label="Warnings" className="mb-4 space-y-1.5 text-sm">
          {warnings.map((w, i) => (
            <li key={i} className="flex gap-2">
              <Badge tone={w.level === "check" ? "warn" : "info"}>{w.level === "check" ? "Check" : "Note"}</Badge>
              <span>{w.text}</span>
            </li>
          ))}
        </ul>
      ) : (
        <p className="mb-4 text-sm text-ink-soft">Everything adds up: no open shifts or tables, no missing invoice numbers.</p>
      )}
      <ActionForm action={closeDay.bind(null, day)} label="Close the day" resetOnSuccess={false}>
        <input type="hidden" name="digest" value={v.report.digest} />
        <div className="flex flex-wrap items-center gap-4">
          {mustCheck && (
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" name="acknowledge" className="h-4 w-4 accent-neel-700" />
              I have checked the warnings above
            </label>
          )}
          <SubmitButton pendingText="Closing…">Close the day</SubmitButton>
        </div>
        <p className="mt-2 text-xs text-ink-faint">
          Closing saves this Z report as it is now. Bills that sync later are still counted, and the day is marked as amended.
        </p>
      </ActionForm>
    </Card>
  );
}

function Drawers({ r }: { r: ZReport }) {
  if (r.shifts.length === 0) {
    return (
      <EmptyState title="No till shifts on this day">
        Tills open a shift with a cash float and close it by counting the drawer. Cash taken: {formatPaise(r.payments.find((p) => p.method === "cash")?.amount ?? 0)}.
      </EmptyState>
    );
  }
  const moves = (s: Shift) => r.cash_moves.filter((m) => m.shift_id === s.id);
  return (
    <>
      <Table>
        <thead>
          <tr>
            <th className={`${th} min-w-44`}>Till and cashier</th>
            <th className={`${th} text-right`}>Float</th>
            <th className={`${th} text-right`}>Cash sales</th>
            <th className={`${th} text-right`}>Paid out</th>
            <th className={`${th} text-right`}>Safe</th>
            <th className={`${th} text-right`}>Expected</th>
            <th className={`${th} text-right`}>Counted</th>
            <th className={`${th} text-right`}>Difference</th>
          </tr>
        </thead>
        <tbody>
          {r.shifts.map((s) => (
            <tr key={s.id}>
              <td className={td}>
                <div className="font-medium">{s.device_name}</div>
                <div className="text-xs text-ink-soft">
                  {s.opened_by} · {timeIST(s.opened_at)}–{s.closed_at ? timeIST(s.closed_at) : "open"}
                </div>
                {moves(s).map((m, i) => (
                  <div key={i} className="text-xs text-ink-faint">
                    {cashMoveLabel[m.kind]} {formatPaise(m.amount)} · {m.reason}
                  </div>
                ))}
              </td>
              <td className={`${td} text-right`}>{formatPaise(s.opening_float)}</td>
              <td className={`${td} text-right`}>{formatPaise(s.cash_sales)}</td>
              <td className={`${td} text-right`}>{s.expenses ? `− ${formatPaise(s.expenses)}` : "—"}</td>
              <td className={`${td} text-right`}>{safe(s)}</td>
              <td className={`${td} text-right font-medium`}>{formatPaise(s.expected)}</td>
              <td className={`${td} text-right`}>{s.counted === undefined ? <Badge tone="warn">Open</Badge> : formatPaise(s.counted)}</td>
              <td className={`${td} text-right`}>
                <Difference d={s.difference} />
              </td>
            </tr>
          ))}
        </tbody>
      </Table>
      {r.cash.outside_shifts > 0 && (
        <p className="mt-3 text-sm text-marigold-700">{formatPaise(r.cash.outside_shifts)} in cash was taken on bills while no shift was open on that till.</p>
      )}
    </>
  );
}

function safe(s: Shift): string {
  const parts = [];
  if (s.to_safe) parts.push(`− ${formatPaise(s.to_safe)}`);
  if (s.from_safe) parts.push(`+ ${formatPaise(s.from_safe)}`);
  return parts.join(" ") || "—";
}

function Difference({ d }: { d?: number }) {
  if (d === undefined) return <span className="text-ink-faint">—</span>;
  if (d === 0) return <Badge tone="good">Exact</Badge>;
  return <Badge tone={Math.abs(d) > 50000 ? "bad" : "warn"}>{`${formatPaise(Math.abs(d))} ${d < 0 ? "short" : "over"}`}</Badge>;
}

function RecentDays({ list, day }: { list: DayList; day: string }) {
  if (list.closed.length === 0) return null;
  return (
    <section className="mt-8 print:hidden">
      <h2 className="mb-3 text-sm font-semibold">Closed days</h2>
      <Table>
        <thead>
          <tr>
            <th className={th}>Day</th>
            <th className={th}>Closed</th>
            <th className={`${th} text-right`}>Bills</th>
            <th className={`${th} text-right`}>Sales</th>
            <th className={`${th} text-right`}>Cash over/short</th>
          </tr>
        </thead>
        <tbody>
          {list.closed.map((c) => (
            <tr key={c.business_date} className={c.business_date === day ? "bg-neel-50" : "hover:bg-chalk"}>
              <td className={td}>
                <Link href={`/day?date=${c.business_date}`} className="text-neel-700 hover:underline">
                  {longDate(c.business_date)}
                </Link>
              </td>
              <td className={td}>
                {dateTimeIST(c.closed_at)} · {c.closed_by}
              </td>
              <td className={`${td} text-right`}>{c.bills}</td>
              <td className={`${td} text-right`}>{formatPaise(c.grand_total)}</td>
              <td className={`${td} text-right`}>
                {c.shifts === 0 ? <span className="text-ink-faint">no shifts</span> : <Difference d={c.cash_difference} />}
              </td>
            </tr>
          ))}
        </tbody>
      </Table>
    </section>
  );
}
