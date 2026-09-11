# Deployment (S4a)

One path, declared in git:

```
code -> deploy/images/Dockerfile.service (one build for every service)
     -> deploy/helm/satta-service        (one chart for every service)
     -> deploy/argocd/environments/<env> (values differ, structure does not)
```

`root-app.yaml` is the only thing applied by hand to a cluster, once.

## Stage 0 — the dev box (ADR-0002)

1. `task up` (substrate), `task image:build`, `task up:k3s` if present.
2. Import the image into k3s's containerd (`ctr -n k8s.io images import`), or
   use `registry:` overrides once a registry exists (S51).
3. `kubectl apply -f deploy/argocd/project.yaml -f deploy/argocd/root-app.yaml`
   — after Argo CD itself is installed (upstream manifest, pinned version).

Stage-0 auto-syncs but does **not** self-heal: engineers investigating a live
hand-patch must find it there. Environments that matter flip `selfHeal: true`.

## Ladder of environments

| Env | What it proves | What it adds |
|-----|----------------|--------------|
| stage-0 | build + deploy path on one box | loopback substrate, dev creds |
| staging (S28a) | the same chart under real TLS/secret wiring | Vault-synced secrets, backups on |
| prod (S51) | money | HA Postgres, PDB+HPA on, NetworkPolicy on, ServiceMonitor on, self-heal on, images by digest |

A promotion is a values-file diff, never a template change (ADR-0004).

## What this pipeline does NOT yet prove (tracked)

- Image **publishing to GHCR** and digest-pinning — blocked with the Actions
  suppression on this GitHub account (Phase 0 follow-up). The build is local
  until the registry push exists; Argo is configured for the repo path already.
- k3s provisioning automation (`task up:k3s`) — S51 scope.
- Certified-build attestation (S48) and multi-region (S51).

## Verify locally, identically

```
task ci:infra    # image build + chart lint/render of every ADR-0004 binding
```
