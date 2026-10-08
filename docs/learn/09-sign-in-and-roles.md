# 09 Signing in with a code, and who may do what

**Problem.** Until slice 8 a restaurant had one key: the owner token from sign-up. A real restaurant has a manager who closes the day and cashiers who take deliveries at the back door. Each needs their own way in, the owner must be able to lock someone out the minute they leave, and a cashier should not see the books or pay suppliers. In India, staff sign in with their mobile number, not a password.

**Truth to remember:** *authentication* is "who are you?" (the code proves the phone). *Authorization* is "what may you do?" (the role decides). Keep them apart, and keep the second in one place.

## The flow

```
 BROWSER                     NEXT.JS (BFF)                         GO API                              POSTGRES
 ───────                     ─────────────                         ──────                              ────────
 types 98765 43210 ───▶ requestCode ───▶ POST /v1/auth/otp {phone} ──▶ advisory lock on the number
                         cookie onx_code                               limits: 30 s, 3 / 15 min, 10 / day
                         (challenge id)                                login_accounts(phone)? ─────────▶ SECURITY DEFINER
                                                                       yes: code, SHA-256(id:code) ────▶ otp_challenge
                                                                       no:  a challenge with no code
                                                                       202 {challenge_id}  (same either way)
                                          SMS (today: the API's log) ◀── SIGN-IN CODE 482913
 types 482913 ────────▶ verifyCode ───▶ POST /v1/auth/otp/verify ────▶ row FOR UPDATE, attempts + 1
                                                                       constant-time compare
                                                                       ticket (5 min, once) + businesses
                         one business: ─▶ POST /v1/auth/sessions ────▶ ticket used once (UPDATE … RETURNING)
                         several: pick                                 person still active, same number
                         cookie onx_session ◀───────────────────────── onx_s_… (30 days) ────────────────▶ auth_token
 every page ─────────▶ api() + token ──▶ auth_resolve(hash) ─────────▶ tenant, kind, person, ROLE
                                         auth.Need(permission) ──────▶ 403 unless the role has it
```

Three steps, not two, because **one number can belong to several businesses**: a manager who also helps at a friend's café. The code proves the number; the ticket then lets the person pick a business, once.

## Not telling strangers who works where

"Send a code to +91 98765 43210" must answer exactly the same whether that number has an account or not. Otherwise anyone could test numbers and learn who works at which restaurant (**user enumeration**). So the API makes a challenge for every request; for an unknown number it stores no code, sends nothing, and no code can ever match it. The status, the body, the rate limits and the wrong-code messages are the same. The only difference left is time (sending an SMS is slower than not sending one); a production SMS sender should queue the message (outbox) so the request returns before it is sent.

## Making six digits safe

A code has a million values, which is not many. What makes it safe is everything around it:

| Limit | Why |
| --- | --- |
| 5 tries per code | a guesser gets 5 of a million |
| 10 minutes | a code seen later is useless |
| once | a verified code, or a used ticket, never works again (row lock + `UPDATE … WHERE used IS NULL RETURNING`) |
| 30 s between codes, 3 per 15 min, 10 per day per number | at most 50 guesses a day: about 1 in 20,000 |
| SHA-256 of challenge id + code, constant-time compare | the database never holds a usable code; no timing hints |

The limits are counted in Postgres under an **advisory lock** per number (`pg_advisory_xact_lock(hashtextextended('otp:'||phone, 0))`), so twelve requests at once still get exactly one code; the test proves it.

The flip side: anyone can spend a number's ten codes and keep that person out for a day. That is the classic trade-off of rate limits; per-IP limits at the edge reduce it, and the owner always has the owner token.

## Sessions are tokens, and the role is read every time

A session is a row in `auth_token` (kind `session`, `expires_at` in 30 days), found by the SHA-256 of the token, like device tokens (note 03, ADR 0009). `auth_resolve` now also returns the person's **role**, joined from `app_user` on every request, and refuses expired sessions and switched-off people. So:

- promoting a cashier to manager applies on their next click, with no new sign-in;
- switching someone off locks them out at once, everywhere (and their sessions are revoked too);
- signing out revokes that one session.

This is why we did not use JWTs: a JWT is valid until it expires, so "this person left" and "this person is now a manager" would need a lookup anyway. We already do one lookup per request; it answers both.

## Roles and permissions: one table

```go
var matrix = map[Permission][]Role{
	Floor: everyone, Bills: everyone, Stock: everyone, StockReceive: everyone,
	Menu: managers, Devices: managers, Day: managers, StockManage: managers, CashOut: managers,
	Costs: managers, Suppliers: managers, Alerts: managers,
	PaySuppliers: owner, Books: owner, Staff: owner,
}
```

This is **role-based access control (RBAC)**. Routes never ask "is this an owner?"; they say what they need:

```go
need(r, auth.Day).Mount("/days", dayclose.Routes(d))
need(auth.PaySuppliers).Post("/supplier-payments", postSupplierPayment(d))
```

and only the table says which roles have it. Changing what a manager may do is one line, and reviewing the whole policy is reading one screen. `/v1/me` sends the person's permissions to the back office, which shows only the links, tabs and forms that will work; every page also checks, and shows "Cashiers can't open this page" instead of an error. The API stays the real gate: a hidden button is a courtesy, not security.

**Default deny, by test.** `TestEveryRouteNamesWhoMayCallIt` walks the router (`chi.Walk`), fails if any route is missing from its table, and then calls every route as an owner, a manager and a cashier, checking that exactly the right roles get `403`. Add a route and forget its permission, and the build fails. Removing a `Need` makes it fail too (we checked).

## What the review found

The adversarial review of this slice proved three defects (`signin_review_test.go`, `e2e/staff-review.spec.ts`):

- **A sign-in racing a switch-off survived it.** The session check read "is Asha active?" without locking her row; the owner's switch-off committed between that read and the session's insert, so its revoke missed the new session, which came back to life when Asha was switched on again. This is **write skew** again (note 08): each transaction checked something the other was changing. The fix locks what is read: `SELECT … FOR SHARE` on the person, which the switch-off must wait for. The test makes the interleaving deterministic by holding a lock the insert needs.
- **A cashier could book a delivery as paid from the safe or the bank.** Receiving goods is a cashier's job, but "₹4,000 of paneer, paid from the safe" moves money nothing a cashier counts ever checks: a fake delivery and ₹4,000 taken from the safe would surface weeks later as a stock count difference. **Separation of duties**: whoever handles the goods may book them on credit or from a till (the drawer count checks that); the safe and the bank need `cash.out` (managers).
- Pages pointed cashiers at things they cannot do ("Set up tables", "add the pack on the item's page").

## Proof in tests

| Test | Proves |
| --- | --- |
| `TestStaffSignInWithACode` | code, session, role from `/v1/me`, a promotion applying at once, sign-out, switch-off, owner token unaffected |
| `TestOneNumberInTwoBusinesses` | the ticket picks one business, once; another business's person is refused |
| `TestSignInCodesResistGuessing` | same answer for unknown numbers, 5 tries, expiry, once, the three rate limits, ticket expiry |
| `TestOwnerManagesStaff` | validation, duplicate numbers, the owner row, a new number signs out, cross-tenant 404 |
| `TestEveryRouteNamesWhoMayCallIt` | every route classified, every role checked |
| `TestReviewA1`, `A2` | the review's findings stay fixed |
| `e2e/staff.spec.ts` | the whole flow in a browser: add a cashier, sign in with the code from the API log, a cashier's view, promotion, switch-off, choosing a business, wrong codes |

## What would break at 10x?

- **Codes in the log.** Real use needs an SMS or WhatsApp provider behind `CodeSender`, sending through the outbox.
- **No per-IP limits.** Behind the BFF the API sees one address; the edge (load balancer or WAF) must limit by client address.
- **Every outlet.** A cashier at Bandra sees Andheri too. Per-outlet staff need an outlet list per person, checked on every outlet route.
- **Tables that only grow.** Old challenges of unknown numbers and expired sessions are never pruned globally; a nightly job should.

## Interview questions

<details><summary>1. Authentication or authorization?</summary>
Authentication proves who you are (here: the code proves you hold the phone). Authorization decides what you may do (here: the role's permissions). Mixing them leads to checks like "signed in, so allowed".</details>

<details><summary>2. Why return the same answer for a number with no account?</summary>
Otherwise the endpoint tells anyone which numbers are staff where (user enumeration), which helps phishing and targeted guessing.</details>

<details><summary>3. A 6-digit code has only a million values. Why is it acceptable?</summary>
Because of the limits around it: a few tries per code, a short life, single use, and few codes per number per day. The guesser's odds are what the limits allow, not what the code length allows.</details>

<details><summary>4. JWT or opaque session token?</summary>
A JWT can be checked without a lookup but stays valid until it expires, so revocation and role changes need a lookup anyway. An opaque token always needs one lookup, which then gives revocation, expiry and the current role for free.</details>

<details><summary>5. How do you make sure no route forgets its permission check?</summary>
Make every route name its permission and test the inventory: walk the router, fail on any route missing from the table, and call each route as each role.</details>

<details><summary>6. What is separation of duties?</summary>
No one person can both do something and approve or record the money side of it: the person who receives goods should not also be able to say cash left the safe to pay for them.</details>

## Exercises

1. Add per-outlet staff: what table, what check, and where would you put it so no outlet route can forget it?
2. Write a `CodeSender` that queues the code in the outbox and a worker that sends it. What happens when the SMS provider is down?
3. Add "sign out everywhere else" for a person. Which rows change, and what does the person see on their other phone?
