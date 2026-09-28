# Start here

This repo is 1x.n's POS core: the backend that a restaurant's tills sync to. This first slice covers phase P0 and the core of app 1 (POS Restaurant). It proves one thing end to end: **a restaurant signs up, a till pairs, a bill is priced offline, synced (twice, harmlessly) and posted as a balanced journal entry.** That is P0's exit gate from the architecture doc. `make demo` shows it, and `TestP0Gate` checks it.

The notes in this folder explain *why* the code looks the way it does. Read them in order:

| Note | You learn |
| --- | --- |
| [01 Money and the GST engine](01-money-and-gst-engine.md) | integer money, rounding, fair discount splitting, GST maths; the spec three engines share |
| [02 Tenancy and Row-Level Security](02-tenancy-and-rls.md) | how 1,000 restaurants share one database without seeing each other |
| [03 Offline sync, exactly once](03-offline-sync-exactly-once.md) | op logs, idempotency, sequence numbers, why retries are always safe |
| [04 The double-entry ledger](04-double-entry-ledger.md) | debits, credits, posting rules, and why the database polices the books |

## The life of one bill

```
 TILL (offline-capable)                     CLOUD (services/core)                          POSTGRES
 ──────────────────────                     ─────────────────────                          ────────
 1. price with pricing engine
    (same rules as the server)
 2. take next number from its own
    series: 001-2627-000042
 3. print; write op to local op log
 4. POST /v1/sync/push  ─────────────▶  auth.Middleware: token hash ─▶ auth_resolve() ──▶ tenant id
    (re-sent until acked)                syncgw.applyOne:
                                           BEGIN; set app.tenant_id ─────────────────────▶ RLS on
                                           lock device row (one push at a time per till)
                                           seen (device, op_id)? ─── yes ─▶ answer "duplicate"
                                           SAVEPOINT
                                           sales.ApplySettledBill:
                                             validate series, FY, dates, payments
                                             recompute with pricing engine ─▶ match / mismatch
                                             next server_seq (row-locked counter)
                                             ledger.PostBillSettled ───────────────────────▶ balance checked
                                             insert bill, lines, taxes, payments               at COMMIT
                                             alerts (mismatch, payment difference)
                                             outbox event sales.bill.settled.v1
                                           record outcome in sync_inbox
                                           COMMIT ───────────────────────────────────────────▶ all or nothing
 5. ack ◀──────────────────────────────  {"status": "applied", "server_seq": 42}
    drop op from the local queue
```

Everything between BEGIN and COMMIT happens or none of it does. That one property removes whole classes of bugs, like "bill saved but no journal entry" or "event sent but bill rolled back".

## Read the code in this order

1. `services/core/modules/sales/pricing/engine.go`: the pure pricing engine. No database, no HTTP.
2. `packages/golden/tax/cases.json`: what the engine must answer, case by case.
3. `services/core/migrations/0001_platform.sql` to `0005_sales_sync.sql`: the data model. The comments explain the rules.
4. `services/core/internal/platform/db/db.go`: `InTenantTx`, the heart of multi-tenancy.
5. `services/core/modules/identity/identity.go`: signup, one transaction.
6. `services/core/modules/devices/devices.go`: pairing and invoice series.
7. `services/core/modules/syncgw/syncgw.go`: exactly-once sync.
8. `services/core/modules/sales/ingest.go`: what happens to a bill.
9. `services/core/modules/ledger/rules.go` then `ledger.go`: posting rules and posting.
10. `services/core/internal/e2e/*_test.go`: the whole thing exercised against real Postgres.

## How we test

| Layer | What | Where |
| --- | --- | --- |
| Golden | 27 hand-written bills and 200 generated ones must match to the paisa | `pricing/golden_test.go` |
| Property | 20,000 random bills must satisfy the engine's invariants (totals add up, CGST = SGST, etc.) | `pricing/property_test.go` |
| Fuzz | Go's fuzzer hunts for inputs that panic or break an invariant (`make fuzz`) | `FuzzCompute` |
| Oracle | An independent Python engine must agree with the golden files | `tools/golden/oracle.py` |
| End to end | The real API on a real Postgres, as the real non-owner role | `internal/e2e` |

The slice also went through an adversarial review by a second agent. It found eight real issues, the worst being a comped bill that blocked a till's sync forever. Each fix has a regression test in `internal/e2e/hardening_test.go`.

A test is only useful if it fails when the code is wrong. While building this slice we checked that. We removed the device row lock in `syncgw`, and `TestConcurrentRetriesApplyOnce` failed with 503s. We restored the lock, and it passed again. Try the same trick on any test you doubt.

## Glossary

- **paise**: 1/100 of a rupee. All money is stored as whole paise.
- **bp (basis points)**: 1/100 of a percent. 500 bp = 5%.
- **GSTIN**: 15-character GST registration number; the first two digits are the state code.
- **SAC 996331**: the services code for restaurant food service.
- **FY**: Indian financial year, 1 April to 31 March; written `2627` for Apr 2026 to Mar 2027.
- **RLS**: Row-Level Security, Postgres filtering rows per tenant for every query.
- **UUIDv7**: a UUID that starts with a timestamp, so ids sort by creation time.
- **Idempotent**: doing it twice has the same effect as doing it once.
- **Outbox**: events written in the same transaction as the data, sent out later.
- **Suspense account**: where an accountant parks money that does not match yet, so the books still balance.
