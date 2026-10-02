import type { Metadata } from "next";
import { Badge, EmptyState, PageHeader, Table, td, th } from "@/components/ui";
import { api } from "@/lib/api";
import { formatPaise } from "@/lib/money";
import type { TrialBalance } from "@/lib/types";

export const metadata: Metadata = { title: "Books" };

export default async function BooksPage() {
  const tb = await api<TrialBalance>("/v1/ledger/trial-balance");
  tb.accounts ??= []; // older APIs answered null before the first bill
  const debit = tb.accounts.reduce((s, a) => s + a.debit, 0);
  const credit = tb.accounts.reduce((s, a) => s + a.credit, 0);
  return (
    <>
      <PageHeader
        title="Books"
        subtitle="Trial balance: every account that has moved. Each bill is posted as a double entry the moment it syncs."
        actions={tb.balanced ? <Badge tone="good">Balanced</Badge> : <Badge tone="bad">Not balanced</Badge>}
      />
      {tb.accounts.length === 0 ? (
        <EmptyState title="Nothing posted yet">The first synced bill creates the first journal entry.</EmptyState>
      ) : (
        <Table>
          <thead>
            <tr>
              <th className={th}>Account</th>
              <th className={th}>Type</th>
              <th className={`${th} text-right`}>Debit</th>
              <th className={`${th} text-right`}>Credit</th>
            </tr>
          </thead>
          <tbody>
            {tb.accounts.map((a) => (
              <tr key={a.legal_entity_id + a.code}>
                <td className={td}>
                  <span className="mr-2 font-mono text-xs text-ink-faint">{a.code}</span>
                  {a.name}
                </td>
                <td className={`${td} capitalize text-ink-soft`}>{a.kind}</td>
                <td className={`${td} text-right`}>{a.debit ? formatPaise(a.debit) : ""}</td>
                <td className={`${td} text-right`}>{a.credit ? formatPaise(a.credit) : ""}</td>
              </tr>
            ))}
          </tbody>
          <tfoot>
            <tr>
              <td className={`${td} font-semibold`} colSpan={2}>
                Totals
              </td>
              <td className={`${td} text-right font-semibold`}>{formatPaise(debit)}</td>
              <td className={`${td} text-right font-semibold`}>{formatPaise(credit)}</td>
            </tr>
          </tfoot>
        </Table>
      )}
    </>
  );
}
