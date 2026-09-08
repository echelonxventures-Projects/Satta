# Implementation State — Current Progress

Live progress tracker. Any session should be able to read this and know exactly where things
stand without asking.

**Architectural decisions live in `docs/adr/`, not here.** This file previously grew to 376
lines holding contested positions, one of which flipped twice in a single session. Decisions
now have ADRs with Status fields. Keep this file to state.

## Current Phase: Phase 1 — Foundation (Task 1 in progress)

## Completed Tasks

- **Setup**: Project steering files and automation hooks established (2026-07-31)
- **Phase 0 executed (2026-09-07)** per `docs/PLAN.md` v1.3: plan promoted to
  `docs/PLAN.md` (S0); initial commit on `main` + private GitHub remote
  `echelonxventures-Projects/Satta` + push (S1–S2); pre-commit installed and
  `run --all-files` clean, `buf breaking` baselined (S3); CI workflow
  `.github/workflows/ci.yml` mirroring `task ci` + govulncheck SCA (S4); ADR-0006
  accepted Stage 4, ADR-0007 written, ADR-0002 pointer, ADR-0008 drafted (S5)
- **S8 wire-level conformance (2026-09-07, PLAN v1.5)**: evidence contract suite
  runs over real gRPC (bufconn) — MemoryStore unit + PostgresStore integration
  (first `//go:build integration` file; migrator-role migrations, satta_app
  RLS-bound traffic). Proto closed additively (Actor.on_behalf_of,
  payload_encoded canonical bytes, Range + GetByIdempotencyKey RPCs — zero
  breaking changes). evidencegrpc adapters, status↔sentinel mapping, metadata
  convention. Canonical payload codec (DecodePayload/EncodePayload) with
  round-trip + malformed-rejection tests. Suite caught 2 real wire bugs on
  first run (chain-ID masking in client; caller-level auditor enforcement).

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

- **GitHub Actions silently suppressed on this account's repos** (GitHub-side flag from
  the earlier "repository is disabled" 403; pushes work, Actions do not fire — 0 runs,
  0 workflows visible via API despite admin + workflow scope and Actions enabled).
  Contact GitHub Support; until resolved, the CI workflow file is in place but inert.
- **Branch protection requires GitHub Pro** (private repo). Ruleset JSON staged at
  `/var/folders/sg/wcc5mm0d0hncgvmbdf5sp0l00000gp/T/opencode/ruleset.json`; apply via
  `gh api -X POST repos/echelonxventures-Projects/Satta/rulesets --input <file>` once
  upgraded, or make the repo public. Follow-up, not a blocker to code.
- **Conformance suites gate further service work.** Any service written before the
  wire-level suites exist needs retrofitting, since in-process Go tests cannot validate
  non-Go implementations. Build them before Task 2. Sequencing, not a defect — but it
  compounds if ignored. **Next up (S8).**
- **ADR-0008 is Proposed.** Decide with first-market counsel (plan O1) before M4.
- **Task 37 / 46 scope overlap.** Both claim the partner self-service portal; 37 is L, 46 is
  R. ADR-0005 treats it as R pending confirmation (plan S44).
- **`task verify-evidence` needs chains to verify** — currently "no evidence chains found"
  on a fresh volume; expected until first append. Not a defect.

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
pre-commit 4.x (installed 2026-09-07, hooks active)

## Next Up

1. ~~`git init` and initial commit~~ — done 2026-09-07 (Phase 0, S1–S3)
2. ~~S8 wire-level conformance~~ — done 2026-09-07 (v1.5): 11-scenario suite runs
   over real gRPC against MemoryStore (unit) and PostgresStore (integration);
   proto closed additively (on_behalf_of, payload_encoded, Range,
   GetByIdempotencyKey); evidencegrpc adapters + metadata convention; suite
   caught and fixed 2 real wire bugs on first run
3. S9 — ADR-0001 retrofits (optimistic concurrency; trigger/RLS demotion; INSERT-only grant conformance)
4. S10 — Golden test vectors (money / ratio / rounding / hash pre-image) — payload codec round-trip tests landed as part of S8; the cross-language corpus remains
5. S11 — Tests for the untested packages + Postgres integration suite (evidence part done via S8 wire integration; db/config/tenancy/identity/metering/clock/correlation/platformctl remain)
6. S4a — Deployment pipeline (Dockerfile + Helm + Argo CD) with the S12 service template
7. S12 — Go service template: observability, httpx middleware, bootstrap, `services/template`
8. Postgres metering emitter durability follow-up; `apps/`+`packages/` scaffold (S13)
9. Reconcile steering docs with ADR-0007; confirm Task 37/46 boundary (S44)
10. Then Task 2 — workflow orchestration, Temporal behind a port (S15)

**Master sequence:** `docs/PLAN.md` (S0–S52, now in execution; Phase 0 complete, S8 complete).

## Notes

- Update at the END of each session
- Never remove entries from Completed Tasks — only add
- Architectural decisions go in `docs/adr/` with a Status field, not in this file
- Task 1 is NOT complete. Walk the 10 DoD gates before marking it so
