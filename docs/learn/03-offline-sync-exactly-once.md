# 03 Offline sync, exactly once

**Problem.** A till bills for up to 72 hours without internet, then syncs over a network that drops packets, times out and retries. Any request may arrive twice, and any response may be lost. Yet every bill must land **exactly once**. Lose one and revenue disappears. Apply one twice and you double-count revenue and GST.

**Truth to remember:** exactly-once *delivery* over a network is impossible. Exactly-once *effects* are possible: deliver at least once, and make applying idempotent.

## The two halves

**Device side (at least once).** Every change becomes an **op**:
```json
{"op_id": "0192f...-7...", "device_seq": 42, "type": "bill.settled", "payload": {...}}
```
- `op_id` is a UUIDv7, made on the device, so no server is needed to name it.
- `device_seq` counts 1, 2, 3... with no gaps.
- The op is written to the local op log (SQLite) in the **same local transaction** as the bill. It is re-sent, byte for byte, until the server acks it.

**Server side (idempotent).** `syncgw.applyOne` for each op:
1. `BEGIN`, set the tenant, and **lock the device row**. Two pushes from one till now take turns instead of racing.
2. Look up `(device_id, op_id)` in `sync_inbox`. If found, answer from memory ("duplicate", or the earlier rejection). If found but the payload hash differs, refuse: an op id must never be reused.
3. `SAVEPOINT`, run the applier (for example `sales.ApplySettledBill`). A **rejection** rolls back to the savepoint and records the op as rejected, with its payload kept. A **transient error** (database down) rolls back everything, and the device retries.
4. Record the outcome in `sync_inbox`, then `COMMIT`.

The inbox row and the bill commit together. So "applied" and "remembered as applied" can never disagree.

## Server sequence numbers, and a classic bug

Each applied op gets a `server_seq` so other devices can later pull "everything after N". The obvious choice is a Postgres `SEQUENCE`. It has a trap:

```
tx A: nextval() -> 41 ........................ COMMIT (slow)
tx B:        nextval() -> 42 .. COMMIT
reader:                              pulls "after 40" -> sees 42, remembers 42
reader:                                                  pulls "after 42" -> never sees 41
```

Sequences hand out numbers in **start** order, but readers see rows in **commit** order. We instead increment a per-tenant counter row (`UPDATE sync_state ... RETURNING`). The row lock is held until commit, so numbers come out in commit order. Cost: writes within one tenant are serialised for the few milliseconds of each transaction. At about 56 orders a second across all tenants, that is invisible.

## Poison ops: permanent vs transient failures

The most dangerous bug in a sync queue is the **poison op**: an op that fails the same way every time, while the server calls the failure "temporary". The device retries it forever, and every bill behind it waits. The adversarial review of this slice found exactly that. A fully comped bill (total ₹0) produced no ledger lines, the ledger refused it, and the gateway answered 503 on every retry.

The fix is to classify every failure:

| Failure | Same result on retry? | Answer |
| --- | --- | --- |
| Validation (wrong series, totals that do not add up) | yes | reject, keep the payload, alert |
| Database data or constraint error (SQLSTATE class 22, 23) | yes | reject (`db_rejected`), keep the payload, alert |
| Deadlock, serialization failure, connection lost | no | 503; the device retries later |

Two details make this work:
- **Deferred constraints** (the ledger balance check) normally fire at COMMIT, which is outside the savepoint and too late to reject cleanly. The gateway runs `SET CONSTRAINTS ALL IMMEDIATE` inside the savepoint, so these violations surface while they can still be rolled back.
- **Rejected payloads** are stored as `bytea`, byte for byte. Stored as `jsonb`, a NUL character that broke the bill would also break storing the rejection.

## What re-pricing cannot catch

The server re-prices the device's **own input**. If a stale or tampered till bills a 5% item at 0%, both engines agree on the wrong answer. So each bill is also compared with the catalogue's current rates and the outlet's settings. A difference raises an alert, but does not reject the bill: the till may simply have billed from an older menu.

## Proof in tests

- `TestP0Gate` pushes the same op twice: `applied` then `duplicate`, with the same `server_seq`.
- `TestConcurrentRetriesApplyOnce` fires the same op ten times at once: 1 applied, 9 duplicates, 1 bill, 1 journal entry. We removed the device lock once to check the test works, and it failed.
- `TestInvoiceRules` covers the rest: a rejected op stays rejected, the same bill under a new op id is a duplicate, and a reused op id is refused.
- `TestSequenceGapIsFlagged` checks that a jump in `device_seq` raises an alert, because a gap means the device lost ops before sending them.
- `TestFullCompBillDoesNotBlockTheQueue` and `TestMalformedOpsAreRejectedNotRetriedForever` send poison ops with a good op queued behind them. The poison ops are rejected, and the good op is applied.

## What would break at 10x?

- **The per-tenant counter** becomes the hot spot for a 500-outlet chain. Fix: a counter per outlet (sequence per stream), or hybrid logical clocks.
- **Per-op transactions** cost one commit each. A 72-hour backlog of 5,000 ops is fine. At 100x, batch several ops per transaction, with care about partial failure.
- **`sync_inbox` grows forever.** Partition it by month and drop partitions after the retention period. A device never retries a 90-day-old op.

## Interview questions

<details><summary>1. Design an idempotent payments/orders API.</summary>
The client generates an idempotency key.
The server stores key + request hash + response atomically with the effect.
A repeat with the same hash returns the stored response. A repeat with a different hash is an error.
Keys expire after a retention window.</details>

<details><summary>2. Why is exactly-once delivery impossible, and what do real systems do?</summary>
Two Generals: the sender can never know the last message arrived.
Real systems combine at-least-once delivery with idempotent consumers (dedupe on a key). That gives exactly-once effects.</details>

<details><summary>3. Why is a DB sequence a bad cursor for "changes since N"?</summary>
Numbers are allocated when a transaction starts, but become visible when it commits.
A lower number can commit after a higher one, so a reader skips it forever.
Fix it with commit-order allocation (a locked counter), or have readers stay behind the oldest in-flight transaction.</details>

<details><summary>4. What is a SAVEPOINT for here?</summary>
It undoes only the applier's partial work when the op is rejected.
We still commit the inbox row and the alert in the same transaction, so the rejection is remembered.</details>

## Exercises

1. Draw the timeline when the server COMMITs but the HTTP response is lost. What does the device do next, and what does the server answer?
2. Change `nextSeq` to use a Postgres SEQUENCE. Write a test with two concurrent transactions that makes a reader skip a number.
3. Sketch the pull side: `GET /v1/sync/pull?after=N` for menu changes and other devices' KOTs. What index does it need?
