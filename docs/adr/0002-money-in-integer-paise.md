# 0002. Money is an int64 count of paise

Status: accepted, 29 Sep 2026

## Context

Floating point cannot represent 0.1 exactly. A GST invoice must be exact to the paisa, and totals must match across the device, the server and the tax return.

## Decision

Every amount in every language and table is an integer number of paise (`bigint` in Postgres, `int64` in Go, `int` in Dart). Rates are integer basis points (500 = 5%). Division happens only through `mulDivRound` (128-bit intermediate, round half up).

## Consequences

No rounding drift, ever. Formatting to rupees happens only at the edges (print, UI). Anyone adding a float to a money path is a bug caught in review.

## Alternatives considered

Decimal types (fine in Go and Postgres, awkward in Dart and JSON). Floats (never).
