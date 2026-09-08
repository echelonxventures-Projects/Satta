# ADR-0007: Production cost posture supersedes the ADR-0002 budget mandate

- **Status**: Accepted
- **Date**: 2026-09-07
- **Supersedes**: ADR-0002's cost mandate (the <€10/mo stage-0 budget cap)
- **Superseded by**: none

## Context

ADR-0006 was accepted at Stage 4 (licensed real money). Its own consequences state that
the stage-3/4 cost profile cannot hold under ADR-0002's <€10/mo single-box mandate: an
MGA-class licence (~€25k application, ~€25k/yr, ~€100k share capital), RNG and game-math
lab certification (€10–50k per engagement), high-risk card acquiring (5–10% rolling
reserve), per-check KYC/AML vendor fees, and per-jurisdiction counsel are all costs that
precede a single real-money bet.

ADR-0002 made two separable commitments: (a) a **topology** — tiered local substrate,
stage-0 single-box, deferred components as values-file bindings — and (b) a **budget
mandate** that the entire platform must fit under €10/mo. Stage 4 invalidates (b) but
not (a).

## Decision

1. The **ADR-0002 budget mandate is superseded.** Production runs on infrastructure sized
   for a licensed real-money operation: managed Postgres HA, offsite backups + PITR,
   production secret management (Vault), and observability retention that reflects
   regulatory retention obligations rather than dev defaults.
2. The **ADR-0002 stage-0 topology remains valid as the dev/local topology** — the tiered
   compose substrate and stage-0 single-box deployment stay exactly what ADR-0002 and
   ADR-0004 specify for development, CI, and the stage-1–3 pre-launch environments.
   Nothing in that ADR's topology work is wasted; only its budget ceiling is retired.
3. Deferred components remain deferrals (ADR-0004): re-adoption is still triggered by
   load/obligation, now with real-money launch as an explicit trigger for the S51
   production-infra hard gate (HA, backups, prod secrets) before M6.

## Consequences

- The Master Build Plan's S51 production infra hard gate is now **mandatory before M6**
  (real-money launch), not a cost-optional upgrade.
- Dev/local costs stay at stage-0 levels; the cost curve bends at staging (S28a) and again
  at production (S51).
- ADR-0002's "cheap is negligent without offsite backup" principle is retained and
  strengthened: offsite backup + PITR are the first production infra items funded.

## Alternatives considered

**Keep the mandate and launch stage 3.** Rejected: launch posture is decided (ADR-0006
Stage 4); the mandate cannot survive it.

**Reinterpret the mandate as "dev-only."** Rejected as silent drift — the supersede is
explicit so future readers see the coupling, per the repo's own ADR discipline.

## Invariant impact

None. The 12 invariants are stage-independent (ADR-0006). This ADR only changes what
infrastructure budget the platform may assume at each lifecycle stage.
