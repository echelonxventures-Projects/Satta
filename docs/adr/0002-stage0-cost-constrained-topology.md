# ADR-0002: Stage-0 single-box topology under a domain-and-hosting budget

- **Status**: Accepted (topology); **cost mandate superseded by [ADR-0007](0007-production-cost-posture.md)** following the ADR-0006 Stage 4 acceptance
- **Date**: 2026-08-01
- **Supersedes**: none
- **Superseded by**: [ADR-0007](0007-production-cost-posture.md) (cost mandate only; stage-0 topology remains the dev/local topology)

## Context

Two directives: zero cost on the local machine, and minimum cost for live — domain and
hosting only.

Local is already free. Every component is open-source and self-hostable, and no paid
tooling is required. The binding local constraint is RAM, not money: the full substrate is
8-12GB, which exceeds a typical laptop's comfortable budget.

Live is where the constraint bites, and where it collides with two existing requirements.
`project-context.md` requires enterprise scale and millions of concurrent users;
`delivery-realities.md` states 12-18 months with 25-40 engineers. Neither is reachable on a
single small box with a solo team. That collision is resolved here by scope, not by
pretending it does not exist.

## Decision

Treat the cost mandate as a **stage-0 topology** that leaves the target architecture
intact. The architecture in `project-context.md` remains the destination; stage 0 is a
deployment profile of it, not a replacement for it.

Local development is tiered so the inner loop stays cheap:

| Tier | Contents | When it runs |
|---|---|---|
| 0 | Nothing. In-memory ports, embedded NATS | `go test ./...` — must never need a substrate |
| 1 | Postgres, Redis | Integration tests, ordinary development |
| 2 | ClickHouse, OpenSearch, Vault, Temporal, observability | On demand only |

Production stage 0 is one box:

| Line | Choice | Cost |
|---|---|---|
| Compute | Oracle Cloud Always Free (4 ARM cores, 24GB, 200GB) | $0 |
| Compute (preferred, reliable) | Hetzner CX32 (4 vCPU, 8GB) | ~€6.80/mo |
| Domain | Cloudflare Registrar, sold at wholesale | ~$10/yr |
| TLS | Let's Encrypt | $0 |
| CDN / WAF / DDoS | Cloudflare free tier | $0 |
| Object storage | On-box MinIO, or Cloudflare R2 free tier | $0 |
| Transactional email | Resend or MailerSend free tier | $0 |
| Observability | Grafana Cloud free tier — saves ~1.5GB RAM | $0 |
| CI / registry | GitHub Actions + GHCR | $0 |
| **Offsite backups** | Hetzner Storage Box 1TB — **not optional** | €3.20/mo |

Target footprint is roughly 2.5-3GB resident: Postgres 1GB, Temporal 300MB, Go services
500MB, Next.js 300MB, Redis 200MB, NATS and Caddy under 150MB.

## Consequences

Easy: the whole platform runs for under €10/month, and the inner development loop needs no
substrate at all.

Hard:

- **Enterprise scale and zero downtime are not met at stage 0.** A single box has no HA.
  This is a deliberate scope decision, and `project-context.md` should be read as the target
  rather than the current state.
- **Backups are the one line item that cannot be cut.** A platform holding a financial
  ledger with no offsite backup is not cheap, it is negligent.
- Oracle's free tier carries idle-instance reclamation, scarce ARM capacity, and a
  well-documented history of account terminations. Acceptable only because ADR-0003 keeps
  everything reproducible — migration is a values-file change plus a restore.
- Component deferrals are governed by ADR-0004 and must not become deletions.

## Alternatives considered

**Managed cloud services** (RDS, MSK, managed OpenSearch). Rejected on cost; would be
thousands per month, and would introduce provider coupling that ADR-0001 forbids.

**Free-tier PaaS** (Fly, Railway, Render). Rejected: free allowances have repeatedly been
withdrawn, and their primitives are proprietary enough to constitute coupling.

**Cut backups to reach true $0.** Rejected. See above.

## Invariant impact

- **Invariant 4 (evidence retention)** — at risk from *operational* rather than design
  causes. A single box with no offsite backup cannot honour a retention period, which is why
  backups are mandatory in this ADR rather than advisory.
- All others — untouched. Topology does not alter the invariants; it alters how much
  headroom exists before they are stressed.
