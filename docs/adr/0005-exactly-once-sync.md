# 0005. Exactly-once effects from at-least-once delivery

Status: accepted, 29 Sep 2026

## Context

Tills bill offline for up to 72 hours and sync over flaky networks. Any request can be sent twice, and any response can be lost.

## Decision

Devices record each change as an op with a UUIDv7 `op_id` and a per-device `device_seq`, keep it in a local op log, and re-send it until acked. The gateway stores every `(device_id, op_id)` in `sync_inbox` with the payload's SHA-256 and the outcome. A repeat is answered from the inbox. Each applied op gets a per-tenant `server_seq` from a row-locked counter, so sequence order equals commit order. Pushes from one device are serialised by locking its `device` row.

## Consequences

Retries are always safe. Gaps in `device_seq` are detected and alerted. The per-tenant counter serialises writes within a tenant; at our volume that is milliseconds, and it can be split per outlet later.

## Alternatives considered

Two-phase commit (no). Trusting the client to never retry (no). A Postgres SEQUENCE for server_seq (rejected: numbers are handed out in start order, not commit order, so a reader can skip one).
