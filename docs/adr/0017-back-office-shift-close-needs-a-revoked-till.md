# 0017. A shift is closed in the back office only when its till is revoked

Status: accepted, 8 Oct 2026

## Context

A till closes its own shift: the cashier counts the drawer and the till sends `shift.closed` (ADR 0015). When the till never can (lost, stolen, broken), the shift stays open forever, and every Z report of that day asks the owner to acknowledge it. The owner needs to count the drawer and close the shift from the back office.

A till that still works may be offline with ops it has not sent: bills, cash moves, its own count, the next shift. If the owner counted the same drawer in the meantime, those ops would have to be fitted around a second count after the fact. The review of the first version proved how that goes wrong: a safe drop made before the owner's count was refused and booked as a loss; the cashier's earlier, exact count lost to the owner's later one, the next shift was refused as overlapping and its sale charged to the closed one. When two people count one drawer there is no right answer to fit.

## Decision

- `POST /v1/outlets/{id}/shifts/{shift}/close {id, counted_paise, denominations?, note}` closes a shift **only if its till is revoked** (`409 till_active` otherwise: close it on the till, or revoke it first).
- A revoked till sends nothing more: the sync gateway re-checks `revoked_at` under the device row lock, which the revoke's own UPDATE also takes. So once revoked, every bill and cash move of the shift is on the server, and the owner's count is the last word.
- The count's window ends at the last bill or cash move the till recorded, or now, whichever is later (a till's clock may run ahead of ours). Expected cash, the 5900 entry and the `cash_difference` alert are exactly those of a till close; a locked month keeps the close with `ledger_status = 'blocked'` and a `period_locked` alert, like every drawer path.
- The request carries a UUIDv7 made when the form opens and a SHA-256 of the request: a replay gets the same shift back (200), a reused id for anything else gets `409 id_used`, a closed shift `409 shift_closed`.
- The close writes the tenant's `sync_state` row, so a day close that took its snapshot before it is retried and sees the counted drawer. Closing a shift of a day already closed raises `shift_after_day_close`, as a till's late close does.
- The shift records who closed it there (`closed_by_user`) and why (`close_note`, required); the Z report shows "Counted in the back office by …".

## Consequences

- An owner whose cashier simply went home without closing cannot close the shift here; the till closes it (late is fine). Revoking a working till to close its shift is possible but costly (it must be paired again), which is the point: it is the owner saying "this till will never send anything again".
- Ops a till had not sent when it was revoked are lost, as they always were for a revoked till. That is what revoking means.
- No till-side special cases: a till never meets a shift closed in the back office.

## Alternatives considered

- **Any open shift, first count wins.** Simple, but provably wrong when the till was only offline (see Context).
- **Any open shift, earliest count wins** (re-close at the till's time when its count arrives, reverse the owner's entry). Correct for the two-count case but needs reversing entries, re-validating the next shift, and still cannot say whose physical count was right.
- **A "last seen" guard** (allow only if the till has not synced for N hours). A till can be offline for days with ops queued; silence is not proof.
