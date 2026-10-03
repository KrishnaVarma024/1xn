# 08 Stock and recipes: food cost every day

**Problem.** A restaurant's biggest cost after rent and salaries is what goes into the food, usually 25 to 35% of sales. Most owners only find out what it was at a month-end count, too late to fix a portion size, a supplier price or a leak. They need: what is in the store, what it is worth, what each dish costs to make, and what the kitchen really used, every day.

**Truth to remember:** stock is money sitting on a shelf. Every gram that comes in or goes out moves money in the books too, and the two must always agree.

## Units without fractions

Quantities are whole numbers in thousandths of the item's unit, "milli-units": grams for an item counted in kg, millilitres for litres, thousandths of a piece. Half a lemon is 500. Like paise for money, this keeps every sum exact; floating point never touches a quantity. The back office converts only at the edges: `parseQty("250 g", "kg")` is 250, `formatQty(1250, "kg")` is "1.25 kg".

## Recipes use up stock as bills sync

A recipe says what one portion uses: butter naan = 100 g flour + 15 g butter + 10 g curd. When a till's bill syncs, `stock.ConsumeForBill` multiplies each dish's recipe by the portions sold and takes the ingredients out of the outlet's stock, in the same transaction as the bill. The bill's line `ref` is the menu item's id; that is how a bill finds its recipes. Open items typed at the till have no recipe and use nothing.

Recipes are **effective-dated**. Saving a recipe ends the current lines (`valid_to`) and starts new ones (`valid_from`), and a bill uses the recipe in force when it was **issued**, not when it synced. A till offline all evening still uses up what the kitchen cooked by, even if the owner changed the recipe in between. A dish's first recipe counts from the start, because it describes how the dish has always been made. This is the same idea as a price list or a tax rate with "valid from" dates: history is never rewritten.

```
 bill.settled ──▶ sales.ApplySettledBill
                    ├─ bill, lines, ledger entry (sales, GST)
                    └─ stock.ConsumeForBill
                         ├─ recipes × portions = grams per ingredient
                         ├─ lock balances (item-id order), take stock out at average cost
                         ├─ stock_doc (kind sale) + stock_move per ingredient
                         └─ ledger: Dr 5100 Cost of food sold / Cr 1300 Stock
```

This is **perpetual** inventory: the books know the stock at every moment, not only after a count. The other kind, **periodic**, counts at month end and works out cost as opening + purchases − closing. Perpetual needs recipes; it pays back with a food cost you can read every morning.

## Weighted average cost, with no rounding dust

Each outlet's balance of an item holds a quantity and a value. A purchase adds both. Stock that leaves takes **its share of the value**: `value × q / qty`. When the last gram leaves, the share is the whole value, so a balance at zero is always worth exactly zero. Rounding errors cannot pile up. `TestValueAlwaysReconciles` runs 5,000 random histories and checks after every step that value in − value out = the balance.

## Selling what was never booked in

Kitchens use stock before anyone records the delivery. 1x.n never refuses a bill for that (the guest ate). The stock goes negative, the missing quantity leaves at the **last purchase price** as an estimate, and a `negative_stock` alert asks someone to book the delivery. When the delivery is booked, the estimate is **trued up**: those units now have a real price. If 500 g of flour left at Rs 40/kg (Rs 20) and arrives at Rs 50/kg, the true-up adds Rs 5 to food cost, and the rest of the delivery is worth exactly its own price. `Balance.In` returns the true-up; the purchase's journal entry books it.

## Paying a supplier

A delivery is paid one of four ways, and each credits a different account:

| Paid by | Account | Notes |
| --- | --- | --- |
| On credit | 2100 Payable to suppliers | settled later |
| Bank or UPI | 1140 Bank account | |
| Cash from the safe | 1105 Cash in safe | |
| Cash from a till drawer | 1150 Supplier payments from tills | the cashier also records a `supplier` cash move on the till |

The till drawer is the tricky one. Its cash is counted against what it should hold (note 07), so if the vegetable vendor is paid Rs 500 from the drawer, the drawer must know, or the shift closes Rs 500 short and the money is booked twice: once by the purchase and once as a shortage. So both sides record their part. The till's payout lowers the drawer's expected cash and books Dr 1150 / Cr 1100 Cash in hand. The owner's delivery books Dr 1300 Stock / Cr 1150. **Account 1150 is a clearing account:** when both sides were recorded, it is back to zero. When it is not zero, one side is missing, and the balance says which.

## Documents, and doing them once

Every change is a stock **document**: a purchase (goods received), the stock a bill used, a wastage, or a count. Each has its stock moves (append-only, like the journal) and one journal entry:

| Document | Stock | Books |
| --- | --- | --- |
| Purchase | + quantity, + cost | Dr 1300 Stock, Cr 1100 Cash / 1140 Bank / 2100 Payable to suppliers |
| Bill | − recipe × portions | Dr 5100 Cost of food sold, Cr 1300 |
| Wastage | − quantity | Dr 5110 Food wastage, Cr 1300 |
| Count | to what was counted | Dr 5120 Count differences, Cr 1300 (reversed when stock is found) |

The back office makes a document's id (a UUIDv7) when it shows the form. A double click, or a retry after a timeout, sends the same id; the API takes a Postgres **advisory lock** on that id, finds the first document and answers with it. This is an **idempotency key**, the same idea as Stripe's `Idempotency-Key` header, and the HTTP cousin of the sync inbox (note 03). The document also stores a fingerprint (SHA-256) of the request: the same id with a *different* document is refused with `409 id_used`, never answered with the first one as if it were the same.

Owner documents follow rules a bill cannot:

- **Dated in a locked month: refused** (`409 period_locked`). The owner picks the date, so they pick an open month. A bill cannot be refused, so its stock entry is booked in the first open month (the bill's own sales entry waits for the accountant, as before). Either way stock never moves without its entry, and account 1300 always equals the stock.
- **Wastage only of stock on hand** (`409 not_enough_stock`). Stock that was never booked in has no real price, and its true-up would land in food cost instead of wastage.
- **A count is today's** (`400 count_is_today`). It compares the shelves with the books *now*; a count entered a day late would compare that night's shelves with books later bills have changed.
- **Stock found with no known cost needs a price** (`400 needs_cost`). An opening count of rice never bought here would otherwise enter the books at zero and dilute every average after it.
- **Figures that do not fit in 64 bits are refused** (`400 too_large`), never wrapped into nonsense.

## Locks, and the order you take them in

Two documents touching the same ingredient must not both read "5 kg" and both write their own result. `openBook` locks the balance rows with `SELECT ... FOR UPDATE`, **in item-id order**. If one document locked oil then flour while another locked flour then oil, each could hold one and wait for the other forever: a deadlock. With one global order, that cannot happen.

The order covers more than balances. Every journal entry locks its accounting month's period row, so `openBook` opens (and locks) the period **before** the balances. A bill does the same: its sales entry locks the period, then its stock locks balances. The review found the first version had it the other way round for owner documents, which could deadlock against a bill on the first entry of a new month. Postgres detected it and the retry saved the day, but the rule is now true everywhere: period first, then balances by id.

Bills already take turns (every applied op locks the tenant's `sync_state` row for its seq), so we checked the lock matters by mixing owner wastage into a test of four tills selling at once. Without `FOR UPDATE` it lost updates in every run; with it, the oil adds up to the millilitre.

## Food cost

`GET /v1/outlets/{id}/stock/food-cost` puts the kitchen against sales for a range of trading days:

- **Food sales**: goods sold after discounts, without GST or service charge (`taxable_total - service_charge`).
- **Food cost**: what bills used (plus true-ups). Restaurants watch it as a percentage of food sales.
- **Wastage** and **count differences**: the leaks. A gap between food cost (what the recipes say) and food cost + leaks (what the kitchen really used) is portion drift, theft or unrecorded wastage.

The recipe card (`/recipes`) shows each dish's cost per portion at today's average prices, against its price without GST (prices that include GST are taken apart first), and flags dishes over 35%.

## What the review broke

A second agent attacked the slice and proved 14 bugs, each with a failing test (`internal/e2e/stock_review_test.go`, `src/lib/review.test.ts`, `e2e/stock-review.spec.ts`). The rules above come from it. The two most instructive:

- **"1,50" kg was read as 150 kg.** The quantity and rupee parsers accepted any comma as a thousands separator, so a decimal comma (common on phone keyboards) was read a hundred times too big, silently. Numbers are now read only with real groupings (1,500 or 1,20,000: the last group always has three digits), and anything else is refused, never guessed.
- **A delivery paid from the till was booked twice**: once by the purchase, once as the drawer's shortage. The fix was the clearing account above.

## Proof in tests

| Test | Proves |
| --- | --- |
| `valuation_test.go` | average cost, no dust, true-ups, found stock, 5,000 random histories reconcile |
| `TestStockStory` | buy, sell by recipe, low-stock alert, wastage, count, sell into negative, true-up on the next purchase; books equal stock at each step |
| `TestRecipesFollowTheBill` | no recipe, no stock; a resent bill uses stock once; removing a recipe |
| `TestConcurrentBillsKeepStockExact` | four tills and the owner at once, no lost updates (fails without the row lock) |
| `TestStockGuards` | other tenants, bad units, duplicates, bad dates and ids, id reuse |
| `TestTillPaysASupplier` | a delivery paid from a drawer: shift exact, 1150 back to zero |
| `TestStockFiguresTooLargeAreRefused` | 64-bit overflow refused, nothing kept |
| `TestReviewS1` to `S14` | the review's findings stay fixed |
| `e2e/stock.spec.ts` | the owner's whole flow in the browser |

## What would break at 10x?

- **One recipe per dish, no modifiers.** "Extra cheese" uses cheese too. Modifier options need recipes of their own.
- **No units of purchase.** Suppliers sell flour by the 25 kg sack; today the owner types 25. A "pack of" conversion per supplier item is next.
- **No input tax credit.** A restaurant at 5% GST cannot claim it, so the cost includes tax. A restaurant at 18% (in a hotel) can; then the tax on purchases must go to an input GST account.
- **Reports use today's recipes and prices.** The recipe card and the per-dish cost in the food cost report use the current recipe at today's average cost; the food cost itself is what bills really used.

## Interview questions

<details><summary>1. Perpetual or periodic inventory?</summary>
Perpetual books every movement as it happens, so stock and cost are known at any moment; it needs recipes or item-level sales.
Periodic counts at the end and derives cost as opening + purchases − closing; simpler, but you learn the number a month late and cannot see where it went.</details>

<details><summary>2. Why does a balance at zero quantity always have zero value here?</summary>
Stock leaves at its share of the value, value × q / qty, not at a rounded unit cost.
The last unit's share is all the remaining value, so the rounding of earlier exits is absorbed exactly.</details>

<details><summary>3. How do you make a POST safe to retry?</summary>
The client sends an idempotency key it made before the first attempt.
The server serialises on the key (an advisory lock or a unique index) and answers a repeat with the stored result instead of doing the work again.</details>

<details><summary>4. How do you avoid deadlocks when a transaction locks several rows?</summary>
Always lock them in one global order, here by id.
Two transactions can then only wait in one direction, so no cycle of waits can form.</details>

## Exercises

1. Add a recipe for the "Extra cheese" modifier. Where does `ConsumeForBill` need to look, and what must a bill line carry for it to work?
2. Change `Out` to use a rounded unit cost (`UnitCost() × q / 1000`). Which test fails first, and why?
3. Write the SQL for "the 5 ingredients with the most wastage this month, by value".
