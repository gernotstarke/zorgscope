# zorgscope

**zorgscope** is Gernot Starke's ("zorg") always-on personal status backend. It condenses the few
things worth a glance into one client-neutral API for a polished macOS/Wails client, a browser client,
or both:

* **GitHub attention** – new and unanswered issues / PRs across the arc42 and personal site repositories, plus mentions and review requests, so no contributor waits for an answer.
* **Repository health** – open counts and GitHub‑Actions build status per monitored repo.
* **Site statistics** – Plausible visitor numbers and trends for the arc42 and personal sites.
* **Watch** – expiry countdown for registered credentials/certificates (incl. zorgscope's own GitHub token, detected automatically), auth failures of any source, and simple health checks of your own apps (e.g. status.arc42.org).

One Go binary polls continuously on a small, always-on fly.io Machine and keeps the authoritative cache,
snapshots, dismissals and runtime configuration on its persistent volume. A versioned authenticated JSON
API is the stable product boundary; the existing server-rendered page remains a usable local-development
client while production client authentication is designed.
Bootstrap authentication uses one high-entropy bearer token, with passkey-backed device authentication
planned behind the same API boundary. Locally everything runs under Docker: `make app`.

## Quick start

```sh
make app        # build + run in Docker, then open http://localhost:8080
make test       # unit + integration tests (in Docker)
make lint       # go vet + golangci-lint (in Docker)
make e2e        # Playwright end-to-end tests against fake sources (Docker Compose)
make deploy     # deploy to fly.io (flyctl in Docker)
make help       # all targets
```

Only Docker and GNU make are required locally. Copy `deploy/env.example` to `.env` and fill the required
tokens and configuration-encryption key before starting the app.

## Repository layout

| Path | Purpose |
|------|---------|
| `docs/requirements/` | Requirements (req42 style): goals, stakeholders, scope, functional & quality requirements, constraints |
| `docs/architecture/` | Architecture documentation (arc42) incl. `decisions/` (ADRs, MADR format) |
| `docs/plans/` | Implementation plans (task lists executable by agents) |
| `docs/guides/` | How‑tos: browser new‑tab setup, secrets, deployment |
| `cmd/` | Go entry points (`zorgscope`, `fakesources`) |
| `internal/` | Go source: `domain`, `ports`, `config`, `app`, `adapters/*`, `server`, `logging` |
| `web/` | Templates and static assets (CSS, vendored htmx) |
| `config/` | Non-secret local/Fly bootstrap seeds; production runtime config is persisted under `/data` |
| `deploy/` | Dockerfiles, Compose files, `fly.toml` |
| `test/` | Cross‑cutting tests: e2e (Playwright), fake upstream servers, recorded fixtures |
| `.github/workflows/` | CI: lint → test → build → e2e → deploy |

Go unit tests live next to the code they test (`*_test.go`), as is idiomatic in Go.

## Documentation entry points

* [Requirements](docs/requirements/README.md)
* [Architecture (arc42)](docs/architecture/README.md)
* [Architecture decisions](docs/architecture/decisions/README.md)
* [Guides](docs/guides/)

## Status

The walking skeleton now includes GitHub attention/builds, Plausible metrics, credential/TLS watching,
SQLite caching, revisioned live configuration, the `/api/v1/*` client API, Docker and fly.io deployment
assets. The native and browser visual clients remain an explicit follow-up decision.

## Licence

MIT — see [LICENSE](LICENSE).
