# Implementation State — Current Progress

Live progress tracker. Any session should be able to read this and know exactly where things
stand without asking.

**Architectural decisions live in `docs/adr/`, not here.** This file previously grew to 376
lines holding contested positions, one of which flipped twice in a single session. Decisions
now have ADRs with Status fields. Keep this file to state.

## Current Phase: Phase 1 — Foundation (Task 1 in progress)

## Completed Tasks

- **Setup**: Project steering files and automation hooks established (2026-07-31)

## In Progress

**Task 1 — Monorepo scaffolding and fully local infrastructure** (started 2026-08-01)

Built and green (`go build ./...`, `go test ./...` both pass):

- Go module `github.com/satta/platform`, go 1.26; `.gitignore`, `.editorconfig`
- Shared libraries: `clock`, `config`, `correlation`, `db`, `evidence`, `identity`,
  `metering`, `money`, `tenancy`
- Evidence store: port, in-memory double, Postgres implementation, hash-chain verifier,
  contract suite, schema with append-only triggers and RLS
- Metering SDK: usage event contract, in-memory emitter, usage schema with immutability
  triggers and RLS
- Protobuf contracts for common/audit/metering; buf lint and codegen config
- Tooling: `Taskfile.yml`, `Makefile`, `.golangci.yml`, `.pre-commit-config.yaml`
- ADRs 0001-0006 in `docs/adr/`

Outstanding:

| Item | Note |
|---|---|
| Local substrate | Compose file per ADR-0003, tiered per ADR-0002 |
| `cmd/platformctl` | migrate / seed / verify-evidence — every Taskfile target calls it |
| `scripts/check-invariants.sh`, `check-state-freshness.sh` | Referenced by Taskfile and pre-commit; do not exist |
| Generated proto code | `libs/go/gen` — plugins installed, `buf generate` not yet run |
| Go service template | observability, httpx middleware, health/ready, graceful shutdown |
| Postgres metering emitter | durable `Emitter` + `EmitTx` |
| Integration tests | Postgres-backed, gated on `PLATFORM_TEST_DATABASE_URL` |
| Golden test vectors | money, ratio, rounding, hash pre-image — per ADR-0001 |
| `apps/` + `packages/` scaffold | Per ADR-0005; monorepo layout is Task 1 scope |
| Config seed file | Local policy layers for the `FileResolver` |

Retrofit required by ADR-0001 (touches code already written):

| File | Change |
|---|---|
| `evidence/evidencetest/contract.go` | Go-interface suite → wire-level gRPC conformance |
| `evidence/postgres.go` | `SELECT … FOR UPDATE` → optimistic concurrency on head sequence |
| `evidence/migrations/0001_evidence.sql` | Triggers/RLS demoted to optional hardening |
| `metering/migrations/0001_usage.sql` | Same demotion for immutability triggers |
| both | INSERT-only grants become mandatory and conformance-asserted |

## Blockers

- **Conformance suites gate further service work.** Any service written before the
  wire-level suites exist needs retrofitting, since in-process Go tests cannot validate
  non-Go implementations. Build them before Task 2. Sequencing, not a defect — but it
  compounds if ignored.
- **ADR-0006 (launch stage) is Proposed, not Accepted.** Phase 1 is identical under all four
  options so foundation work proceeds. **Do not start Tasks 10, 11, 12, 14 or 42 until it is
  decided** — under stages 1-2 they are not launch-critical and the roadmap L/R tagging
  needs rewriting.
- **Not a git repository.** `git rev-parse` fails, which blocks `pre-commit install` and
  `task breaking` (`buf breaking --against '../.git#subdir=proto'`).
- **`pre-commit` not installed** (`pipx install pre-commit`). Config written, no hooks active.
- **Steering conflicts with the cost mandate, unreconciled.** `project-context.md` requires
  enterprise scale and zero downtime; `delivery-realities.md` assumes 25-40 engineers.
  ADR-0002 resolves this as a stage-0 topology with the target architecture intact, but the
  two steering files still read as current-state. Reconcile by pointing them at ADR-0002.
- **Task 37 / 46 scope overlap.** Both claim the partner self-service portal; 37 is L, 46 is
  R. ADR-0005 treats it as R pending confirmation.

## Key Files & Paths

| Path | Purpose |
|------|---------|
| .kiro/steering/*.md | Project context, safety invariants, DoD, roadmap, delivery realities, decisions log, this file |
| .kiro/hooks/*.json | Steering validation, state load on session start, state update on stop |
| docs/adr/ | Architecture decision records — 0001 agnosticism, 0002 stage-0 topology, 0003 container runtime, 0004 deferred components, 0005 frontend architecture, 0006 launch stage (Proposed) |
| go.mod, go.sum | Go module, go 1.26 |
| libs/go/clock/ | Single time authority (INVARIANT 12); injectable clock |
| libs/go/config/ | Layered policy resolution; no resolve-with-default by design |
| libs/go/correlation/ | Long-lived correlation ID, distinct from sampled trace IDs |
| libs/go/db/ | pgx pool with per-transaction tenant session vars; forward-only migrations |
| libs/go/evidence/ | Tamper-evident hash-chained evidence store (INVARIANT 4) |
| libs/go/identity/ | Actor model; auditor type structurally read-only (INVARIANT 11) |
| libs/go/metering/ | Usage event SDK (DoD gate 10, INVARIANT 7) |
| libs/go/money/ | Integer-only money, exact ratios, allocation (INVARIANT 1) |
| libs/go/tenancy/ | Tenancy scope and isolation primitives |
| proto/ | buf v2 workspace; common, audit, metering v1 |
| Taskfile.yml, Makefile | Canonical task runner plus `make up` shim for DoD gate 7 |
| .golangci.yml | forbidigo/depguard encode the time, RNG, float-money and port rules |

## Toolchain On This Machine

go 1.26.5 darwin/arm64 · task 3.52.0 · golangci-lint 2.12.2 · buf 1.72.0 ·
protoc-gen-go 1.36.6 · protoc-gen-go-grpc 1.5.1 · Docker 27.5.1 / Compose 2.32.4 ·
**pre-commit missing**

## Next Up

1. `git init` and initial commit — unblocks pre-commit and buf breaking
2. `scripts/check-invariants.sh` and `check-state-freshness.sh`
3. `buf generate` into `libs/go/gen`
4. Local substrate compose file, tiered per ADR-0002
5. `cmd/platformctl` — migrate, seed, verify-evidence
6. Go service template: observability, httpx middleware, bootstrap, `services/template`
7. Postgres metering emitter and `EmitTx`
8. Golden test vectors
9. Wire-level conformance suite, then the ADR-0001 retrofits
10. `apps/` + `packages/` scaffold with generated `api-client`
11. Reconcile steering with ADR-0002; confirm Task 37/46 boundary
12. Walk all 10 Definition of Done gates before marking Task 1 complete
13. Then Task 2 — workflow orchestration, Temporal behind a port

## Notes

- Update at the END of each session
- Never remove entries from Completed Tasks — only add
- Architectural decisions go in `docs/adr/` with a Status field, not in this file
- Task 1 is NOT complete. Walk the 10 DoD gates before marking it so
