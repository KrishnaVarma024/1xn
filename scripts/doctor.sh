#!/usr/bin/env bash
# `make doctor`: checks this machine can run 1x.n, and says what to do when
# it cannot. Read-only: it changes nothing. (Written for macOS's bash 3.2.)
set -u
cd "$(dirname "$0")/.."

DB_PORT="${DB_PORT:-5433}"
problems=0
ok()   { printf 'ok       %s\n' "$*"; }
bad()  { printf 'PROBLEM  %s\n' "$*"; problems=$((problems + 1)); }
info() { printf '         %s\n' "$*"; }
has()  { command -v "$1" >/dev/null 2>&1; }
listening() { # is something accepting connections on localhost:$1?
  if has nc; then nc -z -w 1 localhost "$1" >/dev/null 2>&1; return; fi
  (exec 3<>"/dev/tcp/127.0.0.1/$1") >/dev/null 2>&1
}

echo "== tools"
for t in go git python3 node npm; do
  if has "$t"; then ok "$t"; else bad "$t is missing (see README: Run it on your Mac)"; fi
done
if has node && [ "$(node -p 'process.versions.node.split(".")[0]')" -lt 22 ]; then
  bad "Node $(node -v) is too old for the back office: install Node 22 or newer (brew install node)"
fi

echo "== database (DB_PORT=$DB_PORT)"
if grep -qs '^DB_PORT=' .env; then
  info "DB_PORT comes from .env"
elif [ "$DB_PORT" != "5433" ]; then
  info "DB_PORT comes from this terminal only; to make every terminal use it: echo \"DB_PORT=$DB_PORT\" >> .env"
else
  info "DB_PORT is the default (Docker's 5433). Using Homebrew's Postgres? echo \"DB_PORT=5432\" >> .env"
fi
if listening "$DB_PORT"; then
  ok "something answers on localhost:$DB_PORT"
  if has psql; then
    if PGPASSWORD=dev PGCONNECT_TIMEOUT=3 psql -h localhost -p "$DB_PORT" -U onexn_owner -d onexn -qtAc 'SELECT 1' >/dev/null 2>&1; then
      ok "database onexn and role onexn_owner are set up"
      if PGPASSWORD=dev PGCONNECT_TIMEOUT=3 psql -h localhost -p "$DB_PORT" -U onexn_app -d onexn -qtAc 'SELECT 1' >/dev/null 2>&1; then
        ok "role onexn_app (the API's) can log in"
      else
        bad "role onexn_app cannot log in: run make db-local"
      fi
    else
      bad "it does not have the onexn database or the onexn_owner role yet"
      info "your own Postgres: make db-local    the Docker one: make reset && make up"
    fi
  fi
else
  bad "nothing is listening on localhost:$DB_PORT, so Postgres is not running there"
  other=""
  for p in 5432 5433 5434; do
    if [ "$p" != "$DB_PORT" ] && listening "$p"; then other="$p"; break; fi
  done
  if [ -n "$other" ]; then
    info "something answers on $other. If it is your Postgres: echo \"DB_PORT=$other\" >> .env, then make db-local (first time)"
  fi
  if has docker; then
    if docker info >/dev/null 2>&1; then info "Docker is running: make up starts Postgres on $DB_PORT"
    else info "Docker is installed but not running: open Docker Desktop, then make up"; fi
  fi
  if has brew; then info "Homebrew: brew services start postgresql@16 (it listens on 5432)"; fi
fi

echo "== running now"
if listening 8080; then ok "the API answers on :8080"; else info "the API is not running: make run (its own terminal; leave it running)"; fi
if listening 3000; then ok "the back office answers on http://localhost:3000"; else info "the back office is not running: make web (another terminal)"; fi

swaps=$(find apps services -name '.*.sw[po]' -not -path '*/node_modules/*' 2>/dev/null)
if [ -n "$swaps" ]; then
  echo "== editor leftovers"
  for f in $swaps; do info "vi swap file $f: close the other vi that has it open, or delete it if vi crashed"; done
fi

echo
if [ "$problems" -eq 0 ]; then echo "All good."; else echo "$problems problem(s) above; each says what to do."; exit 1; fi
