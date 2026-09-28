# 02 Tenancy and Row-Level Security

**Problem.** 1,000 restaurants share one Postgres. One forgotten `WHERE tenant_id = $1` in one query in one module, and restaurant A sees restaurant B's sales. Code review will miss it eventually.

**Solution.** Make the database refuse, whatever the code asks.

## The four pieces

1. **Every tenant table has `tenant_id`, and a policy.**
   ```sql
   CREATE POLICY tenant_isolation ON bill
     USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
   ```
   `USING` filters reads. `WITH CHECK` stops writes of rows for another tenant.

2. **The API does not own the tables.** Migrations run as `onexn_owner`. The API connects as `onexn_app`, a member of `app_rw`. Table owners and superusers skip RLS. `app_rw` does not, because it owns nothing and has no `BYPASSRLS`.

3. **The tenant is set per transaction, not per connection.**
   ```go
   tx.Exec(`SELECT set_config('app.tenant_id', $1, true)`, tenantID) // true = transaction-local
   ```
   Connections are pooled and reused. A session-level setting would leak one request's tenant into the next request on that connection. A transaction-local one disappears at COMMIT. See `db.InTenantTx`.

4. **Fail closed.** `app_tenant()` returns NULL when nothing is set, and `tenant_id = NULL` is never true. A code path that forgets the tenant sees **nothing**, not everything. `TestTenantIsolation` checks this directly.

## Two more guards

- **Composite foreign keys.** `outlet (tenant_id, legal_entity_id) REFERENCES legal_entity (tenant_id, id)`. A bug that attaches tenant A's outlet to tenant B's GSTIN fails with a constraint error. It never becomes silently corrupt data.
- **Two narrow doors.** Before we know the tenant, we must resolve a token, or redeem a pairing code. These are the only cross-tenant reads. Each is a `SECURITY DEFINER` function that returns a few fields for exactly one hash. The app role can call them, but it cannot read `auth_token` or `pairing_code` rows of other tenants any other way.

## What it costs

- Every query carries `tenant_id = $x`. With `tenant_id` leading the indexes, that is nearly free.
- Tests must run as the real app role, not a superuser; a superuser would bypass RLS and hide bugs. `internal/e2e` creates `onexn_app` and uses it.
- Reporting across tenants (our own admin dashboards) needs a separate, audited role. Do not widen the app role.

## What would break at 10x?

At 10,000 tenants, one database is too big to operate comfortably (backups, vacuum, noisy neighbours). The architecture doc's answer is **cells**: many identical copies of this stack, each holding about 1,000 tenants, with a small router that maps each tenant to its cell. RLS stays exactly as it is inside each cell.

## Interview questions

<details><summary>1. Shared schema + RLS vs schema-per-tenant vs database-per-tenant?</summary>
Shared schema is the cheapest and scales to many small tenants. Isolation depends on RLS being correct.
Schema-per-tenant or database-per-tenant gives stronger isolation and per-tenant backups, but migrations and connection counts grow with tenants.
Common hybrid: shared for small tenants, dedicated for the biggest.</details>

<details><summary>2. Why SET LOCAL / set_config(..., true) and not SET?</summary>
With connection pooling, a session-level SET survives the request and leaks into the next request that reuses the connection.
A transaction-local setting cannot outlive its transaction.</details>

<details><summary>3. How do you test that isolation really works?</summary>
Connect as the real non-owner role.
Query with no tenant filter on purpose, and assert you see only your rows.
Assert that with no tenant set you see none.
Try to insert another tenant's row and expect a policy violation.</details>

<details><summary>4. What does SECURITY DEFINER do and what is the risk?</summary>
The function runs with its owner's privileges.
Risks: search_path hijacking (always SET search_path in the definition), and functions that return too much.
Keep them tiny and give EXECUTE only to the roles that need it.</details>

## Exercises

1. In a `psql` session as `onexn_app`, run `SELECT count(*) FROM bill;` with no tenant set, then with `SELECT set_config('app.tenant_id', '<your tenant id>', false);`. Explain both results.
2. Comment out the `set_config` line in `db.InTenantTx` and run `make test`. Predict which tests fail before you run them.
3. Write a migration for a new tenant table. What four things must it have? (Answer: `tenant_id`, `ENABLE ROW LEVEL SECURITY`, the policy, and grants to `app_rw`.)
