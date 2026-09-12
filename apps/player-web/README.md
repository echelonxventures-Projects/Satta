# apps/player-web — the player PWA (roadmap Task 32, L)

Intentionally empty. Next.js, theming, lobby, cashier, and game launcher all
land with Tasks 17–32; a scaffold filled in ahead of its backend becomes a
second source of truth for policy the config engine already owns (DoD gate 1
applies to the browser too).

Before any commit here:

- UI strings live in `@satta/i18n` (ICU, no concatenation — ADR-0005).
- Money display uses `@satta/money` / `@satta/formatting` with the shared
  golden vectors passing in CI (Invariant 1 must not break at the browser).
- The client never decides jurisdiction; the server delivers capabilities.
- WCAG 2.2 AA is not optional (DoD gate 8), RTL from the start.
