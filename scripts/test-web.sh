#!/usr/bin/env bash
# End-to-end test of the back office: the real Go API on your local Postgres,
# the real web app (production build), a real browser. `make test-web`.
#
# Tests sign up their own restaurants, so they never touch your data; the
# test restaurants they leave behind are separate tenants (RLS keeps them
# apart), like the ones `make seed` creates.
set -euo pipefail
cd "$(dirname "$0")/.."

DB_PORT="${DB_PORT:-5433}"
API_PORT="${E2E_API_PORT:-8099}"
WEB_PORT="${E2E_WEB_PORT:-3100}"
WORK="$(mktemp -d)"
API_PID=""
cleanup() {
  [ -n "$API_PID" ] && kill "$API_PID" 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "== migrate"
(cd services/core && MIGRATE_DATABASE_URL="postgres://onexn_owner:dev@localhost:${DB_PORT}/onexn?sslmode=disable" go run ./cmd/migrate)

echo "== start the API on :${API_PORT}"
(cd services/core && go build -o "$WORK/api" ./cmd/api)
DATABASE_URL="postgres://onexn_app:dev@localhost:${DB_PORT}/onexn?sslmode=disable" HTTP_ADDR=":${API_PORT}" \
  "$WORK/api" >"$WORK/api.log" 2>&1 &
API_PID=$!
for _ in $(seq 1 50); do
  curl -sf "http://localhost:${API_PORT}/readyz" >/dev/null && break
  sleep 0.2
done
# Ready AND ours: another program already on the port would also answer.
if ! curl -sf "http://localhost:${API_PORT}/readyz" >/dev/null || ! kill -0 "$API_PID" 2>/dev/null; then
  cat "$WORK/api.log"; echo "the API did not start (is port ${API_PORT} taken? set E2E_API_PORT)"; exit 1
fi

echo "== build the back office"
cd apps/backoffice
[ -d node_modules ] || npm ci
NEXT_TELEMETRY_DISABLED=1 npx next build >"$WORK/build.log" 2>&1 || { cat "$WORK/build.log"; exit 1; }

# A browser for Playwright, once (skipped where browsers are preinstalled).
[ -n "${PLAYWRIGHT_BROWSERS_PATH:-}" ] || npx playwright install chromium >/dev/null

echo "== browser tests"
E2E_API_URL="http://localhost:${API_PORT}" E2E_WEB_PORT="$WEB_PORT" npx playwright test "$@" || {
  echo "--- API log (last 40 lines)"; tail -40 "$WORK/api.log"; exit 1;
}
