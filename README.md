# 1x.n

Restaurant-first operating system for Indian F&B: POS, stock, kitchen, money and people in one product (Petpooja's speed plus Odoo's depth). Architecture and plans live in the project's Claude Docs; this repo is the code.

**Status (3 Oct 2026): P0 foundations, the POS core engine, the restaurant floor, the back office web app, day close, and stock with recipes.** A restaurant can sign up, pair a till, publish a menu, sync bills that were priced offline (retries are safe) and see them posted as balanced journal entries. Captains, tills and kitchen screens can now run tables together: orders, KOTs by kitchen section, voids, table moves and merges, split bills, and pull of other devices' changes, all offline-first. Owners run it from a web back office: menu, tables, devices, a live floor, bills, alerts and books. Tills run cash shifts (float, expenses, safe drops, counted cash), and the owner closes each day from its Z report, with drawer differences booked and late bills flagged. Dishes have recipes, so every bill uses up its ingredients as it syncs: stock, its value and the day's food cost are always current. The device app (Flutter) comes next.

## What is built

| Area | What works | Code |
| --- | --- | --- |
| GST pricing engine | Integer paise, inclusive/exclusive prices, CGST+SGST or IGST, line and bill discounts split to the paisa, service charge (off by default), round-off. 227 golden cases, 20k random bills, fuzzing | `services/core/modules/sales/pricing` |
| Tenancy | Postgres Row-Level Security, app role that cannot bypass it, fail-closed tenant context, composite FKs | `migrations/0001_platform.sql`, `internal/platform/db` |
| Identity | Signup in one transaction, GSTIN check-digit validation, hashed bearer tokens | `modules/identity`, `internal/platform/auth` |
| Devices | Pairing codes (single-use, 10 min), a per-device GST invoice series `PPP-FFFF-NNNNNN` | `modules/devices`, `internal/platform/gst` |
| Catalogue | Tax profiles, categories, items, channel prices, one-tap sold out, kitchen sections, modifier groups, floors and tables, versioned menu with ETag / 304 | `modules/catalogue` |
| Orders | Tables, lines with modifiers and courses, KOTs per kitchen section, KDS status, voids (with approval or an alert), table moves, merges (with forwarding), late items (spillover order), split bills linked to lines. A pure reducer; conflicts settled by rule, not by refusal | `modules/orders` |
| Sync pull | Other devices' changes as current order state, by a commit-ordered cursor, paged; bootstrap on first pull | `modules/orders/http.go` |
| Back office | Next.js web app: sign up / sign in, menu builder (GST rates, categories, kitchen sections, add-ons, dishes, sold out), floors and tables, pair and revoke devices, live floor, order and bill pages, day's bills, day close (Z report, print), stock (items, deliveries, wastage, counts, food cost), recipes with costing, alerts, trial balance. The browser never sees the token (httpOnly cookie, BFF) | `apps/backoffice` |
| Day close | Till shifts (float, expenses, safe drops, count with notes and coins), expected cash worked out on the server, differences posted to Cash over and short, the Z report (sales, GST by rate, payments, drawers, invoice gaps, voids, best sellers), a digest-guarded close, late records kept and shown as amendments, a revoked till's shift counted and closed from the back office, change-log pruning with `410` for devices too far behind | `modules/dayclose` |
| Stock and recipes | Ingredients in kg, litres or pieces (integer milli-units), effective-dated recipes per dish, perpetual stock at weighted average cost used up by each bill, purchases (on credit, by bank, from the safe or from a till drawer, matched through a clearing account), wastage, counts, true-ups for stock sold before it was booked in, low and negative stock alerts, recipe costing and daily food cost | `modules/stock` |
| Demo data | `make seed`: a restaurant with a live evening (3 tables eating, 1 paid, 2 takeaways, a void, one cash shift closed ₹50 short and one open, 17 ingredients with recipes for every dish, three deliveries, curd running low) made through the real API | `cmd/seed` |
| Sync gateway | Exactly-once ops: inbox dedupe, device row lock, savepoints, poison ops rejected (never retried forever), commit-ordered server sequence, gap alerts | `modules/syncgw` |
| Sales | Bill ingest with server-side re-pricing and catalogue drift checks; mismatches kept and flagged, never dropped | `modules/sales` |
| Ledger | Double entry in paise, posting rule for bills, suspense for payment differences, DB-enforced balance, append-only, periods | `modules/ledger`, `migrations/0004_ledger.sql` |
| Events | Transactional outbox rows (relay to NATS comes later) | `internal/platform/outbox` |

## Run it on your Mac

One-time setup:

```bash
xcode-select --install                     # git, make, python3
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
brew install go node                       # Go 1.24+ and Node 22+
```

Then pick **one** Postgres. Docker is the simplest; use Homebrew only if you already run your own Postgres:

| Docker (`brew install --cask docker`, open it once) | Homebrew Postgres (no Docker) |
| --- | --- |
| nothing more: `make run` starts Postgres 16 on port 5433 when it is not running (so does `make up`) | `brew install postgresql@16 && brew services start postgresql@16` |
| | `echo "DB_PORT=5432" >> .env` (once; every terminal reads it) |
| | `make db-local` (once: creates the database and its roles) |

`make doctor` checks all of this and says what to do about anything missing.

**Three terminals**, each in this folder. The API must keep running while you use the others:

| Terminal | Command | What you see |
| --- | --- | --- |
| 1 | `make run` | migrations, then `api listening :8080`. Leave it running. |
| 2 | `make seed` | a demo restaurant with a live evening; prints its **owner token** |
| 3 | `make web` | the back office; open **http://localhost:3000** and paste the token |

Then `make test` (every Go test) and `make test-web` (the back office in a real browser). `make demo` narrates the P0 exit gate against the running API. `make help` lists every command.

When something fails, the command says why and what to run. The usual ones:

| You see | It means | Do |
| --- | --- | --- |
| `connection refused` on 5433 or 5432 | no Postgres on that port, and Docker is not running | open Docker Desktop and run it again; or option B printed below the error |
| `database "onexn" does not exist`, `password authentication failed` | your own Postgres is not set up for 1x.n yet | `make db-local` |
| `address already in use` on :8080 | the API is already running in another terminal | use that one, or Ctrl-C it there |
| `Can't reach the 1x.n API` in the back office | terminal 1 is not running `make run` | start it |
| Safari: "Can't find the server www.localhost.com" or "Connection is not secure" | Safari could not reach `localhost:8080` (the API was not running) and guessed `https://www.localhost.com` | open the back office, `http://localhost:3000`, with the `http://`. The API is not a website: only the back office talks to it (`http://localhost:8080/healthz` answers 200 when it is up) |
| Next.js: "Blocked cross-origin request" | you opened the Network address (`http://192.168...:3000`) | fine on this Mac or a phone on the same Wi-Fi now; restart `make web` after changing networks |
| vi: `E325: ATTENTION` ... `.swp` | another vi has that file open, or one crashed | close the other vi; if vi offers "(D)elete it", press `D` |
| "port is already allocated" from `make up` | something else on your Mac already uses 5433 | `make doctor` shows who; `echo "DB_PORT=5434" >> .env` moves ours |

## API (v1, this slice)

| Who | Method and path | Does |
| --- | --- | --- |
| anyone | `POST /v1/signup` | tenant + GSTIN + outlet + owner; returns the owner token once |
| anyone | `POST /v1/devices/register` | redeem a pairing code; returns the device token and invoice series |
| owner | `GET /v1/me` | the business, the signed-in owner and their outlets |
| owner | `GET /v1/outlets/{id}/menu` | the outlet's full menu, floors and tables (what its devices get) |
| owner | `POST /v1/outlets/{id}/pairing-codes` | a code for a new till, captain phone, KDS or hub |
| owner | `GET /v1/outlets/{id}/devices`, `POST /v1/devices/{id}/revoke` | list devices; cut off a lost one at once |
| owner | `POST /v1/outlets/{id}/floors`, `/tables`; `PATCH /v1/outlets/{id}/tables/{table}` | lay out the dining room; relabel, resize or switch a table off |
| owner | `POST /v1/catalogue/tax-profiles`, `/categories`, `/sections`, `/modifier-groups`, `/items` | build the menu and route it to kitchen sections |
| owner | `PATCH /v1/catalogue/items/{id}` | change a dish: name, price, category, GST, section (`null` clears), add-ons, channel prices |
| owner | `PATCH /v1/catalogue/items/{id}/availability` | one-tap sold out |
| owner | `GET /v1/outlets/{id}/orders` | the live floor: every open order |
| owner | `GET /v1/orders/{id}` | one order with its lines, KOTs and bill links |
| owner | `GET /v1/bills?outlet_id=&date=` | a business day's bills, totals and payments by method |
| owner | `GET /v1/bills/{id}` | a bill: lines, taxes, payments, verification and journal lines |
| owner | `GET /v1/outlets/{id}/days` | the trading day now and the last 60 closed days |
| owner | `GET /v1/outlets/{id}/days/{date}` | the Z report, live; for a closed day also the closing copy and what changed since |
| owner | `POST /v1/outlets/{id}/days/{date}/close` | close the day: `{digest, acknowledge_warnings}`; 409 if the figures changed |
| owner | `GET /v1/outlets/{id}/shifts` | open till shifts, whatever their day, with what each drawer should hold now |
| owner | `POST /v1/outlets/{id}/shifts/{shift}/close` | count a revoked till's drawer and close its shift: `{id, counted_paise, note}` |
| owner | `POST /v1/stock/items`, `PATCH /v1/stock/items/{id}` | stock items: name, unit (kg, l, pc), reorder level |
| owner | `PUT /v1/stock/recipes/{menuItem}` | what one portion of a dish uses |
| owner | `GET /v1/outlets/{id}/stock`, `/stock/items/{item}` | what the outlet holds and what it is worth; one item's history |
| owner | `POST /v1/outlets/{id}/stock/purchases`, `/wastage`, `/counts` | stock documents, each with an idempotency id |
| owner | `GET /v1/outlets/{id}/stock/recipes`, `/stock/food-cost?from=&to=` | recipe costing; food cost against sales |
| owner | `GET /v1/ledger/trial-balance` | every account's total; `balanced: true` |
| owner | `GET /v1/alerts` | mismatches, conflicts, gaps, locked periods |
| device | `GET /v1/devices/me/menu` | menu snapshot with ETag |
| device | `POST /v1/sync/push` | up to 200 ops; one ack each. Ops: `bill.settled`, `order.opened`, `order.lines_added`, `kot.fired`, `kot.status_changed`, `order.line_voided`, `order.moved`, `order.merged`, `order.cancelled`, `shift.opened`, `cash.moved`, `shift.closed` |
| device | `GET /v1/sync/pull?after=N` | orders changed by any device since cursor N (`after=0` bootstraps; `410 cursor_expired` means bootstrap again) |

## Layout

```
1x.n/
├── apps/
│   └── backoffice/       Next.js owner web app (Flutter device app comes next)
├── services/core/        Go modular monolith
│   ├── cmd/              api, migrate, demo, seed
│   ├── internal/         app wiring, platform (db, auth, gst, httpx, ids, alerts, outbox, syncop), e2e tests
│   ├── migrations/       SQL schema, embedded in the binary
│   └── modules/          identity, devices, catalogue, orders, syncgw, sales (+ pricing), ledger, dayclose, stock
├── packages/golden/      tax golden cases shared by every engine
├── tools/golden/         Python reference oracle
├── infra/local/          local database init
├── scripts/              test-web.sh (browser tests)
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

1. **Flutter POS (device app):** Dart pricing engine and order reducer passing the shared cases, SQLite op log, table and order screens, KOT and bill print, push and pull sync. Blocked until the sandbox can reach `storage.googleapis.com` and `pub.dev`.
2. **Stock, next steps:** recipes for modifiers, purchase units (a 25 kg sack), suppliers and payables, purchase orders from low stock.
3. **Outlet hub** (LAN relay, so tables keep syncing between devices when the internet is down), push hints instead of polling, outbox relay to NATS JetStream.
