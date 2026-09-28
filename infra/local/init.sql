-- Runs once, when the Docker volume is first created.
-- Creates the login role the API uses. It is NOT the table owner and has no
-- BYPASSRLS, so Row-Level Security applies to every query it makes.
-- The migrations grant table privileges to the app_rw group role.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app_rw') THEN
    CREATE ROLE app_rw NOLOGIN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'onexn_app') THEN
    CREATE ROLE onexn_app LOGIN PASSWORD 'dev' IN ROLE app_rw; -- local only; never reuse this password
  END IF;
END
$$;
