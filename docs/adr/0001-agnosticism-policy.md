# ADR-0001: Agnosticism is a property of boundaries

- **Status**: Accepted
- **Date**: 2026-08-01
- **Supersedes**: none
- **Superseded by**: none

## Context

The platform must be agnostic across every dimension — infrastructure, cloud, database,
service language, API protocol and vendor. Not infrastructure alone.

The instinct this produces is to abstract everything, and that instinct is wrong in a
specific and expensive way: an abstraction added for a swap that never happens costs
readability permanently and proves nothing. Meanwhile the abstractions that matter —
payment processors, KYC vendors, game providers — are the ones under constant real
pressure to change, jurisdiction by jurisdiction.

An earlier position held that database-engine and service-language agnosticism were not
achievable without forfeiting Safety Invariant 4, and recorded both as accepted limits.
That was too conservative and is retracted here. Both are achievable; the mechanisms are
below and the residual cost is named.

## Decision

Agnosticism is achieved by making contracts the source of truth and everything behind them
a detail. It is **not** achieved by making implementations portable.

Three mechanisms deliver it:

**1. Wire-level conformance suites.** An implementation is correct if it passes a black-box
suite driving it over its wire protocol. In-process interface tests only prove
same-language implementations correct, so they cannot deliver language agnosticism. The
executable specification is what delivers it; the interface is not.

**2. Cross-language golden test vectors.** Money arithmetic, ratio allocation, rounding and
the evidence hash pre-image get a shared JSON corpus that every language implementation
must reproduce byte for byte. This is what keeps a safety invariant true in a second
language rather than merely restated there.

**3. Storage-agnostic enforcement; the database is a dumb append target.** Every
PostgreSQL-specific mechanism has a portable substitute:

| Postgres-specific | Portable substitute |
|---|---|
| Row-level security policies | Scoped query builder that cannot omit the tenant predicate; optional per-tenant schema separation |
| `BEFORE UPDATE/DELETE` triggers | INSERT-only grants on a dedicated write credential — **mandatory in every profile** |
| `SELECT … FOR UPDATE` chain lock | Optimistic concurrency: conditional insert on expected head sequence, unique `(chain_id, sequence)`, retry on conflict |
| `timestamptz` microsecond semantics | Already normalised in application code; the engine needs only ≥microsecond storage |

RLS and triggers are retained on PostgreSQL deployments as **optional hardening**, never as
load-bearing correctness. Conformance suites therefore run against a configuration with
them disabled.

Investment is sequenced by real swap pressure — PSPs, KYC/AML vendors, game providers and
odds feeds first; cloud, broker, object storage and cache next; database engine and service
language last. Sequencing only; all are promised.

## Consequences

Easy: a service can be rewritten in any language and plugged in if it passes conformance.
A database engine can be replaced without touching domain logic. Vendor swaps are adapter
work.

Hard, and the real costs:

- **Insider tampering moves from blocked to detected.** With triggers demoted, the database
  no longer refuses a `DELETE`; the hash chain reveals it afterwards. This is narrower than
  it first appears — a superuser can `DROP TRIGGER`, so triggers never stopped a determined
  insider either — but it is a genuine reduction in defence-in-depth.
- **INSERT-only grants become load-bearing.** They are the portable replacement for the
  append-only triggers. If they are treated as optional, Invariant 4's "no deletion path"
  clause is breached. Conformance must assert them.
- **Retrofit of code already written**: `evidencetest` to wire-level, `evidence/postgres.go`
  to optimistic concurrency, both migration files' triggers demoted.
- **No single canonical implementation language** costs velocity and widens the hiring
  surface. Accepted as a business tradeoff.
- Conformance suites must exist before further services are built, or every service written
  first needs retrofitting.

## Alternatives considered

**Accept Postgres coupling for systems of record.** Cheaper and preserves three enforcement
layers. Rejected: it fails the directive, and the coupling would have spread as more
services were written.

**Generate service implementations from a DSL** to get one logic definition in many
languages. Rejected: produces a generator nobody can debug, and the generated code is what
gets audited.

**Share logic via WASM everywhere.** Works only for pure computation with no I/O. Retained
for game math and fairness verification, where it is already the choice; rejected for
stateful services such as the ledger.

## Invariant impact

- **Invariant 1 (integer money)** — strengthened. Golden vectors make it verifiable across
  languages instead of true only in Go.
- **Invariant 4 (tamper-evident evidence)** — preserved, conditionally. The hash chain is
  unchanged and remains the layer that matters. "No deletion path" now rests on INSERT-only
  grants rather than triggers, so those grants are mandatory and conformance-asserted. If
  that condition lapses, this invariant is breached.
- **Invariant 12 (single time authority)** — preserved. Normalisation already happens in
  application code, which is why timestamp precision was portable to begin with.
- All others — untouched.
