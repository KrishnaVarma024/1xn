# 0001. A modular monolith in Go

Status: accepted, 29 Sep 2026

## Context

13 apps share one customer, one menu, one ledger and one GST registration. Splitting them into microservices on day one would turn every bill into a distributed transaction (sales, stock and ledger in three databases) and give a team of one to three people fifteen deployables to run.

## Decision

One Go binary (`services/core`) holding one package per module under `modules/`. It runs as several roles (API, sync gateway, workers) from the same build. Modules talk to each other only through the functions listed at the top of each package ("Cross-module API"), never through each other's tables.

## Consequences

A bill, its journal entry and its outbox event commit in ONE Postgres transaction, so they can never disagree. A module can be cut out into its own service later along the boundary we already keep. The cost: one slow module can hurt the others inside the same process, so we watch per-module latency.

## Alternatives considered

Microservices per app (rejected: distributed transactions, operational load). A single package with no module boundaries (rejected: turns into a ball of mud by app five).
