# 0006. Every device has its own invoice series

Status: accepted, 29 Sep 2026

## Context

GST rule 46(b) requires invoice numbers to be consecutive, unique per financial year per GSTIN, at most 16 characters, using letters, digits, "-" and "/". Two tills billing offline at the same moment cannot ask a server for the next number.

## Decision

Each device gets a 3-character base-36 prefix at pairing, unique per legal entity. Invoice numbers are `PPP-FFFF-NNNNNN` (prefix, financial year, running number), 15 characters. The server rejects a bill whose prefix is not the pushing device's or whose FY does not match the issue time in IST.

## Consequences

No coordination needed to bill. A reinstalled device gets a new prefix, so numbers are never reused. 46,655 devices per GSTIN before the prefix space runs out.

## Alternatives considered

One central counter (fails offline). Random invoice numbers (not consecutive, fails the rule).
