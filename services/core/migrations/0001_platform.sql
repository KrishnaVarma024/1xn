-- 0001 platform: roles, tenancy, identity.
--
-- Two database roles, on purpose:
--   * the owner role runs these migrations and owns every table;
--   * app_rw is what the running service uses. It is not the owner and has no
--     BYPASSRLS, so Row-Level Security applies to every query it makes.
-- A login role (onexn_app locally) is created outside migrations and granted
-- app_rw; see infra/local/init.sql.

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app_rw') THEN
    CREATE ROLE app_rw NOLOGIN;
  END IF;
END
$$;

GRANT USAGE ON SCHEMA public TO app_rw;

-- The tenant for the current transaction. The service sets it with
-- set_config('app.tenant_id', ..., true) at the start of every transaction.
-- Unset means NULL, and NULL matches no row: a forgotten tenant context fails
-- closed (returns nothing) instead of open (returns everyone's data).
CREATE FUNCTION app_tenant() RETURNS uuid
LANGUAGE sql STABLE AS $$
  SELECT nullif(current_setting('app.tenant_id', true), '')::uuid
$$;

-- A tenant is one paying customer: a restaurant brand or group.
CREATE TABLE tenant (
  id         uuid PRIMARY KEY,
  name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
  created_at timestamptz NOT NULL DEFAULT now()
);

-- A legal entity is one GSTIN. Invoice numbers are unique per legal entity per
-- financial year, so the invoice series hangs off this table.
CREATE TABLE legal_entity (
  id                 uuid PRIMARY KEY,
  tenant_id          uuid NOT NULL REFERENCES tenant (id),
  name               text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
  gstin              text CHECK (gstin ~ '^[0-9]{2}[A-Z]{5}[0-9]{4}[A-Z][1-9A-Z]Z[0-9A-Z]$'),
  state_code         text NOT NULL CHECK (state_code ~ '^[0-9]{2}$'),
  next_series_no     integer NOT NULL DEFAULT 1, -- next device invoice series to hand out
  created_at         timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  CHECK (gstin IS NULL OR left(gstin, 2) = state_code)
);

-- An outlet is one physical restaurant or kitchen.
CREATE TABLE outlet (
  id              uuid PRIMARY KEY,
  tenant_id       uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  name            text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
  state_code      text NOT NULL CHECK (state_code ~ '^[0-9]{2}$'),
  timezone        text NOT NULL DEFAULT 'Asia/Kolkata',
  price_mode      text NOT NULL DEFAULT 'exclusive' CHECK (price_mode IN ('exclusive', 'inclusive')),
  rounding        text NOT NULL DEFAULT 'nearest_rupee' CHECK (rounding IN ('nearest_rupee', 'none')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  -- Composite foreign key: the legal entity must belong to the SAME tenant.
  -- A bug that mixes tenants fails here, in the database, not in production data.
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES legal_entity (tenant_id, id)
);

CREATE TABLE app_user (
  id         uuid PRIMARY KEY,
  tenant_id  uuid NOT NULL REFERENCES tenant (id),
  name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
  phone      text NOT NULL CHECK (phone ~ '^\+[1-9][0-9]{7,14}$'), -- E.164
  role       text NOT NULL CHECK (role IN ('owner', 'manager', 'cashier')),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id)
);

-- Opaque bearer tokens. Only a SHA-256 hash is stored: a leaked database dump
-- does not leak working tokens.
CREATE TABLE auth_token (
  token_hash bytea PRIMARY KEY CHECK (length(token_hash) = 32),
  tenant_id  uuid NOT NULL REFERENCES tenant (id),
  kind       text NOT NULL CHECK (kind IN ('owner', 'device')),
  subject_id uuid NOT NULL, -- app_user.id or device.id
  created_at timestamptz NOT NULL DEFAULT now(),
  revoked_at timestamptz
);

-- Row-Level Security ---------------------------------------------------------
ALTER TABLE tenant       ENABLE ROW LEVEL SECURITY;
ALTER TABLE legal_entity ENABLE ROW LEVEL SECURITY;
ALTER TABLE outlet       ENABLE ROW LEVEL SECURITY;
ALTER TABLE app_user     ENABLE ROW LEVEL SECURITY;
ALTER TABLE auth_token   ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON tenant
  USING (id = app_tenant()) WITH CHECK (id = app_tenant());
CREATE POLICY tenant_isolation ON legal_entity
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON outlet
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON app_user
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON auth_token
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());

GRANT SELECT, INSERT, UPDATE ON tenant, legal_entity, outlet, app_user, auth_token TO app_rw;

-- Resolving a token happens BEFORE we know the tenant, so it cannot go through
-- RLS. This SECURITY DEFINER function runs as the table owner and returns only
-- the three fields auth needs, for exactly one hash. It is the only
-- cross-tenant read the app role can make.
CREATE FUNCTION auth_resolve(p_hash bytea)
RETURNS TABLE (tenant_id uuid, kind text, subject_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT t.tenant_id, t.kind, t.subject_id
  FROM auth_token t
  WHERE t.token_hash = p_hash AND t.revoked_at IS NULL
$$;
REVOKE ALL ON FUNCTION auth_resolve(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_resolve(bytea) TO app_rw;
GRANT EXECUTE ON FUNCTION app_tenant() TO app_rw;
