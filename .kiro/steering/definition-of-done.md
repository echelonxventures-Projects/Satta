# Cross-Cutting Definition of Done

Applies to EVERY task. Do not repeat per task — enforce universally.

## The 10 Gates

1. **No literal business-policy values in code** — All resolved from Config & Rules engine. If you find yourself writing a magic number, percentage, threshold, or policy string, it belongs in config.
2. **Every external dependency behind a port** — With a local implementation plus contract tests. No direct vendor SDK calls in domain logic.
3. **Multi-tenant isolation test** — Proving tenant A cannot read or affect tenant B. Every service must pass this.
4. **Structured logs, Prometheus metrics, OpenTelemetry traces** — Emitted from every service. No service ships without observability.
5. **Audit events for every state change** — Touching money, identity, or compliance. Events flow to the tamper-evident evidence store.
6. **OpenAPI/protobuf contract published** — Breaking changes versioned. Consumers must not break on deploy.
7. **Unit + integration tests green** — Runs under `make up` with no cloud credentials. If it needs the internet to test, it's wrong.
8. **i18n-ready strings; WCAG 2.2 AA on any UI** — Accessibility and internationalization are not optional.
9. **Capability registered as a product** — With an entitlement check enforced before use. Unentitled tenants get a clear denial, not a crash.
10. **Every billable action emits a metered usage event** — With an idempotency key. If it's billable, it's metered. No exceptions.

## How to Use This

Before marking any task complete, walk through all 10 gates. If any gate is not met, the task is not done regardless of whether the feature "works."
