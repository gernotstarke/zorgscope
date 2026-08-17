# 4. Solution strategy

| Decision | Summary | ADR |
|----------|---------|-----|
| Always-on backend, client-neutral API | Fly backend is the source of truth and serves versioned JSON to a future Wails and/or browser client; supersedes web-only ADR-0001. | [ADR‑0014](decisions/ADR-0014-flyio-backend-client-neutral-api-runtime-config.md) |
| Modular Go monolith, hexagonal | One backend binary; `domain` (pure) ← `app` (use cases, scheduler, config activation) ← adapters (GitHub, Plausible, watch, SQLite) and HTTP JSON delivery. Dependencies point inward. | [ADR‑0002](decisions/ADR-0002-go-modular-monolith-hexagonal.md) |
| Presentation remains replaceable | ADR-0003 describes the current browser implementation, but the target contract is JSON and does not choose Wails versus browser. Client implementation gets its own decision. | [ADR‑0003](decisions/ADR-0003-server-rendered-ui-htmx.md), [ADR‑0014](decisions/ADR-0014-flyio-backend-client-neutral-api-runtime-config.md) |
| SQLite (pure Go driver) | `modernc.org/sqlite`, WAL, single file on a Fly volume; stores item cache, snapshots, dismissals and fetch status. Runtime config/secrets use separate files. | [ADR‑0004](decisions/ADR-0004-sqlite-persistence.md), [ADR‑0014](decisions/ADR-0014-flyio-backend-client-neutral-api-runtime-config.md) |
| fly.io, one always‑on machine, in‑process scheduler | No separate cron worker; per‑source tickers with jitter/backoff inside the app; deploy from CI. | [ADR‑0005](decisions/ADR-0005-flyio-single-machine-inprocess-scheduler.md) |
| Bootstrap auth, passkey device auth target | Initial `/api/v1/*` uses a Fly-provided bearer token; ADR-0006 remains the target but must be adapted for native-device authorisation. | [ADR‑0006](decisions/ADR-0006-passkey-authentication.md), [ADR‑0014](decisions/ADR-0014-flyio-backend-client-neutral-api-runtime-config.md) |
| Mutable runtime config, immutable deployment trust root | Complete runtime YAML and encrypted write-only upstream secrets live on the Fly volume. Deployment/master/auth values stay in Fly secrets; ADR-0014 supersedes ADR-0007. | [ADR‑0014](decisions/ADR-0014-flyio-backend-client-neutral-api-runtime-config.md) |
| Snapshot‑based new‑detection + dismissals | Daily id snapshots define "new"; dismissals bound to `updated_at`. | [ADR‑0008](decisions/ADR-0008-snapshot-new-detection.md) |
| Docker‑only toolchain via make | All commands in containers; identical locally and in CI. | [ADR‑0009](decisions/ADR-0009-docker-only-toolchain.md) |
| Testing pyramid with fake sources | Domain unit/property tests, adapter contract tests with fixtures, handler tests, Playwright e2e against `cmd/fakesources`. | [ADR‑0010](decisions/ADR-0010-testing-strategy.md) |
| GitHub GraphQL for repo data | One query per repo page fetches issues + PRs + last comment + workflow runs; REST only for notifications. | [ADR‑0011](decisions/ADR-0011-github-graphql.md) |
| Repository layout | Root `go.mod`; `cmd/ internal/ web/ docs/ test/ deploy/ config/`. | [ADR‑0012](decisions/ADR-0012-repository-layout.md) |
| Documentation formats | req42, arc42, MADR, Markdown, stable ids. | [ADR‑0013](decisions/ADR-0013-documentation-formats.md) |

Guiding principles for implementers: **std lib first**, **one concern per package**, **tests before code**,
**failures are data, not exceptions to hide**, **every runtime property is manageable through one contract**,
**deployment trust roots are never remotely mutable**.
