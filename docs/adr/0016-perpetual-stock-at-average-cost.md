# 0016. Perpetual stock at weighted average cost, used by recipes

Status: accepted, 3 Oct 2026

## Context

Owners need stock levels, recipe costing and a daily food cost. Bills arrive by sync, offline-first, sometimes before the delivery they used is recorded. The ledger must always agree with the stock (ADR 0010), and issued bills are never refused (ADR 0007).

## Decision

- **Quantities are integer milli-units** of the item's unit (kg, l or pc): grams, millilitres, thousandths of a piece. Money stays integer paise.
- **Stock items and recipes are per tenant; balances are per outlet.** A recipe lists what one portion of a menu item uses. Recipes are effective-dated; a bill uses the recipe in force when it was issued, and a dish's first recipe counts from the start.
- **Perpetual inventory:** each bill uses its dishes' recipes the moment it syncs, in the bill's transaction, and books Dr 5100 Cost of food sold / Cr 1300 Stock. Purchases, wastage and counts are owner documents with their own entries (accounts 1140 Bank, 1300 Stock, 2100 Payable to suppliers, 5100, 5110 Food wastage, 5120 Stock count differences).
- **Weighted average cost**, with exits valued at their share of the balance's value, so a zero balance is worth zero.
- **Negative stock is allowed** and flagged (`negative_stock`); it leaves at the last purchase price, and the next purchase trues up the estimate into cost of food sold.
- **Every change is a document with append-only moves** (`stock_doc`, `stock_move`); `stock_balance` is their running total, row-locked in item-id order.
- **Owner documents carry a client-made UUIDv7** as an idempotency key, serialised with an advisory lock, with a request fingerprint so a reused id is refused.
- **Stock never moves without its journal entry.** Owner documents dated in a locked month are refused; a bill's stock entry goes to the first open month. Locks are taken period first, then balances in item-id order.
- **Owner documents are checked against reality:** wastage only of stock on hand, counts only for today, found stock with no known cost needs a price, figures that overflow are refused.
- **A delivery is paid on credit, by bank, from the safe, or from a till.** A till payment goes through clearing account 1150, matched by the till's `supplier` cash move, which lowers the drawer's expected cash.
- `low_stock` alerts fire once per crossing of an item's reorder level.

## Consequences

- Food cost is known per trading day and per dish; leaks (wastage, count differences) are separate from food cost.
- Bills of a dish without a recipe use nothing; the food cost report counts those portions so the owner knows the cost is incomplete.
- A recipe change affects bills issued after it, even ones that sync later.
- Purchase prices include GST (no input tax credit at 5%); a restaurant that can claim input credit needs an input GST account later.

## Alternatives considered

- **Periodic inventory (month-end count only).** Simpler, but the food cost arrives a month late and cannot be traced to dishes or days.
- **FIFO costing.** Closer to physical flow for perishables, but needs lots per purchase and per-lot consumption; average cost is the norm for restaurant ingredients and far simpler to reason about.
- **Refusing sales beyond stock.** Would refuse bills that were printed and paid; stock records lag reality in every kitchen.
- **Decimal quantities (NUMERIC).** Exact too, but every layer (Go, JSON, TypeScript) would need a decimal type; integers in a small unit keep all of them simple.
