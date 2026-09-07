# ADR-0004: Deferred components are values-file bindings, not deletions

- **Status**: Accepted
- **Date**: 2026-08-01
- **Supersedes**: none
- **Superseded by**: none

## Context

ADR-0002 fits the platform onto one small box, which means several components in the target
architecture cannot run at stage 0. OpenSearch alone wants more RAM than the entire stage-0
budget.

There are two ways to express a deferral, and they look identical in the first week and
completely different in the first year. Deleting the package makes re-adoption a rewrite and
quietly destroys the property that made the substitution safe in the first place. Leaving the
port in place and unbinding it makes re-adoption a configuration change.

## Decision

A deferral is `enabled: false` in a Helm values file. **Never a deleted package.**

Every port stays in the codebase with its interface intact. What changes per profile is which
adapter is bound and whether the component is scheduled at all.

| Port / concern | Stage-0 binding | Target binding | Re-adoption trigger |
|---|---|---|---|
| Orchestration | single-node k3s | multi-node Kubernetes | A second node is needed for HA |
| OLTP | plain PostgreSQL | PostgreSQL + Citus | Multi-TB data, or tenant-shard contention |
| Event backbone | NATS JetStream | Kafka | Replay volume, or multi-consumer fan-out |
| Search / log index | port unbound; Postgres FTS + Loki | OpenSearch | Search latency, or log retention obligations |
| Analytics | port unbound; Postgres | ClickHouse | Postgres aggregation becomes too slow |
| Realtime fan-out | Go WebSocket adapter | Elixir / Phoenix | Concurrent connections outgrow one process |
| Regions | single | multi-region + residency (Task 51) | A second jurisdiction, or a residency obligation |

Each row carries a re-adoption trigger because "we will add it when we need it" is not a
plan — nobody knows what "need it" means at 3am eighteen months later.

## Consequences

Easy: stage 0 fits the box. Re-adoption is a values change plus an adapter, not a project.

Hard:

- **Unbound ports are untested ports.** An OpenSearch adapter that nothing exercises will
  have rotted by the time it is wanted. Mitigation: the conformance suites from ADR-0001 run
  against every adapter in CI even when the stage-0 profile does not schedule it.
- **Citus deferral means tenant sharding is never exercised until staging.** This is the most
  consequential row in the table. Sharding behaviour under multi-tenant load is exactly the
  kind of thing that behaves differently at scale, and stage 0 gives no signal about it.
- Plain Postgres carrying both OLTP and analytics will show strain earlier than the target
  architecture would, which may pull the ClickHouse trigger forward.

## Alternatives considered

**Delete the deferred components and re-add later.** Simpler codebase now. Rejected: it
converts a configuration change into a rewrite, and it spends the port discipline that
Definition of Done gate 2 paid for.

**Run everything on a larger box.** Defeats the cost mandate in ADR-0002.

**Skip the ports entirely and code directly against the stage-0 choices.** Fastest option
available, and the one that would make every subsequent row in that table impossible.
Rejected for that reason.

## Invariant impact

None directly. Indirectly, the Citus and analytics deferrals reduce headroom before
Invariant 4's retention obligations become operationally difficult on a single box — which
is why ADR-0002 makes offsite backups mandatory.
