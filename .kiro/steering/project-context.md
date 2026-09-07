# Project Context — Global Multi-Vertical Gambling Platform

## Problem Statement

Build a platform-agnostic, multi-tenant, multi-region gambling platform covering every gambling vertical, every payment rail, every distribution channel, and every jurisdiction — operating simultaneously as a B2C brand operator and a B2B white-label/API SaaS. Every capability must be independently sellable, metered, billable, monitored, auditable, and reconcilable. Zero hardcoded business policy, zero vendor coupling. Must run end-to-end on a single developer machine with no cloud account, and deploy unchanged to any cloud, hybrid, or on-premise target.

## Requirements

| Area | Requirement |
|------|-------------|
| Verticals | Casino, live dealer, slots, table games, crash/originals, sportsbook, esports, virtuals, exchange, pari-mutuel/racing, poker, tournaments, skill games, lottery, bingo, DFS, sweepstakes/social — infinitely extensible |
| Scale | Enterprise, millions concurrent, zero downtime |
| Tenancy | Multi-tenant, multi-brand, multi-country, multi-region; own brand + white-label + API partners |
| Payments | All fiat rails, all e-wallets, all crypto, multi-currency with conversion |
| Wallets | Multi-wallet with configurable distribution/waterfall, wallet conversion, win routing |
| Money math | Rake, winner calculation, payout strategies, FX, commissions, tax — all pluggable |
| Compliance | All jurisdictions configurable, KYC/AML, responsible gambling, harm detection, audit, certification |
| Channels | Web, mobile apps, retail/land-based, agent networks, SMS/USSD/IVR/messaging |
| Commercial | Everything billable and sellable, entitlements, metering, invoicing, SLA, marketplace |
| Growth | Offers platform, gamification, affiliates, ad platform, SEO, CMS, social, CRM, communications |
| Intelligence | P&L, all analysis domains, AI/ML, forecasting, reporting, statements, reconciliation, logs |
| Principle | Zero hardcoding, all agnostic, with a defined set of safety invariants |

## Architecture (Mermaid)

```mermaid
graph TB
    subgraph Channels
        WEB[Web PWA]
        APP[Mobile Apps]
        RETAIL[Retail POS / SSBT]
        AGENT[Agent Network]
        ALT[SMS / USSD / IVR / Messaging]
        PARTNER[B2B API Partners]
        CONSOLE[Operator / Trading / Admin Consoles]
    end

    subgraph Edge
        GEO[Geo Router / CDN / WAF]
        GW[API Gateway]
        RTG[Realtime Gateway]
    end

    subgraph Foundation
        WF[Workflow and Saga Orchestration]
        CFG[Config, Rules, Product Registry, Entitlements]
        SCH[Scheduling and Calendars]
        IAM[Auth, IAM, Tenancy]
        EVID[Tamper-Evident Evidence Store]
    end

    subgraph MoneyCore
        LED[Ledger and Multi-Wallet]
        FX[Currency and FX]
        FIN[Financial Engine: rake, winners, payouts]
        PAY[Payments]
        WD[Withdrawals]
        CRYPTO[Crypto Custody and On-Chain]
    end

    subgraph Compliance
        KYC[KYC and Onboarding]
        RG[Responsible Gambling and Harm Detection]
        RISK[Risk, Fraud, AML]
        SECOPS[Security Operations]
    end

    subgraph Verticals
        GAL[Game Aggregation and RNG]
        CAS[Casino, Live Dealer, Jackpots]
        SB[Sportsbook, Esports, Virtuals]
        EX[Exchange]
        TOTE[Pari-Mutuel and Racing]
        PKR[Poker, Tournaments, Skill]
        DRAW[Lottery and Bingo]
        DFS[Fantasy Sports]
        SWEEP[Sweepstakes and Social]
    end

    subgraph Growth
        OFF[Offers, Bonuses, Gamification]
        EXP[Experimentation and Flags]
        CMS[CMS and SEO]
        CHAT[Chat and Community]
        CRM[CRM and VIP]
        COMMS[Communication Orchestration]
        ADS[Ad Platform]
    end

    subgraph Intelligence
        DATA[Analytics and Data Platform]
        ML[AI/ML Platform]
        PNL[P and L and Analysis Suite]
    end

    subgraph MoneyOps
        COMM[Commission Platform]
        TAX[Global Tax Engine]
        RECON[Recon, Reporting, Statements, Logs]
        REGOPS[Regulatory Ops and Accounting]
        BILL[Commercial Platform and Billing]
        B2B[Partner Platform and Feeds]
        AUDIT[Audit and Certification]
        MIG[Onboarding and Migration]
    end

    subgraph Infrastructure
        REGION[Multi-Region and Residency]
        PROD[Production Readiness]
    end

    Channels --> GEO --> GW
    WEB --> RTG
    GW --> Foundation
    GW --> MoneyCore
    GW --> Verticals
    Verticals --> FIN --> LED
    MoneyCore --> Compliance
    Verticals --> Compliance
    Growth --> Compliance
    MoneyCore --> MoneyOps
    Verticals --> Intelligence
    Growth --> Intelligence
    Intelligence --> MoneyOps
    MoneyOps --> AUDIT
    Foundation --> EVID
    Infrastructure --> Foundation
```

## Technology Stack

| Concern | Choice | Rationale |
|---------|--------|-----------|
| Core services | Go | Latency, concurrency, operational simplicity, hiring pool |
| Game math, RNG, fairness | Rust (native + WASM) | Determinism, memory safety, identical client/server verification |
| Exchange & tote matching | Rust, single-writer per market | Deterministic ordering, predictable tail latency |
| Realtime fan-out | Elixir / Phoenix | Proven millions of persistent connections |
| Durable execution | Temporal (or equivalent, behind a port) | Sagas, compensation, exactly-once scheduling |
| Web | Next.js + TypeScript | SSR/ISR for SEO, per-tenant theming |
| Mobile | React Native | Shared domain logic with web |
| Consoles | React + dense data-grid UI kit | Operational tables and dashboards |
| OLTP | PostgreSQL + Citus | ACID, tenant partitioning, mature tooling |
| Cache / leaderboards | Redis Cluster | Latency, sorted sets |
| Event backbone | Kafka (prod) / NATS JetStream (local) | Durable audit stream, replay |
| Analytics | ClickHouse | Billions of events, sub-second aggregation |
| Search / logs | OpenSearch | Vendor-neutral |
| Secrets / keys | Vault + PKCS#11 | Rotation, HSM-ready for RNG and custody |
| Custody | MPC/HSM behind a port | Crypto hot/warm/cold segregation |
| Orchestration | Kubernetes; k3s locally | Cloud-agnostic by construction |
| Local dev | Docker Compose + MinIO + MailHog + mock vendors | No cloud account required |
| CI/CD | GitHub Actions + Argo CD + Helm | GitOps, reproducible environments |
| Observability | Prometheus, Grafana, Loki, Tempo | Portable, no lock-in |
