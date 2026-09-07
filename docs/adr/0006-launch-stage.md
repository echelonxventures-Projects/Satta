# ADR-0006: Launch stage and product legality

- **Status**: **Proposed** — implementation must not assume any option below
- **Date**: 2026-08-01
- **Supersedes**: none
- **Superseded by**: none

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

**Not yet made.** Four options, in ascending order of cost and legal exposure.

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

This decision **gates roadmap scope**, which is why it is the highest-leverage item
outstanding.

If stage 1 or 2, then Tasks 10 (payments), 11 (withdrawals), 12 (KYC), 14 (risk/fraud/AML)
and 42 (tax) are **not launch-critical**, and the L/R tagging in `task-roadmap.md` needs
rewriting. Roughly a third of the launch slice disappears.

If stage 3 or 4, the cost mandate in ADR-0002 cannot hold and that ADR must be superseded.

What is **not** affected: Task 1 and the Phase 1 foundation are identical under all four
options. Implementation can proceed on the foundation without this decision. It must not
proceed into Phase 2 or 3 without it.

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
