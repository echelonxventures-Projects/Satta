# Satta platform — root npm workspace (ADR-0005).

Apps and shared packages resolve through workspaces; the generated
`@satta/api-client` is built by `task generate:ts` from the same protos the
Go services compile — do not hand-edit its `src/gen/`.

Stage-0 builds two apps (ADR-0005): `apps/player-web` (Next.js PWA, Task 32)
and `apps/admin-console` (React, Task 37). Both are intentionally near-empty
scaffolds today: the platform has no product surface yet to fill them with,
and scaffolding invites filling in ahead of the backend that makes it real.

Local commands:

```
npm install            # once (and after dependency changes)
task generate:ts       # regenerate api-client from proto/
task npm:test          # packages tests incl. the golden-corpus replay
task npm:build         # typecheck + build everything installable
```
