# 0008. database/sql with lib/pq for now, pgx later

Status: accepted, 29 Sep 2026

## Context

The architecture doc picks pgx. The build environment for this first slice could not download golang.org/x modules, which pgx depends on.

## Decision

Code is written against the standard `database/sql` interface using `github.com/lib/pq`, which has no dependencies. To switch, replace the driver import with `github.com/jackc/pgx/v5/stdlib`, change `sql.Open("postgres", ...)` to `sql.Open("pgx", ...)` and `pq.Array`/`pq.Error` to their pgx equivalents. sqlc can be added then too.

## Consequences

lib/pq is in maintenance mode but stable and widely used. The switch is small because nothing outside `internal/platform/db` and a few `pq.Array` calls touches the driver.

## Alternatives considered

Vendoring pgx by hand (fragile).
