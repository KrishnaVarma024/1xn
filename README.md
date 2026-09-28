# 1x.n

Restaurant-first operating system for Indian F&B: POS, stock, kitchen, money and people in one product (Petpooja's speed plus Odoo's depth). Architecture and plans live in the project's Claude Docs; this repo is the code.

**Status (29 Sep 2026): P0 foundations plus the POS core engine.** A restaurant can sign up, pair a till, publish a menu, sync bills that were priced offline (retries are safe) and see them posted as balanced journal entries. The screens (Flutter POS) come next.

## What is built

| Area | What works | Code |
| --- | --- | --- |
| GST pricing engine | Integer paise, inclusive/exclusive prices, CGST+SGST or IGST, line and bill discounts split to the paisa, service charge (off by default), round-off. 227 golden cases, 20k random bills, fuzzing | `services/core/modules/sales/pricing` |
| Tenancy | Postgres Row-Level Security, app role that cannot bypass it, fail-closed tenant context, composite FKs | `migrations/0001_platform.sql`, `internal/platform/db` |
| Identity | Signup in one transaction, GSTIN check-digit validation, hashed bearer tokens | `modules/identity`, `internal/platform/auth` |
| Devices | Pairing codes (single-use, 10 min), a per-device GST invoice series `PPP-FFFF-NNNNNN` | `modules/devices`, `internal/platform/gst` |
| Catalogue | Tax profiles, categories, items, channel prices, one-tap sold out, versioned menu with ETag / 304 | `modules/catalogue` |
| Sync gateway | Exactly-once ops: inbox dedupe, device row lock, savepoints, poison ops rejected (never retried forever), commit-ordered server sequence, gap alerts | `modules/syncgw` |
| Sales | Bill ingest with server-side re-pricing and catalogue drift checks; mismatches kept and flagged, never dropped | `modules/sales` |
| Ledger | Double entry in paise, posting rule for bills, suspense for payment differences, DB-enforced balance, append-only, periods | `modules/ledger`, `migrations/0004_ledger.sql` |
| Events | Transactional outbox rows (relay to NATS comes later) | `internal/platform/outbox` |

## Run it on your Mac

One-time setup:

```bash
xcode-select --install                     # git, make, python3
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
brew install go                            # Go 1.24 or newer
brew install --cask docker                 # Docker Desktop; open it once so it starts
```

Then, in this folder:

```bash
make doctor    # checks go, docker, python3, git and that port 5433 is free
make up        # Postgres 16 in Docker, on port 5433
make test      # every test, including end-to-end against that Postgres
make run       # migrate, then start the API on :8080  (leave running)
make demo      # in a second terminal: the P0 exit gate, narrated
```

**"port is already allocated"?** Something else on your Mac already uses that port (often another Postgres). This repo uses 5433 so that is rare; if it happens, `make doctor` shows who owns the port, and `export DB_PORT=5434` moves ours.

No Docker? Use Homebrew's Postgres instead (it listens on 5432):

```bash
brew install postgresql@16 && brew services start postgresql@16
psql postgres -c "CREATE ROLE onexn_owner LOGIN SUPERUSER PASSWORD 'dev'"
psql postgres -c "CREATE DATABASE onexn OWNER onexn_owner"
psql -U onexn_owner onexn -f infra/local/init.sql
export DB_PORT=5432   # then make test / make run as usual (skip make up)
```

`make help` lists every command. The URLs are in `.env.example`.

## API (v1, this slice)

| Who | Method and path | Does |
| --- | --- | --- |
| anyone | `POST /v1/signup` | tenant + GSTIN + outlet + owner; returns the owner token once |
| anyone | `POST /v1/devices/register` | redeem a pairing code; returns the device token and invoice series |
| owner | `POST /v1/outlets/{id}/pairing-codes` | a code for a new till, captain phone, KDS or hub |
| owner | `POST /v1/catalogue/tax-profiles`, `/categories`, `/items` | build the menu |
| owner | `PATCH /v1/catalogue/items/{id}/availability` | one-tap sold out |
| owner | `GET /v1/bills/{id}` | a bill, its verification and its journal lines |
| owner | `GET /v1/ledger/trial-balance` | every account's total; `balanced: true` |
| owner | `GET /v1/alerts` | mismatches, conflicts, gaps, locked periods |
| device | `GET /v1/devices/me/menu` | menu snapshot with ETag |
| device | `POST /v1/sync/push` | up to 200 ops; one ack each |

## Layout

```
1x.n/
├── apps/                 client apps (Flutter device app is next)
├── services/core/        Go modular monolith
│   ├── cmd/              api, migrate, demo
│   ├── internal/         app wiring, platform (db, auth, gst, httpx, ids, alerts, outbox, syncop), e2e tests
│   ├── migrations/       SQL schema, embedded in the binary
│   └── modules/          identity, devices, catalogue, syncgw, sales (+ pricing), ledger
├── packages/golden/      tax golden cases shared by every engine
├── tools/golden/         Python reference oracle
├── infra/local/          local database init
└── docs/
    ├── adr/              decisions and their trade-offs
    └── learn/            how and why it works, with interview questions
```

**New here? Read `docs/learn/00-start-here.md` first.**

## Rules for this repo

- Money is `int64` paise. No floats in any money path.
- Every tenant table has `tenant_id`, RLS on, a policy, and grants to `app_rw`.
- Migrations are never edited after they are applied; add a new one.
- A pricing rule change updates `docs/learn/01`, the golden files and every engine together.
- No secrets in git: `.env` is ignored; production secrets live in AWS Secrets Manager.

## Next

1. **Flutter POS (device app):** Dart pricing engine passing the golden files, SQLite op log, order screen, bill print, push sync.
2. **Sync pull:** menu and cross-device changes by `server_seq`; outbox relay to NATS JetStream.
3. **KOT routing and the outlet hub** (LAN relay), then day close.
