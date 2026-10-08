-- 0012 Staff sign in with a mobile number and a one-time code, and roles.
--
-- Until now a restaurant had one person, its owner, holding one long-lived
-- token. Now the owner adds managers and cashiers; each signs in with their
-- own mobile number and a code, gets a session that expires, and sees what
-- their role allows. The owner token from sign-up keeps working (developers
-- and `make seed` use it).

-- People can be switched off (they left); a number signs in to one person per
-- business.
ALTER TABLE app_user ADD COLUMN active boolean NOT NULL DEFAULT true;
CREATE UNIQUE INDEX app_user_phone ON app_user (tenant_id, phone);

-- Sessions are tokens too: kind 'session', with an expiry.
ALTER TABLE auth_token DROP CONSTRAINT auth_token_kind_check;
ALTER TABLE auth_token
  ADD CONSTRAINT auth_token_kind_check CHECK (kind IN ('owner', 'session', 'device')),
  ADD COLUMN expires_at timestamptz,
  ADD CHECK ((kind = 'session') = (expires_at IS NOT NULL));
CREATE INDEX auth_token_subject ON auth_token (tenant_id, subject_id) WHERE revoked_at IS NULL;

-- One-time codes. A challenge is made before anyone knows which business the
-- caller belongs to, so this table has no tenant and no RLS (the one table
-- like that, besides tenant lookups through SECURITY DEFINER functions). It
-- holds no business data: a phone number, hashes, counters.
--
-- A challenge is made for every request, also for numbers with no account
-- (code_hash NULL: nothing was sent, and no code can match), so the answer
-- and the rate limits are the same either way and never tell a stranger
-- whether a number has an account.
CREATE TABLE otp_challenge (
  id                uuid PRIMARY KEY,
  phone             text NOT NULL CHECK (phone ~ '^\+[1-9][0-9]{7,14}$'),
  code_hash         bytea CHECK (length(code_hash) = 32),
  created_at        timestamptz NOT NULL DEFAULT now(),
  expires_at        timestamptz NOT NULL,
  attempts          int NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  verified_at       timestamptz,
  -- After the code: a ticket, good for five minutes, to pick a business.
  ticket_hash       bytea UNIQUE CHECK (length(ticket_hash) = 32),
  ticket_expires_at timestamptz,
  ticket_used_at    timestamptz,
  CHECK ((ticket_hash IS NULL) = (ticket_expires_at IS NULL)),
  CHECK (ticket_hash IS NULL OR verified_at IS NOT NULL)
);
CREATE INDEX otp_challenge_phone ON otp_challenge (phone, created_at);
GRANT SELECT, INSERT, UPDATE, DELETE ON otp_challenge TO app_rw;

-- Token resolution now also returns the person's role, and refuses expired
-- sessions and people who were switched off. (A new result column needs
-- DROP and CREATE, not CREATE OR REPLACE.)
DROP FUNCTION auth_resolve(bytea);
CREATE FUNCTION auth_resolve(p_hash bytea)
RETURNS TABLE (tenant_id uuid, kind text, subject_id uuid, role text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT t.tenant_id, t.kind, t.subject_id, coalesce(u.role, '')
  FROM auth_token t
  LEFT JOIN app_user u ON t.kind IN ('owner', 'session') AND u.id = t.subject_id AND u.tenant_id = t.tenant_id
  WHERE t.token_hash = p_hash
    AND t.revoked_at IS NULL
    AND (t.expires_at IS NULL OR t.expires_at > now())
    AND CASE t.kind
          WHEN 'device' THEN EXISTS (SELECT 1 FROM device d WHERE d.id = t.subject_id AND d.revoked_at IS NULL)
          ELSE coalesce(u.active, false)
        END
$$;
REVOKE ALL ON FUNCTION auth_resolve(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_resolve(bytea) TO app_rw;

-- Signing in: which businesses a mobile number can sign in to. Like
-- auth_resolve it crosses tenants, so it returns only what the sign-in screen
-- shows, for exactly one number. The app calls it to decide whether to send a
-- code, and after the code is right, to list the businesses.
CREATE FUNCTION login_accounts(p_phone text)
RETURNS TABLE (user_id uuid, tenant_id uuid, business_name text, user_name text, role text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT u.id, u.tenant_id, t.name, u.name, u.role
  FROM app_user u JOIN tenant t ON t.id = u.tenant_id
  WHERE u.phone = p_phone AND u.active
  ORDER BY t.created_at DESC, t.id
  LIMIT 50
$$;
REVOKE ALL ON FUNCTION login_accounts(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION login_accounts(text) TO app_rw;
