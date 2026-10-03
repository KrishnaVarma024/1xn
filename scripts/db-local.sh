#!/usr/bin/env bash
# `make db-local`: sets up 1x.n's database on a Postgres you run yourself
# (Homebrew, Postgres.app) instead of the Docker one `make up` starts.
# Safe to run again: it only creates what is missing.
#
# It connects as your own Postgres superuser; Homebrew makes one named after
# your Mac user. Using another one? PGUSER=postgres make db-local
set -euo pipefail
cd "$(dirname "$0")/.."

PORT="${DB_PORT:-5432}"
if ! command -v psql >/dev/null 2>&1; then
  echo "psql not found. With Homebrew: brew install postgresql@16 && brew link --force postgresql@16"
  exit 1
fi

admin() { psql -h localhost -p "$PORT" -d postgres -v ON_ERROR_STOP=1 -qtA "$@"; }

if ! admin -c 'SELECT 1' >/dev/null 2>&1; then
  echo "Could not connect to Postgres on localhost:$PORT as ${PGUSER:-$(whoami)}."
  echo "  Is it running?       brew services start postgresql@16"
  echo "  Is it on that port?  DB_PORT is $PORT here; Homebrew uses 5432: echo \"DB_PORT=5432\" >> .env"
  exit 1
fi

# The owner role runs migrations; init.sql adds the app role the API uses.
admin <<'SQL'
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'onexn_owner') THEN
    CREATE ROLE onexn_owner LOGIN SUPERUSER PASSWORD 'dev'; -- local only; never reuse this password
  END IF;
END
$$;
SQL
if [ "$(admin -c "SELECT 1 FROM pg_database WHERE datname = 'onexn'")" != "1" ]; then
  admin -c "CREATE DATABASE onexn OWNER onexn_owner"
fi
PGPASSWORD=dev psql -h localhost -p "$PORT" -U onexn_owner -d onexn -v ON_ERROR_STOP=1 -q -f infra/local/init.sql

echo "ready: database onexn, roles onexn_owner and onexn_app, on localhost:$PORT"
if [ "$PORT" != "5433" ] && ! grep -qs "^DB_PORT=$PORT\$" .env; then
  echo "now point 1x.n at it (once):  echo \"DB_PORT=$PORT\" >> .env"
fi
echo "next: make run"
