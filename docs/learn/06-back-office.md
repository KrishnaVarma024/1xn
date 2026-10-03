# 06 The back office: a web app that holds no secrets

**Problem.** The owner needs screens: the menu, the tables, who is eating right now, today's bills, the books. They will open them on a laptop at home and on a phone in the kitchen. The Go API already does all the work and checks every request. The web app must add screens **without** becoming a second place where money rules or security can go wrong.

**Truth to remember:** the browser is enemy territory. Anything that runs there can be read and changed by whoever holds the device. So the browser gets HTML and buttons, nothing more. The token, the API address and every decision stay on the server.

## The shape: Backend-for-Frontend (BFF)

```
 BROWSER                         NEXT.JS SERVER (apps/backoffice)            GO API (services/core)
 ───────                         ────────────────────────────────            ──────────────────────
 GET /menu  ──── cookie ────▶    page.tsx (Server Component)
                                   getContext() ─ api("/v1/me") ───────────▶ auth: token → tenant
                                   api("/v1/outlets/…/menu") ──────────────▶ RLS-scoped read
 ◀──────────── HTML ──────────   renders the page

 click "Mark sold out" ─POST─▶   Server Action setAvailable(id)
                                   mustUUID(id)
                                   api(PATCH …/availability) ──────────────▶ checks token, tenant, input
                                   revalidatePath("/menu")
 ◀──────── fresh HTML ────────   page re-rendered with new data
```

The browser never calls the Go API. It only talks to the Next.js server it was served from. That gives three things:

1. **The owner token never reaches JavaScript.** It lives in an `httpOnly` cookie. The browser attaches it to requests to our own site, but `document.cookie` cannot read it. Malicious code injected into the page (cross-site scripting, XSS) can therefore not steal it. The Playwright test checks exactly that.
2. **No CORS, no public API address.** The API can sit on a private network later; only the Next.js server needs to reach it.
3. **One door.** Every call goes through `src/lib/api.ts`. That is one place to add the token, set `cache: "no-store"` (owner data is live), map errors, and send a 401 to `/sign-in`.

## Server Components read, Server Actions write

In the App Router, a page is a **Server Component** by default. It is an `async` function that runs on the server, can `await` the API, and returns HTML:

```tsx
export default async function MenuPage() {
  const { outlet } = await getContext();
  const menu = await api<Menu>(`/v1/outlets/${outlet.id}/menu`);
  return <ItemList menu={menu} />;
}
```

No `useEffect`, no loading spinners per widget, no API client in the bundle. Only components that need the browser (a form that shows "Saving…", the 5-second refresh on the live floor) are **Client Components**, marked `"use client"`.

Writes are **Server Actions**: functions marked `"use server"` that a form can call. Next.js turns each one into a POST endpoint. Two rules follow from that:

- **Every action is a public endpoint.** Anyone can POST to it with any arguments, including the ids bound with `.bind(null, item.id)`. So every action calls the API as the signed-in owner (the API checks the token and the tenant), and every id goes through `mustUUID()` before it becomes part of a URL. Without that check, an id like `../ledger/trial-balance` would steer the request to a different API endpoint.
- **Validation happens twice, for different reasons.** The web app checks the shape ("320.50" is a price; "12O" is not) to give a fast, friendly message. The API checks the rules ("this table label already exists", "that GSTIN's check digit is wrong") because it is the authority. Never only the first.

## Money at the edges

Prices are integer paise everywhere, here too. `src/lib/money.ts` converts only at the edges:

- `parseRupees("320.50")` returns `32050`. It parses the string as digits, so `0.1 + 0.2` style float bugs cannot happen.
- `formatPaise(1234567)` returns `₹12,345.67`, with Indian digit grouping (`₹12,34,56,789.01`).

Both have unit tests (`npm test`).

## Forms that keep what you typed

React 19 resets a form after every submission through its `action` prop, even when the server answered "error". An owner who typed a long GSTIN and got one character wrong would lose the whole form. `ActionForm` (in `src/components/forms.tsx`) submits through `startTransition` instead and clears the form only after a success. The browser test types a bad GSTIN and checks that the restaurant name is still there. We removed the fix once to make sure the test notices, and it failed.

## A GSTIN decides every bill's tax

A GSTIN's first two digits are its state, and the state decides the tax on every bill: the same state as the guest means CGST + SGST, another state means IGST. So sign-up checks three things, each with its own message. The check digit (the 15th character) catches one mistyped character. The state code must be one in use (28 was Andhra Pradesh before 2014; GST starts at 37). And the GSTIN must belong to the state the owner picked: typing a GSTIN picks its state in the form, and if the owner changes the state afterwards, the form names both states instead of quietly using one. The rule lives in the API (`gst.ValidStateCode`, `bad_state_code`); the form only explains it better.

## Opening the dev server from a phone

`next dev` serves its own dev files (hot reload, page chunks) only to pages opened on `localhost`. A page opened on the Mac's network address (`http://192.168.1.3:3000`, the "Network:" line) is another origin, and Next.js blocks it, because any website you visit could otherwise read your dev server's code. `next.config.ts` allows this machine's own network addresses (`allowedDevOrigins`) and nobody else's, so the back office works on a phone on the same Wi-Fi. It also pins `turbopack.root` to this folder: Next.js looks for the nearest lockfile to find the project root, and a stray `package-lock.json` in your home folder would otherwise make it watch your whole home folder.

## The proxy is a doorman, not a guard

`src/proxy.ts` runs before every request. If there is no token cookie, it redirects to `/sign-in` without rendering anything. It does **not** decide whether the token is valid; it cannot, cheaply. The Go API decides that on every call. A doorman who waves away people without a ticket saves work; the guard inside still checks every ticket.

## Live floor: pull now, push later

The live floor re-renders every 5 seconds with `router.refresh()`, and only while the tab is visible. This is the same state-based pull idea as device sync (note 05): ask for the current state, replace what you show. When push hints arrive (WebSocket or the outlet hub), only the trigger changes; the page stays the same.

## Proof in tests

| Layer | What | Where |
| --- | --- | --- |
| Unit | money and rate parsing and formatting, id checks | `src/lib/*.test.ts` (`npm test`) |
| Types | every page against the API types, route params typed by Next | `npm run typecheck` |
| API | the new owner endpoints, tenant isolation, revoke | `services/core/internal/e2e/backoffice_test.go` |
| Browser | sign up, build a menu, tables, pair and revoke a device, a captain's order on the live floor, a seeded evening's bills and books | `e2e/backoffice.spec.ts` (`make test-web`) |

The browser tests run the production build against the real API and Postgres. They sign up their own restaurants, so they never touch yours.

A second agent then attacked the slice and found 13 real problems, each with a failing test. The ones worth remembering:

- **When the API went down, the whole app showed Next's bare 500 page, and the live floor never came back.** Our friendly error page sat in `(app)/error.tsx`, but an error boundary only catches errors *below* its own layout, and the signed-in layout is what calls the API first. The fix adds `app/error.tsx` above it, and the error panel retries every 10 seconds, so a wall display recovers by itself after a restart.
- **The live floor counted dishes already paid on a split bill as still open.** It now shows `openValue`: not voided and not yet billed.
- **"Lobster, 1,200" became an add-on called "Lobster, 1" at ₹200.** The add-on parser now reads the price from the end and understands Indian digit grouping.
- **A 100-character Hindi dish name could be created but never edited.** Go's `len()` counts bytes, and the database counts characters; `utf8.RuneCountInString` fixed it. Any value Postgres refuses (too long, a NUL byte, a year 0000) is now a 400, never a 500.
- **Saving a dish's price reordered its add-on questions on the till,** and **every save bumped the menu version,** making every till re-download the menu for nothing. Edits now send groups in the item's own order, and a save that changes nothing changes nothing.
- **A 9-digit mobile number was saved as a "foreign" number.** Sign-up now refuses instead of guessing: a guessed number would get someone else's login codes.

All of them have regression tests in `e2e/hardening.spec.ts`, `src/lib/hardening.test.ts` and `services/core/internal/e2e/backoffice_hardening_test.go`. We undid two of the fixes to check the tests catch them, and they did.

## What would break at 10x?

- **Every page asks `/v1/me` and the menu again.** Fine for one owner. With many managers polling the live floor, add a short per-request cache, or move the floor to push updates.
- **One outlet per screen.** Chains need a switcher and "all outlets" reports. The context already has an outlet cookie for that.
- **Tokens instead of logins.** Phone OTP (WhatsApp or SMS) and staff roles replace pasted tokens in the identity slice. The cookie and the BFF shape stay the same; only how the token is obtained changes.

## Interview questions

<details><summary>1. Why keep a session token in an httpOnly cookie instead of localStorage?</summary>
Any script running on the page can read localStorage, including injected ones (XSS).
An httpOnly cookie is sent by the browser but cannot be read by JavaScript.
Add SameSite=Lax (or Strict) and Secure, and CSRF needs a forged same-site request, which Next.js also checks for Server Actions.</details>

<details><summary>2. What is a Backend-for-Frontend?</summary>
A server made for one frontend.
It aggregates and shapes calls to the real APIs, holds the credentials, and keeps the browser from talking to internal services.
The cost is one more hop; the benefit is a smaller attack surface and a simpler client.</details>

<details><summary>3. Server Components or client-side fetching?</summary>
Server Components fetch on the server and send HTML: less JavaScript, no waterfalls in the browser, and secrets stay on the server.
Client fetching suits highly interactive views that change without navigation.
Use the server by default and the client where the interaction needs it.</details>

<details><summary>4. If the API validates everything, why validate in the web app too?</summary>
For people, not for security: an instant, specific message beats a round trip and a generic error.
Security validation must live in the API, because anyone can call the API without our web app.</details>

## Exercises

1. Remove `mustUUID` from `setAvailable` and write a test that calls the action with `../../ledger/trial-balance` as the id. What request reaches the API?
2. Add an "outlet" dropdown to the sidebar that writes the outlet cookie. Which pages need no change at all, and why?
3. Change the live floor to refresh every 2 seconds. How many API calls does one open tab make per hour? What would you do before a 500-outlet chain uses it?
