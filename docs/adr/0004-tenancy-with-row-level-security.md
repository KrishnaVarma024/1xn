# 0004. Tenant isolation with Postgres Row-Level Security

Status: accepted, 29 Sep 2026

## Context

1,000 customers share one database cell. A single forgotten `WHERE tenant_id = ...` would leak one restaurant's sales to another.

## Decision

Every tenant table has `tenant_id` and a policy `tenant_id = app_tenant()`. The API connects as `onexn_app` (member of `app_rw`), which does not own the tables and cannot bypass RLS. Each transaction sets `app.tenant_id` with `set_config(..., true)`, so it resets at commit. Unset means NULL, which matches nothing: failures are closed, not open. Composite foreign keys `(tenant_id, x_id)` stop cross-tenant references. Ids that devices generate (bills) and event ids (journal `source_event_id`) are unique per tenant, not globally, so one tenant cannot collide with or probe another's ids. The only cross-tenant reads are two `SECURITY DEFINER` functions (token lookup, pairing code redemption).

## Consequences

Isolation holds even when application code is wrong; `TestTenantIsolation` queries with no WHERE clause on purpose. Cost: every query carries the policy predicate (cheap with indexes on tenant_id), and pooled connections must never set the tenant at session level.

## Alternatives considered

Database per tenant (rejected at 1,000 tenants: migrations and connections do not scale). Application-only filtering (rejected: one bug from a breach).
