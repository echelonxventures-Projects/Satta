# Task Roadmap — 52 Tasks Across 7 Phases

L = launch-critical (first-release slice). R = roadmap (post-launch).

## Phase 1 — Foundation

| # | Task | Priority | Scope |
|---|------|----------|-------|
| 1 | Monorepo scaffolding and fully local infrastructure | L | Monorepo layout, Docker Compose substrate (Postgres, Redis, Kafka/NATS, ClickHouse, OpenSearch, MinIO, Vault, MailHog, Prometheus/Grafana), Go service template, shared protobuf + codegen, Taskfile, linting, pre-commit, ADR directory, tamper-evident evidence store skeleton, metering event SDK |
| 2 | Workflow orchestration (durable execution) | L | Temporal-based saga engine behind a port, compensation logic, exactly-once scheduling, local worker pool |
| 3 | Configuration & rules engine + product/SKU registry + entitlements + metering contract | L | Hierarchical config (global > jurisdiction > tenant > brand), rules engine for policy evaluation, product catalog, entitlement gate, metering contract definition |
| 4 | Scheduling, calendars & temporal control | L | Cron-like scheduler, calendar-aware windows (e.g. maintenance, promotions), timezone-safe execution |
| 5 | Test data management & simulation harness | L | Seed data generators, scenario replay, load simulation, synthetic player factory |
| 6 | Auth, IAM & multi-tenancy | L | OAuth2/OIDC, RBAC + ABAC, tenant isolation, API key management, session management, MFA |

## Phase 2 — Money Core

| # | Task | Priority | Scope |
|---|------|----------|-------|
| 7 | Ledger & multi-wallet core | L | Double-entry ledger, multi-wallet per player, waterfall distribution, wallet-to-wallet transfers, balance snapshots |
| 8 | Currency & FX conversion | L | Multi-currency support, FX rate feeds behind port, conversion with spread, rate locking |
| 9 | Financial engine: rake, winners, payouts | L | Pluggable rake strategies, winner determination, payout waterfall, hold/release cycles |
| 10 | Payments & deposits | L | PSP abstraction port, card/bank/e-wallet/crypto processors, deposit routing, 3DS, retry logic |
| 11 | Withdrawals & winner payouts | L | Withdrawal queue, approval workflows, KYC gates, payout batching, reversal handling |

## Phase 3 — Compliance, Risk, Realtime

| # | Task | Priority | Scope |
|---|------|----------|-------|
| 12 | KYC, onboarding & jurisdiction compliance | L | Identity verification port, document upload, jurisdiction-specific flows, enhanced due diligence triggers |
| 13 | Responsible gambling & harm detection | L | Self-exclusion, deposit/loss/time limits, reality checks, AI-driven harm signals, cooling-off enforcement |
| 14 | Risk, fraud & AML | L | Transaction monitoring, velocity rules, device fingerprinting, SAR generation, chargeback management |
| 15 | Security operations (SIEM, PAM, insider threat) | L | Log aggregation for security, privileged access management, anomaly detection, incident response automation |
| 16 | Realtime backbone & notifications | L | Event bus fan-out, WebSocket/SSE gateway, push notifications, presence tracking |

## Phase 4 — Game Verticals

| # | Task | Priority | Scope |
|---|------|----------|-------|
| 17 | Game aggregation layer, RNG & provable fairness | L | Provider integration port, game session lifecycle, RNG with seed-commit, fairness verification API |
| 18 | Casino verticals, live dealer & jackpots | L | Slot/table/crash game integration, live dealer streams, progressive jackpot pools, bonus-round triggers |
| 19 | Sportsbook: events, odds, trading, esports, virtuals | L | Event creation, market/outcome modeling, odds feed integration, trading tools, live/pre-match, esports and virtuals |
| 20 | Betting exchange | R | Order book matching engine (Rust), back/lay, market making, commission on net winnings |
| 21 | Pari-mutuel & horse racing | R | Pool-based betting, dividend calculation, racing calendar integration, tote protocols |
| 22 | Poker, tournaments, skill games & P2P | R | Table management, hand engine (Rust), tournament structures, rake collection, P2P matchmaking |
| 23 | Lottery, bingo & draw games | R | Draw scheduling, ticket generation, prize tiers, syndicate play, regulatory draw witnessing |
| 24 | Daily fantasy sports | R | Contest creation, salary cap, scoring engine, prize pool distribution, lineup management |
| 25 | Sweepstakes and social casino (dual-currency) | R | Gold/sweeps coin economy, purchase flows, redemption to cash, social features, no-purchase-necessary entry |

## Phase 5 — Growth, Engagement, Clients

| # | Task | Priority | Scope |
|---|------|----------|-------|
| 26 | Offers, bonuses, gamification & affiliates | L | Bonus engine, wagering requirements, free spins/bets, loyalty points, achievements, affiliate tracking |
| 27 | Experimentation & feature flag platform | R | A/B/n testing, gradual rollouts, cohort targeting, statistical significance engine |
| 28 | CMS, SEO & landing page builder | L | Headless CMS, per-tenant content, SEO metadata, landing page templates, i18n content |
| 29 | Chat, social & community | L | In-app messaging, chat rooms, social feeds, moderation tools, tipping |
| 30 | CRM & VIP management | R | Player segmentation, VIP tiers, host assignment, lifecycle triggers, knowledge base |
| 31 | Communication & engagement orchestration | L | Email/SMS/push/in-app orchestration, journey builder, template engine, delivery tracking, opt-out |
| 32 | Player web application | L | Next.js PWA, per-tenant theming, responsive, lobby, account, cashier, game launcher |
| 33 | Mobile applications | R | React Native apps (iOS/Android), biometric auth, push, offline-capable |
| 34 | Alternative channels: SMS, USSD, IVR messaging | R | Bet-via-SMS, USSD menus, IVR integration, session management for low-bandwidth |
| 35 | Retail & land-based channel | R | POS terminal integration, SSBT, ticket printing, retail settlement |
| 36 | Agent & sub-agent network | R | Agent hierarchy, commission tiers, player registration via agent, agent wallet, reporting |
| 37 | Admin, operator, trading & partner consoles | L | React admin dashboard, operator tools, trading interface, partner self-service portal |

## Phase 6 — Money Ops, Commercial, Intelligence

| # | Task | Priority | Scope |
|---|------|----------|-------|
| 38 | Analytics & data platform | L | ClickHouse pipelines, event schemas, real-time dashboards, cohort analysis, data warehouse |
| 39 | AI/ML platform | R | Feature store, model registry, inference engine, recommendation systems, churn prediction |
| 40 | P&L, performance & analysis suite | L | Revenue attribution, margin analysis, product-level P&L, forecasting, benchmarking |
| 41 | Unified commission platform | R | Multi-level commissions, revenue share, CPA, hybrid models, automated calculation and payment |
| 42 | Global tax engine | L | Jurisdiction-specific tax rules, withholding calculation, tax reporting, player tax certificates |
| 43 | Reconciliation, reporting, statements & log governance | L | Auto-reconciliation, regulatory reports, player statements, data retention, log lifecycle |
| 44 | Regulatory operations, accounting & player rights | L | License compliance workflows, regulatory submissions, player data rights (GDPR), account closure |
| 45 | Commercial platform: packaging, entitlements, billing, SLA | R | Product packaging, usage-based billing, invoice generation, SLA monitoring, marketplace |
| 46 | B2B partner platform with APIs & data feeds | R | Partner onboarding, API marketplace, data feed subscriptions, sandbox environment |
| 47 | Ad platform | R | Internal ad serving, targeting rules (RG-compliant), impression tracking, revenue attribution |
| 48 | Crypto & digital asset platform | R | Hot/warm/cold wallet management, MPC custody, on-chain settlement, blockchain analytics for AML |

## Phase 7 — Assurance, Migration, Production

| # | Task | Priority | Scope |
|---|------|----------|-------|
| 49 | Audit & certification | L | Control framework mapping, evidence collection automation, lab certification pipeline, SOC2/ISO prep |
| 50 | Operator onboarding & data migration | R | Tenant provisioning automation, data import pipelines, migration validation, parallel-run tooling |
| 51 | Multi-region deployment & data residency | L | Region-aware routing, data residency enforcement, cross-region replication strategy, failover |
| 52 | Production readiness | L | Chaos testing, capacity planning, runbooks, incident management, SLA dashboards, DR drills |

## Traceability Matrix (Requirement → Tasks)

| Requirement | Tasks |
|-------------|-------|
| Verticals (all) | 17-25 |
| Scale & zero downtime | 51, 52 |
| Multi-tenant/brand/country/region | 3, 6, 51 |
| All payment rails | 10, 11, 48 |
| Multi-wallet | 7, 8, 9 |
| Money math (rake, FX, commissions, tax) | 9, 41, 42 |
| Compliance & KYC/AML | 12, 13, 14 |
| All channels | 32-36 |
| Commercial & billing | 3, 45 |
| Growth & engagement | 26-31, 47 |
| Intelligence & analytics | 38-40 |
| Zero hardcoding | 3 + cross-cutting DoD |
| Security | 6, 14, 15, 52 |
| Reconciliation & reporting | 43, 44 |
| Audit & certification | 49 |
