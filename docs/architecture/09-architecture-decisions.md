# 9. Architecture decisions

All decisions are recorded as MADR files in [decisions/](decisions/README.md). Index:

| ADR | Title | Status |
|-----|-------|--------|
| [ADR‑0001](decisions/ADR-0001-web-app-instead-of-wails.md) | Web application instead of a Wails desktop app | superseded by ADR-0014 |
| [ADR‑0002](decisions/ADR-0002-go-modular-monolith-hexagonal.md) | Go modular monolith with hexagonal package structure | accepted |
| [ADR‑0003](decisions/ADR-0003-server-rendered-ui-htmx.md) | Server‑rendered UI with html/template, htmx and hand‑written CSS | superseded by ADR-0014; retained as local interim UI |
| [ADR‑0004](decisions/ADR-0004-sqlite-persistence.md) | SQLite via pure‑Go driver for cached product state | accepted |
| [ADR‑0005](decisions/ADR-0005-flyio-single-machine-inprocess-scheduler.md) | fly.io single always‑on machine with in‑process scheduler | accepted |
| [ADR‑0006](decisions/ADR-0006-passkey-authentication.md) | Passkey-backed device authentication target | accepted target; not implemented |
| [ADR‑0007](decisions/ADR-0007-config-yaml-secrets-env.md) | YAML configuration in the repository, secrets via environment | superseded by ADR-0014 |
| [ADR‑0008](decisions/ADR-0008-snapshot-new-detection.md) | Daily snapshots define "new"; dismissals bound to updated_at | accepted |
| [ADR‑0009](decisions/ADR-0009-docker-only-toolchain.md) | Docker‑only local toolchain driven by make | accepted |
| [ADR‑0010](decisions/ADR-0010-testing-strategy.md) | Testing strategy: layered tests, fixtures, fake sources, Playwright e2e, CI | accepted |
| [ADR‑0011](decisions/ADR-0011-github-graphql.md) | GitHub GraphQL API for repository data, REST for notifications | accepted |
| [ADR‑0012](decisions/ADR-0012-repository-layout.md) | Repository layout with strict separation of concerns | accepted |
| [ADR‑0013](decisions/ADR-0013-documentation-formats.md) | Documentation formats: req42, arc42, MADR in Markdown | accepted |
| [ADR‑0014](decisions/ADR-0014-flyio-backend-client-neutral-api-runtime-config.md) | Always-on fly.io backend with client-neutral API and mutable runtime configuration | accepted |
