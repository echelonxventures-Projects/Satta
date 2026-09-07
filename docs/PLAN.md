# Satta Platform — Master Build Plan

**Version:** 1.3
**Status:** In execution — Phase 0 (Unblock)
**Home:** this file, `docs/PLAN.md`, is the single living master plan.
**Supersedes:** `Implementation Plan.docx` (frozen as a historical planning transcript; never edited, never deleted)

This is the **single, living, ever-growing build plan** for the platform. It is upgraded in
place as execution proceeds — appended to, never replaced. Governance relationships:

- Architecture decisions live in `docs/adr/` — this doc references them, and its ratified
  decisions (§1) may only be superseded, never rewritten.
- Session-level progress lives in `.kiro/steering/implementation-state.md`, updated at the
  end of each session per its own rules.
- If any steering file conflicts with this doc, **this doc plus its ADRs win**.

---

## 0. Upgrade Log

Append-only. Every execution session adds a row. Rows are never edited or deleted.

| Version | Date | Step(s) | Summary |
|---------|------|---------|---------|
| 1.0 | 2026-09-07 | — | Initial ratification. End-to-end plan S1–S52 across Phases 0–7; competitive benchmarks assimilated; living-doc conventions established. |
| 1.1 | 2026-09-07 | S0, S12a, S19a, S29a, S29b, S35a, S50a added; S20/S22/S29/S35/S37/S40/S41/S50 amended; D8–D9 + O1–O2 ratified | End-to-end gap audit folded in: edge layer, geolocation enforcement, national self-exclusion registers, explicit age gate, player-funds segregation, per-market game-certification registry, trader operations, player helpdesk, alert authoring/on-call, deferred-adapter conformance in CI, licensing procurement register, IBIA/ESSA + ADR-provider membership, first-market open decision, timeline stretched to a 20–30 month envelope for a 10–15 engineer team. |
| 1.2 | 2026-09-07 | S4a, S28a added; S4/S5/S30/S35/S45/S48 amended; D10 ratified | Residual implementation audit folded in: deployment pipeline (per-service Dockerfiles, Helm charts, Argo CD gitops — previously unowned by any step), SCA/dependency scanning in CI + external penetration test as an MGA licensing-gate deliverable, ADR-0008 stance for GDPR right-to-erasure vs append-only evidence (pseudonymization at closure), staging environment provisioning (M2/M3/M4 gates all referenced a staging that no step built), affordability/financial-risk checks, and RNG key custody (HSM/KMS-class — Vault dev-mode will not satisfy cert labs). |
| 1.3 | 2026-09-07 | S0 | Plan promoted to `docs/PLAN.md` as the single living doc; `.opencode/plans` working copy retired. Phase 0 execution begins. |
| 1.4 | 2026-09-07 | S1–S6 ☑, S7 ◐ | Phase 0 executed: initial commit on `main` (aa72d0f), repo private at `echelonxventures-Projects/Satta` + pushed, pre-commit active + all-files clean, `buf breaking` baselined, CI workflow written (S4) + SCA, ADR-0006 Accepted/0007 written/0002 pointer/0008 drafted, state + decisions log updated. Pre-existing defects found & fixed (Taskfile YAML parse bug, 82 lint findings, schema_migration owner-version collision). **S7 blocked GitHub-side**: Actions silently suppressed on the flagged account (0 runs despite admin + workflow scope); branch protection requires GitHub Pro. Follow-ups: GitHub Support contact for Actions; Pro upgrade or public repo for rulesets (staged JSON). All local gates green: build, 0 lint issues, 14/14 invariants, unit + integration tests. |

## Step status convention

Every step carries a live marker that advances forward only:

- `☐` pending
- `◐` in progress
- `☑` done `[v1.x]` — referencing the Upgrade Log row that completed it

---

## 1. Ratified Decisions

| # | Decision |
|---|----------|
| D1 | **Launch posture: Stage 4, licensed real money.** ADR-0006 accepted at Stage 4; stages 1 (play-money), 2 (social casino), 3 (sweepstakes) run as progressive pre-launch environments. Un-gates Tasks 10, 11, 12, 14, 42. |
| D2 | **Everything configurable.** Multi-entity model: each tenant/brand binds to a licensed operator entity per jurisdiction (MGA entity for regulated markets; Anjouan-class entity for permissive offshore markets; any future license is an additive config layer). Market activation = counsel sign-off + jurisdiction policy layer + entity binding — never code. The platform enforces configured policy; it makes no market-legality decisions itself. |
| D3 | **Crypto at launch.** Task 48 partially pulled forward (48a): hot/warm/cold wallets, on-chain settlement, crypto deposit/withdraw, crypto AML. MPC custody stays post-launch (48b). |
| D4 | **Aggregator-first game content.** Third-party studios via the Task 17 aggregation port. Proprietary/exclusive titles are post-launch. |
| D5 | **Team: 10+ engineers** in pod structure (§6); full 52-task scope across 7 phases. |
| D6 | **Repo: private GitHub**, branch protection on `main`, CI mirrors `task ci` exactly in GitHub Actions (anything CI runs must be runnable identically by a developer with one command). |
| D7 | **ADR-0002 cost mandate superseded by ADR-0007** (Stage-0 topology remains valid as the dev/local topology; the <€10/mo budget mandate does not survive Stage 4). |
| D8 | **Timeline stretched (v1.1).** Full launch slice retained (casino + sportsbook, all L tasks); milestones recalibrated to a 20–30 month envelope for a 10–15 engineer team. The v1.0 week numbers implicitly assumed the 25–40 engineer staffing of `delivery-realities.md` and are superseded by v1.1 month ranges in §4. |
| D9 | **Gap-audit findings are plan scope (v1.1).** The added steps (S0, S12a, S19a, S29a, S29b, S35a, S50a) and the S20/S22/S29/S35/S37/S40/S41/S50 amendments are ratified as part of this single plan — no separate audit doc exists or will exist. |
| D10 | **Residual-audit findings are plan scope (v1.2).** S4a and S28a, the S4/S5/S30/S35/S45/S48 amendments, and ADR-0008 (drafted alongside S5's ADR batch) are ratified into this single plan. Two full audit passes have now found no further engineering gaps; the remaining open items are business-side (O1, O2, and named owners for the S20 procurement register). |

**Open decisions (ratify before their owning step; tracked here, resolved by Upgrade Log rows):**

| # | Decision | Owner | Needed by |
|---|----------|-------|-----------|
| O1 | First-launch-market shortlist (counsel-gated; drives license entity, PSP, KYC/AML vendors, cert labs) | PM + counsel | Before S20 procurement contracts |
| O2 | Market go-live sign-off authority (who may flip a market active under D2) | Compliance + PM | Before M4 |

## 2. Current State (as verified at v1.0)

**Built** (Phase 1, Task 1, ~80%): 9 shared Go libraries (`money`, `evidence`, `metering`,
`tenancy`, `identity`, `config`, `clock`, `correlation`, `db`), `platformctl` CLI
(migrate / seed / verify-evidence), protobuf contracts (common/audit/metering), tiered
Docker Compose substrate, invariant-checking script. Build, vet, and lint are clean;
`money` and `evidence` are the only tested packages.

**Missing:** git history (0 commits — blocks pre-commit and `task breaking`), CI, installed
pre-commit hooks, authentication/IAM, any serving service, frontend, deployment manifests,
backups/DR, in-code observability (OTel present transitively, unused), integration tests,
golden test vectors, wire-level conformance suites.

**Repo-declared blockers** (from `implementation-state.md`) drive Phase 0: conformance
suites gate further service work; ADR-0006 was Proposed; repo was not committed.

## 3. Competitive Benchmarks (assimilated)

Reference set spans three market tiers:

| Tier | Sites | Posture |
|------|-------|---------|
| Regulated global | Betway, PokerStars (Flutter) | MGA-licensed, GLI/eCOGRA/IBIA-certified, heavy RG surfacing, native apps, transparency reports |
| India-facing offshore | Rajabets, casinokhajana | Anjouan-licensed, cricket/kabaddi/Teen Patti/Andar Bahar, UPI/Net Banking, INR, Hindi, aggressive promos |
| Crypto offshore | Rolletto, MyStake, LuckyCasino | Crypto rails, crash/minigames, esports-heavy, Telegram/Discord growth, geo-splitting |

Absorbed into the sequence below:

- **T18:** crash games (Aviator-class), Indian card games (Teen Patti, Andar Bahar), game
  shows, jackpot pools, tournaments, daily drops
- **T19:** cash-out, bet builder, in-play, cricket/kabaddi market depth, esports, virtuals
- **T26/T30:** promo engine elevated to a core product surface — deposit-tiered free spins,
  instant cashback, VIP tiers/wheel, races/challenges (reference tier is bonus-heavy)
- **T10/T48a:** UPI/Net Banking/e-wallet adapters + crypto rails at launch
- **T14:** crypto transaction monitoring, blockchain analytics, travel-rule support
- **T32 / DoD-8:** multi-language (Hindi, Bengali, …) and locale-aware money display
  (lakh/crore digit grouping — PokerStars ships this today)
- **T26/T49:** affiliate programs and review-site ecosystem presence
- **T51:** geo-splitting as jurisdiction config
- **T32/T33:** PWA-first launch, native apps post-launch

## 4. The Master Sequence

> **Timeline note (v1.1, D8):** phase week-numbers below are the v1.0 planning envelope
> written against the 25–40 engineer staffing assumption. With the ratified 10–15 engineer
> team, treat each phase as taking ~2× its listed span; the binding gates are M1–M6, not
> calendar weeks. The overall real-money launch envelope is 20–30 months.

### Phase 0 — Unblock (Week 1)

| Step | Status | Work |
|------|--------|------|
| S0 | ☑ [v1.3] | **v1.1:** Promote this plan to `docs/PLAN.md` (single living doc); retire the `.opencode/plans` copy; record the promotion in the Upgrade Log |
| S1 | ☑ [v1.4] | Initial commit on `main` (before hooks install, so `no-commit-to-branch` cannot block it) |
| S2 | ◐ [v1.4] | `gh repo create satta --private` + push + branch protection on `main` via rulesets — push ✓, tracking ✓; ruleset staged but requires GitHub Pro on private repos |
| S3 | ☑ [v1.4] | `pipx install pre-commit` → `pre-commit install` → `run --all-files` → baseline `task breaking` |
| S4 | ☑ [v1.4] | `.github/workflows/ci.yml` mirroring `task ci`: build → lint (go + proto + invariants) → unit → integration with local substrate. **v1.2 (G14):** + SCA/dependency-scanning stage in CI (gosec already lint-local; add govulncheck-class dependency scan) — supply-chain hygiene from the first commit, not retrofitted |
| S4a | ☐ | **v1.2 (G13):** Deployment pipeline: per-service Dockerfile + Helm chart + Argo CD app-of-apps on k3s (per ADR-0003), starting with the S12 service template — deployability proven at M1, not discovered at S51. GHCR images published from CI. Owned by SRE pod; grows one chart per service thereafter |
| S5 | ☑ [v1.4] | ADR-0006 → Accepted (Stage 4); write ADR-0007 (supersedes ADR-0002 cost mandate); add superseded-pointer to ADR-0002. **v1.2 (G15):** + draft ADR-0008 — GDPR right-to-erasure vs append-only evidence stance (pseudonymization-at-closure: evidence records persist; PII fields are crypto-pseudonymized on account closure; stance validated with counsel per market under O1)** — drafted here, decided with the first-market counsel pass |
| S6 | ☑ [v1.4] | Update `implementation-state.md` (blockers cleared; Next Up resequenced; toolchain line) |
| S7 | ◐ [v1.4] | Commit, push, `gh run watch` green — **Phase 0 exit gate**. Push ✓ (branch `phase-0/foundation-unblock`, commit 9942357); CI never triggered — GitHub-side Actions suppression on the flagged account (0 runs, 0 workflows visible, despite admin + workflow scope + Actions enabled). Local full gate green (build, lint 0, invariants 14/14, unit + integration). Unblocked via GitHub Support. |

### Phase 1 — Foundation (Weeks 2–10)

| Step | Status | Work |
|------|--------|------|
| S8 | ☐ | Wire-level gRPC conformance suites (`evidencetest` in-process → wire) — hard gate before any service work |
| S9 | ☐ | ADR-0001 retrofits: evidence PG store `FOR UPDATE` → optimistic concurrency on head sequence; trigger/RLS demoted to optional hardening; INSERT-only grants conformance-asserted |
| S10 | ☐ | Golden test vectors: money / ratio / rounding / hash pre-image, cross-language corpus |
| S11 | ☐ | Tests for the 10 untested packages (config, tenancy, identity, db, metering, clock, correlation, platformctl) + Postgres integration suite (RLS / grants / tamper), gated on `PLATFORM_TEST_DATABASE_URL` |
| S12 | ☐ | `services/` Go service template: OTel tracing/metrics, structured logging, httpx middleware, health/ready, graceful shutdown |
| S12a | ☐ | **v1.1 (G1):** Edge layer — API gateway, WAF/CDN, geo-router wiring; in the project-context architecture diagram but previously owned by no task; needed the moment any service serves traffic (M1) |
| S13 | ☐ | `apps/` + `packages/` scaffold + generated `api-client` (per ADR-0005) |
| S14 | ☐ | Walk all 10 DoD gates → **close Task 1** |
| S15 | ☐ | Task 2 — workflow orchestration: Temporal behind a port (sagas, compensation, exactly-once scheduling) |
| S16 | ☐ | Task 3 — config & rules engine, product/SKU registry, entitlements, metering contract **+ jurisdiction→entity routing table + market-activation gates (D2)** |
| S17 | ☐ | Task 4 — scheduler, calendars, timezone-safe temporal control |
| S18 | ☐ | Task 5 — test data management, synthetic player factory, scenario replay, load simulation |
| S19 | ☐ | Task 6 — Auth/IAM: OAuth2/OIDC, RBAC + ABAC deny-by-default, tenant isolation, API keys, sessions, MFA |
| S19a | ☐ | **v1.1 (G1):** Deploy S12a edge components behind the service template's deploy profile (gateway → template service → substrate, one command) |
| S20 | ☐ | Licensing track (external, parallel, starts now): MGA application + per-market counsel; managed by PM. **v1.1 annex — procurement register (delivery-realities "cannot build" items), each with named owner and start date:** odds/data-feed vendors · game studios + aggregator contracts · PSP/acquiring for gambling MCC (high-risk onboarding, 5–10% rolling reserve) · KYC/AML per-check vendors · blockchain analytics (D3) · certification labs (GLI/BMM — book slots early) · geolocation vendor · live-stream rights (post-launch) · **(G12)** betting-integrity membership (IBIA/ESSA) + ADR-provider membership (required in many licensed markets) |
| S21 | ☐ | **M1 gate:** Tasks 1–2 done, first service live locally via the template |

### Phase 2 — Money Core (Weeks 8–20)

| Step | Status | Work |
|------|--------|------|
| S22 | ☐ | Task 7 — double-entry ledger, multi-wallet, waterfall distribution, snapshots (integer minor units on `libs/go/money`) **+ v1.1 (G5): player-funds segregation modeling (MGA Player Funds directive; segregated ledger accounts + reconciliation + annual interest/fees pass-through policy)** |
| S23 | ☐ | Task 8 — FX feeds behind a port, conversion with spread, rate locking (parallel workstream) |
| S24 | ☐ | Task 9 — financial engine: rake strategies, winner determination, payout waterfall, hold/release |
| S25 | ☐ | Task 10 — PSP abstraction: cards, banks, e-wallets, **UPI / Net Banking / India-rail adapters, crypto rails (D3)**, deposit routing, 3DS, retries |
| S26 | ☐ | Task 48a (pulled forward) — hot/warm/cold wallets, on-chain settlement, crypto deposit/withdraw |
| S27 | ☐ | Task 11 — withdrawal queue, approval workflows, KYC gates, payout batching, reversals |
| S28 | ☐ | **M2 gate:** ledger + payments in staging on play-money (stage-1 environment) |
| S28a | ☐ | **v1.2 (G16):** Staging environment provisioned before M2 — stage-0+ substrate profile + Argo CD (from S4a), anonymized seed data via the Task 5 harness (S18), isolated secrets (Vault staging namespace), defined promote path local → staging → prod. Owned by SRE pod. (M2/M3/M4 gates all reference staging; previously no step built it) |

### Phase 3 — Compliance, Risk, Realtime (Weeks 12–26)

| Step | Status | Work |
|------|--------|------|
| S29 | ☐ | Task 12 — KYC port, document upload, jurisdiction-specific flows, EDD triggers **+ v1.1 (G4): age verification as an explicit, separately-configured gate (18+/21+ per jurisdiction) ahead of KYC — different vendors, different law** |

**Phase 3 gap-audit steps (v1.1):**

| Step | Status | Work |
|------|--------|------|
| S29a | ☐ | **(G2)** Geolocation + market-boundary enforcement: GeoComply-class port, jurisdiction verdict before every real-money session, enforcement wired to D2 market-activation gates |
| S29b | ☐ | **(G3)** National self-exclusion register integrations behind a port — OASIS (DE), Spelpaus (SE), ROFUS (DK), ADM (IT) class — one adapter per activated market, legal register-matching (name/DOB), integrated with Task 13 self-exclusion enforcement |
| S30 | ☐ | Task 13 — responsible gambling: self-exclusion, deposit/loss/time limits, reality checks, harm signals (Invariant 5). **v1.2 (G17):** + affordability / financial-risk checks (UK-style single-source-of-truth checks; emerging regulated-market RG requirement distinct from deposit limits — policy-configured per jurisdiction, not hardcoded) |
| S31 | ☐ | Task 14 — risk/fraud/AML: transaction monitoring, velocity, device fingerprinting, SAR, chargebacks **+ crypto analytics + travel rule (D3)** |
| S32 | ☐ | Task 15 — security operations: SIEM, PAM, insider threat, incident response |
| S33 | ☐ | Task 16 — realtime backbone: event bus fan-out, WebSocket/SSE gateway, push, presence |
| S34 | ☐ | **M3 gate:** KYC / RG / AML + realtime green in staging |

### Phase 4 — Game Verticals (Weeks 18–40)

| Step | Status | Work |
|------|--------|------|
| S35 | ☐ | Task 17 — game aggregation port, game session lifecycle, RNG seed-commit, provable fairness API → **RNG lab certification (GLI/BMM) starts immediately**. **v1.2 (G18):** + RNG key custody — HSM/KMS-class key management for RNG seeds/keys (Vault dev-mode will not satisfy cert labs; production KMS binding is a certification dependency, folded into S51 Vault work) |
| S35a | ☐ | **v1.1 (G6):** Per-market game-certification registry — every game title × jurisdiction certification status tracked, enforced at lobby-assembly time (uncertified title never surfaces in a market); aggregator onboarding requirement |
| S36 | ☐ | Task 18 — casino launch content: aggregator slots/live dealer **+ crash games, Teen Patti/Andar Bahar, game shows, jackpots, tournaments, daily drops** |
| S37 | ☐ | Task 19 — sportsbook: pre-match/live, odds feed integration, trading tools **+ cash-out, bet builder, cricket/kabaddi depth**, esports, virtuals **+ v1.1 (G7): trader operations — market suspension tooling, per-market liability limits, palpable-error/void rules engine, settlement engine with results-grading workflow** |
| S38 | ☐ | **M4 gate:** casino + sportsbook beta on social/sweepstakes currency (stage-2/3 environments) |
| S39 | ☐ | Post-launch vertical waves (sequential, each licensed-jurisdiction-scoped): Task 20 exchange → 21 pari-mutuel → 22 poker → 23 lottery/bingo/draws → 24 DFS → 25 sweepstakes dual-currency |
| S40 | ☐ | Frontend co-track: Task 32 Next.js PWA (**multi-language, lakh/crore money display**) and Task 37 admin/trading consoles land here **+ v1.1 (G7): trading console scope includes liability dashboards, suspension controls, settlement grading UI** |

### Phase 5 — Growth, Engagement, Clients (Weeks 24–44)

| Step | Status | Work |
|------|--------|------|
| S41 | ☐ | Launch set: Task 26 promo engine (bonus-heavy per benchmarks: deposit-tiered free spins, instant cashback, VIP tiers/wheel, races), Task 28 CMS/SEO, Task 31 comms orchestration **+ v1.1 (G8): player support/helpdesk integration (live chat vendor behind a port) + third-party RG referral surfaces (helpline links per market, e.g. gamblingtherapy.org — every reference site ships these)** |
| S42 | ☐ | Affiliate + review-site ecosystem presence (benchmark-derived launch dependency) |
| S43 | ☐ | Post-launch: Task 27 experimentation, 29 chat, 30 CRM/VIP, 33 native mobile, 34 SMS/USSD/IVR, 35 retail, 36 agent network |
| S44 | ☐ | Task 37/46 boundary resolved per ADR-0005 (partner portal ownership) |

### Phase 6 — Money Ops, Commercial, Intelligence (Weeks 32–48)

| Step | Status | Work |
|------|--------|------|
| S45 | ☐ | Launch set: Task 38 analytics (ClickHouse pipelines, dashboards), 40 P&L, 42 tax engine, 43 reconciliation/reporting/statements, 44 regulatory ops + GDPR **implemented per ADR-0008 (v1.2): erasure = pseudonymization-at-closure over the append-only evidence store, never deletion of chain records — preserving Invariant 4 (tamper-evidence) and legal-retention obligations simultaneously** |
| S46 | ☐ | Post-launch: Task 39 AI/ML, 41 commissions, 45 commercial billing, 46 B2B partner platform, 47 ads, 48b MPC custody |
| S47 | ☐ | **M5 gate:** production readiness review complete |

### Phase 7 — Assurance & Production (Weeks 40–52+)

| Step | Status | Work |
|------|--------|------|
| S48 | ☐ | Task 49 — audit & certification: control framework mapping, evidence automation, lab cert pipeline, SOC 2 / ISO 27001 prep. **v1.2 (G14):** + external penetration test as a scheduled licensing-gate deliverable (MGA condition — booked in the S20 procurement register with the cert labs, remediation tracked to closure before M6)** |
| S49 | ☐ | Task 51 — multi-region deployment, region-aware routing, data residency, failover |
| S50 | ☐ | Task 52 — chaos testing, capacity planning, runbooks, DR drills, incident management, SLA dashboards, scheduled `verify-evidence` cron **+ v1.1 (G10): alert-rule authoring, SLO definitions, on-call rotation setup — incident management is unexecutable without them** |
| S50a | ☐ | **v1.1 (G9 / ADR-0004 obligation):** Deferred-adapter conformance in CI — every `enabled: false` port's adapter (OpenSearch, ClickHouse, Kafka, Elixir fan-out, Citus sharding) runs the ADR-0001 conformance suites in CI despite never being scheduled at stage-0. "Unbound ports are untested ports" |
| S51 | ☐ | Production infra hard gate: managed Postgres HA, offsite backups + PITR, Vault prod secrets (dev credentials purged), production observability retention (local 72h Loki is dev-only), evidence checkpoint notarisation schedule |
| S52 | ☐ | **M6 — real-money launch in the first licensed market.** Launch date is license-bound, not code-bound. Additional markets = config-layer additions (D2), each gated on counsel sign-off. |

## 5. Cross-Cutting Invariants (apply at every step)

The 12 safety invariants and the 10 Definition-of-Done gates govern all work: integer
minor-unit money · injectable UTC-only clock · deny-by-default authorization ·
tamper-evident hash-chained evidence · self-exclusion enforcement · immutable metering ·
auditor structurally read-only · RLS tenant isolation · no resolve-with-default config ·
every service ships with observability, contract tests, audit events, and i18n/WCAG.
Static checks: `scripts/check-invariants.sh`; gates walked before any task is marked done.

## 6. Team Pods (10+)

| Pod | Size | Owns |
|-----|------|------|
| Platform | 2–3 | S8–S21: conformance, retrofits, service template, Tasks 2–6 |
| Money | 2–3 | S22–S28, S45: Tasks 7–11, 48a, 42–43 |
| Compliance | 2 | S29–S34, S48: Tasks 12–15, 44, 49 |
| Games | 2–3 | S35–S40: Tasks 16–25 |
| Frontend | 2 | S40–S44: Tasks 26–32, 37 |
| SRE / DevOps | 1–2 | S4, S4a, S28a, S51–S52 and all infra: CI/CD, deploy pipeline, staging, HA, DR |
| PM | 1 | Licensing track (S20), market go/no-go gates, traceability |

## 7. Top Risks

1. **Licensing lag** (6–12 months, external) — stages 1–3 run as live test environments; launch date is license-bound, not code-bound.
2. **Conformance debt compounding** — S8 is a hard gate before any service work.
3. **"Unlimited scope" pressure** — wave-based launches keep each release shippable; ADR-0001 agnosticism keeps extension additive (new verticals/currencies/jurisdictions = config + modules).
4. **Stage-0 single box** — acceptable through M4; S51 HA gate blocks real-money go-live.
5. **Bonus-heavy market economics** — promo engine (S41) is modeled with the financial engine (Task 9) and tax engine (Task 42) before launch-market promo calibration.
6. **Market-legality exposure** — every market activation passes counsel sign-off (D2); the platform never decides legality on its own.
7. **(v1.1) Resourcing mismatch** — a 10–15 engineer team on a launch slice scoped for 25–40 stretches the calendar ~2× (D8). The mitigation is gate-based sequencing (M1–M6) and the stretched 20–30 month envelope; scope-cut levers if needed: single vertical at M4, native apps and remaining R-tasks deferred.
8. **(v1.1) Compliance mechanics discovered late** — the gap audit found geolocation, national exclusion registers, age gates, funds segregation and per-market game certification absent from the original 52 tasks (S12a, S29a, S29b, S35a, S22, S29 amendments). Late discovery of further such requirements is expected in regulated markets; the mitigation is the S20 counsel cadence and O1 first-market decision forcing the full compliance checklist early.
9. **(v1.2) Deployment debt surfacing late** — the residual audit found the deployment pipeline (Dockerfiles/Helm/Argo CD) and the staging environment owned by no step while gates M2–M4 assumed both existed (G13/G16). Mitigated by S4a/S28a moving deployability and staging provisioning to Phase 0–2. Pattern to watch: any gate that references an environment or artifact must have a numbered step that builds it.

## 8. Upgrade Rules (living-document conventions)

This doc is **ever evolving, ever growing — upgraded, never replaced**:

1. **Append-only Upgrade Log (§0).** Every execution session adds a version row. Rows are never edited or deleted.
2. **Step markers advance forward only.** `☐` → `◐` → `☑ [v1.x]`, the tag referencing the Upgrade Log row that completed the step.
3. **Sequences may be appended or re-sequenced** by adding a new Upgrade Log row and updating step markers — but ratified decisions (§1) are never rewritten; they are superseded by an explicit new row that names what replaces what (same discipline as `docs/adr/` Status fields and the decisions log).
4. **New findings enter as new rows** in their respective sections (benchmarks, risks, scope deltas) — never as edits to prior rows.
5. **Authority:** `docs/PLAN.md` + `docs/adr/` decide; `.kiro/steering/implementation-state.md` tracks session state and is updated at the end of each session per its own rules.
6. **Freshness:** `check-state-freshness.sh` is extended (S11 window) to flag this doc's staleness against actual git history.
7. `Implementation Plan.docx` remains frozen in the repo as the historical planning transcript this doc supersedes.
