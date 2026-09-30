# 05 Orders, tables, KOTs and conflicts

**Problem.** A Saturday night: the till, two captain phones and a KDS screen all change the same orders at once. The Wi-Fi drops for ten minutes and each device keeps working. A captain adds a naan to table 4 on his phone just as the cashier merges table 4 into table 5 on the till. Both actions are correct and both already happened in the real world: the naan went to the kitchen and the guests moved. When the network returns, the server must combine them without losing the naan or putting table 4 back.

**Truth to remember:** a device that was offline is not asking for permission. It is **reporting what already happened**. The server's job is to record the facts in a sensible order and **flag** the ones that break policy. It should not refuse them.

## The pieces

```
floor ──< dining_table            kitchen_section (Tandoor, Bar, ...)
                │                          │
                ▼                          ▼
pos_order (doc jsonb) ── lines[] ──── section_id ──▶ KOT routing
    │  status: open → closed | merged | cancelled
    │  merged_into ──▶ the order it was merged into (a tombstone)
    │  spillover_of ─▶ the closed order late items belonged to
    ▼
bill.order_id, and each billed line's bill_id
```

| Word | Meaning |
| --- | --- |
| **Line** | one item on an order: qty, price, modifiers, tax rate, course, section, note. Status: `held` → `fired` → `billed`, or `voided` |
| **KOT** | kitchen order ticket: lines fired together to one section. Status only moves forward: `new` → `preparing` → `ready` → `served` |
| **Course** | 1 starters, 2 mains... lets the captain hold mains and fire them later |
| **Spillover** | a new order opened automatically for items added after the table's bill was paid |
| **Tombstone** | an order that was merged away. It keeps a pointer to where its lines went |

## One reducer, pure

All the rules are in `modules/orders/domain.go`, a **reducer**: `new state = f(old state, command)`. It has no database, no clock and no network. That gives three things:

1. **Unit tests are tiny.** `domain_test.go` builds an order in memory and checks each rule, with no Postgres at all.
2. **The device can run the same rules.** The Flutter app will get a Dart port of this file, checked against the same cases, exactly like the pricing engine (note 01).
3. **The store stays thin.** `store.go` loads the order `FOR UPDATE`, calls the reducer and saves the result. All the logic lives in one place that is easy to read.

The whole order is saved as one JSONB document (`pos_order.doc`), with the columns we query on (`status`, `outlet_id`, `table_id`, `merged_into`) copied next to it. An open order is small (tens of lines), always read and written as a whole, and never joined line by line. When an order closes, the bill tables hold the relational copy that reports and GST use. See ADR 0011.

## Every op is idempotent by its own ids

The sync gateway already dedupes by `op_id` (note 03). Order ops also carry **their own ids**, made on the device: `order_id`, `line_id`, `kot_id`. So even the same fact sent under a different op id cannot happen twice:

| Op | If it arrives again |
| --- | --- |
| `order.opened` | order id exists → `duplicate` |
| `order.lines_added` | line ids already used anywhere in the tenant are skipped (`order_line_key`), wherever those lines went since: billed, voided, merged, spilled over |
| `kot.fired` | KOT id exists → no change |
| `order.line_voided` | line already voided → no change |
| `kot.status_changed` | status can only move forward; same or older → no change |
| `order.merged` | sources already merged into the target → no change, even if the target was billed since |
| `order.cancelled` | already cancelled → no change |

`TestOrderOpsAreIdempotent` sends the whole dine-in flow twice under fresh op ids. The order's version stays the same.

## Conflicts, and how each one is settled

| Race | What we do | Why |
| --- | --- | --- |
| Two captains add lines to one order | both sets land; the order row lock makes them take turns | adding is commutative: order of arrival does not matter |
| Line added to an order that was **merged** meanwhile | follow `merged_into` to the live order and add it there | the guest moved; the naan follows them |
| Line added after the bill was **paid** (order closed) | open a **spillover** order on the same table, raise `late_items` | the invoice is final (ADR 0007); the item still needs a bill |
| Fire, void or move sent to a merged order | forwarded the same way | same reason |
| **Cancel** sent to a merged order | reject `order_merged` | the lines now belong to another party, which did not leave. Following the merge would cancel the wrong table |
| Void of a line that was already sent to the kitchen, without a manager's approval | apply it, raise `void_after_kot_without_approval` | the device already did it; a manager should look |
| Void of a line that is already **billed** | reject `line_already_billed` (critical) | changing a paid invoice needs a credit note, not a void |
| Cancel an order with billed lines | reject `order_has_billed_lines` | same |
| Cancel an order whose food was already fired, without approval | apply it, raise `void_after_kot_without_approval` | served, paid in cash, then cancelled is the oldest fraud at a till |
| Order opened on a table the owner has since switched off | apply it, raise `inactive_table` | the guests are sitting there |
| KDS marks "ready" after another marked "served" | ignored | status only moves forward |
| Bill names lines that are voided, on another bill, or named twice | bill is stored, alert `bill_order_mismatch` | the printed bill is what the guest paid |
| Bill charges a line differently from the order (1 naan against a line of 4) | bill is stored, alert `bill_order_mismatch` | charging less is how food walks out unpaid |

The rule behind the table: **reject only what can never become true**, such as a malformed op, an item that does not exist, or a change to a paid invoice. For everything else, apply the fact and raise an alert. A rejected op is a hole in the history. An alert is a note for a human, and the history stays whole.

## Merge leaves a forwarding address

Merging table 4 into table 5 moves every line and KOT to order 5. It marks each line with `from_order_id` so the bill can still show where it came from. Order 4 then becomes a **tombstone**: `status = merged`, `merged_into = 5`. The database checks that `merged` and `merged_into` always go together.

Any later op for order 4 calls `resolve()`. That follows the pointer and lands on the live order. This is how DNS CNAMEs and moved web pages (HTTP 301) work.

If order 5 is later merged into order 6, the server also re-points order 4 straight at 6 (`flatten`). So forwarding is always **one hop**, however often a party moves. Without that, merging 1 into 2, 2 into 3 and so on builds a chain that grows with every merge. `resolve()` still gives up after 16 hops, so hand-edited data with a loop cannot hang the server. That case is rejected as `merge_chain_too_long`, never retried.

We could delete order 4 instead. But a captain who was offline still holds its id, and his late naan would then fail as `unknown_order`. `TestMergeForwardsLateOps` covers exactly that case.

## Locks, in one order

Deadlocks come from two transactions taking the same locks in opposite orders. Every writer here takes locks in the same order:

```
device row  →  sync_state counter (NextSeq)  →  pos_order rows
```

The gateway takes the device lock. Each applier calls `NextSeq()` **before** it touches an order, and the bill ingest does the same. `NextSeq()` locks the tenant's counter row until commit, so all writers in one tenant take turns from that point on. They can wait for each other, but they cannot deadlock over orders. `TestConcurrentCaptainsAddLines` runs five captains at once, each adding three lines, and checks that all 15 lines land. `TestConcurrentOppositeMergesDoNotDeadlock` merges A into B and B into A at the same moment, 15 times over.

## Pull: state, not ops

Devices also need changes made by *other* devices: the KDS must see new KOTs, and the till must see the captain's lines. `GET /v1/sync/pull?after=N` answers with **the current state of each order that changed after N**, once each, oldest change first. It does not send the op history.

```
after = 0   → bootstrap: every OPEN order of this outlet, cursor = tenant's last seq
after = N   → orders changed after N (from change_log), about `limit` per page, has_more if cut off
```

A page never ends in the middle of a seq. One op can change several orders under one seq: a merge saves the target and every source, and a bill can close an order and its spillover. The next page asks for `seq > cursor`, so a page that split such a group would lose the rest of it for good. So a page takes the whole group, even if that runs a little over `limit`. Pages also stop at about 4 MB of documents, again only between two seqs. `TestPullNeverSplitsASeq` pages one order at a time across a merge.

Each save writes `(seq, order_id)` to `change_log`. The query groups by order and keeps each order's latest seq. So an order changed 50 times while a device was offline arrives once, in its final form.

Why state rather than ops?

- **The device stays simple.** It replaces its copy of the order. It does not need to replay other devices' ops, or know about merges and spillovers.
- **The server stays the authority.** If a rule changes on the server, devices get the result without an app update.
- **Paging is safe.** The cursor is a seq, and seqs are handed out in commit order (note 03). The read runs in one REPEATABLE READ snapshot, so the cursor matches the data it returns.

See ADR 0012.

## The device side: rebase

This is the algorithm the Flutter app will follow. It is the same idea as `git rebase`:

```
local view of an order = server copy (from pull)  +  my unacked ops, re-applied in order
```

1. Push unacked ops. Drop each one the server acks (`applied`, `duplicate` or `rejected`).
2. Pull. For each order received, **replace** the server copy. A bootstrap (`after=0`) replaces **everything**: it lists only open orders, so any local order missing from it was closed, merged or cancelled.
3. Re-apply the ops that are still unacked on top, using the Dart port of the reducer. Ops are idempotent by their ids, so re-applying one that the server already has changes nothing.
4. Show the result.

The screen never waits for the network, and it matches the server as soon as every op is acked.

## Proof in tests

The slice went through an adversarial review by a second agent. It found eight real bugs and three smaller ones, each with a failing test. The worst: a re-sent "lines added" op could bring an already-paid paneer back as a new order, a cancel aimed at a merged-away order cancelled the other table, a long merge chain blocked a till's queue forever, and paging a pull could skip a merged order. All are fixed, and the tests live on in `internal/e2e/orders_hardening_test.go` and `modules/orders/hardening_test.go`. We checked the fixes the same way as before: undo each one, and its test fails.

- `TestDineInFlow`: a captain opens table 4, adds paneer (with extra cheese), naan and lassi, and fires a Tandoor KOT and a Bar KOT. The KDS marks its KOT ready, and a stale "preparing" that arrives later changes nothing. The till bootstraps, voids a burnt naan with approval, and bills the table in two splits. The order closes, and the books balance.
- `TestMergeForwardsLateOps`: the till merges table 5 into table 4. Then a captain who has not pulled yet adds a lassi to 5 and fires it. Both land on 4, and the till can even bill through the old id.
- `TestLateItemsGoToSpillover`: pay table 4, then a late item arrives. A spillover order appears with a `late_items` alert, and a second bill closes it.
- `TestOrderOpsAreIdempotent` and `TestConcurrentCaptainsAddLines`: see above.
- `TestPullPagingAndIsolation`: paging with `limit=1` visits every order once, in order. A second outlet sees none of the first outlet's orders.
- `TestOrderOpValidation`: wrong outlet, unknown table, item or section, a KDS trying to add lines, and a void of a billed line are all rejected with the right code.

## What would break at 10x?

- **JSONB docs rewritten on every change.** A busy table is a few KB, but a 500-line banquet can reach hundreds of KB, rewritten on every op. The document is capped at 1 MiB (`order_too_large`). If big orders become common, split lines into their own table.
- **`change_log` grows forever.** Prune rows older than the longest time a device may stay offline (72 h), plus a margin. A device whose cursor is older than that bootstraps again.
- **Pull by polling.** Every device asks every few seconds. Fix: a push channel (WebSocket or the outlet hub on the LAN) that says "pull now". The pull endpoint itself stays the same.

## Interview questions

<details><summary>1. Two users edit the same record offline. How do you merge?</summary>
It depends on the data.
Commutative changes (add a line) can simply both apply.
Changes to a single value (the table) need a rule: last writer wins, or a merge function.
Changes that break a business rule (voiding a paid line) get rejected or flagged for a human.
Name the rule for each field. Do not say "use a CRDT" and stop there.</details>

<details><summary>2. Event log or current state for sync pull?</summary>
Ops (an event log) give a full history, and let a client rebuild any past state. The cost is that every client must replay every rule correctly.
State is simpler for clients and keeps the rules on the server.
Many systems keep ops for the server's audit trail and send state to clients. That is what we do here.</details>

<details><summary>3. How do you avoid deadlocks in a transactional system?</summary>
Take locks in one global order.
Keep transactions short.
Lock the parent before the children.
Retry on 40P01, since the database picks a victim.
Document the order, so the next developer keeps it.</details>

<details><summary>4. Why keep a tombstone instead of deleting a merged record?</summary>
Clients that were offline still hold the old id.
A tombstone with a forwarding pointer turns their late writes into correct writes, instead of errors.
The same trick appears in DNS CNAMEs, HTTP 301, and deleted markers in Cassandra.</details>

## Exercises

1. Draw the timeline for the naan example in the Problem section: the merge acked first, then the naan. Then the other way round: the naan acked first, then the merge. Do both end with the same order 5? Why?
2. Add a `order.covers_changed` op. Which conflict rule fits it: commutative, last writer wins, or reject?
3. In the `order.lines_added` applier, move the `NextSeq()` call to after the order is loaded. Then write a test where a captain adds lines while the till pushes a bill for the same order, at the same moment, 20 times. What error appears, and which two locks explain it?
