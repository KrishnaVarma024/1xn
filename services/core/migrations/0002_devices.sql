-- 0002 devices: pairing codes and registered devices.

-- A short-lived, single-use code shown on the owner's screen and typed (or
-- scanned) on the new till. Only its hash is stored.
CREATE TABLE pairing_code (
  code_hash  bytea PRIMARY KEY CHECK (length(code_hash) = 32),
  tenant_id  uuid NOT NULL,
  outlet_id  uuid NOT NULL,
  role       text NOT NULL CHECK (role IN ('pos', 'captain', 'kds', 'hub')),
  created_by uuid NOT NULL,
  expires_at timestamptz NOT NULL,
  used_at    timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, outlet_id) REFERENCES outlet (tenant_id, id)
);

-- Every device gets its own invoice series prefix, so two tills can bill
-- offline at the same moment without ever producing the same invoice number.
CREATE TABLE device (
  id              uuid PRIMARY KEY,
  tenant_id       uuid NOT NULL,
  outlet_id       uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  name            text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  role            text NOT NULL CHECK (role IN ('pos', 'captain', 'kds', 'hub')),
  invoice_prefix  text NOT NULL CHECK (invoice_prefix ~ '^[0-9A-Z]{3}$'),
  last_device_seq bigint NOT NULL DEFAULT 0, -- highest op sequence seen from this device
  registered_at   timestamptz NOT NULL DEFAULT now(),
  revoked_at      timestamptz,
  UNIQUE (tenant_id, id),
  UNIQUE (legal_entity_id, invoice_prefix),
  FOREIGN KEY (tenant_id, outlet_id) REFERENCES outlet (tenant_id, id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES legal_entity (tenant_id, id)
);

ALTER TABLE pairing_code ENABLE ROW LEVEL SECURITY;
ALTER TABLE device       ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON pairing_code
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON device
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());

GRANT SELECT, INSERT ON pairing_code TO app_rw;
GRANT SELECT, INSERT, UPDATE ON device TO app_rw;

-- Redeeming a code is the second (and last) cross-tenant operation: the new
-- device does not know its tenant yet. The UPDATE ... WHERE used_at IS NULL
-- makes redemption atomic: two devices racing with one code, one wins.
CREATE FUNCTION pairing_redeem(p_hash bytea)
RETURNS TABLE (tenant_id uuid, outlet_id uuid, role text)
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  UPDATE pairing_code p
  SET used_at = now()
  WHERE p.code_hash = p_hash AND p.used_at IS NULL AND p.expires_at > now()
  RETURNING p.tenant_id, p.outlet_id, p.role
$$;
REVOKE ALL ON FUNCTION pairing_redeem(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pairing_redeem(bytea) TO app_rw;

-- Now that devices exist, token resolution also refuses tokens of revoked
-- devices (0001 could not reference this table yet).
CREATE OR REPLACE FUNCTION auth_resolve(p_hash bytea)
RETURNS TABLE (tenant_id uuid, kind text, subject_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT t.tenant_id, t.kind, t.subject_id
  FROM auth_token t
  WHERE t.token_hash = p_hash
    AND t.revoked_at IS NULL
    AND (t.kind <> 'device'
         OR EXISTS (SELECT 1 FROM device d WHERE d.id = t.subject_id AND d.revoked_at IS NULL))
$$;
