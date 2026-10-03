#!/usr/bin/env bash
# Runs before migrate, run and the tests: makes sure a Postgres answers on
# DB_PORT. If none does and Docker is running, it starts the Docker one
# (the same as `make up`), so on a fresh checkout `make run` is all it takes.
#
# It never starts Docker on 5432: that port is for a Postgres you run
# yourself (Homebrew, Postgres.app), and if that one is stopped, the command
# that follows says so and how to start it.
set -u
cd "$(dirname "$0")/.."

DB_PORT="${DB_PORT:-5433}"
listening() {
  if command -v nc >/dev/null 2>&1; then nc -z -w 1 localhost "$1" >/dev/null 2>&1; return; fi
  (exec 3<>"/dev/tcp/127.0.0.1/$1") >/dev/null 2>&1
}

listening "$DB_PORT" && exit 0
[ "$DB_PORT" = "5432" ] && exit 0
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
  echo "No Postgres on localhost:$DB_PORT yet: starting the Docker one (make up)."
  echo "The first time, Docker downloads the Postgres image; that takes a minute."
  ONEXN_DB_PORT="$DB_PORT" docker compose up -d --wait || {
    echo "Docker could not start Postgres. make doctor shows what is wrong."
    exit 1
  }
fi
exit 0 # without Docker, the command that follows explains what to do
