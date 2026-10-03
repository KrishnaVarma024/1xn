# 07 Day close: cash drawers and the Z report

**Problem.** At the end of a shift a cashier counts the drawer and hands over. At the end of the day the owner wants one page: what we sold, how it was paid, how much GST we owe, whether every drawer matched, what was voided, and whether any bill is missing. In a busy Indian restaurant the till may be offline when all this happens, bills can sync hours later, and the two oldest frauds at a till are cash that goes missing and food that is voided after it was cooked.

**Truth to remember:** the till reports facts (I opened with ₹2,000, I paid ₹300 for ice, I counted ₹1,845). The server works out what the drawer *should* hold, from the bills it has. A till never tells the server its own expected cash, because then a till could hide a shortage.

## A shift, from float to count

```
 TILL (offline-capable)                                 SERVER
 ──────────────────────                                 ──────
 shift.opened   float ₹2,000, "Asha", 11:00   ───▶  till_shift row (open)
 bill.settled   cash ₹420     12:30           ───▶  bill + bill_payment (cash)
 bill.settled   UPI ₹830      13:00           ───▶  (not cash: not in the drawer)
 cash.moved     expense ₹300 "Ice", 14:00     ───▶  cash_move row + Dr 5200 Petty expenses / Cr 1100 Cash
 cash.moved     to_safe ₹1,000, 15:00         ───▶  cash_move row + Dr 1105 Cash in safe / Cr 1100 Cash
 shift.closed   counted ₹1,070 in notes, 16:00 ───▶  expected = 2,000 + 420 − 300 − 1,000 = ₹1,120
                                                     difference = 1,070 − 1,120 = ₹50 short
                                                     Dr 5900 Cash over and short ₹50 / Cr 1100 Cash ₹50
                                                     alert cash_difference (critical above ₹500)
```

Five things make this safe:

1. **Cash sales are found by till and time.** A shift's cash sales are the cash payments on that till's bills between `opened_at` and `closed_at`. One till's clock is consistent with itself, and its ops arrive in the order it made them, so every bill of the shift is in before its `shift.closed` op.
2. **Shifts of one till never overlap.** A second `shift.opened` while one is open is refused (`shift_already_open`), and so is one that opens before the last one closed (`shift_overlaps`). So a bill belongs to at most one shift, and no rupee is counted twice.
3. **Every payout is a cash move.** Expenses, cash to and from the safe, and suppliers paid from the drawer (`supplier`, matched with the owner's delivery through account 1150, note 08) all change what the drawer should hold.
4. **The count is checked.** If the till sends the notes and coins (`{"500": 2, "50": 1, "20": 1}`), they must add up to the counted amount, and only real Indian denominations are accepted.
5. **The difference is booked, not hidden.** Over or short, it posts to account 5900 Cash over and short, so the books always match the cash that is really there.

## The trading day starts at 04:00

A bar that closes at 01:30 is still selling "yesterday". Bills carry the business date the till gave them (the issue date or the day before, checked on the way in). Voids and cancellations have only a time, so the server places them by a cutoff: a trading day runs from 04:00 to 04:00 in India. `TradingDay(now)` is also what the back office opens by default.

## The Z report is computed, never counted up

`dayclose.Build` reads the stored bills, payments, tax lines, orders and shifts and adds them up. There are no running counters to keep in step. That matters because records keep arriving after the day ends: a till that was offline syncs its last two bills at 11 pm, or the next morning.

It shows sales (items, discounts, taxable value, CGST/SGST/IGST, round-off, total), GST by rate (what GSTR-1 asks for), payments and channels, each drawer with float, cash sales, paid out, safe, expected, counted and difference, invoice numbers per till with any gaps, voids (and whether the kitchen already had the food, and who approved it), cancelled orders, best sellers, and warnings.

## Closing the day: "close what you saw"

When the owner closes a day, the server stores the report as it is at that moment (`day_close.report`). Two rules guard that moment:

- **The digest.** Every report carries a digest, a hash of everything it says. The close request sends the digest of the report the owner was looking at. The server rebuilds the report in a REPEATABLE READ transaction (one snapshot for every query) and refuses with `409 report_changed` if the digest differs. This is optimistic concurrency, the same idea as an HTTP `If-Match` header: no lock is held while a person reads, and a write based on stale data is refused instead of silently accepted. The back office then re-renders with the new figures.
- **Warnings must be acknowledged.** Some warnings are notes (a drawer was short, cash taken outside a shift). Others need a decision before closing: a shift still open, a table still eating, a missing invoice number. Closing with those needs `acknowledge_warnings: true`, a tick box in the back office.

## After closing: kept, flagged, amended

A bill for a closed day is never refused (ADR 0007: a guest paid and an invoice exists). It is stored, posted, and raises `bill_after_day_close`. A shift that arrives late raises `shift_after_day_close`. The day page then compares the stored report with a fresh one and shows **Amended after closing** with each settled figure that moved: "Bills: 3 → 4", "Sales: ₹1,659 → ₹1,764". Figures that move on their own (an open shift's running cash, which tables are still eating) are not amendments.

## Invoice numbers must have no gaps

GST expects every invoice number in a series to be accounted for. Each till numbers its bills 1, 2, 3 afresh each financial year. For each till, the report starts from the till's last number before the day (or from 1 for a new series) and lists every number up to the day's last one that is not on the server. Numbers are looked up a week either side, so a night bill filed under the neighbouring business day is not reported missing. A missing number usually means a bill that has not synced yet.

## Housekeeping: pruning the change log

Devices pull "every order changed after my cursor" from `change_log` (note 05). It grows forever unless pruned. Closing a day also deletes the tenant's changes older than seven days and raises `sync_state.change_log_floor`. A device that pulls from below the floor gets `410 cursor_expired` and bootstraps again (`after=0`), which downloads every open order and loses nothing.

The floor is the **highest** seq deleted, not the lowest kept. Seqs are handed out in commit order, but `created_at` is the transaction's start time, so the rows older than a week are not always a neat prefix of the log. With the highest deleted seq as the floor, every cursor at or above it still finds every change after it.

The floor is per tenant, but a device pulls only its own outlet's changes. So a pull that has caught up moves the cursor to the tenant's latest seq, even when its outlet had nothing new. Otherwise a till at a quiet outlet would keep an old cursor and be told "offline too long" while it was online all along.

## What the review broke

A second agent attacked this slice and proved ten bugs, each with a failing test (`internal/e2e/dayclose_review_test.go`). The ones worth remembering:

- **A void could vanish from every Z report.** Voids were read from the order documents of orders open during the day. A table voided at 23:30 and merged at 05:00 into a table opened the next day carried its void into an order the report never looked at. Fix: an append-only **void log** (`order_void`), written when a line is voided and read by the time it happened. The order document stays the truth; the log is a copy shaped for the question "what was voided between these two times". This is the same idea as an index or a read model: store data a second time, in the shape a query needs.
- **The digest included figures that move on their own.** An open shift's cash is counted up to *now*, so a till billing the next morning on a shift opened last night changed last night's digest, and the owner could never close it. Fix: the digest covers only settled figures (bills, taxes, payments, invoice numbers, voids, shifts' counts, cash moves), not running ones.
- **A cash bill that arrived after its shift was counted was in no drawer.** The drawer looked over by exactly that bill, and the "gain" went to the books. Fix: when such a bill lands, `dayclose.LateCash` recomputes the shift's expected cash and posts an adjusting entry, so the shift and the books are right again, and raises `cash_after_shift_close`.
- **Ledger idempotency was keyed on the event id alone.** A till that reused one id for a shift and a cash move got two events booked as one. Fix: posting is idempotent on (source type, event id).
- **Dates came from the device's UTC offset.** A till that sends `Z` times booked a 01:00 India expense on the previous day, in the previous month. Fix: device times are converted to India time (and rounded to microseconds, as Postgres stores them) before anything is derived from them.
- **The close did not take turns with sync.** A bill committing during the close was neither in the snapshot nor flagged. Fix: the close first locks the tenant's `sync_state` row, the same row every applied op locks for its seq.

The others: cash moves dated outside their shift (now refused), drawer entries kept out of a locked month not counted in the report's "not posted" check, and a resent shift close with nanosecond times taken for a different close.

## Proof in tests

| Test | Proves |
| --- | --- |
| `TestDayClose` | a full day: drawer ₹50 short posted and alerted, expense and safe drop in the books, voids, a cancelled order, an open table, warnings, the digest and acknowledgement rules, closing twice refused, a late bill flagged and shown as an amendment |
| `TestShiftRules` | role, business date, UUIDv7, duplicates, one open shift per till, wrong till, bad kinds and reasons, denominations, closing before opening, re-close, moves after close, overlapping shifts |
| `TestInvoiceGaps` | a gap inside a day, across midnight, cleared when the bill syncs, and a new series that starts at 3 |
| `TestCashOutsideShifts` | cash taken while no shift was open is reported |
| `TestPullCursorExpired` | pruning on close, the floor, 410 below it, catching up at it, bootstrap still complete |
| `TestLateCashCorrectsTheDrawer` | a cash bill after the count corrects the shift and the books, once |
| `TestReviewF1` to `F10` | the ten review findings above stay fixed |
| `TestDayCloseGuards` | other tenants get 404, impossible dates 400, a day that has not started cannot be closed, a device cannot read the report |
| `e2e/dayclose.spec.ts` | the owner's view: Z report, close, acknowledge, a stale page refused and refreshed, amended banner |

## What would break at 10x?

- **The report is rebuilt on every view.** For one outlet that is a few indexed queries. A 500-outlet chain's "all outlets" report would want daily summary rows written at close (the snapshot is already one).
- **One time zone.** The 04:00 cutoff and the dates use India time. `outlet.timezone` exists for the day a restaurant opens in Dubai.
- **Pruning rides on day close.** A tenant that never closes a day never prunes. A scheduled job (per tenant, in batches) is the grown-up version; the function is already separate (`orders.PruneChangeLog`).
- **Shifts are per till.** Restaurants where two cashiers share one drawer, or one cashier moves between tills, need cashier-level sessions on top.

## Interview questions

<details><summary>1. What is the difference between an X report and a Z report?</summary>
An X report is a reading taken during the day; it changes nothing.
A Z report closes the day: on old cash registers it reset the counters to zero (hence Z).
Here the "reset" is the stored closing snapshot; the figures themselves are always recomputed from records.</details>

<details><summary>2. Why does the server, not the till, compute expected cash?</summary>
The till is the device being audited.
If it reported its own expected cash, a tampered till could make any shortage disappear.
The server derives it from bills and cash moves it already holds, so the only number the till supplies is the count.</details>

<details><summary>3. How does the close avoid a race with a bill that syncs while the owner reads?</summary>
Optimistic concurrency: the client sends the digest of what it saw; the server recomputes in one snapshot and refuses if it differs.
No lock is held while a person reads.
A bill that commits after the close is kept and shows up as an amendment.</details>

<details><summary>4. Why keep a bill that arrives for a closed day instead of refusing it?</summary>
The invoice was printed and the guest paid; refusing it would lose real money and break the invoice series.
Records follow reality; reports show where reality changed after the fact.</details>

<details><summary>5. Why is the change-log floor the highest deleted seq?</summary>
Deletion is by age, and age (transaction start) does not follow seq (commit order) exactly.
A floor at the lowest kept seq could let a cursor sit above a deleted row it never saw.
The highest deleted seq guarantees every cursor at or above it misses nothing.</details>

## Exercises

1. Remove the `shift_overlaps` check and write a test where two shifts of one till share a bill. What happens to the second shift's difference?
2. Add an `X report` endpoint that returns the live report without closing. Which code do you reuse, and which check would you leave out?
3. Change the cutoff from 04:00 to 06:00. Which tests break, and why?
