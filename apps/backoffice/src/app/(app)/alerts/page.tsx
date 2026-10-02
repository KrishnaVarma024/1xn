import type { Metadata } from "next";
import { Badge, EmptyState, PageHeader } from "@/components/ui";
import { api } from "@/lib/api";
import { alertText, dateTimeIST } from "@/lib/labels";
import type { Alert } from "@/lib/types";

export const metadata: Metadata = { title: "Alerts" };

const tone = { info: "info", warning: "warn", critical: "bad" } as const;

export default async function AlertsPage() {
  const { alerts } = await api<{ alerts: Alert[] }>("/v1/alerts");
  return (
    <>
      <PageHeader title="Alerts" subtitle="Things a person should look at. Nothing here was thrown away: each one is kept and flagged." />
      {alerts.length === 0 ? (
        <EmptyState title="All clear">No alerts. Mismatched bills, voids without approval and sync problems would show up here.</EmptyState>
      ) : (
        <ul className="space-y-3">
          {alerts.map((a) => {
            const t = alertText[a.kind] ?? { title: a.kind.replaceAll("_", " "), help: "" };
            return (
              <li key={a.id} className="rounded-xl border border-line bg-white p-4" data-testid={`alert-${a.kind}`}>
                <div className="flex flex-wrap items-start justify-between gap-2">
                  <div>
                    <div className="font-medium">{t.title}</div>
                    {t.help && <div className="mt-0.5 text-sm text-ink-soft">{t.help}</div>}
                  </div>
                  <div className="flex items-center gap-2">
                    <Badge tone={tone[a.severity]}>{a.severity}</Badge>
                    <span className="text-xs text-ink-faint">{dateTimeIST(a.created_at)}</span>
                  </div>
                </div>
                <Details details={a.details} />
              </li>
            );
          })}
        </ul>
      )}
    </>
  );
}

function Details({ details }: { details: Record<string, unknown> }) {
  const entries = Object.entries(details ?? {});
  if (entries.length === 0) return null;
  return (
    <dl className="mt-3 grid gap-x-4 gap-y-1 rounded-lg bg-chalk p-3 text-xs sm:grid-cols-[max-content_1fr]">
      {entries.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-ink-faint">{k.replaceAll("_", " ")}</dt>
          <dd className="break-all font-mono">{typeof v === "string" || typeof v === "number" ? String(v) : JSON.stringify(v)}</dd>
        </div>
      ))}
    </dl>
  );
}
