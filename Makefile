# 1x.n developer commands. Run `make help` for the list.
#
# Two database roles on purpose (see docs/learn/02-tenancy-and-rls.md):
#   onexn_owner  owns the schema, runs migrations
#   onexn_app    what the API uses; Row-Level Security applies to it

#
# The Docker Postgres (`make up`) listens on host port 5433, not the default
# 5432, so it never fights with another Postgres already on your Mac.
#
# Your own settings go in .env (gitignored), read by every make command in
# every terminal. Usually one line is all it takes:
#   DB_PORT=5432     you run Homebrew's Postgres instead of Docker
# A one-off works too: make run DB_PORT=5434

-include .env
DB_PORT              ?= 5433
MIGRATE_DATABASE_URL ?= postgres://onexn_owner:dev@localhost:$(DB_PORT)/onexn?sslmode=disable
DATABASE_URL         ?= postgres://onexn_app:dev@localhost:$(DB_PORT)/onexn?sslmode=disable
TEST_DATABASE_URL    ?= postgres://onexn_owner:dev@localhost:$(DB_PORT)/postgres?sslmode=disable
CORE := services/core
WEB  := apps/backoffice
export ONEXN_DB_PORT := $(DB_PORT)

.PHONY: help doctor up down reset db-local db-ready migrate run demo seed web web-install test test-unit test-web golden fuzz fmt vet check

help: ## list commands
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  make %-12s %s\n", $$1, $$2}'

doctor: ## check your tools, the database and what is running; says what to do about each problem
	@DB_PORT=$(DB_PORT) ./scripts/doctor.sh

up: ## start Postgres in Docker (port 5433, or DB_PORT) and wait until healthy
	docker compose up -d --wait

down: ## stop Postgres (data is kept in a Docker volume)
	docker compose down

reset: ## stop Postgres AND delete its data
	docker compose down -v

db-local: ## no Docker? set up the onexn database and roles on your own Postgres (Homebrew), once
	@DB_PORT=$(DB_PORT) ./scripts/db-local.sh

# Before anything that needs the database: if no Postgres answers on DB_PORT
# and Docker is running, start the Docker one (scripts/db-ready.sh).
db-ready:
	@DB_PORT=$(DB_PORT) ./scripts/db-ready.sh

migrate: db-ready ## apply schema migrations (as the owner role); starts the Docker Postgres if needed
	cd $(CORE) && MIGRATE_DATABASE_URL="$(MIGRATE_DATABASE_URL)" go run ./cmd/migrate

run: migrate ## run the API on :8080 (as the app role); leave it running in its own terminal
	cd $(CORE) && DATABASE_URL="$(DATABASE_URL)" go run ./cmd/api

demo: ## walk through the P0 exit gate against a running API (make run first)
	cd $(CORE) && go run ./cmd/demo

seed: ## create a demo restaurant with a live evening in it; prints the owner token (make run first)
	cd $(CORE) && go run ./cmd/seed

web-install: ## install the back office's packages (first time, and after package.json changes)
	cd $(WEB) && npm ci

$(WEB)/node_modules: $(WEB)/package-lock.json
	cd $(WEB) && npm ci
	@touch $@

web: $(WEB)/node_modules ## run the back office on http://localhost:3000 (make run first)
	cd $(WEB) && ONEXN_API_URL=http://localhost:8080 npm run dev

test: golden db-ready ## everything: unit, golden, property and end-to-end tests
	cd $(CORE) && TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -race -count=1 ./...

test-unit: ## fast tests only, no database needed
	cd $(CORE) && go test -count=1 ./...

test-web: $(WEB)/node_modules db-ready ## back office in a real browser against the real API
	DB_PORT=$(DB_PORT) ./scripts/test-web.sh

golden: ## check the Python oracle agrees with every golden case
	python3 tools/golden/oracle.py check

fuzz: ## hunt for pricing bugs for 60 seconds
	cd $(CORE) && go test ./modules/sales/pricing -run XXX -fuzz FuzzCompute -fuzztime 60s

fmt: ## format Go code
	cd $(CORE) && gofmt -w .

vet: ## static checks
	cd $(CORE) && go vet ./...

check: ## what CI runs
	cd $(CORE) && test -z "$$(gofmt -l .)" || (echo "run make fmt" && exit 1)
	$(MAKE) vet test
	cd $(WEB) && npm run typecheck && npm test
