# ADR-0006: Launch stage and product legality

- **Status**: **Accepted** — Stage 4, licensed real money (2026-09-07)
- **Date**: 2026-08-01 (proposed), 2026-09-07 (accepted)
- **Supersedes**: none
- **Superseded by**: none (cost-mandate consequence handled by ADR-0007)

## Context

ADR-0002 commits to a domain-and-hosting budget. That budget is achievable for a
non-real-money product and **legally impossible** for real-money wagering, so the two
decisions are coupled and this one cannot be deferred indefinitely.

Costs that precede a single real-money bet, none of which are hosting:

| Item | Order of magnitude |
|---|---|
| Malta MGA licence | ~€25k application, ~€25k/yr, plus €100k issued share capital |
| Audited accounts, compliance officers | Recurring, staffed |
| RNG and game-math lab certification | €10-50k per engagement |
| Card acquiring | Licensed entity, 5-10% rolling reserve held ~6 months |
| Per-jurisdiction legal and tax advice | Ongoing, per market |
| KYC/AML vendor | Per-check fees |

`delivery-realities.md` already states this: the licence track runs 6-12 months in parallel
with engineering, and engineering completion without it yields a platform that cannot legally
take a bet.

## Decision

**Stage 4 — licensed real money.** Accepted 2026-09-07 per the Master Build Plan
(`docs/PLAN.md` D1, v1.3). Stages 1–3 are **not abandoned**: they run as progressive
pre-launch environments — stage 1 (play-money) from M2, stage 2 (social casino) and
stage 3 (sweepstakes) as beta environments through M4 — exercising the full architecture
honestly before real-money go-live at M6.

The four options considered, in ascending order of cost and legal exposure:

**Stage 1 — play-money / demo / portfolio.** No purchase, no prize of value. Legal
essentially everywhere. Exercises the full architecture honestly. Fits the cost mandate with
no compromise.

**Stage 2 — social casino.** Virtual coin purchases, no cash redemption. Requires a company,
terms, privacy policy and a payment processor. Note that Stripe's terms restrict
gambling-adjacent products, so processor selection needs checking before the cashier is
built.

**Stage 3 — sweepstakes dual-currency** (Task 25). A US legal grey zone requiring specialist
counsel; five figures minimum before launch.

**Stage 4 — licensed real money.** Six figures minimum before launch, 6-12 months of parallel
licensing work.

## Consequences

Stage 4 chosen: Tasks 10 (payments), 11 (withdrawals), 12 (KYC), 14 (risk/fraud/AML) and
42 (tax) remain **launch-critical**; the L/R tagging in `task-roadmap.md` stands. The cost
mandate in ADR-0002 **cannot hold** and is superseded by ADR-0007.

What was **not** affected remains unchanged: Task 1 and the Phase 1 foundation are
identical under all four options, which is why foundation work proceeded while this was
Proposed.

## Alternatives considered

**Build for stage 4 and downgrade if needed.** Rejected: it means building payments, KYC and
AML infrastructure that may never be used, at the cost of the features that would actually
ship.

**Build for stage 1 and upgrade later.** Viable, and the port discipline in ADR-0001 makes it
much cheaper than it would otherwise be. The unavoidable cost is that compliance obligations
are not retrofittable onto a design that never anticipated them — which is why the
foundation retains KYC, RG and AML *ports* even under stage 1.

**Defer the decision.** What is happening now. Acceptable only while work stays inside
Phase 1.

## Invariant impact

None of the 12 change with stage. Their *scope of application* narrows under stages 1 and 2 —
Invariant 5 (self-exclusion) still applies to a play-money product, because harm from
simulated gambling is real and several jurisdictions regulate it. Nothing here licenses
dropping an invariant.
