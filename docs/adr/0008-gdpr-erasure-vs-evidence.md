# ADR-0008: GDPR right-to-erasure vs the append-only evidence store

- **Status**: Proposed (drafted at Master Build Plan S5, v1.2 G15; to be decided with
  first-market counsel under O1 before M4)
- **Date**: 2026-09-07
- **Supersedes**: none
- **Superseded by**: none

## Context

Two obligations collide:

1. **GDPR Art. 17** — right to erasure of personal data, strongest for data withdrawn
   consent and data no longer necessary.
2. **Invariant 4 / regulatory audit** — the evidence store is hash-chained and structurally
   append-only (no Update/Delete on the port; forward-only triggers; INSERT-only grants).
   AML law (e.g. MGA, FATF-aligned rules) requires transaction records to persist for
   5–10 years and be tamper-evident. Deleting a chain record breaks the chain for every
   later record and voids the tamper-evidence property the whole store exists to provide.

Erasure-by-deletion is legally impossible in one direction (AML retention) and
architecturally forbidden in the other (hash chain integrity). This tension is not
accidental; it is the normal collision between privacy law and AML law, and the industry
answer is established practice.

## Decision (proposed)

**Pseudonymization-at-closure.** On account closure (or an erasure request):

- Evidence chain records **persist unmodified** — sequence numbers, hashes, chain links,
  categories, and money amounts stay intact.
- Directly identifying PII fields (name, address, document numbers, contact identifiers)
  inside evidence payloads are **replaced by an irreversible pseudonym** (e.g.
  `player:closed:<uuid>`), with the mapping held only for the legally mandated AML
  retention window, then destroyed.
- The account's operational profile is deleted downstream (auth, CRM, marketing), where
  no chain integrity exists.
- A closure **evidence event is appended** recording that pseudonymization occurred,
  preserving the audit trail of the erasure itself.

This satisfies Art. 17 in substance (data no longer identifies a person) while honoring
AML retention and Invariant 4. The pseudonymized record cannot be re-identified by the
platform after the mapping is destroyed.

## Consequences

- Task 44 (S45) player-rights flows implement this stance, not deletion.
- Ledger entries: financial records follow the same AML retention; player-facing
  statements are regenerated from pseudonymized data or archived under the retention
  policy.
- Counsel per market (O1) must confirm window lengths and the exact field set
  pseudonymized — jurisdictions differ on what counts as "no longer needed."

## Alternatives considered

**Delete chain records on erasure.** Rejected: breaks the hash chain (Invariant 4),
voids tamper-evidence for all subsequent records, and breaches AML retention law.

**Never erase anything.** Rejected: fails GDPR where processing is consent-based
(marketing) and invites regulatory action; also unnecessary — the collision only exists
on the evidence store, not the whole platform.

**Separate PII-free evidence from the start.** Partially adopted going forward: new
evidence categories prefer references over inline PII (by id, not value). It does not
solve existing records or vendors that echo PII, so the closure-time stance remains
necessary.

## Invariant impact

Invariant 4 (tamper-evidence) preserved — the chain never rewrites. Invariant 11
(auditor read-only) unchanged. This ADR narrows what "erasure" means operationally; it
does not weaken any invariant.
