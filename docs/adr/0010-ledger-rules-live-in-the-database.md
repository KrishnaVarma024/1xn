# 0010. Ledger rules are enforced by the database

Status: accepted, 29 Sep 2026

## Context

The ledger will be written by many modules over years. One buggy posting rule must not be able to corrupt the books.

## Decision

Postgres enforces: every entry balances to zero and has at least two lines (deferred constraint triggers, checked at commit); entries go only into open periods; the journal is append-only (no UPDATE/DELETE grants for the app role, plus triggers that stop even the owner); one entry per source event (`source_event_id` unique per tenant); lines can only be added in the transaction that created their entry (so a posted entry cannot grow later); a period being written into is share-locked, so locking the month waits for in-flight entries; a locked period cannot be re-opened by the app.

## Consequences

Corrections are reversing entries, so history is kept. The checks cost a few indexed queries per entry. The Go code checks the same rules first to give clearer errors. Residual risk: the same-transaction rule uses a transaction-local marker that a deliberately malicious app role could forge; it stops bugs, not attackers. Routing all posting through one SECURITY DEFINER function closes that, and is planned with the accounting module.

## Alternatives considered

Application-only checks (one bug away from unbalanced books).
