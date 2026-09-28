# 0007. A bill the guest holds is never dropped or rewritten

Status: accepted, 29 Sep 2026

## Context

Once printed, an invoice is a legal document in the guest's hand. If the server recomputes a different total, "fixing" it would make our records disagree with the paper.

## Decision

The server recomputes every bill. On mismatch it stores the device's figures (the tax actually charged), keeps its own result next to them, posts the device's figures to the ledger, and raises a critical alert. It refuses a bill only when storing it would corrupt data (wrong series, reused number, totals that do not add up), and even then the op payload is kept in `sync_inbox`. Payment differences go to a suspense account instead of blocking the entry. A bill dated in a locked accounting period is stored with `ledger_status = blocked` and alerted. A fully comped bill (total zero) is stored with `ledger_status = none`: it used an invoice number, but no money moved. Database errors that would fail identically on every retry (SQLSTATE classes 22 and 23) become rejections, so one malformed op can never block the ops queued behind it. Because re-pricing the device's own input cannot catch a wrong input, each bill is also compared with the catalogue's current tax rates and the outlet's price mode and rounding; differences raise alerts.

## Consequences

Nothing a customer paid for disappears. Humans fix disagreements with evidence on both sides.

## Alternatives considered

Reject mismatched bills (loses revenue records). Silently correct them (records disagree with the printed invoice).
