# 04 The double-entry ledger

**Idea (about 700 years old, still undefeated).** Money never appears or vanishes; it moves. Each movement is written as lines that **sum to zero**: debits positive, credits negative. If every entry sums to zero, the whole book sums to zero. A trial balance that does not balance means a bug, and you know it immediately.

## One bill, as the accountant sees it

₹95 pav bhaji, paid by UPI (`make demo` prints this):

| Account | Dr | Cr | Why |
| --- | ---: | ---: | --- |
| 1110 UPI clearing | 100.00 | | money the bank will settle to us |
| 4100 Food and beverage sales | | 95.00 | our revenue |
| 2210 Output CGST | | 2.38 | owed to the central government |
| 2220 Output SGST | | 2.38 | owed to the state |
| 4900 Round-off | | 0.24 | the paise rounding added |
| **Total** | **100.00** | **100.00** | sums to zero |

GST collected is **not revenue**. It is a liability, money we hold for the government. Owners who see ₹100 as sales are overstating revenue by the tax. The ledger keeps the two apart from the first bill.

## Posting rules are pure functions

`ledger.BillSettledEntry(bill) → entry` has no database and no clock. It is tested alone in `rules_test.go`: split tender, tips, negative round-off, underpayment. The same function can later run in an asynchronous consumer of `sales.bill.settled.v1` events, without changing.

**Underpayment.** The device recorded ₹90 against a ₹100 bill. We do not block the entry, and we do not invent money. The ₹10 goes to **1190 Suspense**, the entry still balances, and an alert tells a human.

## The database polices the books

Service code has bugs, and code written next year will have different bugs. So Postgres enforces the rules (`0004_ledger.sql`):

| Rule | How |
| --- | --- |
| Every entry sums to zero, with 2+ lines | deferred constraint trigger, checked at COMMIT |
| Lines use accounts of the entry's own GSTIN | same trigger |
| Only into an open period that contains the date | BEFORE INSERT trigger |
| Never edited, never deleted | no UPDATE/DELETE grant for the app role, and triggers that stop even the owner |
| One entry per business event | `source_event_id UNIQUE` |

**Why deferred?** Lines are inserted one at a time. Midway through, an entry is unbalanced, and that is fine. The check waits for COMMIT, when all lines are in.

**Two holes the review found, and how they were closed:**
- *Growing a posted entry.* The balance check only asks "does it sum to zero?", so code could append a balanced pair of lines to last month's entry. Now lines can only be inserted in the transaction that created their entry.
- *Racing the month-end lock.* The period check was a plain read, so an accountant could lock September between an entry's check and its COMMIT. Now the period row is read `FOR SHARE`, so the lock waits for in-flight entries. Locked months also cannot be re-opened by the app.

**Comped bills.** A 100% discount means no money moved. The bill is stored (it used an invoice number), with `ledger_status = none`, and no entry is posted.

**Mistakes** are fixed with a **reversing entry**, never an edit. History then shows what we believed, when, and when we corrected it. Auditors and the GST department both care about that.

**Locked months.** After the accountant closes September, a late-syncing bill dated September is kept (`ledger_status = blocked`), and an alert asks a human to post it on purpose. `TestLedgerInvariants` covers this and the other rules.

## Two dates on every entry

- `entry_date`: the invoice date in IST (the GST tax point). A bill printed at 00:30 on 1 April is in the new financial year.
- `business_date`: the restaurant's trading day. That 00:30 bill still belongs to the 31 March night shift for day close.

Mixing these up is a classic restaurant-software bug.

## What would break at 10x?

- `journal_line` grows by about 7 rows per bill. Partition it monthly (planned in the architecture doc).
- The balance trigger runs once per line. For large entries (payroll, for example), switch to a statement-level trigger with transition tables.
- Trial balances over years get slow. Keep monthly per-account balance snapshots, written when a period closes.

## Interview questions

<details><summary>1. Why double entry instead of a single "balance" column?</summary>
A balance column loses history, and cannot prove itself correct.
Double entry keeps every movement, and has a built-in checksum: everything sums to zero.
Balances are derived from it and can always be rebuilt.</details>

<details><summary>2. How do you correct a posted transaction in an append-only ledger?</summary>
Post a reversing entry (same lines, signs flipped, linked with reverses_id), then post the correct entry.
Never UPDATE.</details>

<details><summary>3. Where should invariants live: app or database?</summary>
Both. The app gives early, readable errors.
The database is the last line of defence, and it holds for every writer, including future services and manual fixes.</details>

<details><summary>4. A payment of ₹90 against a ₹100 invoice arrives. What do you post?</summary>
Dr cash 90, Dr suspense 10, and the normal credits for the full invoice.
Alert a human, who will later move the 10 to the right account (receivable, write-off, or staff shortage).</details>

## Exercises

1. Write the entry for a ₹1,000 bill with a ₹100 tip, paid ₹600 by card and ₹500 by cash. Check it with `ledger.BillSettledEntry` in a test.
2. Add a posting rule for a **cash payout** (petty cash spent on vegetables). Which accounts does it touch?
3. Why is `source_event_id` unique? What goes wrong in a retry without it?
