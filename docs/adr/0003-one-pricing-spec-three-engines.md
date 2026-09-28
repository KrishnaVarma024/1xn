# 0003. One pricing spec, three engines, golden files as the contract

Status: accepted, 29 Sep 2026

## Context

The POS must price bills offline, so the tax engine runs on the device (Dart). The server must check every bill (Go). If the two disagree by one paisa, invoices and ledgers diverge.

## Decision

The rules are written once in `docs/learn/01-money-and-gst-engine.md`. They are implemented three times: Go (server), Dart (device), Python (`tools/golden/oracle.py`, a deliberately different implementation using exact fractions). `packages/golden/tax/*.json` holds the cases; every implementation must reproduce them exactly, in CI.

## Consequences

A rule change is a spec change plus a golden-file change, reviewed together. Hand-written cases say what is right; parity cases catch drift between engines. Before pilot, a chartered accountant signs off the hand-written set.

## Alternatives considered

Server-only pricing (rejected: no billing when the internet is down). Sharing one engine through WebAssembly or FFI (possible later; the golden files still stay).
