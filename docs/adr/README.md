# Architecture Decision Records

An ADR records one decision, why it was made, and what it costs. It is written when the
decision is made, not afterwards, and it is never edited to reflect a change of mind —
a superseded ADR keeps its text and gains a `Superseded by` line.

That immutability is the point. A decision log that gets edited is indistinguishable from
a decision log that was always right, which makes it useless for the two things it exists
for: onboarding someone who wasn't there, and answering an auditor asking why a control
works the way it does.

## Status values

| Status | Meaning |
|---|---|
| Proposed | Written, not yet agreed. Implementation must not assume it |
| Accepted | Agreed and in force |
| Superseded | Replaced. Keeps its original text plus a pointer forward |
| Rejected | Considered and declined. Kept, because "why not X" recurs |

## Index

| # | Decision | Status |
|---|---|---|
| [0001](0001-agnosticism-policy.md) | Agnosticism is a property of boundaries | Accepted |
| [0002](0002-stage0-cost-constrained-topology.md) | Stage-0 single-box topology under a domain-and-hosting budget | Accepted |
| [0003](0003-container-runtime.md) | Podman locally, single-node k3s in production | Accepted |
| [0004](0004-deferred-components.md) | Deferred components are values-file bindings, not deletions | Accepted |
| [0005](0005-frontend-architecture.md) | One repo, thin apps, fat shared packages | Accepted |
| [0006](0006-launch-stage.md) | Launch stage and product legality | **Proposed** |

## Decisions already documented in code

Several load-bearing decisions predate this directory and are documented as package
comments at the point of use, which is where they are most likely to be read. They are
listed here so the set is discoverable, and should be promoted to ADRs if they ever come
under challenge.

| Decision | Where |
|---|---|
| Money is integer minor units; floats rejected in payloads | `libs/go/money`, `libs/go/evidence/record.go` |
| Migrations are forward-only; no down files | `libs/go/db/migrate.go` |
| Tenant isolation is layered, not single-mechanism | `libs/go/tenancy`, `libs/go/db/pool.go` |
| Evidence hash pre-image is tagged and length-prefixed, not JSON | `libs/go/evidence/record.go` |
| Configuration has no resolve-with-default | `libs/go/config/config.go` |
| Time is injected; UTC only | `libs/go/clock` |
| Usage events carry no monetary amount | `libs/go/metering/event.go` |

## Writing one

Copy `0000-template.md`. Number sequentially. Keep it short — an ADR nobody reads is an
ADR that did not get written. State the cost honestly; an ADR with no downside listed is
usually an advertisement rather than a decision.
