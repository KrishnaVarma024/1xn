# Architecture decision records

One file per decision: what we chose, why, and what it costs. Numbering follows the order we made them in code; the architecture doc's section 4 lists the full set planned for v1.

| # | Decision |
| --- | --- |
| 0001 | [A modular monolith in Go](0001-modular-monolith-in-go.md) |
| 0002 | [Money is an int64 count of paise](0002-money-in-integer-paise.md) |
| 0003 | [One pricing spec, three engines, golden files as the contract](0003-one-pricing-spec-three-engines.md) |
| 0004 | [Tenant isolation with Postgres Row-Level Security](0004-tenancy-with-row-level-security.md) |
| 0005 | [Exactly-once effects from at-least-once delivery](0005-exactly-once-sync.md) |
| 0006 | [Every device has its own invoice series](0006-per-device-invoice-series.md) |
| 0007 | [A bill the guest holds is never dropped or rewritten](0007-issued-bills-are-never-dropped.md) |
| 0008 | [database/sql with lib/pq for now, pgx later](0008-database-sql-and-lib-pq-for-now.md) |
| 0009 | [P0 authentication: opaque, hashed bearer tokens](0009-p0-auth-opaque-tokens.md) |
| 0010 | [Ledger rules are enforced by the database](0010-ledger-rules-live-in-the-database.md) |
| 0011 | [An open order is one JSONB document, changed by a pure reducer](0011-order-as-a-jsonb-document-with-a-pure-reducer.md) |
| 0012 | [Sync pull sends current state, by a commit-ordered cursor](0012-state-based-sync-pull.md) |
| 0013 | [Device facts are recorded and flagged, not refused](0013-device-facts-are-recorded-and-flagged.md) |
| 0014 | [The back office is a Next.js Backend-for-Frontend](0014-back-office-is-a-nextjs-bff.md) |
