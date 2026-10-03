# 0015. Day close: device shifts, a computed Z report, a guarded close

Status: accepted, 3 Oct 2026

## Context

Restaurants need shift handovers (float, cash moves, counted cash) and an end-of-day (Z) report: sales, GST by rate, payments, drawer differences, voids, invoice gaps. Tills are offline-first, so shifts happen on the device and arrive by sync, and bills keep arriving after a day is over (ADR 0007: issued bills are never dropped). Devices pull changes from `change_log`, which grows forever.

## Decision

- **Shifts are device ops:** `shift.opened`, `cash.moved` (expense, to_safe, from_safe) and `shift.closed` (counted cash, optional notes and coins). Only tills (role `pos`) send them. One open shift per till, and shifts of one till never overlap.
- **The server computes expected cash** = float + cash payments on that till's bills within the shift's time window + from safe − expenses − to safe. The difference posts to 5900 Cash over and short; expenses to 5200, safe moves to 1105. A difference raises `cash_difference` (critical above ₹500).
- **A trading day runs 04:00 to 04:00 India time.** Bills use their own business date; voids and cancellations are placed by the cutoff.
- **The Z report is always computed from stored records** (`dayclose.Build`), never kept as counters.
- **Closing stores a snapshot** of the report (`day_close`). The close must carry the digest of the report the owner reviewed; the server recomputes in one REPEATABLE READ snapshot and answers `409 report_changed` if it differs. Warnings of level `check` (open shift, open order, missing invoice number) need `acknowledge_warnings`.
- **Late records are kept and flagged** (`bill_after_day_close`, `shift_after_day_close`); the day view compares snapshot and live report and lists amendments.
- **Closing a day prunes the change log** (older than 7 days, in batches) and raises `sync_state.change_log_floor` to the highest deleted seq; a pull from below it gets `410 cursor_expired` and bootstraps.

## Consequences

- Late bills never need special handling: the report is right whenever it is read, and the snapshot records what the owner signed off.
- The close is safe against concurrent syncs without holding locks while a person reads.
- A tenant that never closes a day never prunes its change log; a scheduled prune can be added without changing the floor rule.
- Matching bills to shifts by time trusts the till's clock relative to itself, which ordered sync usually makes reliable; times before GST began or more than 48 h ahead are refused for bills, shifts and order ops. A cash bill that still lands after its shift was counted corrects the shift and posts an adjusting entry (`shift_adjust`).
- Voids are also written to an append-only void log (`order_void`), so the report counts each void on the day it happened even after its line moves to another order.
- The close locks the tenant's `sync_state` row first, so it takes turns with sync: a bill is either in the stored report or flagged as late.
- Posting is idempotent on (source type, event id), so device ids that collide across kinds of event never merge two events into one entry.

## Alternatives considered

- **Tag each bill with its shift id.** Exact, but needs a device schema change before the device app exists, and fails for bills made while no shift was open. Time-window matching works today and reports cash outside shifts. A `shift_id` on bills can be added later and preferred when present.
- **Refuse records for closed days.** Simpler reports, but loses real money and breaks the invoice series.
- **Lock the outlet while closing.** Would stall the tills' sync for the whole review; the digest check gives the same guarantee without blocking anyone.
- **Prune by a fixed seq window or keep the log forever.** A seq window ignores how long a device was offline; keeping forever grows without bound.
