# 0011. An open order is one JSONB document, changed by a pure reducer

Status: accepted, 30 Sep 2026

## Context

An open order changes dozens of times: lines added, KOTs fired, voids, moves, merges, bills. Four devices may change it at once, some after being offline. The same rules must run on the server (the authority) and on the device (to show the result without waiting for the network). The relational schema for money (bills, lines, taxes, payments, ledger) already exists and is where reports and GST read from.

## Decision

- The order's rules live in `modules/orders/domain.go` as a **pure reducer**: `(order, command) → new order or error`. It has no database, no clock and no I/O. The device will get a port of it, checked against the same cases.
- The server stores each order as **one JSONB document** in `pos_order.doc`. Columns that are queried or constrained (`status`, `outlet_id`, `table_id`, `merged_into`, `spillover_of`, `version`, `last_seq`) sit next to it. The database checks that `status = 'merged'` exactly when `merged_into` is set, and that tables and outlets exist (composite tenant FKs).
- Every write is: lock the row `FOR UPDATE`, run the reducer, save the whole document, bump `version`, and append to `change_log`.
- When a bill settles, the relational bill tables are the record. The order document only points to the bill (`bill_id` on each line, `bill.order_id`).

## Consequences

- One place holds the rules, and it is unit-tested without Postgres.
- Reads of an order are one row. Sync pull sends the document as it is.
- Each change rewrites the whole document. A busy table is a few KB. Orders are capped at 500 lines and 1 MiB of JSON (`order_too_large`), so the worst case stays bounded.
- Line ids are unique per tenant, forever (`order_line_key`), so the same dish cannot be added twice, wherever its line has moved.
- There are no SQL queries across lines inside open orders (for example "all open naans"). That is fine: live kitchen views read the documents, and reports read closed bills.
- Changing the document's shape needs care: new fields must be optional, since old documents stay in the table.

## Alternatives considered

- **Normalised tables** (`order_line`, `kot`, `kot_line`). Better for queries across orders, but each op becomes several statements and the rules spread across SQL and Go. We can move lines out later if large orders or line queries need it.
- **Event sourcing** (store only ops, rebuild the order by replay). Full history, but every read replays, and every rule change must replay old events correctly. The op history is already kept in `sync_inbox` for audit.
