import type { Metadata } from "next";
import { Badge, Card, EmptyState, PageHeader, Table, td, th } from "@/components/ui";
import { api } from "@/lib/api";
import { getContext } from "@/lib/context";
import { fyCode } from "@/lib/dates";
import { dateTimeIST, roleLabel } from "@/lib/labels";
import type { Device } from "@/lib/types";
import { revokeDevice } from "./actions";
import { PairForm } from "./pair-form";
import { NoAccess } from "@/components/no-access";
import { can } from "@/lib/access";

export const metadata: Metadata = { title: "Devices" };

export default async function DevicesPage() {
  if (!(await can("devices"))) return <NoAccess page="Devices" />;
  const fy = fyCode();
  const { outlet } = await getContext();
  const { devices } = await api<{ devices: Device[] }>(`/v1/outlets/${outlet.id}/devices`);
  return (
    <>
      <PageHeader title="Devices" subtitle="Tills, captain phones and kitchen screens paired with this outlet." />
      <div className="space-y-6">
        <Card title="Pair a new device">
          <PairForm />
        </Card>
        {devices.length === 0 ? (
          <EmptyState title="No devices yet">Get a pairing code above and type it into the device.</EmptyState>
        ) : (
          <Table>
            <thead>
              <tr>
                <th className={th}>Name</th>
                <th className={th}>Kind</th>
                <th className={th}>Invoice series</th>
                <th className={th}>Paired</th>
                <th className={th}>Synced ops</th>
                <th className={th} />
              </tr>
            </thead>
            <tbody>
              {devices.map((d) => (
                <tr key={d.id} className={d.revoked_at ? "text-ink-faint" : ""} data-testid={`device-${d.name}`}>
                  <td className={`${td} font-medium`}>{d.name}</td>
                  <td className={td}>{roleLabel[d.role] ?? d.role}</td>
                  <td className={`${td} font-mono`} title={d.role === "pos" ? `This till numbers its bills ${d.invoice_prefix}-${fy}-000001, 000002, … afresh each financial year` : undefined}>
                    {d.role === "pos" ? `${d.invoice_prefix}-${fy}-…` : "—"}
                  </td>
                  <td className={td}>{dateTimeIST(d.registered_at)}</td>
                  <td className={td}>{d.last_device_seq}</td>
                  <td className={`${td} text-right`}>
                    {d.revoked_at ? (
                      <Badge tone="bad">Revoked {dateTimeIST(d.revoked_at)}</Badge>
                    ) : (
                      <details className="inline-block text-left">
                        <summary className="cursor-pointer text-xs font-medium text-chilli-700">Revoke…</summary>
                        <form action={revokeDevice.bind(null, d.id)} className="mt-2 w-56 rounded-lg border border-line bg-white p-3 text-xs">
                          <p className="text-ink-soft">
                            {d.name} stops working at once. Use this for a lost or stolen device. It cannot be undone.
                            {d.role === "pos" && " Bills this till has not synced yet will never arrive."}
                          </p>
                          <button className="mt-2 rounded-lg bg-chilli-700 px-2.5 py-1 font-medium text-white">Yes, revoke {d.name}</button>
                        </form>
                      </details>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </div>
    </>
  );
}
