# Safety Invariants — NEVER Configurable

These are deliberately NOT runtime-configurable. Making any of these configurable is a defect, not a feature. This is the approved exception to the zero-hardcoding principle.

## The 12 Invariants

1. **Ledger double-entry balance** — Append-only journal; integer-only money arithmetic. No floating-point for money, ever.
2. **RNG generation path** — Seed-commit/reveal protocol. The RNG pipeline and fairness proof mechanism are structurally enforced.
3. **AuthN/AuthZ enforcement** — Deny-by-default. Every endpoint requires explicit permission grant.
4. **Audit and evidence store** — Tamper-evident hash chaining, no deletion path within retention period.
5. **Self-exclusion and RG suppression** — Enforcement across wagering, ads, marketing, and host actions. A self-excluded player cannot be targeted by any system.
6. **Ad and comms decisioning isolation** — Cannot access KYC documents or payment instruments. Marketing never sees sensitive identity or financial data.
7. **Metered usage immutability** — Once rated, usage records are immutable. Entitlements are not tenant-bypassable. Billing cannot mutate the gaming ledger.
8. **Commission immutability** — Commission lines immutable once paid. Commission cannot mint value outside the ledger.
9. **Tax calculation immutability** — Tax calculations immutable once filed. Tax engine posts only to defined tax accounts.
10. **Statement immutability** — Statements immutable once issued. Reconciliation results are append-only.
11. **Auditor role isolation** — Auditor roles are structurally read-only. Control test results are superseded, never edited.
12. **Single time authority** — UTC storage, explicit display timezone, no local-time arithmetic anywhere in the system.

## Implementation Rule

When implementing any feature, check whether it touches one of these invariants. If it does, the invariant is the constraint — the feature adapts to the invariant, never the reverse.
