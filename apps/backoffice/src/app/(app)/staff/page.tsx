import type { Metadata } from "next";
import { ActionForm, SubmitButton } from "@/components/forms";
import { NoAccess } from "@/components/no-access";
import { Badge, Card, Field, Input, PageHeader, Select, Table, td, th } from "@/components/ui";
import { can } from "@/lib/access";
import { api } from "@/lib/api";
import { getContext } from "@/lib/context";
import { longDate } from "@/lib/dates";
import { dayIST, personRoleLabel } from "@/lib/labels";
import { formatPhone } from "@/lib/phone";
import type { Person } from "@/lib/types";
import { addPerson, setPersonActive, updatePerson } from "./actions";

export const metadata: Metadata = { title: "Staff" };

// What each role can do: the API's auth/permissions.go, in words.
const roles = [
  { role: "Owner", can: "Everything: staff, the books and paying suppliers too." },
  { role: "Manager", can: "Menu, tables, devices, day close, stock, recipes and costs, suppliers (not paying them), alerts." },
  { role: "Cashier", can: "The live floor and bills; receiving goods (on credit or paid from a till) and recording wastage." },
];

export default async function StaffPage() {
  if (!(await can("staff"))) return <NoAccess page="Staff" />;
  const { me } = await getContext();
  const { staff } = await api<{ staff: Person[] }>("/v1/staff");

  return (
    <>
      <PageHeader
        title="Staff"
        subtitle={`Who can sign in to ${me.business_name}, and as what. Everyone signs in with their own mobile number and a code.`}
      />
      <div className="space-y-2">
        {staff.map((p) => (
          <details key={p.id} className="rounded-xl border border-line bg-white" data-testid={`person-${p.name}`}>
            <summary className="flex cursor-pointer list-none flex-wrap items-center justify-between gap-3 px-4 py-3">
              <span className={p.active ? "" : "text-ink-faint"}>
                <span className="font-medium">{p.name}</span>{" "}
                <span className="text-sm text-ink-soft tabular-nums">{formatPhone(p.phone)}</span>
              </span>
              <span className="flex flex-wrap items-center gap-2">
                <Badge tone={p.role === "owner" ? "info" : "neutral"}>{personRoleLabel[p.role] ?? p.role}</Badge>
                {!p.active ? <Badge tone="bad">Switched off</Badge> : p.signed_in && <Badge tone="good">Signed in</Badge>}
              </span>
            </summary>
            <div className="border-t border-line px-4 py-3">
              <ActionForm
                action={updatePerson.bind(null, p.id, p.role === "owner")}
                label={`Change ${p.name}`}
                resetOnSuccess={false}
                className="grid gap-3 sm:grid-cols-[2fr_1fr_1fr_auto] sm:items-end"
              >
                <Field label="Name">
                  <Input name="name" defaultValue={p.name} required maxLength={200} />
                </Field>
                {p.role === "owner" ? (
                  <p className="text-sm text-ink-soft sm:col-span-2 sm:pb-2">The owner&apos;s number and role stay as signed up.</p>
                ) : (
                  <>
                    <Field label="Mobile number" hint="a new number signs them out">
                      <Input name="phone" defaultValue={formatPhone(p.phone)} required inputMode="tel" />
                    </Field>
                    <Field label="Role">
                      <Select name="role" defaultValue={p.role}>
                        <option value="manager">Manager</option>
                        <option value="cashier">Cashier</option>
                      </Select>
                    </Field>
                  </>
                )}
                <SubmitButton variant="secondary">Save</SubmitButton>
              </ActionForm>
              {p.role !== "owner" && (
                <div className="mt-3 flex flex-wrap items-center gap-3 border-t border-line pt-3">
                  <ActionForm
                    action={setPersonActive.bind(null, p.id, !p.active)}
                    label={`${p.active ? "Switch off" : "Switch on"} ${p.name}`}
                    resetOnSuccess={false}
                  >
                    <SubmitButton variant={p.active ? "danger" : "secondary"} size="sm">
                      {p.active ? "Switch off" : "Switch on again"}
                    </SubmitButton>
                  </ActionForm>
                  <span className="text-xs text-ink-faint">
                    {p.active ? "They are signed out everywhere and cannot sign in." : "They can sign in again with a new code."} Added{" "}
                    {longDate(dayIST(p.created_at))}.
                  </span>
                </div>
              )}
            </div>
          </details>
        ))}
      </div>

      <Card title="Add someone" className="mt-6">
        <ActionForm action={addPerson} label="Add someone" className="grid gap-3 sm:grid-cols-[2fr_1fr_1fr_auto] sm:items-end">
          <Field label="Name">
            <Input name="name" placeholder="Asha" required maxLength={200} />
          </Field>
          <Field label="Mobile number">
            <Input name="phone" inputMode="tel" placeholder="98765 43210" required />
          </Field>
          <Field label="Role">
            <Select name="role" defaultValue="cashier">
              <option value="cashier">Cashier</option>
              <option value="manager">Manager</option>
            </Select>
          </Field>
          <SubmitButton>Add</SubmitButton>
        </ActionForm>
      </Card>

      <h2 className="mt-8 mb-3 text-sm font-semibold">What each role can do</h2>
      <Table>
        <thead>
          <tr>
            <th className={th}>Role</th>
            <th className={th}>In the back office</th>
          </tr>
        </thead>
        <tbody>
          {roles.map((r) => (
            <tr key={r.role}>
              <td className={`${td} font-medium`}>{r.role}</td>
              <td className={td}>{r.can}</td>
            </tr>
          ))}
        </tbody>
      </Table>
    </>
  );
}
