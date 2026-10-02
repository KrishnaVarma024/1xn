# apps

Client apps live here. They arrive in the next slices:

| Folder | What | Stack | When |
| --- | --- | --- | --- |
| `device/` | POS, captain, KDS and hub modes in one app | Flutter, drift (SQLite), Riverpod | next: POS screens, the Dart pricing engine against `packages/golden`, the op log, and push to `/v1/sync/push` |
| `backoffice/` | owner and accountant web app | Next.js (App Router, Server Actions), Tailwind; TanStack and Radix when screens need them | **built**: see `docs/learn/06-back-office.md` |
| `owner/` | owner phone app | Flutter | v1.1 |

The Dart pricing engine must pass every case in `packages/golden/tax` before any screen uses it.
