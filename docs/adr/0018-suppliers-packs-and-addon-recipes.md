# 0018. Suppliers' balances come from documents; packs and add-on recipes never rewrite history

Status: accepted, 8 Oct 2026

## Context

Stock (ADR 0016) knew deliveries by a free-text supplier name, could not say what was owed to whom, received everything in the item's own unit (the owner typed 25 for a 25 kg sack), and ignored add-ons: "Extra cheese" sold cheese without using any.

## Decision

- **Suppliers are rows**, made from the distinct names of past deliveries by migration 0011 and, from then on, by any delivery that names a new one (`INSERT ... ON CONFLICT DO NOTHING`, names unique ignoring case). Every purchase has a `supplier_id`.
- **What is owed is computed, not stored**: deliveries on credit less payments, per legal entity. It is a subledger of account 2100 Payable to suppliers, and tests reconcile the two after every step.
- **Supplier payments** (`POST /v1/outlets/{id}/stock/supplier-payments`) book Dr 2100 / Cr 1105 safe, 1140 bank or 1150 till clearing. They are owner documents: UUIDv7 id and SHA-256 fingerprint, locked months refused. The supplier row is locked `FOR UPDATE` (after the period, keeping the lock order), so payments to one supplier take turns. A payment may not exceed what is owed now, nor the lowest running balance from its own date to today, so a backdated payment can never be an advance in the past. At most ₹100 crore per payment, as the table allows.
- **Packs** (`stock_pack`) convert on the way in only; stock stays in the item's unit. A pack's name and size never change: a new size is a new pack, the old one is switched off. Purchase moves keep `pack_id` and `packs`.
- **Add-on recipes** (`addon_recipe_line`) are versioned like dish recipes and saved by the same code (`putRecipe` with a `recipeKind`). Bill modifiers carry `ref` (the option id); `ConsumeForBill` adds each add-on's recipe per portion, using the recipe in force when the bill was issued. A `ref` that matches no recipe uses nothing; the bill is never refused for it.
- A recipe save locks its stock items `FOR SHARE`; switching an item off locks it first. Neither can miss the other (write skew).

## Consequences

- No balance column can drift from the documents, at the cost of a sum per request; fine for a restaurant's suppliers, and the obvious place to add a cached balance later.
- Payments are not matched to invoices yet, and advances are refused.
- Old deliveries keep meaning what they said: "2 × Bag" stays 50 kg even after a 10 kg bag is added.
- The till's bill format grew one optional field (`modifiers[].ref`); old tills that do not send it simply use no add-on stock.

## Alternatives considered

- **A balance column per supplier**, updated with each document. Faster to read, but one more thing to keep exactly in step with 2100; deferred until the sum is slow.
- **Editable packs.** Simpler screens, but rewrites the meaning of past deliveries.
- **Add-ons as hidden dishes on the bill.** Would have reused dish recipes, but changes what the till prints and how GST is split; a ref on the modifier is smaller and honest.
