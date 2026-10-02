# 0014. The back office is a Next.js Backend-for-Frontend

Status: accepted, 2 Oct 2026

## Context

Owners need a web back office: menu, tables, devices, the live floor, bills, alerts, books. The architecture doc picked Next.js for it. The Go API already enforces every rule (tenancy, validation, money). Owner auth in P0 is an opaque bearer token (ADR 0009); phone OTP comes later.

## Decision

- **Next.js App Router, TypeScript, Tailwind.** Pages are Server Components that call the Go API on the server. Writes are Server Actions. Only interactive bits are Client Components.
- **Backend-for-Frontend.** The browser only talks to the Next.js server; the Next.js server talks to the API through one function, `src/lib/api.ts`. The API address (`ONEXN_API_URL`) is server-only.
- **The owner token lives in an `httpOnly`, `SameSite=Lax` cookie** (`Secure` in production). Page JavaScript can never read it. Sign-up shows it once so the owner can save it, mirroring the API.
- **The API stays the only authority.** The Next.js proxy only redirects requests that have no cookie. Every action calls the API as the owner; the API checks the token, the tenant and the input. The web app validates shapes (prices, ids) only to give better messages, and checks every id is a UUID before putting it in an API path.
- **No client data library or component kit yet.** Server Components and Server Actions cover today's screens. TanStack Query, TanStack Table and Radix (named in the architecture doc) come in when a screen needs client-side caching, big sortable tables, or complex accessible widgets.
- **Tests:** unit tests for money and ids, a type check, Go e2e tests for the new owner endpoints, and Playwright tests that drive the production build against the real API (`make test-web`).

## Consequences

- No CORS, no API key or token in the browser bundle, one place to change auth when OTP arrives.
- Each page render is one or more server-to-API calls. Fine for owners; the live floor's 5-second refresh is the hottest path.
- Server Actions are public POST endpoints by design; the rule "every action goes through the API as the owner" must hold for every new action. Code review checks it.

## Alternatives considered

- **Single-page app calling the API directly.** Needs CORS and a token in JavaScript-readable storage, which XSS can steal.
- **Server-rendered pages from the Go service (templates).** One fewer process, but a weaker toolkit for the many screens the back office will grow, and the architecture already chose React and Next.js.
- **Next.js Route Handlers as a generic proxy to the API.** Simple, but it would expose the whole owner API to the browser again; Server Actions expose only the operations the screens need.
