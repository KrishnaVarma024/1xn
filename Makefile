# 1x.n developer commands. Run `make help` for the list.
#
# Two database roles on purpose (see docs/learn/02-tenancy-and-rls.md):
#   onexn_owner  owns the schema, runs migrations
#   onexn_app    what the API uses; Row-Level Security applies to it

#
# Postgres listens on host port 5433, not the default 5432, so it never fights
# with another Postgres already on your Mac. Use another port with, for example:
#   export DB_PORT=5434      (then every make command uses it)

DB_PORT              ?= 5433
MIGRATE_DATABASE_URL ?= postgres://onexn_owner:dev@localhost:$(DB_PORT)/onexn?sslmode=disable
DATABASE_URL         ?= postgres://onexn_app:dev@localhost:$(DB_PORT)/onexn?sslmode=disable
TEST_DATABASE_URL    ?= postgres://onexn_owner:dev@localhost:$(DB_PORT)/postgres?sslmode=disable
CORE := services/core
WEB  := apps/backoffice
export ONEXN_DB_PORT := $(DB_PORT)

.PHONY: help doctor up down reset migrate run demo seed web web-install test test-unit test-web golden fuzz fmt vet check

help: ## list commands
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  make %-10s %s\n", $$1, $$2}'

doctor: ## check your tools, Docker and the database port
	@for t in go docker python3 git node npm; do \
	  if command -v $$t >/dev/null; then echo "ok       $$t"; else echo "MISSING  $$t (see README: Run it on your Mac)"; fi; done
	@if command -v node >/dev/null && [ "$$(node -p 'process.versions.node.split(".")[0]')" -lt 22 ]; then \
	  echo "PROBLEM  Node $$(node -v) is too old for the back office: install Node 22 or newer (brew install node)"; fi
	@if docker info >/dev/null 2>&1; then echo "ok       Docker is running"; \
	  else echo "PROBLEM  Docker is installed but not running: open Docker Desktop"; fi
	@if ! lsof -nP -iTCP:$(DB_PORT) -sTCP:LISTEN >/dev/null 2>&1; then echo "ok       port $(DB_PORT) is free"; \
	  elif docker compose ps --status running 2>/dev/null | grep -q postgres; then echo "ok       port $(DB_PORT) is our Postgres"; \
	  else echo "PROBLEM  port $(DB_PORT) is taken by:"; lsof -nP -iTCP:$(DB_PORT) -sTCP:LISTEN; \
	       echo "         pick another port: export DB_PORT=5434"; fi

up: ## start Postgres in Docker (port 5433, or DB_PORT) and wait until healthy
	docker compose up -d --wait

down: ## stop Postgres (data is kept in a Docker volume)
	docker compose down

reset: ## stop Postgres AND delete its data
	docker compose down -v

migrate: ## apply schema migrations (as the owner role)
	cd $(CORE) && MIGRATE_DATABASE_URL="$(MIGRATE_DATABASE_URL)" go run ./cmd/migrate

run: migrate ## run the API on :8080 (as the app role)
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

test: golden ## everything: unit, golden, property and end-to-end tests
	cd $(CORE) && TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -race -count=1 ./...

test-unit: ## fast tests only, no database needed
	cd $(CORE) && go test -count=1 ./...

test-web: $(WEB)/node_modules ## back office in a real browser against the real API (make up first)
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
