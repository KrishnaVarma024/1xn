# 0013. Device facts are recorded and flagged, not refused

Status: accepted, 30 Sep 2026

## Context

Devices work offline. By the time an op reaches the server, the thing it describes has already happened: the naan went to the kitchen, the guests moved tables, the dessert was eaten. Meanwhile other devices changed the same orders. If the server refuses such an op, the record now disagrees with what happened, and nobody can fix the real world.

## Decision

Classify every order conflict:

1. **Can never become true → reject** with a code, keeping the payload (ADR 0005). Examples: malformed payload, unknown item, table, section or modifier option, an order from another outlet, a role that may not do this (a KDS adding lines), any change to a line that is already billed, and a cancel aimed at an order that was merged away (its lines belong to another party now). A paid invoice changes only through a credit note (ADR 0007).
2. **Commutative → apply.** Two captains adding lines, KOTs fired from two devices. The order row lock makes them take turns.
3. **Target moved → forward.**
   - A merged order becomes a **tombstone** with `merged_into`. Later ops for it follow the pointers (at most 16 hops) to the live order.
   - A line added to a closed order opens a **spillover** order on the same table (`spillover_of`) and raises `late_items`. Fire, void and bill ops for those lines find the spillover through the original id.
4. **Allowed, but against policy → apply and alert.** Examples:
   - a fired line voided, or an order with fired lines cancelled, without a manager's approval (`void_after_kot_without_approval`);
   - a bill that names voided lines, lines already on another bill, or charges a line differently from the order (`bill_order_mismatch`);
   - an order opened on a table that was switched off meanwhile (`inactive_table`).
5. **Old news → no change.** A KOT status that is not ahead of the current one, a repeated line (line ids are unique per tenant, forever), KOT, merge or cancel. The ack is `duplicate`.

## Consequences

- The history is never missing an event that happened. Managers review alerts instead of staff redoing work.
- Orders can end up in states a strict server would forbid (a void without approval). That is deliberate, and each one raises an alert.
- Every rule needs a matching test in `internal/e2e/orders_test.go` or `modules/orders/domain_test.go`.

## Alternatives considered

- **Reject every conflict and let the device resolve it.** Correct on paper, but in practice staff re-enter orders, and the kitchen double-cooks.
- **Last writer wins on the whole order.** Silently loses a captain's lines.
- **CRDTs for the whole order.** They merge anything, but they cannot express rules like "a billed line never changes", which matter more here than automatic merging.
