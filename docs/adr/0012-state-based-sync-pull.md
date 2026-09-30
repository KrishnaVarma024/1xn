# 0012. Sync pull sends current state, by a commit-ordered cursor

Status: accepted, 30 Sep 2026

## Context

Devices must see changes made by other devices: the KDS needs new KOTs, and the till needs a captain's lines. A device may have been offline for hours. It must never miss a change, and it should not download the same order 50 times.

## Decision

- Every order save writes `(tenant_id, seq, outlet_id, entity, entity_id)` to `change_log`, with the `server_seq` of the op that caused it. Seqs come from the row-locked per-tenant counter, so they are handed out in commit order (ADR 0005).
- `GET /v1/sync/pull?after=N&limit=L` returns the **current document** of each order of the device's outlet that changed after N. Each order appears once, at the position of its latest change, oldest first. The response carries the new `cursor` and `has_more`.
- A page never ends inside a seq. One op can change several orders under one seq (a merge), and the next page starts after the cursor, so a split group would be lost. A page may run over `limit` by one group, and stops early at about 4 MB, but only between seqs.
- `after=0` is a bootstrap: all open orders of the outlet, and the tenant's current seq as the cursor. The device replaces everything it had.
- The read runs in one `REPEATABLE READ, READ ONLY` transaction, so the cursor and the documents come from the same snapshot.
- On the device, the local view is the server copy plus the device's unacked ops, re-applied with the reducer (a rebase).

## Consequences

- Devices do not replay other devices' ops. Rules for merges, spillovers and conflicts stay on the server.
- A long offline gap costs one page per 200 changed orders, not one per op.
- Pull is polled for now. A push hint (WebSocket or the outlet hub) can say "pull now" later, and the endpoint stays the same.
- `change_log` must be pruned. A device whose cursor is older than the pruning horizon bootstraps again.

## Alternatives considered

- **Send ops** (an event feed). Gives full history to devices, but every device must apply every rule identically, forever.
- **`updated_at` timestamps as the cursor.** Clocks and commit order disagree, so a reader can skip a change. This is the same trap as a Postgres SEQUENCE (ADR 0005).
- **Logical replication or CDC to devices.** Too heavy for a phone, and it bypasses RLS.
