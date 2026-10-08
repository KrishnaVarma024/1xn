# 0019. Staff sign in with a one-time code; roles and permissions live in one table

Status: accepted, 8 Oct 2026. Replaces the "no per-staff identity" part of ADR 0009.

## Context

Until now a restaurant had one person, the owner, holding one long-lived token from sign-up. Restaurants run on staff: a manager who closes the day, cashiers who take deliveries at the back door. Each needs their own sign-in, the owner needs to switch people off when they leave, and a cashier should not see the books or pay suppliers. Indian restaurant staff sign in with their mobile number, not a password.

## Decision

- **Sign in with a mobile number and a 6-digit code.** `POST /v1/auth/otp` makes a challenge and sends a code; `POST /v1/auth/otp/verify` checks it and returns a five-minute, single-use **ticket** and the businesses the number belongs to; `POST /v1/auth/sessions` turns the ticket and a chosen business into a **session** (`onx_s_…`, 30 days). One number may work at several businesses, so the code proves the number and the person then picks.
- **No enumeration.** A challenge is made for every request, also for numbers with no account (no code is sent, none can match), with the same answer and the same rate limits. Wrong codes, unknown challenges and numbers without accounts look alike.
- **Guessing is bounded.** A code allows 5 tries and lives 10 minutes; a number gets a code at most every 30 seconds, 3 per 15 minutes and 10 per day (an advisory lock per number makes the counts exact under concurrency). Codes are stored as SHA-256 of challenge id and code, and compared in constant time.
- **Sessions are tokens** in `auth_token` (kind `session`, with `expires_at`). `auth_resolve` returns the person's **role** with the token and refuses expired sessions and switched-off people, so a change of role applies on the next request and switching someone off locks them out at once (their sessions are also revoked). Signing out revokes the session. The owner token from sign-up still works and does not expire: it is the owner's spare key (and what `make seed` prints).
- **Roles and permissions.** Three roles (owner, manager, cashier). Fifteen permissions, each a thing a person does in the back office. `auth/permissions.go` holds the only table of which role has which; every route names the permission it needs with `auth.Need`, and `/v1/me` returns the person's permissions so the back office shows only what works. A route inventory test (`TestEveryRouteNamesWhoMayCallIt`) fails when a route is not classified, and calls every route as each role.
- **Separation of duties.** Whoever receives goods may book them on credit or paid from a till (checked at the drawer count); booking money out of the safe or the bank needs `cash.out` (managers); paying suppliers is the owner's.
- **The owner manages staff** (`/v1/staff`): adds managers and cashiers, changes their names, numbers and roles, switches them off. A business has one owner; the owner's role, number and access are not changed there.
- `otp_challenge` has no tenant and no RLS: it exists before anyone knows the business. Reading across tenants for sign-in goes only through `login_accounts(phone)`, a `SECURITY DEFINER` function that returns what the sign-in screen shows, like `auth_resolve`.

## Consequences

- Codes are printed in the API's log (`LogSender`) until an SMS or WhatsApp provider is wired in. That must happen before real use; the back office shows a hint while `ONEXN_SMS_PROVIDER` is unset.
- Anyone can use up a number's 10 codes a day and keep that person from signing in by code (the owner keeps the owner token). Per-IP limits belong at the edge (the API only sees the back office server's address behind the BFF).
- Staff see every outlet of the business; per-outlet staff come later.
- Challenges of numbers with no account and expired sessions are not pruned yet beyond each number's own two-day sweep.

## Alternatives considered

- **Passwords.** Staff forget them, share them, write them on the till. Codes to a phone they already carry are what Indian restaurant software uses.
- **JWT sessions.** Revocation (someone leaves) and role changes would need a lookup anyway; the opaque-token lookup we already do gives both for free.
- **Roles checked in each handler** (`if role != owner`). Rules scattered in fifty places cannot be reviewed; one table plus a test that every route is in it can.
- **A code that signs in to every business of the number.** Simpler, but a person who helps at a friend's café would carry both businesses in one session.
