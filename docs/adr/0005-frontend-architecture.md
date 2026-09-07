# ADR-0005: One repo, thin apps, fat shared packages

- **Status**: Accepted
- **Date**: 2026-08-01
- **Supersedes**: none
- **Superseded by**: none

## Context

The requirement is a single codebase covering player apps, operator portals, web, and CRM,
supporting multiple languages, currencies, regions and countries.

"Single codebase" admits two readings. One is a monorepo with aggressive code sharing, which
is correct and valuable. The other is a single application serving every surface, which fails
for ordinary engineering reasons — a player PWA and a dense trading grid have opposite
bundle-size, latency and accessibility profiles — and for one reason specific to this domain
that is not negotiable.

Safety Invariant 6 requires ad and comms decisioning to be structurally unable to reach KYC
documents or payment instruments. Invariant 11 requires auditor roles to be structurally
read-only. If the CRM console, the KYC review screen and the auditor view are one bundle
behind one auth context, both invariants become runtime flags — a permission check that a bug,
a misconfiguration or a malicious contributor can flip. "Structurally" is precisely what rules
that out.

## Decision

One repository. Many **thin** apps, each independently deployable with its own bundle and its
own auth boundary. Fat shared packages beneath them.

```
apps/
  player-web/        Next.js PWA              Task 32  L
  admin-console/     React                    Task 37  L
  trading-console/   React, dense grids       Task 37  L
  crm-console/       React                    Task 30  R
  partner-portal/    React                    Task 37/46 — see note
  player-mobile/     React Native             Task 33  R
  retail-pos/        React, offline-first     Task 35  R
  agent-portal/      React                    Task 36  R
packages/
  api-client/        generated from proto/ — the load-bearing piece
  domain/            shared TypeScript types and pure logic
  money/             integer minor units, currency display
  i18n/              ICU catalogs, locale negotiation, fallback chains
  formatting/        UTC to display timezone, numbers, dates
  ui/                design system, WCAG 2.2 AA primitives
  theming/           per-tenant and per-brand runtime tokens
  auth/              session handling, permission gates
  config-client/     config and rules engine client
```

**Note on `partner-portal`**: Task 37 is L-tagged and its scope includes a "partner
self-service portal", while Task 46 (B2B partner platform) is R-tagged and also covers it.
The roadmap is ambiguous here. Treated as R until Task 37's launch scope is narrowed
explicitly, because shipping a partner portal before there are partners is unlikely to be
launch-critical. This needs confirming.

`packages/api-client` is **generated from the same protobuf that generates the Go services**.
Contracts are shared, not re-typed. This is what makes "single codebase" a real property
rather than an aspiration, and it is also what reconciles a single TypeScript frontend with
the language agnosticism of ADR-0001 — backends share contracts and test vectors, not source.

Rules that follow:

- **Share logic, not pixels.** Non-UI packages cross Next.js and React Native cleanly.
  Components do not, and attempting it is the standard way these projects fail.
- **`packages/money` must reproduce the same golden vectors as `libs/go/money`.** Otherwise
  Invariant 1 holds on the server and silently breaks in the browser. The display layer
  formats a server-computed amount and performs no money arithmetic. Minor-unit exponents
  vary — JPY 0, USD 2, KWD 3, crypto to 18. FX conversion is server-side only.
- **i18n uses ICU MessageFormat, never concatenation**, because plural and gender rules differ
  per language and concatenation cannot express them. Locale propagates like tenancy scope.
  UI strings live in `packages/i18n`; tenant content lives in the CMS (Task 28) — distinct
  systems that must not be conflated. RTL from the start. Translation vendors behind a port.
- **The client never decides jurisdiction.** The server derives it from session, geo and
  licence, and delivers capabilities. A client-supplied jurisdiction is a compliance bypass.
- **Brand theming is runtime tokens, not build-time.** Otherwise every release is N builds and
  the single-codebase benefit is lost at deploy.

Stage 0 builds two apps: `player-web` and `admin-console`. The rest is directory structure and
shared packages.

## Consequences

Easy: one dependency graph, one lint and test configuration, atomic changes across contract
and consumers, and no version skew between an API and its clients.

Hard:

- Monorepo tooling becomes load-bearing. A slow or flaky build graph taxes every change.
- Eight apps is a lot of surface. Six of them are R-tagged and must stay unbuilt, which
  requires discipline — scaffolding invites filling in.
- Shared packages need real API discipline. A careless change to `packages/money` breaks every
  surface at once, which is the cost of the sharing that makes this worthwhile.
- Runtime theming is harder than build-time theming, and rules out some CSS-level
  optimisations.

## Alternatives considered

**One application with role-based routing.** Cheapest to build. Rejected: it reduces
Invariants 6 and 11 to runtime flags, which is disqualifying regardless of its other merits.

**Separate repositories per app.** Better isolation. Rejected: contract drift between an API
and its clients is then invisible until runtime, which is precisely what generating
`api-client` from `proto/` exists to prevent.

**Share UI components across web and native** via react-strict-dom or similar. Rejected for
now; the lowest common denominator degrades both, and a trading grid shares nothing useful
with a mobile bet slip. Reconsider for narrowly-scoped primitives only.

## Invariant impact

- **Invariant 1 (integer money)** — preserved into the client, but only because
  `packages/money` shares golden vectors with the Go implementation. Without that, this ADR
  would weaken it.
- **Invariant 6 (ad/comms isolation)** — strengthened. Separate bundles and auth boundaries
  make the isolation structural rather than a permission check.
- **Invariant 11 (auditor read-only)** — strengthened, for the same reason.
- **Invariant 12 (single time authority)** — preserved. `packages/formatting` applies display
  timezone at the edge and never stores a local time.
