# apps/admin-console — operator, trading and audit surfaces (roadmap Task 37, L)

Intentionally empty. One console is built here when Task 6 (IAM) and the
money/compliance services exist. Separate bundles per auth boundary are not
cosmetic: Invariant 6 (ad/comms cannot reach KYC/payment data) and Invariant
11 (auditors read-only) are structural only if these views ship as separate
apps behind separate auth contexts — one bundle with role-flag routing turns
both into a runtime permission check a bug can flip (ADR-0005).
