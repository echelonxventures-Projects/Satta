# ADR-0003: Podman locally, single-node k3s in production

- **Status**: Accepted
- **Date**: 2026-08-01
- **Supersedes**: none
- **Superseded by**: none

## Context

Docker was rejected as the local substrate runtime. Two readings of that rejection were
possible, and they lead to different answers:

- If the objection is Docker Desktop specifically — its licence terms for larger companies,
  its daemon, its VM overhead on macOS — then a drop-in replacement suffices.
- If the objection is containers as a concept, then the substrate must be host processes,
  which means pinned release binaries under a process supervisor.

The cost mandate in ADR-0002 resolved it. Production stage 0 is a single box, and the
cheapest possible production runtime would be plain systemd units on a VPS. That option is
rejected on portability grounds: it would couple deployment to one host's init system and
directory layout, which ADR-0001 forbids. Production therefore runs Kubernetes, which means
containers exist in the production path regardless. Going container-free locally would give
up dev/prod parity for no saving at all.

## Decision

**Podman locally. Single-node k3s with Helm in production.**

Podman is free, rootless, needs no daemon, needs no Docker Desktop licence, and reads the
same Compose file. `docker compose` and `podman compose` are interchangeable for this
purpose, so the substrate definition is written once and works under either.

Kubernetes is the portability layer, and that is the reason for choosing it rather than a
lighter option. The same Helm charts deploy unchanged to k3s on one box, to EKS, GKE or AKS,
or to on-premise hardware. Deployment differences live in values files — `local`,
`single-box`, `multi-region` — and never in application code or image builds.

## Consequences

Easy: one substrate definition serves both runtimes. Developers on Docker Desktop are not
forced to migrate. Production and local run the same images.

Hard:

- k3s costs roughly 512MB of the box's RAM for its control plane. On a 4GB box that is
  material; on Oracle's 24GB it is noise.
- `podman compose` is a shim over an external compose implementation and occasionally lags
  Docker Compose on newer schema features. The substrate file must stay on well-established
  schema, which is a mild constraint on how clever it can be.
- Rootless Podman handles low-numbered ports and some volume ownership cases differently.
  The substrate binds only high ports for this reason.

## Alternatives considered

**Pinned release binaries plus process-compose.** Genuinely attractive: ten of thirteen
components are single static binaries, checksum pinning would give supply-chain provenance
useful for Task 49, and teardown is `rm -rf`. Rejected because production runs Kubernetes
anyway, so this buys no parity — and Postgres, Redis and OpenSearch are all awkward to
package natively. Reconsider if container-free local becomes a firm requirement.

**Colima or Lima.** Works, but it is a Linux VM hosting a container runtime — the same
overhead being objected to, one layer down.

**systemd units in production.** Cheapest of all. Rejected: couples deployment to one host
and forfeits the infrastructure agnosticism in ADR-0001.

## Invariant impact

None. Runtime choice does not touch any of the 12 invariants. It is recorded because it was
contested and because the reasoning — that Kubernetes is chosen *for portability*, not for
scale — is the part most likely to be misread later as over-engineering.
