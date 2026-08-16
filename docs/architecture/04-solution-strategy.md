# 4. Solution strategy

| Decision | Summary | ADR |
|----------|---------|-----|
| Web application, not desktop | Server‑rendered web app served over HTTPS; the browser new‑tab page is simply a URL. | [ADR‑0001](decisions/ADR-0001-web-app-instead-of-wails.md) |
| Modular Go monolith, hexagonal | One binary; `domain` (pure) ← `app` (use cases, scheduler) ← `adapters` (GitHub, Plausible, Todoist, feed, SQLite) and `http` (delivery). Dependencies point inward, enforced by lint. | [ADR‑0002](decisions/ADR-0002-go-modular-monolith-hexagonal.md) |
| Server‑rendered UI with htmx | `html/template` + vendored htmx for tile polling and actions + hand‑written CSS with design tokens. No JS/CSS build step, no SPA. | [ADR‑0003](decisions/ADR-0003-server-rendered-ui-htmx.md) |
| SQLite (pure Go driver) | `modernc.org/sqlite`, WAL, single file on a fly volume; stores items cache, snapshots, dismissals, fetch status, credentials, sessions. | [ADR‑0004](decisions/ADR-0004-sqlite-persistence.md) |
| fly.io, one always‑on machine, in‑process scheduler | No separate cron worker; per‑source tickers with jitter/backoff inside the app; deploy from CI. | [ADR‑0005](decisions/ADR-0005-flyio-single-machine-inprocess-scheduler.md) |
| Passkey‑only authentication | WebAuthn via `go-webauthn/webauthn`; enrolment gated by `ENROLL_TOKEN`; sessions in SQLite. | [ADR‑0006](decisions/ADR-0006-passkey-authentication.md) |
| YAML config in repo, secrets in env | Non‑secret config versioned; secrets never in files. | [ADR‑0007](decisions/ADR-0007-config-yaml-secrets-env.md) |
| Snapshot‑based new‑detection + dismissals | Daily id snapshots define "new"; dismissals bound to `updated_at`. | [ADR‑0008](decisions/ADR-0008-snapshot-new-detection.md) |
| Docker‑only toolchain via make | All commands in containers; identical locally and in CI. | [ADR‑0009](decisions/ADR-0009-docker-only-toolchain.md) |
| Testing pyramid with fake sources | Domain unit/property tests, adapter contract tests with fixtures, handler tests, Playwright e2e against `cmd/fakesources`. | [ADR‑0010](decisions/ADR-0010-testing-strategy.md) |
| GitHub GraphQL for repo data | One query per repo page fetches issues + PRs + last comment + workflow runs; REST only for notifications. | [ADR‑0011](decisions/ADR-0011-github-graphql.md) |
| Repository layout | Root `go.mod`; `cmd/ internal/ web/ docs/ test/ deploy/ config/`. | [ADR‑0012](decisions/ADR-0012-repository-layout.md) |
| Documentation formats | req42, arc42, MADR, Markdown, stable ids. | [ADR‑0013](decisions/ADR-0013-documentation-formats.md) |

Guiding principles for implementers: **std lib first**, **one concern per package**, **tests before code**,
**failures are data, not exceptions to hide**, **everything configurable that a source might vary in**,
**nothing configurable that nobody asked for**.
